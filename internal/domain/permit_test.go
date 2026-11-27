package domain

import (
	"errors"
	"math/rand/v2"
	"testing"
	"time"
)

func TestPermitEntryAndExit(t *testing.T) {
	now := t0
	acct := Account{ID: "real", Name: "Live", Mode: ModeReal, Platform: PlatformEtoro}
	gate := LockedGate("real").Arm("demo", "u", GateEvidence{}, now.Add(-time.Hour))
	p, err := PermitEntry(gate, acct, now, time.Time{})
	if err != nil || !p.IsEntry() || p.AccountID() != "real" {
		t.Fatalf("PermitEntry = %+v %v", p, err)
	}
	tests := []struct {
		name string
		acct string
		at   time.Time
		want bool
	}{
		{"at issue", "real", now, true},
		{"within the TTL", "real", now.Add(RealPermitTTL), true},
		{"past the TTL", "real", now.Add(RealPermitTTL + time.Nanosecond), false},
		{"before issue", "real", now.Add(-time.Nanosecond), false},
		{"other account", "real-2", now, false},
		{"no account", "", now, false},
	}
	for _, tt := range tests {
		if got := p.AllowsEntry(tt.acct, tt.at); got != tt.want {
			t.Errorf("%s: AllowsEntry = %v", tt.name, got)
		}
	}
	// Never past the gate's own expiry, even within the TTL.
	late, err := PermitEntry(gate, acct, gate.ExpiresAt.Add(-time.Second), time.Time{})
	if err != nil || late.AllowsEntry("real", gate.ExpiresAt) {
		t.Fatalf("a permit outlived its gate: %v", err)
	}
	if !p.AllowsExit("real") || p.AllowsExit("real-2") || p.AllowsExit("") {
		t.Fatal("an entry permit allows exits of its own account only")
	}
	x, err := PermitExit(acct, true)
	if err != nil || x.IsEntry() || !x.AllowsExit("real") || x.AllowsExit("other") || x.AllowsEntry("real", now) {
		t.Fatalf("PermitExit = %+v %v", x, err)
	}
	if (RealPermit{}).AllowsExit("real") || (RealPermit{}).AllowsEntry("real", now) || (RealPermit{}).AllowsEntry("", time.Time{}) {
		t.Fatal("the zero permit allows something")
	}
	if _, err := PermitEntry(gate, acct, time.Time{}, time.Time{}); !errors.Is(err, ErrNoRealPermit) {
		t.Fatalf("a permit without a time: %v", err)
	}
}

// TestRealEntryPermitProperties (PLAN 7.19): over random gates, permits, accounts and times, a
// real entry is allowed by AssessEntry, and an intent built by IntentFor, only with an entry
// permit for that account, built from its gate while armed and live, no older than
// RealPermitTTL and before the gate expires; a demo entry's outcome never depends on the
// permit.
func TestRealEntryPermitProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 19))
	acct := Account{ID: "real", Name: "Live", Mode: ModeReal, Platform: PlatformEtoro}
	other := Account{ID: "other", Name: "Other", Mode: ModeReal, Platform: PlatformEtoro}
	allowed := 0
	for range 20_000 {
		now := t0
		armedAt := now.Add(-time.Duration(r.Int64N(int64(40 * 24 * time.Hour))))
		g := LockedGate(acct.ID).Arm("demo", "u", GateEvidence{}, armedAt)
		if r.IntN(5) == 0 {
			g = g.Disarm("u", "test", armedAt.Add(time.Minute))
		}
		var restored time.Time
		if r.IntN(5) == 0 {
			restored = armedAt.Add(time.Duration(r.Int64N(int64(2*time.Hour))) - time.Hour)
		}
		issued := now.Add(-time.Duration(r.Int64N(int64(2 * RealPermitTTL))))
		forAcct := acct
		if r.IntN(6) == 0 {
			forAcct = other
			g.AccountID = other.ID
		}
		var p RealPermit
		var perr error
		switch r.IntN(6) {
		case 0: // no permit
		case 1:
			p, perr = PermitExit(acct, r.IntN(2) == 0)
		default:
			p, perr = PermitEntry(g, forAcct, issued, restored)
		}
		live, _ := g.Live(issued, restored)
		want := perr == nil && p.IsEntry() && forAcct.ID == acct.ID && live &&
			now.Sub(issued) <= RealPermitTTL && now.Before(g.ExpiresAt)

		in := okInput()
		l := DefaultRealRiskLimits
		l.TradingBudget = 500_000
		in.Limits, in.Mode, in.AccountID, in.Permit = l, ModeReal, acct.ID, p
		d := AssessEntry(in)
		if d.Allowed != want {
			t.Fatalf("AssessEntry allowed=%v, want %v (gate %+v, restored %v, issued %v, for %s): %s", d.Allowed, want, g, restored, issued, forAcct.ID, d.Reason)
		}
		o, err := NewOrder(Order{
			RequestID: "11111111-2222-4333-8444-555555555555", AccountID: acct.ID, Mode: ModeReal, Kind: OrderOpen,
			Direction: Long, Instrument: 1, Amount: 100_00, Entry: 100_000_000, StopLoss: 97_000_000,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := IntentFor(o, p); (err == nil) != want {
			t.Fatalf("IntentFor err=%v, want allowed %v", err, want)
		}
		if want {
			allowed++
		}
		// Demo: the permit changes nothing.
		demo := okInput()
		demo.Permit = p
		if a, b := AssessEntry(demo), AssessEntry(okInput()); a != b {
			t.Fatalf("a permit changed a demo decision: %+v vs %+v", a, b)
		}
	}
	if allowed == 0 {
		t.Fatal("no case was allowed: the property is vacuous")
	}
}
