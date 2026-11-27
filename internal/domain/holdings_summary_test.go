package domain

import "testing"

func TestHoldingsSummary(t *testing.T) {
	h := Holdings{Positions: []Holding{
		{Long: true, Value: 1000, PnL: 50},
		{Long: true, Value: 2500, PnL: -20},
		{Long: true, Unknown: true},
		{Long: false, CFD: true, Value: 700, PnL: 30},
	}}
	got := h.Summary()
	want := HoldingsSummary{Longs: 3, Shorts: 1, LongValue: 3500, ShortValue: 700, LongPnL: 30, ShortPnL: 30, UnknownValues: 1}
	if got != want {
		t.Fatalf("summary %+v, want %+v", got, want)
	}
	if (Holdings{}).Summary() != (HoldingsSummary{}) {
		t.Fatal("no positions is all zero")
	}
}
