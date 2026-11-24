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
		Broker: b, Market: b, Calendar: lseOnly{}, Research: b,
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
	if code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Main demo","mode":"demo"}`, "X-CSRF-Token", x.csrf); code != 201 || m["access"] != "owner" {
		t.Fatalf("create account %d %v", code, m)
	}
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
	if code := x.getJSON("/api/v1/market/status", &sessions); code != 200 || len(sessions) != 3 || sessions[2]["market"] != "ASX" || sessions[0]["open"] != true || sessions[1]["trading"] != false {
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
	_, accts, _ := x.do("GET", "/api/v1/system/status", "")
	acctID := accts["account"].(map[string]any)["id"].(string)
	if code, m, _ := x.do("PUT", "/api/v1/accounts/"+acctID+"/shares", `{"username":"vera","access":"trade"}`, "X-CSRF-Token", x.csrf); code != 204 {
		t.Fatalf("share %d %v", code, m)
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
	if code != 200 || m["unread"] != 2.0 || len(evs) != 2 || evs[0].(map[string]any)["kind"] != "watchlist.added" || evs[1].(map[string]any)["kind"] != "account.created" {
		t.Fatalf("events %d %v", code, m)
	}
	// each event names its account (R26: the dashboard's activity log keeps the current one's)
	if id, ok := evs[0].(map[string]any)["accountId"].(string); !ok || id == "" {
		t.Fatalf("event without its account: %v", evs[0])
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
	body := `{"tradingBudgetCents":50000,"dailyLossBp":150,"drawdownBp":1000,"maxPositionBp":2000,"maxOpenPositions":5,"noEntryBeforeCloseMinutes":30,"flattenBeforeCloseMinutes":10,"maxPortfolioAgeSeconds":5,"maxQuoteAgeSeconds":30,"maxShortBp":2500}`
	if code, m, _ := x.do("PUT", "/api/v1/risk/limits", body, csrf...); code != 200 || m["maxOpenPositions"] != 5.0 {
		t.Fatalf("set limits %d %v", code, m)
	}
	if code, m, _ := x.do("PUT", "/api/v1/risk/limits", strings.Replace(body, `"maxOpenPositions":5`, `"maxOpenPositions":99`, 1), csrf...); code != 400 || m["field"] != "maxOpenPositions" {
		t.Fatalf("invalid limits %d %v", code, m)
	}
}

func TestLlmEndpoints(t *testing.T) {
	x, _ := newMarketTC(t)
	csrf := []string{"X-CSRF-Token", x.csrf}
	code, m, _ := x.do("GET", "/api/v1/llm", "")
	if code != 200 || m["defaultAnthropicModel"] != "claude-opus-5-5" || len(m["providers"].([]any)) != 9 || len(m["profiles"].([]any)) != 0 {
		t.Fatalf("setup %d %v", code, m)
	}
	body := `{"name":"GPT","provider":"openai","model":"m","baseUrl":"","timeoutSeconds":30,"apiKey":"sk-secret"}`
	code, m, _ = x.do("POST", "/api/v1/llm/profiles", body, csrf...)
	if code != 201 || m["provider"] != "openai" || m["keySet"] != true || m["apiKey"] != nil {
		t.Fatalf("create %d %v", code, m)
	}
	id := m["id"].(string)
	if code, m, _ := x.do("POST", "/api/v1/llm/profiles", `{"name":"x","provider":"openai","model":"","baseUrl":"","timeoutSeconds":0}`, csrf...); code != 400 || m["field"] != "model" {
		t.Fatalf("invalid %d %v", code, m)
	}
	if code, m, _ := x.do("PUT", "/api/v1/llm/profiles/"+id, `{"name":"GPT 2","provider":"openai","model":"m2","baseUrl":"","timeoutSeconds":30}`, csrf...); code != 200 || m["name"] != "GPT 2" || m["keySet"] != true {
		t.Fatalf("update %d %v", code, m)
	}
	if code, m, _ := x.do("PUT", "/api/v1/llm", `{"enabled":true,"activeProfileId":"`+id+`"}`, csrf...); code != 200 || m["settings"].(map[string]any)["enabled"] != true {
		t.Fatalf("switch on %d %v", code, m)
	}
	// no reviewer adapter in this test server
	if code, _, _ := x.do("POST", "/api/v1/llm/profiles/"+id+"/test", "", csrf...); code != 503 {
		t.Fatalf("test %d", code)
	}
	if code, _, _ := x.do("POST", "/api/v1/llm/models", `{"provider":"openai","baseUrl":"","profileId":"`+id+`"}`, csrf...); code != 503 {
		t.Fatalf("models %d", code)
	}
	if code, _, _ := x.do("DELETE", "/api/v1/llm/profiles/"+id, "", csrf...); code != 204 {
		t.Fatalf("delete %d", code)
	}
	if code, m, _ := x.do("PUT", "/api/v1/llm/profiles/"+id, `{"name":"x","provider":"openai","model":"m","baseUrl":"","timeoutSeconds":0}`, csrf...); code != 404 {
		t.Fatalf("update deleted %d %v", code, m)
	}
	for _, a := range x.store.AuditEntries() {
		if strings.Contains(a.Details, "sk-secret") {
			t.Fatal("key in audit")
		}
	}
}

func TestAccountEndpoints(t *testing.T) {
	x, b := newMarketTC(t)
	csrf := []string{"X-CSRF-Token", x.csrf}
	var accts []map[string]any
	if code := x.getJSON("/api/v1/accounts", &accts); code != 200 || len(accts) != 1 || accts[0]["access"] != "owner" || accts[0]["ownerName"] != "admin" {
		t.Fatalf("list %d %v", code, accts)
	}
	main := accts[0]["id"].(string)
	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Real one","mode":"real"}`, csrf...)
	if code != 201 || m["mode"] != "real" {
		t.Fatalf("create real %d %v", code, m)
	}
	realID := m["id"].(string)
	if code, m, _ := x.do("PUT", "/api/v1/accounts/current", `{"accountId":"`+realID+`"}`, csrf...); code != 200 || m["id"] != realID {
		t.Fatalf("switch %d %v", code, m)
	}
	if _, m, _ := x.do("GET", "/api/v1/system/status", ""); m["mode"] != "real" || m["account"].(map[string]any)["name"] != "Real one" {
		t.Fatalf("status after switching to a real account %v", m)
	}
	if code, m, _ := x.do("PATCH", "/api/v1/accounts/"+realID, `{"name":"Real ISA"}`, csrf...); code != 200 || m["name"] != "Real ISA" {
		t.Fatalf("rename %d %v", code, m)
	}
	if code, _, _ := x.do("PUT", "/api/v1/accounts/"+realID+"/keys", `{"apiKey":"a-key","userKey":"u-key"}`, csrf...); code != 204 {
		t.Fatalf("keys %d", code)
	}
	if code, m, _ := x.do("GET", "/api/v1/accounts/"+realID+"/keys", ""); code != 200 || m["apiKeySet"] != true || m["userKeySet"] != true || m["fromEnvironment"] != false {
		t.Fatalf("keys status %d %v", code, m)
	}
	b.SeeAccount = true
	if code, m, _ := x.do("POST", "/api/v1/accounts/"+realID+"/test", "", csrf...); code != 200 || m["ok"] != true || m["mode"] != "real" || b.LastAccount.ID != realID {
		t.Fatalf("test %d %v (broker saw %+v)", code, m, b.LastAccount)
	}
	if _, err := x.svc.CreateUser(context.Background(), app.Principal{UserID: "x", Role: domain.RoleAdmin, Via: app.ViaSession, Username: "admin"}, "bob", "bob password 12", domain.RoleOperator); err != nil {
		t.Fatal(err)
	}
	if code, m, _ := x.do("PUT", "/api/v1/accounts/"+realID+"/shares", `{"username":"bob","access":"view"}`, csrf...); code != 204 {
		t.Fatalf("share %d %v", code, m)
	}
	var shares []map[string]any
	if code := x.getJSON("/api/v1/accounts/"+realID+"/shares", &shares); code != 200 || len(shares) != 1 || shares[0]["username"] != "bob" {
		t.Fatalf("shares %d %v", code, shares)
	}
	if code, m, _ := x.do("PUT", "/api/v1/accounts/"+realID+"/shares", `{"username":"bob","access":"owner"}`, csrf...); code != 400 {
		t.Fatalf("share as owner %d %v", code, m)
	}
	if code, _, _ := x.do("DELETE", "/api/v1/accounts/"+realID+"/shares/"+shares[0]["userId"].(string), "", csrf...); code != 204 {
		t.Fatalf("unshare %d", code)
	}
	if code, _, _ := x.do("DELETE", "/api/v1/accounts/"+realID, "", csrf...); code != 204 {
		t.Fatalf("delete %d", code)
	}
	if code, _, _ := x.do("DELETE", "/api/v1/accounts/"+realID, "", csrf...); code != 404 {
		t.Fatalf("delete again %d", code)
	}
	if _, m, _ := x.do("GET", "/api/v1/system/status", ""); m["account"].(map[string]any)["id"] != main {
		t.Fatalf("current account falls back to the remaining one %v", m)
	}
	if code, _, _ := x.do("PUT", "/api/v1/accounts/current", `{"accountId":"nope"}`, csrf...); code != 404 {
		t.Fatalf("switch to unknown %d", code)
	}
}

func TestResearchEndpoints(t *testing.T) {
	x, b := newMarketTC(t)
	csrf := []string{"X-CSRF-Token", x.csrf}
	b.Inds = []domain.Industry{{ID: 9, Name: "Technology"}}
	var inds []map[string]any
	if code := x.getJSON("/api/v1/industries", &inds); code != 200 || len(inds) != 1 || inds[0]["name"] != "Technology" {
		t.Fatalf("industries %d %v", code, inds)
	}
	code, m, _ := x.do("POST", "/api/v1/themes", `{"name":"Space","keywords":["rocket"],"industryId":0,"enabled":true,"instruments":[1]}`, csrf...)
	if code != 201 || m["name"] != "Space" || len(m["instruments"].([]any)) != 1 {
		t.Fatalf("create theme %d %v", code, m)
	}
	id := m["id"].(string)
	if code, m, _ := x.do("POST", "/api/v1/themes", `{"name":"Empty","keywords":[],"industryId":0,"enabled":true,"instruments":[]}`, csrf...); code != 400 {
		t.Fatalf("empty theme %d %v", code, m)
	}
	if code, m, _ := x.do("PUT", "/api/v1/themes/"+id, `{"name":"Space","keywords":["rocket","satellite"],"industryId":9,"enabled":false,"instruments":[]}`, csrf...); code != 200 || m["enabled"] != false || m["industryId"] != 9.0 {
		t.Fatalf("update theme %d %v", code, m)
	}
	var themes []map[string]any
	if code := x.getJSON("/api/v1/themes", &themes); code != 200 || len(themes) != 1 {
		t.Fatalf("themes %d %v", code, themes)
	}
	b.Bars = map[domain.InstrumentID][]domain.Candle{}
	if code, m, _ := x.do("POST", "/api/v1/scanner/run", "", csrf...); code != 200 || m["added"] == nil {
		t.Fatalf("run %d %v", code, m)
	}
	code, m, _ = x.do("GET", "/api/v1/reviews?limit=10", "")
	if code != 200 || m["scannerEnabled"] != true {
		t.Fatalf("reviews %d %v", code, m)
	}
	if code, _, _ := x.do("PUT", "/api/v1/scanner", `{"enabled":false}`, csrf...); code != 204 {
		t.Fatalf("switch %d", code)
	}
	if _, m, _ := x.do("GET", "/api/v1/reviews", ""); m["scannerEnabled"] != false {
		t.Fatalf("after switch %v", m)
	}
	if code, _, _ := x.do("DELETE", "/api/v1/themes/"+id, "", csrf...); code != 204 {
		t.Fatalf("delete %d", code)
	}
}
