package strategy

import (
	"fmt"
	"math"

	"github.com/albievan/cactustrade/internal/domain"
)

// Config tunes the model and the playbooks. Zero values take the defaults.
type Config struct {
	FeatureWindow int // bars for volatility / volume (default 20)
	FitWindow     int // observations the HMM is fitted on (default 390: one NYSE day of 1-minute bars)
	RefitEvery    int // refit after this many new bars (default 30); filter only in between
	States        int // HMM states, 2 or 3 (default 3)

	EntryProb   float64 // minimum P(regime) for a playbook to act (default 0.6)
	HighVolStop float64 // P(high_volatility) at or above which nothing is entered (default 0.4)

	BreakoutBars   int     // trend: close above the highest close of the previous N bars (default 20)
	ReversionBars  int     // mean reversion: look-back for the mean (default 20)
	ReversionZ     float64 // mean reversion: enter when the close is this many sd below (default 2)
	StopSigmas     float64 // stop distance in bar volatilities (default 3)
	MinStopPct     float64 // stop distance floor as a fraction of price (default 0.002)
	RewardRisk     float64 // trend take-profit as a multiple of the stop distance (default 2)
	RiskPerTrade   float64 // fraction of equity lost if the stop is hit (default 0.005)
	MaxPositionPct float64 // largest suggested size as a fraction of equity (default 0.2)
}

func (c Config) withDefaults() Config {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	deff := func(v *float64, d float64) {
		if *v <= 0 {
			*v = d
		}
	}
	def(&c.FeatureWindow, FeatureWindow)
	def(&c.FitWindow, 390)
	def(&c.RefitEvery, 30)
	def(&c.States, 3)
	def(&c.BreakoutBars, 20)
	def(&c.ReversionBars, 20)
	deff(&c.EntryProb, 0.6)
	deff(&c.HighVolStop, 0.4)
	deff(&c.ReversionZ, 2)
	deff(&c.StopSigmas, 3)
	deff(&c.MinStopPct, 0.002)
	deff(&c.RewardRisk, 2)
	deff(&c.RiskPerTrade, 0.005)
	deff(&c.MaxPositionPct, 0.2)
	return c
}

// Input is what a playbook sees at the close of the last bar.
type Input struct {
	Instrument domain.InstrumentID
	Bars       []domain.Candle // closed bars, oldest first; the last one is "now"
	Features   []Feature       // Features(Bars), aligned to the end of Bars
	Probs      map[domain.Regime]float64
	Model      *HMM
	Equity     domain.Cents
	Cfg        Config
}

// Playbook turns a regime into an entry, or explains why not.
type Playbook interface {
	Name() string
	// Regime is the regime the playbook trades in.
	Regime() domain.Regime
	Propose(in Input) (domain.Candidate, bool, string)
}

// Playbooks in the order they are tried.
var Playbooks = []Playbook{TrendBreakout{}, MeanReversion{}}

// stopAndSize computes the stop and a risk-based size for a long entry.
func stopAndSize(in Input, entry float64) (stop float64, size domain.Cents, ok bool) {
	vol := in.Features[len(in.Features)-1].Vol
	dist := max(in.Cfg.StopSigmas*vol*entry, in.Cfg.MinStopPct*entry)
	stop = entry - dist
	if stop <= 0 || dist <= 0 {
		return 0, 0, false
	}
	eq := float64(in.Equity)
	s := min(eq*in.Cfg.RiskPerTrade/(dist/entry), eq*in.Cfg.MaxPositionPct)
	size = domain.Cents(math.Floor(s))
	return stop, size, size > 0
}

func micros(f float64) domain.Micros { return domain.Micros(math.Round(f * 1e6)) }

// TrendBreakout buys a close above the recent high while the market trends up.
type TrendBreakout struct{}

// Name of the playbook.
func (TrendBreakout) Name() string { return "trend_breakout" }

// Regime it trades in.
func (TrendBreakout) Regime() domain.Regime { return domain.RegimeTrending }

// Propose an entry.
func (p TrendBreakout) Propose(in Input) (domain.Candidate, bool, string) {
	pt := in.Probs[domain.RegimeTrending]
	if pt < in.Cfg.EntryProb {
		return domain.Candidate{}, false, fmt.Sprintf("P(trending) %.2f below %.2f", pt, in.Cfg.EntryProb)
	}
	mu := in.Model.StateMean(domain.RegimeTrending)
	if mu <= 0 {
		return domain.Candidate{}, false, "the trending state is a down-trend (long only)"
	}
	n := in.Cfg.BreakoutBars
	if len(in.Bars) < n+1 {
		return domain.Candidate{}, false, "not enough bars for a breakout"
	}
	last := in.Bars[len(in.Bars)-1]
	hi := domain.Micros(0)
	for _, b := range in.Bars[len(in.Bars)-1-n : len(in.Bars)-1] {
		hi = max(hi, b.Close)
	}
	if last.Close <= hi {
		return domain.Candidate{}, false, "no breakout above the recent high"
	}
	entry := float64(last.Close) / 1e6
	stop, size, ok := stopAndSize(in, entry)
	if !ok {
		return domain.Candidate{}, false, "no valid stop or size"
	}
	tp := entry + in.Cfg.RewardRisk*(entry-stop)
	return domain.Candidate{
		Instrument: in.Instrument, Playbook: p.Name(),
		Reason: fmt.Sprintf("P(trending) %.2f, close %s above the %d-bar high %s", pt, last.Close, n, hi),
		Entry:  last.Close, StopLossRate: micros(stop), TakeProfitRate: micros(tp), SuggestedSize: size,
		RegimeProbs: in.Probs, ExpectedEdge: micros(pt * mu * entry),
	}, true, ""
}

// MeanReversion buys a sharp dip below the recent mean while the market is range-bound.
type MeanReversion struct{}

// Name of the playbook.
func (MeanReversion) Name() string { return "mean_reversion" }

// Regime it trades in.
func (MeanReversion) Regime() domain.Regime { return domain.RegimeChoppy }

// Propose an entry.
func (p MeanReversion) Propose(in Input) (domain.Candidate, bool, string) {
	pc := in.Probs[domain.RegimeChoppy]
	if pc < in.Cfg.EntryProb {
		return domain.Candidate{}, false, fmt.Sprintf("P(choppy) %.2f below %.2f", pc, in.Cfg.EntryProb)
	}
	n := in.Cfg.ReversionBars
	if len(in.Bars) < n+1 {
		return domain.Candidate{}, false, "not enough bars for a mean"
	}
	hist := make([]float64, n)
	for i, b := range in.Bars[len(in.Bars)-1-n : len(in.Bars)-1] {
		hist[i] = float64(b.Close) / 1e6
	}
	last := in.Bars[len(in.Bars)-1]
	entry := float64(last.Close) / 1e6
	z := zscore(hist, entry)
	if z > -in.Cfg.ReversionZ {
		return domain.Candidate{}, false, fmt.Sprintf("close z-score %.2f not below -%.1f", z, in.Cfg.ReversionZ)
	}
	mean := 0.0
	for _, v := range hist {
		mean += v
	}
	mean /= float64(n)
	stop, size, ok := stopAndSize(in, entry)
	if !ok || mean <= entry {
		return domain.Candidate{}, false, "no valid stop, size or target"
	}
	return domain.Candidate{
		Instrument: in.Instrument, Playbook: p.Name(),
		Reason: fmt.Sprintf("P(choppy) %.2f, close %.2f sd below the %d-bar mean", pc, -z, n),
		Entry:  last.Close, StopLossRate: micros(stop), TakeProfitRate: micros(mean), SuggestedSize: size,
		RegimeProbs: in.Probs, ExpectedEdge: micros(pc * (mean - entry)),
	}, true, ""
}

// Engine runs the model bar by bar for one instrument: it refits the HMM every RefitEvery
// bars on the last FitWindow observations that exist at that bar, and filters in between.
// It never uses a bar after the one it is evaluating.
type Engine struct {
	cfg    Config
	model  *HMM
	fitAt  int // number of features when the model was fitted
	Refits int
}

// NewEngine builds an engine.
func NewEngine(cfg Config) *Engine { return &Engine{cfg: cfg.withDefaults(), fitAt: -1} }

// Decision is the engine's output for one bar.
type Decision struct {
	Candidate *domain.Candidate
	Probs     map[domain.Regime]float64
	Reason    string // why there is no candidate
}

// Evaluate decides at the close of the last of bars (all closed, oldest first).
func (e *Engine) Evaluate(id domain.InstrumentID, bars []domain.Candle, equity domain.Cents) Decision {
	fs := Features(bars, e.cfg.FeatureWindow)
	if len(fs) == 0 || !fs[len(fs)-1].Start.Equal(bars[len(bars)-1].Start) {
		return Decision{Reason: "not enough valid bars"}
	}
	obs := Observations(fs)
	if e.model == nil || len(fs)-e.fitAt >= e.cfg.RefitEvery || len(fs) < e.fitAt {
		from := max(0, len(obs)-e.cfg.FitWindow)
		m, err := Fit(obs[from:], FitConfig{K: e.cfg.States})
		if err != nil {
			return Decision{Reason: "regime model: " + err.Error()}
		}
		e.model, e.fitAt = m, len(fs)
		e.Refits++
	}
	from := max(0, len(obs)-e.cfg.FitWindow)
	filtered := e.model.Filter(obs[from:])
	probs := e.model.RegimeProbs(filtered[len(filtered)-1])
	if probs[domain.RegimeHighVol] >= e.cfg.HighVolStop {
		return Decision{Probs: probs, Reason: fmt.Sprintf("high volatility (P %.2f): no entries", probs[domain.RegimeHighVol])}
	}
	in := Input{Instrument: id, Bars: bars, Features: fs, Probs: probs, Model: e.model, Equity: equity, Cfg: e.cfg}
	var why []string
	for _, pb := range Playbooks {
		c, ok, reason := pb.Propose(in)
		if ok {
			return Decision{Candidate: &c, Probs: probs}
		}
		why = append(why, pb.Name()+": "+reason)
	}
	return Decision{Probs: probs, Reason: fmt.Sprint(why)}
}
