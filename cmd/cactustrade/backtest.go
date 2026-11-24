package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/strategy"
)

// backtestCmd runs a walk-forward backtest on eToro's own candles and quoted costs.
//
//	cactustrade backtest --instrument 1001 [--account ID] [--interval 1m] [--bars 1000] [--equity 10000] [--market NYSE] [--json]
//	cactustrade backtest --watchlist --account ID [--interval 1m] [--bars 1000] [--equity 10000] [--json]
//
// It only reads from eToro (candles, costs) with a demo account's own keys — the given
// account's, else the price stream's — and never places an order. --watchlist backtests every
// share on the account's watchlist, each in its own market's sessions and each from the same
// starting equity, and adds an aggregate. --json prints a machine-readable summary (the
// per-share reports included) to stdout instead of the text. Nothing is tuned: the same
// strategy configuration as the agent's.
func backtestCmd(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("backtest", flag.ContinueOnError)
	fs.SetOutput(out)
	id := fs.Int64("instrument", 0, "eToro instrument id (or --watchlist)")
	interval := fs.Duration("interval", time.Minute, "bar interval: 1m, 5m, 10m, 15m, 30m, 1h, 4h")
	bars := fs.Int("bars", 1000, "bars to fetch (eToro returns at most 1000)")
	equity := fs.Float64("equity", 10000, "starting equity in USD (per share)")
	market := fs.String("market", "NYSE", "LSE, NYSE or ASX for --instrument (sessions are this market's local dates)")
	account := fs.String("account", "", "demo eToro account whose keys are used (default: the price stream's)")
	watchlist := fs.Bool("watchlist", false, "backtest every share on --account's watchlist")
	asJSON := fs.Bool("json", false, "print a machine-readable JSON summary instead of the text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch {
	case *watchlist && *id > 0:
		return errors.New("backtest: --instrument and --watchlist exclude each other")
	case *watchlist && *account == "":
		return errors.New("backtest: --watchlist needs --account")
	case !*watchlist && *id <= 0:
		return errors.New("backtest: --instrument (or --watchlist --account ID) is required")
	}
	if *equity < 1 || *equity > 1e9 {
		return errors.New("backtest: --equity must be 1 to 1e9 USD")
	}
	if _, err := marketLocation(*market); err != nil {
		return err
	}
	r, err := openResearch(ctx, getenv, *account, true, nil)
	if err != nil {
		return fmt.Errorf("backtest: %w", err)
	}
	defer r.Close()
	ctx = ports.WithAccount(ctx, r.acct)
	targets := []domain.Instrument{{ID: domain.InstrumentID(*id), Market: domain.Market(strings.ToUpper(*market))}}
	if *watchlist {
		items, err := r.store.Watchlist().List(ctx, r.acct.ID)
		if err != nil {
			return fmt.Errorf("backtest: watchlist of %s: %w", r.acct.ID, err)
		}
		targets = targets[:0]
		for _, it := range items {
			targets = append(targets, it.Instrument)
		}
	}
	o := backtestOptions{interval: *interval, bars: *bars, start: domain.Cents(math.Round(*equity * 100)), now: time.Now().UTC()}
	results := runBacktests(ctx, r.client, targets, o)
	switch {
	case *asJSON:
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(summarise(r.acct.ID, *watchlist, o, results))
	case !*watchlist:
		if results[0].err != nil {
			return results[0].err
		}
		_, err := fmt.Fprint(out, results[0].rep.String())
		return err
	}
	_, err = io.WriteString(out, backtestText(results, aggregateBacktests(results)))
	return err
}

// backtestReader is all the backtest asks eToro: candles and costs, read only.
type backtestReader interface {
	Candles(ctx context.Context, id domain.InstrumentID, interval time.Duration, count int) ([]domain.Candle, error)
	Costs(ctx context.Context, id domain.InstrumentID, d domain.Direction, amount domain.Cents) ([]domain.CostItem, error)
}

type backtestOptions struct {
	interval time.Duration
	bars     int
	start    domain.Cents
	now      time.Time
}

// backtestResult is one share's run: its report, or why it failed or was skipped.
type backtestResult struct {
	in      domain.Instrument
	rep     strategy.Report
	err     error
	skipped string
}

// runBacktests backtests each share in turn (the client's limiter paces the reads); a share
// that fails or cannot be tested is reported, the others still run.
func runBacktests(ctx context.Context, r backtestReader, targets []domain.Instrument, o backtestOptions) []backtestResult {
	out := make([]backtestResult, 0, len(targets))
	for _, in := range targets {
		res := backtestResult{in: in}
		m := in.Market
		if m == "" {
			m, _ = domain.MarketForSymbol(in.Symbol)
		}
		loc, err := marketLocation(string(m))
		switch {
		case in.Type != "" && !domain.IsShare(in.Type):
			res.skipped = "not a share (" + in.Type + ")"
		case err != nil:
			res.skipped = "not on a supported market"
		default:
			res.rep, res.err = backtestShare(ctx, r, in.ID, loc, o)
		}
		out = append(out, res)
	}
	return out
}

// backtestShare runs strategy.Backtest on eToro's latest bars of one share with the costs
// eToro quotes for buying 20 % of the equity (the playbooks' largest suggested size).
func backtestShare(ctx context.Context, r backtestReader, id domain.InstrumentID, loc *time.Location, o backtestOptions) (strategy.Report, error) {
	candles, err := r.Candles(ctx, id, o.interval, o.bars)
	if err != nil {
		return strategy.Report{}, fmt.Errorf("backtest %d: candles: %w", id, err)
	}
	size := o.start / 5
	items, err := r.Costs(ctx, id, domain.Long, size)
	if err != nil {
		return strategy.Report{}, fmt.Errorf("backtest %d: costs: %w", id, err)
	}
	return strategy.Backtest(id, candles, strategy.BacktestConfig{
		StartEquity: o.start, Costs: costsFromQuote(items, size, o.now),
		SameSession: func(a, b time.Time) bool {
			ay, am, ad := a.In(loc).Date()
			by, bm, bd := b.In(loc).Date()
			return ay == by && am == bm && ad == bd
		},
	}), nil
}

// backtestAggregate sums independent runs (each share from the same starting equity).
type backtestAggregate struct {
	Shares           int      `json:"shares"`
	Tested           int      `json:"tested"`
	Failed           int      `json:"failed"`
	Skipped          int      `json:"skipped"`
	WithTrades       int      `json:"withTrades"`
	NoTrades         int      `json:"noTrades"`
	LostMoney        int      `json:"lostMoney"`
	WorseThanHolding int      `json:"worseThanHolding"`
	PositiveOnData   int      `json:"positiveOnThisDataOnly"`
	Trades           int      `json:"trades"`
	CostsPaidUSD     string   `json:"costsPaidUsd"`
	NetResultUSD     string   `json:"netResultUsd"` // sum over the tested shares of end − start equity
	MeanNetReturn    *float64 `json:"meanNetReturn"`
	MeanBuyAndHold   *float64 `json:"meanBuyAndHold"`
	Verdict          string   `json:"verdict"`
}

// aggregateBacktests counts the verdicts and sums the results of the tested shares. Its
// verdict is checked in the report's order: nothing tested, no trades, a loss in total after
// costs, a mean return below holding the shares, else positive on this data only.
func aggregateBacktests(results []backtestResult) backtestAggregate {
	a := backtestAggregate{Shares: len(results)}
	var costs, net domain.Cents
	var sumNet, sumHold float64
	for _, r := range results {
		switch {
		case r.skipped != "":
			a.Skipped++
			continue
		case r.err != nil:
			a.Failed++
			continue
		}
		a.Tested++
		a.Trades += len(r.rep.Trades)
		costs += r.rep.CostsPaid
		net += r.rep.EndEquity - r.rep.StartEquity
		sumNet += r.rep.NetReturn
		sumHold += r.rep.BuyAndHold
		switch r.rep.Verdict() {
		case strategy.VerdictNoTrades:
			a.NoTrades++
		case strategy.VerdictLostMoney:
			a.LostMoney++
		case strategy.VerdictWorseThanHolding:
			a.WorseThanHolding++
		default:
			a.PositiveOnData++
		}
	}
	a.WithTrades = a.Tested - a.NoTrades
	a.CostsPaidUSD, a.NetResultUSD = costs.String(), net.String()
	if a.Tested > 0 {
		a.MeanNetReturn, a.MeanBuyAndHold = finite(sumNet/float64(a.Tested)), finite(sumHold/float64(a.Tested))
	}
	switch {
	case a.Tested == 0:
		a.Verdict = "nothing_tested"
	case a.Trades == 0:
		a.Verdict = string(strategy.VerdictNoTrades)
	case net < 0:
		a.Verdict = string(strategy.VerdictLostMoney)
	case sumNet < sumHold:
		a.Verdict = string(strategy.VerdictWorseThanHolding)
	default:
		a.Verdict = string(strategy.VerdictPositiveOnThisData)
	}
	return a
}

// aggregateWords says the aggregate verdict plainly.
var aggregateWords = map[string]string{
	"nothing_tested":                           "no share could be tested: nothing can be concluded",
	string(strategy.VerdictNoTrades):           "no trades on any share: nothing can be concluded",
	string(strategy.VerdictLostMoney):          "the strategy LOST money in total over these shares after costs",
	string(strategy.VerdictWorseThanHolding):   "the strategy did worse on average than simply holding the shares",
	string(strategy.VerdictPositiveOnThisData): "positive on this data only; a backtest does not predict future results",
}

// backtestText is the human report: each share's report, then the aggregate.
func backtestText(results []backtestResult, a backtestAggregate) string {
	var b strings.Builder
	for _, r := range results {
		fmt.Fprintf(&b, "== %s (instrument %d, %s)\n", r.in.Symbol, r.in.ID, r.in.Market)
		switch {
		case r.skipped != "":
			fmt.Fprintf(&b, "  skipped: %s\n", r.skipped)
		case r.err != nil:
			fmt.Fprintf(&b, "  failed: %v\n", r.err)
		default:
			b.WriteString(r.rep.String())
		}
		b.WriteString("\n")
	}
	pct := func(f *float64) string {
		if f == nil {
			return "n/a"
		}
		return fmt.Sprintf("%+.2f%%", *f*100)
	}
	fmt.Fprintf(&b, "== Aggregate over %d shares (independent runs, each from the same starting equity)\n", a.Shares)
	fmt.Fprintf(&b, "  tested %d, failed %d, skipped %d; with trades %d, no trades %d\n", a.Tested, a.Failed, a.Skipped, a.WithTrades, a.NoTrades)
	fmt.Fprintf(&b, "  lost money %d, worse than holding %d, positive on this data only %d\n", a.LostMoney, a.WorseThanHolding, a.PositiveOnData)
	fmt.Fprintf(&b, "  trades %d, costs paid $%s, net result $%s (sum over the tested shares)\n", a.Trades, a.CostsPaidUSD, a.NetResultUSD)
	fmt.Fprintf(&b, "  mean return %s after costs, mean buy and hold %s (no costs)\n", pct(a.MeanNetReturn), pct(a.MeanBuyAndHold))
	fmt.Fprintf(&b, "  verdict       %s\n", aggregateWords[a.Verdict])
	return b.String()
}

// backtestSummary is the --json output.
type backtestSummary struct {
	Account        string              `json:"account"`
	Watchlist      bool                `json:"watchlist"`
	Interval       string              `json:"interval"`
	BarsRequested  int                 `json:"barsRequested"`
	StartEquityUSD string              `json:"startEquityUsd"`
	GeneratedAt    time.Time           `json:"generatedAt"`
	Note           string              `json:"note"`
	Shares         []backtestShareJSON `json:"shares"`
	Aggregate      backtestAggregate   `json:"aggregate"`
}

// backtestShareJSON is one share in the --json output. Ratios are fractions (0.01 = 1 %).
type backtestShareJSON struct {
	InstrumentID   int64      `json:"instrumentId"`
	Symbol         string     `json:"symbol"`
	Market         string     `json:"market"`
	Skipped        string     `json:"skipped,omitempty"`
	Error          string     `json:"error,omitempty"`
	From           *time.Time `json:"from,omitempty"`
	To             *time.Time `json:"to,omitempty"`
	Bars           int        `json:"bars"`
	Trades         int        `json:"trades"`
	SkippedEntries int        `json:"skippedEntries"`
	EndEquityUSD   string     `json:"endEquityUsd,omitempty"`
	NetReturn      *float64   `json:"netReturn"`
	BuyAndHold     *float64   `json:"buyAndHold"`
	MaxDrawdown    *float64   `json:"maxDrawdown"`
	Sharpe         *float64   `json:"sharpe"`
	WinRate        *float64   `json:"winRate"`
	CostsPaidUSD   string     `json:"costsPaidUsd,omitempty"`
	CostsSource    string     `json:"costsSource,omitempty"`
	Verdict        string     `json:"verdict,omitempty"`
	Report         string     `json:"report,omitempty"`
}

func summarise(account string, watchlist bool, o backtestOptions, results []backtestResult) backtestSummary {
	s := backtestSummary{
		Account: account, Watchlist: watchlist, Interval: o.interval.String(), BarsRequested: o.bars,
		StartEquityUSD: o.start.String(), GeneratedAt: o.now,
		Note:      "walk-forward backtest on eToro's own candles and quoted costs; nothing tuned; a backtest does not predict future results",
		Aggregate: aggregateBacktests(results),
	}
	for _, r := range results {
		j := backtestShareJSON{InstrumentID: int64(r.in.ID), Symbol: r.in.Symbol, Market: string(r.in.Market), Skipped: r.skipped}
		switch {
		case r.skipped != "":
		case r.err != nil:
			j.Error = r.err.Error()
		default:
			rep := r.rep
			if rep.Bars > 0 {
				from, to := rep.From.UTC(), rep.To.UTC()
				j.From, j.To = &from, &to
			}
			j.Bars, j.Trades, j.SkippedEntries = rep.Bars, len(rep.Trades), rep.SkippedEntries
			j.EndEquityUSD, j.CostsPaidUSD, j.CostsSource = rep.EndEquity.String(), rep.CostsPaid.String(), rep.Costs.Recorded
			j.NetReturn, j.BuyAndHold, j.MaxDrawdown = finite(rep.NetReturn), finite(rep.BuyAndHold), finite(rep.MaxDrawdown)
			j.Sharpe, j.WinRate = finite(rep.Sharpe), finite(rep.WinRate)
			j.Verdict, j.Report = string(rep.Verdict()), rep.String()
		}
		s.Shares = append(s.Shares, j)
	}
	return s
}

// finite returns f, or nil (JSON null) when it is NaN or infinite.
func finite(f float64) *float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return &f
}

func marketLocation(m string) (*time.Location, error) {
	switch strings.ToUpper(m) {
	case string(domain.MarketLSE):
		return time.LoadLocation("Europe/London")
	case string(domain.MarketNYSE), "NASDAQ":
		return time.LoadLocation("America/New_York")
	case string(domain.MarketASX):
		return time.LoadLocation("Australia/Sydney")
	}
	return nil, fmt.Errorf("backtest: --market must be LSE, NYSE or ASX, not %q", m)
}

// costsFromQuote turns eToro's cost lines for buying `size` into fractions of the amount.
// The market spread is paid half on entry and half on exit; everything else (markup, fees,
// tax such as UK stamp duty) is charged on the buy. Overnight fees do not apply (positions
// are closed each session).
func costsFromQuote(items []domain.CostItem, size domain.Cents, at time.Time) strategy.Costs {
	c := strategy.Costs{Recorded: fmt.Sprintf("eToro costs endpoint for $%s at %s", size, at.Format(time.RFC3339))}
	if size <= 0 {
		return c
	}
	amount := float64(size) / 100
	for _, it := range items {
		f := float64(it.Value) / 1e6 / amount
		switch it.Type {
		case "marketSpread":
			c.Spread += f
		case "overnightFee", "overWeekendFee":
		default:
			c.BuyFee += f
		}
	}
	return c
}
