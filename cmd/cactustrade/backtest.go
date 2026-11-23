package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/adapters/etoro"
	"github.com/albievan/cactustrade/internal/adapters/system"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/strategy"
)

// backtestCmd runs a walk-forward backtest on eToro's own candles and quoted costs.
//
//	cactustrade backtest --instrument 1001 [--interval 1m] [--bars 1000] [--equity 10000] [--market NYSE]
//
// It only reads from eToro (candles, costs) with the demo keys; it never places an order.
func backtestCmd(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("backtest", flag.ContinueOnError)
	fs.SetOutput(out)
	id := fs.Int64("instrument", 0, "eToro instrument id (required)")
	interval := fs.Duration("interval", time.Minute, "bar interval: 1m, 5m, 10m, 15m, 30m, 1h, 4h")
	bars := fs.Int("bars", 1000, "bars to fetch (eToro returns at most 1000)")
	equity := fs.Float64("equity", 10000, "starting equity in USD")
	market := fs.String("market", "NYSE", "LSE or NYSE (sessions are this market's local dates)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id <= 0 {
		return errors.New("backtest: --instrument is required")
	}
	if *equity < 1 || *equity > 1e9 {
		return errors.New("backtest: --equity must be 1 to 1e9 USD")
	}
	loc, err := marketLocation(*market)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(nil, getenv)
	if err != nil {
		return err
	}
	store, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	if cfg.MigrateOnStart {
		if err := store.Migrate(ctx); err != nil {
			return err
		}
	}
	secrets, _, err := openSecrets(cfg, store, getenv, system.Clock{}, newLogger(cfg.LogLevel))
	if err != nil {
		return err
	}
	client, err := etoro.New(etoro.Config{HTTP: &http.Client{Timeout: 30 * time.Second}, Secrets: secrets, Random: system.Random{}})
	if err != nil {
		return err
	}
	// Research only ever reads with a demo account's keys.
	acct, ok := streamAccount(ctx, store, secrets)
	if !ok || acct.Mode == domain.ModeReal {
		return fmt.Errorf("backtest: no demo eToro account with both keys set: %w: %w", domain.ErrCredentialsMissing, domain.ErrUnavailable)
	}
	ctx = ports.WithAccount(ctx, acct)
	candles, err := client.Candles(ctx, domain.InstrumentID(*id), *interval, *bars)
	if err != nil {
		return fmt.Errorf("backtest: candles: %w", err)
	}
	start := domain.Cents(math.Round(*equity * 100))
	size := start / 5 // the playbooks' largest suggested size (20 % of equity)
	items, err := client.Costs(ctx, domain.InstrumentID(*id), domain.Long, size)
	if err != nil {
		return fmt.Errorf("backtest: costs: %w", err)
	}
	costs := costsFromQuote(items, size, time.Now().UTC())
	rep := strategy.Backtest(domain.InstrumentID(*id), candles, strategy.BacktestConfig{
		StartEquity: start, Costs: costs,
		SameSession: func(a, b time.Time) bool {
			ay, am, ad := a.In(loc).Date()
			by, bm, bd := b.In(loc).Date()
			return ay == by && am == bm && ad == bd
		},
	})
	_, err = fmt.Fprint(out, rep.String())
	return err
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
