package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// t212X is a Trading 212 practice account (GBP) with BARC.L (eToro 1) matched and confirmed
// as BARCl_EQ at 200 p, reconciled, a sleep that only moves the fake clock, and the
// persisted-before-Place hook on the simulated account.
type t212X struct {
	*t212Env
	a     domain.Account
	slept []time.Duration
	// age makes the cash figure of the next entry that much older.
	age time.Duration
}

const barc = "BARCl_EQ"

func newT212X(t *testing.T) *t212X {
	t.Helper()
	te := newT212Env(t)
	x := &t212X{t212Env: te, a: te.t212.Account}
	te.svc.d.Sleep = func(ctx context.Context, d time.Duration) error {
		if d > 0 {
			x.slept = append(x.slept, d)
			te.clock.Advance(d)
		}
		return ctx.Err()
	}
	te.sim.Prices = map[string]domain.Micros{barc: 200_000_000}
	te.sim.OnPlace = fakes.RequireT212OrderRow(te.store.Orders())
	if _, err := te.svc.AddToWatchlist(te.ctx, te.owner, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := te.svc.ConfirmT212Match(te.ctx, te.owner, 1); err != nil {
		t.Fatal(err)
	}
	te.svc.setReconciled(te.ctx, x.a, true, "")
	te.sim.Reads = 0
	return x
}

func (x *t212X) entry(id string) t212Entry {
	q := 3 * domain.Micros(1_000_000)
	return t212Entry{
		Instrument: 1, Ticker: barc, Quantity: q, Amount: domain.T212Reserve(q, 200_000_000),
		Entry: 200_000_000, StopLoss: 194_000_000, TakeProfit: 212_000_000, AccountAt: x.clock.Now().Add(-x.age), RequestID: id,
	}
}

func (x *t212X) place(t *testing.T, id string) (domain.Order, error) {
	t.Helper()
	return x.svc.placeT212Entry(x.ctx, x.a, x.entry(id), x.svc.riskLimitsFor(x.ctx, x.a))
}

func (x *t212X) orders(t *testing.T, kind domain.OrderKind) []domain.Order {
	t.Helper()
	os, err := x.store.Orders().List(x.ctx, x.a.ID, 500)
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(os, func(o domain.Order) bool { return kind != "" && o.Kind != kind })
}

func (x *t212X) position(t *testing.T, id int64) domain.AgentPosition {
	t.Helper()
	ap, err := x.store.AgentPositions().Get(x.ctx, x.a.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

func (x *t212X) events(t *testing.T) []string {
	t.Helper()
	es, err := x.store.Events().List(x.ctx, []string{x.a.ID}, 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Kind)
	}
	return out
}

func (x *t212X) isBlocked(t *testing.T) bool {
	t.Helper()
	b, err := x.svc.isBlocked(x.ctx, x.a.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// activeStops are the sell stops Trading 212 holds.
func (x *t212X) activeStops(t *testing.T) []domain.BrokerOrder {
	t.Helper()
	os, err := x.sim.OpenOrders(ports.WithAccount(x.ctx, x.a))
	if err != nil {
		t.Fatal(err)
	}
	return slices.DeleteFunc(os, func(o domain.BrokerOrder) bool { return o.Type != domain.T212StopOrder })
}

// sound checks what every case must keep: no violation at the simulated account (the order
// row persisted as sent before every Place, no sell above the held quantity, no fraction, no
// real account), each order Placed at most once, and only the three shapes.
func (x *t212X) sound(t *testing.T) {
	t.Helper()
	if len(x.sim.Violations) > 0 {
		t.Fatalf("violations: %v", x.sim.Violations)
	}
	seen := map[string]bool{}
	for _, in := range x.sim.Intents {
		if seen[in.RequestID] {
			t.Fatalf("order %s Placed twice", in.RequestID)
		}
		seen[in.RequestID] = true
		switch {
		case in.Side == domain.T212Buy && in.Type == domain.T212Market,
			in.Side == domain.T212Sell && in.Type == domain.T212StopOrder,
			in.Side == domain.T212Sell && in.Type == domain.T212Market:
		default:
			t.Fatalf("intent shape %+v", in)
		}
	}
}

func intentsOf(sim *fakes.T212Sim, side domain.T212Side, typ domain.T212OrderType) []domain.T212Intent {
	return slices.DeleteFunc(slices.Clone(sim.Intents), func(in domain.T212Intent) bool { return in.Side != side || in.Type != typ })
}

func TestT212ExecutorFillAndStop(t *testing.T) {
	x := newT212X(t)
	o, err := x.place(t, "req-1")
	if err != nil || o.State != domain.OrderFilled || o.EtoroOrderID <= 0 || o.PositionID != o.EtoroOrderID {
		t.Fatalf("buy = %+v %v", o, err)
	}
	ap := x.position(t, o.EtoroOrderID)
	if ap.Ticker != barc || ap.Quantity != 3_000_000 || !ap.Open() || ap.Instrument != 1 {
		t.Fatalf("position %+v", ap)
	}
	stops := x.orders(t, domain.OrderStop)
	if len(stops) != 1 || stops[0].State != domain.OrderPending || stops[0].Quantity != 3_000_000 || stops[0].StopLoss != 194_000_000 || stops[0].PositionID != ap.PositionID {
		t.Fatalf("stops %+v", stops)
	}
	if as := x.activeStops(t); len(as) != 1 || as[0].ID != stops[0].EtoroOrderID {
		t.Fatalf("active stops at Trading 212: %+v", as)
	}
	x.sound(t)
	if x.isBlocked(t) {
		t.Fatal("a good fill blocked the instrument")
	}
}

// TestT212ExecutorTable covers the executor's paths: every one keeps the order rows persisted
// before Place, each order Placed at most once, and only the three shapes.
func TestT212ExecutorTable(t *testing.T) {
	share := domain.Micros(1_000_000)
	tests := []struct {
		name  string
		setup func(t *testing.T, x *t212X)
		check func(t *testing.T, x *t212X, o domain.Order, err error)
	}{
		{
			"Trading 212 refuses the buy", func(t *testing.T, x *t212X) { x.sim.Script = []fakes.T212Outcome{fakes.T212Refused} },
			func(t *testing.T, x *t212X, o domain.Order, err error) {
				if !errors.Is(err, domain.ErrNotAccepted) || o.State != domain.OrderRejected || len(x.orders(t, domain.OrderStop)) != 0 {
					t.Fatalf("%+v %v", o, err)
				}
			},
		},
		{
			"Trading 212 rejects the buy after accepting it", func(t *testing.T, x *t212X) { x.sim.Script = []fakes.T212Outcome{fakes.T212Reject} },
			func(t *testing.T, x *t212X, o domain.Order, err error) {
				if err != nil || o.State != domain.OrderRejected || len(x.sim.Intents) != 1 {
					t.Fatalf("%+v %v", o, err)
				}
			},
		},
		{"stop rejected: sold at market, blocked, alerted", func(t *testing.T, x *t212X) {
			x.sim.Script = []fakes.T212Outcome{fakes.T212Fill, fakes.T212Reject}
		}, func(t *testing.T, x *t212X, o domain.Order, err error) {
			sells := x.orders(t, domain.OrderClose)
			if len(sells) != 1 || sells[0].State != domain.OrderFilled || x.position(t, o.EtoroOrderID).Open() || !x.isBlocked(t) {
				t.Fatalf("sells %+v", sells)
			}
			if !slices.Contains(x.events(t), "order.stop_failed") || len(x.sim.Held) != 0 {
				t.Fatalf("events %v held %v", x.events(t), x.sim.Held)
			}
		}},
		{"stop's answer lost and not there: it waits, then the position is sold", func(t *testing.T, x *t212X) {
			x.sim.Script = []fakes.T212Outcome{fakes.T212Fill, fakes.T212LostNotApplied}
		}, func(t *testing.T, x *t212X, o domain.Order, err error) {
			st := x.orders(t, domain.OrderStop)[0]
			if st.State != domain.OrderUnknown || !x.isBlocked(t) || len(intentsOf(x.sim, domain.T212Sell, domain.T212Market)) != 0 {
				t.Fatalf("stop %+v: the unknown stop must hold the sale back (no double sell)", st)
			}
			x.clock.Advance(domain.T212MatchAfter)
			if st, p := x.svc.resolveT212(x.ctx, x.a, st); st.State != domain.OrderAbandoned || p != "" {
				t.Fatalf("resolve = %+v %q", st, p)
			}
			ap := x.position(t, o.EtoroOrderID)
			if _, err := x.svc.closeT212Position(x.ctx, x.a, ap, "no stop", t212ClosePolls); err != nil {
				t.Fatal(err)
			}
			if x.position(t, o.EtoroOrderID).Open() || len(x.sim.Held) != 0 {
				t.Fatal("not sold once the stop was abandoned")
			}
		}},
		{"stop not listed in the open orders: sold", func(t *testing.T, x *t212X) {
			x.svc.d.T212 = hideStops{x.sim}
		}, func(t *testing.T, x *t212X, o domain.Order, err error) {
			if st := x.orders(t, domain.OrderStop)[0]; st.State != domain.OrderCancelled && st.State != domain.OrderFilled {
				t.Fatalf("stop %+v", st)
			}
			if x.position(t, o.EtoroOrderID).Open() || !x.isBlocked(t) || len(x.sim.Held) != 0 || len(x.activeStops(t)) != 0 {
				t.Fatalf("not sold: held %v", x.sim.Held)
			}
		}},
		{"fill more than 200 bp from the eToro quote: sold and blocked", func(t *testing.T, x *t212X) {
			x.sim.Prices[barc] = 205_000_000
		}, func(t *testing.T, x *t212X, o domain.Order, err error) {
			if o.State != domain.OrderFilled || x.position(t, o.EtoroOrderID).Open() || !x.isBlocked(t) || len(x.orders(t, domain.OrderStop)) != 0 {
				t.Fatalf("%+v", o)
			}
			if !slices.Contains(x.events(t), "order.nonconforming") {
				t.Fatalf("events %v", x.events(t))
			}
		}},
		{"partial fill: the rest cancelled at 60 s, a stop for the filled quantity", func(t *testing.T, x *t212X) {
			x.sim.Script = []fakes.T212Outcome{fakes.T212PartialFill}
		}, func(t *testing.T, x *t212X, o domain.Order, err error) {
			if o.State != domain.OrderCancelled || o.PositionID <= 0 {
				t.Fatalf("%+v %v", o, err)
			}
			ap := x.position(t, o.PositionID)
			st := x.orders(t, domain.OrderStop)
			if ap.Quantity != share || len(st) != 1 || st[0].Quantity != share || st[0].State != domain.OrderPending {
				t.Fatalf("position %+v stops %+v", ap, st)
			}
			if len(x.orders(t, domain.OrderCancel)) != 1 {
				t.Fatal("no cancel of the rest")
			}
		}},
		{"cancel loses to the fill: treated as filled", func(t *testing.T, x *t212X) {
			x.sim.Script = []fakes.T212Outcome{fakes.T212Pending}
			x.sim.CancelFillsFirst = true
		}, func(t *testing.T, x *t212X, o domain.Order, err error) {
			if o.State != domain.OrderFilled || x.position(t, o.EtoroOrderID).Quantity != 3*share || len(x.activeStops(t)) != 1 {
				t.Fatalf("%+v %v", o, err)
			}
		}},
		{"pending buy cancelled after 60 s", func(t *testing.T, x *t212X) {
			x.sim.Script = []fakes.T212Outcome{fakes.T212Pending}
		}, func(t *testing.T, x *t212X, o domain.Order, err error) {
			if o.State != domain.OrderCancelled || len(x.sim.CancelIDs) != 1 || len(x.orders(t, domain.OrderStop)) != 0 {
				t.Fatalf("%+v %v", o, err)
			}
		}},
		{"market closed: nothing sent", func(t *testing.T, x *t212X) {
			x.clock.T = time.Date(2026, 11, 23, 17, 0, 0, 0, time.UTC)
		}, func(t *testing.T, x *t212X, o domain.Order, err error) {
			if err == nil || o.State != domain.OrderAbandoned || len(x.sim.Intents) != 0 {
				t.Fatalf("%+v %v", o, err)
			}
		}},
		{"stale cash figure: nothing sent", func(t *testing.T, x *t212X) {
			x.age = time.Minute
		}, func(t *testing.T, x *t212X, o domain.Order, err error) {
			if !errors.Is(err, domain.ErrUnavailable) || o.State != domain.OrderAbandoned || len(x.sim.Intents) != 0 {
				t.Fatalf("%+v %v", o, err)
			}
		}},
		{"order row not storable: zero calls", func(t *testing.T, x *t212X) {
			e := x.entry("req-1")
			if _, err := x.svc.placeT212Entry(x.ctx, x.a, e, x.svc.riskLimitsFor(x.ctx, x.a)); err != nil {
				t.Fatal(err)
			}
			x.sim.Intents, x.sim.Places, x.sim.Reads = nil, 0, 0
		}, func(t *testing.T, x *t212X, o domain.Order, err error) {
			if err == nil || len(x.sim.Intents) != 0 || x.sim.Reads != 0 {
				t.Fatalf("%+v %v, intents %v", o, err, x.sim.Intents)
			}
		}},
		{"kill switch on: zero calls", func(t *testing.T, x *t212X) {
			x.svc.rt.latchKill(x.a.ID)
		}, func(t *testing.T, x *t212X, o domain.Order, err error) {
			if !errors.Is(err, domain.ErrConflict) || len(x.sim.Intents) != 0 || x.sim.Reads != 0 {
				t.Fatalf("%+v %v", o, err)
			}
		}},
		{"not reconciled: zero calls", func(t *testing.T, x *t212X) {
			x.svc.unreconcile(x.a.ID, "test")
		}, func(t *testing.T, x *t212X, o domain.Order, err error) {
			if !errors.Is(err, domain.ErrUnavailable) || len(x.sim.Intents) != 0 {
				t.Fatalf("%+v %v", o, err)
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			x := newT212X(t)
			tc.setup(t, x)
			o, err := x.place(t, "req-1")
			tc.check(t, x, o, err)
			x.sound(t)
		})
	}
}

// hideStops is the simulated account whose open orders never list a stop (T212-h: a stop not
// confirmed).
type hideStops struct{ *fakes.T212Sim }

func (h hideStops) OpenOrders(ctx context.Context) ([]domain.BrokerOrder, error) {
	os, err := h.T212Sim.OpenOrders(ctx)
	return slices.DeleteFunc(os, func(o domain.BrokerOrder) bool { return o.Type == domain.T212StopOrder }), err
}

func (h hideStops) Order(ctx context.Context, id int64) (domain.BrokerOrder, error) {
	o, err := h.T212Sim.Order(ctx, id)
	if err == nil && o.Type == domain.T212StopOrder {
		return domain.BrokerOrder{}, domain.ErrNotFound
	}
	return o, err
}

func TestT212ExecutorLostAnswer(t *testing.T) {
	t.Run("applied: adopted from the history, then protected", func(t *testing.T) {
		x := newT212X(t)
		x.sim.Script = []fakes.T212Outcome{fakes.T212LostApplied}
		o, err := x.place(t, "req-1")
		if !errors.Is(err, domain.ErrOutcomeUnknown) || o.State != domain.OrderUnknown || len(x.sim.Intents) != 1 {
			t.Fatalf("%+v %v", o, err)
		}
		x.clock.Advance(5 * time.Second)
		o, p := x.svc.resolveT212(x.ctx, x.a, o)
		if p != "" || o.State != domain.OrderFilled || o.EtoroOrderID <= 0 || len(x.activeStops(t)) != 1 {
			t.Fatalf("%+v %q", o, p)
		}
		x.sound(t)
	})
	t.Run("not applied: abandoned after 2 minutes, never resent", func(t *testing.T) {
		x := newT212X(t)
		x.sim.Script = []fakes.T212Outcome{fakes.T212LostNotApplied}
		o, _ := x.place(t, "req-1")
		if o, p := x.svc.resolveT212(x.ctx, x.a, o); o.State != domain.OrderUnknown || p == "" {
			t.Fatalf("resolved too early: %+v", o)
		}
		x.clock.Advance(domain.T212MatchAfter)
		if o, p := x.svc.resolveT212(x.ctx, x.a, o); o.State != domain.OrderAbandoned || p != "" {
			t.Fatalf("%+v %q", o, p)
		}
		if len(x.sim.Intents) != 1 {
			t.Fatalf("resent: %v", x.sim.Intents)
		}
		x.sound(t)
	})
	t.Run("ambiguous: stays unknown until the owner resolves it", func(t *testing.T) {
		x := newT212X(t)
		x.sim.Script = []fakes.T212Outcome{fakes.T212LostNotApplied}
		o, _ := x.place(t, "req-1")
		// Two orders of the same shape from the API (another tool of the owner's).
		bctx := ports.WithAccount(x.ctx, x.a)
		x.sim.OnPlace = nil
		var ids []int64
		for i := range 2 {
			bo, err := x.sim.Place(bctx, domain.T212Intent{RequestID: "other-" + string(rune('a'+i)), Ticker: barc, Quantity: 3_000_000, Side: domain.T212Buy, Type: domain.T212Market})
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, bo.ID)
		}
		x.sim.OnPlace = fakes.RequireT212OrderRow(x.store.Orders())
		x.clock.Advance(domain.T212MatchAfter)
		o, p := x.svc.resolveT212(x.ctx, x.a, o)
		if o.State != domain.OrderUnknown || p == "" || !slices.Contains(x.events(t), "order.ambiguous") {
			t.Fatalf("%+v %q", o, p)
		}
		api := x.owner
		api.Via = ViaAPIKey
		if _, err := x.svc.ResolveT212Order(x.ctx, api, o.RequestID, ids[0], false); !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("API key: %v", err)
		}
		if _, err := x.svc.ResolveT212Order(x.ctx, x.owner, o.RequestID, 0, false); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("neither: %v", err)
		}
		if _, err := x.svc.ResolveT212Order(x.ctx, x.owner, o.RequestID, 424242, false); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("unknown id: %v", err)
		}
		n, err := x.svc.ResolveT212Order(x.ctx, x.owner, o.RequestID, ids[1], false)
		if err != nil || n.State != domain.OrderFilled || n.EtoroOrderID != ids[1] || audited(x.env, "t212.order_resolved") != 1 {
			t.Fatalf("%+v %v", n, err)
		}
		if _, err := x.svc.ResolveT212Order(x.ctx, x.owner, o.RequestID, 0, true); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("resolved twice: %v", err)
		}
		x.sound(t)
	})
	t.Run("not placed: the owner abandons it", func(t *testing.T) {
		x := newT212X(t)
		x.sim.Script = []fakes.T212Outcome{fakes.T212RateLimited}
		o, _ := x.place(t, "req-1")
		n, err := x.svc.ResolveT212Order(x.ctx, x.owner, o.RequestID, 0, true)
		if err != nil || n.State != domain.OrderAbandoned {
			t.Fatalf("%+v %v", n, err)
		}
	})
}

func TestT212ExecutorClose(t *testing.T) {
	share := domain.Micros(1_000_000)
	filled := func(t *testing.T) (*t212X, domain.AgentPosition, domain.Order) {
		x := newT212X(t)
		o, err := x.place(t, "req-1")
		if err != nil {
			t.Fatal(err)
		}
		return x, x.position(t, o.EtoroOrderID), x.orders(t, domain.OrderStop)[0]
	}
	t.Run("cancels the stop first, then sells what was bought", func(t *testing.T) {
		x, ap, st := filled(t)
		o, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", t212ClosePolls)
		if err != nil || o.State != domain.OrderFilled || o.Quantity != 3*share {
			t.Fatalf("%+v %v", o, err)
		}
		if len(x.sim.CancelIDs) != 1 || x.sim.CancelIDs[0] != st.EtoroOrderID {
			t.Fatalf("cancels %v", x.sim.CancelIDs)
		}
		// The cancel was sent before the sell.
		cancels := x.orders(t, domain.OrderCancel)
		if len(cancels) != 1 || cancels[0].CreatedAt.After(o.CreatedAt) {
			t.Fatalf("cancel %+v sell %+v", cancels, o)
		}
		if x.position(t, ap.PositionID).Open() || len(x.sim.Held) != 0 || len(x.activeStops(t)) != 0 {
			t.Fatal("still open")
		}
		x.sound(t)
	})
	t.Run("the stop fired meanwhile: nothing sold", func(t *testing.T) {
		x, ap, _ := filled(t)
		if x.sim.TriggerStop(barc, 190_000_000) != 1 {
			t.Fatal("stop did not fire")
		}
		o, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", t212ClosePolls)
		if err != nil || o.RequestID != "" || len(intentsOf(x.sim, domain.T212Sell, domain.T212Market)) != 0 {
			t.Fatalf("%+v %v", o, err)
		}
		if x.position(t, ap.PositionID).Open() || !slices.Contains(x.events(t), "position.stop_hit") {
			t.Fatal("not settled by the stop")
		}
		x.sound(t)
	})
	t.Run("never more than held: only what is available is sold", func(t *testing.T) {
		x, ap, _ := filled(t)
		p := x.sim.Held[barc]
		p.QuantityAvailable = share // the rest is locked (T13)
		x.sim.Held[barc] = p
		o, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", t212ClosePolls)
		if err != nil || o.Quantity != share || o.State != domain.OrderFilled {
			t.Fatalf("%+v %v", o, err)
		}
		if !x.position(t, ap.PositionID).Open() || !slices.Contains(x.events(t), "order.partial_close") {
			t.Fatal("a partial sale closed the position")
		}
		x.sound(t)
	})
	t.Run("only what cactusTrade bought: the owner's shares stay", func(t *testing.T) {
		x, ap, _ := filled(t)
		p := x.sim.Held[barc]
		p.Quantity += 10 * share
		p.QuantityAvailable += 10 * share
		x.sim.Held[barc] = p
		o, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", t212ClosePolls)
		if err != nil || o.Quantity != 3*share || x.sim.Held[barc].Quantity != 10*share {
			t.Fatalf("%+v %v held %v", o, err, x.sim.Held)
		}
		x.sound(t)
	})
	t.Run("market closed: no market order, an alert", func(t *testing.T) {
		x, ap, _ := filled(t)
		x.clock.T = time.Date(2026, 11, 23, 17, 0, 0, 0, time.UTC)
		if _, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", nil); !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("closed market: %v", err)
		}
		if len(intentsOf(x.sim, domain.T212Sell, domain.T212Market)) != 0 || len(x.activeStops(t)) != 1 || !slices.Contains(x.events(t), "order.exit_waits") {
			t.Fatal("sent outside the session or dropped the stop")
		}
		x.sound(t)
	})
	t.Run("the position is gone: settled, nothing sold", func(t *testing.T) {
		x, ap, _ := filled(t)
		delete(x.sim.Held, barc)
		// Right after the fill a missing position may only be listed late (review B2): the
		// sale waits, the position stays ours.
		if _, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", nil); !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("within the grace: %v", err)
		}
		if !x.position(t, ap.PositionID).Open() {
			t.Fatal("settled as gone right after its fill")
		}
		x.clock.Advance(fillVisibleAfter)
		if _, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", nil); err != nil {
			t.Fatal(err)
		}
		if x.position(t, ap.PositionID).Open() || len(intentsOf(x.sim, domain.T212Sell, domain.T212Market)) != 0 {
			t.Fatal("not settled")
		}
		x.sound(t)
	})
}

// TestT212ExecutorRealAccountNoCalls: a real Trading 212 account never reaches the broker,
// whatever the executor is asked.
func TestT212ExecutorRealAccountNoCalls(t *testing.T) {
	x := newT212X(t)
	rl := x.t212Rl.Account
	x.sim.Reads = 0
	ap := domain.AgentPosition{AccountID: rl.ID, PositionID: 7, Ticker: barc, Quantity: 1_000_000, Instrument: 1, Direction: domain.Long}
	tgt := domain.Order{RequestID: "t", AccountID: rl.ID, Platform: domain.PlatformTrading212, EtoroOrderID: 5}
	_, e1 := x.svc.placeT212Entry(x.ctx, rl, x.entry("req-r"), domain.DefaultRiskLimits)
	_, e2 := x.svc.closeT212Position(x.ctx, rl, ap, "x", nil)
	_, e3 := x.svc.placeT212Stop(x.ctx, rl, ap, 100)
	_, e4 := x.svc.cancelT212(x.ctx, rl, tgt, "x")
	for i, err := range []error{e1, e2, e3, e4} {
		if !errors.Is(err, domain.ErrRealModeGated) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(x.sim.Intents) != 0 || len(x.sim.CancelIDs) != 0 || x.sim.Reads != 0 || len(x.sim.Violations) != 0 {
		t.Fatalf("the broker was called: %+v", x.sim)
	}
	if _, err := x.svc.placeT212Entry(x.ctx, x.etoro.Account, x.entry("req-e"), domain.DefaultRiskLimits); !errors.Is(err, domain.ErrViewOnly) {
		t.Fatalf("eToro account: %v", err)
	}
}
