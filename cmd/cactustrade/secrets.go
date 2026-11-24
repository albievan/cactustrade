package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"time"

	"github.com/albievan/cactustrade/internal/adapters/secretstore"
	"github.com/albievan/cactustrade/internal/adapters/system"
	"github.com/albievan/cactustrade/internal/adapters/vault"
	"github.com/albievan/cactustrade/internal/domain"
)

// secretsCmd is `cactustrade secrets migrate --to vault [--dry-run]` (PLAN 7.8): it copies
// every credential kept in the database (decrypted with the master key) into Vault, reads
// each one back to check it, and prints the names only (never a value). The database copies
// stay (switch with CACTUSTRADE_SECRETS=vault, then delete them in the web UI or keep them
// as a fallback). Credentials from the environment are not in the database and not copied.
func secretsCmd(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	const usage = "secrets migrate --to vault [--dry-run]"
	if len(args) == 0 || args[0] != "migrate" {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("secrets migrate", flag.ContinueOnError)
	to := fs.String("to", "", "where to copy the credentials: vault")
	dry := fs.Bool("dry-run", false, "list what would be copied, change nothing")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *to != secretsVault || fs.NArg() > 0 {
		return errors.New(usage)
	}
	cfg, err := loadConfig(nil, func(k string) string {
		if k == "CACTUSTRADE_SECRETS" {
			return secretsVault // the Vault settings are needed whatever the server uses now
		}
		return getenv(k)
	})
	if err != nil {
		return err
	}
	// Never generate a master key here: a new one could not read what is stored.
	if cfg.MasterKey == "" && cfg.MasterKeyFile == "" {
		if f := filepath.Join(cfg.DataDir, "master.key"); fileExists(f) {
			cfg.MasterKeyFile = f
		}
	}
	key, _, err := secretstore.LoadMasterKey(cfg.MasterKey, cfg.MasterKeyFile, "")
	if err != nil {
		return err
	}
	cipher, err := secretstore.NewCipher(key)
	if err != nil {
		return err
	}
	store, err := openStore(ctx, cfg)
	if err != nil {
		return err
	}
	defer store.Close()
	db := secretstore.NewDB(store.Secrets(), cipher, system.Clock{})
	vc := cfg.Vault
	vc.Log = slog.New(slog.DiscardHandler)
	vs, err := vault.New(vc)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := vs.Check(cctx); err != nil {
		return fmt.Errorf("secrets migrate: %w", err)
	}
	st, err := db.Status(cctx)
	if err != nil {
		return err
	}
	names := make([]domain.CredentialName, 0, len(st))
	for n := range st {
		names = append(names, n)
	}
	slices.Sort(names)
	verb := "copied"
	if *dry {
		verb = "would copy"
	}
	for _, n := range names {
		v, err := db.Get(cctx, n)
		if err != nil {
			return fmt.Errorf("secrets migrate: reading %s from the database: %w", n, err)
		}
		if !*dry {
			if err := vs.Put(cctx, n, v); err != nil {
				return fmt.Errorf("secrets migrate: writing %s to Vault: %w", n, err)
			}
			back, err := vs.Get(cctx, n)
			if err != nil {
				return fmt.Errorf("secrets migrate: reading %s back from Vault: %w", n, err)
			}
			if back != v {
				return fmt.Errorf("secrets migrate: %s reads back differently from Vault: stopped here", n)
			}
		}
		if _, err := fmt.Fprintf(out, "%s %s\n", verb, n); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(out, "%s %d credential(s) to Vault %s (mount %s, folder %s); the database copies are unchanged.\n",
		verb, len(names), vc.Addr, orDefault(vc.Mount, vault.DefaultMount), orDefault(vc.Prefix, vault.DefaultPrefix))
	return err
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
