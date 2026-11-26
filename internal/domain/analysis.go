package domain

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// An analysis (R31) summarises the trades of a chosen period on the accounts a user can see,
// grouped by date, exchange, account, theme, direction, playbook and outcome, with the equity
// curve and drawdown per currency, risk events, the agent's activity and a narrative written
// from the figures. It is built here, deterministically and without an LLM (R31-a), from the
// same building blocks as a trading statement (TradeStats, equityOf, equityTotals,
// FormatMoney …). It describes past records only; nothing here sizes, approves or refuses an
// order.

// More event kinds an analysis counts (EventKillSwitch and EventDailyStop are in statement.go).
const (
	EventKillReset     = "agent.kill_reset"    // the kill switch was reset
	EventLimitsChanged = "risk.limits_changed" // the risk limits were changed
	EventForceClosed   = "agent.force_closed"  // positions were force-closed
)

// Labels of the groups that hold what an analysis cannot place.
const (
	NoThemeLabel     = "No theme"
	NoPlaybookLabel  = "Not recorded"
	analysisMaxPoint = 240 // the most points of one currency's equity curve
)

// AnalysisDimension names a grouping of the trades.
type AnalysisDimension string

// The groupings, in display order.
const (
	DimDate      AnalysisDimension = "date"
	DimExchange  AnalysisDimension = "exchange"
	DimAccount   AnalysisDimension = "account"
	DimTheme     AnalysisDimension = "theme"
	DimDirection AnalysisDimension = "direction"
	DimPlaybook  AnalysisDimension = "playbook"
	DimOutcome   AnalysisDimension = "outcome"
)

// AnalysisDimensions lists every grouping in display order.
var AnalysisDimensions = []AnalysisDimension{DimDate, DimExchange, DimAccount, DimTheme, DimDirection, DimPlaybook, DimOutcome}

// AnalysisAccountInput is what the app read about one account for an analysis.
type AnalysisAccountInput struct {
	Account Account
	// Trades are eToro's closed trades of the account closed in the window (ours and the
	// others).
	Trades []StatementTrade
	// Snapshots are the account's equity snapshots taken in the window.
	Snapshots []EquitySnapshot
	// Themes are the account's themes now: a trade's theme is its share's theme at analysis
	// time (R31-c).
	Themes []Theme
	// Decisions are the account's decisions up to the window's end, including some made
	// before it (for the playbook of a trade opened earlier); only those in the window are
	// counted as activity.
	Decisions []Decision
	// Events are the account's events (only those in the window are counted).
	Events []Event
	// LLM sums the account's LLM calls in the window.
	LLM LLMUsage
	// ActivityCapped: more decisions or events existed than were read; their counts are lower
	// bounds.
	ActivityCapped bool
}

// AnalysisInput is everything an analysis is built from.
type AnalysisInput struct {
	Window      AnalysisWindow
	GeneratedAt time.Time
	// AllAccounts: no account was chosen, so the scope is every account the user can see.
	AllAccounts bool
	Accounts    []AnalysisAccountInput
}

// AnalysisTradeRef names one trade (the best or worst of a group).
type AnalysisTradeRef struct {
	Account    string
	Symbol     string // "" when the share is not cached
	PositionID int64
	ClosedAt   time.Time
	Net        Cents
}

// AnalysisGroup is the figures of one group of cactusTrade's closed trades (US dollar cents).
type AnalysisGroup struct {
	Key   string
	Label string
	Stats TradeSummary
	// AvgNet is the realised P&L per trade, rounded toward zero (HasAvg false without trades).
	AvgNet      Cents
	HasAvg      bool
	Best, Worst *AnalysisTradeRef
}

// AnalysisGrouping is the groups of one dimension.
type AnalysisGrouping struct {
	Dimension AnalysisDimension
	Title     string // "By exchange"
	Groups    []AnalysisGroup
}

// AnalysisPoint is one point of an equity curve.
type AnalysisPoint struct {
	At     time.Time
	Equity Cents // minor units of the curve's currency
}

// AnalysisCurve is the recorded equity of the accounts of one currency over the window: at
// each point the sum of every account's latest snapshot so far (an account joins the curve at
// its first snapshot in the window). Amounts are minor units of Currency.
type AnalysisCurve struct {
	Currency string
	Accounts int
	Points   []AnalysisPoint
	Drawdown DrawdownFigures
}

// AnalysisRisk counts the risk and discipline events of the window.
type AnalysisRisk struct {
	KillSwitches, KillResets, DailyStops, LimitChanges, ForceCloses int
	// RiskRefusals counts the candidates the risk engine refused (decisions).
	RiskRefusals int
}

// AnalysisSection is one part of the narrative: a heading and its paragraphs.
type AnalysisSection struct {
	Heading    string
	Paragraphs []string
}

// Analysis is a built analysis. Trade amounts are US dollar cents (eToro's account currency);
// equity amounts are minor units of each account's own currency and never added across
// currencies.
type Analysis struct {
	Window      AnalysisWindow
	Title       string // the period ("November 2026")
	GeneratedAt time.Time
	AllAccounts bool
	Scope       string // "All 3 accounts you can see", "2 chosen accounts"
	Accounts    []StatementAccountLine

	// Totals are cactusTrade's own closed trades (OD15); ForeignTrades counts the trades of
	// positions it did not open (listed apart, not counted); OursShareBp is the share of
	// cactusTrade's trades among all closed trades (HasOursShare false when there were none).
	Totals        AnalysisGroup
	ForeignTrades int
	OursShareBp   BasisPoints
	HasOursShare  bool
	TradeAccounts int // accounts with at least one of cactusTrade's trades
	Groupings     []AnalysisGrouping

	Equity       []StatementEquity
	EquityTotals []StatementEquityTotal
	Curves       []AnalysisCurve

	Risk           AnalysisRisk
	Decisions      []StatementCount
	DecisionsTotal int
	Verdicts       []StatementCount
	LLM            LLMUsage
	ActivityCapped bool

	Narrative []AnalysisSection
	Notes     []string

	// Trades are the period's closed trades behind the figures, newest first, at most
	// AnalysisMaxTradeRows (TradesCapped then): cactusTrade's own (counted) and the others
	// (Ours false, never counted). The page drills into them (R35).
	Trades       []AnalysisTradeRow
	TradesCapped bool
}

// AnalysisMaxTradeRows is the most trades an analysis lists for drilling into (R35).
const AnalysisMaxTradeRows = 2000

// AnalysisTradeRow is one closed trade as the analysis's drill-down shows it.
type AnalysisTradeRow struct {
	AccountID  string
	Account    string // the account's name
	PositionID int64
	Symbol     string // "" when the share is not cached
	Exchange   string // LSE, NYSE, ASX or Other
	Theme      string // NoThemeLabel when none
	Playbook   string // NoPlaybookLabel when not recorded
	Direction  Direction
	OpenedAt   time.Time
	ClosedAt   time.Time
	OpenRate   Micros
	CloseRate  Micros
	Investment Cents
	Net        Cents
	Fees       Cents
	FeesKnown  bool
	Outcome    string // "Win", "Loss" or "Breakeven"
	Ours       bool
}

// Grouping returns the groups of dimension d (nil when unknown).
func (a Analysis) Grouping(d AnalysisDimension) []AnalysisGroup {
	for _, g := range a.Groupings {
		if g.Dimension == d {
			return g.Groups
		}
	}
	return nil
}

// verdictLabels names the reviewer's verdicts for people, in display order.
var verdictLabels = []struct {
	k     VerdictKind
	label string
}{
	{VerdictApprove, "Approved"},
	{VerdictReduce, "Reduced"},
	{VerdictReject, "Rejected"},
	{"", "Not reviewed"},
}

// analysisTrade is one of cactusTrade's trades with what the analysis groups it by.
type analysisTrade struct {
	StatementTrade
	account, theme, playbook string
}

// BuildAnalysis builds the analysis of in. It sorts and copies what it keeps, so the result
// depends only on the input's content, not its order.
func BuildAnalysis(in AnalysisInput) Analysis {
	w := in.Window
	an := Analysis{Window: w, Title: w.Title(), GeneratedAt: in.GeneratedAt, AllAccounts: in.AllAccounts}
	accts := slices.Clone(in.Accounts)
	slices.SortStableFunc(accts, func(a, b AnalysisAccountInput) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Account.Name), strings.ToLower(b.Account.Name)), strings.Compare(a.Account.ID, b.Account.ID))
	})
	an.Scope = plural(len(accts), "chosen account", "chosen accounts")
	if in.AllAccounts {
		an.Scope = "All " + plural(len(accts), "account", "accounts") + " you can see"
		if len(accts) == 1 {
			an.Scope = "The 1 account you can see"
		}
	}

	var ours, others []analysisTrade
	decisions := map[DecisionOutcome]int{}
	verdicts := map[VerdictKind]int{}
	for _, a := range accts {
		an.Accounts = append(an.Accounts, StatementAccountLine{
			ID: a.Account.ID, Name: a.Account.Name, Platform: a.Account.OnPlatform().Label(), Mode: a.Account.Mode, ViewOnly: !a.Account.Tradable(),
		})
		themeOf := themeNames(a.Themes)
		playbookOf := map[string]string{}
		for _, d := range a.Decisions {
			if d.OrderRequestID != "" && d.Signal.Playbook != "" {
				playbookOf[d.OrderRequestID] = d.Signal.Playbook
			}
			if d.AccountID != a.Account.ID || !w.Contains(d.At) {
				continue
			}
			decisions[d.Outcome]++
			verdicts[d.Verdict.Kind]++
			if d.Outcome == DecisionRejectedRisk {
				an.Risk.RiskRefusals++
			}
		}
		n := 0
		for _, t := range a.Trades {
			if !w.Contains(t.ClosedAt) {
				continue
			}
			at := analysisTrade{StatementTrade: t, account: a.Account.Name, theme: NoThemeLabel, playbook: NoPlaybookLabel}
			if name, ok := themeOf[t.Instrument]; ok {
				at.theme = name
			}
			if pb, ok := playbookOf[t.OpenRequestID]; ok && t.OpenRequestID != "" {
				at.playbook = pb
			}
			if !t.Ours {
				an.ForeignTrades++
				others = append(others, at)
				continue
			}
			n++
			ours = append(ours, at)
		}
		if n > 0 {
			an.TradeAccounts++
		}
		for _, e := range a.Events {
			if e.AccountID != a.Account.ID || !w.Contains(e.At) {
				continue
			}
			switch e.Kind {
			case EventKillSwitch:
				an.Risk.KillSwitches++
			case EventKillReset:
				an.Risk.KillResets++
			case EventDailyStop:
				an.Risk.DailyStops++
			case EventLimitsChanged:
				an.Risk.LimitChanges++
			case EventForceClosed:
				an.Risk.ForceCloses++
			}
		}
		an.LLM = an.LLM.Add(a.LLM)
		an.ActivityCapped = an.ActivityCapped || a.ActivityCapped
		an.Equity = append(an.Equity, equityOf(StatementAccountInput{Account: a.Account, Snapshots: inWindow(a.Snapshots, w)}))
	}
	slices.SortStableFunc(ours, func(a, b analysisTrade) int {
		return cmp.Or(a.ClosedAt.Compare(b.ClosedAt), strings.Compare(a.AccountID, b.AccountID), cmp.Compare(a.PositionID, b.PositionID))
	})

	an.Totals = analysisGroup("all", "All trades", ours)
	if all := len(ours) + an.ForeignTrades; all > 0 {
		an.HasOursShare = true
		an.OursShareBp = BasisPoints(int64(len(ours)) * 10_000 / int64(all))
	}
	for _, d := range AnalysisDimensions {
		an.Groupings = append(an.Groupings, AnalysisGrouping{Dimension: d, Title: dimensionTitle(d, w.Bucket), Groups: groupBy(d, w, ours)})
	}
	an.EquityTotals = equityTotals(an.Equity)
	an.Curves = equityCurves(accts, w)

	for _, d := range decisionLabels {
		if n := decisions[d.o]; n > 0 {
			an.Decisions = append(an.Decisions, StatementCount{Label: d.label, N: n})
			an.DecisionsTotal += n
		}
	}
	for _, v := range verdictLabels {
		if n := verdicts[v.k]; n > 0 {
			an.Verdicts = append(an.Verdicts, StatementCount{Label: v.label, N: n})
		}
	}
	an.Trades, an.TradesCapped = tradeRows(append(slices.Clone(ours), others...))
	an.Narrative = analysisNarrative(an)
	an.Notes = analysisNotes(an)
	return an
}

// tradeRows lists ts newest first (then account, position), at most AnalysisMaxTradeRows.
func tradeRows(ts []analysisTrade) ([]AnalysisTradeRow, bool) {
	slices.SortStableFunc(ts, func(a, b analysisTrade) int {
		return cmp.Or(b.ClosedAt.Compare(a.ClosedAt), strings.Compare(a.AccountID, b.AccountID), cmp.Compare(a.PositionID, b.PositionID))
	})
	capped := len(ts) > AnalysisMaxTradeRows
	if capped {
		ts = ts[:AnalysisMaxTradeRows]
	}
	out := make([]AnalysisTradeRow, 0, len(ts))
	for _, t := range ts {
		d := Short
		if t.Long {
			d = Long
		}
		out = append(out, AnalysisTradeRow{
			AccountID: t.AccountID, Account: t.account, PositionID: t.PositionID, Symbol: t.Symbol, Exchange: exchangeLabel(t.Market),
			Theme: t.theme, Playbook: t.playbook, Direction: d, OpenedAt: t.OpenedAt, ClosedAt: t.ClosedAt,
			OpenRate: t.OpenRate, CloseRate: t.CloseRate, Investment: t.Investment, Net: t.NetProfit,
			Fees: t.Fees, FeesKnown: t.FeesKnown, Outcome: outcomeLabel(t.NetProfit), Ours: t.Ours,
		})
	}
	return out, capped
}

// themeNames maps each share to its theme's name: the first theme holding it by name
// (case-insensitive, then id), as the watchlist shows it (R27-a).
func themeNames(themes []Theme) map[InstrumentID]string {
	ts := slices.Clone(themes)
	slices.SortStableFunc(ts, func(a, b Theme) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), strings.Compare(a.ID, b.ID))
	})
	out := map[InstrumentID]string{}
	for _, t := range ts {
		for _, id := range t.Instruments {
			if _, ok := out[id]; !ok {
				out[id] = t.Name
			}
		}
	}
	return out
}

func inWindow(snaps []EquitySnapshot, w AnalysisWindow) []EquitySnapshot {
	out := make([]EquitySnapshot, 0, len(snaps))
	for _, s := range snaps {
		if w.Contains(s.At) {
			out = append(out, s)
		}
	}
	return out
}

func dimensionTitle(d AnalysisDimension, b AnalysisBucket) string {
	switch d {
	case DimDate:
		return "By " + b.noun()
	case DimAccount:
		return "By trading account"
	}
	return "By " + string(d)
}

// analysisGroup sums trades (sorted by close time).
func analysisGroup(key, label string, ts []analysisTrade) AnalysisGroup {
	g := AnalysisGroup{Key: key, Label: label}
	cts := make([]ClosedTrade, len(ts))
	for i, t := range ts {
		cts[i] = t.ClosedTrade
		ref := &AnalysisTradeRef{Account: t.account, Symbol: t.Symbol, PositionID: t.PositionID, ClosedAt: t.ClosedAt, Net: t.NetProfit}
		if g.Best == nil || t.NetProfit > g.Best.Net {
			g.Best = ref
		}
		if g.Worst == nil || t.NetProfit < g.Worst.Net {
			g.Worst = ref
		}
	}
	g.Stats = TradeStats(cts)
	if g.Stats.Count > 0 {
		g.HasAvg, g.AvgNet = true, g.Stats.Net/Cents(g.Stats.Count)
	}
	return g
}

// groupBy splits trades along d. Date groups cover the whole window (empty buckets
// included); the others hold only groups with trades.
func groupBy(d AnalysisDimension, w AnalysisWindow, ts []analysisTrade) []AnalysisGroup {
	type keyed struct{ key, label string }
	keyOf := func(t analysisTrade) keyed {
		switch d {
		case DimDate:
			k, l := w.Bucket.bucketOf(RiskDay(t.ClosedAt).date())
			return keyed{k, l}
		case DimExchange:
			x := exchangeLabel(t.Market)
			return keyed{x, x}
		case DimAccount:
			return keyed{t.AccountID, t.account}
		case DimTheme:
			return keyed{strings.ToLower(t.theme), t.theme}
		case DimDirection:
			if t.Long {
				return keyed{string(Long), "Long"}
			}
			return keyed{string(Short), "Short"}
		case DimPlaybook:
			return keyed{t.playbook, t.playbook}
		}
		switch {
		case t.NetProfit > 0:
			return keyed{"win", "Wins"}
		case t.NetProfit < 0:
			return keyed{"loss", "Losses"}
		}
		return keyed{"breakeven", "Breakeven"}
	}
	by := map[string][]analysisTrade{}
	labels := map[string]string{}
	for _, t := range ts {
		k := keyOf(t)
		by[k.key] = append(by[k.key], t)
		if _, ok := labels[k.key]; !ok {
			labels[k.key] = k.label
		}
	}
	if d == DimDate {
		has := map[string]bool{}
		for k := range by {
			has[k] = true
		}
		out := w.buckets(has)
		for i := range out {
			out[i] = analysisGroup(out[i].Key, out[i].Label, by[out[i].Key])
		}
		return out
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int {
		return cmp.Or(cmp.Compare(groupRank(d, a), groupRank(d, b)), strings.Compare(strings.ToLower(labels[a]), strings.ToLower(labels[b])), strings.Compare(a, b))
	})
	out := make([]AnalysisGroup, 0, len(keys))
	for _, k := range keys {
		out = append(out, analysisGroup(k, labels[k], by[k]))
	}
	return out
}

// groupRank orders groups before their label: exchanges as Markets lists them with Other
// last, long before short, wins/losses/breakeven, and "no theme"/"not recorded" last.
func groupRank(d AnalysisDimension, key string) int {
	switch d {
	case DimExchange:
		for i, m := range Markets {
			if string(m) == key {
				return i
			}
		}
		if key == OtherExchange {
			return len(Markets) + 1
		}
		return len(Markets)
	case DimTheme:
		if key == strings.ToLower(NoThemeLabel) {
			return 1
		}
	case DimPlaybook:
		if key == NoPlaybookLabel {
			return 1
		}
	case DimDirection:
		if key == string(Short) {
			return 1
		}
	case DimOutcome:
		switch key {
		case "loss":
			return 1
		case "breakeven":
			return 2
		}
	}
	return 0
}

// equityCurves builds one curve per currency from the accounts' snapshots in the window. The
// window is cut into equal slots (15 minutes, 1 hour, 4 hours, 1 day or 1 week: the
// smallest that gives at most analysisMaxPoint slots); each slot with a snapshot is one point
// at its latest snapshot, worth the sum of every account's latest equity so far.
func equityCurves(accts []AnalysisAccountInput, w AnalysisWindow) []AnalysisCurve {
	step := 15 * time.Minute
	for _, s := range []time.Duration{15 * time.Minute, time.Hour, 4 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour} {
		step = s
		if w.To.Sub(w.From)/s <= analysisMaxPoint {
			break
		}
	}
	type snap struct {
		EquitySnapshot
		acct string
	}
	byCur := map[string][]snap{}
	for _, a := range accts {
		for _, s := range sortedSnapshots(inWindow(a.Snapshots, w)) {
			cur := strings.ToUpper(s.Currency)
			byCur[cur] = append(byCur[cur], snap{s, a.Account.ID})
		}
	}
	curs := make([]string, 0, len(byCur))
	for c := range byCur {
		curs = append(curs, c)
	}
	slices.Sort(curs)
	out := make([]AnalysisCurve, 0, len(curs))
	for _, cur := range curs {
		ss := byCur[cur]
		slices.SortStableFunc(ss, func(a, b snap) int { return cmp.Or(a.At.Compare(b.At), strings.Compare(a.acct, b.acct)) })
		latest := map[string]Cents{}
		c := AnalysisCurve{Currency: cur}
		for i, s := range ss {
			latest[s.acct] = s.Equity
			slot := s.At.Sub(w.From) / step
			if i+1 < len(ss) && ss[i+1].At.Sub(w.From)/step == slot {
				continue // the slot's latest snapshot makes the point
			}
			var sum wide
			for _, v := range latest {
				sum = sum.add(v)
			}
			c.Points = append(c.Points, AnalysisPoint{At: s.At, Equity: sum.clamp()})
		}
		c.Accounts = len(latest)
		snaps := make([]EquitySnapshot, len(c.Points))
		for i, p := range c.Points {
			snaps[i] = EquitySnapshot{At: p.At, Currency: cur, Equity: p.Equity}
		}
		c.Drawdown = Drawdown(snaps)
		out = append(out, c)
	}
	return out
}

// ---- narrative --------------------------------------------------------------------------

// Narrative headings, in order.
const (
	HeadingOverview = "Overview"
	HeadingWell     = "What went well"
	HeadingBadly    = "What went badly"
	HeadingCosts    = "Costs"
	HeadingRisk     = "Risk & discipline"
	HeadingActivity = "Activity"
)

// PastResultsNote closes every narrative.
const PastResultsNote = "Past results say nothing about future results."

// analysisNarrative writes the narrative from the analysis's own figures (R31-a): neutral,
// factual, never predictive; every amount in it is in one of the analysis's tables.
func analysisNarrative(an Analysis) []AnalysisSection {
	return []AnalysisSection{
		{Heading: HeadingOverview, Paragraphs: overviewText(an)},
		{Heading: HeadingWell, Paragraphs: extremesText(an, true)},
		{Heading: HeadingBadly, Paragraphs: extremesText(an, false)},
		{Heading: HeadingCosts, Paragraphs: costsText(an)},
		{Heading: HeadingRisk, Paragraphs: riskText(an)},
		{Heading: HeadingActivity, Paragraphs: activityText(an)},
	}
}

func overviewText(an Analysis) []string {
	t := an.Totals.Stats
	w := an.Window
	var b strings.Builder
	fmt.Fprintf(&b, "Period: %s (%s, %s). Scope: %s. ", an.Title, strings.ToLower(w.Preset.Label()), plural(w.Days, "risk day", "risk days"), an.Scope)
	if t.Count == 0 {
		b.WriteString("cactusTrade closed no trades in the period.")
	} else {
		fmt.Fprintf(&b, "cactusTrade closed %s on %s: %s, %s and %d breakeven, a win rate of %s. ", plural(t.Count, "trade", "trades"),
			plural(an.TradeAccounts, "account", "accounts"), plural(t.Wins, "win", "wins"), plural(t.Losses, "loss", "losses"), t.Breakeven,
			FormatPercent(t.WinRate))
		fmt.Fprintf(&b, "Their realised P&L after eToro's fees was %s, %s per trade on average.", usd(t.Net), usd(an.Totals.AvgNet))
		if ex := an.Grouping(DimExchange); len(ex) > 0 {
			parts := make([]string, 0, len(ex))
			for _, g := range ex {
				parts = append(parts, g.Label+" "+strconv.Itoa(g.Stats.Count))
			}
			b.WriteString(" Trades by exchange: " + strings.Join(parts, ", ") + ".")
		}
	}
	out := []string{b.String()}
	if len(an.Equity) > 0 {
		out = append(out, equitySentence(an.EquityTotals)+" Equity includes deposits, withdrawals and positions cactusTrade did not open, so it is not a trading result.")
	}
	return out
}

// bestGroup is the group of gs with the largest (well) or smallest (!well) realised P&L that is
// above (below) zero; nil when there is none.
func bestGroup(gs []AnalysisGroup, well bool) *AnalysisGroup {
	var out *AnalysisGroup
	for i := range gs {
		g := &gs[i]
		n := g.Stats.Net
		if g.Stats.Count == 0 || (well && n <= 0) || (!well && n >= 0) {
			continue
		}
		if out == nil || (well && n > out.Stats.Net) || (!well && n < out.Stats.Net) {
			out = g
		}
	}
	return out
}

func tradeRefText(r *AnalysisTradeRef) string {
	return fmt.Sprintf("%s (%s on %s, closed %s)", usd(r.Net), symbolOr(r.Symbol), r.Account, r.ClosedAt.UTC().Format("2 Jan 2006"))
}

func groupText(g *AnalysisGroup) string {
	return fmt.Sprintf("%s (%s over %s)", g.Label, usd(g.Stats.Net), plural(g.Stats.Count, "trade", "trades"))
}

func extremesText(an Analysis, well bool) []string {
	t := an.Totals.Stats
	if t.Count == 0 {
		return []string{"No trades closed in the period."}
	}
	var out []string
	switch {
	case well && t.Wins == 0:
		out = append(out, "No trade closed with a positive result.")
	case !well && t.Losses == 0:
		out = append(out, "No trade closed with a negative result.")
	case well:
		out = append(out, fmt.Sprintf("%s of %d closed with a positive result; the best was %s.", plural(t.Wins, "trade", "trades"), t.Count, tradeRefText(an.Totals.Best)))
	default:
		out = append(out, fmt.Sprintf("%s of %d closed with a negative result; the worst was %s.", plural(t.Losses, "trade", "trades"), t.Count, tradeRefText(an.Totals.Worst)))
	}
	var parts []string
	for _, d := range []AnalysisDimension{DimExchange, DimAccount, DimTheme, DimPlaybook, DimDirection} {
		if g := bestGroup(an.Grouping(d), well); g != nil {
			parts = append(parts, "by "+dimensionNoun(d)+", "+groupText(g))
		}
	}
	if len(parts) > 0 {
		lead := "The largest positive realised P&L was "
		if !well {
			lead = "The largest negative realised P&L was "
		}
		out = append(out, lead+strings.Join(parts, "; ")+".")
	}
	buckets := an.Grouping(DimDate)
	n, with := 0, 0
	for _, g := range buckets {
		if g.Stats.Count == 0 {
			continue
		}
		with++
		if (well && g.Stats.Net > 0) || (!well && g.Stats.Net < 0) {
			n++
		}
	}
	if with > 0 {
		sign := "positive"
		if !well {
			sign = "negative"
		}
		noun := an.Window.Bucket.noun()
		out = append(out, fmt.Sprintf("%d of the %s with trades ended with a %s realised P&L.", n, plural(with, noun, noun+"s"), sign))
	}
	return out
}

func dimensionNoun(d AnalysisDimension) string {
	if d == DimAccount {
		return "trading account"
	}
	return string(d)
}

func costsText(an Analysis) []string {
	t := an.Totals.Stats
	var out []string
	if t.Count > 0 {
		s := fmt.Sprintf("eToro reported %s of fees on %s.", usd(t.Fees), plural(t.Count-t.FeesUnknown, "trade", "trades"))
		if t.FeesUnknown > 0 {
			s += fmt.Sprintf(" It did not report the fees of %s; they are shown as unknown and not counted as zero.", plural(t.FeesUnknown, "trade", "trades"))
		}
		s += fmt.Sprintf(" Before reported fees the realised P&L was %s.", usd(t.Gross))
		out = append(out, s)
	} else {
		out = append(out, "No trades closed, so eToro reported no trading fees.")
	}
	u := an.LLM
	switch {
	case u.Calls == 0:
		out = append(out, "The LLM reviewer was not called.")
	case u.CostKnown():
		out = append(out, fmt.Sprintf("The LLM reviewer was called %s at a cost of %s.", times(int(min(u.Calls, 1<<31))), FormatMicrosUSD(u.Cost)))
	default:
		out = append(out, fmt.Sprintf("The LLM reviewer was called %s; the cost is known for %s only (%s).", times(int(min(u.Calls, 1<<31))),
			plural(int(min(u.Calls-u.CostUnknownCalls, 1<<31)), "call", "calls"), FormatMicrosUSD(u.Cost)))
	}
	return out
}

func riskText(an Analysis) []string {
	r := an.Risk
	var parts []string
	if r.KillSwitches == 0 {
		parts = append(parts, "the kill switch was not used")
	} else {
		parts = append(parts, "the kill switch was used "+times(r.KillSwitches))
		if r.KillResets > 0 {
			parts[len(parts)-1] += " and reset " + times(r.KillResets)
		}
	}
	if r.DailyStops == 0 {
		parts = append(parts, "the daily loss limit did not stop trading")
	} else {
		parts = append(parts, "the daily loss limit stopped new entries "+times(r.DailyStops))
	}
	if r.LimitChanges == 0 {
		parts = append(parts, "the risk limits were not changed")
	} else {
		parts = append(parts, "the risk limits were changed "+times(r.LimitChanges))
	}
	if r.ForceCloses == 0 {
		parts = append(parts, "nothing was force-closed")
	} else {
		parts = append(parts, "positions were force-closed "+times(r.ForceCloses))
	}
	s := "In the period " + strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1] + "."
	if r.RiskRefusals > 0 {
		s += fmt.Sprintf(" The risk engine refused %s.", plural(r.RiskRefusals, "candidate", "candidates"))
	} else {
		s += " The risk engine refused no candidate."
	}
	return []string{s}
}

func activityText(an Analysis) []string {
	var out []string
	if an.DecisionsTotal == 0 {
		out = append(out, "The agent logged no decisions.")
	} else {
		parts := make([]string, 0, len(an.Decisions))
		for _, c := range an.Decisions {
			parts = append(parts, strings.ToLower(c.Label[:1])+c.Label[1:]+" "+strconv.Itoa(c.N))
		}
		s := fmt.Sprintf("The agent logged %s: %s.", plural(an.DecisionsTotal, "decision", "decisions"), strings.Join(parts, ", "))
		vs := make([]string, 0, len(an.Verdicts))
		for _, v := range an.Verdicts {
			vs = append(vs, strings.ToLower(v.Label)+" "+strconv.Itoa(v.N))
		}
		s += " The LLM reviewer's verdicts: " + strings.Join(vs, ", ") + "."
		out = append(out, s)
	}
	if an.HasOursShare {
		all := an.Totals.Stats.Count + an.ForeignTrades
		s := fmt.Sprintf("Of the %s eToro reported on these accounts, %d (%s) were of positions cactusTrade opened.",
			plural(all, "closed trade", "closed trades"), an.Totals.Stats.Count, FormatPercent(an.OursShareBp))
		switch {
		case an.ForeignTrades == 1:
			s += " The other one is counted apart and not in the figures above."
		case an.ForeignTrades > 1:
			s += fmt.Sprintf(" The other %d are counted apart and not in the figures above.", an.ForeignTrades)
		}
		out = append(out, s)
	}
	if an.ActivityCapped {
		out = append(out, "Some activity counts are lower bounds: more records existed than one analysis reads.")
	}
	return append(out, PastResultsNote)
}

// analysisNotes are the fixed explanations under the figures.
func analysisNotes(an Analysis) []string {
	notes := []string{
		"Trade figures are eToro's closed trades of positions cactusTrade opened, in US dollars, after the fees eToro reported; " +
			"trades of positions it did not open are counted apart.",
		"Fees eToro did not report are shown as unknown and are never counted as zero.",
		"Equity is shown per currency: amounts in different currencies are never added together (there is no currency conversion).",
		"Periods are whole risk days from 22:00 UTC; trades are grouped by the " + an.Window.Bucket.noun() + " they closed in.",
		"A trade's theme is its share's theme now (" + NoThemeLabel + " when it has none); its playbook comes from the decision that opened it.",
	}
	if an.ActivityCapped {
		notes = append(notes, "Some activity counts are lower bounds: more records existed than one analysis reads.")
	}
	return append(notes, PastResultsNote+" cactusTrade is software, not financial advice; trading can lose money.")
}

// outcomeLabel words a trade's result: "Win", "Loss" or "Breakeven".
func outcomeLabel(net Cents) string {
	switch {
	case net > 0:
		return "Win"
	case net < 0:
		return "Loss"
	}
	return "Breakeven"
}
