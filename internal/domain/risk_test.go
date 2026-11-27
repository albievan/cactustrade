package domain

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // real zones for the DST cases on any host
)

var t0 = time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)

func okInput() RiskInput {
	l := DefaultRiskLimits
	l.TradingBudget = 500_000 // $5,000
	return RiskInput{
		Now: t0, Mode: ModeDemo, SessionOpen: t0.Add(-30 * time.Minute), SessionClose: t0.Add(6 * time.Hour), Trading: true,
		Portfolio:  Portfolio{AvailableCash: 1_000_000, AsOf: t0.Add(-time.Second)},
		Instrument: 1, Direction: Long, Eligibility: Eligibility{Instrument: 1, RealLong: true, MinPositionAmount: 1000, ShortCFD: true, MinShortAmount: 2000, CheckedAt: t0.Add(-time.Hour)},
		Entry: 100_000_000, QuoteAt: t0.Add(-2 * time.Second), StopLoss: 98_000_000, Requested: 100_000,
		Limits: l, State: RiskState{Day: RiskDay(t0), StartOfDayEquity: 1_000_000, HighWaterMark: 1_000_000}, Reconciled: true,
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
		{"real mode without a permit", func(in *RiskInput) { in.Mode = ModeReal }, "real mode is gated", false},
		{"no mode", func(in *RiskInput) { in.Mode = "" }, "real mode is gated", false},
		{"killed", func(in *RiskInput) { in.State.Killed = true }, "kill switch", false},
		{"daily latch", func(in *RiskInput) { in.State.DayStopped = true }, "until the next risk day", false},
		{"state from yesterday", func(in *RiskInput) { in.State.Day = RiskDay(t0.Add(-24 * time.Hour)) }, "not from today's risk day", false},
		{"state not observed", func(in *RiskInput) { in.State.Day = "" }, "not from today's risk day", false},
		{"order in flight", func(in *RiskInput) { in.PendingInstruments = []InstrumentID{7, 1} }, "in flight", false},
		{"unknown order outcome", func(in *RiskInput) { in.UnknownOrders = 1 }, "unknown outcome", false},
		{"blocked instrument", func(in *RiskInput) { in.Blocked = true }, "blocked", false},
		{"stale eligibility", func(in *RiskInput) { in.Eligibility.CheckedAt = t0.Add(-EligibilityMaxAge - time.Second) }, "eligibility check is stale", false},
		{"eligibility never checked", func(in *RiskInput) { in.Eligibility.CheckedAt = time.Time{} }, "eligibility check is stale", false},
		{"eligibility from the future", func(in *RiskInput) { in.Eligibility.CheckedAt = t0.Add(time.Hour) }, "eligibility check is stale", false},
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
		}, "does not conform", false},
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
		forbidden := ""
		switch r.IntN(14) {
		case 0:
			in.Mode, forbidden = ModeReal, "real mode"
		case 1:
			in.State.DayStopped, forbidden = true, "the daily latch"
		case 2:
			in.Blocked, forbidden = true, "a blocked instrument"
		case 3:
			in.PendingInstruments, forbidden = []InstrumentID{InstrumentID(r.IntN(3)), in.Instrument}, "an order in flight"
		case 4:
			in.Eligibility.CheckedAt, forbidden = in.Now.Add(-EligibilityMaxAge-time.Duration(1+r.Int64N(int64(48*time.Hour)))), "stale eligibility"
		case 5:
			in.UnknownOrders, forbidden = 1+r.IntN(3), "an unknown order outcome"
		}
		d := AssessEntry(in)
		if forbidden != "" && d.Allowed {
			t.Fatalf("allowed with %s: %+v", forbidden, in)
		}
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

func TestFlattenDue(t *testing.T) {
	cl := t0.Add(time.Hour)
	for _, c := range []struct {
		now     time.Time
		trading bool
		want    bool
	}{
		{t0, true, false},
		{cl.Add(-11 * time.Minute), true, false},
		{cl.Add(-10 * time.Minute), true, true},
		{cl, true, true},
		{t0, false, true},
	} {
		if got := FlattenDue(c.now, cl, c.trading, DefaultRiskLimits); got != c.want {
			t.Errorf("FlattenDue(%s, trading %v) = %v, want %v", c.now, c.trading, got, c.want)
		}
	}
}

func TestObserve(t *testing.T) {
	l := DefaultRiskLimits // daily loss 2 %
	d1, d2 := RiskDate("2026-11-23"), RiskDate("2026-11-24")
	tests := []struct {
		name  string
		st    RiskState
		eq    Cents
		day   RiskDate
		want  RiskState
		latch bool
	}{
		{"first observation", RiskState{}, 1000_00, d1, RiskState{Day: d1, StartOfDayEquity: 1000_00, HighWaterMark: 1000_00}, false},
		{"same day keeps the start", RiskState{Day: d1, StartOfDayEquity: 1000_00, HighWaterMark: 1000_00}, 1200_00, d1, RiskState{Day: d1, StartOfDayEquity: 1000_00, HighWaterMark: 1200_00}, false},
		{"just above the floor", RiskState{Day: d1, StartOfDayEquity: 1000_00, HighWaterMark: 1000_00}, 980_01, d1, RiskState{Day: d1, StartOfDayEquity: 1000_00, HighWaterMark: 1000_00}, false},
		{"at the floor latches", RiskState{Day: d1, StartOfDayEquity: 1000_00, HighWaterMark: 1000_00}, 980_00, d1, RiskState{Day: d1, StartOfDayEquity: 1000_00, HighWaterMark: 1000_00, DayStopped: true}, true},
		{"recovery keeps the latch", RiskState{Day: d1, StartOfDayEquity: 1000_00, HighWaterMark: 1000_00, DayStopped: true}, 1100_00, d1, RiskState{Day: d1, StartOfDayEquity: 1000_00, HighWaterMark: 1100_00, DayStopped: true}, true},
		{"a new day resets start and latch", RiskState{Day: d1, StartOfDayEquity: 1000_00, HighWaterMark: 1200_00, DayStopped: true}, 900_00, d2, RiskState{Day: d2, StartOfDayEquity: 900_00, HighWaterMark: 1200_00}, false},
		{"an earlier day resets nothing", RiskState{Day: d2, StartOfDayEquity: 1000_00, HighWaterMark: 1000_00, DayStopped: true}, 1000_00, d1, RiskState{Day: d2, StartOfDayEquity: 1000_00, HighWaterMark: 1000_00, DayStopped: true}, true},
		{"no start yet on the same day", RiskState{Day: d1, HighWaterMark: 1000_00}, 990_00, d1, RiskState{Day: d1, StartOfDayEquity: 990_00, HighWaterMark: 1000_00}, false},
		{"killed stays killed", RiskState{Day: d1, StartOfDayEquity: 1000_00, HighWaterMark: 1000_00, Killed: true}, 1000_00, d2, RiskState{Day: d2, StartOfDayEquity: 1000_00, HighWaterMark: 1000_00, Killed: true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.st.Observe(tt.eq, tt.day, l)
			if got != tt.want || got.DayStopped != tt.latch {
				t.Fatalf("Observe = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDrawdownBreached(t *testing.T) {
	l := DefaultRiskLimits // 10 %
	for _, c := range []struct {
		eq, hwm Cents
		want    bool
	}{
		{900_00, 1000_00, true},
		{900_01, 1000_00, false},
		{0, 1000_00, true},
		{1200_00, 1000_00, false},
		{0, 0, false},
	} {
		if got := DrawdownBreached(c.eq, RiskState{HighWaterMark: c.hwm}, l); got != c.want {
			t.Errorf("DrawdownBreached(%d, hwm %d) = %v", c.eq, c.hwm, got)
		}
	}
}

// TestRiskDay: a risk day starts at 22:00 UTC whatever the local clocks do, on both sides of
// every 2026 DST change in London, New York and Sydney.
func TestRiskDay(t *testing.T) {
	zones := map[string]*time.Location{}
	for _, name := range []string{"Europe/London", "America/New_York", "Australia/Sydney"} {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		zones[name] = loc
	}
	// The day of each 2026 change and the days either side of it.
	changes := []string{"2026-03-08", "2026-03-29", "2026-04-05", "2026-10-04", "2026-10-25", "2026-11-01"}
	for _, c := range changes {
		day, _ := time.Parse(time.DateOnly, c)
		for off := -1; off <= 1; off++ {
			d := day.AddDate(0, 0, off)
			before := time.Date(d.Year(), d.Month(), d.Day(), 21, 59, 59, 999_999_999, time.UTC)
			at := before.Add(time.Nanosecond)
			want, next := RiskDate(d.Format(time.DateOnly)), RiskDate(d.AddDate(0, 0, 1).Format(time.DateOnly))
			for name, loc := range zones {
				if got := RiskDay(before.In(loc)); got != want {
					t.Errorf("%s %s: RiskDay = %s, want %s", name, before.In(loc), got, want)
				}
				if got := RiskDay(at.In(loc)); got != next {
					t.Errorf("%s %s: RiskDay = %s, want %s", name, at.In(loc), got, next)
				}
			}
		}
	}
	if RiskDay(time.Date(2026, 12, 31, 22, 0, 0, 0, time.UTC)) != "2027-01-01" || RiskDay(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) != "2027-01-01" {
		t.Fatal("year boundary")
	}
	if RiskDay(t0) >= RiskDay(t0.Add(24*time.Hour)) {
		t.Fatal("risk dates must order as strings")
	}
}

// TestDailyLatch: along random equity paths, once equity touches the daily floor no entry is
// allowed again until the risk day changes, even when equity recovers.
func TestDailyLatch(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	l := DefaultRiskLimits
	l.TradingBudget = 500_000
	l.Drawdown = 5_000 // keep the kill switch out of the way: the latch is under test
	recoveredRefusals, allowedAfterLatch := 0, 0
	for range 400 {
		now := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC).Add(time.Duration(r.Int64N(int64(700 * 24 * time.Hour))))
		equity := Cents(1_000_000)
		var st RiskState
		var curDay, latchedDay RiskDate
		var sod Cents
		everLatched := false
		for range 200 {
			now = now.Add(time.Duration(1+r.IntN(90)) * time.Minute)
			equity = max(1, equity+Cents(r.Int64N(30_001)-15_000))
			day := RiskDay(now)
			// An independent model of the rule: the first equity of a risk day is its start.
			if day != curDay {
				curDay, sod = day, equity
			}
			floor := sod - sod*Cents(l.DailyLoss)/10_000
			if equity <= floor {
				latchedDay, everLatched = day, true
			}
			st = st.Observe(equity, day, l)
			in := okInput()
			in.Now, in.SessionOpen, in.SessionClose = now, now.Add(-time.Hour), now.Add(5*time.Hour)
			in.QuoteAt, in.Portfolio, in.Eligibility.CheckedAt = now, Portfolio{AvailableCash: equity, AsOf: now}, now.Add(-time.Hour)
			in.Limits, in.State, in.Requested = l, st, 10_000
			d := AssessEntry(in)
			switch {
			case latchedDay == day && d.Allowed:
				t.Fatalf("entry allowed on %s after the daily floor was reached (equity %s, start %s)", day, equity, sod)
			case latchedDay == day && equity > floor:
				recoveredRefusals++
			case everLatched && d.Allowed:
				allowedAfterLatch++
			}
		}
	}
	if recoveredRefusals < 500 || allowedAfterLatch < 500 {
		t.Fatalf("not exercised: %d refusals after a recovery, %d entries on a later day", recoveredRefusals, allowedAfterLatch)
	}
}
