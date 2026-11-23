package strategy

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// scripted returns a decision function that signals at the given bar indices.
func scripted(at map[int]domain.Candidate) func([]domain.Candle, domain.Cents) Decision {
	return func(bs []domain.Candle, _ domain.Cents) Decision {
		if c, ok := at[len(bs)-1]; ok {
			return Decision{Candidate: &c}
		}
		return Decision{}
	}
}

func TestBacktestFillRules(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 14, 30, 0, 0, time.UTC)
	day2 := t0.Add(24 * time.Hour)
	m := func(f float64) domain.Micros { return domain.Micros(math.Round(f * 1e6)) }
	bars := []domain.Candle{
		bar(t0, 0, 100, 100, 100, 100),
		bar(t0, 1, 100, 100, 100, 100),     // signal A (stop 99, tp 102)
		bar(t0, 2, 100, 101, 99.5, 100.5),  // A fills at 100 open
		bar(t0, 3, 100.5, 102.5, 100, 102), // A: target 102 reached → exit 102
		bar(t0, 4, 102, 102, 102, 102),     // signal B (stop 101, tp 110)
		bar(t0, 5, 102, 102, 98, 99),       // B fills at 102; low 98 ≤ stop 101 → exit at 101
		bar(t0, 6, 99, 99, 99, 99),         // signal C (stop 100: open 98 below → skipped)
		bar(t0, 7, 98, 98, 98, 98),
		bar(t0, 8, 98, 98, 98, 98),           // signal D (stop 90, tp 120)
		bar(t0, 9, 98, 99, 97, 98.5),         // D fills; last bar of the session → session_end at 98.5
		bar(day2, 0, 98.5, 98.5, 98.5, 98.5), // a new session
		bar(day2, 1, 98.5, 98.5, 98.5, 98.5),
	}
	sig := map[int]domain.Candidate{
		1: {Playbook: "a", StopLossRate: m(99), TakeProfitRate: m(102), SuggestedSize: 50_000},
		4: {Playbook: "b", StopLossRate: m(101), TakeProfitRate: m(110), SuggestedSize: 50_000},
		6: {Playbook: "c", StopLossRate: m(100), TakeProfitRate: m(110), SuggestedSize: 50_000},
		8: {Playbook: "d", StopLossRate: m(90), TakeProfitRate: m(120), SuggestedSize: 10_000_000}, // more than the cash
		9: {Playbook: "e", StopLossRate: m(90), TakeProfitRate: m(120), SuggestedSize: 50_000},     // never: last bar of session
	}
	rep := Backtest(1, bars, BacktestConfig{StartEquity: 100_000, Warmup: 1, decide: scripted(sig)})
	if len(rep.Trades) != 3 || rep.SkippedEntries != 1 {
		t.Fatalf("trades %+v skipped %d", rep.Trades, rep.SkippedEntries)
	}
	a, b, d := rep.Trades[0], rep.Trades[1], rep.Trades[2]
	if a.Exit != "take_profit" || a.ExitPrice != m(102) || a.PnL != 1000 {
		t.Errorf("A = %+v", a)
	}
	if b.Exit != "stop" || b.ExitPrice != m(101) {
		t.Errorf("B = %+v", b)
	}
	if d.Exit != "session_end" || d.ExitPrice != m(98.5) || !d.Entered.Before(day2) || !d.Exited.Before(day2) {
		t.Errorf("D must close in its own session: %+v", d)
	}
	if d.Amount > 100_000+1000 {
		t.Errorf("D spent more than the cash: %d", d.Amount)
	}
	if rep.WinRate < 0.66 || rep.WinRate > 0.67 || rep.MaxDrawdown <= 0 { // A and D win, B loses
		t.Errorf("win rate %v drawdown %v", rep.WinRate, rep.MaxDrawdown)
	}

	// the same trades with costs: strictly worse, and the costs are reported
	costly := Backtest(1, bars, BacktestConfig{
		StartEquity: 100_000, Warmup: 1, decide: scripted(sig),
		Costs: Costs{Spread: 0.001, BuyFee: 0.005, SellFee: 0.001, Recorded: "test"},
	})
	if costly.EndEquity >= rep.EndEquity || costly.CostsPaid <= 0 {
		t.Errorf("costs: %d vs %d, paid %d", costly.EndEquity, rep.EndEquity, costly.CostsPaid)
	}
	if !strings.Contains(costly.String(), "LOST") && !strings.Contains(costly.String(), "worse") {
		t.Errorf("report hides a poor result:\n%s", costly)
	}
}

func TestBacktestGapAndBothInOneBar(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 14, 30, 0, 0, time.UTC)
	m := func(f float64) domain.Micros { return domain.Micros(math.Round(f * 1e6)) }
	bars := []domain.Candle{
		bar(t0, 0, 100, 100, 100, 100),
		bar(t0, 1, 100, 100, 100, 100),
		bar(t0, 2, 100, 100, 100, 100),
		bar(t0, 3, 96, 106, 95, 100), // gap below the stop and the target in range: stop first, at the open
		bar(t0, 4, 100, 100, 100, 100),
	}
	sig := map[int]domain.Candidate{1: {Playbook: "x", StopLossRate: m(97), TakeProfitRate: m(105), SuggestedSize: 10_000}}
	rep := Backtest(1, bars, BacktestConfig{StartEquity: 100_000, Warmup: 1, decide: scripted(sig)})
	if len(rep.Trades) != 1 || rep.Trades[0].Exit != "stop" || rep.Trades[0].ExitPrice != m(96) {
		t.Fatalf("gap through the stop must fill at the open: %+v", rep.Trades)
	}
}

func TestBacktestWithEngine(t *testing.T) {
	bars := synth(5, 900)
	// four "sessions" of 225 bars
	same := func(a, b time.Time) bool {
		return a.Sub(bars[0].Start)/(225*time.Minute) == b.Sub(bars[0].Start)/(225*time.Minute)
	}
	rep := Backtest(9, bars, BacktestConfig{
		Strategy: Config{FitWindow: 200, RefitEvery: 50}, StartEquity: 1_000_000, SameSession: same,
		Costs: Costs{Spread: 0.0005, BuyFee: 0.001, Recorded: "synthetic"},
	})
	if rep.Bars == 0 || rep.Refits == 0 {
		t.Fatalf("report = %+v", rep)
	}
	for _, tr := range rep.Trades {
		if !same(tr.Entered, tr.Exited) {
			t.Fatalf("held across sessions: %+v", tr)
		}
		if !tr.Exited.After(tr.Entered) && tr.Exit != "stop" && tr.Exit != "take_profit" {
			t.Fatalf("exit before entry: %+v", tr)
		}
	}
	s := rep.String()
	for _, want := range []string{"walk-forward", "buy and hold", "max drawdown", "verdict", "synthetic"} {
		if !strings.Contains(s, want) {
			t.Errorf("report lacks %q:\n%s", want, s)
		}
	}
	if Backtest(1, bars[:10], BacktestConfig{}).Bars != 0 {
		t.Fatal("too few bars")
	}
	if sharpe([]float64{0.01}) != 0 || sharpe([]float64{0.01, 0.01}) != 0 {
		t.Fatal("degenerate sharpe")
	}
	if !strings.Contains((Report{}).String(), "no trades") {
		t.Fatal("empty report verdict")
	}
}

func TestBacktestShorts(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 14, 30, 0, 0, time.UTC)
	m := func(f float64) domain.Micros { return domain.Micros(math.Round(f * 1e6)) }
	bars := []domain.Candle{
		bar(t0, 0, 100, 100, 100, 100),
		bar(t0, 1, 100, 100, 100, 100),   // short A: stop 101, tp 98
		bar(t0, 2, 100, 100.5, 99, 99),   // A fills at 100
		bar(t0, 3, 99, 99, 97.5, 97.8),   // A: low reaches 98 → target 98
		bar(t0, 4, 98, 98, 98, 98),       // short B: stop 99, tp 90
		bar(t0, 5, 98, 99.5, 97.9, 99.2), // B fills at 98; high 99.5 ≥ stop 99 → stop at 99
		bar(t0, 6, 99, 99, 99, 99),       // short C: stop 100 — next open 101 gaps above: skipped
		bar(t0, 7, 101, 101, 101, 101),
		bar(t0, 8, 101, 101, 101, 101),
	}
	sig := map[int]domain.Candidate{
		1: {Playbook: "a", Direction: domain.Short, StopLossRate: m(101), TakeProfitRate: m(98), SuggestedSize: 50_000},
		4: {Playbook: "b", Direction: domain.Short, StopLossRate: m(99), TakeProfitRate: m(90), SuggestedSize: 50_000},
		6: {Playbook: "c", Direction: domain.Short, StopLossRate: m(100), TakeProfitRate: m(90), SuggestedSize: 50_000},
	}
	rep := Backtest(1, bars, BacktestConfig{StartEquity: 100_000, Warmup: 1, decide: scripted(sig)})
	if len(rep.Trades) != 2 || rep.SkippedEntries != 1 {
		t.Fatalf("trades %+v skipped %d", rep.Trades, rep.SkippedEntries)
	}
	a, b := rep.Trades[0], rep.Trades[1]
	if a.Direction != domain.Short || a.Exit != "take_profit" || a.ExitPrice != m(98) || a.PnL != 1000 {
		t.Errorf("A (short 100 → 98 on $500 = +$10) = %+v", a)
	}
	if b.Exit != "stop" || b.ExitPrice != m(99) || b.PnL >= 0 {
		t.Errorf("B (short 98 stopped at 99 must lose) = %+v", b)
	}
	if rep.EndEquity != 100_000+a.PnL+b.PnL {
		t.Errorf("equity %d != start + Σ pnl", rep.EndEquity)
	}
	costly := Backtest(1, bars, BacktestConfig{StartEquity: 100_000, Warmup: 1, decide: scripted(sig), Costs: Costs{Spread: 0.002, BuyFee: 0.001}})
	if costly.EndEquity >= rep.EndEquity || costly.CostsPaid <= 0 {
		t.Errorf("short costs: %d vs %d (paid %d)", costly.EndEquity, rep.EndEquity, costly.CostsPaid)
	}
}
