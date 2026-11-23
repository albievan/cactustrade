package domain

import (
	"fmt"
	"time"
)

// ExitReason says why a position must be closed now.
type ExitReason string

// Exit reasons, most serious first (a position gets the first that applies).
const (
	ExitNonConforming ExitReason = "non_conforming"  // not real/long/x1 or cfd/short/x1
	ExitNoStop        ExitReason = "no_stop"         // no stop-loss on eToro
	ExitStopWrongSide ExitReason = "stop_wrong_side" // a stop that cannot limit the loss
	ExitNoSession     ExitReason = "no_session"      // unknown market or calendar error
	ExitFlatten       ExitReason = "flatten"         // the session ends soon or is not on
)

// MarketSession is a calendar answer for one instrument's market at the moment of the check.
// The zero value (Known false) stands for an unknown market or a calendar error, and makes
// the position due (fail closed).
type MarketSession struct {
	Known   bool
	Trading bool
	Open    time.Time
	Close   time.Time
}

// Exit is one position that must be closed now, and why.
type Exit struct {
	PositionID int64
	Instrument InstrumentID
	Reason     ExitReason
	Detail     string
}

// minFlatten is the shortest flatten time RiskLimits.Validate accepts; ExitsDue never uses
// less, even with limits that would not validate.
const minFlatten = 5 * time.Minute

// ExitsDue returns the positions that must be closed now. The caller passes only the
// positions cactusTrade opened (OD1: others are never closed by it) and, per instrument, the
// session of that instrument's own market. A position is due when it does not conform, has
// no stop or a stop on the wrong side of its open rate, has no known session, or its
// session is not trading, has not opened yet, or ends within FlattenBeforeClose. Nothing is
// held overnight.
func ExitsDue(positions []Position, sessions map[InstrumentID]MarketSession, now time.Time, l RiskLimits) []Exit {
	if l.FlattenBeforeClose < minFlatten {
		l.FlattenBeforeClose = DefaultRiskLimits.FlattenBeforeClose
	}
	var out []Exit
	for _, p := range positions {
		if r, why := exitReason(p, sessions[p.Instrument], now, l); r != "" {
			out = append(out, Exit{PositionID: p.ID, Instrument: p.Instrument, Reason: r, Detail: why})
		}
	}
	return out
}

func exitReason(p Position, s MarketSession, now time.Time, l RiskLimits) (ExitReason, string) {
	switch {
	case !p.Conforms():
		return ExitNonConforming, fmt.Sprintf("position %d is not a real share long or a CFD short at leverage 1", p.ID)
	case p.StopLossRate <= 0:
		return ExitNoStop, fmt.Sprintf("position %d has no stop-loss", p.ID)
	case p.Long && p.StopLossRate >= p.OpenRate, !p.Long && p.StopLossRate <= p.OpenRate:
		return ExitStopWrongSide, fmt.Sprintf("position %d: stop %s on the wrong side of the open rate %s", p.ID, p.StopLossRate, p.OpenRate)
	case !s.Known:
		return ExitNoSession, fmt.Sprintf("no market session known for instrument %d", p.Instrument)
	case !s.Trading || now.Before(s.Open) || FlattenDue(now, s.Close, s.Trading, l):
		return ExitFlatten, fmt.Sprintf("the session ends at %s (flatten %s before)", s.Close.UTC().Format(time.RFC3339), l.FlattenBeforeClose)
	}
	return "", ""
}
