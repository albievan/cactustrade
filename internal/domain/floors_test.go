package domain

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestDrawdownFloor(t *testing.T) {
	l := DefaultRiskLimits
	l.Drawdown = 1_000 // 10 %
	tests := []struct {
		name   string
		equity Cents
		hwm    Cents
		want   Cents
	}{
		{"below the mark", 90_000, 100_000, 90_000},
		{"above the mark: measured from equity", 120_000, 100_000, 108_000},
		{"no mark yet", 50_000, 0, 45_000},
		{"nothing at all", 0, 0, 0},
		{"negative equity, no mark", -5, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DrawdownFloor(tt.equity, RiskState{HighWaterMark: tt.hwm}, l); got != tt.want {
				t.Fatalf("DrawdownFloor = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestDrawdownFloorAgreesWithBreach: the floor shown to the user is the one the kill switch
// uses.
func TestDrawdownFloorAgreesWithBreach(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for range 20_000 {
		l := DefaultRiskLimits
		l.Drawdown = BasisPoints(1 + r.IntN(5_000))
		st := RiskState{HighWaterMark: Cents(r.Int64N(10_000_000))}
		eq := Cents(r.Int64N(12_000_000)) - 1_000_000
		if max(st.HighWaterMark, eq) <= 0 {
			continue
		}
		if got, want := eq <= DrawdownFloor(eq, st, l), DrawdownBreached(eq, st, l); got != want {
			t.Fatalf("equity %d hwm %d bp %d: floor says %v, breach says %v", eq, st.HighWaterMark, l.Drawdown, got, want)
		}
	}
}

func TestFlattenAt(t *testing.T) {
	closeAt := time.Date(2026, 11, 24, 21, 0, 0, 0, time.UTC)
	l := DefaultRiskLimits
	l.FlattenBeforeClose = 15 * time.Minute
	if got := FlattenAt(closeAt, l); !got.Equal(closeAt.Add(-15 * time.Minute)) {
		t.Fatalf("FlattenAt = %s", got)
	}
	l.FlattenBeforeClose = time.Minute // below the minimum: the default applies, as in ExitsDue
	if got := FlattenAt(closeAt, l); !got.Equal(closeAt.Add(-DefaultRiskLimits.FlattenBeforeClose)) {
		t.Fatalf("FlattenAt below minimum = %s", got)
	}
}
