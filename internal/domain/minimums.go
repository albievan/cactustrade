package domain

import (
	"fmt"
	"time"
)

// TradeMinimums are a trading platform's smallest trade sizes as its eligibility answers state
// them (PLAN R38). Long and Short are the highest minimum among the instruments looked at, so
// a trade of that size is accepted on every one of them; the lowest are shown alongside.
type TradeMinimums struct {
	Platform    Platform
	Long, Short Cents
	LongLowest  Cents
	ShortLowest Cents
	// Instruments is how many instruments were looked at; LongKnown / ShortKnown how many
	// stated a minimum for a long (real, x1) and a short (CFD, x1).
	Instruments           int
	LongKnown, ShortKnown int
}

// MinimumsFrom works the minimums out of the platform's eligibility answers els.
func MinimumsFrom(p Platform, els []Eligibility) TradeMinimums {
	m := TradeMinimums{Platform: p, Instruments: len(els)}
	for _, e := range els {
		if e.RealLong && e.MinPositionAmount > 0 {
			m.LongKnown++
			m.Long = max(m.Long, e.MinPositionAmount)
			if m.LongLowest == 0 || e.MinPositionAmount < m.LongLowest {
				m.LongLowest = e.MinPositionAmount
			}
		}
		if e.ShortCFD && e.MinShortAmount > 0 {
			m.ShortKnown++
			m.Short = max(m.Short, e.MinShortAmount)
			if m.ShortLowest == 0 || e.MinShortAmount < m.ShortLowest {
				m.ShortLowest = e.MinShortAmount
			}
		}
	}
	return m
}

// MinimumFit says whether limits allow a trade of the platform's minimum size, and the
// limits that would (Proposed). Nothing here applies anything: the owner approves Proposed
// (PLAN R38), and the change then goes through every rule of a change of limits.
type MinimumFit struct {
	// Fits: the current limits already allow a minimum long (and a minimum short when shorts
	// are on); Proposed then equals the current limits.
	Fits     bool
	Proposed RiskLimits
	// Changes are the proposed changes in plain words; Problems what no change of limits can
	// fix (e.g. an account too small for a minimum short within the 25 % short cap).
	Changes  []string
	Problems []string
	// LongRoom and ShortRoom are the largest long and short the current limits allow.
	LongRoom, ShortRoom Cents
}

// ceilBp is the basis points of whole that part needs, rounded up (0 when whole is 0).
func ceilBp(part, whole Cents) BasisPoints {
	if whole <= 0 {
		return 0
	}
	return BasisPoints((int64(part)*10_000 + int64(whole) - 1) / int64(whole))
}

// bpOf is bp of v, rounded down.
func bpOf(v Cents, bp BasisPoints) Cents { return Cents(mulDiv(int64(v), int64(bp), 10_000)) }

func pct(bp BasisPoints) string { return fmt.Sprintf("%d.%02d %%", bp/100, bp%100) }

// FitMinimums checks limits l of an account with equity against minimums m in mode mode, and
// proposes the smallest changes that make one trade of the minimum size possible:
//   - a long needs the trading budget, and the largest position (a share of equity), to be
//     at least the minimum long;
//   - a short (only when shorts are on, MaxShort > 0) needs the short allowance (a share of
//     the budget, at most HardMaxShort) to be at least the minimum short.
//
// The budget is only raised when no percentage can make room, and never above the equity. A
// proposal that the account's mode does not allow is not made (a problem instead).
func FitMinimums(l RiskLimits, m TradeMinimums, equity Cents, mode TradingMode) MinimumFit {
	f := MinimumFit{Proposed: l}
	p := &f.Proposed
	posRoom := func(x RiskLimits) Cents {
		r := x.TradingBudget
		if x.MaxPosition > 0 {
			r = min(r, bpOf(equity, x.MaxPosition))
		}
		return r
	}
	f.LongRoom = posRoom(l)
	if l.MaxShort > 0 {
		f.ShortRoom = min(bpOf(l.TradingBudget, l.MaxShort), f.LongRoom)
	}
	if equity <= 0 {
		f.Problems = append(f.Problems, "the account's equity is unknown or zero, so nothing can be sized")
		return f
	}
	raiseBudget := func(to Cents, why string) bool {
		if to <= p.TradingBudget {
			return true
		}
		if to > equity {
			f.Problems = append(f.Problems, fmt.Sprintf("%s needs a trading budget of $%s, more than the account's equity ($%s)", why, to, equity))
			return false
		}
		f.Changes = append(f.Changes, fmt.Sprintf("Trading budget $%s → $%s, so %s fits", p.TradingBudget, to, why))
		p.TradingBudget = to
		return true
	}
	// Longs.
	if m.Long > 0 {
		why := fmt.Sprintf("a minimum long ($%s)", m.Long)
		if raiseBudget(m.Long, why) && p.MaxPosition > 0 && bpOf(equity, p.MaxPosition) < m.Long {
			need := ceilBp(m.Long, equity)
			if need > 10_000 {
				f.Problems = append(f.Problems, fmt.Sprintf("%s is more than the account's equity ($%s)", why, equity))
			} else {
				f.Changes = append(f.Changes, fmt.Sprintf("Largest position %s → %s of equity, so %s fits", pct(p.MaxPosition), pct(need), why))
				p.MaxPosition = need
			}
		}
	}
	// Shorts, when they are on.
	if m.Short > 0 && p.MaxShort > 0 && bpOf(p.TradingBudget, p.MaxShort) < m.Short {
		why := fmt.Sprintf("a minimum short ($%s)", m.Short)
		need := ceilBp(m.Short, p.TradingBudget)
		if need > HardMaxShort {
			budget := Cents((int64(m.Short)*10_000 + int64(HardMaxShort) - 1) / int64(HardMaxShort))
			if raiseBudget(budget, why+" within the 25 % short cap") {
				need = ceilBp(m.Short, p.TradingBudget)
			}
		}
		if need <= HardMaxShort && need > p.MaxShort {
			f.Changes = append(f.Changes, fmt.Sprintf("Short allowance %s → %s of the budget, so %s fits", pct(p.MaxShort), pct(need), why))
			p.MaxShort = need
		}
	}
	if len(f.Changes) > 0 {
		if err := p.ValidateFor(mode); err != nil {
			f.Problems = append(f.Problems, "the proposed limits are not allowed on this account: "+err.Error())
			f.Proposed, f.Changes = l, nil
		}
	}
	f.Fits = len(f.Changes) == 0 && len(f.Problems) == 0
	return f
}

// MinimumsStatus is where a minimums review stands.
type MinimumsStatus string

const (
	MinimumsFit      MinimumsStatus = "fits"     // the limits already allow the minimums
	MinimumsPending  MinimumsStatus = "pending"  // changes proposed, waiting for the owner
	MinimumsApproved MinimumsStatus = "approved" // the owner approved and the limits were changed
	MinimumsDeclined MinimumsStatus = "declined" // the owner declined; nothing changed
	MinimumsBlocked  MinimumsStatus = "blocked"  // a problem no change of limits fixes
)

// MinimumsReview is one check of an account's limits against its platform's minimums, kept
// until the owner decides (PLAN R38).
type MinimumsReview struct {
	ID        string
	AccountID string
	At        time.Time
	Minimums  TradeMinimums
	Equity    Cents
	Current   RiskLimits // the limits when checked (an approval of a review whose limits changed since is refused)
	Fit       MinimumFit
	Status    MinimumsStatus
	DecidedBy string
	DecidedAt time.Time
}

// StatusOf is the status a fresh review with fit f starts in.
func StatusOf(f MinimumFit) MinimumsStatus {
	switch {
	case len(f.Changes) > 0:
		return MinimumsPending
	case len(f.Problems) > 0:
		return MinimumsBlocked
	}
	return MinimumsFit
}
