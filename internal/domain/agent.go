package domain

import (
	"fmt"
	"math"
	"math/bits"
	"time"
)

// EntriesAllowed reports whether the agent may open positions on market m: London (LSE),
// New York (NYSE/NASDAQ) and Sydney (ASX). LSE was off until its units were verified
// (owner's decision OD9, settled 2026-11-27). Exits are never affected.
func EntriesAllowed(m Market) (bool, string) {
	switch m {
	case MarketNYSE, MarketASX, MarketLSE:
		// LSE back on 2026-11-27 (OD9 settled): prices, candles and stops are all in pence
		// (GBX), so stops are in the same unit as the prices they come from (PLAN step 2.7).
		return true, ""
	}
	return false, fmt.Sprintf("market %q is not supported", m)
}

// NextDecision returns the agent's next decision time after now: the next bar close on the
// interval grid (UTC) plus delay, so the bar that just closed is complete at eToro (OD3,
// D26). A time exactly at a bar close plus delay is not "after now".
func NextDecision(now time.Time, interval, delay time.Duration) time.Time {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	t := now.UTC().Truncate(interval).Add(delay)
	if !t.After(now) {
		t = t.Add(interval)
	}
	return t
}

// ClosedBars drops the bars that have not closed at now (a bar starting at s closes at
// s + interval): eToro's newest candle may still be forming (U5), and a decision must rest
// on closed bars only. Bars are oldest first; the result shares bars' backing array.
func ClosedBars(bars []Candle, interval time.Duration, now time.Time) []Candle {
	n := len(bars)
	for n > 0 && bars[n-1].Start.Add(interval).After(now) {
		n--
	}
	return bars[:n]
}

// BarsFresh reports whether the newest closed bar ended at most two intervals before now:
// older price history (a halted share, a feed that stopped) is not traded on.
func BarsFresh(closed []Candle, interval time.Duration, now time.Time) bool {
	if len(closed) == 0 {
		return false
	}
	end := closed[len(closed)-1].Start.Add(interval)
	return !end.After(now) && now.Sub(end) <= 2*interval
}

// mulDiv returns a × b / c rounded down for a, b ≥ 0 and c > 0, saturating at MaxInt64
// instead of overflowing.
func mulDiv(a, b, c int64) int64 {
	if a <= 0 || b <= 0 || c <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi >= uint64(c) {
		return math.MaxInt64
	}
	q, _ := bits.Div64(hi, lo, uint64(c))
	if q > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(q)
}

// toUSDMicros converts v micros of a currency worth rate USD micros per unit to USD micros,
// rounded up (a cost is never understated); a negative v (a rebate) converts to 0.
func toUSDMicros(v int64, rate Micros) int64 {
	if v <= 0 || rate <= 0 {
		return 0
	}
	hi, lo := bits.Mul64(uint64(v), uint64(rate))
	if hi >= 1_000_000 {
		return math.MaxInt64
	}
	q, r := bits.Div64(hi, lo, 1_000_000)
	if q >= math.MaxInt64 {
		return math.MaxInt64
	}
	if r != 0 {
		q++
	}
	return int64(q)
}

// microsPerCent converts USD micros (1 USD = 1 000 000) to cents (1 USD = 100).
const microsPerCent = 10_000

// CostCheck is the cost gate (OD8): an entry of size in direction d at entry, aiming at
// takeProfit, is refused when eToro's costs for it are at least the gross gain at the
// take-profit, size × |takeProfit − entry| / entry. A cost line in another currency than
// USD (the account currency), e.g. an ASX overnight fee in AUD or an LSE one in GBX, is
// converted with eToro's own rate in fx (rounded up). It fails closed: no take-profit (or
// one on the losing side), no cost lines, or a cost line in a currency fx has no rate for
// is a refusal. Negative cost lines (rebates) are ignored. It returns the gross gain and the
// costs in cents (costs rounded up) and, when refused, why.
func CostCheck(size Cents, d Direction, entry, takeProfit Micros, costs []CostItem, fx FXRates) (ok bool, gain, total Cents, why string) {
	switch {
	case !d.Valid():
		return false, 0, 0, "unknown direction"
	case size <= 0 || entry <= 0:
		return false, 0, 0, "no size or entry price"
	case takeProfit <= 0:
		return false, 0, 0, "no take-profit: the gain the costs must stay below is unknown"
	case d == Long && takeProfit <= entry, d == Short && takeProfit >= entry:
		return false, 0, 0, "the take-profit is not on the winning side of the entry"
	case len(costs) == 0:
		return false, 0, 0, "eToro returned no cost breakdown"
	}
	var micros int64
	for _, c := range costs {
		v := int64(c.Value)
		if cur := NormCurrency(c.Currency); cur != "" && cur != "USD" {
			rate, ok := fx[cur]
			if !ok || rate <= 0 {
				return false, 0, 0, fmt.Sprintf("a cost line (%s) is in %s and eToro's rate to convert it to USD is not available", c.Type, c.Currency)
			}
			v = toUSDMicros(v, rate)
		}
		if v > 0 {
			if micros > math.MaxInt64-v {
				micros = math.MaxInt64
				break
			}
			micros += v
		}
	}
	total = Cents(micros / microsPerCent)
	if micros%microsPerCent != 0 {
		total++
	}
	diff := int64(takeProfit - entry)
	if diff < 0 {
		diff = -diff
	}
	gain = Cents(mulDiv(int64(size), diff, int64(entry)))
	if total >= gain {
		return false, gain, total, fmt.Sprintf("costs $%s are not below the gross gain at the take-profit $%s", total, gain)
	}
	return true, gain, total, ""
}
