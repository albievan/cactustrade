// Package logctx wraps a slog.Handler so that every record logged with a context names the
// account the context is for: account_id and mode (demo / real), PLAN 7.2. A real-money line
// can therefore never be mistaken for a demo one.
package logctx

import (
	"context"
	"log/slog"

	"github.com/albievan/cactustrade/internal/ports"
)

// Handler adds ports.AccountLogAttrs(ctx) to each record before passing it on.
type Handler struct {
	next slog.Handler
}

var _ slog.Handler = Handler{}

// New wraps next.
func New(next slog.Handler) Handler { return Handler{next: next} }

// Enabled reports whether next handles records at level.
func (h Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle adds the account's id and mode (when ctx names an account) and passes r on. An
// attribute the call already set under the same key is kept and not repeated.
func (h Handler) Handle(ctx context.Context, r slog.Record) error {
	if ctx != nil {
		if as := ports.AccountLogAttrs(ctx); len(as) > 0 {
			have := map[string]bool{}
			r.Attrs(func(a slog.Attr) bool {
				have[a.Key] = true
				return true
			})
			r = r.Clone()
			for _, a := range as {
				if !have[a.Key] {
					r.AddAttrs(a)
				}
			}
		}
	}
	return h.next.Handle(ctx, r)
}

// WithAttrs returns a Handler whose next handler has attrs.
func (h Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return Handler{next: h.next.WithAttrs(attrs)}
}

// WithGroup returns a Handler whose next handler opens group name.
func (h Handler) WithGroup(name string) slog.Handler {
	return Handler{next: h.next.WithGroup(name)}
}
