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
)

// gateEnv is a reconciled demo account (x.a) and a reconciled real-money account with keys
// and real limits with a budget, both owned by the admin. The current account is the demo.
type gateEnv struct {
	*execEnv
	real domain.Account
}

func newGateEnv(t *testing.T) *gateEnv {
	t.Helper()
	x := newExecEnv(t)
	x.svc.d.Market = &agentMarket{clock: x.clock}
	g := &gateEnv{execEnv: x, real: x.account(t, "Live", domain.ModeReal)}
	// The test's real account stands in for the one the owner's consent covers (D39).
	consented := g.real.ID
	x.svc.consentFor = func(id string) (string, bool) { return "D39", id == consented }
	if r, err := x.svc.Reconcile(x.ctx, g.real.ID); err != nil || !r.OK {
		t.Fatalf("reconcile real = %+v %v", r, err)
	}
	l := domain.DefaultRealRiskLimits
	l.TradingBudget = 1_000_00
	g.setLimits(t, l)
	return g
}

// setLimits stores the real account's limits (it is current only meanwhile).
func (g *gateEnv) setLimits(t *testing.T, l domain.RiskLimits) {
	t.Helper()
	if _, err := g.svc.SetCurrentAccount(g.ctx, g.admin, g.real.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := g.svc.SetRiskLimits(g.ctx, g.admin, l); err != nil {
		t.Fatal(err)
	}
	if _, err := g.svc.SetCurrentAccount(g.ctx, g.admin, g.a.ID); err != nil {
		t.Fatal(err)
	}
}

// seedEvidence gives the demo account n qualifying risk days, the latest age days before
// today: a decision, a filled open and a position closed the same day on each.
func (g *gateEnv) seedEvidence(t *testing.T, demoID string, n, age int) {
	t.Helper()
	start := domain.RiskDayStart(g.clock.Now())
	for i := range n {
		at := start.AddDate(0, 0, -(age + i)).Add(17 * time.Hour) // 15:00 UTC on that risk day
		id := fmt.Sprintf("ev-%s-%d-%d", demoID, age, i)
		o, err := domain.NewOrder(domain.Order{
			RequestID: id, AccountID: demoID, Mode: domain.ModeDemo, Kind: domain.OrderOpen, Direction: domain.Long,
			Instrument: instNYSE, Amount: 100_00, Entry: 100_000_000, StopLoss: 97_000_000,
		}, at)
		if err != nil {
			t.Fatal(err)
		}
		if o, err = o.Transition(domain.OrderSent, at, ""); err != nil {
			t.Fatal(err)
		}
		if o, err = o.Transition(domain.OrderFilled, at, ""); err != nil {
			t.Fatal(err)
		}
		if err := g.store.Orders().Create(g.ctx, o); err != nil {
			t.Fatal(err)
		}
		d := domain.Decision{
			ID: "dec-" + id, AccountID: demoID, At: at, Signal: domain.DecisionSignal{Instrument: instNYSE, Direction: domain.Long},
			Risk: domain.DecisionRisk{Allowed: true, Size: 100_00}, Outcome: domain.DecisionOrdered, OrderRequestID: id,
		}
		if err := g.store.Decisions().Add(g.ctx, d); err != nil {
			t.Fatal(err)
		}
		if err := g.store.AgentPositions().Add(g.ctx, domain.AgentPosition{
			AccountID: demoID, PositionID: int64(1_000 + 100*age + i), OpenRequestID: id, Instrument: instNYSE,
			Direction: domain.Long, OpenedAt: at, ClosedAt: at.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// arm stores the real gate armed through armLocked, as only a build with the owner's consent
// could (PLAN 7.17's setup).
func (g *gateEnv) arm(t *testing.T) domain.RealGate {
	t.Helper()
	locks := g.svc.rt.agent(g.real.ID)
	locks.ctl.Lock()
	defer locks.ctl.Unlock()
	gate, err := g.svc.armLocked(g.ctx, g.admin, g.real, g.a.ID, domain.GateEvidence{Days: []domain.RiskDate{"2026-11-20"}, Count: 1, First: "2026-11-20", Last: "2026-11-20"})
	if err != nil || gate.State != domain.GateArmed {
		t.Fatalf("armLocked = %+v %v", gate, err)
	}
	if !g.svc.realGateArmed(g.ctx, g.real) {
		t.Fatal("the gate armed through armLocked is not in force")
	}
	return gate
}

func (g *gateEnv) gate(t *testing.T) domain.RealGate {
	t.Helper()
	gate, err := g.svc.storedGate(g.ctx, g.real.ID)
	if err != nil {
		t.Fatal(err)
	}
	return gate
}

func (g *gateEnv) audits(action string) []domain.AuditEntry {
	var out []domain.AuditEntry
	for _, e := range g.store.AuditEntries() {
		if e.Action == action {
			out = append(out, e)
		}
	}
	return out
}

func (g *gateEnv) shareReal(t *testing.T, name string, role domain.Role, access domain.AccountAccess) Principal {
	t.Helper()
	p := g.user(t, name, role)
	if err := g.svc.ShareAccount(g.ctx, g.admin, g.real.ID, name, access); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestArmRealRoleViaOwnerMatrix: only an admin who owns the real account, in a web session,
// gets as far as the checklist; there, with everything in order, the gate is armed (D39).
func TestArmRealRoleViaOwnerMatrix(t *testing.T) {
	tests := []struct {
		name string
		who  func(t *testing.T, g *gateEnv) Principal
		want error
	}{
		{"owner admin, web session", func(_ *testing.T, g *gateEnv) Principal { return g.admin }, nil},
		{"owner admin, API key", func(_ *testing.T, g *gateEnv) Principal { return apiKey(g.admin) }, domain.ErrForbidden},
		{"another admin with trade access", func(t *testing.T, g *gateEnv) Principal {
			return g.shareReal(t, "ad", domain.RoleAdmin, domain.AccessTrade)
		}, domain.ErrForbidden},
		{"another admin without access", func(t *testing.T, g *gateEnv) Principal { return g.user(t, "ad", domain.RoleAdmin) }, domain.ErrNotFound},
		{"operator with trade access", func(t *testing.T, g *gateEnv) Principal {
			return g.shareReal(t, "op", domain.RoleOperator, domain.AccessTrade)
		}, domain.ErrForbidden},
		{"viewer with view access", func(t *testing.T, g *gateEnv) Principal {
			return g.shareReal(t, "vi", domain.RoleViewer, domain.AccessView)
		}, domain.ErrForbidden},
		{"nobody", func(*testing.T, *gateEnv) Principal { return Principal{} }, domain.ErrUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGateEnv(t)
			g.seedEvidence(t, g.a.ID, 20, 1)
			p := tt.who(t, g)
			gate, err := g.svc.ArmReal(g.ctx, p, g.real.ID, g.a.ID, domain.ArmPhrase(g.real.Name), passwordOf(p))
			if tt.want == nil {
				if err != nil || gate.State != domain.GateArmed || !g.svc.realGateArmed(g.ctx, g.real) {
					t.Fatalf("arming = %+v %v", gate, err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if _, err := g.store.RealGates().Get(g.ctx, g.real.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("a gate row was stored: %v", err)
			}
			if g.svc.realGateArmed(g.ctx, g.real) {
				t.Fatal("armed")
			}
		})
	}
	// An operator who owns a real account cannot arm it either (mode.switch is admin only).
	g := newGateEnv(t)
	op := g.user(t, "op", domain.RoleOperator)
	v, err := g.svc.CreateAccount(g.ctx, op, "Op live", domain.ModeReal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.svc.ArmReal(g.ctx, op, v.ID, "", domain.ArmPhrase("Op live"), passwordOf(op)); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("operator owner: %v", err)
	}
}

func TestArmRealRefusals(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, g *gateEnv) (demoID, confirm, password string)
		want     error
		check    string // a failing check the refusal names ("" = none)
		auditHas string
	}{
		{"wrong phrase (422)", func(_ *testing.T, g *gateEnv) (string, string, string) {
			return g.a.ID, "ENABLE REAL TRADING live", adminPassword
		}, domain.ErrInvalid, "", "wrong phrase"},
		{"wrong password (403)", func(_ *testing.T, g *gateEnv) (string, string, string) {
			return g.a.ID, domain.ArmPhrase(g.real.Name), "not it"
		}, domain.ErrWrongPassword, "", "wrong password"},
		{"4 qualifying days (the default needs 5)", func(t *testing.T, g *gateEnv) (string, string, string) {
			g.seedEvidence(t, g.a.ID, 4, 1)
			return g.a.ID, domain.ArmPhrase(g.real.Name), adminPassword
		}, domain.ErrConflict, domain.CheckEvidence, "demo_evidence"},
		{"20 days, the latest 8 days old", func(t *testing.T, g *gateEnv) (string, string, string) {
			g.seedEvidence(t, g.a.ID, 20, 8)
			return g.a.ID, domain.ArmPhrase(g.real.Name), adminPassword
		}, domain.ErrConflict, domain.CheckEvidence, "demo_evidence"},
		{"held positions do not count", func(t *testing.T, g *gateEnv) (string, string, string) {
			g.seedEvidence(t, g.a.ID, 5, 1)
			ps, _ := g.store.AgentPositions().ListSince(g.ctx, g.a.ID, time.Time{})
			p := ps[len(ps)-1]
			p.ClosedAt = p.OpenedAt.Add(12 * time.Hour) // the next risk day
			if err := g.store.AgentPositions().Update(g.ctx, p); err != nil {
				t.Fatal(err)
			}
			return g.a.ID, domain.ArmPhrase(g.real.Name), adminPassword
		}, domain.ErrConflict, domain.CheckEvidence, "demo_evidence"},
		{"no demo account chosen", func(t *testing.T, g *gateEnv) (string, string, string) {
			g.seedEvidence(t, g.a.ID, 20, 1)
			return "", domain.ArmPhrase(g.real.Name), adminPassword
		}, domain.ErrConflict, domain.CheckDemoAccount, "demo_account"},
		{"another owner's demo account", func(t *testing.T, g *gateEnv) (string, string, string) {
			op := g.user(t, "op", domain.RoleOperator)
			v, err := g.svc.CreateAccount(g.ctx, op, "Op demo", domain.ModeDemo)
			if err != nil {
				t.Fatal(err)
			}
			if err := g.svc.ShareAccount(g.ctx, op, v.ID, "admin", domain.AccessView); err != nil {
				t.Fatal(err)
			}
			g.seedEvidence(t, v.ID, 20, 1)
			return v.ID, domain.ArmPhrase(g.real.Name), adminPassword
		}, domain.ErrConflict, domain.CheckDemoAccount, "demo_account"},
		{"agent running", func(t *testing.T, g *gateEnv) (string, string, string) {
			g.seedEvidence(t, g.a.ID, 20, 1)
			if err := g.svc.setAgentState(g.ctx, g.real.ID, domain.AgentPaused); err != nil {
				t.Fatal(err)
			}
			return g.a.ID, domain.ArmPhrase(g.real.Name), adminPassword
		}, domain.ErrConflict, domain.CheckAgentStopped, "real_agent_stopped"},
		{"limits over the real ceilings", func(t *testing.T, g *gateEnv) (string, string, string) {
			g.seedEvidence(t, g.a.ID, 20, 1)
			// stored before 7.11 or by hand: the checklist refuses them
			if err := g.store.Settings().Set(g.ctx, riskLimitsKey(g.real.ID), `{"tradingBudgetCents":100,"dailyLossBp":500,"drawdownBp":1000,"maxPositionBp":2000,"maxOpenPositions":8,"noEntryBeforeCloseS":1800,"flattenBeforeCloseS":600,"maxPortfolioAgeS":5,"maxQuoteAgeS":30}`, g.clock.Now()); err != nil {
				t.Fatal(err)
			}
			return g.a.ID, domain.ArmPhrase(g.real.Name), adminPassword
		}, domain.ErrConflict, domain.CheckRealLimits, "real_limits"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGateEnv(t)
			demoID, confirm, password := tt.setup(t, g)
			_, err := g.svc.ArmReal(g.ctx, g.admin, g.real.ID, demoID, confirm, password)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if tt.check != "" {
				gr, ok := errors.AsType[*domain.GateRefusal](err)
				if !ok || !slices.ContainsFunc(gr.Failed, func(c domain.GateCheck) bool { return c.Name == tt.check }) || !errors.Is(err, domain.ErrRealModeGated) {
					t.Fatalf("refusal %v does not name %s", err, tt.check)
				}
			}
			as := g.audits("real.arm_refused")
			if len(as) != 1 || !strings.Contains(as[0].Details, tt.auditHas) || as[0].Target != g.real.ID {
				t.Fatalf("audit = %+v", as)
			}
			if _, err := g.store.RealGates().Get(g.ctx, g.real.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("a gate row was stored: %v", err)
			}
		})
	}
	// A demo account is not a real-money account; an unknown one is not found.
	g := newGateEnv(t)
	if _, err := g.svc.ArmReal(g.ctx, g.admin, g.a.ID, g.a.ID, domain.ArmPhrase(g.a.Name), adminPassword); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("demo account: %v", err)
	}
	if _, err := g.svc.ArmReal(g.ctx, g.admin, "nope", g.a.ID, "x", adminPassword); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	if _, err := g.svc.ArmReal(g.ctx, g.admin, "", g.a.ID, "x", adminPassword); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("no account: %v", err)
	}
}

// TestArmRealArms (PLAN 7.19, D39): with every check passing, ArmReal stores the gate armed
// with the demo evidence, audits real.arm, raises the danger event, and the gate is in force.
// Nothing is sent to the broker: arming is not trading.
func TestArmRealArms(t *testing.T) {
	g := newGateEnv(t)
	g.seedEvidence(t, g.a.ID, 20, 1)
	gate, err := g.svc.ArmReal(g.ctx, g.admin, g.real.ID, g.a.ID, "  "+domain.ArmPhrase(g.real.Name)+" ", adminPassword)
	if err != nil || gate.State != domain.GateArmed || gate.DemoAccountID != g.a.ID || gate.ArmedBy != g.admin.UserID || gate.Evidence.Count != 20 {
		t.Fatalf("ArmReal = %+v %v", gate, err)
	}
	if !gate.ExpiresAt.Equal(g.clock.Now().UTC().Add(domain.RealGateTTL)) {
		t.Fatalf("expires %v", gate.ExpiresAt)
	}
	if !g.svc.realGateArmed(g.ctx, g.real) {
		t.Fatal("the gate is not in force")
	}
	if len(g.audits("real.arm")) != 1 || len(g.audits("real.arm_refused")) != 0 {
		t.Fatalf("audits = %+v", g.store.AuditEntries())
	}
	es, _ := g.store.Events().List(g.ctx, []string{g.real.ID}, 50)
	if !slices.ContainsFunc(es, func(e domain.Event) bool { return e.Kind == "real.armed" && e.Severity == domain.SeverityDanger }) {
		t.Fatalf("events = %+v", es)
	}
	if g.b.Opens+g.b.Closes+g.b.Cancels != 0 {
		t.Fatal("arming sent an order")
	}
	v, err := g.svc.RealGateStatus(g.ctx, g.admin, g.real.ID, "")
	if err != nil || !v.Armed || !v.Enabled {
		t.Fatalf("status after arming = %+v %v", v, err)
	}
}

func TestRealGateStatus(t *testing.T) {
	g := newGateEnv(t)
	g.seedEvidence(t, g.a.ID, 20, 1)
	other := g.account(t, "Other demo", domain.ModeDemo)
	v, err := g.svc.RealGateStatus(g.ctx, g.admin, g.real.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Armed || !v.Enabled || v.Gate.State != domain.GateLocked || v.Phrase != "ENABLE REAL TRADING Live" || !v.CanArm || !v.CanDisarm ||
		v.Rule != domain.DefaultEvidenceRule || len(v.DemoCandidates) != 2 || v.DemoAccountID != g.a.ID || v.Evidence.Count != 20 {
		t.Fatalf("status = %+v", v)
	}
	for _, c := range v.Checks {
		if c.Name == domain.CheckPhrase || !c.Pass {
			t.Fatalf("check %+v", c)
		}
	}
	// Another demo account chosen: no evidence there.
	v, err = g.svc.RealGateStatus(g.ctx, g.admin, g.real.ID, other.ID)
	if err != nil || v.DemoAccountID != other.ID || v.Evidence.Count != 0 {
		t.Fatalf("other demo = %+v %v", v, err)
	}
	// An unknown demo account: none chosen.
	v, err = g.svc.RealGateStatus(g.ctx, g.admin, g.real.ID, "nope")
	if err != nil || v.DemoAccountID != "" || slices.ContainsFunc(v.Checks, func(c domain.GateCheck) bool { return c.Name == domain.CheckDemoAccount && c.Pass }) {
		t.Fatalf("unknown demo = %+v %v", v, err)
	}
	// A viewer sees the checklist, cannot arm or disarm, and sees no demo account it cannot see.
	vi := g.shareReal(t, "vi", domain.RoleViewer, domain.AccessView)
	v, err = g.svc.RealGateStatus(g.ctx, vi, g.real.ID, "")
	if err != nil || v.CanArm || v.CanDisarm || len(v.DemoCandidates) != 0 || len(v.Checks) != 10 {
		t.Fatalf("viewer = %+v %v", v, err)
	}
	// An operator with trade access may disarm.
	op := g.shareReal(t, "op", domain.RoleOperator, domain.AccessTrade)
	if v, err := g.svc.RealGateStatus(g.ctx, apiKey(op), g.real.ID, ""); err != nil || v.CanArm || !v.CanDisarm {
		t.Fatalf("operator = %+v %v", v, err)
	}
	if _, err := g.svc.RealGateStatus(g.ctx, g.admin, g.a.ID, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("demo account: %v", err)
	}
	if _, err := g.svc.RealGateStatus(g.ctx, g.user(t, "xx", domain.RoleViewer), g.real.ID, ""); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unshared: %v", err)
	}
	// The gate armed with a demo account is the default choice.
	g.arm(t)
	if v, err := g.svc.RealGateStatus(g.ctx, g.admin, g.real.ID, ""); err != nil || !v.Armed || v.DemoAccountID != g.a.ID {
		t.Fatalf("armed = %+v %v", v, err)
	}
}

// TestDisarmReal (OD23): trade access and agent.kill, from a session or an API key;
// idempotent; the agent stops; nothing is cancelled or closed (a close in flight stays).
func TestDisarmReal(t *testing.T) {
	tests := []struct {
		name string
		who  func(t *testing.T, g *gateEnv) Principal
		want error
	}{
		{"owner admin, web session", func(_ *testing.T, g *gateEnv) Principal { return g.admin }, nil},
		{"owner admin, API key", func(_ *testing.T, g *gateEnv) Principal { return apiKey(g.admin) }, nil},
		{"operator with trade access, API key", func(t *testing.T, g *gateEnv) Principal {
			return apiKey(g.shareReal(t, "op", domain.RoleOperator, domain.AccessTrade))
		}, nil},
		{"operator with view access", func(t *testing.T, g *gateEnv) Principal {
			return g.shareReal(t, "op", domain.RoleOperator, domain.AccessView)
		}, domain.ErrForbidden},
		{"viewer with trade access", func(t *testing.T, g *gateEnv) Principal {
			return g.shareReal(t, "vi", domain.RoleViewer, domain.AccessTrade)
		}, domain.ErrForbidden},
		{"admin without access", func(t *testing.T, g *gateEnv) Principal { return g.user(t, "ad", domain.RoleAdmin) }, domain.ErrNotFound},
		{"nobody", func(*testing.T, *gateEnv) Principal { return Principal{} }, domain.ErrUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGateEnv(t)
			g.arm(t)
			// A close of ours in flight on the real account, and the agent marked running.
			cl, err := domain.NewOrder(domain.Order{
				RequestID: "close-1", AccountID: g.real.ID, Mode: domain.ModeReal, Kind: domain.OrderClose,
				Direction: domain.Long, Instrument: instNYSE, PositionID: 77,
			}, g.clock.Now())
			if err != nil {
				t.Fatal(err)
			}
			if cl, err = cl.Transition(domain.OrderSent, g.clock.Now(), ""); err != nil {
				t.Fatal(err)
			}
			if err := g.store.Orders().Create(g.ctx, cl); err != nil {
				t.Fatal(err)
			}
			if err := g.svc.setAgentState(g.ctx, g.real.ID, domain.AgentRunning); err != nil {
				t.Fatal(err)
			}
			cb := &callCounter{Broker: g.b}
			g.svc.d.Broker = cb
			g.clock.Advance(time.Minute)
			p := tt.who(t, g)
			got, err := g.svc.DisarmReal(g.ctx, p, g.real.ID, "  drill  ")
			if tt.want != nil {
				if !errors.Is(err, tt.want) || !g.svc.realGateArmed(g.ctx, g.real) {
					t.Fatalf("err = %v, want %v (armed %v)", err, tt.want, g.svc.realGateArmed(g.ctx, g.real))
				}
				return
			}
			if err != nil || got.State != domain.GateLocked || got.DisarmedBy != p.UserID || got.DisarmReason != "disarmed by "+p.Username+": drill" {
				t.Fatalf("disarm = %+v %v", got, err)
			}
			if g.svc.realGateArmed(g.ctx, g.real) || g.gate(t).State != domain.GateLocked {
				t.Fatal("still armed")
			}
			if st := g.svc.agentState(g.ctx, g.real.ID); st != domain.AgentStopped {
				t.Fatalf("agent %s", st)
			}
			if cb.calls() != 0 {
				t.Fatalf("%d broker calls", cb.calls())
			}
			if o := g.order(t, "close-1"); o.State != domain.OrderSent {
				t.Fatalf("the close in flight was touched: %+v", o)
			}
			if n := len(g.audits("real.disarm")); n != 1 {
				t.Fatalf("%d disarm audits", n)
			}
			// Idempotent: a second disarm changes nothing and announces nothing.
			again, err := g.svc.DisarmReal(g.ctx, p, g.real.ID, "")
			if err != nil || again.State != domain.GateLocked || again.Version != got.Version || len(g.audits("real.disarm")) != 1 {
				t.Fatalf("again = %+v %v", again, err)
			}
		})
	}
	g := newGateEnv(t)
	if _, err := g.svc.DisarmReal(g.ctx, g.admin, g.a.ID, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("demo account: %v", err)
	}
	// Never armed: 200, nothing stored.
	if got, err := g.svc.DisarmReal(g.ctx, g.admin, g.real.ID, ""); err != nil || got.State != domain.GateLocked || got.Version != 0 {
		t.Fatalf("never armed = %+v %v", got, err)
	}
	// A long reason is cut. (A disarm latches its time, so arm a moment later.)
	g.clock.Advance(time.Second)
	g.arm(t)
	g.clock.Advance(time.Second)
	if got, err := g.svc.DisarmReal(g.ctx, g.admin, g.real.ID, strings.Repeat("x", 1000)); err != nil || len(got.DisarmReason) > len("disarmed by admin: ")+maxDisarmReasonInput {
		t.Fatalf("long reason = %+v %v", got, err)
	}
}

// TestRealGateAutoDisarm (OD24): with a gate armed through armLocked, a kill, a key change, a
// raised limit, a restore and the expiry each disarm it; lowering a limit does not.
func TestRealGateAutoDisarm(t *testing.T) {
	tests := []struct {
		name  string
		event func(t *testing.T, g *gateEnv)
		why   string // "" = stays armed
	}{
		{"kill switch", func(t *testing.T, g *gateEnv) {
			if _, err := g.svc.Kill(g.ctx, apiKey(g.admin), g.real.ID, "drill"); err != nil {
				t.Fatal(err)
			}
		}, domain.DisarmKill},
		{"supervisor drawdown trip", func(t *testing.T, g *gateEnv) {
			if err := g.svc.trip(g.ctx, g.real, "drawdown"); err != nil {
				t.Fatal(err)
			}
		}, domain.DisarmKill},
		{"key change", func(t *testing.T, g *gateEnv) {
			if err := g.svc.SetAccountKeys(g.ctx, g.admin, g.real.ID, "new-api-key", ""); err != nil {
				t.Fatal(err)
			}
		}, domain.DisarmKeys},
		{"a limit raised", func(t *testing.T, g *gateEnv) {
			l := domain.DefaultRealRiskLimits
			l.TradingBudget = 1_000_00
			l.MaxOpenPositions++
			g.setLimits(t, l)
		}, domain.DisarmLimits},
		{"demo days required changed", func(t *testing.T, g *gateEnv) {
			if _, err := g.svc.SetDemoDaysRequired(g.ctx, g.admin, g.real.ID, 10); err != nil {
				t.Fatal(err)
			}
		}, domain.DisarmDays},
		{"a limit lowered", func(t *testing.T, g *gateEnv) {
			l := domain.DefaultRealRiskLimits
			l.TradingBudget = 500_00
			g.setLimits(t, l)
		}, ""},
		{"a restore after arming (sweep)", func(t *testing.T, g *gateEnv) {
			g.clock.Advance(time.Second)
			if err := g.store.Settings().Set(g.ctx, domain.SettingRestoreAt, g.clock.Now().UTC().Format(time.RFC3339Nano), g.clock.Now()); err != nil {
				t.Fatal(err)
			}
			if g.svc.realGateArmed(g.ctx, g.real) {
				t.Fatal("in force after a restore")
			}
			g.tick()
		}, domain.DisarmRestore},
		{"a restore before arming", func(t *testing.T, g *gateEnv) {
			if err := g.store.Settings().Set(g.ctx, domain.SettingRestoreAt, g.clock.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano), g.clock.Now()); err != nil {
				t.Fatal(err)
			}
			g.tick()
		}, ""},
		{"an unreadable restore time", func(t *testing.T, g *gateEnv) {
			if err := g.store.Settings().Set(g.ctx, domain.SettingRestoreAt, "yesterday", g.clock.Now()); err != nil {
				t.Fatal(err)
			}
			if _, err := g.svc.RealGateStatus(g.ctx, g.admin, g.real.ID, ""); err != nil {
				t.Fatal(err)
			}
		}, DisarmUnreadableRestore},
		{"expiry (sweep)", func(_ *testing.T, g *gateEnv) {
			g.clock.Advance(domain.RealGateTTL)
			g.tick()
		}, domain.DisarmExpired},
		{"expiry (status)", func(t *testing.T, g *gateEnv) {
			g.clock.Advance(domain.RealGateTTL + time.Hour)
			v, err := g.svc.RealGateStatus(g.ctx, apiKey(g.admin), g.real.ID, "")
			if err != nil || v.Armed || v.Gate.State != domain.GateLocked {
				t.Fatalf("status = %+v %v", v, err)
			}
		}, domain.DisarmExpired},
		{"one second before expiry", func(_ *testing.T, g *gateEnv) {
			g.clock.Advance(domain.RealGateTTL - time.Second)
			g.tick()
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newGateEnv(t)
			g.arm(t)
			tt.event(t, g)
			gate := g.gate(t)
			if tt.why == "" {
				if gate.State != domain.GateArmed || !g.svc.realGateArmed(g.ctx, g.real) {
					t.Fatalf("disarmed: %+v", gate)
				}
				return
			}
			if gate.State != domain.GateLocked || !strings.Contains(gate.DisarmReason, tt.why) || g.svc.realGateArmed(g.ctx, g.real) {
				t.Fatalf("gate = %+v, want disarmed for %q", gate, tt.why)
			}
			if as := g.audits("real.disarm"); len(as) != 1 || !strings.Contains(as[0].Details, tt.why) {
				t.Fatalf("audit = %+v", as)
			}
		})
	}
}

// TestRealGateDisarmLatched: a disarm that cannot be stored still takes the gate out of
// force in this process (fail closed).
func TestRealGateDisarmLatched(t *testing.T) {
	g := newGateEnv(t)
	g.arm(t)
	g.clock.Advance(time.Second)
	g.store.GatesErr = errors.New("database down")
	if _, err := g.svc.DisarmReal(g.ctx, g.admin, g.real.ID, "drill"); err == nil {
		t.Fatal("the failed write was not reported")
	}
	g.store.GatesErr = nil
	if g.gate(t).State != domain.GateArmed {
		t.Fatal("test setup: the write should have failed")
	}
	if g.svc.realGateArmed(g.ctx, g.real) {
		t.Fatal("a disarm that could not be stored left the gate in force")
	}
	if len(g.audits("real.disarm_failed")) != 1 {
		t.Fatal("the failed disarm is not audited")
	}
	// Armed again later (a build with consent): in force again.
	g.clock.Advance(time.Second)
	g.arm(t)
}

// TestRealGateBannerAndHealth: Status.RealArmed lists the armed accounts the caller can see
// (OD27); TradingHealth reports it per account.
func TestRealGateBannerAndHealth(t *testing.T) {
	g := newGateEnv(t)
	vi := g.shareReal(t, "vi", domain.RoleViewer, domain.AccessView)
	stranger := g.user(t, "st", domain.RoleAdmin)
	if st, err := g.svc.Status(g.ctx, g.admin); err != nil || len(st.RealArmed) != 0 {
		t.Fatalf("locked: %+v %v", st.RealArmed, err)
	}
	g.arm(t)
	for _, p := range []Principal{g.admin, vi} {
		st, err := g.svc.Status(g.ctx, p)
		if err != nil || len(st.RealArmed) != 1 || st.RealArmed[0] != (RealArmedAccount{ID: g.real.ID, Name: "Live"}) {
			t.Fatalf("%s: %+v %v", p.Username, st.RealArmed, err)
		}
	}
	if st, err := g.svc.Status(g.ctx, stranger); err != nil || len(st.RealArmed) != 0 {
		t.Fatalf("stranger: %+v %v", st.RealArmed, err)
	}
	h := g.svc.TradingHealth(g.ctx)
	for _, a := range h.Accounts {
		if a.RealGateArmed != (a.AccountID == g.real.ID) {
			t.Fatalf("health %+v", a)
		}
	}
	g.clock.Advance(time.Second)
	if _, err := g.svc.DisarmReal(g.ctx, g.admin, g.real.ID, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := g.svc.Status(g.ctx, vi); len(st.RealArmed) != 0 {
		t.Fatalf("after the disarm: %+v", st.RealArmed)
	}
}

// TestRealGateMail: arming and disarming e-mail every admin and the owner once, whatever
// their notification choices; nobody else.
func TestRealGateMail(t *testing.T) {
	g := newGateEnv(t)
	setMail := func(p Principal, email string) {
		u, err := g.store.Users().Get(g.ctx, p.UserID)
		if err != nil {
			t.Fatal(err)
		}
		u.Email = email
		if err := g.store.Users().Update(g.ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	other := g.user(t, "ad2", domain.RoleAdmin)
	op := g.shareReal(t, "op", domain.RoleOperator, domain.AccessTrade)
	plain := g.user(t, "plain", domain.RoleOperator)
	setMail(g.admin, "owner@example.test")
	setMail(other, "admin2@example.test")
	setMail(op, "op@example.test")
	setMail(plain, "plain@example.test")
	// The other admin asked for no alert e-mails: still told.
	if err := g.store.Settings().Set(g.ctx, notifyKey(other.UserID), `{"trades":false,"alerts":false}`, g.clock.Now()); err != nil {
		t.Fatal(err)
	}
	g.arm(t)
	g.clock.Advance(time.Second)
	if _, err := g.svc.DisarmReal(g.ctx, g.admin, g.real.ID, ""); err != nil {
		t.Fatal(err)
	}
	ms, err := g.store.Outbox().List(g.ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := map[string]int{}
	for _, m := range ms {
		if strings.Contains(m.Subject, "Live") {
			count[m.To]++
		}
	}
	want := map[string]int{"owner@example.test": 2, "admin2@example.test": 2, "op@example.test": 2}
	for k, v := range want {
		if count[k] != v {
			t.Fatalf("mails %v, want %v", count, want)
		}
	}
	if count["plain@example.test"] != 0 {
		t.Fatalf("a user without access was mailed: %v", count)
	}
}

// countingDecisions counts the decision-log reads.
type countingDecisions struct {
	ports.Store
	n    *int
	fail bool
}

func (c countingDecisions) Decisions() ports.Decisions {
	return countedDecisions{c.Store.Decisions(), c.n, c.fail}
}

type countedDecisions struct {
	ports.Decisions
	n    *int
	fail bool
}

func (c countedDecisions) List(ctx context.Context, accountID string, before time.Time, o domain.DecisionOutcome, limit int) ([]domain.Decision, error) {
	*c.n++
	if c.fail {
		return nil, errors.New("database down")
	}
	return c.Decisions.List(ctx, accountID, before, o, limit)
}

// TestDemoEvidenceReadsADayAtATime: the decisions are read one per risk day, at most the
// window plus one reads however many there are; read errors are returned.
func TestDemoEvidenceReadsADayAtATime(t *testing.T) {
	g := newGateEnv(t)
	g.seedEvidence(t, g.a.ID, 70, 1) // more days than the window
	for i := range 5 {               // and several decisions on one day
		if err := g.store.Decisions().Add(g.ctx, domain.Decision{
			ID: fmt.Sprintf("extra-%d", i), AccountID: g.a.ID, At: domain.RiskDayStart(g.clock.Now()).Add(-time.Duration(i+1) * time.Minute),
			Signal: domain.DecisionSignal{Instrument: instNYSE, Direction: domain.Long}, Outcome: domain.DecisionRejectedRisk,
		}); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	g.svc.d.Store = countingDecisions{Store: g.svc.d.Store, n: &n}
	ev, err := g.svc.demoEvidence(g.ctx, g.a.ID, g.clock.Now(), domain.DefaultEvidenceRule)
	if err != nil || ev.Count != 60 || ev.Last != domain.RiskDay(g.clock.Now().AddDate(0, 0, -1)) {
		t.Fatalf("evidence = %+v %v", ev, err)
	}
	if n > domain.DefaultEvidenceRule.WindowDays+1 {
		t.Fatalf("%d decision reads", n)
	}
	if ev, err := g.svc.demoEvidence(g.ctx, "nope", g.clock.Now(), domain.DefaultEvidenceRule); err != nil || ev.Count != 0 {
		t.Fatalf("unknown account: %+v %v", ev, err)
	}
	g.svc.d.Store = countingDecisions{Store: g.svc.d.Store, n: &n, fail: true}
	if _, err := g.svc.RealGateStatus(g.ctx, g.admin, g.real.ID, ""); err == nil {
		t.Fatal("a read error was ignored")
	}
}

// TestSetDemoDaysRequired (D36): the admin who owns the real account sets how many qualifying
// demo days arming needs, 1 up to the 60-day window (default 5, D37); the checklist and the view use it;
// anyone else, an API key or a number out of range is refused; every change is audited.
func TestSetDemoDaysRequired(t *testing.T) {
	g := newGateEnv(t)
	demo := g.a.ID
	g.seedEvidence(t, demo, 3, 1)
	v, err := g.svc.RealGateStatus(g.ctx, g.admin, g.real.ID, demo)
	if err != nil || v.Rule.MinDays != 5 || !v.CanSetDays || gateCheck(v, domain.CheckEvidence).Pass || !strings.Contains(gateCheck(v, domain.CheckEvidence).Detail, "5 needed") {
		t.Fatalf("default: rule %+v canSet %v evidence %+v %v", v.Rule, v.CanSetDays, gateCheck(v, domain.CheckEvidence), err)
	}
	v, err = g.svc.SetDemoDaysRequired(g.ctx, g.admin, g.real.ID, 2)
	if err != nil || v.Rule.MinDays != 2 {
		t.Fatalf("set 2: %+v %v", v.Rule, err)
	}
	v, _ = g.svc.RealGateStatus(g.ctx, g.admin, g.real.ID, demo)
	if c := gateCheck(v, domain.CheckEvidence); !c.Pass {
		t.Fatalf("3 days now pass a 2-day rule: %+v", c)
	}
	for _, n := range []int{0, 61, -1} {
		if _, err := g.svc.SetDemoDaysRequired(g.ctx, g.admin, g.real.ID, n); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%d days accepted: %v", n, err)
		}
	}
	op := g.user(t, "operator2", domain.RoleOperator)
	for name, p := range map[string]Principal{"api key": apiKey(g.admin), "not admin": op} {
		if _, err := g.svc.SetDemoDaysRequired(g.ctx, p, g.real.ID, 30); err == nil {
			t.Errorf("%s set the days", name)
		}
	}
	if _, err := g.svc.SetDemoDaysRequired(g.ctx, g.admin, g.a.ID, 30); err == nil {
		t.Error("a demo account got a demo-days rule")
	}
	if got, _ := g.svc.RealGateStatus(g.ctx, g.admin, g.real.ID, demo); got.Rule.MinDays != 2 {
		t.Fatalf("refused changes stored: %d", got.Rule.MinDays)
	}
	entries, err := g.store.Audit().List(g.ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(entries, func(a domain.AuditEntry) bool {
		return a.Action == "realgate.demo_days_changed" && a.Details == "old=5 new=2"
	}) {
		t.Fatal("the change is not audited")
	}
}

func gateCheck(v RealGateView, name string) domain.GateCheck {
	for _, c := range v.Checks {
		if c.Name == name {
			return c
		}
	}
	return domain.GateCheck{}
}
