package domain

import (
	"cmp"
	"fmt"
	"slices"
	"time"
)

// OrderKind is what an order of ours asks the broker to do.
type OrderKind string

// Order kinds.
const (
	OrderOpen   OrderKind = "open"   // open a position (the only kind that reserves cash)
	OrderClose  OrderKind = "close"  // close a position by its positionId (Trading 212: sell the held quantity)
	OrderCancel OrderKind = "cancel" // cancel a pending order
	// OrderStop is a Trading 212 sell stop of a held quantity, placed right after a fill
	// (D41 c). eToro carries the stop on the open order, so an eToro order is never a stop.
	OrderStop OrderKind = "stop"
)

// Valid reports whether k is a known kind.
func (k OrderKind) Valid() bool {
	return k == OrderOpen || k == OrderClose || k == OrderCancel || k == OrderStop
}

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

// MaxSends bounds how often one request id is sent. An open is sent once and never again,
// not even after a 429 (review M3, stricter than OD2's default): until 4.17 verifies whether
// eToro answers a 429 before processing (U7) and dedupes a repeated x-request-id (U1), a
// resend could double the order. A close may be resent when a lost answer left the position
// open, a cancel after a 429.
func MaxSends(k OrderKind) int {
	switch k {
	case OrderOpen:
		return 1
	case OrderClose:
		return 5
	}
	return 3
}

// MaxSendsFor bounds how often one order of the platform p is sent. eToro: MaxSends. Trading
// 212 takes no request id, so a resend could duplicate the order (T14): a buy, a stop and a
// sell are sent once and never again (D41 a); a cancel may be sent up to 3 times, because it
// cannot add exposure (D44).
func MaxSendsFor(p Platform, k OrderKind) int {
	if p == PlatformTrading212 {
		if k == OrderCancel {
			return 3
		}
		return 1
	}
	return MaxSends(k)
}

// Order is one request of ours to a broker. Its RequestID is eToro's x-request-id; it is
// persisted before the request is sent and reused on every resend (CLAUDE.md §2.7). Trading
// 212 never sees it: there it only names our row, which is still persisted before the send.
type Order struct {
	RequestID string
	AccountID string
	Mode      TradingMode
	Kind      OrderKind
	// Platform is the broker the order goes to ("" reads as eToro).
	Platform Platform
	// Ticker is Trading 212's instrument (e.g. "AAPL_US_EQ"); "" for eToro.
	Ticker string
	// Quantity is the Trading 212 share quantity (always positive; the side comes from the
	// kind); 0 for eToro.
	Quantity Micros
	// Direction of the position opened (open) or closed (close).
	Direction  Direction
	Instrument InstrumentID
	Amount     Cents  // open: the cash to invest
	Entry      Micros // open: the price the size and the stop were based on
	StopLoss   Micros // open: required, on the losing side of Entry
	TakeProfit Micros // open: optional (0), on the winning side of Entry
	// PositionID is the position to close (close) or the first position filled (open).
	PositionID    int64
	TargetOrderID int64 // cancel: the broker's order id of the order to cancel
	// EtoroOrderID is the broker's order id once known: eToro's, or Trading 212's for a
	// Trading 212 order (the name and its column are kept from before Trading 212).
	EtoroOrderID int64
	State        OrderState
	Reason       string
	Sends        int
	CreatedAt    time.Time
	SentAt       time.Time // the latest send
	UpdatedAt    time.Time
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

// OnPlatform is the order's platform ("" = eToro).
func (o Order) OnPlatform() Platform {
	if o.Platform == "" {
		return PlatformEtoro
	}
	return o.Platform
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
		return Invalid("kind", "open, close, cancel or stop")
	}
	switch o.OnPlatform() {
	case PlatformEtoro:
	case PlatformTrading212:
		return o.validateT212()
	default:
		return Invalid("platform", "orders go to eToro or Trading 212 only")
	}
	if o.Ticker != "" || o.Quantity != 0 {
		return Invalid("ticker", "an eToro order has no ticker or quantity")
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
	case OrderStop:
		return Invalid("kind", "an eToro order carries its stop on the open: there is no stop order")
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
		if o.Sends >= MaxSendsFor(o.OnPlatform(), o.Kind) {
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
// anything but a valid open order that is created or sent, of a demo account, or of a
// real-money account with an entry permit for it that was valid when the order was created
// (S2, PLAN 7.19; a demo order ignores the permit).
func IntentFor(o Order, permit RealPermit) (OrderIntent, error) {
	switch {
	case o.OnPlatform() != PlatformEtoro:
		return OrderIntent{}, Invalid("platform", "only an eToro order has an eToro intent (Trading 212: T212IntentFor)")
	case o.Kind != OrderOpen:
		return OrderIntent{}, Invalid("kind", "only an open order has an intent")
	case o.Mode != ModeDemo && o.Mode != ModeReal:
		return OrderIntent{}, fmt.Errorf("order %s: %w", o.RequestID, ErrRealModeGated)
	case o.Mode == ModeReal && !permit.AllowsEntry(o.AccountID, o.CreatedAt):
		return OrderIntent{}, fmt.Errorf("order %s: %w", o.RequestID, ErrNoRealPermit)
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

// FillVisibleAfter is how long a fill of ours may take to show in eToro's portfolio (U11,
// unverified until 4.17). Until a portfolio read shows it, the cash figure may not have
// paid for it yet.
const FillVisibleAfter = 2 * time.Minute

// filledOpen reports whether o is a final open order that opened positions: filled, or
// cancelled after a partial fill.
func filledOpen(o Order) bool {
	return o.Kind == OrderOpen && (o.State == OrderFilled || (o.State == OrderCancelled && o.PositionID > 0))
}

// WithUnseenFills adds to r the final open orders whose fills pf may not show yet (review
// B1): a filled order keeps its full amount reserved (and its instrument in flight, and a
// place under the position cap) until a portfolio read from no earlier than the fill (its
// UpdatedAt) shows every position it opened, or until FillVisibleAfter has passed since the
// fill. Without this a second order in the same tick would be sized from a cash figure that
// has not paid for the first one. Counting a fill twice (reserved and in the portfolio) is
// the accepted direction. orders are the account's recent orders (non-final ones are
// ignored: Reservations counts them); ours are its agent positions (to find every position
// an order opened; the order itself stores only the first).
func (r Reservation) WithUnseenFills(orders []Order, ours []AgentPosition, pf Portfolio, now time.Time) Reservation {
	inPF := map[int64]bool{}
	for _, p := range pf.Positions {
		inPF[p.ID] = true
	}
	r.Instruments = slices.Clone(r.Instruments)
	for _, o := range orders {
		if !filledOpen(o) || now.Sub(o.UpdatedAt) >= FillVisibleAfter {
			continue
		}
		var ids []int64
		if o.PositionID > 0 {
			ids = append(ids, o.PositionID)
		}
		for _, ap := range ours {
			if ap.OpenRequestID == o.RequestID {
				ids = append(ids, ap.PositionID)
			}
		}
		seen := len(ids) > 0 && !pf.AsOf.Before(o.UpdatedAt)
		for _, id := range ids {
			seen = seen && inPF[id]
		}
		if seen {
			continue
		}
		r.Amount += o.Amount
		r.Count++
		if o.Direction == Short {
			r.Short += o.Amount
		}
		if o.Instrument != 0 && !slices.Contains(r.Instruments, o.Instrument) {
			r.Instruments = append(r.Instruments, o.Instrument)
		}
	}
	slices.Sort(r.Instruments)
	return r
}

// AdoptionWindow is how long after its send an order of ours can still explain a position
// nobody recorded (review B2).
const AdoptionWindow = 24 * time.Hour

// adoptionToleranceBp: the positions adopted for one order may exceed its amount by at most
// 1 % (eToro's rounding of units).
const adoptionToleranceBp = 100

// Adoption is a position in eToro's portfolio that one of our orders opened but that was never
// recorded as ours.
type Adoption struct {
	Position Position
	Order    Order
}

// AdoptUnrecorded matches positions that are not recorded as ours (unrecorded) with our open
// orders that ended without their fills being recorded (review B2): a fill whose recording
// failed, or an order abandoned after lookups kept answering 404 although eToro holds it (U2).
// An order can explain a position when it is an open order that is filled, cancelled or
// abandoned, was sent (SentAt set) within AdoptionWindow before now, has the position's
// instrument and direction, was sent no later than the position opened, has stored that
// position's id when it stores one, and the positions it explains add up to at most its amount
// (+1 %). Each position goes to the latest such order. Anything else is left alone (OD1).
func AdoptUnrecorded(orders []Order, unrecorded []Position, now time.Time) []Adoption {
	var cands []Order
	for _, o := range orders {
		switch {
		case o.Kind != OrderOpen, o.State != OrderFilled && o.State != OrderCancelled && o.State != OrderAbandoned,
			o.SentAt.IsZero(), o.SentAt.After(now), now.Sub(o.SentAt) > AdoptionWindow:
			continue
		}
		cands = append(cands, o)
	}
	// Latest send first, so a position goes to the order sent just before it opened.
	slices.SortStableFunc(cands, func(a, b Order) int { return b.SentAt.Compare(a.SentAt) })
	ps := slices.Clone(unrecorded)
	slices.SortStableFunc(ps, func(a, b Position) int {
		if c := a.OpenedAt.Compare(b.OpenedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	used := map[string]Cents{}
	var out []Adoption
	for _, p := range ps {
		for _, o := range cands {
			switch {
			case p.Instrument != o.Instrument, p.Direction() != o.Direction, p.OpenedAt.IsZero(), p.OpenedAt.Before(o.SentAt),
				o.PositionID > 0 && p.ID != o.PositionID,
				used[o.RequestID]+p.Amount > o.Amount+adoptionToleranceBp*o.Amount/10_000:
				continue
			}
			used[o.RequestID] += p.Amount
			out = append(out, Adoption{Position: p, Order: o})
			break
		}
	}
	return out
}

// Apply puts the reservation into an entry decision's input.
func (r Reservation) Apply(in RiskInput) RiskInput {
	in.State.Reserved, in.State.ReservedOrders, in.State.ReservedShort = r.Amount, r.Count, r.Short
	in.PendingInstruments = slices.Clone(r.Instruments)
	in.UnknownOrders = r.Unknown
	return in
}
