package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// storedOrder puts an order of ours straight into the store, as a crash would have left it:
// created at `at`, and sent at `at` when sent is true.
func (x *execEnv) storedOrder(t *testing.T, o domain.Order, at time.Time, state domain.OrderState) domain.Order {
	t.Helper()
	o.AccountID, o.Mode = x.a.ID, domain.ModeDemo
	if o.RequestID == "" {
		o.RequestID = x.svc.d.Random.ID()
	}
	o, err := domain.NewOrder(o, at)
	if err != nil {
		t.Fatal(err)
	}
	if state != domain.OrderCreated {
		if o, err = o.Transition(domain.OrderSent, at, ""); err != nil {
			t.Fatal(err)
		}
		if state != domain.OrderSent {
			if o, err = o.Transition(state, at, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := x.store.Orders().Create(x.ctx, o); err != nil {
		t.Fatal(err)
	}
	return o
}

func openSpec(inst domain.InstrumentID) domain.Order {
	return domain.Order{Kind: domain.OrderOpen, Direction: domain.Long, Instrument: inst, Amount: 50_00, Entry: 100_000_000, StopLoss: 97_000_000}
}

func TestReconcileCleanAccount(t *testing.T) {
	x := newExecEnv(t)
	r := x.svc.Reconciled(x.a.ID)
	if !r.OK || r.Reason != "" || !r.At.Equal(x.clock.Now()) || r.AccountID != x.a.ID {
		t.Fatalf("reconciled = %+v", r)
	}
	if r := x.svc.Reconciled("nobody"); r.OK || r.Reason == "" {
		t.Fatalf("never reconciled = %+v", r)
	}
	// a conforming position cactusTrade did not open is fine and never touched (OD1)
	x.b.Positions = append(x.b.Positions, domain.Position{ID: 77, Instrument: instNYSE, OpenRate: 1, Amount: 10, RealAsset: true, Long: true, Leverage: 1})
	if r, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || !r.OK {
		t.Fatalf("with a foreign conforming position = %+v %v", r, err)
	}
	if x.b.Opens+x.b.Closes+x.b.Cancels != 0 {
		t.Fatal("reconciliation of a clean account changed it")
	}
}

func TestReconcileCrashBetweenPersistAndSend(t *testing.T) {
	x := newExecEnv(t)
	now := x.clock.Now()
	neverSent := x.storedOrder(t, openSpec(instNYSE), now.Add(-3*time.Minute), domain.OrderCreated)
	lostOld := x.storedOrder(t, openSpec(instLSE), now.Add(-3*time.Minute), domain.OrderUnknown)
	young := x.storedOrder(t, openSpec(instASX), now.Add(-30*time.Second), domain.OrderSent)
	// sent, and eToro has it: adopted
	sent := x.storedOrder(t, openSpec(instNYSE), now.Add(-10*time.Second), domain.OrderSent)
	in, err := domain.IntentFor(sent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.b.Open(ports.WithAccount(x.ctx, x.a), in); err != nil {
		t.Fatal(err)
	}

	r, err := x.svc.Reconcile(x.ctx, x.a.ID)
	if err != nil || !r.OK {
		t.Fatalf("reconcile = %+v %v", r, err)
	}
	for _, c := range []struct {
		o    domain.Order
		want domain.OrderState
	}{{neverSent, domain.OrderAbandoned}, {lostOld, domain.OrderAbandoned}, {young, domain.OrderSent}, {sent, domain.OrderFilled}} {
		if got := x.order(t, c.o.RequestID); got.State != c.want {
			t.Fatalf("order %s (%s) = %s, want %s", c.o.RequestID, c.o.State, got.State, c.want)
		}
	}
	aps := x.agentPositions(t)
	if len(aps) != 1 || aps[0].OpenRequestID != sent.RequestID {
		t.Fatalf("adopted positions = %+v", aps)
	}
	if x.b.Opens != 1 || x.b.Closes+x.b.Cancels != 0 {
		t.Fatalf("reconciliation sent something: opens=%d", x.b.Opens)
	}
	noViolations(t, x.b)
	// the young one is abandoned once it has been unknown to eToro for 2 minutes
	x.clock.Advance(lookupGiveUp)
	if _, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	if got := x.order(t, young.RequestID); got.State != domain.OrderAbandoned {
		t.Fatalf("young order later = %s", got.State)
	}
}

func TestReconcileForeignNonConforming(t *testing.T) {
	x := newExecEnv(t)
	x.b.Positions = append(x.b.Positions, domain.Position{ID: 66, Instrument: instNYSE, OpenRate: 1, Amount: 10, CFD: true, Long: true, Leverage: 2})
	r, err := x.svc.Reconcile(x.ctx, x.a.ID)
	if err != nil || r.OK || !strings.Contains(r.Reason, "66") {
		t.Fatalf("reconcile = %+v %v", r, err)
	}
	if _, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, k := range x.eventKinds(t) {
		if k == "reconcile.failed" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("reconcile.failed events = %d, want 1 (only on change)", n)
	}
	if x.b.Closes != 0 {
		t.Fatal("a position cactusTrade did not open was closed (OD1)")
	}
	// entries are refused while unreconciled
	if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); err == nil || x.b.Opens != 0 {
		t.Fatalf("entry while unreconciled: %v", err)
	}
	// the owner closes it on eToro: reconciled again, with an event
	x.b.Positions = nil
	if r, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || !r.OK {
		t.Fatalf("after the owner closed it = %+v %v", r, err)
	}
	if !slices.Contains(x.eventKinds(t), "reconcile.ok") {
		t.Fatal("no reconcile.ok event")
	}
}

func TestReconcileBrokerErrors(t *testing.T) {
	x := newExecEnv(t)
	x.b.PortfolioErr = fmt.Errorf("HTTP 503: %w", domain.ErrUnavailable)
	r, err := x.svc.Reconcile(x.ctx, x.a.ID)
	if !errors.Is(err, domain.ErrUnavailable) || r.OK || !strings.Contains(r.Reason, "portfolio") {
		t.Fatalf("portfolio error = %+v %v", r, err)
	}
	if x.svc.Reconciled(x.a.ID).OK {
		t.Fatal("flag still OK")
	}
	if !slices.Contains(x.eventKinds(t), "reconcile.failed") {
		t.Fatal("no warning event")
	}

	x.b.PortfolioErr = nil
	x.storedOrder(t, openSpec(instNYSE), x.clock.Now(), domain.OrderSent)
	h := &hookBroker{SimBroker: x.b}
	h.lookup = func(context.Context, string) (domain.OrderStatus, error) {
		return domain.OrderStatus{}, fmt.Errorf("timeout: %w", domain.ErrUnavailable)
	}
	x.svc.d.Broker = h
	if r, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || r.OK || !strings.Contains(r.Reason, "looking up") {
		t.Fatalf("lookup error = %+v %v", r, err)
	}

	// the second portfolio read (after adopting) fails too
	reads := 0
	h.lookup = nil
	h.pf = func(ctx context.Context) (domain.Portfolio, error) {
		if reads++; reads > 1 {
			return domain.Portfolio{}, domain.ErrUnavailable
		}
		return x.b.Portfolio(ctx)
	}
	if r, err := x.svc.Reconcile(x.ctx, x.a.ID); err == nil || r.OK {
		t.Fatalf("second read error = %+v %v", r, err)
	}

	x.svc.d.Broker = nil
	if r, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || r.OK {
		t.Fatalf("no broker = %+v %v", r, err)
	}
	if _, err := x.svc.Reconcile(x.ctx, "nobody"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	t212, err := x.svc.CreateAccountOn(x.ctx, x.admin, domain.PlatformTrading212, "T212", domain.ModeReal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.Reconcile(x.ctx, t212.ID); !errors.Is(err, domain.ErrViewOnly) {
		t.Fatalf("view-only: %v", err)
	}
}

func TestReconcilePositions(t *testing.T) {
	x := newExecEnv(t)
	gone := x.openPosition(t, instNYSE, domain.Long)
	kept := x.openPosition(t, instLSE, domain.Short)
	// ours gone at eToro (a stop hit): closed locally once it has had time to show
	x.b.Positions = slices.DeleteFunc(x.b.Positions, func(p domain.Position) bool { return p.ID == gone.PositionID })
	if _, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	if len(x.agentPositions(t)) != 2 {
		t.Fatal("a fresh fill missing from the portfolio was taken for closed (U11)")
	}
	x.clock.Advance(fillVisibleAfter)
	if r, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || !r.OK {
		t.Fatalf("reconcile = %+v %v", r, err)
	}
	aps := x.agentPositions(t)
	if len(aps) != 1 || aps[0].PositionID != kept.PositionID {
		t.Fatalf("open agent positions = %+v", aps)
	}
	// recorded as closed but still open at eToro: ours again
	k, _ := x.store.AgentPositions().Get(x.ctx, x.a.ID, kept.PositionID)
	k.ClosedAt = x.clock.Now()
	if err := x.store.AgentPositions().Update(x.ctx, k); err != nil {
		t.Fatal(err)
	}
	if _, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	if aps := x.agentPositions(t); len(aps) != 1 {
		t.Fatalf("not reopened: %+v", aps)
	}
	if x.b.Closes != 0 {
		t.Fatal("reconciliation closed a position")
	}
}

func TestReconcileCloseAndCancelOrders(t *testing.T) {
	x := newExecEnv(t)
	now := x.clock.Now()

	// a close with an eToro id that has since filled
	a1 := x.openPosition(t, instNYSE, domain.Long)
	x.b.CloseScript = []fakes.SimOutcome{fakes.SimPending}
	c1, err := x.svc.closePosition(x.ctx, x.a, a1, "flatten", nil)
	if err != nil || c1.EtoroOrderID == 0 {
		t.Fatalf("close = %+v %v", c1, err)
	}
	// a close that never got an id, whose position is gone
	a2 := x.openPosition(t, instLSE, domain.Long)
	c2 := x.storedOrder(t, domain.Order{Kind: domain.OrderClose, Direction: domain.Long, Instrument: instLSE, PositionID: a2.PositionID}, now, domain.OrderUnknown)
	// a stale close whose position is still open: abandoned (the supervisor closes again)
	a3 := x.openPosition(t, instASX, domain.Long)
	c3 := x.storedOrder(t, domain.Order{Kind: domain.OrderClose, Direction: domain.Long, Instrument: instASX, PositionID: a3.PositionID}, now.Add(-3*time.Minute), domain.OrderSent)
	// cancels: of an order that is final, and a stale one of an order still pending
	cancelDone := x.storedOrder(t, domain.Order{Kind: domain.OrderCancel, TargetOrderID: 424242, Instrument: instNYSE}, now, domain.OrderSent)
	target := x.storedOrder(t, openSpec(instNYSE), now, domain.OrderPending)
	tgt := target
	tgt.EtoroOrderID = 515151
	if err := x.store.Orders().Update(x.ctx, target, tgt); err != nil {
		t.Fatal(err)
	}
	cancelStale := x.storedOrder(t, domain.Order{Kind: domain.OrderCancel, TargetOrderID: 515151, Instrument: instNYSE}, now.Add(-3*time.Minute), domain.OrderSent)

	h := &hookBroker{SimBroker: x.b}
	h.co = func(_ context.Context, id int64) (domain.OrderStatus, error) {
		if id != c1.EtoroOrderID {
			return domain.OrderStatus{}, domain.ErrNotFound
		}
		return domain.OrderStatus{OrderID: id, State: domain.OrderFilled, Final: true}, nil
	}
	h.lookup = func(context.Context, string) (domain.OrderStatus, error) {
		return domain.OrderStatus{State: domain.OrderPending, OrderID: 515151}, nil
	}
	x.svc.d.Broker = h
	x.b.Positions = slices.DeleteFunc(x.b.Positions, func(p domain.Position) bool {
		return p.ID == a1.PositionID || p.ID == a2.PositionID
	})
	// Old enough for a missing position to prove its close (U11).
	x.clock.Advance(fillVisibleAfter)
	if _, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		o    domain.Order
		want domain.OrderState
	}{
		{c1, domain.OrderFilled},
		{c2, domain.OrderFilled},
		{c3, domain.OrderAbandoned},
		{cancelDone, domain.OrderFilled},
		{cancelStale, domain.OrderAbandoned},
		{target, domain.OrderPending},
	} {
		if got := x.order(t, c.o.RequestID); got.State != c.want {
			t.Fatalf("%s order %s = %s (%s), want %s", c.o.Kind, c.o.RequestID, got.State, got.Reason, c.want)
		}
	}
	aps := x.agentPositions(t)
	if len(aps) != 1 || aps[0].PositionID != a3.PositionID {
		t.Fatalf("open agent positions = %+v", aps)
	}
	// a close order whose read fails keeps the account unreconciled
	x.b.CloseScript = []fakes.SimOutcome{fakes.SimPending}
	if _, err := x.svc.closePosition(x.ctx, x.a, aps[0], "flatten", nil); err != nil {
		t.Fatal(err)
	}
	h.co = func(context.Context, int64) (domain.OrderStatus, error) {
		return domain.OrderStatus{}, domain.ErrUnavailable
	}
	if r, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || r.OK {
		t.Fatalf("close read failing = %+v %v", r, err)
	}
}

func TestReconcileAll(t *testing.T) {
	x := newExecEnv(t)
	noKeys, err := x.svc.CreateAccount(x.ctx, x.admin, "No keys", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	real := x.account(t, "Real", domain.ModeReal)
	if _, err := x.svc.CreateAccountOn(x.ctx, x.admin, domain.PlatformTrading212, "T212", domain.ModeReal); err != nil {
		t.Fatal(err)
	}
	fresh := x.account(t, "Fresh demo", domain.ModeDemo)

	// an entry before the start-up reconciliation never reaches eToro
	if _, err := x.svc.placeEntry(x.ctx, fresh, x.entry(domain.Long), domain.DefaultRiskLimits); err == nil || x.b.Opens != 0 {
		t.Fatalf("entry before ReconcileAll: %v opens=%d", err, x.b.Opens)
	}
	rs, err := x.svc.ReconcileAll(x.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.AccountID)
		if !r.OK {
			t.Fatalf("not reconciled: %+v", r)
		}
	}
	slices.Sort(ids)
	want := []string{x.a.ID, real.ID, fresh.ID}
	slices.Sort(want)
	if !slices.Equal(ids, want) || slices.Contains(ids, noKeys.ID) {
		t.Fatalf("reconciled %v, want %v", ids, want)
	}
	// the real account was only read
	if x.b.Opens+x.b.Closes+x.b.Cancels != 0 {
		t.Fatal("ReconcileAll changed an account")
	}
	if _, err := x.svc.placeEntry(x.ctx, fresh, x.entry(domain.Long), domain.DefaultRiskLimits); err != nil || x.b.Opens != 1 {
		t.Fatalf("entry after ReconcileAll: %v", err)
	}
	// a failing account is reported, not fatal
	x.b.PortfolioErr = domain.ErrUnavailable
	rs, err = x.svc.ReconcileAll(x.ctx)
	if err != nil || len(rs) != 3 || rs[0].OK {
		t.Fatalf("failing = %+v %v", rs, err)
	}
	x.sec.Err = errors.New("vault sealed")
	if rs, err := x.svc.ReconcileAll(x.ctx); err != nil || len(rs) != 0 {
		t.Fatalf("without the key status = %+v %v", rs, err)
	}
}
