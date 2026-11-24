package pdf

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

const analysisSamplePath = "testdata/analysis-month.pdf"

// analysisFixture is November 2026 ("this month" at 24 November) on two eToro accounts (USD),
// a Trading 212 ISA (GBP) and a Saxo account (AUD), with themes, playbooks, risk events and
// LLM use; extraThemes adds that many more themed trades (a longer page).
func analysisFixture(extraThemes int) domain.Analysis {
	now := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	w, _ := domain.AnalysisWindowFor(domain.AnalysisPresetMonth, "", "", now)
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, time.UTC) }
	tr := func(acct string, pos int64, ins domain.InstrumentID, sym string, m domain.Market, net, fees domain.Cents, known, ours bool, req string, closed time.Time) domain.StatementTrade {
		return domain.StatementTrade{
			Trade: domain.Trade{AccountID: acct, Mode: domain.ModeDemo, Ours: ours, OpenRequestID: req, ClosedTrade: domain.ClosedTrade{
				PositionID: pos, Instrument: ins, ClosedAt: closed, NetProfit: net, Fees: fees, FeesKnown: known, Long: pos != 3, Leverage: 1,
			}},
			Symbol: sym, Market: m,
		}
	}
	dec := func(id string, when time.Time, o domain.DecisionOutcome, v domain.VerdictKind, req, pb string) domain.Decision {
		return domain.Decision{
			ID: id, AccountID: "a1", At: when, Outcome: o, OrderRequestID: req, Verdict: domain.DecisionVerdict{Kind: v},
			Signal: domain.DecisionSignal{Direction: domain.Long, Playbook: pb},
		}
	}
	main := domain.AnalysisAccountInput{
		Account: domain.Account{ID: "a1", Name: "Demo main", Mode: domain.ModeDemo},
		Trades: []domain.StatementTrade{
			tr("a1", 1, 1, "AAPL", domain.MarketNYSE, 25_00, 1_00, true, true, "r1", at(2, 15)),
			tr("a1", 2, 2, "BARC.L", domain.MarketLSE, -40_00, 0, false, true, "r2", at(2, 16)),
			tr("a1", 3, 1, "AAPL", domain.MarketNYSE, 0, 50, true, true, "r3", at(10, 15)),
			tr("a1", 4, 3, "TSLA", domain.MarketNYSE, 99_00, 0, true, false, "", at(10, 16)),
		},
		Snapshots: []domain.EquitySnapshot{
			{AccountID: "a1", At: at(2, 9), Currency: "USD", Equity: 10_000_00},
			{AccountID: "a1", At: at(10, 9), Currency: "USD", Equity: 10_200_00},
			{AccountID: "a1", At: at(12, 9), Currency: "USD", Equity: 9_900_00},
			{AccountID: "a1", At: at(20, 9), Currency: "USD", Equity: 10_100_00},
		},
		Themes: []domain.Theme{{ID: "t1", Name: "AI", Instruments: []domain.InstrumentID{1}}, {ID: "t2", Name: "Banks", Instruments: []domain.InstrumentID{2}}},
		Decisions: []domain.Decision{
			dec("d1", at(2, 14), domain.DecisionOrdered, domain.VerdictApprove, "r1", "trend_breakout"),
			dec("d2", at(2, 15), domain.DecisionOrdered, domain.VerdictApprove, "r2", "mean_reversion"),
			dec("d3", at(5, 14), domain.DecisionRejectedRisk, domain.VerdictReduce, "", "trend_breakout"),
		},
		Events: []domain.Event{
			{ID: "e1", AccountID: "a1", Kind: domain.EventKillSwitch, At: at(3, 10)},
			{ID: "e2", AccountID: "a1", Kind: domain.EventDailyStop, At: at(4, 10)},
		},
		LLM: domain.LLMUsage{Calls: 3, InputTokens: 4_000, OutputTokens: 300, Cost: 120_000},
	}
	for i := range extraThemes {
		ins := domain.InstrumentID(100 + i)
		main.Themes = append(main.Themes, domain.Theme{ID: fmt.Sprintf("x%02d", i), Name: fmt.Sprintf("Theme %02d", i), Instruments: []domain.InstrumentID{ins}})
		main.Trades = append(main.Trades, tr("a1", int64(100+i), ins, fmt.Sprintf("S%02d", i), domain.MarketNYSE, domain.Cents((i%7-3)*1_00), 10, true, true, "", at(3+i%20, 15)))
	}
	in := domain.AnalysisInput{
		Window: w, GeneratedAt: now, AllAccounts: true,
		Accounts: []domain.AnalysisAccountInput{
			main,
			{
				Account:   domain.Account{ID: "a2", Name: "Démo second 日本", Mode: domain.ModeDemo},
				Trades:    []domain.StatementTrade{tr("a2", 21, 4, "CBA.ASX", domain.MarketASX, 60_00, 2_00, true, true, "r21", at(20, 3))},
				Snapshots: []domain.EquitySnapshot{{AccountID: "a2", At: at(5, 9), Currency: "USD", Equity: 500_00}, {AccountID: "a2", At: at(20, 9), Currency: "USD", Equity: 560_00}},
			},
			{
				Account:   domain.Account{ID: "t1", Name: "ISA", Mode: domain.ModeReal, Platform: domain.PlatformTrading212},
				Snapshots: []domain.EquitySnapshot{{AccountID: "t1", At: at(3, 9), Currency: "GBP", Equity: 1_000_00}, {AccountID: "t1", At: at(15, 9), Currency: "GBP", Equity: 1_050_00}},
			},
			{
				Account:   domain.Account{ID: "s1", Name: "Saxo AU", Mode: domain.ModeDemo, Platform: domain.PlatformSaxo},
				Snapshots: []domain.EquitySnapshot{{AccountID: "s1", At: at(4, 1), Currency: "AUD", Equity: 2_500_00}, {AccountID: "s1", At: at(18, 5), Currency: "AUD", Equity: 2_450_50}},
			},
		},
	}
	return domain.BuildAnalysis(in)
}

func renderAnalysis(t *testing.T, an domain.Analysis) []byte {
	t.Helper()
	b, err := New().RenderAnalysis(context.Background(), an)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// mediaBoxes returns the page sizes (points) the PDF declares.
func mediaBoxes(t *testing.T, b []byte) [][2]float64 {
	t.Helper()
	var out [][2]float64
	for _, m := range regexp.MustCompile(`/MediaBox \[0 0 ([0-9.]+) ([0-9.]+)\]`).FindAllSubmatch(b, -1) {
		w, err1 := strconv.ParseFloat(string(m[1]), 64)
		h, err2 := strconv.ParseFloat(string(m[2]), 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("media box %q", m[0])
		}
		out = append(out, [2]float64{w, h})
	}
	if len(out) == 0 {
		t.Fatal("no media box")
	}
	return out
}

// pageMM is the page's size in millimetres (every media box must agree).
func pageMM(t *testing.T, b []byte) (w, h float64) {
	t.Helper()
	boxes := mediaBoxes(t, b)
	for _, x := range boxes[1:] {
		if x != boxes[0] {
			t.Fatalf("media boxes differ: %v", boxes)
		}
	}
	return boxes[0][0] * 25.4 / 72, boxes[0][1] * 25.4 / 72
}

// TestGoldenAnalysis renders this month's analysis: one A4 page with the header, the KPI
// tiles, the narrative's headings and figures, the charts' captions, the tables, USD, £ and A$
// never added together, unknown fees and the disclaimer; the same bytes twice; and the same
// text as the committed sample (testdata/analysis-month.pdf, regenerated with -update).
func TestGoldenAnalysis(t *testing.T) {
	an := analysisFixture(0)
	b := renderAnalysis(t, an)
	if again := renderAnalysis(t, an); !bytes.Equal(b, again) {
		t.Fatal("the same analysis gave different bytes")
	}
	text, pages := pdfText(t, b)
	if pages != 1 {
		t.Fatalf("pages = %d", pages)
	}
	// The page follows the rule: 210 mm wide, A4's 297 mm or the content rounded up to 10 mm.
	want := AnalysisPageHeight(newAnalysisDoc(an, analysisMeasureH).layout())
	w, h := pageMM(t, b)
	if math.Abs(w-210) > 0.1 || math.Abs(h-want) > 0.1 || h < 297 {
		t.Fatalf("page %.1f × %.1f mm, want 210 × %.0f", w, h, want)
	}
	for _, want := range []string{
		"cactus", "Trade", "Trading analysis", "Analysis · November 2026", "This month · 30 risk days", "All 4 accounts you can see",
		"Demo main (eToro, demo); Démo second ?? (eToro, demo); ISA (Trading 212, real, view-only); Saxo AU (Saxo, demo, view-only)",
		// KPI tiles
		"Closed trades", "Win rate", "50.00 %", "Realised P&L (after fees)", "$45.00", "Average per trade", "$11.25", "Fees reported by eToro", "$3.50",
		"+ unknown for 1 trade", "Largest drawdown", "Opened by cactusTrade", "80.00 %", "LLM cost", "$0.1200",
		// narrative
		"What happened", "Overview", "What went well", "What went badly", "Costs", "Risk & discipline", "Activity",
		"the best was $60.00 (CBA.ASX on Démo second ??, closed 20 Nov 2026)", "the worst was -$40.00 (BARC.L on Demo main, closed 2 Nov 2026)",
		"from A$2,500.00 to A$2,450.50 (AUD, 1 account) and from £1,000.00 to £1,050.00 (GBP, 1 account) and from $10,500.00 to",
		"Past results say nothing about future results.",
		// charts
		"Realised P&L by day (US dollars)", "Equity in USD (2 accounts)", "largest drawdown $300.00 (2.80 %)",
		"Equity in GBP (1 account)", "Equity in AUD (1 account)", "By exchange", "By trading account",
		"By theme", "By playbook", "By direction", "By outcome", "Wins, losses and breakeven (trades)", "Wins: 2 of 4",
		// tables
		"Figures by group (cactusTrade's closed trades, US dollars, after eToro's fees)", "By day (days with trades)", "Mon 2 Nov", "-$15.00", "LSE", "NYSE", "ASX", "AI", "Banks", "No theme", "trend_breakout", "mean_reversion", "Not recorded", "Long", "Short", "Wins", "Losses", "Breakeven",
		"Total USD (2 accounts)", "Total GBP (1 account)", "Total AUD (1 account)", "-A$49.50", "Saxo AU (view-only)",
		"Kill switch used", "Daily loss limit stopped new entries", "Decisions logged", "Reviewer: Approved", "Closed trades not opened by cactusTrade",
		"How these figures are made", "never added together",
		"Generated 24 November 2026 15:00 UTC · cactusTrade is software, not financial advice — trading can lose money", fmt.Sprintf("One page · 210 × %.0f mm", want),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q", want)
		}
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(analysisSamplePath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(analysisSamplePath, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sample, err := os.ReadFile(analysisSamplePath)
	if err != nil {
		t.Fatalf("%v (create it with -update)", err)
	}
	if got, _ := pdfText(t, sample); got != text {
		t.Fatalf("the committed sample is stale: run go test ./internal/adapters/pdf -run TestGoldenAnalysis -update")
	}
}

// TestAnalysisShortPeriodIsA4: one account, one day, a few trades: an A4 page.
func TestAnalysisShortPeriodIsA4(t *testing.T) {
	now := time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC)
	w, _ := domain.AnalysisWindowFor(domain.AnalysisPresetToday, "", "", now)
	at := func(h int) time.Time { return time.Date(2026, 11, 24, h, 0, 0, 0, time.UTC) }
	var trades []domain.StatementTrade
	for i, net := range []domain.Cents{12_00, -5_00, 3_00} {
		trades = append(trades, domain.StatementTrade{Symbol: "AAPL", Market: domain.MarketNYSE, Trade: domain.Trade{AccountID: "a1", Ours: true, ClosedTrade: domain.ClosedTrade{
			PositionID: int64(i + 1), Instrument: 1, ClosedAt: at(14 + i), NetProfit: net, FeesKnown: true, Long: true, Leverage: 1,
		}}})
	}
	an := domain.BuildAnalysis(domain.AnalysisInput{Window: w, GeneratedAt: now, AllAccounts: true, Accounts: []domain.AnalysisAccountInput{{
		Account: domain.Account{ID: "a1", Name: "Demo main", Mode: domain.ModeDemo}, Trades: trades,
		Snapshots: []domain.EquitySnapshot{{AccountID: "a1", At: at(9), Currency: "USD", Equity: 10_000_00}, {AccountID: "a1", At: at(14), Currency: "USD", Equity: 10_010_00}},
	}}})
	b := renderAnalysis(t, an)
	text, pages := pdfText(t, b)
	if w, h := pageMM(t, b); pages != 1 || math.Abs(w-210) > 0.1 || math.Abs(h-297) > 0.1 || !strings.Contains(text, "One page · 210 × 297 mm") {
		t.Fatalf("%d pages, %.1f × %.1f mm", pages, w, h)
	}
}

// TestAnalysisPageGrows: more content makes a taller single page (never a second one), sized
// by the rule: A4 when it fits, else the content rounded up to 10 mm.
func TestAnalysisPageGrows(t *testing.T) {
	prev := 0.0
	for _, n := range []int{0, 15, 40, 80} {
		b := renderAnalysis(t, analysisFixture(n))
		text, pages := pdfText(t, b)
		w, h := pageMM(t, b)
		if pages != 1 || math.Abs(w-210) > 0.1 {
			t.Fatalf("%d themes: %d pages, %.1f mm wide", n, pages, w)
		}
		if h+0.1 < prev {
			t.Fatalf("%d themes: %.1f mm, shorter than %.1f mm", n, h, prev)
		}
		if h > 297.1 && math.Abs(h/10-math.Round(h/10)) > 0.01 {
			t.Fatalf("%d themes: %.2f mm is not a whole 10 mm", n, h)
		}
		if !strings.Contains(text, fmt.Sprintf("One page · 210 × %.0f mm", h)) || !strings.Contains(text, "trading can lose money") {
			t.Fatalf("%d themes: footer missing", n)
		}
		if n == 80 && (h <= 297.1 || !regexp.MustCompile(`… and \d+ more \(see the Analysis page\)`).MatchString(text)) {
			t.Fatalf("80 themes: %.1f mm, caps not shown", h)
		}
		prev = h
	}
	for _, tc := range []struct{ bottom, want float64 }{{0, 297}, {283, 297}, {283.5, 300}, {286, 300}, {300, 320}, {1000, 1020}} {
		if got := AnalysisPageHeight(tc.bottom); got != tc.want {
			t.Errorf("AnalysisPageHeight(%v) = %v, want %v", tc.bottom, got, tc.want)
		}
	}
}

// TestAnalysisEmptyAndCancelled: an analysis without trades or snapshots still makes one A4
// page; a cancelled request makes nothing.
func TestAnalysisEmptyAndCancelled(t *testing.T) {
	w, _ := domain.AnalysisWindowFor(domain.AnalysisPresetToday, "", "", time.Date(2026, 11, 24, 15, 0, 0, 0, time.UTC))
	an := domain.BuildAnalysis(domain.AnalysisInput{Window: w, GeneratedAt: w.From, AllAccounts: true})
	text, pages := pdfText(t, renderAnalysis(t, an))
	for _, want := range []string{"No trades.", "No equity snapshots in the period.", "No accounts.", "cactusTrade closed no trades in the period."} {
		if !strings.Contains(text, want) {
			t.Errorf("empty text lacks %q", want)
		}
	}
	if pages != 1 {
		t.Fatalf("pages %d", pages)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().RenderAnalysis(ctx, an); err == nil {
		t.Fatal("rendered for a cancelled request")
	}
}
