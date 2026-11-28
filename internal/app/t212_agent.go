package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/strategy"
)

// The Trading 212 agent (PLAN R45.7, T212-b): the eToro agent's steps on a Trading 212
// practice account, long only. Signals, entry and stop prices come from eToro's data of the
// matched instrument through the account's price source (D42, T212-l); cash, budget, positions
// and orders come from Trading 212. It shares the strategy, the LLM review, the risk engine,
// the decision log and the session rules with the eToro agent, and never touches its path.

// t212Gate says why the Trading 212 agent may not enter now ("" = it may): a practice account
// (a real one never ticks, D41 g), the adapters, a calendar, running, not killed, reconciled,
// the lease, a valid price source, a minimum order value, a catalogue no older than a day, no
// order of ours with an unknown outcome and every position of ours with a confirmed stop.
func (s *Service) t212Gate(ctx context.Context, a domain.Account, l domain.RiskLimits) string {
	switch {
	case !a.T212Trading():
		return "Trading 212 trades practice accounts only (D41 g)"
	case s.d.T212 == nil:
		return "the Trading 212 client is not configured"
	case s.d.Market == nil:
		return "the eToro market data client is not configured (the price source)"
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
	if why := s.leaseProblem(ctx, a.ID); why != "" {
		return why
	}
	if _, err := s.t212PriceSource(ctx, a); err != nil {
		return err.Error()
	}
	if l.MinOrderValue <= 0 {
		return "no minimum order value is set (required on Trading 212)"
	}
	if err := s.freshCatalogue(ctx); err != nil {
		return err.Error()
	}
	return s.t212EntriesBlocked(ctx, a)
}

// t212EntriesBlocked says why no entry may be made on the account because of its orders and
// positions ("" = none): an order of ours with an unknown outcome (T212-d), or a position of
// ours without a confirmed stop (T212-h).
func (s *Service) t212EntriesBlocked(ctx context.Context, a domain.Account) string {
	open, err := s.d.Store.Orders().ListOpen(ctx, a.ID)
	if err != nil {
		return "reading our orders failed"
	}
	for _, o := range open {
		if o.State == domain.OrderUnknown || (o.EtoroOrderID <= 0 && o.State == domain.OrderSent) {
			return fmt.Sprintf("order %s has an unknown outcome: no entries until it is matched or resolved", o.RequestID)
		}
	}
	ours, err := s.d.Store.AgentPositions().ListOpen(ctx, a.ID)
	if err != nil {
		return "reading our positions failed"
	}
	for _, ap := range ours {
		if !slices.ContainsFunc(open, func(o domain.Order) bool {
			return o.Kind == domain.OrderStop && o.PositionID == ap.PositionID && o.State == domain.OrderPending && o.EtoroOrderID > 0
		}) {
			return fmt.Sprintf("the position in %s has no confirmed stop: no entries", ap.Ticker)
		}
	}
	return ""
}

// t212TickLocked is AgentTick on a Trading 212 account, under the account's entry lock. It
// returns why the kill switch must be tripped ("" = it need not).
func (s *Service) t212TickLocked(ctx context.Context, a domain.Account, tr *AgentTrace) string {
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
	l := s.riskLimitsFor(ctx, a)
	if tr.Skipped = s.t212Gate(ctx, a, l); tr.Skipped != "" {
		return ""
	}
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
	pctx, err := s.t212PriceCtx(ctx, a)
	if err != nil {
		tr.Skipped = err.Error()
		return ""
	}
	cfg := s.strategyConfig(ctx, a, l)
	s.markRan(ctx, a, domain.RiskDay(s.d.Clock.Now()))
	acct, err := s.d.T212.Account(ports.WithAccount(ctx, a))
	if err != nil {
		tr.Skipped = "reading the Trading 212 account failed"
		tr.Errors = append(tr.Errors, err.Error())
		return ""
	}
	pf, err := s.t212Portfolio(ctx, a, acct)
	if err != nil {
		tr.Skipped = "reading our positions failed"
		tr.Errors = append(tr.Errors, err.Error())
		return ""
	}
	sizing := min(l.TradingBudget, pf.Equity())
	if sizing <= 0 {
		tr.Skipped = "no equity"
		return ""
	}
	for _, w := range wl {
		if ctx.Err() != nil {
			tr.Errors = append(tr.Errors, "cancelled: "+ctx.Err().Error())
			return ""
		}
		if st := s.agentState(ctx, a.ID); st != domain.AgentRunning {
			tr.Errors = append(tr.Errors, "the agent became "+string(st))
			return ""
		}
		it := InstrumentTrace{Instrument: w.Instrument.ID, Symbol: w.Instrument.Symbol, Market: w.Instrument.Market}
		killWhy := s.t212Consider(ctx, pctx, a, l, cfg, w.Instrument, acct, pf, sizing, &it)
		tr.Instruments = append(tr.Instruments, it)
		if it.DecisionErr != "" {
			tr.Errors = append(tr.Errors, it.DecisionErr)
		}
		if it.Entered {
			tr.Entries++
			// The account changed: the next instrument is sized from a fresh read.
			if acct, err = s.d.T212.Account(ports.WithAccount(ctx, a)); err != nil {
				tr.Errors = append(tr.Errors, "reading the Trading 212 account failed: "+err.Error())
				return ""
			}
			if pf, err = s.t212Portfolio(ctx, a, acct); err != nil {
				tr.Errors = append(tr.Errors, err.Error())
				return ""
			}
			sizing = min(l.TradingBudget, pf.Equity())
		}
		if killWhy != "" {
			return killWhy
		}
		if s.t212EntriesBlocked(ctx, a) != "" {
			return "" // an order became unknown or a position lost its stop: no more entries
		}
	}
	return ""
}

// t212Portfolio is the Trading 212 account as the risk engine weighs it, with our open
// positions and the watchlist's matches (domain.T212Account.Portfolio).
func (s *Service) t212Portfolio(ctx context.Context, a domain.Account, acct domain.T212Account) (domain.Portfolio, error) {
	ours, err := s.d.Store.AgentPositions().ListOpen(ctx, a.ID)
	if err != nil {
		return domain.Portfolio{}, err
	}
	ms, err := s.d.Store.InstrumentMatches().List(ctx, a.ID)
	if err != nil {
		return domain.Portfolio{}, err
	}
	of := map[string]domain.InstrumentID{}
	for _, m := range ms {
		of[m.Ticker] = m.Instrument
	}
	return acct.Portfolio(ours, of), nil
}

// t212Consider takes one watchlist share through the entry steps on Trading 212.
func (s *Service) t212Consider(ctx, pctx context.Context, a domain.Account, l domain.RiskLimits, cfg strategy.Config, in domain.Instrument,
	acct domain.T212Account, pf domain.Portfolio, sizing domain.Cents, it *InstrumentTrace,
) string {
	now := s.d.Clock.Now()
	if _, ok := s.entrySession(in, now, l, it); !ok {
		return ""
	}
	el, m, err := s.t212Eligibility(ctx, a, in, acct.Currency)
	switch {
	case err != nil:
		it.fail(StepSession, "the Trading 212 match cannot be read: %v", err)
		return ""
	case !el.RealLong:
		it.fail(StepSession, "not tradable on Trading 212 %s", el.Reason)
		return ""
	}
	if _, held := acct.Position(m.Ticker); held {
		// T212-i: the owner's shares are never mixed with ours.
		it.fail(StepSession, "Trading 212 already holds %s", m.Ticker)
		return ""
	}
	bars, err := s.d.Market.Candles(pctx, in.ID, AgentBarInterval, agentBars)
	if err != nil {
		it.fail(StepCandles, "no price history from the eToro price source: %v", err)
		return ""
	}
	bars = domain.ClosedBars(bars, AgentBarInterval, now)
	if !domain.BarsFresh(bars, AgentBarInterval, now) {
		it.fail(StepCandles, "the newest closed bar is missing or stale (%d bars)", len(bars))
		return ""
	}
	it.pass(StepCandles, fmt.Sprintf("%d closed bars (eToro)", len(bars)))
	c, ok := s.signal(in, bars, sizing, l, cfg, it)
	if !ok {
		return ""
	}
	if c.Direction != domain.Long {
		it.fail(StepSignal, "long only on Trading 212")
		return ""
	}
	dl := &decisionLog{d: domain.Decision{
		ID: s.d.Random.ID(), AccountID: a.ID, At: now.UTC(), Signal: domain.SignalFromCandidate(c, in.Symbol),
	}}
	killWhy := s.t212Decide(ctx, pctx, a, l, in, el, m, acct, c, sizing, bars, pf, dl, it)
	s.finishDecision(ctx, dl, it)
	return killWhy
}

// t212Quote reads eToro's quote of the instrument through the price source and checks it is
// live, fresh and on the right side of the candidate's stop and take-profit.
func (s *Service) t212Quote(pctx context.Context, c domain.Candidate, l domain.RiskLimits, it *InstrumentTrace, step TraceStep) (domain.Quote, bool) {
	qs, err := s.d.Market.Quotes(pctx, []domain.InstrumentID{c.Instrument})
	if err != nil {
		return domain.Quote{}, it.fail(step, "reading the eToro price failed: %v", err)
	}
	i := slices.IndexFunc(qs, func(q domain.Quote) bool { return q.Instrument == c.Instrument })
	if i < 0 {
		return domain.Quote{}, it.fail(step, "no eToro price")
	}
	q, now := qs[i], s.d.Clock.Now()
	switch {
	case q.Delayed:
		return q, it.fail(step, "the price is delayed")
	case q.At.IsZero() || now.Sub(q.At) > l.MaxQuoteAge || q.At.After(now.Add(time.Second)):
		return q, it.fail(step, "the price is stale (%s)", q.At.Format(time.RFC3339))
	case q.Ask <= 0 || q.Bid <= 0 || q.Bid > q.Ask:
		return q, it.fail(step, "no usable bid/ask")
	case c.StopLossRate >= q.Ask:
		return q, it.fail(step, "the price %s has passed the stop %s", q.Ask, c.StopLossRate)
	case c.TakeProfitRate > 0 && c.TakeProfitRate <= q.Ask:
		return q, it.fail(step, "the price %s has passed the take-profit %s", q.Ask, c.TakeProfitRate)
	}
	return q, true
}

// t212CostGate weighs the costs of quantity at the quote (T212-k).
func (s *Service) t212CostGate(in domain.Instrument, c domain.Candidate, q domain.Quote, quantity domain.Micros, ccy, accountCcy string, it *InstrumentTrace) bool {
	cost := domain.T212CostCheck(domain.T212CostInput{
		Market: in.Market, Quantity: quantity, Bid: q.Bid, Ask: q.Ask, TakeProfit: c.TakeProfitRate,
		InstrumentCurrency: ccy, AccountCurrency: accountCcy,
	})
	if !cost.Allowed {
		return it.fail(StepCosts, "%s", cost.Reason)
	}
	it.pass(StepCosts, fmt.Sprintf("costs %s, gross gain at the target %s (%s shares)", domain.FormatMoney(accountCcy, cost.Cost), domain.FormatMoney(accountCcy, cost.Gain), quantity))
	return true
}

// t212Decide runs the entry steps after the signal: costs, the LLM review, fresh Trading 212
// figures and a fresh eToro ask, reservations, the risk engine, the quantity, and the buy.
func (s *Service) t212Decide(ctx, pctx context.Context, a domain.Account, l domain.RiskLimits, in domain.Instrument, el domain.Eligibility,
	m domain.InstrumentMatch, acct domain.T212Account, c domain.Candidate, sizing domain.Cents, bars []domain.Candle, pf domain.Portfolio,
	dl *decisionLog, it *InstrumentTrace,
) string {
	reject := func(o domain.DecisionOutcome) string {
		dl.end(o, lastDetail(it))
		return ""
	}
	t, err := s.d.Store.T212Instruments().Get(ctx, m.Ticker)
	if err != nil {
		it.fail(StepCosts, "the Trading 212 instrument %s cannot be read: %v", m.Ticker, err)
		return reject(domain.DecisionRejectedCost)
	}
	frac := l.T212Fractional && domain.T212FractionsVerified
	size := min(c.SuggestedSize, sizing)
	// (4) Costs below the gross gain at the take-profit (T212-k), at eToro's quote.
	q0, ok := s.t212Quote(pctx, c, l, it, StepCosts)
	if !ok {
		return reject(domain.DecisionRejectedCost)
	}
	per0, err := domain.T212MinorPerShare(q0.Ask, t.Currency, acct.Currency)
	if err != nil {
		it.fail(StepCosts, "%v", err)
		return reject(domain.DecisionRejectedCost)
	}
	if !s.t212CostGate(in, c, q0, domain.T212Quantity(size, per0, frac), t.Currency, acct.Currency, it) {
		return reject(domain.DecisionRejectedCost)
	}
	// (5) The LLM reviewer: it can only shrink or reject.
	size, ok = s.agentReview(ctx, a, c, size, bars, pf, dl, it)
	if !ok {
		dl.end(domain.DecisionRejectedReviewer, "")
		return ""
	}
	// (6) Fresh Trading 212 figures and a fresh eToro ask.
	acct, err = s.d.T212.Account(ports.WithAccount(ctx, a))
	if err != nil {
		it.fail(StepPrices, "reading the Trading 212 account failed: %v", err)
		return reject(domain.DecisionRejectedRisk)
	}
	fresh, err := s.t212Portfolio(ctx, a, acct)
	if err != nil {
		it.fail(StepPrices, "reading our positions failed: %v", err)
		return reject(domain.DecisionRejectedRisk)
	}
	q, ok := s.t212Quote(pctx, c, l, it, StepPrices)
	if !ok {
		return reject(domain.DecisionRejectedRisk)
	}
	per, err := domain.T212MinorPerShare(q.Ask, t.Currency, acct.Currency)
	if err != nil {
		it.fail(StepPrices, "%v", err)
		return reject(domain.DecisionRejectedRisk)
	}
	it.pass(StepPrices, fmt.Sprintf("entry %s (eToro ask), cash %s", q.Ask, domain.FormatMoney(acct.Currency, acct.AvailableToTrade)))
	now := s.d.Clock.Now()
	sess, ok := s.sessionNow(in, now, it)
	if !ok {
		return reject(domain.DecisionRejectedRisk)
	}
	// (7) Our unfinished orders hold cash, and so do fills the account does not show yet.
	res, err := s.reservations(ctx, a.ID, fresh, now)
	if err != nil {
		it.fail(StepReservations, "reading our orders failed: %v", err)
		return reject(domain.DecisionRejectedRisk)
	}
	it.pass(StepReservations, fmt.Sprintf("%s reserved by %d order(s)", domain.FormatMoney(acct.Currency, res.Amount), res.Count))
	// (8) The risk engine on the fresh equity.
	rs, err := s.observe(ctx, a, fresh, l, now)
	if err != nil {
		it.fail(StepRisk, "storing the risk state failed: %v", err)
		return reject(domain.DecisionRejectedRisk)
	}
	blocked, err := s.isBlocked(ctx, a.ID, in.ID)
	if err != nil {
		it.fail(StepRisk, "reading the instrument blocks failed: %v", err)
		return reject(domain.DecisionRejectedRisk)
	}
	rin := res.Apply(domain.RiskInput{
		Now: now, Mode: a.Mode, AccountID: a.ID, SessionOpen: sess.Open, SessionClose: sess.Close, Trading: sess.Trading,
		Portfolio: fresh, Instrument: in.ID, Direction: domain.Long, Eligibility: el,
		Entry: q.Ask, QuoteAt: q.At, StopLoss: c.StopLossRate, Requested: size, Limits: l, State: rs.State,
		Reconciled: s.Reconciled(a.ID).OK, Blocked: blocked,
	})
	dec := domain.AssessEntry(rin)
	if dec.Kill || !dec.Allowed {
		it.fail(StepRisk, "%s", dec.Reason)
		dl.d.Risk = domain.DecisionRisk{Reason: dec.Reason}
		dl.end(domain.DecisionRejectedRisk, "")
		if dec.Kill {
			return dec.Reason
		}
		return ""
	}
	// The quantity: whole shares (unless switched), the reservation within the allowed size.
	qty := domain.T212Quantity(dec.Size, per, frac)
	reserve := domain.T212Reserve(qty, per)
	value := domain.T212OrderValue(qty, per)
	held := domain.Micros(0)
	if p, ok := acct.Position(m.Ticker); ok {
		held = p.Quantity
	}
	why := ""
	switch {
	case qty <= 0:
		why = fmt.Sprintf("%s buys no whole share at %s", domain.FormatMoney(acct.Currency, dec.Size), q.Ask)
	case reserve > dec.Size:
		why = "the reservation exceeds the allowed size" // never: T212Reserve ≤ the size it came from
	case value < l.MinOrderValue:
		why = fmt.Sprintf("the order value %s is below the minimum order value %s", domain.FormatMoney(acct.Currency, value), domain.FormatMoney(acct.Currency, l.MinOrderValue))
	case held+qty > t.MaxOpenQuantity:
		why = fmt.Sprintf("%s more shares would exceed Trading 212's maximum %s", qty, t.MaxOpenQuantity)
	}
	if why != "" {
		it.fail(StepRisk, "%s", why)
		dl.d.Risk = domain.DecisionRisk{Reason: why}
		dl.end(domain.DecisionRejectedRisk, "")
		return ""
	}
	dl.d.Risk = domain.DecisionRisk{Allowed: true, Size: reserve, Reason: fmt.Sprintf("allowed %s shares (%s reserved) of %s requested",
		qty, domain.FormatMoney(acct.Currency, reserve), domain.FormatMoney(acct.Currency, size))}
	// The cost gate again on the final quantity and the fresh quote.
	if !s.t212CostGate(in, c, q, qty, t.Currency, acct.Currency, it) {
		return reject(domain.DecisionRejectedCost)
	}
	it.pass(StepRisk, dl.d.Risk.Reason)
	// (9) The decision first (pending, with the request id), then the order row, then the buy.
	if st := s.agentState(ctx, a.ID); st != domain.AgentRunning {
		it.fail(StepOrder, "the agent became %s", st)
		dl.end(domain.DecisionOrderFailed, "not sent: the agent became "+string(st))
		return ""
	}
	dl.d.Outcome, dl.d.OrderRequestID = domain.DecisionPending, s.d.Random.ID()
	if err := s.d.Store.Decisions().Add(ctx, dl.d); err != nil {
		it.fail(StepOrder, "storing the decision failed, nothing sent: %v", err)
		dl.d.OrderRequestID = ""
		dl.end(domain.DecisionOrderFailed, "not sent: the decision could not be stored first")
		return ""
	}
	dl.stored = true
	o, err := s.placeT212Entry(ctx, a, t212Entry{
		Instrument: in.ID, Ticker: m.Ticker, Quantity: qty, Amount: reserve, Entry: q.Ask,
		StopLoss: c.StopLossRate, TakeProfit: c.TakeProfitRate, AccountAt: acct.AsOf, RequestID: dl.d.OrderRequestID,
	}, l)
	it.RequestID, it.OrderState, it.Amount = o.RequestID, o.State, o.Amount
	it.Entered = o.RequestID != "" && o.Sends > 0
	dl.d.OrderRequestID = o.RequestID
	if it.Entered {
		dl.end(domain.DecisionOrdered, "")
	} else {
		w := "not sent"
		if err != nil {
			w += ": " + err.Error()
		}
		dl.end(domain.DecisionOrderFailed, w)
	}
	if err != nil && !errors.Is(err, domain.ErrOutcomeUnknown) {
		it.fail(StepOrder, "%v", err)
		return ""
	}
	it.pass(StepOrder, fmt.Sprintf("buy %s %s, order %s is %s", qty, m.Ticker, o.RequestID, o.State))
	return ""
}

// t212Startable says why the agent of a Trading 212 account may not run (nil = it may): a
// practice account (a real one is view-only, D41 g), the adapters, keys, not killed, valid
// limits for Trading 212 with a budget, reconciled, a valid price source and a catalogue no
// older than a day (T212-l, T212-m).
func (s *Service) t212Startable(ctx context.Context, a domain.Account) error {
	switch {
	case !a.T212Trading():
		return fmt.Errorf("%s: Trading 212 trades practice accounts only: %w", a.Name, domain.ErrViewOnly)
	case s.d.T212 == nil:
		return s.needT212()
	case s.d.Market == nil:
		return fmt.Errorf("the eToro market data client is not configured (the price source): %w", domain.ErrUnavailable)
	case !s.hasKeys(ctx, a):
		return domain.MissingSetup(domain.MissingAccountKeys, "The account's Trading 212 keys are not set", domain.ErrCredentialsMissing)
	}
	switch killed, err := s.isKilled(ctx, a.ID); {
	case err != nil:
		return fmt.Errorf("reading the kill switch: %w: %w", err, domain.ErrUnavailable)
	case killed:
		return domain.MissingSetup(domain.MissingKillReset, msgKilled, domain.ErrConflict)
	}
	l := s.riskLimitsFor(ctx, a)
	if err := l.Validate(); err != nil {
		return err
	}
	if err := l.ValidateForPlatform(domain.PlatformTrading212); err != nil {
		return err
	}
	if l.TradingBudget <= 0 {
		return domain.MissingSetup(domain.MissingTradingBudget, msgNoBudget, domain.Invalid("tradingBudgetCents", msgNoBudget))
	}
	if rc := s.Reconciled(a.ID); !rc.OK {
		return domain.MissingSetup(domain.MissingReconcile, fmt.Sprintf("The account is not reconciled (%s)", rc.Reason), domain.ErrConflict)
	}
	if _, err := s.t212PriceSource(ctx, a); err != nil {
		return err
	}
	return s.freshCatalogue(ctx)
}

// resetEquity is the equity a kill reset takes as the new high-water mark: eToro's portfolio,
// or the Trading 212 practice account as the risk engine weighs it.
func (s *Service) resetEquity(ctx context.Context, v domain.AccountView) (domain.Cents, error) {
	var eq domain.Cents
	if v.T212Trading() {
		if err := s.needT212(); err != nil {
			return 0, err
		}
		acct, err := s.d.T212.Account(ports.WithAccount(ctx, v.Account))
		if err != nil {
			return 0, fmt.Errorf("app.ResetKill: reading the equity for the new high-water mark: %w", err)
		}
		pf, err := s.t212Portfolio(ctx, v.Account, acct)
		if err != nil {
			return 0, fmt.Errorf("app.ResetKill: %w", err)
		}
		eq = pf.Equity()
	} else {
		if err := s.needBroker(); err != nil {
			return 0, err
		}
		pf, err := s.d.Broker.Portfolio(brokerCtx(ctx, v))
		if err != nil {
			return 0, fmt.Errorf("app.ResetKill: reading the equity for the new high-water mark: %w", err)
		}
		eq = pf.Equity()
	}
	if eq <= 0 {
		return 0, fmt.Errorf("app.ResetKill: the account has no equity: %w", domain.ErrConflict)
	}
	return eq, nil
}
