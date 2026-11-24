package domain

import (
	"math/rand/v2"
	"testing"
	"time"
)

func goodLong(id int64, inst InstrumentID) Position {
	return Position{ID: id, Instrument: inst, OpenRate: 100_000_000, StopLossRate: 97_000_000, RealAsset: true, Long: true, Leverage: 1, Amount: 10_000}
}

func goodShort(id int64, inst InstrumentID) Position {
	return Position{ID: id, Instrument: inst, OpenRate: 100_000_000, StopLossRate: 103_000_000, CFD: true, Leverage: 1, Amount: 10_000}
}

func TestExitsDueTable(t *testing.T) {
	cl := t0.Add(2 * time.Hour)
	open := MarketSession{Known: true, Trading: true, Open: t0.Add(-5 * time.Hour), Close: cl}
	tests := []struct {
		name string
		pos  Position
		sess MarketSession
		now  time.Time
		want ExitReason
	}{
		{"long in session", goodLong(1, 1), open, t0, ""},
		{"short in session", goodShort(1, 1), open, t0, ""},
		{"close-11 min", goodLong(1, 1), open, cl.Add(-11 * time.Minute), ""},
		{"close-10 min", goodLong(1, 1), open, cl.Add(-10 * time.Minute), ExitFlatten},
		{"after the close", goodShort(1, 1), open, cl.Add(time.Hour), ExitFlatten},
		{"before the open", goodLong(1, 1), open, open.Open.Add(-time.Minute), ExitFlatten},
		{"holiday", goodLong(1, 1), MarketSession{Known: true}, t0, ExitFlatten},
		{"unknown market or calendar error", goodLong(1, 1), MarketSession{}, t0, ExitNoSession},
		{"leverage 2", func() Position { p := goodLong(1, 1); p.Leverage = 2; return p }(), open, t0, ExitNonConforming},
		{"CFD long", func() Position { p := goodLong(1, 1); p.RealAsset, p.CFD = false, true; return p }(), open, t0, ExitNonConforming},
		{"long without stop", func() Position { p := goodLong(1, 1); p.StopLossRate = 0; return p }(), open, t0, ExitNoStop},
		{"long stop above the open", func() Position { p := goodLong(1, 1); p.StopLossRate = p.OpenRate; return p }(), open, t0, ExitStopWrongSide},
		{"short stop below the open", func() Position { p := goodShort(1, 1); p.StopLossRate = p.OpenRate - 1; return p }(), open, t0, ExitStopWrongSide},
		{"non-conforming beats flatten", func() Position { p := goodShort(1, 1); p.Leverage = 5; return p }(), MarketSession{}, t0, ExitNonConforming},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExitsDue([]Position{tt.pos}, map[InstrumentID]MarketSession{1: tt.sess}, tt.now, DefaultRiskLimits)
			switch {
			case tt.want == "" && len(got) != 0:
				t.Fatalf("exits = %+v, want none", got)
			case tt.want != "" && (len(got) != 1 || got[0].Reason != tt.want || got[0].PositionID != 1 || got[0].Instrument != 1 || got[0].Detail == ""):
				t.Fatalf("exits = %+v, want %s", got, tt.want)
			}
		})
	}
	// a position whose instrument has no session entry at all is due
	if got := ExitsDue([]Position{goodLong(4, 77)}, map[InstrumentID]MarketSession{1: open}, t0, DefaultRiskLimits); len(got) != 1 || got[0].Reason != ExitNoSession {
		t.Fatalf("missing session: %+v", got)
	}
	// limits that would not validate never flatten later than the default
	l := DefaultRiskLimits
	l.FlattenBeforeClose = 0
	if got := ExitsDue([]Position{goodLong(1, 1)}, map[InstrumentID]MarketSession{1: open}, cl.Add(-10*time.Minute), l); len(got) != 1 {
		t.Fatal("a zero flatten time must fall back to the default")
	}
}

// TestExitsDue (property): for any now at or after close − FlattenBeforeClose every position
// is due; every non-conforming position is always due; a conforming position with a sound
// stop in an open session well before the flatten time never is.
func TestExitsDue(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	exercised := map[ExitReason]int{}
	for range 5_000 {
		l := DefaultRiskLimits
		l.FlattenBeforeClose = time.Duration(5+r.IntN(116)) * time.Minute
		openAt := t0.Add(-time.Duration(r.IntN(10*60)) * time.Minute)
		closeAt := openAt.Add(time.Duration(180+r.IntN(400)) * time.Minute)
		sessions := map[InstrumentID]MarketSession{}
		var positions []Position
		for i := range 1 + r.IntN(8) {
			inst := InstrumentID(1 + r.IntN(4))
			var p Position
			if r.IntN(2) == 0 {
				p = goodLong(int64(i+1), inst)
			} else {
				p = goodShort(int64(i+1), inst)
			}
			switch r.IntN(10) {
			case 0:
				p.Leverage = 1 + r.IntN(5)
			case 1:
				p.RealAsset, p.CFD = r.IntN(2) == 0, r.IntN(2) == 0
			case 2:
				p.Long = !p.Long
			case 3:
				p.StopLossRate = Micros(r.Int64N(200_000_000))
			case 4:
				p.StopLossRate = 0
			}
			positions = append(positions, p)
			switch r.IntN(12) {
			case 0: // no entry: unknown market
			case 1:
				sessions[inst] = MarketSession{Known: true}
			default:
				sessions[inst] = MarketSession{Known: true, Trading: true, Open: openAt, Close: closeAt}
			}
		}
		now := closeAt.Add(time.Duration(r.Int64N(int64(12*time.Hour))) - 9*time.Hour)
		exits := ExitsDue(positions, sessions, now, l)
		due := map[int64]ExitReason{}
		for _, e := range exits {
			if _, dup := due[e.PositionID]; dup {
				t.Fatalf("position %d listed twice", e.PositionID)
			}
			due[e.PositionID] = e.Reason
			exercised[e.Reason]++
		}
		for _, p := range positions {
			reason, isDue := due[p.ID]
			s := sessions[p.Instrument]
			soundStop := p.StopLossRate > 0 && ((p.Long && p.StopLossRate < p.OpenRate) || (!p.Long && p.StopLossRate > p.OpenRate))
			switch {
			case !now.Before(closeAt.Add(-l.FlattenBeforeClose)) && !isDue:
				t.Fatalf("position %+v not due at %s (close %s, flatten %s)", p, now, closeAt, l.FlattenBeforeClose)
			case !p.Conforms() && reason != ExitNonConforming:
				t.Fatalf("non-conforming position %+v: reason %q", p, reason)
			case p.Conforms() && soundStop && s.Known && s.Trading && !now.Before(s.Open) &&
				now.Before(s.Close.Add(-l.FlattenBeforeClose)) && isDue:
				t.Fatalf("sound position %+v due (%s) at %s", p, reason, now)
			}
		}
		if len(exits) == 0 {
			exercised[""]++
		}
	}
	for _, r := range []ExitReason{ExitNonConforming, ExitNoStop, ExitStopWrongSide, ExitNoSession, ExitFlatten, ""} {
		if exercised[r] < 50 {
			t.Fatalf("reason %q exercised only %d times: %v", r, exercised[r], exercised)
		}
	}
}

func TestSessionExitsDue(t *testing.T) {
	cl := t0.Add(2 * time.Hour)
	open := MarketSession{Known: true, Trading: true, Open: t0.Add(-5 * time.Hour), Close: cl}
	ap := func(id int64, inst InstrumentID) AgentPosition {
		return AgentPosition{AccountID: "a", PositionID: id, Instrument: inst, Direction: Long}
	}
	closed := ap(9, 1)
	closed.ClosedAt = t0
	tests := []struct {
		name string
		sess MarketSession
		now  time.Time
		l    RiskLimits
		want ExitReason
	}{
		{"in session", open, t0, DefaultRiskLimits, ""},
		{"close-11 min", open, cl.Add(-11 * time.Minute), DefaultRiskLimits, ""},
		{"close-10 min", open, cl.Add(-10 * time.Minute), DefaultRiskLimits, ExitFlatten},
		{"before the open", open, open.Open.Add(-time.Second), DefaultRiskLimits, ExitFlatten},
		{"holiday", MarketSession{Known: true}, t0, DefaultRiskLimits, ExitFlatten},
		{"unknown", MarketSession{}, t0, DefaultRiskLimits, ExitNoSession},
		{"invalid flatten falls back to 10 min", open, cl.Add(-10 * time.Minute), RiskLimits{}, ExitFlatten},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SessionExitsDue([]AgentPosition{ap(1, 1), closed}, map[InstrumentID]MarketSession{1: tt.sess}, tt.now, tt.l)
			switch {
			case tt.want == "" && len(got) != 0:
				t.Fatalf("exits = %+v, want none", got)
			case tt.want != "" && (len(got) != 1 || got[0].Reason != tt.want || got[0].PositionID != 1 || got[0].Detail == ""):
				t.Fatalf("exits = %+v, want %s", got, tt.want)
			}
		})
	}
}
