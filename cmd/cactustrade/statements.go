package main

import (
	"context"
	"log/slog"
	"time"
)

// statementSender is the use case the statements loop runs (app.Service).
type statementSender interface {
	SendDueStatements(ctx context.Context) (int, error)
}

// statementsLoop sends the trading statements whose period has closed (R30): once first
// after (the start-up settles), then every interval. Sending is idempotent per schedule and
// period, so a restart or a second process never sends a period twice.
func statementsLoop(ctx context.Context, svc statementSender, log *slog.Logger, first, every time.Duration) {
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := svc.SendDueStatements(ctx); err != nil && ctx.Err() == nil {
				log.WarnContext(ctx, "trading statements failed", "err", err)
			} else if n > 0 {
				log.InfoContext(ctx, "trading statements queued", "emails", n)
			}
			t.Reset(every)
		}
	}
}
