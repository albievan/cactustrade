package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// The supervisor (PLAN 4.9, D21) closes what must be closed whatever the agent is doing
// (running, paused, stopped or killed). It needs no LLM, no prices and no reconciliation;
// when the portfolio cannot be read it works from the locally recorded agent positions.

const (
	// SupervisorInterval is how often every eToro account is supervised.
	SupervisorInterval = 10 * time.Second
	// superviseTimeout bounds one account's pass.
	superviseTimeout = 2 * time.Minute
)

// SupervisorReport says what one supervisor pass over an account did.
type SupervisorReport struct {
	AccountID string
	// Skipped says why nothing was done ("" = the pass ran): a view-only or real-mode
	// account, no eToro client, or another process holds the lease.
	Skipped string
	// LocalPositions: the portfolio could not be read; the recorded agent positions were
	// used, with the session rule only.
	LocalPositions bool
	// Killed: the kill switch is on (Tripped: it was tripped by this pass).
	Killed  bool
	Tripped bool
	// Exits are the positions found due; Closing counts the closes sent or in flight.
	Exits   []domain.Exit
	Closing int
	// Cancelled counts cancels sent for pending open orders.
	Cancelled int
	// Reconciled: a periodic reconciliation ran in this pass.
	Reconciled bool
	// Errors lists what failed (the pass goes on after each).
	Errors []string
	// flatten, when set, cancels every pending open order of ours at once with this reason,
	// as Killed does (the kill switch and the force close, R24, set it).
	flatten string
}

func (r *SupervisorReport) fail(format string, args ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, args...))
}

// SuperviseLoop supervises every eToro demo account with keys every SupervisorInterval until
// ctx ends. Each account runs in its own goroutine with a timeout, so one account's exits
// never wait for another's; an account whose previous pass is still running is skipped.
// It returns when ctx ends and every pass has finished.
func (s *Service) SuperviseLoop(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		s.superviseTick(ctx, &wg)
		if s.d.Sleep(ctx, SupervisorInterval) != nil {
			return
		}
	}
}

// superviseTick starts one pass per account that is not still running.
func (s *Service) superviseTick(ctx context.Context, wg *sync.WaitGroup) {
	as, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		s.d.Log.ErrorContext(ctx, "supervisor: listing accounts failed", "err", err)
		return
	}
	s.health.tick(s.d.Clock.Now())
	for _, a := range as {
		if !s.supervisable(ctx, a) {
			continue
		}
		s.health.want(a.ID, s.d.Clock.Now())
		if !s.rt.claim(a.ID) {
			continue
		}
		wg.Go(func() {
			defer s.rt.release(a.ID)
			actx := ports.WithAccount(ctx, a) // log lines name the account and mode (PLAN 7.2)
			cctx, cancel := context.WithTimeout(actx, superviseTimeout)
			defer cancel()
			rep, err := s.Supervise(cctx, a.ID)
			switch {
			case err != nil:
				s.d.Log.ErrorContext(actx, "supervisor pass failed", "account", a.ID, "err", err)
			case len(rep.Errors) > 0:
				s.d.Log.WarnContext(actx, "supervisor pass had errors", "account", a.ID, "errors", rep.Errors)
			}
		})
	}
}

func (r *runtimeState) claim(accountID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.supervising[accountID] {
		return false
	}
	r.supervising[accountID] = true
	return true
}

func (r *runtimeState) release(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.supervising, accountID)
}

// Supervise runs one supervisor pass over an eToro demo account: take the lease, read the
// portfolio (else use the recorded agent positions), fold the equity into the risk state
// (risk day, high-water mark, daily latch; persisted), trip the kill switch on a drawdown,
// close the positions due in their own market's session (all of them when killed), cancel
// open orders pending for over a minute (all pending ones when killed), and reconcile every
// ReconcileEvery. Only cactusTrade's own positions are ever closed (OD1).
func (s *Service) Supervise(ctx context.Context, accountID string) (SupervisorReport, error) {
	rep, err := s.supervise(ctx, accountID)
	s.health.pass(accountID, s.d.Clock.Now(), rep, err) // TradingHealth (PLAN 7.5)
	return rep, err
}

func (s *Service) supervise(ctx context.Context, accountID string) (SupervisorReport, error) {
	rep := SupervisorReport{AccountID: accountID}
	a, err := s.d.Store.Accounts().Get(ctx, accountID)
	if err != nil {
		return rep, fmt.Errorf("app.Supervise: %w", err)
	}
	ctx = ports.WithAccount(ctx, a) // log lines name the account and mode (PLAN 7.2)
	switch {
	case !a.Tradable():
		rep.Skipped = "view-only account"
	case a.Mode != domain.ModeDemo:
		rep.Skipped = "real mode is gated (phase 7)"
	case s.d.Broker == nil:
		rep.Skipped = "the eToro client is not configured"
	}
	if rep.Skipped != "" {
		s.d.Log.DebugContext(ctx, "supervisor pass skipped", "reason", rep.Skipped)
		return rep, nil
	}
	if rep.Skipped = s.leaseProblem(ctx, a.ID); rep.Skipped != "" {
		return rep, nil
	}
	ours, err := s.d.Store.AgentPositions().ListOpen(ctx, a.ID)
	if err != nil {
		return rep, fmt.Errorf("app.Supervise: %w", err)
	}
	now := s.d.Clock.Now()
	l := s.riskLimits(ctx, a.ID)
	if k, err := s.isKilled(ctx, a.ID); err != nil {
		rep.fail("reading the kill switch: %v", err)
	} else {
		rep.Killed = k
	}
	pf, perr := s.d.Broker.Portfolio(ports.WithAccount(ctx, a))
	if perr != nil {
		rep.LocalPositions = true
		rep.fail("reading the portfolio: %v", perr)
	} else {
		// Shared with the views (PLAN 6.6a): stored only, never read back on this path.
		s.rt.views.put(a.ID, pf, s.d.Clock.Now())
		s.publishPortfolio(a.ID, pf)
		st, err := s.observe(ctx, a, pf, l, now)
		switch {
		case err != nil:
			rep.fail("storing the risk state: %v", err)
		case !rep.Killed && domain.DrawdownBreached(pf.Equity(), st.State, l):
			why := fmt.Sprintf("drawdown limit: the account equity $%s (every position, also those not opened by cactusTrade) is $%s below the high-water mark $%s (limit %d bp)",
				pf.Equity(), st.State.HighWaterMark-pf.Equity(), st.State.HighWaterMark, l.Drawdown)
			if err := s.trip(ctx, a, why); err != nil {
				rep.fail("persisting the kill: %v", err)
			}
			rep.Killed, rep.Tripped = true, true
		}
	}

	byID := map[int64]domain.AgentPosition{}
	for _, ap := range ours {
		byID[ap.PositionID] = ap
	}
	sessions := s.sessionsFor(ctx, ours, now)
	switch {
	case rep.Killed:
		for _, ap := range ours {
			rep.Exits = append(rep.Exits, domain.Exit{PositionID: ap.PositionID, Instrument: ap.Instrument, Reason: domain.ExitKilled, Detail: "the kill switch is on"})
		}
	case perr == nil:
		var mine []domain.Position
		for _, p := range pf.Positions {
			if _, ok := byID[p.ID]; ok {
				mine = append(mine, p)
			}
		}
		rep.Exits = domain.ExitsDue(mine, sessions, now, l)
	default:
		rep.Exits = domain.SessionExitsDue(ours, sessions, now, l)
	}
	// Send every close first (no polling here: the next pass reads them), so a kill with
	// many positions is not held up by the first one.
	for _, ex := range rep.Exits {
		if _, err := s.closePosition(ctx, a, byID[ex.PositionID], string(ex.Reason)+": "+ex.Detail, nil); err != nil {
			rep.fail("closing position %d: %v", ex.PositionID, err)
			continue
		}
		rep.Closing++
	}
	s.superviseOrders(ctx, a, &rep)
	if last := s.Reconciled(a.ID).At; s.d.Clock.Now().Sub(last) >= ReconcileEvery {
		if _, err := s.Reconcile(ctx, a.ID); err != nil {
			rep.fail("reconciling: %v", err)
		}
		rep.Reconciled = true
	}
	return rep, nil
}

// leaseProblem takes or renews the lease and says why not ("" = held).
func (s *Service) leaseProblem(ctx context.Context, accountID string) string {
	if err := s.holdLease(ctx, accountID); err != nil {
		return err.Error()
	}
	return ""
}

// superviseOrders follows up our orders that are not final: open orders pending longer than
// pendingMax (any pending one when killed) are looked up once and cancelled if still
// pending; close orders with an eToro id are read once.
func (s *Service) superviseOrders(ctx context.Context, a domain.Account, rep *SupervisorReport) {
	orders, err := s.d.Store.Orders().ListOpen(ctx, a.ID)
	if err != nil {
		rep.fail("reading our orders: %v", err)
		return
	}
	recent, err := s.recentCancels(ctx, a.ID)
	if err != nil {
		rep.fail("reading our cancels: %v", err)
		return
	}
	now := s.d.Clock.Now()
	for _, o := range orders {
		switch {
		case o.Kind == domain.OrderClose && o.EtoroOrderID > 0:
			if p := s.settleOrder(ctx, a, o, domain.Portfolio{}, orders); p != "" {
				rep.fail("%s", p)
			}
		case o.Kind == domain.OrderOpen:
			sent := o.SentAt
			if sent.IsZero() {
				sent = o.CreatedAt
			}
			if !rep.Killed && rep.flatten == "" && now.Sub(sent) < pendingMax {
				continue // the executor is still following it
			}
			if p := s.settleOrder(ctx, a, o, domain.Portfolio{}, orders); p != "" {
				rep.fail("%s", p)
			}
			cur, err := s.d.Store.Orders().Get(ctx, o.RequestID)
			if err != nil {
				rep.fail("reading order %s: %v", o.RequestID, err)
				continue
			}
			if (cur.State == domain.OrderPending || cur.State == domain.OrderPartial) && cur.EtoroOrderID > 0 && !recent[cur.EtoroOrderID] {
				why := fmt.Sprintf("pending for over %s", pendingMax)
				switch {
				case rep.flatten != "":
					why = rep.flatten
				case rep.Killed:
					why = "the kill switch is on"
				}
				if _, err := s.cancelOrder(ctx, a, cur, why); err != nil {
					rep.fail("cancelling order %s: %v", cur.RequestID, err)
					continue
				}
				rep.Cancelled++
			}
		}
	}
}

// recentCancels returns the eToro order ids a cancel of ours was sent for within
// pendingMax, or is still in flight for: they are not cancelled again yet.
func (s *Service) recentCancels(ctx context.Context, accountID string) (map[int64]bool, error) {
	os, err := s.d.Store.Orders().List(ctx, accountID, 200)
	if err != nil {
		return nil, err
	}
	now := s.d.Clock.Now()
	out := map[int64]bool{}
	for _, o := range os {
		if o.Kind == domain.OrderCancel && (!o.State.Final() || now.Sub(o.CreatedAt) < pendingMax) {
			out[o.TargetOrderID] = true
		}
	}
	return out, nil
}

// sessionsFor returns, per instrument of the positions, the calendar session of its own
// market now. An instrument that is not cached, has no supported market, or whose calendar
// answer fails is left out: ExitsDue treats it as unknown (fail closed, D25). No market data
// is read.
func (s *Service) sessionsFor(ctx context.Context, ours []domain.AgentPosition, now time.Time) map[domain.InstrumentID]domain.MarketSession {
	out := map[domain.InstrumentID]domain.MarketSession{}
	if s.d.Calendar == nil {
		return out
	}
	for _, ap := range ours {
		if _, done := out[ap.Instrument]; done {
			continue
		}
		in, _, err := s.d.Store.Instruments().Get(ctx, ap.Instrument)
		if err != nil || in.Market == "" {
			continue
		}
		open, closeAt, trading, err := s.d.Calendar.Session(in.Market, now)
		if err != nil {
			continue
		}
		out[ap.Instrument] = domain.MarketSession{Known: true, Trading: trading, Open: open, Close: closeAt}
	}
	return out
}

// observe folds the portfolio's equity into the account's risk state and stores it when it
// changed (versioned: a concurrent write is re-read and re-applied, so a kill is never lost).
func (s *Service) observe(ctx context.Context, a domain.Account, pf domain.Portfolio, l domain.RiskLimits, now time.Time) (domain.AccountRiskState, error) {
	for range 3 {
		cur, err := s.d.Store.RiskStates().Get(ctx, a.ID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			cur = domain.AccountRiskState{AccountID: a.ID}
		case err != nil:
			return cur, err
		}
		next := cur
		next.State = cur.State.Observe(pf.Equity(), domain.RiskDay(now), l)
		if next.State == cur.State {
			return cur, nil
		}
		next.UpdatedAt = now.UTC()
		saved, err := s.d.Store.RiskStates().Put(ctx, next)
		if errors.Is(err, domain.ErrConflict) {
			continue
		}
		if err == nil && next.State.DayStopped && (!cur.State.DayStopped || cur.State.Day != next.State.Day) {
			s.emitFor(ctx, a.ID, "agent", "risk.daily_stop", domain.SeverityWarning, "Daily loss limit reached on "+a.Name,
				fmt.Sprintf("equity $%s, floor $%s: no new entries until the next risk day", pf.Equity(), domain.DailyFloor(next.State, l)))
		}
		return saved, err
	}
	return domain.AccountRiskState{}, fmt.Errorf("app.observe: the risk state kept changing: %w", domain.ErrConflict)
}

// killEffect says what a kill does and does not do (OD1, PLAN 4.9-x): it is part of every
// kill event, because the supervisor watches the equity of every eToro demo account with keys,
// including positions cactusTrade did not open.
const killEffect = "New entries are stopped. Only the positions cactusTrade opened are closed and its pending orders cancelled; " +
	"positions opened outside cactusTrade are left alone. An admin must reset the kill switch."

// trip turns the kill switch on for the supervisor and the agent (drawdown): it takes the
// account's control lock and calls tripLocked. Closing the positions and cancelling the
// orders follows in the caller (the supervisor does it in the same pass, and on every later
// pass while killed).
func (s *Service) trip(ctx context.Context, a domain.Account, why string) error {
	l := s.rt.agent(a.ID)
	l.ctl.Lock()
	defer l.ctl.Unlock()
	_, err := s.tripLocked(ctx, a, Principal{}, why)
	return err
}

// tripLocked turns the kill switch on; the caller holds the account's control lock. The kill
// is persisted first (risk state and agent state), then the running agent tick is cancelled,
// then it is audited and announced. already reports that the switch was on before (nothing
// is announced again).
func (s *Service) tripLocked(ctx context.Context, a domain.Account, p Principal, why string) (already bool, err error) {
	now := s.d.Clock.Now().UTC()
	// Latched in memory first: it holds even if neither write below reaches the database
	// (review m3), until ResetKill.
	s.rt.latchKill(a.ID)
	already, err = s.persistKill(ctx, a.ID, why, now)
	if serr := s.setAgentState(ctx, a.ID, domain.AgentKilled); serr != nil {
		err = errors.Join(err, serr)
		s.publishAgentState(a.ID, domain.AgentKilled) // latched in memory all the same
	}
	s.rt.cancelEntries(a.ID)
	if already && err == nil {
		return true, nil
	}
	s.audit(ctx, p, "agent.kill", a.ID, why)
	s.emitFor(ctx, a.ID, p.Username, "agent.killed", domain.SeverityDanger, "Kill switch on "+a.Name, why+". "+killEffect)
	if err != nil {
		return already, fmt.Errorf("app.trip: %w", err)
	}
	return already, nil
}

// persistKill stores the kill in the account's risk state (versioned, retried on a
// conflict). already reports that it was stored before.
func (s *Service) persistKill(ctx context.Context, accountID, why string, now time.Time) (already bool, err error) {
	for range 3 {
		cur, err := s.d.Store.RiskStates().Get(ctx, accountID)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			cur = domain.AccountRiskState{AccountID: accountID}
		case err != nil:
			return false, err
		}
		if cur.State.Killed {
			return true, nil
		}
		cur.State.Killed, cur.KillReason, cur.KilledAt, cur.UpdatedAt = true, why, now, now
		if _, err = s.d.Store.RiskStates().Put(ctx, cur); !errors.Is(err, domain.ErrConflict) {
			return false, err
		}
	}
	return false, fmt.Errorf("the risk state kept changing: %w", domain.ErrConflict)
}
