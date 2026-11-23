package strategy

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

func bar(t0 time.Time, i int, o, h, l, c float64) domain.Candle {
	m := func(f float64) domain.Micros { return domain.Micros(math.Round(f * 1e6)) }
	return domain.Candle{Start: t0.Add(time.Duration(i) * time.Minute), Open: m(o), High: m(h), Low: m(l), Close: m(c), Volume: 1000}
}

func series(closes ...float64) []domain.Candle {
	t0 := time.Date(2026, 11, 23, 14, 30, 0, 0, time.UTC)
	out := make([]domain.Candle, len(closes))
	for i, c := range closes {
		out[i] = bar(t0, i, c, c, c, c)
	}
	return out
}

func input(bars []domain.Candle, probs map[domain.Regime]float64, trendMean float64) Input {
	cfg := Config{FeatureWindow: 5, BreakoutBars: 10, ReversionBars: 10}.withDefaults()
	return Input{
		Instrument: 7, Bars: bars, Features: Features(bars, cfg.FeatureWindow), Probs: probs, Equity: 1_000_000, Cfg: cfg,
		Model: &HMM{K: 2, Mean: [][]float64{{trendMean, 0.001}, {0, 0.001}}, Label: []domain.Regime{domain.RegimeTrending, domain.RegimeChoppy}},
	}
}

func TestTrendBreakout(t *testing.T) {
	up := make([]float64, 30)
	for i := range up {
		up[i] = 100 + float64(i)*0.1 + 0.05*float64(i%2)
	}
	trending := map[domain.Regime]float64{domain.RegimeTrending: 0.9, domain.RegimeChoppy: 0.1}
	c, ok, why := TrendBreakout{}.Propose(input(series(up...), trending, 0.001))
	if !ok {
		t.Fatalf("no candidate: %s", why)
	}
	if c.StopLossRate >= c.Entry || c.Entry >= c.TakeProfitRate || c.SuggestedSize <= 0 || c.SuggestedSize > 200_000 || c.Playbook != "trend_breakout" || c.Instrument != 7 {
		t.Fatalf("candidate = %+v", c)
	}
	// take-profit is RewardRisk times the stop distance
	if d1, d2 := c.Entry-c.StopLossRate, c.TakeProfitRate-c.Entry; math.Abs(float64(d2)-2*float64(d1)) > 2 {
		t.Fatalf("reward/risk: stop %d, target %d", d1, d2)
	}
	for name, tc := range map[string]struct {
		probs map[domain.Regime]float64
		mean  float64
		bars  []domain.Candle
	}{
		"not trending": {map[domain.Regime]float64{domain.RegimeTrending: 0.5}, 0.001, series(up...)},
		"down-trend":   {trending, -0.001, series(up...)},
		"no breakout":  {trending, 0.001, series(append(append([]float64{}, up[:29]...), 99)...)},
		"too short":    {trending, 0.001, series(up[:8]...)},
	} {
		if _, ok, why := (TrendBreakout{}).Propose(input(tc.bars, tc.probs, tc.mean)); ok || why == "" {
			t.Errorf("%s: proposed (%s)", name, why)
		}
	}
}

func TestMeanReversion(t *testing.T) {
	flat := make([]float64, 30)
	for i := range flat {
		flat[i] = 100 + 0.05*float64(i%3)
	}
	dip := append(append([]float64{}, flat[:29]...), 99)
	choppy := map[domain.Regime]float64{domain.RegimeChoppy: 0.8, domain.RegimeTrending: 0.2}
	c, ok, why := MeanReversion{}.Propose(input(series(dip...), choppy, 0))
	if !ok {
		t.Fatalf("no candidate: %s", why)
	}
	if c.StopLossRate >= c.Entry || c.Entry >= c.TakeProfitRate || c.TakeProfitRate < 100_000_000 || c.Playbook != "mean_reversion" {
		t.Fatalf("candidate = %+v", c)
	}
	if _, ok, _ := (MeanReversion{}).Propose(input(series(flat...), choppy, 0)); ok {
		t.Error("no dip, but proposed")
	}
	if _, ok, _ := (MeanReversion{}).Propose(input(series(dip...), map[domain.Regime]float64{domain.RegimeChoppy: 0.3}, 0)); ok {
		t.Error("not choppy, but proposed")
	}
	if _, ok, _ := (MeanReversion{}).Propose(input(series(dip[:5]...), choppy, 0)); ok {
		t.Error("too short, but proposed")
	}
	if MeanReversion.Regime(MeanReversion{}) != domain.RegimeChoppy || (TrendBreakout{}).Regime() != domain.RegimeTrending {
		t.Fatal("regimes")
	}
}

func TestStopAndSizeCaps(t *testing.T) {
	in := input(series(make([]float64, 0)...), nil, 0)
	in.Features = []Feature{{Vol: 0}}
	stop, size, ok := stopAndSize(in, 100, domain.Long)
	// MinStopPct 0.2 %: risking 0.5 % of equity would need 250 % of equity → capped at 20 %
	if !ok || math.Abs(stop-99.8) > 1e-9 || size != 200_000 {
		t.Fatalf("stop %v size %v ok %v", stop, size, ok)
	}
	in.Equity = 0
	if _, _, ok := stopAndSize(in, 100, domain.Long); ok {
		t.Fatal("zero equity sized a trade")
	}
}

func TestEngine(t *testing.T) {
	bars := synth(11, 700)
	e := NewEngine(Config{FitWindow: 200, RefitEvery: 50})
	if d := e.Evaluate(1, bars[:10], 1_000_000); d.Candidate != nil || d.Reason == "" {
		t.Fatal("decided without history")
	}
	if d := e.Evaluate(1, bars[:25], 1_000_000); d.Candidate != nil || !strings.Contains(d.Reason, "regime model") {
		t.Fatalf("too few observations: %+v", d)
	}
	seen := map[string]bool{}
	for t0 := 260; t0 < len(bars); t0++ {
		d := e.Evaluate(1, bars[:t0+1], 1_000_000)
		switch {
		case d.Candidate != nil:
			seen["candidate:"+d.Candidate.Playbook] = true
		case strings.Contains(d.Reason, "high volatility"):
			seen["highvol"] = true
		default:
			seen["none"] = true
		}
		if d.Probs == nil {
			t.Fatalf("no probabilities at %d: %s", t0, d.Reason)
		}
	}
	if !seen["highvol"] || !seen["none"] {
		t.Errorf("outcomes = %v", seen)
	}
	if e.Refits < 8 || e.Refits > 10 {
		t.Errorf("refits = %d, want one per 50 bars", e.Refits)
	}
	bad := append([]domain.Candle{}, bars[:300]...)
	bad[299].Close = 0
	if d := e.Evaluate(1, bad, 1); d.Candidate != nil {
		t.Fatal("decided on a bad bar")
	}
}

func TestShortPlaybooks(t *testing.T) {
	down := make([]float64, 30)
	for i := range down {
		down[i] = 100 - float64(i)*0.1 - 0.05*float64(i%2)
	}
	trending := map[domain.Regime]float64{domain.RegimeTrending: 0.9, domain.RegimeChoppy: 0.1}
	in := input(series(down...), trending, -0.001)
	if _, ok, why := (TrendBreakdown{}).Propose(in); ok || why != "shorts are off for this account" {
		t.Fatalf("shorts off: %v %q", ok, why)
	}
	in.Cfg.Shorts = true
	c, ok, why := TrendBreakdown{}.Propose(in)
	if !ok {
		t.Fatalf("no short: %s", why)
	}
	if c.Direction != domain.Short || c.StopLossRate <= c.Entry || c.TakeProfitRate >= c.Entry || c.SuggestedSize <= 0 || c.Playbook != "trend_breakdown_short" {
		t.Fatalf("short candidate = %+v", c)
	}
	up := input(series(down...), trending, 0.001)
	up.Cfg.Shorts = true
	if _, ok, _ := (TrendBreakdown{}).Propose(up); ok {
		t.Fatal("shorted an up-trend")
	}
	notTrend := input(series(down...), map[domain.Regime]float64{domain.RegimeTrending: 0.3}, -0.001)
	notTrend.Cfg.Shorts = true
	if _, ok, _ := (TrendBreakdown{}).Propose(notTrend); ok {
		t.Fatal("shorted without a trend")
	}
	noBreak := input(series(append(append([]float64{}, down[:29]...), 101)...), trending, -0.001)
	noBreak.Cfg.Shorts = true
	if _, ok, _ := (TrendBreakdown{}).Propose(noBreak); ok {
		t.Fatal("shorted without a breakdown")
	}
	short := input(series(down[:5]...), trending, -0.001)
	short.Cfg.Shorts = true
	if _, ok, _ := (TrendBreakdown{}).Propose(short); ok {
		t.Fatal("shorted on too few bars")
	}

	flat := make([]float64, 30)
	for i := range flat {
		flat[i] = 100 + 0.05*float64(i%3)
	}
	spike := append(append([]float64{}, flat[:29]...), 101)
	choppy := map[domain.Regime]float64{domain.RegimeChoppy: 0.8, domain.RegimeTrending: 0.2}
	sp := input(series(spike...), choppy, 0)
	if _, ok, _ := (SpikeReversion{}).Propose(sp); ok {
		t.Fatal("spike short while shorts are off")
	}
	sp.Cfg.Shorts = true
	c, ok, why = SpikeReversion{}.Propose(sp)
	if !ok || c.Direction != domain.Short || c.StopLossRate <= c.Entry || c.TakeProfitRate >= c.Entry || c.Playbook != "spike_reversion_short" {
		t.Fatalf("spike short = %+v %v %s", c, ok, why)
	}
	for name, x := range map[string]Input{
		"no spike":     input(series(flat...), choppy, 0),
		"not choppy":   input(series(spike...), map[domain.Regime]float64{domain.RegimeChoppy: 0.2}, 0),
		"too few bars": input(series(spike[:5]...), choppy, 0),
	} {
		x.Cfg.Shorts = true
		if _, ok, _ := (SpikeReversion{}).Propose(x); ok {
			t.Errorf("%s: shorted", name)
		}
	}
	if (TrendBreakdown{}).Regime() != domain.RegimeTrending || (SpikeReversion{}).Regime() != domain.RegimeChoppy {
		t.Fatal("regimes")
	}
	in2 := input(series(make([]float64, 0)...), nil, 0)
	in2.Features = []Feature{{Vol: 0}}
	if stop, _, ok := stopAndSize(in2, 100, domain.Short); !ok || math.Abs(stop-100.2) > 1e-9 {
		t.Fatalf("short stop = %v", stop)
	}
}
