package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

type lseOnly struct{}

func (lseOnly) Session(m domain.Market, at time.Time) (time.Time, time.Time, bool, error) {
	if m != domain.MarketLSE {
		return time.Time{}, time.Time{}, false, nil
	}
	y, mo, d := at.UTC().Date()
	return time.Date(y, mo, d, 8, 0, 0, 0, time.UTC), time.Date(y, mo, d, 16, 30, 0, 0, time.UTC), true, nil
}

func newMarketTC(t *testing.T) (*tc, *fakes.Broker) {
	t.Helper()
	st := fakes.NewStore()
	clk := &fakes.Clock{T: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	b := &fakes.Broker{
		Account: domain.Portfolio{AvailableCash: 123456, PendingOpen: 500, AsOf: clk.T, Positions: []domain.Position{
			{ID: 7, Instrument: 1, OpenRate: 189123456, Units: 2500000, Amount: 47280, UnrealisedPnL: -120, StopLossRate: 180000000, RealAsset: true, Long: true, Leverage: 1},
		}},
		Known: map[domain.InstrumentID]domain.Instrument{
			1: {ID: 1, Symbol: "AAPL", Name: "Apple", Type: "Stocks", ExchangeID: 4, Market: domain.MarketNYSE},
			3: {ID: 3, Symbol: "SAP.DE", Name: "SAP", Type: "Stocks"},
		},
		Eligible: map[domain.InstrumentID]domain.Eligibility{1: {Instrument: 1, RealLong: true, MinPositionAmount: 1000, CheckedAt: clk.T}},
		Prices:   map[domain.InstrumentID]domain.Quote{1: {Instrument: 1, Bid: 189100000, Ask: 189200000, At: clk.T}},
	}
	svc, err := app.New(app.Deps{
		Store: st, Secrets: &fakes.SecretStore{Now: clk.Now}, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: clk,
		Broker: b, Market: b, Calendar: lseOnly{},
	}, app.Config{Version: "9.9.9"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(New(svc, slog.New(slog.DiscardHandler), Config{CookieSecure: true, OpenAPI: []byte("openapi: 3.0.3\n")}, nil))
	t.Cleanup(srv.Close)
	c := srv.Client()
	c.Jar, _ = cookiejar.New(nil)
	x := &tc{t: t, srv: srv, c: c, store: st, svc: svc}
	x.setupAndLogin()
	return x, b
}

func TestBrokerAndMarketEndpoints(t *testing.T) {
	x, b := newMarketTC(t)
	csrf := []string{"X-CSRF-Token", x.csrf}

	code, m, _ := x.do("POST", "/api/v1/broker/test", "", csrf...)
	if code != 200 || m["ok"] != true || m["availableCashCents"] != 123456.0 || m["positions"] != 1.0 || m["mode"] != "demo" {
		t.Fatalf("test connection %d %v", code, m)
	}
	code, m, _ = x.do("GET", "/api/v1/broker/portfolio", "")
	pos := m["positions"].([]any)[0].(map[string]any)
	if code != 200 || m["equityCents"] != 123456.0+500+47280-120 || pos["openRate"] != "189.123456" || pos["units"] != "2.5" ||
		pos["stopLossRate"] != "180" || pos["takeProfitRate"] != nil {
		t.Fatalf("portfolio %d %v", code, m)
	}
	var sessions []map[string]any
	if code := x.getJSON("/api/v1/market/status", &sessions); code != 200 || len(sessions) != 2 || sessions[0]["open"] != true || sessions[1]["trading"] != false {
		t.Fatalf("market status %d %v", code, sessions)
	}
	var found []map[string]any
	if code := x.getJSON("/api/v1/instruments/search?q=apple", &found); code != 200 || len(found) != 2 || found[0]["market"] != "NYSE" || found[1]["market"] != "" {
		t.Fatalf("search %d %v", code, found)
	}
	if code, _, _ := x.do("GET", "/api/v1/instruments/search", ""); code != 400 {
		t.Fatalf("search without q %d", code)
	}
	if code, m, _ := x.do("POST", "/api/v1/watchlist", `{"instrumentId":3}`, csrf...); code != 400 || m["field"] != "instrumentId" {
		t.Fatalf("unsupported market %d %v", code, m)
	}
	code, m, _ = x.do("POST", "/api/v1/watchlist", `{"instrumentId":1}`, csrf...)
	if code != 201 || m["instrument"].(map[string]any)["symbol"] != "AAPL" || m["eligibility"].(map[string]any)["minPositionCents"] != 1000.0 {
		t.Fatalf("add %d %v", code, m)
	}
	if code, _, _ := x.do("POST", "/api/v1/watchlist", `{"instrumentId":1}`, csrf...); code != 409 {
		t.Fatalf("duplicate add %d", code)
	}
	var items, quotes []map[string]any
	if code := x.getJSON("/api/v1/watchlist", &items); code != 200 || len(items) != 1 {
		t.Fatalf("watchlist %d %v", code, items)
	}
	if code := x.getJSON("/api/v1/watchlist/quotes", &quotes); code != 200 || len(quotes) != 1 || quotes[0]["bid"] != "189.1" || quotes[0]["ask"] != "189.2" {
		t.Fatalf("quotes %d %v", code, quotes)
	}
	if code, _, _ := x.do("DELETE", "/api/v1/watchlist/1", "", csrf...); code != 204 {
		t.Fatalf("remove %d", code)
	}
	if code, _, _ := x.do("DELETE", "/api/v1/watchlist/1", "", csrf...); code != 404 {
		t.Fatalf("remove again %d", code)
	}

	// eToro down: test connection reports it in the body; portfolio is 503
	b.Err = domain.ErrUnavailable
	if code, m, _ := x.do("POST", "/api/v1/broker/test", "", csrf...); code != 200 || m["ok"] != false || m["error"] == "" {
		t.Fatalf("failed test %d %v", code, m)
	}
	if code, _, _ := x.do("GET", "/api/v1/broker/portfolio", ""); code != 503 {
		t.Fatalf("portfolio while eToro is down %d", code)
	}
}

func TestMarketEndpointsNeedRoles(t *testing.T) {
	x, _ := newMarketTC(t)
	_, err := x.svc.CreateUser(context.Background(), app.Principal{UserID: "x", Role: domain.RoleAdmin, Via: app.ViaSession, Username: "admin"}, "vera", "vera password 1", domain.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	x.c.Jar, _ = cookiejar.New(nil) // a fresh browser
	x.login("vera", "vera password 1")
	csrf := []string{"X-CSRF-Token", x.csrf}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/broker/test"}, {"GET", "/api/v1/instruments/search?q=a"}, {"DELETE", "/api/v1/watchlist/1"},
	} {
		if code, _, _ := x.do(c.method, c.path, "", csrf...); code != 403 {
			t.Errorf("viewer %s %s = %d", c.method, c.path, code)
		}
	}
	if code, _, _ := x.do("POST", "/api/v1/watchlist", `{"instrumentId":1}`, csrf...); code != 403 {
		t.Errorf("viewer add = %d", code)
	}
	for _, p := range []string{"/api/v1/broker/portfolio", "/api/v1/market/status", "/api/v1/watchlist", "/api/v1/watchlist/quotes"} {
		if code, _, _ := x.do("GET", p, ""); code != 200 {
			t.Errorf("viewer GET %s = %d", p, code)
		}
	}
}

// getJSON GETs path and decodes a JSON array or object into out.
func (x *tc) getJSON(path string, out any) int {
	x.t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", x.srv.URL+path, nil)
	resp, err := x.c.Do(req)
	if err != nil {
		x.t.Fatal(err)
	}
	defer resp.Body.Close()
	_ = json.NewDecoder(resp.Body).Decode(out)
	return resp.StatusCode
}

func TestEventsSystemAndRiskEndpoints(t *testing.T) {
	x, _ := newMarketTC(t)
	csrf := []string{"X-CSRF-Token", x.csrf}
	if code, _, _ := x.do("POST", "/api/v1/watchlist", `{"instrumentId":1}`, csrf...); code != 201 {
		t.Fatal("add")
	}
	code, m, _ := x.do("GET", "/api/v1/events?limit=5", "")
	evs := m["events"].([]any)
	if code != 200 || m["unread"] != 1.0 || len(evs) != 1 || evs[0].(map[string]any)["kind"] != "watchlist.added" || evs[0].(map[string]any)["severity"] != "info" {
		t.Fatalf("events %d %v", code, m)
	}
	if code, _, _ := x.do("POST", "/api/v1/events/seen", "", csrf...); code != 204 {
		t.Fatalf("seen %d", code)
	}
	if _, m, _ := x.do("GET", "/api/v1/events", ""); m["unread"] != 0.0 {
		t.Fatalf("after seen %v", m)
	}
	// reload and restart are not wired in this test server
	if code, _, _ := x.do("POST", "/api/v1/system/reload", "", csrf...); code != 503 {
		t.Fatalf("reload %d", code)
	}
	if code, _, _ := x.do("POST", "/api/v1/system/restart", "", csrf...); code != 503 {
		t.Fatalf("restart %d", code)
	}
	code, m, _ = x.do("GET", "/api/v1/risk/limits", "")
	if code != 200 || m["dailyLossBp"] != 200.0 || m["tradingBudgetCents"] != 0.0 || m["noEntryBeforeCloseMinutes"] != 30.0 {
		t.Fatalf("limits %d %v", code, m)
	}
	body := `{"tradingBudgetCents":50000,"dailyLossBp":150,"drawdownBp":1000,"maxPositionBp":2000,"maxOpenPositions":5,"noEntryBeforeCloseMinutes":30,"flattenBeforeCloseMinutes":10,"maxPortfolioAgeSeconds":5,"maxQuoteAgeSeconds":30}`
	if code, m, _ := x.do("PUT", "/api/v1/risk/limits", body, csrf...); code != 200 || m["maxOpenPositions"] != 5.0 {
		t.Fatalf("set limits %d %v", code, m)
	}
	if code, m, _ := x.do("PUT", "/api/v1/risk/limits", strings.Replace(body, `"maxOpenPositions":5`, `"maxOpenPositions":99`, 1), csrf...); code != 400 || m["field"] != "maxOpenPositions" {
		t.Fatalf("invalid limits %d %v", code, m)
	}
}
