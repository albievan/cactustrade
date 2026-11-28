package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// The Trading 212 executor (PLAN R45.6, T212-b, T212-d … T212-o) is the only code that sends
// orders to a Trading 212 practice account, through ports.T212Broker. It shares nothing of
// the eToro executor's sending path (real money goes live there). Its rules:
//   - a practice account only: anything else is refused before any broker call (D41 g);
//   - every order row is stored (created, then sent) before its request leaves, and a buy, a
//     stop and a sell are sent once and never again (Trading 212 takes no request id, T14);
//     a cancel may be sent up to 3 times (D44);
//   - a lost answer leaves the order unknown: entries wait until it is matched in Trading
//     212's order lists (domain.MatchT212Order), never by sending it again;
//   - a buy is followed until it is final; its fill is checked (T212-j) and protected at once
//     by a sell stop of the filled quantity (T212-h), confirmed in the open orders; a fill
//     that does not conform, or a stop that cannot be confirmed, is sold at market and the
//     instrument blocked;
//   - a close cancels the stop first, confirms it is gone, reads the positions fresh and
//     sells at most what cactusTrade bought and Trading 212 makes available (T212-i), and
//     only while the instrument's market is in session (T212-o).

// Trading 212 execution timings. Offsets are measured from the order's send.
var (
	// t212StopPolls are the reads of the open orders (1 request / 5 s) that confirm a new
	// stop within t212StopConfirm.
	t212StopPolls = []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second}
	// t212ClosePolls are the reads of a sell order after its send.
	t212ClosePolls = []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 30 * time.Second}
)

const (
	// t212StopConfirm: a stop must show in the open orders this long after its send, or the
	// position is sold (T212-h). The supervisor does not check a position younger than this.
	t212StopConfirm = 30 * time.Second
	// t212HistoryPages bounds the history pages read to find one order (50 orders a page).
	t212HistoryPages = 3
	// t212ClaimWindow is how far back the Trading 212 ids recorded on our orders are read
	// when an order is matched (an id claimed by another order of ours is never adopted).
	t212ClaimWindow = 24 * time.Hour
)

// t212Entry is a Trading 212 buy the agent allowed: the share quantity, the cash it reserves
// and the eToro prices it rests on.
type t212Entry struct {
	Instrument domain.InstrumentID // the eToro instrument (signals, prices)
	Ticker     string              // the Trading 212 instrument (orders)
	Quantity   domain.Micros
	Amount     domain.Cents // the reservation (domain.T212Reserve), account minor units
	Entry      domain.Micros
	StopLoss   domain.Micros
	TakeProfit domain.Micros
	// AccountAt is when the Trading 212 cash figure the size came from was read; it must
	// still be within MaxPortfolioAge when the order is sent (CLAUDE.md §2.2).
	AccountAt time.Time
	// RequestID names the order row; the agent writes it into the pending decision first.
	RequestID string
}

// mayT212 refuses a change of the Trading 212 account a unless it is a Trading 212 practice
// account (a real one is refused with ErrRealModeGated, D41 g), the adapter is wired and this
// process holds the account's lease (D22). No broker call is made before it passes.
func (s *Service) mayT212(ctx context.Context, a domain.Account) error {
	switch {
	case a.OnPlatform() != domain.PlatformTrading212:
		return fmt.Errorf("%s is not a Trading 212 account: %w", a.Name, domain.ErrViewOnly)
	case !a.T212Trading():
		s.d.Log.WarnContext(ports.WithAccount(ctx, a), "Trading 212 order refused: practice accounts only")
		return fmt.Errorf("%s: Trading 212 trades practice accounts only: %w", a.Name, domain.ErrRealModeGated)
	case s.d.T212 == nil:
		return s.needT212()
	}
	return s.holdLease(ctx, a.ID)
}

// t212Fractional reports whether the account's quantities may be fractional: its switch is
// on and fractions are verified (D43). Otherwise every quantity is whole shares.
func (s *Service) t212Fractional(ctx context.Context, a domain.Account) bool {
	return s.riskLimitsFor(ctx, a).T212Fractional && domain.T212FractionsVerified
}

// t212Label names a Trading 212 order's share for people: the eToro symbol and the ticker.
func (s *Service) t212Label(ctx context.Context, id domain.InstrumentID, ticker string) string {
	if id > 0 {
		return s.instrumentLabel(ctx, id) + " (" + ticker + ")"
	}
	return ticker
}

// t212InSession reports whether the market of eToro instrument id is in session at now (a
// market order is sent only then, T212-o), and why not.
func (s *Service) t212InSession(ctx context.Context, id domain.InstrumentID, now time.Time) (bool, string) {
	if s.d.Calendar == nil {
		return false, "no market calendar"
	}
	in, _, err := s.d.Store.Instruments().Get(ctx, id)
	if err != nil || in.Market == "" {
		return false, "the instrument's market is not known"
	}
	open, closeAt, trading, err := s.d.Calendar.Session(in.Market, now)
	switch {
	case err != nil:
		return false, "calendar: " + err.Error()
	case !trading || now.Before(open) || !now.Before(closeAt):
		return false, string(in.Market) + " is not in session"
	}
	return true, ""
}

// placeT212Entry sends a buy the agent allowed and follows it until Trading 212 has filled,
// rejected or cancelled it. Nothing is sent unless the account is a reconciled Trading 212
// practice account, not killed, whose lease this process holds, its market is in session, and
// the order row is stored first (created, then sent). The returned order is its latest stored
// version; an error means it was not (or not knowingly) placed.
func (s *Service) placeT212Entry(ctx context.Context, a domain.Account, r t212Entry, l domain.RiskLimits) (domain.Order, error) {
	ctx = ports.WithAccount(ctx, a)
	if err := s.mayT212(ctx, a); err != nil {
		return domain.Order{}, fmt.Errorf("t212.placeEntry: %w", err)
	}
	switch killed, err := s.isKilled(ctx, a.ID); {
	case err != nil:
		return domain.Order{}, fmt.Errorf("t212.placeEntry: reading the kill switch: %w: %w", err, domain.ErrUnavailable)
	case killed:
		return domain.Order{}, fmt.Errorf("t212.placeEntry: the kill switch is on: %w", domain.ErrConflict)
	}
	if rc := s.Reconciled(a.ID); !rc.OK {
		return domain.Order{}, fmt.Errorf("t212.placeEntry: account not reconciled (%s): %w", rc.Reason, domain.ErrUnavailable)
	}
	if r.RequestID == "" {
		return domain.Order{}, fmt.Errorf("t212.placeEntry: no request id (the decision must be stored first): %w", domain.ErrInvalid)
	}
	o, err := domain.NewOrder(domain.Order{
		RequestID: r.RequestID, AccountID: a.ID, Mode: a.Mode, Kind: domain.OrderOpen, Platform: domain.PlatformTrading212,
		Ticker: r.Ticker, Quantity: r.Quantity, Direction: domain.Long, Instrument: r.Instrument, Amount: r.Amount,
		Entry: r.Entry, StopLoss: r.StopLoss, TakeProfit: r.TakeProfit,
	}, s.d.Clock.Now().UTC())
	if err != nil {
		return domain.Order{}, fmt.Errorf("t212.placeEntry: %w", err)
	}
	if err := s.createOrder(ctx, o); err != nil {
		return domain.Order{}, fmt.Errorf("t212.placeEntry: storing the order (nothing sent): %w", err)
	}
	now := s.d.Clock.Now()
	if r.AccountAt.IsZero() || now.Sub(r.AccountAt) > l.MaxPortfolioAge || r.AccountAt.After(now.Add(time.Second)) {
		o, _ = s.move(ctx, o, domain.OrderAbandoned, "the cash figure was stale when the order was due: not sent", nil)
		return o, fmt.Errorf("t212.placeEntry: the account figures are older than %s: %w", l.MaxPortfolioAge, domain.ErrUnavailable)
	}
	if ok, why := s.t212InSession(ctx, r.Instrument, now); !ok {
		o, _ = s.move(ctx, o, domain.OrderAbandoned, "not sent: "+why+" (a market order would be queued, T212-o)", nil)
		return o, fmt.Errorf("t212.placeEntry: %s: %w", why, domain.ErrUnavailable)
	}
	intent, err := domain.T212IntentFor(o, 0, s.t212Fractional(ctx, a))
	if err != nil {
		o, _ = s.move(ctx, o, domain.OrderAbandoned, "no valid intent: not sent", nil)
		return o, fmt.Errorf("t212.placeEntry: %w", err)
	}
	return s.sendT212(ctx, a, o, intent, s.followT212Buy)
}

// sendT212 marks a stored order sent and sends it once (never again, D41 a), then follows it
// with follow when Trading 212 accepted it and it is not final yet. Once the request has left,
// its outcome is stored even if ctx was cancelled meanwhile.
func (s *Service) sendT212(ctx context.Context, a domain.Account, o domain.Order, in domain.T212Intent,
	follow func(context.Context, domain.Account, domain.Order) (domain.Order, error),
) (domain.Order, error) {
	wctx := context.WithoutCancel(ctx)
	if cerr := ctx.Err(); cerr != nil {
		o, _ = s.move(wctx, o, domain.OrderAbandoned, "cancelled before it was sent", nil)
		return o, fmt.Errorf("t212.send: not sent: %w", cerr)
	}
	o, err := s.move(ctx, o, domain.OrderSent, "", nil)
	if err != nil {
		return o, fmt.Errorf("t212.send: marking the order sent (nothing sent): %w", err)
	}
	label := s.t212Label(ctx, o.Instrument, o.Ticker)
	bo, err := s.d.T212.Place(ports.WithAccount(ctx, a), in)
	na, isNA := errors.AsType[*domain.NotAcceptedError](err)
	switch {
	case err == nil:
		s.orderEvent(wctx, a, domain.Long, "order.sent", domain.SeverityInfo, fmt.Sprintf("%s %s %s sent to Trading 212", t212Verb(o), o.Quantity, label),
			fmt.Sprintf("Trading 212 order %d, request %s", bo.ID, o.RequestID))
		if bo.ID <= 0 {
			o, _ = s.move(wctx, o, domain.OrderUnknown, "Trading 212 answered without an order id", nil)
			return o, fmt.Errorf("t212.send: no order id in the answer: %w", domain.ErrOutcomeUnknown)
		}
		if o, err = s.applyT212(wctx, a, o, bo); err != nil {
			return o, fmt.Errorf("t212.send: %w", err)
		}
		if o.State.Final() || follow == nil {
			return o, nil
		}
		return follow(ctx, a, o)
	case isNA:
		o, _ = s.move(wctx, o, domain.OrderRejected, "Trading 212 refused the order: "+na.Reason, nil)
		s.orderEvent(wctx, a, domain.Long, "order.rejected", domain.SeverityWarning, t212Verb(o)+" "+label+" refused by Trading 212", na.Reason)
		return o, fmt.Errorf("t212.send: %w", err)
	case errors.Is(err, domain.ErrRealModeGated):
		o, _ = s.move(wctx, o, domain.OrderAbandoned, "refused before sending: Trading 212 trades practice accounts only", nil)
		return o, fmt.Errorf("t212.send: %w", err)
	default:
		// Sent, or maybe sent: only Trading 212's order lists can tell. Never resent (T14).
		o, _ = s.move(wctx, o, domain.OrderUnknown, "the answer was lost: "+err.Error(), nil)
		s.orderEvent(wctx, a, domain.Long, "order.unknown", domain.SeverityWarning, t212Verb(o)+" "+label+": outcome unknown",
			"it is matched in Trading 212's order lists (never sent again); entries on the account wait. Request "+o.RequestID)
		return o, fmt.Errorf("t212.send: %w", err)
	}
}

// t212Verb names what an order of ours does on Trading 212.
func t212Verb(o domain.Order) string {
	switch o.Kind {
	case domain.OrderOpen:
		return "Buy"
	case domain.OrderStop:
		return "Sell stop"
	case domain.OrderClose:
		return "Sell"
	}
	return "Cancel"
}

// followT212Buy polls a buy on the openPolls schedule until it is final. Still pending (or
// partly filled) at pendingMax → cancelled once (T212-o: a stale buy is not left working);
// a cancel that loses to a fill is a fill (T10). An order Trading 212 accepted is never
// abandoned: what is left unresolved is followed by the supervisor and reconciliation.
func (s *Service) followT212Buy(ctx context.Context, a domain.Account, o domain.Order) (domain.Order, error) {
	cancelSent := false
	for i, off := range openPolls {
		if i < len(openPolls)-1 && o.SentAt.Add(off).Before(s.d.Clock.Now()) {
			continue
		}
		if err := s.sleepUntil(ctx, o.SentAt.Add(off)); err != nil {
			return o, fmt.Errorf("t212.followBuy: %w", err)
		}
		var err error
		if o, err = s.pollT212(ctx, a, o); err != nil {
			s.d.Log.WarnContext(ctx, "following a Trading 212 buy failed", "account", a.ID, "request", o.RequestID, "err", err)
			continue
		}
		if o.State.Final() {
			return o, nil
		}
		if !cancelSent && s.d.Clock.Now().Sub(o.SentAt) >= pendingMax && (o.State == domain.OrderPending || o.State == domain.OrderPartial) {
			cancelSent = true
			if _, err := s.cancelT212(ctx, a, o, fmt.Sprintf("still pending after %s", pendingMax)); err != nil {
				s.d.Log.WarnContext(ctx, "cancelling a stale Trading 212 buy failed", "account", a.ID, "request", o.RequestID, "err", err)
			}
		}
	}
	return o, nil
}

// t212Lookup finds one Trading 212 order by its id: among the active orders, else in the
// first t212HistoryPages pages of the ticker's order history. found is false when neither
// lists it (yet: T4, T5b).
func (s *Service) t212Lookup(ctx context.Context, a domain.Account, id int64, ticker string) (domain.BrokerOrder, bool, error) {
	bctx := ports.WithAccount(ctx, a)
	bo, err := s.d.T212.Order(bctx, id)
	switch {
	case err == nil:
		return bo, true, nil
	case !errors.Is(err, domain.ErrNotFound):
		return domain.BrokerOrder{}, false, err
	}
	cursor := ""
	for range t212HistoryPages {
		hp, err := s.d.T212.History(bctx, ticker, cursor)
		if err != nil {
			return domain.BrokerOrder{}, false, err
		}
		if i := slices.IndexFunc(hp.Orders, func(h domain.BrokerOrder) bool { return h.ID == id }); i >= 0 {
			return hp.Orders[i], true, nil
		}
		if hp.Next == "" {
			break
		}
		cursor = hp.Next
	}
	return domain.BrokerOrder{}, false, nil
}

// pollT212 reads an order of ours that has a Trading 212 id once and folds the answer in.
func (s *Service) pollT212(ctx context.Context, a domain.Account, o domain.Order) (domain.Order, error) {
	if o.EtoroOrderID <= 0 {
		return o, nil
	}
	bo, found, err := s.t212Lookup(ctx, a, o.EtoroOrderID, o.Ticker)
	if err != nil || !found {
		return o, err
	}
	return s.applyT212(ctx, a, o, bo)
}

// applyT212 folds what Trading 212 reports about one of our orders into it, by kind.
func (s *Service) applyT212(ctx context.Context, a domain.Account, o domain.Order, bo domain.BrokerOrder) (domain.Order, error) {
	switch o.Kind {
	case domain.OrderOpen:
		return s.applyT212Buy(ctx, a, o, bo)
	case domain.OrderStop:
		return s.applyT212Stop(ctx, a, o, bo)
	case domain.OrderClose:
		return s.applyT212Sell(ctx, a, o, bo)
	}
	return o, nil
}

// moveT212 moves o to the state Trading 212 reports (when the move is legal), recording the
// Trading 212 id and the quantity filled (in the same write as the state, never lowered: a
// position's Sold is the sum of its stops' and sales' Filled, D44); a final state already
// reached is kept. A conflict (another writer moved it first) returns the stored version with
// errMovedElsewhere.
func (s *Service) moveT212(ctx context.Context, o domain.Order, to domain.OrderState, reason string, bo domain.BrokerOrder, edit func(*domain.Order)) (domain.Order, error) {
	set := func(n *domain.Order) {
		if bo.ID > 0 {
			n.EtoroOrderID = bo.ID
		}
		n.Filled = max(n.Filled, bo.FilledQuantity)
		if edit != nil {
			edit(n)
		}
	}
	if to == o.State {
		if (bo.ID > 0 && bo.ID != o.EtoroOrderID) || bo.FilledQuantity > o.Filled {
			return s.move(ctx, o, to, "", set)
		}
		return o, nil
	}
	if !domain.CanMove(o.State, to) {
		return o, nil
	}
	n, err := s.move(ctx, o, to, reason, set)
	if errors.Is(err, domain.ErrConflict) {
		if cur, gerr := s.d.Store.Orders().Get(ctx, o.RequestID); gerr == nil {
			return cur, errMovedElsewhere
		}
	}
	return n, err
}

// errMovedElsewhere: another writer (the supervisor, reconciliation) moved the order first;
// the caller follows the stored version and leaves the follow-up to that writer.
var errMovedElsewhere = errors.New("the order was moved by another writer")

// pendingState maps a Trading 212 state that is not final to ours.
func pendingState(st domain.OrderState) (domain.OrderState, bool) {
	switch st {
	case domain.OrderPending, domain.OrderPartial:
		return st, true
	}
	return "", false
}

// applyT212Buy folds a buy's state in. A buy that ended with something filled (filled, or
// cancelled or rejected after a partial fill) is recorded as an agent position of the filled
// quantity first, then moved on, then checked and protected by a stop (t212AfterFill). The
// position is recorded only once the buy is final, so its quantity never changes.
func (s *Service) applyT212Buy(ctx context.Context, a domain.Account, o domain.Order, bo domain.BrokerOrder) (domain.Order, error) {
	if o.State.Final() {
		return o, nil
	}
	if bo.State == domain.OrderFilled && bo.FillPrice <= 0 {
		// The active-order answer may lack the fill price: the history has it.
		if h, found, err := s.t212Lookup(ctx, a, bo.ID, o.Ticker); err == nil && found && h.Final {
			bo = h
		}
	}
	filled := bo.Final && bo.FilledQuantity > 0
	switch {
	case filled:
		ap, err := s.recordT212Fill(ctx, a, o, bo)
		if err != nil {
			return o, fmt.Errorf("t212.applyBuy: order %s: %w", o.RequestID, err)
		}
		to, why := domain.OrderFilled, "filled by Trading 212"
		if bo.State != domain.OrderFilled {
			to, why = domain.OrderCancelled, fmt.Sprintf("%s filled of %s; Trading 212 ended the rest (%s)", bo.FilledQuantity, o.Quantity, bo.Status)
		}
		n, err := s.moveT212(ctx, o, to, why, bo, func(n *domain.Order) { n.PositionID = bo.ID })
		if err != nil {
			if errors.Is(err, errMovedElsewhere) {
				return n, nil
			}
			return n, fmt.Errorf("t212.applyBuy: %w", err)
		}
		s.t212AfterFill(ctx, a, n, ap, bo)
		return n, nil
	case bo.Final:
		to := bo.State
		if to != domain.OrderRejected && to != domain.OrderCancelled {
			to = domain.OrderCancelled
		}
		if o.State == domain.OrderPartial && to == domain.OrderRejected {
			to = domain.OrderCancelled
		}
		n, err := s.moveT212(ctx, o, to, "Trading 212: "+bo.Status, bo, nil)
		if err != nil && !errors.Is(err, errMovedElsewhere) {
			return n, fmt.Errorf("t212.applyBuy: %w", err)
		}
		if n.State == to {
			kind, title := "order.cancelled", "Buy "+s.t212Label(ctx, o.Instrument, o.Ticker)+" cancelled"
			if to == domain.OrderRejected {
				kind, title = "order.rejected", "Buy "+s.t212Label(ctx, o.Instrument, o.Ticker)+" rejected by Trading 212"
			}
			s.orderEvent(ctx, a, domain.Long, kind, domain.SeverityWarning, title, "Trading 212 status "+bo.Status+". Request "+o.RequestID)
		}
		return n, nil
	}
	if to, ok := pendingState(bo.State); ok {
		n, err := s.moveT212(ctx, o, to, "", bo, nil)
		if errors.Is(err, errMovedElsewhere) {
			return n, nil
		}
		return n, err
	}
	return o, nil // an unknown status moves nothing (fail closed: it stays in flight)
}

// recordT212Fill records the filled quantity of a final buy as an agent position, once: its
// id is the buy's Trading 212 id (a Trading 212 position has none of its own).
func (s *Service) recordT212Fill(ctx context.Context, a domain.Account, o domain.Order, bo domain.BrokerOrder) (domain.AgentPosition, error) {
	ap := domain.AgentPosition{
		AccountID: a.ID, PositionID: bo.ID, OpenRequestID: o.RequestID, Instrument: o.Instrument,
		Direction: domain.Long, OpenedAt: s.d.Clock.Now().UTC(), Ticker: o.Ticker, Quantity: bo.FilledQuantity,
	}
	err := s.d.Store.AgentPositions().Add(ctx, ap)
	if errors.Is(err, domain.ErrConflict) {
		return s.d.Store.AgentPositions().Get(ctx, a.ID, bo.ID)
	}
	return ap, err
}

// t212AfterFill checks a fill (T212-j): something filled, no more than sent, within 200 bp of
// the eToro entry quote. A fill that does not conform is sold at once and its instrument
// blocked; a conforming one gets its stop (T212-h).
func (s *Service) t212AfterFill(ctx context.Context, a domain.Account, o domain.Order, ap domain.AgentPosition, bo domain.BrokerOrder) {
	label := s.t212Label(ctx, o.Instrument, o.Ticker)
	if ok, why := domain.T212FillConforms(o.Quantity, bo.FilledQuantity, bo.FillPrice, o.Entry); !ok {
		s.blockT212(ctx, a, o.Instrument, "non-conforming Trading 212 fill: "+why)
		s.orderEvent(ctx, a, domain.Long, "order.nonconforming", domain.SeverityDanger, "Non-conforming fill of "+label+": selling it and blocking the instrument", why)
		if _, err := s.closeT212Position(ctx, a, ap, "non-conforming fill: "+why, t212ClosePolls); err != nil {
			s.d.Log.ErrorContext(ctx, "selling a non-conforming Trading 212 fill failed", "account", a.ID, "position", ap.PositionID, "err", err)
		}
		return
	}
	s.orderEvent(ctx, a, domain.Long, "order.filled", domain.SeverityInfo, fmt.Sprintf("Buy %s %s filled", bo.FilledQuantity, label),
		fmt.Sprintf("at %s (eToro quote %s); Trading 212 order %d", bo.FillPrice, o.Entry, bo.ID))
	if ok, _ := s.t212InSession(ctx, o.Instrument, s.d.Clock.Now()); ok && t212Flattening(ctx) {
		return // the kill or the force close sells it next: no stop to cancel first (review g)
	}
	if _, err := s.placeT212Stop(ctx, a, ap, o.StopLoss); err != nil {
		s.d.Log.WarnContext(ctx, "placing the Trading 212 stop failed", "account", a.ID, "position", ap.PositionID, "err", err)
	}
}

// t212FlattenKey marks a context whose caller sells every position of the account next (the
// kill switch, a force close): a buy finalised meanwhile gets no stop.
type t212FlattenKey struct{}

// withT212Flattening marks ctx as flattening.
func withT212Flattening(ctx context.Context) context.Context {
	return context.WithValue(ctx, t212FlattenKey{}, true)
}

// t212Flattening reports whether ctx is marked as flattening.
func t212Flattening(ctx context.Context) bool {
	v, _ := ctx.Value(t212FlattenKey{}).(bool)
	return v
}

// t212ExitKey marks a context that holds the account's exit lock (t212ExitLock).
type t212ExitKey struct{ account string }

// t212ExitLock takes the account's exit lock: one sale of a Trading 212 position at a time
// per account (the kill, the supervisor and a failed stop never sell the same shares
// concurrently). A context that holds it already passes (no self-deadlock); waiting ends
// with ctx.
func (s *Service) t212ExitLock(ctx context.Context, accountID string) (context.Context, func(), error) {
	if held, _ := ctx.Value(t212ExitKey{accountID}).(bool); held {
		return ctx, func() {}, nil
	}
	s.rt.mu.Lock()
	if s.rt.t212Exits == nil {
		s.rt.t212Exits = map[string]chan struct{}{}
	}
	ch := s.rt.t212Exits[accountID]
	if ch == nil {
		ch = make(chan struct{}, 1)
		s.rt.t212Exits[accountID] = ch
	}
	s.rt.mu.Unlock()
	select {
	case ch <- struct{}{}:
	case <-ctx.Done():
		return ctx, func() {}, fmt.Errorf("waiting for the account's exit lock: %w", ctx.Err())
	}
	return context.WithValue(ctx, t212ExitKey{accountID}, true), func() { <-ch }, nil
}

// blockT212 blocks an instrument for the account (no more entries in it).
func (s *Service) blockT212(ctx context.Context, a domain.Account, id domain.InstrumentID, why string) {
	if err := s.d.Store.InstrumentBlocks().Block(ctx, domain.InstrumentBlock{AccountID: a.ID, Instrument: id, Reason: why, At: s.d.Clock.Now().UTC()}); err != nil {
		s.d.Log.ErrorContext(ctx, "blocking an instrument failed", "account", a.ID, "instrument", id, "err", err)
	}
}

// placeT212Stop protects an agent position with a sell stop of its whole quantity at the
// playbook stop (rounded up to T212StopTick, T212-h), GOOD_TILL_CANCEL, and confirms it in the
// open orders within t212StopConfirm. A stop that is refused, not confirmed or whose answer
// was lost and cannot be matched means the position is sold at once and its instrument
// blocked (t212StopFailed). The returned order is the stop's latest stored version.
func (s *Service) placeT212Stop(ctx context.Context, a domain.Account, ap domain.AgentPosition, stop domain.Micros) (domain.Order, error) {
	ctx = ports.WithAccount(ctx, a)
	if err := s.mayT212(ctx, a); err != nil {
		return domain.Order{}, fmt.Errorf("t212.placeStop: %w", err)
	}
	cur, err := s.d.Store.AgentPositions().Get(ctx, a.ID, ap.PositionID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("t212.placeStop: reading the position (nothing sent): %w", err)
	}
	if ap = cur; !ap.Open() || ap.Unsold() <= 0 {
		return domain.Order{}, fmt.Errorf("t212.placeStop: nothing of position %d is left to protect: %w", ap.PositionID, domain.ErrConflict)
	}
	// Trading 212 may list a fresh fill late: a stop above what it shows available would be
	// refused, so it waits (bounded) until the fill is listed (review B2).
	s.t212AwaitListed(ctx, a, ap)
	// The stop covers only what cactusTrade bought and has not sold yet (D44): never the
	// owner's own shares in the ticker.
	o, err := domain.NewOrder(domain.Order{
		RequestID: s.d.Random.ID(), AccountID: a.ID, Mode: a.Mode, Kind: domain.OrderStop, Platform: domain.PlatformTrading212,
		Ticker: ap.Ticker, Quantity: ap.Unsold() - ap.Unsold()%domain.T212QuantityStep(s.t212Fractional(ctx, a)), Direction: domain.Long, Instrument: ap.Instrument,
		StopLoss: domain.T212StopPrice(stop), PositionID: ap.PositionID,
	}, s.d.Clock.Now().UTC())
	if err != nil {
		s.t212StopFailed(ctx, a, ap, "no valid stop: "+err.Error())
		return domain.Order{}, fmt.Errorf("t212.placeStop: %w", err)
	}
	if err := s.createOrder(ctx, o); err != nil {
		s.t212StopFailed(ctx, a, ap, "the stop could not be stored: "+err.Error())
		return domain.Order{}, fmt.Errorf("t212.placeStop: storing the stop (nothing sent): %w", err)
	}
	resting, err := s.t212RestingSells(ctx, a.ID, ap.Ticker, o.RequestID)
	if err != nil {
		o, _ = s.move(ctx, o, domain.OrderAbandoned, "our resting sells could not be read: not sent", nil)
		return o, fmt.Errorf("t212.placeStop: %w", err)
	}
	if resting > 0 {
		// A stop or a sale of ours already rests on these shares: never a second one.
		o, _ = s.move(ctx, o, domain.OrderAbandoned, fmt.Sprintf("%s already rests in sell orders of ours: not sent", resting), nil)
		return o, fmt.Errorf("t212.placeStop: a sell of ours already rests on %s: %w", ap.Ticker, domain.ErrConflict)
	}
	intent, err := domain.T212IntentFor(o, ap.Unsold()-resting, s.t212Fractional(ctx, a))
	if err != nil {
		o, _ = s.move(ctx, o, domain.OrderAbandoned, "no valid intent: not sent", nil)
		s.t212StopFailed(ctx, a, ap, "no valid stop: "+err.Error())
		return o, fmt.Errorf("t212.placeStop: %w", err)
	}
	o, err = s.sendT212(ctx, a, o, intent, nil)
	switch {
	case o.State == domain.OrderPending && o.EtoroOrderID > 0:
		return s.confirmT212Stop(ctx, a, o, ap)
	case o.State == domain.OrderFilled:
		return o, nil // fired at once: applyT212Stop settled the position
	case o.State == domain.OrderUnknown:
		// The answer was lost: matched once after a short wait, else the position is sold.
		if serr := s.d.Sleep(ctx, t212StopPolls[0]); serr == nil {
			if n, _ := s.resolveT212(ctx, a, o); n.EtoroOrderID > 0 {
				if n.State == domain.OrderPending {
					return s.confirmT212Stop(ctx, a, n, ap)
				}
				return n, nil
			}
		}
		s.t212StopFailed(ctx, a, ap, "the stop's answer was lost and it was not found in the open orders")
		return o, err
	}
	why := "the stop was not accepted"
	if err != nil {
		why += ": " + err.Error()
	}
	s.t212StopFailed(ctx, a, ap, why)
	return o, err
}

// t212ListPoll is how often the positions are read while a fresh fill is not listed yet.
const t212ListPoll = 5 * time.Second

// t212AwaitListed waits until Trading 212 lists at least the unsold quantity of a fresh
// position as available for trading, reading the positions every t212ListPoll, for no longer
// than fillVisibleAfter after the position opened. A read error or the end of the wait goes on
// (what follows is refused, never sent above what is available).
func (s *Service) t212AwaitListed(ctx context.Context, a domain.Account, ap domain.AgentPosition) {
	for {
		acct, err := s.d.T212.Account(ports.WithAccount(ctx, a))
		if err != nil {
			return
		}
		if pos, held := acct.Position(ap.Ticker); held && pos.QuantityAvailable >= ap.Unsold() {
			return
		}
		if s.d.Clock.Now().Sub(ap.OpenedAt) >= fillVisibleAfter {
			return
		}
		s.d.Log.InfoContext(ctx, "the fill is not listed by Trading 212 yet: the stop waits", "account", a.ID, "position", ap.PositionID)
		if err := s.d.Sleep(ctx, t212ListPoll); err != nil {
			return
		}
	}
}

// confirmT212Stop waits for a new stop to show in the open orders (t212StopPolls). Seen:
// confirmed. Fired meanwhile: the position is settled. Not seen by t212StopConfirm, or ended
// at Trading 212: the position is sold (t212StopFailed).
func (s *Service) confirmT212Stop(ctx context.Context, a domain.Account, o domain.Order, ap domain.AgentPosition) (domain.Order, error) {
	for _, off := range t212StopPolls {
		if err := s.sleepUntil(ctx, o.SentAt.Add(off)); err != nil {
			return o, fmt.Errorf("t212.confirmStop: %w", err)
		}
		open, err := s.d.T212.OpenOrders(ports.WithAccount(ctx, a))
		if err != nil {
			s.d.Log.WarnContext(ctx, "reading the open orders to confirm a stop failed", "account", a.ID, "request", o.RequestID, "err", err)
			continue
		}
		if i := slices.IndexFunc(open, func(b domain.BrokerOrder) bool { return b.ID == o.EtoroOrderID }); i >= 0 {
			return s.applyT212Stop(ctx, a, o, open[i])
		}
	}
	bo, found, err := s.t212Lookup(ctx, a, o.EtoroOrderID, o.Ticker)
	if err == nil && found {
		if o, err = s.applyT212Stop(ctx, a, o, bo); err == nil && (o.State == domain.OrderFilled || (o.State == domain.OrderPending && !bo.Final)) {
			return o, nil
		}
	}
	why := fmt.Sprintf("the stop (Trading 212 order %d) was not seen in the open orders within %s", o.EtoroOrderID, t212StopConfirm)
	if !o.State.Final() {
		// It may still rest at Trading 212: cancelled first, so the sale never sells the
		// same shares twice (a stop not confirmed gone keeps the sale waiting).
		if _, fired, err := s.cancelT212Stop(ctx, a, o, why); err != nil {
			s.d.Log.WarnContext(ctx, "cancelling an unconfirmed stop failed", "account", a.ID, "request", o.RequestID, "err", err)
		} else if fired {
			return o, nil
		}
	}
	s.t212StopFailed(ctx, a, ap, why)
	if cur, err := s.d.Store.Orders().Get(ctx, o.RequestID); err == nil {
		o = cur
	}
	return o, nil
}

// applyT212Stop folds a stop's state in. Filled: it fired, the position is settled as closed
// by the stop (nothing is sold). Rejected or cancelled at Trading 212: the stop is final (the
// caller decides what follows: a missing stop means a sale).
func (s *Service) applyT212Stop(ctx context.Context, a domain.Account, o domain.Order, bo domain.BrokerOrder) (domain.Order, error) {
	if o.State.Final() {
		return o, nil
	}
	switch {
	case bo.Final && bo.FilledQuantity > 0:
		n, err := s.moveT212(ctx, o, domain.OrderFilled, "the stop fired at Trading 212", bo, nil)
		if err != nil && !errors.Is(err, errMovedElsewhere) {
			return n, fmt.Errorf("t212.applyStop: %w", err)
		}
		s.t212StopHit(ctx, a, o, bo)
		return n, nil
	case bo.Final:
		to := bo.State
		if to != domain.OrderRejected && to != domain.OrderCancelled {
			to = domain.OrderCancelled
		}
		if o.State == domain.OrderPartial {
			to = domain.OrderCancelled
		}
		n, err := s.moveT212(ctx, o, to, "Trading 212: "+bo.Status, bo, nil)
		if errors.Is(err, errMovedElsewhere) {
			return n, nil
		}
		return n, err
	}
	if to, ok := pendingState(bo.State); ok {
		n, err := s.moveT212(ctx, o, to, "", bo, nil)
		if errors.Is(err, errMovedElsewhere) {
			return n, nil
		}
		return n, err
	}
	return o, nil
}

// t212StopHit settles the position a stop of ours protected: closed by the stop once nothing
// of what cactusTrade bought is left unsold. A stop that sold only part of it (cancelled or
// ended after a partial fill) leaves the rest open without a stop: the supervisor sells it
// (never more than bought − sold, D44).
func (s *Service) t212StopHit(ctx context.Context, a domain.Account, stop domain.Order, bo domain.BrokerOrder) {
	ap, err := s.d.Store.AgentPositions().Get(ctx, a.ID, stop.PositionID)
	if err != nil || !ap.Open() {
		return
	}
	if ap.Unsold() > 0 {
		s.orderEvent(ctx, a, domain.Long, "position.stop_partial", domain.SeverityWarning, "Stop of "+s.t212Label(ctx, ap.Instrument, ap.Ticker)+" sold only part of it",
			fmt.Sprintf("%s sold at %s by the stop at %s; %s of the %s cactusTrade bought is left and is sold", bo.FilledQuantity, bo.FillPrice, stop.StopLoss, ap.Unsold(), ap.Quantity))
		return
	}
	s.orderEvent(ctx, a, domain.Long, "position.stop_hit", domain.SeverityWarning, "Stop of "+s.t212Label(ctx, ap.Instrument, ap.Ticker)+" fired",
		fmt.Sprintf("%s sold at %s by the stop at %s", bo.FilledQuantity, bo.FillPrice, stop.StopLoss))
	s.markClosed(ctx, a, ap, fmt.Sprintf("closed by its stop (Trading 212 order %d)", bo.ID))
}

// t212StopFailed handles a position left without a confirmed stop (T212-h): its instrument is
// blocked, an alert raised and the position sold at market at once (in session; otherwise the
// sale waits for the open and the supervisor keeps trying to place a stop).
func (s *Service) t212StopFailed(ctx context.Context, a domain.Account, ap domain.AgentPosition, why string) {
	label := s.t212Label(ctx, ap.Instrument, ap.Ticker)
	s.blockT212(ctx, a, ap.Instrument, "no confirmed Trading 212 stop: "+why)
	s.orderEvent(ctx, a, domain.Long, "order.stop_failed", domain.SeverityDanger, "No confirmed stop for "+label+": selling it and blocking the instrument", why)
	if _, err := s.closeT212Position(ctx, a, ap, "no confirmed stop: "+why, t212ClosePolls); err != nil {
		s.d.Log.ErrorContext(ctx, "selling a Trading 212 position without a stop failed", "account", a.ID, "position", ap.PositionID, "err", err)
	}
}

// t212RestingSells is the quantity the sell orders of ours that are not final (stops and
// sales, whatever their state: an unknown one may rest at Trading 212) may still sell in
// ticker (each its quantity less what it filled already), except the order exclude. It is taken off what a new sell may sell (T212IntentFor's
// held), so a stop and a sale never sell the same shares twice.
func (s *Service) t212RestingSells(ctx context.Context, accountID, ticker, exclude string) (domain.Micros, error) {
	open, err := s.d.Store.Orders().ListOpen(ctx, accountID)
	if err != nil {
		return 0, err
	}
	var q domain.Micros
	for _, o := range open {
		if o.RequestID != exclude && o.Ticker == ticker && o.OnPlatform() == domain.PlatformTrading212 &&
			(o.Kind == domain.OrderStop || o.Kind == domain.OrderClose) {
			q += max(o.Quantity-o.Filled, 0) // what it filled already counts in the position's Sold
		}
	}
	return q, nil
}

// t212Cancels sums up the cancels already stored for an order of ours (t212CancelBlocked
// decides whether another may be sent now).
func (s *Service) t212Cancels(ctx context.Context, a domain.Account, target domain.Order) (cancelRecord, error) {
	os, err := s.d.Store.Orders().ListSince(ctx, a.ID, target.CreatedAt)
	if err != nil {
		return cancelRecord{}, err
	}
	var r cancelRecord
	for _, c := range os {
		if c.Kind != domain.OrderCancel || c.TargetOrderID != target.EtoroOrderID {
			continue
		}
		r.sent++
		if c.CreatedAt.After(r.lastAt) {
			r.lastAt = c.CreatedAt
		}
		switch {
		case !c.State.Final():
			r.inFlight = true
		case c.State == domain.OrderFilled:
			r.accepted = true
		}
	}
	return r, nil
}

// t212OurStops returns the stop orders of ours that are not final for an agent position.
func (s *Service) t212OurStops(ctx context.Context, accountID string, positionID int64) ([]domain.Order, error) {
	open, err := s.d.Store.Orders().ListOpen(ctx, accountID)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(open, func(o domain.Order) bool { return o.Kind != domain.OrderStop || o.PositionID != positionID }), nil
}

// closeT212Position sells one of cactusTrade's Trading 212 positions (OD1: never the owner's
// shares), in the order of T212-i: (1) the market must be in session (T212-o), else nothing
// is sent and an alert says the sale waits for the open while the stop stays; (2) every stop
// of ours on it is cancelled and confirmed gone — one that fired meanwhile settles the
// position and nothing is sold; (3) the positions are read fresh; (4) at most min(the
// quantity cactusTrade bought, Trading 212's quantityAvailableForTrading) is sold at market,
// once. polls are the reads of the sell afterwards (nil = none: the supervisor follows it).
// It works while the kill switch is on.
func (s *Service) closeT212Position(ctx context.Context, a domain.Account, ap domain.AgentPosition, why string, polls []time.Duration) (domain.Order, error) {
	ctx = ports.WithAccount(ctx, a)
	if err := s.mayT212(ctx, a); err != nil {
		return domain.Order{}, fmt.Errorf("t212.closePosition: %w", err)
	}
	if ap.AccountID != a.ID || ap.PositionID <= 0 || ap.Ticker == "" || ap.Quantity <= 0 || !ap.Open() {
		return domain.Order{}, domain.Invalid("position", "only an open Trading 212 position of cactusTrade's can be sold")
	}
	// One exit at a time per account (the kill, the supervisor and a failed stop may all want
	// to sell the same position), and the position read again inside: the caller's copy may
	// be stale (review of R45.6-R45.8, MAJOR).
	ctx, unlock, err := s.t212ExitLock(ctx, a.ID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("t212.closePosition: %w", err)
	}
	defer unlock()
	switch cur, err := s.d.Store.AgentPositions().Get(ctx, a.ID, ap.PositionID); {
	case err != nil:
		return domain.Order{}, fmt.Errorf("t212.closePosition: reading the position: %w", err)
	case !cur.Open():
		return domain.Order{}, nil
	default:
		ap = cur
	}
	label := s.t212Label(ctx, ap.Instrument, ap.Ticker)
	if ap.CloseRequestID != "" {
		prev, err := s.d.Store.Orders().Get(ctx, ap.CloseRequestID)
		if err == nil && !prev.State.Final() {
			if prev.EtoroOrderID > 0 {
				if prev, err = s.pollT212(ctx, a, prev); err != nil {
					return prev, err
				}
				if !prev.State.Final() && s.d.Clock.Now().Sub(prev.SentAt) >= pendingMax {
					// A sale still working after a minute is cancelled (at most maxCancels,
					// one in flight) and, once it ended unfilled, sent again below.
					switch r, rerr := s.t212Cancels(ctx, a, prev); {
					case rerr == nil && !t212CancelBlocked(r, s.d.Clock.Now()):
						if _, err := s.cancelT212(ctx, a, prev, fmt.Sprintf("sale still pending after %s", pendingMax)); err != nil {
							s.d.Log.WarnContext(ctx, "cancelling a stale Trading 212 sale failed", "account", a.ID, "request", prev.RequestID, "err", err)
						}
						if prev, err = s.pollT212(ctx, a, prev); err != nil {
							return prev, err
						}
					case rerr == nil && r.sent >= maxCancels && !r.inFlight && !r.accepted:
						// No cancel left: the sale keeps working at Trading 212 and nothing more is
						// sent for it. Never silent: a person must look before the close.
						if s.rt.firstWarning("order.sale_stuck", prev.RequestID) {
							s.orderEvent(ctx, a, domain.Long, "order.sale_stuck", domain.SeverityDanger, "Sale of "+label+" is stuck at Trading 212",
								fmt.Sprintf("Trading 212 order %d is still working after %d cancels; cactusTrade sends nothing more for it. Sell it or cancel it in Trading 212 before the close. Request %s",
									prev.EtoroOrderID, maxCancels, prev.RequestID))
						}
					}
				}
				if !prev.State.Final() {
					return prev, nil
				}
				if cur, gerr := s.d.Store.AgentPositions().Get(ctx, a.ID, ap.PositionID); gerr == nil && !cur.Open() {
					return prev, nil
				}
			} else {
				if prev, _ = s.resolveT212(ctx, a, prev); !prev.State.Final() {
					return prev, nil // in flight, or being matched
				}
			}
		}
	}
	now := s.d.Clock.Now()
	if ok, closed := s.t212InSession(ctx, ap.Instrument, now); !ok {
		s.warnOnce(ctx, a, domain.Order{RequestID: "position-" + strconv.FormatInt(ap.PositionID, 10)}, "order.exit_waits",
			"Sale of "+label+" waits for the market to open",
			"the exit is due ("+why+") but "+closed+": a market order would be queued (T212-o). Its stop stays at Trading 212; it is sold at the next open")
		return domain.Order{}, fmt.Errorf("t212.closePosition: %s: %w", closed, domain.ErrUnavailable)
	}
	stops, err := s.t212OurStops(ctx, a.ID, ap.PositionID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("t212.closePosition: reading the stops: %w", err)
	}
	for _, st := range stops {
		if st.EtoroOrderID <= 0 {
			if st, _ = s.resolveT212(ctx, a, st); st.EtoroOrderID <= 0 || st.State.Final() {
				continue // never sent, abandoned, or not matched yet: nothing to cancel by id
			}
		}
		gone, fired, err := s.cancelT212Stop(ctx, a, st, why)
		switch {
		case err != nil:
			return domain.Order{}, fmt.Errorf("t212.closePosition: cancelling the stop: %w", err)
		case fired:
			continue // it sold everything it covered: the position is read again below
		case !gone:
			return domain.Order{}, fmt.Errorf("t212.closePosition: the stop (Trading 212 order %d) is not confirmed gone; the sale waits: %w", st.EtoroOrderID, domain.ErrUnavailable)
		}
	}
	// Read again: a stop that fired (in full: closed; in part: in Sold now), also one matched
	// only above, settled the position meanwhile (review B1, B3).
	switch cur, err := s.d.Store.AgentPositions().Get(ctx, a.ID, ap.PositionID); {
	case err != nil:
		return domain.Order{}, fmt.Errorf("t212.closePosition: reading the position: %w", err)
	case !cur.Open():
		return domain.Order{}, nil
	default:
		ap = cur
	}
	acct, err := s.d.T212.Account(ctx)
	if err != nil {
		return domain.Order{}, fmt.Errorf("t212.closePosition: reading the positions: %w", err)
	}
	pos, held := acct.Position(ap.Ticker)
	if !held || pos.Quantity <= 0 {
		if acct.AsOf.Sub(ap.OpenedAt) < fillVisibleAfter {
			// A fresh fill may not be listed yet (review B2): never settled as gone so soon.
			return domain.Order{}, fmt.Errorf("t212.closePosition: %s is not listed yet (a fresh fill): %w", ap.Ticker, domain.ErrUnavailable)
		}
		s.t212Vanished(ctx, a, ap)
		return domain.Order{}, nil
	}
	frac := s.t212Fractional(ctx, a)
	resting, err := s.t212RestingSells(ctx, a.ID, ap.Ticker, "")
	if err != nil {
		return domain.Order{}, fmt.Errorf("t212.closePosition: reading our resting sells: %w", err)
	}
	// What cactusTrade bought and has not sold yet (a partly filled sale or stop is in Sold),
	// capped by what Trading 212 makes available, net of what our resting sells (a stop not
	// confirmed gone, a sale in flight) may still sell: never the owner's shares, never the
	// same shares twice (D44).
	sellable := domain.T212Sellable(ap.Quantity, ap.Sold, pos.QuantityAvailable, resting, 0)
	qty := domain.T212Sellable(ap.Quantity, ap.Sold, pos.QuantityAvailable, resting, domain.T212QuantityStep(frac))
	if qty <= 0 {
		s.warnOnce(ctx, a, domain.Order{RequestID: "position-" + strconv.FormatInt(ap.PositionID, 10)}, "order.nothing_to_sell",
			"Nothing of "+label+" is available to sell", fmt.Sprintf("Trading 212 holds %s, %s available for trading; cactusTrade bought %s and sold %s", pos.Quantity, pos.QuantityAvailable, ap.Quantity, ap.Sold))
		return domain.Order{}, fmt.Errorf("t212.closePosition: nothing available to sell: %w", domain.ErrUnavailable)
	}
	o, err := domain.NewOrder(domain.Order{
		RequestID: s.d.Random.ID(), AccountID: a.ID, Mode: a.Mode, Kind: domain.OrderClose, Platform: domain.PlatformTrading212,
		Ticker: ap.Ticker, Quantity: qty, Direction: domain.Long, Instrument: ap.Instrument, PositionID: ap.PositionID,
	}, s.d.Clock.Now().UTC())
	if err != nil {
		return domain.Order{}, fmt.Errorf("t212.closePosition: %w", err)
	}
	o.Reason = why
	if err := s.createOrder(ctx, o); err != nil {
		return domain.Order{}, fmt.Errorf("t212.closePosition: storing the sell (nothing sent): %w", err)
	}
	ap.CloseRequestID = o.RequestID
	if err := s.d.Store.AgentPositions().SetCloseRequest(ctx, a.ID, ap.PositionID, o.RequestID); err != nil {
		s.d.Log.WarnContext(ctx, "recording the sell on the agent position failed", "account", a.ID, "position", ap.PositionID, "err", err)
	}
	intent, err := domain.T212IntentFor(o, sellable, frac)
	if err != nil {
		o, _ = s.move(ctx, o, domain.OrderAbandoned, "no valid intent: not sent", nil)
		return o, fmt.Errorf("t212.closePosition: %w", err)
	}
	follow := func(ctx context.Context, a domain.Account, o domain.Order) (domain.Order, error) {
		return s.followT212Sell(ctx, a, o, polls)
	}
	o, err = s.sendT212(ctx, a, o, intent, follow)
	if err != nil {
		return o, fmt.Errorf("t212.closePosition: %w", err)
	}
	return o, nil
}

// followT212Sell reads a sell at the given offsets from its send until it is final.
func (s *Service) followT212Sell(ctx context.Context, a domain.Account, o domain.Order, polls []time.Duration) (domain.Order, error) {
	for _, off := range polls {
		if err := s.sleepUntil(ctx, o.SentAt.Add(off)); err != nil {
			return o, fmt.Errorf("t212.followSell: %w", err)
		}
		var err error
		if o, err = s.pollT212(ctx, a, o); err != nil {
			s.d.Log.WarnContext(ctx, "following a Trading 212 sell failed", "account", a.ID, "request", o.RequestID, "err", err)
			continue
		}
		if o.State.Final() {
			return o, nil
		}
	}
	return o, nil
}

// applyT212Sell folds a sell's state in. Filled: the position is closed when nothing of what
// cactusTrade bought is left (else the supervisor sells the rest). Ended unfilled: the
// position stays open and is sold again.
func (s *Service) applyT212Sell(ctx context.Context, a domain.Account, o domain.Order, bo domain.BrokerOrder) (domain.Order, error) {
	if o.State.Final() {
		return o, nil
	}
	label := s.t212Label(ctx, o.Instrument, o.Ticker)
	switch {
	case bo.Final && bo.FilledQuantity > 0:
		to := domain.OrderFilled
		if bo.State != domain.OrderFilled {
			to = domain.OrderCancelled
		}
		n, err := s.moveT212(ctx, o, to, fmt.Sprintf("sold %s of %s at %s", bo.FilledQuantity, o.Quantity, bo.FillPrice), bo, nil)
		if err != nil {
			if errors.Is(err, errMovedElsewhere) {
				return n, nil
			}
			return n, fmt.Errorf("t212.applySell: %w", err)
		}
		// The position's Sold now includes this sale's fill (stored with its state above).
		ap, gerr := s.d.Store.AgentPositions().Get(ctx, a.ID, o.PositionID)
		switch {
		case errors.Is(gerr, domain.ErrNotFound):
			return n, nil
		case gerr != nil:
			return n, fmt.Errorf("t212.applySell: reading the position: %w", gerr)
		case !ap.Open():
			return n, nil
		}
		if ap.Unsold() <= 0 {
			s.markClosed(ctx, a, ap, fmt.Sprintf("sold %s at %s — %s", bo.FilledQuantity, bo.FillPrice, o.Reason))
			return n, nil
		}
		s.orderEvent(ctx, a, domain.Long, "order.partial_close", domain.SeverityWarning, "Sale of "+label+" only partly filled",
			fmt.Sprintf("%s sold; %s of the %s cactusTrade bought is sold again (never more). Request %s", bo.FilledQuantity, ap.Unsold(), ap.Quantity, o.RequestID))
		return n, nil
	case bo.Final:
		to := bo.State
		if to != domain.OrderRejected && to != domain.OrderCancelled {
			to = domain.OrderCancelled
		}
		if o.State == domain.OrderPartial {
			to = domain.OrderCancelled
		}
		n, err := s.moveT212(ctx, o, to, "Trading 212: "+bo.Status, bo, nil)
		if err != nil && !errors.Is(err, errMovedElsewhere) {
			return n, fmt.Errorf("t212.applySell: %w", err)
		}
		s.orderEvent(ctx, a, domain.Long, "order.rejected", domain.SeverityWarning, "Sale of "+label+" not executed", "Trading 212 status "+bo.Status+"; it is sold again")
		return n, nil
	}
	if to, ok := pendingState(bo.State); ok {
		n, err := s.moveT212(ctx, o, to, "", bo, nil)
		if errors.Is(err, errMovedElsewhere) {
			return n, nil
		}
		return n, err
	}
	return o, nil
}

// t212Vanished settles an agent position Trading 212 no longer holds: closed by its stop when
// the history says the stop filled, else gone (sold by hand, or by a stop whose fill is not
// listed yet). A stop of ours still active on it is cancelled (it would sell the owner's
// shares).
func (s *Service) t212Vanished(ctx context.Context, a domain.Account, ap domain.AgentPosition) {
	stops, err := s.t212OurStops(ctx, a.ID, ap.PositionID)
	if err != nil {
		s.d.Log.WarnContext(ctx, "reading the stops of a vanished position failed", "account", a.ID, "position", ap.PositionID, "err", err)
	}
	for _, st := range stops {
		if st.EtoroOrderID <= 0 {
			continue
		}
		if st, err = s.pollT212(ctx, a, st); err == nil && st.State == domain.OrderFilled {
			continue // applyT212Stop settled it (closed by the stop when it sold everything)
		}
		if !st.State.Final() {
			if _, _, err := s.cancelT212Stop(ctx, a, st, "its position is gone"); err != nil {
				s.d.Log.WarnContext(ctx, "cancelling the stop of a vanished position failed", "account", a.ID, "position", ap.PositionID, "err", err)
			}
		}
	}
	if cur, err := s.d.Store.AgentPositions().Get(ctx, a.ID, ap.PositionID); err == nil && !cur.Open() {
		return
	}
	s.markClosed(ctx, a, ap, "no longer held at Trading 212 (its stop, or sold outside cactusTrade)")
}

// cancelT212Stop cancels a stop of ours and confirms it: gone (cancelled, or no longer listed
// anywhere, or ended after selling only part: that part is in the position's Sold), fired (it
// filled in full first: the position is settled, T212-i), or neither (still active, e.g.
// CANCELLING: the caller waits).
func (s *Service) cancelT212Stop(ctx context.Context, a domain.Account, st domain.Order, why string) (gone, fired bool, err error) {
	if _, err := s.cancelT212(ctx, a, st, why); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return false, false, err
	}
	bo, found, err := s.t212Lookup(ctx, a, st.EtoroOrderID, st.Ticker)
	switch {
	case err != nil:
		return false, false, err
	case !found:
		// Neither active nor in the history pages read: taken as gone. A sell of the held
		// quantity can never short (a stop left behind finds nothing to sell).
		if _, err := s.move(ctx, st, domain.OrderCancelled, "cancelled: no longer listed by Trading 212", nil); err != nil {
			s.d.Log.WarnContext(ctx, "recording a cancelled stop failed", "account", a.ID, "request", st.RequestID, "err", err)
		}
		return true, false, nil
	}
	cur, err := s.applyT212Stop(ctx, a, st, bo)
	if err != nil {
		return false, false, err
	}
	switch {
	case cur.State == domain.OrderFilled && cur.Filled >= cur.Quantity:
		return false, true, nil
	case cur.State.Final():
		return true, false, nil // cancelled (or filled only in part: what it sold is in Sold)
	}
	return false, false, nil
}

// cancelT212 asks Trading 212 to cancel an order of ours (a stale buy, a stop before a sale),
// with its own stored cancel row (created, then sent). A lost answer is sent again, up to
// MaxSendsFor (3, D44: a cancel cannot add exposure). Accepted is not done: the caller
// confirms with a lookup (T10). ErrNotFound: Trading 212 does not list the order as active.
func (s *Service) cancelT212(ctx context.Context, a domain.Account, target domain.Order, why string) (domain.Order, error) {
	ctx = ports.WithAccount(ctx, a)
	if err := s.mayT212(ctx, a); err != nil {
		return domain.Order{}, fmt.Errorf("t212.cancel: %w", err)
	}
	if target.AccountID != a.ID || target.EtoroOrderID <= 0 || target.OnPlatform() != domain.PlatformTrading212 {
		return domain.Order{}, domain.Invalid("target", "only an order of ours with a Trading 212 id can be cancelled")
	}
	o, err := domain.NewOrder(domain.Order{
		RequestID: s.d.Random.ID(), AccountID: a.ID, Mode: a.Mode, Kind: domain.OrderCancel, Platform: domain.PlatformTrading212,
		Ticker: target.Ticker, Direction: domain.Long, Instrument: target.Instrument, TargetOrderID: target.EtoroOrderID,
	}, s.d.Clock.Now().UTC())
	if err != nil {
		return domain.Order{}, fmt.Errorf("t212.cancel: %w", err)
	}
	o.Reason = why
	if err := s.createOrder(ctx, o); err != nil {
		return domain.Order{}, fmt.Errorf("t212.cancel: storing the cancel (nothing sent): %w", err)
	}
	for {
		if o, err = s.move(ctx, o, domain.OrderSent, "", nil); err != nil {
			return o, fmt.Errorf("t212.cancel: marking the cancel sent (nothing sent): %w", err)
		}
		err := s.d.T212.Cancel(ctx, target.EtoroOrderID)
		na, isNA := errors.AsType[*domain.NotAcceptedError](err)
		switch {
		case err == nil:
			o, err = s.move(ctx, o, domain.OrderFilled, "cancel accepted: "+why, nil)
			if err != nil {
				return o, fmt.Errorf("t212.cancel: %w", err)
			}
			return o, nil
		case errors.Is(err, domain.ErrNotFound):
			o, _ = s.move(ctx, o, domain.OrderRejected, "Trading 212 does not list the order as active", nil)
			return o, fmt.Errorf("t212.cancel: %w", err)
		case isNA:
			o, _ = s.move(ctx, o, domain.OrderRejected, "Trading 212 refused the cancel: "+na.Reason, nil)
			return o, fmt.Errorf("t212.cancel: %w", err)
		case errors.Is(err, domain.ErrRealModeGated):
			o, _ = s.move(ctx, o, domain.OrderAbandoned, "refused before sending: Trading 212 trades practice accounts only", nil)
			return o, fmt.Errorf("t212.cancel: %w", err)
		case o.Sends >= domain.MaxSendsFor(domain.PlatformTrading212, domain.OrderCancel):
			o, _ = s.move(ctx, o, domain.OrderUnknown, "the answer was lost: "+err.Error(), nil)
			return o, fmt.Errorf("t212.cancel: %w", err)
		}
		if err := s.waitToResend(ctx, a, minRetryAfter); err != nil {
			return o, fmt.Errorf("t212.cancel: %w", err)
		}
	}
}

// t212Candidates lists the Trading 212 orders that could be o: the active ones and the
// ticker's history, newest first, until a page reaches back before o's match window.
func (s *Service) t212Candidates(ctx context.Context, a domain.Account, o domain.Order) ([]domain.BrokerOrder, error) {
	bctx := ports.WithAccount(ctx, a)
	cands, err := s.d.T212.OpenOrders(bctx)
	if err != nil {
		return nil, err
	}
	from := o.SentAt.Add(-domain.T212MatchBefore)
	cursor := ""
	for range t212HistoryPages {
		hp, err := s.d.T212.History(bctx, o.Ticker, cursor)
		if err != nil {
			return nil, err
		}
		cands = append(cands, hp.Orders...)
		if hp.Next == "" || len(hp.Orders) == 0 || hp.Orders[len(hp.Orders)-1].CreatedAt.Before(from) {
			break
		}
		cursor = hp.Next
	}
	return cands, nil
}

// t212Claimed lists the Trading 212 ids recorded on the account's other orders (never
// adopted twice).
func (s *Service) t212Claimed(ctx context.Context, a domain.Account, o domain.Order) ([]int64, error) {
	os, err := s.d.Store.Orders().ListSince(ctx, a.ID, o.CreatedAt.Add(-t212ClaimWindow))
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, c := range os {
		if c.RequestID != o.RequestID && c.EtoroOrderID > 0 && c.OnPlatform() == domain.PlatformTrading212 {
			ids = append(ids, c.EtoroOrderID)
		}
	}
	return ids, nil
}

// resolveT212 resolves an order of ours whose Trading 212 id is not known (its answer was
// lost, or the process stopped while it was sent; T212-d): matched in the open orders and
// the history (domain.MatchT212Order): one candidate → adopted and followed; none in both
// lists T212MatchAfter after the send → abandoned (never resent); several → it stays unknown
// with an alert until the owner resolves it (ResolveT212Order). An order created but never
// sent is abandoned. It returns the latest stored version and a problem ("" = resolved, or
// legitimately waiting).
func (s *Service) resolveT212(ctx context.Context, a domain.Account, o domain.Order) (domain.Order, string) {
	if o.State.Final() || o.EtoroOrderID > 0 {
		return o, ""
	}
	label := s.t212Label(ctx, o.Instrument, o.Ticker)
	if o.State == domain.OrderCreated || o.SentAt.IsZero() {
		n, err := s.abandon(ctx, a, o, "never sent (stopped before the send)")
		if err != nil {
			return o, fmt.Sprintf("order %s: %v", o.RequestID, err)
		}
		return n, ""
	}
	cands, err := s.t212Candidates(ctx, a, o)
	if err != nil {
		return o, fmt.Sprintf("matching order %s: reading Trading 212's orders failed: %v", o.RequestID, err)
	}
	claimed, err := s.t212Claimed(ctx, a, o)
	if err != nil {
		return o, fmt.Sprintf("matching order %s: reading our orders failed: %v", o.RequestID, err)
	}
	bo, res := domain.MatchT212Order(o, cands, claimed)
	switch res {
	case domain.T212MatchOne:
		n, err := s.adoptT212(ctx, a, o, bo, "matched in Trading 212's order lists")
		if err != nil {
			return o, fmt.Sprintf("order %s: %v", o.RequestID, err)
		}
		return n, ""
	case domain.T212MatchAmbiguous:
		s.warnOnce(ctx, a, o, "order.ambiguous", t212Verb(o)+" "+label+": more than one Trading 212 order could be ours",
			"entries on the account wait until the owner resolves it (Orders → resolve)")
		return o, fmt.Sprintf("order %s matches more than one Trading 212 order: the owner must resolve it", o.RequestID)
	}
	if s.d.Clock.Now().Sub(o.SentAt) < domain.T212MatchAfter {
		return o, fmt.Sprintf("order %s is not matched yet", o.RequestID)
	}
	n, err := s.abandon(ctx, a, o, fmt.Sprintf("Trading 212 lists no such order %s after it was sent", domain.T212MatchAfter))
	if err != nil {
		return o, fmt.Sprintf("order %s: %v", o.RequestID, err)
	}
	return n, ""
}

// adoptT212 records bo as the Trading 212 order of o and folds its state in.
func (s *Service) adoptT212(ctx context.Context, a domain.Account, o domain.Order, bo domain.BrokerOrder, why string) (domain.Order, error) {
	to := o.State
	if to == domain.OrderSent {
		to = domain.OrderUnknown // a found order is moved on from unknown by its state below
	}
	n, err := s.move(ctx, o, to, why, func(n *domain.Order) { n.EtoroOrderID = bo.ID })
	if err != nil {
		return o, fmt.Errorf("t212.adopt: %w", err)
	}
	s.orderEvent(ctx, a, domain.Long, "order.adopted", domain.SeverityWarning, t212Verb(n)+" "+s.t212Label(ctx, n.Instrument, n.Ticker)+" found at Trading 212",
		fmt.Sprintf("%s: Trading 212 order %d (%s). Request %s", why, bo.ID, bo.Status, n.RequestID))
	return s.applyT212(ctx, a, n, bo)
}

// ResolveT212Order resolves an order of ours on a Trading 212 practice account whose outcome
// is unknown (T212-d): the owner names the Trading 212 order it is (brokerOrderID, which must
// have the order's ticker, side, type and quantity and not be another order's), or says it
// was not placed (notPlaced: abandoned). Owner of the account, web session. Audited.
func (s *Service) ResolveT212Order(ctx context.Context, p Principal, requestID string, brokerOrderID int64, notPlaced bool) (domain.Order, error) {
	if err := s.authorize(p, domain.PermAgentControl); err != nil {
		return domain.Order{}, err
	}
	if p.Via != ViaSession {
		return domain.Order{}, fmt.Errorf("resolving an order is done in the web UI, not with an API key: %w", domain.ErrForbidden)
	}
	if (brokerOrderID > 0) == notPlaced {
		return domain.Order{}, domain.Invalid("brokerOrderId", "name the Trading 212 order, or say it was not placed")
	}
	o, err := s.d.Store.Orders().Get(ctx, requestID)
	if err != nil {
		return domain.Order{}, fmt.Errorf("order %s: %w", requestID, err)
	}
	v, err := s.t212Account(ctx, p, o.AccountID, domain.AccessOwner)
	if err != nil {
		return domain.Order{}, err
	}
	if o.OnPlatform() != domain.PlatformTrading212 || o.State != domain.OrderUnknown || o.EtoroOrderID > 0 {
		return domain.Order{}, fmt.Errorf("order %s is not a Trading 212 order with an unknown outcome: %w", requestID, domain.ErrConflict)
	}
	a := v.Account
	ctx = ports.WithAccount(ctx, a)
	if err := s.mayT212(ctx, a); err != nil {
		return domain.Order{}, err
	}
	if notPlaced {
		n, err := s.abandon(ctx, a, o, "the owner resolved it: not placed at Trading 212")
		if err != nil {
			return o, err
		}
		s.audit(ctx, p, "t212.order_resolved", a.ID+"/"+requestID, "not placed")
		return n, nil
	}
	bo, found, err := s.t212Lookup(ctx, a, brokerOrderID, o.Ticker)
	if err != nil {
		return domain.Order{}, fmt.Errorf("app.ResolveT212Order: %w", err)
	}
	claimed, err := s.t212Claimed(ctx, a, o)
	if err != nil {
		return domain.Order{}, fmt.Errorf("app.ResolveT212Order: %w", err)
	}
	side, typ := domain.T212Buy, domain.T212Market
	switch o.Kind {
	case domain.OrderStop:
		side, typ = domain.T212Sell, domain.T212StopOrder
	case domain.OrderClose:
		side = domain.T212Sell
	}
	switch {
	case !found:
		return domain.Order{}, domain.Invalid("brokerOrderId", "Trading 212 lists no such order for "+o.Ticker)
	case bo.Ticker != o.Ticker || bo.Side != side || bo.Type != typ || bo.Quantity != o.Quantity:
		return domain.Order{}, domain.Invalid("brokerOrderId", "that Trading 212 order has another ticker, side, type or quantity")
	case slices.Contains(claimed, bo.ID):
		return domain.Order{}, domain.Invalid("brokerOrderId", "that Trading 212 order is already another order of ours")
	}
	n, err := s.adoptT212(ctx, a, o, bo, "the owner resolved it")
	if err != nil {
		return n, err
	}
	s.audit(ctx, p, "t212.order_resolved", a.ID+"/"+requestID, "Trading 212 order "+strconv.FormatInt(bo.ID, 10))
	return n, nil
}
