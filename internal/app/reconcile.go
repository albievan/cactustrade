package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// ReconcileEvery is how often the supervisor reconciles an account (PLAN 4.8).
const ReconcileEvery = 5 * time.Minute

// fillVisibleAfter is how long a recorded fill may be missing from eToro's portfolio before
// reconciliation takes it for closed (U11, unverified until 4.17). The agent keeps a fill's
// cash reserved as long (domain.Reservation.WithUnseenFills, review B1).
const fillVisibleAfter = domain.FillVisibleAfter

// Reconciliation is the outcome of an account's latest reconciliation with eToro. Entries
// need OK; after a restart an agent that was running resumes by itself only when it is OK
// (OD5).
type Reconciliation struct {
	AccountID string
	// OK: the portfolio was read, every order of ours was resolved or is legitimately in
	// flight, and no position cactusTrade did not open is non-conforming.
	OK bool
	// At is when it ran (zero = never in this process).
	At time.Time
	// Reason says why it is not OK ("" when OK), or what was done.
	Reason string
}

// Reconciled returns the account's latest reconciliation in this process. Never reconciled
// is not OK (fail closed).
func (s *Service) Reconciled(accountID string) Reconciliation {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	if r, ok := s.rt.reconciled[accountID]; ok {
		return r
	}
	return Reconciliation{AccountID: accountID, Reason: "not reconciled yet"}
}

// setReconciled stores the result and raises a warning event and alert when the account
// becomes unreconciled or the reason changes (not on every pass).
func (s *Service) setReconciled(ctx context.Context, a domain.Account, ok bool, reason string) Reconciliation {
	r := Reconciliation{AccountID: a.ID, OK: ok, At: s.d.Clock.Now().UTC(), Reason: reason}
	s.rt.mu.Lock()
	prev, seen := s.rt.reconciled[a.ID]
	s.rt.reconciled[a.ID] = r
	s.rt.mu.Unlock()
	switch {
	case !ok && (!seen || prev.OK || prev.Reason != reason):
		s.emitFor(ctx, a.ID, "agent", "reconcile.failed", domain.SeverityWarning, "Reconciliation of "+a.Name+" failed: no new entries", reason)
	case ok && seen && !prev.OK:
		s.emitFor(ctx, a.ID, "agent", "reconcile.ok", domain.SeverityInfo, a.Name+" is reconciled again", "")
	}
	return r
}

// unreconcile marks an account not reconciled (no new entries until the next clean
// reconciliation), without an alert: it is the expected result of an owner's action, such as
// new keys (PLAN 7.1.3).
func (s *Service) unreconcile(accountID, reason string) {
	s.rt.mu.Lock()
	defer s.rt.mu.Unlock()
	s.rt.reconciled[accountID] = Reconciliation{AccountID: accountID, At: s.d.Clock.Now().UTC(), Reason: reason}
}

// ReconcileAll reconciles every eToro account that has keys, at start-up before any
// supervisor or agent loop, then every ReconcileEvery from the supervisor. Real-mode accounts
// are only read: their orders and positions are matched, nothing is sent. Accounts
// that fail are returned not OK; the error is only for a store failure listing them.
func (s *Service) ReconcileAll(ctx context.Context) ([]Reconciliation, error) {
	as, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("app.ReconcileAll: %w", err)
	}
	var out []Reconciliation
	for _, a := range as {
		if !a.Tradable() || !s.hasKeys(ctx, a) {
			continue
		}
		r, err := s.Reconcile(ctx, a.ID)
		if err != nil {
			s.d.Log.WarnContext(ports.WithAccount(ctx, a), "reconciliation failed", "account", a.ID, "err", err)
			r = s.Reconciled(a.ID)
		}
		out = append(out, r)
	}
	return out, nil
}

// hasKeys reports whether an account's eToro key pair is set (a read error counts as no).
func (s *Service) hasKeys(ctx context.Context, a domain.Account) bool {
	st, err := s.d.Secrets.Status(ctx)
	if err != nil {
		s.d.Log.WarnContext(ctx, "reading the key status failed", "account", a.ID, "err", err)
		return false
	}
	ak, uk := domain.AccountKeyNames(a)
	_, aok := st[ak]
	_, uok := st[uk]
	return aok && uok
}

// Reconcile matches one eToro account with eToro (PLAN 4.8): every order of ours that is not
// final is resolved by its request id (adopted when eToro has it; abandoned when eToro still
// does not know it lookupGiveUp after it was sent), then the agent positions are matched with
// a fresh portfolio: ours that are gone are closed locally. Positions cactusTrade did not
// open are never touched (OD1); a non-conforming one keeps the account unreconciled (entries
// blocked) and raises an alert. Any broker or store error leaves it unreconciled and is also
// returned; a mismatch is a not-OK result without an error.
func (s *Service) Reconcile(ctx context.Context, accountID string) (Reconciliation, error) {
	a, err := s.d.Store.Accounts().Get(ctx, accountID)
	if err != nil {
		return Reconciliation{}, fmt.Errorf("app.Reconcile: %w", err)
	}
	if !a.Tradable() {
		return Reconciliation{}, fmt.Errorf("app.Reconcile: %s: %w", a.Name, domain.ErrViewOnly)
	}
	ctx = ports.WithAccount(ctx, a) // log lines name the account and mode (PLAN 7.2)
	if s.d.Broker == nil {
		return s.setReconciled(ctx, a, false, "the eToro client is not configured"), nil
	}
	bctx := ports.WithAccount(ctx, a)
	pf, err := s.d.Broker.Portfolio(bctx)
	if err != nil {
		return s.setReconciled(ctx, a, false, "reading the portfolio failed: "+err.Error()), fmt.Errorf("app.Reconcile: %w", err)
	}
	orders, err := s.d.Store.Orders().ListOpen(ctx, a.ID)
	if err != nil {
		return s.setReconciled(ctx, a, false, "reading our orders failed: "+err.Error()), fmt.Errorf("app.Reconcile: %w", err)
	}
	var problems []string
	for _, o := range orders {
		if p := s.settleOrder(ctx, a, o, pf, orders); p != "" {
			problems = append(problems, p)
		}
	}
	// Adopted fills may have opened positions after the first read: match with a fresh one.
	if pf, err = s.d.Broker.Portfolio(bctx); err != nil {
		return s.setReconciled(ctx, a, false, "reading the portfolio failed: "+err.Error()), fmt.Errorf("app.Reconcile: %w", err)
	}
	problems = append(problems, s.matchPositions(ctx, a, pf)...)
	if len(problems) > 0 {
		return s.setReconciled(ctx, a, false, strings.Join(problems, "; ")), nil
	}
	return s.setReconciled(ctx, a, true, ""), nil
}

// settleOrder resolves one order that is not final, with one broker read, and returns a
// problem ("" = resolved or legitimately in flight). open lists the account's open orders
// (to find a cancel's target).
func (s *Service) settleOrder(ctx context.Context, a domain.Account, o domain.Order, pf domain.Portfolio, open []domain.Order) string {
	now := s.d.Clock.Now()
	sent := o.SentAt
	if sent.IsZero() {
		sent = o.CreatedAt
	}
	stale := now.Sub(sent) >= lookupGiveUp
	bctx := ports.WithAccount(ctx, a)
	switch o.Kind {
	case domain.OrderOpen:
		st, err := s.d.Broker.Lookup(bctx, o.RequestID)
		switch {
		case err == nil:
			if _, err := s.applyOpenStatus(ctx, a, o, st); err != nil {
				return fmt.Sprintf("order %s: %v", o.RequestID, err)
			}
		case errors.Is(err, domain.ErrNotFound) && stale:
			if _, err := s.abandon(ctx, a, o, fmt.Sprintf("eToro does not know it %s after it was sent", lookupGiveUp)); err != nil {
				return fmt.Sprintf("order %s: %v", o.RequestID, err)
			}
		case errors.Is(err, domain.ErrNotFound):
			// In flight (U2: a new order may take a moment to show): its instrument stays
			// blocked and its cash reserved until it resolves.
		default:
			return fmt.Sprintf("looking up order %s failed: %v", o.RequestID, err)
		}
	case domain.OrderClose:
		return s.settleClose(ctx, a, o, pf, stale)
	case domain.OrderCancel:
		for _, t := range open {
			if t.Kind == domain.OrderOpen && t.EtoroOrderID == o.TargetOrderID && !t.State.Final() {
				if stale {
					if _, err := s.abandon(ctx, a, o, "cancel not confirmed: the target is still open"); err != nil {
						return fmt.Sprintf("order %s: %v", o.RequestID, err)
					}
				}
				return ""
			}
		}
		if _, err := s.move(ctx, o, domain.OrderFilled, "nothing left to cancel: the target order is final", nil); err != nil {
			return fmt.Sprintf("order %s: %v", o.RequestID, err)
		}
	}
	return ""
}

// settleClose resolves a close order: by its eToro order id when known, else by the
// portfolio (eToro has no lookup of closes by request id, U3).
func (s *Service) settleClose(ctx context.Context, a domain.Account, o domain.Order, pf domain.Portfolio, stale bool) string {
	ap, getErr := s.d.Store.AgentPositions().Get(ctx, a.ID, o.PositionID)
	if getErr != nil {
		ap = domain.AgentPosition{AccountID: a.ID, PositionID: o.PositionID, Instrument: o.Instrument, Direction: o.Direction}
	}
	if o.EtoroOrderID > 0 {
		st, err := s.d.Broker.CloseOrder(ports.WithAccount(ctx, a), o.EtoroOrderID)
		if err != nil {
			return fmt.Sprintf("reading close order %s failed: %v", o.RequestID, err)
		}
		if _, err := s.applyCloseStatus(ctx, a, ap, o, st); err != nil {
			return fmt.Sprintf("close order %s: %v", o.RequestID, err)
		}
		return ""
	}
	// A position recorded within fillVisibleAfter of the read may just not show yet (U11): its
	// absence does not prove the close.
	open := getErr == nil && pf.AsOf.Sub(ap.OpenedAt) < fillVisibleAfter
	for _, p := range pf.Positions {
		open = open || p.ID == o.PositionID
	}
	if open {
		if stale {
			if _, err := s.abandon(ctx, a, o, "close not confirmed and the position is still open: the supervisor closes it again"); err != nil {
				return fmt.Sprintf("order %s: %v", o.RequestID, err)
			}
		}
		return ""
	}
	if _, err := s.move(ctx, o, domain.OrderFilled, "the position is gone from the portfolio", nil); err != nil {
		return fmt.Sprintf("order %s: %v", o.RequestID, err)
	}
	if ap.Open() && getErr == nil {
		s.markClosed(ctx, a, ap, "gone from the portfolio")
	}
	return ""
}

// matchPositions matches the agent positions with the portfolio and returns problems.
func (s *Service) matchPositions(ctx context.Context, a domain.Account, pf domain.Portfolio) []string {
	ours, err := s.d.Store.AgentPositions().ListOpen(ctx, a.ID)
	if err != nil {
		return []string{"reading the agent positions failed: " + err.Error()}
	}
	inPF := map[int64]bool{}
	for _, p := range pf.Positions {
		inPF[p.ID] = true
	}
	var (
		problems   []string
		unrecorded []domain.Position
	)
	for _, ap := range ours {
		// A fill may take a moment to show in the portfolio (U11): a position recorded
		// within fillVisibleAfter of the read is not taken for closed.
		if !inPF[ap.PositionID] && pf.AsOf.Sub(ap.OpenedAt) >= fillVisibleAfter {
			s.markClosed(ctx, a, ap, "closed at eToro (stop-loss, take-profit or by hand)")
		}
	}
	for _, p := range pf.Positions {
		ap, err := s.d.Store.AgentPositions().Get(ctx, a.ID, p.ID)
		switch {
		case err == nil && !ap.Open():
			// Recorded as closed but still open at eToro (e.g. a lost close answer): ours
			// again, so the supervisor closes it.
			ap.ClosedAt = time.Time{}
			if err := s.d.Store.AgentPositions().Update(ctx, ap); err != nil {
				problems = append(problems, fmt.Sprintf("reopening position %d failed: %v", p.ID, err))
			}
		case err == nil:
		case errors.Is(err, domain.ErrNotFound):
			unrecorded = append(unrecorded, p)
		default:
			problems = append(problems, fmt.Sprintf("reading position %d failed: %v", p.ID, err))
		}
	}
	// Positions no one recorded: ours when one of our recent orders explains them (review
	// B2), otherwise the owner's (OD1).
	adopted, aerr := s.adopt(ctx, a, unrecorded)
	if aerr != "" {
		problems = append(problems, aerr)
	}
	for _, p := range unrecorded {
		if !adopted[p.ID] && !p.Conforms() {
			// OD1: never closed by cactusTrade; blocks entries until the owner acts.
			problems = append(problems, fmt.Sprintf("position %d (not opened by cactusTrade) is not a real share long or a CFD short at leverage 1: close it on eToro", p.ID))
		}
	}
	return problems
}

// adopt records as ours the unrecorded positions one of our recent open orders explains
// (domain.AdoptUnrecorded): a fill whose recording failed, or an order abandoned after its
// lookups kept answering 404 although eToro holds it (U2). Each adoption raises a warning
// event; from then on the supervisor closes the position like any of ours. It returns the
// adopted position ids and a problem ("" = none).
func (s *Service) adopt(ctx context.Context, a domain.Account, unrecorded []domain.Position) (map[int64]bool, string) {
	adopted := map[int64]bool{}
	if len(unrecorded) == 0 {
		return adopted, ""
	}
	recent, err := s.d.Store.Orders().List(ctx, a.ID, recentOrders)
	if err != nil {
		return adopted, "reading our recent orders failed: " + err.Error()
	}
	now := s.d.Clock.Now().UTC()
	var problems []string
	for _, ad := range domain.AdoptUnrecorded(recent, unrecorded, now) {
		p, o := ad.Position, ad.Order
		ap := domain.AgentPosition{
			AccountID: a.ID, PositionID: p.ID, OpenRequestID: o.RequestID,
			Instrument: p.Instrument, Direction: o.Direction, OpenedAt: now,
		}
		if err := s.d.Store.AgentPositions().Add(ctx, ap); err != nil && !errors.Is(err, domain.ErrConflict) {
			problems = append(problems, fmt.Sprintf("adopting position %d failed: %v", p.ID, err))
			continue
		}
		adopted[p.ID] = true
		s.orderEvent(ctx, a, "position.adopted", domain.SeverityWarning,
			"Position in "+s.instrumentLabel(ctx, p.Instrument)+" adopted as cactusTrade's",
			fmt.Sprintf("eToro holds position %d ($%s) that order %s (%s) opened but that was never recorded; it is now closed like any position of cactusTrade", p.ID, p.Amount, o.RequestID, o.State))
	}
	return adopted, strings.Join(problems, "; ")
}
