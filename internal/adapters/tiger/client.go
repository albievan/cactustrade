package tiger

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
	"strings"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// maxResponse bounds a response body.
const maxResponse = 8 << 20

// maxRateWait is the longest a call waits for a rate-limit slot before it gives up.
const maxRateWait = 15 * time.Second

// rateBackoff is how long a method is held back after Tiger says "too many requests" (HTTP
// 429 or business code 5) without saying for how long.
const rateBackoff = 10 * time.Second

// Envelope constants (Tiger's SDKs send the same).
const (
	charset    = "UTF-8"
	signType   = "RSA"
	apiVersion = "2.0"
	timeLayout = "2006-01-02 15:04:05" // UTC
)

// Config builds a Client. Only tests set GatewayURL, HTTP, Now or Sleep.
type Config struct {
	GatewayURL string
	HTTP       *http.Client
	Secrets    ports.SecretStore
	Log        *slog.Logger
	// RequestTimeout per attempt (default 15 s).
	RequestTimeout time.Duration
	Now            func() time.Time
	Sleep          func(context.Context, time.Duration) error
}

// Client reads Tiger accounts through the method allowlist only. It implements
// ports.Holdings and nothing that changes an account.
type Client struct {
	cfg     Config
	gateway *url.URL
	mu      sync.Mutex
	lims    map[string]*limiter // per account
}

var _ ports.Holdings = (*Client)(nil)

// New validates cfg and builds the client.
func New(cfg Config) (*Client, error) {
	if cfg.Secrets == nil {
		return nil, errors.New("tiger.New: Secrets is required")
	}
	if cfg.GatewayURL == "" {
		cfg.GatewayURL = GatewayURL
	}
	gw, err := url.Parse(cfg.GatewayURL)
	if err != nil || gw.Host == "" || (gw.Scheme != "https" && gw.Scheme != "http") {
		return nil, fmt.Errorf("tiger.New: bad gateway URL %q", cfg.GatewayURL)
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
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
	return &Client{cfg: cfg, gateway: gw, lims: map[string]*limiter{}}, nil
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

// account is the Tiger account a call is for (ports.WithAccount). Without one, or for an
// account on another platform, nothing is sent (fail closed).
func account(ctx context.Context) (domain.Account, error) {
	a, ok := ports.AccountFrom(ctx)
	if !ok || a.ID == "" {
		return domain.Account{}, fmt.Errorf("no Tiger account was given for this call: %w", domain.ErrUnavailable)
	}
	if a.OnPlatform() != domain.PlatformTiger {
		return domain.Account{}, fmt.Errorf("%s is not a Tiger account: %w", a.Name, domain.ErrUnavailable)
	}
	return a, nil
}

// creds are an account's Tiger ID, account number and private key.
type creds struct {
	tigerID, account, key string
}

func (c *Client) creds(ctx context.Context, a domain.Account) (creds, error) {
	var out creds
	for _, x := range []struct {
		key string
		dst *string
	}{{domain.TigerID, &out.tigerID}, {domain.TigerAccount, &out.account}, {domain.TigerPrivateKey, &out.key}} {
		n, _ := domain.AccountCredentialName(a, x.key)
		v, err := c.cfg.Secrets.Get(ctx, n)
		switch {
		case errors.Is(err, domain.ErrNotFound) || (err == nil && v == ""):
			return creds{}, fmt.Errorf("the Tiger ID, account number and private key of %s are not all set: %w: %w", a.Name, domain.ErrCredentialsMissing, domain.ErrUnavailable)
		case err != nil:
			return creds{}, fmt.Errorf("reading the Tiger credentials: %w", err)
		}
		*x.dst = v
	}
	return out, nil
}

// APIError is a non-2xx HTTP answer from the gateway: status only.
type APIError struct {
	Method Method
	Status int
}

func (e *APIError) Error() string {
	s := fmt.Sprintf("Tiger %s: HTTP %d", e.Method, e.Status)
	if e.Status == http.StatusTooManyRequests {
		s += ": rate limit reached, try again shortly"
	}
	return s
}

// Unwrap: every failure means Tiger is unavailable for this request.
func (e *APIError) Unwrap() error { return domain.ErrUnavailable }

// BusinessError is an answer with a non-zero code. Message is Tiger's text with the
// account's identifiers removed, shortened.
type BusinessError struct {
	Method  Method
	Code    int
	Message string
}

func (e *BusinessError) Error() string {
	s := fmt.Sprintf("Tiger %s: code %d", e.Method, e.Code)
	switch {
	case e.Code == 5:
		s += ": rate limit reached, try again shortly"
	case e.Code >= 1000 && e.Code < 1010:
		s += ": the request was refused (check the Tiger ID and that the public key is uploaded)"
	case e.Code >= 2400 && e.Code < 5000:
		s += ": no permission or the sign-in was refused"
	}
	if e.Message != "" {
		s += " (" + e.Message + ")"
	}
	return s
}

// Unwrap: every failure means Tiger is unavailable for this request.
func (e *BusinessError) Unwrap() error { return domain.ErrUnavailable }

// envelope is Tiger's answer.
type envelope struct {
	Code    *int            `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// call signs and sends one allowlisted method for the account in ctx and decodes its data
// into out. A rate limit or 5xx is retried once when the wait is short.
func (c *Client) call(ctx context.Context, m Method, biz map[string]any, out any) error {
	every, ok := Allowlist[m]
	if !ok {
		return fmt.Errorf("tiger: method %q is not allowlisted", m)
	}
	acct, err := account(ctx)
	if err != nil {
		return err
	}
	cr, err := c.creds(ctx, acct)
	if err != nil {
		return err
	}
	key, err := parseKey(cr.key)
	if err != nil {
		return fmt.Errorf("tiger %s: %w: %w", m, err, domain.ErrUnavailable)
	}
	if biz == nil {
		biz = map[string]any{}
	}
	biz["account"] = cr.account
	content, err := json.Marshal(biz)
	if err != nil {
		return fmt.Errorf("tiger %s: %w", m, err)
	}
	lim := c.limiter(acct.ID)
	for attempt := 1; ; attempt++ {
		params := map[string]string{
			"tiger_id": cr.tigerID, "method": string(m), "charset": charset, "sign_type": signType,
			"timestamp": c.cfg.Now().UTC().Format(timeLayout), "version": apiVersion, "biz_content": string(content),
		}
		sig, err := sign(key, params)
		if err != nil {
			return fmt.Errorf("tiger %s: %w: %w", m, err, domain.ErrUnavailable)
		}
		params["sign"] = sig
		retry, err := c.attempt(ctx, lim, m, every, params, cr, out)
		if err == nil || !retry || attempt >= 2 {
			return err
		}
	}
}

func (c *Client) attempt(ctx context.Context, lim *limiter, m Method, every time.Duration, params map[string]string, cr creds, out any) (retry bool, err error) {
	wait, ok := lim.Reserve(m, every, maxRateWait)
	if !ok {
		return false, fmt.Errorf("tiger %s: rate limited for another %s: %w", m, wait.Round(time.Second), domain.ErrUnavailable)
	}
	if wait > 0 {
		if err := c.cfg.Sleep(ctx, wait); err != nil {
			return false, err
		}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return false, fmt.Errorf("tiger %s: %w", m, err)
	}
	actx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, c.gateway.String(), bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("tiger %s: %w", m, err)
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Accept", "application/json")
	start := c.cfg.Now()
	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		c.cfg.Log.WarnContext(ctx, "Tiger request failed", "op", string(m), "err", redact(err))
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, fmt.Errorf("tiger %s: %w: %w", m, domain.ErrUnavailable, redact(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	c.cfg.Log.DebugContext(ctx, "Tiger request", "op", string(m), "status", resp.StatusCode,
		"duration_ms", c.cfg.Now().Sub(start).Milliseconds())
	if err != nil {
		return true, fmt.Errorf("tiger %s: reading the response: %w: %w", m, domain.ErrUnavailable, err)
	}
	if len(data) > maxResponse {
		return false, fmt.Errorf("tiger %s: response larger than %d bytes: %w", m, maxResponse, domain.ErrUnavailable)
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		lim.Block(m, c.cfg.Now().Add(rateBackoff))
		c.cfg.Log.WarnContext(ctx, "Tiger rate limit hit", "op", string(m), "status", resp.StatusCode)
		return true, &APIError{Method: m, Status: resp.StatusCode}
	case resp.StatusCode >= 500:
		return true, &APIError{Method: m, Status: resp.StatusCode}
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		c.cfg.Log.WarnContext(ctx, "Tiger refused a request", "op", string(m), "status", resp.StatusCode)
		return false, &APIError{Method: m, Status: resp.StatusCode}
	}
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Code == nil {
		return false, fmt.Errorf("tiger %s: unexpected response: %w", m, domain.ErrUnavailable)
	}
	if *env.Code != 0 {
		bizErr := &BusinessError{Method: m, Code: *env.Code, Message: scrub(env.Message, cr)}
		c.cfg.Log.WarnContext(ctx, "Tiger refused a request", "op", string(m), "code", *env.Code)
		if *env.Code == 5 {
			lim.Block(m, c.cfg.Now().Add(rateBackoff))
			return true, bizErr
		}
		return false, bizErr
	}
	if err := unmarshalData(env.Data, out); err != nil {
		return false, fmt.Errorf("tiger %s: unexpected data: %w", m, domain.ErrUnavailable)
	}
	return false, nil
}

// unmarshalData decodes data into out; Tiger sometimes sends data as a JSON string holding
// the JSON (its SDK handles both).
func unmarshalData(data json.RawMessage, out any) error {
	if len(data) == 0 || string(data) == "null" {
		return errors.New("no data")
	}
	if err := json.Unmarshal(data, out); err == nil {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	return json.Unmarshal([]byte(s), out)
}

// scrub removes the account's identifiers and anything unprintable from Tiger's message and
// keeps at most 160 characters.
func scrub(msg string, cr creds) string {
	for _, s := range []string{cr.key, cr.tigerID, cr.account} {
		if s != "" {
			msg = strings.ReplaceAll(msg, s, "…")
		}
	}
	var b strings.Builder
	for _, r := range msg {
		if b.Len() >= 160 {
			break
		}
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// redact drops the URL from a transport error.
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
