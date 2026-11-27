package domain

import (
	"math"
	"testing"
	"time"
)

func TestEntriesAllowed(t *testing.T) {
	tests := []struct {
		m    Market
		want bool
	}{
		{MarketNYSE, true}, {MarketASX, true}, {MarketLSE, true}, {"", false}, {"XETRA", false},
	}
	for _, tt := range tests {
		got, why := EntriesAllowed(tt.m)
		if got != tt.want || (!got && why == "") {
			t.Errorf("EntriesAllowed(%q) = %v %q, want %v", tt.m, got, why, tt.want)
		}
	}
}

func TestNextDecision(t *testing.T) {
	at := func(h, m, s int) time.Time { return time.Date(2026, 11, 23, h, m, s, 0, time.UTC) }
	tests := []struct {
		now, want time.Time
	}{
		{at(14, 30, 0), at(14, 30, 5)},
		{at(14, 30, 4), at(14, 30, 5)},
		{at(14, 30, 5), at(14, 35, 5)},
		{at(14, 33, 0), at(14, 35, 5)},
		{at(14, 34, 59), at(14, 35, 5)},
		{at(23, 59, 30), time.Date(2026, 11, 24, 0, 0, 5, 0, time.UTC)},
	}
	for _, tt := range tests {
		if got := NextDecision(tt.now, 5*time.Minute, 5*time.Second); !got.Equal(tt.want) {
			t.Errorf("NextDecision(%s) = %s, want %s", tt.now, got, tt.want)
		}
	}
	// another time zone: the same instant
	syd, _ := time.LoadLocation("Australia/Sydney")
	if got := NextDecision(at(14, 33, 0).In(syd), 5*time.Minute, 5*time.Second); !got.Equal(at(14, 35, 5)) {
		t.Errorf("Sydney time = %s", got)
	}
	if got := NextDecision(at(14, 33, 0), 0, 0); !got.Equal(at(14, 35, 0)) {
		t.Errorf("zero interval defaults to 5 min: %s", got)
	}
}

func TestClosedBarsAndFresh(t *testing.T) {
	start := time.Date(2026, 11, 23, 14, 30, 0, 0, time.UTC)
	iv := 5 * time.Minute
	bars := make([]Candle, 10)
	for i := range bars {
		bars[i] = Candle{Start: start.Add(time.Duration(i) * iv), Close: 1}
	}
	// at 15:15:05 the bar starting 15:15 is forming: dropped; 15:10 closed at 15:15
	now := start.Add(45*time.Minute + 5*time.Second)
	closed := ClosedBars(bars, iv, now)
	if len(closed) != 9 || !closed[8].Start.Equal(start.Add(40*time.Minute)) {
		t.Fatalf("closed = %d bars, last %s", len(closed), closed[len(closed)-1].Start)
	}
	if !BarsFresh(closed, iv, now) {
		t.Fatal("fresh bars reported stale")
	}
	// exactly at the close of the newest bar: it counts as closed
	if got := ClosedBars(bars, iv, start.Add(50*time.Minute)); len(got) != 10 {
		t.Fatalf("bar ending now = %d", len(got))
	}
	if BarsFresh(closed, iv, now.Add(11*time.Minute)) {
		t.Fatal("bars 11 min old reported fresh")
	}
	if BarsFresh(nil, iv, now) || len(ClosedBars(nil, iv, now)) != 0 {
		t.Fatal("no bars")
	}
	if BarsFresh(bars, iv, start) {
		t.Fatal("a bar ending in the future is not fresh")
	}
}

func TestCostCheck(t *testing.T) {
	aud := FXRates{"AUD": 650_000}
	usd := func(v Micros) []CostItem { return []CostItem{{Type: "marketSpread", Value: v, Currency: "USD"}} }
	tests := []struct {
		name      string
		size      Cents
		d         Direction
		entry, tp Micros
		costs     []CostItem
		fx        FXRates
		ok        bool
		gain      Cents
		total     Cents
	}{
		// $1000 at 100 aiming at 106: gain $60; costs $0.10
		{"long below the gain", 1000_00, Long, 100_000_000, 106_000_000, usd(100_000), nil, true, 60_00, 10},
		{"short below the gain", 1000_00, Short, 100_000_000, 94_000_000, usd(100_000), nil, true, 60_00, 10},
		{"costs equal the gain", 1000_00, Long, 100_000_000, 106_000_000, usd(60_000_000), nil, false, 60_00, 60_00},
		{"costs above the gain", 100, Long, 100_000_000, 101_000_000, usd(20_000), nil, false, 1, 2},
		{"costs rounded up", 1000_00, Long, 100_000_000, 100_010_000, usd(9_999), nil, true, 10, 1},
		{
			"several lines, rebate ignored", 1000_00, Long, 100_000_000, 106_000_000,
			[]CostItem{{Type: "a", Value: 30_000_000}, {Type: "rebate", Value: -50_000_000, Currency: "usd"}, {Type: "b", Value: 30_000_000}},
			nil, false, 60_00, 60_00,
		},
		{"no take-profit", 1000_00, Long, 100_000_000, 0, usd(1), nil, false, 0, 0},
		{"take-profit on the losing side (long)", 1000_00, Long, 100_000_000, 99_000_000, usd(1), nil, false, 0, 0},
		{"take-profit on the losing side (short)", 1000_00, Short, 100_000_000, 101_000_000, usd(1), nil, false, 0, 0},
		{"no cost lines", 1000_00, Long, 100_000_000, 106_000_000, nil, nil, false, 0, 0},
		{"cost in GBP, no rate", 1000_00, Long, 100_000_000, 106_000_000, []CostItem{{Type: "sdrt", Value: 1, Currency: "GBP"}}, nil, false, 0, 0},
		{"cost in a currency not converted", 1000_00, Long, 100_000_000, 106_000_000, []CostItem{{Type: "fee", Value: 1, Currency: "EUR"}}, aud, false, 0, 0},
		// AUD 1.00 at 0.65 = $0.65, plus $0.05 spread: $0.70
		{"AUD line converted", 1000_00, Short, 100_000_000, 94_000_000, []CostItem{{Type: "marketSpread", Value: 50_000, Currency: "USD"}, {Type: "overnightFee", Value: 1_000_000, Currency: "AUD"}}, aud, true, 60_00, 70},
		{"AUD zero line converted", 1000_00, Long, 100_000_000, 106_000_000, []CostItem{{Type: "marketSpread", Value: 50_000, Currency: "USD"}, {Type: "overnightFee", Value: 0, Currency: "AUD"}}, aud, true, 60_00, 5},
		{"AUD line, no AUD rate", 1000_00, Long, 100_000_000, 106_000_000, []CostItem{{Type: "overnightFee", Value: 0, Currency: "AUD"}}, FXRates{"GBX": 12_700}, false, 0, 0},
		// 100 pence at GBP 1.27: $1.27
		{"GBX line converted", 1000_00, Long, 100_000_000, 106_000_000, []CostItem{{Type: "overnightFee", Value: 100_000_000, Currency: "GBp"}}, FXRates{"GBX": 12_700}, true, 60_00, 127},
		{"conversion rounds up", 1000_00, Long, 100_000_000, 106_000_000, []CostItem{{Type: "overnightFee", Value: 1, Currency: "AUD"}}, aud, true, 60_00, 1},
		{"unknown direction", 1000_00, "up", 100_000_000, 106_000_000, usd(1), nil, false, 0, 0},
		{"no size", 0, Long, 100_000_000, 106_000_000, usd(1), nil, false, 0, 0},
		{"huge size does not overflow", math.MaxInt64, Long, 1, 2, usd(1), nil, true, math.MaxInt64, 1},
		{"huge costs saturate", 1000_00, Long, 100_000_000, 106_000_000, []CostItem{{Value: math.MaxInt64}, {Value: math.MaxInt64}}, nil, false, 60_00, math.MaxInt64/10_000 + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, gain, total, why := CostCheck(tt.size, tt.d, tt.entry, tt.tp, tt.costs, tt.fx)
			if ok != tt.ok || gain != tt.gain || total != tt.total || ok != (why == "") {
				t.Fatalf("CostCheck = %v gain %d costs %d %q, want %v %d %d", ok, gain, total, why, tt.ok, tt.gain, tt.total)
			}
		})
	}
}

func TestMulDiv(t *testing.T) {
	tests := []struct{ a, b, c, want int64 }{
		{6, 7, 2, 21},
		{0, 7, 2, 0},
		{7, 0, 2, 0},
		{7, 7, 0, 0},
		{-1, 7, 1, 0},
		{math.MaxInt64, math.MaxInt64, 1, math.MaxInt64},
		{math.MaxInt64, 4, 2, math.MaxInt64},
		{math.MaxInt64, 2, 4, math.MaxInt64 / 2},
	}
	for _, tt := range tests {
		if got := mulDiv(tt.a, tt.b, tt.c); got != tt.want {
			t.Errorf("mulDiv(%d,%d,%d) = %d, want %d", tt.a, tt.b, tt.c, got, tt.want)
		}
	}
}

func TestFXPair(t *testing.T) {
	tests := []struct {
		cur, sym string
		units    int64
		ok       bool
	}{
		{"AUD", "AUDUSD", 1, true},
		{" aud ", "AUDUSD", 1, true},
		{"GBP", "GBPUSD", 1, true},
		{"GBX", "GBPUSD", 100, true},
		{"GBp", "GBPUSD", 100, true},
		{"USD", "", 0, false},
		{"EUR", "", 0, false},
		{"", "", 0, false},
	}
	for _, tt := range tests {
		sym, units, ok := FXPair(tt.cur)
		if sym != tt.sym || units != tt.units || ok != tt.ok {
			t.Errorf("FXPair(%q) = %q %d %v, want %q %d %v", tt.cur, sym, units, ok, tt.sym, tt.units, tt.ok)
		}
	}
}

func TestFXRateFromQuote(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	q := Quote{Instrument: 7, Bid: 649_000, Ask: 651_000, At: now.Add(-2 * time.Second)}
	if r, err := FXRateFromQuote(q, 1, now, 8*time.Second); err != nil || r != 651_000 {
		t.Fatalf("AUD rate = %d %v, want the ask 651000", r, err)
	}
	gbp := Quote{Bid: 1_269_000, Ask: 1_270_001, At: now}
	if r, err := FXRateFromQuote(gbp, 100, now, 8*time.Second); err != nil || r != 12_701 {
		t.Fatalf("GBX rate = %d %v, want 12701 (rounded up)", r, err)
	}
	bad := []struct {
		name  string
		q     Quote
		units int64
	}{
		{"stale", Quote{Bid: 1, Ask: 2, At: now.Add(-9 * time.Second)}, 1},
		{"future", Quote{Bid: 1, Ask: 2, At: now.Add(2 * time.Second)}, 1},
		{"no time", Quote{Bid: 1, Ask: 2}, 1},
		{"delayed", Quote{Bid: 1, Ask: 2, At: now, Delayed: true}, 1},
		{"no ask", Quote{Bid: 1, At: now}, 1},
		{"crossed", Quote{Bid: 3, Ask: 2, At: now}, 1},
		{"no units", Quote{Bid: 1, Ask: 2, At: now}, 0},
	}
	for _, tt := range bad {
		if r, err := FXRateFromQuote(tt.q, tt.units, now, 8*time.Second); err == nil {
			t.Errorf("%s: rate %d accepted", tt.name, r)
		}
	}
}

func TestToUSDMicros(t *testing.T) {
	tests := []struct {
		v    int64
		rate Micros
		want int64
	}{
		{1_000_000, 650_000, 650_000},
		{1, 650_000, 1},
		{3, 500_000, 2},
		{0, 650_000, 0},
		{-5, 650_000, 0},
		{math.MaxInt64, 2_000_000, math.MaxInt64},
	}
	for _, tt := range tests {
		if got := toUSDMicros(tt.v, tt.rate); got != tt.want {
			t.Errorf("toUSDMicros(%d, %d) = %d, want %d", tt.v, tt.rate, got, tt.want)
		}
	}
}
