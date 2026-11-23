// Package strategy holds cactusTrade's market model: features from intraday candles, a
// Gaussian hidden Markov model of market regimes, and the playbooks that turn a regime into
// trade candidates. Everything here is a pure, deterministic function of the bars it is
// given: no I/O, no clock, no randomness, and nothing at time t ever looks at a bar after t.
//
// Floats are used here (and only here and at the eToro JSON edge, CLAUDE.md §3); prices
// enter and leave as domain.Micros.
package strategy

import (
	"math"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Feature is what the model sees for one bar.
type Feature struct {
	Start time.Time
	// Return is the log return from the previous close to this close.
	Return float64
	// Vol is the realised volatility: the root mean square of the last Window returns.
	Vol float64
	// VolumeZ is this bar's volume as a z-score against the previous Window bars (0 when
	// the window has no variance, e.g. volume not reported).
	VolumeZ float64
}

// FeatureWindow is the default look-back for volatility and volume.
const FeatureWindow = 20

// Features computes per-bar features from candles in time order. The first `window` bars
// only warm the window up and produce no feature, so len(out) = max(0, len(bars)-window).
// Bars with a non-positive close are invalid and stop the computation (fail closed: the
// caller gets fewer features, never made-up ones). Feature i depends only on bars ≤ i.
func Features(bars []domain.Candle, window int) []Feature {
	if window < 2 {
		window = 2
	}
	rets := make([]float64, 0, len(bars))
	vols := make([]float64, 0, len(bars))
	var out []Feature
	for i := 1; i < len(bars); i++ {
		prev, cur := bars[i-1].Close, bars[i].Close
		if prev <= 0 || cur <= 0 {
			break
		}
		rets = append(rets, math.Log(float64(cur)/float64(prev)))
		vols = append(vols, float64(bars[i].Volume))
		if len(rets) < window {
			continue
		}
		w := rets[len(rets)-window:]
		ss := 0.0
		for _, r := range w {
			ss += r * r
		}
		out = append(out, Feature{
			Start:   bars[i].Start,
			Return:  rets[len(rets)-1],
			Vol:     math.Sqrt(ss / float64(window)),
			VolumeZ: zscore(vols[len(vols)-window:len(vols)-1], vols[len(vols)-1]),
		})
	}
	return out
}

// zscore of x against the sample hist (population standard deviation); 0 when flat.
func zscore(hist []float64, x float64) float64 {
	if len(hist) == 0 {
		return 0
	}
	mean := 0.0
	for _, v := range hist {
		mean += v
	}
	mean /= float64(len(hist))
	ss := 0.0
	for _, v := range hist {
		ss += (v - mean) * (v - mean)
	}
	sd := math.Sqrt(ss / float64(len(hist)))
	if sd == 0 {
		return 0
	}
	return (x - mean) / sd
}

// Observations turns features into the HMM's 2-D observations: (return, volatility).
func Observations(fs []Feature) [][]float64 {
	out := make([][]float64, len(fs))
	for i, f := range fs {
		out[i] = []float64{f.Return, f.Vol}
	}
	return out
}
