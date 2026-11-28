package app

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// Tests for the review of R45.6-R45.8 (B1-B4, MAJOR, minors a and g). Each fails on the code
// before the fix. The owner holds 10 shares of BARC besides the 3 cactusTrade bought, so a
// sale of more than cactusTrade's own would sell the owner's.

const oneShare = domain.Micros(1_000_000)

// ownerHolds10 adds 10 shares the owner bought by hand to the ticker.
func (x *t212X) ownerHolds10() {
	p := x.sim.Held[barc]
	p.Ticker = barc
	p.Quantity += 10 * oneShare
	p.QuantityAvailable += 10 * oneShare
	x.sim.Held[barc] = p
}

func (x *t212X) filledWithStop(t *testing.T) (domain.AgentPosition, domain.Order) {
	t.Helper()
	o, err := x.place(t, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	x.ownerHolds10()
	return x.position(t, o.EtoroOrderID), x.orders(t, domain.OrderStop)[0]
}

func (x *t212X) hasEvent(t *testing.T, kind string) bool {
	t.Helper()
	return slices.Contains(x.events(t), kind)
}

// B1: a stop that filled only in part is not "fired": the rest is sold, never left held.
func TestT212ReviewPartialStop(t *testing.T) {
	t.Run("on a close", func(t *testing.T) {
		x := newT212X(t)
		ap, st := x.filledWithStop(t)
		if !x.sim.FillPart(st.EtoroOrderID, oneShare) {
			t.Fatal("no stop to fill")
		}
		if _, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", t212ClosePolls); err != nil {
			t.Fatal(err)
		}
		if got := x.position(t, ap.PositionID); got.Open() || got.Sold != 3*oneShare {
			t.Fatalf("position %+v", got)
		}
		if held := x.sim.Held[barc].Quantity; held != 10*oneShare {
			t.Fatalf("%s held, want the owner's 10", held)
		}
		if !x.hasEvent(t, "position.stop_partial") {
			t.Fatalf("events %v", x.events(t))
		}
		x.sound(t)
	})
	t.Run("found by the supervisor", func(t *testing.T) {
		x := newT212X(t)
		ap, st := x.filledWithStop(t)
		x.sim.FillPart(st.EtoroOrderID, oneShare)
		if err := x.sim.Cancel(ports.WithAccount(x.ctx, x.a), st.EtoroOrderID); err != nil {
			t.Fatal(err)
		}
		x.clock.Advance(t212StopConfirm + time.Second)
		if _, err := x.svc.Supervise(x.ctx, x.a.ID); err != nil {
			t.Fatal(err)
		}
		got := x.position(t, ap.PositionID)
		if held := x.sim.Held[barc].Quantity; held != 10*oneShare {
			t.Fatalf("%s held after the supervisor (position %+v), want the owner's 10", held, got)
		}
		if got.Open() || got.Sold != 3*oneShare {
			t.Fatalf("position %+v", got)
		}
		x.sound(t)
	})
}

// lagT212 is Trading 212 listing a fresh fill late: its first lag account reads hide the
// ticker, and a sell is refused (more than available, by the adapter) until it is listed.
type lagT212 struct {
	ports.T212Broker
	mu     sync.Mutex
	lag    int
	listed bool
}

func (l *lagT212) Account(ctx context.Context) (domain.T212Account, error) {
	acct, err := l.T212Broker.Account(ctx)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil && l.lag > 0 {
		l.lag--
		acct.Positions = slices.DeleteFunc(acct.Positions, func(p domain.T212Position) bool { return p.Ticker == barc })
		return acct, nil
	}
	l.listed = true
	return acct, err
}

func (l *lagT212) Place(ctx context.Context, in domain.T212Intent) (domain.BrokerOrder, error) {
	l.mu.Lock()
	listed := l.listed
	l.mu.Unlock()
	if in.Side == domain.T212Sell && !listed {
		return domain.BrokerOrder{}, &domain.NotAcceptedError{Reason: "a sell of more than the 0 available"}
	}
	return l.T212Broker.Place(ctx, in)
}

// B2: a fill Trading 212 lists late gets its stop once listed; it is never settled as gone.
func TestT212ReviewLateListing(t *testing.T) {
	x := newT212X(t)
	x.svc.d.T212 = &lagT212{T212Broker: x.sim, lag: 2}
	o, err := x.place(t, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	ap := x.position(t, o.EtoroOrderID)
	if !ap.Open() || x.hasEvent(t, "order.stop_failed") {
		t.Fatalf("position %+v events %v", ap, x.events(t))
	}
	if as := x.activeStops(t); len(as) != 1 || as[0].Quantity != 3*oneShare {
		t.Fatalf("stops at Trading 212: %+v", as)
	}
	if len(intentsOf(x.sim, domain.T212Sell, domain.T212Market)) != 0 {
		t.Fatal("sold")
	}
	x.sound(t)
}

// B3: a stop whose lost answer is matched as filled during the close settles the position:
// nothing more is sold (the owner's shares stay).
func TestT212ReviewStopMatchedFilledDuringClose(t *testing.T) {
	x := newT212X(t)
	now := x.clock.Now().UTC()
	x.sim.Held[barc] = domain.T212Position{Ticker: barc, Quantity: 13 * oneShare, QuantityAvailable: 13 * oneShare, Cost: 2_600, OpenedAt: now}
	ap := domain.AgentPosition{
		AccountID: x.a.ID, PositionID: 777, OpenRequestID: "buy-777", Instrument: 1, Direction: domain.Long,
		OpenedAt: now.Add(-10 * time.Minute), Ticker: barc, Quantity: 3 * oneShare,
	}
	if err := x.store.AgentPositions().Add(x.ctx, ap); err != nil {
		t.Fatal(err)
	}
	st, err := domain.NewOrder(domain.Order{
		RequestID: "stop-lost", AccountID: x.a.ID, Mode: domain.ModeDemo, Kind: domain.OrderStop, Platform: domain.PlatformTrading212,
		Ticker: barc, Quantity: 3 * oneShare, Direction: domain.Long, Instrument: 1, StopLoss: 194_000_000, PositionID: 777,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.store.Orders().Create(x.ctx, st); err != nil {
		t.Fatal(err)
	}
	sent, _ := st.Transition(domain.OrderSent, now, "")
	if err := x.store.Orders().Update(x.ctx, st, sent); err != nil {
		t.Fatal(err)
	}
	lost, err := sent.Transition(domain.OrderUnknown, now, "the answer was lost")
	if err != nil {
		t.Fatal(err)
	}
	if err := x.store.Orders().Update(x.ctx, sent, lost); err != nil {
		t.Fatal(err)
	}
	// It did reach Trading 212, and fired there.
	x.sim.OnPlace = nil
	if _, err := x.sim.Place(ports.WithAccount(x.ctx, x.a), domain.T212Intent{
		RequestID: "stop-lost", Ticker: barc, Quantity: 3 * oneShare, Side: domain.T212Sell, Type: domain.T212StopOrder, StopPrice: 194_000_000,
	}); err != nil {
		t.Fatal(err)
	}
	if x.sim.TriggerStop(barc, 190_000_000) != 1 {
		t.Fatal("the stop did not fire")
	}
	x.sim.Intents = nil
	if _, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", t212ClosePolls); err != nil {
		t.Fatal(err)
	}
	if held := x.sim.Held[barc].Quantity; held != 10*oneShare {
		t.Fatalf("%s held, want the owner's 10", held)
	}
	if got := x.position(t, 777); got.Open() || len(x.sim.Intents) != 0 {
		t.Fatalf("position %+v, intents %+v", got, x.sim.Intents)
	}
}

// cancelHook runs during once, inside the first Cancel (before Trading 212 sees it).
type cancelHook struct {
	ports.T212Broker
	once   sync.Once
	during func()
}

func (c *cancelHook) Cancel(ctx context.Context, id int64) error {
	c.once.Do(c.during)
	return c.T212Broker.Cancel(ctx, id)
}

// MAJOR: two exits of the same position (the kill and the supervisor) never run together:
// the second waits for the first, reads the position again and sells nothing.
func TestT212ReviewOneExitAtATime(t *testing.T) {
	x := newT212X(t)
	ap, _ := x.filledWithStop(t)
	var (
		secondDone  = make(chan error, 1)
		overlapping bool
	)
	x.svc.d.T212 = &cancelHook{T212Broker: x.sim, during: func() {
		go func() {
			_, err := x.svc.closeT212Position(x.ctx, x.a, ap, "supervisor", nil)
			secondDone <- err
		}()
		select {
		case <-secondDone:
			overlapping = true
		case <-time.After(300 * time.Millisecond):
		}
	}}
	if _, err := x.svc.closeT212Position(x.ctx, x.a, ap, "kill", t212ClosePolls); err != nil {
		t.Fatal(err)
	}
	if overlapping {
		t.Fatal("a second exit of the position ran while the first was selling it")
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second exit: %v", err)
	}
	if sells := intentsOf(x.sim, domain.T212Sell, domain.T212Market); len(sells) != 1 || sells[0].Quantity != 3*oneShare {
		t.Fatalf("sells %+v", sells)
	}
	if held := x.sim.Held[barc].Quantity; held != 10*oneShare {
		t.Fatalf("%s held, want the owner's 10", held)
	}
	x.sound(t)
}

// Minor a: a sale still working after every cancel was refused raises a danger event once.
func TestT212ReviewStuckSaleAlert(t *testing.T) {
	x := newT212X(t)
	ap, _ := x.filledWithStop(t)
	x.sim.Script = []fakes.T212Outcome{fakes.T212Pending}
	sale, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", nil)
	if err != nil || sale.State != domain.OrderPending {
		t.Fatalf("sale %+v %v", sale, err)
	}
	x.sim.CancelErr = &domain.NotAcceptedError{Reason: "cannot cancel now"}
	for range maxCancels + 1 {
		x.clock.Advance(pendingMax + time.Second)
		if _, err := x.svc.closeT212Position(x.ctx, x.a, x.position(t, ap.PositionID), "flatten", nil); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(x.sim.CancelIDs); got != 1+maxCancels { // the stop's cancel, then the sale's
		t.Fatalf("%d cancels sent", got)
	}
	es, err := x.store.Events().List(x.ctx, []string{x.a.ID}, 500)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range es {
		if e.Kind == "order.sale_stuck" {
			n++
			if e.Severity != domain.SeverityDanger {
				t.Fatalf("severity %s", e.Severity)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d stuck-sale alerts, want 1 (events %v)", n, x.events(t))
	}
}

// Minor g: a buy finalised while the kill flattens the account gets no stop; the kill sells it.
func TestT212ReviewKillGivesNoStopToAFreshFill(t *testing.T) {
	x := newT212X(t)
	advance := x.svc.d.Sleep
	x.sim.Script = []fakes.T212Outcome{fakes.T212Pending}
	x.svc.d.Sleep = func(context.Context, time.Duration) error { return context.Canceled } // the follow-up stops
	o, _ := x.place(t, "req-1")
	x.svc.d.Sleep = advance
	if o.State != domain.OrderPending || !x.sim.Fill(o.EtoroOrderID) {
		t.Fatalf("buy %+v", o)
	}
	if _, err := x.svc.Kill(x.ctx, x.owner, x.a.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if stops := intentsOf(x.sim, domain.T212Sell, domain.T212StopOrder); len(stops) != 0 {
		t.Fatalf("a stop placed during the kill: %+v", stops)
	}
	if sells := intentsOf(x.sim, domain.T212Sell, domain.T212Market); len(sells) != 1 {
		t.Fatalf("sells %+v", sells)
	}
	if len(x.sim.Held) != 0 {
		t.Fatalf("held %+v", x.sim.Held)
	}
	x.sound(t)
}
