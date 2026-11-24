package domain

import "time"

// DrawdownFloor is the equity at or below which the kill switch trips (DrawdownBreached),
// measured from the high-water mark or from equity when that is higher. It is 0 when neither
// is above zero. For any equity e with a positive mark, e ≤ DrawdownFloor(e, st, l) exactly
// when DrawdownBreached(e, st, l).
func DrawdownFloor(equity Cents, st RiskState, l RiskLimits) Cents {
	hwm := max(st.HighWaterMark, equity)
	if hwm <= 0 {
		return 0
	}
	return hwm - l.Drawdown.of(hwm)
}

// FlattenAt is when the supervisor starts closing positions of a session that ends at
// sessionClose: FlattenBeforeClose earlier, never less than the 5-minute minimum ExitsDue
// uses.
func FlattenAt(sessionClose time.Time, l RiskLimits) time.Time {
	if l.FlattenBeforeClose < minFlatten {
		l.FlattenBeforeClose = DefaultRiskLimits.FlattenBeforeClose
	}
	return sessionClose.Add(-l.FlattenBeforeClose)
}
