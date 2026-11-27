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
)

// mutationCounter counts the Open calls that reach the broker (exits and reads are allowed on
// a real account and are not counted).
type mutationCounter struct {
	ports.Broker
	mu    sync.Mutex
	opens int
}

func (c *mutationCounter) openCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens
}

func (c *mutationCounter) Open(ctx context.Context, o domain.OrderIntent) (domain.OrderStatus, error) {
	c.mu.Lock()
	c.opens++
	c.mu.Unlock()
	return c.Broker.Open(ctx, o)
}

// realEnv is a real-money eToro account with keys, reconciled, valid real limits with a
// budget, a shortlisted instrument and its agent stopped, next to the demo account (whose
// agent is stopped, so every broker call counted would be the real account's). armed arms its
// gate through armLocked; optIn sets the deployment opt-in (OD28, Config.RealTrading).
func realEnv(t *testing.T, armed, optIn bool) (*agentEnv, *gateEnv, *mutationCounter) {
	t.Helper()
	ae := newAgentEnv(t)
	g := &gateEnv{execEnv: ae.execEnv, real: ae.account(t, "Live", domain.ModeReal)}
	consented := g.real.ID // stands in for the account the owner's consent covers (D39)
	ae.svc.consentFor = func(id string) (string, bool) { return "D39", id == consented }
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
	if armed {
		g.arm(t)
	}
	ae.svc.cfg.RealTrading = optIn
	// The real account's decisions are logged before its orders (PLAN 5.5).
	ae.b.OnOpen = nil
	cb := &mutationCounter{Broker: ae.b}
	ae.svc.d.Broker = cb
	return ae, g, cb
}

// TestRealRefusalApp (PLAN 7.17, converted by 7.22): S5 (entries), S8, S9 and S10 refuse a
// real-money entry whenever one of its keys is missing, however complete the rest is — the
// gate armed (through armLocked) but the deployment opt-in off; the opt-in on but the gate
// never armed; both but the account not reconciled; both but limits over the real ceilings;
// both but the gate expired or older than a restore. The agent is forced to running each
// time. Nothing reaches the broker's Open. Exits are the subject of TestRealExits*.
func TestRealRefusalApp(t *testing.T) {
	t.Setenv("CACTUSTRADE_REAL_TRADING", "on") // the app never reads the environment: only Config counts
	tests := []struct {
		name         string
		armed, optIn bool
		edit         func(t *testing.T, ae *agentEnv, g *gateEnv)
		why          string
	}{
		{"armed, opt-in off", true, false, nil, "CACTUSTRADE_REAL_TRADING"},
		{"opt-in on, never armed", false, true, nil, "not armed"},
		{"armed and opt-in, not reconciled", true, true, func(_ *testing.T, ae *agentEnv, g *gateEnv) {
			ae.svc.unreconcile(g.real.ID, "test")
		}, "not reconciled"},
		{"armed and opt-in, limits over the ceilings", true, true, func(t *testing.T, ae *agentEnv, g *gateEnv) {
			if err := ae.store.Settings().Set(ae.ctx, riskLimitsKey(g.real.ID), `{"tradingBudgetCents":100000,"dailyLossBp":500,"drawdownBp":1000,"maxPositionBp":2000,"maxOpenPositions":8,"noEntryBeforeCloseS":1800,"flattenBeforeCloseS":600,"maxPortfolioAgeS":5,"maxQuoteAgeS":30}`, ae.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}, "ceilings"},
		{"armed and opt-in, expired", true, true, func(_ *testing.T, ae *agentEnv, _ *gateEnv) {
			ae.clock.Advance(domain.RealGateTTL)
		}, "not armed"},
		{"armed and opt-in, restored since", true, true, func(t *testing.T, ae *agentEnv, _ *gateEnv) {
			if err := ae.store.Settings().Set(ae.ctx, domain.SettingRestoreAt, ae.clock.Now().UTC().Format(time.RFC3339Nano), ae.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}, "not armed"},
		{"armed and opt-in, disarmed in this process", true, true, func(_ *testing.T, ae *agentEnv, g *gateEnv) {
			ae.svc.gates.latch(g.real.ID, ae.clock.Now())
		}, "not armed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae, g, cb := realEnv(t, tt.armed, tt.optIn)
			real := g.real
			ctx := ae.ctx
			if tt.edit != nil {
				tt.edit(t, ae, g)
			}
			if err := ae.svc.setAgentState(ctx, real.ID, domain.AgentRunning); err != nil {
				t.Fatal(err)
			}
			// S9: the agent tick and the agent loop place nothing.
			tr, err := ae.svc.AgentTick(ctx, real.ID)
			if err != nil || tr.Entries != 0 || !strings.Contains(tr.Skipped, "real mode") || !strings.Contains(tr.Skipped, tt.why) {
				t.Fatalf("S9 AgentTick = %+v %v", tr, err)
			}
			var wg sync.WaitGroup
			ae.svc.agentTickAll(ctx, &wg)
			wg.Wait()
			// S5: every entry of the executor is refused before the broker.
			for _, d := range []domain.Direction{domain.Long, domain.Short} {
				if _, err := ae.svc.placeEntry(ctx, real, ae.entry(d), domain.DefaultRealRiskLimits); !errors.Is(err, domain.ErrRealModeGated) {
					t.Fatalf("S5 placeEntry %s = %v", d, err)
				}
			}
			if _, err := ae.svc.mayOpen(ctx, real); !errors.Is(err, domain.ErrRealModeGated) {
				t.Fatalf("S5 mayOpen = %v", err)
			}
			// S10: the controls are gated, with the reason.
			v, err := ae.svc.AgentStatusView(ctx, ae.admin, real.ID)
			if err != nil || !v.Controls.Gated || v.Controls.Start || !strings.Contains(v.StartBlocked, tt.why) || !v.Controls.Kill || !v.Controls.ForceClose {
				t.Fatalf("S10 controls = %+v (%q) %v", v.Controls, v.StartBlocked, err)
			}
			// S8: the OD5 resume never resumes a real agent, and start refuses.
			out, err := ae.svc.ResumeAgents(ctx)
			i := slices.IndexFunc(out, func(r ResumeOutcome) bool { return r.AccountID == real.ID })
			if err != nil || i < 0 || out[i].State != domain.AgentPaused || !strings.Contains(out[i].Reason, "real mode") {
				t.Fatalf("S8 ResumeAgents = %+v %v", out, err)
			}
			if err := ae.svc.setAgentState(ctx, real.ID, domain.AgentStopped); err != nil {
				t.Fatal(err)
			}
			if _, err := ae.svc.StartAgent(ctx, ae.admin, real.ID); !errors.Is(err, domain.ErrRealModeGated) {
				t.Fatalf("S8 StartAgent = %v", err)
			}
			// S4: the simulated broker refuses on its own without a permit.
			if _, err := ae.b.Open(ports.WithAccount(ctx, real), domain.OrderIntent{RequestID: "x", Direction: domain.Long, Instrument: instNYSE, Amount: 1}); !errors.Is(err, domain.ErrRealModeGated) {
				t.Fatalf("S4 SimBroker.Open = %v", err)
			}
			if cb.openCalls() != 0 {
				t.Fatalf("%d opens reached the broker", cb.openCalls())
			}
			os, _ := ae.store.Orders().ListSince(ctx, real.ID, ae.clock.Now().AddDate(0, 0, -40))
			if slices.ContainsFunc(os, func(o domain.Order) bool { return o.Kind == domain.OrderOpen && o.Sends > 0 }) {
				t.Fatalf("an open was sent for the real account: %+v", os)
			}
		})
	}
}
