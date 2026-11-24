package strategy

import (
	"fmt"
	"strings"

	"github.com/albievan/cactustrade/internal/domain"
)

// ConfigFor is the strategy configuration of an aggressiveness level (PLAN R28): only the
// risk per trade, the entry probability and the largest suggested size change. The
// volatility gate (HighVolStop) and every stop rule stay the same at every level, and the
// risk engine still sizes and refuses every entry.
func ConfigFor(a domain.Aggressiveness, shorts bool) Config {
	p := a.Params()
	return Config{
		Shorts:         shorts,
		RiskPerTrade:   float64(p.RiskPerTrade) / 10_000,
		EntryProb:      float64(p.EntryProb) / 10_000,
		MaxPositionPct: float64(p.MaxPosition) / 10_000,
	}
}

// Stance is what the agent may do in a share's current market mood.
type Stance string

// The stances.
const (
	StanceStayOut   Stance = "stay_out"  // volatile: no entries
	StanceTrend     Stance = "trend"     // trending: breakouts (and breakdowns when shorts are on)
	StanceReversion Stance = "reversion" // choppy: dips back to the average (and spikes when shorts are on)
	StanceBoth      Stance = "both"      // both moods pass the threshold
	StanceWait      Stance = "wait"      // no mood is clear enough
	StanceUnknown   Stance = "unknown"   // not reviewed yet
)

// MayDo says which playbooks may act on regime probabilities probs at the thresholds of cfg
// (the same order and gates as Engine.Evaluate), in plain words. It decides nothing: whether
// a playbook proposes an entry also depends on the price (a breakout, a dip).
func MayDo(probs map[domain.Regime]float64, cfg Config) (Stance, string) {
	if len(probs) == 0 {
		return StanceUnknown, "Not reviewed yet"
	}
	c := cfg.withDefaults()
	pct := func(v float64) string { return fmt.Sprintf("%.0f %%", v*100) }
	if hv := probs[domain.RegimeHighVol]; hv >= c.HighVolStop {
		return StanceStayOut, fmt.Sprintf("Stays out: volatile (%s, stays out from %s)", pct(hv), pct(c.HighVolStop))
	}
	trend := probs[domain.RegimeTrending] >= c.EntryProb
	chop := probs[domain.RegimeChoppy] >= c.EntryProb
	var parts []string
	if trend {
		s := "buy a breakout"
		if c.Shorts {
			s += " or short a breakdown"
		}
		parts = append(parts, s)
	}
	if chop {
		s := "buy a dip back to the average"
		if c.Shorts {
			s += " or short a spike"
		}
		parts = append(parts, s)
	}
	switch {
	case trend && chop:
		return StanceBoth, "May " + strings.Join(parts, "; or ")
	case trend:
		return StanceTrend, "May " + parts[0]
	case chop:
		return StanceReversion, "May " + parts[0]
	}
	return StanceWait, fmt.Sprintf("Waits: no mood is clear enough (needs %s)", pct(c.EntryProb))
}
