package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/albievan/cactustrade/internal/adapters/etoro"
	"github.com/albievan/cactustrade/internal/adapters/system"
	"github.com/albievan/cactustrade/internal/ports"
)

// etoroLookupCmd prints eToro's raw orders:lookup answer for one of our orders (read only,
// an allowlisted GET with the account's own keys): `cactustrade etoro-lookup --account ID
// --request-id UUID`. For diagnosing how eToro reports an order (PLAN 4.17, U-list).
func etoroLookupCmd(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("etoro-lookup", flag.ContinueOnError)
	fs.SetOutput(out)
	acct := fs.String("account", "", "account id (required)")
	req := fs.String("request-id", "", "the order's request id (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *acct == "" || *req == "" {
		return errors.New("etoro-lookup: --account and --request-id are required")
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
	raw, err := c.LookupRaw(ports.WithAccount(ctx, a), *req)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(raw))
	return err
}
