package domain

import (
	"cmp"
	"math"
	"math/bits"
	"slices"
	"time"
)

// The live figures of an account (PLAN 6.1): what the dashboard shows next to the agent.
// They are computed from the same inputs and with the same helpers as the risk engine
// (DailyFloor, DrawdownFloor, FlattenAt, Reservation), so a distance that has run out is an
// entry AssessEntry refuses. They are a view only: no decision is ever taken on them.

// MarketClock is how long until one market's no-entry time and flatten time today.
type MarketClock struct {
	Market  Market
	Trading bool      // the market trades today (calendar known and trading)
	Open    time.Time // zero when not trading
	Close   time.Time // zero when not trading
	// NoEntryAt is Close − NoEntryBeforeClose and FlattenAt is FlattenAt(Close); both zero
	// when not trading.
	NoEntryAt time.Time
	FlattenAt time.Time
	// ToNoEntry and ToFlatten are the time left from now, never negative (0 = passed or not
	// trading).
	ToNoEntry time.Duration
	ToFlatten time.Duration
}

// LiveFigures are an account's equity, cash, budget and its distances to every limit the
// risk engine applies. Money is in cents; every sum saturates at the int64 range instead of
// wrapping.
//
// The distances are CashLeft, BudgetLeft, ToDailyFloor, ToDrawdownFloor and SlotsLeft for
// every entry, plus ShortLeft for a short and the market's ToNoEntry: when one of those is
// ≤ 0, AssessEntry refuses even the smallest entry of that kind.
type LiveFigures struct {
	AsOf     time.Time // when eToro's figures were read (the portfolio's AsOf)
	Equity   Cents     // cash + pending opens + every position's amount and unrealised P&L
	Cash     Cents     // eToro's available cash
	Reserved Cents     // cash held for our own open orders (Reservation.Amount)
	CashLeft Cents     // Cash − Reserved
	// Budget is the trading budget the engine applies: min(TradingBudget, Equity), 0 when
	// no budget is set. BudgetUsed is everything invested or committed (positions, pending
	// opens at eToro and our reservations); BudgetLeft = Budget − BudgetUsed.
	Budget     Cents
	BudgetUsed Cents
	BudgetLeft Cents
	// DayKnown: the stored risk state belongs to today's risk day and has a start-of-day
	// equity. Only then are StartOfDay, DayPnL, DailyFloor and ToDailyFloor set (else 0).
	DayKnown     bool
	Day          RiskDate // today's risk day
	StartOfDay   Cents
	DayPnL       Cents // Equity − StartOfDay
	DailyFloor   Cents
	ToDailyFloor Cents // Equity − DailyFloor
	DayStopped   bool  // the daily latch is on (no entries until the next risk day)
	// HighWaterMark is the stored mark; DrawdownFloor is measured from it (or from equity,
	// when that is higher) as the kill switch measures it. ToDrawdownFloor = Equity −
	// DrawdownFloor.
	HighWaterMark   Cents
	DrawdownFloor   Cents
	ToDrawdownFloor Cents
	Killed          bool
	// Open counts open positions (any, OD1) plus our open orders not yet final; Cap is
	// MaxOpenPositions; SlotsLeft = Cap − Open.
	Open      int
	Cap       int
	SlotsLeft int
	// ShortUsed is the amount in short positions plus our reserved short orders; ShortCap is
	// MaxShort of Budget; ShortLeft = ShortCap − ShortUsed.
	ShortUsed Cents
	ShortCap  Cents
	ShortLeft Cents
	// Markets are the markets of sessions, in Markets order.
	Markets []MarketClock
}

// LimitDistances computes an account's live figures at now from eToro's portfolio, the stored
// risk state, the limits, the reservation of our own unfinished orders and today's session
// per market. It is pure: the caller reads everything first.
func LimitDistances(pf Portfolio, st AccountRiskState, l RiskLimits, r Reservation, now time.Time, sessions map[Market]MarketSession) LiveFigures {
	rs := st.State
	rs.Reserved, rs.ReservedOrders, rs.ReservedShort = r.Amount, r.Count, r.Short
	f := LiveFigures{
		AsOf: pf.AsOf, Cash: pf.AvailableCash, Reserved: rs.Reserved, Day: RiskDay(now),
		HighWaterMark: rs.HighWaterMark, Killed: rs.Killed, DayStopped: rs.DayStopped && rs.Day == RiskDay(now),
		Cap: l.MaxOpenPositions,
	}
	// Sums are exact in 128 bits and clamped to the int64 range only when shown, so a
	// distance is never off because an intermediate sum saturated.
	equity := wideOf(pf.AvailableCash).add(pf.PendingOpen)
	invested := wideOf(pf.PendingOpen).add(rs.Reserved)
	shortUsed := wideOf(rs.ReservedShort)
	for _, p := range pf.Positions {
		equity = equity.add(p.Amount).add(p.UnrealisedPnL)
		invested = invested.add(p.Amount)
		if !p.Long {
			shortUsed = shortUsed.add(p.Amount)
		}
	}
	f.Equity = equity.clamp()
	f.CashLeft = wideOf(pf.AvailableCash).sub(rs.Reserved).clamp()
	if l.TradingBudget > 0 {
		f.Budget = min(l.TradingBudget, f.Equity)
	}
	f.BudgetUsed, f.BudgetLeft = invested.clamp(), wideOf(f.Budget).subWide(invested).clamp()

	if rs.Day == f.Day && rs.StartOfDayEquity > 0 {
		f.DayKnown, f.StartOfDay = true, rs.StartOfDayEquity
		f.DayPnL = equity.sub(rs.StartOfDayEquity).clamp()
		f.DailyFloor = DailyFloor(rs, l)
		f.ToDailyFloor = equity.sub(f.DailyFloor).clamp()
	}
	f.DrawdownFloor = DrawdownFloor(f.Equity, rs, l)
	f.ToDrawdownFloor = equity.sub(f.DrawdownFloor).clamp()

	f.Open = len(pf.Positions) + max(rs.ReservedOrders, 0)
	f.SlotsLeft = l.MaxOpenPositions - f.Open
	f.ShortUsed, f.ShortCap = shortUsed.clamp(), l.MaxShort.of(f.Budget)
	f.ShortLeft = wideOf(f.ShortCap).subWide(shortUsed).clamp()

	for m, s := range sessions {
		f.Markets = append(f.Markets, marketClock(m, s, now, l))
	}
	slices.SortFunc(f.Markets, func(a, b MarketClock) int {
		return cmp.Or(cmp.Compare(marketRank(a.Market), marketRank(b.Market)), cmp.Compare(a.Market, b.Market))
	})
	return f
}

// marketRank orders markets as in Markets; any other market comes after them.
func marketRank(m Market) int {
	if i := slices.Index(Markets, m); i >= 0 {
		return i
	}
	return len(Markets)
}

func marketClock(m Market, s MarketSession, now time.Time, l RiskLimits) MarketClock {
	c := MarketClock{Market: m, Trading: s.Known && s.Trading}
	if !c.Trading {
		return c
	}
	c.Open, c.Close = s.Open, s.Close
	c.NoEntryAt, c.FlattenAt = s.Close.Add(-l.NoEntryBeforeClose), FlattenAt(s.Close, l)
	c.ToNoEntry, c.ToFlatten = max(c.NoEntryAt.Sub(now), 0), max(c.FlattenAt.Sub(now), 0)
	return c
}

// wide is an exact sum of int64 amounts, kept as hi × 2^32 + lo with 0 ≤ lo < 2^32 (after
// norm). Billions of amounts can be added without overflow, so a sum is clamped to the
// int64 range only once, when it is shown.
type wide struct{ hi, lo int64 }

const (
	wideShift = 32
	wideMask  = 1<<wideShift - 1
)

func wideOf(v Cents) wide { return wide{hi: int64(v) >> wideShift, lo: int64(v) & wideMask} }

// norm moves the carry (or borrow) of lo into hi.
func (w wide) norm() wide { return wide{hi: w.hi + w.lo>>wideShift, lo: w.lo & wideMask} }

func (w wide) addWide(x wide) wide { return wide{hi: w.hi + x.hi, lo: w.lo + x.lo}.norm() }
func (w wide) subWide(x wide) wide { return wide{hi: w.hi - x.hi, lo: w.lo - x.lo}.norm() }
func (w wide) add(v Cents) wide    { return w.addWide(wideOf(v)) }
func (w wide) sub(v Cents) wide    { return w.subWide(wideOf(v)) }

// clamp is the sum saturated at the int64 range.
func (w wide) clamp() Cents {
	switch {
	case w.hi > math.MaxInt64>>wideShift:
		return math.MaxInt64
	case w.hi < math.MinInt64>>wideShift:
		return math.MinInt64
	}
	return Cents(w.hi<<wideShift | w.lo)
}

// mulDivTrunc returns a × b / c truncated towards zero for any a, b and c > 0, computed in
// 128 bits and saturated at the int64 range (c ≤ 0 gives 0).
func mulDivTrunc(a, b, c int64) int64 {
	if c <= 0 || a == 0 || b == 0 {
		return 0
	}
	neg := (a < 0) != (b < 0)
	hi, lo := bits.Mul64(absU(a), absU(b))
	var q uint64
	if hi >= uint64(c) {
		q = math.MaxUint64 // overflows any int64: saturate below
	} else {
		q, _ = bits.Div64(hi, lo, uint64(c))
	}
	if q > math.MaxInt64 { // 2^63 itself is exactly MinInt64 when negative
		if neg {
			return math.MinInt64
		}
		return math.MaxInt64
	}
	if neg {
		return -int64(q)
	}
	return int64(q)
}

// absU is |v| as an unsigned number (exact for math.MinInt64 too).
func absU(v int64) uint64 {
	if v >= 0 {
		return uint64(v)
	}
	if n := -(v + 1); n >= 0 { // always: |v| − 1 fits
		return uint64(n) + 1
	}
	return 0
}
