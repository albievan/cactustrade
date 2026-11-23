package etoro

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// maxResponse bounds a response body; the largest (portfolio, 1000 candles) are far smaller.
const maxResponse = 8 << 20

// ErrNotEnabled is returned for operations that change the account before the phase that
// enables them (orders: phase 4). The client is read-only until then.
var ErrNotEnabled = errors.New("eToro order operations are not enabled in this version")

// Config builds a Client. Only tests set BaseURL, Sleep or Now.
type Config struct {
	BaseURL string
	HTTP    *http.Client
	Secrets ports.SecretStore
	Random  ports.Random
	Log     *slog.Logger
	// MaxAttempts for operations that do not change the account (default 3).
	MaxAttempts int
	// RequestTimeout per attempt (default 15 s).
	RequestTimeout time.Duration
	Now            func() time.Time
	Sleep          func(context.Context, time.Duration) error
}

// Client talks to the eToro Public API through the allowlist only.
type Client struct {
	cfg  Config
	base *url.URL
	mu   sync.Mutex
	lims map[string]*limiter // per account: eToro's limits are per user key
}

// New validates cfg and builds the client.
func New(cfg Config) (*Client, error) {
	if cfg.Secrets == nil || cfg.Random == nil {
		return nil, errors.New("etoro.New: Secrets and Random are required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = BaseURL
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("etoro.New: bad base URL %q", cfg.BaseURL)
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 3
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
	return &Client{cfg: cfg, base: base, lims: map[string]*limiter{}}, nil
}

func (c *Client) limiter(accountID string) *limiter {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.lims[accountID]
	if !ok {
		l = newLimiter(c.cfg.Now, c.cfg.Sleep)
		c.lims[accountID] = l
	}
	return l
}

// account is the eToro account a call is for (set by the app with ports.WithAccount); its
// mode is real only when the account says so. Without one nothing is sent (fail closed).
func account(ctx context.Context) (domain.Account, domain.TradingMode, error) {
	a, ok := ports.AccountFrom(ctx)
	if !ok || a.ID == "" {
		return domain.Account{}, "", fmt.Errorf("no eToro account was given for this call: %w", domain.ErrUnavailable)
	}
	if a.Mode == domain.ModeReal {
		return a, domain.ModeReal, nil
	}
	return a, domain.ModeDemo, nil
}

// APIError is a non-2xx answer from eToro. It never contains a key.
type APIError struct {
	Op      Op
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("eToro %s: HTTP %d", e.Op, e.Status)
	if e.Code != "" {
		s += " " + e.Code
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// Unwrap maps eToro failures onto domain errors: 404 is "not found"; anything else means
// the broker is unavailable for this request (fail closed). A rejected key is NOT our
// caller's authentication problem, so it is never ErrUnauthenticated.
func (e *APIError) Unwrap() error {
	if e.Status == http.StatusNotFound {
		return domain.ErrNotFound
	}
	return domain.ErrUnavailable
}

// keys returns the account's key pair.
func (c *Client) keys(ctx context.Context, a domain.Account, mode domain.TradingMode) (apiKey, userKey string, err error) {
	ak, uk := domain.AccountKeyNames(a)
	apiKey, err1 := c.cfg.Secrets.Get(ctx, ak)
	userKey, err2 := c.cfg.Secrets.Get(ctx, uk)
	if errors.Is(err1, domain.ErrNotFound) || errors.Is(err2, domain.ErrNotFound) || (err1 == nil && apiKey == "") || (err2 == nil && userKey == "") {
		return "", "", fmt.Errorf("the eToro %s API key and user key of %s are not both set: %w: %w", mode, a.Name, domain.ErrCredentialsMissing, domain.ErrUnavailable)
	}
	if err := errors.Join(err1, err2); err != nil {
		return "", "", fmt.Errorf("reading the eToro keys: %w", err)
	}
	return apiKey, userKey, nil
}

// call performs one allowlisted operation. path holds the values for the template's {name}
// placeholders; query and body are optional; out receives the decoded 2xx JSON.
func (c *Client) call(ctx context.Context, op Op, path map[string]string, query url.Values, body, out any) error {
	ep, ok := Allowlist[op]
	if !ok {
		return fmt.Errorf("etoro: operation %q is not allowlisted", op)
	}
	if ep.Mutates {
		return fmt.Errorf("etoro %s: %w", op, ErrNotEnabled)
	}
	acct, mode, err := account(ctx)
	if err != nil {
		return err
	}
	p, err := expand(ep.Path(mode), path)
	if err != nil {
		return fmt.Errorf("etoro %s: %w", op, err)
	}
	apiKey, userKey, err := c.keys(ctx, acct, mode)
	if err != nil {
		return err
	}
	var payload []byte
	if body != nil {
		if payload, err = json.Marshal(body); err != nil {
			return fmt.Errorf("etoro %s: %w", op, err)
		}
	}
	u := *c.base
	u.Path = p
	u.RawPath = ""
	u.RawQuery = query.Encode()
	for attempt := 1; ; attempt++ {
		retry, wait, err := c.attempt(ctx, c.limiter(acct.ID), op, ep, mode, u.String(), payload, apiKey, userKey, out)
		if err == nil || !retry || attempt >= c.cfg.MaxAttempts {
			return err
		}
		if wait <= 0 {
			wait = backoff(attempt)
		}
		if serr := c.cfg.Sleep(ctx, wait); serr != nil {
			return errors.Join(err, serr)
		}
	}
}

// attempt sends one request. It reports whether the failure is worth retrying and how long
// eToro asked us to wait.
func (c *Client) attempt(ctx context.Context, lim *limiter, op Op, ep Endpoint, mode domain.TradingMode, target string, payload []byte, apiKey, userKey string, out any) (retry bool, wait time.Duration, err error) {
	if err := lim.Wait(ctx, ep.Quota); err != nil {
		return false, 0, err
	}
	actx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	var rd io.Reader
	if payload != nil {
		rd = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(actx, ep.Method, target, rd)
	if err != nil {
		return false, 0, fmt.Errorf("etoro %s: %w", op, err)
	}
	reqID := c.cfg.Random.ID()
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("x-user-key", userKey)
	req.Header.Set("x-request-id", reqID)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	start := c.cfg.Now()
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		c.cfg.Log.WarnContext(ctx, "eToro request failed", "op", string(op), "mode", string(mode), "request_id", reqID, "err", err)
		if ctx.Err() != nil {
			return false, 0, ctx.Err()
		}
		return true, 0, fmt.Errorf("etoro %s: %w: %w", op, domain.ErrUnavailable, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	c.cfg.Log.DebugContext(ctx, "eToro request", "op", string(op), "mode", string(mode), "request_id", reqID,
		"status", resp.StatusCode, "duration_ms", c.cfg.Now().Sub(start).Milliseconds())
	if err != nil {
		return true, 0, fmt.Errorf("etoro %s: reading the response: %w: %w", op, domain.ErrUnavailable, err)
	}
	if len(data) > maxResponse {
		return false, 0, fmt.Errorf("etoro %s: response larger than %d bytes: %w", op, maxResponse, domain.ErrUnavailable)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := parseError(op, resp.StatusCode, data)
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			wait = retryAfter(resp.Header.Get("Retry-After"))
			lim.Block(ep.Quota, c.cfg.Now().Add(wait))
			c.cfg.Log.WarnContext(ctx, "eToro rate limit hit", "op", string(op), "quota", string(ep.Quota), "retry_after_s", wait.Seconds())
			return true, wait, apiErr
		case resp.StatusCode >= 500:
			return true, 0, apiErr
		}
		c.cfg.Log.WarnContext(ctx, "eToro refused a request", "op", string(op), "mode", string(mode), "request_id", reqID,
			"status", resp.StatusCode, "code", apiErr.Code)
		return false, 0, apiErr
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return false, 0, nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return false, 0, fmt.Errorf("etoro %s: unexpected response: %w: %w", op, domain.ErrUnavailable, err)
	}
	return false, 0, nil
}

// expand fills a path template; values are path-escaped and none may be left unfilled.
func expand(tpl string, vals map[string]string) (string, error) {
	out := tpl
	for k, v := range vals {
		if v == "" {
			return "", fmt.Errorf("empty path value %q: %w", k, domain.ErrInvalid)
		}
		out = strings.ReplaceAll(out, "{"+k+"}", url.PathEscape(v))
	}
	if strings.ContainsAny(out, "{}") {
		return "", fmt.Errorf("unfilled path template %q: %w", tpl, domain.ErrInvalid)
	}
	return out, nil
}

// parseError reads eToro's three error shapes: the gateway's {errorCode, errorMessage}, the
// API envelope {success:false, error:{code, message}} and RFC 7807 problem details.
func parseError(op Op, status int, data []byte) *APIError {
	var e struct {
		ErrorCode    string `json:"errorCode"`
		ErrorMessage string `json:"errorMessage"`
		Error        *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(data, &e)
	ae := &APIError{Op: op, Status: status, Code: e.ErrorCode, Message: e.ErrorMessage}
	if e.Error != nil {
		ae.Code, ae.Message = e.Error.Code, e.Error.Message
	}
	if ae.Code == "" && ae.Message == "" {
		ae.Code, ae.Message = e.Title, e.Detail
	}
	if len(ae.Message) > 300 {
		ae.Message = ae.Message[:300] + "…"
	}
	return ae
}

// retryAfter reads Retry-After in seconds (eToro's form); missing or odd → 5 s; capped at
// one window.
func retryAfter(h string) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || n <= 0 {
		return 5 * time.Second
	}
	return min(time.Duration(n)*time.Second, window)
}

// backoff is exponential with full jitter: up to 0.5 s, 1 s, 2 s, … capped at 8 s.
func backoff(attempt int) time.Duration {
	capped := min(500*time.Millisecond<<(attempt-1), 8*time.Second)
	n, err := rand.Int(rand.Reader, big.NewInt(int64(capped)))
	if err != nil {
		return capped
	}
	return time.Duration(n.Int64() + 1)
}

// Compile-time checks: the client is the MarketData and Broker adapter.
var (
	_ ports.MarketData = (*Client)(nil)
	_ ports.Broker     = (*Client)(nil)
)
