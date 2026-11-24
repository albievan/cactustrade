package domain

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

func utc(y int, m time.Month, d, h, mi int) time.Time {
	return time.Date(y, m, d, h, mi, 0, 0, time.UTC)
}

func TestWindowFor(t *testing.T) {
	tests := []struct {
		name     string
		p        StatementPeriod
		day      RiskDate
		from, to time.Time
		first    RiskDate
		last     RiskDate
		key      string
		title    string
	}{
		{"daily", PeriodDaily, "2026-11-24", utc(2026, 11, 23, 22, 0), utc(2026, 11, 24, 22, 0), "2026-11-24", "2026-11-24", "2026-11-24", "Tuesday 24 November 2026"},
		// UTC boundaries do not move when London (29 March), New York (1 November) or Sydney
		// (5 April) change their clocks.
		{"daily London DST", PeriodDaily, "2026-03-30", utc(2026, 3, 29, 22, 0), utc(2026, 3, 30, 22, 0), "2026-03-30", "2026-03-30", "2026-03-30", "Monday 30 March 2026"},
		{"daily New York DST", PeriodDaily, "2026-11-02", utc(2026, 11, 1, 22, 0), utc(2026, 11, 2, 22, 0), "2026-11-02", "2026-11-02", "2026-11-02", "Monday 2 November 2026"},
		{"weekly mid-week day", PeriodWeekly, "2026-11-26", utc(2026, 11, 22, 22, 0), utc(2026, 11, 27, 22, 0), "2026-11-23", "2026-11-27", "2026-W48", "23–27 November 2026"},
		{"weekly Sunday belongs to its ISO week", PeriodWeekly, "2026-11-29", utc(2026, 11, 22, 22, 0), utc(2026, 11, 27, 22, 0), "2026-11-23", "2026-11-27", "2026-W48", "23–27 November 2026"},
		{"weekly across months", PeriodWeekly, "2026-12-01", utc(2026, 11, 29, 22, 0), utc(2026, 12, 4, 22, 0), "2026-11-30", "2026-12-04", "2026-W49", "30 November – 4 December 2026"},
		{"weekly across years", PeriodWeekly, "2027-01-01", utc(2026, 12, 27, 22, 0), utc(2027, 1, 1, 22, 0), "2026-12-28", "2027-01-01", "2026-W53", "28 December 2026 – 1 January 2027"},
		{"weekly over London DST", PeriodWeekly, "2026-03-27", utc(2026, 3, 22, 22, 0), utc(2026, 3, 27, 22, 0), "2026-03-23", "2026-03-27", "2026-W13", "23–27 March 2026"},
		{"monthly", PeriodMonthly, "2026-11-24", utc(2026, 10, 31, 22, 0), utc(2026, 11, 30, 22, 0), "2026-11-01", "2026-11-30", "2026-11", "November 2026"},
		{"monthly February leap year", PeriodMonthly, "2028-02-10", utc(2028, 1, 31, 22, 0), utc(2028, 2, 29, 22, 0), "2028-02-01", "2028-02-29", "2028-02", "February 2028"},
		{"monthly December", PeriodMonthly, "2026-12-31", utc(2026, 11, 30, 22, 0), utc(2026, 12, 31, 22, 0), "2026-12-01", "2026-12-31", "2026-12", "December 2026"},
		{"monthly over London DST", PeriodMonthly, "2026-03-01", utc(2026, 2, 28, 22, 0), utc(2026, 3, 31, 22, 0), "2026-03-01", "2026-03-31", "2026-03", "March 2026"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, err := WindowFor(tc.p, tc.day)
			if err != nil {
				t.Fatal(err)
			}
			if !w.From.Equal(tc.from) || !w.To.Equal(tc.to) || w.FirstDay != tc.first || w.LastDay != tc.last || w.Key != tc.key || w.Title() != tc.title {
				t.Fatalf("got %+v %q", w, w.Title())
			}
			// Every risk day in the window is named by RiskDay of an instant in it.
			if RiskDay(w.From) != tc.first || RiskDay(w.To.Add(-time.Nanosecond)) != tc.last || w.Contains(w.To) || !w.Contains(w.From) {
				t.Fatalf("risk days of %+v", w)
			}
		})
	}
	if _, err := WindowFor("hourly", "2026-11-24"); err == nil {
		t.Fatal("unknown period accepted")
	}
	if _, err := WindowFor(PeriodDaily, "24/11/2026"); err == nil {
		t.Fatal("bad date accepted")
	}
}

func TestLastCompleteWindowAndNextSend(t *testing.T) {
	tests := []struct {
		name string
		p    StatementPeriod
		now  time.Time
		key  string
		next time.Time
	}{
		{"daily before 22:30", PeriodDaily, utc(2026, 11, 24, 22, 29), "2026-11-23", utc(2026, 11, 24, 22, 30)},
		{"daily at 22:30", PeriodDaily, utc(2026, 11, 24, 22, 30), "2026-11-24", utc(2026, 11, 25, 22, 30)},
		{"daily after the boundary (next risk day began)", PeriodDaily, utc(2026, 11, 24, 23, 0), "2026-11-24", utc(2026, 11, 25, 22, 30)},
		{"daily skips Saturday", PeriodDaily, utc(2026, 11, 28, 23, 0), "2026-11-27", utc(2026, 11, 30, 22, 30)},
		{"daily skips Sunday", PeriodDaily, utc(2026, 11, 30, 10, 0), "2026-11-27", utc(2026, 11, 30, 22, 30)},
		{"daily Monday evening", PeriodDaily, utc(2026, 11, 30, 22, 31), "2026-11-30", utc(2026, 12, 1, 22, 30)},
		{"weekly Saturday 05:59", PeriodWeekly, utc(2026, 11, 28, 5, 59), "2026-W47", utc(2026, 11, 28, 6, 0)},
		{"weekly Saturday 06:00", PeriodWeekly, utc(2026, 11, 28, 6, 0), "2026-W48", utc(2026, 12, 5, 6, 0)},
		{"weekly mid-week", PeriodWeekly, utc(2026, 11, 25, 12, 0), "2026-W47", utc(2026, 11, 28, 6, 0)},
		{"monthly the 1st 05:59", PeriodMonthly, utc(2026, 12, 1, 5, 59), "2026-10", utc(2026, 12, 1, 6, 0)},
		{"monthly the 1st 06:00", PeriodMonthly, utc(2026, 12, 1, 6, 0), "2026-11", utc(2027, 1, 1, 6, 0)},
		{"monthly last evening is the next month's risk day", PeriodMonthly, utc(2026, 11, 30, 23, 0), "2026-10", utc(2026, 12, 1, 6, 0)},
		{"monthly March from London DST", PeriodMonthly, utc(2026, 4, 1, 7, 0), "2026-03", utc(2026, 5, 1, 6, 0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, err := LastCompleteWindow(tc.p, tc.now)
			if err != nil || w.Key != tc.key || w.SendAt().After(tc.now) {
				t.Fatalf("LastCompleteWindow = %+v %v, want %s", w, err, tc.key)
			}
			next, err := NextSendAt(tc.p, tc.now)
			if err != nil || !next.Equal(tc.next) || !next.After(tc.now) {
				t.Fatalf("NextSendAt = %v %v, want %v", next, err, tc.next)
			}
		})
	}
	if _, err := LastCompleteWindow("x", time.Now()); err == nil {
		t.Fatal("unknown period accepted")
	}
	if _, err := NextSendAt("x", time.Now()); err == nil {
		t.Fatal("unknown period accepted")
	}
}

func TestStatementPeriodParsing(t *testing.T) {
	for _, p := range StatementPeriods {
		if got, err := ParseStatementPeriod(string(p)); err != nil || got != p || p.Label() == "" {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if _, err := ParseStatementPeriod("yearly"); err == nil {
		t.Fatal("yearly accepted")
	}
	for in, want := range map[string]StatementScope{"": ScopeCombined, "combined": ScopeCombined, "account": ScopeAccount, "exchange": ScopeExchange} {
		if got, err := ParseStatementScope(in); err != nil || got != want {
			t.Fatalf("%q: %v %v", in, got, err)
		}
	}
	if _, err := ParseStatementScope("all"); err == nil {
		t.Fatal("bad scope accepted")
	}
	if d, err := ParseRiskDate("2026-11-24"); err != nil || d != "2026-11-24" {
		t.Fatal(d, err)
	}
	if _, err := ParseRiskDate("2026-13-01"); err == nil {
		t.Fatal("bad date accepted")
	}
	w, _ := WindowFor(PeriodWeekly, "2026-11-24")
	if p := w.Previous(); p.Key != "2026-W47" || p.Next().Key != w.Key {
		t.Fatal(p.Key)
	}
	m, _ := WindowFor(PeriodMonthly, "2026-01-15")
	if p := m.Previous(); p.Key != "2025-12" || p.Next().Key != "2026-01" {
		t.Fatal(p.Key)
	}
}

func TestFormatMoney(t *testing.T) {
	tests := []struct {
		cur  string
		c    Cents
		want string
	}{
		{"USD", 123456, "$1,234.56"},
		{"usd", -5, "-$0.05"},
		{"GBP", -1230, "-£12.30"},
		{"EUR", 100, "€1.00"},
		{"AUD", 500, "A$5.00"},
		{"JPY", 1200, "¥1,200"},
		{"CHF", 310, "CHF 3.10"},
		{"", 310, "3.10"},
		{"USD", 0, "$0.00"},
		{"USD", 100_000_000_00, "$100,000,000.00"},
		{"USD", math.MinInt64, "-$92,233,720,368,547,758.08"},
		{"USD", math.MaxInt64, "$92,233,720,368,547,758.07"},
	}
	for _, tc := range tests {
		if got := FormatMoney(tc.cur, tc.c); got != tc.want {
			t.Errorf("FormatMoney(%s, %d) = %q, want %q", tc.cur, tc.c, got, tc.want)
		}
	}
	for bp, want := range map[BasisPoints]string{5833: "58.33 %", 0: "0.00 %", 10_000: "100.00 %", -150: "-1.50 %"} {
		if got := FormatPercent(bp); got != want {
			t.Errorf("FormatPercent(%d) = %q", bp, got)
		}
	}
	for m, want := range map[Micros]string{123_400: "$0.1234", 1_234_567_890: "$1,234.5679", -50: "-$0.0001", 0: "$0.0000"} {
		if got := FormatMicrosUSD(m); got != want {
			t.Errorf("FormatMicrosUSD(%d) = %q, want %q", m, got, want)
		}
	}
}

// statementFixture is two eToro accounts (USD) and a Trading 212 account (GBP).
func statementFixture() StatementInput {
	w, _ := WindowFor(PeriodWeekly, "2026-11-24")
	at := func(d, h int) time.Time { return utc(2026, 11, d, h, 0) }
	tr := func(acct string, pos int64, sym string, m Market, net Cents, fees Cents, known, ours bool, closed time.Time) StatementTrade {
		return StatementTrade{
			Trade: Trade{
				AccountID: acct, Mode: ModeDemo, Ours: ours,
				ClosedTrade: ClosedTrade{PositionID: pos, ClosedAt: closed, NetProfit: net, Fees: fees, FeesKnown: known, Long: true, Leverage: 1},
			},
			Symbol: sym, Market: m,
		}
	}
	demo := Account{ID: "a1", Name: "Demo main", Mode: ModeDemo, Platform: PlatformEtoro}
	demo2 := Account{ID: "a2", Name: "demo second", Mode: ModeDemo}
	t212 := Account{ID: "t1", Name: "ISA", Mode: ModeReal, Platform: PlatformTrading212}
	return StatementInput{
		Window: w, Scope: ScopeCombined, GeneratedAt: utc(2026, 11, 28, 6, 0),
		Accounts: []StatementAccountInput{
			{
				Account: t212,
				Snapshots: []EquitySnapshot{
					{AccountID: "t1", At: at(25, 12), Currency: "GBP", Equity: 1_010_00},
					{AccountID: "t1", At: at(23, 9), Currency: "GBP", Equity: 1_000_00},
				},
				Holdings: &Holdings{Platform: PlatformTrading212, Currency: "GBP", Equity: 1_012_00, Positions: []Holding{{Symbol: "VOD", Value: 500_00}}},
			},
			{
				Account: demo,
				Trades: []StatementTrade{
					tr("a1", 11, "BARC.L", MarketLSE, 25_00, 1_00, true, true, at(24, 10)),
					tr("a1", 12, "AAPL", MarketNYSE, -40_00, 0, false, true, at(24, 15)),
					tr("a1", 13, "MSFT", MarketNYSE, 0, 50, true, true, at(25, 15)),
					tr("a1", 14, "TSLA", MarketNYSE, 99_00, 0, true, false, at(25, 16)), // not ours
				},
				Snapshots: []EquitySnapshot{
					{AccountID: "a1", At: at(23, 9), Currency: "USD", Equity: 10_000_00},
					{AccountID: "a1", At: at(24, 9), Currency: "USD", Equity: 10_100_00},
					{AccountID: "a1", At: at(27, 20), Currency: "USD", Equity: 9_985_00},
				},
				OpenPositions: []StatementPosition{{PositionID: 99, Symbol: "CBA.ASX", Market: MarketASX, Direction: Long, OpenedAt: at(27, 1)}},
				Decisions:     map[DecisionOutcome]int{DecisionOrdered: 3, DecisionRejectedRisk: 2},
				Orders:        map[OrderState]int{OrderFilled: 3, OrderCancelled: 1},
				KillSwitches:  1,
				LLM:           LLMUsage{Calls: 5, Cost: 120_000},
			},
			{
				Account: demo2,
				Trades: []StatementTrade{
					tr("a2", 21, "", "", 60_00, 2_00, true, true, at(26, 15)),
				},
				Snapshots:  []EquitySnapshot{{AccountID: "a2", At: at(26, 20), Currency: "USD", Equity: 500_00}},
				DailyStops: 2,
				LLM:        LLMUsage{Calls: 2, CostUnknownCalls: 2},
			},
		},
	}
}

func TestBuildStatementCombined(t *testing.T) {
	st := BuildStatement(statementFixture())
	if st.Title != "Combined statement" || !st.AccountWide || len(st.Accounts) != 3 || st.Accounts[0].Name != "Demo main" ||
		st.Accounts[1].Name != "demo second" || st.Accounts[2].Name != "ISA" || !st.Accounts[2].ViewOnly || st.Accounts[2].Platform != "Trading 212" {
		t.Fatalf("header %+v", st.Accounts)
	}
	tr := st.Trades
	if tr.Count != 4 || tr.Wins != 2 || tr.Losses != 1 || tr.Breakeven != 1 || !tr.HasWinRate || tr.WinRate != 5000 ||
		tr.Net != 45_00 || tr.Fees != 3_50 || tr.FeesUnknown != 1 || st.ForeignTrades != 1 || st.TradeAccounts != 2 {
		t.Fatalf("trades %+v foreign %d accounts %d", tr, st.ForeignTrades, st.TradeAccounts)
	}
	if st.LargestWin == nil || st.LargestWin.Net != 60_00 || st.LargestWin.Account != "demo second" || st.LargestLoss == nil ||
		st.LargestLoss.Symbol != "AAPL" || st.LargestLoss.Net != -40_00 {
		t.Fatalf("largest %+v %+v", st.LargestWin, st.LargestLoss)
	}
	var names []string
	for _, e := range st.ByExchange {
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"LSE", "NYSE", "Other"}) || st.ByExchange[1].Stats.Count != 2 || len(st.ByAccount) != 3 || st.ByAccount[2].Stats.Count != 0 {
		t.Fatalf("breakdowns %v %+v", names, st.ByAccount)
	}
	// Equity per currency: USD of two accounts added, GBP apart, never USD + GBP.
	if len(st.EquityTotals) != 2 || st.EquityTotals[0].Currency != "GBP" || st.EquityTotals[0].Opening != 1_000_00 ||
		st.EquityTotals[0].Closing != 1_010_00 || st.EquityTotals[1].Currency != "USD" || st.EquityTotals[1].Accounts != 2 ||
		st.EquityTotals[1].Opening != 10_500_00 || st.EquityTotals[1].Closing != 10_485_00 || st.EquityTotals[1].Change != -15_00 {
		t.Fatalf("totals %+v", st.EquityTotals)
	}
	if e := st.Equity[0]; !e.Known || e.Opening != 10_000_00 || e.Closing != 9_985_00 || e.Drawdown.MaxDrawdown != 115_00 {
		t.Fatalf("equity %+v", e)
	}
	if len(st.Holdings) != 1 || st.Holdings[0].Account != "ISA" || st.Holdings[0].Holdings == nil {
		t.Fatalf("holdings %+v", st.Holdings)
	}
	if len(st.OpenPositions) != 1 || st.OpenPositions[0].Account != "Demo main" {
		t.Fatalf("positions %+v", st.OpenPositions)
	}
	if st.KillSwitches != 1 || st.DailyStops != 2 || st.DecisionsTotal != 5 || st.OrdersTotal != 4 || st.OrdersFill != 3 ||
		st.Decisions[0].Label != "Order sent" || st.LLM.Calls != 7 || st.LLM.CostKnown() {
		t.Fatalf("activity %+v", st)
	}
	for _, want := range []string{
		"Over the week (23–27 November 2026) cactusTrade closed 4 trades on 2 accounts (LSE 1, NYSE 2, Other 1): 2 wins, 1 loss and 1 breakeven, a win rate of 50.00 %.",
		"Their realised P&L was $45.00 after $3.50 of fees reported by eToro; eToro did not report the fees of 1 trade.",
		"The largest win was $60.00 (share not cached) and the largest loss -$40.00 (AAPL).",
		"eToro also reported 1 closed trade of positions cactusTrade did not open",
		"Recorded equity went from £1,000.00 to £1,010.00 (GBP, 1 account) and from $10,500.00 to $10,485.00 (USD, 2 accounts); amounts in different currencies are not added together.",
		"1 position cactusTrade opened was still open",
		"The kill switch was used once. The daily loss limit stopped new entries twice.",
		"The agent logged 5 decisions and cactusTrade stored 4 orders (3 filled).",
		"The LLM reviewer was called 7 times; the cost is known for 5 calls only ($0.1200).",
	} {
		if !strings.Contains(st.Summary, want) {
			t.Errorf("summary lacks %q:\n%s", want, st.Summary)
		}
	}
	notes := strings.Join(st.Notes, " ")
	for _, want := range []string{"never added together", "unknown", "view-only", "not financial advice"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q", want)
		}
	}
	// Deterministic: the order of the input does not matter.
	in := statementFixture()
	slices.Reverse(in.Accounts)
	for i := range in.Accounts {
		slices.Reverse(in.Accounts[i].Trades)
		slices.Reverse(in.Accounts[i].Snapshots)
	}
	if again := BuildStatement(in); again.Summary != st.Summary || len(again.ByExchange) != len(st.ByExchange) {
		t.Fatalf("order changed the statement:\n%s\n%s", again.Summary, st.Summary)
	}
}

func TestBuildStatementScopes(t *testing.T) {
	in := statementFixture()
	in.Scope, in.Exchange = ScopeExchange, "NYSE"
	st := BuildStatement(in)
	if st.AccountWide || st.Title != "Exchange statement: NYSE" || st.Trades.Count != 2 || st.ForeignTrades != 1 || len(st.Equity) != 0 ||
		len(st.EquityTotals) != 0 || len(st.OpenPositions) != 0 || st.DecisionsTotal != 0 || st.LLM.Calls != 0 || len(st.Holdings) != 0 {
		t.Fatalf("exchange %+v", st)
	}
	if !strings.Contains(st.Summary, "closed 2 trades on NYSE on 1 account:") || strings.Contains(st.Summary, "equity") ||
		strings.Contains(st.Summary, "kill switch") || !strings.Contains(strings.Join(st.Notes, " "), "belong to accounts, not exchanges") {
		t.Fatalf("exchange summary %q", st.Summary)
	}

	in = statementFixture()
	in.Scope, in.Accounts = ScopeExchange, in.Accounts[1:2]
	in.Exchange = "ASX"
	if st := BuildStatement(in); st.Trades.Count != 0 || len(st.OpenPositions) != 1 || !strings.Contains(st.Summary, "closed no trades on ASX on 1 account.") {
		t.Fatalf("ASX %+v %q", st.OpenPositions, st.Summary)
	}

	in = statementFixture()
	in.Scope, in.Accounts = ScopeAccount, in.Accounts[0:1] // the view-only account alone
	st = BuildStatement(in)
	if st.Title != "Account statement: ISA" || st.Trades.Count != 0 || st.Trades.HasWinRate || st.LargestWin != nil || len(st.ByAccount) != 0 ||
		len(st.EquityTotals) != 1 || len(st.Holdings) != 1 {
		t.Fatalf("account %+v", st)
	}
	if !strings.Contains(st.Summary, "closed no trades on 1 account.") || strings.Contains(st.Summary, "win rate") ||
		!strings.Contains(st.Summary, "The kill switch was not used and the daily loss limit did not stop trading.") ||
		!strings.Contains(st.Summary, "No position cactusTrade opened was open") || strings.Contains(st.Summary, "LLM") {
		t.Fatalf("account summary %q", st.Summary)
	}

	// No snapshots at all; a holdings read that failed; only a loss.
	in = StatementInput{Window: statementFixture().Window, Scope: ScopeCombined, Accounts: []StatementAccountInput{
		{Account: Account{ID: "x", Name: "X", Platform: PlatformSaxo}, HoldingsError: "sign-in expired"},
		{Account: Account{ID: "y", Name: "Y"}, ActivityCapped: true, Trades: []StatementTrade{{Trade: Trade{
			AccountID: "y", Ours: true,
			ClosedTrade: ClosedTrade{PositionID: 1, NetProfit: -5, FeesKnown: true},
		}, Symbol: "BP.L", Market: MarketLSE}}},
	}}
	st = BuildStatement(in)
	if !strings.Contains(st.Summary, "No equity was recorded in the period.") || !strings.Contains(st.Summary, "The largest loss was -$0.05 (BP.L).") ||
		st.Holdings[0].Error != "sign-in expired" || !strings.Contains(strings.Join(st.Notes, " "), "lower bounds") {
		t.Fatalf("summary %q %+v", st.Summary, st.Notes)
	}
	in.Accounts[1].Trades[0].NetProfit = 5
	if st := BuildStatement(in); !strings.Contains(st.Summary, "The largest win was $0.05 (BP.L).") {
		t.Fatalf("summary %q", st.Summary)
	}
	in.Window, _ = WindowFor(PeriodDaily, "2026-11-24")
	in.Accounts[1].LLM = LLMUsage{Calls: 1, Cost: 2500}
	in.Accounts[1].OpenPositions = []StatementPosition{{PositionID: 1}, {PositionID: 2}}
	if st := BuildStatement(in); !strings.HasPrefix(st.Summary, "On Tuesday 24 November 2026 cactusTrade closed 1 trade on 1 account (LSE 1)") ||
		!strings.Contains(st.Summary, "called once at a cost of $0.0025.") || !strings.Contains(st.Summary, "2 positions cactusTrade opened were still open") {
		t.Fatalf("daily summary %q", st.Summary)
	}
	in.Window, _ = WindowFor(PeriodMonthly, "2026-11-24")
	if st := BuildStatement(in); !strings.HasPrefix(st.Summary, "Over the month (November 2026)") {
		t.Fatalf("monthly summary %q", st.Summary)
	}
}

func TestStatementFileName(t *testing.T) {
	d, _ := WindowFor(PeriodDaily, "2026-11-24")
	w, _ := WindowFor(PeriodWeekly, "2026-11-24")
	m, _ := WindowFor(PeriodMonthly, "2026-11-24")
	tests := []struct {
		w     StatementWindow
		scope StatementScope
		name  string
		want  string
	}{
		{d, ScopeCombined, "", "cactusTrade-statement-2026-11-24-daily-combined.pdf"},
		{w, ScopeAccount, "Demo (main) £ account", "cactusTrade-statement-2026-w48-weekly-account-demo-main-account.pdf"},
		{m, ScopeExchange, "LSE", "cactusTrade-statement-2026-11-monthly-exchange-lse.pdf"},
		{d, ScopeAccount, "../../etc/passwd", "cactusTrade-statement-2026-11-24-daily-account-etc-passwd.pdf"},
		{d, ScopeAccount, "日本", "cactusTrade-statement-2026-11-24-daily-account.pdf"},
		{d, ScopeAccount, strings.Repeat("ab ", 40), "cactusTrade-statement-2026-11-24-daily-account-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-a.pdf"},
	}
	for _, tc := range tests {
		got := StatementFileName(tc.w, tc.scope, tc.name)
		if got != tc.want || !SafeAttachmentName(got) {
			t.Errorf("StatementFileName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStatementScheduleValidate(t *testing.T) {
	ok := StatementSchedule{Period: PeriodDaily, Recipients: []string{" Me@Example.com "}, AccountIDs: []string{" a1 ", ""}, IncludeCombined: true}
	if n := ok.Normalize(); n.Validate() != nil || n.Recipients[0] != "me@example.com" || !slices.Equal(n.AccountIDs, []string{"a1"}) {
		t.Fatalf("%+v %v", n, n.Validate())
	}
	many := make([]string, 11)
	for i := range many {
		many[i] = string(rune('a'+i)) + "@example.com"
	}
	ids := make([]string, 51)
	for i := range ids {
		ids[i] = strings.Repeat("x", i+1)
	}
	tests := []struct {
		name  string
		edit  func(*StatementSchedule)
		field string
	}{
		{"period", func(s *StatementSchedule) { s.Period = "yearly" }, "period"},
		{"no recipients", func(s *StatementSchedule) { s.Recipients = nil }, "recipients"},
		{"11 recipients", func(s *StatementSchedule) { s.Recipients = many }, "recipients"},
		{"bad address", func(s *StatementSchedule) { s.Recipients = []string{"Me <me@example.com>"} }, "recipients"},
		{"header injection", func(s *StatementSchedule) { s.Recipients = []string{"me@example.com\r\nBcc: x@example.com"} }, "recipients"},
		{"twice", func(s *StatementSchedule) { s.Recipients = []string{"a@example.com", "A@example.com"} }, "recipients"},
		{"51 accounts", func(s *StatementSchedule) { s.AccountIDs = ids }, "accountIds"},
		{"account twice", func(s *StatementSchedule) { s.AccountIDs = []string{"a", "a"} }, "accountIds"},
		{"long id", func(s *StatementSchedule) { s.AccountIDs = []string{strings.Repeat("x", 65)} }, "accountIds"},
		{"nothing included", func(s *StatementSchedule) { s.IncludeCombined = false }, "include"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := ok
			tc.edit(&s)
			var ve *ValidationError
			if err := s.Normalize().Validate(); !errors.As(err, &ve) || ve.Field != tc.field {
				t.Fatalf("Validate = %v, want field %s", err, tc.field)
			}
		})
	}
}

func TestValidateAttachments(t *testing.T) {
	pdf := func(name string, n int) Attachment {
		return Attachment{Filename: name, ContentType: "application/pdf", Data: make([]byte, n)}
	}
	if err := ValidateAttachments([]Attachment{pdf("a.pdf", 10), pdf("B-2_x.PDF", 1)}); err != nil {
		t.Fatal(err)
	}
	tooMany := make([]Attachment, MaxMailAttachments+1)
	for i := range tooMany {
		tooMany[i] = pdf("a.pdf", 1)
	}
	tests := []struct {
		name string
		as   []Attachment
	}{
		{"type", []Attachment{{Filename: "a.html", ContentType: "text/html", Data: []byte("x")}}},
		{"extension", []Attachment{pdf("a.exe", 1)}},
		{"quote", []Attachment{pdf(`a".pdf`, 1)}},
		{"path", []Attachment{pdf("../a.pdf", 1)}},
		{"space", []Attachment{pdf("a b.pdf", 1)}},
		{"line break", []Attachment{pdf("a\r\n.pdf", 1)}},
		{"hidden", []Attachment{pdf(".pdf", 1)}},
		{"empty name", []Attachment{pdf("", 1)}},
		{"long name", []Attachment{pdf(strings.Repeat("a", 97)+".pdf", 1)}},
		{"empty file", []Attachment{pdf("a.pdf", 0)}},
		{"over 10 MiB", []Attachment{pdf("a.pdf", MaxMailAttachmentBytes), pdf("b.pdf", 1)}},
		{"too many", tooMany},
	}
	for _, tc := range tests {
		if err := ValidateAttachments(tc.as); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
}
