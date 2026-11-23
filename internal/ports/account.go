package ports

import (
	"context"

	"github.com/albievan/cactustrade/internal/domain"
)

type accountKey struct{}

// WithAccount tells adapters (the eToro client) which account a call is for: its mode and
// key pair. The app sets it after checking the caller's access.
func WithAccount(ctx context.Context, a domain.Account) context.Context {
	return context.WithValue(ctx, accountKey{}, a)
}

// AccountFrom returns the account set by WithAccount. Without one, adapters refuse to act.
func AccountFrom(ctx context.Context) (domain.Account, bool) {
	a, ok := ctx.Value(accountKey{}).(domain.Account)
	return a, ok
}
