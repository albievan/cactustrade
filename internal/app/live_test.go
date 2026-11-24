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

// orderRecorder records every order version the service stored, in order.
type orderRecorder struct {
	ports.Orders
	mu     sync.Mutex
	stored []domain.Order
}

func (r *orderRecorder) Create(ctx context.Context, o domain.Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.Orders.Create(ctx, o); err != nil {
		return err
	}
	r.stored = append(r.stored, o)
	return nil
}

func (r *orderRecorder) Update(ctx context.Context, prev, next domain.Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.Orders.Update(ctx, prev, next); err != nil {
		return err
	}
	r.stored = append(r.stored, next)
	return nil
}

func (r *orderRecorder) versions() []domain.Order {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.stored)
}

// recordingStore is a store whose order writes are recorded.
type recordingStore struct {
	ports.Store
	orders *orderRecorder
}

func (s recordingStore) Orders() ports.Orders { return s.orders }

func orderKey(o domain.Order) string {
	return fmt.Sprintf("%s %s %s sends=%d upd=%s etoro=%d pos=%d %q", o.RequestID, o.Kind, o.State, o.Sends, o.UpdatedAt.Format(time.RFC3339Nano), o.EtoroOrderID, o.PositionID, o.Reason)
}

// publishedAsStored asserts that the bus carried exactly the order versions the service
// stored, in the same order (PLAN 6.6b).
func (x *execEnv) publishedAsStored(t *testing.T) {
	t.Helper()
	var stored, published []string
	for _, o := range x.orders.versions() {
		stored = append(stored, orderKey(o))
	}
	for _, e := range x.bus.OfType(BusOrder) {
		o, ok := e.Data.(domain.Order)
		if !ok || BusAccount(e) != x.a.ID || !e.At.Equal(o.UpdatedAt) {
			t.Fatalf("bus order event = %+v", e)
		}
		published = append(published, orderKey(o))
	}
	if len(stored) == 0 || !slices.Equal(stored, published) {
		t.Fatalf("stored transitions:\n%v\npublished:\n%v", stored, published)
	}
}

func TestPublishReconcileTransitions(t *testing.T) {
	x := newExecEnv(t)
	x.b.OpenScript = []fakes.SimOutcome{fakes.SimServerError}
	h := &hookBroker{SimBroker: x.b}
	h.lookup = func(context.Context, string, int64) (domain.OrderStatus, error) {
		return domain.OrderStatus{}, fmt.Errorf("down: %w", domain.ErrUnavailable)
	}
	x.svc.d.Broker = h
	o, _ := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
	if o.RequestID == "" || o.State.Final() {
		t.Fatalf("entry with lookups down = %+v", o)
	}
	x.svc.d.Broker = x.b
	before := len(x.bus.OfType(BusOrder))
	x.clock.Advance(lookupGiveUp)
	if _, err := x.svc.Reconcile(x.ctx, x.a.ID); err != nil {
		t.Fatal(err)
	}
	if o = x.order(t, o.RequestID); o.State != domain.OrderAbandoned {
		t.Fatalf("after reconcile = %+v", o)
	}
	if after := len(x.bus.OfType(BusOrder)); after <= before {
		t.Fatalf("reconcile published %d order events", after-before)
	}
	x.publishedAsStored(t)
}

func TestPublishAgentStateAndEvents(t *testing.T) {
	ae := newAgentEnv(t)
	ae.bus = &fakes.Bus{}
	ae.svc.d.Bus = ae.bus
	// Every event is on the bus only after it was written: the subscriber finds it stored.
	ch, cancel := ae.bus.Subscribe(100)
	defer cancel()
	if _, err := ae.svc.PauseAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.Kill(ae.ctx, ae.admin, ae.a.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.ResetKill(ae.ctx, ae.admin, ae.a.ID, ResetPhrase(ae.a.Name)); err != nil {
		t.Fatal(err)
	}
	var states []domain.AgentState
	for _, e := range ae.bus.OfType(BusAgentState) {
		st, ok := e.Data.(AgentStatus)
		if !ok || BusAccount(e) != ae.a.ID {
			t.Fatalf("agent.state = %+v", e)
		}
		states = append(states, st.State)
	}
	want := []domain.AgentState{domain.AgentPaused, domain.AgentStopped, domain.AgentRunning, domain.AgentKilled, domain.AgentStopped}
	if !slices.Equal(states, want) {
		t.Fatalf("states = %v, want %v", states, want)
	}
	stored, err := ae.store.Events().List(ae.ctx, []string{ae.a.ID}, 500)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for len(ch) > 0 {
		e := <-ch
		if e.Type != BusEvent {
			continue
		}
		ev := e.Data.(domain.Event)
		if BusAccount(e) != ae.a.ID || !slices.ContainsFunc(stored, func(x domain.Event) bool { return x.ID == ev.ID }) {
			t.Fatalf("published event %+v is not stored", ev)
		}
		n++
	}
	if n < 5 {
		t.Fatalf("%d events published", n)
	}
}

func TestPublishSupervisorReadAndKill(t *testing.T) {
	x := supEnv(t, agentStart)
	if _, err := x.svc.SetRiskLimits(x.ctx, x.admin, domain.DefaultRiskLimits); err != nil {
		t.Fatal(err)
	}
	x.supervise(t) // the high-water mark
	x.b.Cash /= 2  // half the equity gone: the drawdown limit trips the kill
	x.supervise(t)
	reads := x.bus.OfType(BusPortfolio)
	if len(reads) != 2 {
		t.Fatalf("portfolio events = %+v", reads)
	}
	if r := reads[1].Data.(PortfolioRead); r.AccountID != x.a.ID || r.Equity != x.b.Cash || !r.At.Equal(x.clock.Now()) {
		t.Fatalf("portfolio read = %+v", r)
	}
	states := x.bus.OfType(BusAgentState)
	if len(states) != 1 || states[0].Data.(AgentStatus).State != domain.AgentKilled {
		t.Fatalf("agent states = %+v", states)
	}
	// the supervisor's read is in the view cache
	if pf, at, ok := x.svc.rt.views.peek(x.a.ID); !ok || pf.AvailableCash != x.b.Cash || !at.Equal(x.clock.Now()) {
		t.Fatalf("cached = %+v %s %v", pf, at, ok)
	}
}

func TestPublishNeverBlocksAnOrder(t *testing.T) {
	x := newExecEnv(t)
	_, cancel := x.bus.Subscribe(1) // full after one event, never read
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a full subscriber blocked the order")
	}
	if x.b.Opens != 1 || x.bus.Dropped() == 0 {
		t.Fatalf("opens %d dropped %d", x.b.Opens, x.bus.Dropped())
	}
	x.publishedAsStored(t)
}

func TestPublishWithoutBus(t *testing.T) {
	x := newExecEnv(t)
	x.svc.d.Bus = nil
	before := len(x.bus.Events())
	if _, err := x.svc.placeEntry(x.ctx, x.a, x.entry(domain.Long), domain.DefaultRiskLimits); err != nil {
		t.Fatal(err)
	}
	if n := len(x.bus.Events()) - before; n != 0 {
		t.Fatalf("%d events without a bus", n)
	}
}

// TestSubscribeLive: with a bus the stream gets what is published until it cancels; without
// one it is told so and gets a harmless cancel.
func TestSubscribeLive(t *testing.T) {
	tests := []struct {
		name  string
		noBus bool
	}{
		{"bus", false},
		{"no bus", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newExecEnv(t)
			if tt.noBus {
				x.svc.d.Bus = nil
			}
			ch, cancel, ok := x.svc.SubscribeLive(4)
			if ok == tt.noBus {
				t.Fatalf("ok = %v", ok)
			}
			if tt.noBus {
				cancel()
				if ch != nil {
					t.Fatal("a channel without a bus")
				}
				return
			}
			x.svc.emitFor(x.ctx, x.a.ID, "", "test.kind", domain.SeverityInfo, "title", "")
			if e := <-ch; e.Type != BusEvent || BusAccount(e) != x.a.ID {
				t.Fatalf("event %+v", e)
			}
			cancel()
			if _, open := <-ch; open {
				t.Fatal("still subscribed after cancel")
			}
		})
	}
}

func TestBusAccount(t *testing.T) {
	tests := []struct {
		data any
		want string
	}{
		{domain.Event{AccountID: "a"}, "a"},
		{domain.Order{AccountID: "b"}, "b"},
		{AgentStatus{AccountID: "c"}, "c"},
		{PortfolioRead{AccountID: "d"}, "d"},
		{domain.Event{}, ""},
		{"something else", ""},
		{nil, ""},
	}
	for _, tt := range tests {
		if got := BusAccount(ports.Event{Data: tt.data}); got != tt.want {
			t.Fatalf("BusAccount(%#v) = %q, want %q", tt.data, got, tt.want)
		}
	}
}

func TestLiveFilter(t *testing.T) {
	x := newExecEnv(t)
	other := x.account(t, "Other", domain.ModeDemo)
	viewer := x.user(t, "vera", domain.RoleViewer)
	if err := x.svc.ShareAccount(x.ctx, x.admin, x.a.ID, "vera", domain.AccessView); err != nil {
		t.Fatal(err)
	}
	key := viewer
	key.Via, key.APIKeyID = ViaAPIKey, "k1"
	tests := []struct {
		name    string
		p       Principal
		want    []string
		wantErr error
	}{
		{"owner sees both", x.admin, []string{x.a.ID, other.ID}, nil},
		{"viewer sees the shared one only", viewer, []string{x.a.ID}, nil},
		{"API key of the viewer", key, []string{x.a.ID}, nil},
		{"no one", Principal{}, nil, domain.ErrUnauthenticated},
		{"default password", Principal{UserID: viewer.UserID, Role: domain.RoleViewer, MustChangePassword: true}, nil, domain.ErrPasswordChangeRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := x.svc.LiveFilter(x.ctx, tt.p)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("filter = %v, want %v", got, tt.want)
			}
			for _, id := range tt.want {
				if !got[id] {
					t.Fatalf("filter = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// countingBroker counts portfolio reads (and may slow them down).
type countingBroker struct {
	*fakes.SimBroker
	mu    sync.Mutex
	reads int
	delay time.Duration
}

func (c *countingBroker) Portfolio(ctx context.Context) (domain.Portfolio, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return c.SimBroker.Portfolio(ctx)
}

func (c *countingBroker) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads
}

func TestLiveSnapshot(t *testing.T) {
	ae := newAgentEnv(t)
	foreign := domain.Position{ID: 777, Instrument: instLSE, Amount: 300_00, RealAsset: true, Long: true, Leverage: 1, OpenedAt: ae.clock.Now()}
	ae.b.Positions = append(ae.b.Positions, foreign)
	if tr := ae.tick(t); tr.Entries != 1 {
		t.Fatalf("trace = %+v", tr)
	}
	if err := ae.store.Reviews().Upsert(ae.ctx, domain.Review{
		AccountID: ae.a.ID, Instrument: instNYSE, Symbol: "AAPL", At: ae.clock.Now(), Outcome: domain.ReviewCandidate,
		Probs: map[domain.Regime]float64{domain.RegimeTrending: 0.7, domain.RegimeChoppy: 0.2, domain.RegimeHighVol: 0.1},
	}); err != nil {
		t.Fatal(err)
	}
	cb := &countingBroker{SimBroker: ae.b}
	ae.svc.d.Broker = cb

	// Nothing cached yet: no figures, stale, and no eToro call.
	ae.svc.rt.views = newViewCache()
	snap, err := ae.svc.LiveSnapshot(ae.ctx, ae.admin, ae.a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.PortfolioKnown || !snap.Stale || len(snap.Positions) != 0 || cb.count() != 0 {
		t.Fatalf("empty snapshot = %+v, reads %d", snap, cb.count())
	}
	if snap.State != domain.AgentRunning || !snap.Reconciliation.OK || snap.Killed || !snap.Lease.Mine {
		t.Fatalf("snapshot state = %+v", snap)
	}
	if len(snap.OpenOrders) != 0 || len(snap.RecentOrders) != 1 || snap.RecentOrders[0].Symbol != "AAPL" {
		t.Fatalf("orders = %+v / %+v", snap.OpenOrders, snap.RecentOrders)
	}
	if len(snap.Regimes) != 1 || !snap.Regimes[0].Known || snap.Regimes[0].Regime != domain.RegimeTrending {
		t.Fatalf("regimes = %+v", snap.Regimes)
	}

	// After a supervisor pass the figures come from its read, still without a call.
	ae.supervise(t)
	reads := cb.count()
	snap, err = ae.svc.LiveSnapshot(ae.ctx, ae.admin, "")
	if err != nil {
		t.Fatal(err)
	}
	if cb.count() != reads {
		t.Fatalf("LiveSnapshot read the portfolio (%d reads)", cb.count()-reads)
	}
	if !snap.PortfolioKnown || snap.Stale || !snap.PortfolioAt.Equal(ae.clock.Now()) {
		t.Fatalf("freshness = %v %v %s", snap.PortfolioKnown, snap.Stale, snap.PortfolioAt)
	}
	act := ae.b.Actual()
	if snap.Figures.Equity != act.Equity() || snap.Figures.Cash != act.AvailableCash || snap.Figures.Budget != 10_000_00 || !snap.Figures.DayKnown {
		t.Fatalf("figures = %+v", snap.Figures)
	}
	if len(snap.Positions) != 2 {
		t.Fatalf("positions = %+v", snap.Positions)
	}
	for _, p := range snap.Positions {
		switch p.ID {
		case foreign.ID:
			if p.Ours || !p.FlattenAt.IsZero() || p.Symbol != "BARC.L" {
				t.Fatalf("foreign position = %+v", p)
			}
		default:
			if !p.Ours || p.OpenRequestID == "" || p.StopLossRate == 0 || p.Market != domain.MarketNYSE ||
				!p.FlattenAt.Equal(time.Date(2026, 11, 23, 20, 50, 0, 0, time.UTC)) {
				t.Fatalf("our position = %+v", p)
			}
		}
	}

	// 31 s later without a new read: stale.
	ae.clock.Advance(liveStaleAfter + time.Second)
	if snap, err = ae.svc.LiveSnapshot(ae.ctx, ae.admin, ""); err != nil || !snap.Stale || !snap.PortfolioKnown {
		t.Fatalf("stale snapshot = %v %v %v", snap.Stale, snap.PortfolioKnown, err)
	}
	if cb.count() != reads {
		t.Fatal("a stale snapshot read the portfolio")
	}
}

func TestLiveSnapshotAccess(t *testing.T) {
	x := newExecEnv(t)
	other := x.account(t, "Other", domain.ModeDemo)
	viewer := x.user(t, "vera", domain.RoleViewer)
	if err := x.svc.ShareAccount(x.ctx, x.admin, x.a.ID, "vera", domain.AccessView); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		p       Principal
		account string
		wantErr error
	}{
		{"viewer of a shared account", viewer, x.a.ID, nil},
		{"viewer of another account", viewer, other.ID, domain.ErrNotFound},
		{"signed out", Principal{}, x.a.ID, domain.ErrUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := x.svc.LiveSnapshot(x.ctx, tt.p, tt.account)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("LiveSnapshot = %v, want %v", err, tt.wantErr)
			}
			err = x.svc.WarmLiveView(x.ctx, tt.p, tt.account)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("WarmLiveView = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestLiveWarmView(t *testing.T) {
	x := newExecEnv(t)
	cb := &countingBroker{SimBroker: x.b}
	x.svc.d.Broker = cb
	for range 3 {
		if err := x.svc.WarmLiveView(x.ctx, x.admin, x.a.ID); err != nil {
			t.Fatal(err)
		}
	}
	if cb.count() != 1 {
		t.Fatalf("reads = %d, want 1", cb.count())
	}
	snap, err := x.svc.LiveSnapshot(x.ctx, x.admin, x.a.ID)
	if err != nil || !snap.PortfolioKnown || snap.Stale {
		t.Fatalf("snapshot = %+v %v", snap, err)
	}
	x.b.PortfolioErr = fmt.Errorf("HTTP 503: %w", domain.ErrUnavailable)
	x.clock.Advance(viewCacheTTL)
	if err := x.svc.WarmLiveView(x.ctx, x.admin, x.a.ID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("warm with eToro down = %v", err)
	}
	x.svc.d.Broker = nil
	if err := x.svc.WarmLiveView(x.ctx, x.admin, x.a.ID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("warm without a client = %v", err)
	}
}

func TestLiveMostLikely(t *testing.T) {
	tests := []struct {
		probs map[domain.Regime]float64
		want  domain.Regime
	}{
		{map[domain.Regime]float64{domain.RegimeChoppy: 0.6, domain.RegimeTrending: 0.4}, domain.RegimeChoppy},
		{map[domain.Regime]float64{domain.RegimeHighVol: 0.5, domain.RegimeTrending: 0.5}, domain.RegimeHighVol},
		{map[domain.Regime]float64{domain.RegimeTrending: 1}, domain.RegimeTrending},
	}
	for _, tt := range tests {
		if got := mostLikely(tt.probs); got != tt.want {
			t.Fatalf("mostLikely(%v) = %s, want %s", tt.probs, got, tt.want)
		}
	}
}
