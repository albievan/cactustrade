package domain

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)

func okInput() RiskInput {
	l := DefaultRiskLimits
	l.TradingBudget = 500_000 // $5,000
	return RiskInput{
		Now: t0, SessionOpen: t0.Add(-30 * time.Minute), SessionClose: t0.Add(6 * time.Hour), Trading: true,
		Portfolio:  Portfolio{AvailableCash: 1_000_000, AsOf: t0.Add(-time.Second)},
		Instrument: 1, Direction: Long, Eligibility: Eligibility{Instrument: 1, RealLong: true, MinPositionAmount: 1000, ShortCFD: true, MinShortAmount: 2000},
		Entry: 100_000_000, QuoteAt: t0.Add(-2 * time.Second), StopLoss: 98_000_000, Requested: 100_000,
		Limits: l, State: RiskState{StartOfDayEquity: 1_000_000, HighWaterMark: 1_000_000}, Reconciled: true,
	}
}

func TestAssessEntryAllows(t *testing.T) {
	d := AssessEntry(okInput())
	if !d.Allowed || d.Size != 100_000 || d.Kill {
		t.Fatalf("decision = %+v", d)
	}
}

func TestAssessEntryRefuses(t *testing.T) {
	tests := []struct {
		name   string
		change func(*RiskInput)
		want   string
		kill   bool
	}{
		{"killed", func(in *RiskInput) { in.State.Killed = true }, "kill switch", false},
		{"not reconciled", func(in *RiskInput) { in.Reconciled = false }, "not reconciled", false},
		{"invalid limits", func(in *RiskInput) { in.Limits.DailyLoss = 0 }, "invalid", false},
		{"no budget", func(in *RiskInput) { in.Limits.TradingBudget = 0 }, "no trading budget", false},
		{"stale portfolio", func(in *RiskInput) { in.Portfolio.AsOf = t0.Add(-10 * time.Second) }, "stale", false},
		{"no portfolio time", func(in *RiskInput) { in.Portfolio.AsOf = time.Time{} }, "stale", false},
		{"portfolio from the future", func(in *RiskInput) { in.Portfolio.AsOf = t0.Add(time.Minute) }, "stale", false},
		{"stale quote", func(in *RiskInput) { in.QuoteAt = t0.Add(-time.Minute) }, "price is stale", false},
		{"not trading", func(in *RiskInput) { in.Trading = false }, "closed", false},
		{"before open", func(in *RiskInput) { in.SessionOpen = t0.Add(time.Minute) }, "closed", false},
		{"after close", func(in *RiskInput) { in.SessionClose = t0 }, "closed", false},
		{"last 30 minutes", func(in *RiskInput) { in.SessionClose = t0.Add(29 * time.Minute) }, "too close", false},
		{"no start-of-day equity", func(in *RiskInput) { in.State.StartOfDayEquity = 0 }, "start-of-day", false},
		{"drawdown", func(in *RiskInput) { in.State.HighWaterMark = 1_120_000 }, "kill switch", true},
		{"daily loss", func(in *RiskInput) { in.State.StartOfDayEquity = 1_030_000 }, "daily loss", false},
		{"non-conforming position", func(in *RiskInput) {
			in.Portfolio.Positions = []Position{{ID: 9, Instrument: 5, RealAsset: false, Long: true, Leverage: 1}}
		}, "not a real share", false},
		{"already holding", func(in *RiskInput) {
			in.Portfolio.Positions = []Position{{ID: 9, Instrument: 1, RealAsset: true, Long: true, Leverage: 1, Amount: 1}}
		}, "already open", false},
		{"position cap", func(in *RiskInput) { in.Limits.MaxOpenPositions = 1; in.State.ReservedOrders = 1 }, "cap 1", false},
		{"not eligible", func(in *RiskInput) { in.Eligibility.RealLong = false }, "not confirmed", false},
		{"eligibility of another instrument", func(in *RiskInput) { in.Eligibility.Instrument = 2 }, "not confirmed", false},
		{"stop above entry", func(in *RiskInput) { in.StopLoss = in.Entry }, "stop below", false},
		{"no stop", func(in *RiskInput) { in.StopLoss = 0 }, "price and a stop", false},
		{"no direction", func(in *RiskInput) { in.Direction = "" }, "unknown direction", false},
		{"short with the stop below", func(in *RiskInput) { in.Direction = Short }, "stop above", false},
		{"short not shortable", func(in *RiskInput) { in.Direction = Short; in.StopLoss = 102_000_000; in.Eligibility.ShortCFD = false }, "not confirmed shortable", false},
		{"short cap used up", func(in *RiskInput) {
			in.Direction, in.StopLoss = Short, 102_000_000
			in.Portfolio.Positions = []Position{{ID: 3, Instrument: 9, Amount: 125_000, CFD: true, Long: false, Leverage: 1}}
		}, "short allowance", false},
		{"shorts switched off", func(in *RiskInput) { in.Direction, in.StopLoss, in.Limits.MaxShort = Short, 102_000_000, 0 }, "short allowance", false},
		{"short below eToro minimum", func(in *RiskInput) { in.Direction, in.StopLoss, in.Requested = Short, 102_000_000, 1500 }, "minimum", false},
		{"nothing requested", func(in *RiskInput) { in.Requested = 0 }, "nothing", false},
		{"no cash", func(in *RiskInput) { in.Portfolio.AvailableCash = 0; in.Portfolio.PendingOpen = 1_000_000 }, "no cash", false},
		{"below eToro minimum", func(in *RiskInput) { in.Requested = 500 }, "minimum", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := okInput()
			tt.change(&in)
			d := AssessEntry(in)
			if d.Allowed || d.Size != 0 || !strings.Contains(d.Reason, tt.want) || d.Kill != tt.kill {
				t.Fatalf("decision = %+v, want refusal containing %q (kill %v)", d, tt.want, tt.kill)
			}
		})
	}
}

func TestAssessEntryShrinks(t *testing.T) {
	tests := []struct {
		name   string
		change func(*RiskInput)
		want   Cents
	}{
		{"by cash minus reservations", func(in *RiskInput) {
			// equity 1,000,000 but only 60,000 cash, 20,000 of it reserved for a sent order
			in.Limits.TradingBudget = 2_000_000
			in.Portfolio.AvailableCash = 60_000
			in.Portfolio.Positions = []Position{{ID: 2, Instrument: 7, Amount: 480_000, UnrealisedPnL: 460_000, RealAsset: true, Long: true, Leverage: 1}}
			in.State.Reserved, in.State.ReservedOrders = 20_000, 1
		}, 40_000},
		{"by remaining budget", func(in *RiskInput) {
			in.Portfolio.Positions = []Position{{ID: 2, Instrument: 7, Amount: 450_000, RealAsset: true, Long: true, Leverage: 1}}
			in.Portfolio.AvailableCash = 550_000
		}, 50_000},
		{"budget never above equity", func(in *RiskInput) {
			in.Limits.TradingBudget = 10_000_000
			in.Limits.MaxPosition = 0
			in.Requested = 5_000_000
			in.StopLoss = 99_990_000 // a tiny stop so the loss allowance is not the limit
		}, 1_000_000},
		{"by position cap", func(in *RiskInput) { in.Requested = 400_000 }, 200_000},
		{"a short by the short cap (25 % of the budget)", func(in *RiskInput) {
			in.Direction, in.StopLoss = Short, 101_000_000
			in.Portfolio.Positions = []Position{{ID: 3, Instrument: 9, Amount: 100_000, CFD: true, Long: false, Leverage: 1}}
			in.State.ReservedShort, in.State.Reserved, in.State.ReservedOrders = 5_000, 5_000, 1
		}, 20_000},
		{"a short by the daily loss allowance", func(in *RiskInput) {
			in.Direction = Short
			in.State.StartOfDayEquity = 1_010_000 // allowance 10,200
			in.StopLoss = 110_000_000             // a 10 % stop above
			in.Limits.TradingBudget = 2_000_000
			in.Requested = 150_000
		}, 102_000},
		{"by the daily loss allowance", func(in *RiskInput) {
			in.State.StartOfDayEquity = 1_010_000 // floor 989,800 → allowance 10,200
			in.StopLoss = 90_000_000              // a 10 % stop: at most 102,000 at risk of 10,200
			in.Requested = 150_000
		}, 102_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := okInput()
			tt.change(&in)
			if d := AssessEntry(in); !d.Allowed || d.Size != tt.want {
				t.Fatalf("decision = %+v, want size %d", d, tt.want)
			}
		})
	}
}

// TestAssessEntryProperties: whatever the inputs, an allowed entry never exceeds the cash
// left after reservations, the remaining budget, the position cap, equity, or what was asked,
// and a stop-out can never breach the daily loss floor.
func TestAssessEntryProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	allowed, shorts := 0, 0
	for range 20_000 {
		in := okInput()
		in.Portfolio.AvailableCash = Cents(r.Int64N(2_000_000))
		in.Portfolio.PendingOpen = Cents(r.Int64N(100_000))
		for j := range r.IntN(4) {
			in.Portfolio.Positions = append(in.Portfolio.Positions, Position{
				ID: int64(j + 10), Instrument: InstrumentID(j + 10), Amount: Cents(r.Int64N(300_000)),
				UnrealisedPnL: Cents(r.Int64N(40_000) - 20_000), Leverage: 1 + r.IntN(20)/19,
			})
			p := &in.Portfolio.Positions[len(in.Portfolio.Positions)-1]
			if r.IntN(3) == 0 { // a short CFD, now and then a malformed one
				p.CFD, p.Long, p.RealAsset = true, false, r.IntN(20) == 0
			} else {
				p.RealAsset, p.Long, p.CFD = r.IntN(20) > 0, true, false
			}
		}
		in.State.Reserved = Cents(r.Int64N(200_000))
		in.State.ReservedOrders = r.IntN(3)
		in.State.StartOfDayEquity = Cents(500_000 + r.Int64N(1_500_000))
		in.State.HighWaterMark = in.State.StartOfDayEquity + Cents(r.Int64N(300_000))
		in.Limits.TradingBudget = Cents(r.Int64N(3_000_000))
		in.Limits.MaxPosition = BasisPoints(r.Int64N(3_000))
		in.Limits.MaxOpenPositions = 1 + r.IntN(HardMaxOpenPositions)
		in.Requested = Cents(r.Int64N(1_000_000)) - 1000
		in.Limits.MaxShort = BasisPoints(r.Int64N(int64(HardMaxShort) + 1))
		if r.IntN(2) == 0 {
			in.Direction = Short
			in.StopLoss = in.Entry + Micros(r.Int64N(int64(in.Entry)))
			in.State.ReservedShort = min(in.State.Reserved, Cents(r.Int64N(100_000)))
		} else {
			in.StopLoss = in.Entry - Micros(r.Int64N(int64(in.Entry)))
		}
		d := AssessEntry(in)
		if !d.Allowed {
			if d.Size != 0 {
				t.Fatalf("refused with a size: %+v", d)
			}
			continue
		}
		allowed++
		pf, st, l := in.Portfolio, in.State, in.Limits
		eq := pf.Equity()
		invested := pf.PendingOpen + st.Reserved
		for _, p := range pf.Positions {
			invested += p.Amount
			if !p.Conforms() {
				t.Fatalf("allowed next to a non-conforming position: %+v", in)
			}
		}
		dist := int64(in.Entry - in.StopLoss)
		if dist < 0 {
			dist = -dist
		}
		lossAtStop := int64(d.Size) * dist / int64(in.Entry)
		shortOpen := st.ReservedShort
		for _, p := range pf.Positions {
			if !p.Long {
				shortOpen += p.Amount
			}
		}
		switch {
		case d.Size <= 0 || d.Size > in.Requested:
			t.Fatalf("size %d vs requested %d", d.Size, in.Requested)
		case d.Size > pf.AvailableCash-st.Reserved:
			t.Fatalf("spends more than the cash left: %+v → %+v", in, d)
		case invested+d.Size > min(l.TradingBudget, eq):
			t.Fatalf("breaches the budget: invested %d + %d > %d", invested, d.Size, min(l.TradingBudget, eq))
		case l.MaxPosition > 0 && d.Size > l.MaxPosition.of(eq):
			t.Fatalf("breaches the position cap")
		case len(pf.Positions)+st.ReservedOrders >= l.MaxOpenPositions:
			t.Fatalf("breaches the open-position cap")
		case eq-Cents(lossAtStop) < st.StartOfDayEquity-l.DailyLoss.of(st.StartOfDayEquity):
			t.Fatalf("a stop-out would breach the daily loss floor")
		case eq <= st.HighWaterMark-l.Drawdown.of(st.HighWaterMark):
			t.Fatalf("allowed in drawdown")
		case in.Direction == Short && shortOpen+d.Size > l.MaxShort.of(min(l.TradingBudget, eq)):
			t.Fatalf("a short breaches the short cap: %d + %d > %d", shortOpen, d.Size, l.MaxShort.of(min(l.TradingBudget, eq)))
		case in.Direction == Short && in.StopLoss <= in.Entry, in.Direction == Long && in.StopLoss >= in.Entry:
			t.Fatalf("the stop is on the wrong side")
		}
		if in.Direction == Short {
			shorts++
		}
	}
	if allowed < 200 || shorts < 50 {
		t.Fatalf("only %d of 20000 random inputs allowed (%d shorts): the property test is not exercising much", allowed, shorts)
	}
}

func TestRiskLimitsValidate(t *testing.T) {
	if err := DefaultRiskLimits.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := []func(*RiskLimits){
		func(l *RiskLimits) { l.TradingBudget = -1 },
		func(l *RiskLimits) { l.DailyLoss = 5000 },
		func(l *RiskLimits) { l.Drawdown = 10 },
		func(l *RiskLimits) { l.MaxPosition = 20_000 },
		func(l *RiskLimits) { l.MaxOpenPositions = HardMaxOpenPositions + 1 },
		func(l *RiskLimits) { l.FlattenBeforeClose = time.Minute },
		func(l *RiskLimits) { l.NoEntryBeforeClose = 5 * time.Minute },
		func(l *RiskLimits) { l.MaxPortfolioAge = 0 },
		func(l *RiskLimits) { l.MaxQuoteAge = time.Hour },
		func(l *RiskLimits) { l.MaxShort = HardMaxShort + 1 },
		func(l *RiskLimits) { l.MaxShort = -1 },
	}
	for i, f := range bad {
		l := DefaultRiskLimits
		f(&l)
		if l.Validate() == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestFlattenAndObserve(t *testing.T) {
	cl := t0.Add(time.Hour)
	if FlattenDue(t0, cl, true, DefaultRiskLimits) || !FlattenDue(cl.Add(-10*time.Minute), cl, true, DefaultRiskLimits) || !FlattenDue(t0, cl, false, DefaultRiskLimits) {
		t.Fatal("FlattenDue")
	}
	st := RiskState{}.Observe(1000, false)
	st = st.Observe(1200, false)
	st = st.Observe(900, false)
	if st.StartOfDayEquity != 1000 || st.HighWaterMark != 1200 {
		t.Fatalf("observe = %+v", st)
	}
	if st = st.Observe(800, true); st.StartOfDayEquity != 800 || st.HighWaterMark != 1200 {
		t.Fatalf("new day = %+v", st)
	}
}
