package domain

import (
	"fmt"
	"slices"
	"time"
)

// BasisPoints is a fraction in 1/10 000 (100 bp = 1 %).
type BasisPoints int64

// of returns bp of c, truncated towards zero (never rounds a limit up). The product is
// formed in 128 bits, so huge amounts never wrap (PLAN 6.1); the result saturates at the
// int64 range, which it can only reach for bp above 10 000.
func (bp BasisPoints) of(c Cents) Cents { return Cents(mulDivTrunc(int64(c), int64(bp), 10_000)) }

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
	// MinOrderValue is the smallest entry the agent sends, in the account's minor units (D41 f,
	// T212-m). Trading 212 documents no minimum, so on a Trading 212 account the owner sets
	// one (required); on eToro it is 0 (eToro states its own minimum per instrument).
	MinOrderValue Cents
	// T212Fractional allows fractional share quantities on a Trading 212 account (D43). Off
	// by default, refused on eToro, and refused while T212FractionsVerified is false.
	T212Fractional bool
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
	case l.MinOrderValue < 0:
		return Invalid("minOrderValueCents", "must not be negative")
	}
	return nil
}

// ValidateForPlatform checks the limits' platform rules (T212-e, T212-m); Validate and
// ValidateFor check the rest. eToro: no minimum order value (eToro states its own) and no
// fractional switch. Trading 212: no shorts (MaxShort 0, long only, D41 b), a minimum order
// value above 0 and at most the trading budget (with a budget of 0 nothing is entered, so it
// may stay 0), and the fractional switch only once
// T212FractionsVerified (D43). Any other platform has no limits to set (view-only).
func (l RiskLimits) ValidateForPlatform(p Platform) error {
	switch p {
	case PlatformEtoro, "":
		switch {
		case l.MinOrderValue != 0:
			return Invalid("minOrderValueCents", "must be 0 on an eToro account (eToro states its own minimum)")
		case l.T212Fractional:
			return Invalid("t212Fractional", "applies to Trading 212 accounts only")
		}
		return nil
	case PlatformTrading212:
		switch {
		case l.MaxShort != 0:
			return Invalid("maxShortBp", "must be 0 on a Trading 212 account (long only)")
		case l.T212Fractional && !T212FractionsVerified:
			// Before the budget-0 case: the switch stays locked whatever the budget (review e).
			return Invalid("t212Fractional", "stays off until Trading 212's fraction and stop rules are verified on a practice account")
		case l.TradingBudget == 0 && l.MinOrderValue >= 0:
			// No budget: nothing is entered, so the minimum may stay unset or exceed it (review of R45.5).
		case l.MinOrderValue <= 0:
			return Invalid("minOrderValueCents", "is required on a Trading 212 account (it documents no minimum)")
		case l.MinOrderValue > l.TradingBudget:
			return Invalid("minOrderValueCents", "must not exceed the trading budget")
		}
		return nil
	}
	return Invalid("platform", "cactusTrade does not trade "+p.Label()+" accounts")
}

// DefaultRealRiskLimits are the defaults of a real-money account (OD25, PLAN 7.11): stricter
// than demo, no trading budget and no shorts until the owner sets them.
var DefaultRealRiskLimits = RiskLimits{
	DailyLoss: 100, Drawdown: 500, MaxPosition: 1000, MaxOpenPositions: 4,
	NoEntryBeforeClose: 30 * time.Minute, FlattenBeforeClose: 10 * time.Minute,
	MaxPortfolioAge: 5 * time.Second, MaxQuoteAge: 30 * time.Second, MaxShort: 0,
}

// DefaultLimitsFor is the default limits of an account in mode m (real: DefaultRealRiskLimits;
// anything else: DefaultRiskLimits).
func DefaultLimitsFor(m TradingMode) RiskLimits {
	if m == ModeReal {
		return DefaultRealRiskLimits
	}
	return DefaultRiskLimits
}

// LimitCeilings are the most permissive values a set of limits may take on top of Validate.
// TradingBudget 0 means no cap on the budget (the budget is still capped by equity).
type LimitCeilings struct {
	TradingBudget    Cents
	DailyLoss        BasisPoints
	Drawdown         BasisPoints
	MaxPosition      BasisPoints // a real account must also have one (0 = off is refused)
	MaxOpenPositions int
	MaxShort         BasisPoints
}

// RealCeilings are the ceilings of a real-money account (OD25): daily loss 2 %, drawdown
// 10 %, one position at most 20 % of equity, at most 8 positions, shorts at most 25 % of
// the budget (D19). No budget cap unless the deployment sets one (WithBudgetCap).
var RealCeilings = LimitCeilings{DailyLoss: 200, Drawdown: 1000, MaxPosition: 2000, MaxOpenPositions: 8, MaxShort: HardMaxShort}

// WithBudgetCap returns c with the trading budget capped at budget (0 = no cap): the
// optional deployment cap on a real account's budget (OD25).
func (c LimitCeilings) WithBudgetCap(budget Cents) LimitCeilings {
	c.TradingBudget = budget
	return c
}

// ValidateFor checks limits for an account in mode m: Validate, and for a real-money account
// also RealCeilings (OD25).
func (l RiskLimits) ValidateFor(m TradingMode) error {
	if m != ModeReal {
		return l.Validate()
	}
	return l.ValidateWithin(RealCeilings)
}

// ValidateWithin checks limits with Validate and then against the ceilings c: every field
// over its ceiling is refused, and a position cap is required (0 = off is above any ceiling).
func (l RiskLimits) ValidateWithin(c LimitCeilings) error {
	if err := l.Validate(); err != nil {
		return err
	}
	switch {
	case c.TradingBudget > 0 && l.TradingBudget > c.TradingBudget:
		return Invalid("tradingBudgetCents", fmt.Sprintf("must not exceed $%s on a real-money account (deployment cap)", c.TradingBudget))
	case l.DailyLoss > c.DailyLoss:
		return Invalid("dailyLossBp", fmt.Sprintf("must be at most %d on a real-money account", c.DailyLoss))
	case l.Drawdown > c.Drawdown:
		return Invalid("drawdownBp", fmt.Sprintf("must be at most %d on a real-money account", c.Drawdown))
	case l.MaxPosition <= 0 || l.MaxPosition > c.MaxPosition:
		return Invalid("maxPositionBp", fmt.Sprintf("must be 1-%d on a real-money account (no position without a cap)", c.MaxPosition))
	case l.MaxOpenPositions > c.MaxOpenPositions:
		return Invalid("maxOpenPositions", fmt.Sprintf("must be at most %d on a real-money account", c.MaxOpenPositions))
	case l.MaxShort > c.MaxShort:
		return Invalid("maxShortBp", fmt.Sprintf("must be at most %d on a real-money account", c.MaxShort))
	}
	return nil
}

// RaisesAny reports whether next is more permissive than old in any field: a larger budget,
// daily loss, drawdown, position cap (or the cap switched off), position count, short
// share, portfolio or quote age, or a shorter no-entry or flatten window. On an armed
// real-money account any such change disarms the gate (OD24, PLAN 7.11).
func RaisesAny(old, next RiskLimits) bool {
	posCap := func(bp BasisPoints) BasisPoints { // 0 = off = no cap at all
		if bp <= 0 {
			return BasisPoints(1 << 62)
		}
		return bp
	}
	return next.TradingBudget > old.TradingBudget ||
		next.DailyLoss > old.DailyLoss ||
		next.Drawdown > old.Drawdown ||
		posCap(next.MaxPosition) > posCap(old.MaxPosition) ||
		next.MaxOpenPositions > old.MaxOpenPositions ||
		next.MaxShort > old.MaxShort ||
		next.MaxPortfolioAge > old.MaxPortfolioAge ||
		next.MaxQuoteAge > old.MaxQuoteAge ||
		next.NoEntryBeforeClose < old.NoEntryBeforeClose ||
		next.FlattenBeforeClose < old.FlattenBeforeClose
}

// EligibilityMaxAge is how old an eligibility check may be when an entry is decided: older
// answers are refused (eToro can withdraw a share's real/long or CFD/short offer).
const EligibilityMaxAge = 24 * time.Hour

// RiskDayStartHourUTC is the hour (UTC) at which a risk day begins (OD4). At 22:00 UTC no LSE,
// NYSE or ASX session is open in any season: New York has closed (20:00/21:00 UTC) and
// Sydney has not opened (23:00/00:00 UTC).
const RiskDayStartHourUTC = 22

// RiskDate names a risk day as "YYYY-MM-DD": the UTC date of the day that begins at the
// 22:00 UTC before it. Dates of this form compare in time order as strings.
type RiskDate string

// RiskDay returns the risk day containing t. A risk day runs from 22:00 UTC to 22:00 UTC the
// next day and is named after the later of the two dates, so the ASX session that opens at
// 23:00 UTC on Sunday belongs to Monday's risk day together with London and New York.
func RiskDay(t time.Time) RiskDate {
	return RiskDate(t.UTC().Add((24 - RiskDayStartHourUTC) * time.Hour).Format("2006-01-02"))
}

// RiskState is what the risk engine remembers between decisions.
type RiskState struct {
	Day              RiskDate // the risk day StartOfDayEquity and DayStopped belong to
	StartOfDayEquity Cents    // equity at the risk day's first observation
	HighWaterMark    Cents    // highest equity seen
	// DayStopped is the daily latch: set once equity reaches the daily floor, it refuses every
	// entry until the next risk day, even if equity recovers.
	DayStopped     bool
	Killed         bool  // drawdown or manual kill: only an admin reset clears it
	Reserved       Cents // cash reserved for orders sent but not yet in the portfolio
	ReservedOrders int   // how many such orders
	ReservedShort  Cents // the part of Reserved that is for short orders
}

// DailyFloor is the equity at or below which no entry is allowed for the rest of the risk day.
func DailyFloor(st RiskState, l RiskLimits) Cents {
	return st.StartOfDayEquity - l.DailyLoss.of(st.StartOfDayEquity)
}

// DrawdownBreached reports whether equity has fallen to or below the drawdown limit under the
// high-water mark: the caller must trip the kill switch. Used by AssessEntry and by the
// supervisor on every tick. With no high-water mark yet it is false (AssessEntry refuses
// such a state separately).
func DrawdownBreached(equity Cents, st RiskState, l RiskLimits) bool {
	hwm := max(st.HighWaterMark, equity)
	return hwm > 0 && equity <= hwm-l.Drawdown.of(hwm)
}

// Observe folds a fresh equity figure into the state. When day is a later risk day than the
// state's, the start-of-day equity is reset to equity and the daily latch is cleared; an
// earlier day (a clock going backwards) never resets anything (fail closed). The high-water
// mark only rises. Once equity is at or below the daily floor the latch (DayStopped) is set
// and stays set until the next risk day.
func (st RiskState) Observe(equity Cents, day RiskDate, l RiskLimits) RiskState {
	if st.Day == "" || day > st.Day || st.StartOfDayEquity <= 0 {
		if st.Day == "" || day > st.Day {
			st.Day, st.DayStopped = day, false
		}
		st.StartOfDayEquity = equity
	}
	st.HighWaterMark = max(st.HighWaterMark, equity)
	if equity <= DailyFloor(st, l) {
		st.DayStopped = true
	}
	return st
}

// RiskInput is everything one entry decision depends on. All of it is passed in: the engine
// reads no clock and calls no broker.
type RiskInput struct {
	Now  time.Time
	Mode TradingMode // the account's mode: demo, or real with a valid entry permit
	// AccountID and Permit: a real-money entry needs an entry permit for this account that is
	// valid at Now (PermitEntry, PLAN 7.19). A demo entry ignores both.
	AccountID    string
	Permit       RealPermit
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
	Reconciled   bool // start-up / periodic reconciliation passed (4.8)
	// PendingInstruments have an order of ours in flight (any non-final order): no second
	// entry until it is resolved.
	PendingInstruments []InstrumentID
	// UnknownOrders counts our orders whose outcome is unknown (OD2): entries stay blocked
	// until every one is resolved.
	UnknownOrders int
	// Blocked: the instrument is blocked for this account (e.g. a fill that did not conform).
	Blocked bool
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

// AssessEntry decides whether an entry (long or short) may be sent and how large it may be. It fails
// closed: any missing, stale or inconsistent input is a refusal. A real-money entry also needs a
// valid entry permit for the account (RiskInput.Permit) and limits within RealCeilings (S1,
// PLAN 7.19); any other mode is refused. The size it returns is never
// larger than requested and never more than
//
//	min(available cash − reserved, remaining trading budget, position cap, loss allowance)
//
// where the loss allowance keeps a stop-out from breaching the daily loss limit.
func AssessEntry(in RiskInput) RiskDecision {
	l, st, pf := in.Limits, in.State, in.Portfolio
	equity := pf.Equity()
	switch {
	case in.Mode != ModeDemo && in.Mode != ModeReal:
		return deny("real mode is gated (phase 7): unknown trading mode")
	case in.Mode == ModeReal && !in.Permit.AllowsEntry(in.AccountID, in.Now):
		return deny("real mode is gated (phase 7): no valid real-trading permit (armed gate, opt-in, fresh permit)")
	case in.Mode == ModeReal && l.ValidateFor(ModeReal) != nil:
		return deny("real mode is gated (phase 7): the limits are not within the real-money ceilings: %v", l.ValidateFor(ModeReal))
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
	case st.Day != RiskDay(in.Now):
		return deny("the risk state is not from today's risk day (observe the equity first)")
	}
	if DrawdownBreached(equity, st, l) {
		return RiskDecision{Reason: fmt.Sprintf("equity %s is %s below the high-water mark %s: kill switch", equity, st.HighWaterMark-equity, st.HighWaterMark), Kill: true}
	}
	dayFloor := DailyFloor(st, l)
	switch {
	case st.DayStopped:
		return deny("daily loss limit reached today: no entries until the next risk day")
	case equity <= dayFloor:
		return deny("daily loss limit reached (equity %s, floor %s)", equity, dayFloor)
	case in.UnknownOrders > 0:
		return deny("%d order(s) with an unknown outcome: no entries until resolved", in.UnknownOrders)
	case in.Blocked:
		return deny("the instrument is blocked for this account")
	case slices.Contains(in.PendingInstruments, in.Instrument):
		return deny("an order for this instrument is in flight")
	}
	for _, p := range pf.Positions {
		if !p.Conforms() {
			return deny("position %d does not conform (a real share long or a CFD short, leverage 1): entries are blocked", p.ID)
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
	if ec := in.Eligibility.CheckedAt; ec.IsZero() || in.Now.Sub(ec) > EligibilityMaxAge || ec.After(in.Now.Add(time.Second)) {
		return deny("the eligibility check is stale (older than %s): check again", EligibilityMaxAge)
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
	// mulDiv saturates instead of overflowing (review m1): huge figures never wrap negative.
	if maxBySto := Cents(mulDiv(int64(allowance), int64(in.Entry), stopDist)); maxBySto < size {
		size = maxBySto
	}
	switch {
	case size <= 0:
		return deny("no cash, budget, short allowance or loss allowance left (cash %s, budget left %s)", cash, remaining)
	case in.Eligibility.MinAmount(in.Direction) > 0 && size < in.Eligibility.MinAmount(in.Direction):
		return deny("the size %s is below eToro's minimum %s", size, in.Eligibility.MinAmount(in.Direction))
	case l.MinOrderValue > 0 && size < l.MinOrderValue:
		return deny("the size %s is below the account's minimum order value %s", size, l.MinOrderValue)
	}
	return RiskDecision{Allowed: true, Size: size}
}

// FlattenDue reports whether open positions must be closed now (the session ends within
// FlattenBeforeClose, or the market is not trading).
func FlattenDue(now, sessionClose time.Time, trading bool, l RiskLimits) bool {
	return !trading || !now.Before(sessionClose.Add(-l.FlattenBeforeClose))
}
