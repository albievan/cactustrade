package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// The Trading 212 supervisor (PLAN R45.7): every SupervisorInterval it closes what must be
// closed on a Trading 212 practice account, whatever the agent is doing. It needs no LLM, no
// eToro and no price source: the calendar and Trading 212 only. When the account cannot be
// read it works from the recorded agent positions (the session rule only).

// t212Supervise is Supervise on a Trading 212 practice account: take the lease; read the
// account (else use our recorded positions); fold the equity into the risk state and trip the
// kill switch on a drawdown; follow our orders (stale buys cancelled, unknown ones matched);
// sell the positions due (all of them when killed); check every position still has its stop
// (missing → sold, T212-h; gone → settled, a fired stop announced); reconcile every
// ReconcileEvery. Only cactusTrade's own positions are ever sold (OD1).
func (s *Service) t212Supervise(ctx context.Context, a domain.Account) (SupervisorReport, error) {
	rep := SupervisorReport{AccountID: a.ID}
	switch {
	case !a.T212Trading():
		rep.Skipped = "Trading 212 trades practice accounts only"
	case s.d.T212 == nil:
		rep.Skipped = "the Trading 212 client is not configured"
	}
	if rep.Skipped != "" {
		return rep, nil
	}
	if rep.Skipped = s.leaseProblem(ctx, a.ID); rep.Skipped != "" {
		return rep, nil
	}
	now := s.d.Clock.Now()
	l := s.riskLimitsFor(ctx, a)
	if k, err := s.isKilled(ctx, a.ID); err != nil {
		rep.fail("reading the kill switch: %v", err)
	} else {
		rep.Killed = k
	}
	acct, aerr := s.d.T212.Account(ports.WithAccount(ctx, a))
	if aerr != nil {
		rep.LocalPositions = true
		rep.fail("reading the Trading 212 account: %v", aerr)
	} else if pf, err := s.t212Portfolio(ctx, a, acct); err != nil {
		rep.fail("reading our positions: %v", err)
	} else {
		st, err := s.observe(ctx, a, pf, l, now)
		switch {
		case err != nil:
			rep.fail("storing the risk state: %v", err)
		case !rep.Killed && domain.DrawdownBreached(pf.Equity(), st.State, l):
			why := fmt.Sprintf("drawdown limit: the account equity %s (every position, also those not opened by cactusTrade) is %s below the high-water mark %s (limit %d bp)",
				domain.FormatMoney(acct.Currency, pf.Equity()), domain.FormatMoney(acct.Currency, st.State.HighWaterMark-pf.Equity()),
				domain.FormatMoney(acct.Currency, st.State.HighWaterMark), l.Drawdown)
			if err := s.trip(ctx, a, why); err != nil {
				rep.fail("persisting the kill: %v", err)
			}
			rep.Killed, rep.Tripped = true, true
		}
	}
	s.superviseT212Orders(ctx, a, &rep)
	ours, err := s.d.Store.AgentPositions().ListOpen(ctx, a.ID)
	if err != nil {
		return rep, fmt.Errorf("app.Supervise: %w", err)
	}
	if rep.Killed {
		for _, ap := range ours {
			rep.Exits = append(rep.Exits, domain.Exit{PositionID: ap.PositionID, Instrument: ap.Instrument, Reason: domain.ExitKilled, Detail: "the kill switch is on"})
		}
	} else {
		// The session rule (flatten before the close, no unknown session). The stop rule is
		// Trading 212's own: checked below against its open orders.
		rep.Exits = domain.SessionExitsDue(ours, s.sessionsFor(ctx, ours, now), now, l)
	}
	due := map[int64]bool{}
	for _, ex := range rep.Exits {
		due[ex.PositionID] = true
		i := slices.IndexFunc(ours, func(ap domain.AgentPosition) bool { return ap.PositionID == ex.PositionID })
		if _, err := s.closeT212Position(ctx, a, ours[i], string(ex.Reason)+": "+ex.Detail, nil); err != nil {
			rep.fail("selling position %d: %v", ex.PositionID, err)
			continue
		}
		rep.Closing++
	}
	if aerr == nil {
		rest := slices.DeleteFunc(slices.Clone(ours), func(ap domain.AgentPosition) bool { return due[ap.PositionID] })
		s.t212CheckStops(ctx, a, acct, rest, &rep)
	}
	if last := s.Reconciled(a.ID).At; s.d.Clock.Now().Sub(last) >= ReconcileEvery {
		if _, err := s.Reconcile(ctx, a.ID); err != nil {
			rep.fail("reconciling: %v", err)
		}
		rep.Reconciled = true
	}
	return rep, nil
}

// superviseT212Orders follows up our Trading 212 orders that are not final: an order without
// a Trading 212 id that is no longer being sent is matched (resolveT212); buys and sells with
// an id are read once; a buy still pending after pendingMax (any pending buy when killed or
// flattening) is cancelled, at most one cancel in flight per order and maxCancels in all; a
// cancel row left unconfirmed is abandoned after lookupGiveUp.
func (s *Service) superviseT212Orders(ctx context.Context, a domain.Account, rep *SupervisorReport) {
	orders, err := s.d.Store.Orders().ListOpen(ctx, a.ID)
	if err != nil {
		rep.fail("reading our orders: %v", err)
		return
	}
	hist, err := s.cancelHistory(ctx, a.ID, orders)
	if err != nil {
		rep.fail("reading our cancels: %v", err)
		return
	}
	now := s.d.Clock.Now()
	for _, o := range orders {
		if o.OnPlatform() != domain.PlatformTrading212 {
			continue
		}
		sent := o.SentAt
		if sent.IsZero() {
			sent = o.CreatedAt
		}
		age := now.Sub(sent)
		switch {
		case o.Kind == domain.OrderCancel:
			if age >= lookupGiveUp {
				if _, err := s.abandon(ctx, a, o, "cancel not confirmed"); err != nil {
					rep.fail("order %s: %v", o.RequestID, err)
				}
			}
		case o.EtoroOrderID <= 0:
			// Still being sent by the executor while young and sent; otherwise matched.
			if o.State == domain.OrderUnknown || age >= t212StopConfirm {
				if _, p := s.resolveT212(ctx, a, o); p != "" {
					rep.fail("%s", p)
				}
			}
		case o.Kind == domain.OrderOpen:
			if !rep.Killed && rep.flatten == "" && age < pendingMax {
				continue // the executor is still following it
			}
			cur, err := s.pollT212(ctx, a, o)
			if err != nil {
				rep.fail("reading order %s: %v", o.RequestID, err)
				continue
			}
			if (cur.State != domain.OrderPending && cur.State != domain.OrderPartial) || t212CancelBlocked(hist[cur.EtoroOrderID], now) {
				continue
			}
			why := fmt.Sprintf("pending for over %s", pendingMax)
			switch {
			case rep.flatten != "":
				why = rep.flatten
			case rep.Killed:
				why = "the kill switch is on"
			}
			if _, err := s.cancelT212(ctx, a, cur, why); err != nil {
				rep.fail("cancelling order %s: %v", cur.RequestID, err)
				continue
			}
			rep.Cancelled++
			if cur, err = s.pollT212(ctx, a, cur); err != nil {
				rep.fail("reading order %s: %v", cur.RequestID, err)
			}
		case o.Kind == domain.OrderClose:
			if _, err := s.pollT212(ctx, a, o); err != nil {
				rep.fail("reading order %s: %v", o.RequestID, err)
			}
		}
	}
}

// t212CancelBlocked reports whether no new cancel may be sent for a buy now: one is in flight,
// one was accepted (the buy's own state must say cancelled or filled first, T10), maxCancels
// were sent, or the latest was sent less than pendingMax ago.
func t212CancelBlocked(r cancelRecord, now time.Time) bool {
	return r.inFlight || r.accepted || r.sent >= maxCancels || (r.sent > 0 && now.Sub(r.lastAt) < pendingMax)
}

// t212CheckStops checks, against Trading 212's open orders, that every position of ours that
// is not due has its stop (T212-h): a position the account no longer holds is settled (its
// stop fired, or it was sold outside cactusTrade); a stop that ended at Trading 212 or is not
// listed means the position is sold at once (in session) and the instrument blocked; with the
// market closed a missing stop is placed again. A position younger than t212StopConfirm, or
// with a sale in flight, is left to the executor.
func (s *Service) t212CheckStops(ctx context.Context, a domain.Account, acct domain.T212Account, ours []domain.AgentPosition, rep *SupervisorReport) {
	if len(ours) == 0 {
		return
	}
	open, err := s.d.T212.OpenOrders(ports.WithAccount(ctx, a))
	if err != nil {
		rep.fail("reading the open orders: %v", err)
		return
	}
	now := s.d.Clock.Now()
	for _, ap := range ours {
		if cur, err := s.d.Store.AgentPositions().Get(ctx, a.ID, ap.PositionID); err != nil || !cur.Open() {
			continue // settled meanwhile
		} else {
			ap = cur
		}
		if now.Sub(ap.OpenedAt) < t212StopConfirm || s.t212SaleInFlight(ctx, ap) {
			continue
		}
		if _, held := acct.Position(ap.Ticker); !held && acct.AsOf.Sub(ap.OpenedAt) >= fillVisibleAfter {
			s.t212Vanished(ctx, a, ap)
			continue
		}
		stops, err := s.t212OurStops(ctx, a.ID, ap.PositionID)
		if err != nil {
			rep.fail("reading the stops of position %d: %v", ap.PositionID, err)
			continue
		}
		listed, waiting := false, false
		for _, st := range stops {
			switch {
			case st.EtoroOrderID <= 0:
				waiting = true // being matched (superviseT212Orders)
			case slices.ContainsFunc(open, func(b domain.BrokerOrder) bool { return b.ID == st.EtoroOrderID }):
				listed = true
			default:
				cur, err := s.pollT212(ctx, a, st)
				switch {
				case err != nil:
					rep.fail("reading stop %s: %v", st.RequestID, err)
					waiting = true
				case cur.State == domain.OrderFilled:
					listed = true // fired: applyT212Stop settled the position
				case !cur.State.Final():
					waiting = true
				}
			}
		}
		if listed || waiting {
			continue
		}
		if ok, _ := s.t212InSession(ctx, ap.Instrument, now); !ok {
			// No market order now (T212-o): protect it with a new stop until the open.
			if _, err := s.placeT212Stop(ctx, a, ap, s.t212StopOf(ctx, ap)); err != nil {
				rep.fail("placing a stop for position %d: %v", ap.PositionID, err)
			}
			continue
		}
		s.t212StopFailed(ctx, a, ap, "its stop is no longer at Trading 212")
		rep.Closing++
	}
}

// t212SaleInFlight reports whether a sale of the position is sent and not final.
func (s *Service) t212SaleInFlight(ctx context.Context, ap domain.AgentPosition) bool {
	if ap.CloseRequestID == "" {
		return false
	}
	o, err := s.d.Store.Orders().Get(ctx, ap.CloseRequestID)
	return err == nil && !o.State.Final()
}

// t212StopOf is the stop price of the buy that opened the position (its playbook stop).
func (s *Service) t212StopOf(ctx context.Context, ap domain.AgentPosition) domain.Micros {
	if o, err := s.d.Store.Orders().Get(ctx, ap.OpenRequestID); err == nil {
		return o.StopLoss
	}
	return 0
}

// t212Flatten cancels every pending buy of ours and sells every agent position (OD1: never
// the owner's shares), each request sent once without polling (the supervisor follows them):
// the kill switch and the force close on a Trading 212 practice account. Each sale cancels
// its stop first (T212-i).
func (s *Service) t212Flatten(ctx context.Context, a domain.Account, exit domain.ExitReason, why string, rep *KillReport) {
	if !a.T212Trading() || s.d.T212 == nil {
		return
	}
	sup := SupervisorReport{AccountID: a.ID, Killed: exit == domain.ExitKilled, flatten: why}
	s.superviseT212Orders(ctx, a, &sup)
	rep.Cancelled = sup.Cancelled
	rep.Errors = append(rep.Errors, sup.Errors...)
	ours, err := s.d.Store.AgentPositions().ListOpen(ctx, a.ID)
	if err != nil {
		rep.Errors = append(rep.Errors, "reading the agent positions: "+err.Error())
		return
	}
	for _, ap := range ours {
		if _, err := s.closeT212Position(ctx, a, ap, string(exit)+": "+why, nil); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("selling position %d: %v", ap.PositionID, err))
			continue
		}
		rep.Closing++
	}
}
