package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// halfDay is a short NYSE session of the test calendar (the day after Thanksgiving).
var halfDay = time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC)

// testCalendar trades every day: LSE 08:00–16:30 UTC; NYSE 14:30–21:00 UTC, but 14:30–18:00
// on the half day; ASX 23:00–05:00 UTC (the session opening at 23:00 belongs to the next
// UTC date, as Sydney's local date does).
type testCalendar struct{}

func (testCalendar) Session(m domain.Market, at time.Time) (time.Time, time.Time, bool, error) {
	at = at.UTC()
	y, mo, d := at.Date()
	day := func(h, mi int) time.Time { return time.Date(y, mo, d, h, mi, 0, 0, time.UTC) }
	switch m {
	case domain.MarketLSE:
		return day(8, 0), day(16, 30), true, nil
	case domain.MarketNYSE:
		if day(0, 0).Equal(halfDay) {
			return day(14, 30), day(18, 0), true, nil
		}
		return day(14, 30), day(21, 0), true, nil
	case domain.MarketASX:
		if at.Hour() >= 22 {
			return day(23, 0), day(23, 0).Add(6 * time.Hour), true, nil
		}
		return day(0, 0).Add(-time.Hour), day(5, 0), true, nil
	}
	return time.Time{}, time.Time{}, false, fmt.Errorf("no table for %s: %w", m, domain.ErrUnavailable)
}

// supEnv is an execEnv with the test calendar, the clock set to t.
func supEnv(t *testing.T, at time.Time) *execEnv {
	t.Helper()
	x := newExecEnv(t)
	x.svc.d.Calendar = testCalendar{}
	x.clock.Advance(at.Sub(x.clock.Now()))
	if _, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	return x
}

func (x *execEnv) supervise(t *testing.T) SupervisorReport {
	t.Helper()
	rep, err := x.svc.Supervise(x.ctx, x.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func TestSuperviseFlattensPerMarket(t *testing.T) {
	tests := []struct {
		name  string
		inst  domain.InstrumentID
		dir   domain.Direction
		close time.Time
	}{
		{"LSE", instLSE, domain.Long, time.Date(2026, 11, 23, 16, 30, 0, 0, time.UTC)},
		{"NYSE", instNYSE, domain.Short, time.Date(2026, 11, 23, 21, 0, 0, 0, time.UTC)},
		{"NYSE half day", instNYSE, domain.Long, time.Date(2026, 11, 27, 18, 0, 0, 0, time.UTC)},
		{"ASX", instASX, domain.Short, time.Date(2026, 11, 24, 5, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := supEnv(t, tt.close.Add(-2*time.Hour))
			ap := x.openPosition(t, tt.inst, tt.dir)
			x.clock.Advance(tt.close.Add(-11 * time.Minute).Sub(x.clock.Now()))
			if rep := x.supervise(t); len(rep.Exits) != 0 || x.b.Closes != 0 {
				t.Fatalf("at close-11 min: %+v closes=%d", rep, x.b.Closes)
			}
			x.clock.Advance(time.Minute)
			rep := x.supervise(t)
			if len(rep.Exits) != 1 || rep.Exits[0].Reason != domain.ExitFlatten || rep.Exits[0].PositionID != ap.PositionID || rep.Closing != 1 || x.b.Closes != 1 {
				t.Fatalf("at close-10 min: %+v closes=%d", rep, x.b.Closes)
			}
			// the next pass reads the close order and records the position closed
			x.clock.Advance(SupervisorInterval)
			if rep := x.supervise(t); len(rep.Errors) != 0 {
				t.Fatalf("next pass: %+v", rep)
			}
			if len(x.agentPositions(t)) != 0 || x.b.Closes != 1 {
				t.Fatalf("still open after the close: %+v closes=%d", x.agentPositions(t), x.b.Closes)
			}
			noViolations(t, x.b)
		})
	}
}

// countingMarket counts calls and fails them: the supervisor must not need prices.
type countingMarket struct {
	mu    sync.Mutex
	calls int
}

func (f *countingMarket) fail() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return fmt.Errorf("market data down: %w", domain.ErrUnavailable)
}

func (f *countingMarket) Quotes(context.Context, []domain.InstrumentID) ([]domain.Quote, error) {
	return nil, f.fail()
}

func (f *countingMarket) Candles(context.Context, domain.InstrumentID, time.Duration, int) ([]domain.Candle, error) {
	return nil, f.fail()
}

func (f *countingMarket) Search(context.Context, string) ([]domain.Instrument, error) {
	return nil, f.fail()
}

func (f *countingMarket) Instruments(context.Context, []domain.InstrumentID) ([]domain.Instrument, error) {
	return nil, f.fail()
}

// failingReviewer counts calls and fails them: the supervisor never asks the LLM.
type failingReviewer struct{ countingMarket }

func (f *failingReviewer) Review(context.Context, domain.LLMConfig, string, ports.ReviewInput) (domain.Verdict, domain.LLMCall, error) {
	return domain.Verdict{}, domain.LLMCall{}, f.fail()
}

func (f *failingReviewer) Models(context.Context, domain.LLMConfig, string) ([]string, error) {
	return nil, f.fail()
}

func TestSuperviseExitsWhateverTheAgentDoes(t *testing.T) {
	closeAt := time.Date(2026, 11, 23, 21, 0, 0, 0, time.UTC)
	mkt, rev := &countingMarket{}, &failingReviewer{}
	tests := []struct {
		name  string
		setup func(t *testing.T, x *execEnv)
		local bool
	}{
		{"running", func(t *testing.T, x *execEnv) { x.setAgent(t, domain.AgentRunning) }, false},
		{"paused", func(t *testing.T, x *execEnv) { x.setAgent(t, domain.AgentPaused) }, false},
		{"stopped", func(*testing.T, *execEnv) {}, false},
		{"unreconciled", func(_ *testing.T, x *execEnv) {
			x.svc.setReconciled(x.ctx, x.a, false, "test")
		}, false},
		{"portfolio read fails: local positions", func(_ *testing.T, x *execEnv) {
			x.b.PortfolioErr = fmt.Errorf("HTTP 503: %w", domain.ErrUnavailable)
		}, true},
		{"market data and LLM fail", func(_ *testing.T, x *execEnv) {
			x.svc.d.Market, x.svc.d.LLM = mkt, rev
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := supEnv(t, closeAt.Add(-2*time.Hour))
			x.openPosition(t, instNYSE, domain.Long)
			x.openPosition(t, instLSE, domain.Long) // LSE has closed by now: due at once
			tt.setup(t, x)
			x.clock.Advance(closeAt.Add(-10 * time.Minute).Sub(x.clock.Now()))
			rep := x.supervise(t)
			if rep.LocalPositions != tt.local || len(rep.Exits) != 2 || x.b.Closes != 2 || rep.Closing != 2 {
				t.Fatalf("report = %+v closes=%d", rep, x.b.Closes)
			}
			if x.b.Opens != 2 {
				t.Fatalf("opens = %d", x.b.Opens)
			}
		})
	}
	if mkt.calls != 0 || rev.calls != 0 {
		t.Fatalf("the supervisor used market data (%d) or the LLM (%d)", mkt.calls, rev.calls)
	}
}

func (x *execEnv) setAgent(t *testing.T, st domain.AgentState) {
	t.Helper()
	if err := x.store.Settings().Set(x.ctx, agentStateKey(x.a.ID), string(st), x.clock.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestSuperviseLocalPositionsKeepStopChecksForLater(t *testing.T) {
	// with the portfolio unreadable, a position in session is not closed just because its
	// stop cannot be seen
	x := supEnv(t, time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
	x.openPosition(t, instNYSE, domain.Long)
	x.b.PortfolioErr = domain.ErrUnavailable
	if rep := x.supervise(t); !rep.LocalPositions || len(rep.Exits) != 0 || x.b.Closes != 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestSuperviseNonConformingAndNoCalendar(t *testing.T) {
	x := supEnv(t, time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
	ap := x.openPosition(t, instNYSE, domain.Long)
	// eToro reports the stop gone: closed although the session runs
	for i := range x.b.Positions {
		x.b.Positions[i].StopLossRate = 0
	}
	rep := x.supervise(t)
	if len(rep.Exits) != 1 || rep.Exits[0].Reason != domain.ExitNoStop || rep.Exits[0].PositionID != ap.PositionID {
		t.Fatalf("report = %+v", rep)
	}

	y := supEnv(t, time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
	y.openPosition(t, instNYSE, domain.Long)
	y.svc.d.Calendar = nil
	if rep := y.supervise(t); len(rep.Exits) != 1 || rep.Exits[0].Reason != domain.ExitNoSession {
		t.Fatalf("no calendar: %+v", rep)
	}
}

func TestSuperviseDrawdownKills(t *testing.T) {
	x := supEnv(t, time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
	x.setAgent(t, domain.AgentRunning)
	x.openPosition(t, instNYSE, domain.Long)
	x.openPosition(t, instLSE, domain.Short)
	if rep := x.supervise(t); rep.Killed || len(rep.Exits) != 0 || len(rep.Errors) != 0 {
		t.Fatalf("first pass: %+v", rep)
	}
	st, err := x.store.RiskStates().Get(x.ctx, x.a.ID)
	hwm := st.State.HighWaterMark
	if err != nil || hwm <= 0 || st.State.Day != domain.RiskDay(x.clock.Now()) || st.State.StartOfDayEquity != hwm {
		t.Fatalf("observed = %+v %v", st, err)
	}
	// 3 % down: the daily latch, not the kill switch
	x.b.Cash -= hwm * 3 / 100
	if rep := x.supervise(t); rep.Killed || x.b.Closes != 0 {
		t.Fatalf("daily loss: %+v", rep)
	}
	if st, _ := x.store.RiskStates().Get(x.ctx, x.a.ID); !st.State.DayStopped || st.State.Killed {
		t.Fatalf("latch = %+v", st)
	}
	if !slices.Contains(x.eventKinds(t), "risk.daily_stop") {
		t.Fatal("no daily stop event")
	}
	// 11 % below the high-water mark: killed, every agent position closed, no Open
	x.b.Cash -= hwm * 8 / 100
	rep := x.supervise(t)
	if !rep.Tripped || !rep.Killed || len(rep.Exits) != 2 || rep.Exits[0].Reason != domain.ExitKilled || x.b.Closes != 2 {
		t.Fatalf("drawdown: %+v closes=%d", rep, x.b.Closes)
	}
	if st, _ := x.store.RiskStates().Get(x.ctx, x.a.ID); !st.State.Killed || st.KillReason == "" || st.KilledAt.IsZero() {
		t.Fatalf("kill not persisted: %+v", st)
	}
	if x.svc.agentState(x.ctx, x.a.ID) != domain.AgentKilled || !slices.Contains(x.eventKinds(t), "agent.killed") {
		t.Fatal("agent state or event missing")
	}
	if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); err == nil {
		t.Fatal("entry after the kill")
	}
	if x.b.Opens != 2 {
		t.Fatalf("opens after the kill = %d", x.b.Opens)
	}
	// later passes stay killed and do not trip again
	x.clock.Advance(SupervisorInterval)
	if rep := x.supervise(t); rep.Tripped || !rep.Killed {
		t.Fatalf("later pass: %+v", rep)
	}
}

func TestSuperviseLeaseHeldElsewhere(t *testing.T) {
	x := supEnv(t, time.Date(2026, 11, 23, 20, 50, 0, 0, time.UTC))
	x.openPosition(t, instNYSE, domain.Long)
	x.clock.Advance(domain.LeaseTTL) // our lease from the entry has expired
	other, err := New(Deps{Store: x.store, Secrets: x.sec, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: x.clock, Broker: x.b},
		Config{LeaseHolder: "other-process"})
	if err != nil {
		t.Fatal(err)
	}
	if err := other.holdLease(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	rep := x.supervise(t)
	if rep.Skipped == "" || len(rep.Exits) != 0 || x.b.Closes+x.b.Cancels != 0 {
		t.Fatalf("report = %+v", rep)
	}
	// the lease holder does the work
	if rep, err := other.Supervise(x.ctx, x.a.ID); err != nil || rep.Closing != 1 {
		t.Fatalf("holder = %+v %v", rep, err)
	}
}

// pendingEntry places an entry that stays pending (its lookups fail while it is placed).
func (x *execEnv) pendingEntry(t *testing.T) domain.Order {
	t.Helper()
	x.b.OpenScript = []fakes.SimOutcome{fakes.SimPending}
	h := &hookBroker{SimBroker: x.b}
	h.lookup = func(context.Context, string, int64) (domain.OrderStatus, error) {
		return domain.OrderStatus{}, domain.ErrUnavailable
	}
	x.svc.d.Broker = h
	o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
	x.svc.d.Broker = x.b
	if err != nil || o.State != domain.OrderPending {
		t.Fatalf("pending = %+v %v", o, err)
	}
	return o
}

func TestSuperviseCancelsStalePendingOrders(t *testing.T) {
	x := supEnv(t, time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
	o := x.pendingEntry(t) // the executor gave up polling after 2 min
	rep := x.supervise(t)
	if rep.Cancelled != 1 || x.b.Cancels != 1 {
		t.Fatalf("report = %+v cancels=%d", rep, x.b.Cancels)
	}
	x.clock.Advance(SupervisorInterval)
	rep = x.supervise(t)
	if rep.Cancelled != 0 || x.b.Cancels != 1 || x.order(t, o.RequestID).State != domain.OrderCancelled {
		t.Fatalf("second pass: %+v cancels=%d state=%s", rep, x.b.Cancels, x.order(t, o.RequestID).State)
	}

	// a fresh pending order is left to the executor; when killed it is cancelled at once
	y := supEnv(t, time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
	young := y.storedOrder(t, openSpec(instNYSE), y.clock.Now(), domain.OrderSent)
	in, _ := domain.IntentFor(young, domain.RealPermit{})
	y.b.OpenScript = []fakes.SimOutcome{fakes.SimPending}
	st, err := y.b.Open(ports.WithAccount(y.ctx, y.a), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := y.svc.move(y.ctx, young, domain.OrderPending, "", func(n *domain.Order) { n.EtoroOrderID = st.OrderID }); err != nil {
		t.Fatal(err)
	}
	if rep := y.supervise(t); rep.Cancelled != 0 {
		t.Fatalf("young order cancelled: %+v", rep)
	}
	if err := y.svc.trip(y.ctx, y.a, "test"); err != nil {
		t.Fatal(err)
	}
	if rep := y.supervise(t); rep.Cancelled != 1 || y.b.CancelRequests[0] == young.RequestID {
		t.Fatalf("killed: %+v", rep)
	}
}

func TestSuperviseReadsCloseOrdersAndReconciles(t *testing.T) {
	x := supEnv(t, time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
	ap := x.openPosition(t, instNYSE, domain.Long)
	x.b.CloseScript = []fakes.SimOutcome{fakes.SimPending}
	c, err := x.svc.closePosition(x.ctx, x.a, ap, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &hookBroker{SimBroker: x.b}
	h.co = func(context.Context, int64) (domain.OrderStatus, error) {
		return domain.OrderStatus{State: domain.OrderFilled, Final: true}, nil
	}
	x.svc.d.Broker = h
	if rep := x.supervise(t); rep.Reconciled {
		t.Fatalf("reconciled too soon: %+v", rep)
	}
	if x.order(t, c.RequestID).State != domain.OrderFilled {
		t.Fatal("close order not read")
	}
	x.clock.Advance(ReconcileEvery)
	if rep := x.supervise(t); !rep.Reconciled {
		t.Fatalf("no periodic reconcile: %+v", rep)
	}
}

func TestSuperviseSkips(t *testing.T) {
	x := supEnv(t, time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
	real := x.account(t, "Real", domain.ModeReal)
	t212, err := x.svc.CreateAccountOn(x.ctx, x.admin, domain.PlatformTrading212, "T212", domain.ModeReal)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{real.ID, t212.ID} {
		if rep, err := x.svc.Supervise(x.ctx, id); err != nil || rep.Skipped == "" {
			t.Fatalf("%s: %+v %v", id, rep, err)
		}
	}
	if _, err := x.svc.Supervise(x.ctx, "nobody"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	x.svc.d.Broker = nil
	if rep := x.supervise(t); rep.Skipped == "" {
		t.Fatalf("no broker: %+v", rep)
	}
	if x.b.Lookups+x.b.Opens+x.b.Closes+x.b.Cancels != 0 {
		t.Fatal("broker called")
	}
}

func TestSuperviseLoop(t *testing.T) {
	closeAt := time.Date(2026, 11, 23, 21, 0, 0, 0, time.UTC)
	x := supEnv(t, closeAt.Add(-time.Hour))
	x.openPosition(t, instNYSE, domain.Long)
	x.account(t, "Real", domain.ModeReal) // never supervised
	x.clock.Advance(50 * time.Minute)     // close-10 min

	ctx, cancel := context.WithCancel(x.ctx)
	defer cancel()
	var mu sync.Mutex
	ticks := 0
	x.svc.d.Sleep = func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		if d == SupervisorInterval {
			if ticks++; ticks == 3 {
				cancel()
			}
		}
		if d > 0 {
			x.clock.Advance(d)
		}
		return ctx.Err()
	}
	done := make(chan struct{})
	go func() { x.svc.SuperviseLoop(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the loop did not stop")
	}
	if x.b.Closes != 1 || len(x.agentPositions(t)) != 0 {
		t.Fatalf("closes = %d positions = %+v", x.b.Closes, x.agentPositions(t))
	}
	noViolations(t, x.b)

	// an account still being supervised is not started twice
	if !x.svc.rt.claim("a") || x.svc.rt.claim("a") {
		t.Fatal("claim")
	}
	x.svc.rt.release("a")
	if !x.svc.rt.claim("a") {
		t.Fatal("release")
	}
}
