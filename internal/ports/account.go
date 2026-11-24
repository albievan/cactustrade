package ports

import (
	"context"
	"log/slog"
	"sync"

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

type noteKey struct{}

// accountNote holds the first account a request resolved (NoteAccount), for its log lines.
type accountNote struct {
	mu sync.Mutex
	a  domain.Account
	ok bool
}

// WithAccountNote gives ctx a place for NoteAccount to record the account an inbound request
// is about (the HTTP adapter adds one per request). It never changes what adapters may do:
// only WithAccount does.
func WithAccountNote(ctx context.Context) context.Context {
	return context.WithValue(ctx, noteKey{}, &accountNote{})
}

// NoteAccount records a as the account the request in ctx is about, for its log lines
// (PLAN 7.2). The first account noted wins; without WithAccountNote it does nothing.
func NoteAccount(ctx context.Context, a domain.Account) {
	n, ok := ctx.Value(noteKey{}).(*accountNote)
	if !ok {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if !n.ok {
		n.a, n.ok = a, true
	}
}

// AccountLogAttrs are the log attributes naming the account ctx is for: account_id and mode
// (PLAN 7.2). WithAccount wins over NoteAccount; none: no attributes. Only the id and the mode
// are ever logged (an account holds no secret, but its keys are never reachable from here).
func AccountLogAttrs(ctx context.Context) []slog.Attr {
	a, ok := AccountFrom(ctx)
	if !ok {
		n, has := ctx.Value(noteKey{}).(*accountNote)
		if !has {
			return nil
		}
		n.mu.Lock()
		a, ok = n.a, n.ok
		n.mu.Unlock()
		if !ok {
			return nil
		}
	}
	out := []slog.Attr{slog.String("account_id", a.ID)}
	if a.Mode != "" {
		out = append(out, slog.String("mode", string(a.Mode)))
	}
	return out
}
