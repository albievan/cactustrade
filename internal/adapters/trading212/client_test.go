package trading212

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// fakeT212 serves the fixtures by path and records every request.
type fakeT212 struct {
	t    *testing.T
	name string // "demo" or "live"
	mu   sync.Mutex
	reqs []*http.Request
	// route overrides the answer: status 0 = serve the fixture.
	route func(r *http.Request, n int) (int, string, http.Header)
}

func (f *fakeT212) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	n := len(f.reqs)
	f.mu.Unlock()
	if f.route != nil {
		if status, body, h := f.route(r, n); status != 0 {
			for k, v := range h {
				w.Header()[k] = v
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
	}
	name := map[string]string{
		"/api/v0/equity/account/summary": "summary.json",
		"/api/v0/equity/positions":       "positions.json",
	}[r.URL.Path]
	if name == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		f.t.Errorf("fixture %s: %v", name, err)
	}
	_, _ = w.Write(data)
}

func (f *fakeT212) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.reqs) }

type env struct {
	c          *Client
	demo, live *fakeT212
	sec        *fakes.SecretStore
	mu         sync.Mutex
	now        time.Time
	slept      []time.Duration
	logs       strings.Builder
}

var (
	demoAcct = domain.Account{ID: "t-demo", Name: "Practice", Mode: domain.ModeDemo, Platform: domain.PlatformTrading212}
	liveAcct = domain.Account{ID: "t-live", Name: "ISA", Mode: domain.ModeReal, Platform: domain.PlatformTrading212}
)

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{
		demo: &fakeT212{t: t, name: "demo"}, live: &fakeT212{t: t, name: "live"}, sec: &fakes.SecretStore{},
		now: time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC),
	}
	ds, ls := httptest.NewServer(e.demo), httptest.NewServer(e.live)
	t.Cleanup(ds.Close)
	t.Cleanup(ls.Close)
	ctx := context.Background()
	for _, a := range []domain.Account{demoAcct, liveAcct} {
		k, s := domain.AccountKeyNames(a)
		_ = e.sec.Put(ctx, k, "key-"+a.ID)
		_ = e.sec.Put(ctx, s, "secret-"+a.ID)
	}
	c, err := New(Config{
		DemoBaseURL: ds.URL, LiveBaseURL: ls.URL, HTTP: ds.Client(), Secrets: e.sec,
		Log: slog.New(slog.NewTextHandler(&lockedWriter{e: e}, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
		Sleep: func(_ context.Context, d time.Duration) error { // a fake clock: sleeping moves it
			e.mu.Lock()
			defer e.mu.Unlock()
			e.slept = append(e.slept, d)
			e.now = e.now.Add(d)
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.c = c
	return e
}

type lockedWriter struct{ e *env }

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.e.mu.Lock()
	defer w.e.mu.Unlock()
	return w.e.logs.Write(p)
}

func (e *env) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

func ctxFor(a domain.Account) context.Context { return ports.WithAccount(context.Background(), a) }

func TestHoldings(t *testing.T) {
	e := newEnv(t)
	h, err := e.c.Holdings(ctxFor(demoAcct))
	if err != nil {
		t.Fatal(err)
	}
	if h.Platform != domain.PlatformTrading212 || h.Currency != "GBP" || h.Cash != 123457 || h.Equity != 384547 ||
		h.Invested != 250000 || h.UnrealisedPnL != 10040 || h.RealisedPnL != -1235 || len(h.Positions) != 2 || !h.AsOf.Equal(e.now) {
		t.Fatalf("holdings = %+v", h)
	}
	a := h.Positions[0]
	if a.Symbol != "AAPL_US_EQ" || a.Name != "Apple" || a.Currency != "USD" || a.Quantity != 2_500_000 || a.AvgPrice != 180_123_456 ||
		a.CurrentPrice != 190_500_000 || a.Value != 37512 || a.Cost != 35500 || a.PnL != 2012 || !a.Long || a.CFD ||
		!a.OpenedAt.Equal(time.Date(2026, 11, 20, 14, 31, 5, 123_000_000, time.UTC)) {
		t.Fatalf("AAPL = %+v", a)
	}
	if b := h.Positions[1]; b.Currency != "GBX" || b.PnL != -145 || b.Quantity != 10_000_000 {
		t.Fatalf("ULVR = %+v", b)
	}
	r := e.demo.reqs[0]
	user, pass, ok := r.BasicAuth()
	if !ok || user != "key-t-demo" || pass != "secret-t-demo" || r.Method != http.MethodGet || r.URL.Path != "/api/v0/equity/account/summary" {
		t.Fatalf("request %s %s basic=%v %q", r.Method, r.URL.Path, ok, user)
	}
	if e.demo.reqs[1].URL.Path != "/api/v0/equity/positions" || e.live.count() != 0 {
		t.Fatal("demo account must use the demo host only")
	}
	if strings.Contains(e.logs.String(), "secret-t-demo") || strings.Contains(e.logs.String(), "key-t-demo") {
		t.Fatal("a key reached the log")
	}
}

func TestRealAccountUsesLiveHost(t *testing.T) {
	e := newEnv(t)
	if _, err := e.c.Holdings(ctxFor(liveAcct)); err != nil {
		t.Fatal(err)
	}
	if e.live.count() != 2 || e.demo.count() != 0 {
		t.Fatalf("live %d demo %d", e.live.count(), e.demo.count())
	}
	if u, _, _ := e.live.reqs[0].BasicAuth(); u != "key-t-live" {
		t.Fatal("live account's own key")
	}
}

func TestFailsClosedWithoutAccountOrKeys(t *testing.T) {
	e := newEnv(t)
	tests := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"no account", context.Background(), domain.ErrUnavailable},
		{"an eToro account", ctxFor(domain.Account{ID: "e1", Name: "eToro", Platform: domain.PlatformEtoro}), domain.ErrUnavailable},
		{"no platform = eToro", ctxFor(domain.Account{ID: "e2", Name: "old"}), domain.ErrUnavailable},
		{"no keys", ctxFor(domain.Account{ID: "nokeys", Name: "New", Platform: domain.PlatformTrading212}), domain.ErrCredentialsMissing},
	}
	for _, tt := range tests {
		if _, err := e.c.Holdings(tt.ctx); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	// only the secret set: still missing
	half := domain.Account{ID: "half", Name: "Half", Platform: domain.PlatformTrading212}
	_, s := domain.AccountKeyNames(half)
	_ = e.sec.Put(context.Background(), s, "only-secret")
	if _, err := e.c.Holdings(ctxFor(half)); !errors.Is(err, domain.ErrCredentialsMissing) || strings.Contains(err.Error(), "only-secret") {
		t.Fatalf("half keys: %v", err)
	}
	if e.demo.count()+e.live.count() != 0 {
		t.Fatal("called Trading 212 without an account or keys")
	}
}

func TestErrorsNeverCarryKeys(t *testing.T) {
	for _, status := range []int{401, 403, 404, 408} {
		e := newEnv(t)
		e.demo.route = func(*http.Request, int) (int, string, http.Header) { return status, `{"secret":"secret-t-demo"}`, nil }
		_, err := e.c.Holdings(ctxFor(demoAcct))
		if err == nil || strings.Contains(err.Error(), "secret-t-demo") || strings.Contains(err.Error(), "key-t-demo") {
			t.Fatalf("%d: %v", status, err)
		}
		want := domain.ErrUnavailable
		if status == 404 {
			want = domain.ErrNotFound
		}
		if !errors.Is(err, want) || !strings.Contains(err.Error(), "HTTP "+strconv.Itoa(status)) {
			t.Fatalf("%d: %v", status, err)
		}
		if e.demo.count() != 1 {
			t.Fatalf("%d retried (%d calls)", status, e.demo.count())
		}
	}
}

func TestRateLimit429BacksOffUntilReset(t *testing.T) {
	e := newEnv(t)
	reset := e.now.Add(8 * time.Second).Unix() // later than the 5 s summary spacing
	e.demo.route = func(_ *http.Request, n int) (int, string, http.Header) {
		if n == 1 {
			return 429, "", http.Header{"X-Ratelimit-Reset": {strconv.FormatInt(reset, 10)}, "X-Ratelimit-Remaining": {"0"}}
		}
		return 0, "", nil
	}
	if _, err := e.c.Holdings(ctxFor(demoAcct)); err != nil {
		t.Fatal(err)
	}
	if len(e.slept) == 0 || e.slept[0] != 8*time.Second {
		t.Fatalf("slept %v, want the 8 s to the reset", e.slept)
	}
	// a reset far away: no long wait, the caller gets "unavailable" now
	e2 := newEnv(t)
	e2.demo.route = func(*http.Request, int) (int, string, http.Header) {
		return 429, "", http.Header{"X-Ratelimit-Reset": {strconv.FormatInt(e2.now.Add(50*time.Second).Unix(), 10)}}
	}
	if _, err := e2.c.Holdings(ctxFor(demoAcct)); !errors.Is(err, domain.ErrUnavailable) || e2.demo.count() != 1 {
		t.Fatalf("far reset: %v (%d calls)", err, e2.demo.count())
	}
}

func TestServerErrorRetriedOnce(t *testing.T) {
	e := newEnv(t)
	e.demo.route = func(*http.Request, int) (int, string, http.Header) { return 503, "", nil }
	if _, err := e.c.Holdings(ctxFor(demoAcct)); !errors.Is(err, domain.ErrUnavailable) || e.demo.count() != 2 {
		t.Fatalf("%v (%d calls)", err, e.demo.count())
	}
}

func TestLimiterSpacesRequests(t *testing.T) {
	e := newEnv(t)
	for range 2 {
		if _, err := e.c.Holdings(ctxFor(demoAcct)); err != nil {
			t.Fatal(err)
		}
	}
	// the second summary waits for the 5 s window; positions (1 s) are then free again
	if len(e.slept) != 1 || e.slept[0] != 5*time.Second {
		t.Fatalf("slept %v", e.slept)
	}
	if a1, a2 := e.c.limiter("a"), e.c.limiter("a"); a1 != a2 || a1 == e.c.limiter("b") {
		t.Fatal("one limiter per account")
	}
	l := newLimiter(func() time.Time { return e.now })
	if w, ok := l.Reserve(OpPositions, time.Second, time.Second); !ok || w != 0 {
		t.Fatal("first slot")
	}
	l.Block(OpPositions, e.now.Add(time.Minute))
	l.Block(OpPositions, e.now) // an earlier block never shortens the wait
	if w, ok := l.Reserve(OpPositions, time.Second, maxRateWait); ok || w != time.Minute {
		t.Fatalf("blocked: %v %v", w, ok)
	}
	e.advance(time.Minute)
	if _, ok := l.Reserve(OpPositions, time.Second, maxRateWait); !ok {
		t.Fatal("a refused reservation must not have booked a slot")
	}
}

func TestBadResponsesFailClosed(t *testing.T) {
	tests := map[string]struct{ summary, positions string }{
		"not JSON":         {`<html>`, `[]`},
		"no currency":      {`{"cash":{"availableToTrade":1},"totalValue":1}`, `[]`},
		"no total":         {`{"cash":{"availableToTrade":1},"currency":"GBP"}`, `[]`},
		"no cash":          {`{"currency":"GBP","totalValue":1}`, `[]`},
		"positions broken": {`{"cash":{"availableToTrade":1},"currency":"GBP","totalValue":1}`, `{}`},
		"huge number":      {`{"cash":{"availableToTrade":1e300},"currency":"GBP","totalValue":1}`, `[]`},
		"huge quantity":    {`{"cash":{"availableToTrade":1},"currency":"GBP","totalValue":1}`, `[{"quantity":1e300}]`},
	}
	for name, tt := range tests {
		e := newEnv(t)
		e.demo.route = func(r *http.Request, _ int) (int, string, http.Header) {
			if strings.HasSuffix(r.URL.Path, "/summary") {
				return 200, tt.summary, nil
			}
			return 200, tt.positions, nil
		}
		if _, err := e.c.Holdings(ctxFor(demoAcct)); !errors.Is(err, domain.ErrUnavailable) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a body over the cap
	e := newEnv(t)
	e.demo.route = func(*http.Request, int) (int, string, http.Header) {
		return 200, `"` + strings.Repeat("x", maxResponse) + `"`, nil
	}
	if _, err := e.c.Holdings(ctxFor(demoAcct)); !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("cap: %v", err)
	}
}

func TestTransportErrorIsUnavailable(t *testing.T) {
	sec := &fakes.SecretStore{}
	k, s := domain.AccountKeyNames(demoAcct)
	_ = sec.Put(context.Background(), k, "key")
	_ = sec.Put(context.Background(), s, "secret")
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // nothing listens any more
	c, err := New(Config{DemoBaseURL: srv.URL, LiveBaseURL: srv.URL, Secrets: sec, Sleep: func(context.Context, time.Duration) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Holdings(ctxFor(demoAcct)); !errors.Is(err, domain.ErrUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("%v", err)
	}
	ctx, cancel := context.WithCancel(ctxFor(demoAcct))
	cancel()
	if _, err := c.Holdings(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestNewValidates(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("no secrets")
	}
	if _, err := New(Config{Secrets: &fakes.SecretStore{}, DemoBaseURL: "::"}); err == nil {
		t.Fatal("bad demo URL")
	}
	if _, err := New(Config{Secrets: &fakes.SecretStore{}, LiveBaseURL: "nohost"}); err == nil {
		t.Fatal("bad live URL")
	}
	c, err := New(Config{Secrets: &fakes.SecretStore{}})
	if err != nil || c.demo.String() != DemoBaseURL || c.live.String() != LiveBaseURL || c.cfg.RequestTimeout != 15*time.Second {
		t.Fatalf("defaults: %v", err)
	}
	if err := c.get(ctxFor(demoAcct), Op("orders"), nil, nil); err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("unknown op: %v", err)
	}
}

func TestHelpers(t *testing.T) {
	now := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	for h, want := range map[string]time.Duration{
		"":                                     5 * time.Second,
		"soon":                                 5 * time.Second,
		strconv.FormatInt(now.Unix()-10, 10):   5 * time.Second,
		strconv.FormatInt(now.Unix()+3, 10):    3 * time.Second,
		strconv.FormatInt(now.Unix()+3600, 10): time.Minute,
		" " + strconv.FormatInt(now.Unix()+7, 10): 7 * time.Second,
	} {
		if got := resetAt(h, now).Sub(now); got != want {
			t.Errorf("resetAt(%q) = %v, want %v", h, got, want)
		}
	}
	for f, want := range map[float64]int64{0.125: 13, -0.125: -13, 1.005: 100, 12.34: 1234, -0.004: 0} {
		if got, err := scaled(f, 100); err != nil || got != want {
			t.Errorf("scaled(%v) = %d %v, want %d", f, got, err, want)
		}
	}
	if _, err := scaled(1e300, 100); !errors.Is(err, errRange) {
		t.Fatal("range")
	}
	var cv conv
	_ = cv.cents(1e300)
	_ = cv.micros(1e300)
	if !errors.Is(cv.err, errRange) {
		t.Fatal("conv keeps the first error")
	}
	if got := (&APIError{Op: OpPositions, Status: 500}).Error(); got != "Trading 212 positions: HTTP 500" {
		t.Fatal(got)
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal("sleepCtx ignores ctx")
	}
}
