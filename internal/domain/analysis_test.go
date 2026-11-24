package domain

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBucketFor(t *testing.T) {
	for _, tc := range []struct {
		days int
		want AnalysisBucket
	}{{1, BucketDay}, {5, BucketDay}, {31, BucketDay}, {32, BucketWeek}, {186, BucketWeek}, {187, BucketMonth}, {400, BucketMonth}} {
		if got := BucketFor(tc.days); got != tc.want {
			t.Errorf("BucketFor(%d) = %s, want %s", tc.days, got, tc.want)
		}
	}
}

func TestAnalysisWindowFor(t *testing.T) {
	now := utc(2026, 11, 24, 15, 0) // Tuesday
	tests := []struct {
		name        string
		p           AnalysisPreset
		from, to    string
		now         time.Time
		first, last RiskDate
		days        int
		bucket      AnalysisBucket
		title       string
		err         bool
	}{
		{"today", AnalysisPresetToday, "", "", now, "2026-11-24", "2026-11-24", 1, BucketDay, "Tuesday 24 November 2026", false},
		{"today after 22:00 UTC is the next risk day", AnalysisPresetToday, "", "", utc(2026, 11, 24, 22, 30), "2026-11-25", "2026-11-25", 1, BucketDay, "Wednesday 25 November 2026", false},
		{"this week", AnalysisPresetWeek, "", "", now, "2026-11-23", "2026-11-27", 5, BucketDay, "23–27 November 2026", false},
		{"this month", AnalysisPresetMonth, "", "", now, "2026-11-01", "2026-11-30", 30, BucketDay, "November 2026", false},
		{"last month", AnalysisPresetLastMonth, "", "", now, "2026-10-01", "2026-10-31", 31, BucketDay, "October 2026", false},
		{"last month in January", AnalysisPresetLastMonth, "", "", utc(2027, 1, 5, 9, 0), "2026-12-01", "2026-12-31", 31, BucketDay, "December 2026", false},
		{"all", AnalysisPresetAll, "", "", now, "2025-10-21", "2026-11-24", 400, BucketMonth, "21 October 2025 – 24 November 2026", false},
		{"custom across months", AnalysisPresetCustom, "2026-10-20", "2026-11-24", now, "2026-10-20", "2026-11-24", 36, BucketWeek, "20 October – 24 November 2026", false},
		{"custom one day", AnalysisPresetCustom, "2026-11-02", "2026-11-02", now, "2026-11-02", "2026-11-02", 1, BucketDay, "Monday 2 November 2026", false},
		{"custom 31 days is daily", AnalysisPresetCustom, "2026-10-25", "2026-11-24", now, "2026-10-25", "2026-11-24", 31, BucketDay, "25 October – 24 November 2026", false},
		{"custom 187 days is monthly", AnalysisPresetCustom, "2026-05-22", "2026-11-24", now, "2026-05-22", "2026-11-24", 187, BucketMonth, "22 May – 24 November 2026", false},
		{"custom 400 days back", AnalysisPresetCustom, "2025-10-21", "2025-10-22", now, "2025-10-21", "2025-10-22", 2, BucketDay, "21–22 October 2025", false},
		{"custom without from", AnalysisPresetCustom, "", "2026-11-24", now, "", "", 0, "", "", true},
		{"custom without to", AnalysisPresetCustom, "2026-11-01", "", now, "", "", 0, "", "", true},
		{"custom bad date", AnalysisPresetCustom, "2026-13-01", "2026-11-24", now, "", "", 0, "", "", true},
		{"custom reversed", AnalysisPresetCustom, "2026-11-10", "2026-11-09", now, "", "", 0, "", "", true},
		{"custom in the future", AnalysisPresetCustom, "2026-11-20", "2026-11-25", now, "", "", 0, "", "", true},
		{"custom older than 400 days", AnalysisPresetCustom, "2025-10-20", "2025-10-22", now, "", "", 0, "", "", true},
		{"unknown preset", AnalysisPreset("year"), "", "", now, "", "", 0, "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w, err := AnalysisWindowFor(tc.p, tc.from, tc.to, tc.now)
			if tc.err {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("err = %v, want invalid", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if w.FirstDay != tc.first || w.LastDay != tc.last || w.Days != tc.days || w.Bucket != tc.bucket || w.Title() != tc.title {
				t.Fatalf("window %+v title %q", w, w.Title())
			}
			if !w.From.Equal(dayStart(w.FirstDay.date())) || !w.To.Equal(dayStart(w.LastDay.date()).Add(24*time.Hour)) || w.From.Hour() != 22 {
				t.Fatalf("bounds %v – %v", w.From, w.To)
			}
		})
	}
	for _, s := range []string{"today", "week", "month", "last_month", "custom", "all"} {
		if p, err := ParseAnalysisPreset(s); err != nil || string(p) != s || p.Label() == "" {
			t.Errorf("ParseAnalysisPreset(%q) = %q, %v", s, p, err)
		}
	}
	if _, err := ParseAnalysisPreset("ytd"); !errors.Is(err, ErrInvalid) {
		t.Error("an unknown preset was accepted")
	}
	w, _ := AnalysisWindowFor(AnalysisPresetMonth, "", "", now)
	if got := AnalysisFileName(w); got != "cactusTrade-analysis-2026-11-01-to-2026-11-30.pdf" {
		t.Errorf("file name %q", got)
	}
}

// analysisFixture is November 2026 (this month at 24 November) on two eToro accounts (USD)
// and a Trading 212 ISA (GBP).
func analysisFixture() AnalysisInput {
	w, _ := AnalysisWindowFor(AnalysisPresetMonth, "", "", utc(2026, 11, 24, 15, 0))
	tr := func(acct string, pos int64, ins InstrumentID, sym string, m Market, net, fees Cents, known, ours, long bool, req string, closed time.Time) StatementTrade {
		return StatementTrade{
			Trade: Trade{AccountID: acct, Mode: ModeDemo, Ours: ours, OpenRequestID: req, ClosedTrade: ClosedTrade{
				PositionID: pos, Instrument: ins, ClosedAt: closed, NetProfit: net, Fees: fees, FeesKnown: known, Long: long, Leverage: 1,
			}},
			Symbol: sym, Market: m,
		}
	}
	dec := func(acct, id string, at time.Time, o DecisionOutcome, v VerdictKind, req, playbook string) Decision {
		return Decision{
			ID: id, AccountID: acct, At: at, Outcome: o, OrderRequestID: req, Verdict: DecisionVerdict{Kind: v},
			Signal: DecisionSignal{Direction: Long, Playbook: playbook},
		}
	}
	ev := func(acct, kind string, at time.Time) Event {
		return Event{ID: kind + at.String(), AccountID: acct, Kind: kind, At: at}
	}
	snap := func(acct, cur string, at time.Time, eq Cents) EquitySnapshot {
		return EquitySnapshot{AccountID: acct, At: at, Currency: cur, Equity: eq}
	}
	return AnalysisInput{
		Window: w, GeneratedAt: utc(2026, 11, 24, 15, 0), AllAccounts: true,
		Accounts: []AnalysisAccountInput{
			{
				Account: Account{ID: "a2", Name: "Second", Mode: ModeDemo},
				Trades:  []StatementTrade{tr("a2", 21, 4, "CBA.ASX", MarketASX, 60_00, 2_00, true, true, true, "r21", utc(2026, 11, 20, 3, 0))},
				Snapshots: []EquitySnapshot{
					snap("a2", "USD", utc(2026, 11, 5, 9, 0), 500_00), snap("a2", "USD", utc(2026, 11, 20, 9, 30), 560_00),
				},
				Decisions: []Decision{dec("a2", "d21", utc(2026, 11, 20, 2, 0), DecisionOrdered, "", "r21", "trend_breakout")},
				LLM:       LLMUsage{Calls: 1, CostUnknownCalls: 1},
			},
			{
				Account: Account{ID: "a1", Name: "Demo main", Mode: ModeDemo},
				Trades: []StatementTrade{
					tr("a1", 1, 1, "AAPL", MarketNYSE, 25_00, 1_00, true, true, true, "r1", utc(2026, 11, 2, 15, 0)),
					tr("a1", 2, 2, "BARC.L", MarketLSE, -40_00, 0, false, true, true, "r2", utc(2026, 11, 2, 16, 0)),
					tr("a1", 3, 1, "AAPL", MarketNYSE, 0, 50, true, true, false, "r3", utc(2026, 11, 10, 15, 0)),
					tr("a1", 4, 3, "TSLA", MarketNYSE, 99_00, 0, true, false, true, "", utc(2026, 11, 10, 16, 0)),
					tr("a1", 5, 1, "AAPL", MarketNYSE, 7_00, 0, true, true, true, "r5", utc(2026, 10, 31, 21, 0)), // before the window
				},
				Snapshots: []EquitySnapshot{
					snap("a1", "USD", utc(2026, 11, 12, 9, 0), 9_900_00), snap("a1", "USD", utc(2026, 11, 2, 9, 0), 10_000_00),
					snap("a1", "USD", utc(2026, 11, 10, 9, 0), 10_200_00), snap("a1", "USD", utc(2026, 11, 20, 9, 0), 10_100_00),
				},
				Themes: []Theme{
					{ID: "t3", Name: "chips", Instruments: []InstrumentID{1}},
					{ID: "t2", Name: "Banks", Instruments: []InstrumentID{2}},
					{ID: "t1", Name: "AI", Instruments: []InstrumentID{1}},
				},
				Decisions: []Decision{
					dec("a1", "d1", utc(2026, 10, 30, 14, 0), DecisionOrdered, VerdictApprove, "r1", "trend_breakout"), // before the window: joins, not counted
					dec("a1", "d2", utc(2026, 11, 2, 14, 0), DecisionOrdered, VerdictApprove, "r2", "mean_reversion"),
					dec("a1", "d3", utc(2026, 11, 5, 14, 0), DecisionRejectedRisk, VerdictReduce, "", "trend_breakout"),
					dec("a1", "d4", utc(2026, 11, 6, 14, 0), DecisionRejectedReviewer, VerdictReject, "", "mean_reversion"),
				},
				Events: []Event{
					ev("a1", EventKillSwitch, utc(2026, 11, 3, 10, 0)), ev("a1", EventKillReset, utc(2026, 11, 3, 11, 0)),
					ev("a1", EventDailyStop, utc(2026, 11, 4, 10, 0)), ev("a1", EventDailyStop, utc(2026, 11, 9, 10, 0)),
					ev("a1", EventLimitsChanged, utc(2026, 11, 5, 10, 0)), ev("a1", EventForceClosed, utc(2026, 11, 6, 10, 0)),
					ev("a1", EventKillSwitch, utc(2026, 10, 20, 10, 0)), // before the window
					ev("", EventKillSwitch, utc(2026, 11, 3, 10, 0)),    // system-wide
				},
				LLM: LLMUsage{Calls: 3, Cost: 120_000},
			},
			{
				Account: Account{ID: "t1", Name: "ISA", Mode: ModeReal, Platform: PlatformTrading212},
				Snapshots: []EquitySnapshot{
					snap("t1", "GBP", utc(2026, 11, 3, 9, 0), 1_000_00), snap("t1", "GBP", utc(2026, 11, 15, 9, 0), 1_050_00),
				},
			},
		},
	}
}

func groupsOf(an Analysis, d AnalysisDimension) string {
	var parts []string
	for _, g := range an.Grouping(d) {
		wr := "-"
		if g.Stats.HasWinRate {
			wr = FormatPercent(g.Stats.WinRate)
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%s:%s", g.Label, g.Stats.Count, usd(g.Stats.Net), wr))
	}
	return strings.Join(parts, " | ")
}

func TestBuildAnalysisGroupings(t *testing.T) {
	an := BuildAnalysis(analysisFixture())
	if an.Title != "November 2026" || an.Scope != "All 3 accounts you can see" || len(an.Accounts) != 3 || an.Accounts[0].Name != "Demo main" ||
		!an.Accounts[1].ViewOnly || an.Accounts[2].Name != "Second" {
		t.Fatalf("header %q %q %+v", an.Title, an.Scope, an.Accounts)
	}
	tot := an.Totals
	if s := tot.Stats; s.Count != 4 || s.Wins != 2 || s.Losses != 1 || s.Breakeven != 1 || s.WinRate != 5000 || s.Net != 45_00 ||
		s.Fees != 3_50 || s.FeesUnknown != 1 || s.Gross != 48_50 || !tot.HasAvg || tot.AvgNet != 11_25 {
		t.Fatalf("totals %+v avg %d", s, tot.AvgNet)
	}
	if tot.Best.Symbol != "CBA.ASX" || tot.Best.Net != 60_00 || tot.Best.Account != "Second" || tot.Worst.Symbol != "BARC.L" || tot.Worst.Net != -40_00 {
		t.Fatalf("best %+v worst %+v", tot.Best, tot.Worst)
	}
	if an.ForeignTrades != 1 || !an.HasOursShare || an.OursShareBp != 8000 || an.TradeAccounts != 2 {
		t.Fatalf("foreign %d share %d accounts %d", an.ForeignTrades, an.OursShareBp, an.TradeAccounts)
	}
	tests := []struct {
		d    AnalysisDimension
		want string
	}{
		{DimExchange, "LSE:1:-$40.00:0.00 % | NYSE:2:$25.00:50.00 % | ASX:1:$60.00:100.00 %"},
		{DimAccount, "Demo main:3:-$15.00:33.33 % | Second:1:$60.00:100.00 %"},
		// AAPL is in "AI" and "chips": the first by name wins; CBA.ASX has no theme on Second.
		{DimTheme, "AI:2:$25.00:50.00 % | Banks:1:-$40.00:0.00 % | No theme:1:$60.00:100.00 %"},
		{DimDirection, "Long:3:$45.00:66.66 % | Short:1:$0.00:0.00 %"},
		// r1's decision was made before the window and still names its playbook; r3 has none.
		{DimPlaybook, "mean_reversion:1:-$40.00:0.00 % | trend_breakout:2:$85.00:100.00 % | Not recorded:1:$0.00:0.00 %"},
		{DimOutcome, "Wins:2:$85.00:100.00 % | Losses:1:-$40.00:0.00 % | Breakeven:1:$0.00:0.00 %"},
	}
	for _, tc := range tests {
		if got := groupsOf(an, tc.d); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.d, got, tc.want)
		}
	}
	// Dates: one bucket per weekday risk day of November (21), empty days included, no win
	// rate without trades.
	days := an.Grouping(DimDate)
	if len(days) != 21 || days[0].Key != "2026-11-02" || days[0].Label != "Mon 2 Nov" || days[len(days)-1].Key != "2026-11-30" {
		t.Fatalf("days %d: %+v … %+v", len(days), days[0], days[len(days)-1])
	}
	for _, g := range days {
		switch g.Key {
		case "2026-11-02":
			if g.Stats.Count != 2 || g.Stats.Net != -15_00 || g.Best.Net != 25_00 || g.Worst.Net != -40_00 || g.AvgNet != -7_50 {
				t.Errorf("2 Nov %+v", g)
			}
		case "2026-11-10", "2026-11-20":
			if g.Stats.Count != 1 {
				t.Errorf("%s %+v", g.Key, g)
			}
		default:
			if g.Stats.Count != 0 || g.Stats.HasWinRate || g.HasAvg || g.Best != nil {
				t.Errorf("empty day %+v", g)
			}
		}
	}
	if got := []string{an.Groupings[0].Title, an.Groupings[2].Title, an.Groupings[3].Title}; !slices.Equal(got, []string{"By day", "By trading account", "By theme"}) {
		t.Errorf("titles %v", got)
	}
}

func TestAnalysisDateBuckets(t *testing.T) {
	now := utc(2026, 11, 24, 15, 0)
	trade := func(closed time.Time, net Cents) StatementTrade {
		return StatementTrade{Trade: Trade{AccountID: "a", Ours: true, ClosedTrade: ClosedTrade{PositionID: closed.Unix(), ClosedAt: closed, NetProfit: net, FeesKnown: true}}}
	}
	build := func(p AnalysisPreset, from, to string, ts ...StatementTrade) Analysis {
		w, err := AnalysisWindowFor(p, from, to, now)
		if err != nil {
			t.Fatal(err)
		}
		return BuildAnalysis(AnalysisInput{Window: w, Accounts: []AnalysisAccountInput{{Account: Account{ID: "a", Name: "A"}, Trades: ts}}})
	}
	// Weekly buckets: ISO weeks, keyed like weekly statements.
	an := build(AnalysisPresetCustom, "2026-10-01", "2026-11-24", trade(utc(2026, 10, 1, 15, 0), 1_00), trade(utc(2026, 11, 24, 10, 0), 2_00), trade(utc(2026, 11, 23, 10, 0), 3_00))
	ws := an.Grouping(DimDate)
	if an.Window.Bucket != BucketWeek || len(ws) != 9 || ws[0].Key != "2026-W40" || ws[0].Label != "w/c 28 Sep" || ws[8].Key != "2026-W48" ||
		ws[8].Stats.Count != 2 || ws[8].Stats.Net != 5_00 || an.Groupings[0].Title != "By week" {
		t.Fatalf("weeks %d %+v … %+v", len(ws), ws[0], ws[len(ws)-1])
	}
	// Monthly buckets over everything kept.
	an = build(AnalysisPresetAll, "", "", trade(utc(2026, 1, 15, 15, 0), -1_00))
	ms := an.Grouping(DimDate)
	if an.Window.Bucket != BucketMonth || len(ms) != 14 || ms[0].Key != "2025-10" || ms[0].Label != "Oct 2025" || ms[3].Key != "2026-01" ||
		ms[3].Stats.Losses != 1 || ms[13].Key != "2026-11" {
		t.Fatalf("months %d %+v", len(ms), ms)
	}
	// Daily: a weekend risk day appears only with a trade on it.
	an = build(AnalysisPresetCustom, "2026-11-20", "2026-11-23", trade(utc(2026, 11, 21, 10, 0), 4_00))
	if got := groupKeys(an.Grouping(DimDate)); got != "2026-11-20,2026-11-21,2026-11-23" {
		t.Fatalf("days %s", got)
	}
}

func groupKeys(gs []AnalysisGroup) string {
	ks := make([]string, len(gs))
	for i, g := range gs {
		ks[i] = g.Key
	}
	return strings.Join(ks, ",")
}

func TestAnalysisEquityPerCurrency(t *testing.T) {
	an := BuildAnalysis(analysisFixture())
	if len(an.EquityTotals) != 2 || an.EquityTotals[0].Currency != "GBP" || an.EquityTotals[0].Closing != 1_050_00 ||
		an.EquityTotals[1].Currency != "USD" || an.EquityTotals[1].Accounts != 2 || an.EquityTotals[1].Opening != 10_500_00 || an.EquityTotals[1].Closing != 10_660_00 {
		t.Fatalf("totals %+v", an.EquityTotals)
	}
	if e := an.Equity[0]; e.Account != "Demo main" || e.Drawdown.MaxDrawdown != 300_00 || e.Drawdown.MaxDrawdownBp != 294 {
		t.Fatalf("Demo main %+v", e)
	}
	if len(an.Curves) != 2 {
		t.Fatalf("curves %+v", an.Curves)
	}
	gbp, us := an.Curves[0], an.Curves[1]
	if gbp.Currency != "GBP" || gbp.Accounts != 1 || len(gbp.Points) != 2 || gbp.Points[1].Equity != 1_050_00 {
		t.Fatalf("GBP %+v", gbp)
	}
	// USD: a1 and a2 are summed at each point (a2 joins on 5 Nov); the two snapshots of
	// 20 Nov fall in one 4-hour slot and make one point. GBP is never added to USD.
	var pts []string
	for _, p := range us.Points {
		pts = append(pts, p.At.Format("02T15:04")+"="+usd(p.Equity))
	}
	if got := strings.Join(pts, " "); us.Accounts != 2 || got != "02T09:00=$10,000.00 05T09:00=$10,500.00 10T09:00=$10,700.00 12T09:00=$10,400.00 20T09:30=$10,660.00" {
		t.Fatalf("USD points %s", got)
	}
	if us.Drawdown.MaxDrawdown != 300_00 || us.Drawdown.Peak != 10_700_00 {
		t.Fatalf("USD drawdown %+v", us.Drawdown)
	}
	// A long period samples coarser: at most analysisMaxPoint points per currency.
	w, _ := AnalysisWindowFor(AnalysisPresetAll, "", "", utc(2026, 11, 24, 15, 0))
	var many []EquitySnapshot
	for at := w.From; at.Before(utc(2026, 11, 24, 15, 0)); at = at.Add(15 * time.Minute) {
		many = append(many, EquitySnapshot{AccountID: "a", At: at, Currency: "usd", Equity: 1_000_00})
	}
	big := BuildAnalysis(AnalysisInput{Window: w, Accounts: []AnalysisAccountInput{{Account: Account{ID: "a", Name: "A"}, Snapshots: many}}})
	if n := len(big.Curves[0].Points); n > analysisMaxPoint || n < 50 || big.Curves[0].Currency != "USD" {
		t.Fatalf("%d points", n)
	}
}

func TestAnalysisActivityAndRisk(t *testing.T) {
	an := BuildAnalysis(analysisFixture())
	r := an.Risk
	if r.KillSwitches != 1 || r.KillResets != 1 || r.DailyStops != 2 || r.LimitChanges != 1 || r.ForceCloses != 1 || r.RiskRefusals != 1 {
		t.Fatalf("risk %+v", r)
	}
	counts := func(cs []StatementCount) string {
		var s []string
		for _, c := range cs {
			s = append(s, fmt.Sprintf("%s=%d", c.Label, c.N))
		}
		return strings.Join(s, ",")
	}
	if got := counts(an.Decisions); an.DecisionsTotal != 4 || got != "Order sent=2,Refused by the risk engine=1,Refused by the LLM reviewer=1" {
		t.Fatalf("decisions %d %s", an.DecisionsTotal, got)
	}
	if got := counts(an.Verdicts); got != "Approved=1,Reduced=1,Rejected=1,Not reviewed=1" {
		t.Fatalf("verdicts %s", got)
	}
	if an.LLM.Calls != 4 || an.LLM.Cost != 120_000 || an.LLM.CostUnknownCalls != 1 {
		t.Fatalf("llm %+v", an.LLM)
	}
}

func TestBuildAnalysisOrderIndependent(t *testing.T) {
	a := analysisFixture()
	b := analysisFixture()
	slices.Reverse(b.Accounts)
	for i := range b.Accounts {
		slices.Reverse(b.Accounts[i].Trades)
		slices.Reverse(b.Accounts[i].Snapshots)
		slices.Reverse(b.Accounts[i].Themes)
		slices.Reverse(b.Accounts[i].Decisions)
		slices.Reverse(b.Accounts[i].Events)
	}
	if x, y := BuildAnalysis(a), BuildAnalysis(b); !reflect.DeepEqual(x, y) {
		t.Fatalf("input order changed the analysis:\n%+v\n%+v", x, y)
	}
}

// narrativeText joins the narrative: "## heading" then its paragraphs.
func narrativeText(an Analysis) string {
	var b strings.Builder
	for _, s := range an.Narrative {
		b.WriteString("## " + s.Heading + "\n")
		for _, p := range s.Paragraphs {
			b.WriteString(p + "\n")
		}
	}
	return b.String()
}

// bannedNarrative mirrors the wording guard (cmd/cactustrade TestNoProfitabilityClaims) and
// adds words that predict or advise.
var bannedNarrative = regexp.MustCompile(`(?i)profitab\w*|guarantee\w*|beat(s|ing)? the market|outperform\w*|\bma(ke|kes|king|de) money\b|` +
	`returns of|risk[- ]free|high(er)? returns?|consistent (profits?|returns?|gains?)|proven|edge|\bwill\b|\bexpect\w*|forecast\w*|` +
	`\blikely\b|predict\w*|\bshould\b|recommend\w*|\bgood\b|\bbad\b|excellent|great|poor|strong|weak|\bbuy\b|\bsell\b`)

func TestAnalysisNarrative(t *testing.T) {
	an := BuildAnalysis(analysisFixture())
	var heads []string
	for _, s := range an.Narrative {
		heads = append(heads, s.Heading)
		if len(s.Paragraphs) == 0 {
			t.Errorf("%s is empty", s.Heading)
		}
	}
	if !slices.Equal(heads, []string{"Overview", "What went well", "What went badly", "Costs", "Risk & discipline", "Activity"}) {
		t.Fatalf("headings %v", heads)
	}
	text := narrativeText(an)
	for _, want := range []string{
		"Period: November 2026 (this month, 30 risk days). Scope: All 3 accounts you can see.",
		"cactusTrade closed 4 trades on 2 accounts: 2 wins, 1 loss and 1 breakeven, a win rate of 50.00 %.",
		"realised P&L after eToro's fees was $45.00, $11.25 per trade on average.",
		"Trades by exchange: LSE 1, NYSE 2, ASX 1.",
		"from £1,000.00 to £1,050.00 (GBP, 1 account) and from $10,500.00 to $10,660.00 (USD, 2 accounts); amounts in different currencies are not added together.",
		"2 trades of 4 closed with a positive result; the best was $60.00 (CBA.ASX on Second, closed 20 Nov 2026).",
		"by exchange, ASX ($60.00 over 1 trade)", "by playbook, trend_breakout ($85.00 over 2 trades)", "by theme, No theme ($60.00 over 1 trade)",
		"1 loss" + "", "the worst was -$40.00 (BARC.L on Demo main, closed 2 Nov 2026).",
		"by exchange, LSE (-$40.00 over 1 trade)", "by trading account, Demo main (-$15.00 over 3 trades)",
		"1 of the 3 days with trades ended with a positive realised P&L.", "1 of the 3 days with trades ended with a negative realised P&L.",
		"eToro reported $3.50 of fees on 3 trades. It did not report the fees of 1 trade; they are shown as unknown and not counted as zero.",
		"Before reported fees the realised P&L was $48.50.",
		"The LLM reviewer was called 4 times; the cost is known for 3 calls only ($0.1200).",
		"the kill switch was used once and reset once, the daily loss limit stopped new entries twice, the risk limits were changed once and positions were force-closed once.",
		"The risk engine refused 1 candidate.",
		"The agent logged 4 decisions: order sent 2, refused by the risk engine 1, refused by the LLM reviewer 1.",
		"verdicts: approved 1, reduced 1, rejected 1, not reviewed 1.",
		"Of the 5 closed trades eToro reported on these accounts, 4 (80.00 %) were of positions cactusTrade opened. The other one is counted apart and not in the figures above.",
		PastResultsNote,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("narrative lacks %q\n%s", want, text)
		}
	}
	if m := bannedNarrative.FindString(text); m != "" {
		t.Errorf("narrative wording %q", m)
	}
	if m := bannedNarrative.FindString(strings.Join(an.Notes, "\n")); m != "" {
		t.Errorf("notes wording %q", m)
	}
	assertTraceable(t, an, text)
}

// assertTraceable checks that every amount and percentage in the narrative is a figure the
// analysis's tables show.
func assertTraceable(t *testing.T, an Analysis, text string) {
	t.Helper()
	shown := map[string]bool{}
	addGroup := func(g AnalysisGroup) {
		s := g.Stats
		for _, c := range []Cents{s.Net, s.Fees, s.Gross, g.AvgNet} {
			shown[usd(c)] = true
		}
		if s.HasWinRate {
			shown[FormatPercent(s.WinRate)] = true
		}
		for _, r := range []*AnalysisTradeRef{g.Best, g.Worst} {
			if r != nil {
				shown[usd(r.Net)] = true
			}
		}
	}
	addGroup(an.Totals)
	for _, gg := range an.Groupings {
		for _, g := range gg.Groups {
			addGroup(g)
		}
	}
	for _, e := range an.Equity {
		for _, c := range []Cents{e.Opening, e.Closing, e.Change, e.Drawdown.MaxDrawdown} {
			shown[FormatMoney(e.Currency, c)] = true
		}
	}
	for _, e := range an.EquityTotals {
		for _, c := range []Cents{e.Opening, e.Closing, e.Change} {
			shown[FormatMoney(e.Currency, c)] = true
		}
	}
	shown[FormatMicrosUSD(an.LLM.Cost)] = true
	if an.HasOursShare {
		shown[FormatPercent(an.OursShareBp)] = true
	}
	amounts := regexp.MustCompile(`-?(A\$|[$£€¥])[0-9][0-9,]*(\.[0-9]+)?|[0-9]+\.[0-9]{2} %`)
	found := amounts.FindAllString(text, -1)
	if len(found) < 15 {
		t.Fatalf("only %d figures in the narrative", len(found))
	}
	for _, f := range found {
		if !shown[f] {
			t.Errorf("narrative figure %s is in no table", f)
		}
	}
}

func TestAnalysisEmpty(t *testing.T) {
	w, _ := AnalysisWindowFor(AnalysisPresetToday, "", "", utc(2026, 11, 24, 15, 0))
	an := BuildAnalysis(AnalysisInput{Window: w, AllAccounts: true, Accounts: []AnalysisAccountInput{{Account: Account{ID: "a", Name: "Only"}}}})
	if an.Totals.Stats.HasWinRate || an.Totals.HasAvg || an.Totals.Best != nil || an.HasOursShare || an.Scope != "The 1 account you can see" {
		t.Fatalf("empty %+v", an)
	}
	text := narrativeText(an)
	for _, want := range []string{
		"cactusTrade closed no trades in the period.", "No trades closed in the period.", "No equity was recorded in the period.",
		"No trades closed, so eToro reported no trading fees.", "The LLM reviewer was not called.",
		"the kill switch was not used, the daily loss limit did not stop trading, the risk limits were not changed and nothing was force-closed.",
		"The risk engine refused no candidate.", "The agent logged no decisions.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("narrative lacks %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "%") {
		t.Errorf("a percentage without trades:\n%s", text)
	}
	// Only losses; a chosen scope; the activity cap.
	in := analysisFixture()
	in.AllAccounts = false
	in.Accounts = in.Accounts[1:2]
	in.Accounts[0].Trades = in.Accounts[0].Trades[1:2]
	in.Accounts[0].ActivityCapped = true
	an = BuildAnalysis(in)
	text = narrativeText(an)
	for _, want := range []string{"Scope: 1 chosen account.", "No trade closed with a positive result.", "lower bounds"} {
		if !strings.Contains(text, want) {
			t.Errorf("narrative lacks %q\n%s", want, text)
		}
	}
	if !slices.ContainsFunc(an.Notes, func(n string) bool { return strings.Contains(n, "lower bounds") }) {
		t.Error("notes lack the cap")
	}
	in.Accounts[0].Trades = []StatementTrade{analysisFixture().Accounts[1].Trades[0]}
	in.Accounts[0].LLM = LLMUsage{Calls: 2, Cost: 3_000}
	text = narrativeText(BuildAnalysis(in))
	for _, want := range []string{"No trade closed with a negative result.", "called twice at a cost of $0.0030."} {
		if !strings.Contains(text, want) {
			t.Errorf("narrative lacks %q\n%s", want, text)
		}
	}
}
