package main

import (
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// TestPrintLookup: the operator sees how cactusTrade reads the answer, then the answer as is.
func TestPrintLookup(t *testing.T) {
	var b strings.Builder
	st := domain.OrderStatus{
		OrderID: 396359836, State: domain.OrderFilled, Final: true, Reason: "Filled", Instrument: 1002,
		Settlement: domain.SettlementReal, Leverage: 1, Side: domain.Long,
		Executions: []domain.PositionExecution{{PositionID: 3649965576, Units: 362_279, Amount: 99_99, OpenRate: 275_990_000, StopLossRate: 275_180_000, Closed: true}},
	}
	if err := printLookup(&b, st, []byte(`{"orderId":396359836}`)); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"order 396359836, state filled (final true)", `settlement "real"`, "position 3649965576", "amount $99.99", "closed true", `raw: {"orderId":396359836}`} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}
