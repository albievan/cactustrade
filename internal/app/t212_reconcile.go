package app

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// t212Reconcile matches a Trading 212 practice account with Trading 212 (PLAN R45.7): every
// order of ours that is not final is resolved (created and never sent → abandoned; sent or
// unknown → matched, T212-d; with an id → read); our positions are matched with the account's
// (one it does not list stays open and the account unreconciled, re-review 2026-11-28); holdings cactusTrade did not buy are left alone (OD1);
// a stop of ours still active at Trading 212 for a position that is closed, or for an order we
// gave up, is cancelled (an orphan would sell the owner's shares). Any order left unresolved,
// any position of ours without a confirmed stop and any read error leave the account
// unreconciled (no entries; exits still run).
func (s *Service) t212Reconcile(ctx context.Context, a domain.Account) (Reconciliation, error) {
	if s.d.T212 == nil {
		return s.setReconciled(ctx, a, false, "the Trading 212 client is not configured"), nil
	}
	bctx := ports.WithAccount(ctx, a)
	if _, err := s.d.T212.Account(bctx); err != nil {
		return s.setReconciled(ctx, a, false, "reading the Trading 212 account failed: "+err.Error()), fmt.Errorf("app.Reconcile: %w", err)
	}
	orders, err := s.d.Store.Orders().ListOpen(ctx, a.ID)
	if err != nil {
		return s.setReconciled(ctx, a, false, "reading our orders failed: "+err.Error()), fmt.Errorf("app.Reconcile: %w", err)
	}
	var problems []string
	for _, o := range orders {
		if o.OnPlatform() != domain.PlatformTrading212 {
			continue
		}
		switch {
		case o.Kind == domain.OrderCancel:
			if o.State == domain.OrderCreated || s.d.Clock.Now().Sub(o.UpdatedAt) >= lookupGiveUp {
				if _, err := s.abandon(ctx, a, o, "cancel not confirmed"); err != nil {
					problems = append(problems, fmt.Sprintf("order %s: %v", o.RequestID, err))
				}
			}
		case o.EtoroOrderID <= 0:
			if _, p := s.resolveT212(ctx, a, o); p != "" {
				problems = append(problems, p)
			}
		default:
			switch _, listed, err := s.pollT212Listed(ctx, a, o); {
			case err != nil:
				problems = append(problems, fmt.Sprintf("reading order %s failed: %v", o.RequestID, err))
			case !listed:
				// Never taken for gone (re-review 2026-11-28): the account waits until Trading 212
				// lists it, or the owner settles its position.
				problems = append(problems, fmt.Sprintf("order %s (Trading 212 order %d) is not listed by Trading 212", o.RequestID, o.EtoroOrderID))
			}
		}
	}
	// Fresh figures after the orders were resolved (a fill may have been recorded).
	acct, err := s.d.T212.Account(bctx)
	if err != nil {
		return s.setReconciled(ctx, a, false, "reading the Trading 212 account failed: "+err.Error()), fmt.Errorf("app.Reconcile: %w", err)
	}
	open, err := s.d.T212.OpenOrders(bctx)
	if err != nil {
		return s.setReconciled(ctx, a, false, "reading the open orders failed: "+err.Error()), fmt.Errorf("app.Reconcile: %w", err)
	}
	problems = append(problems, s.t212MatchPositions(ctx, a, acct, open)...)
	problems = append(problems, s.t212CancelOrphanStops(ctx, a, open)...)
	if len(problems) > 0 {
		return s.setReconciled(ctx, a, false, strings.Join(problems, "; ")), nil
	}
	return s.setReconciled(ctx, a, true, ""), nil
}

// t212MatchPositions reports our positions the account does not list (never settled as gone:
// t212Vanished) and the ones without a confirmed stop.
func (s *Service) t212MatchPositions(ctx context.Context, a domain.Account, acct domain.T212Account, open []domain.BrokerOrder) []string {
	ours, err := s.d.Store.AgentPositions().ListOpen(ctx, a.ID)
	if err != nil {
		return []string{"reading the agent positions failed: " + err.Error()}
	}
	var problems []string
	for _, ap := range ours {
		if _, held := acct.Position(ap.Ticker); !held {
			if acct.AsOf.Sub(ap.OpenedAt) >= fillVisibleAfter {
				s.t212Vanished(ctx, a, ap)
				if cur, err := s.d.Store.AgentPositions().Get(ctx, a.ID, ap.PositionID); err != nil || cur.Open() {
					problems = append(problems, fmt.Sprintf("the position in %s is not listed by Trading 212: the owner must check it", ap.Ticker))
				}
			}
			continue
		}
		s.t212Present(t212PositionKey(a.ID, ap.PositionID))
		stops, err := s.t212OurStops(ctx, a.ID, ap.PositionID)
		if err != nil {
			problems = append(problems, "reading our stops failed: "+err.Error())
			continue
		}
		if !slices.ContainsFunc(stops, func(st domain.Order) bool {
			return st.State == domain.OrderPending && slices.ContainsFunc(open, func(b domain.BrokerOrder) bool { return b.ID == st.EtoroOrderID })
		}) {
			problems = append(problems, fmt.Sprintf("the position in %s has no confirmed stop at Trading 212", ap.Ticker))
		}
	}
	return problems
}

// t212CancelOrphanStops cancels the stops of ours Trading 212 still lists although their
// position is closed or the stop order is final here (an orphan, e.g. a stop whose lost answer
// was matched only after its position was sold).
func (s *Service) t212CancelOrphanStops(ctx context.Context, a domain.Account, open []domain.BrokerOrder) []string {
	recent, err := s.d.Store.Orders().List(ctx, a.ID, recentOrders)
	if err != nil {
		return []string{"reading our recent orders failed: " + err.Error()}
	}
	var problems []string
	for _, b := range open {
		i := slices.IndexFunc(recent, func(o domain.Order) bool { return o.Kind == domain.OrderStop && o.EtoroOrderID == b.ID })
		if i < 0 {
			continue // not ours (OD1)
		}
		st := recent[i]
		ap, err := s.d.Store.AgentPositions().Get(ctx, a.ID, st.PositionID)
		if err == nil && ap.Open() && !st.State.Final() {
			continue // it protects an open position
		}
		if _, err := s.cancelT212(ctx, a, st, "orphan stop: its position is closed"); err != nil {
			problems = append(problems, fmt.Sprintf("cancelling the orphan stop %d failed: %v", b.ID, err))
			continue
		}
		s.orderEvent(ctx, a, domain.Long, "order.orphan_cancelled", domain.SeverityWarning, "Orphan stop of "+s.t212Label(ctx, st.Instrument, st.Ticker)+" cancelled",
			fmt.Sprintf("Trading 212 order %d protected a position that is closed. Request %s", b.ID, st.RequestID))
	}
	return problems
}
