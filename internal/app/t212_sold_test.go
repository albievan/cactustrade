package app

import (
	"slices"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// TestT212SoldNeverTheOwnersShares (D44: only what cactusTrade bought is ever sold): the owner
// holds 10 shares of the same ticker besides the 3 cactusTrade bought. A sale fills only 1
// share and the rest is cancelled; the next sale sells the 2 left (never 3), a new stop covers
// only those 2, and the owner keeps all 10.
func TestT212SoldNeverTheOwnersShares(t *testing.T) {
	const share = domain.Micros(1_000_000)
	setup := func(t *testing.T) (*t212X, domain.AgentPosition) {
		t.Helper()
		x := newT212X(t)
		o, err := x.place(t, "req-1")
		if err != nil {
			t.Fatal(err)
		}
		// The owner buys 10 shares of the same ticker by hand.
		p := x.sim.Held[barc]
		p.Quantity += 10 * share
		p.QuantityAvailable += 10 * share
		x.sim.Held[barc] = p
		ap := x.position(t, o.EtoroOrderID)
		// The sale fills 1 of 3 and the rest stays working.
		x.sim.Script = []fakes.T212Outcome{fakes.T212PartialFill}
		s1, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", nil)
		if err != nil || s1.Quantity != 3*share {
			t.Fatalf("first sale %+v %v", s1, err)
		}
		if s1, err = x.svc.pollT212(x.ctx, x.a, s1); err != nil || s1.State != domain.OrderPartial || s1.Filled != share {
			t.Fatalf("first sale after a poll: %+v %v", s1, err)
		}
		// While it works, its rest (2) is resting and its fill (1) is sold.
		if r, err := x.svc.t212RestingSells(x.ctx, x.a.ID, barc, ""); err != nil || r != 2*share {
			t.Fatalf("resting %s %v", r, err)
		}
		if ap = x.position(t, ap.PositionID); ap.Sold != share || ap.Unsold() != 2*share || !ap.Open() {
			t.Fatalf("position after a partial fill: %+v", ap)
		}
		// Trading 212 ends the rest (cancelled after the partial fill).
		if err := x.sim.Cancel(ports.WithAccount(x.ctx, x.a), s1.EtoroOrderID); err != nil {
			t.Fatal(err)
		}
		if s1, err = x.svc.pollT212(x.ctx, x.a, s1); err != nil || s1.State != domain.OrderCancelled || s1.Filled != share {
			t.Fatalf("first sale ended: %+v %v", s1, err)
		}
		if !slices.Contains(x.events(t), "order.partial_close") {
			t.Fatalf("no partial close event: %v", x.events(t))
		}
		if ap = x.position(t, ap.PositionID); !ap.Open() || ap.Sold != share {
			t.Fatalf("position after the partial sale: %+v", ap)
		}
		if got := x.sim.Held[barc].Quantity; got != 12*share {
			t.Fatalf("held %s after selling 1 of 13", got)
		}
		return x, ap
	}
	t.Run("the next sale sells only the rest", func(t *testing.T) {
		x, ap := setup(t)
		s2, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", t212ClosePolls)
		if err != nil || s2.Quantity != 2*share || s2.State != domain.OrderFilled || s2.Filled != 2*share {
			t.Fatalf("second sale %+v %v", s2, err)
		}
		if ap = x.position(t, ap.PositionID); ap.Open() || ap.Sold != 3*share {
			t.Fatalf("position after the second sale: %+v", ap)
		}
		if got := x.sim.Held[barc].Quantity; got != 10*share {
			t.Fatalf("the owner holds %s, want 10 shares", got)
		}
		// Nothing more is ever sold.
		if _, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", nil); err == nil {
			t.Fatal("a closed position was sold again")
		}
		if got := x.sim.Held[barc].Quantity; got != 10*share {
			t.Fatalf("the owner holds %s after a second close", got)
		}
		x.sound(t)
	})
	t.Run("a new stop covers only the rest", func(t *testing.T) {
		x, ap := setup(t)
		st, err := x.svc.placeT212Stop(x.ctx, x.a, ap, 194_000_000)
		if err != nil || st.Quantity != 2*share || st.State != domain.OrderPending {
			t.Fatalf("stop %+v %v", st, err)
		}
		if n := x.sim.TriggerStop(barc, 190_000_000); n != 1 {
			t.Fatalf("%d stops fired", n)
		}
		if _, err := x.svc.pollT212(x.ctx, x.a, st); err != nil {
			t.Fatal(err)
		}
		if ap = x.position(t, ap.PositionID); ap.Open() || ap.Sold != 3*share {
			t.Fatalf("position after the stop: %+v", ap)
		}
		if got := x.sim.Held[barc].Quantity; got != 10*share {
			t.Fatalf("the owner holds %s, want 10 shares", got)
		}
		x.sound(t)
	})
	t.Run("a sale is sized after the clock moves on", func(t *testing.T) {
		x, ap := setup(t)
		x.clock.Advance(time.Second)
		s2, err := x.svc.closeT212Position(x.ctx, x.a, ap, "kill", nil)
		if err != nil || s2.Quantity != 2*share {
			t.Fatalf("%+v %v", s2, err)
		}
		for _, in := range intentsOf(x.sim, domain.T212Sell, domain.T212Market) {
			if in.Quantity > 3*share {
				t.Fatalf("a sell of %s", in.Quantity)
			}
		}
		x.sound(t)
	})
}
