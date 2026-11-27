package domain

import (
	"errors"
	"testing"
	"time"
)

// TestRealRefusalDomain (PLAN 7.17, converted by 7.19): the domain refuses real money however
// complete the rest is — a gate armed and in force, every arming check passing (arming is
// allowed since D39), limits within the real ceilings, a reconciled account, a fresh portfolio
// and an eligible instrument — unless the entry carries a valid entry permit for the account:
// no permit, another account's, an expired one, an exit permit, or one that cannot be built
// from a locked, expired or restored gate. With a valid permit the same entry is allowed.
func TestRealRefusalDomain(t *testing.T) {
	now := t0
	realAcct := Account{ID: "real", Name: "Live", Mode: ModeReal, Platform: PlatformEtoro}
	other := Account{ID: "real-2", Name: "Other", Mode: ModeReal, Platform: PlatformEtoro}
	gate := LockedGate("real").Arm("demo", "u-admin", GateEvidence{Days: []RiskDate{"2026-11-22"}, Count: 1, First: "2026-11-22", Last: "2026-11-22"}, now.Add(-time.Hour))
	if ok, _ := gate.Live(now, time.Time{}); !ok {
		t.Fatal("setup: the gate is not in force")
	}
	if err := CanArm(GateChecks(gateInput())); err != nil {
		t.Fatalf("CanArm(all pass) = %v", err)
	}
	valid, err := PermitEntry(gate, realAcct, now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	otherGate := gate
	otherGate.AccountID = other.ID
	otherPermit, err := PermitEntry(otherGate, other, now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	stale, err := PermitEntry(gate, realAcct, now.Add(-RealPermitTTL-time.Second), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	exit, err := PermitExit(realAcct, true)
	if err != nil {
		t.Fatal(err)
	}
	// Permits that cannot be built at all.
	locked := gate.Disarm("u-admin", "test", now.Add(-time.Minute))
	for name, build := range map[string]func() (RealPermit, error){
		"locked gate":    func() (RealPermit, error) { return PermitEntry(locked, realAcct, now, time.Time{}) },
		"never armed":    func() (RealPermit, error) { return PermitEntry(LockedGate("real"), realAcct, now, time.Time{}) },
		"expired gate":   func() (RealPermit, error) { return PermitEntry(gate, realAcct, gate.ExpiresAt, time.Time{}) },
		"restored after": func() (RealPermit, error) { return PermitEntry(gate, realAcct, now, now.Add(-time.Minute)) },
		"other account":  func() (RealPermit, error) { return PermitEntry(gate, other, now, time.Time{}) },
		"demo account": func() (RealPermit, error) {
			return PermitEntry(gate, Account{ID: "real", Mode: ModeDemo}, now, time.Time{})
		},
		"exit, not ours": func() (RealPermit, error) { return PermitExit(realAcct, false) },
		"exit on demo":   func() (RealPermit, error) { return PermitExit(Account{ID: "d", Mode: ModeDemo}, true) },
		"exit on Trading212": func() (RealPermit, error) {
			return PermitExit(Account{ID: "t", Mode: ModeReal, Platform: PlatformTrading212}, true)
		},
	} {
		if p, err := build(); !errors.Is(err, ErrNoRealPermit) || p != (RealPermit{}) {
			t.Errorf("%s: permit %+v, err %v", name, p, err)
		}
	}

	// S1: the risk engine refuses a real entry that a demo account would be allowed, unless
	// the permit is valid.
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
	refusing := map[string]RealPermit{"none": {}, "other account": otherPermit, "expired": stale, "exit only": exit}
	for _, dir := range []Direction{Long, Short} {
		in.Mode, in.AccountID, in.Direction, in.StopLoss = ModeReal, realAcct.ID, dir, 98_000_000
		in.Limits.MaxShort = 0
		if dir == Short {
			in.StopLoss = 102_000_000
			in.Limits.MaxShort = HardMaxShort
		}
		for name, p := range refusing {
			in.Permit = p
			if d := AssessEntry(in); d.Allowed || d.Size != 0 {
				t.Fatalf("S1: a real %s entry was allowed with permit %q: %+v", dir, name, d)
			}
		}
		in.Permit = valid
		if d := AssessEntry(in); !d.Allowed {
			t.Fatalf("S1: a real %s entry with a valid permit was refused: %s", dir, d.Reason)
		}
		// Limits above the real ceilings are refused even with a valid permit.
		over := in
		over.Limits.DailyLoss = RealCeilings.DailyLoss + 1
		if d := AssessEntry(over); d.Allowed {
			t.Fatalf("S1: real %s allowed above the ceilings", dir)
		}
	}

	// S2: no broker intent is built for a real order without a valid permit.
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
		for name, p := range refusing {
			if _, err := IntentFor(o, p); !errors.Is(err, ErrRealModeGated) {
				t.Fatalf("S2: IntentFor(real %s, %s) = %v", dir, name, err)
			}
		}
		if in, err := IntentFor(o, valid); err != nil || in.RequestID != o.RequestID || in.Direction != dir {
			t.Fatalf("S2: IntentFor(real %s, valid) = %+v %v", dir, in, err)
		}
	}
}
