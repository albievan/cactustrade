package trading212

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// orderFixtures serves the order fixtures (and the read fixtures) by method and path, and
// records every body sent.
func orderFixtures(t *testing.T, e *env) *[]string {
	t.Helper()
	var bodies []string
	e.demo.route = func(r *http.Request, _ int) (int, string, http.Header) {
		name := ""
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v0/equity/orders/market":
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
			name = "order_market.json"
			if strings.Contains(string(b), `"quantity":-`) {
				name = "order_sell.json"
			}
		case r.Method == http.MethodPost && r.URL.Path == "/api/v0/equity/orders/stop":
			b, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(b))
			name = "order_stop.json"
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v0/equity/orders/1002":
			return 200, "", nil
		case r.Method == http.MethodDelete:
			return 404, "", nil
		case r.URL.Path == "/api/v0/equity/orders":
			name = "orders.json"
		case r.URL.Path == "/api/v0/equity/orders/1002":
			name = "order.json"
		case r.URL.Path == "/api/v0/equity/history/orders" && r.URL.Query().Get("cursor") != "":
			name = "history_orders_p2.json"
		case r.URL.Path == "/api/v0/equity/history/orders":
			name = "history_orders_p1.json"
		case r.URL.Path == "/api/v0/equity/metadata/instruments":
			name = "instruments.json"
		default:
			return 0, "", nil // the read fixtures
		}
		data, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatalf("fixture %s: %v", name, err)
		}
		return 200, string(data), nil
	}
	return &bodies
}

var (
	buyIn  = domain.T212Intent{RequestID: "r1", Ticker: "BARCl_EQ", Quantity: 3_000_000, Side: domain.T212Buy, Type: domain.T212Market}
	sellIn = domain.T212Intent{RequestID: "r2", Ticker: "ULVLl_EQ", Quantity: 4_000_000, Side: domain.T212Sell, Type: domain.T212Market}
	stopIn = domain.T212Intent{RequestID: "r3", Ticker: "ULVLl_EQ", Quantity: 4_000_000, Side: domain.T212Sell, Type: domain.T212StopOrder, StopPrice: 2_150_120_000}
)

// TestOrderShapesGolden: the three bodies, exactly.
func TestOrderShapesGolden(t *testing.T) {
	e := newEnv(t)
	bodies := orderFixtures(t, e)
	ctx := ctxFor(demoAcct)
	b, err := e.c.Place(ctx, buyIn)
	if err != nil || b.ID != 1001 || b.Side != domain.T212Buy || b.Type != domain.T212Market || b.Quantity != 3_000_000 || b.State != domain.OrderPending {
		t.Fatalf("buy = %+v %v", b, err)
	}
	s, err := e.c.Place(ctx, stopIn)
	if err != nil || s.ID != 1002 || s.Type != domain.T212StopOrder || s.Side != domain.T212Sell || s.StopPrice != 2_150_120_000 {
		t.Fatalf("stop = %+v %v", s, err)
	}
	want := []string{
		`{"ticker":"BARCl_EQ","quantity":3,"extendedHours":false}`,
		`{"ticker":"ULVLl_EQ","quantity":-4,"stopPrice":2150.12,"timeValidity":"GOOD_TILL_CANCEL"}`,
	}
	if !slices.Equal(*bodies, want) {
		t.Fatalf("bodies\n%v\nwant\n%v", *bodies, want)
	}
	// A sell with nothing resting.
	e2 := newEnv(t)
	bodies3 := orderFixtures(t, e2)
	e2.demo.route = withoutOrders(e2.demo.route)
	if _, err := e2.c.Place(ctx, sellIn); err != nil {
		t.Fatal(err)
	}
	if got := (*bodies3)[0]; got != `{"ticker":"ULVLl_EQ","quantity":-4,"extendedHours":false}` {
		t.Fatalf("sell body %s", got)
	}
	// Exact keys of each shape.
	for _, raw := range append(*bodies, *bodies3...) {
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		var keys []string
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		switch strings.Join(keys, ",") {
		case "extendedHours,quantity,ticker", "quantity,stopPrice,ticker,timeValidity":
		default:
			t.Fatalf("keys %v", keys)
		}
		if v, ok := m["extendedHours"]; ok && v != false {
			t.Fatal("extended hours")
		}
	}
}

// withoutOrders answers the active orders with an empty list.
func withoutOrders(next func(*http.Request, int) (int, string, http.Header)) func(*http.Request, int) (int, string, http.Header) {
	return func(r *http.Request, n int) (int, string, http.Header) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v0/equity/orders" {
			return 200, `[]`, nil
		}
		return next(r, n)
	}
}

func posts(f *fakeT212) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if r.Method != http.MethodGet {
			n++
		}
	}
	return n
}

// TestSellNeverExceedsHeld: a sell above what Trading 212 makes available, above what is held
// net of resting sells, or with the positions unreadable is refused with zero POSTs.
func TestSellNeverExceedsHeld(t *testing.T) {
	tests := map[string]struct {
		in    domain.T212Intent
		route func(next func(*http.Request, int) (int, string, http.Header)) func(*http.Request, int) (int, string, http.Header)
	}{
		"above available": {domain.T212Intent{Ticker: "ULVLl_EQ", Quantity: 11_000_000, Side: domain.T212Sell, Type: domain.T212Market}, withoutOrders},
		"not held":        {domain.T212Intent{Ticker: "BARCl_EQ", Quantity: 1_000_000, Side: domain.T212Sell, Type: domain.T212Market}, withoutOrders},
		"a stop rests": {domain.T212Intent{Ticker: "ULVLl_EQ", Quantity: 7_000_000, Side: domain.T212Sell, Type: domain.T212StopOrder, StopPrice: 1}, func(n func(*http.Request, int) (int, string, http.Header)) func(*http.Request, int) (int, string, http.Header) {
			return n
		}},
		"positions unreadable": {sellIn, func(next func(*http.Request, int) (int, string, http.Header)) func(*http.Request, int) (int, string, http.Header) {
			return func(r *http.Request, n int) (int, string, http.Header) {
				if r.URL.Path == "/api/v0/equity/positions" {
					return 401, "", nil
				}
				return next(r, n)
			}
		}},
		"a fraction while locked": {domain.T212Intent{Ticker: "ULVLl_EQ", Quantity: 1_500_000, Side: domain.T212Sell, Type: domain.T212Market}, withoutOrders},
		"a buy stop":              {domain.T212Intent{Ticker: "ULVLl_EQ", Quantity: 1_000_000, Side: domain.T212Buy, Type: domain.T212StopOrder, StopPrice: 1}, withoutOrders},
		"a limit order":           {domain.T212Intent{Ticker: "ULVLl_EQ", Quantity: 1_000_000, Side: domain.T212Sell, Type: "limit"}, withoutOrders},
		"no ticker":               {domain.T212Intent{Quantity: 1_000_000, Side: domain.T212Buy, Type: domain.T212Market}, withoutOrders},
		"no quantity":             {domain.T212Intent{Ticker: "X", Side: domain.T212Buy, Type: domain.T212Market}, withoutOrders},
	}
	for name, tt := range tests {
		e := newEnv(t)
		orderFixtures(t, e)
		e.demo.route = tt.route(e.demo.route)
		if _, err := e.c.Place(ctxFor(demoAcct), tt.in); !errors.Is(err, domain.ErrNotAccepted) {
			t.Errorf("%s: %v", name, err)
		}
		if posts(e.demo) != 0 {
			t.Errorf("%s: sent", name)
		}
	}
	// With the switch on (verified fractions), a fraction is allowed through to the checks.
	if q, err := quantity(1_500_000, true); err != nil || q != 1.5 {
		t.Fatalf("fraction switched on: %v %v", q, err)
	}
}

// TestMutationsNeverRetry: whatever goes wrong, one request, and an unknown outcome when it may
// have been processed.
func TestMutationsNeverRetry(t *testing.T) {
	tests := map[string]struct {
		status int
		body   string
		want   error
	}{
		"500":          {500, "", domain.ErrOutcomeUnknown},
		"503":          {503, "", domain.ErrOutcomeUnknown},
		"408":          {408, "", domain.ErrOutcomeUnknown},
		"429":          {429, "", domain.ErrOutcomeUnknown},
		"unreadable":   {200, "<html>", domain.ErrOutcomeUnknown},
		"no order id":  {200, `{"status":"NEW"}`, domain.ErrOutcomeUnknown},
		"400 refused":  {400, `{"code":"x"}`, domain.ErrNotAccepted},
		"403 no scope": {403, "", domain.ErrNotAccepted},
	}
	for name, tt := range tests {
		e := newEnv(t)
		e.demo.route = func(r *http.Request, _ int) (int, string, http.Header) {
			if r.Method == http.MethodPost {
				return tt.status, tt.body, nil
			}
			return 0, "", nil
		}
		_, err := e.c.Place(ctxFor(demoAcct), buyIn)
		if !errors.Is(err, tt.want) || posts(e.demo) != 1 {
			t.Errorf("%s: %v (%d posts)", name, err, posts(e.demo))
		}
	}
	// A connection reset or a timeout: unknown, one request.
	e := newEnv(t)
	e.demo.route = func(r *http.Request, _ int) (int, string, http.Header) {
		if r.Method == http.MethodPost {
			panic(http.ErrAbortHandler) // the server drops the connection
		}
		return 0, "", nil
	}
	if _, err := e.c.Place(ctxFor(demoAcct), buyIn); !errors.Is(err, domain.ErrOutcomeUnknown) || posts(e.demo) != 1 {
		t.Fatalf("reset: %v (%d posts)", err, posts(e.demo))
	}
	e = newEnv(t)
	e.c.cfg.RequestTimeout = 20 * time.Millisecond
	e.demo.route = func(r *http.Request, _ int) (int, string, http.Header) {
		if r.Method == http.MethodDelete {
			time.Sleep(200 * time.Millisecond)
		}
		return 200, "", nil
	}
	if err := e.c.Cancel(ctxFor(demoAcct), 7); !errors.Is(err, domain.ErrOutcomeUnknown) || posts(e.demo) != 1 {
		t.Fatalf("timeout: %v (%d)", err, posts(e.demo))
	}
}

// TestRealAccountNoMutation: a real Trading 212 account, whatever permit ctx carries, gets
// zero requests to either host from every T212Broker call.
func TestRealAccountNoMutation(t *testing.T) {
	e := newEnv(t)
	ctx := ports.WithPermit(ctxFor(liveAcct), domain.RealPermit{})
	calls := map[string]func() error{
		"Place":       func() error { _, err := e.c.Place(ctx, buyIn); return err },
		"Cancel":      func() error { return e.c.Cancel(ctx, 1) },
		"Account":     func() error { _, err := e.c.Account(ctx); return err },
		"Instruments": func() error { _, err := e.c.Instruments(ctx); return err },
		"OpenOrders":  func() error { _, err := e.c.OpenOrders(ctx); return err },
		"Order":       func() error { _, err := e.c.Order(ctx, 1); return err },
		"History":     func() error { _, err := e.c.History(ctx, "", ""); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, domain.ErrRealModeGated) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if e.demo.count()+e.live.count() != 0 {
		t.Fatalf("requests sent: demo %d live %d", e.demo.count(), e.live.count())
	}
	if g := e.sec.Gets; g != 0 {
		t.Fatalf("%d keys read for a real account (want none)", g)
	}
	// The mutation core refuses a real account too, before its keys.
	if err := e.c.mutate(ctxFor(liveAcct), request{op: OpCancelOrder, id: 1}, nil); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("mutate: %v", err)
	}
	if err := e.c.mutate(ctxFor(demoAcct), request{op: OpOrders}, nil); err == nil {
		t.Fatal("a read through mutate")
	}
	if err := e.c.read(ctxFor(demoAcct), request{op: OpCancelOrder}, nil); err == nil {
		t.Fatal("a change through read")
	}
}

// TestRateWaitNotSent: a change whose rate-limit wait is too long is not sent.
func TestRateWaitNotSent(t *testing.T) {
	e := newEnv(t)
	orderFixtures(t, e)
	e.c.limiter(demoAcct.ID).Block(OpMarketOrder, e.now.Add(time.Minute))
	_, err := e.c.Place(ctxFor(demoAcct), buyIn)
	na, ok := errors.AsType[*domain.NotAcceptedError](err)
	if !ok || !na.RateLimited || posts(e.demo) != 0 {
		t.Fatalf("%v (%d posts)", err, posts(e.demo))
	}
	// A short wait is waited for (one cancel every 1.2 s).
	e = newEnv(t)
	orderFixtures(t, e)
	for range 2 {
		if err := e.c.Cancel(ctxFor(demoAcct), 1002); err != nil {
			t.Fatal(err)
		}
	}
	if posts(e.demo) != 2 || !slices.Equal(e.slept, []time.Duration{1200 * time.Millisecond}) {
		t.Fatalf("posts %d slept %v", posts(e.demo), e.slept)
	}
}

func TestCancel(t *testing.T) {
	e := newEnv(t)
	orderFixtures(t, e)
	if err := e.c.Cancel(ctxFor(demoAcct), 1002); err != nil {
		t.Fatal(err)
	}
	if err := e.c.Cancel(ctxFor(demoAcct), 999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	if err := e.c.Cancel(ctxFor(demoAcct), 0); !errors.Is(err, domain.ErrNotAccepted) {
		t.Fatalf("no id: %v", err)
	}
	if r := e.demo.reqs[0]; r.Method != http.MethodDelete || r.URL.Path != "/api/v0/equity/orders/1002" {
		t.Fatalf("%s %s", r.Method, r.URL.Path)
	}
	if e.demo.count() != 2 {
		t.Fatalf("%d requests", e.demo.count())
	}
}

func TestReadsFromFixtures(t *testing.T) {
	e := newEnv(t)
	orderFixtures(t, e)
	ctx := ctxFor(demoAcct)
	a, err := e.c.Account(ctx)
	if err != nil || a.Currency != "GBP" || a.AvailableToTrade != 123457 || a.ReservedForOrders != 1050 || a.Equity != 384547 || len(a.Positions) != 2 {
		t.Fatalf("account %+v %v", a, err)
	}
	if p := a.Positions[1]; p.Ticker != "ULVLl_EQ" || p.Quantity != 10_000_000 || p.QuantityAvailable != 10_000_000 || p.Cost != 22250 || p.AvgPrice != 2_225_000_000 {
		t.Fatalf("position %+v", p)
	}
	ins, err := e.c.Instruments(ctx)
	if err != nil || len(ins) != 3 || ins[0].Ticker != "BARCl_EQ" || ins[0].ShortName != "BARC" || ins[0].Currency != "GBX" || ins[0].Type != "STOCK" ||
		ins[0].MaxOpenQuantity != 1_000_000_000_000 || !ins[0].FetchedAt.Equal(e.now) {
		t.Fatalf("instruments %+v %v", ins, err)
	}
	os, err := e.c.OpenOrders(ctx)
	if err != nil || len(os) != 1 || os[0].ID != 1002 || os[0].Side != domain.T212Sell || os[0].Type != domain.T212StopOrder || os[0].Quantity != 4_000_000 ||
		os[0].InitiatedFrom != "API" || os[0].State != domain.OrderPending || os[0].ISIN != "GB00B10RZP78" {
		t.Fatalf("orders %+v %v", os, err)
	}
	o, err := e.c.Order(ctx, 1002)
	if err != nil || o.ID != 1002 || o.StopPrice != 2_150_120_000 {
		t.Fatalf("order %+v %v", o, err)
	}
	if _, err := e.c.Order(ctx, 77); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown order: %v", err)
	}
	if _, err := e.c.Order(ctx, 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("no id: %v", err)
	}
	p1, err := e.c.History(ctx, "BARCl_EQ", "")
	if err != nil || len(p1.Orders) != 2 || p1.Next == "" {
		t.Fatalf("history p1 %+v %v", p1, err)
	}
	f := p1.Orders[0]
	if f.ID != 1001 || f.State != domain.OrderFilled || !f.Final || f.FilledQuantity != 3_000_000 || f.FillPrice != 200_500_000 ||
		f.FilledAt.IsZero() || len(f.Taxes) != 2 || f.Taxes[0].Name != "STAMP_DUTY" {
		t.Fatalf("fill %+v", f)
	}
	q := e.demo.reqs[len(e.demo.reqs)-1].URL.Query()
	if q.Get("ticker") != "BARCl_EQ" || q.Get("limit") != "50" {
		t.Fatalf("query %v", q)
	}
	p2, err := e.c.History(ctx, "BARCl_EQ", p1.Next)
	if err != nil || len(p2.Orders) != 1 || p2.Next != "" || p2.Orders[0].State != domain.OrderRejected {
		t.Fatalf("history p2 %+v %v", p2, err)
	}
	for _, bad := range []string{"/api/v0/equity/orders?cursor=1", "https://evil.example/api/v0/equity/history/orders?cursor=1", "/api/v0/equity/history/ordersX", "/api/v0/equity/history/orders?%zz"} {
		if _, err := e.c.History(ctx, "", bad); !errors.Is(err, domain.ErrUnavailable) {
			t.Errorf("next %q: %v", bad, err)
		}
	}
	if strings.Contains(e.logs.String(), "secret-t-demo") || strings.Contains(e.logs.String(), "key-t-demo") {
		t.Fatal("a key reached the log")
	}
}

func TestHistoryRefusesAForeignNextPage(t *testing.T) {
	e := newEnv(t)
	e.demo.route = func(r *http.Request, _ int) (int, string, http.Header) {
		return 200, `{"items":[],"nextPagePath":"/api/v0/equity/pies?cursor=2"}`, nil
	}
	if _, err := e.c.History(ctxFor(demoAcct), "", ""); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

func TestToOrderSideFromQuantity(t *testing.T) {
	var cv conv
	neg, pos := -2.0, 2.0
	if o := cv.toOrder(orderWire{ID: 1, Quantity: &neg, Type: "MARKET", Status: "FILLED"}); o.Side != domain.T212Sell || o.Quantity != 2_000_000 || !o.Final {
		t.Fatalf("%+v", o)
	}
	if o := cv.toOrder(orderWire{ID: 1, Quantity: &pos, Type: "LIMIT", Status: "REPLACED"}); o.Side != domain.T212Buy || o.Type != "limit" || o.State != domain.OrderUnknown {
		t.Fatalf("%+v", o)
	}
	if o := cv.toOrder(orderWire{ID: 1}); o.Side != "" {
		t.Fatalf("%+v", o)
	}
}

// TestAccountAsOfIsTheCashRead (review B4): the account's AsOf is when the cash was read, not
// when the positions read (which may wait for its rate limit) ended.
func TestAccountAsOfIsTheCashRead(t *testing.T) {
	e := newEnv(t)
	orderFixtures(t, e)
	start := e.now
	e.c.limiter(demoAcct.ID).Block(OpPositions, e.now.Add(10*time.Second))
	a, err := e.c.Account(ctxFor(demoAcct))
	if err != nil {
		t.Fatal(err)
	}
	if !a.AsOf.Equal(start) || !e.now.After(start) {
		t.Fatalf("AsOf %s, the cash was read at %s (clock now %s)", a.AsOf, start, e.now)
	}
}

// TestRecordAnswerReadsOnly (R45.10): the probe's hook sees every read's answer and never a
// change's.
func TestRecordAnswerReadsOnly(t *testing.T) {
	e := newEnv(t)
	orderFixtures(t, e)
	var ops []Op
	e.c.cfg.RecordAnswer = func(op Op, body []byte) {
		if len(body) == 0 {
			t.Errorf("%s: empty body", op)
		}
		ops = append(ops, op)
	}
	ctx := ctxFor(demoAcct)
	if _, err := e.c.Account(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.Place(ctx, buyIn); err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0] != OpAccountSummary || ops[1] != OpPositions {
		t.Fatalf("recorded %v", ops)
	}
}
