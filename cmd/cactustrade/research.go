package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/albievan/cactustrade/internal/adapters/etoro"
	"github.com/albievan/cactustrade/internal/adapters/sqlstore"
	"github.com/albievan/cactustrade/internal/adapters/system"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// research is what a read-only console research command (backtest, etoro-probe) works with:
// the eToro client, the account whose keys it reads with, and the store (for the account's
// watchlist). Close it when done.
type research struct {
	client *etoro.Client
	acct   domain.Account
	store  *sqlstore.Store
}

// Close closes the store.
func (r *research) Close() error { return r.store.Close() }

// openResearch opens the store and the secrets as the server does and builds an eToro client
// for reading with one demo account's own keys: accountID, or (when empty) the account the
// price stream would use. record, when set, receives every read answer as received
// (etoro.Config.RecordAnswer). Logs go to stderr so stdout stays the command's output.
func openResearch(ctx context.Context, getenv func(string) string, accountID string, migrate bool, record func(etoro.Op, []byte)) (*research, error) {
	cfg, err := loadConfig(nil, getenv)
	if err != nil {
		return nil, err
	}
	log := newLoggerTo(os.Stderr, cfg.LogLevel)
	store, err := openStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*research, error) { return nil, errors.Join(err, store.Close()) }
	if migrate && cfg.MigrateOnStart {
		if err := store.Migrate(ctx); err != nil {
			return fail(err)
		}
	}
	secrets, _, err := openSecrets(ctx, cfg, store, getenv, system.Clock{}, log)
	if err != nil {
		return fail(err)
	}
	acct, err := researchAccount(ctx, store, secrets, accountID)
	if err != nil {
		return fail(err)
	}
	client, err := etoro.New(etoro.Config{
		HTTP: &http.Client{Timeout: 30 * time.Second}, Secrets: secrets, Random: system.Random{}, Log: log,
		RecordAnswer: record,
	})
	if err != nil {
		return fail(err)
	}
	return &research{client: client, acct: acct, store: store}, nil
}

// researchAccount is the account a research command reads with. Research only ever reads,
// and only with a demo eToro account's keys: a real-money account, a view-only platform
// account or an account without both keys is refused before any request (fail closed).
func researchAccount(ctx context.Context, store *sqlstore.Store, secrets ports.SecretStore, id string) (domain.Account, error) {
	if id == "" {
		a, ok := streamAccount(ctx, store, secrets)
		if !ok || a.Mode != domain.ModeDemo {
			return domain.Account{}, fmt.Errorf("no demo eToro account with both keys set: %w: %w", domain.ErrCredentialsMissing, domain.ErrUnavailable)
		}
		return a, nil
	}
	a, err := store.Accounts().Get(ctx, id)
	if err != nil {
		return domain.Account{}, fmt.Errorf("account %q: %w", id, err)
	}
	switch {
	case !a.Tradable():
		return domain.Account{}, fmt.Errorf("account %q is not an eToro account: %w", id, domain.ErrUnavailable)
	case a.Mode != domain.ModeDemo:
		return domain.Account{}, fmt.Errorf("account %q is not a demo account: research reads only with demo keys: %w", id, domain.ErrRealModeGated)
	}
	ak, uk := domain.AccountKeyNames(a)
	for _, k := range []domain.CredentialName{ak, uk} {
		if v, err := secrets.Get(ctx, k); err != nil || v == "" {
			return domain.Account{}, fmt.Errorf("account %q does not have both eToro keys set: %w: %w", id, domain.ErrCredentialsMissing, domain.ErrUnavailable)
		}
	}
	return a, nil
}
