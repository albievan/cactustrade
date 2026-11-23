package domain

import (
	"fmt"
	"slices"
	"time"
)

// OrderKind is what an order of ours asks eToro to do.
type OrderKind string

// Order kinds.
const (
	OrderOpen   OrderKind = "open"   // open a position (the only kind that reserves cash)
	OrderClose  OrderKind = "close"  // close a position by its positionId
	OrderCancel OrderKind = "cancel" // cancel a pending open order
)

// Valid reports whether k is a known kind.
func (k OrderKind) Valid() bool { return k == OrderOpen || k == OrderClose || k == OrderCancel }

// OrderState is the lifecycle of an order. The broker's lookup reports pending, filled,
// partially_filled, rejected or unknown; created, sent, cancelled and abandoned are ours.
type OrderState string

// Order states.
const (
	// OrderCreated: persisted, not sent yet. A crash here leaves an order eToro may never
	// have seen: reconciliation adopts it (lookup finds it) or abandons it.
	OrderCreated OrderState = "created"
	// OrderSent: persisted as sent before the request leaves (CLAUDE.md §2.7).
	OrderSent OrderState = "sent"
	// OrderPending: eToro has it and has not executed it yet.
	OrderPending OrderState = "pending"
	// OrderFilled: executed (final).
	OrderFilled OrderState = "filled"
	// OrderPartial: partly executed; the rest may still fill or be cancelled.
	OrderPartial OrderState = "partially_filled"
	// OrderRejected: eToro refused it (final).
	OrderRejected OrderState = "rejected"
	// OrderCancelled: cancelled before it executed (final).
	OrderCancelled OrderState = "cancelled"
	// OrderUnknown: sent, but the answer was lost; it keeps its reservation and blocks
	// entries until a lookup resolves it (OD2).
	OrderUnknown OrderState = "unknown"
	// OrderAbandoned: eToro never knew it (lookup 404 for 2 minutes); never resent (final).
	OrderAbandoned OrderState = "abandoned"
)

// OrderStates lists every state, for tests and the repository's checks.
var OrderStates = []OrderState{
	OrderCreated, OrderSent, OrderPending, OrderFilled, OrderPartial,
	OrderRejected, OrderCancelled, OrderUnknown, OrderAbandoned,
}

// Valid reports whether s is a known state.
func (s OrderState) Valid() bool { return slices.Contains(OrderStates, s) }

// Final reports whether s can never change again.
func (s OrderState) Final() bool {
	switch s {
	case OrderFilled, OrderRejected, OrderCancelled, OrderAbandoned:
		return true
	}
	return false
}

// orderMoves is the transition table. A missing pair is illegal; final states have no row.
// sent → sent is a resend with the same request id, allowed only within MaxSends.
// There is no way back to sent from unknown: an unclear outcome is never resent (OD2).
var orderMoves = map[OrderState][]OrderState{
	OrderCreated: {OrderSent, OrderPending, OrderFilled, OrderPartial, OrderRejected, OrderCancelled, OrderAbandoned},
	OrderSent:    {OrderSent, OrderPending, OrderFilled, OrderPartial, OrderRejected, OrderCancelled, OrderUnknown, OrderAbandoned},
	OrderPending: {OrderFilled, OrderPartial, OrderRejected, OrderCancelled, OrderUnknown},
	OrderPartial: {OrderFilled, OrderCancelled, OrderUnknown},
	OrderUnknown: {OrderPending, OrderFilled, OrderPartial, OrderRejected, OrderCancelled, OrderAbandoned},
}

// CanMove reports whether an order may go from one state to another.
func CanMove(from, to OrderState) bool { return slices.Contains(orderMoves[from], to) }

// MaxSends bounds how often one request id is sent: an open only after a 429 (OD2), a close
// when a lost answer left the position open.
func MaxSends(k OrderKind) int {
	if k == OrderClose {
		return 5
	}
	return 3
}

// Order is one request of ours to eToro. Its RequestID is the x-request-id; it is persisted
// before the request is sent and reused on every resend (CLAUDE.md §2.7).
type Order struct {
	RequestID string
	AccountID string
	Mode      TradingMode
	Kind      OrderKind
	// Direction of the position opened (open) or closed (close).
	Direction  Direction
	Instrument InstrumentID
	Amount     Cents  // open: the cash to invest
	Entry      Micros // open: the price the size and the stop were based on
	StopLoss   Micros // open: required, on the losing side of Entry
	TakeProfit Micros // open: optional (0), on the winning side of Entry
	// PositionID is the position to close (close) or the first position filled (open).
	PositionID    int64
	TargetOrderID int64 // cancel: eToro's order id of the open order to cancel
	EtoroOrderID  int64 // eToro's id once known
	State         OrderState
	Reason        string
	Sends         int
	CreatedAt     time.Time
	SentAt        time.Time // the latest send
	UpdatedAt     time.Time
}

// NewOrder validates o for its kind and returns it in state created, stamped at.
func NewOrder(o Order, at time.Time) (Order, error) {
	if err := o.validate(); err != nil {
		return Order{}, err
	}
	o.State, o.Sends, o.Reason = OrderCreated, 0, ""
	o.CreatedAt, o.UpdatedAt, o.SentAt = at, at, time.Time{}
	return o, nil
}

func (o Order) validate() error {
	switch {
	case o.RequestID == "":
		return Invalid("requestId", "required")
	case o.AccountID == "":
		return Invalid("accountId", "required")
	case o.Mode != ModeDemo && o.Mode != ModeReal:
		return Invalid("mode", "demo or real")
	case !o.Kind.Valid():
		return Invalid("kind", "open, close or cancel")
	}
	switch o.Kind {
	case OrderOpen:
		return validEntry(o.Direction, o.Instrument, o.Amount, o.Entry, o.StopLoss, o.TakeProfit)
	case OrderClose:
		if o.PositionID <= 0 || o.Instrument <= 0 {
			return Invalid("positionId", "a close needs the position and its instrument")
		}
	case OrderCancel:
		if o.TargetOrderID <= 0 {
			return Invalid("targetOrderId", "a cancel needs eToro's order id")
		}
	}
	return nil
}

func validEntry(d Direction, id InstrumentID, amount Cents, entry, stop, tp Micros) error {
	switch {
	case !d.Valid():
		return Invalid("direction", "long or short")
	case id <= 0:
		return Invalid("instrumentId", "required")
	case amount <= 0:
		return Invalid("amount", "must be positive")
	case entry <= 0 || stop <= 0:
		return Invalid("stopLoss", "an entry needs a price and a stop")
	case d == Long && stop >= entry, d == Short && stop <= entry:
		return Invalid("stopLoss", "the stop must be on the losing side of the entry")
	case tp < 0, tp > 0 && d == Long && tp <= entry, tp > 0 && d == Short && tp >= entry:
		return Invalid("takeProfit", "the take-profit must be on the winning side of the entry")
	}
	return nil
}

// Transition moves the order to state to at time at. Illegal moves (including any move out
// of a final state) and a send beyond MaxSends fail with ErrIllegalTransition and leave the
// order unchanged. A move to sent counts a send.
func (o Order) Transition(to OrderState, at time.Time, reason string) (Order, error) {
	if !CanMove(o.State, to) {
		return o, fmt.Errorf("order %s: %s → %s: %w", o.RequestID, o.State, to, ErrIllegalTransition)
	}
	if to == OrderSent {
		if o.Sends >= MaxSends(o.Kind) {
			return o, fmt.Errorf("order %s: already sent %d times: %w", o.RequestID, o.Sends, ErrIllegalTransition)
		}
		o.Sends++
		o.SentAt = at
	}
	o.State, o.UpdatedAt = to, at
	if reason != "" {
		o.Reason = reason
	}
	return o, nil
}

// IntentFor builds the broker request for an open order. It is the only place an
// OrderIntent is built: RequestID is the order's persisted id, and the intent carries no
// leverage or settlement (the adapter's two constant shapes, CLAUDE.md §2.3). It refuses
// anything but a valid open order of a demo account that is created or sent.
func IntentFor(o Order) (OrderIntent, error) {
	switch {
	case o.Kind != OrderOpen:
		return OrderIntent{}, Invalid("kind", "only an open order has an intent")
	case o.Mode != ModeDemo:
		return OrderIntent{}, fmt.Errorf("order %s: real mode is gated (phase 7): %w", o.RequestID, ErrForbidden)
	case o.State != OrderCreated && o.State != OrderSent:
		return OrderIntent{}, fmt.Errorf("order %s is %s: %w", o.RequestID, o.State, ErrIllegalTransition)
	}
	if err := o.validate(); err != nil {
		return OrderIntent{}, err
	}
	return OrderIntent{
		RequestID: o.RequestID, Direction: o.Direction, Instrument: o.Instrument, Amount: o.Amount,
		StopLossRate: o.StopLoss, TakeProfitRate: o.TakeProfit,
	}, nil
}

// Reservation is what an account's own unfinished orders hold back (D24).
type Reservation struct {
	Amount Cents // cash reserved by open orders that are not final
	Count  int   // how many such open orders (they count against the position cap)
	Short  Cents // the part of Amount for shorts
	// Unknown counts orders of any kind whose outcome is unknown: entries wait for them.
	Unknown int
	// Instruments with any order of ours in flight (any kind), sorted, without repeats.
	Instruments []InstrumentID
}

// Reservations sums one account's orders that are not final. An open order reserves its
// full amount until it is final, even when partly filled (double counting with the
// portfolio is accepted: it only makes the engine more cautious).
func Reservations(orders []Order) Reservation {
	var r Reservation
	for _, o := range orders {
		if o.State.Final() {
			continue
		}
		if !slices.Contains(r.Instruments, o.Instrument) && o.Instrument != 0 {
			r.Instruments = append(r.Instruments, o.Instrument)
		}
		if o.State == OrderUnknown {
			r.Unknown++
		}
		if o.Kind == OrderOpen {
			r.Amount += o.Amount
			r.Count++
			if o.Direction == Short {
				r.Short += o.Amount
			}
		}
	}
	slices.Sort(r.Instruments)
	return r
}

// Apply puts the reservation into an entry decision's input.
func (r Reservation) Apply(in RiskInput) RiskInput {
	in.State.Reserved, in.State.ReservedOrders, in.State.ReservedShort = r.Amount, r.Count, r.Short
	in.PendingInstruments = slices.Clone(r.Instruments)
	in.UnknownOrders = r.Unknown
	return in
}
