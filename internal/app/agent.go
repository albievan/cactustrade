package app

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// The agent (PLAN 4.10) opens positions: at each bar close it walks the account's watchlist,
// asks the strategy for a candidate, checks costs and the LLM reviewer, re-reads cash and the
// price, applies the risk engine and hands an allowed entry to the executor. It never closes
// anything (the supervisor does, D21) and every failure means "no entry" (fail closed).

// Agent timing (OD3, D26).
const (
	// AgentBarInterval is the bar the agent decides on: 5-minute bars.
	AgentBarInterval = 5 * time.Minute
	// AgentDecisionDelay is how long after a bar close the agent decides, so the bar is
	// complete at eToro.
	AgentDecisionDelay = 5 * time.Second
	// agentBars is how many bars the strategy gets (~5 NYSE sessions).
	agentBars = 400
	// agentTickTimeout bounds one account's tick: an entry is followed for up to 2 minutes.
	agentTickTimeout = 4 * time.Minute
	// agentReviewTimeout is the reviewer's time limit when its profile sets none.
	agentReviewTimeout = 60 * time.Second
)

// TraceStep names one step of an entry decision.
type TraceStep string

// The steps of an entry decision, in order (PLAN 4.10).
const (
	StepSession      TraceStep = "session"      // the instrument's market is in session (LSE off, OD9)
	StepCandles      TraceStep = "candles"      // closed 5-minute bars, the forming one dropped
	StepSignal       TraceStep = "signal"       // the strategy proposes a candidate
	StepCosts        TraceStep = "costs"        // eToro's costs below the gross gain at the target (OD8)
	StepReview       TraceStep = "review"       // the LLM reviewer's verdict (OD7)
	StepPrices       TraceStep = "prices"       // fresh portfolio and quote
	StepReservations TraceStep = "reservations" // cash held by our unfinished orders
	StepRisk         TraceStep = "risk"         // domain.AssessEntry
	StepOrder        TraceStep = "order"        // the executor placed it
)

// TraceCheck is the outcome of one step.
type TraceCheck struct {
	Step   TraceStep
	Pass   bool
	Detail string
}

// InstrumentTrace is one instrument's way through an agent tick.
type InstrumentTrace struct {
	Instrument domain.InstrumentID
	Symbol     string
	Market     domain.Market
	Direction  domain.Direction
	Checks     []TraceCheck
	// Entered: an order was placed (RequestID, Amount and OrderState say how it ended).
	Entered    bool
	RequestID  string
	Amount     domain.Cents
	OrderState domain.OrderState
}

func (t *InstrumentTrace) pass(step TraceStep, detail string) {
	t.Checks = append(t.Checks, TraceCheck{Step: step, Pass: true, Detail: detail})
}

// fail records a failed step and returns false, so a caller can `return t.fail(...)`.
func (t *InstrumentTrace) fail(step TraceStep, format string, args ...any) bool {
	t.Checks = append(t.Checks, TraceCheck{Step: step, Detail: fmt.Sprintf(format, args...)})
	return false
}

// AgentTrace is what one agent tick did, step by step (the decision log persists it in
// phase 5).
type AgentTrace struct {
	AccountID string
	At        time.Time
	// Skipped says why the tick did nothing ("" = it ran): not running, not reconciled,
	// killed, real mode, no calendar, no lease, no budget…
	Skipped     string
	Instruments []InstrumentTrace
	// Entries counts the orders placed.
	Entries int
	// Killed: the risk engine found the drawdown limit breached and the kill switch was
	// tripped.
	Killed bool
	Errors []string
}

// AgentLoop runs the agent of every eToro demo account whose agent is running, at each bar
// close plus AgentDecisionDelay, until ctx ends. Each account ticks in its own goroutine with
// a timeout; an account whose previous tick still runs is skipped. It returns when ctx ends
// and every tick has finished.
func (s *Service) AgentLoop(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		now := s.d.Clock.Now()
		if s.d.Sleep(ctx, domain.NextDecision(now, AgentBarInterval, AgentDecisionDelay).Sub(now)) != nil {
			return
		}
		s.agentTickAll(ctx, &wg)
	}
}

// agentTickAll starts one tick per running demo account that is not still ticking.
func (s *Service) agentTickAll(ctx context.Context, wg *sync.WaitGroup) {
	as, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		s.d.Log.ErrorContext(ctx, "agent: listing accounts failed", "err", err)
		return
	}
	for _, a := range as {
		if !a.Tradable() || a.Mode != domain.ModeDemo || s.agentState(ctx, a.ID) != domain.AgentRunning || !s.rt.claimTick(a.ID) {
			continue
		}
		wg.Go(func() {
			defer s.rt.releaseTick(a.ID)
			cctx, cancel := context.WithTimeout(ctx, agentTickTimeout)
			defer cancel()
			tr, err := s.AgentTick(cctx, a.ID)
			switch {
			case err != nil:
				s.d.Log.ErrorContext(ctx, "agent tick failed", "account", a.ID, "err", err)
			case len(tr.Errors) > 0:
				s.d.Log.WarnContext(ctx, "agent tick had errors", "account", a.ID, "errors", tr.Errors)
			}
		})
	}
}

func (r *runtimeState) claimTick(accountID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ticking[accountID] {
		return false
	}
	r.ticking[accountID] = true
	return true
}

func (r *runtimeState) releaseTick(accountID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.ticking, accountID)
}

// AgentTick runs one decision round of an account's agent. It does nothing unless the
// account is an eToro demo account whose agent is running, not killed, reconciled, with a
// market calendar (D25), a trading budget and this process holding its lease. One tick runs
// at a time per account; Kill cancels a running one. The trace says what each step decided.
func (s *Service) AgentTick(ctx context.Context, accountID string) (AgentTrace, error) {
	tr := AgentTrace{AccountID: accountID, At: s.d.Clock.Now().UTC()}
	a, err := s.d.Store.Accounts().Get(ctx, accountID)
	if err != nil {
		return tr, fmt.Errorf("app.AgentTick: %w", err)
	}
	killWhy := s.agentTickLocked(ctx, a, &tr)
	if killWhy != "" {
		// Tripped outside the entry lock: Kill holds the control lock and waits for it.
		if err := s.trip(ctx, a, killWhy); err != nil {
			tr.Errors = append(tr.Errors, "persisting the kill: "+err.Error())
		}
		tr.Killed = true
	}
	return tr, nil
}

// agentTickLocked is the tick under the account's entry lock. It returns why the kill switch
// must be tripped ("" = it need not).
func (s *Service) agentTickLocked(ctx context.Context, a domain.Account, tr *AgentTrace) string {
	locks := s.rt.agent(a.ID)
	if !locks.entry.TryLock() {
		tr.Skipped = "the previous tick is still running"
		return ""
	}
	defer locks.entry.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	s.rt.setEntryCancel(a.ID, cancel)
	defer func() {
		s.rt.setEntryCancel(a.ID, nil)
		cancel()
	}()
	if tr.Skipped = s.agentGate(ctx, a); tr.Skipped != "" {
		return ""
	}
	l := s.riskLimits(ctx, a.ID)
	if l.TradingBudget <= 0 {
		tr.Skipped = "no trading budget is set"
		return ""
	}
	wl, err := s.d.Store.Watchlist().List(ctx, a.ID)
	if err != nil {
		tr.Skipped = "reading the watchlist failed"
		tr.Errors = append(tr.Errors, err.Error())
		return ""
	}
	if len(wl) == 0 {
		tr.Skipped = "the watchlist is empty"
		return ""
	}
	pf, err := s.d.Broker.Portfolio(ports.WithAccount(ctx, a))
	if err != nil {
		tr.Skipped = "reading the portfolio failed"
		tr.Errors = append(tr.Errors, err.Error())
		return ""
	}
	sizing := min(l.TradingBudget, pf.Equity())
	if sizing <= 0 {
		tr.Skipped = "no equity"
		return ""
	}
	items := make([]universeItem, 0, len(wl))
	for _, w := range wl {
		items = append(items, universeItem{in: w.Instrument})
	}
	elig := s.refreshEligibility(ctx, domain.AccountView{Account: a, Access: domain.AccessOwner}, items)
	for _, w := range wl {
		if ctx.Err() != nil {
			tr.Errors = append(tr.Errors, "cancelled: "+ctx.Err().Error())
			return ""
		}
		// A pause, stop or kill between two instruments ends the round.
		if st := s.agentState(ctx, a.ID); st != domain.AgentRunning {
			tr.Errors = append(tr.Errors, "the agent became "+string(st))
			return ""
		}
		it := InstrumentTrace{Instrument: w.Instrument.ID, Symbol: w.Instrument.Symbol, Market: w.Instrument.Market}
		killWhy := s.consider(ctx, a, l, w.Instrument, elig[w.Instrument.ID], pf, sizing, &it)
		tr.Instruments = append(tr.Instruments, it)
		if it.Entered {
			tr.Entries++
		}
		if killWhy != "" {
			return killWhy
		}
	}
	return ""
}

// agentGate says why the account's agent may not enter now ("" = it may).
func (s *Service) agentGate(ctx context.Context, a domain.Account) string {
	switch {
	case !a.Tradable():
		return "view-only account"
	case a.Mode != domain.ModeDemo:
		return "real mode is gated (phase 7): only demo accounts trade"
	case s.d.Broker == nil || s.d.Market == nil:
		return "the eToro client is not configured"
	case s.d.Calendar == nil:
		return "no market calendar: entries need one (D25)"
	}
	if st := s.agentState(ctx, a.ID); st != domain.AgentRunning {
		return "the agent is " + string(st)
	}
	switch killed, err := s.isKilled(ctx, a.ID); {
	case err != nil:
		return "the kill switch cannot be read"
	case killed:
		return "the kill switch is on"
	}
	if rc := s.Reconciled(a.ID); !rc.OK {
		return "the account is not reconciled: " + rc.Reason
	}
	return s.leaseProblem(ctx, a.ID)
}

// consider takes one watchlist instrument through the entry steps and places the entry if
// every step passes. It returns why the kill switch must be tripped ("" = it need not).
func (s *Service) consider(ctx context.Context, a domain.Account, l domain.RiskLimits, in domain.Instrument, el domain.Eligibility, pf domain.Portfolio, sizing domain.Cents, it *InstrumentTrace) string {
	bctx := ports.WithAccount(ctx, a)
	now := s.d.Clock.Now()
	// (1) The instrument's own market is in session, and not too close to its end.
	sess, ok := s.entrySession(in, now, l, it)
	if !ok {
		return ""
	}
	for _, p := range pf.Positions {
		if p.Instrument == in.ID {
			it.fail(StepSession, "a position in %s is already open", in.Symbol)
			return ""
		}
	}
	// (2) Closed 5-minute bars only (U5: the newest may still be forming).
	bars, err := s.d.Market.Candles(bctx, in.ID, AgentBarInterval, agentBars)
	if err != nil {
		it.fail(StepCandles, "no price history: %v", err)
		return ""
	}
	bars = domain.ClosedBars(bars, AgentBarInterval, now)
	if !domain.BarsFresh(bars, AgentBarInterval, now) {
		it.fail(StepCandles, "the newest closed bar is missing or stale (%d bars)", len(bars))
		return ""
	}
	it.pass(StepCandles, fmt.Sprintf("%d closed bars", len(bars)))
	// (3) The strategy, sized by min(budget, equity); shorts only when allowed.
	c, ok := s.signal(in, bars, sizing, l, it)
	if !ok {
		return ""
	}
	size := min(c.SuggestedSize, sizing)
	// (4) Costs below the gross gain at the take-profit (OD8).
	if !s.costGate(bctx, c, size, c.Entry, it) {
		return ""
	}
	// (5) The LLM reviewer, when reviewing is on (OD7): it can only shrink or reject.
	if size, ok = s.agentReview(ctx, a, c, size, bars, pf, it); !ok {
		return ""
	}
	// (6) Fresh cash and a fresh price; the stop must still be on the losing side.
	fresh, q, entry, ok := s.freshPrices(bctx, c, l, it)
	if !ok {
		return ""
	}
	// The steps before (bars, costs, the reviewer) may have taken a while: the decision is
	// made at the time of the fresh figures (review M1), and the session is looked up again.
	now = s.d.Clock.Now()
	if sess, ok = s.sessionNow(in, now, it); !ok {
		return ""
	}
	// (7) Our unfinished orders hold cash and block their instruments (D24), and so do fills
	// the portfolio does not show yet (review B1).
	res, err := s.reservations(ctx, a.ID, fresh, now)
	if err != nil {
		it.fail(StepReservations, "reading our orders failed: %v", err)
		return ""
	}
	it.pass(StepReservations, fmt.Sprintf("$%s reserved by %d order(s)", res.Amount, res.Count))
	// (8) The risk engine, on the fresh equity folded into today's risk state.
	rs, err := s.observe(ctx, a, fresh, l, now)
	if err != nil {
		it.fail(StepRisk, "storing the risk state failed: %v", err)
		return ""
	}
	blocked, err := s.isBlocked(ctx, a.ID, in.ID)
	if err != nil {
		it.fail(StepRisk, "reading the instrument blocks failed: %v", err)
		return ""
	}
	rin := res.Apply(domain.RiskInput{
		Now: now, Mode: a.Mode, SessionOpen: sess.Open, SessionClose: sess.Close, Trading: sess.Trading,
		Portfolio: fresh, Instrument: in.ID, Direction: c.Direction, Eligibility: el,
		Entry: entry, QuoteAt: q.At, StopLoss: c.StopLossRate, Requested: size, Limits: l, State: rs.State,
		Reconciled: s.Reconciled(a.ID).OK, Blocked: blocked,
	})
	dec := domain.AssessEntry(rin)
	if dec.Kill {
		it.fail(StepRisk, "%s", dec.Reason)
		return dec.Reason
	}
	if !dec.Allowed {
		it.fail(StepRisk, "%s", dec.Reason)
		return ""
	}
	// The cost gate again when the engine shrank the size: fixed fees weigh more.
	if dec.Size < size && !s.costGate(bctx, c, dec.Size, entry, it) {
		return ""
	}
	it.pass(StepRisk, fmt.Sprintf("allowed $%s of $%s requested", dec.Size, size))
	// (9) The executor: persisted first, then sent, then followed.
	if st := s.agentState(ctx, a.ID); st != domain.AgentRunning {
		it.fail(StepOrder, "the agent became %s", st)
		return ""
	}
	o, err := s.placeEntry(ctx, a, entryRequest{
		Direction: c.Direction, Instrument: in.ID, Amount: dec.Size, Entry: entry,
		StopLoss: c.StopLossRate, TakeProfit: c.TakeProfitRate, PortfolioAt: fresh.AsOf,
	}, l)
	it.RequestID, it.OrderState, it.Amount = o.RequestID, o.State, o.Amount
	it.Entered = o.RequestID != "" && o.Sends > 0
	if err != nil {
		it.fail(StepOrder, "%v", err)
		return ""
	}
	it.pass(StepOrder, fmt.Sprintf("%s $%s, order %s is %s", c.Direction, o.Amount, o.RequestID, o.State))
	return ""
}

// entrySession checks step (1): the instrument's market takes entries (OD9) and is in
// session now, before the no-entry window at its end.
func (s *Service) entrySession(in domain.Instrument, now time.Time, l domain.RiskLimits, it *InstrumentTrace) (domain.MarketSession, bool) {
	if ok, why := domain.EntriesAllowed(in.Market); !ok {
		return domain.MarketSession{}, it.fail(StepSession, "%s", why)
	}
	open, closeAt, trading, err := s.d.Calendar.Session(in.Market, now)
	switch {
	case err != nil:
		return domain.MarketSession{}, it.fail(StepSession, "calendar: %v", err)
	case !trading || now.Before(open) || !now.Before(closeAt):
		return domain.MarketSession{}, it.fail(StepSession, "%s is not in session", in.Market)
	case !now.Before(closeAt.Add(-l.NoEntryBeforeClose)):
		return domain.MarketSession{}, it.fail(StepSession, "%s closes within %s", in.Market, l.NoEntryBeforeClose)
	}
	it.pass(StepSession, fmt.Sprintf("%s in session until %s", in.Market, closeAt.Format(time.RFC3339)))
	return domain.MarketSession{Known: true, Trading: trading, Open: open, Close: closeAt}, true
}

// sessionNow looks the instrument's session up again at now (the entry window itself is
// checked by domain.AssessEntry).
func (s *Service) sessionNow(in domain.Instrument, now time.Time, it *InstrumentTrace) (domain.MarketSession, bool) {
	open, closeAt, trading, err := s.d.Calendar.Session(in.Market, now)
	if err != nil {
		return domain.MarketSession{}, it.fail(StepRisk, "calendar: %v", err)
	}
	return domain.MarketSession{Known: true, Trading: trading, Open: open, Close: closeAt}, true
}

// recentOrders is how many of an account's latest orders are searched for fills the
// portfolio may not show yet (far more than eToro's execution quota allows in
// FillVisibleAfter).
const recentOrders = 200

// reservations is what the account's own orders hold back at now against the portfolio pf:
// every unfinished open order, plus every fill pf does not show yet (review B1).
func (s *Service) reservations(ctx context.Context, accountID string, pf domain.Portfolio, now time.Time) (domain.Reservation, error) {
	open, err := s.d.Store.Orders().ListOpen(ctx, accountID)
	if err != nil {
		return domain.Reservation{}, err
	}
	recent, err := s.d.Store.Orders().List(ctx, accountID, recentOrders)
	if err != nil {
		return domain.Reservation{}, err
	}
	ours, err := s.d.Store.AgentPositions().ListOpen(ctx, accountID)
	if err != nil {
		return domain.Reservation{}, err
	}
	return domain.Reservations(open).WithUnseenFills(recent, ours, pf, now), nil
}

// signal runs step (3): the strategy's candidate, checked for shape.
func (s *Service) signal(in domain.Instrument, bars []domain.Candle, sizing domain.Cents, l domain.RiskLimits, it *InstrumentTrace) (domain.Candidate, bool) {
	d := s.evaluate(in.ID, bars, sizing, l.MaxShort > 0)
	if d.Candidate == nil {
		return domain.Candidate{}, it.fail(StepSignal, "no signal: %s", d.Reason)
	}
	c := *d.Candidate
	it.Direction = c.Direction
	switch {
	case c.Instrument != in.ID:
		return c, it.fail(StepSignal, "the candidate is for instrument %d", c.Instrument)
	case !c.Direction.Valid():
		return c, it.fail(StepSignal, "unknown direction %q", c.Direction)
	case c.Direction == domain.Short && l.MaxShort <= 0:
		return c, it.fail(StepSignal, "shorts are off for this account (short limit 0)")
	case c.SuggestedSize <= 0 || c.Entry <= 0 || c.StopLossRate <= 0:
		return c, it.fail(StepSignal, "the candidate has no size, entry or stop")
	case c.Direction == domain.Long && c.StopLossRate >= c.Entry, c.Direction == domain.Short && c.StopLossRate <= c.Entry:
		return c, it.fail(StepSignal, "the stop %s is not on the losing side of the entry %s", c.StopLossRate, c.Entry)
	}
	it.pass(StepSignal, c.Playbook+": "+c.Reason)
	return c, true
}

// costGate runs step (4) for size at entry: eToro's costs must stay below the gross gain at
// the take-profit; a failed costs call is a refusal (OD8).
func (s *Service) costGate(bctx context.Context, c domain.Candidate, size domain.Cents, entry domain.Micros, it *InstrumentTrace) bool {
	costs, err := s.d.Broker.Costs(bctx, c.Instrument, c.Direction, size)
	if err != nil {
		return it.fail(StepCosts, "the costs could not be read: %v", err)
	}
	ok, gain, total, why := domain.CostCheck(size, c.Direction, entry, c.TakeProfitRate, costs)
	if !ok {
		return it.fail(StepCosts, "%s", why)
	}
	it.pass(StepCosts, fmt.Sprintf("costs $%s, gross gain at the target $%s (size $%s)", total, gain, size))
	return true
}

// agentReview runs step (5). With reviewing off the size passes unchanged. With it on, any
// error, timeout or unusable answer is a rejection (OD7), and the verdict can only shrink
// the size (domain.ApplyVerdict).
func (s *Service) agentReview(ctx context.Context, a domain.Account, c domain.Candidate, size domain.Cents, bars []domain.Candle, pf domain.Portfolio, it *InstrumentTrace) (domain.Cents, bool) {
	pr, key, on, err := s.reviewerFor(ctx, "")
	switch {
	case !on:
		it.pass(StepReview, "reviewing is off")
		return size, true
	case err != nil:
		return 0, it.fail(StepReview, "the reviewer is unusable: %v", err)
	case s.d.Reviewer == nil:
		return 0, it.fail(StepReview, "reviewing is on but no reviewer is available")
	}
	if err := pr.Validate(); err != nil {
		return 0, it.fail(StepReview, "the reviewer's setup is invalid: %v", err)
	}
	timeout := time.Duration(pr.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = agentReviewTimeout
	}
	recent := bars
	if len(recent) > 30 {
		recent = recent[len(recent)-30:]
	}
	var today domain.Cents
	if rs, err := s.d.Store.RiskStates().Get(ctx, a.ID); err == nil && rs.State.StartOfDayEquity > 0 {
		today = pf.Equity() - rs.State.StartOfDayEquity
	}
	c.SuggestedSize = size
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	v, err := s.d.Reviewer.Review(rctx, pr.LLMConfig, key, ports.ReviewInput{
		Candidate: c, RecentCandles: recent, Positions: slices.Clone(pf.Positions), TodayPnL: today,
	})
	if err == nil && rctx.Err() != nil {
		err = rctx.Err() // an answer after the time limit is not used
	}
	if err != nil {
		return 0, it.fail(StepReview, "no usable answer from %s: %v", pr.Name, err)
	}
	after := domain.ApplyVerdict(size, v)
	if after <= 0 {
		return 0, it.fail(StepReview, "%s rejected it: %s", pr.Name, domain.ClampVerdict(v).Reason)
	}
	it.pass(StepReview, fmt.Sprintf("%s: %s, $%s of $%s", pr.Name, domain.ClampVerdict(v).Kind, after, size))
	return after, true
}

// freshPrices runs step (6): a fresh portfolio and quote; the entry is the ask for a long and
// the bid for a short. The stop must still be on the losing side and the take-profit on the
// winning side of that price.
func (s *Service) freshPrices(bctx context.Context, c domain.Candidate, l domain.RiskLimits, it *InstrumentTrace) (domain.Portfolio, domain.Quote, domain.Micros, bool) {
	pf, err := s.d.Broker.Portfolio(bctx)
	if err != nil {
		return pf, domain.Quote{}, 0, it.fail(StepPrices, "reading the portfolio failed: %v", err)
	}
	qs, err := s.d.Market.Quotes(bctx, []domain.InstrumentID{c.Instrument})
	if err != nil {
		return pf, domain.Quote{}, 0, it.fail(StepPrices, "reading the price failed: %v", err)
	}
	i := slices.IndexFunc(qs, func(q domain.Quote) bool { return q.Instrument == c.Instrument })
	if i < 0 {
		return pf, domain.Quote{}, 0, it.fail(StepPrices, "no price")
	}
	q := qs[i]
	entry := q.Ask
	if c.Direction == domain.Short {
		entry = q.Bid
	}
	now := s.d.Clock.Now()
	switch {
	case q.Delayed:
		return pf, q, 0, it.fail(StepPrices, "the price is delayed")
	case q.At.IsZero() || now.Sub(q.At) > l.MaxQuoteAge || q.At.After(now.Add(time.Second)):
		return pf, q, 0, it.fail(StepPrices, "the price is stale (%s)", q.At.Format(time.RFC3339))
	case entry <= 0:
		return pf, q, 0, it.fail(StepPrices, "no bid/ask")
	case c.Direction == domain.Long && c.StopLossRate >= entry, c.Direction == domain.Short && c.StopLossRate <= entry:
		return pf, q, 0, it.fail(StepPrices, "the price %s has passed the stop %s", entry, c.StopLossRate)
	case c.TakeProfitRate > 0 && (c.Direction == domain.Long && c.TakeProfitRate <= entry || c.Direction == domain.Short && c.TakeProfitRate >= entry):
		return pf, q, 0, it.fail(StepPrices, "the price %s has passed the take-profit %s", entry, c.TakeProfitRate)
	}
	it.pass(StepPrices, fmt.Sprintf("entry %s, cash $%s", entry, pf.AvailableCash))
	return pf, q, entry, true
}

// isBlocked reports whether the instrument is blocked for the account.
func (s *Service) isBlocked(ctx context.Context, accountID string, id domain.InstrumentID) (bool, error) {
	bs, err := s.d.Store.InstrumentBlocks().List(ctx, accountID)
	if err != nil {
		return true, err
	}
	return slices.ContainsFunc(bs, func(b domain.InstrumentBlock) bool { return b.Instrument == id }), nil
}
