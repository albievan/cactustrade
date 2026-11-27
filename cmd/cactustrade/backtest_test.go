package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/strategy"
)

// fakeBars answers candles and costs; candle reads for failIDs fail.
type fakeBars struct {
	bars    []domain.Candle
	failIDs map[domain.InstrumentID]bool
	asked   []domain.InstrumentID
}

func (f *fakeBars) Candles(_ context.Context, id domain.InstrumentID, _ time.Duration, _ int) ([]domain.Candle, error) {
	f.asked = append(f.asked, id)
	if f.failIDs[id] {
		return nil, domain.ErrUnavailable
	}
	return f.bars, nil
}

func (f *fakeBars) Costs(context.Context, domain.InstrumentID, domain.Direction, domain.Cents) ([]domain.CostItem, error) {
	return []domain.CostItem{{Type: "marketSpread", Value: 100_000}}, nil
}

// TestRunBacktests: every share on the list is run in its own market, a failing share is
// reported and the others still run, a non-share or unsupported market is skipped unread.
func TestRunBacktests(t *testing.T) {
	t0 := time.Date(2026, 11, 23, 14, 30, 0, 0, time.UTC)
	var bars []domain.Candle
	for i := range 300 {
		p := domain.Micros(100_000_000 + int64(math.Sin(float64(i)/7)*1_000_000))
		bars = append(bars, domain.Candle{Start: t0.Add(time.Duration(i) * time.Minute), Open: p, High: p + 50_000, Low: p - 50_000, Close: p, Volume: 100})
	}
	f := &fakeBars{bars: bars, failIDs: map[domain.InstrumentID]bool{2: true}}
	targets := []domain.Instrument{
		{ID: 1, Symbol: "AAA", Type: "Stocks", Market: domain.MarketNYSE},
		{ID: 2, Symbol: "BBB.L", Type: "Stocks"}, // market from the symbol
		{ID: 3, Symbol: "SAP.DE", Type: "Stocks"},
		{ID: 4, Symbol: "IDX", Type: "Indices", Market: domain.MarketNYSE},
		{ID: 5, Symbol: "CBA.ASX", Type: "Stocks", Market: domain.MarketASX},
	}
	o := backtestOptions{interval: time.Minute, bars: 300, start: 10_000_00, now: t0}
	res := runBacktests(context.Background(), f, targets, o)
	if len(res) != 5 || len(f.asked) != 3 || f.asked[0] != 1 || f.asked[1] != 2 || f.asked[2] != 5 {
		t.Fatalf("asked %v for %d results", f.asked, len(res))
	}
	if !errors.Is(res[1].err, domain.ErrUnavailable) || res[2].skipped != "not on a supported market" || !strings.Contains(res[3].skipped, "not a share") {
		t.Fatalf("results %+v", res)
	}
	if res[0].err != nil || res[0].rep.StartEquity != 10_000_00 || res[0].rep.Bars == 0 || !strings.Contains(res[0].rep.Costs.Recorded, "$2000.00") {
		t.Fatalf("AAA: %+v", res[0])
	}
	a := aggregateBacktests(res)
	if a.Shares != 5 || a.Tested != 2 || a.Failed != 1 || a.Skipped != 2 {
		t.Fatalf("aggregate %+v", a)
	}
	text := backtestText(res, a)
	for _, want := range []string{"== AAA (instrument 1, NYSE)", "Backtest of instrument 1", "failed: backtest 2: candles", "skipped: not on a supported market", "== Aggregate over 5 shares", "verdict"} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	var s backtestSummary
	b, err := json.Marshal(summarise("acc-1", true, o, res))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s.Account != "acc-1" || !s.Watchlist || s.Interval != "1m0s" || s.StartEquityUSD != "10000.00" || len(s.Shares) != 5 ||
		s.Shares[0].Report == "" || s.Shares[0].Verdict == "" || s.Shares[0].NetReturn == nil || s.Shares[1].Error == "" ||
		s.Shares[2].Skipped == "" || s.Aggregate.Tested != 2 || !strings.Contains(s.Note, "does not predict") {
		t.Fatalf("summary %s", b)
	}
}

// TestAggregateBacktests: the aggregate verdict, in the report's order.
func TestAggregateBacktests(t *testing.T) {
	trade := []strategy.Trade{{}}
	rep := func(net, hold float64, end domain.Cents, trades []strategy.Trade) backtestResult {
		return backtestResult{rep: strategy.Report{StartEquity: 100_00, EndEquity: end, NetReturn: net, BuyAndHold: hold, Trades: trades, CostsPaid: 1_00}}
	}
	for _, c := range []struct {
		name string
		in   []backtestResult
		want string
	}{
		{"none tested", []backtestResult{{skipped: "x"}, {err: errors.New("x")}}, "nothing_tested"},
		{"no trades", []backtestResult{rep(0, 0.1, 100_00, nil)}, "no_trades"},
		{"lost in total", []backtestResult{rep(0.05, 0, 105_00, trade), rep(-0.10, 0, 90_00, trade)}, "lost_money"},
		{"worse than holding", []backtestResult{rep(0.01, 0.05, 101_00, trade)}, "worse_than_holding"},
		{"positive on this data", []backtestResult{rep(0.02, 0.01, 102_00, trade), rep(0, 0, 100_00, nil)}, "positive_on_this_data_only"},
	} {
		a := aggregateBacktests(c.in)
		if a.Verdict != c.want || aggregateWords[a.Verdict] == "" {
			t.Errorf("%s: verdict %q, want %q (%+v)", c.name, a.Verdict, c.want, a)
		}
	}
	a := aggregateBacktests([]backtestResult{rep(0.05, 0, 105_00, trade), rep(-0.10, 0.2, 90_00, trade), rep(0, 0, 100_00, nil)})
	if a.Tested != 3 || a.WithTrades != 2 || a.NoTrades != 1 || a.LostMoney != 1 || a.PositiveOnData != 1 || a.Trades != 2 ||
		a.NetResultUSD != "-5.00" || a.CostsPaidUSD != "3.00" || a.MeanNetReturn == nil || math.Abs(*a.MeanNetReturn+0.05/3) > 1e-12 {
		t.Fatalf("aggregate %+v", a)
	}
	if finite(math.NaN()) != nil || finite(math.Inf(1)) != nil || *finite(0.5) != 0.5 {
		t.Fatal("finite")
	}
}

// TestResearchCommandsRefuseBeforeAnyRequest: the backtest and etoro-probe validate their
// flags, and read only with a demo eToro account that has both keys: an unknown account, a
// real-money account, a view-only platform account or one without keys is refused before
// any request (no network in this test: the eToro host is never reached).
func TestResearchCommandsRefuseBeforeAnyRequest(t *testing.T) {
	dir := t.TempDir()
	e := envOf(map[string]string{"CACTUSTRADE_DATA_DIR": dir, "CACTUSTRADE_DB_PATH": filepath.Join(dir, "c.db")})
	ctx := context.Background()
	cfg, err := loadConfig(nil, e)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openStore(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := store.Users().Create(ctx, domain.User{ID: "u1", Username: "owner", Role: domain.RoleAdmin, PasswordHash: "x", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, a := range []domain.Account{
		{ID: "demo-nokeys", Name: "Demo", OwnerID: "u1", Mode: domain.ModeDemo},
		{ID: "real-1", Name: "Real", OwnerID: "u1", Mode: domain.ModeReal},
		{ID: "t212", Name: "T212", OwnerID: "u1", Mode: domain.ModeDemo, Platform: domain.PlatformTrading212},
	} {
		a.CreatedAt, a.UpdatedAt = now, now
		if err := store.Accounts().Create(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"backtest", "--watchlist"}, "--watchlist needs --account"},
		{[]string{"backtest", "--watchlist", "--account", "x", "--instrument", "1"}, "exclude each other"},
		{[]string{"backtest", "--account", "demo-nokeys"}, "--instrument (or --watchlist"},
		{[]string{"backtest", "--instrument", "1", "--account", "nobody"}, `account "nobody"`},
		{[]string{"backtest", "--instrument", "1", "--account", "real-1"}, "not a demo account"},
		{[]string{"backtest", "--watchlist", "--account", "real-1", "--json"}, "not a demo account"},
		{[]string{"backtest", "--instrument", "1", "--account", "t212"}, "not an eToro account"},
		{[]string{"backtest", "--watchlist", "--account", "demo-nokeys"}, "does not have both eToro keys"},
		{[]string{"etoro-probe"}, "--account and --symbol are required"},
		{[]string{"etoro-probe", "--account", "demo-nokeys"}, "--account and --symbol are required"},
		{[]string{"etoro-probe", "--account", "real-1", "--symbol", "BARC.L"}, "not a demo account"},
		// D39: the probe reads a real account only when told to, and still only with its keys
		{[]string{"etoro-probe", "--account", "real-1", "--symbol", "BARC.L", "--real-read"}, "does not have both eToro keys"},
		{[]string{"backtest", "--instrument", "1", "--account", "real-1", "--real-read"}, "flag provided but not defined"},
		{[]string{"etoro-probe", "--account", "demo-nokeys", "--symbol", "BARC.L", "--raw"}, "does not have both eToro keys"},
		{[]string{"etoro-probe", "--bogus"}, "flag provided but not defined"},
	} {
		err := run(ctx, c.args, e, &out)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
	err = run(ctx, []string{"backtest", "--instrument", "1", "--account", "real-1"}, e, &out)
	if !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("real account: %v", err)
	}
}
