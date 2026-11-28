package app

import (
	"context"
	"errors"
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

// Tests for the re-review of 2026-11-28. Each fails on the code before the fix.

// noHistory is Trading 212 whose order history lags (T4, T5b unverified): while hide is set,
// a finished order is listed nowhere (neither active nor in the history).
type noHistory struct {
	ports.T212Broker
	mu   sync.Mutex
	hide bool
}

func (n *noHistory) set(hide bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.hide = hide
}

func (n *noHistory) History(ctx context.Context, ticker, cursor string) (domain.T212HistoryPage, error) {
	n.mu.Lock()
	hide := n.hide
	n.mu.Unlock()
	if hide {
		return domain.T212HistoryPage{}, nil
	}
	return n.T212Broker.History(ctx, ticker, cursor)
}

// BLOCKER: a stop that fired while Trading 212 lists it nowhere is never taken for cancelled:
// the sale waits (the owner's 10 shares stay), the account takes no entries, a danger alert
// follows; once the fill is listed the position is settled by it and nothing is sold.
func TestT212ReReviewUnlistedStopIsNotGone(t *testing.T) {
	x := newT212X(t)
	ap, st := x.filledWithStop(t)
	nh := &noHistory{T212Broker: x.sim, hide: true}
	x.svc.d.T212 = nh
	if x.sim.TriggerStop(barc, 190_000_000) != 1 {
		t.Fatal("the stop did not fire")
	}
	if _, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", t212ClosePolls); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("close: %v", err)
	}
	if held := x.sim.Held[barc].Quantity; held != 10*oneShare {
		t.Fatalf("%s held, want the owner's 10 (the sale sold the owner's shares)", held)
	}
	if got := x.order(t, st.RequestID); got.State.Final() {
		t.Fatalf("stop %+v taken for gone without an answer", got)
	}
	if !x.position(t, ap.PositionID).Open() || x.svc.Reconciled(x.a.ID).OK {
		t.Fatalf("position %+v, reconciled %+v", x.position(t, ap.PositionID), x.svc.Reconciled(x.a.ID))
	}
	// Still not listed after the bound: the supervisor keeps looking, a danger alert, no sale.
	x.clock.Advance(t212UnlistedAlertAfter + time.Second)
	if _, err := x.svc.Supervise(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", t212ClosePolls); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("close after the bound: %v", err)
	}
	if !x.hasEvent(t, "order.unlisted") || len(intentsOf(x.sim, domain.T212Sell, domain.T212Market)) != 0 {
		t.Fatalf("events %v sells %+v", x.events(t), intentsOf(x.sim, domain.T212Sell, domain.T212Market))
	}
	if rc, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || rc.OK {
		t.Fatalf("reconciled while the stop is listed nowhere: %+v %v", rc, err)
	}
	if cancels := len(x.sim.CancelIDs); cancels > maxCancels {
		t.Fatalf("%d cancels sent for one stop", cancels)
	}
	// Trading 212 lists the fill: the stop settles the position, nothing is sold.
	nh.set(false)
	if _, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", t212ClosePolls); err != nil {
		t.Fatal(err)
	}
	if got := x.position(t, ap.PositionID); got.Open() || got.Sold != 3*oneShare || x.order(t, st.RequestID).State != domain.OrderFilled {
		t.Fatalf("position %+v", got)
	}
	if held := x.sim.Held[barc].Quantity; held != 10*oneShare || len(intentsOf(x.sim, domain.T212Sell, domain.T212Market)) != 0 {
		t.Fatalf("%s held, want the owner's 10", held)
	}
	if rc, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || !rc.OK {
		t.Fatalf("not reconciled once listed: %+v %v", rc, err)
	}
	x.sound(t)
}

// BLOCKER (the same root cause): a sale whose answer was lost, that filled while Trading 212
// lists it nowhere, is never abandoned after T212MatchAfter: no second sale.
func TestT212ReReviewUnlistedSaleIsNotAbandoned(t *testing.T) {
	x := newT212X(t)
	ap, _ := x.filledWithStop(t)
	nh := &noHistory{T212Broker: x.sim, hide: true}
	x.svc.d.T212 = nh
	x.sim.Script = []fakes.T212Outcome{fakes.T212LostApplied}
	// The stop's cancel is listed nowhere either: the sale would wait. Cancel it first, with
	// the history shown, so the sale is sent.
	nh.set(false)
	st := x.orders(t, domain.OrderStop)[0]
	if gone, fired, err := x.svc.cancelT212Stop(x.ctx, x.a, st, "test"); err != nil || !gone || fired {
		t.Fatalf("cancel %v %v %v", gone, fired, err)
	}
	nh.set(true)
	sale, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", nil)
	if !errors.Is(err, domain.ErrOutcomeUnknown) || sale.State != domain.OrderUnknown {
		t.Fatalf("sale %+v %v", sale, err)
	}
	x.clock.Advance(domain.T212MatchAfter + time.Second)
	for range 3 {
		if _, err := x.svc.Supervise(x.ctx, x.a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", nil); err != nil {
			t.Fatal(err)
		}
		x.clock.Advance(SupervisorInterval)
	}
	if got := x.order(t, sale.RequestID); got.State != domain.OrderUnknown || !x.hasEvent(t, "order.unmatched") {
		t.Fatalf("sale %+v events %v", got, x.events(t))
	}
	if sells := intentsOf(x.sim, domain.T212Sell, domain.T212Market); len(sells) != 1 {
		t.Fatalf("sold again: %+v", sells)
	}
	if held := x.sim.Held[barc].Quantity; held != 10*oneShare {
		t.Fatalf("%s held, want the owner's 10", held)
	}
	// Not placed is refused: Trading 212 lists it now, and it is matched instead.
	nh.set(false)
	if _, err := x.svc.ResolveT212Order(x.ctx, x.owner, sale.RequestID, 0, true); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("not placed while listed: %v", err)
	}
	if _, err := x.svc.Supervise(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	if got := x.position(t, ap.PositionID); got.Open() || got.Sold != 3*oneShare {
		t.Fatalf("position %+v", got)
	}
	x.sound(t)
}

// MAJOR: a fill Trading 212 lists far too late is never settled as gone (and then held
// without a stop overnight): it stays open, the account takes no entries, a danger alert;
// once listed it is protected or sold like any position.
func TestT212ReReviewLateFillStaysOpen(t *testing.T) {
	x := newT212X(t)
	lag := &lagT212{T212Broker: x.sim, lag: 1000}
	x.svc.d.T212 = lag
	o, err := x.place(t, "req-1")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		x.clock.Advance(SupervisorInterval)
		if _, err := x.svc.Supervise(x.ctx, x.a.ID); err != nil {
			t.Fatal(err)
		}
	}
	ap := x.position(t, o.EtoroOrderID)
	if !ap.Open() || !x.hasEvent(t, "position.unlisted") || x.svc.Reconciled(x.a.ID).OK {
		t.Fatalf("position %+v reconciled %+v events %v", ap, x.svc.Reconciled(x.a.ID), x.events(t))
	}
	if why := x.svc.t212Gate(x.ctx, x.a, x.svc.riskLimitsFor(x.ctx, x.a)); why == "" {
		t.Fatal("entries allowed while a position of ours is not listed")
	}
	if len(intentsOf(x.sim, domain.T212Sell, domain.T212Market)) != 0 {
		t.Fatal("sold")
	}
	// Trading 212 lists it: in session it has no confirmed stop, so it is sold (T212-h).
	lag.mu.Lock()
	lag.lag = 0
	lag.mu.Unlock()
	x.clock.Advance(SupervisorInterval)
	if _, err := x.svc.Supervise(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	if got := x.position(t, ap.PositionID); got.Open() || got.Sold != 3*oneShare || len(x.sim.Held) != 0 {
		t.Fatalf("position %+v held %v", got, x.sim.Held)
	}
	if rc, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || !rc.OK {
		t.Fatalf("not reconciled once settled: %+v %v", rc, err)
	}
	x.sound(t)
}

// Minor 1: t212Vanished, the supervisor's stop placement and ResolveT212Order take the
// account's exit lock: none runs while an exit holds it.
func TestT212ReReviewExitLock(t *testing.T) {
	calls := map[string]func(x *t212X, ap domain.AgentPosition, lost domain.Order) error{
		"t212Vanished": func(x *t212X, ap domain.AgentPosition, _ domain.Order) error {
			x.svc.t212Vanished(x.ctx, x.a, ap)
			return nil
		},
		"supervisor stop": func(x *t212X, ap domain.AgentPosition, _ domain.Order) error {
			return x.svc.t212ProtectUnlisted(x.ctx, x.a, ap)
		},
		"ResolveT212Order": func(x *t212X, _ domain.AgentPosition, lost domain.Order) error {
			_, err := x.svc.ResolveT212Order(x.ctx, x.owner, lost.RequestID, 0, true)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			x := newT212X(t)
			o, err := x.place(t, "req-1")
			if err != nil {
				t.Fatal(err)
			}
			ap := x.position(t, o.EtoroOrderID)
			x.sim.Script = []fakes.T212Outcome{fakes.T212RateLimited}
			lost, _ := x.svc.closeT212Position(x.ctx, x.a, ap, "flatten", nil)
			if lost.State != domain.OrderUnknown {
				t.Fatalf("sale %+v", lost)
			}
			_, unlock, err := x.svc.t212ExitLock(x.ctx, x.a.ID)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = call(x, ap, lost)
			}()
			select {
			case <-done:
				unlock()
				t.Fatalf("%s ran while another exit held the account's exit lock", name)
			case <-time.After(100 * time.Millisecond):
			}
			unlock()
			<-done
		})
	}
}

// Minor 2: "not placed" is refused while Trading 212 lists an order that could be it.
func TestT212ReReviewNotPlacedRefusedWhileListed(t *testing.T) {
	x := newT212X(t)
	x.sim.Script = []fakes.T212Outcome{fakes.T212LostApplied}
	o, _ := x.place(t, "req-1")
	if o.State != domain.OrderUnknown {
		t.Fatalf("buy %+v", o)
	}
	if _, err := x.svc.ResolveT212Order(x.ctx, x.owner, o.RequestID, 0, true); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("not placed while Trading 212 lists it: %v", err)
	}
	if got := x.order(t, o.RequestID); got.State != domain.OrderUnknown || audited(x.env, "t212.order_resolved") != 0 {
		t.Fatalf("buy %+v", got)
	}
}

// Minor 3: a fresh fill the kill cannot sell yet (Trading 212 does not list it) gets its
// stop, then the supervisor sells it once listed.
func TestT212ReReviewKillProtectsUnlistedFill(t *testing.T) {
	x := newT212X(t)
	advance := x.svc.d.Sleep
	x.sim.Script = []fakes.T212Outcome{fakes.T212Pending}
	x.svc.d.Sleep = func(context.Context, time.Duration) error { return context.Canceled }
	o, _ := x.place(t, "req-1")
	x.svc.d.Sleep = advance
	if o.State != domain.OrderPending || !x.sim.Fill(o.EtoroOrderID) {
		t.Fatalf("buy %+v", o)
	}
	x.svc.d.T212 = &lagT212{T212Broker: x.sim, lag: 2}
	if _, err := x.svc.Kill(x.ctx, x.owner, x.a.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if as := x.activeStops(t); len(as) != 1 || as[0].Quantity != 3*oneShare {
		t.Fatalf("no stop on the fill the kill could not sell: %+v (events %v)", as, x.events(t))
	}
	x.clock.Advance(SupervisorInterval)
	if _, err := x.svc.Supervise(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	if len(x.sim.Held) != 0 || len(x.activeStops(t)) != 0 {
		t.Fatalf("held %v stops %v", x.sim.Held, x.activeStops(t))
	}
	x.sound(t)
}
