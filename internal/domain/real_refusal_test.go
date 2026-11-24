package domain

import (
	"errors"
	"testing"
	"time"
)

// TestRealRefusalDomain (PLAN 7.17, S1 and S2): before the owner's written consent (7.18) the
// domain refuses real money however complete the rest is — a gate armed and in force, every
// arming check passing, limits within the real ceilings, a reconciled account, a fresh
// portfolio and an eligible instrument.
func TestRealRefusalDomain(t *testing.T) {
	now := t0
	gate := LockedGate("real").Arm("demo", "u-admin", GateEvidence{Days: []RiskDate{"2026-11-22"}, Count: 1, First: "2026-11-22", Last: "2026-11-22"}, now.Add(-time.Hour))
	if ok, _ := gate.Live(now, time.Time{}); !ok {
		t.Fatal("setup: the gate is not in force")
	}
	// Every arming check passes, and arming is still refused.
	if err := CanArm(GateChecks(gateInput())); !errors.Is(err, ErrRealTradingDisabled) {
		t.Fatalf("CanArm = %v", err)
	}

	// S1: the risk engine refuses a real entry that a demo account would be allowed.
	in := okInput()
	l := DefaultRealRiskLimits
	l.TradingBudget = 500_000
	in.Limits = l
	if err := in.Limits.ValidateFor(ModeReal); err != nil {
		t.Fatal(err)
	}
	if d := AssessEntry(in); !d.Allowed {
		t.Fatalf("setup: demo refused: %s", d.Reason)
	}
	for _, dir := range []Direction{Long, Short} {
		in.Mode, in.Direction, in.StopLoss = ModeReal, dir, 98_000_000
		if dir == Short {
			in.StopLoss = 102_000_000
		}
		if d := AssessEntry(in); d.Allowed || d.Size != 0 {
			t.Fatalf("S1: a real %s entry was allowed: %+v", dir, d)
		}
	}

	// S2: no broker intent is ever built for a real order.
	for _, dir := range []Direction{Long, Short} {
		stop := Micros(97_000_000)
		if dir == Short {
			stop = 103_000_000
		}
		o, err := NewOrder(Order{
			RequestID: "11111111-2222-4333-8444-555555555555", AccountID: "real", Mode: ModeReal, Kind: OrderOpen,
			Direction: dir, Instrument: 1, Amount: 100_00, Entry: 100_000_000, StopLoss: stop,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := IntentFor(o); !errors.Is(err, ErrRealModeGated) {
			t.Fatalf("S2: IntentFor(real %s) = %v", dir, err)
		}
	}
}
