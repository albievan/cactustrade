package domain

import (
	"math"
	"math/big"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"
)

func ct(net Cents, closed time.Time) ClosedTrade {
	return ClosedTrade{PositionID: 1, NetProfit: net, ClosedAt: closed, Fees: 10, FeesKnown: true, Long: true}
}

func TestTradeStats(t *testing.T) {
	at := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	unknownFees := ct(-300, at)
	unknownFees.Fees, unknownFees.FeesKnown = 0, false
	tests := []struct {
		name   string
		trades []ClosedTrade
		want   TradeSummary
	}{
		{"no trades: no win rate", nil, TradeSummary{}},
		{"one of each", []ClosedTrade{ct(500, at), ct(-200, at), ct(0, at)}, TradeSummary{
			Count: 3, Wins: 1, Losses: 1, Breakeven: 1, WinRate: 3_333, HasWinRate: true, Gross: 330, Fees: 30, Net: 300,
		}},
		{"unknown fees are counted apart", []ClosedTrade{ct(500, at), unknownFees}, TradeSummary{
			Count: 2, Wins: 1, Losses: 1, WinRate: 5_000, HasWinRate: true, Gross: 210, Fees: 10, Net: 200, FeesUnknown: 1,
		}},
		{"all losses", []ClosedTrade{ct(-1, at), ct(-2, at)}, TradeSummary{
			Count: 2, Losses: 2, WinRate: 0, HasWinRate: true, Gross: 17, Fees: 20, Net: -3,
		}},
		{"saturates", []ClosedTrade{ct(math.MaxInt64, at), ct(math.MaxInt64, at)}, TradeSummary{
			Count: 2, Wins: 2, WinRate: 10_000, HasWinRate: true, Gross: math.MaxInt64, Fees: 20, Net: math.MaxInt64,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TradeStats(tt.trades); got != tt.want {
				t.Fatalf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestTradeStatsProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(91, 92))
	for range 2_000 {
		var trades []ClosedTrade
		net, fees := new(big.Int), new(big.Int)
		for range r.IntN(30) {
			x := ClosedTrade{NetProfit: Cents(r.Int64N(2_000_001) - 1_000_000)}
			switch r.IntN(4) {
			case 0:
				x.NetProfit = 0
			case 1:
				x.NetProfit = Cents(r.Int64()) // huge
			case 2:
				x.Fees, x.FeesKnown = Cents(r.Int64N(1_000)), true
			}
			trades = append(trades, x)
			net.Add(net, bigOf(x.NetProfit))
			fees.Add(fees, bigOf(x.Fees))
		}
		s := TradeStats(trades)
		if s.Wins+s.Losses+s.Breakeven != s.Count || s.Count != len(trades) {
			t.Fatalf("wins+losses+breakeven != count: %+v", s)
		}
		if s.Net != clampBig(net) || s.Fees != clampBig(fees) || s.Gross != clampBig(new(big.Int).Add(net, fees)) {
			t.Fatalf("sums %+v, want net %s fees %s", s, net, fees)
		}
		if s.HasWinRate != (s.Count > 0) || s.WinRate < 0 || s.WinRate > 10_000 {
			t.Fatalf("win rate %+v", s)
		}
	}
}

func snap(at time.Time, eq Cents) EquitySnapshot {
	return EquitySnapshot{AccountID: "a", At: at, Currency: "USD", Equity: eq}
}

func TestDailyPnL(t *testing.T) {
	d1 := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC) // risk day 2026-11-23
	unknown := ct(70, d1.Add(time.Hour))
	unknown.Fees, unknown.FeesKnown = 0, false
	trades := []ClosedTrade{ct(100, d1), ct(-40, d1.Add(time.Hour)), unknown, ct(5, d1.Add(48*time.Hour))}
	snaps := []EquitySnapshot{
		snap(d1.Add(24*time.Hour+time.Hour), 1_200), // 2026-11-24, given out of order
		snap(d1, 1_000), snap(d1.Add(2*time.Hour), 1_030),
		snap(d1.Add(24*time.Hour), 1_100),
	}
	got := DailyPnL(trades, snaps)
	want := []DayPnL{
		{Day: "2026-11-23", Trades: 3, Realised: 130, Fees: 20, FeesUnknown: 1, EquityKnown: true, EquityEnd: 1_030, EquityChange: 30},
		{Day: "2026-11-24", EquityKnown: true, EquityEnd: 1_200, EquityChange: 170},
		{Day: "2026-11-25", Trades: 1, Realised: 5, Fees: 10},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if got := DailyPnL(nil, nil); len(got) != 0 {
		t.Fatalf("empty history = %+v", got)
	}
}

// TestDailyPnLBoundary: 21:59:59 and 22:00:00 UTC are on different risk days.
func TestDailyPnLBoundary(t *testing.T) {
	before := time.Date(2026, 11, 23, 21, 59, 59, 0, time.UTC)
	after := before.Add(time.Second)
	got := DailyPnL([]ClosedTrade{ct(1, before), ct(2, after)}, nil)
	if len(got) != 2 || got[0].Day != "2026-11-23" || got[1].Day != "2026-11-24" || got[0].Realised != 1 || got[1].Realised != 2 {
		t.Fatalf("days = %+v", got)
	}
}

func TestDailyPnLProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(93, 94))
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for range 1_000 {
		var trades []ClosedTrade
		sum := new(big.Int)
		for range r.IntN(50) {
			x := ct(Cents(r.Int64()-r.Int64()), start.Add(time.Duration(r.Int64N(int64(30*24*time.Hour)))))
			trades = append(trades, x)
			sum.Add(sum, bigOf(x.NetProfit))
		}
		days := DailyPnL(trades, nil)
		n, total := 0, new(big.Int)
		for i, d := range days {
			n += d.Trades
			total.Add(total, bigOf(d.Realised))
			if i > 0 && days[i-1].Day >= d.Day {
				t.Fatalf("days not ascending: %+v", days)
			}
		}
		if n != len(trades) {
			t.Fatalf("%d trades in days, want %d", n, len(trades))
		}
		// With moderate sums no day saturates: the days add up to the total.
		if sum.IsInt64() && total.Cmp(sum) != 0 && !anySaturated(days) {
			t.Fatalf("days sum to %s, want %s", total, sum)
		}
	}
}

func anySaturated(days []DayPnL) bool {
	for _, d := range days {
		if d.Realised == math.MaxInt64 || d.Realised == math.MinInt64 {
			return true
		}
	}
	return false
}

func TestDrawdown(t *testing.T) {
	at := time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)
	series := func(eqs ...Cents) []EquitySnapshot {
		var out []EquitySnapshot
		for i, e := range eqs {
			out = append(out, snap(at.Add(time.Duration(i)*time.Hour), e))
		}
		return out
	}
	tests := []struct {
		name string
		in   []EquitySnapshot
		want DrawdownFigures
	}{
		{"no snapshots", nil, DrawdownFigures{}},
		{"one snapshot", series(1_000), DrawdownFigures{Known: true, Peak: 1_000, PeakAt: at}},
		{"rise, fall, partial recovery", series(1_000, 2_000, 1_500, 1_800), DrawdownFigures{
			Known: true, Peak: 2_000, PeakAt: at.Add(time.Hour), MaxDrawdown: 500, MaxDrawdownBp: 2_500, Current: 200, CurrentBp: 1_000,
		}},
		{"largest fall and largest relative fall differ", series(100, 50, 1_000, 900, 1_000), DrawdownFigures{
			Known: true, Peak: 1_000, PeakAt: at.Add(2 * time.Hour), MaxDrawdown: 100, MaxDrawdownBp: 5_000,
		}},
		{"negative equity counts as 0", series(1_000, -500), DrawdownFigures{
			Known: true, Peak: 1_000, PeakAt: at, MaxDrawdown: 1_000, MaxDrawdownBp: 10_000, Current: 1_000, CurrentBp: 10_000,
		}},
		{"out of order input", []EquitySnapshot{snap(at.Add(time.Hour), 500), snap(at, 1_000)}, DrawdownFigures{
			Known: true, Peak: 1_000, PeakAt: at, MaxDrawdown: 500, MaxDrawdownBp: 5_000, Current: 500, CurrentBp: 5_000,
		}},
		{"huge values", series(math.MaxInt64, 0), DrawdownFigures{
			Known: true, Peak: math.MaxInt64, PeakAt: at, MaxDrawdown: math.MaxInt64, MaxDrawdownBp: 10_000, Current: math.MaxInt64, CurrentBp: 10_000,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Drawdown(tt.in); got != tt.want {
				t.Fatalf("got  %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestDrawdownProperties(t *testing.T) {
	r := rand.New(rand.NewPCG(95, 96))
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for range 5_000 {
		n := 1 + r.IntN(40)
		var snaps []EquitySnapshot
		rising := r.IntN(3) == 0
		eq := Cents(r.Int64N(1_000_000))
		for i := range n {
			if rising {
				eq += Cents(r.Int64N(1_000))
			} else {
				eq = Cents(r.Int64N(2_000_000)) - 100_000
				if r.IntN(10) == 0 {
					eq = Cents(r.Int64())
				}
			}
			snaps = append(snaps, snap(at.Add(time.Duration(i)*15*time.Minute), eq))
		}
		f := Drawdown(snaps)
		if !f.Known || f.MaxDrawdown < 0 || f.MaxDrawdown > f.Peak || f.Current < 0 || f.Current > f.MaxDrawdown ||
			f.MaxDrawdownBp < 0 || f.MaxDrawdownBp > 10_000 || f.CurrentBp > f.MaxDrawdownBp {
			t.Fatalf("figures out of bounds: %+v", f)
		}
		if rising && (f.MaxDrawdown != 0 || f.Current != 0 || f.MaxDrawdownBp != 0) {
			t.Fatalf("a non-decreasing series has a drawdown: %+v", f)
		}
	}
}

func TestTradeValidate(t *testing.T) {
	at := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	ok := Trade{AccountID: "a", Mode: ModeDemo, ClosedTrade: ClosedTrade{PositionID: 1, ClosedAt: at}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Trade){
		"no account":         func(x *Trade) { x.AccountID = "" },
		"no position":        func(x *Trade) { x.PositionID = 0 },
		"no mode":            func(x *Trade) { x.Mode = "" },
		"no close time":      func(x *Trade) { x.ClosedAt = time.Time{} },
		"unknown fees not 0": func(x *Trade) { x.Fees = 5 },
	} {
		x := ok
		mut(&x)
		if err := x.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if (ClosedTrade{Long: true}).Direction() != Long || (ClosedTrade{}).Direction() != Short {
		t.Fatal("Direction")
	}
}

func TestRiskDayStart(t *testing.T) {
	for _, tt := range []struct{ at, want time.Time }{
		{time.Date(2026, 11, 23, 21, 59, 59, 0, time.UTC), time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC)},
		{time.Date(2026, 11, 23, 22, 0, 0, 0, time.UTC), time.Date(2026, 11, 23, 22, 0, 0, 0, time.UTC)},
		{time.Date(2026, 11, 24, 0, 30, 0, 0, time.UTC), time.Date(2026, 11, 23, 22, 0, 0, 0, time.UTC)},
		{time.Date(2026, 11, 24, 8, 59, 0, 0, time.FixedZone("AEDT", 11*3600)), time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC)},
	} {
		got := RiskDayStart(tt.at)
		if !got.Equal(tt.want) || RiskDay(got) != RiskDay(tt.at) || RiskDay(got.Add(-time.Nanosecond)) == RiskDay(tt.at) {
			t.Errorf("RiskDayStart(%s) = %s, want %s", tt.at, got, tt.want)
		}
	}
}
