package domain

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestEntriesAllowed(t *testing.T) {
	tests := []struct {
		m    Market
		want bool
	}{
		{MarketNYSE, true}, {MarketASX, true}, {MarketLSE, false}, {"", false}, {"XETRA", false},
	}
	for _, tt := range tests {
		got, why := EntriesAllowed(tt.m)
		if got != tt.want || (!got && why == "") {
			t.Errorf("EntriesAllowed(%q) = %v %q, want %v", tt.m, got, why, tt.want)
		}
	}
	if _, why := EntriesAllowed(MarketLSE); !strings.Contains(why, "OD9") {
		t.Errorf("LSE reason %q does not name OD9", why)
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
	usd := func(v Micros) []CostItem { return []CostItem{{Type: "marketSpread", Value: v, Currency: "USD"}} }
	tests := []struct {
		name      string
		size      Cents
		d         Direction
		entry, tp Micros
		costs     []CostItem
		ok        bool
		gain      Cents
		total     Cents
	}{
		// $1000 at 100 aiming at 106: gain $60; costs $0.10
		{"long below the gain", 1000_00, Long, 100_000_000, 106_000_000, usd(100_000), true, 60_00, 10},
		{"short below the gain", 1000_00, Short, 100_000_000, 94_000_000, usd(100_000), true, 60_00, 10},
		{"costs equal the gain", 1000_00, Long, 100_000_000, 106_000_000, usd(60_000_000), false, 60_00, 60_00},
		{"costs above the gain", 100, Long, 100_000_000, 101_000_000, usd(20_000), false, 1, 2},
		{"costs rounded up", 1000_00, Long, 100_000_000, 100_010_000, usd(9_999), true, 10, 1},
		{
			"several lines, rebate ignored", 1000_00, Long, 100_000_000, 106_000_000,
			[]CostItem{{Type: "a", Value: 30_000_000}, {Type: "rebate", Value: -50_000_000, Currency: "usd"}, {Type: "b", Value: 30_000_000}},
			false, 60_00, 60_00,
		},
		{"no take-profit", 1000_00, Long, 100_000_000, 0, usd(1), false, 0, 0},
		{"take-profit on the losing side (long)", 1000_00, Long, 100_000_000, 99_000_000, usd(1), false, 0, 0},
		{"take-profit on the losing side (short)", 1000_00, Short, 100_000_000, 101_000_000, usd(1), false, 0, 0},
		{"no cost lines", 1000_00, Long, 100_000_000, 106_000_000, nil, false, 0, 0},
		{"cost in GBP", 1000_00, Long, 100_000_000, 106_000_000, []CostItem{{Type: "sdrt", Value: 1, Currency: "GBP"}}, false, 0, 0},
		{"unknown direction", 1000_00, "up", 100_000_000, 106_000_000, usd(1), false, 0, 0},
		{"no size", 0, Long, 100_000_000, 106_000_000, usd(1), false, 0, 0},
		{"huge size does not overflow", math.MaxInt64, Long, 1, 2, usd(1), true, math.MaxInt64, 1},
		{"huge costs saturate", 1000_00, Long, 100_000_000, 106_000_000, []CostItem{{Value: math.MaxInt64}, {Value: math.MaxInt64}}, false, 60_00, math.MaxInt64/10_000 + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, gain, total, why := CostCheck(tt.size, tt.d, tt.entry, tt.tp, tt.costs)
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
