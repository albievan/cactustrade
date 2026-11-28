package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// trader is what the trading loops need from the use cases (*app.Service implements it).
type trader interface {
	ReconcileAll(ctx context.Context) ([]app.Reconciliation, error)
	ResumeAgents(ctx context.Context) ([]app.ResumeOutcome, error)
	SuperviseLoop(ctx context.Context)
	AgentLoop(ctx context.Context)
	// SetAgentLoop says whether the agent loop runs: StartAgent is refused until it does.
	SetAgentLoop(running bool)
	ReleaseLeases(ctx context.Context) error
	// SyncTradesAll reads every eToro account's closed trades (read-only; the history loop).
	SyncTradesAll(ctx context.Context) ([]app.TradeSync, error)
	// RefreshT212InstrumentsAuto refreshes Trading 212's instrument list (read-only; the
	// catalogue loop, R45.10).
	RefreshT212InstrumentsAuto(ctx context.Context) (app.T212Catalogue, error)
}

// tradingSchedule holds the start-up timing of the trading side's periodic work.
type tradingSchedule struct {
	// HistoryFirst is when the first trade-history sync runs after the start, HistoryEvery
	// how often it runs after that (PLAN 6.10).
	HistoryFirst, HistoryEvery time.Duration
	// CatalogueFirst is when Trading 212's instrument list is first refreshed after the
	// start, CatalogueEvery how often after that (R45.10).
	CatalogueFirst, CatalogueEvery time.Duration
}

// prodSchedule is the production timing: the history first 2 minutes after the start (the
// start-up reconciliation has the eToro quota first), then every 15 minutes.
func prodSchedule() tradingSchedule {
	return tradingSchedule{HistoryFirst: 2 * time.Minute, HistoryEvery: 15 * time.Minute, CatalogueFirst: 30 * time.Second, CatalogueEvery: 12 * time.Hour}
}

var _ trader = (*app.Service)(nil)

const (
	// startupReconcileTimeout bounds the start-up reconciliation of all accounts: an eToro
	// that does not answer must not keep the supervisor (exits) from starting. Accounts it
	// did not reach stay unreconciled: no entries, and their agents come back paused (OD5).
	startupReconcileTimeout = 2 * time.Minute
	// releaseTimeout bounds giving the leases up at shutdown.
	releaseTimeout = 5 * time.Second
	// maxLeaseHolder is the width of agent_leases.holder.
	maxLeaseHolder = 64
)

// leaseHolderID names this process for the single-writer lease (D22): a random UUID, after
// the host name (cut short) so an operator can tell the holders apart. Never shared by two
// processes, also not by a restarted one.
func leaseHolderID(host string, r ports.Random) string {
	id := r.ID()
	if len(host) > maxLeaseHolder-len(id)-1 {
		host = host[:max(0, maxLeaseHolder-len(id)-1)]
	}
	if host == "" {
		return id
	}
	return host + "-" + id
}

// startTrading starts the trading side in the order PLAN 4.14 requires, in its own
// goroutine so the web server (and with it the kill switch) is up meanwhile:
//
//  1. ReconcileAll: every eToro account with keys is matched with eToro (bounded by
//     startupReconcileTimeout) before any loop can send anything;
//  2. ResumeAgents: agents that were running resume only after a clean reconciliation (OD5);
//  3. the supervisor loop (exits, every 10 s, one goroutine per account);
//  4. the agent loop (entries at each bar close + 5 s), only when step 2 worked: if the
//     resume could not decide, no agent ticks until the next start (fail closed); exits
//     still run. SetAgentLoop records which it was, so StartAgent is refused (with the
//     reason on the panel) until the loop runs (PLAN 6.13).
//
// Beside them the history loop (PLAN 6.10) syncs the trade history on sched. It only reads
// (closed trades, never an order), so it does not wait for the reconciliation.
//
// The returned stop ends the loops, waits for them, and then releases this process's
// leases so another process can take over at once. Positions are left as they are (OD6:
// they carry eToro-side stops and the next supervisor closes what is due).
func startTrading(parent context.Context, t trader, log *slog.Logger, sched tradingSchedule) (stop func()) {
	ctx, cancel := context.WithCancel(parent)
	var wg sync.WaitGroup
	wg.Go(func() { historyLoop(ctx, t, log, sched.HistoryFirst, sched.HistoryEvery) })
	wg.Go(func() { catalogueLoop(ctx, t, log, sched.CatalogueFirst, sched.CatalogueEvery) })
	wg.Go(func() {
		rctx, rcancel := context.WithTimeout(ctx, startupReconcileTimeout)
		rs, err := t.ReconcileAll(rctx)
		rcancel()
		if err != nil {
			log.ErrorContext(ctx, "start-up reconciliation failed: no account is reconciled", "err", err)
		}
		for _, r := range rs {
			if r.OK {
				log.InfoContext(ctx, "account reconciled", "account", r.AccountID)
			} else {
				log.WarnContext(ctx, "account not reconciled: no entries until it is", "account", r.AccountID, "reason", r.Reason)
			}
		}
		if ctx.Err() != nil {
			return
		}
		outs, rerr := t.ResumeAgents(ctx)
		if rerr != nil {
			t.SetAgentLoop(false) // StartAgent is refused and the panel says why (6.13)
			log.ErrorContext(ctx, "deciding which agents resume failed: the agent loop stays off until the next start", "err", rerr)
		}
		for _, o := range outs {
			log.InfoContext(ctx, "agent after restart", "account", o.AccountID, "state", string(o.State), "reason", o.Reason)
		}
		if ctx.Err() != nil {
			return
		}
		wg.Go(func() { t.SuperviseLoop(ctx) })
		if rerr == nil {
			t.SetAgentLoop(true)
			wg.Go(func() { t.AgentLoop(ctx) })
		}
	})
	return func() {
		cancel()
		wg.Wait()
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(parent), releaseTimeout)
		defer rcancel()
		if err := t.ReleaseLeases(rctx); err != nil {
			log.WarnContext(rctx, "releasing the account leases failed: another process takes over when they expire", "err", err)
		}
	}
}

// historyLoop syncs every eToro account's closed trades (demo and real, read-only: OD18's
// 90-day backfill on the first sync, then from the latest stored close) first after first,
// then every interval, until ctx ends. A failed account is logged by the sync and tried
// again next time.
func historyLoop(ctx context.Context, t trader, log *slog.Logger, first, every time.Duration) {
	tm := time.NewTimer(first)
	defer tm.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tm.C:
			rs, err := t.SyncTradesAll(ctx)
			if err != nil && ctx.Err() == nil {
				log.WarnContext(ctx, "trade history sync failed", "err", err)
			}
			for _, r := range rs {
				if r.Err == nil && r.Stored > 0 {
					log.InfoContext(ctx, "trade history synced", "account", r.AccountID, "stored", r.Stored, "ours", r.Ours)
				}
			}
			tm.Reset(every)
		}
	}
}

// catalogueLoop refreshes Trading 212's instrument list (read-only: one GET with a practice
// account's keys) first after first, then every interval, until ctx ends. Matches and entries
// need a list younger than 24 h; refreshing every 12 h keeps it fresh even when one attempt
// fails. Without a Trading 212 practice account there is nothing to do (logged at debug). A
// zero first or every (a schedule without it) runs nothing.
func catalogueLoop(ctx context.Context, t trader, log *slog.Logger, first, every time.Duration) {
	if first <= 0 || every <= 0 {
		return
	}
	tm := time.NewTimer(first)
	defer tm.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tm.C:
			c, err := t.RefreshT212InstrumentsAuto(ctx)
			switch {
			case err == nil:
				log.InfoContext(ctx, "Trading 212 instrument list refreshed", "instruments", c.Count)
			case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrBusy):
				log.DebugContext(ctx, "Trading 212 instrument list not refreshed", "err", err)
			case ctx.Err() == nil:
				log.WarnContext(ctx, "refreshing the Trading 212 instrument list failed", "err", err)
			}
			tm.Reset(every)
		}
	}
}
