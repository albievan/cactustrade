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

// realPosition records an open long of cactusTrade's on the real-money account a (an agent
// position) and puts it into the simulated broker's portfolio.
func realPosition(t *testing.T, x *execEnv, a domain.Account, id int64) domain.AgentPosition {
	t.Helper()
	ap := domain.AgentPosition{AccountID: a.ID, PositionID: id, OpenRequestID: "r-open-" + a.ID, Instrument: instNYSE, Direction: domain.Long, OpenedAt: x.clock.Now()}
	if err := x.store.AgentPositions().Add(x.ctx, ap); err != nil {
		t.Fatal(err)
	}
	x.b.Positions = append(x.b.Positions, domain.Position{
		ID: id, Instrument: instNYSE, Amount: 100_00, Units: 1_000_000, OpenRate: 100_000_000, Long: true, RealAsset: true, Leverage: 1,
		StopLossRate: 97_000_000,
	})
	return ap
}

// permitRecorder records the permit every account change carried (ports.PermitFrom) and the
// time it reached the broker.
type permitRecorder struct {
	ports.Broker
	clock *fakes.Clock
	mu    sync.Mutex
	opens []sentPermit
	exits []sentPermit
}

type sentPermit struct {
	account string
	permit  domain.RealPermit
	at      time.Time
}

func (r *permitRecorder) record(ctx context.Context, to *[]sentPermit) {
	a, _ := ports.AccountFrom(ctx)
	p, _ := ports.PermitFrom(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	*to = append(*to, sentPermit{account: a.ID, permit: p, at: r.clock.Now()})
}

func (r *permitRecorder) Open(ctx context.Context, o domain.OrderIntent) (domain.OrderStatus, error) {
	r.record(ctx, &r.opens)
	return r.Broker.Open(ctx, o)
}

func (r *permitRecorder) Close(ctx context.Context, pos int64, inst domain.InstrumentID, id string) (domain.OrderStatus, error) {
	r.record(ctx, &r.exits)
	return r.Broker.Close(ctx, pos, inst, id)
}

func (r *permitRecorder) Cancel(ctx context.Context, orderID int64, id string) error {
	r.record(ctx, &r.exits)
	return r.Broker.Cancel(ctx, orderID, id)
}

// realPendingOrder makes an open order of ours on the real account pending at the simulated
// broker (sent with an entry permit taken from a gate armed for the purpose) and stores it.
func realPendingOrder(t *testing.T, x *execEnv, a domain.Account) domain.Order {
	t.Helper()
	gate := domain.LockedGate(a.ID).Arm("demo", "u", domain.GateEvidence{}, x.clock.Now())
	p, err := domain.PermitEntry(gate, a, x.clock.Now(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	x.b.OpenScript = []fakes.SimOutcome{fakes.SimPending}
	hook := x.b.OnOpen
	x.b.OnOpen = nil
	id := x.svc.d.Random.ID()
	st, err := x.b.Open(ports.WithPermit(ports.WithAccount(x.ctx, a), p), domain.OrderIntent{
		RequestID: id, Direction: domain.Long, Instrument: instLSE, Amount: 50_00, StopLossRate: 97_000_000,
	})
	x.b.OnOpen = hook
	if err != nil {
		t.Fatal(err)
	}
	o, err := domain.NewOrder(domain.Order{
		RequestID: id, AccountID: a.ID, Mode: domain.ModeReal, Kind: domain.OrderOpen, Direction: domain.Long,
		Instrument: instLSE, Amount: 50_00, Entry: 100_000_000, StopLoss: 97_000_000,
	}, x.clock.Now().Add(-2*pendingMax))
	if err != nil {
		t.Fatal(err)
	}
	o.State, o.Sends, o.EtoroOrderID, o.SentAt = domain.OrderPending, 1, st.OrderID, x.clock.Now().Add(-2*pendingMax)
	if err := x.store.Orders().Create(x.ctx, o); err != nil {
		t.Fatal(err)
	}
	return o
}

// TestRealExitsWhateverTheGate (PLAN 7.21): on a real-money account cactusTrade's own position
// is closed at the session's close − 10 min by the supervisor, whatever the gate says — never
// armed, disarmed, expired — and whatever the deployment opt-in; every close carries an exit
// permit for the account and nothing is opened.
func TestRealExitsWhateverTheGate(t *testing.T) {
	tests := []struct {
		name         string
		armed, optIn bool
		edit         func(t *testing.T, ae *agentEnv, g *gateEnv)
	}{
		{"never armed, opt-in off", false, false, nil},
		{"armed, then disarmed", true, true, func(t *testing.T, ae *agentEnv, g *gateEnv) {
			if _, err := ae.svc.DisarmReal(ae.ctx, ae.admin, g.real.ID, "test"); err != nil {
				t.Fatal(err)
			}
		}},
		{"armed, opt-in off", true, false, nil},
		{"armed, expired by close", true, true, func(t *testing.T, ae *agentEnv, g *gateEnv) {
			gate := g.gate(t)
			gate.ExpiresAt = ae.clock.Now().Add(time.Minute)
			if _, err := ae.store.RealGates().Put(ae.ctx, gate); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae, g, _ := realEnv(t, tt.armed, tt.optIn)
			rec := &permitRecorder{Broker: ae.b, clock: ae.clock}
			ae.svc.d.Broker = rec
			ap := realPosition(t, ae.execEnv, g.real, 7001)
			if tt.edit != nil {
				tt.edit(t, ae, g)
			}
			// 15:00:05 → 20:50:05, NYSE closes at 21:00: the position is due.
			ae.clock.Advance(5*time.Hour + 50*time.Minute)
			if !ae.svc.supervisable(ae.ctx, g.real) {
				t.Fatal("a real account holding our position is not supervised")
			}
			var wg sync.WaitGroup
			ae.svc.superviseTick(ae.ctx, &wg)
			wg.Wait()
			got, err := ae.store.AgentPositions().Get(ae.ctx, g.real.ID, ap.PositionID)
			if err != nil || got.CloseRequestID == "" {
				t.Fatalf("the real position was not closed: %+v %v", got, err)
			}
			if ae.b.Closes != 1 || ae.b.Opens != 0 || len(rec.opens) != 0 {
				t.Fatalf("closes %d, opens %d", ae.b.Closes, ae.b.Opens)
			}
			for _, e := range rec.exits {
				if e.account != g.real.ID || !e.permit.AllowsExit(g.real.ID) {
					t.Fatalf("an exit without an exit permit: %+v", e)
				}
			}
			noViolations(t, ae.b)
		})
	}
}

// TestRealKillClosesAllOurs (PLAN 7.21): a kill on an armed real-money account cancels our
// pending order and closes every position of ours (never the owner's own, OD1), disarms the
// gate (OD24) and opens nothing; a force close on it after the disarm works the same way.
func TestRealKillClosesAllOurs(t *testing.T) {
	ae, g, cb := realEnv(t, true, true)
	realPosition(t, ae.execEnv, g.real, 7001)
	realPosition(t, ae.execEnv, g.real, 7002)
	ae.b.Positions = append(ae.b.Positions, domain.Position{ID: 7999, Instrument: instLSE, Amount: 10_00, Long: true, RealAsset: true, Leverage: 1})
	pending := realPendingOrder(t, ae.execEnv, g.real)
	opens := ae.b.Opens
	rep, err := ae.svc.Kill(ae.ctx, ae.admin, g.real.ID, "drill")
	if err != nil || rep.Closing != 2 || rep.Cancelled != 1 || len(rep.Errors) != 0 {
		t.Fatalf("kill = %+v %v", rep, err)
	}
	if ae.svc.realGateArmed(ae.ctx, g.real) {
		t.Fatal("the kill did not disarm the gate")
	}
	if !slices.ContainsFunc(ae.b.Positions, func(p domain.Position) bool { return p.ID == 7999 }) {
		t.Fatal("the owner's own position was closed")
	}
	if ae.b.Opens != opens || cb.openCalls() != 0 {
		t.Fatal("a kill opened something")
	}
	if ae.b.CancelRequests == nil || len(ae.b.CancelRequests) != 1 {
		t.Fatalf("cancels %v for %s", ae.b.CancelRequests, pending.RequestID)
	}
	noViolations(t, ae.b)
}

// TestRealSupervisable (PLAN 7.21): the supervisor covers a real-money account that is armed
// or holds positions or unfinished orders of ours; a locked one with nothing of ours is left
// alone, and one without keys is never covered.
func TestRealSupervisable(t *testing.T) {
	ae, g, _ := realEnv(t, false, false)
	if ae.svc.supervisable(ae.ctx, g.real) {
		t.Fatal("a locked real account with nothing of ours is supervised")
	}
	if rep, err := ae.svc.Supervise(ae.ctx, g.real.ID); err != nil || rep.Skipped == "" {
		t.Fatalf("Supervise = %+v %v", rep, err)
	}
	if !ae.svc.supervisable(ae.ctx, ae.a) {
		t.Fatal("the demo account is not supervised")
	}
	realPendingOrder(t, ae.execEnv, g.real)
	if !ae.svc.supervisable(ae.ctx, g.real) {
		t.Fatal("a real account with an unfinished order of ours is not supervised")
	}
	ae2, g2, _ := realEnv(t, true, false)
	if !ae2.svc.supervisable(ae2.ctx, g2.real) {
		t.Fatal("an armed real account is not supervised")
	}
	rep, err := ae2.svc.Supervise(ae2.ctx, g2.real.ID)
	if err != nil || rep.Skipped != "" {
		t.Fatalf("armed Supervise = %+v %v", rep, err)
	}
	noKeys := ae2.account(t, "No keys", domain.ModeReal)
	ak, uk := domain.AccountKeyNames(noKeys)
	_ = ae2.sec.Delete(ae2.ctx, ak)
	_ = ae2.sec.Delete(ae2.ctx, uk)
	if ae2.svc.supervisable(ae2.ctx, noKeys) {
		t.Fatal("a real account without keys is supervised")
	}
}

// TestRealEntryWithPermit (PLAN 7.22): with the gate armed, the deployment opt-in on, the
// account reconciled and real limits, the agent can be started on a real-money account and
// its tick places an entry whose Open carries a valid entry permit for the account; after a
// disarm no entry is made and the agent is stopped.
func TestRealEntryWithPermit(t *testing.T) {
	ae, g, _ := realEnv(t, true, true)
	ae.b.OnOpen = fakes.AllHooks(fakes.RequireOrderRow(ae.store.Orders()), fakes.RequireDecision(ae.store.Decisions(), g.real.ID))
	rec := &permitRecorder{Broker: ae.b, clock: ae.clock}
	ae.svc.d.Broker = rec
	v, err := ae.svc.AgentStatusView(ae.ctx, ae.admin, g.real.ID)
	if err != nil || v.Controls.Gated || !v.Controls.Start || v.StartBlocked != "" {
		t.Fatalf("controls = %+v %q %v", v.Controls, v.StartBlocked, err)
	}
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, g.real.ID); err != nil {
		t.Fatal(err)
	}
	tr, err := ae.svc.AgentTick(ae.ctx, g.real.ID)
	if err != nil || tr.Entries != 1 || tr.Skipped != "" {
		t.Fatalf("AgentTick = %+v %v", tr, err)
	}
	if len(rec.opens) != 1 {
		t.Fatalf("%d opens", len(rec.opens))
	}
	o := rec.opens[0]
	if o.account != g.real.ID || !o.permit.IsEntry() || !o.permit.AllowsEntry(g.real.ID, o.at) {
		t.Fatalf("the real open carried %+v", o)
	}
	os, _ := ae.store.Orders().List(ae.ctx, g.real.ID, 10)
	if len(os) != 1 || os[0].Mode != domain.ModeReal || os[0].Amount > 100_000 {
		t.Fatalf("orders %+v", os)
	}
	noViolations(t, ae.b)
	// A disarm: the agent is stopped, and a tick (forced to running) makes no entry.
	if _, err := ae.svc.DisarmReal(ae.ctx, ae.admin, g.real.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if st := ae.svc.agentState(ae.ctx, g.real.ID); st != domain.AgentStopped {
		t.Fatalf("agent %s after the disarm", st)
	}
	if err := ae.svc.setAgentState(ae.ctx, g.real.ID, domain.AgentRunning); err != nil {
		t.Fatal(err)
	}
	ae.setCand(longCand)
	if err := ae.store.Watchlist().Add(ae.ctx, g.real.ID, instLSE, "test", ae.clock.Now()); err != nil {
		t.Fatal(err)
	}
	tr, err = ae.svc.AgentTick(ae.ctx, g.real.ID)
	if err != nil || tr.Entries != 0 || !strings.Contains(tr.Skipped, "not armed") || len(rec.opens) != 1 {
		t.Fatalf("after the disarm: %+v %v (%d opens)", tr, err, len(rec.opens))
	}
}

// TestRealEntryWithRunningClock (review M1 of the lift): on a clock that moves on every read,
// as a real one does, an armed real account still makes its entry (the permit is issued at
// the decision's own time, never after it) and the open carries a valid permit.
func TestRealEntryWithRunningClock(t *testing.T) {
	ae, g, _ := realEnv(t, true, true)
	rec := &permitRecorder{Broker: ae.b, clock: ae.clock}
	ae.svc.d.Broker = rec
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, g.real.ID); err != nil {
		t.Fatal(err)
	}
	ae.clock.Step = 5 * time.Millisecond
	tr, err := ae.svc.AgentTick(ae.ctx, g.real.ID)
	if err != nil || tr.Entries != 1 || len(rec.opens) != 1 {
		t.Fatalf("AgentTick on a running clock = %+v %v (%d opens)", tr, err, len(rec.opens))
	}
	if o := rec.opens[0]; !o.permit.AllowsEntry(g.real.ID, o.at) {
		t.Fatalf("the real open carried %+v", o)
	}
	noViolations(t, ae.b)
}

// TestRealAgentsNeverAutoResume (OD26, PLAN 7.22): a real agent running before a restart comes
// back paused with an alert even when everything else would let it run; a demo agent in the
// same state resumes (OD5).
func TestRealAgentsNeverAutoResume(t *testing.T) {
	ae, g, _ := realEnv(t, true, true)
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, g.real.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	out, err := ae.svc.ResumeAgents(ae.ctx)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]ResumeOutcome{}
	for _, r := range out {
		by[r.AccountID] = r
	}
	if r := by[g.real.ID]; r.State != domain.AgentPaused || !strings.Contains(r.Reason, "OD26") {
		t.Fatalf("real resume = %+v", r)
	}
	if r := by[ae.a.ID]; r.State != domain.AgentRunning {
		t.Fatalf("demo resume = %+v", r)
	}
	if st := ae.svc.agentState(ae.ctx, g.real.ID); st != domain.AgentPaused {
		t.Fatalf("real agent %s", st)
	}
	es, _ := ae.store.Events().List(ae.ctx, []string{g.real.ID}, 50)
	if !slices.ContainsFunc(es, func(e domain.Event) bool {
		return e.Kind == "agent.resume_refused" && e.Severity == domain.SeverityWarning
	}) {
		t.Fatalf("no alert: %+v", es)
	}
}

// TestRealAgentStoredRunningDoesNotTrade (OD26, review M2 of the lift): a real agent whose
// stored state says "running" but that no person started in this process (a restart where
// writing "paused" failed, or the state could not be read) makes no entry and gets no permit,
// even with the gate armed, the opt-in on and the account reconciled; a person's Start (even
// with the state already "running") lets it trade; any move away from running clears it.
func TestRealAgentStoredRunningDoesNotTrade(t *testing.T) {
	ae, g, _ := realEnv(t, true, true)
	rec := &permitRecorder{Broker: ae.b, clock: ae.clock}
	ae.svc.d.Broker = rec
	if err := ae.svc.setAgentState(ae.ctx, g.real.ID, domain.AgentRunning); err != nil { // as left by an old process
		t.Fatal(err)
	}
	tr, err := ae.svc.AgentTick(ae.ctx, g.real.ID)
	if err != nil || tr.Entries != 0 || !strings.Contains(tr.Skipped, "OD26") || len(rec.opens) != 0 {
		t.Fatalf("stored running, not started here: %+v %v (%d opens)", tr, err, len(rec.opens))
	}
	if _, err := ae.svc.realEntryPermit(ae.ctx, g.real, ae.clock.Now()); !errors.Is(err, domain.ErrNoRealPermit) {
		t.Fatalf("a permit without a start: %v", err)
	}
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, g.real.ID); err != nil {
		t.Fatal(err)
	}
	if tr, err := ae.svc.AgentTick(ae.ctx, g.real.ID); err != nil || tr.Entries != 1 {
		t.Fatalf("after a person's start: %+v %v", tr, err)
	}
	if _, err := ae.svc.PauseAgent(ae.ctx, ae.admin, g.real.ID); err != nil {
		t.Fatal(err)
	}
	if ae.svc.rt.realStartedHere(g.real.ID) {
		t.Fatal("a pause left the real agent marked as started")
	}
}

// TestRealOnlyConsentedAccounts (D39 covers Real Money Maker only; review M3 of the lift): a
// real account the owner's consent does not cover cannot be armed, gets no entry permit and
// cannot start, even with everything else in place.
func TestRealOnlyConsentedAccounts(t *testing.T) {
	ae, g, _ := realEnv(t, true, true)
	ae.svc.consentFor = func(string) (string, bool) { return "", false }
	if why := ae.svc.realEntryBlocked(ae.ctx, g.real); !strings.Contains(why, "consent") {
		t.Fatalf("entry not blocked without consent: %q", why)
	}
	if _, err := ae.svc.realEntryPermit(ae.ctx, g.real, ae.clock.Now()); !errors.Is(err, domain.ErrNoRealPermit) {
		t.Fatalf("a permit without consent: %v", err)
	}
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, g.real.ID); err == nil {
		t.Fatal("started a real agent without consent")
	}
	v, err := ae.svc.RealGateStatus(ae.ctx, ae.admin, g.real.ID, "")
	if err != nil || slices.ContainsFunc(v.Checks, func(c domain.GateCheck) bool { return c.Name == domain.CheckConsent && c.Pass }) {
		t.Fatalf("consent check passes: %+v %v", v.Checks, err)
	}
}

// TestAnnounceRealTrading (PLAN 7.23, OD28): with the deployment opt-in on, start-up records a
// danger system event and an audit entry; with it off, nothing.
func TestAnnounceRealTrading(t *testing.T) {
	for _, on := range []bool{false, true} {
		x := newExecEnv(t)
		x.svc.cfg.RealTrading = on
		x.svc.AnnounceRealTrading(x.ctx)
		es, _ := x.store.Events().List(x.ctx, []string{x.a.ID}, 50)
		got := slices.ContainsFunc(es, func(e domain.Event) bool {
			return e.Kind == "system.real_trading_on" && e.Severity == domain.SeverityDanger && e.AccountID == ""
		})
		audited := slices.ContainsFunc(x.store.AuditEntries(), func(a domain.AuditEntry) bool { return a.Action == "system.real_trading_on" })
		if got != on || audited != on {
			t.Fatalf("opt-in %v: event %v, audit %v", on, got, audited)
		}
	}
}
