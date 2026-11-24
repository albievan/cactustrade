package domain

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A trading statement (R30) summarises what happened on one or more accounts over a period:
// equity per currency, cactusTrade's closed trades, open positions at the end, drawdown, risk
// events, agent activity and LLM usage, with a plain-English summary. It is built here from
// figures the app read, deterministically and without an LLM, so the same records always
// give the same statement. It describes past records only; nothing here sizes, approves or
// refuses an order.

// StatementScope says which accounts and trades a statement covers.
type StatementScope string

// Statement scopes.
const (
	ScopeCombined StatementScope = "combined" // every account of the schedule, every exchange
	ScopeAccount  StatementScope = "account"  // one account
	ScopeExchange StatementScope = "exchange" // the trades on one exchange, across the accounts
)

// ParseStatementScope validates a scope name ("" = combined).
func ParseStatementScope(s string) (StatementScope, error) {
	switch StatementScope(s) {
	case "", ScopeCombined:
		return ScopeCombined, nil
	case ScopeAccount, ScopeExchange:
		return StatementScope(s), nil
	}
	return "", Invalid("scope", "combined, account or exchange")
}

// OtherExchange names trades whose exchange cactusTrade does not know (no cached instrument
// and no recognised symbol suffix).
const OtherExchange = "Other"

// Event kinds a statement counts.
const (
	EventKillSwitch = "agent.killed"    // the kill switch (by a user or the supervisor)
	EventDailyStop  = "risk.daily_stop" // the daily loss limit stopped new entries
)

// StatementTrade is a closed trade with what the statement shows about its share.
type StatementTrade struct {
	Trade
	Symbol string // "" when the instrument is not cached
	Market Market // "" when unknown
}

// exchange is the trade's exchange label.
func exchangeLabel(m Market) string {
	if m == "" {
		return OtherExchange
	}
	return string(m)
}

// StatementPosition is a position cactusTrade opened that was still open at the end of the
// period.
type StatementPosition struct {
	Account    string // the account's name
	PositionID int64
	Symbol     string
	Market     Market
	Direction  Direction
	OpenedAt   time.Time
}

// StatementAccountInput is what the app read about one account for a statement.
type StatementAccountInput struct {
	Account Account
	// Trades are eToro's closed trades of the account in the period (ours and the others).
	Trades []StatementTrade
	// Snapshots are the account's equity snapshots taken in the period.
	Snapshots []EquitySnapshot
	// OpenPositions are cactusTrade's positions open at the end of the period.
	OpenPositions []StatementPosition
	// Holdings are a view-only platform's holdings as read when the statement was made
	// (nil when not read; HoldingsError says why).
	Holdings      *Holdings
	HoldingsError string
	// Decisions counts the agent's decisions in the period by outcome; Orders cactusTrade's
	// orders created in the period by their current state.
	Decisions map[DecisionOutcome]int
	Orders    map[OrderState]int
	// KillSwitches and DailyStops count those events in the period.
	KillSwitches, DailyStops int
	// LLM sums the account's LLM calls in the period.
	LLM LLMUsage
	// ActivityCapped: there were more decisions or events than were read, so their counts
	// are lower bounds.
	ActivityCapped bool
}

// StatementInput is everything a statement is built from.
type StatementInput struct {
	Window      StatementWindow
	Scope       StatementScope
	Exchange    string // ScopeExchange: the exchange (a Market or OtherExchange)
	GeneratedAt time.Time
	Accounts    []StatementAccountInput
}

// StatementAccountLine names an account on the statement.
type StatementAccountLine struct {
	ID       string
	Name     string
	Platform string // "eToro", "Trading 212" …
	Mode     TradingMode
	ViewOnly bool
}

// StatementEquity is one account's recorded equity over the period: the first and the last
// snapshot taken in it (Known false when none was).
type StatementEquity struct {
	Account              string
	Currency             string
	Known                bool
	Opening, Closing     Cents
	Change               Cents
	OpeningAt, ClosingAt time.Time
	Drawdown             DrawdownFigures
	ViewOnly             bool
}

// StatementEquityTotal adds up the accounts of one currency (never across currencies).
type StatementEquityTotal struct {
	Currency         string
	Accounts         int
	Opening, Closing Cents
	Change           Cents
}

// StatementBreakdown is the trade figures of one exchange or one account.
type StatementBreakdown struct {
	Name  string
	Stats TradeSummary
}

// StatementTradeLine is one notable trade (the largest win or loss).
type StatementTradeLine struct {
	Account string
	Symbol  string
	Net     Cents
}

// StatementCount is a count with its label (decisions by outcome, orders by state).
type StatementCount struct {
	Label string
	N     int
}

// StatementHoldings is a view-only account's holdings as read when the statement was made.
type StatementHoldings struct {
	Account  string
	Platform string
	Holdings *Holdings
	Error    string
}

// Statement is a built trading statement. Trade amounts are US dollar cents (eToro's
// account currency); equity amounts are minor units of each account's own currency.
type Statement struct {
	Window      StatementWindow
	Scope       StatementScope
	Exchange    string
	Title       string // "Combined statement", "Account statement: Demo", "Exchange statement: LSE"
	GeneratedAt time.Time
	Accounts    []StatementAccountLine

	// AccountWide: equity, drawdown, risk events, agent activity and LLM usage are shown
	// (combined and account statements; an exchange statement has trades and positions only).
	AccountWide  bool
	Equity       []StatementEquity
	EquityTotals []StatementEquityTotal

	// Trades are the figures of cactusTrade's own closed trades (OD15); ForeignTrades counts
	// the trades of positions it did not open (listed apart, not counted).
	Trades                  TradeSummary
	LargestWin, LargestLoss *StatementTradeLine
	ForeignTrades           int
	TradeAccounts           int // accounts with at least one of cactusTrade's trades
	ByExchange              []StatementBreakdown
	ByAccount               []StatementBreakdown

	OpenPositions []StatementPosition
	Holdings      []StatementHoldings

	KillSwitches, DailyStops int
	Decisions                []StatementCount
	DecisionsTotal           int
	Orders                   []StatementCount
	OrdersTotal, OrdersFill  int
	LLM                      LLMUsage
	ActivityCapped           bool

	Notes   []string
	Summary string
}

// decisionLabels names decision outcomes for people, in display order.
var decisionLabels = []struct {
	o     DecisionOutcome
	label string
}{
	{DecisionOrdered, "Order sent"},
	{DecisionOrderFailed, "Allowed, not sent"},
	{DecisionRejectedRisk, "Refused by the risk engine"},
	{DecisionRejectedReviewer, "Refused by the LLM reviewer"},
	{DecisionRejectedCost, "Refused on costs"},
	{DecisionPending, "Pending"},
}

// orderLabels names order states for people, in display order.
var orderLabels = []struct {
	s     OrderState
	label string
}{
	{OrderFilled, "Filled"},
	{OrderPartial, "Partly filled"},
	{OrderPending, "Pending at eToro"},
	{OrderSent, "Sent"},
	{OrderCreated, "Created"},
	{OrderCancelled, "Cancelled"},
	{OrderRejected, "Rejected"},
	{OrderAbandoned, "Abandoned"},
	{OrderUnknown, "Unknown"},
}

// BuildStatement builds the statement of in. It sorts and copies what it keeps, so the
// result depends only on the input's content, not its order.
func BuildStatement(in StatementInput) Statement {
	st := Statement{
		Window: in.Window, Scope: in.Scope, Exchange: in.Exchange, GeneratedAt: in.GeneratedAt,
		AccountWide: in.Scope != ScopeExchange,
	}
	accts := slices.Clone(in.Accounts)
	slices.SortStableFunc(accts, func(a, b StatementAccountInput) int {
		return cmp.Or(strings.Compare(strings.ToLower(a.Account.Name), strings.ToLower(b.Account.Name)), strings.Compare(a.Account.ID, b.Account.ID))
	})
	switch in.Scope {
	case ScopeAccount:
		name := ""
		if len(accts) > 0 {
			name = accts[0].Account.Name
		}
		st.Title = "Account statement: " + name
	case ScopeExchange:
		st.Title = "Exchange statement: " + in.Exchange
	default:
		st.Title = "Combined statement"
	}

	var ours []StatementTrade
	decisions := map[DecisionOutcome]int{}
	orders := map[OrderState]int{}
	viewOnly := false
	for _, a := range accts {
		st.Accounts = append(st.Accounts, StatementAccountLine{
			ID: a.Account.ID, Name: a.Account.Name, Platform: a.Account.OnPlatform().Label(), Mode: a.Account.Mode, ViewOnly: !a.Account.Tradable(),
		})
		if !a.Account.Tradable() {
			viewOnly = true
		}
		var mine []ClosedTrade
		for _, t := range a.Trades {
			if in.Scope == ScopeExchange && exchangeLabel(t.Market) != in.Exchange {
				continue
			}
			if !t.Ours {
				st.ForeignTrades++
				continue
			}
			ours = append(ours, t)
			mine = append(mine, t.ClosedTrade)
		}
		if len(mine) > 0 {
			st.TradeAccounts++
		}
		if in.Scope == ScopeCombined || in.Scope == ScopeExchange {
			st.ByAccount = append(st.ByAccount, StatementBreakdown{Name: a.Account.Name, Stats: TradeStats(mine)})
		}
		for _, p := range a.OpenPositions {
			if in.Scope == ScopeExchange && exchangeLabel(p.Market) != in.Exchange {
				continue
			}
			p.Account = a.Account.Name
			st.OpenPositions = append(st.OpenPositions, p)
		}
		if !st.AccountWide {
			continue
		}
		st.Equity = append(st.Equity, equityOf(a))
		if !a.Account.Tradable() {
			st.Holdings = append(st.Holdings, StatementHoldings{
				Account: a.Account.Name, Platform: a.Account.OnPlatform().Label(), Holdings: a.Holdings, Error: a.HoldingsError,
			})
		}
		for o, n := range a.Decisions {
			decisions[o] += n
		}
		for s, n := range a.Orders {
			orders[s] += n
		}
		st.KillSwitches += a.KillSwitches
		st.DailyStops += a.DailyStops
		st.LLM = st.LLM.Add(a.LLM)
		st.ActivityCapped = st.ActivityCapped || a.ActivityCapped
	}

	slices.SortStableFunc(ours, func(a, b StatementTrade) int {
		return cmp.Or(a.ClosedAt.Compare(b.ClosedAt), cmp.Compare(a.PositionID, b.PositionID))
	})
	all := make([]ClosedTrade, len(ours))
	byExchange := map[string][]ClosedTrade{}
	for i, t := range ours {
		all[i] = t.ClosedTrade
		byExchange[exchangeLabel(t.Market)] = append(byExchange[exchangeLabel(t.Market)], t.ClosedTrade)
		switch {
		case t.NetProfit > 0 && (st.LargestWin == nil || t.NetProfit > st.LargestWin.Net):
			st.LargestWin = &StatementTradeLine{Account: accountName(accts, t.AccountID), Symbol: t.Symbol, Net: t.NetProfit}
		case t.NetProfit < 0 && (st.LargestLoss == nil || t.NetProfit < st.LargestLoss.Net):
			st.LargestLoss = &StatementTradeLine{Account: accountName(accts, t.AccountID), Symbol: t.Symbol, Net: t.NetProfit}
		}
	}
	st.Trades = TradeStats(all)
	for _, name := range sortedExchanges(byExchange) {
		st.ByExchange = append(st.ByExchange, StatementBreakdown{Name: name, Stats: TradeStats(byExchange[name])})
	}
	slices.SortStableFunc(st.OpenPositions, func(a, b StatementPosition) int {
		return cmp.Or(strings.Compare(a.Account, b.Account), a.OpenedAt.Compare(b.OpenedAt), cmp.Compare(a.PositionID, b.PositionID))
	})

	if st.AccountWide {
		st.EquityTotals = equityTotals(st.Equity)
		for _, d := range decisionLabels {
			if n := decisions[d.o]; n > 0 {
				st.Decisions = append(st.Decisions, StatementCount{Label: d.label, N: n})
				st.DecisionsTotal += n
			}
		}
		for _, o := range orderLabels {
			if n := orders[o.s]; n > 0 {
				st.Orders = append(st.Orders, StatementCount{Label: o.label, N: n})
				st.OrdersTotal += n
			}
		}
		st.OrdersFill = orders[OrderFilled] + orders[OrderPartial]
	}
	st.Notes = statementNotes(st, viewOnly)
	st.Summary = statementSummary(st)
	return st
}

func accountName(accts []StatementAccountInput, id string) string {
	for _, a := range accts {
		if a.Account.ID == id {
			return a.Account.Name
		}
	}
	return id
}

// sortedExchanges orders exchange names as Markets lists them, then any other name, Other
// last.
func sortedExchanges(m map[string][]ClosedTrade) []string {
	rank := func(name string) int {
		for i, mk := range Markets {
			if string(mk) == name {
				return i
			}
		}
		if name == OtherExchange {
			return len(Markets) + 1
		}
		return len(Markets)
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b string) int { return cmp.Or(cmp.Compare(rank(a), rank(b)), strings.Compare(a, b)) })
	return out
}

// equityOf is an account's first and last snapshot in the period and its drawdown.
func equityOf(a StatementAccountInput) StatementEquity {
	e := StatementEquity{Account: a.Account.Name, ViewOnly: !a.Account.Tradable()}
	snaps := sortedSnapshots(a.Snapshots)
	if len(snaps) == 0 {
		if a.Holdings != nil {
			e.Currency = strings.ToUpper(a.Holdings.Currency)
		}
		return e
	}
	first, last := snaps[0], snaps[len(snaps)-1]
	e.Known, e.Currency = true, strings.ToUpper(last.Currency)
	e.Opening, e.Closing, e.OpeningAt, e.ClosingAt = first.Equity, last.Equity, first.At, last.At
	e.Change = wideOf(last.Equity).sub(first.Equity).clamp()
	e.Drawdown = Drawdown(snaps)
	return e
}

// equityTotals adds up the known equity per currency, sorted by currency code. Different
// currencies are never added together (there is no FX conversion).
func equityTotals(es []StatementEquity) []StatementEquityTotal {
	type acc struct {
		n                int
		opening, closing wide
	}
	by := map[string]*acc{}
	for _, e := range es {
		if !e.Known {
			continue
		}
		a, ok := by[e.Currency]
		if !ok {
			a = &acc{}
			by[e.Currency] = a
		}
		a.n++
		a.opening, a.closing = a.opening.add(e.Opening), a.closing.add(e.Closing)
	}
	out := make([]StatementEquityTotal, 0, len(by))
	for cur, a := range by {
		out = append(out, StatementEquityTotal{
			Currency: cur, Accounts: a.n, Opening: a.opening.clamp(), Closing: a.closing.clamp(), Change: a.closing.subWide(a.opening).clamp(),
		})
	}
	slices.SortFunc(out, func(a, b StatementEquityTotal) int { return strings.Compare(a.Currency, b.Currency) })
	return out
}

// statementNotes are the fixed explanations printed under the figures.
func statementNotes(st Statement, viewOnly bool) []string {
	notes := []string{
		"Trade figures are eToro's closed trades of positions cactusTrade opened, in US dollars; " +
			"trades of positions it did not open are counted apart.",
		"Fees eToro did not report are shown as unknown and are never counted as zero.",
	}
	if st.AccountWide {
		notes = append(notes,
			"Equity is shown per currency: amounts in different currencies are never added together (there is no currency conversion).",
			"Equity is the first and the last snapshot recorded in the period; it includes deposits, withdrawals and positions "+
				"cactusTrade did not open, so it is not a trading result.")
	} else {
		notes = append(notes, "Equity, drawdown, risk events, agent activity and LLM usage belong to accounts, not exchanges: "+
			"see the combined and account statements.")
	}
	if viewOnly && st.AccountWide {
		notes = append(notes, "Trading 212, Saxo and Tiger Brokers accounts are view-only: they add equity and holdings only. "+
			"Their holdings are as read when this statement was made.")
	}
	if st.ActivityCapped {
		notes = append(notes, "Some activity counts are lower bounds: more records existed than one statement reads.")
	}
	return append(notes, "Past results say nothing about future results. cactusTrade is software, not financial advice; trading can lose money.")
}

// times writes a count as "once", "twice" or "N times".
func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return strconv.Itoa(n) + " times"
}

// plural writes "1 trade", "2 trades".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// usd formats US dollar cents.
func usd(c Cents) string { return FormatMoney("USD", c) }

// statementSummary is the plain-English paragraph at the top of the statement, built only
// from the statement's own figures. The wording is neutral: it reports, it never judges.
func statementSummary(st Statement) string {
	var b strings.Builder
	w := st.Window
	period := "Over the " + w.Period.noun() + " (" + w.Title() + ")"
	if w.Period == PeriodDaily {
		period = "On " + w.Title()
	}
	where := ""
	if st.Scope == ScopeExchange {
		where = " on " + st.Exchange
	}
	t := st.Trades
	if t.Count == 0 {
		fmt.Fprintf(&b, "%s cactusTrade closed no trades%s on %s.", period, where, plural(len(st.Accounts), "account", "accounts"))
	} else {
		fmt.Fprintf(&b, "%s cactusTrade closed %s%s on %s", period, plural(t.Count, "trade", "trades"), where,
			plural(st.TradeAccounts, "account", "accounts"))
		if st.Scope != ScopeExchange && len(st.ByExchange) > 0 {
			parts := make([]string, 0, len(st.ByExchange))
			for _, e := range st.ByExchange {
				parts = append(parts, e.Name+" "+strconv.Itoa(e.Stats.Count))
			}
			b.WriteString(" (" + strings.Join(parts, ", ") + ")")
		}
		fmt.Fprintf(&b, ": %s, %s and %s, a win rate of %s.", plural(t.Wins, "win", "wins"), plural(t.Losses, "loss", "losses"),
			strconv.Itoa(t.Breakeven)+" breakeven", FormatPercent(t.WinRate))
		fmt.Fprintf(&b, " Their realised P&L was %s after %s of fees reported by eToro", usd(t.Net), usd(t.Fees))
		if t.FeesUnknown > 0 {
			fmt.Fprintf(&b, "; eToro did not report the fees of %s", plural(t.FeesUnknown, "trade", "trades"))
		}
		b.WriteString(".")
		switch {
		case st.LargestWin != nil && st.LargestLoss != nil:
			fmt.Fprintf(&b, " The largest win was %s (%s) and the largest loss %s (%s).", usd(st.LargestWin.Net), symbolOr(st.LargestWin.Symbol),
				usd(st.LargestLoss.Net), symbolOr(st.LargestLoss.Symbol))
		case st.LargestWin != nil:
			fmt.Fprintf(&b, " The largest win was %s (%s).", usd(st.LargestWin.Net), symbolOr(st.LargestWin.Symbol))
		case st.LargestLoss != nil:
			fmt.Fprintf(&b, " The largest loss was %s (%s).", usd(st.LargestLoss.Net), symbolOr(st.LargestLoss.Symbol))
		}
	}
	if st.ForeignTrades > 0 {
		fmt.Fprintf(&b, " eToro also reported %s of positions cactusTrade did not open; they are not counted above.",
			plural(st.ForeignTrades, "closed trade", "closed trades"))
	}
	if st.AccountWide {
		b.WriteString(" " + equitySentence(st.EquityTotals))
	}
	switch n := len(st.OpenPositions); n {
	case 0:
		b.WriteString(" No position cactusTrade opened was open at the end of the period.")
	default:
		fmt.Fprintf(&b, " %s cactusTrade opened %s still open at the end of the period.", plural(n, "position", "positions"),
			map[bool]string{true: "was", false: "were"}[n == 1])
	}
	if !st.AccountWide {
		return b.String()
	}
	switch {
	case st.KillSwitches == 0 && st.DailyStops == 0:
		b.WriteString(" The kill switch was not used and the daily loss limit did not stop trading.")
	default:
		if st.KillSwitches > 0 {
			fmt.Fprintf(&b, " The kill switch was used %s.", times(st.KillSwitches))
		}
		if st.DailyStops > 0 {
			fmt.Fprintf(&b, " The daily loss limit stopped new entries %s.", times(st.DailyStops))
		}
	}
	fmt.Fprintf(&b, " The agent logged %s and cactusTrade stored %s (%s filled).", plural(st.DecisionsTotal, "decision", "decisions"),
		plural(st.OrdersTotal, "order", "orders"), strconv.Itoa(st.OrdersFill))
	if st.LLM.Calls > 0 {
		fmt.Fprintf(&b, " The LLM reviewer was called %s", times(int(min(st.LLM.Calls, 1<<31))))
		if st.LLM.CostKnown() {
			fmt.Fprintf(&b, " at a cost of %s.", FormatMicrosUSD(st.LLM.Cost))
		} else {
			fmt.Fprintf(&b, "; the cost is known for %s only (%s).", plural(int(min(st.LLM.Calls-st.LLM.CostUnknownCalls, 1<<31)), "call", "calls"),
				FormatMicrosUSD(st.LLM.Cost))
		}
	}
	return b.String()
}

// equitySentence says how recorded equity went, per currency (never added across
// currencies): "Recorded equity went from $1.00 to $2.00 (USD, 1 account)." Statements (R30)
// and analyses (R31) share it.
func equitySentence(totals []StatementEquityTotal) string {
	if len(totals) == 0 {
		return "No equity was recorded in the period."
	}
	parts := make([]string, 0, len(totals))
	for _, e := range totals {
		parts = append(parts, fmt.Sprintf("from %s to %s (%s, %s)", FormatMoney(e.Currency, e.Opening), FormatMoney(e.Currency, e.Closing),
			e.Currency, plural(e.Accounts, "account", "accounts")))
	}
	s := "Recorded equity went " + strings.Join(parts, " and ")
	if len(totals) > 1 {
		s += "; amounts in different currencies are not added together"
	}
	return s + "."
}

func symbolOr(s string) string {
	if s == "" {
		return "share not cached"
	}
	return s
}

// FormatMicrosUSD writes a US dollar amount held in micros with four decimals: "$0.1234".
func FormatMicrosUSD(m Micros) string {
	v := int64(m)
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	v = (v + 50) / 100 // to 1/10 000 dollar, rounded
	return fmt.Sprintf("%s$%s.%04d", sign, groupThousands(strconv.FormatInt(v/10_000, 10)), v%10_000)
}

// ---- files ------------------------------------------------------------------------------

// StatementFileName is the attachment's file name:
// cactusTrade-statement-<key>-<period>-<scope>[-<name>].pdf, with name reduced to lower-case
// letters, digits and hyphens.
func StatementFileName(w StatementWindow, scope StatementScope, name string) string {
	base := "cactusTrade-statement-" + strings.ToLower(w.Key) + "-" + string(w.Period) + "-" + string(scope)
	if s := slug(name); s != "" {
		base += "-" + s
	}
	return base + ".pdf"
}

// slug keeps ASCII letters and digits (lower-cased) and joins the runs with hyphens, at most
// 40 characters.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		default:
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.TrimRight(b.String(), "-")
}
