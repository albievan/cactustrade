package trading212

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// maxResponse bounds a response body; the instrument catalogue (some 15 000 instruments) may
// be larger.
const (
	maxResponse            = 8 << 20
	maxInstrumentsResponse = 32 << 20
)

// maxMutationWait is the longest a change waits for its rate-limit window; longer → not sent.
const maxMutationWait = 10 * time.Second

// maxRateWait is the longest a request waits for a rate-limit window before it gives up
// (the overview is interactive; the next 15-minute snapshot tries again).
const maxRateWait = 15 * time.Second

// Config builds a Client. Only tests set the base URLs, HTTP, Now or Sleep.
type Config struct {
	DemoBaseURL string
	LiveBaseURL string
	HTTP        *http.Client
	Secrets     ports.SecretStore
	Log         *slog.Logger
	// RequestTimeout per attempt (default 15 s).
	RequestTimeout time.Duration
	Now            func() time.Time
	Sleep          func(context.Context, time.Duration) error
}

// Client talks to Trading 212 through the allowlist only. It implements ports.Holdings (any
// Trading 212 account, read-only) and ports.T212Broker (practice accounts only).
type Client struct {
	cfg        Config
	demo, live *url.URL
	mu         sync.Mutex
	lims       map[string]*limiter // per account: the limits are per key
}

var _ ports.Holdings = (*Client)(nil)

// New validates cfg and builds the client.
func New(cfg Config) (*Client, error) {
	if cfg.Secrets == nil {
		return nil, errors.New("trading212.New: Secrets is required")
	}
	if cfg.DemoBaseURL == "" {
		cfg.DemoBaseURL = DemoBaseURL
	}
	if cfg.LiveBaseURL == "" {
		cfg.LiveBaseURL = LiveBaseURL
	}
	demo, err := url.Parse(cfg.DemoBaseURL)
	if err != nil || demo.Host == "" {
		return nil, fmt.Errorf("trading212.New: bad demo base URL %q", cfg.DemoBaseURL)
	}
	live, err := url.Parse(cfg.LiveBaseURL)
	if err != nil || live.Host == "" {
		return nil, fmt.Errorf("trading212.New: bad live base URL %q", cfg.LiveBaseURL)
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	// Never follow a redirect (PLAN 7.1.7): it would carry the credentials to another host.
	// A 3xx is answered like any other unexpected status. The caller's client is not changed.
	hc := *cfg.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	cfg.HTTP = &hc
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 15 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = sleepCtx
	}
	return &Client{cfg: cfg, demo: demo, live: live, lims: map[string]*limiter{}}, nil
}

func (c *Client) limiter(accountID string) *limiter {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.lims[accountID]
	if !ok {
		l = newLimiter(c.cfg.Now)
		c.lims[accountID] = l
	}
	return l
}

// account is the Trading 212 account a call is for (ports.WithAccount). Without one, or for
// an account on another platform, nothing is sent (fail closed).
func account(ctx context.Context) (domain.Account, error) {
	a, ok := ports.AccountFrom(ctx)
	if !ok || a.ID == "" {
		return domain.Account{}, fmt.Errorf("no Trading 212 account was given for this call: %w", domain.ErrUnavailable)
	}
	if a.OnPlatform() != domain.PlatformTrading212 {
		return domain.Account{}, fmt.Errorf("%s is not a Trading 212 account: %w", a.Name, domain.ErrUnavailable)
	}
	return a, nil
}

// APIError is a non-2xx answer from Trading 212. Its error bodies are not documented, so it
// carries only the status and our own explanation; it never contains a key.
type APIError struct {
	Op     Op
	Status int
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("Trading 212 %s: HTTP %d", e.Op, e.Status)
	switch e.Status {
	case http.StatusUnauthorized:
		s += ": the API key or secret was refused"
	case http.StatusForbidden:
		s += ": the API key lacks a permission (account and portfolio; to trade a practice account also metadata, orders:read, orders:execute and history:orders)"
	case http.StatusRequestTimeout:
		s += ": Trading 212 timed out"
	case http.StatusTooManyRequests:
		s += ": rate limit reached, try again shortly"
	}
	return s
}

// Unwrap maps failures onto domain errors: 404 is "not found", anything else means
// Trading 212 is unavailable for this request.
func (e *APIError) Unwrap() error {
	if e.Status == http.StatusNotFound {
		return domain.ErrNotFound
	}
	return domain.ErrUnavailable
}

// keys returns the account's API key and secret.
func (c *Client) keys(ctx context.Context, a domain.Account) (key, secret string, err error) {
	kn, sn := domain.AccountKeyNames(a)
	key, err1 := c.cfg.Secrets.Get(ctx, kn)
	secret, err2 := c.cfg.Secrets.Get(ctx, sn)
	if errors.Is(err1, domain.ErrNotFound) || errors.Is(err2, domain.ErrNotFound) || (err1 == nil && key == "") || (err2 == nil && secret == "") {
		return "", "", fmt.Errorf("the Trading 212 API key and secret of %s are not both set: %w: %w", a.Name, domain.ErrCredentialsMissing, domain.ErrUnavailable)
	}
	if err := errors.Join(err1, err2); err != nil {
		return "", "", fmt.Errorf("reading the Trading 212 keys: %w", err)
	}
	return key, secret, nil
}

// request is one allowlisted request: its operation, the order id of a "{id}" path, and
// its query (or the raw query of a nextPagePath) or JSON body.
type request struct {
	op       Op
	id       int64
	query    url.Values
	rawQuery string
	body     any
}

// target builds the request's URL on the account's host.
func (c *Client) target(acct domain.Account, ep Endpoint, r request) string {
	u := *c.demo
	if acct.Mode == domain.ModeReal {
		u = *c.live
	}
	u.Path, u.RawPath, u.RawQuery = ep.Path, "", r.rawQuery
	if strings.Contains(ep.Path, "{id}") {
		u.Path = orderPath(ep.Path, r.id)
	}
	if r.query != nil {
		u.RawQuery = r.query.Encode()
	}
	return u.String()
}

// get performs one allowlisted read with a query and decodes the JSON answer into out.
func (c *Client) get(ctx context.Context, op Op, query url.Values, out any) error {
	return c.read(ctx, request{op: op, query: query}, out)
}

// read performs one allowlisted GET (for any Trading 212 account in ctx) and decodes the JSON
// answer into out. A 429 or a 5xx is retried once when the wait is short; every other
// failure is returned at once.
func (c *Client) read(ctx context.Context, r request, out any) error {
	ep, ok := Allowlist[r.op]
	if !ok || ep.Mutates {
		return fmt.Errorf("trading212: operation %q is not allowlisted as a read", r.op)
	}
	acct, err := account(ctx)
	if err != nil {
		return err
	}
	key, secret, err := c.keys(ctx, acct)
	if err != nil {
		return err
	}
	target, lim := c.target(acct, ep, r), c.limiter(acct.ID)
	for attempt := 1; ; attempt++ {
		retry, err := c.attempt(ctx, lim, r.op, ep, target, key, secret, nil, out)
		if err == nil || !retry || attempt >= 2 {
			return err
		}
	}
}

// mutate sends one allowlisted change for the Trading 212 PRACTICE account in ctx, once: a
// real account is refused before its keys are read (D41 g); a rate-limit wait over
// maxMutationWait is not sent (NotAccepted); it is never retried. Outcomes: 2xx → out;
// 400/401/403/other 4xx → NotAccepted (Trading 212 answered and refused it); 404 →
// NotAccepted wrapping ErrNotFound; a transport error, a timeout, 408, 429, 5xx or an
// unreadable 2xx answer → ErrOutcomeUnknown (it may have been processed, T6b).
func (c *Client) mutate(ctx context.Context, r request, out any) error {
	ep, ok := Allowlist[r.op]
	if !ok || !ep.Mutates {
		return fmt.Errorf("trading212: operation %q is not allowlisted as a change", r.op)
	}
	acct, err := practice(ctx)
	if err != nil {
		return err
	}
	key, secret, err := c.keys(ctx, acct)
	if err != nil {
		return &domain.NotAcceptedError{Reason: "no keys: not sent", Err: err}
	}
	var body []byte
	if r.body != nil {
		if body, err = json.Marshal(r.body); err != nil {
			return &domain.NotAcceptedError{Reason: "unencodable body: not sent", Err: err}
		}
	}
	lim := c.limiter(acct.ID)
	wait, ok := lim.Reserve(r.op, ep.Every, maxMutationWait)
	if !ok {
		return &domain.NotAcceptedError{RateLimited: true, RetryAfter: wait, Reason: fmt.Sprintf("Trading 212 %s: the rate limit would delay it %s: not sent", r.op, wait.Round(time.Second))}
	}
	if wait > 0 {
		if err := c.cfg.Sleep(ctx, wait); err != nil {
			return &domain.NotAcceptedError{Reason: "cancelled while waiting for the rate limit: not sent", Err: err}
		}
	}
	status, data, err := c.send(ctx, lim, r.op, ep, c.target(acct, ep, r), key, secret, body)
	switch {
	case err != nil:
		return errMutation(r.op, status, err)
	case status < 200 || status > 299:
		c.cfg.Log.WarnContext(ctx, "Trading 212 refused a change", "op", string(r.op), "status", status)
		return errMutation(r.op, status, nil)
	case out == nil:
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("trading212 %s: unreadable answer: %w", r.op, domain.ErrOutcomeUnknown)
	}
	return nil
}

func (c *Client) attempt(ctx context.Context, lim *limiter, op Op, ep Endpoint, target, key, secret string, body []byte, out any) (retry bool, err error) {
	wait, ok := lim.Reserve(op, ep.Every, maxRateWait)
	if !ok {
		return false, fmt.Errorf("trading212 %s: rate limited for another %s: %w", op, wait.Round(time.Second), domain.ErrUnavailable)
	}
	if wait > 0 {
		if err := c.cfg.Sleep(ctx, wait); err != nil {
			return false, err
		}
	}
	status, data, err := c.send(ctx, lim, op, ep, target, key, secret, body)
	switch {
	case err != nil && status == 0:
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, fmt.Errorf("trading212 %s: %w: %w", op, domain.ErrUnavailable, err)
	case err != nil:
		return false, err
	case status < 200 || status > 299:
		apiErr := &APIError{Op: op, Status: status}
		if status == http.StatusTooManyRequests || status >= 500 {
			return true, apiErr
		}
		c.cfg.Log.WarnContext(ctx, "Trading 212 refused a request", "op", string(op), "status", status)
		return false, apiErr
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("trading212 %s: unexpected response: %w: %w", op, domain.ErrUnavailable, err)
	}
	return false, nil
}

// send performs one HTTP request with the endpoint's method and returns its status and body
// (status 0 when no answer came). A 429 holds the operation until Trading 212's reset.
func (c *Client) send(ctx context.Context, lim *limiter, op Op, ep Endpoint, target, key, secret string, body []byte) (int, []byte, error) {
	actx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	var rd io.Reader = http.NoBody
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(actx, ep.Method, target, rd)
	if err != nil {
		return 0, nil, fmt.Errorf("trading212 %s: %w", op, err)
	}
	req.SetBasicAuth(key, secret)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	start := c.cfg.Now()
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		c.cfg.Log.WarnContext(ctx, "Trading 212 request failed", "op", string(op), "err", redact(err))
		return 0, nil, redact(err)
	}
	defer resp.Body.Close()
	limit := int64(maxResponse)
	if op == OpInstruments {
		limit = maxInstrumentsResponse
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	c.cfg.Log.DebugContext(ctx, "Trading 212 request", "op", string(op), "status", resp.StatusCode,
		"duration_ms", c.cfg.Now().Sub(start).Milliseconds())
	if resp.StatusCode == http.StatusTooManyRequests {
		until := resetAt(resp.Header.Get("x-ratelimit-reset"), c.cfg.Now())
		lim.Block(op, until)
		c.cfg.Log.WarnContext(ctx, "Trading 212 rate limit hit", "op", string(op), "reset_in_s", until.Sub(c.cfg.Now()).Seconds())
	}
	switch {
	case err != nil:
		return resp.StatusCode, nil, fmt.Errorf("trading212 %s: reading the response: %w: %w", op, domain.ErrUnavailable, err)
	case int64(len(data)) > limit:
		return resp.StatusCode, nil, fmt.Errorf("trading212 %s: response larger than %d bytes: %w", op, limit, domain.ErrUnavailable)
	}
	return resp.StatusCode, data, nil
}

// redact drops the URL from a transport error: it names only the host and path, never a
// key, but it is all we need to say less.
func redact(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("%s: %w", strings.ToLower(ue.Op), ue.Err)
	}
	return err
}

// resetAt reads x-ratelimit-reset (Unix seconds). Missing, odd or past → 5 s from now;
// never more than a minute ahead.
func resetAt(h string, now time.Time) time.Time {
	n, err := strconv.ParseInt(strings.TrimSpace(h), 10, 64)
	if err != nil {
		return now.Add(5 * time.Second)
	}
	t := time.Unix(n, 0)
	switch {
	case !t.After(now):
		return now.Add(5 * time.Second)
	case t.Sub(now) > time.Minute:
		return now.Add(time.Minute)
	}
	return t
}

// --- conversions at the JSON edge (floats end here) ------------------------------------

// errRange is returned for a number that does not fit the minor units.
var errRange = errors.New("number out of range")

// scaled rounds f×scale half away from zero to an int64.
func scaled(f, scale float64) (int64, error) {
	v := math.Round(f * scale)
	if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) >= 1<<62 {
		return 0, errRange
	}
	return int64(v), nil
}

// conv collects the first conversion error so a response converts in one pass.
type conv struct{ err error }

// cents converts an amount of a two-decimal currency to its minor units.
func (c *conv) cents(f float64) domain.Cents {
	v, err := scaled(f, 100)
	if err != nil && c.err == nil {
		c.err = err
	}
	return domain.Cents(v)
}

// micros converts a price or quantity to millionths.
func (c *conv) micros(f float64) domain.Micros {
	v, err := scaled(f, 1e6)
	if err != nil && c.err == nil {
		c.err = err
	}
	return domain.Micros(v)
}

// sleepCtx sleeps for d or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
