package domain

import (
	"cmp"
	"slices"
	"time"
)

// Trade history (PLAN 6.2, 6.4): eToro's closed trades as cactusTrade keeps them, and the
// figures shown about them. The figures describe past trades only; nothing here is used to
// size, approve or refuse an order.

// Trade is a closed trade of an account as stored: eToro's record plus whether cactusTrade
// opened it (OD15: positions it did not open are listed and flagged, never hidden).
type Trade struct {
	AccountID string
	Mode      TradingMode
	ClosedTrade
	// Ours: the position is one of the account's agent positions (cactusTrade opened it);
	// OpenRequestID is then the x-request-id of the order that opened it.
	Ours          bool
	OpenRequestID string
	SyncedAt      time.Time // when it was last read from eToro
}

// Validate checks a trade before it is stored.
func (t Trade) Validate() error {
	switch {
	case t.AccountID == "":
		return Invalid("accountId", "is required")
	case t.PositionID <= 0:
		return Invalid("positionId", "must be positive")
	case t.Mode != ModeDemo && t.Mode != ModeReal:
		return Invalid("mode", "demo or real")
	case t.ClosedAt.IsZero():
		return Invalid("closedAt", "is required")
	case !t.FeesKnown && t.Fees != 0:
		return Invalid("fees", "unknown fees are stored as 0 with feesKnown false")
	}
	return nil
}

// ExportMaxRows is the most rows one history export may hold (PLAN 6.5).
const ExportMaxRows = 50_000

// RiskDayStart is when the risk day containing t began: the 22:00 UTC at or before t.
func RiskDayStart(t time.Time) time.Time {
	const shift = (24 - RiskDayStartHourUTC) * time.Hour
	return t.UTC().Add(shift).Truncate(24 * time.Hour).Add(-shift)
}

// TradeSummary sums a set of closed trades. A trade with a positive net profit is a win, a
// negative one a loss, exactly zero breakeven. Sums saturate at the int64 range.
type TradeSummary struct {
	Count, Wins, Losses, Breakeven int
	// WinRate is Wins / Count in basis points, rounded down; HasWinRate is false when there
	// are no trades (no rate rather than 0 %).
	WinRate    BasisPoints
	HasWinRate bool
	// Net is the sum of eToro's net profit; Fees the sum of the fees eToro reported;
	// Gross = Net + Fees (taking eToro's net profit as after fees, RU5). FeesUnknown counts
	// trades whose fees eToro did not report: their fees are in neither figure.
	Gross, Fees, Net Cents
	FeesUnknown      int
}

// TradeStats sums trades.
func TradeStats(trades []ClosedTrade) TradeSummary {
	var s TradeSummary
	var net, fees wide
	for _, t := range trades {
		s.Count++
		switch {
		case t.NetProfit > 0:
			s.Wins++
		case t.NetProfit < 0:
			s.Losses++
		default:
			s.Breakeven++
		}
		net = net.add(t.NetProfit)
		if t.FeesKnown {
			fees = fees.add(t.Fees)
		} else {
			s.FeesUnknown++
		}
	}
	s.Net, s.Fees, s.Gross = net.clamp(), fees.clamp(), net.addWide(fees).clamp()
	if s.Count > 0 {
		s.HasWinRate = true
		s.WinRate = BasisPoints(int64(s.Wins) * 10_000 / int64(s.Count))
	}
	return s
}

// DayPnL is one risk day (22:00 UTC to 22:00 UTC, OD4) of an account's history.
type DayPnL struct {
	Day RiskDate
	// Trades closed that day, their summed net profit (Realised) and reported fees.
	Trades      int
	Realised    Cents
	Fees        Cents
	FeesUnknown int
	// EquityKnown: there is an equity snapshot that day. EquityEnd is the last one;
	// EquityChange is EquityEnd minus the last snapshot of the previous day that has one
	// (else minus the day's first snapshot). The change includes deposits and withdrawals
	// and positions cactusTrade did not open: it is not a trading result.
	EquityKnown  bool
	EquityEnd    Cents
	EquityChange Cents
}

// DailyPnL groups trades by the risk day they closed on and snapshots by the risk day they
// were taken on, oldest day first. Days with neither are left out.
func DailyPnL(trades []ClosedTrade, snapshots []EquitySnapshot) []DayPnL {
	type acc struct {
		d              DayPnL
		realised, fees wide
	}
	days := map[RiskDate]*acc{}
	get := func(d RiskDate) *acc {
		if a, ok := days[d]; ok {
			return a
		}
		a := &acc{d: DayPnL{Day: d}}
		days[d] = a
		return a
	}
	for _, t := range trades {
		a := get(RiskDay(t.ClosedAt))
		a.d.Trades++
		a.realised = a.realised.add(t.NetProfit)
		if t.FeesKnown {
			a.fees = a.fees.add(t.Fees)
		} else {
			a.d.FeesUnknown++
		}
	}
	snaps := sortedSnapshots(snapshots)
	first := map[RiskDate]Cents{}
	for _, s := range snaps {
		a := get(RiskDay(s.At))
		if !a.d.EquityKnown {
			first[a.d.Day] = s.Equity
		}
		a.d.EquityKnown, a.d.EquityEnd = true, s.Equity
	}
	out := make([]DayPnL, 0, len(days))
	for _, a := range days {
		a.d.Realised, a.d.Fees = a.realised.clamp(), a.fees.clamp()
		out = append(out, a.d)
	}
	slices.SortFunc(out, func(a, b DayPnL) int { return cmp.Compare(a.Day, b.Day) })
	prev, havePrev := Cents(0), false
	for i := range out {
		if !out[i].EquityKnown {
			continue
		}
		base := first[out[i].Day]
		if havePrev {
			base = prev
		}
		out[i].EquityChange = wideOf(out[i].EquityEnd).sub(base).clamp()
		prev, havePrev = out[i].EquityEnd, true
	}
	return out
}

// DrawdownFigures describe the falls of an account's equity from its running peak, over a
// series of snapshots. Negative equity counts as 0, so a drawdown is never more than its
// peak.
type DrawdownFigures struct {
	Known bool // there was at least one snapshot
	// Peak is the highest equity seen; PeakAt when (the first time).
	Peak   Cents
	PeakAt time.Time
	// MaxDrawdown is the largest fall in cents from a running peak; MaxDrawdownBp the
	// largest fall relative to its running peak (the two can come from different falls).
	MaxDrawdown   Cents
	MaxDrawdownBp BasisPoints
	// Current is the fall of the last snapshot from the peak.
	Current   Cents
	CurrentBp BasisPoints
}

// Drawdown computes the drawdown figures of snapshots (any order; one account).
func Drawdown(snapshots []EquitySnapshot) DrawdownFigures {
	var f DrawdownFigures
	for _, s := range sortedSnapshots(snapshots) {
		eq := max(s.Equity, 0)
		if !f.Known || eq > f.Peak {
			f.Peak, f.PeakAt = eq, s.At
		}
		f.Known = true
		dd := f.Peak - eq // 0 ≤ eq ≤ Peak: no overflow
		bp := ddBp(dd, f.Peak)
		f.MaxDrawdown, f.MaxDrawdownBp = max(f.MaxDrawdown, dd), max(f.MaxDrawdownBp, bp)
		f.Current, f.CurrentBp = dd, bp
	}
	return f
}

// ddBp is dd as a share of peak in basis points, rounded down (0 without a peak).
func ddBp(dd, peak Cents) BasisPoints {
	if peak <= 0 {
		return 0
	}
	return BasisPoints(mulDiv(int64(dd), 10_000, int64(peak)))
}

// sortedSnapshots returns the snapshots oldest first (stable for equal times).
func sortedSnapshots(snapshots []EquitySnapshot) []EquitySnapshot {
	out := slices.Clone(snapshots)
	slices.SortStableFunc(out, func(a, b EquitySnapshot) int { return a.At.Compare(b.At) })
	return out
}
