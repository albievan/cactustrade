package domain

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func openOrder(t *testing.T) Order {
	t.Helper()
	o, err := NewOrder(Order{
		RequestID: "5f0c1d2e-0000-4000-8000-000000000001", AccountID: "acc", Mode: ModeDemo, Kind: OrderOpen,
		Direction: Long, Instrument: 1001, Amount: 50_000, Entry: 100_000_000, StopLoss: 98_000_000, TakeProfit: 104_000_000,
	}, t0)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// TestOrderTransitions checks every pair of states against the lifecycle written out here
// independently of the implementation's table.
func TestOrderTransitions(t *testing.T) {
	legal := map[OrderState][]OrderState{
		OrderCreated: {OrderSent, OrderPending, OrderFilled, OrderPartial, OrderRejected, OrderCancelled, OrderAbandoned},
		OrderSent:    {OrderSent, OrderPending, OrderFilled, OrderPartial, OrderRejected, OrderCancelled, OrderUnknown, OrderAbandoned},
		OrderPending: {OrderFilled, OrderPartial, OrderRejected, OrderCancelled, OrderUnknown},
		OrderPartial: {OrderFilled, OrderCancelled, OrderUnknown},
		OrderUnknown: {OrderPending, OrderFilled, OrderPartial, OrderRejected, OrderCancelled, OrderAbandoned},
		// final: no way out
		OrderFilled: nil, OrderRejected: nil, OrderCancelled: nil, OrderAbandoned: nil,
	}
	if len(legal) != len(OrderStates) {
		t.Fatalf("the test covers %d states, the domain has %d", len(legal), len(OrderStates))
	}
	at := t0.Add(time.Minute)
	for _, from := range append(slices.Clone(OrderStates), "bogus") {
		for _, to := range append(slices.Clone(OrderStates), "bogus") {
			o := openOrder(t)
			o.State = from
			want := slices.Contains(legal[from], to)
			got, err := o.Transition(to, at, "why")
			if want != (err == nil) || want != CanMove(from, to) {
				t.Errorf("%s → %s: err = %v, want legal = %v", from, to, err, want)
				continue
			}
			if !want {
				if !errors.Is(err, ErrIllegalTransition) || !errors.Is(err, ErrConflict) || got != o {
					t.Errorf("%s → %s: refused move changed the order or has the wrong error: %v", from, to, err)
				}
				continue
			}
			if got.State != to || !got.UpdatedAt.Equal(at) || got.Reason != "why" {
				t.Errorf("%s → %s: got %+v", from, to, got)
			}
			if wantSends := map[bool]int{true: 1, false: 0}[to == OrderSent]; got.Sends != wantSends {
				t.Errorf("%s → %s: sends = %d", from, to, got.Sends)
			}
		}
	}
	for _, s := range OrderStates {
		if s.Final() != (len(legal[s]) == 0) || !s.Valid() {
			t.Errorf("%s: final = %v", s, s.Final())
		}
	}
	if OrderState("x").Valid() || OrderKind("x").Valid() {
		t.Fatal("unknown values are valid")
	}
}

func TestOrderSendLimit(t *testing.T) {
	for kind, max := range map[OrderKind]int{OrderOpen: 3, OrderClose: 5, OrderCancel: 3} {
		o := Order{RequestID: "r", Kind: kind, State: OrderCreated}
		var err error
		for i := range max {
			if o, err = o.Transition(OrderSent, t0.Add(time.Duration(i)*time.Second), ""); err != nil {
				t.Fatalf("%s send %d: %v", kind, i+1, err)
			}
		}
		if o.Sends != max || !o.SentAt.Equal(t0.Add(time.Duration(max-1)*time.Second)) {
			t.Fatalf("%s: %+v", kind, o)
		}
		if _, err := o.Transition(OrderSent, t0, ""); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("%s: send %d allowed", kind, max+1)
		}
	}
	// an unknown outcome is never resent (OD2)
	o := openOrder(t)
	o, _ = o.Transition(OrderSent, t0, "")
	o, _ = o.Transition(OrderUnknown, t0, "answer lost")
	if _, err := o.Transition(OrderSent, t0, ""); !errors.Is(err, ErrIllegalTransition) {
		t.Fatal("an unknown order was resent")
	}
	if o.Reason != "answer lost" {
		t.Fatal("reason")
	}
	if o, _ = o.Transition(OrderPending, t0, ""); o.Reason != "answer lost" {
		t.Fatal("an empty reason must keep the last one")
	}
}

func TestNewOrder(t *testing.T) {
	base := func() Order {
		return Order{
			RequestID: "r1", AccountID: "acc", Mode: ModeDemo, Kind: OrderOpen, Direction: Long, Instrument: 1,
			Amount: 100, Entry: 100_000_000, StopLoss: 99_000_000,
		}
	}
	tests := []struct {
		name   string
		change func(*Order)
		field  string // "" = valid
	}{
		{"long", func(*Order) {}, ""},
		{"long with take-profit", func(o *Order) { o.TakeProfit = 101_000_000 }, ""},
		{"short", func(o *Order) { o.Direction, o.StopLoss, o.TakeProfit = Short, 101_000_000, 99_000_000 }, ""},
		{"real-mode order can be recorded", func(o *Order) { o.Mode = ModeReal }, ""},
		{"close", func(o *Order) {
			*o = Order{RequestID: "r", AccountID: "a", Mode: ModeDemo, Kind: OrderClose, PositionID: 7, Instrument: 1}
		}, ""},
		{"cancel", func(o *Order) {
			*o = Order{RequestID: "r", AccountID: "a", Mode: ModeDemo, Kind: OrderCancel, TargetOrderID: 9}
		}, ""},
		{"no id", func(o *Order) { o.RequestID = "" }, "requestId"},
		{"no account", func(o *Order) { o.AccountID = "" }, "accountId"},
		{"no mode", func(o *Order) { o.Mode = "" }, "mode"},
		{"bad kind", func(o *Order) { o.Kind = "modify" }, "kind"},
		{"no direction", func(o *Order) { o.Direction = "" }, "direction"},
		{"no instrument", func(o *Order) { o.Instrument = 0 }, "instrumentId"},
		{"no amount", func(o *Order) { o.Amount = 0 }, "amount"},
		{"no stop", func(o *Order) { o.StopLoss = 0 }, "stopLoss"},
		{"no entry", func(o *Order) { o.Entry = 0 }, "stopLoss"},
		{"long stop above", func(o *Order) { o.StopLoss = 100_000_000 }, "stopLoss"},
		{"short stop below", func(o *Order) { o.Direction = Short }, "stopLoss"},
		{"long take-profit below", func(o *Order) { o.TakeProfit = 99_500_000 }, "takeProfit"},
		{"short take-profit above", func(o *Order) { o.Direction, o.StopLoss, o.TakeProfit = Short, 101_000_000, 100_500_000 }, "takeProfit"},
		{"negative take-profit", func(o *Order) { o.TakeProfit = -1 }, "takeProfit"},
		{"close without position", func(o *Order) {
			*o = Order{RequestID: "r", AccountID: "a", Mode: ModeDemo, Kind: OrderClose, Instrument: 1}
		}, "positionId"},
		{"cancel without target", func(o *Order) { *o = Order{RequestID: "r", AccountID: "a", Mode: ModeDemo, Kind: OrderCancel} }, "targetOrderId"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := base()
			tt.change(&o)
			o.State, o.Sends = OrderFilled, 4 // overwritten
			got, err := NewOrder(o, t0)
			if tt.field == "" {
				if err != nil || got.State != OrderCreated || got.Sends != 0 || !got.CreatedAt.Equal(t0) || !got.UpdatedAt.Equal(t0) {
					t.Fatalf("NewOrder = %+v, %v", got, err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) || ve.Field != tt.field {
				t.Fatalf("err = %v, want a validation error on %s", err, tt.field)
			}
		})
	}
}

func TestIntentFor(t *testing.T) {
	o := openOrder(t)
	in, err := IntentFor(o)
	want := OrderIntent{RequestID: o.RequestID, Direction: Long, Instrument: 1001, Amount: 50_000, StopLossRate: 98_000_000, TakeProfitRate: 104_000_000}
	if err != nil || in != want {
		t.Fatalf("IntentFor = %+v %v", in, err)
	}
	sent, _ := o.Transition(OrderSent, t0, "")
	if in, err := IntentFor(sent); err != nil || in.RequestID != o.RequestID {
		t.Fatal("a resend must reuse the request id")
	}
	tests := []struct {
		name   string
		change func(*Order)
		is     error
	}{
		{"real mode", func(o *Order) { o.Mode = ModeReal }, ErrForbidden},
		{"close order", func(o *Order) { o.Kind = OrderClose }, ErrInvalid},
		{"pending", func(o *Order) { o.State = OrderPending }, ErrConflict},
		{"unknown", func(o *Order) { o.State = OrderUnknown }, ErrConflict},
		{"filled", func(o *Order) { o.State = OrderFilled }, ErrConflict},
		{"stop moved to the wrong side", func(o *Order) { o.StopLoss = o.Entry + 1 }, ErrInvalid},
		{"amount gone", func(o *Order) { o.Amount = 0 }, ErrInvalid},
		{"no id", func(o *Order) { o.RequestID = "" }, ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := openOrder(t)
			tt.change(&o)
			if in, err := IntentFor(o); !errors.Is(err, tt.is) || in != (OrderIntent{}) {
				t.Fatalf("IntentFor = %+v %v, want %v", in, err, tt.is)
			}
		})
	}
}

func TestReservations(t *testing.T) {
	ord := func(kind OrderKind, st OrderState, d Direction, id InstrumentID, amount Cents) Order {
		return Order{Kind: kind, State: st, Direction: d, Instrument: id, Amount: amount}
	}
	tests := []struct {
		name   string
		orders []Order
		want   Reservation
	}{
		{"none", nil, Reservation{}},
		{"final orders hold nothing", []Order{
			ord(OrderOpen, OrderFilled, Long, 1, 100), ord(OrderOpen, OrderRejected, Long, 2, 100),
			ord(OrderOpen, OrderCancelled, Short, 3, 100), ord(OrderOpen, OrderAbandoned, Long, 4, 100),
		}, Reservation{}},
		{"every unfinished open reserves", []Order{
			ord(OrderOpen, OrderCreated, Long, 5, 100), ord(OrderOpen, OrderSent, Short, 2, 200),
			ord(OrderOpen, OrderPending, Long, 3, 300), ord(OrderOpen, OrderPartial, Short, 4, 400),
			ord(OrderOpen, OrderUnknown, Long, 1, 500),
		}, Reservation{Amount: 1500, Count: 5, Short: 600, Unknown: 1, Instruments: []InstrumentID{1, 2, 3, 4, 5}}},
		{"closes and cancels reserve no cash but are in flight", []Order{
			ord(OrderClose, OrderSent, Long, 7, 0), ord(OrderCancel, OrderUnknown, "", 0, 0),
			ord(OrderClose, OrderUnknown, Short, 7, 0), ord(OrderClose, OrderFilled, Long, 8, 0),
		}, Reservation{Unknown: 2, Instruments: []InstrumentID{7}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Reservations(tt.orders)
			if got.Amount != tt.want.Amount || got.Count != tt.want.Count || got.Short != tt.want.Short ||
				got.Unknown != tt.want.Unknown || !slices.Equal(got.Instruments, tt.want.Instruments) {
				t.Fatalf("Reservations = %+v, want %+v", got, tt.want)
			}
		})
	}
	// Apply feeds the engine: an instrument in flight and an unknown order both refuse.
	in := okInput()
	r := Reservations([]Order{ord(OrderOpen, OrderPending, Long, 1, 1000)})
	if d := AssessEntry(r.Apply(in)); d.Allowed {
		t.Fatal("entry allowed with an order in flight in the same instrument")
	}
	r = Reservations([]Order{ord(OrderOpen, OrderUnknown, Short, 9, 1000)})
	applied := r.Apply(in)
	if applied.State.Reserved != 1000 || applied.State.ReservedOrders != 1 || applied.State.ReservedShort != 1000 || applied.UnknownOrders != 1 {
		t.Fatalf("Apply = %+v", applied.State)
	}
	if d := AssessEntry(applied); d.Allowed {
		t.Fatal("entry allowed with an unknown order outcome")
	}
	r = Reservations([]Order{ord(OrderOpen, OrderPending, Long, 9, 30_000)})
	if d := AssessEntry(r.Apply(in)); !d.Allowed || d.Size != 100_000 {
		t.Fatalf("an order elsewhere must only reserve: %+v", d)
	}
}
