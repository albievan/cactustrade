package domain

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// PLAN 7.11: real-mode limits (OD25) and RaisesAny.

func TestRealLimits(t *testing.T) {
	if err := DefaultRealRiskLimits.ValidateFor(ModeReal); err != nil {
		t.Fatalf("the real defaults: %v", err)
	}
	if DefaultRealRiskLimits.TradingBudget != 0 || DefaultRealRiskLimits.MaxShort != 0 {
		t.Fatal("the real defaults must start with no budget and no shorts (OD25)")
	}
	if DefaultLimitsFor(ModeReal) != DefaultRealRiskLimits || DefaultLimitsFor(ModeDemo) != DefaultRiskLimits || DefaultLimitsFor("") != DefaultRiskLimits {
		t.Fatal("DefaultLimitsFor")
	}
	// The demo limits are not checked against the real ceilings.
	demo := DefaultRiskLimits
	demo.MaxPosition = 0
	if err := demo.ValidateFor(ModeDemo); err != nil {
		t.Fatalf("demo: %v", err)
	}
	if err := demo.ValidateFor(ModeReal); err == nil {
		t.Fatal("no position cap accepted on real")
	}
	over := []struct {
		field string
		edit  func(*RiskLimits)
	}{
		{"dailyLossBp", func(l *RiskLimits) { l.DailyLoss = RealCeilings.DailyLoss + 1 }},
		{"drawdownBp", func(l *RiskLimits) { l.Drawdown = RealCeilings.Drawdown + 1 }},
		{"maxPositionBp", func(l *RiskLimits) { l.MaxPosition = RealCeilings.MaxPosition + 1 }},
		{"maxPositionBp", func(l *RiskLimits) { l.MaxPosition = 0 }},
		{"maxOpenPositions", func(l *RiskLimits) { l.MaxOpenPositions = RealCeilings.MaxOpenPositions + 1 }},
		{"maxShortBp", func(l *RiskLimits) { l.MaxShort = HardMaxShort + 1 }}, // D19 unchanged (Validate refuses it too)
		{"dailyLossBp", func(l *RiskLimits) { l.DailyLoss = 1 }},              // Validate's own rules still apply
	}
	for _, tt := range over {
		l := DefaultRealRiskLimits
		tt.edit(&l)
		err := l.ValidateFor(ModeReal)
		if ve, ok := errors.AsType[*ValidationError](err); !ok || ve.Field != tt.field {
			t.Errorf("%s: ValidateFor(real) = %v", tt.field, err)
		}
	}
	// At the ceilings is fine, shorts up to 25 % of the budget included.
	at := DefaultRealRiskLimits
	at.DailyLoss, at.Drawdown, at.MaxPosition, at.MaxOpenPositions, at.MaxShort =
		RealCeilings.DailyLoss, RealCeilings.Drawdown, RealCeilings.MaxPosition, RealCeilings.MaxOpenPositions, HardMaxShort
	if err := at.ValidateFor(ModeReal); err != nil {
		t.Fatalf("at the ceilings: %v", err)
	}
	// The optional deployment cap on the budget.
	at.TradingBudget = 100_001
	if err := at.ValidateWithin(RealCeilings.WithBudgetCap(100_000)); err == nil || !strings.Contains(err.Error(), "tradingBudgetCents") {
		t.Fatalf("budget cap: %v", err)
	}
	if err := at.ValidateWithin(RealCeilings.WithBudgetCap(0)); err != nil {
		t.Fatalf("no budget cap: %v", err)
	}
}

// TestRaisesAny compares RaisesAny with a field-by-field reading of "more permissive": one
// field changed at a time in either direction, and random pairs against the same table.
func TestRaisesAny(t *testing.T) {
	type field struct {
		name string
		get  func(RiskLimits) int64
		set  func(*RiskLimits, int64)
		// looser: a larger value is more permissive (false: a smaller one is).
		looser bool
	}
	posCap := func(l RiskLimits) int64 {
		if l.MaxPosition <= 0 {
			return 1 << 62
		}
		return int64(l.MaxPosition)
	}
	fields := []field{
		{"budget", func(l RiskLimits) int64 { return int64(l.TradingBudget) }, func(l *RiskLimits, v int64) { l.TradingBudget = Cents(v) }, true},
		{"daily", func(l RiskLimits) int64 { return int64(l.DailyLoss) }, func(l *RiskLimits, v int64) { l.DailyLoss = BasisPoints(v) }, true},
		{"drawdown", func(l RiskLimits) int64 { return int64(l.Drawdown) }, func(l *RiskLimits, v int64) { l.Drawdown = BasisPoints(v) }, true},
		{"position", posCap, func(l *RiskLimits, v int64) { l.MaxPosition = BasisPoints(v) }, true},
		{"open", func(l RiskLimits) int64 { return int64(l.MaxOpenPositions) }, func(l *RiskLimits, v int64) { l.MaxOpenPositions = int(v) }, true},
		{"short", func(l RiskLimits) int64 { return int64(l.MaxShort) }, func(l *RiskLimits, v int64) { l.MaxShort = BasisPoints(v) }, true},
		{"pfAge", func(l RiskLimits) int64 { return int64(l.MaxPortfolioAge) }, func(l *RiskLimits, v int64) { l.MaxPortfolioAge = time.Duration(v) }, true},
		{"quoteAge", func(l RiskLimits) int64 { return int64(l.MaxQuoteAge) }, func(l *RiskLimits, v int64) { l.MaxQuoteAge = time.Duration(v) }, true},
		{"noEntry", func(l RiskLimits) int64 { return int64(l.NoEntryBeforeClose) }, func(l *RiskLimits, v int64) { l.NoEntryBeforeClose = time.Duration(v) }, false},
		{"flatten", func(l RiskLimits) int64 { return int64(l.FlattenBeforeClose) }, func(l *RiskLimits, v int64) { l.FlattenBeforeClose = time.Duration(v) }, false},
	}
	reference := func(old, next RiskLimits) bool {
		for _, f := range fields {
			a, b := f.get(old), f.get(next)
			if (f.looser && b > a) || (!f.looser && b < a) {
				return true
			}
		}
		return false
	}
	base := DefaultRealRiskLimits
	base.TradingBudget = 100_000
	if RaisesAny(base, base) {
		t.Fatal("no change raised a limit")
	}
	for _, f := range fields {
		up, down := base, base
		f.set(&up, f.get(base)+1)
		f.set(&down, f.get(base)-1)
		if got := RaisesAny(base, up); got != f.looser {
			t.Errorf("%s +1: RaisesAny = %v", f.name, got)
		}
		if got := RaisesAny(base, down); got == f.looser {
			t.Errorf("%s -1: RaisesAny = %v", f.name, got)
		}
	}
	off := base
	off.MaxPosition = 0
	if !RaisesAny(base, off) || RaisesAny(off, base) {
		t.Fatal("switching the position cap off must count as raising it (and on as lowering)")
	}
	r := rand.New(rand.NewPCG(25, 11))
	for range 20_000 {
		a, b := base, base
		for _, f := range fields {
			if r.IntN(3) == 0 {
				f.set(&a, f.get(base)+int64(r.IntN(5))-2)
			}
			if r.IntN(3) == 0 {
				f.set(&b, f.get(base)+int64(r.IntN(5))-2)
			}
		}
		if RaisesAny(a, b) != reference(a, b) {
			t.Fatalf("RaisesAny(%+v, %+v) = %v, reference %v", a, b, RaisesAny(a, b), reference(a, b))
		}
	}
}
