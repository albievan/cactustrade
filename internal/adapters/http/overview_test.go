package httpapi

import (
	"log/slog"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

func TestOverviewEndpoints(t *testing.T) {
	st := fakes.NewStore()
	clk := &fakes.Clock{T: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	b := &fakes.Broker{Account: domain.Portfolio{AvailableCash: 10_000, AsOf: clk.T, Positions: []domain.Position{
		{ID: 7, Instrument: 1, OpenRate: 189_123_456, Units: 2_500_000, Amount: 47_280, UnrealisedPnL: -120, RealAsset: true, Long: true, Leverage: 1, OpenedAt: clk.T},
	}}, Known: map[domain.InstrumentID]domain.Instrument{1: {ID: 1, Symbol: "AAPL", Name: "Apple"}}}
	t212 := &fakes.Holdings{ByAccount: map[string]domain.Holdings{}}
	svc, err := app.New(app.Deps{
		Store: st, Secrets: &fakes.SecretStore{Now: clk.Now}, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: clk,
		Broker: b, Market: b, Holdings: map[domain.Platform]ports.Holdings{domain.PlatformTrading212: t212},
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
	csrf := []string{"X-CSRF-Token", x.csrf}

	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Main demo","mode":"demo"}`, csrf...)
	if code != 201 || m["platform"] != "etoro" {
		t.Fatalf("create eToro %d %v", code, m)
	}
	code, m, _ = x.do("POST", "/api/v1/accounts", `{"name":"ISA","mode":"real","platform":"trading212"}`, csrf...)
	if code != 201 || m["platform"] != "trading212" {
		t.Fatalf("create Trading 212 %d %v", code, m)
	}
	isa := m["id"].(string)
	if code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"X","mode":"demo","platform":"ibkr"}`, csrf...); code != 400 {
		t.Fatalf("unknown platform %d %v", code, m)
	}
	t212.ByAccount[isa] = domain.Holdings{
		Platform: domain.PlatformTrading212, Currency: "GBP", Cash: 100, Equity: 1_000, AsOf: clk.T,
		Positions: []domain.Holding{{Symbol: "AAPL_US_EQ", Quantity: 1_500_000, AvgPrice: 180_000_000, Value: 900, Long: true}},
	}

	var ov struct {
		Accounts []struct {
			Account  map[string]any `json:"account"`
			Holdings *struct {
				Currency  string           `json:"currency"`
				Equity    int64            `json:"equity"`
				Positions []map[string]any `json:"positions"`
				Summary   map[string]any   `json:"summary"`
			} `json:"holdings"`
			Error  string `json:"error"`
			NoKeys bool   `json:"noKeys"`
		} `json:"accounts"`
		Totals []map[string]any `json:"totals"`
	}
	if code := x.getJSON("/api/v1/overview", &ov); code != 200 || len(ov.Accounts) != 2 || len(ov.Totals) != 2 {
		t.Fatalf("overview %d %+v", code, ov)
	}
	for _, a := range ov.Accounts {
		switch a.Account["name"] {
		case "ISA":
			if a.Holdings == nil || a.Holdings.Currency != "GBP" || a.Holdings.Positions[0]["quantity"] != "1.5" || a.Holdings.Positions[0]["openedAt"] != nil ||
				a.Holdings.Summary["longs"] != 1.0 || a.Holdings.Summary["shorts"] != 0.0 || a.Holdings.Summary["longValue"] != 900.0 {
				t.Fatalf("ISA %+v", a)
			}
		case "Main demo":
			if a.Holdings == nil || a.Holdings.Currency != "USD" || a.Holdings.Positions[0]["symbol"] != "AAPL" || a.Holdings.Positions[0]["openedAt"] == nil {
				t.Fatalf("Main %+v", a)
			}
		}
	}
	if ov.Totals[0]["currency"] != "GBP" || ov.Totals[1]["currency"] != "USD" {
		t.Fatalf("totals %+v", ov.Totals)
	}
	// a Trading 212 account is refused by every trading path with a clear answer
	if code, m, _ := x.do("PUT", "/api/v1/accounts/current", `{"accountId":"`+isa+`"}`, csrf...); code != 403 || m["title"] != "View-only account" {
		t.Fatalf("switch to Trading 212 %d %v", code, m)
	}
	// keys: API key + secret
	if code, _, _ := x.do("PUT", "/api/v1/accounts/"+isa+"/keys", `{"apiKey":"k","userKey":"s"}`, csrf...); code != 204 {
		t.Fatalf("set keys %d", code)
	}
	delete(t212.ByAccount, isa) // the fake now reports "no keys" again
	ov.Accounts = nil
	if code := x.getJSON("/api/v1/overview", &ov); code != 200 {
		t.Fatalf("overview %d", code)
	}
	for _, a := range ov.Accounts {
		if a.Account["name"] == "ISA" && (!a.NoKeys || a.Error != "no keys yet" || a.Holdings != nil) {
			t.Fatalf("no keys %+v", a)
		}
	}

	svc.RecordEquity(t.Context())
	var series []map[string]any
	if code := x.getJSON("/api/v1/overview/history", &series); code != 200 || len(series) != 1 || series[0]["currency"] != "USD" ||
		series[0]["platform"] != "etoro" || len(series[0]["points"].([]any)) != 1 {
		t.Fatalf("history %d %v", code, series)
	}
	if code := x.getJSON("/api/v1/overview/history?days=400", &series); code != 200 {
		t.Fatalf("history 400 %d", code)
	}
	for _, bad := range []string{"0", "401", "-3"} {
		if code, _, _ := x.do("GET", "/api/v1/overview/history?days="+bad, ""); code != 400 {
			t.Fatalf("days=%s %d", bad, code)
		}
	}
}
