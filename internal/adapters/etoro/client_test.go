package etoro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// fakeEtoro serves fixtures by path and records every request.
type fakeEtoro struct {
	t      *testing.T
	mu     sync.Mutex
	reqs   []*http.Request
	bodies []string
	// route returns status, body and headers for a request; nil = fixture by path.
	route func(r *http.Request, n int) (int, string, http.Header)
}

var fixtureFor = map[string]string{
	"/api/v1/trading/info/demo/pnl":           "pnl.json",
	"/api/v1/trading/info/real/pnl":           "pnl.json",
	"/api/v2/market-data/rates":               "rates.json",
	"/api/v2/market-data/instruments/search":  "search.json",
	"/api/v2/trading/info/demo/eligibility":   "eligibility.json",
	"/api/v2/trading/info/demo/costs":         "costs.json",
	"/api/v2/trading/info/demo/orders:lookup": "lookup.json",
	"/api/v1/trading/info/trade/demo/history": "history.json",
}

func (f *fakeEtoro) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	f.bodies = append(f.bodies, string(b))
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
	name := fixtureFor[r.URL.Path]
	if strings.Contains(r.URL.Path, "/history/candles/") {
		name = "candles.json"
	}
	if name == "" {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"errorCode":"NotFound","errorMessage":"no route"}`)
		return
	}
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		f.t.Errorf("fixture %s: %v", name, err)
	}
	_, _ = w.Write(data)
}

type env struct {
	c     *Client
	fake  *fakeEtoro
	sec   *fakes.SecretStore
	mode  atomic.Value // domain.TradingMode
	slept []time.Duration
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{fake: &fakeEtoro{t: t}, sec: &fakes.SecretStore{}}
	e.mode.Store(domain.ModeDemo)
	srv := httptest.NewServer(e.fake)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	_ = e.sec.Put(ctx, domain.CredEtoroDemoAPIKey, "demo-api")
	_ = e.sec.Put(ctx, domain.CredEtoroDemoUserKey, "demo-user")
	var mu sync.Mutex
	now := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	c, err := New(Config{
		BaseURL: srv.URL, HTTP: srv.Client(), Secrets: e.sec, Random: &fakes.Random{},
		Now: func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		},
		Sleep: func(_ context.Context, d time.Duration) error { // a fake clock: sleeping moves it
			mu.Lock()
			e.slept = append(e.slept, d)
			now = now.Add(d)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.c = c
	return e
}

func (e *env) last() *http.Request { return e.fake.reqs[len(e.fake.reqs)-1] }

// ctx carries the account the calls are for: the env-keyed demo account, or a real account
// with its own key pair when the test switched e.mode to real.
func (e *env) ctx() context.Context { return ports.WithAccount(context.Background(), e.account()) }

func (e *env) account() domain.Account {
	if e.mode.Load().(domain.TradingMode) == domain.ModeReal {
		return domain.Account{ID: "real-1", Name: "Real", Mode: domain.ModeReal}
	}
	return domain.Account{ID: domain.EnvAccountID, Name: "Demo", Mode: domain.ModeDemo, EnvKeys: true}
}

func TestHeadersKeysAndRequestIDs(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	for range 2 {
		if _, err := e.c.Portfolio(ctx); err != nil {
			t.Fatal(err)
		}
	}
	a, b := e.fake.reqs[0], e.fake.reqs[1]
	if a.Header.Get("x-api-key") != "demo-api" || a.Header.Get("x-user-key") != "demo-user" {
		t.Fatal("keys not sent as x-api-key / x-user-key")
	}
	if a.Header.Get("x-request-id") == "" || a.Header.Get("x-request-id") == b.Header.Get("x-request-id") {
		t.Fatal("every call needs its own x-request-id")
	}
	if a.Method != http.MethodGet || a.URL.Path != "/api/v1/trading/info/demo/pnl" {
		t.Fatalf("demo portfolio went to %s %s", a.Method, a.URL.Path)
	}
}

func TestRealModeUsesRealKeysAndPaths(t *testing.T) {
	e := newEnv(t)
	e.mode.Store(domain.ModeReal)
	ctx := e.ctx()
	if _, err := e.c.Portfolio(ctx); !errors.Is(err, domain.ErrUnavailable) || len(e.fake.reqs) != 0 {
		t.Fatalf("real mode without real keys must not call eToro: %v (%d calls)", err, len(e.fake.reqs))
	}
	ak, uk := domain.AccountKeyNames(e.account())
	_ = e.sec.Put(ctx, ak, "real-api")
	_ = e.sec.Put(ctx, uk, "real-user")
	if _, err := e.c.Portfolio(ctx); err != nil {
		t.Fatal(err)
	}
	if r := e.last(); r.URL.Path != "/api/v1/trading/info/real/pnl" || r.Header.Get("x-api-key") != "real-api" {
		t.Fatalf("real request %s with %q", r.URL.Path, r.Header.Get("x-api-key"))
	}
}

func TestNoAccountFailsClosed(t *testing.T) {
	e := newEnv(t)
	if _, err := e.c.Portfolio(context.Background()); !errors.Is(err, domain.ErrUnavailable) || len(e.fake.reqs) != 0 {
		t.Fatalf("a call without an account: %v (%d requests)", err, len(e.fake.reqs))
	}
}

func TestLimitersArePerAccount(t *testing.T) {
	e := newEnv(t)
	a1, a2, b := e.c.limiter("a"), e.c.limiter("a"), e.c.limiter("b")
	if a1 == b || a1 != a2 {
		t.Fatal("one limiter per account")
	}
}

func TestMissingKeysFailClosed(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	_ = e.sec.Delete(ctx, domain.CredEtoroDemoUserKey)
	_, err := e.c.Portfolio(ctx)
	if !errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), "not both set") {
		t.Fatalf("err = %v", err)
	}
	if len(e.fake.reqs) != 0 {
		t.Fatal("called eToro without keys")
	}
}

func TestPortfolio(t *testing.T) {
	e := newEnv(t)
	p, err := e.c.Portfolio(e.ctx())
	if err != nil {
		t.Fatal(err)
	}
	if p.AvailableCash != 123456 || p.PendingOpen != 7550 || len(p.Positions) != 2 {
		t.Fatalf("portfolio = %+v", p)
	}
	a, b := p.Positions[0], p.Positions[1]
	if a.ID != 1001 || a.OpenRate != 189123456 || a.Units != 2643793 || a.Amount != 50000 || a.UnrealisedPnL != 1025 || !a.Conforms() {
		t.Fatalf("position a = %+v", a)
	}
	if b.Conforms() || b.RealAsset || b.Leverage != 2 || b.UnrealisedPnL != 0 {
		t.Fatalf("position b (CFD x2) = %+v", b)
	}
	// equity = cash + pending + Σ(amount + pnl)
	if got := p.Equity(); got != 123456+7550+50000+1025+10000 {
		t.Fatalf("equity = %d", got)
	}
}

func TestPortfolioWithoutCreditFailsClosed(t *testing.T) {
	e := newEnv(t)
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 200, `{"clientPortfolio":{"positions":[]}}`, nil
	}
	if _, err := e.c.Portfolio(e.ctx()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestQuotes(t *testing.T) {
	e := newEnv(t)
	qs, err := e.c.Quotes(e.ctx(), []domain.InstrumentID{1001, 2002, 3003})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.last().URL.Query().Get("instrumentIds"); got != "1001,2002,3003" {
		t.Fatalf("instrumentIds = %q (eToro wants form, explode=false)", got)
	}
	if len(qs) != 2 || qs[0].Bid != 189100000 || qs[0].Ask != 189200000 || qs[0].Delayed || !qs[1].Delayed {
		t.Fatalf("quotes = %+v (an instrument without a price must be left out)", qs)
	}
}

func TestCandles(t *testing.T) {
	e := newEnv(t)
	cs, err := e.c.Candles(e.ctx(), 1001, time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	if p := e.last().URL.Path; p != "/api/v1/market-data/instruments/1001/history/candles/desc/OneMinute/2" {
		t.Fatalf("path = %s", p)
	}
	if len(cs) != 2 || !cs[0].Start.Before(cs[1].Start) || cs[0].Close != 189200000 || cs[0].Volume != 1200 {
		t.Fatalf("candles must be oldest first: %+v", cs)
	}
	for _, bad := range []struct {
		iv time.Duration
		n  int
	}{{2 * time.Minute, 10}, {time.Minute, 0}, {time.Minute, 1001}} {
		if _, err := e.c.Candles(e.ctx(), 1001, bad.iv, bad.n); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("Candles(%v, %d) = %v", bad.iv, bad.n, err)
		}
	}
}

func TestSearchMarksSupportedShares(t *testing.T) {
	e := newEnv(t)
	is, err := e.c.Search(e.ctx(), "  example ")
	if err != nil {
		t.Fatal(err)
	}
	if q := e.last().URL.Query(); q.Get("query") != "example" || q.Get("limit") != "20" {
		t.Fatalf("query = %v", q)
	}
	want := map[domain.InstrumentID]domain.Market{1001: domain.MarketNYSE, 2002: domain.MarketLSE, 4004: "", 5005: ""}
	for _, in := range is {
		if in.Market != want[in.ID] {
			t.Errorf("%s (%s) market = %q, want %q", in.Symbol, in.Type, in.Market, want[in.ID])
		}
	}
	if _, err := e.c.Search(e.ctx(), " "); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("empty query accepted")
	}
}

func TestInstrumentsPaginates(t *testing.T) {
	e := newEnv(t)
	e.fake.route = func(r *http.Request, n int) (int, string, http.Header) {
		if r.URL.Query().Get("instrumentsIds") != "1001,2002" {
			t.Errorf("instrumentsIds = %q", r.URL.Query().Get("instrumentsIds"))
		}
		if n == 1 {
			return 200, `{"results":[{"instrumentId":1001,"displayName":"A","type":"Stocks","symbol":"A","exchangeId":5}],"pagination":{"hasNext":true,"nextPageToken":"p2"}}`, nil
		}
		if r.URL.Query().Get("pageToken") != "p2" {
			t.Errorf("page token not passed on")
		}
		return 200, `{"results":[{"instrumentId":2002,"displayName":"B","type":"Stocks","symbol":"B.L","exchangeId":7}],"pagination":{"hasNext":false}}`, nil
	}
	is, err := e.c.Instruments(e.ctx(), []domain.InstrumentID{1001, 2002})
	if err != nil || len(is) != 2 || is[1].Market != domain.MarketLSE {
		t.Fatalf("instruments = %+v %v", is, err)
	}
}

func TestEligibility(t *testing.T) {
	e := newEnv(t)
	els, err := e.c.Eligibility(e.ctx(), []domain.InstrumentID{1001, 2002, 3003, 6006, 9999})
	if err != nil {
		t.Fatal(err)
	}
	if b := e.fake.bodies[0]; b != `{"instrumentIds":[1001,2002,3003,6006,9999]}` {
		t.Fatalf("body = %s", b)
	}
	got := map[domain.InstrumentID]domain.Eligibility{}
	for _, el := range els {
		got[el.Instrument] = el
	}
	if !got[1001].RealLong || got[1001].MinPositionAmount != 1000 {
		t.Errorf("1001 real long x1 offered: %+v", got[1001])
	}
	for id, why := range map[domain.InstrumentID]string{
		2002: "CFD only", 3003: "only potential", 6006: "opening not allowed", 9999: "not found",
	} {
		if got[id].RealLong || got[id].Reason == "" {
			t.Errorf("%d (%s) must not be eligible: %+v", id, why, got[id])
		}
	}
}

func TestCostsSendOnlyGuardrailConstants(t *testing.T) {
	e := newEnv(t)
	items, err := e.c.Costs(e.ctx(), 2002, 12345)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(e.fake.bodies[0]), &body)
	want := map[string]any{
		"action": "open", "transaction": "buy", "instrumentId": 2002.0, "settlementType": "real",
		"orderType": "mkt", "leverage": 1.0, "amount": 123.45, "orderCurrency": "usd",
	}
	if fmt.Sprint(body) != fmt.Sprint(want) {
		t.Fatalf("costs body = %v", body)
	}
	if len(items) != 3 || items[2].Type != "sdrt" || items[2].Value != 500000 {
		t.Fatalf("costs = %+v", items)
	}
	if _, err := e.c.Costs(e.ctx(), 2002, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("zero amount accepted")
	}
}

func TestLookupAndHistory(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	st, err := e.c.Lookup(ctx, "req-1")
	if err != nil || st.State != domain.OrderFilled || st.OrderID != 555 || len(st.PositionIDs) != 1 {
		t.Fatalf("lookup = %+v %v", st, err)
	}
	if e.last().URL.Query().Get("referenceId") != "req-1" {
		t.Fatal("lookup must use referenceId = our x-request-id")
	}
	if _, err := e.c.Lookup(ctx, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("empty request id")
	}
	trades, err := e.c.TradeHistory(ctx, time.Date(2026, 11, 1, 23, 0, 0, 0, time.UTC))
	if err != nil || len(trades) != 1 || trades[0].NetProfit != -321 || !trades[0].Long {
		t.Fatalf("history = %+v %v", trades, err)
	}
	if q := e.last().URL.Query(); q.Get("minDate") != "2026-11-01" || q.Get("page") != "1" {
		t.Fatalf("history query = %v", q)
	}
}

func TestLookupStates(t *testing.T) {
	for id, want := range map[int]domain.OrderState{1: domain.OrderPending, 3: domain.OrderFilled, 4: domain.OrderRejected, 5: domain.OrderPartial, 10: domain.OrderPartial, 99: domain.OrderUnknown} {
		e := newEnv(t)
		e.fake.route = func(*http.Request, int) (int, string, http.Header) {
			return 200, fmt.Sprintf(`{"orderId":1,"status":{"id":%d,"name":"x","errorMessage":"why"}}`, id), nil
		}
		st, err := e.c.Lookup(e.ctx(), "r")
		if err != nil || st.State != want || st.Reason != "why" {
			t.Errorf("status %d → %+v %v", id, st, err)
		}
	}
	e := newEnv(t)
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 404, `{"type":"x","title":"Not Found","status":404,"detail":"no such order"}`, nil
	}
	st, err := e.c.Lookup(e.ctx(), "r")
	if !errors.Is(err, domain.ErrNotFound) || st.State != domain.OrderUnknown || !strings.Contains(err.Error(), "no such order") {
		t.Fatalf("404 lookup = %+v %v", st, err)
	}
}

func TestRetriesHonourRetryAfter(t *testing.T) {
	e := newEnv(t)
	e.fake.route = func(_ *http.Request, n int) (int, string, http.Header) {
		switch n {
		case 1:
			return 429, `{"errorCode":"TooManyRequests","errorMessage":"slow down"}`, http.Header{"Retry-After": {"7"}}
		case 2:
			return 503, `{"success":false,"error":{"code":"Unavailable","message":"try later"}}`, nil
		}
		return 0, "", nil
	}
	if _, err := e.c.Portfolio(e.ctx()); err != nil {
		t.Fatalf("after retries: %v", err)
	}
	if len(e.fake.reqs) != 3 {
		t.Fatalf("attempts = %d", len(e.fake.reqs))
	}
	if len(e.slept) < 1 || e.slept[0] != 7*time.Second {
		t.Fatalf("slept %v: must wait Retry-After first", e.slept)
	}
	if e.fake.reqs[0].Header.Get("x-request-id") == e.fake.reqs[1].Header.Get("x-request-id") {
		t.Fatal("each attempt carries a new x-request-id (reads only)")
	}
}

func TestNoRetryOnClientErrorsAndGivesUp(t *testing.T) {
	e := newEnv(t)
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 401, `{"errorCode":"Unauthorized","errorMessage":"bad key"}`, nil
	}
	_, err := e.c.Portfolio(e.ctx())
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 401 || ae.Code != "Unauthorized" || !errors.Is(err, domain.ErrUnavailable) || errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("401 = %v", err)
	}
	if len(e.fake.reqs) != 1 {
		t.Fatalf("401 retried %d times", len(e.fake.reqs))
	}
	if strings.Contains(err.Error(), "demo-api") || strings.Contains(err.Error(), "demo-user") {
		t.Fatal("key in error")
	}

	e = newEnv(t)
	e.fake.route = func(*http.Request, int) (int, string, http.Header) { return 500, "oops", nil }
	if _, err := e.c.Portfolio(e.ctx()); !errors.Is(err, domain.ErrUnavailable) || len(e.fake.reqs) != 3 {
		t.Fatalf("500: %v after %d attempts", err, len(e.fake.reqs))
	}
}

func TestBadResponses(t *testing.T) {
	e := newEnv(t)
	e.fake.route = func(*http.Request, int) (int, string, http.Header) { return 200, "{not json", nil }
	if _, err := e.c.Quotes(e.ctx(), []domain.InstrumentID{1}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("bad json: %v", err)
	}
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 200, strings.Repeat(" ", maxResponse+10), nil
	}
	if _, err := e.c.Quotes(e.ctx(), []domain.InstrumentID{1}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("oversized: %v", err)
	}
}

func TestOrdersAreNotEnabled(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	if _, err := e.c.Open(ctx, domain.OrderIntent{RequestID: "r", Instrument: 1, Amount: 100}); !errors.Is(err, ErrNotEnabled) {
		t.Fatal("open")
	}
	if err := e.c.Cancel(ctx, 1); !errors.Is(err, ErrNotEnabled) {
		t.Fatal("cancel")
	}
	if _, err := e.c.Close(ctx, 1, 1); !errors.Is(err, ErrNotEnabled) {
		t.Fatal("close")
	}
	// even through the generic call, every mutating operation is refused before any I/O
	for op, ep := range Allowlist {
		if !ep.Mutates {
			continue
		}
		if err := e.c.call(ctx, op, map[string]string{"orderId": "1", "positionId": "1"}, nil, nil, nil); !errors.Is(err, ErrNotEnabled) {
			t.Errorf("%s: %v", op, err)
		}
	}
	if err := e.c.call(ctx, Op("transfer"), nil, nil, nil, nil); err == nil {
		t.Fatal("non-allowlisted op")
	}
	if len(e.fake.reqs) != 0 {
		t.Fatal("a mutating request reached eToro")
	}
}

func TestExpandAndHelpers(t *testing.T) {
	if _, err := expand("/a/{x}/{y}", map[string]string{"x": "1"}); err == nil {
		t.Fatal("unfilled placeholder accepted")
	}
	if _, err := expand("/a/{x}", map[string]string{"x": ""}); err == nil {
		t.Fatal("empty value accepted")
	}
	if p, _ := expand("/a/{x}", map[string]string{"x": "../b"}); p != "/a/..%2Fb" {
		t.Fatalf("not escaped: %s", p)
	}
	if retryAfter("") != 5*time.Second || retryAfter("3") != 3*time.Second || retryAfter("999") != window {
		t.Fatal("retryAfter")
	}
	for a := 1; a <= 6; a++ {
		if b := backoff(a); b <= 0 || b > 8*time.Second {
			t.Fatalf("backoff(%d) = %v", a, b)
		}
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("New without deps")
	}
	if _, err := New(Config{Secrets: &fakes.SecretStore{}, Random: &fakes.Random{}, BaseURL: "::"}); err == nil {
		t.Fatal("bad base URL")
	}
	long := parseError(OpRates, 400, []byte(`{"errorCode":"X","errorMessage":"`+strings.Repeat("a", 400)+`"}`))
	if len(long.Message) > 310 {
		t.Fatal("message not truncated")
	}
}

func TestLimiterKeepsUnderQuota(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var slept []time.Duration
	l := newLimiter(func() time.Time { return now }, func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		now = now.Add(d)
		return nil
	})
	ctx := context.Background()
	for range QuotaLimits[QuotaExecution] {
		if err := l.Wait(ctx, QuotaExecution); err != nil {
			t.Fatal(err)
		}
	}
	if len(slept) != 0 {
		t.Fatal("slept within quota")
	}
	if err := l.Wait(ctx, QuotaExecution); err != nil || len(slept) != 1 || slept[0] != window {
		t.Fatalf("21st execution call must wait for the window: %v", slept)
	}
	// other groups are independent; a 429 blocks only its group
	l.Block(QuotaMarketData, now.Add(10*time.Second))
	l.Block(QuotaMarketData, now.Add(time.Second)) // an earlier block never shortens it
	if err := l.Wait(ctx, QuotaMarketData); err != nil || slept[len(slept)-1] != 10*time.Second {
		t.Fatalf("blocked group: %v", slept)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	l2 := newLimiter(time.Now, sleepCtx)
	l2.Block(QuotaCosts, time.Now().Add(time.Hour))
	if err := l2.Wait(cctx, QuotaCosts); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait: %v", err)
	}
	if err := l2.Wait(ctx, QuotaGroup("unknown")); err != nil {
		t.Fatal(err)
	}
}
