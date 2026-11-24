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
	defer s.sweepRealGatesAside(ctx, wg, as) // after the passes have started (review minor 4)
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
			announce, err := s.tripQuietly(ctx, a, why)
			defer announce() // a gate disarm is announced after this pass's exits (review minor 4)
			if err != nil {
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
// pendingMax (any pending one when killed) are looked up once (by eToro's order id when known)
// and cancelled if still pending, at most once per order while a cancel is in flight or was
// accepted (cancelRecord.blocked); close orders with an eToro id are read once.
func (s *Service) superviseOrders(ctx context.Context, a domain.Account, rep *SupervisorReport) {
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
			// Look up first: a cancel is only for what is still pending after the lookup.
			if p := s.settleOrder(ctx, a, o, domain.Portfolio{}, orders); p != "" {
				rep.fail("%s", p)
			}
			cur, err := s.d.Store.Orders().Get(ctx, o.RequestID)
			if err != nil {
				rep.fail("reading order %s: %v", o.RequestID, err)
				continue
			}
			if (cur.State != domain.OrderPending && cur.State != domain.OrderPartial) || cur.EtoroOrderID <= 0 {
				continue
			}
			if s.cancelBlocked(ctx, a, cur, hist[cur.EtoroOrderID], now) {
				continue
			}
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

// maxCancels is the most cancels sent for one open order while none of them was accepted
// (eToro refused them, or they were abandoned unconfirmed), a pendingMax apart. An accepted
// cancel is never followed by another: on 2026-11-24 the supervisor sent 151 accepted cancels
// for one filled order in 90 minutes (live findings).
const maxCancels = 3

// cancelRecord sums up the cancels of ours for one eToro order.
type cancelRecord struct {
	// inFlight: one is not final yet. accepted: eToro accepted one (stored as filled), the
	// latest at acceptedAt.
	inFlight, accepted bool
	acceptedAt         time.Time
	// sent counts every cancel stored for the order; lastAt is when the latest was stored.
	sent   int
	lastAt time.Time
}

// cancelHistory returns the cancels of ours per eToro order id, for every order sent from the
// oldest open order with an eToro id on (a cancel is always younger than its target).
func (s *Service) cancelHistory(ctx context.Context, accountID string, open []domain.Order) (map[int64]cancelRecord, error) {
	out := map[int64]cancelRecord{}
	var since time.Time
	for _, o := range open {
		if o.Kind == domain.OrderOpen && o.EtoroOrderID > 0 && (since.IsZero() || o.CreatedAt.Before(since)) {
			since = o.CreatedAt
		}
	}
	if since.IsZero() {
		return out, nil
	}
	os, err := s.d.Store.Orders().ListSince(ctx, accountID, since)
	if err != nil {
		return nil, err
	}
	for _, c := range os {
		if c.Kind != domain.OrderCancel || c.TargetOrderID <= 0 {
			continue
		}
		r := out[c.TargetOrderID]
		r.sent++
		if c.CreatedAt.After(r.lastAt) {
			r.lastAt = c.CreatedAt
		}
		switch {
		case !c.State.Final():
			r.inFlight = true
		case c.State == domain.OrderFilled:
			r.accepted = true
			if c.UpdatedAt.After(r.acceptedAt) {
				r.acceptedAt = c.UpdatedAt
			}
		}
		out[c.TargetOrderID] = r
	}
	return out, nil
}

// cancelBlocked reports whether no new cancel may be sent for the pending open order target
// now: one is in flight; one was accepted (eToro's lookup must say cancelled or filled first);
// maxCancels were sent; or the latest was sent less than pendingMax ago. An accepted cancel
// still unconfirmed after pendingMax, and the maxCancels limit, raise one warning per order.
func (s *Service) cancelBlocked(ctx context.Context, a domain.Account, target domain.Order, r cancelRecord, now time.Time) bool {
	label := s.instrumentLabel(ctx, target.Instrument)
	switch {
	case r.inFlight:
		return true
	case r.accepted:
		if now.Sub(r.acceptedAt) >= pendingMax {
			s.warnOnce(ctx, a, target, "order.cancel_unconfirmed",
				fmt.Sprintf("Cancel of %s order %d accepted but not confirmed", label, target.EtoroOrderID),
				fmt.Sprintf("eToro accepted the cancel at %s but its lookup still says %s or does not find the order. No further cancel is sent; "+
					"its cash stays reserved and entries in %s wait until a lookup says cancelled or filled", r.acceptedAt.UTC().Format(time.RFC3339), target.State, label))
		}
		return true
	case r.sent >= maxCancels:
		s.warnOnce(ctx, a, target, "order.cancel_refused",
			fmt.Sprintf("Cancel of %s order %d not accepted after %d tries", label, target.EtoroOrderID, r.sent),
			fmt.Sprintf("No further cancel is sent; its cash stays reserved and entries in %s wait until a lookup says cancelled or filled", label))
		return true
	case r.sent > 0 && now.Sub(r.lastAt) < pendingMax:
		return true
	}
	return false
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
	announce, err := s.tripQuietly(ctx, a, why)
	announce()
	return err
}

// tripQuietly is trip leaving the gate disarm's announcement (never nil) to the caller, which
// runs it after its exit work (the supervisor: at the end of the pass).
func (s *Service) tripQuietly(ctx context.Context, a domain.Account, why string) (func(), error) {
	l := s.rt.agent(a.ID)
	l.ctl.Lock()
	defer l.ctl.Unlock()
	_, announce, err := s.tripLocked(ctx, a, Principal{}, why)
	return announce, err
}

// tripLocked turns the kill switch on; the caller holds the account's control lock. The kill
// is persisted first (risk state and agent state), then the running agent tick is cancelled,
// a real-money account's gate is disarmed (latched and stored, OD24), then the kill is
// audited and announced. already reports that the switch was on before (nothing is announced
// again). announce (never nil) sends the gate disarm's event and e-mails: the caller runs it
// after its exit work (review minor 4).
func (s *Service) tripLocked(ctx context.Context, a domain.Account, p Principal, why string) (already bool, announce func(), err error) {
	already, err = s.storeKillLocked(ctx, a, why)
	announce = s.disarmIfArmedQuietly(ctx, a, p, domain.DisarmKill+": "+why) // OD24
	if already && err == nil {
		return true, announce, nil
	}
	s.announceKill(ctx, a, p, why)
	if err != nil {
		return already, announce, fmt.Errorf("app.trip: %w", err)
	}
	return already, announce, nil
}

// storeKillLocked latches the kill in memory, stores it (risk state and agent state) and
// cancels the running agent tick; the caller holds the account's control lock. It leaves the
// gate alone (tripLocked disarms it; a failed disarm calls this to make its refusal hold in
// every process).
func (s *Service) storeKillLocked(ctx context.Context, a domain.Account, why string) (already bool, err error) {
	// Latched in memory first: it holds even if neither write below reaches the database
	// (review m3), until ResetKill.
	s.rt.latchKill(a.ID)
	already, err = s.persistKill(ctx, a.ID, why, s.d.Clock.Now().UTC())
	if serr := s.setAgentState(ctx, a.ID, domain.AgentKilled); serr != nil {
		err = errors.Join(err, serr)
		s.publishAgentState(a.ID, domain.AgentKilled) // latched in memory all the same
	}
	s.rt.cancelEntries(a.ID)
	return already, err
}

// announceKill audits a kill and raises its danger event.
func (s *Service) announceKill(ctx context.Context, a domain.Account, p Principal, why string) {
	s.audit(ctx, p, "agent.kill", a.ID, why)
	s.emitFor(ctx, a.ID, p.Username, "agent.killed", domain.SeverityDanger, "Kill switch on "+a.Name, why+". "+killEffect)
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
