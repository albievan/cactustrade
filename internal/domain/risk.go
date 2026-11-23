package domain

import (
	"fmt"
	"time"
)

// BasisPoints is a fraction in 1/10 000 (100 bp = 1 %).
type BasisPoints int64

// of returns bp of c, rounded down (never rounds a limit up).
func (bp BasisPoints) of(c Cents) Cents { return Cents(int64(c) * int64(bp) / 10_000) }

// RiskLimits are the owner's limits (CLAUDE.md §2, brief §3). They can be changed only while
// the agent is stopped, and every change is audited (phase 4.5).
type RiskLimits struct {
	// TradingBudget is the most the agent may have invested at once (open positions,
	// pending orders and reservations together). It can never exceed account equity.
	TradingBudget Cents
	// DailyLoss stops new entries for the day when equity falls this far below the
	// start-of-day equity (default 200 bp = 2 %).
	DailyLoss BasisPoints
	// Drawdown trips the kill switch when equity falls this far below its high-water mark
	// (default 1000 bp = 10 %); only an admin can reset it.
	Drawdown BasisPoints
	// MaxPosition caps one position as a share of equity (default 2000 bp = 20 %; 0 = off).
	MaxPosition BasisPoints
	// MaxOpenPositions caps open positions plus pending orders (default 8, D12: the kill
	// switch must flatten everything within eToro's 20-per-minute execution quota).
	MaxOpenPositions int
	// NoEntryBeforeClose: no entries this close to the session's end (default 30 min).
	NoEntryBeforeClose time.Duration
	// FlattenBeforeClose: everything is closed this long before the end (default 10 min).
	FlattenBeforeClose time.Duration
	// MaxPortfolioAge: the cash figure must be at most this old (default 5 s).
	MaxPortfolioAge time.Duration
	// MaxQuoteAge: the entry price must be at most this old (default 30 s).
	MaxQuoteAge time.Duration
	// MaxShort caps all short exposure (open shorts and short orders) as a share of the
	// trading budget: at most HardMaxShort (owner's decision D19); 0 = no shorts.
	MaxShort BasisPoints
}

// DefaultRiskLimits are the brief's defaults; the trading budget starts at zero (nothing
// can be traded until the owner sets one).
var DefaultRiskLimits = RiskLimits{
	DailyLoss: 200, Drawdown: 1000, MaxPosition: 2000, MaxOpenPositions: 8,
	NoEntryBeforeClose: 30 * time.Minute, FlattenBeforeClose: 10 * time.Minute,
	MaxPortfolioAge: 5 * time.Second, MaxQuoteAge: 30 * time.Second, MaxShort: HardMaxShort,
}

// HardMaxShort is the owner's ceiling on short exposure: 25 % of the trading budget (D19).
const HardMaxShort BasisPoints = 2_500

// HardMaxOpenPositions is the ceiling for MaxOpenPositions: with eToro's execution quota of
// 20 per minute shared by opens, closes and cancels, more could not be flattened at once.
const HardMaxOpenPositions = 15

// Validate checks limits an admin entered. Nothing here can enable leverage, shorts or CFDs:
// those are not limits, they are constants of the eToro adapter.
func (l RiskLimits) Validate() error {
	switch {
	case l.TradingBudget < 0:
		return Invalid("tradingBudgetCents", "must not be negative")
	case l.DailyLoss < 10 || l.DailyLoss > 2_000:
		return Invalid("dailyLossBp", "must be 10-2000 (0.1 %-20 %)")
	case l.Drawdown < 50 || l.Drawdown > 5_000:
		return Invalid("drawdownBp", "must be 50-5000 (0.5 %-50 %)")
	case l.MaxPosition < 0 || l.MaxPosition > 10_000:
		return Invalid("maxPositionBp", "must be 0 (off) to 10000")
	case l.MaxOpenPositions < 1 || l.MaxOpenPositions > HardMaxOpenPositions:
		return Invalid("maxOpenPositions", fmt.Sprintf("must be 1-%d", HardMaxOpenPositions))
	case l.FlattenBeforeClose < 5*time.Minute || l.FlattenBeforeClose > 2*time.Hour:
		return Invalid("flattenBeforeCloseMinutes", "must be 5-120")
	case l.NoEntryBeforeClose < l.FlattenBeforeClose || l.NoEntryBeforeClose > 4*time.Hour:
		return Invalid("noEntryBeforeCloseMinutes", "must be at least the flatten time and at most 240")
	case l.MaxPortfolioAge <= 0 || l.MaxPortfolioAge > time.Minute:
		return Invalid("maxPortfolioAge", "must be 1-60 seconds")
	case l.MaxQuoteAge <= 0 || l.MaxQuoteAge > 5*time.Minute:
		return Invalid("maxQuoteAge", "must be 1-300 seconds")
	case l.MaxShort < 0 || l.MaxShort > HardMaxShort:
		return Invalid("maxShortBp", "must be 0 (no shorts) to 2500 (25 % of the budget)")
	}
	return nil
}

// RiskState is what the risk engine remembers between decisions.
type RiskState struct {
	StartOfDayEquity Cents // equity at the session's first check
	HighWaterMark    Cents // highest equity seen
	Killed           bool  // drawdown or manual kill: only an admin reset clears it
	Reserved         Cents // cash reserved for orders sent but not yet in the portfolio
	ReservedOrders   int   // how many such orders
	ReservedShort    Cents // the part of Reserved that is for short orders
}

// RiskInput is everything one entry decision depends on. All of it is passed in: the engine
// reads no clock and calls no broker.
type RiskInput struct {
	Now          time.Time
	SessionOpen  time.Time // the instrument's market session today
	SessionClose time.Time
	Trading      bool // the market trades today (calendar); false = closed
	Portfolio    Portfolio
	Instrument   InstrumentID
	Direction    Direction
	Eligibility  Eligibility
	Entry        Micros // the price the size is based on
	QuoteAt      time.Time
	StopLoss     Micros
	Requested    Cents // the playbook's (or the reviewer's, never larger) size
	Limits       RiskLimits
	State        RiskState
	Reconciled   bool // start-up / periodic reconciliation passed (4.3)
}

// RiskDecision is the engine's final say on an entry.
type RiskDecision struct {
	Allowed bool
	Size    Cents
	Reason  string
	// Kill is set when the drawdown limit is breached: the caller trips the kill switch.
	Kill bool
}

func deny(reason string, args ...any) RiskDecision {
	return RiskDecision{Reason: fmt.Sprintf(reason, args...)}
}

// AssessEntry decides whether a long entry may be sent and how large it may be. It fails
// closed: any missing, stale or inconsistent input is a refusal. The size it returns is never
// larger than requested and never more than
//
//	min(available cash − reserved, remaining trading budget, position cap, loss allowance)
//
// where the loss allowance keeps a stop-out from breaching the daily loss limit.
func AssessEntry(in RiskInput) RiskDecision {
	l, st, pf := in.Limits, in.State, in.Portfolio
	equity := pf.Equity()
	switch {
	case st.Killed:
		return deny("the kill switch is on (admin reset required)")
	case !in.Reconciled:
		return deny("the account is not reconciled")
	case l.Validate() != nil:
		return deny("the risk limits are invalid: %v", l.Validate())
	case l.TradingBudget <= 0:
		return deny("no trading budget is set")
	case pf.AsOf.IsZero() || in.Now.Sub(pf.AsOf) > l.MaxPortfolioAge || pf.AsOf.After(in.Now.Add(time.Second)):
		return deny("the account figures are stale (fetch the portfolio again)")
	case in.QuoteAt.IsZero() || in.Now.Sub(in.QuoteAt) > l.MaxQuoteAge || in.QuoteAt.After(in.Now.Add(time.Second)):
		return deny("the price is stale")
	case !in.Trading || in.Now.Before(in.SessionOpen) || !in.Now.Before(in.SessionClose):
		return deny("the market is closed")
	case !in.Now.Before(in.SessionClose.Add(-l.NoEntryBeforeClose)):
		return deny("too close to the close (no entries in the last %s)", l.NoEntryBeforeClose)
	case equity <= 0 || st.StartOfDayEquity <= 0 || st.HighWaterMark <= 0:
		return deny("no start-of-day equity or high-water mark yet")
	}
	if equity <= st.HighWaterMark-l.Drawdown.of(st.HighWaterMark) {
		return RiskDecision{Reason: fmt.Sprintf("equity %s is %s below the high-water mark %s: kill switch", equity, st.HighWaterMark-equity, st.HighWaterMark), Kill: true}
	}
	dayFloor := st.StartOfDayEquity - l.DailyLoss.of(st.StartOfDayEquity)
	if equity <= dayFloor {
		return deny("daily loss limit reached (equity %s, floor %s)", equity, dayFloor)
	}
	for _, p := range pf.Positions {
		if !p.Conforms() {
			return deny("position %d is not a real share, long, leverage 1 (close it first)", p.ID)
		}
		if p.Instrument == in.Instrument {
			return deny("a position in this instrument is already open")
		}
	}
	if n := len(pf.Positions) + st.ReservedOrders; n >= l.MaxOpenPositions {
		return deny("%d positions and orders open (cap %d)", n, l.MaxOpenPositions)
	}
	if !in.Direction.Valid() {
		return deny("unknown direction")
	}
	if ok, why := in.Eligibility.Allows(in.Direction); !ok || in.Eligibility.Instrument != in.Instrument {
		if in.Direction == Short {
			return deny("the instrument is not confirmed shortable as a CFD at leverage 1 %s", why)
		}
		return deny("the instrument is not confirmed tradable as a real share, long, leverage 1 %s", why)
	}
	switch {
	case in.Entry <= 0 || in.StopLoss <= 0:
		return deny("an entry needs a price and a stop")
	case in.Direction == Long && in.StopLoss >= in.Entry:
		return deny("a long entry needs a stop below the entry price")
	case in.Direction == Short && in.StopLoss <= in.Entry:
		return deny("a short entry needs a stop above the entry price")
	}
	if in.Requested <= 0 {
		return deny("nothing requested")
	}

	size := in.Requested
	cash := pf.AvailableCash - st.Reserved
	budget := min(l.TradingBudget, equity)
	invested := pf.PendingOpen + st.Reserved
	for _, p := range pf.Positions {
		invested += p.Amount
	}
	remaining := budget - invested
	size = min(size, cash, remaining)
	if l.MaxPosition > 0 {
		size = min(size, l.MaxPosition.of(equity))
	}
	if in.Direction == Short {
		shortOpen := st.ReservedShort
		for _, p := range pf.Positions {
			if !p.Long {
				shortOpen += p.Amount
			}
		}
		if left := l.MaxShort.of(budget) - shortOpen; left < size {
			size = left
		}
	}
	// A stop-out must not take equity below the daily floor: loss ≈ size × |stop−entry|/entry.
	allowance := equity - dayFloor
	stopDist := int64(in.Entry - in.StopLoss)
	if stopDist < 0 {
		stopDist = -stopDist
	}
	if maxBySto := Cents(int64(allowance) * int64(in.Entry) / stopDist); maxBySto < size {
		size = maxBySto
	}
	switch {
	case size <= 0:
		return deny("no cash, budget, short allowance or loss allowance left (cash %s, budget left %s)", cash, remaining)
	case in.Eligibility.MinAmount(in.Direction) > 0 && size < in.Eligibility.MinAmount(in.Direction):
		return deny("the size %s is below eToro's minimum %s", size, in.Eligibility.MinAmount(in.Direction))
	}
	return RiskDecision{Allowed: true, Size: size}
}

// FlattenDue reports whether open positions must be closed now (the session ends within
// FlattenBeforeClose, or the market is not trading).
func FlattenDue(now, sessionClose time.Time, trading bool, l RiskLimits) bool {
	return !trading || !now.Before(sessionClose.Add(-l.FlattenBeforeClose))
}

// Observe updates the start-of-day equity and the high-water mark with a fresh equity
// figure. newDay is true at the first observation of a trading day.
func (st RiskState) Observe(equity Cents, newDay bool) RiskState {
	if newDay || st.StartOfDayEquity <= 0 {
		st.StartOfDayEquity = equity
	}
	st.HighWaterMark = max(st.HighWaterMark, equity)
	return st
}
