package etoro

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// Recorded answers from the demo account (PLAN "Live findings 2026-11-24"): sanitised, with
// accountId and gcid set to 0. They are the source of truth where they differ from the
// synthetic, spec-shaped fixtures.

const (
	recordedOrderID    = 396359836
	recordedPositionID = 3649965576
)

// TestLookupRecordedFilledByOrderID: eToro answered 404 to orders:lookup?referenceId= for two
// filled demo orders; orderId= works. The fill's amount is initialExposureAccountCurrency
// (investedAmountCurrency was 1 for $100), settlementType is upper case, and the execution
// was already closed by eToro (take-profit).
func TestLookupRecordedFilledByOrderID(t *testing.T) {
	e := newEnv(t)
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 200, readFixture(t, "recorded/lookup_filled_by_order_id.json"), nil
	}
	st, err := e.c.Lookup(e.ctx(), testRequestID, recordedOrderID)
	want := domain.OrderStatus{
		RequestID: testRequestID, OrderID: recordedOrderID, State: domain.OrderFilled, Final: true, Instrument: 1002,
		Settlement: domain.SettlementReal, Leverage: 1, Side: domain.Long, Reason: "Filled",
		Executions: []domain.PositionExecution{{
			PositionID: recordedPositionID, Units: 362_279, Amount: 99_99, OpenRate: 275_990_000, StopLossRate: 275_180_000, Closed: true,
		}},
	}
	if err != nil || !reflect.DeepEqual(st, want) {
		t.Fatalf("Lookup = %+v %v\nwant %+v", st, err, want)
	}
	q := e.last().URL.Query()
	if q.Get("orderId") != "396359836" || q.Has("referenceId") {
		t.Fatalf("lookup query = %v: with an eToro order id it must use orderId only", q)
	}
	if ok, why := st.Conforms(domain.Long); !ok {
		t.Fatalf("the recorded GOOG fill must conform as a long: %s", why)
	}
	// Without an eToro order id the lookup is by our request id.
	if _, err := e.c.Lookup(e.ctx(), testRequestID, 0); err != nil {
		t.Fatal(err)
	}
	if q := e.last().URL.Query(); q.Get("referenceId") != testRequestID || q.Has("orderId") {
		t.Fatalf("lookup query = %v: without an order id it must use referenceId", q)
	}
	if _, err := e.c.Lookup(e.ctx(), "", 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("no ids: %v", err)
	}
}

// TestLookupWrongOrder: an answer about another order than the one asked for is not used.
func TestLookupWrongOrder(t *testing.T) {
	e := newEnv(t)
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 200, readFixture(t, "recorded/lookup_filled_by_order_id.json"), nil
	}
	if _, err := e.c.Lookup(e.ctx(), testRequestID, 123); err == nil || errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an answer for order %d accepted for order 123: %v", recordedOrderID, err)
	}
}

// TestLookupAmountFields: the amount of an execution is initialExposureAccountCurrency; when
// it is 0 or absent marginAccountCurrency; then investedAmountCurrency. An execution's state
// "closed" (any case) is read as closed; settlementType is read in any case.
func TestLookupAmountFields(t *testing.T) {
	tests := []struct {
		name, exec string
		amount     domain.Cents
		closed     bool
	}{
		{"exposure", `"initialExposureAccountCurrency":99.985,"marginAccountCurrency":50,"investedAmountCurrency":1`, 99_99, false},
		{"exposure zero → margin", `"initialExposureAccountCurrency":0,"marginAccountCurrency":99.99,"investedAmountCurrency":1,"state":"Closed"`, 99_99, true},
		{"no exposure → margin", `"marginAccountCurrency":42.5,"investedAmountCurrency":1,"state":"open"`, 42_50, false},
		{"only invested", `"investedAmountCurrency":12.34`, 12_34, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			body := `{"orderId":7,"status":{"id":3,"name":"Filled"},"asset":{"instrumentId":1,"settlementType":"Real","leverage":1,"side":"long"},
				"positionExecutions":[{"positionId":9,` + tt.exec + `,"stopLossRate":9,"openingData":{"units":1,"avgPrice":10}}]}`
			e.fake.route = func(*http.Request, int) (int, string, http.Header) { return 200, body, nil }
			st, err := e.c.Lookup(e.ctx(), testRequestID, 7)
			if err != nil || len(st.Executions) != 1 {
				t.Fatalf("Lookup = %+v %v", st, err)
			}
			if x := st.Executions[0]; x.Amount != tt.amount || x.Closed != tt.closed || st.Settlement != domain.SettlementReal {
				t.Fatalf("execution = %+v, settlement %q; want amount %s closed %v", x, st.Settlement, tt.amount, tt.closed)
			}
		})
	}
}

// TestLookupWithRaw: the operator's diagnosis (cactustrade etoro-lookup) sees the parsed status
// and eToro's answer exactly as received, through the same lookup.
func TestLookupWithRaw(t *testing.T) {
	e := newEnv(t)
	fixture := readFixture(t, "recorded/lookup_filled_by_order_id.json")
	e.fake.route = func(*http.Request, int) (int, string, http.Header) { return 200, fixture, nil }
	st, raw, err := e.c.LookupWithRaw(e.ctx(), "", recordedOrderID)
	if err != nil || st.State != domain.OrderFilled || len(st.Executions) != 1 || !json.Valid(raw) ||
		strings.TrimSpace(string(raw)) != strings.TrimSpace(fixture) {
		t.Fatalf("LookupWithRaw = %+v %s %v", st, raw, err)
	}
	e.fake.route = func(*http.Request, int) (int, string, http.Header) {
		return 404, `{"type":"x","title":"Not Found","status":404,"detail":"no such order"}`, nil
	}
	if _, _, err := e.c.LookupWithRaw(e.ctx(), testRequestID, 0); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
}
