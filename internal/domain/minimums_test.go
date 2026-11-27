package domain

import (
	"strings"
	"testing"
)

func TestMinimumsFrom(t *testing.T) {
	m := MinimumsFrom(PlatformEtoro, []Eligibility{
		{RealLong: true, MinPositionAmount: 10_00, ShortCFD: true, MinShortAmount: 10_00},
		{RealLong: true, MinPositionAmount: 25_00, ShortCFD: true, MinShortAmount: 50_00},
		{RealLong: true, MinPositionAmount: 0},        // no minimum stated
		{RealLong: false, MinPositionAmount: 1000_00}, // not offered: not counted
		{ShortCFD: false, MinShortAmount: 1000_00},    // not offered: not counted
	})
	if m.Platform != PlatformEtoro || m.Instruments != 5 || m.Long != 25_00 || m.LongLowest != 10_00 || m.LongKnown != 2 ||
		m.Short != 50_00 || m.ShortLowest != 10_00 || m.ShortKnown != 2 {
		t.Fatalf("minimums = %+v", m)
	}
	if z := MinimumsFrom(PlatformEtoro, nil); z.Long != 0 || z.Short != 0 || z.Instruments != 0 {
		t.Fatalf("no instruments = %+v", z)
	}
}

func TestFitMinimums(t *testing.T) {
	base := DefaultRiskLimits
	base.TradingBudget, base.MaxPosition, base.MaxShort = 2000_00, 2000, 2500
	mins := TradeMinimums{Long: 10_00, Short: 10_00}
	with := func(edit func(*RiskLimits)) RiskLimits { l := base; edit(&l); return l }
	tests := []struct {
		name     string
		l        RiskLimits
		m        TradeMinimums
		equity   Cents
		mode     TradingMode
		status   MinimumsStatus
		want     func(RiskLimits) bool
		contains string
	}{
		{"fits already", base, mins, 100_000_00, ModeDemo, MinimumsFit, func(p RiskLimits) bool { return p == base }, ""},
		{
			"short allowance too small", with(func(l *RiskLimits) { l.TradingBudget, l.MaxShort = 100_00, 500 }),
			TradeMinimums{Long: 10_00, Short: 10_00},
			100_000_00, ModeDemo,
			MinimumsPending, func(p RiskLimits) bool { return p.MaxShort == 1000 && p.TradingBudget == 100_00 }, "Short allowance 5.00 % → 10.00 %",
		},
		{
			"short needs a bigger budget", with(func(l *RiskLimits) { l.TradingBudget = 20_00 }), mins, 100_000_00, ModeDemo,
			MinimumsPending, func(p RiskLimits) bool { return p.TradingBudget == 40_00 && p.MaxShort == 2500 }, "Trading budget $20.00 → $40.00",
		},
		{
			"shorts off: left off", with(func(l *RiskLimits) { l.TradingBudget, l.MaxShort = 50_00, 0 }), mins, 100_000_00, ModeDemo,
			MinimumsFit, func(p RiskLimits) bool { return p.MaxShort == 0 }, "",
		},
		{
			"position cap too small", with(func(l *RiskLimits) { l.MaxPosition = 1 }), mins, 50_000_00, ModeDemo,
			MinimumsPending, func(p RiskLimits) bool { return p.MaxPosition == 2 }, "Largest position 0.01 % → 0.02 %",
		},
		{
			"budget below the minimum long", with(func(l *RiskLimits) { l.TradingBudget, l.MaxShort = 5_00, 0 }), mins, 100_000_00, ModeDemo,
			MinimumsPending, func(p RiskLimits) bool { return p.TradingBudget == 10_00 }, "Trading budget $5.00 → $10.00",
		},
		{
			"equity too small for a short", with(func(l *RiskLimits) { l.TradingBudget = 20_00; l.MaxPosition = 10_000 }), mins, 30_00, ModeDemo,
			MinimumsBlocked, func(p RiskLimits) bool { return p.TradingBudget == 20_00 }, "more than the account's equity",
		},
		{"no equity", base, mins, 0, ModeDemo, MinimumsBlocked, func(p RiskLimits) bool { return p == base }, "equity is unknown"},
		{
			"real ceilings refuse the proposal", with(func(l *RiskLimits) { l.MaxPosition = 1 }),
			TradeMinimums{Long: 5000_00},
			10_000_00, ModeReal,
			MinimumsBlocked, func(p RiskLimits) bool { return p.MaxPosition == 1 }, "not allowed on this account",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := FitMinimums(tt.l, tt.m, tt.equity, tt.mode)
			if st := StatusOf(f); st != tt.status || !tt.want(f.Proposed) || f.Fits != (st == MinimumsFit) {
				t.Fatalf("status %s, fit %+v", st, f)
			}
			all := strings.Join(append(append([]string{}, f.Changes...), f.Problems...), "; ")
			if tt.contains != "" && !strings.Contains(all, tt.contains) {
				t.Fatalf("%q lacks %q", all, tt.contains)
			}
			if f.Proposed.TradingBudget > max(tt.l.TradingBudget, tt.equity) || f.Proposed.MaxShort > HardMaxShort {
				t.Fatalf("proposal beyond equity or the short cap: %+v", f.Proposed)
			}
		})
	}
}
