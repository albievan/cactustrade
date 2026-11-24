package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// Tests for the findings of the phase-4 money-path review (2026-11-24). Each one failed on the
// code before its fix.

// faultStore wraps the fake store with faults that can be switched on and off.
type faultStore struct {
	*fakes.Store
	mu sync.Mutex
	// apAddErr makes AgentPositions().Add fail; riskPutErr RiskStates().Put.
	apAddErr, riskPutErr error
	// getErr / setErr make Settings().Get / Set of one key fail.
	getErr, setErr map[string]error
	// ctxOrders makes Orders writes fail on a cancelled context, as a database driver does.
	ctxOrders bool
}

func newFaultStore(s *fakes.Store) *faultStore {
	return &faultStore{Store: s, getErr: map[string]error{}, setErr: map[string]error{}}
}

func (f *faultStore) set(edit func(*faultStore)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(f)
}

func (f *faultStore) fault(pick func(*faultStore) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return pick(f)
}

func (f *faultStore) AgentPositions() ports.AgentPositions {
	return faultAP{f.Store.AgentPositions(), f}
}

func (f *faultStore) RiskStates() ports.RiskStates { return faultRisk{f.Store.RiskStates(), f} }

func (f *faultStore) Settings() ports.Settings { return faultSettings{f.Store.Settings(), f} }

func (f *faultStore) Orders() ports.Orders { return faultOrders{f.Store.Orders(), f} }

type faultAP struct {
	ports.AgentPositions
	f *faultStore
}

func (r faultAP) Add(ctx context.Context, p domain.AgentPosition) error {
	if err := r.f.fault(func(f *faultStore) error { return f.apAddErr }); err != nil {
		return err
	}
	return r.AgentPositions.Add(ctx, p)
}

type faultRisk struct {
	ports.RiskStates
	f *faultStore
}

func (r faultRisk) Put(ctx context.Context, st domain.AccountRiskState) (domain.AccountRiskState, error) {
	if err := r.f.fault(func(f *faultStore) error { return f.riskPutErr }); err != nil {
		return domain.AccountRiskState{}, err
	}
	return r.RiskStates.Put(ctx, st)
}

type faultSettings struct {
	ports.Settings
	f *faultStore
}

func (r faultSettings) Get(ctx context.Context, k string) (string, error) {
	if err := r.f.fault(func(f *faultStore) error { return f.getErr[k] }); err != nil {
		return "", err
	}
	return r.Settings.Get(ctx, k)
}

func (r faultSettings) Set(ctx context.Context, k, v string, at time.Time) error {
	if err := r.f.fault(func(f *faultStore) error { return f.setErr[k] }); err != nil {
		return err
	}
	return r.Settings.Set(ctx, k, v, at)
}

type faultOrders struct {
	ports.Orders
	f *faultStore
}

func (r faultOrders) ctxErr(ctx context.Context) error {
	if r.f.fault(func(f *faultStore) error {
		if f.ctxOrders {
			return ctx.Err()
		}
		return nil
	}) != nil {
		return ctx.Err()
	}
	return nil
}

func (r faultOrders) Create(ctx context.Context, o domain.Order) error {
	if err := r.ctxErr(ctx); err != nil {
		return err
	}
	return r.Orders.Create(ctx, o)
}

func (r faultOrders) Update(ctx context.Context, prev, next domain.Order) error {
	if err := r.ctxErr(ctx); err != nil {
		return err
	}
	return r.Orders.Update(ctx, prev, next)
}

// B1: a fill eToro's portfolio does not show yet keeps its cash reserved, so the next entry
// in the same tick is not sized from a cash figure that has not paid for it.
func TestAgentTickReservesUnseenFills(t *testing.T) {
	ae := newAgentEnv(t)
	ae.addNYSE(t, instMSFT, "MSFT")
	if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	ae.limits(t, func(l *domain.RiskLimits) { l.MaxPosition = 0 })
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	ae.b.Cash, ae.b.PortfolioLag = 3_000_00, 30*time.Second
	tr := ae.tick(t)
	if tr.Entries != 2 || len(ae.b.Intents) != 2 {
		t.Fatalf("entries = %d, intents %+v, trace %+v", tr.Entries, ae.b.Intents, tr)
	}
	if ae.b.HiddenFills == 0 {
		t.Fatal("the first fill was never hidden: the test does not exercise the lag")
	}
	var sum domain.Cents
	for _, in := range ae.b.Intents {
		sum += in.Amount
	}
	if sum > 3_000_00 || ae.b.Actual().AvailableCash < 0 {
		t.Fatalf("spent $%s of $3000 cash (intents %+v)", sum, ae.b.Intents)
	}
	if second := ae.b.Intents[1].Amount; second > 1_000_00 {
		t.Fatalf("second entry $%s, want at most the $1000 the first fill left", second)
	}
	ae.checkIntents(t)
}

// B2: a fill whose recording fails keeps the order open (never final), so the next pass
// records it; the position is never left as a foreign one.
func TestExecutorRecordFailureKeepsTheOrderOpen(t *testing.T) {
	x := newExecEnv(t)
	fs := newFaultStore(x.store)
	x.svc.d.Store = fs
	fs.set(func(f *faultStore) { f.apAddErr = errors.New("disk full") })
	o, _ := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
	stored := x.order(t, o.RequestID)
	if stored.State.Final() || len(x.agentPositions(t)) != 0 {
		t.Fatalf("order %s with its fill unrecorded: must not be final", stored.State)
	}
	if r := domain.Reservations(x.openOrders(t)); r.Amount != stored.Amount {
		t.Fatalf("the unrecorded fill must stay reserved: %+v", r)
	}
	if x.b.Cancels != 0 {
		t.Fatalf("a filled order was cancelled %d times", x.b.Cancels)
	}
	fs.set(func(f *faultStore) { f.apAddErr = nil })
	if _, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	aps := x.agentPositions(t)
	if st := x.order(t, o.RequestID).State; st != domain.OrderFilled || len(aps) != 1 || aps[0].OpenRequestID != o.RequestID {
		t.Fatalf("after recovery: order %s, positions %+v", st, aps)
	}
}

// B2: a lookup that says filled without any positionExecutions is still pending.
func TestExecutorFilledWithoutExecutions(t *testing.T) {
	x := newExecEnv(t)
	x.b.OpenScript, x.b.NoExecLookups = []fakes.SimOutcome{fakes.SimFilledNoExecutions}, 2
	o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
	if err != nil || o.State != domain.OrderFilled {
		t.Fatalf("= %+v %v", o, err)
	}
	if aps := x.agentPositions(t); len(aps) != 1 || aps[0].OpenRequestID != o.RequestID {
		t.Fatalf("agent positions = %+v", aps)
	}
	noViolations(t, x.b)
}

// B2/U2: an order abandoned after two minutes of 404s that eToro did fill is adopted by
// reconciliation (and then closed like any position of ours); positions the owner opened
// are never adopted.
func TestReconcileAdoptsUnrecordedFills(t *testing.T) {
	x := newExecEnv(t)
	x.b.OpenScript, x.b.LookupLag = []fakes.SimOutcome{fakes.SimLostAnswer}, 3*time.Minute
	o, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
	if err != nil || o.State != domain.OrderAbandoned || len(x.b.Actual().Positions) != 1 {
		t.Fatalf("= %+v %v", o, err)
	}
	ours := x.b.Actual().Positions[0]
	// the owner's own positions: same share but opened before our send; same share, short
	x.b.Positions = append(x.b.Positions,
		domain.Position{ID: 777, Instrument: instNYSE, Long: true, RealAsset: true, Leverage: 1, Amount: 10_00, OpenedAt: o.SentAt.Add(-time.Minute)},
		domain.Position{ID: 778, Instrument: instNYSE, Long: false, CFD: true, Leverage: 1, Amount: 10_00, OpenedAt: o.SentAt.Add(time.Second)},
	)
	r, err := x.svc.Reconcile(x.ctx, x.a.ID)
	if err != nil || !r.OK {
		t.Fatalf("reconcile = %+v %v", r, err)
	}
	aps := x.agentPositions(t)
	if len(aps) != 1 || aps[0].PositionID != ours.ID || aps[0].OpenRequestID != o.RequestID || aps[0].Direction != domain.Long {
		t.Fatalf("adopted = %+v, want position %d of order %s only", aps, ours.ID, o.RequestID)
	}
	if !slices.Contains(x.eventKinds(t), "position.adopted") {
		t.Fatalf("events = %v", x.eventKinds(t))
	}
	// a second pass adopts nothing more
	if _, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || len(x.agentPositions(t)) != 1 {
		t.Fatalf("second pass: %v %+v", err, x.agentPositions(t))
	}
}

// M1: a slow reviewer does not make every entry "stale": the clock is read again after the
// fresh portfolio and quote.
func TestAgentTickSlowReviewer(t *testing.T) {
	ae := newAgentEnv(t)
	ae.reviewing(t, func(context.Context, ports.ReviewInput) (domain.Verdict, error) {
		ae.clock.Advance(3 * time.Second)
		return domain.Verdict{Kind: domain.VerdictApprove, SizeBp: domain.FullSize}, nil
	})
	tr := ae.tick(t)
	if tr.Entries != 1 || ae.b.Opens != 1 {
		t.Fatalf("entry refused after a 3 s review: %+v", tr.Instruments)
	}
	ae.checkIntents(t)
}

// fixedQuotes stamps every quote at a fixed time.
type fixedQuotes struct {
	*agentMarket
	at time.Time
}

func (m fixedQuotes) Quotes(ctx context.Context, ids []domain.InstrumentID) ([]domain.Quote, error) {
	qs, err := m.agentMarket.Quotes(ctx, ids)
	for i := range qs {
		qs[i].At = m.at
	}
	return qs, err
}

// M1: a quote older than MaxQuoteAge by the time the slow call returns is still refused.
func TestAgentTickSlowReviewerStaleQuote(t *testing.T) {
	ae := newAgentEnv(t)
	ae.svc.d.Market = fixedQuotes{ae.m, ae.clock.Now()}
	ae.reviewing(t, func(context.Context, ports.ReviewInput) (domain.Verdict, error) {
		ae.clock.Advance(domain.DefaultRiskLimits.MaxQuoteAge + time.Second)
		return domain.Verdict{Kind: domain.VerdictApprove, SizeBp: domain.FullSize}, nil
	})
	tr := ae.tick(t)
	if c := lastCheck(tr.Instruments[0]); ae.b.Opens != 0 || c.Step != StepPrices || !strings.Contains(c.Detail, "stale") {
		t.Fatalf("opens %d, last check %+v", ae.b.Opens, c)
	}
}

// M2: a close that eToro ends partly filled leaves the position open, so the supervisor
// closes the rest.
func TestExecutorPartialCloseKeepsThePositionOpen(t *testing.T) {
	x := newExecEnv(t)
	ap := x.openPosition(t, instNYSE, domain.Long)
	x.b.CloseScript = []fakes.SimOutcome{fakes.SimPartialClose}
	o, err := x.svc.closePosition(x.ctx, x.a, ap, "test", closePolls)
	if err != nil || !o.State.Final() || o.State == domain.OrderFilled {
		t.Fatalf("partial close = %+v %v", o, err)
	}
	if len(x.agentPositions(t)) != 1 || len(x.b.Actual().Positions) != 1 {
		t.Fatalf("the rest of the position must stay ours: agent %+v broker %+v", x.agentPositions(t), x.b.Actual().Positions)
	}
	ap, _ = x.store.AgentPositions().Get(x.ctx, x.a.ID, ap.PositionID)
	o2, err := x.svc.closePosition(x.ctx, x.a, ap, "test", closePolls)
	if err != nil || o2.State != domain.OrderFilled || o2.RequestID == o.RequestID || len(x.agentPositions(t)) != 0 || len(x.b.Actual().Positions) != 0 {
		t.Fatalf("second close = %+v %v", o2, err)
	}
}

// Found by the review-M4 simulator (seed 240): a close whose answer is lost re-reads the
// portfolio; a fill younger than the visibility window may be missing from it (U11), and
// that absence must not record the position as closed.
func TestExecutorLostCloseAnswerWhileTheFillIsHidden(t *testing.T) {
	x := newExecEnv(t)
	x.b.PortfolioLag = 30 * time.Second
	ap := x.openPosition(t, instNYSE, domain.Long)
	x.b.CloseScript = []fakes.SimOutcome{fakes.SimServerError}
	o, _ := x.svc.closePosition(x.ctx, x.a, ap, "non-conforming", closePolls)
	if o.State == domain.OrderFilled || len(x.agentPositions(t)) != 1 || len(x.b.Actual().Positions) != 1 {
		t.Fatalf("close %s, agent positions %+v, broker %+v: an open position was recorded closed", o.State, x.agentPositions(t), x.b.Actual().Positions)
	}
	// reconciliation settles the unconfirmed close the same way
	if _, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	if st := x.order(t, o.RequestID).State; st == domain.OrderFilled || len(x.agentPositions(t)) != 1 {
		t.Fatalf("reconcile: close %s, agent positions %+v", st, x.agentPositions(t))
	}
	// later the close is replaced and goes through
	x.clock.Advance(lookupGiveUp)
	ap, _ = x.store.AgentPositions().Get(x.ctx, x.a.ID, ap.PositionID)
	if o, err := x.svc.closePosition(x.ctx, x.a, ap, "non-conforming", closePolls); err != nil || o.State != domain.OrderFilled ||
		len(x.agentPositions(t)) != 0 || len(x.b.Actual().Positions) != 0 {
		t.Fatalf("second close = %+v %v", o, err)
	}
}

// M2 in the supervisor: a partial close is followed by a new close on the next pass.
func TestSupervisePartialCloseClosesTheRest(t *testing.T) {
	x := supEnv(t, time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC))
	x.openPosition(t, instNYSE, domain.Long)
	x.clock.Advance(time.Date(2026, 11, 23, 20, 51, 0, 0, time.UTC).Sub(x.clock.Now())) // NYSE close − 9 min
	// Reconciled just now: the periodic reconciliation (which would reopen a position wrongly
	// recorded as closed) is not due for 5 minutes, past the close.
	if r, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil || !r.OK {
		t.Fatalf("reconcile = %+v %v", r, err)
	}
	x.b.CloseScript = []fakes.SimOutcome{fakes.SimPartialClose}
	for range 3 {
		x.supervise(t)
		x.clock.Advance(SupervisorInterval)
	}
	if len(x.b.Actual().Positions) != 0 || len(x.agentPositions(t)) != 0 {
		t.Fatalf("after three passes: broker %+v agent %+v", x.b.Actual().Positions, x.agentPositions(t))
	}
}

// M3: an open is never resent. After a 429 the executor waits Retry-After, then looks it up:
// found → adopted; not found → polled and abandoned after two minutes. Its cash stays
// reserved until then.
func TestExecutorNeverResendsAnOpen(t *testing.T) {
	tests := []struct {
		name      string
		script    []fakes.SimOutcome
		lag       time.Duration
		wantState domain.OrderState
		positions int
	}{
		{"429, not processed: abandoned after 2 min", []fakes.SimOutcome{fakes.SimRateLimited, fakes.SimFill}, 0, domain.OrderAbandoned, 0},
		{"429, processed: adopted", []fakes.SimOutcome{fakes.SimRateLimitedProcessed}, 0, domain.OrderFilled, 1},
		{"429, processed, lookup lags: found later", []fakes.SimOutcome{fakes.SimRateLimitedProcessed}, 20 * time.Second, domain.OrderFilled, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newExecEnv(t)
			x.b.OpenScript, x.b.LookupLag = tt.script, tt.lag
			h := &hookBroker{SimBroker: x.b}
			var firstLookup time.Time
			h.lookup = func(ctx context.Context, id string) (domain.OrderStatus, error) {
				if firstLookup.IsZero() {
					firstLookup = x.clock.Now()
				}
				if o := x.order(t, id); !o.State.Final() {
					if r := domain.Reservations(x.openOrders(t)); r.Amount != o.Amount || !slices.Contains(r.Instruments, o.Instrument) {
						t.Errorf("order %s in flight without its reservation: %+v", o.State, r)
					}
				}
				return x.b.Lookup(ctx, id)
			}
			x.svc.d.Broker = h
			start := x.clock.Now()
			o, _ := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
			stored := x.order(t, o.RequestID)
			if x.b.Opens != 1 || stored.Sends != 1 || stored.State != tt.wantState {
				t.Fatalf("opens %d, stored %+v, want one send and %s", x.b.Opens, stored, tt.wantState)
			}
			if firstLookup.Sub(start) < fakes.SimRetryAfter {
				t.Fatalf("looked up %s after the 429, before Retry-After %s", firstLookup.Sub(start), fakes.SimRetryAfter)
			}
			if n := len(x.agentPositions(t)); n != tt.positions {
				t.Fatalf("agent positions = %d, want %d", n, tt.positions)
			}
			noViolations(t, x.b)
		})
	}
}

// m2: SetRiskLimits refuses when the agent state cannot be read (it used to read "stopped").
func TestSetRiskLimitsUnreadableAgentState(t *testing.T) {
	ae := newAgentEnv(t)
	fs := newFaultStore(ae.store)
	ae.svc.d.Store = fs
	fs.set(func(f *faultStore) { f.getErr[agentStateKey(ae.a.ID)] = errors.New("database gone") })
	l := domain.DefaultRiskLimits
	l.TradingBudget = 1_00
	if _, err := ae.svc.SetRiskLimits(ae.ctx, ae.admin, l); err == nil {
		t.Fatal("limits changed while the agent state could not be read")
	}
	if got := ae.svc.riskLimits(ae.ctx, ae.a.ID); got.TradingBudget != 10_000_00 {
		t.Fatalf("budget = %s", got.TradingBudget)
	}
}

// m3: when both kill writes fail, the kill still holds in this process after the database
// recovers, until ResetKill.
func TestKillLatchWhenTheWritesFail(t *testing.T) {
	ae := newAgentEnv(t)
	fs := newFaultStore(ae.store)
	ae.svc.d.Store = fs
	down := errors.New("database gone")
	fs.set(func(f *faultStore) { f.riskPutErr, f.setErr[agentStateKey(ae.a.ID)] = down, down })
	if _, err := ae.svc.Kill(ae.ctx, ae.admin, ae.a.ID, "test"); err == nil {
		t.Fatal("Kill reported success although nothing was stored")
	}
	fs.set(func(f *faultStore) { f.riskPutErr, f.setErr = nil, map[string]error{} })
	if killed, _ := ae.svc.isKilled(ae.ctx, ae.a.ID); !killed {
		t.Fatal("the kill was forgotten once the database recovered")
	}
	if tr := ae.tick(t); ae.b.Opens != 0 || tr.Skipped == "" {
		t.Fatalf("the agent entered after a kill: %+v", tr)
	}
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("start after a kill = %v", err)
	}
	if _, err := ae.svc.ResetKill(ae.ctx, ae.admin, ae.a.ID, ResetPhrase(ae.a.Name)); err != nil {
		t.Fatal(err)
	}
	if killed, err := ae.svc.isKilled(ae.ctx, ae.a.ID); killed || err != nil {
		t.Fatalf("after reset: killed %v %v", killed, err)
	}
}

// m4: a Kill that cancels the context while Open is in flight does not lose eToro's answer.
func TestSendOpenStoresTheAnswerAfterACancel(t *testing.T) {
	for _, tt := range []struct {
		outcome fakes.SimOutcome
		want    domain.OrderState
	}{
		{fakes.SimPending, domain.OrderPending},
		{fakes.SimLostAnswer, domain.OrderUnknown},
	} {
		t.Run(string(tt.outcome), func(t *testing.T) {
			x := newExecEnv(t)
			fs := newFaultStore(x.store)
			fs.ctxOrders = true
			x.svc.d.Store = fs
			x.b.OpenScript = []fakes.SimOutcome{tt.outcome}
			ctx, cancel := context.WithCancel(x.ctx)
			defer cancel()
			h := &hookBroker{SimBroker: x.b}
			h.open = func(c context.Context, in domain.OrderIntent) (domain.OrderStatus, error) {
				st, err := x.b.Open(c, in)
				cancel() // the kill arrives while the request is in flight
				return st, err
			}
			x.svc.d.Broker = h
			o, _ := x.svc.placeEntry(ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
			if stored := x.order(t, o.RequestID); stored.State != tt.want || (tt.want == domain.OrderPending && stored.EtoroOrderID == 0) {
				t.Fatalf("stored %+v, want %s", stored, tt.want)
			}
		})
	}
}

// m5: SetRiskLimits reads eToro's equity before it takes the control lock, so a Kill never
// waits for that read.
func TestSetRiskLimitsDoesNotDelayAKill(t *testing.T) {
	ae := newAgentEnv(t)
	if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	h := &hookBroker{SimBroker: ae.b}
	h.pf = func(ctx context.Context) (domain.Portfolio, error) {
		first := false
		once.Do(func() { first = true })
		if first {
			close(entered)
			<-release
		}
		return ae.b.Portfolio(ctx)
	}
	ae.svc.d.Broker = h
	limitsDone := make(chan error, 1)
	go func() {
		l := domain.DefaultRiskLimits
		l.TradingBudget = 5_000_00
		_, err := ae.svc.SetRiskLimits(ae.ctx, ae.admin, l)
		limitsDone <- err
	}()
	<-entered
	killDone := make(chan error, 1)
	go func() {
		_, err := ae.svc.Kill(ae.ctx, ae.admin, ae.a.ID, "test")
		killDone <- err
	}()
	select {
	case err := <-killDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		close(release)
		<-killDone
		t.Fatal("Kill waited for SetRiskLimits' portfolio read")
	}
	close(release)
	if err := <-limitsDone; !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("limits changed after the kill: %v", err)
	}
}
