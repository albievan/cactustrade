package etoro

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// testRequestID is the synthetic request id the open.json fixture echoes as referenceId.
const testRequestID = "8a3e2f4c-1b6d-4c3e-9f2a-7d5e6b4c3a21"

func longIntent() domain.OrderIntent {
	return domain.OrderIntent{RequestID: testRequestID, Direction: domain.Long, Instrument: 1001, Amount: 500_00, StopLossRate: 185_000_000, TakeProfitRate: 199_500_000}
}

func shortIntent() domain.OrderIntent {
	return domain.OrderIntent{RequestID: testRequestID, Direction: domain.Short, Instrument: 2002, Amount: 120_50, StopLossRate: 52_250_000}
}

// openKeys are the only keys an open-order body may have (takeProfitRate only with a target).
var openKeys = []string{"action", "amount", "instrumentId", "leverage", "orderCurrency", "orderType", "settlementType", "stopLossRate", "stopLossType", "takeProfitRate", "transaction"}

// TestOpenGoldenBodies: exactly two order shapes exist, byte for byte.
func TestOpenGoldenBodies(t *testing.T) {
	tests := []struct {
		name   string
		intent domain.OrderIntent
		golden string
	}{
		{
			"long = buy + real, x1", longIntent(),
			`{"action":"open","transaction":"buy","instrumentId":1001,"settlementType":"real","orderType":"mkt","leverage":1,"amount":500,"orderCurrency":"usd","stopLossRate":185,"takeProfitRate":199.5,"stopLossType":"fixed"}`,
		},
		{
			"short = sellShort + cfd, x1", shortIntent(),
			`{"action":"open","transaction":"sellShort","instrumentId":2002,"settlementType":"cfd","orderType":"mkt","leverage":1,"amount":120.5,"orderCurrency":"usd","stopLossRate":52.25,"stopLossType":"fixed"}`,
		},
		{
			"short with a target", func() domain.OrderIntent {
				in := shortIntent()
				in.TakeProfitRate = 45_123_456
				in.Amount = 1
				return in
			}(),
			`{"action":"open","transaction":"sellShort","instrumentId":2002,"settlementType":"cfd","orderType":"mkt","leverage":1,"amount":0.01,"orderCurrency":"usd","stopLossRate":52.25,"takeProfitRate":45.123456,"stopLossType":"fixed"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			st, err := e.c.Open(e.ctx(), tt.intent)
			want := domain.OrderStatus{RequestID: testRequestID, OrderID: 700001, State: domain.OrderPending, Instrument: tt.intent.Instrument}
			if err != nil || !reflect.DeepEqual(st, want) {
				t.Fatalf("Open = %+v %v", st, err)
			}
			if len(e.fake.reqs) != 1 {
				t.Fatalf("%d requests", len(e.fake.reqs))
			}
			r, body := e.fake.reqs[0], e.fake.bodies[0]
			if body != tt.golden {
				t.Fatalf("body\n got %s\nwant %s", body, tt.golden)
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(body), &m); err != nil {
				t.Fatal(err)
			}
			for k := range m {
				if !slices.Contains(openKeys, k) {
					t.Errorf("unexpected key %q", k)
				}
			}
			if m["leverage"] != float64(1) {
				t.Error("leverage must be 1")
			}
			if r.Method != http.MethodPost || r.URL.Path != "/api/v2/trading/execution/demo/orders" || r.URL.RawQuery != "" {
				t.Fatalf("request %s %s", r.Method, r.URL)
			}
			if r.Header.Get("x-request-id") != tt.intent.RequestID || r.Header.Get("Content-Type") != "application/json" ||
				r.Header.Get("x-api-key") != "demo-api" || r.Header.Get("x-user-key") != "demo-user" {
				t.Fatalf("headers %v", r.Header)
			}
		})
	}
}

// TestMutationsRefusedBeforeIO: real accounts, other platforms, missing accounts or keys,
// bad ids and invalid intents never reach eToro.
func TestMutationsRefusedBeforeIO(t *testing.T) {
	realAcct := domain.Account{ID: "real-1", Name: "Real", Mode: domain.ModeReal}
	noMode := domain.Account{ID: "x-1", Name: "No mode"}
	t212 := domain.Account{ID: "t-1", Name: "ISA", Mode: domain.ModeDemo, Platform: domain.PlatformTrading212}
	demoNoKeys := domain.Account{ID: "d-2", Name: "Demo 2", Mode: domain.ModeDemo}
	in := longIntent()
	with := func(f func(*domain.OrderIntent)) domain.OrderIntent { x := in; f(&x); return x }
	type call func(c *Client, ctx context.Context) error
	open := func(i domain.OrderIntent) call {
		return func(c *Client, ctx context.Context) error { _, err := c.Open(ctx, i); return err }
	}
	closePos := func(id string) call {
		return func(c *Client, ctx context.Context) error { _, err := c.Close(ctx, 1001, 1001, id); return err }
	}
	cancel := func(id string) call {
		return func(c *Client, ctx context.Context) error { return c.Cancel(ctx, 700001, id) }
	}
	tests := []struct {
		name string
		acct *domain.Account // nil = the env's demo account; zero ID = no account
		do   call
		is   error
	}{
		{"real open", &realAcct, open(in), domain.ErrRealModeGated},
		{"real short", &realAcct, open(shortIntent()), domain.ErrRealModeGated},
		{"real close", &realAcct, closePos(testRequestID), domain.ErrRealModeGated},
		{"real cancel", &realAcct, cancel(testRequestID), domain.ErrRealModeGated},
		{"account without a mode", &noMode, open(in), domain.ErrRealModeGated},
		{"trading 212 account", &t212, open(in), domain.ErrUnavailable},
		{"no account", &domain.Account{}, closePos(testRequestID), domain.ErrUnavailable},
		{"no keys", &demoNoKeys, open(in), domain.ErrCredentialsMissing},
		{"no stop", nil, open(with(func(x *domain.OrderIntent) { x.StopLossRate = 0 })), domain.ErrInvalid},
		{"third shape", nil, open(with(func(x *domain.OrderIntent) { x.Direction = "buyToCover" })), domain.ErrInvalid},
		{"no amount", nil, open(with(func(x *domain.OrderIntent) { x.Amount = 0 })), domain.ErrInvalid},
		{"no instrument", nil, open(with(func(x *domain.OrderIntent) { x.Instrument = 0 })), domain.ErrInvalid},
		{"negative target", nil, open(with(func(x *domain.OrderIntent) { x.TakeProfitRate = -1 })), domain.ErrInvalid},
		{"request id not a UUID", nil, open(with(func(x *domain.OrderIntent) { x.RequestID = "id0001" })), domain.ErrInvalid},
		{"braced UUID", nil, open(with(func(x *domain.OrderIntent) { x.RequestID = "{" + testRequestID[:34] + "}" })), domain.ErrInvalid},
		{"empty request id", nil, closePos(""), domain.ErrInvalid},
		{"close without position", nil, func(c *Client, ctx context.Context) error { _, err := c.Close(ctx, 0, 1, testRequestID); return err }, domain.ErrInvalid},
		{"close without instrument", nil, func(c *Client, ctx context.Context) error { _, err := c.Close(ctx, 1, 0, testRequestID); return err }, domain.ErrInvalid},
		{"cancel without order", nil, func(c *Client, ctx context.Context) error { return c.Cancel(ctx, 0, testRequestID) }, domain.ErrInvalid},
		{"cancel bad id", nil, cancel("not-a-uuid"), domain.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			_ = e.sec.Put(context.Background(), "etoro.real-1.api_key", "real-api")
			_ = e.sec.Put(context.Background(), "etoro.real-1.user_key", "real-user")
			ctx := e.ctx()
			switch {
			case tt.acct == nil:
			case tt.acct.ID == "":
				ctx = context.Background()
			default:
				ctx = ports.WithAccount(context.Background(), *tt.acct)
			}
			err := tt.do(e.c, ctx)
			if !errors.Is(err, tt.is) {
				t.Fatalf("err = %v, want %v", err, tt.is)
			}
			if errors.Is(err, domain.ErrOutcomeUnknown) {
				t.Fatal("a refusal before sending is never an unknown outcome")
			}
			if len(e.fake.reqs) != 0 {
				t.Fatalf("%d requests reached eToro", len(e.fake.reqs))
			}
		})
	}
}

// TestMutationsNeverRetry: an unclear outcome is reported once, never resent; a refusal is
// a NotAcceptedError; a 429 blocks the execution quota for Retry-After.
func TestMutationsNeverRetry(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		header  http.Header
		unknown bool
		refused bool
		limited time.Duration
		is      error
		reason  string
	}{
		{"500", 500, "oops", nil, true, false, 0, domain.ErrUnavailable, ""},
		{"503 envelope", 503, `{"success":false,"error":{"code":"Unavailable","message":"later"}}`, nil, true, false, 0, nil, "later"},
		{"timeout", stall, "", nil, true, false, 0, nil, ""},
		{"connection dropped", dropConnection, "", nil, true, false, 0, nil, ""},
		{"429", 429, `{"errorCode":"TooManyRequests","errorMessage":"slow down"}`, http.Header{"Retry-After": {"7"}}, false, true, 7 * time.Second, nil, "slow down"},
		{"429 without Retry-After", 429, ``, nil, false, true, 5 * time.Second, nil, ""},
		{"400 problem", 400, `{"type":"x","title":"Bad Request","status":400,"detail":"amount below the minimum"}`, nil, false, true, 0, domain.ErrUnavailable, "amount below the minimum"},
		{"401", 401, `{"errorCode":"Unauthorized","errorMessage":"bad key"}`, nil, false, true, 0, nil, "Unauthorized bad key"},
		{"422 no request id", 422, `{"errorCode":"RequestIdRequired"}`, nil, false, true, 0, nil, "RequestIdRequired"},
		{"404", 404, `{"errorCode":"NotFound","errorMessage":"no such instrument"}`, nil, false, true, 0, domain.ErrNotFound, "no such instrument"},
		{"302 is not followed", 302, ``, http.Header{"Location": {"/api/v1/trading/info/demo/pnl"}}, true, false, 0, nil, ""},
		{"200 other reference", 200, `{"token":"t","orderId":1,"referenceId":"00000000-0000-0000-0000-000000000000"}`, nil, true, false, 0, nil, ""},
		{"200 no order id", 200, `{"token":"t","referenceId":"` + testRequestID + `"}`, nil, true, false, 0, nil, ""},
		{"200 empty", 200, ``, nil, true, false, 0, nil, ""},
		{"200 garbage", 200, `{"orderId":`, nil, true, false, 0, nil, ""},
		{"200 oversized", 200, strings.Repeat(" ", maxResponse+10), nil, true, false, 0, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, func(c *Config) { c.RequestTimeout = 200 * time.Millisecond })
			e.fake.route = func(*http.Request, int) (int, string, http.Header) { return tt.status, tt.body, tt.header }
			_, err := e.c.Open(e.ctx(), longIntent())
			reqs := e.fake.requests()
			if len(reqs) != 1 {
				t.Fatalf("%d requests: an account change must be sent exactly once", len(reqs))
			}
			if reqs[0].Header.Get("x-request-id") != testRequestID {
				t.Fatal("x-request-id must be the order's id")
			}
			if errors.Is(err, domain.ErrOutcomeUnknown) != tt.unknown || errors.Is(err, domain.ErrNotAccepted) != tt.refused {
				t.Fatalf("err = %v (unknown %v, refused %v)", err, tt.unknown, tt.refused)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Fatalf("err = %v, want %v", err, tt.is)
			}
			na, isNA := errors.AsType[*domain.NotAcceptedError](err)
			if isNA && (na.RateLimited != (tt.limited > 0) || na.RetryAfter != tt.limited || !strings.Contains(na.Reason, tt.reason)) {
				t.Fatalf("NotAccepted = %+v", na)
			}
			if len(e.slept) != 0 {
				t.Fatalf("slept %v: no retry, no backoff", e.slept)
			}
			if strings.Contains(err.Error(), "demo-api") || strings.Contains(err.Error(), "demo-user") {
				t.Fatal("key in error")
			}
			// after a 429 the next execution call waits for Retry-After first
			e.fake.setRoute(nil)
			if _, err := e.c.Open(e.ctx(), longIntent()); err != nil {
				t.Fatal(err)
			}
			if tt.limited > 0 && (len(e.slept) != 1 || e.slept[0] != tt.limited) {
				t.Fatalf("limiter not blocked by the 429: slept %v", e.slept)
			}
			if tt.limited == 0 && len(e.slept) != 0 {
				t.Fatalf("slept %v", e.slept)
			}
		})
	}
}

// TestExecutionQuota: open, close and cancel share eToro's 20-per-minute execution quota;
// the 21st call within 60 s waits (fake clock).
func TestExecutionQuota(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()
	e.fake.route = func(r *http.Request, _ int) (int, string, http.Header) {
		if r.Method == http.MethodDelete {
			return 200, `{"token":"t"}`, nil
		}
		return 0, "", nil
	}
	for i := range 20 {
		var err error
		switch i % 3 {
		case 0:
			_, err = e.c.Open(ctx, longIntent())
		case 1:
			_, err = e.c.Close(ctx, 1001, 1001, testRequestID)
		default:
			err = e.c.Cancel(ctx, 700001, testRequestID)
		}
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if len(e.slept) != 0 {
		t.Fatalf("slept within the quota: %v", e.slept)
	}
	if _, err := e.c.Lookup(ctx, testRequestID); err != nil || len(e.slept) != 0 { // reads have their own quota
		t.Fatalf("lookup waited: %v %v", err, e.slept)
	}
	if _, err := e.c.Open(ctx, longIntent()); err != nil {
		t.Fatal(err)
	}
	if len(e.slept) != 1 || e.slept[0] != window {
		t.Fatalf("the 21st execution call must wait for the window: slept %v", e.slept)
	}
	if len(e.fake.reqs) != 22 {
		t.Fatalf("%d requests", len(e.fake.reqs))
	}

	// a context that ends while waiting for the quota sends nothing and is not "unknown"
	e2 := newEnv(t, func(c *Config) { c.Sleep = sleepCtx })
	e2.c.limiter(domain.EnvAccountID).Block(QuotaExecution, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	cctx, cancel := context.WithCancel(e2.ctx())
	cancel()
	if _, err := e2.c.Open(cctx, longIntent()); !errors.Is(err, context.Canceled) || errors.Is(err, domain.ErrOutcomeUnknown) {
		t.Fatalf("cancelled before sending: %v", err)
	}
	if len(e2.fake.reqs) != 0 {
		t.Fatal("sent after the context ended")
	}
}

func TestClose(t *testing.T) {
	e := newEnv(t)
	st, err := e.c.Close(e.ctx(), 1001, 1001, testRequestID)
	want := domain.OrderStatus{RequestID: testRequestID, OrderID: 800001, State: domain.OrderPending, Instrument: 1001}
	if err != nil || !reflect.DeepEqual(st, want) {
		t.Fatalf("Close = %+v %v", st, err)
	}
	r := e.last()
	if r.Method != http.MethodPost || r.URL.Path != "/api/v1/trading/execution/demo/market-close-orders/positions/1001" || r.Header.Get("x-request-id") != testRequestID {
		t.Fatalf("request %s %s %v", r.Method, r.URL, r.Header)
	}
	if body := e.fake.bodies[0]; body != `{"InstrumentID":1001}` {
		t.Fatalf("close body %s: the instrument only, never UnitsToDeduct", body)
	}

	for name, answer := range map[string]string{
		"another position": `{"orderForClose":{"positionID":1002,"instrumentID":1001,"orderID":800001,"statusID":1},"token":"t"}`,
		"no close order":   `{"token":"t"}`,
		"no order id":      `{"orderForClose":{"positionID":1001,"instrumentID":1001,"statusID":1}}`,
	} {
		e := newEnv(t)
		e.fake.route = func(*http.Request, int) (int, string, http.Header) { return 200, answer, nil }
		if _, err := e.c.Close(e.ctx(), 1001, 1001, testRequestID); !errors.Is(err, domain.ErrOutcomeUnknown) {
			t.Errorf("%s: %v", name, err)
		}
	}
	e = newEnv(t)
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 200, `{"orderForClose":{"instrumentID":1001,"orderID":800002,"statusID":3}}`, nil
	}
	if st, err := e.c.Close(e.ctx(), 1001, 1001, testRequestID); err != nil || st.State != domain.OrderFilled || !st.Final || st.OrderID != 800002 {
		t.Fatalf("filled close = %+v %v", st, err)
	}
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 404, `{"errorCode":"PositionNotFound","errorMessage":"position is closed"}`, nil
	}
	if _, err := e.c.Close(e.ctx(), 1001, 1001, testRequestID); !errors.Is(err, domain.ErrNotAccepted) || !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("closed position: %v", err)
	}
}

func TestCancel(t *testing.T) {
	e := newEnv(t)
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 200, `{"token":"ad74d0fd-ca92-4974-a932-9668d29ec366"}`, nil
	}
	if err := e.c.Cancel(e.ctx(), 700001, testRequestID); err != nil {
		t.Fatal(err)
	}
	r := e.last()
	if r.Method != http.MethodDelete || r.URL.Path != "/api/v2/trading/execution/demo/orders/700001" || e.fake.bodies[0] != "" || r.Header.Get("x-request-id") != testRequestID {
		t.Fatalf("request %s %s body %q", r.Method, r.URL, e.fake.bodies[0])
	}
	e.fake.route = func(*http.Request, int) (int, string, http.Header) { return 500, "", nil }
	if err := e.c.Cancel(e.ctx(), 700001, testRequestID); !errors.Is(err, domain.ErrOutcomeUnknown) || len(e.fake.reqs) != 2 {
		t.Fatalf("cancel 500: %v after %d requests", err, len(e.fake.reqs))
	}
}

func TestCloseOrder(t *testing.T) {
	e := newEnv(t)
	st, err := e.c.CloseOrder(e.ctx(), 800001)
	want := domain.OrderStatus{
		RequestID: "5b1c9e2d-3f4a-4b6c-8d7e-1a2b3c4d5e6f", OrderID: 800001, State: domain.OrderFilled, Final: true, Instrument: 1001,
		Executions: []domain.PositionExecution{{PositionID: 1001, Units: 2_645_503, Amount: 503_25, CloseRate: 190_230_000}},
	}
	if err != nil || !reflect.DeepEqual(st, want) {
		t.Fatalf("CloseOrder = %+v %v\nwant %+v", st, err, want)
	}
	if r := e.last(); r.Method != http.MethodGet || r.URL.Path != "/api/v1/trading/info/demo/close-orders/800001" {
		t.Fatalf("request %s %s", r.Method, r.URL)
	}
	for statusID, want := range map[int]domain.OrderState{1: domain.OrderPending, 6: domain.OrderPending, 7: domain.OrderCancelled, 4: domain.OrderRejected, 42: domain.OrderUnknown} {
		e := newEnv(t)
		e.fake.route = func(*http.Request, int) (int, string, http.Header) {
			return 200, `{"orderID":800001,"statusID":` + strconv.Itoa(statusID) + `,"errorMessage":"why","positions":[{"positionID":1001}]}`, nil
		}
		st, err := e.c.CloseOrder(e.ctx(), 800001)
		if err != nil || st.State != want || st.Reason != "why" || len(st.Executions) != 1 || st.Executions[0].CloseRate != 0 {
			t.Errorf("status %d → %+v %v", statusID, st, err)
		}
	}
	e = newEnv(t)
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 404, `{"title":"Not Found","detail":"Close order not found."}`, nil
	}
	if st, err := e.c.CloseOrder(e.ctx(), 800001); !errors.Is(err, domain.ErrNotFound) || st.State != domain.OrderUnknown {
		t.Fatalf("404 = %+v %v", st, err)
	}
	if _, err := e.c.CloseOrder(e.ctx(), 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("order id 0")
	}
	// a real account may read its close orders (reads are not gated)
	e = newEnv(t)
	e.mode.Store(domain.ModeReal)
	_ = e.sec.Put(context.Background(), "etoro.real-1.api_key", "real-api")
	_ = e.sec.Put(context.Background(), "etoro.real-1.user_key", "real-user")
	e.fake.route = func(*http.Request, int) (int, string, http.Header) { return 200, `{"orderID":1,"statusID":3}`, nil }
	if _, err := e.c.CloseOrder(e.ctx(), 1); err != nil || e.last().URL.Path != "/api/v1/trading/info/real/close-orders/1" {
		t.Fatalf("real read: %v", err)
	}
}

func TestLookupFixtures(t *testing.T) {
	tests := []struct {
		fixture string
		want    domain.OrderStatus
		long    bool // conforms as a long
		short   bool // conforms as a short
	}{
		{"lookup_pending.json", domain.OrderStatus{OrderID: 700001, State: domain.OrderPending, Instrument: 1001, Settlement: domain.SettlementReal, Leverage: 1, Side: domain.Long, Reason: "WaitingForMarket"}, true, false},
		{"lookup_rejected.json", domain.OrderStatus{OrderID: 700002, State: domain.OrderRejected, Final: true, Instrument: 1001, Settlement: domain.SettlementReal, Leverage: 1, Side: domain.Long, Reason: "Insufficient funds"}, true, false},
		{"lookup_partial.json", domain.OrderStatus{
			OrderID: 700003, State: domain.OrderPartial, Final: true, Instrument: 1001, Settlement: domain.SettlementReal, Leverage: 1, Side: domain.Long, Reason: "RejectedPartiallyFilled",
			Executions: []domain.PositionExecution{{PositionID: 1002, Units: 1_322_751, Amount: 250_00, OpenRate: 189_000_000, StopLossRate: 185_000_000}},
		}, true, false},
		{"lookup_short.json", domain.OrderStatus{
			OrderID: 700004, State: domain.OrderFilled, Final: true, Instrument: 2002, Settlement: domain.SettlementCFD, Leverage: 1, Side: domain.Short, Reason: "Filled",
			Executions: []domain.PositionExecution{{PositionID: 1003, Units: 2_400_000, Amount: 120_50, OpenRate: 50_208_333, StopLossRate: 52_250_000}},
		}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			e := newEnv(t)
			fixtureFile := tt.fixture
			e.fake.route = func(*http.Request, int) (int, string, http.Header) { return 200, readFixture(t, fixtureFile), nil }
			st, err := e.c.Lookup(e.ctx(), testRequestID)
			tt.want.RequestID = testRequestID
			if err != nil || !reflect.DeepEqual(st, tt.want) {
				t.Fatalf("Lookup = %+v %v\nwant %+v", st, err, tt.want)
			}
			if ok, _ := st.Conforms(domain.Long); ok != tt.long {
				t.Errorf("conforms as long = %v", ok)
			}
			if ok, _ := st.Conforms(domain.Short); ok != tt.short {
				t.Errorf("conforms as short = %v", ok)
			}
		})
	}
	// eToro's own example: CFD, leverage 2, side spelled buy/sell — never conforms
	e := newEnv(t)
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 200, `{"orderId":1,"status":{"id":3},"asset":{"settlementType":"CFD","leverage":2,"side":"buy"},
			"positionExecutions":[{"positionId":9,"remainingUnits":1,"stopLossRate":null,"openingData":{"avgPrice":10}}]}`, nil
	}
	st, err := e.c.Lookup(e.ctx(), testRequestID)
	if err != nil || st.Side != domain.Long || st.Settlement != domain.SettlementCFD || st.Leverage != 2 || st.Executions[0].StopLossRate != 0 || st.Executions[0].Units != 1_000_000 {
		t.Fatalf("Lookup = %+v %v", st, err)
	}
	if ok, why := st.Conforms(domain.Long); ok || !strings.Contains(why, "settled") {
		t.Fatalf("a CFD long conforms: %s", why)
	}
	for in, want := range map[string]domain.Direction{"short": domain.Short, "SELL": domain.Short, "sellShort": domain.Short, "\tlong": domain.Long, "": "", "flat": ""} {
		if side(in) != want {
			t.Errorf("side(%q) = %q", in, side(in))
		}
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestIsUUID(t *testing.T) {
	for s, want := range map[string]bool{
		testRequestID:                           true,
		strings.ToUpper(testRequestID):          true,
		"":                                      false,
		"id0001":                                false,
		"8a3e2f4c1b6d4c3e9f2a7d5e6b4c3a21":      false,
		"8a3e2f4c-1b6d-4c3e-9f2a-7d5e6b4c3a2g":  false,
		"8a3e2f4c-1b6d-4c3e-9f2a_7d5e6b4c3a21":  false,
		"urn:uuid:" + testRequestID:             false,
		"8a3e2f4c-1b6d-4c3e-9f2a-7d5e6b4c3a210": false,
	} {
		if isUUID(s) != want {
			t.Errorf("isUUID(%q) = %v", s, !want)
		}
	}
}

// TestRedirectsAreNeverFollowed: a redirect would forward the keys (custom headers survive a
// cross-host redirect) to wherever Location points.
func TestRedirectsAreNeverFollowed(t *testing.T) {
	var stolen atomic.Int32
	thief := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "" || r.Header.Get("x-user-key") != "" {
			stolen.Add(1)
		}
	}))
	defer thief.Close()
	e := newEnv(t)
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return http.StatusTemporaryRedirect, "", http.Header{"Location": {thief.URL + "/x"}}
	}
	if _, err := e.c.Portfolio(e.ctx()); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("redirected read: %v", err)
	}
	if _, err := e.c.Open(e.ctx(), longIntent()); !errors.Is(err, domain.ErrOutcomeUnknown) {
		t.Fatalf("redirected open: %v", err)
	}
	if stolen.Load() != 0 || len(e.fake.reqs) != 2 {
		t.Fatalf("followed a redirect: %d key leaks, %d requests", stolen.Load(), len(e.fake.reqs))
	}
}
