package domain

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The agent's style (PLAN R28): risk presets (a full set of limits in one choice), the
// strategy's aggressiveness and the platform's autonomy. None of them is a guardrail
// (CLAUDE.md §2): every preset passes Validate, aggressiveness only tunes the strategy inside
// the risk limits (the risk engine still clamps every entry), and autonomy never changes a
// limit, resets a kill or touches the real-mode gate.

// RiskPreset names a set of risk limits.
type RiskPreset string

// The risk presets, from the most careful to the boldest.
const (
	PresetLow        RiskPreset = "low"
	PresetLowMedium  RiskPreset = "low_medium"
	PresetMedium     RiskPreset = "medium"
	PresetMediumHigh RiskPreset = "medium_high"
	PresetHigh       RiskPreset = "high"
)

// PresetCustom is what limits that match no preset are called (never stored as a preset).
const PresetCustom RiskPreset = "custom"

// RiskPresets are the presets in order.
var RiskPresets = []RiskPreset{PresetLow, PresetLowMedium, PresetMedium, PresetMediumHigh, PresetHigh}

// PresetLimits are the limits a preset sets. The trading budget, the windows before the
// close and the freshness limits are not part of a preset (ApplyPreset keeps them safe).
type PresetLimits struct {
	DailyLoss        BasisPoints
	Drawdown         BasisPoints
	MaxPosition      BasisPoints
	MaxOpenPositions int
	MaxShort         BasisPoints
}

// presetTable is the owner's table (R28; recorded in docs/PLAN.md). Every row passes
// RiskLimits.Validate and stays within the hard limits (HardMaxOpenPositions, HardMaxShort).
var presetTable = map[RiskPreset]PresetLimits{
	PresetLow:        {DailyLoss: 50, Drawdown: 300, MaxPosition: 500, MaxOpenPositions: 3, MaxShort: 0},
	PresetLowMedium:  {DailyLoss: 100, Drawdown: 500, MaxPosition: 1_000, MaxOpenPositions: 5, MaxShort: 0},
	PresetMedium:     {DailyLoss: 200, Drawdown: 1_000, MaxPosition: 2_000, MaxOpenPositions: 8, MaxShort: 1_000},
	PresetMediumHigh: {DailyLoss: 300, Drawdown: 1_500, MaxPosition: 2_500, MaxOpenPositions: 12, MaxShort: 2_000},
	PresetHigh:       {DailyLoss: 400, Drawdown: 2_000, MaxPosition: 3_000, MaxOpenPositions: HardMaxOpenPositions, MaxShort: HardMaxShort},
}

// Valid reports whether p is one of RiskPresets.
func (p RiskPreset) Valid() bool { return slices.Contains(RiskPresets, p) }

// Label is the preset's name for people ("Low–medium").
func (p RiskPreset) Label() string {
	switch p {
	case PresetLow:
		return "Low"
	case PresetLowMedium:
		return "Low–medium"
	case PresetMedium:
		return "Medium"
	case PresetMediumHigh:
		return "Medium–high"
	case PresetHigh:
		return "High"
	}
	return "Custom"
}

// PresetFor is the limits preset p sets on an account in mode m: the table's row, and on a
// real-money account each value capped at RealCeilings (OD25). ok is false for an unknown
// preset.
func PresetFor(p RiskPreset, m TradingMode) (PresetLimits, bool) {
	v, ok := presetTable[p]
	if !ok {
		return PresetLimits{}, false
	}
	if m == ModeReal {
		c := RealCeilings
		v.DailyLoss = min(v.DailyLoss, c.DailyLoss)
		v.Drawdown = min(v.Drawdown, c.Drawdown)
		v.MaxPosition = min(v.MaxPosition, c.MaxPosition)
		v.MaxOpenPositions = min(v.MaxOpenPositions, c.MaxOpenPositions)
		v.MaxShort = min(v.MaxShort, c.MaxShort)
	}
	return v, true
}

// ApplyPreset returns cur with preset p applied for an account in mode m. The trading budget
// is kept; the no-entry and flatten windows are never shorter than the defaults (a longer
// window the owner chose is kept), and the freshness limits never looser than the defaults.
// The result is validated for the mode (ValidateFor).
func ApplyPreset(cur RiskLimits, p RiskPreset, m TradingMode) (RiskLimits, error) {
	v, ok := PresetFor(p, m)
	if !ok {
		return RiskLimits{}, Invalid("preset", "must be one of "+presetNames())
	}
	d := DefaultRiskLimits
	next := cur
	next.DailyLoss, next.Drawdown, next.MaxPosition = v.DailyLoss, v.Drawdown, v.MaxPosition
	next.MaxOpenPositions, next.MaxShort = v.MaxOpenPositions, v.MaxShort
	next.NoEntryBeforeClose = max(cur.NoEntryBeforeClose, d.NoEntryBeforeClose)
	next.FlattenBeforeClose = max(cur.FlattenBeforeClose, d.FlattenBeforeClose)
	next.MaxPortfolioAge = positiveMin(cur.MaxPortfolioAge, d.MaxPortfolioAge)
	next.MaxQuoteAge = positiveMin(cur.MaxQuoteAge, d.MaxQuoteAge)
	if err := next.ValidateFor(m); err != nil {
		return RiskLimits{}, err
	}
	return next, nil
}

// positiveMin is the smaller of a and b, ignoring a when it is not positive.
func positiveMin(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	return min(a, b)
}

// MatchPreset names the preset limits l correspond to on an account in mode m (the five
// preset fields are equal), or PresetCustom.
func MatchPreset(l RiskLimits, m TradingMode) RiskPreset {
	for _, p := range RiskPresets {
		v, _ := PresetFor(p, m)
		if l.DailyLoss == v.DailyLoss && l.Drawdown == v.Drawdown && l.MaxPosition == v.MaxPosition &&
			l.MaxOpenPositions == v.MaxOpenPositions && l.MaxShort == v.MaxShort {
			return p
		}
	}
	return PresetCustom
}

// Summary says what the preset's limits mean, in plain words.
func (v PresetLimits) Summary() string {
	shorts := "no shorts"
	if v.MaxShort > 0 {
		shorts = "shorts up to " + PercentText(v.MaxShort) + " of the budget"
	}
	return fmt.Sprintf("stops buying after a %s loss in a day, stops everything after a %s fall, each share at most %s of the account, at most %d positions, %s",
		PercentText(v.DailyLoss), PercentText(v.Drawdown), PercentText(v.MaxPosition), v.MaxOpenPositions, shorts)
}

// PercentText writes basis points as a percentage for people: 50 → "0.5 %", 2000 → "20 %".
func PercentText(bp BasisPoints) string {
	sign := ""
	if bp < 0 {
		sign, bp = "-", -bp
	}
	whole, frac := int64(bp)/100, int64(bp)%100
	s := sign + strconv.FormatInt(whole, 10)
	switch {
	case frac == 0:
	case frac%10 == 0:
		s += "." + strconv.FormatInt(frac/10, 10)
	default:
		s += "." + fmt.Sprintf("%02d", frac)
	}
	return s + " %"
}

func presetNames() string {
	names := make([]string, len(RiskPresets))
	for i, p := range RiskPresets {
		names[i] = string(p)
	}
	return strings.Join(names, ", ")
}

// Aggressiveness is how boldly the strategy proposes entries. It changes only strategy
// parameters: every entry is still sized and refused by the risk engine (AssessEntry).
type Aggressiveness string

// The aggressiveness levels, from the most careful to the boldest.
const (
	AggressivenessLow    Aggressiveness = "low"
	AggressivenessMedium Aggressiveness = "medium"
	AggressivenessHigh   Aggressiveness = "high"
	AggressivenessFull   Aggressiveness = "full"
)

// AggressivenessLevels are the levels in order.
var AggressivenessLevels = []Aggressiveness{AggressivenessLow, AggressivenessMedium, AggressivenessHigh, AggressivenessFull}

// DefaultAggressiveness is today's behaviour (the strategy's defaults).
const DefaultAggressiveness = AggressivenessMedium

// AggressivenessParams are the strategy parameters of a level, as basis points (the
// strategy converts them to its float parameters).
type AggressivenessParams struct {
	// RiskPerTrade is the share of equity lost if the stop is hit (50 = 0.5 %).
	RiskPerTrade BasisPoints
	// EntryProb is the probability a regime must reach for a playbook to act (6000 = 0.60).
	EntryProb BasisPoints
	// MaxPosition is the largest size the strategy suggests as a share of equity; the risk
	// limit "largest position" still caps it.
	MaxPosition BasisPoints
}

var aggressivenessTable = map[Aggressiveness]AggressivenessParams{
	AggressivenessLow:    {RiskPerTrade: 25, EntryProb: 7_000, MaxPosition: 1_000},
	AggressivenessMedium: {RiskPerTrade: 50, EntryProb: 6_000, MaxPosition: 2_000},
	AggressivenessHigh:   {RiskPerTrade: 100, EntryProb: 5_500, MaxPosition: 3_000},
	AggressivenessFull:   {RiskPerTrade: 150, EntryProb: 5_000, MaxPosition: 4_000},
}

// Valid reports whether a is one of AggressivenessLevels.
func (a Aggressiveness) Valid() bool { return slices.Contains(AggressivenessLevels, a) }

// Params are the level's strategy parameters (an unknown level: the default's).
func (a Aggressiveness) Params() AggressivenessParams {
	if p, ok := aggressivenessTable[a]; ok {
		return p
	}
	return aggressivenessTable[DefaultAggressiveness]
}

// Down is the level steps below a (never below low; an unknown level counts as the default).
func (a Aggressiveness) Down(steps int) Aggressiveness {
	i := slices.Index(AggressivenessLevels, a)
	if i < 0 {
		i = slices.Index(AggressivenessLevels, DefaultAggressiveness)
	}
	return AggressivenessLevels[max(0, i-max(0, steps))]
}

// Summary says what the level does, in plain words.
func (a Aggressiveness) Summary() string {
	p := a.Params()
	return fmt.Sprintf("risks %s of the account per trade, acts when a market mood is at least %s likely, suggests at most %s of the account per share (the risk limits still cap it)",
		PercentText(p.RiskPerTrade), PercentText(p.EntryProb), PercentText(p.MaxPosition))
}

// AggressivenessAdapt is full autonomy's adjustment of the aggressiveness (R28): one step
// down after a risk day on which the daily loss latch was set, one step back up after three
// clean risk days on which the agent ran, never above the level the owner chose.
type AggressivenessAdapt struct {
	Down      int      // steps below the chosen level
	CleanDays int      // clean days the agent ran on since the last step
	Folded    RiskDate // the last risk day folded in
	RanOn     RiskDate // the last risk day on which the agent ran
}

// AdaptCleanDays is how many clean days bring the aggressiveness one step back up.
const AdaptCleanDays = 3

// EndOfDay folds a finished risk day in: latched says whether the daily loss latch was set
// on it. A day already folded (or an earlier one) changes nothing. A day without a latch on
// which the agent did not run neither counts nor breaks the run of clean days.
func (a AggressivenessAdapt) EndOfDay(day RiskDate, latched bool) AggressivenessAdapt {
	if day == "" || day <= a.Folded {
		return a
	}
	a.Folded = day
	switch {
	case latched:
		a.Down, a.CleanDays = min(a.Down+1, len(AggressivenessLevels)-1), 0
	case a.RanOn == day && a.Down > 0:
		a.CleanDays++
		if a.CleanDays >= AdaptCleanDays {
			a.Down, a.CleanDays = a.Down-1, 0
		}
	}
	return a
}

// Autonomy is how much the platform does by itself.
type Autonomy string

// The autonomy levels.
const (
	AutonomyLow    Autonomy = "low"
	AutonomyMedium Autonomy = "medium"
	AutonomyHigh   Autonomy = "high"
	AutonomyFull   Autonomy = "full"
)

// AutonomyLevels are the levels in order.
var AutonomyLevels = []Autonomy{AutonomyLow, AutonomyMedium, AutonomyHigh, AutonomyFull}

// DefaultAutonomy is the owner's default (R28): medium.
const DefaultAutonomy = AutonomyMedium

// Valid reports whether a is one of AutonomyLevels.
func (a Autonomy) Valid() bool { return slices.Contains(AutonomyLevels, a) }

// ResearchMode is what the scanner and the web research may do with the shares they find.
type ResearchMode string

// The research modes.
const (
	ResearchOff     ResearchMode = "off"      // nothing (no account, or a view-only one)
	ResearchPropose ResearchMode = "propose"  // findings are proposals the owner accepts
	ResearchAutoAdd ResearchMode = "auto_add" // findings are added to the watchlist, with an event
)

// Scan intervals per autonomy level.
const (
	ScanEveryDefault = 15 * time.Minute
	ScanEveryOften   = 5 * time.Minute
)

// StaleWatchTradingDays is how many trading days without a signal take a share the scanner
// or the research added off the watchlist at high and full autonomy.
const StaleWatchTradingDays = 10

// AutonomyPolicy is what an autonomy level lets the platform do by itself.
type AutonomyPolicy struct {
	// Research is what the scanner and the web research do with their findings.
	Research ResearchMode
	// ScanEvery is how often the scanner (and the research) runs while a market is open.
	ScanEvery time.Duration
	// OwnerSharesOnly: the agent trades only shares a person put on the watchlist.
	OwnerSharesOnly bool
	// AutoAssignThemes: shares added automatically get the closest theme (R27 scoring).
	AutoAssignThemes bool
	// PruneStale: shares added automatically leave the watchlist after
	// StaleWatchTradingDays trading days without a signal (never shares a person added).
	PruneStale bool
	// AutoStart: a stopped agent starts itself at the first market open of a trading day
	// (demo only, never when killed; OD26: real agents never start by themselves).
	AutoStart bool
	// AdaptAggressiveness: one step down after a daily-loss latch day, back up after
	// AdaptCleanDays clean days (never above the chosen level).
	AdaptAggressiveness bool
}

// Policy is the level's policy for an account in mode m (an unknown level: the default's).
// A real-money account never starts itself.
func (a Autonomy) Policy(m TradingMode) AutonomyPolicy {
	if !a.Valid() {
		a = DefaultAutonomy
	}
	p := AutonomyPolicy{Research: ResearchAutoAdd, ScanEvery: ScanEveryDefault}
	switch a {
	case AutonomyLow:
		p.Research, p.OwnerSharesOnly = ResearchPropose, true
	case AutonomyHigh, AutonomyFull:
		p.ScanEvery, p.AutoAssignThemes, p.PruneStale = ScanEveryOften, true, true
		if a == AutonomyFull {
			p.AutoStart, p.AdaptAggressiveness = m == ModeDemo, true
		}
	}
	return p
}

// Summary says what the level does, in plain words.
func (a Autonomy) Summary() string {
	switch a {
	case AutonomyLow:
		return "The agent trades only shares you put on the watchlist. The scanner and the web research only propose shares; you accept them."
	case AutonomyHigh:
		return "As medium, plus the scanner and the research run every 5 minutes, new shares get the closest theme, and shares they added that showed no signal for 10 trading days leave the watchlist (never shares you added)."
	case AutonomyFull:
		return "As high, plus a stopped demo agent starts itself at the first market open of each trading day (never when the kill switch is on), and the aggressiveness drops one step after a day that hit the daily loss limit and comes back after 3 clean days. It never changes the risk limits, resets a kill or touches real trading."
	}
	return "The scanner and the web research add shares to the watchlist themselves, with an event; the agent trades the whole watchlist."
}

// IsAutoAdded reports whether a watchlist share was added by the scanner or the web
// research rather than by a person (WatchItem.AddedBy).
func IsAutoAdded(addedBy string) bool {
	return addedBy == WatchAddedByScanner || addedBy == WatchAddedByResearch
}

// The AddedBy names of the automatic watchlist sources.
const (
	WatchAddedByScanner  = "scanner"
	WatchAddedByResearch = "research"
)

// RegimeText is a market regime for people: a short name, what it means and what the agent
// may do in it.
type RegimeText struct {
	Regime      Regime
	Label       string
	Description string
	AgentMayDo  string
}

// RegimeLegend is the regimes for people, in the order the dashboard shows them.
var RegimeLegend = []RegimeText{
	{RegimeTrending, "Trending", "moving steadily one way", "the agent may buy breakouts or short breakdowns"},
	{RegimeChoppy, "Choppy", "going sideways", "the agent may buy dips back to the average"},
	{RegimeHighVol, "Volatile", "big swings", "the agent stays out"},
}

// RegimeTextOf is regime r for people (ok false for an unknown regime).
func RegimeTextOf(r Regime) (RegimeText, bool) {
	i := slices.IndexFunc(RegimeLegend, func(t RegimeText) bool { return t.Regime == r })
	if i < 0 {
		return RegimeText{Regime: r, Label: string(r)}, false
	}
	return RegimeLegend[i], true
}
