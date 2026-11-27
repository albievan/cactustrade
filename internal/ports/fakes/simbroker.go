package fakes

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// SimOutcome scripts what the simulated eToro account does with one request.
type SimOutcome string

// Scripted outcomes. Open uses all of them but SimPartialClose; Close uses SimFill,
// SimPending, SimLostAnswer, SimRateLimited, SimServerError, SimRefused and SimPartialClose.
const (
	// SimFill: accepted, and the lookup reports it filled (real/long/x1 or cfd/short/x1 with
	// the stop sent).
	SimFill SimOutcome = "fill"
	// SimPendingThenFill: accepted; the first PendingLookups lookups report pending, then
	// it fills.
	SimPendingThenFill SimOutcome = "pending_then_fill"
	// SimPending: accepted and pending until cancelled.
	SimPending SimOutcome = "pending"
	// SimReject: accepted, then rejected by eToro.
	SimReject SimOutcome = "reject"
	// SimPartial: accepted; half the amount fills, the rest pends until cancelled.
	SimPartial SimOutcome = "partial"
	// SimLostAnswer: accepted and filled, but the answer is lost (domain.ErrOutcomeUnknown).
	SimLostAnswer SimOutcome = "lost_answer"
	// SimRateLimited: a 429 before processing; nothing is recorded at the broker.
	SimRateLimited SimOutcome = "rate_limited"
	// SimServerError: a 5xx and the broker never recorded it: ErrOutcomeUnknown, lookup 404.
	SimServerError SimOutcome = "server_error"
	// SimRefused: a 4xx (e.g. validation); nothing is recorded at the broker.
	SimRefused SimOutcome = "refused"
	// SimLeverage2: filled at leverage 2 (a non-conforming fill).
	SimLeverage2 SimOutcome = "leverage_2"
	// SimWrongSettlement: a long filled as a CFD, a short as the real share (non-conforming).
	SimWrongSettlement SimOutcome = "wrong_settlement"
	// SimNoStop: filled without the stop-loss that was sent.
	SimNoStop SimOutcome = "no_stop"
	// SimFilledNoExecutions: filled, but the first NoExecLookups lookups report it filled
	// (final) without any positionExecutions.
	SimFilledNoExecutions SimOutcome = "filled_no_executions"
	// SimRateLimitedProcessed: answered with a 429, but eToro processed it anyway (U7): the
	// order exists and fills.
	SimRateLimitedProcessed SimOutcome = "rate_limited_processed"
	// SimPartialClose (Close only): the close order ends partly filled and final (status
	// 9/10): half the position is closed, the rest stays open.
	SimPartialClose SimOutcome = "partial_close"
)

// SimRetryAfter is the Retry-After the simulated 429 asks for.
const SimRetryAfter = 5 * time.Second

// SimBroker is a simulated eToro demo account for executor tests: a ports.Broker that
// accepts, fills, rejects or loses orders as scripted, keeps cash and positions, and records
// every request. It refuses a real-money account change without a valid permit like the eToro
// client (ErrRealModeGated).
// Set the exported fields before use; read the records under no concurrent calls.
type SimBroker struct {
	mu sync.Mutex
	// Now stamps the portfolio and fills (default time.Now).
	Now func() time.Time
	// Cash is the available cash; Positions are open positions (including foreign ones).
	Cash      domain.Cents
	Positions []domain.Position
	// Prices are the fill prices per instrument; without one a long fills 1 % above its
	// stop and a short 1 % below it.
	Prices map[domain.InstrumentID]domain.Micros
	// OpenScript and CloseScript are consumed one outcome per Open / Close call; when empty
	// the outcome is SimFill.
	OpenScript  []SimOutcome
	CloseScript []SimOutcome
	// PendingLookups is how many lookups a SimPendingThenFill order stays pending (default 1).
	PendingLookups int
	// NoExecLookups is how many lookups a SimFilledNoExecutions order reports no executions
	// (default 1).
	NoExecLookups int
	// LookupLag: an accepted order is unknown to Lookup (ErrNotFound) for this long (U2).
	LookupLag time.Duration
	// PortfolioLag: a position opened less than this long ago is missing from Portfolio, and
	// the cash it took is still shown as available (U11). Actual shows the account as it is.
	PortfolioLag time.Duration
	// RefLookupNotFound: a lookup by request id (orderID 0) is always 404; only a lookup by
	// eToro's order id finds an order. eToro's demo answered so on 2026-11-24.
	RefLookupNotFound bool
	// IgnoreCancels: a cancel is accepted (200) and the order is left as it is (eToro
	// "accepted" cancels of orders that had already filled, 2026-11-24).
	IgnoreCancels bool
	// DuplicateFills: an Open with a request id the broker already has is processed again and
	// fills a second time (eToro not deduplicating, U1). It is still recorded as a Violation.
	DuplicateFills bool
	// OnOpen runs at the start of every Open, e.g. RequireOrderRow: an error is recorded in
	// Violations (the order is still processed, so the test sees the whole flow).
	OnOpen func(ctx context.Context, in domain.OrderIntent) error
	// ReadErr makes every read (Portfolio, Lookup, CloseOrder, Eligibility, Costs,
	// TradeHistory) fail; PortfolioErr only Portfolio.
	ReadErr      error
	PortfolioErr error
	// History is the trade history TradeHistory returns (the trades closed at or after its
	// since).
	History []domain.ClosedTrade

	// Records.
	Intents        []domain.OrderIntent // every Open call, in order
	Opens          int
	Closes         int
	Cancels        int
	Lookups        int
	CloseRequests  []string // request ids of Close calls, in order
	CancelRequests []string
	// Outcomes counts every scripted outcome used (Open and Close).
	Outcomes map[SimOutcome]int
	// LaggedLookups counts lookups answered 404 because of LookupLag; HiddenFills the
	// positions left out of a Portfolio answer because of PortfolioLag; Duplicates the opens
	// with a request id the broker already had.
	LaggedLookups int
	HiddenFills   int
	Duplicates    int
	// RefNotFound counts lookups by request id answered 404 because of RefLookupNotFound.
	RefNotFound int
	// Violations lists broken rules: OnOpen failures, an Open that reused a request id the
	// broker already had, a call without a demo eToro account.
	Violations []string

	nextID int64
	orders map[string]*simOrder // by request id
	byID   map[int64]*simOrder  // by eToro order id
	closes map[int64]domain.OrderStatus
}

type simOrder struct {
	intent   domain.OrderIntent
	outcome  SimOutcome
	status   domain.OrderStatus
	lookups  int
	accepted time.Time
}

var _ ports.Broker = (*SimBroker)(nil)

func (b *SimBroker) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

func (b *SimBroker) id() int64 {
	b.nextID++
	return 9_000 + b.nextID
}

func (b *SimBroker) init() {
	if b.orders == nil {
		b.orders, b.byID, b.closes = map[string]*simOrder{}, map[int64]*simOrder{}, map[int64]domain.OrderStatus{}
	}
	if b.Outcomes == nil {
		b.Outcomes = map[SimOutcome]int{}
	}
}

// gate refuses like the eToro client (S4, PLAN 7.20): no account or not eToro; an account
// without a mode; a real-money account change ("open", "close", "cancel") without a permit in
// ctx for the account and the operation (open: an entry permit valid at b's now; close and
// cancel: an exit or entry permit). Reads of a real-money account are allowed.
func (b *SimBroker) gate(ctx context.Context, op string) error {
	a, ok := ports.AccountFrom(ctx)
	switch {
	case !ok || a.ID == "" || !a.Tradable():
		b.Violations = append(b.Violations, op+" without an eToro account")
		return domain.ErrUnavailable
	case a.Mode == domain.ModeDemo:
		return nil
	case a.Mode != domain.ModeReal:
		b.Violations = append(b.Violations, op+" for an account without a mode")
		return domain.ErrRealModeGated
	}
	p, _ := ports.PermitFrom(ctx)
	switch op {
	case "open":
		if p.AllowsEntry(a.ID, b.now()) {
			return nil
		}
	case "close", "cancel":
		if p.AllowsExit(a.ID) {
			return nil
		}
	default:
		return nil // a read
	}
	b.Violations = append(b.Violations, op+" for a real-money account without a valid permit")
	return domain.ErrNoRealPermit
}

func (b *SimBroker) nextOutcome(script *[]SimOutcome) SimOutcome {
	o := SimFill
	if len(*script) > 0 {
		o = (*script)[0]
		*script = (*script)[1:]
	}
	b.Outcomes[o]++
	return o
}

// Open accepts, refuses or loses the order as OpenScript says.
func (b *SimBroker) Open(ctx context.Context, in domain.OrderIntent) (domain.OrderStatus, error) {
	if b.OnOpen != nil {
		if err := b.OnOpen(ctx, in); err != nil {
			b.mu.Lock()
			b.Violations = append(b.Violations, fmt.Sprintf("open %s: %v", in.RequestID, err))
			b.mu.Unlock()
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	b.Opens++
	b.Intents = append(b.Intents, in)
	if err := b.gate(ctx, "open"); err != nil {
		return domain.OrderStatus{}, err
	}
	if _, dup := b.orders[in.RequestID]; dup {
		b.Duplicates++
		b.Violations = append(b.Violations, "open resent request id "+in.RequestID+" that the broker already has")
		if !b.DuplicateFills {
			return domain.OrderStatus{}, &domain.NotAcceptedError{Reason: "duplicate request id (simulated)"}
		}
		// eToro does not deduplicate (U1): a second order under the same id, and it fills.
		o := &simOrder{intent: in, outcome: SimFill, accepted: b.now(), status: domain.OrderStatus{
			RequestID: in.RequestID, OrderID: b.id(), State: domain.OrderPending, Instrument: in.Instrument,
		}}
		b.byID[o.status.OrderID] = o
		b.fill(o, in.Amount, true)
		return domain.OrderStatus{RequestID: in.RequestID, OrderID: o.status.OrderID, State: domain.OrderPending}, nil
	}
	if !in.Direction.Valid() || in.StopLossRate <= 0 || in.Amount <= 0 || in.Instrument <= 0 {
		return domain.OrderStatus{}, &domain.NotAcceptedError{Reason: "invalid order (simulated)"}
	}
	out := b.nextOutcome(&b.OpenScript)
	switch out {
	case SimRateLimited:
		return domain.OrderStatus{}, &domain.NotAcceptedError{RateLimited: true, RetryAfter: SimRetryAfter, Reason: "too many requests (simulated)"}
	case SimRefused:
		return domain.OrderStatus{}, &domain.NotAcceptedError{Reason: "refused (simulated)"}
	case SimServerError:
		return domain.OrderStatus{}, fmt.Errorf("simulated 500: %w", domain.ErrOutcomeUnknown)
	}
	o := &simOrder{intent: in, outcome: out, accepted: b.now(), status: domain.OrderStatus{
		RequestID: in.RequestID, OrderID: b.id(), State: domain.OrderPending, Instrument: in.Instrument,
	}}
	b.orders[in.RequestID], b.byID[o.status.OrderID] = o, o
	switch out {
	case SimFill, SimLostAnswer, SimLeverage2, SimWrongSettlement, SimNoStop, SimFilledNoExecutions, SimRateLimitedProcessed:
		b.fill(o, in.Amount, true)
	case SimPartial:
		b.fill(o, in.Amount/2, false)
	case SimReject:
		o.status.State, o.status.Final, o.status.Reason = domain.OrderRejected, true, "rejected (simulated)"
	}
	switch out {
	case SimLostAnswer:
		return domain.OrderStatus{}, fmt.Errorf("simulated lost answer: %w", domain.ErrOutcomeUnknown)
	case SimRateLimitedProcessed:
		return domain.OrderStatus{}, &domain.NotAcceptedError{RateLimited: true, RetryAfter: SimRetryAfter, Reason: "too many requests (simulated, but processed)"}
	}
	return domain.OrderStatus{RequestID: in.RequestID, OrderID: o.status.OrderID, State: domain.OrderPending}, nil
}

// fill opens a position for amount of the order; complete makes the order filled (final),
// otherwise it is partially filled and the rest still pends.
func (b *SimBroker) fill(o *simOrder, amount domain.Cents, complete bool) {
	in := o.intent
	price, ok := b.Prices[in.Instrument]
	if !ok || price <= 0 {
		price = in.StopLossRate * 101 / 100
		if in.Direction == domain.Short {
			price = in.StopLossRate * 99 / 100
		}
	}
	long := in.Direction == domain.Long
	settlement, isReal := domain.SettlementCFD, false
	if long {
		settlement, isReal = domain.SettlementReal, true
	}
	lev, stop := 1, in.StopLossRate
	switch o.outcome {
	case SimLeverage2:
		lev = 2
	case SimWrongSettlement:
		isReal = !isReal
		settlement = map[bool]domain.Settlement{true: domain.SettlementReal, false: domain.SettlementCFD}[isReal]
	case SimNoStop:
		stop = 0
	}
	units := domain.Micros(math.Round(float64(amount) / 100 / (float64(price) / 1e6) * 1e6))
	pos := domain.Position{
		ID: b.id(), Instrument: in.Instrument, OpenedAt: b.now(), OpenRate: price, Units: units, Amount: amount,
		StopLossRate: stop, TakeProfitRate: in.TakeProfitRate, RealAsset: isReal, CFD: !isReal, Long: long, Leverage: lev,
	}
	b.Positions = append(b.Positions, pos)
	b.Cash -= amount
	o.status.Settlement, o.status.Leverage, o.status.Side = settlement, lev, in.Direction
	o.status.Executions = append(o.status.Executions, domain.PositionExecution{
		PositionID: pos.ID, Units: units, Amount: amount, OpenRate: price, StopLossRate: stop,
	})
	o.status.State, o.status.Final = domain.OrderPartial, false
	if complete {
		o.status.State, o.status.Final = domain.OrderFilled, true
	}
}

// Lookup reports a known order by eToro's order id (orderID > 0) or else by request id
// (ErrNotFound for one the broker never recorded, and for every lookup by request id with
// RefLookupNotFound).
func (b *SimBroker) Lookup(ctx context.Context, requestID string, orderID int64) (domain.OrderStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	b.Lookups++
	if err := b.gate(ctx, "lookup"); err != nil && !errors.Is(err, domain.ErrRealModeGated) {
		return domain.OrderStatus{}, err
	}
	if b.ReadErr != nil {
		return domain.OrderStatus{}, b.ReadErr
	}
	var (
		o  *simOrder
		ok bool
	)
	switch {
	case orderID > 0:
		o, ok = b.byID[orderID]
	case b.RefLookupNotFound:
		b.RefNotFound++
	default:
		o, ok = b.orders[requestID]
	}
	if ok && b.now().Sub(o.accepted) < b.LookupLag {
		b.LaggedLookups++
		ok = false
	}
	if !ok {
		return domain.OrderStatus{RequestID: requestID, OrderID: orderID, State: domain.OrderUnknown}, fmt.Errorf("lookup %s/%d: %w", requestID, orderID, domain.ErrNotFound)
	}
	o.lookups++
	if o.outcome == SimPendingThenFill && o.status.State == domain.OrderPending && o.lookups > max(b.PendingLookups, 1) {
		b.fill(o, o.intent.Amount, true)
	}
	st := clone(o.status)
	if o.outcome == SimFilledNoExecutions && o.lookups <= max(b.NoExecLookups, 1) {
		st.Executions = nil
	}
	return st, nil
}

func clone(s domain.OrderStatus) domain.OrderStatus {
	s.Executions = slices.Clone(s.Executions)
	return s
}

// Cancel cancels a pending order (a partly filled one keeps its fill); cancelling a final
// order does nothing, like eToro.
func (b *SimBroker) Cancel(ctx context.Context, orderID int64, requestID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	b.Cancels++
	b.CancelRequests = append(b.CancelRequests, requestID)
	if err := b.gate(ctx, "cancel"); err != nil {
		return err
	}
	o, ok := b.byID[orderID]
	if !ok {
		return &domain.NotAcceptedError{Reason: "order not found (simulated)", Err: domain.ErrNotFound}
	}
	switch {
	case o.status.Final, b.IgnoreCancels:
	case o.status.State == domain.OrderPartial:
		o.status.Final, o.status.Reason = true, "cancelled after a partial fill (simulated)"
	default:
		o.status.State, o.status.Final, o.status.Reason = domain.OrderCancelled, true, "cancelled (simulated)"
	}
	return nil
}

// Close closes a whole position as CloseScript says.
func (b *SimBroker) Close(ctx context.Context, positionID int64, instrument domain.InstrumentID, requestID string) (domain.OrderStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	b.Closes++
	b.CloseRequests = append(b.CloseRequests, requestID)
	if err := b.gate(ctx, "close"); err != nil {
		return domain.OrderStatus{}, err
	}
	out := b.nextOutcome(&b.CloseScript)
	switch out {
	case SimRateLimited:
		return domain.OrderStatus{}, &domain.NotAcceptedError{RateLimited: true, RetryAfter: SimRetryAfter, Reason: "too many requests (simulated)"}
	case SimRefused:
		return domain.OrderStatus{}, &domain.NotAcceptedError{Reason: "refused (simulated)"}
	case SimServerError:
		return domain.OrderStatus{}, fmt.Errorf("simulated 500: %w", domain.ErrOutcomeUnknown)
	}
	i := slices.IndexFunc(b.Positions, func(p domain.Position) bool { return p.ID == positionID })
	if i < 0 || b.Positions[i].Instrument != instrument {
		return domain.OrderStatus{}, &domain.NotAcceptedError{Reason: "position not found (simulated)", Err: domain.ErrNotFound}
	}
	p := b.Positions[i]
	st := domain.OrderStatus{OrderID: b.id(), State: domain.OrderPending, Instrument: instrument}
	switch out {
	case SimPending:
	case SimPartialClose:
		// Half closed, the rest stays open; eToro reports the close order partly filled and
		// final (status 9/10).
		half := domain.Position{Amount: p.Amount / 2, Units: p.Units / 2, UnrealisedPnL: p.UnrealisedPnL / 2}
		b.Positions[i].Amount -= half.Amount
		b.Positions[i].Units -= half.Units
		b.Positions[i].UnrealisedPnL -= half.UnrealisedPnL
		b.Cash += half.Amount + half.UnrealisedPnL
		st.State, st.Final = domain.OrderPartial, true
		st.Executions = []domain.PositionExecution{{PositionID: p.ID, Units: half.Units, Amount: half.Amount + half.UnrealisedPnL, CloseRate: p.OpenRate}}
	default:
		b.Positions = slices.Delete(b.Positions, i, i+1)
		b.Cash += p.Amount + p.UnrealisedPnL
		st.State, st.Final = domain.OrderFilled, true
		st.Executions = []domain.PositionExecution{{PositionID: p.ID, Units: p.Units, Amount: p.Amount + p.UnrealisedPnL, CloseRate: p.OpenRate}}
	}
	b.closes[st.OrderID] = st
	if out == SimLostAnswer {
		return domain.OrderStatus{}, fmt.Errorf("simulated lost answer: %w", domain.ErrOutcomeUnknown)
	}
	return domain.OrderStatus{OrderID: st.OrderID, State: domain.OrderPending, Instrument: instrument}, nil
}

// CloseOrder reports a close order.
func (b *SimBroker) CloseOrder(ctx context.Context, orderID int64) (domain.OrderStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if err := b.gate(ctx, "close order"); err != nil && !errors.Is(err, domain.ErrRealModeGated) {
		return domain.OrderStatus{}, err
	}
	if b.ReadErr != nil {
		return domain.OrderStatus{}, b.ReadErr
	}
	st, ok := b.closes[orderID]
	if !ok {
		return domain.OrderStatus{}, fmt.Errorf("close order %d: %w", orderID, domain.ErrNotFound)
	}
	return clone(st), nil
}

// Portfolio returns the cash, the positions and the money held by pending open orders, as
// eToro would show them: with PortfolioLag, a fill younger than the lag is missing and the
// cash it took is still shown as available.
func (b *SimBroker) Portfolio(ctx context.Context) (domain.Portfolio, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if err := b.gate(ctx, "portfolio"); err != nil && !errors.Is(err, domain.ErrRealModeGated) {
		return domain.Portfolio{}, err
	}
	if err := firstErr(b.ReadErr, b.PortfolioErr); err != nil {
		return domain.Portfolio{}, err
	}
	p := b.actual()
	if b.PortfolioLag > 0 {
		now := b.now()
		p.Positions = slices.DeleteFunc(p.Positions, func(pos domain.Position) bool {
			hidden := !pos.OpenedAt.IsZero() && now.Sub(pos.OpenedAt) < b.PortfolioLag
			if hidden {
				b.HiddenFills++
				p.AvailableCash += pos.Amount
			}
			return hidden
		})
	}
	return p, nil
}

// Actual returns the account as it really is (no PortfolioLag), for a test's invariants.
func (b *SimBroker) Actual() domain.Portfolio {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	return b.actual()
}

func (b *SimBroker) actual() domain.Portfolio {
	p := domain.Portfolio{AvailableCash: b.Cash, Positions: slices.Clone(b.Positions), AsOf: b.now()}
	for _, o := range b.orders {
		if !o.status.Final {
			var filled domain.Cents
			for _, e := range o.status.Executions {
				filled += e.Amount
			}
			p.PendingOpen += o.intent.Amount - filled
		}
	}
	return p
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// Eligibility says every instrument may be opened long (real) and short (CFD) at leverage 1.
func (b *SimBroker) Eligibility(_ context.Context, ids []domain.InstrumentID) ([]domain.Eligibility, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ReadErr != nil {
		return nil, b.ReadErr
	}
	out := make([]domain.Eligibility, 0, len(ids))
	for _, id := range ids {
		out = append(out, domain.Eligibility{Instrument: id, RealLong: true, ShortCFD: true, CheckedAt: b.now()})
	}
	return out, nil
}

// Costs returns one spread line of 0.10 USD.
func (b *SimBroker) Costs(context.Context, domain.InstrumentID, domain.Direction, domain.Cents) ([]domain.CostItem, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ReadErr != nil {
		return nil, b.ReadErr
	}
	return []domain.CostItem{{Type: "marketSpread", Value: 100_000, Currency: "USD"}}, nil
}

// TradeHistory returns the History trades closed at or after since.
func (b *SimBroker) TradeHistory(_ context.Context, since time.Time) ([]domain.ClosedTrade, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ReadErr != nil {
		return nil, b.ReadErr
	}
	var out []domain.ClosedTrade
	for _, t := range b.History {
		if !t.ClosedAt.Before(since) {
			out = append(out, t)
		}
	}
	return out, nil
}

// RequireOrderRow is an OnOpen hook: the order must already be persisted as an open order
// in state sent, with exactly the intent's request id, direction, instrument, amount and
// stops (CLAUDE.md §2.7: the request id is persisted before the request is sent).
func RequireOrderRow(orders ports.Orders) func(context.Context, domain.OrderIntent) error {
	return func(ctx context.Context, in domain.OrderIntent) error {
		o, err := orders.Get(ctx, in.RequestID)
		switch {
		case err != nil:
			return fmt.Errorf("no order row for request id %s: %w", in.RequestID, err)
		case o.Kind != domain.OrderOpen || o.State != domain.OrderSent:
			return fmt.Errorf("order %s is a %s order in state %s, not a sent open", in.RequestID, o.Kind, o.State)
		case o.Direction != in.Direction || o.Instrument != in.Instrument || o.Amount != in.Amount ||
			o.StopLoss != in.StopLossRate || o.TakeProfit != in.TakeProfitRate:
			return fmt.Errorf("order %s differs from the intent sent", in.RequestID)
		}
		return nil
	}
}

// RequireDecision is an OnOpen hook for the agent: the account's decision log must already
// hold a pending decision for the intent's request id, with its direction and instrument
// (PLAN 5.5: the decision is persisted before the order row, and so before the send).
func RequireDecision(decisions ports.Decisions, accountID string) func(context.Context, domain.OrderIntent) error {
	return func(ctx context.Context, in domain.OrderIntent) error {
		ds, err := decisions.List(ctx, accountID, time.Time{}, domain.DecisionPending, 10_000)
		if err != nil {
			return fmt.Errorf("reading the decision log: %w", err)
		}
		for _, d := range ds {
			if d.OrderRequestID != in.RequestID {
				continue
			}
			if d.Signal.Direction != in.Direction || d.Signal.Instrument != in.Instrument || !d.Risk.Allowed || d.Risk.Size < in.Amount {
				return fmt.Errorf("decision %s does not match the intent %s", d.ID, in.RequestID)
			}
			return nil
		}
		return fmt.Errorf("no pending decision for request id %s", in.RequestID)
	}
}

// AllHooks runs every OnOpen hook and joins their errors.
func AllHooks(hooks ...func(context.Context, domain.OrderIntent) error) func(context.Context, domain.OrderIntent) error {
	return func(ctx context.Context, in domain.OrderIntent) error {
		var errs []error
		for _, h := range hooks {
			if err := h(ctx, in); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}
