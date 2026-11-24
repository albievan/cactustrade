package strategy

import (
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

func TestConfigFor(t *testing.T) {
	if got, want := ConfigFor(domain.AggressivenessMedium, true).withDefaults(), (Config{Shorts: true}).withDefaults(); got != want {
		t.Fatalf("medium is not today's defaults: %+v vs %+v", got, want)
	}
	tests := []struct {
		a                   domain.Aggressiveness
		risk, prob, maxSize float64
	}{
		{domain.AggressivenessLow, 0.0025, 0.70, 0.10},
		{domain.AggressivenessMedium, 0.005, 0.60, 0.20},
		{domain.AggressivenessHigh, 0.01, 0.55, 0.30},
		{domain.AggressivenessFull, 0.015, 0.50, 0.40},
	}
	for _, tc := range tests {
		c := ConfigFor(tc.a, false).withDefaults()
		if c.RiskPerTrade != tc.risk || c.EntryProb != tc.prob || c.MaxPositionPct != tc.maxSize || c.Shorts {
			t.Errorf("%s = %+v", tc.a, c)
		}
		if c.HighVolStop != 0.4 || c.StopSigmas != 3 || c.MinStopPct != 0.002 {
			t.Errorf("%s changed a gate or a stop rule: %+v", tc.a, c)
		}
	}
}

// TestAggressivenessNeverSuggestsAboveItsCap: at every level and on random price paths the
// strategy's suggested size stays within MaxPositionPct of the equity, every stop is on the
// losing side, and nothing is proposed while P(high volatility) is at or above the gate. (The
// risk engine's own caps are AssessEntry's property tests and the app's sequence test.)
func TestAggressivenessNeverSuggestsAboveItsCap(t *testing.T) {
	const equity = domain.Cents(10_000_000)
	proposals := 0
	for _, a := range domain.AggressivenessLevels {
		for seed := range uint64(12) {
			bars := synth(seed, 600)
			e := NewEngine(ConfigFor(a, true))
			for n := 450; n <= len(bars); n += 3 {
				d := e.Evaluate(1, bars[:n], equity)
				if d.Candidate == nil {
					continue
				}
				proposals++
				c := d.Candidate
				ceiling := domain.Cents(float64(equity) * float64(a.Params().MaxPosition) / 10_000)
				switch {
				case c.SuggestedSize <= 0 || c.SuggestedSize > ceiling:
					t.Fatalf("%s seed %d bar %d: size %s above the cap %s", a, seed, n, c.SuggestedSize, ceiling)
				case c.Direction == domain.Long && c.StopLossRate >= c.Entry, c.Direction == domain.Short && c.StopLossRate <= c.Entry:
					t.Fatalf("%s: stop on the wrong side: %+v", a, c)
				case d.Probs[domain.RegimeHighVol] >= 0.4:
					t.Fatalf("%s: proposed in a volatile market: %+v", a, d.Probs)
				}
			}
		}
	}
	if proposals == 0 {
		t.Fatal("no level proposed anything: the property was not exercised")
	}
}

func TestMayDo(t *testing.T) {
	p := func(tr, ch, hv float64) map[domain.Regime]float64 {
		return map[domain.Regime]float64{domain.RegimeTrending: tr, domain.RegimeChoppy: ch, domain.RegimeHighVol: hv}
	}
	tests := []struct {
		name   string
		probs  map[domain.Regime]float64
		cfg    Config
		stance Stance
		text   string
	}{
		{"unknown", nil, Config{}, StanceUnknown, "Not reviewed yet"},
		{"volatile", p(0.3, 0.2, 0.5), Config{}, StanceStayOut, "Stays out: volatile (50 %, stays out from 40 %)"},
		{"trend long only", p(0.7, 0.2, 0.1), Config{}, StanceTrend, "May buy a breakout"},
		{"trend with shorts", p(0.7, 0.2, 0.1), Config{Shorts: true}, StanceTrend, "May buy a breakout or short a breakdown"},
		{"choppy", p(0.2, 0.65, 0.15), Config{}, StanceReversion, "May buy a dip back to the average"},
		{"wait at medium", p(0.55, 0.3, 0.15), Config{}, StanceWait, "Waits: no mood is clear enough (needs 60 %)"},
		{"acts at high", p(0.55, 0.3, 0.15), ConfigFor(domain.AggressivenessHigh, false), StanceTrend, "May buy a breakout"},
		{"both at full", p(0.5, 0.5, 0), ConfigFor(domain.AggressivenessFull, true), StanceBoth, "May buy a breakout or short a breakdown; or buy a dip back to the average or short a spike"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, text := MayDo(tc.probs, tc.cfg)
			if st != tc.stance || text != tc.text {
				t.Fatalf("got %s %q", st, text)
			}
		})
	}
	if _, text := MayDo(p(0.2, 0.2, 0.6), ConfigFor(domain.AggressivenessFull, true)); !strings.HasPrefix(text, "Stays out") {
		t.Fatal("full aggressiveness entered a volatile market")
	}
}
