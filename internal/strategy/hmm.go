package strategy

import (
	"errors"
	"math"
	"slices"

	"github.com/albievan/cactustrade/internal/domain"
)

// HMM is a Gaussian hidden Markov model with diagonal covariances.
type HMM struct {
	K     int         // states
	Pi    []float64   // initial distribution, K
	A     [][]float64 // transitions, K×K (rows sum to 1)
	Mean  [][]float64 // K×D
	Var   [][]float64 // K×D (diagonal covariance, floored)
	Label []domain.Regime
}

// FitConfig tunes Baum–Welch.
type FitConfig struct {
	K        int     // 2 or 3 states (default 3)
	MaxIter  int     // default 50
	Tol      float64 // stop when the log-likelihood improves by less (default 1e-6)
	MinObs   int     // fewer observations → ErrTooFewObservations (default 10·K)
	VarFloor float64 // minimum variance per dimension, relative to the data's (default 1e-3)
}

// ErrTooFewObservations means there is not enough history to fit (no regime → no trade).
var ErrTooFewObservations = errors.New("strategy: too few observations to fit the regime model")

func (c FitConfig) withDefaults() FitConfig {
	if c.K < 2 || c.K > 3 {
		c.K = 3
	}
	if c.MaxIter <= 0 {
		c.MaxIter = 50
	}
	if c.Tol <= 0 {
		c.Tol = 1e-6
	}
	if c.MinObs <= 0 {
		c.MinObs = 10 * c.K
	}
	if c.VarFloor <= 0 {
		c.VarFloor = 1e-3
	}
	return c
}

// Fit runs Baum–Welch on obs (time order, all with the same dimension). Initialisation is
// deterministic: states are seeded from volatility quantiles, so the same input always
// gives the same model. Fit uses only the observations it is given; a caller that fits on
// obs[:t+1] gets a model that knows nothing after t.
func Fit(obs [][]float64, cfg FitConfig) (*HMM, error) {
	cfg = cfg.withDefaults()
	if len(obs) < cfg.MinObs {
		return nil, ErrTooFewObservations
	}
	d := len(obs[0])
	for _, o := range obs {
		if len(o) != d {
			return nil, errors.New("strategy: observations differ in dimension")
		}
		for _, v := range o {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, errors.New("strategy: observation is not finite")
			}
		}
	}
	floor := make([]float64, d)
	for j := range d {
		_, v := meanVar(obs, j)
		floor[j] = max(v*cfg.VarFloor, 1e-12)
	}
	m := initModel(obs, cfg.K, floor)
	prev := math.Inf(-1)
	for range cfg.MaxIter {
		ll := m.step(obs, floor)
		if ll-prev < cfg.Tol {
			break
		}
		prev = ll
	}
	m.label()
	return m, nil
}

func meanVar(obs [][]float64, j int) (float64, float64) {
	mean := 0.0
	for _, o := range obs {
		mean += o[j]
	}
	mean /= float64(len(obs))
	ss := 0.0
	for _, o := range obs {
		ss += (o[j] - mean) * (o[j] - mean)
	}
	return mean, ss / float64(len(obs))
}

// initModel seeds K states from the volatility (last dimension) quantiles.
func initModel(obs [][]float64, k int, floor []float64) *HMM {
	d := len(obs[0])
	idx := make([]int, len(obs))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		switch x, y := obs[a][d-1], obs[b][d-1]; {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	})
	m := &HMM{K: k, Pi: make([]float64, k), A: make([][]float64, k), Mean: make([][]float64, k), Var: make([][]float64, k)}
	for s := range k {
		m.Pi[s] = 1 / float64(k)
		m.A[s] = make([]float64, k)
		for t := range k {
			m.A[s][t] = 0.1 / float64(k-1)
		}
		m.A[s][s] = 0.9
		lo, hi := s*len(idx)/k, (s+1)*len(idx)/k
		group := make([][]float64, 0, hi-lo)
		for _, i := range idx[lo:hi] {
			group = append(group, obs[i])
		}
		m.Mean[s], m.Var[s] = make([]float64, d), make([]float64, d)
		for j := range d {
			mu, v := meanVar(group, j)
			m.Mean[s][j], m.Var[s][j] = mu, max(v, floor[j])
		}
	}
	return m
}

// logEmission is log N(o; mean_s, diag var_s).
func (m *HMM) logEmission(s int, o []float64) float64 {
	ll := 0.0
	for j, x := range o {
		v := m.Var[s][j]
		diff := x - m.Mean[s][j]
		ll += -0.5 * (math.Log(2*math.Pi*v) + diff*diff/v)
	}
	return ll
}

// emissions returns per-time, per-state emission likelihoods, each row rescaled by its max
// (the scale is returned in log form so the forward pass stays exact).
func (m *HMM) emissions(obs [][]float64) ([][]float64, []float64) {
	b := make([][]float64, len(obs))
	logScale := make([]float64, len(obs))
	for t, o := range obs {
		row := make([]float64, m.K)
		mx := math.Inf(-1)
		for s := range m.K {
			row[s] = m.logEmission(s, o)
			mx = max(mx, row[s])
		}
		for s := range m.K {
			row[s] = math.Exp(row[s] - mx)
		}
		b[t], logScale[t] = row, mx
	}
	return b, logScale
}

// forward is the scaled forward pass: alpha[t] = P(state_t | obs_0..t). It returns the
// filtered probabilities, the scale factors and the log-likelihood.
func (m *HMM) forward(b [][]float64, logScale []float64) ([][]float64, []float64, float64) {
	alpha := make([][]float64, len(b))
	c := make([]float64, len(b))
	ll := 0.0
	for t := range b {
		a := make([]float64, m.K)
		for s := range m.K {
			if t == 0 {
				a[s] = m.Pi[s] * b[t][s]
				continue
			}
			sum := 0.0
			for r := range m.K {
				sum += alpha[t-1][r] * m.A[r][s]
			}
			a[s] = sum * b[t][s]
		}
		norm := 0.0
		for _, v := range a {
			norm += v
		}
		if norm == 0 || math.IsNaN(norm) {
			norm = math.SmallestNonzeroFloat64
			for s := range a {
				a[s] = 1 / float64(m.K)
			}
		} else {
			for s := range a {
				a[s] /= norm
			}
		}
		alpha[t], c[t] = a, norm
		ll += math.Log(norm) + logScale[t]
	}
	return alpha, c, ll
}

// step is one Baum–Welch iteration (training only: the backward pass never feeds a signal).
func (m *HMM) step(obs [][]float64, floor []float64) float64 {
	n, k, d := len(obs), m.K, len(obs[0])
	b, logScale := m.emissions(obs)
	alpha, c, ll := m.forward(b, logScale)
	beta := make([][]float64, n)
	beta[n-1] = make([]float64, k)
	for s := range k {
		beta[n-1][s] = 1
	}
	for t := n - 2; t >= 0; t-- {
		beta[t] = make([]float64, k)
		for r := range k {
			sum := 0.0
			for s := range k {
				sum += m.A[r][s] * b[t+1][s] * beta[t+1][s]
			}
			beta[t][r] = sum / c[t+1]
		}
	}
	gamma := make([][]float64, n)
	xi := make([][]float64, k)
	for r := range k {
		xi[r] = make([]float64, k)
	}
	for t := range n {
		g := make([]float64, k)
		sum := 0.0
		for s := range k {
			g[s] = alpha[t][s] * beta[t][s]
			sum += g[s]
		}
		for s := range k {
			if sum > 0 {
				g[s] /= sum
			}
		}
		gamma[t] = g
		if t == n-1 {
			continue
		}
		for r := range k {
			for s := range k {
				xi[r][s] += alpha[t][r] * m.A[r][s] * b[t+1][s] * beta[t+1][s] / c[t+1]
			}
		}
	}
	copy(m.Pi, gamma[0])
	for r := range k {
		rowSum := 0.0
		for s := range k {
			rowSum += xi[r][s]
		}
		for s := range k {
			if rowSum > 0 {
				m.A[r][s] = xi[r][s] / rowSum
			}
		}
		gsum := 0.0
		for t := range n {
			gsum += gamma[t][r]
		}
		if gsum < 1e-12 {
			continue // an empty state keeps its parameters
		}
		for j := range d {
			mu := 0.0
			for t := range n {
				mu += gamma[t][r] * obs[t][j]
			}
			mu /= gsum
			v := 0.0
			for t := range n {
				diff := obs[t][j] - mu
				v += gamma[t][r] * diff * diff
			}
			m.Mean[r][j], m.Var[r][j] = mu, max(v/gsum, floor[j])
		}
	}
	return ll
}

// label names the states: the highest-volatility state is high_volatility; of the others,
// the one with the larger mean return per unit of volatility is trending, the rest choppy.
func (m *HMM) label() {
	d := len(m.Mean[0])
	order := make([]int, m.K)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { // by volatility, descending
		switch x, y := m.Mean[a][d-1], m.Mean[b][d-1]; {
		case x > y:
			return -1
		case x < y:
			return 1
		}
		return 0
	})
	m.Label = make([]domain.Regime, m.K)
	m.Label[order[0]] = domain.RegimeHighVol
	rest := order[1:]
	best := rest[0]
	for _, s := range rest[1:] {
		if trendScore(m.Mean[s]) > trendScore(m.Mean[best]) {
			best = s
		}
	}
	for _, s := range rest {
		m.Label[s] = domain.RegimeChoppy
	}
	m.Label[best] = domain.RegimeTrending
}

// trendScore is |mean return| / mean volatility for a state's mean vector (return, vol).
func trendScore(mean []float64) float64 {
	vol := mean[len(mean)-1]
	if vol <= 0 {
		return 0
	}
	return math.Abs(mean[0]) / vol
}

// Filter returns, for every t, P(state_t | obs_0..t) — the scaled forward filter only. It
// never smooths, so the probabilities at t do not change when later observations are added.
func (m *HMM) Filter(obs [][]float64) [][]float64 {
	if len(obs) == 0 {
		return nil
	}
	b, logScale := m.emissions(obs)
	alpha, _, _ := m.forward(b, logScale)
	return alpha
}

// RegimeProbs maps one filtered distribution to the regime names.
func (m *HMM) RegimeProbs(p []float64) map[domain.Regime]float64 {
	out := map[domain.Regime]float64{}
	for s, v := range p {
		out[m.Label[s]] += v
	}
	return out
}

// StateMean returns the mean return of the state labelled r (0 if none).
func (m *HMM) StateMean(r domain.Regime) float64 {
	for s, l := range m.Label {
		if l == r {
			return m.Mean[s][0]
		}
	}
	return 0
}
