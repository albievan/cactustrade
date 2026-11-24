package domain

import (
	"errors"
	"math/rand/v2"
	"testing"
	"time"
)

func TestRiskPresetsAreValidAndOrdered(t *testing.T) {
	var prev PresetLimits
	for i, p := range RiskPresets {
		if !p.Valid() || p.Label() == "Custom" {
			t.Fatalf("%s: not valid or unnamed", p)
		}
		for _, m := range []TradingMode{ModeDemo, ModeReal} {
			v, ok := PresetFor(p, m)
			if !ok {
				t.Fatalf("%s: no limits", p)
			}
			l := DefaultLimitsFor(m)
			l.TradingBudget = 10_000_00
			l.DailyLoss, l.Drawdown, l.MaxPosition, l.MaxOpenPositions, l.MaxShort = v.DailyLoss, v.Drawdown, v.MaxPosition, v.MaxOpenPositions, v.MaxShort
			if err := l.ValidateFor(m); err != nil {
				t.Fatalf("%s on %s: %v", p, m, err)
			}
			if v.MaxOpenPositions > HardMaxOpenPositions || v.MaxShort > HardMaxShort || v.MaxPosition <= 0 {
				t.Fatalf("%s on %s above a hard limit: %+v", p, m, v)
			}
			if m == ModeReal && (v.DailyLoss > RealCeilings.DailyLoss || v.Drawdown > RealCeilings.Drawdown ||
				v.MaxPosition > RealCeilings.MaxPosition || v.MaxOpenPositions > RealCeilings.MaxOpenPositions || v.MaxShort > RealCeilings.MaxShort) {
				t.Fatalf("%s on real above the real ceilings: %+v", p, v)
			}
		}
		v, _ := PresetFor(p, ModeDemo)
		if i > 0 && (v.DailyLoss <= prev.DailyLoss || v.Drawdown <= prev.Drawdown || v.MaxPosition <= prev.MaxPosition ||
			v.MaxOpenPositions <= prev.MaxOpenPositions || v.MaxShort < prev.MaxShort) {
			t.Fatalf("%s is not bolder than the preset before it: %+v after %+v", p, v, prev)
		}
		if v.Summary() == "" {
			t.Fatalf("%s: no summary", p)
		}
		prev = v
	}
	if _, ok := PresetFor("extreme", ModeDemo); ok || RiskPreset("extreme").Valid() || PresetCustom.Valid() {
		t.Fatal("an unknown preset was accepted")
	}
	low, _ := PresetFor(PresetLow, ModeDemo)
	if low.MaxShort != 0 || low.Summary() != "stops buying after a 0.5 % loss in a day, stops everything after a 3 % fall, each share at most 5 % of the account, at most 3 positions, no shorts" {
		t.Fatalf("low = %+v %q", low, low.Summary())
	}
}

func TestApplyPreset(t *testing.T) {
	strict := DefaultRiskLimits
	strict.TradingBudget = 12_345
	strict.NoEntryBeforeClose, strict.FlattenBeforeClose = time.Hour, 45*time.Minute
	strict.MaxPortfolioAge, strict.MaxQuoteAge = 2*time.Second, 10*time.Second
	loose := DefaultRiskLimits
	loose.TradingBudget = 777
	loose.MaxPortfolioAge, loose.MaxQuoteAge = time.Minute, 5*time.Minute
	tests := []struct {
		name   string
		cur    RiskLimits
		preset RiskPreset
		mode   TradingMode
		want   func(RiskLimits) bool
		err    bool
	}{
		{"low keeps the budget and the longer windows", strict, PresetLow, ModeDemo, func(l RiskLimits) bool {
			return l.TradingBudget == 12_345 && l.NoEntryBeforeClose == time.Hour && l.FlattenBeforeClose == 45*time.Minute &&
				l.MaxPortfolioAge == 2*time.Second && l.MaxQuoteAge == 10*time.Second && l.DailyLoss == 50 && l.MaxShort == 0
		}, false},
		{"high tightens loose freshness to the defaults", loose, PresetHigh, ModeDemo, func(l RiskLimits) bool {
			return l.TradingBudget == 777 && l.MaxPortfolioAge == DefaultRiskLimits.MaxPortfolioAge && l.MaxQuoteAge == DefaultRiskLimits.MaxQuoteAge &&
				l.MaxOpenPositions == 15 && l.MaxShort == HardMaxShort
		}, false},
		{"zero windows take the defaults", RiskLimits{}, PresetMedium, ModeDemo, func(l RiskLimits) bool {
			return l.NoEntryBeforeClose == 30*time.Minute && l.FlattenBeforeClose == 10*time.Minute && l.MaxPortfolioAge == 5*time.Second
		}, false},
		{"high on real is clamped to the real ceilings", DefaultRealRiskLimits, PresetHigh, ModeReal, func(l RiskLimits) bool {
			return l.DailyLoss == 200 && l.Drawdown == 1_000 && l.MaxPosition == 2_000 && l.MaxOpenPositions == 8 && l.MaxShort == HardMaxShort
		}, false},
		{"unknown", DefaultRiskLimits, "yolo", ModeDemo, nil, true},
		{"custom is not a preset", DefaultRiskLimits, PresetCustom, ModeDemo, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ApplyPreset(tc.cur, tc.preset, tc.mode)
			if tc.err {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || !tc.want(got) {
				t.Fatalf("got %+v, %v", got, err)
			}
			if MatchPreset(got, tc.mode) != tc.preset {
				t.Fatalf("match = %s", MatchPreset(got, tc.mode))
			}
		})
	}
	if MatchPreset(strict, ModeDemo) != PresetCustom {
		t.Fatal("the brief's defaults (shorts 25 %) are not a preset")
	}
	if !RaisesAny(mustApply(t, DefaultRiskLimits, PresetLow, ModeDemo), mustApply(t, DefaultRiskLimits, PresetHigh, ModeDemo)) {
		t.Fatal("low → high is not a raise")
	}
	if RaisesAny(mustApply(t, DefaultRiskLimits, PresetHigh, ModeDemo), mustApply(t, DefaultRiskLimits, PresetLow, ModeDemo)) {
		t.Fatal("high → low is a raise")
	}
}

func mustApply(t *testing.T, cur RiskLimits, p RiskPreset, m TradingMode) RiskLimits {
	t.Helper()
	l, err := ApplyPreset(cur, p, m)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// TestApplyPresetProperties: from any valid limits, every preset gives valid limits for the
// mode, keeps the budget, and never shortens a window or loosens a freshness limit.
func TestApplyPresetProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(28, 11))
	for range 2_000 {
		cur := RiskLimits{
			TradingBudget:      Cents(r.Int64N(1_000_000_00)),
			DailyLoss:          BasisPoints(10 + r.IntN(1_991)),
			Drawdown:           BasisPoints(50 + r.IntN(4_951)),
			MaxPosition:        BasisPoints(r.IntN(10_001)),
			MaxOpenPositions:   1 + r.IntN(HardMaxOpenPositions),
			FlattenBeforeClose: time.Duration(5+r.IntN(116)) * time.Minute,
			MaxPortfolioAge:    time.Duration(1+r.IntN(60)) * time.Second,
			MaxQuoteAge:        time.Duration(1+r.IntN(300)) * time.Second,
			MaxShort:           BasisPoints(r.IntN(int(HardMaxShort) + 1)),
		}
		cur.NoEntryBeforeClose = cur.FlattenBeforeClose + time.Duration(r.IntN(int(4*time.Hour-cur.FlattenBeforeClose)/int(time.Minute)+1))*time.Minute
		if err := cur.Validate(); err != nil {
			t.Fatalf("generator: %+v %v", cur, err)
		}
		for _, m := range []TradingMode{ModeDemo, ModeReal} {
			for _, p := range RiskPresets {
				got, err := ApplyPreset(cur, p, m)
				if err != nil {
					t.Fatalf("%s %s from %+v: %v", p, m, cur, err)
				}
				switch {
				case got.TradingBudget != cur.TradingBudget:
					t.Fatalf("budget changed: %+v", got)
				case got.NoEntryBeforeClose < cur.NoEntryBeforeClose || got.NoEntryBeforeClose < DefaultRiskLimits.NoEntryBeforeClose,
					got.FlattenBeforeClose < cur.FlattenBeforeClose || got.FlattenBeforeClose < DefaultRiskLimits.FlattenBeforeClose:
					t.Fatalf("a window got shorter: %+v from %+v", got, cur)
				case got.MaxPortfolioAge > cur.MaxPortfolioAge || got.MaxQuoteAge > cur.MaxQuoteAge:
					t.Fatalf("a freshness limit got looser: %+v from %+v", got, cur)
				case got.ValidateFor(m) != nil:
					t.Fatalf("invalid: %+v", got)
				}
			}
		}
	}
}

func TestPercentText(t *testing.T) {
	for bp, want := range map[BasisPoints]string{0: "0 %", 25: "0.25 %", 50: "0.5 %", 100: "1 %", 250: "2.5 %", 2_000: "20 %", 7_000: "70 %", 5: "0.05 %", -150: "-1.5 %"} {
		if got := PercentText(bp); got != want {
			t.Errorf("PercentText(%d) = %q, want %q", bp, got, want)
		}
	}
}

func TestAggressiveness(t *testing.T) {
	var prev AggressivenessParams
	for i, a := range AggressivenessLevels {
		p := a.Params()
		if !a.Valid() || a.Summary() == "" {
			t.Fatalf("%s", a)
		}
		if i > 0 && (p.RiskPerTrade <= prev.RiskPerTrade || p.EntryProb >= prev.EntryProb || p.MaxPosition <= prev.MaxPosition) {
			t.Fatalf("%s is not bolder than the level before: %+v after %+v", a, p, prev)
		}
		if p.EntryProb < 5_000 || p.MaxPosition > 10_000 || p.RiskPerTrade <= 0 {
			t.Fatalf("%s out of range: %+v", a, p)
		}
		prev = p
	}
	if AggressivenessMedium.Params() != (AggressivenessParams{RiskPerTrade: 50, EntryProb: 6_000, MaxPosition: 2_000}) {
		t.Fatal("medium is not today's strategy defaults")
	}
	if Aggressiveness("wild").Valid() || Aggressiveness("wild").Params() != DefaultAggressiveness.Params() {
		t.Fatal("an unknown level is not the default")
	}
	for _, tc := range []struct {
		a     Aggressiveness
		steps int
		want  Aggressiveness
	}{
		{AggressivenessFull, 1, AggressivenessHigh},
		{AggressivenessFull, 3, AggressivenessLow},
		{AggressivenessFull, 9, AggressivenessLow},
		{AggressivenessLow, 1, AggressivenessLow},
		{AggressivenessHigh, 0, AggressivenessHigh},
		{AggressivenessHigh, -2, AggressivenessHigh},
		{"wild", 1, AggressivenessLow},
	} {
		if got := tc.a.Down(tc.steps); got != tc.want {
			t.Errorf("%s.Down(%d) = %s, want %s", tc.a, tc.steps, got, tc.want)
		}
	}
}

func TestAggressivenessAdapt(t *testing.T) {
	type day struct {
		d       RiskDate
		ran     bool
		latched bool
	}
	tests := []struct {
		name     string
		days     []day
		wantDown int
	}{
		{"a latch steps down", []day{{"2026-11-02", true, true}}, 1},
		{"two latches", []day{{"2026-11-02", true, true}, {"2026-11-03", true, true}}, 2},
		{"never below low", []day{{"2026-11-02", true, true}, {"2026-11-03", true, true}, {"2026-11-04", true, true}, {"2026-11-05", true, true}, {"2026-11-06", true, true}}, 3},
		{"three clean days step back up", []day{{"2026-11-02", true, true}, {"2026-11-03", true, false}, {"2026-11-04", true, false}, {"2026-11-05", true, false}}, 0},
		{"two clean days are not enough", []day{{"2026-11-02", true, true}, {"2026-11-03", true, false}, {"2026-11-04", true, false}}, 1},
		{"days without the agent do not count", []day{{"2026-11-02", true, true}, {"2026-11-03", false, false}, {"2026-11-04", false, false}, {"2026-11-05", false, false}}, 1},
		{"a latch breaks the run", []day{{"2026-11-02", true, true}, {"2026-11-03", true, false}, {"2026-11-04", true, false}, {"2026-11-05", true, true}, {"2026-11-06", true, false}}, 2},
		{"never above the chosen level", []day{{"2026-11-03", true, false}, {"2026-11-04", true, false}, {"2026-11-05", true, false}, {"2026-11-06", true, false}}, 0},
		{"a day folded twice counts once", []day{{"2026-11-02", true, true}, {"2026-11-02", true, true}, {"2026-11-01", true, true}}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var a AggressivenessAdapt
			for _, d := range tc.days {
				if d.ran {
					a.RanOn = d.d
				}
				a = a.EndOfDay(d.d, d.latched)
			}
			if a.Down != tc.wantDown {
				t.Fatalf("down = %d, want %d (%+v)", a.Down, tc.wantDown, a)
			}
		})
	}
	if (AggressivenessAdapt{}).EndOfDay("", true).Down != 0 {
		t.Fatal("an empty day was folded")
	}
}

func TestAutonomyPolicy(t *testing.T) {
	tests := []struct {
		a    Autonomy
		mode TradingMode
		want AutonomyPolicy
	}{
		{AutonomyLow, ModeDemo, AutonomyPolicy{Research: ResearchPropose, ScanEvery: ScanEveryDefault, OwnerSharesOnly: true}},
		{AutonomyMedium, ModeDemo, AutonomyPolicy{Research: ResearchAutoAdd, ScanEvery: ScanEveryDefault}},
		{AutonomyHigh, ModeDemo, AutonomyPolicy{Research: ResearchAutoAdd, ScanEvery: ScanEveryOften, AutoAssignThemes: true, PruneStale: true}},
		{AutonomyFull, ModeDemo, AutonomyPolicy{Research: ResearchAutoAdd, ScanEvery: ScanEveryOften, AutoAssignThemes: true, PruneStale: true, AutoStart: true, AdaptAggressiveness: true}},
		{AutonomyFull, ModeReal, AutonomyPolicy{Research: ResearchAutoAdd, ScanEvery: ScanEveryOften, AutoAssignThemes: true, PruneStale: true, AdaptAggressiveness: true}},
		{"bogus", ModeDemo, AutonomyPolicy{Research: ResearchAutoAdd, ScanEvery: ScanEveryDefault}},
	}
	for _, tc := range tests {
		if got := tc.a.Policy(tc.mode); got != tc.want {
			t.Errorf("%s/%s = %+v, want %+v", tc.a, tc.mode, got, tc.want)
		}
	}
	for _, a := range AutonomyLevels {
		if !a.Valid() || a.Summary() == "" {
			t.Fatalf("%s", a)
		}
	}
	if DefaultAutonomy != AutonomyMedium || DefaultAggressiveness != AggressivenessMedium {
		t.Fatal("the defaults must be medium (owner, R28)")
	}
}

func TestIsAutoAddedAndRegimeText(t *testing.T) {
	for by, want := range map[string]bool{"scanner": true, "research": true, "albie": false, "": false, "import": false} {
		if IsAutoAdded(by) != want {
			t.Errorf("IsAutoAdded(%q) != %v", by, want)
		}
	}
	for _, r := range []Regime{RegimeTrending, RegimeChoppy, RegimeHighVol} {
		if x, ok := RegimeTextOf(r); !ok || x.Label == "" || x.Description == "" || x.AgentMayDo == "" {
			t.Fatalf("%s: %+v", r, x)
		}
	}
	if x, ok := RegimeTextOf("odd"); ok || x.Label != "odd" {
		t.Fatalf("unknown regime: %+v", x)
	}
}
