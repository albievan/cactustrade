package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// Instruments of the execution tests, one per market.
const (
	instNYSE domain.InstrumentID = 101
	instLSE  domain.InstrumentID = 102
	instASX  domain.InstrumentID = 103
)

// execEnv is a service with a simulated eToro demo account (SimBroker), a fake sleeper that
// only moves the fake clock, and a reconciled demo account with keys.
type execEnv struct {
	*env
	ctx context.Context
	b   *fakes.SimBroker
	a   domain.Account
	// slept is every wait the service asked for, in order.
	slept []time.Duration
}

func newExecEnv(t *testing.T) *execEnv {
	t.Helper()
	ctx := context.Background()
	e := newEnv(t)
	x := &execEnv{env: e, ctx: ctx}
	x.b = &fakes.SimBroker{Now: e.clock.Now, Cash: 1_000_000_00}
	x.b.OnOpen = fakes.RequireOrderRow(e.store.Orders())
	e.svc.d.Broker = x.b
	e.svc.d.Sleep = func(ctx context.Context, d time.Duration) error {
		if d > 0 {
			x.slept = append(x.slept, d)
			e.clock.Advance(d)
		}
		return ctx.Err()
	}
	x.a = x.account(t, "Demo", domain.ModeDemo)
	for id, sym := range map[domain.InstrumentID]string{instNYSE: "AAPL", instLSE: "BARC.L", instASX: "CBA.ASX"} {
		m, _ := domain.MarketForSymbol(sym)
		in := domain.Instrument{ID: id, Symbol: sym, Type: "Stocks", Market: m}
		if err := e.store.Instruments().Upsert(ctx, in, domain.Eligibility{Instrument: id, RealLong: true, ShortCFD: true}, e.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if r, err := e.svc.Reconcile(ctx, x.a.ID); err != nil || !r.OK {
		t.Fatalf("reconcile = %+v %v", r, err)
	}
	return x
}

// account creates an eToro account of the admin with keys.
func (x *execEnv) account(t *testing.T, name string, mode domain.TradingMode) domain.Account {
	t.Helper()
	v, err := x.svc.CreateAccount(x.ctx, x.admin, name, mode)
	if err != nil {
		t.Fatal(err)
	}
	ak, uk := domain.AccountKeyNames(v.Account)
	for _, n := range []domain.CredentialName{ak, uk} {
		if err := x.sec.Put(x.ctx, n, "test-key-"+string(n)); err != nil {
			t.Fatal(err)
		}
	}
	return v.Account
}

func (x *execEnv) entry(d domain.Direction) entryRequest {
	r := entryRequest{Direction: d, Instrument: instNYSE, Amount: 100_00, Entry: 100_000_000, StopLoss: 97_000_000, TakeProfit: 106_000_000, PortfolioAt: x.clock.Now()}
	if d == domain.Short {
		r.StopLoss, r.TakeProfit = 103_000_000, 94_000_000
	}
	return r
}

func (x *execEnv) order(t *testing.T, id string) domain.Order {
	t.Helper()
	o, err := x.store.Orders().Get(x.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func (x *execEnv) openOrders(t *testing.T) []domain.Order {
	t.Helper()
	os, err := x.store.Orders().ListOpen(x.ctx, x.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return os
}

func (x *execEnv) agentPositions(t *testing.T) []domain.AgentPosition {
	t.Helper()
	ps, err := x.store.AgentPositions().ListOpen(x.ctx, x.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

func (x *execEnv) eventKinds(t *testing.T) []string {
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

func (x *execEnv) blocked(t *testing.T) []domain.InstrumentID {
	t.Helper()
	bs, err := x.store.InstrumentBlocks().List(x.ctx, x.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.InstrumentID
	for _, b := range bs {
		out = append(out, b.Instrument)
	}
	return out
}

func noViolations(t *testing.T, b *fakes.SimBroker) {
	t.Helper()
	if len(b.Violations) > 0 {
		t.Fatalf("broker violations: %v", b.Violations)
	}
}

// sameID asserts every intent sent carries one request id.
func sameID(t *testing.T, ins []domain.OrderIntent, want string) {
	t.Helper()
	for _, in := range ins {
		if in.RequestID != want {
			t.Fatalf("intent ids = %+v, want all %s", ins, want)
		}
	}
}

func TestExecutorPlaceEntry(t *testing.T) {
	tests := []struct {
		name      string
		dir       domain.Direction
		script    []fakes.SimOutcome
		pending   int
		wantState domain.OrderState
		wantErr   error
		opens     int
		cancels   int
		closes    int
		positions int  // agent positions left open
		blocked   bool // instrument blocked
		minWait   time.Duration
		event     string
	}{
		{name: "long filled", dir: domain.Long, script: []fakes.SimOutcome{fakes.SimFill}, wantState: domain.OrderFilled, opens: 1, positions: 1, event: "order.filled"},
		{name: "short filled", dir: domain.Short, script: []fakes.SimOutcome{fakes.SimFill}, wantState: domain.OrderFilled, opens: 1, positions: 1, event: "order.filled"},
		{name: "pending then filled", dir: domain.Long, script: []fakes.SimOutcome{fakes.SimPendingThenFill}, pending: 3, wantState: domain.OrderFilled, opens: 1, positions: 1, event: "order.sent"},
		// review M3: an open is never resent, not even after a 429
		{name: "429 not processed: one open, abandoned after 2 min", dir: domain.Long, script: []fakes.SimOutcome{fakes.SimRateLimited, fakes.SimFill}, wantState: domain.OrderAbandoned, opens: 1, minWait: lookupGiveUp, event: "order.abandoned"},
		{name: "429 processed: one open, adopted by lookup", dir: domain.Long, script: []fakes.SimOutcome{fakes.SimRateLimitedProcessed}, wantState: domain.OrderFilled, opens: 1, positions: 1, minWait: fakes.SimRetryAfter, event: "order.filled"},
		{name: "lost answer: one open, resolved by lookup", dir: domain.Long, script: []fakes.SimOutcome{fakes.SimLostAnswer}, wantState: domain.OrderFilled, opens: 1, positions: 1, event: "order.unknown"},
		{name: "5xx and 404 for 2 min: abandoned, never resent", dir: domain.Long, script: []fakes.SimOutcome{fakes.SimServerError}, wantState: domain.OrderAbandoned, opens: 1, minWait: lookupGiveUp, event: "order.abandoned"},
		{name: "rejected: reservation freed", dir: domain.Long, script: []fakes.SimOutcome{fakes.SimReject}, wantState: domain.OrderRejected, opens: 1, event: "order.rejected"},
		{name: "refused (4xx)", dir: domain.Short, script: []fakes.SimOutcome{fakes.SimRefused}, wantState: domain.OrderRejected, wantErr: domain.ErrNotAccepted, opens: 1, event: "order.rejected"},
		{name: "pending for 60 s: cancelled", dir: domain.Long, script: []fakes.SimOutcome{fakes.SimPending}, wantState: domain.OrderCancelled, opens: 1, cancels: 1, minWait: pendingMax, event: "order.cancelled"},
		{name: "partial fill: rest cancelled, position kept", dir: domain.Long, script: []fakes.SimOutcome{fakes.SimPartial}, wantState: domain.OrderCancelled, opens: 1, cancels: 1, positions: 1, minWait: pendingMax},
		{name: "leverage 2: closed and blocked", dir: domain.Long, script: []fakes.SimOutcome{fakes.SimLeverage2}, wantState: domain.OrderFilled, opens: 1, closes: 1, blocked: true, event: "order.nonconforming"},
		{name: "CFD long: closed and blocked", dir: domain.Long, script: []fakes.SimOutcome{fakes.SimWrongSettlement}, wantState: domain.OrderFilled, opens: 1, closes: 1, blocked: true, event: "order.nonconforming"},
		{name: "real short: closed and blocked", dir: domain.Short, script: []fakes.SimOutcome{fakes.SimWrongSettlement}, wantState: domain.OrderFilled, opens: 1, closes: 1, blocked: true, event: "position.closed"},
		{name: "no stop: closed and blocked", dir: domain.Short, script: []fakes.SimOutcome{fakes.SimNoStop}, wantState: domain.OrderFilled, opens: 1, closes: 1, blocked: true, event: "order.nonconforming"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newExecEnv(t)
			x.b.OpenScript, x.b.PendingLookups = tt.script, tt.pending
			start := x.clock.Now()
			o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(tt.dir), domain.DefaultRiskLimits)
			if (tt.wantErr == nil && err != nil) || (tt.wantErr != nil && !errors.Is(err, tt.wantErr)) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			stored := x.order(t, o.RequestID)
			if stored.State != tt.wantState || o.State != tt.wantState {
				t.Fatalf("state = %s (returned %s), want %s; reason %q", stored.State, o.State, tt.wantState, stored.Reason)
			}
			if x.b.Opens != tt.opens || x.b.Cancels != tt.cancels || x.b.Closes != tt.closes {
				t.Fatalf("opens/cancels/closes = %d/%d/%d, want %d/%d/%d", x.b.Opens, x.b.Cancels, x.b.Closes, tt.opens, tt.cancels, tt.closes)
			}
			sameID(t, x.b.Intents, o.RequestID)
			if stored.Sends != tt.opens {
				t.Fatalf("sends = %d, want %d", stored.Sends, tt.opens)
			}
			noViolations(t, x.b)
			if n := len(x.agentPositions(t)); n != tt.positions {
				t.Fatalf("open agent positions = %d, want %d", n, tt.positions)
			}
			if got := slices.Contains(x.blocked(t), instNYSE); got != tt.blocked {
				t.Fatalf("blocked = %v, want %v", got, tt.blocked)
			}
			if waited := x.clock.Now().Sub(start); waited < tt.minWait {
				t.Fatalf("waited %s, want at least %s", waited, tt.minWait)
			}
			if tt.event != "" && !slices.Contains(x.eventKinds(t), tt.event) {
				t.Fatalf("events = %v, want %s", x.eventKinds(t), tt.event)
			}
			// a final open order holds nothing back
			if r := domain.Reservations(x.openOrders(t)); stored.State.Final() && (r.Amount != 0 || r.Count != 0 || r.Unknown != 0) {
				t.Fatalf("reservation left = %+v", r)
			}
			for _, id := range x.b.CancelRequests {
				if c := x.order(t, id); c.Kind != domain.OrderCancel || c.TargetOrderID != stored.EtoroOrderID {
					t.Fatalf("cancel row = %+v", c)
				}
			}
			for _, id := range x.b.CloseRequests {
				if c := x.order(t, id); c.Kind != domain.OrderClose || c.State != domain.OrderFilled {
					t.Fatalf("close row = %+v", c)
				}
			}
		})
	}
}

func TestExecutorPollSchedule(t *testing.T) {
	x := newExecEnv(t)
	x.b.OpenScript, x.b.PendingLookups = []fakes.SimOutcome{fakes.SimPendingThenFill}, 6
	if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); err != nil {
		t.Fatal(err)
	}
	// lookups at 1, 2, 4, 8, 15, 30 s after the send: waits of 1, 1, 2, 4, 7, 15 s, then 30 s
	want := []time.Duration{1, 1, 2, 4, 7, 15, 30}
	if len(x.slept) != len(want) {
		t.Fatalf("waits = %v", x.slept)
	}
	for i, w := range want {
		if x.slept[i] != w*time.Second {
			t.Fatalf("waits = %v, want %v s", x.slept, want)
		}
	}
	if x.b.Lookups != 7 {
		t.Fatalf("lookups = %d", x.b.Lookups)
	}
}

func TestExecutorRefusals(t *testing.T) {
	t.Run("real account: zero broker calls", func(t *testing.T) {
		x := newExecEnv(t)
		real := x.account(t, "Real", domain.ModeReal)
		_, err := x.svc.placeEntry(x.ctx, real, x.entry(domain.Long), domain.DefaultRiskLimits)
		if !errors.Is(err, domain.ErrRealModeGated) {
			t.Fatalf("err = %v", err)
		}
		ap := domain.AgentPosition{AccountID: real.ID, PositionID: 5, Instrument: instNYSE, Direction: domain.Long}
		if _, err := x.svc.closePosition(x.ctx, real, ap, "test", closePolls); !errors.Is(err, domain.ErrRealModeGated) {
			t.Fatalf("close err = %v", err)
		}
		target := domain.Order{RequestID: "t", Kind: domain.OrderOpen, EtoroOrderID: 9, State: domain.OrderPending}
		if _, err := x.svc.cancelOrder(x.ctx, real, target, "test"); !errors.Is(err, domain.ErrRealModeGated) {
			t.Fatalf("cancel err = %v", err)
		}
		if x.b.Opens+x.b.Closes+x.b.Cancels+x.b.Lookups != 0 {
			t.Fatal("broker called for a real account")
		}
		if os, _ := x.store.Orders().List(x.ctx, real.ID, 10); len(os) != 0 {
			t.Fatalf("orders stored for a real account: %+v", os)
		}
	})
	t.Run("view-only account", func(t *testing.T) {
		x := newExecEnv(t)
		a := x.a
		a.Platform = domain.PlatformTrading212
		if _, err := x.svc.placeEntry(x.ctx, a, x.entry(domain.Long), domain.DefaultRiskLimits); !errors.Is(err, domain.ErrViewOnly) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no broker", func(t *testing.T) {
		x := newExecEnv(t)
		x.svc.d.Broker = nil
		if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); !errors.Is(err, domain.ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("persist failure: zero broker calls", func(t *testing.T) {
		x := newExecEnv(t)
		x.store.OrdersErr = errors.New("disk full")
		if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); err == nil {
			t.Fatal("placed without a stored order")
		}
		ap := domain.AgentPosition{AccountID: x.a.ID, PositionID: 5, Instrument: instNYSE, Direction: domain.Long}
		if _, err := x.svc.closePosition(x.ctx, x.a, ap, "test", nil); err == nil {
			t.Fatal("closed without a stored order")
		}
		target := domain.Order{RequestID: "t", Kind: domain.OrderOpen, EtoroOrderID: 9, State: domain.OrderPending}
		if _, err := x.svc.cancelOrder(x.ctx, x.a, target, "test"); err == nil {
			t.Fatal("cancelled without a stored order")
		}
		if x.b.Opens+x.b.Closes+x.b.Cancels != 0 {
			t.Fatal("broker called without a persisted order")
		}
	})
	t.Run("stale portfolio: abandoned, not sent", func(t *testing.T) {
		x := newExecEnv(t)
		r := x.entry(domain.Long)
		r.PortfolioAt = x.clock.Now().Add(-6 * time.Second)
		o, err := x.svc.placeEntry(x.ctx, x.a, r, domain.DefaultRiskLimits)
		if !errors.Is(err, domain.ErrUnavailable) || o.State != domain.OrderAbandoned || x.b.Opens != 0 {
			t.Fatalf("stale: %+v %v opens=%d", o, err, x.b.Opens)
		}
	})
	t.Run("invalid entry", func(t *testing.T) {
		x := newExecEnv(t)
		r := x.entry(domain.Long)
		r.StopLoss = r.Entry + 1
		if _, err := x.svc.placeEntry(x.ctx, x.a, r, domain.DefaultRiskLimits); !errors.Is(err, domain.ErrInvalid) || x.b.Opens != 0 {
			t.Fatalf("invalid entry: %v", err)
		}
	})
	t.Run("killed: entries refused, close works", func(t *testing.T) {
		x := newExecEnv(t)
		if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); err != nil {
			t.Fatal(err)
		}
		if err := x.svc.trip(x.ctx, x.a, "test kill"); err != nil {
			t.Fatal(err)
		}
		if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Short), domain.DefaultRiskLimits); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("entry while killed: %v", err)
		}
		aps := x.agentPositions(t)
		if len(aps) != 1 {
			t.Fatalf("positions = %+v", aps)
		}
		c, err := x.svc.closePosition(x.ctx, x.a, aps[0], "kill", closePolls)
		if err != nil || c.State != domain.OrderFilled || len(x.agentPositions(t)) != 0 || x.b.Opens != 1 {
			t.Fatalf("close while killed = %+v %v", c, err)
		}
	})
	t.Run("unreadable kill switch refuses entries", func(t *testing.T) {
		x := newExecEnv(t)
		x.svc.d.Store = riskErrStore{x.store}
		if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); !errors.Is(err, domain.ErrUnavailable) || x.b.Opens != 0 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("not reconciled", func(t *testing.T) {
		x := newExecEnv(t)
		other := x.account(t, "Second", domain.ModeDemo)
		if _, err := x.svc.placeEntry(x.ctx, other, x.entry(domain.Long), domain.DefaultRiskLimits); !errors.Is(err, domain.ErrUnavailable) || x.b.Opens != 0 {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("lease held by another process: no mutation", func(t *testing.T) {
		x := newExecEnv(t)
		now := x.clock.Now()
		if err := x.store.AgentLeases().Acquire(x.ctx, x.a.ID, "other-process", now, now.Add(domain.LeaseTTL)); err != nil {
			t.Fatal(err)
		}
		_, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
		ap := domain.AgentPosition{AccountID: x.a.ID, PositionID: 5, Instrument: instNYSE, Direction: domain.Long}
		_, cerr := x.svc.closePosition(x.ctx, x.a, ap, "test", nil)
		if !errors.Is(err, domain.ErrConflict) || !errors.Is(cerr, domain.ErrConflict) || x.b.Opens+x.b.Closes != 0 {
			t.Fatalf("err = %v / %v", err, cerr)
		}
	})
}

// riskErrStore fails every risk-state read.
type riskErrStore struct{ *fakes.Store }

func (s riskErrStore) RiskStates() ports.RiskStates { return riskErrRepo{s.Store.RiskStates()} }

type riskErrRepo struct{ ports.RiskStates }

func (riskErrRepo) Get(context.Context, string) (domain.AccountRiskState, error) {
	return domain.AccountRiskState{}, errors.New("database gone")
}

// hookBroker wraps a SimBroker to script answers it cannot give by itself.
type hookBroker struct {
	*fakes.SimBroker
	open   func(ctx context.Context, in domain.OrderIntent) (domain.OrderStatus, error)
	lookup func(ctx context.Context, id string) (domain.OrderStatus, error)
	cancel func(ctx context.Context, orderID int64, id string) error
	close  func(ctx context.Context, pos int64, inst domain.InstrumentID, id string) (domain.OrderStatus, error)
	pf     func(ctx context.Context) (domain.Portfolio, error)
	co     func(ctx context.Context, orderID int64) (domain.OrderStatus, error)
}

func (h *hookBroker) Open(ctx context.Context, in domain.OrderIntent) (domain.OrderStatus, error) {
	if h.open != nil {
		return h.open(ctx, in)
	}
	return h.SimBroker.Open(ctx, in)
}

func (h *hookBroker) Lookup(ctx context.Context, id string) (domain.OrderStatus, error) {
	if h.lookup != nil {
		return h.lookup(ctx, id)
	}
	return h.SimBroker.Lookup(ctx, id)
}

func (h *hookBroker) Cancel(ctx context.Context, orderID int64, id string) error {
	if h.cancel != nil {
		return h.cancel(ctx, orderID, id)
	}
	return h.SimBroker.Cancel(ctx, orderID, id)
}

func (h *hookBroker) Close(ctx context.Context, pos int64, inst domain.InstrumentID, id string) (domain.OrderStatus, error) {
	if h.close != nil {
		return h.close(ctx, pos, inst, id)
	}
	return h.SimBroker.Close(ctx, pos, inst, id)
}

func (h *hookBroker) Portfolio(ctx context.Context) (domain.Portfolio, error) {
	if h.pf != nil {
		return h.pf(ctx)
	}
	return h.SimBroker.Portfolio(ctx)
}

func (h *hookBroker) CloseOrder(ctx context.Context, orderID int64) (domain.OrderStatus, error) {
	if h.co != nil {
		return h.co(ctx, orderID)
	}
	return h.SimBroker.CloseOrder(ctx, orderID)
}

func TestExecutor429Edges(t *testing.T) {
	rateLimited := &domain.NotAcceptedError{RateLimited: true, RetryAfter: 0, Reason: "429"}
	t.Run("429 but eToro has it: adopted, not resent", func(t *testing.T) {
		x := newExecEnv(t)
		h := &hookBroker{SimBroker: x.b}
		h.open = func(ctx context.Context, in domain.OrderIntent) (domain.OrderStatus, error) {
			if _, err := x.b.Open(ctx, in); err != nil {
				return domain.OrderStatus{}, err
			}
			return domain.OrderStatus{}, rateLimited
		}
		x.svc.d.Broker = h
		o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
		if err != nil || o.State != domain.OrderFilled || x.b.Opens != 1 || o.Sends != 1 {
			t.Fatalf("adopted = %+v %v opens=%d", o, err, x.b.Opens)
		}
	})
	t.Run("429 and the lookup fails: unknown, resolved by lookup, not resent", func(t *testing.T) {
		x := newExecEnv(t)
		h := &hookBroker{SimBroker: x.b}
		calls := 0
		h.open = func(ctx context.Context, in domain.OrderIntent) (domain.OrderStatus, error) {
			if _, err := x.b.Open(ctx, in); err != nil {
				return domain.OrderStatus{}, err
			}
			return domain.OrderStatus{}, rateLimited
		}
		h.lookup = func(ctx context.Context, id string) (domain.OrderStatus, error) {
			calls++
			if calls == 1 {
				return domain.OrderStatus{}, fmt.Errorf("read timeout: %w", domain.ErrUnavailable)
			}
			return x.b.Lookup(ctx, id)
		}
		x.svc.d.Broker = h
		o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
		if err != nil || o.State != domain.OrderFilled || x.b.Opens != 1 {
			t.Fatalf("= %+v %v", o, err)
		}
	})
	t.Run("real mode answer from the broker: abandoned", func(t *testing.T) {
		x := newExecEnv(t)
		h := &hookBroker{SimBroker: x.b}
		h.open = func(context.Context, domain.OrderIntent) (domain.OrderStatus, error) {
			return domain.OrderStatus{}, domain.ErrRealModeGated
		}
		x.svc.d.Broker = h
		o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
		if !errors.Is(err, domain.ErrRealModeGated) || o.State != domain.OrderAbandoned {
			t.Fatalf("= %+v %v", o, err)
		}
	})
	t.Run("context ends while waiting to resend", func(t *testing.T) {
		x := newExecEnv(t)
		x.b.OpenScript = []fakes.SimOutcome{fakes.SimRateLimited}
		ctx, cancel := context.WithCancel(x.ctx)
		x.svc.d.Sleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
		o, err := x.svc.placeEntry(ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
		if !errors.Is(err, context.Canceled) || o.State != domain.OrderSent || x.b.Opens != 1 {
			t.Fatalf("= %+v %v", o, err)
		}
	})
	t.Run("lookups failing: left unknown for reconciliation", func(t *testing.T) {
		x := newExecEnv(t)
		x.b.OpenScript = []fakes.SimOutcome{fakes.SimLostAnswer}
		h := &hookBroker{SimBroker: x.b}
		h.lookup = func(context.Context, string) (domain.OrderStatus, error) {
			return domain.OrderStatus{}, fmt.Errorf("down: %w", domain.ErrUnavailable)
		}
		x.svc.d.Broker = h
		o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
		if err != nil || o.State != domain.OrderUnknown || x.b.Opens != 1 {
			t.Fatalf("= %+v %v", o, err)
		}
		if r := domain.Reservations(x.openOrders(t)); r.Unknown != 1 || r.Amount != 100_00 {
			t.Fatalf("an unknown order must keep its reservation and block entries: %+v", r)
		}
	})
	t.Run("lease lost while waiting after a 429: still one open", func(t *testing.T) {
		x := newExecEnv(t)
		x.b.OpenScript = []fakes.SimOutcome{fakes.SimRateLimited}
		x.svc.d.Sleep = func(ctx context.Context, d time.Duration) error {
			x.clock.Advance(domain.LeaseTTL + d)
			now := x.clock.Now()
			return x.store.AgentLeases().Acquire(ctx, x.a.ID, "other-process", now, now.Add(domain.LeaseTTL))
		}
		o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
		if err != nil || x.b.Opens != 1 || o.State != domain.OrderAbandoned {
			t.Fatalf("= %+v %v opens=%d", o, err, x.b.Opens)
		}
	})
	t.Run("another writer moves the order first: followed", func(t *testing.T) {
		x := newExecEnv(t)
		x.b.OpenScript, x.b.PendingLookups = []fakes.SimOutcome{fakes.SimPendingThenFill}, 2
		h := &hookBroker{SimBroker: x.b}
		h.lookup = func(ctx context.Context, id string) (domain.OrderStatus, error) {
			st, err := x.b.Lookup(ctx, id)
			cur := x.order(t, id)
			if err == nil && st.State == domain.OrderPending && cur.State == domain.OrderPending {
				// the supervisor records eToro's answer between our read and our write
				next, _ := cur.Transition(domain.OrderPartial, x.clock.Now(), "elsewhere")
				if err := x.store.Orders().Update(ctx, cur, next); err != nil {
					t.Fatal(err)
				}
				st.State = domain.OrderPending
			}
			return st, err
		}
		x.svc.d.Broker = h
		o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
		if err != nil || o.State != domain.OrderFilled {
			t.Fatalf("= %+v %v", o, err)
		}
	})
	t.Run("eToro reports another instrument: non-conforming", func(t *testing.T) {
		x := newExecEnv(t)
		h := &hookBroker{SimBroker: x.b}
		h.lookup = func(ctx context.Context, id string) (domain.OrderStatus, error) {
			st, err := x.b.Lookup(ctx, id)
			st.Instrument = 999
			return st, err
		}
		x.svc.d.Broker = h
		if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); err != nil {
			t.Fatal(err)
		}
		if x.b.Closes != 1 || !slices.Contains(x.blocked(t), instNYSE) {
			t.Fatalf("closes = %d blocked = %v", x.b.Closes, x.blocked(t))
		}
	})
	t.Run("stop far from the one sent: non-conforming", func(t *testing.T) {
		x := newExecEnv(t)
		h := &hookBroker{SimBroker: x.b}
		h.lookup = func(ctx context.Context, id string) (domain.OrderStatus, error) {
			st, err := x.b.Lookup(ctx, id)
			for i := range st.Executions {
				st.Executions[i].StopLossRate /= 100 // GBX vs GBP (U4)
			}
			return st, err
		}
		x.svc.d.Broker = h
		if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); err != nil {
			t.Fatal(err)
		}
		if x.b.Closes != 1 || !slices.Contains(x.blocked(t), instNYSE) {
			t.Fatalf("closes = %d blocked = %v", x.b.Closes, x.blocked(t))
		}
	})
}

func TestStopClose(t *testing.T) {
	for _, tt := range []struct {
		got, want domain.Micros
		ok        bool
	}{
		{100_000_000, 100_000_000, true},
		{101_000_000, 100_000_000, true},
		{99_000_000, 100_000_000, true},
		{101_000_001, 100_000_000, false},
		{1_000_000, 100_000_000, false},
		{0, 100_000_000, false},
		{5, 0, false},
	} {
		if got := stopClose(tt.got, tt.want); got != tt.ok {
			t.Fatalf("stopClose(%d, %d) = %v", tt.got, tt.want, got)
		}
	}
}

// openPosition fills a long through the executor and returns its agent position.
func (x *execEnv) openPosition(t *testing.T, inst domain.InstrumentID, d domain.Direction) domain.AgentPosition {
	t.Helper()
	r := x.entry(d)
	r.Instrument, r.PortfolioAt = inst, x.clock.Now()
	o, err := x.svc.placeEntry(x.ctx, x.a, r, domain.DefaultRiskLimits)
	if err != nil || o.State != domain.OrderFilled {
		t.Fatalf("open = %+v %v", o, err)
	}
	ap, err := x.store.AgentPositions().Get(x.ctx, x.a.ID, o.PositionID)
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

func TestExecutorClosePosition(t *testing.T) {
	tests := []struct {
		name      string
		script    []fakes.SimOutcome
		wantState domain.OrderState
		wantErr   error
		closes    int
		sameID    bool
		stillOpen bool
	}{
		{name: "filled", script: []fakes.SimOutcome{fakes.SimFill}, wantState: domain.OrderFilled, closes: 1},
		{name: "lost answer, position gone: filled without a resend", script: []fakes.SimOutcome{fakes.SimLostAnswer}, wantState: domain.OrderFilled, closes: 1},
		{name: "lost answer, position still open: resent with the same id", script: []fakes.SimOutcome{fakes.SimServerError, fakes.SimFill}, wantState: domain.OrderFilled, closes: 2, sameID: true},
		{name: "5 lost answers: unknown", script: []fakes.SimOutcome{fakes.SimServerError, fakes.SimServerError, fakes.SimServerError, fakes.SimServerError, fakes.SimServerError}, wantState: domain.OrderUnknown, wantErr: domain.ErrOutcomeUnknown, closes: 5, sameID: true, stillOpen: true},
		{name: "429 then filled: same id", script: []fakes.SimOutcome{fakes.SimRateLimited, fakes.SimFill}, wantState: domain.OrderFilled, closes: 2, sameID: true},
		{name: "429 five times: rejected", script: []fakes.SimOutcome{fakes.SimRateLimited, fakes.SimRateLimited, fakes.SimRateLimited, fakes.SimRateLimited, fakes.SimRateLimited}, wantState: domain.OrderRejected, wantErr: domain.ErrNotAccepted, closes: 5, sameID: true, stillOpen: true},
		{name: "refused", script: []fakes.SimOutcome{fakes.SimRefused}, wantState: domain.OrderRejected, wantErr: domain.ErrNotAccepted, closes: 1, stillOpen: true},
		{name: "pending: left for the supervisor", script: []fakes.SimOutcome{fakes.SimPending}, wantState: domain.OrderPending, closes: 1, stillOpen: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newExecEnv(t)
			ap := x.openPosition(t, instNYSE, domain.Long)
			// Old enough for its absence from the portfolio to prove it closed (U11).
			x.clock.Advance(fillVisibleAfter)
			x.b.CloseScript = tt.script
			o, err := x.svc.closePosition(x.ctx, x.a, ap, "test", closePolls)
			if (tt.wantErr == nil && err != nil) || (tt.wantErr != nil && !errors.Is(err, tt.wantErr)) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			stored := x.order(t, o.RequestID)
			if stored.State != tt.wantState || stored.Kind != domain.OrderClose || stored.PositionID != ap.PositionID {
				t.Fatalf("close = %+v, want %s", stored, tt.wantState)
			}
			if x.b.Closes != tt.closes || stored.Sends != tt.closes {
				t.Fatalf("closes = %d sends = %d, want %d", x.b.Closes, stored.Sends, tt.closes)
			}
			if tt.sameID {
				for _, id := range x.b.CloseRequests {
					if id != o.RequestID {
						t.Fatalf("close ids = %v", x.b.CloseRequests)
					}
				}
			}
			if open := len(x.agentPositions(t)) == 1; open != tt.stillOpen {
				t.Fatalf("agent position open = %v, want %v", open, tt.stillOpen)
			}
			got, _ := x.store.AgentPositions().Get(x.ctx, x.a.ID, ap.PositionID)
			if got.CloseRequestID != o.RequestID {
				t.Fatalf("close request on the position = %q", got.CloseRequestID)
			}
			noViolations(t, x.b)
		})
	}
}

func TestExecutorCloseEdges(t *testing.T) {
	t.Run("position already gone at eToro", func(t *testing.T) {
		x := newExecEnv(t)
		ap := x.openPosition(t, instNYSE, domain.Long)
		x.clock.Advance(fillVisibleAfter)
		x.b.Positions = nil
		o, err := x.svc.closePosition(x.ctx, x.a, ap, "test", closePolls)
		if err != nil || o.State != domain.OrderRejected || len(x.agentPositions(t)) != 0 {
			t.Fatalf("= %+v %v", o, err)
		}
	})
	t.Run("a young position missing from the portfolio is not taken for closed", func(t *testing.T) {
		x := newExecEnv(t)
		ap := x.openPosition(t, instNYSE, domain.Long)
		x.b.Positions = nil
		o, err := x.svc.closePosition(x.ctx, x.a, ap, "test", closePolls)
		if !errors.Is(err, domain.ErrNotAccepted) || o.State != domain.OrderRejected || len(x.agentPositions(t)) != 1 {
			t.Fatalf("= %+v %v, agent positions %+v", o, err, x.agentPositions(t))
		}
	})
	t.Run("a close in flight is not sent twice", func(t *testing.T) {
		x := newExecEnv(t)
		ap := x.openPosition(t, instNYSE, domain.Long)
		x.b.CloseScript = []fakes.SimOutcome{fakes.SimPending}
		first, err := x.svc.closePosition(x.ctx, x.a, ap, "flatten", nil)
		if err != nil || first.State != domain.OrderPending {
			t.Fatalf("first = %+v %v", first, err)
		}
		ap, _ = x.store.AgentPositions().Get(x.ctx, x.a.ID, ap.PositionID)
		again, err := x.svc.closePosition(x.ctx, x.a, ap, "flatten", nil)
		if err != nil || again.RequestID != first.RequestID || x.b.Closes != 1 {
			t.Fatalf("again = %+v %v closes=%d", again, err, x.b.Closes)
		}
	})
	t.Run("a stale close without an eToro id is abandoned and replaced", func(t *testing.T) {
		x := newExecEnv(t)
		ap := x.openPosition(t, instNYSE, domain.Long)
		h := &hookBroker{SimBroker: x.b}
		h.close = func(context.Context, int64, domain.InstrumentID, string) (domain.OrderStatus, error) {
			return domain.OrderStatus{}, domain.ErrOutcomeUnknown
		}
		h.pf = func(context.Context) (domain.Portfolio, error) { return domain.Portfolio{}, domain.ErrUnavailable }
		x.svc.d.Broker = h
		first, err := x.svc.closePosition(x.ctx, x.a, ap, "flatten", nil)
		if !errors.Is(err, domain.ErrOutcomeUnknown) || first.State != domain.OrderUnknown {
			t.Fatalf("first = %+v %v", first, err)
		}
		ap, _ = x.store.AgentPositions().Get(x.ctx, x.a.ID, ap.PositionID)
		// still in flight for two minutes
		if again, err := x.svc.closePosition(x.ctx, x.a, ap, "flatten", nil); err != nil || again.RequestID != first.RequestID {
			t.Fatalf("in flight = %+v %v", again, err)
		}
		x.clock.Advance(lookupGiveUp)
		x.svc.d.Broker = x.b
		second, err := x.svc.closePosition(x.ctx, x.a, ap, "flatten", closePolls)
		if err != nil || second.RequestID == first.RequestID || second.State != domain.OrderFilled {
			t.Fatalf("second = %+v %v", second, err)
		}
		if x.order(t, first.RequestID).State != domain.OrderAbandoned {
			t.Fatal("stale close not abandoned")
		}
	})
	t.Run("close order rejected by eToro", func(t *testing.T) {
		x := newExecEnv(t)
		ap := x.openPosition(t, instNYSE, domain.Long)
		h := &hookBroker{SimBroker: x.b}
		h.co = func(context.Context, int64) (domain.OrderStatus, error) {
			return domain.OrderStatus{State: domain.OrderRejected, Final: true, Reason: "market closed"}, nil
		}
		x.svc.d.Broker = h
		x.b.CloseScript = []fakes.SimOutcome{fakes.SimPending}
		o, err := x.svc.closePosition(x.ctx, x.a, ap, "flatten", closePolls)
		if err != nil || o.State != domain.OrderRejected || !slices.Contains(x.eventKinds(t), "order.rejected") {
			t.Fatalf("= %+v %v", o, err)
		}
	})
	t.Run("close answered real-mode gated", func(t *testing.T) {
		x := newExecEnv(t)
		ap := x.openPosition(t, instNYSE, domain.Long)
		h := &hookBroker{SimBroker: x.b}
		h.close = func(context.Context, int64, domain.InstrumentID, string) (domain.OrderStatus, error) {
			return domain.OrderStatus{}, domain.ErrRealModeGated
		}
		x.svc.d.Broker = h
		if o, err := x.svc.closePosition(x.ctx, x.a, ap, "flatten", nil); !errors.Is(err, domain.ErrRealModeGated) || o.State != domain.OrderAbandoned {
			t.Fatalf("= %+v %v", o, err)
		}
	})
}

func TestExecutorCancelOrder(t *testing.T) {
	pendingOrder := func(t *testing.T, x *execEnv) domain.Order {
		t.Helper()
		x.b.OpenScript = []fakes.SimOutcome{fakes.SimPending}
		h := &hookBroker{SimBroker: x.b}
		h.lookup = func(context.Context, string) (domain.OrderStatus, error) {
			return domain.OrderStatus{}, fmt.Errorf("down: %w", domain.ErrUnavailable)
		}
		x.svc.d.Broker = h
		o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
		if err != nil || o.State != domain.OrderPending || o.EtoroOrderID == 0 {
			t.Fatalf("pending = %+v %v", o, err)
		}
		x.svc.d.Broker = x.b
		return o
	}
	tests := []struct {
		name      string
		err       []error
		wantState domain.OrderState
		wantErr   error
		sends     int
	}{
		{name: "accepted", wantState: domain.OrderFilled, sends: 1},
		{name: "429 then accepted", err: []error{&domain.NotAcceptedError{RateLimited: true}}, wantState: domain.OrderFilled, sends: 2},
		{name: "429 three times", err: []error{&domain.NotAcceptedError{RateLimited: true}, &domain.NotAcceptedError{RateLimited: true}, &domain.NotAcceptedError{RateLimited: true}}, wantState: domain.OrderRejected, wantErr: domain.ErrNotAccepted, sends: 3},
		{name: "refused", err: []error{&domain.NotAcceptedError{Reason: "no"}}, wantState: domain.OrderRejected, wantErr: domain.ErrNotAccepted, sends: 1},
		{name: "lost", err: []error{domain.ErrOutcomeUnknown}, wantState: domain.OrderUnknown, wantErr: domain.ErrOutcomeUnknown, sends: 1},
		{name: "gated", err: []error{domain.ErrRealModeGated}, wantState: domain.OrderAbandoned, wantErr: domain.ErrRealModeGated, sends: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newExecEnv(t)
			target := pendingOrder(t, x)
			h := &hookBroker{SimBroker: x.b}
			var ids []string
			h.cancel = func(ctx context.Context, orderID int64, id string) error {
				ids = append(ids, id)
				if len(tt.err) > 0 {
					err := tt.err[0]
					tt.err = tt.err[1:]
					return err
				}
				return x.b.Cancel(ctx, orderID, id)
			}
			x.svc.d.Broker = h
			c, err := x.svc.cancelOrder(x.ctx, x.a, target, "test")
			if (tt.wantErr == nil && err != nil) || (tt.wantErr != nil && !errors.Is(err, tt.wantErr)) {
				t.Fatalf("err = %v", err)
			}
			if c.State != tt.wantState || c.Sends != tt.sends || len(ids) != tt.sends || c.TargetOrderID != target.EtoroOrderID {
				t.Fatalf("cancel = %+v ids=%v", c, ids)
			}
			for _, id := range ids {
				if id != c.RequestID || id == target.RequestID {
					t.Fatalf("cancel ids = %v (cancel %s, target %s)", ids, c.RequestID, target.RequestID)
				}
			}
		})
	}
	t.Run("only a pending open order", func(t *testing.T) {
		x := newExecEnv(t)
		for _, o := range []domain.Order{
			{Kind: domain.OrderClose, EtoroOrderID: 1, State: domain.OrderPending},
			{Kind: domain.OrderOpen, State: domain.OrderPending},
			{Kind: domain.OrderOpen, EtoroOrderID: 1, State: domain.OrderFilled},
		} {
			if _, err := x.svc.cancelOrder(x.ctx, x.a, o, "test"); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("cancel of %+v: %v", o, err)
			}
		}
	})
}
