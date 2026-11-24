package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/adapters/etoro"
	"github.com/albievan/cactustrade/internal/adapters/system"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// etoroLookupCmd prints how cactusTrade reads eToro's orders:lookup answer for one of our
// orders, and the raw answer (read only, an allowlisted GET with the account's own keys):
// `cactustrade etoro-lookup --account ID --order-id N` (or `--request-id UUID`; eToro's demo
// answered 404 by request id on 2026-11-24). It goes through the same Lookup as the executor.
// For diagnosing how eToro reports an order (PLAN 4.17, U-list).
func etoroLookupCmd(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("etoro-lookup", flag.ContinueOnError)
	fs.SetOutput(out)
	acct := fs.String("account", "", "account id (required)")
	req := fs.String("request-id", "", "the order's request id")
	oid := fs.Int64("order-id", 0, "eToro's order id (instead of the request id)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *acct == "" || (*req == "" && *oid <= 0) {
		return errors.New("etoro-lookup: --account and --request-id or --order-id are required")
	}
	cfg, err := loadConfig(nil, getenv)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	secrets, _, err := openSecrets(ctx, cfg, store, getenv, system.Clock{}, log)
	if err != nil {
		return err
	}
	a, err := store.Accounts().Get(ctx, *acct)
	if err != nil {
		return err
	}
	c, err := etoro.New(etoro.Config{HTTP: &http.Client{Timeout: 30 * time.Second}, Secrets: secrets, Random: system.Random{}, Log: log})
	if err != nil {
		return err
	}
	st, raw, err := c.LookupWithRaw(ports.WithAccount(ctx, a), *req, *oid)
	if err != nil {
		return err
	}
	return printLookup(out, st, raw)
}

// printLookup prints the order status as cactusTrade reads it (the same parsing the executor
// uses), then eToro's answer exactly as received.
func printLookup(out io.Writer, st domain.OrderStatus, raw []byte) error {
	var b strings.Builder
	fmt.Fprintf(&b, "parsed: order %d, state %s (final %v), reason %q\n", st.OrderID, st.State, st.Final, st.Reason)
	fmt.Fprintf(&b, "        instrument %d, settlement %q, leverage %d, side %q\n", st.Instrument, st.Settlement, st.Leverage, st.Side)
	for _, e := range st.Executions {
		fmt.Fprintf(&b, "        position %d: units %s, amount $%s, open %s, stop %s, closed %v\n",
			e.PositionID, e.Units, e.Amount, e.OpenRate, e.StopLossRate, e.Closed)
	}
	fmt.Fprintf(&b, "raw: %s\n", raw)
	_, err := io.WriteString(out, b.String())
	return err
}
