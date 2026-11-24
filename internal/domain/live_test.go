package domain

import (
	"math"
	"math/big"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"
)

func liveLimits() RiskLimits {
	l := DefaultRiskLimits
	l.TradingBudget = 500_000
	return l
}

func TestLimitDistances(t *testing.T) {
	now := t0 // 15:00 UTC
	today := RiskDay(now)
	nyse := MarketSession{Known: true, Trading: true, Open: now.Add(-30 * time.Minute), Close: now.Add(6 * time.Hour)}
	base := Portfolio{
		AvailableCash: 1_000_000, PendingOpen: 50_000, AsOf: now.Add(-time.Second),
		Positions: []Position{
			{ID: 1, Instrument: 1, Amount: 200_000, UnrealisedPnL: 10_000, Long: true, RealAsset: true, Leverage: 1},
			{ID: 2, Instrument: 2, Amount: 100_000, UnrealisedPnL: -5_000, CFD: true, Leverage: 1},
		},
	}
	st := AccountRiskState{State: RiskState{Day: today, StartOfDayEquity: 1_300_000, HighWaterMark: 1_400_000}}
	res := Reservation{Amount: 30_000, Count: 1, Short: 10_000}
	tests := []struct {
		name  string
		pf    Portfolio
		st    AccountRiskState
		l     RiskLimits
		r     Reservation
		check func(t *testing.T, f LiveFigures)
	}{
		{"every figure", base, st, liveLimits(), res, func(t *testing.T, f LiveFigures) {
			// equity = 1 000 000 + 50 000 + 210 000 + 95 000
			want := LiveFigures{
				AsOf: base.AsOf, Equity: 1_355_000, Cash: 1_000_000, Reserved: 30_000, CashLeft: 970_000,
				Budget: 500_000, BudgetUsed: 380_000, BudgetLeft: 120_000,
				DayKnown: true, Day: today, StartOfDay: 1_300_000, DayPnL: 55_000, DailyFloor: 1_274_000, ToDailyFloor: 81_000,
				HighWaterMark: 1_400_000, DrawdownFloor: 1_260_000, ToDrawdownFloor: 95_000,
				Open: 3, Cap: 8, SlotsLeft: 5, ShortUsed: 110_000, ShortCap: 125_000, ShortLeft: 15_000,
			}
			f.Markets = nil
			if !reflect.DeepEqual(f, want) {
				t.Fatalf("got  %+v\nwant %+v", f, want)
			}
		}},
		{"yesterday's risk state: no day figures", base, AccountRiskState{State: RiskState{Day: RiskDay(now.Add(-24 * time.Hour)), StartOfDayEquity: 1_300_000, HighWaterMark: 1_400_000, DayStopped: true}}, liveLimits(), res, func(t *testing.T, f LiveFigures) {
			if f.DayKnown || f.DayPnL != 0 || f.DailyFloor != 0 || f.ToDailyFloor != 0 || f.DayStopped {
				t.Fatalf("day figures from another risk day: %+v", f)
			}
			if f.ToDrawdownFloor != 95_000 {
				t.Fatalf("the drawdown floor does not depend on the day: %d", f.ToDrawdownFloor)
			}
		}},
		{"no budget", base, st, func() RiskLimits { l := liveLimits(); l.TradingBudget = 0; return l }(), res, func(t *testing.T, f LiveFigures) {
			if f.Budget != 0 || f.BudgetLeft != -380_000 || f.ShortCap != 0 || f.ShortLeft != -110_000 {
				t.Fatalf("figures without a budget: %+v", f)
			}
		}},
		{"budget above equity is capped by equity", Portfolio{AvailableCash: 100_000, AsOf: now}, st, liveLimits(), Reservation{}, func(t *testing.T, f LiveFigures) {
			if f.Budget != 100_000 || f.BudgetLeft != 100_000 {
				t.Fatalf("budget %d left %d", f.Budget, f.BudgetLeft)
			}
		}},
		{"killed and latched are passed through", base, AccountRiskState{State: RiskState{Day: today, StartOfDayEquity: 1, HighWaterMark: 1, DayStopped: true, Killed: true}}, liveLimits(), Reservation{}, func(t *testing.T, f LiveFigures) {
			if !f.Killed || !f.DayStopped {
				t.Fatalf("flags lost: %+v", f)
			}
		}},
		{"no positions, no reservation", Portfolio{AvailableCash: 1_000, AsOf: now}, AccountRiskState{}, liveLimits(), Reservation{}, func(t *testing.T, f LiveFigures) {
			if f.Open != 0 || f.SlotsLeft != 8 || f.ShortUsed != 0 || f.HighWaterMark != 0 || f.DrawdownFloor != 900 {
				t.Fatalf("empty account: %+v", f)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.check(t, LimitDistances(tt.pf, tt.st, tt.l, tt.r, now, map[Market]MarketSession{MarketNYSE: nyse}))
		})
	}
}

func TestLimitDistancesMarkets(t *testing.T) {
	now := t0
	l := liveLimits() // no entries 30 min, flatten 10 min before the close
	sessions := map[Market]MarketSession{
		MarketASX:  {Known: true, Trading: false},
		MarketNYSE: {Known: true, Trading: true, Open: now.Add(-time.Hour), Close: now.Add(20 * time.Minute)},
		MarketLSE:  {Known: true, Trading: true, Open: now.Add(-7 * time.Hour), Close: now.Add(time.Hour)},
		"XETRA":    {Known: true, Trading: true, Open: now.Add(-time.Hour), Close: now.Add(2 * time.Hour)},
		"AAA":      {}, // unknown: treated as not trading
	}
	f := LimitDistances(Portfolio{AsOf: now}, AccountRiskState{}, l, Reservation{}, now, sessions)
	order := []Market{MarketLSE, MarketNYSE, MarketASX, "AAA", "XETRA"}
	if len(f.Markets) != len(order) {
		t.Fatalf("markets = %+v", f.Markets)
	}
	for i, m := range order {
		if f.Markets[i].Market != m {
			t.Fatalf("market %d = %s, want %s", i, f.Markets[i].Market, m)
		}
	}
	lse, nyse, asx, unknown := f.Markets[0], f.Markets[1], f.Markets[2], f.Markets[3]
	if !lse.Trading || lse.ToNoEntry != 30*time.Minute || lse.ToFlatten != 50*time.Minute || !lse.FlattenAt.Equal(now.Add(50*time.Minute)) {
		t.Fatalf("LSE = %+v", lse)
	}
	if !nyse.Trading || nyse.ToNoEntry != 0 || nyse.ToFlatten != 10*time.Minute || !nyse.NoEntryAt.Equal(now.Add(-10*time.Minute)) {
		t.Fatalf("NYSE past its no-entry time = %+v", nyse)
	}
	if asx.Trading || asx.ToNoEntry != 0 || !asx.Close.IsZero() || unknown.Trading {
		t.Fatalf("closed markets = %+v %+v", asx, unknown)
	}
	l.FlattenBeforeClose = time.Minute // below the minimum: FlattenAt's default applies
	f = LimitDistances(Portfolio{AsOf: now}, AccountRiskState{}, l, Reservation{}, now, sessions)
	if f.Markets[0].ToFlatten != 50*time.Minute {
		t.Fatalf("flatten below the minimum = %s", f.Markets[0].ToFlatten)
	}
}

// liveDistances returns the distances that gate an entry in direction d on the market of
// the session.
func liveDistances(f LiveFigures, d Direction) map[string]int64 {
	out := map[string]int64{
		"cash": int64(f.CashLeft), "budget": int64(f.BudgetLeft), "dailyFloor": int64(f.ToDailyFloor),
		"drawdownFloor": int64(f.ToDrawdownFloor), "slots": int64(f.SlotsLeft), "noEntry": int64(f.Markets[0].ToNoEntry),
	}
	if d == Short {
		out["short"] = int64(f.ShortLeft)
	}
	return out
}

// TestLimitDistancesAgreeWithAssessEntry: a distance that has run out is an entry the risk
// engine refuses, and an entry the engine allows leaves every distance positive.
func TestLimitDistancesAgreeWithAssessEntry(t *testing.T) {
	r := rand.New(rand.NewPCG(61, 62))
	allowed, exhausted := 0, 0
	for range 20_000 {
		in := okInput()
		in.Requested = 1 // the smallest entry
		in.Eligibility.MinPositionAmount, in.Eligibility.MinShortAmount = 0, 0
		in.Portfolio.AvailableCash = Cents(r.Int64N(2_000_000))
		in.Portfolio.PendingOpen = Cents(r.Int64N(100_000))
		for j := range r.IntN(4) {
			p := Position{ID: int64(j + 10), Instrument: InstrumentID(j + 10), Amount: Cents(r.Int64N(300_000)), UnrealisedPnL: Cents(r.Int64N(40_000) - 20_000), Leverage: 1}
			if r.IntN(3) == 0 {
				p.CFD = true
			} else {
				p.Long, p.RealAsset = true, true
			}
			in.Portfolio.Positions = append(in.Portfolio.Positions, p)
		}
		res := Reservation{Amount: Cents(r.Int64N(200_000)), Count: r.IntN(3)}
		res.Short = min(res.Amount, Cents(r.Int64N(100_000)))
		in.State.StartOfDayEquity = Cents(500_000 + r.Int64N(1_500_000))
		in.State.HighWaterMark = in.State.StartOfDayEquity + Cents(r.Int64N(600_000))
		switch r.IntN(10) {
		case 0:
			in.State.Day = RiskDay(in.Now.Add(-24 * time.Hour)) // not observed today
		case 1:
			in.State.HighWaterMark = 0
		}
		in.Limits.TradingBudget = Cents(r.Int64N(3_000_000))
		in.Limits.DailyLoss = BasisPoints(10 + r.Int64N(1_990))
		in.Limits.Drawdown = BasisPoints(50 + r.Int64N(4_950))
		in.Limits.MaxPosition = 0
		in.Limits.MaxOpenPositions = 1 + r.IntN(HardMaxOpenPositions)
		in.Limits.MaxShort = BasisPoints(r.Int64N(int64(HardMaxShort) + 1))
		in.SessionClose = in.Now.Add(time.Duration(r.Int64N(int64(2 * time.Hour))))
		if r.IntN(2) == 0 {
			in.Direction = Short
			in.StopLoss = in.Entry + 1 + Micros(r.Int64N(int64(in.Entry)))
		} else {
			in.StopLoss = in.Entry - 1 - Micros(r.Int64N(int64(in.Entry)-1))
		}
		in = res.Apply(in)
		st := AccountRiskState{State: in.State}
		st.State.Reserved, st.State.ReservedOrders, st.State.ReservedShort = 0, 0, 0 // not stored: from the reservation
		f := LimitDistances(in.Portfolio, st, in.Limits, res, in.Now,
			map[Market]MarketSession{MarketNYSE: {Known: true, Trading: true, Open: in.SessionOpen, Close: in.SessionClose}})
		d := AssessEntry(in)
		out := false
		for name, v := range liveDistances(f, in.Direction) {
			if v <= 0 {
				out = true
				if d.Allowed {
					t.Fatalf("distance %s = %d but AssessEntry allows a minimal %s entry: %+v\ninput %+v", name, v, in.Direction, f, in)
				}
			}
		}
		if out {
			exhausted++
		}
		if d.Allowed {
			allowed++
			if in.State.Day != f.Day || !f.DayKnown {
				t.Fatalf("an entry allowed without today's figures: %+v", f)
			}
		}
	}
	if allowed < 2_000 || exhausted < 2_000 {
		t.Fatalf("too few cases: %d allowed, %d with a distance used up", allowed, exhausted)
	}
}

// clampBig is v saturated at the int64 range.
func clampBig(v *big.Int) Cents {
	switch {
	case v.Cmp(big.NewInt(math.MaxInt64)) > 0:
		return math.MaxInt64
	case v.Cmp(big.NewInt(math.MinInt64)) < 0:
		return math.MinInt64
	}
	return Cents(v.Int64())
}

func bigOf(c Cents) *big.Int { return big.NewInt(int64(c)) }

// bpOfBig is bp of c truncated towards zero.
func bpOfBig(c Cents, bp BasisPoints) *big.Int {
	v := new(big.Int).Mul(bigOf(c), big.NewInt(int64(bp)))
	return v.Quo(v, big.NewInt(10_000))
}

// TestLimitDistancesHugeValues checks every money figure against math/big for amounts over
// the whole int64 range: sums never wrap, they saturate.
func TestLimitDistancesHugeValues(t *testing.T) {
	r := rand.New(rand.NewPCG(71, 72))
	anyCents := func() Cents {
		switch r.IntN(4) {
		case 0:
			return Cents(r.Int64())
		case 1:
			return Cents(-r.Int64())
		case 2:
			return Cents(math.MaxInt64 - r.Int64N(1_000))
		}
		return Cents(r.Int64N(1_000_000_000))
	}
	now := t0
	for range 20_000 {
		pf := Portfolio{AvailableCash: anyCents(), PendingOpen: anyCents(), AsOf: now}
		for j := range r.IntN(5) {
			pf.Positions = append(pf.Positions, Position{ID: int64(j), Amount: anyCents(), UnrealisedPnL: anyCents(), Long: r.IntN(2) == 0})
		}
		st := AccountRiskState{State: RiskState{Day: RiskDay(now), StartOfDayEquity: anyCents(), HighWaterMark: anyCents()}}
		res := Reservation{Amount: anyCents(), Short: anyCents(), Count: r.IntN(5)}
		l := DefaultRiskLimits
		l.TradingBudget, l.DailyLoss, l.Drawdown = anyCents(), BasisPoints(r.Int64N(10_001)), BasisPoints(r.Int64N(10_001))
		l.MaxShort = BasisPoints(r.Int64N(int64(HardMaxShort) + 1))
		f := LimitDistances(pf, st, l, res, now, nil)

		eq := new(big.Int).Add(bigOf(pf.AvailableCash), bigOf(pf.PendingOpen))
		inv := new(big.Int).Add(bigOf(pf.PendingOpen), bigOf(res.Amount))
		short := bigOf(res.Short)
		for _, p := range pf.Positions {
			eq.Add(eq, bigOf(p.Amount)).Add(eq, bigOf(p.UnrealisedPnL))
			inv.Add(inv, bigOf(p.Amount))
			if !p.Long {
				short.Add(short, bigOf(p.Amount))
			}
		}
		equity := clampBig(eq)
		budget := Cents(0)
		if l.TradingBudget > 0 {
			budget = min(l.TradingBudget, equity)
		}
		want := map[string][2]Cents{
			"equity":     {f.Equity, equity},
			"cashLeft":   {f.CashLeft, clampBig(new(big.Int).Sub(bigOf(pf.AvailableCash), bigOf(res.Amount)))},
			"budget":     {f.Budget, budget},
			"budgetUsed": {f.BudgetUsed, clampBig(inv)},
			"budgetLeft": {f.BudgetLeft, clampBig(new(big.Int).Sub(bigOf(budget), inv))},
			"shortUsed":  {f.ShortUsed, clampBig(short)},
			"shortCap":   {f.ShortCap, clampBig(bpOfBig(budget, l.MaxShort))},
			"shortLeft":  {f.ShortLeft, clampBig(new(big.Int).Sub(bpOfBig(budget, l.MaxShort), short))},
		}
		if sod := st.State.StartOfDayEquity; sod > 0 {
			floor := new(big.Int).Sub(bigOf(sod), bpOfBig(sod, l.DailyLoss))
			want["dayPnL"] = [2]Cents{f.DayPnL, clampBig(new(big.Int).Sub(eq, bigOf(sod)))}
			want["dailyFloor"] = [2]Cents{f.DailyFloor, clampBig(floor)}
			want["toDailyFloor"] = [2]Cents{f.ToDailyFloor, clampBig(new(big.Int).Sub(eq, floor))}
		} else if f.DayKnown {
			t.Fatalf("day figures without a start-of-day equity: %+v", f)
		}
		hwm := max(st.State.HighWaterMark, equity)
		ddFloor := new(big.Int)
		if hwm > 0 {
			ddFloor.Sub(bigOf(hwm), bpOfBig(hwm, l.Drawdown))
		}
		want["drawdownFloor"] = [2]Cents{f.DrawdownFloor, clampBig(ddFloor)}
		want["toDrawdownFloor"] = [2]Cents{f.ToDrawdownFloor, clampBig(new(big.Int).Sub(eq, ddFloor))}
		for name, gw := range want {
			if gw[0] != gw[1] {
				t.Fatalf("%s = %d, want %d\nportfolio %+v\nstate %+v\nreservation %+v\nlimits %+v", name, gw[0], gw[1], pf, st, res, l)
			}
		}
	}
}

func TestMulDivTrunc(t *testing.T) {
	const maxI, minI = int64(math.MaxInt64), int64(math.MinInt64)
	for _, tt := range []struct{ a, b, c, want int64 }{
		{10, 20, 3, 66},
		{-10, 20, 3, -66}, // truncated towards zero, as int64 division
		{10, -20, 3, -66},
		{-10, -20, 3, 66},
		{maxI, 10_000, 10_000, maxI},
		{minI, 10_000, 10_000, minI},
		{minI, 1, 1, minI},
		{minI, -1, 1, maxI},
		{maxI, maxI, 1, maxI},
		{minI, maxI, 1, minI},
		{maxI, 2, 4, maxI / 2},
		{0, 5, 1, 0},
		{5, 0, 1, 0},
		{5, 5, 0, 0},
		{5, 5, -1, 0},
	} {
		if got := mulDivTrunc(tt.a, tt.b, tt.c); got != tt.want {
			t.Errorf("mulDivTrunc(%d, %d, %d) = %d, want %d", tt.a, tt.b, tt.c, got, tt.want)
		}
	}
	r := rand.New(rand.NewPCG(81, 82))
	for range 20_000 {
		a, b, c := int64(r.Uint64()), int64(r.Uint64()), 1+r.Int64N(1<<40)
		if r.IntN(2) == 0 {
			b = r.Int64N(20_001) - 10_000
		}
		v := new(big.Int).Mul(big.NewInt(a), big.NewInt(b))
		v.Quo(v, big.NewInt(c))
		if got, want := Cents(mulDivTrunc(a, b, c)), clampBig(v); got != want {
			t.Fatalf("mulDivTrunc(%d, %d, %d) = %d, want %d", a, b, c, got, want)
		}
	}
}

// TestBasisPointsOfHugeAmounts: a limit of a huge amount no longer wraps (PLAN 6.1).
func TestBasisPointsOfHugeAmounts(t *testing.T) {
	big1 := Cents(math.MaxInt64 - 5)
	if got := BasisPoints(2_000).of(big1); got != clampBig(bpOfBig(big1, 2_000)) || got <= 0 {
		t.Fatalf("20 %% of %d = %d", big1, got)
	}
	if got := BasisPoints(200).of(-1_000_050); got != -20_001 {
		t.Fatalf("2 %% of a negative amount = %d", got)
	}
}
