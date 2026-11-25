package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// What eToro's demo did with cactusTrade's first live orders on 2026-11-24 (PLAN "Live
// findings 2026-11-24"): both opens filled within 0.4 s; orders:lookup by our request id
// (referenceId) answered 404 before and after the fill, by eToro's order id it worked; the
// fill's investedAmountCurrency was 1 for $100; the take-profit closed the position before we
// looked. Our rows stayed pending, the supervisor sent a cancel every minute (151 accepted
// cancels in 90 minutes), and then every pass failed with "pending → abandoned: illegal order
// transition".

const (
	liveOrderID    = 396359836
	livePositionID = 3649965576
)

// liveGOOG is the order cactusTrade stored for the GOOG buy (instNYSE stands in for GOOG).
func liveGOOG() domain.Order {
	return domain.Order{
		Kind: domain.OrderOpen, Direction: domain.Long, Instrument: instNYSE, Amount: 100_00,
		Entry: 276_000_000, StopLoss: 275_172_243, TakeProfit: 276_676_000, EtoroOrderID: liveOrderID,
	}
}

// liveFilled is the recorded lookup (internal/adapters/etoro/testdata/recorded/
// lookup_filled_by_order_id.json) as the eToro adapter reads it.
func liveFilled(requestID string) domain.OrderStatus {
	return domain.OrderStatus{
		RequestID: requestID, OrderID: liveOrderID, State: domain.OrderFilled, Final: true, Instrument: instNYSE,
		Settlement: domain.SettlementReal, Leverage: 1, Side: domain.Long, Reason: "Filled",
		Executions: []domain.PositionExecution{{
			PositionID: livePositionID, Units: 362_279, Amount: 99_99, OpenRate: 275_990_000, StopLossRate: 275_180_000, Closed: true,
		}},
	}
}

// liveBroker answers lookups like eToro's demo did: 404 by request id, found by order id.
func liveBroker(x *execEnv, byOrderID func(id string) (domain.OrderStatus, error)) *hookBroker {
	h := &hookBroker{SimBroker: x.b}
	h.lookup = func(_ context.Context, id string, orderID int64) (domain.OrderStatus, error) {
		if orderID == liveOrderID && byOrderID != nil {
			return byOrderID(id)
		}
		return domain.OrderStatus{RequestID: id, State: domain.OrderUnknown}, fmt.Errorf("lookup %s: %w", id, domain.ErrNotFound)
	}
	x.svc.d.Broker = h
	return h
}

func (x *execEnv) countEvents(t *testing.T, kind string) int {
	t.Helper()
	n := 0
	for _, k := range x.eventKinds(t) {
		if k == kind {
			n++
		}
	}
	return n
}

func illegal(errs []string) bool {
	return slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, "illegal") })
}

// TestLiveStuckOrderResolvesByOrderID: the stuck GOOG row (pending, 90 minutes old, many
// accepted cancels) resolves on the next supervisor pass: the lookup by eToro's order id says
// filled with a closed execution → the position is recorded as ours and closed → the order is
// filled. No cancel and no close are sent.
func TestLiveStuckOrderResolvesByOrderID(t *testing.T) {
	x := supEnv(t, time.Date(2026, 11, 24, 20, 20, 0, 0, time.UTC))
	sent := x.clock.Now().Add(-90 * time.Minute)
	o := x.storedOrder(t, liveGOOG(), sent, domain.OrderPending)
	for i := range 3 {
		x.storedOrder(t, domain.Order{Kind: domain.OrderCancel, Direction: domain.Long, Instrument: instNYSE, TargetOrderID: liveOrderID},
			sent.Add(time.Duration(i+1)*time.Minute), domain.OrderFilled)
	}
	liveBroker(x, func(id string) (domain.OrderStatus, error) { return liveFilled(id), nil })

	for pass := range 3 {
		rep := x.supervise(t)
		if len(rep.Errors) > 0 || rep.Cancelled != 0 {
			t.Fatalf("pass %d: %+v", pass, rep)
		}
		x.clock.Advance(SupervisorInterval)
	}
	if got := x.order(t, o.RequestID); got.State != domain.OrderFilled || got.PositionID != livePositionID {
		t.Fatalf("order = %s position %d, want filled with position %d", got.State, got.PositionID, livePositionID)
	}
	ap, err := x.store.AgentPositions().Get(x.ctx, x.a.ID, livePositionID)
	if err != nil {
		t.Fatalf("the position is not recorded as ours (OD1 would leave it alone): %v", err)
	}
	if ap.Open() || ap.OpenRequestID != o.RequestID {
		t.Fatalf("agent position = %+v, want ours and closed (eToro's take-profit closed it)", ap)
	}
	if x.b.Cancels != 0 || x.b.Closes != 0 {
		t.Fatalf("cancels %d closes %d, want none", x.b.Cancels, x.b.Closes)
	}
	if x.countEvents(t, "position.closed") != 1 || x.countEvents(t, "order.filled") != 1 {
		t.Fatalf("events = %v", x.eventKinds(t))
	}
	if r, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || !r.OK {
		t.Fatalf("reconcile = %+v %v", r, err)
	}
}

// TestLiveUnresolvablePendingOrderHolds: an accepted (pending) order whose lookups keep
// answering 404 stays pending and reserved (review-c): no illegal transition on every pass,
// exactly one warning, at most maxCancels cancels (eToro refuses them: it does not know the
// order either), and the account stays reconciled (an accepted order
// in flight, D24).
func TestLiveUnresolvablePendingOrderHolds(t *testing.T) {
	x := supEnv(t, time.Date(2026, 11, 24, 19, 0, 0, 0, time.UTC))
	o := x.storedOrder(t, liveGOOG(), x.clock.Now().Add(-3*time.Minute), domain.OrderPending)
	liveBroker(x, func(id string) (domain.OrderStatus, error) {
		return domain.OrderStatus{}, fmt.Errorf("lookup %s: %w", id, domain.ErrNotFound)
	})
	for pass := range 60 { // 10 minutes of passes, with a reconciliation every 5
		if rep := x.supervise(t); illegal(rep.Errors) {
			t.Fatalf("pass %d: %v", pass, rep.Errors)
		}
		x.clock.Advance(SupervisorInterval)
	}
	if got := x.order(t, o.RequestID); got.State != domain.OrderPending {
		t.Fatalf("order = %s, want pending (held)", got.State)
	}
	if n := x.countEvents(t, "order.unresolved"); n != 1 {
		t.Fatalf("%d order.unresolved events, want exactly 1: %v", n, x.eventKinds(t))
	}
	if x.b.Cancels > maxCancels {
		t.Fatalf("%d cancels, want at most %d", x.b.Cancels, maxCancels)
	}
	if r, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || !r.OK {
		t.Fatalf("reconcile = %+v %v", r, err)
	}
	if r := domain.Reservations(x.openOrders(t)); r.Amount != o.Amount || !slices.Contains(r.Instruments, instNYSE) {
		t.Fatalf("the held order no longer reserves its cash: %+v", r)
	}
}

// TestLiveOneCancelPerOrder: an order pending for 10 minutes whose cancel eToro accepts but
// does not act on gets exactly one cancel (it got one a minute before), and one warning; also
// while the kill switch is on. A cancel eToro refuses may be retried, at most maxCancels in
// all, a minute apart.
func TestLiveOneCancelPerOrder(t *testing.T) {
	tests := []struct {
		name    string
		kill    bool
		refuse  bool
		cancels int
		warning string
	}{
		{"accepted, never confirmed", false, false, 1, "order.cancel_unconfirmed"},
		{"accepted, never confirmed, killed", true, false, 1, "order.cancel_unconfirmed"},
		{"refused every time", false, true, maxCancels, "order.cancel_refused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := supEnv(t, time.Date(2026, 11, 24, 19, 0, 0, 0, time.UTC))
			x.b.OpenScript, x.b.IgnoreCancels, x.b.RefLookupNotFound = []fakes.SimOutcome{fakes.SimPending}, true, true
			if tt.refuse {
				h := &hookBroker{SimBroker: x.b}
				h.cancel = func(context.Context, int64, string) error {
					x.b.Cancels++
					return &domain.NotAcceptedError{Reason: "refused (test)"}
				}
				x.svc.d.Broker = h
			}
			start := x.clock.Now()
			o, _ := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
			if tt.kill {
				if _, err := x.svc.Kill(x.ctx, x.admin, x.a.ID, "test"); err != nil {
					t.Fatal(err)
				}
			}
			for x.clock.Now().Sub(start) < 10*time.Minute {
				if rep := x.supervise(t); illegal(rep.Errors) {
					t.Fatalf("%v", rep.Errors)
				}
				x.clock.Advance(SupervisorInterval)
			}
			if x.b.Cancels != tt.cancels {
				t.Fatalf("%d cancels in 10 minutes, want %d", x.b.Cancels, tt.cancels)
			}
			if n := x.countEvents(t, tt.warning); n != 1 {
				t.Fatalf("%d %s events, want 1: %v", n, tt.warning, x.eventKinds(t))
			}
			if got := x.order(t, o.RequestID); got.State.Final() {
				t.Fatalf("order = %s: eToro never confirmed anything", got.State)
			}
		})
	}
}

// TestExecutorLooksUpByOrderID: with eToro answering 404 to every lookup by request id, an
// accepted open is followed by its eToro order id and fills normally.
func TestExecutorLooksUpByOrderID(t *testing.T) {
	x := newExecEnv(t)
	x.b.RefLookupNotFound = true
	o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
	if err != nil || o.State != domain.OrderFilled || o.EtoroOrderID == 0 || len(x.agentPositions(t)) != 1 {
		t.Fatalf("= %+v %v positions %v", o, err, x.agentPositions(t))
	}
	if x.b.RefNotFound != 0 {
		t.Fatalf("%d lookups by request id although eToro's order id was known", x.b.RefNotFound)
	}
	// A lost answer has no eToro order id: abandoned after 2 min of 404s, then adopted by
	// reconciliation once eToro's portfolio shows the position (review B2).
	x.b.OpenScript = []fakes.SimOutcome{fakes.SimLostAnswer}
	lost, _ := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
	if lost.State != domain.OrderAbandoned {
		t.Fatalf("lost answer = %s", lost.State)
	}
	if r, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || !r.OK || len(x.agentPositions(t)) != 2 {
		t.Fatalf("reconcile = %+v %v positions %v", r, err, x.agentPositions(t))
	}
}

// TestExecutorClosedExecutions: a fill whose position eToro already closed is recorded as ours
// and settled as closed, never closed again; a non-conforming one still blocks the instrument
// and raises the alert.
func TestExecutorClosedExecutions(t *testing.T) {
	tests := []struct {
		name        string
		edit        func(*domain.OrderStatus)
		openAfter   int
		blocked     bool
		closedCount int
	}{
		{"closed by its take-profit", nil, 0, false, 1},
		{"one closed, one open", func(st *domain.OrderStatus) {
			e := st.Executions[0]
			e.PositionID, e.Closed = e.PositionID+1, false
			st.Executions = append(st.Executions, e)
		}, 1, false, 1},
		{"closed but leverage 2", func(st *domain.OrderStatus) { st.Leverage = 2 }, 0, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newExecEnv(t)
			h := &hookBroker{SimBroker: x.b}
			h.lookup = func(ctx context.Context, id string, orderID int64) (domain.OrderStatus, error) {
				st, err := x.b.Lookup(ctx, id, orderID)
				if err != nil {
					return st, err
				}
				for i := range st.Executions {
					st.Executions[i].Closed = true
				}
				if tt.edit != nil {
					tt.edit(&st)
				}
				return st, nil
			}
			x.svc.d.Broker = h
			o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
			if err != nil || o.State != domain.OrderFilled {
				t.Fatalf("= %+v %v", o, err)
			}
			if n := len(x.agentPositions(t)); n != tt.openAfter {
				t.Fatalf("%d open agent positions, want %d", n, tt.openAfter)
			}
			if x.b.Closes != 0 {
				t.Fatalf("%d closes sent for positions eToro already closed", x.b.Closes)
			}
			if got := slices.Contains(x.blocked(t), instNYSE); got != tt.blocked {
				t.Fatalf("blocked = %v", got)
			}
			if tt.blocked && x.countEvents(t, "order.nonconforming") != 1 {
				t.Fatalf("events = %v", x.eventKinds(t))
			}
			if n := x.countEvents(t, "position.closed"); n != tt.closedCount {
				t.Fatalf("%d position.closed events, want %d", n, tt.closedCount)
			}
		})
	}
}

// TestOrderLabels: the server words an order's kind and state for people (no wording in
// React): a cancel or close eToro took is "accepted by eToro" or "done", never "filled".
func TestOrderLabels(t *testing.T) {
	tests := []struct {
		name         string
		o            domain.Order
		symbol       string
		kind, status string
	}{
		{"open long filled", domain.Order{Kind: domain.OrderOpen, Direction: domain.Long, State: domain.OrderFilled}, "GOOG", "Buy GOOG", "filled"},
		{"open short partly", domain.Order{Kind: domain.OrderOpen, Direction: domain.Short, State: domain.OrderPartial}, "AAPL", "Short AAPL", "partially filled"},
		{"open, no symbol", domain.Order{Kind: domain.OrderOpen, Direction: domain.Long, Instrument: 1002, State: domain.OrderPending}, "", "Buy instrument 1002", "pending"},
		{
			"cancel accepted",
			domain.Order{Kind: domain.OrderCancel, Direction: domain.Long, TargetOrderID: liveOrderID, State: domain.OrderFilled},
			"GOOG",
			"Cancel request for GOOG (buy order 396359836)", "accepted by eToro",
		},
		{
			"cancel of a short, refused",
			domain.Order{Kind: domain.OrderCancel, Direction: domain.Short, TargetOrderID: 7, State: domain.OrderRejected},
			"AAPL",
			"Cancel request for AAPL (short order 7)", "refused",
		},
		{
			"close done",
			domain.Order{Kind: domain.OrderClose, Direction: domain.Long, PositionID: livePositionID, State: domain.OrderFilled},
			"GOOG",
			"Close request for GOOG (position 3649965576)", "done",
		},
		{
			"close partly done",
			domain.Order{Kind: domain.OrderClose, Direction: domain.Short, PositionID: 5, State: domain.OrderPartial},
			"AAPL",
			"Close request for AAPL (position 5)", "partly done",
		},
		{"close pending", domain.Order{Kind: domain.OrderClose, PositionID: 5, State: domain.OrderPending}, "AAPL", "Close request for AAPL (position 5)", "pending"},
		{"open rejected", domain.Order{Kind: domain.OrderOpen, Direction: domain.Long, State: domain.OrderRejected}, "GOOG", "Buy GOOG", "rejected"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := OrderView{Order: tt.o, Symbol: tt.symbol}
			if got := v.KindLabel(); got != tt.kind {
				t.Errorf("KindLabel = %q, want %q", got, tt.kind)
			}
			if got := v.StateLabel(); got != tt.status {
				t.Errorf("StateLabel = %q, want %q", got, tt.status)
			}
		})
	}
}

// TestExecutorHoldsAnAcceptedOrderItCannotFind: an open eToro accepted (pending, with its
// order id) whose lookups keep answering 404 is never abandoned by the executor either
// (review-c): it is left pending and reserved for the supervisor, without an error.
func TestExecutorHoldsAnAcceptedOrderItCannotFind(t *testing.T) {
	x := newExecEnv(t)
	x.b.LookupLag = time.Hour
	o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
	if err != nil || o.State != domain.OrderPending || o.EtoroOrderID == 0 {
		t.Fatalf("= %s %v", o.State, err)
	}
	if r := domain.Reservations(x.openOrders(t)); r.Amount != o.Amount {
		t.Fatalf("reservation = %+v", r)
	}
}

// TestOrderReasonLabel: a cancel's reason says what was asked, why, and that "accepted" is not
// "cancelled" (the owner, 2026-11-25: "cancel accepted: pending for over 1m0s" was unclear).
func TestOrderReasonLabel(t *testing.T) {
	cancel := func(state domain.OrderState, reason string) OrderView {
		return OrderView{Order: domain.Order{Kind: domain.OrderCancel, Direction: domain.Long, TargetOrderID: liveOrderID, State: state, Reason: reason}, Symbol: "GOOG"}
	}
	tests := []struct {
		name string
		v    OrderView
		want string
	}{
		{
			"accepted, stored before the rewording", cancel(domain.OrderFilled, "cancel accepted: pending for over 1m0s"),
			"eToro accepted the request to cancel order 396359836. Why it was asked: the order was still not filled after 1 minute " +
				"(cactusTrade cancels an entry order eToro has not filled by then, so its cash is not held up). " +
				"This does not mean it was cancelled: if it had already filled, it stays filled (see that order's own line).",
		},
		{
			"accepted, the kill switch", cancel(domain.OrderFilled, "cancel accepted: the kill switch is on"),
			"eToro accepted the request to cancel order 396359836. Why it was asked: the kill switch is on. " +
				"This does not mean it was cancelled: if it had already filled, it stays filled (see that order's own line).",
		},
		{"refused", cancel(domain.OrderRejected, "rate limited 3 times"), "eToro refused the request to cancel order 396359836: rate limited 3 times"},
		{"refused, no reason", cancel(domain.OrderRejected, ""), "eToro refused the request to cancel order 396359836."},
		{"sent", cancel(domain.OrderSent, "pending for over 90s"), "Asked to cancel order 396359836 because the order was still not filled after 90 seconds " +
			"(cactusTrade cancels an entry order eToro has not filled by then, so its cash is not held up)."},
		{"sent, no reason", cancel(domain.OrderSent, ""), ""},
		{"no target id", OrderView{Order: domain.Order{Kind: domain.OrderCancel, State: domain.OrderFilled}}, "eToro accepted the request to cancel the order. " +
			"This does not mean it was cancelled: if it had already filled, it stays filled (see that order's own line)."},
		{"open: as stored", OrderView{Order: domain.Order{Kind: domain.OrderOpen, Reason: "stale prices"}}, "stale prices"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.v.ReasonLabel(); got != tt.want {
				t.Errorf("ReasonLabel =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
	for d, want := range map[time.Duration]string{time.Minute: "1 minute", 2 * time.Minute: "2 minutes", 90 * time.Second: "90 seconds", 1500 * time.Millisecond: "1.5s"} {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%s) = %q, want %q", d, got, want)
		}
	}
}
