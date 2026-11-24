package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// mutationCounter counts the broker calls that change an account (and the order follow-ups):
// reads such as a view's portfolio stay allowed for a real account and are not counted.
type mutationCounter struct {
	ports.Broker
	mu sync.Mutex
	n  int
}

func (c *mutationCounter) count() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *mutationCounter) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *mutationCounter) Open(ctx context.Context, o domain.OrderIntent) (domain.OrderStatus, error) {
	c.count()
	return c.Broker.Open(ctx, o)
}

func (c *mutationCounter) Cancel(ctx context.Context, orderID int64, id string) error {
	c.count()
	return c.Broker.Cancel(ctx, orderID, id)
}

func (c *mutationCounter) Close(ctx context.Context, pos int64, inst domain.InstrumentID, id string) (domain.OrderStatus, error) {
	c.count()
	return c.Broker.Close(ctx, pos, inst, id)
}

func (c *mutationCounter) Lookup(ctx context.Context, id string, orderID int64) (domain.OrderStatus, error) {
	c.count()
	return c.Broker.Lookup(ctx, id, orderID)
}

// realRefusalEnv is the worst case PLAN 7.17 asks for: a real-money eToro account with keys,
// reconciled, valid real limits with a budget, a shortlisted instrument, a gate armed through
// armLocked (as only a build with the owner's consent could), its agent forced to running,
// and the deployment opt-in variable set — while the demo agent is stopped, so every broker
// call counted below would be the real account's.
func realRefusalEnv(t *testing.T) (*agentEnv, domain.Account, *mutationCounter) {
	t.Helper()
	t.Setenv("CACTUSTRADE_REAL_TRADING", "on") // OD28's opt-in (7.23): nothing reads it yet
	ae := newAgentEnv(t)
	g := &gateEnv{execEnv: ae.execEnv, real: ae.account(t, "Live", domain.ModeReal)}
	if r, err := ae.svc.Reconcile(ae.ctx, g.real.ID); err != nil || !r.OK {
		t.Fatalf("reconcile real = %+v %v", r, err)
	}
	if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	l := domain.DefaultRealRiskLimits
	l.TradingBudget = 1_000_00
	g.setLimits(t, l)
	if err := ae.store.Watchlist().Add(ae.ctx, g.real.ID, instNYSE, "test", ae.clock.Now()); err != nil {
		t.Fatal(err)
	}
	g.arm(t)
	if err := ae.svc.setAgentState(ae.ctx, g.real.ID, domain.AgentRunning); err != nil {
		t.Fatal(err)
	}
	if !ae.svc.Reconciled(g.real.ID).OK || !ae.svc.hasKeys(ae.ctx, g.real) || !ae.svc.realGateArmed(ae.ctx, g.real) {
		t.Fatal("setup: the real account is not in the worst case")
	}
	cb := &mutationCounter{Broker: ae.b}
	ae.svc.d.Broker = cb
	ae.b.Violations = nil // the setup's portfolio reads (allowed for a real account)
	return ae, g.real, cb
}

// TestRealRefusalApp (PLAN 7.17): S5–S10 all still refuse with the gate armed.
func TestRealRefusalApp(t *testing.T) {
	ae, real, cb := realRefusalEnv(t)
	ctx := ae.ctx

	// S9: the agent tick and the agent loop place nothing.
	tr, err := ae.svc.AgentTick(ctx, real.ID)
	if err != nil || tr.Entries != 0 || !strings.Contains(tr.Skipped, "real mode") {
		t.Fatalf("S9 AgentTick = %+v %v", tr, err)
	}
	if why := ae.svc.agentGate(ctx, real); !strings.Contains(why, "real mode") {
		t.Fatalf("S9 agentGate = %q", why)
	}
	var wg sync.WaitGroup
	ae.svc.agentTickAll(ctx, &wg)
	wg.Wait()

	// S5: every mutation of the executor is refused before the broker.
	if _, err := ae.svc.placeEntry(ctx, real, ae.entry(domain.Long), domain.DefaultRealRiskLimits); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("S5 placeEntry = %v", err)
	}
	if _, err := ae.svc.placeEntry(ctx, real, ae.entry(domain.Short), domain.DefaultRealRiskLimits); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("S5 placeEntry short = %v", err)
	}
	ap := domain.AgentPosition{AccountID: real.ID, PositionID: 4242, OpenRequestID: "r-open", Instrument: instNYSE, Direction: domain.Long, OpenedAt: ae.clock.Now()}
	if err := ae.store.AgentPositions().Add(ctx, ap); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.closePosition(ctx, real, ap, "test", nil); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("S5 closePosition = %v", err)
	}
	pending, err := domain.NewOrder(domain.Order{
		RequestID: "r-pending", AccountID: real.ID, Mode: domain.ModeReal, Kind: domain.OrderOpen,
		Direction: domain.Long, Instrument: instNYSE, Amount: 100_00, Entry: 100_000_000, StopLoss: 97_000_000,
	}, ae.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	pending.State, pending.Sends, pending.EtoroOrderID = domain.OrderPending, 1, 700_001
	if err := ae.store.Orders().Create(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.cancelOrder(ctx, real, pending, "test"); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("S5 cancelOrder = %v", err)
	}
	if err := ae.svc.mayMutate(ctx, real); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("S5 mayMutate = %v", err)
	}

	// S6: no supervision of a real account.
	if ae.svc.supervisable(ctx, real) {
		t.Fatal("S6: a real account is supervisable")
	}
	rep, err := ae.svc.Supervise(ctx, real.ID)
	if err != nil || rep.Skipped == "" || rep.Closing != 0 || rep.Cancelled != 0 {
		t.Fatalf("S6 Supervise = %+v %v", rep, err)
	}

	// S7: the kill switch's flatten and the force close send nothing.
	var kr KillReport
	ae.svc.flatten(ctx, real, domain.ExitKilled, "test", &kr)
	if kr.Closing != 0 || kr.Cancelled != 0 {
		t.Fatalf("S7 flatten = %+v", kr)
	}
	if _, err := ae.svc.ForceClose(ctx, ae.admin, real.ID, adminPassword); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("S7 ForceClose = %v", err)
	}

	// S10: the controls are gated for everyone.
	v, err := ae.svc.AgentStatusView(ctx, ae.admin, real.ID)
	if err != nil || !v.Controls.Gated || v.Controls.Start || v.Controls.ForceClose {
		t.Fatalf("S10 controls = %+v %v", v.Controls, err)
	}

	// S8: start and the OD5 resume refuse (the agent was forced to running).
	if err := ae.svc.setAgentState(ctx, real.ID, domain.AgentStopped); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.StartAgent(ctx, ae.admin, real.ID); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("S8 StartAgent = %v", err)
	}
	if err := ae.svc.setAgentState(ctx, real.ID, domain.AgentRunning); err != nil {
		t.Fatal(err)
	}
	out, err := ae.svc.ResumeAgents(ctx)
	i := slices.IndexFunc(out, func(r ResumeOutcome) bool { return r.AccountID == real.ID })
	if err != nil || i < 0 || out[i].State != domain.AgentPaused || !strings.Contains(out[i].Reason, "real mode") {
		t.Fatalf("S8 ResumeAgents = %+v %v", out, err)
	}

	// The gate is still armed: none of the refusals above depended on it being locked.
	if !ae.svc.realGateArmed(ctx, real) {
		t.Fatal("the gate was disarmed during the matrix")
	}
	mutating := slices.DeleteFunc(slices.Clone(ae.b.Violations), func(v string) bool { return strings.HasPrefix(v, "portfolio ") })
	if cb.calls() != 0 || ae.b.Opens != 0 || ae.b.Closes != 0 || ae.b.Cancels != 0 || len(mutating) != 0 {
		t.Fatalf("%d broker calls (opens %d, closes %d, cancels %d, violations %v)", cb.calls(), ae.b.Opens, ae.b.Closes, ae.b.Cancels, ae.b.Violations)
	}
	if os, _ := ae.store.Orders().ListSince(ctx, real.ID, ae.clock.Now().AddDate(0, 0, -1)); len(os) != 1 || os[0].RequestID != "r-pending" {
		t.Fatalf("orders were stored for the real account: %+v", os)
	}

	// S4: the simulated broker refuses on its own.
	if _, err := ae.b.Open(ports.WithAccount(ctx, real), domain.OrderIntent{RequestID: "x", Direction: domain.Long, Instrument: instNYSE, Amount: 1}); !errors.Is(err, domain.ErrRealModeGated) {
		t.Fatalf("S4 SimBroker.Open = %v", err)
	}
	// The kill on an armed real account works (stored) and disarms (OD24), and sends nothing.
	if _, err := ae.svc.Kill(ctx, ae.admin, real.ID, "matrix"); err != nil {
		t.Fatal(err)
	}
	if ae.svc.realGateArmed(ctx, real) || cb.calls() != 0 {
		t.Fatalf("after the kill: armed %v, %d calls", ae.svc.realGateArmed(ctx, real), cb.calls())
	}
}
