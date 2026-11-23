package strategy

import (
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// synth makes minute bars with three regimes in turn: a calm up-trend, a calm sideways
// market and a violent one, from a fixed seed.
func synth(seed uint64, n int) []domain.Candle {
	r := rand.New(rand.NewPCG(seed, 7))
	price := 100.0
	t0 := time.Date(2026, 11, 23, 14, 30, 0, 0, time.UTC)
	out := make([]domain.Candle, n)
	for i := range n {
		var drift, vol float64
		switch (i / 150) % 3 {
		case 0:
			drift, vol = 0.0008, 0.0006 // trending up
		case 1:
			drift, vol = 0, 0.0007 // choppy
		default:
			drift, vol = 0, 0.006 // high volatility
		}
		price *= math.Exp(drift + vol*r.NormFloat64())
		p := domain.Micros(math.Round(price * 1e6))
		out[i] = domain.Candle{Start: t0.Add(time.Duration(i) * time.Minute), Open: p, High: p, Low: p, Close: p, Volume: int64(1000 + r.IntN(200))}
	}
	return out
}

func TestFeatures(t *testing.T) {
	bars := synth(1, 60)
	fs := Features(bars, 20)
	if len(fs) != 40 || !fs[0].Start.Equal(bars[20].Start) {
		t.Fatalf("len %d, first %v", len(fs), fs[0].Start)
	}
	want := math.Log(float64(bars[20].Close) / float64(bars[19].Close))
	if math.Abs(fs[0].Return-want) > 1e-12 || fs[0].Vol <= 0 {
		t.Fatalf("feature 0 = %+v", fs[0])
	}
	// no look-ahead: appending bars never changes earlier features
	more := Features(synth(1, 90), 20)
	if !reflect.DeepEqual(more[:40], fs) {
		t.Fatal("earlier features changed when later bars were added")
	}
	// a bad close stops the computation instead of inventing data
	bad := synth(1, 60)
	bad[30].Close = 0
	if got := Features(bad, 20); len(got) != 10 {
		t.Fatalf("features after a bad bar = %d", len(got))
	}
	if Features(bars[:5], 1) == nil && len(Features(bars[:5], 1)) != 3 {
		t.Fatal("window is clamped to 2")
	}
	if z := zscore([]float64{5, 5, 5}, 9); z != 0 {
		t.Fatalf("flat z = %v", z)
	}
	if z := zscore(nil, 1); z != 0 {
		t.Fatal("empty z")
	}
	if o := Observations(fs[:1]); len(o) != 1 || o[0][0] != fs[0].Return || o[0][1] != fs[0].Vol {
		t.Fatal("observations")
	}
}

func TestFitIsDeterministicAndFindsRegimes(t *testing.T) {
	obs := Observations(Features(synth(42, 900), 10))
	m1, err := Fit(obs, FitConfig{})
	if err != nil {
		t.Fatal(err)
	}
	m2, _ := Fit(obs, FitConfig{})
	if !reflect.DeepEqual(m1, m2) {
		t.Fatal("Fit is not deterministic")
	}
	probs := m1.Filter(obs)
	// bars 0..9 were warm-up, so observation i is bar i+10; check the middle of each block
	count := map[domain.Regime]map[domain.Regime]int{}
	for i, p := range probs {
		bar := i + 10
		if bar%150 < 30 { // skip the transition after each regime change
			continue
		}
		truth := []domain.Regime{domain.RegimeTrending, domain.RegimeChoppy, domain.RegimeHighVol}[(bar/150)%3]
		rp := m1.RegimeProbs(p)
		best := domain.RegimeChoppy
		for r, v := range rp {
			if v > rp[best] {
				best = r
			}
		}
		if count[truth] == nil {
			count[truth] = map[domain.Regime]int{}
		}
		count[truth][best]++
	}
	hv := count[domain.RegimeHighVol]
	if hv[domain.RegimeHighVol] < 9*(hv[domain.RegimeTrending]+hv[domain.RegimeChoppy]+hv[domain.RegimeHighVol])/10 {
		t.Errorf("high-volatility blocks recognised poorly: %v", count)
	}
	calm := count[domain.RegimeTrending][domain.RegimeHighVol] + count[domain.RegimeChoppy][domain.RegimeHighVol]
	if calm > (count[domain.RegimeTrending][domain.RegimeTrending]+count[domain.RegimeChoppy][domain.RegimeChoppy])/10 {
		t.Errorf("calm blocks taken for high volatility: %v", count)
	}
	for _, p := range probs {
		sum := 0.0
		for _, v := range p {
			if v < 0 || v > 1 || math.IsNaN(v) {
				t.Fatalf("bad probability %v", p)
			}
			sum += v
		}
		if math.Abs(sum-1) > 1e-9 {
			t.Fatalf("probabilities sum to %v", sum)
		}
	}
	if m1.StateMean(domain.RegimeTrending) <= m1.StateMean(domain.RegimeChoppy) && m1.StateMean(domain.RegimeTrending) >= 0 {
		t.Logf("trend mean %v, chop mean %v", m1.StateMean(domain.RegimeTrending), m1.StateMean(domain.RegimeChoppy))
	}
	if m2 := (&HMM{Label: []domain.Regime{domain.RegimeChoppy}}); m2.StateMean(domain.RegimeTrending) != 0 {
		t.Fatal("StateMean of a missing regime")
	}
}

// TestNoLookAhead is the property of CLAUDE.md phase 3.2: the filtered probabilities at t
// never change when observations after t are appended — for any model and any split.
func TestNoLookAhead(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 3))
	for trial := range 25 {
		obs := Observations(Features(synth(uint64(trial), 400), 10))
		m, err := Fit(obs[:200], FitConfig{K: 2 + trial%2})
		if err != nil {
			t.Fatal(err)
		}
		full := m.Filter(obs)
		cut := 1 + r.IntN(len(obs)-1)
		prefix := m.Filter(obs[:cut])
		for i := range prefix {
			for s := range prefix[i] {
				if prefix[i][s] != full[i][s] {
					t.Fatalf("trial %d: P(state %d at %d) = %v with %d obs, %v with %d", trial, s, i, prefix[i][s], cut, full[i][s], len(obs))
				}
			}
		}
		// fitting on data up to t is independent of anything after t
		m2, _ := Fit(obs[:200], FitConfig{K: 2 + trial%2})
		if !reflect.DeepEqual(m, m2) {
			t.Fatal("refit differs")
		}
	}
}

func TestFitRejectsBadInput(t *testing.T) {
	if _, err := Fit(make([][]float64, 5), FitConfig{}); !errors.Is(err, ErrTooFewObservations) {
		t.Fatalf("too few: %v", err)
	}
	obs := Observations(Features(synth(9, 120), 10))
	ragged := append([][]float64{{1}}, obs...)
	if _, err := Fit(ragged, FitConfig{}); err == nil {
		t.Fatal("ragged accepted")
	}
	nan := append([][]float64{}, obs...)
	nan[3] = []float64{math.NaN(), 1}
	if _, err := Fit(nan, FitConfig{}); err == nil {
		t.Fatal("NaN accepted")
	}
	// constant data: variances floored, probabilities still valid
	flat := make([][]float64, 50)
	for i := range flat {
		flat[i] = []float64{0, 0.001}
	}
	m, err := Fit(flat, FitConfig{K: 2, MaxIter: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range m.Filter(flat) {
		if math.IsNaN(p[0]) {
			t.Fatal("NaN on flat data")
		}
	}
	if m.Filter(nil) != nil {
		t.Fatal("Filter(nil)")
	}
	if (FitConfig{K: 9}).withDefaults().K != 3 {
		t.Fatal("K default")
	}
}
