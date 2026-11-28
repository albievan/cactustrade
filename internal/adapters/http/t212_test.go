package httpapi

import (
	"context"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// TestT212Endpoints (R45.9): the Trading 212 practice account's setup, watchlist match,
// agent panel and order resolution through the API. Every mutation needs CSRF in a web
// session; an API key is refused (403) on the setup, the confirmation and the resolution;
// a change while the kill switch is on is 409; invalid input is 422 (400 on the limits, as
// every limits change).
func TestT212Endpoints(t *testing.T) {
	sim := &fakes.T212Sim{Currency: "GBP", Cash: 500_000, Catalogue: []domain.T212Instrument{
		{Ticker: "BARCl_EQ", ShortName: "BARC", Name: "Barclays", Currency: "GBX", Type: "STOCK", ISIN: "GB0031348658", MaxOpenQuantity: 1e12},
	}}
	var clk ports.Clock
	x := newAgentTCWith(t, func(d *app.Deps) {
		d.T212 = sim
		clk = d.Clock
		sim.Now = d.Clock.Now
		d.Market = &fakes.Broker{Known: map[domain.InstrumentID]domain.Instrument{
			1: {ID: 1, Symbol: "BARC.L", Name: "Barclays", Type: "Stocks", Market: domain.MarketLSE, Currency: "GBX"},
		}}
	})
	csrf := x.csrfHdr()
	code, m, _ := x.do("POST", "/api/v1/accounts", `{"name":"Practice","mode":"demo","platform":"trading212"}`, csrf...)
	if code != 201 || m["agentTradable"] != true {
		t.Fatalf("create %d %v", code, m)
	}
	pr := m["id"].(string)
	x.keys(t, pr)
	code, m, _ = x.do("POST", "/api/v1/accounts", `{"name":"ISA","mode":"real","platform":"trading212"}`, csrf...)
	if code != 201 || m["agentTradable"] != false {
		t.Fatalf("create real %d %v", code, m)
	}
	code, m, _ = x.do("POST", "/api/v1/api-keys", `{"name":"ops","role":"admin","expiresInDays":30}`, csrf...)
	if code != 201 {
		t.Fatalf("api key %d %v", code, m)
	}
	key := m["secret"].(string)

	// The price source: CSRF, validation (422), an API key (403), then set.
	ps := "/api/v1/accounts/" + pr + "/price-source"
	if code, _, _ := x.do("PUT", ps, `{"accountId":"`+x.acct+`"}`); code != 403 {
		t.Fatalf("price source without CSRF %d", code)
	}
	if code, m, _ := x.do("PUT", ps, `{"accountId":"`+pr+`"}`, csrf...); code != 422 {
		t.Fatalf("a Trading 212 price source %d %v", code, m)
	}
	if code, body := x.bearer("PUT", ps, `{"accountId":"`+x.acct+`"}`, key); code != 403 {
		t.Fatalf("price source with a key %d %s", code, body)
	}
	if code, m, _ := x.do("PUT", ps, `{"accountId":"`+x.acct+`"}`, csrf...); code != 204 {
		t.Fatalf("price source %d %v", code, m)
	}

	// The catalogue: stale until refreshed; the refresh is the owner's, in the web UI, once per 50 s.
	cat := "/api/v1/accounts/" + pr + "/t212/instruments"
	if code, m, _ := x.do("GET", cat, ""); code != 200 || m["stale"] != true {
		t.Fatalf("catalogue %d %v", code, m)
	}
	if code, body := x.bearer("POST", cat+"/refresh", "", key); code != 403 {
		t.Fatalf("refresh with a key %d %s", code, body)
	}
	if code, m, _ := x.do("POST", cat+"/refresh", "", csrf...); code != 200 || m["stale"] != false || m["count"] != float64(1) {
		t.Fatalf("refresh %d %v", code, m)
	}
	if code, _, _ := x.do("POST", cat+"/refresh", "", csrf...); code != 429 {
		t.Fatalf("second refresh %d", code)
	}
	if code, _, _ := x.do("GET", "/api/v1/accounts/"+x.acct+"/t212/instruments", ""); code != 422 {
		t.Fatalf("catalogue of an eToro account %d", code)
	}

	// The account list names the price source and (once read) the currency.
	var accounts []map[string]any
	if code := x.getJSON("/api/v1/accounts", &accounts); code != 200 {
		t.Fatalf("accounts %d", code)
	}
	for _, a := range accounts {
		switch a["id"] {
		case pr:
			if a["priceSourceAccountId"] != x.acct || a["platform"] != "trading212" {
				t.Fatalf("practice %v", a)
			}
		case x.acct:
			if a["currency"] != "USD" || a["agentTradable"] != true {
				t.Fatalf("eToro %v", a)
			}
		}
	}

	// The watchlist of the practice account: added unconfirmed, confirmed by the owner.
	if code, m, _ := x.do("PUT", "/api/v1/accounts/current", `{"accountId":"`+pr+`"}`, csrf...); code != 200 {
		t.Fatalf("current %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/watchlist", `{"instrumentId":1}`, csrf...); code != 201 {
		t.Fatalf("add %d %v", code, m)
	}
	var items []map[string]any
	if code := x.getJSON("/api/v1/watchlist", &items); code != 200 || len(items) != 1 {
		t.Fatalf("watchlist %d %v", code, items)
	}
	if bm, _ := items[0]["brokerMatch"].(map[string]any); bm["status"] != "unconfirmed" || bm["ticker"] != "BARCl_EQ" || bm["isin"] != "GB0031348658" {
		t.Fatalf("match %v", items[0])
	}
	if code, _, _ := x.do("POST", "/api/v1/watchlist/1/match/confirm", ""); code != 403 {
		t.Fatalf("confirm without CSRF %d", code)
	}
	if code, body := x.bearer("POST", "/api/v1/watchlist/1/match/confirm", "", key); code != 403 {
		t.Fatalf("confirm with a key %d %s", code, body)
	}
	if code, m, _ := x.do("POST", "/api/v1/watchlist/1/match/confirm", "", csrf...); code != 200 || m["status"] != "confirmed" {
		t.Fatalf("confirm %d %v", code, m)
	}

	// The limits: Trading 212 fields; the fractional switch refused while unverified.
	limits := `{"tradingBudgetCents":100000,"dailyLossBp":200,"drawdownBp":1000,"maxPositionBp":2000,"maxOpenPositions":5,` +
		`"noEntryBeforeCloseMinutes":30,"flattenBeforeCloseMinutes":10,"maxPortfolioAgeSeconds":5,"maxQuoteAgeSeconds":5,"maxShortBp":0,"minOrderValueCents":1000`
	if code, m, _ := x.do("PUT", "/api/v1/risk/limits", limits+`,"t212Fractional":true}`, csrf...); code != 400 || m["field"] != "t212Fractional" {
		t.Fatalf("fractional %d %v", code, m)
	}
	if code, m, _ := x.do("PUT", "/api/v1/risk/limits", limits+`}`, csrf...); code != 200 || m["minOrderValueCents"] != float64(1000) ||
		m["currency"] != "GBP" || m["platform"] != "trading212" || m["fractionsVerified"] != false {
		t.Fatalf("limits %d %v", code, m)
	}

	// The agent panel of the practice account, in its currency; a real one is view-only.
	if code, m, _ := x.do("GET", "/api/v1/agent?accountId="+pr, ""); code != 200 || m["currency"] != "GBP" || m["equityKnown"] != true {
		t.Fatalf("panel %d %v", code, m)
	}

	// Resolving an order: only a Trading 212 order whose outcome is unknown, by its owner.
	ctx := context.Background()
	acct, err := x.store.Accounts().Get(ctx, pr)
	if err != nil {
		t.Fatal(err)
	}

	o, err := domain.NewOrder(domain.Order{
		RequestID: "lost-1", AccountID: acct.ID, Mode: domain.ModeDemo, Kind: domain.OrderOpen, Platform: domain.PlatformTrading212,
		Ticker: "BARCl_EQ", Quantity: 3_000_000, Direction: domain.Long, Instrument: 1, Amount: 700, Entry: 200_000_000, StopLoss: 194_000_000,
	}, clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := x.store.Orders().Create(ctx, o); err != nil {
		t.Fatal(err)
	}
	sent, _ := o.Transition(domain.OrderSent, clk.Now(), "")
	_ = x.store.Orders().Update(ctx, o, sent)
	lost, _ := sent.Transition(domain.OrderUnknown, clk.Now(), "lost")
	_ = x.store.Orders().Update(ctx, sent, lost)
	var orders []map[string]any
	if code := x.getJSON("/api/v1/orders?accountId="+pr, &orders); code != 200 || len(orders) != 1 ||
		orders[0]["resolvable"] != true || orders[0]["platform"] != "trading212" || orders[0]["ticker"] != "BARCl_EQ" || orders[0]["quantity"] != "3" ||
		orders[0]["kindLabel"] != "Buy BARC.L (BARCl_EQ)" {
		t.Fatalf("orders %d %v", code, orders)
	}
	res := "/api/v1/orders/lost-1/resolve"
	if code, _, _ := x.do("POST", res, `{"notPlaced":true}`); code != 403 {
		t.Fatalf("resolve without CSRF %d", code)
	}
	if code, body := x.bearer("POST", res, `{"notPlaced":true}`, key); code != 403 {
		t.Fatalf("resolve with a key %d %s", code, body)
	}
	if code, m, _ := x.do("POST", res, `{}`, csrf...); code != 422 {
		t.Fatalf("resolve with nothing %d %v", code, m)
	}
	if code, m, _ := x.do("POST", "/api/v1/orders/nope/resolve", `{"notPlaced":true}`, csrf...); code != 404 {
		t.Fatalf("resolve unknown %d %v", code, m)
	}
	if code, m, _ := x.do("POST", res, `{"notPlaced":true}`, csrf...); code != 200 || m["state"] != "abandoned" || m["resolvable"] != false {
		t.Fatalf("resolve %d %v", code, m)
	}
	if code, m, _ := x.do("POST", res, `{"notPlaced":true}`, csrf...); code != 409 {
		t.Fatalf("resolve again %d %v", code, m)
	}

	// Settling a position Trading 212 does not list: owner, web session, a buy that opened one
	// (re-review 2026-11-28).
	set := "/api/v1/orders/lost-1/settle"
	if code, _, _ := x.do("POST", set, ``); code != 403 {
		t.Fatalf("settle without CSRF %d", code)
	}
	if code, body := x.bearer("POST", set, ``, key); code != 403 {
		t.Fatalf("settle with a key %d %s", code, body)
	}
	if code, m, _ := x.do("POST", "/api/v1/orders/nope/settle", ``, csrf...); code != 404 {
		t.Fatalf("settle unknown %d %v", code, m)
	}
	if code, m, _ := x.do("POST", set, ``, csrf...); code != 409 {
		t.Fatalf("settle an order that opened no position %d %v", code, m)
	}
	if code := x.getJSON("/api/v1/orders?accountId="+pr, &orders); code != 200 || orders[0]["settleable"] != nil {
		t.Fatalf("orders %d %v", code, orders)
	}

	// The kill switch on: the price source cannot change (409).
	if code, m, _ := x.do("POST", "/api/v1/agent/kill?accountId="+pr, `{"reason":"drill"}`, csrf...); code != 200 {
		t.Fatalf("kill %d %v", code, m)
	}
	if code, m, _ := x.do("PUT", ps, `{"accountId":""}`, csrf...); code != 409 || !strings.Contains(m["detail"].(string), "kill") {
		t.Fatalf("price source while killed %d %v", code, m)
	}
}
