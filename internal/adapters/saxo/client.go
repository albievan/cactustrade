package saxo

import (
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

// maxResponse bounds a response body.
const maxResponse = 8 << 20

// maxRateWait is the longest a request waits for a rate-limit window before it gives up (the
// overview is interactive; the next 15-minute snapshot tries again).
const maxRateWait = 15 * time.Second

// Config builds a Client. Only tests set the URLs, HTTP, Now or Sleep.
type Config struct {
	SimBaseURL, LiveBaseURL string
	SimAuthURL, LiveAuthURL string
	HTTP                    *http.Client
	Secrets                 ports.SecretStore
	Log                     *slog.Logger
	// RequestTimeout per attempt (default 15 s).
	RequestTimeout time.Duration
	Now            func() time.Time
	Sleep          func(context.Context, time.Duration) error
}

// Client reads Saxo accounts through the allowlist only. It implements ports.Holdings and
// ports.OAuthConnector and nothing that changes an account.
type Client struct {
	cfg               Config
	sim, live         *url.URL
	simAuth, liveAuth *url.URL
	mu                sync.Mutex
	sessions          map[string]*session // per account
}

var (
	_ ports.Holdings       = (*Client)(nil)
	_ ports.OAuthConnector = (*Client)(nil)
)

func parseBase(name, s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("saxo.New: bad %s URL %q", name, s)
	}
	return u, nil
}

// New validates cfg and builds the client.
func New(cfg Config) (*Client, error) {
	if cfg.Secrets == nil {
		return nil, errors.New("saxo.New: Secrets is required")
	}
	defaults := []struct {
		v   *string
		def string
	}{
		{&cfg.SimBaseURL, SimBaseURL}, {&cfg.LiveBaseURL, LiveBaseURL}, {&cfg.SimAuthURL, SimAuthURL}, {&cfg.LiveAuthURL, LiveAuthURL},
	}
	for _, d := range defaults {
		if *d.v == "" {
			*d.v = d.def
		}
	}
	c := &Client{sessions: map[string]*session{}}
	var err error
	if c.sim, err = parseBase("SIM", cfg.SimBaseURL); err != nil {
		return nil, err
	}
	if c.live, err = parseBase("LIVE", cfg.LiveBaseURL); err != nil {
		return nil, err
	}
	if c.simAuth, err = parseBase("SIM sign-in", cfg.SimAuthURL); err != nil {
		return nil, err
	}
	if c.liveAuth, err = parseBase("LIVE sign-in", cfg.LiveAuthURL); err != nil {
		return nil, err
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
	c.cfg = cfg
	return c, nil
}

// session is one account's sign-in state and rate limits. mu serialises token refreshes:
// Saxo rotates the refresh token on every refresh, so two at once would burn one.
type session struct {
	mu         sync.Mutex
	access     string
	accessExp  time.Time
	refreshExp time.Time // zero = unknown (after a restart)
	from       string    // hash of the refresh token the access token came from
	dead       string    // hash of a refresh token Saxo refused: never sent again
	lim        *limiter
}

func (c *Client) session(accountID string) *session {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.sessions[accountID]
	if !ok {
		s = &session{lim: newLimiter(c.cfg.Now)}
		c.sessions[accountID] = s
	}
	return s
}

// account is the Saxo account a call is for (ports.WithAccount). Without one, or for an
// account on another platform, nothing is sent (fail closed).
func account(ctx context.Context) (domain.Account, error) {
	a, ok := ports.AccountFrom(ctx)
	if !ok || a.ID == "" {
		return domain.Account{}, fmt.Errorf("no Saxo account was given for this call: %w", domain.ErrUnavailable)
	}
	if a.OnPlatform() != domain.PlatformSaxo {
		return domain.Account{}, fmt.Errorf("%s is not a Saxo account: %w", a.Name, domain.ErrUnavailable)
	}
	return a, nil
}

// hosts are the account's API and sign-in hosts: SIM for a demo account, LIVE for a real one.
func (c *Client) hosts(a domain.Account) (api, auth url.URL) {
	if a.Mode == domain.ModeReal {
		return *c.live, *c.liveAuth
	}
	return *c.sim, *c.simAuth
}

// secret reads one credential of a: "" when it is not set.
func (c *Client) secret(ctx context.Context, a domain.Account, key string) (string, error) {
	n, ok := domain.AccountCredentialName(a, key)
	if !ok {
		return "", fmt.Errorf("saxo: no credential %q", key)
	}
	v, err := c.cfg.Secrets.Get(ctx, n)
	if errors.Is(err, domain.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading the Saxo %s: %w", key, err)
	}
	return v, nil
}

// APIError is a non-2xx answer from Saxo. It carries the status, Saxo's ErrorCode when the
// body has one (a fixed identifier such as "InvalidModelState"), and our own explanation;
// never a token or a key.
type APIError struct {
	Op     string
	Status int
	Code   string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("Saxo %s: HTTP %d", e.Op, e.Status)
	if e.Code != "" {
		s += " (" + e.Code + ")"
	}
	switch e.Status {
	case http.StatusUnauthorized:
		s += ": the sign-in was refused"
	case http.StatusForbidden:
		s += ": the app lacks a permission (it needs read access to the portfolio)"
	case http.StatusTooManyRequests:
		s += ": rate limit reached, try again shortly"
	}
	return s
}

// Unwrap maps failures onto domain errors: 404 is "not found", anything else means Saxo is
// unavailable for this request.
func (e *APIError) Unwrap() error {
	if e.Status == http.StatusNotFound {
		return domain.ErrNotFound
	}
	return domain.ErrUnavailable
}

// errorCode reads Saxo's {"ErrorCode": "..."} (or {"ErrorInfo": {"ErrorCode": ...}}) when it
// is a plain identifier; anything else is dropped.
func errorCode(body []byte) string {
	var e struct {
		ErrorCode string
		ErrorInfo *struct{ ErrorCode string }
		Error     string `json:"error"` // the sign-in host's OAuth error
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	code := e.ErrorCode
	if code == "" && e.ErrorInfo != nil {
		code = e.ErrorInfo.ErrorCode
	}
	if code == "" {
		code = e.Error
	}
	if len(code) > 64 {
		return ""
	}
	for _, r := range code {
		if !idChar(r) {
			return ""
		}
	}
	return code
}

// idChar reports whether r may appear in an error identifier: a letter, digit, "_", "-" or ".".
func idChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return r == '_' || r == '-' || r == '.'
}

// get performs one allowlisted GET with the account's bearer token and decodes the JSON
// answer into out. A 429 or a 5xx is retried once when the wait is short; a 401 drops the
// cached token and is retried once with a fresh one.
func (c *Client) get(ctx context.Context, op Op, query url.Values, out any) error {
	ep, ok := Allowlist[op]
	if !ok {
		return fmt.Errorf("saxo: operation %q is not allowlisted", op)
	}
	acct, err := account(ctx)
	if err != nil {
		return err
	}
	api, _ := c.hosts(acct)
	api.Path, api.RawPath, api.RawQuery = strings.TrimRight(api.Path, "/")+ep.Path, "", query.Encode()
	s := c.session(acct.ID)
	for attempt := 1; ; attempt++ {
		token, err := c.accessToken(ctx, acct, s)
		if err != nil {
			return err
		}
		retry, err := c.attempt(ctx, s, op, ep, api.String(), token, out)
		if err == nil || !retry || attempt >= 2 {
			return err
		}
	}
}

func (c *Client) attempt(ctx context.Context, s *session, op Op, ep Endpoint, target, token string, out any) (retry bool, err error) {
	wait, ok := s.lim.Reserve(op, ep.Every, maxRateWait)
	if !ok {
		return false, fmt.Errorf("saxo %s: rate limited for another %s: %w", op, wait.Round(time.Second), domain.ErrUnavailable)
	}
	if wait > 0 {
		if err := c.cfg.Sleep(ctx, wait); err != nil {
			return false, err
		}
	}
	actx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return false, fmt.Errorf("saxo %s: %w", op, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	start := c.cfg.Now()
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		c.cfg.Log.WarnContext(ctx, "Saxo request failed", "op", string(op), "err", redact(err))
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, fmt.Errorf("saxo %s: %w: %w", op, domain.ErrUnavailable, redact(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	c.cfg.Log.DebugContext(ctx, "Saxo request", "op", string(op), "status", resp.StatusCode,
		"duration_ms", c.cfg.Now().Sub(start).Milliseconds())
	if err != nil {
		return true, fmt.Errorf("saxo %s: reading the response: %w: %w", op, domain.ErrUnavailable, err)
	}
	if len(data) > maxResponse {
		return false, fmt.Errorf("saxo %s: response larger than %d bytes: %w", op, maxResponse, domain.ErrUnavailable)
	}
	if until, exhausted := rateLimit(resp.Header, c.cfg.Now()); exhausted {
		s.lim.Block(op, until)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{Op: string(op), Status: resp.StatusCode, Code: errorCode(data)}
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			until, _ := rateLimit(resp.Header, c.cfg.Now())
			s.lim.Block(op, until)
			c.cfg.Log.WarnContext(ctx, "Saxo rate limit hit", "op", string(op), "reset_in_s", until.Sub(c.cfg.Now()).Seconds())
			return true, apiErr
		case resp.StatusCode == http.StatusUnauthorized:
			s.mu.Lock()
			s.access = "" // the next attempt signs in again
			s.mu.Unlock()
			return true, apiErr
		case resp.StatusCode >= 500:
			return true, apiErr
		}
		c.cfg.Log.WarnContext(ctx, "Saxo refused a request", "op", string(op), "status", resp.StatusCode)
		return false, apiErr
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, fmt.Errorf("saxo %s: unexpected response: %w: %w", op, domain.ErrUnavailable, err)
	}
	return false, nil
}

// rateLimit reads Saxo's X-RateLimit-<Window>-Remaining / -Reset headers (Reset in seconds
// from now). exhausted is true when a window has no requests left; until is the latest reset
// among those (5 s from now when none says, at most a minute ahead).
func rateLimit(h http.Header, now time.Time) (until time.Time, exhausted bool) {
	var longest time.Duration
	for k, v := range h {
		k = http.CanonicalHeaderKey(k)
		if !strings.HasPrefix(k, "X-Ratelimit-") || !strings.HasSuffix(k, "-Remaining") || len(v) == 0 {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v[0])); err != nil || n > 0 {
			continue
		}
		exhausted = true
		reset := strings.TrimSuffix(k, "-Remaining") + "-Reset"
		if sec, err := strconv.Atoi(strings.TrimSpace(h.Get(reset))); err == nil && time.Duration(sec)*time.Second > longest {
			longest = time.Duration(sec) * time.Second
		}
	}
	switch {
	case longest <= 0:
		longest = 5 * time.Second
	case longest > time.Minute:
		longest = time.Minute
	}
	return now.Add(longest), exhausted
}

// redact drops the URL from a transport error: it names only the host and path, never a
// token (tokens travel in headers and bodies), but it is all we need to say less.
func redact(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return fmt.Errorf("%s: %w", strings.ToLower(ue.Op), ue.Err)
	}
	return err
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

// minor converts an amount of currency cur to its minor units (domain.CurrencyDecimals).
func (c *conv) minor(f float64, cur string) domain.Cents {
	v, err := scaled(f, float64(domain.MinorUnitsPerUnit(cur)))
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
