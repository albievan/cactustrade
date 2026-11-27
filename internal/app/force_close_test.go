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

// adminPassword is the env admin's password after newEnv changed it; a user made by env.user
// has name + " password!".
const adminPassword = "admin password 1"

func passwordOf(p Principal) string {
	if p.Username == "admin" {
		return adminPassword
	}
	return p.Username + " password!"
}

// callCounter counts every call to the broker, reads included.
type callCounter struct {
	ports.Broker
	mu sync.Mutex
	n  int
}

func (c *callCounter) count() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *callCounter) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *callCounter) Portfolio(ctx context.Context) (domain.Portfolio, error) {
	c.count()
	return c.Broker.Portfolio(ctx)
}

func (c *callCounter) Eligibility(ctx context.Context, ids []domain.InstrumentID) ([]domain.Eligibility, error) {
	c.count()
	return c.Broker.Eligibility(ctx, ids)
}

func (c *callCounter) Costs(ctx context.Context, id domain.InstrumentID, d domain.Direction, amount domain.Cents) ([]domain.CostItem, error) {
	c.count()
	return c.Broker.Costs(ctx, id, d, amount)
}

func (c *callCounter) TradeHistory(ctx context.Context, since time.Time) ([]domain.ClosedTrade, error) {
	c.count()
	return c.Broker.TradeHistory(ctx, since)
}

func (c *callCounter) Open(ctx context.Context, o domain.OrderIntent) (domain.OrderStatus, error) {
	c.count()
	return c.Broker.Open(ctx, o)
}

func (c *callCounter) Lookup(ctx context.Context, id string, orderID int64) (domain.OrderStatus, error) {
	c.count()
	return c.Broker.Lookup(ctx, id, orderID)
}

func (c *callCounter) Cancel(ctx context.Context, orderID int64, id string) error {
	c.count()
	return c.Broker.Cancel(ctx, orderID, id)
}

func (c *callCounter) Close(ctx context.Context, pos int64, inst domain.InstrumentID, id string) (domain.OrderStatus, error) {
	c.count()
	return c.Broker.Close(ctx, pos, inst, id)
}

func (c *callCounter) CloseOrder(ctx context.Context, orderID int64) (domain.OrderStatus, error) {
	c.count()
	return c.Broker.CloseOrder(ctx, orderID)
}

func TestForceCloseRoleViaAccessMatrix(t *testing.T) {
	tests := []struct {
		name string
		who  func(t *testing.T, ae *agentEnv) Principal
		want error // nil = allowed
	}{
		{"owner admin, web session", func(_ *testing.T, ae *agentEnv) Principal { return ae.admin }, nil},
		{"owner admin, API key", func(_ *testing.T, ae *agentEnv) Principal { return apiKey(ae.admin) }, domain.ErrForbidden},
		{"another admin with trade access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "ad", domain.RoleAdmin, domain.AccessTrade)
		}, nil},
		{"another admin with trade access, API key", func(t *testing.T, ae *agentEnv) Principal {
			return apiKey(ae.shared(t, "ad", domain.RoleAdmin, domain.AccessTrade))
		}, domain.ErrForbidden},
		{"admin with view access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "ad", domain.RoleAdmin, domain.AccessView)
		}, domain.ErrForbidden},
		{"admin without access", func(t *testing.T, ae *agentEnv) Principal { return ae.user(t, "ad", domain.RoleAdmin) }, domain.ErrNotFound},
		{"operator with trade access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "op", domain.RoleOperator, domain.AccessTrade)
		}, domain.ErrForbidden},
		{"viewer with trade access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "vi", domain.RoleViewer, domain.AccessTrade)
		}, domain.ErrForbidden},
		{"nobody", func(*testing.T, *agentEnv) Principal { return Principal{} }, domain.ErrUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			ae.openPosition(t, instNYSE, domain.Long)
			p := tt.who(t, ae)
			rep, err := ae.svc.ForceClose(ae.ctx, p, ae.a.ID, passwordOf(p))
			if tt.want != nil {
				if !errors.Is(err, tt.want) {
					t.Fatalf("err = %v, want %v", err, tt.want)
				}
				if ae.b.Closes != 0 || ae.svc.agentState(ae.ctx, ae.a.ID) != domain.AgentRunning {
					t.Fatalf("refused, but closes %d, state %s", ae.b.Closes, ae.svc.agentState(ae.ctx, ae.a.ID))
				}
				// the preview follows the same rules
				if _, err := ae.svc.ForceClosePreview(ae.ctx, p, ae.a.ID); !errors.Is(err, tt.want) {
					t.Fatalf("preview err = %v, want %v", err, tt.want)
				}
				return
			}
			if err != nil || rep.Closing != 1 || ae.b.Closes != 1 || rep.State != domain.AgentStopped {
				t.Fatalf("force close = %+v %v; closes %d", rep, err, ae.b.Closes)
			}
			// the controls the server shows agree with the use case
			v, err := ae.svc.AgentStatusView(ae.ctx, p, ae.a.ID)
			if err != nil || !v.Controls.ForceClose {
				t.Fatalf("controls = %+v %v", v.Controls, err)
			}
		})
	}
}

func TestForceCloseControlsFollowTheRules(t *testing.T) {
	ae := newAgentEnv(t)
	for _, tt := range []struct {
		name string
		p    Principal
		want bool
	}{
		{"admin, web session", ae.admin, true},
		{"admin, API key", apiKey(ae.admin), false},
		{"operator with trade access", ae.shared(t, "op", domain.RoleOperator, domain.AccessTrade), false},
	} {
		v, err := ae.svc.AgentStatusView(ae.ctx, tt.p, ae.a.ID)
		if err != nil || v.Controls.ForceClose != tt.want || v.Controls.ForceCloseBlocked != "" {
			t.Fatalf("%s: controls = %+v %v", tt.name, v.Controls, err)
		}
	}
}

func TestForceCloseWrongPassword(t *testing.T) {
	for _, pw := range []string{"", "wrong", strings.ToUpper(adminPassword)} {
		ae := newAgentEnv(t)
		ae.openPosition(t, instNYSE, domain.Long)
		cb := &callCounter{Broker: ae.b}
		ae.svc.d.Broker = cb
		_, err := ae.svc.ForceClose(ae.ctx, ae.admin, ae.a.ID, pw)
		if !errors.Is(err, domain.ErrWrongPassword) || !errors.Is(err, domain.ErrForbidden) {
			t.Fatalf("password %q: err = %v", pw, err)
		}
		if cb.calls() != 0 || ae.svc.agentState(ae.ctx, ae.a.ID) != domain.AgentRunning {
			t.Fatalf("password %q: %d broker calls, state %s", pw, cb.calls(), ae.svc.agentState(ae.ctx, ae.a.ID))
		}
		if !slices.ContainsFunc(ae.store.AuditEntries(), func(e domain.AuditEntry) bool { return e.Action == "agent.force_close_refused" }) {
			t.Fatal("a wrong password is not audited")
		}
		if _, err := ae.svc.ForceCloseAll(ae.ctx, ae.admin, pw); !errors.Is(err, domain.ErrWrongPassword) || cb.calls() != 0 {
			t.Fatalf("all accounts, password %q: %v, %d calls", pw, err, cb.calls())
		}
	}
}

// foreignPosition puts a position at the broker that cactusTrade did not open (OD1).
func (ae *agentEnv) foreignPosition() domain.Position {
	p := domain.Position{ID: 4242, Instrument: 999, Amount: 100_00, OpenRate: 1_000_000, StopLossRate: 900_000, RealAsset: true, Long: true, Leverage: 1}
	ae.b.Positions = append(ae.b.Positions, p)
	return p
}

func TestForceCloseDemoAccount(t *testing.T) {
	ae := newAgentEnv(t)
	if _, err := ae.svc.SetMyProfile(ae.ctx, ae.admin, Profile{Email: "admin@example.com", Notify: domain.NotifyPrefs{domain.NotifyAlerts: true}}, adminPassword); err != nil {
		t.Fatal(err)
	}
	for _, inst := range []domain.InstrumentID{instNYSE, instLSE, instASX} {
		ae.openPosition(t, inst, domain.Long)
	}
	foreign := ae.foreignPosition()
	ae.pendingAtBroker(t, 1)
	opens := ae.b.Opens

	plan, err := ae.svc.ForceClosePreview(ae.ctx, ae.admin, ae.a.ID)
	if err != nil || plan.Positions != 3 || plan.Orders != 1 || plan.Summary != "closes 3 positions and cancels 1 order on Demo" {
		t.Fatalf("preview = %+v %v", plan, err)
	}
	rep, err := ae.svc.ForceClose(ae.ctx, ae.admin, ae.a.ID, adminPassword)
	if err != nil || len(rep.Errors) != 0 {
		t.Fatalf("force close = %+v %v", rep, err)
	}
	if rep.Closing != 3 || rep.Cancelled != 1 || ae.b.Closes != 3 || ae.b.Cancels != 1 || ae.b.Opens != opens {
		t.Fatalf("report %+v; broker closes %d cancels %d opens +%d", rep, ae.b.Closes, ae.b.Cancels, ae.b.Opens-opens)
	}
	if len(ae.b.Positions) != 1 || ae.b.Positions[0].ID != foreign.ID {
		t.Fatalf("positions left %+v, want only the owner's own", ae.b.Positions)
	}
	// stopped, not killed: no latch, no stored kill, no reset needed
	if st := ae.svc.agentState(ae.ctx, ae.a.ID); st != domain.AgentStopped || rep.State != domain.AgentStopped {
		t.Fatalf("state %s / %s", st, rep.State)
	}
	if killed, err := ae.svc.isKilled(ae.ctx, ae.a.ID); killed || err != nil || ae.svc.rt.killLatched(ae.a.ID) {
		t.Fatalf("killed %v %v latched %v", killed, err, ae.svc.rt.killLatched(ae.a.ID))
	}
	if !slices.ContainsFunc(ae.store.AuditEntries(), func(e domain.AuditEntry) bool {
		return e.Action == "agent.force_close" && e.Target == ae.a.ID && strings.Contains(e.Details, "3 closes, 1 cancel")
	}) {
		t.Fatalf("not audited: %+v", ae.store.AuditEntries())
	}
	es, _ := ae.store.Events().List(ae.ctx, []string{ae.a.ID}, 100)
	if !slices.ContainsFunc(es, func(e domain.Event) bool {
		return e.Kind == "agent.force_closed" && e.Severity == domain.SeverityDanger
	}) {
		t.Fatalf("no danger event: %v", ae.eventKinds(t))
	}
	mail, err := ae.svc.Outbox(ae.ctx, ae.admin, 0)
	if err != nil || !slices.ContainsFunc(mail, func(m domain.OutboxMail) bool { return strings.Contains(m.Subject, "Force close on Demo") }) {
		t.Fatalf("no alert e-mail: %+v %v", mail, err)
	}
	// the limits can change at once, and the agent starts again without an admin reset
	ae.supervise(t) // follows the closes up
	ae.limits(t, func(l *domain.RiskLimits) { l.TradingBudget = 9_000_00 })
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatalf("start after a force close: %v", err)
	}
	noViolations(t, ae.b)
}

func TestForceCloseKeepsAKilledAgentKilled(t *testing.T) {
	ae := newAgentEnv(t)
	ae.openPosition(t, instNYSE, domain.Long)
	if err := ae.svc.trip(ae.ctx, ae.a, "test"); err != nil {
		t.Fatal(err)
	}
	rep, err := ae.svc.ForceClose(ae.ctx, ae.admin, ae.a.ID, adminPassword)
	if err != nil || rep.State != domain.AgentKilled || ae.svc.agentState(ae.ctx, ae.a.ID) != domain.AgentKilled {
		t.Fatalf("force close while killed = %+v %v", rep, err)
	}
	if !ae.svc.rt.killLatched(ae.a.ID) {
		t.Fatal("the kill latch was cleared")
	}
}

// TestForceCloseRealAccount (PLAN 7.21, was TestForceCloseRealAccountRefused): a force close
// works on a real-money account whatever its gate (here: never armed, the deployment opt-in
// off): cactusTrade's own position there is closed with an exit permit, nothing is opened, and
// the all-accounts variant covers the real account too.
func TestForceCloseRealAccount(t *testing.T) {
	ae := newAgentEnv(t)
	realAcct := ae.account(t, "Real", domain.ModeReal)
	ap := realPosition(t, ae.execEnv, realAcct, 9001)
	cb := &callCounter{Broker: ae.b}
	ae.svc.d.Broker = cb
	v, err := ae.svc.AgentStatusView(ae.ctx, ae.admin, realAcct.ID)
	if err != nil || !v.Controls.ForceClose || v.Controls.ForceCloseBlocked != "" || !v.Controls.Gated || v.Controls.Start {
		t.Fatalf("controls = %+v %v", v.Controls, err)
	}
	if pl, err := ae.svc.ForceClosePreview(ae.ctx, ae.admin, realAcct.ID); err != nil || pl.Positions != 1 {
		t.Fatalf("preview = %+v %v", pl, err)
	}
	rep, err := ae.svc.ForceClose(ae.ctx, ae.admin, realAcct.ID, adminPassword)
	if err != nil || rep.Closing != 1 || len(rep.Errors) != 0 {
		t.Fatalf("force close = %+v %v", rep, err)
	}
	if got, _ := ae.store.AgentPositions().Get(ae.ctx, realAcct.ID, ap.PositionID); got.CloseRequestID == "" {
		t.Fatalf("the real position was not closed: %+v", got)
	}
	if ae.b.Opens != 0 || ae.b.Closes != 1 {
		t.Fatalf("opens %d, closes %d", ae.b.Opens, ae.b.Closes)
	}
	noViolations(t, ae.b)
	// the all-accounts variant covers the real account as well
	plans, err := ae.svc.ForceCloseAllPreview(ae.ctx, ae.admin)
	if err != nil || len(plans) != 2 {
		t.Fatalf("all preview = %+v %v", plans, err)
	}
	reps, err := ae.svc.ForceCloseAll(ae.ctx, ae.admin, adminPassword)
	if err != nil || len(reps) != 2 {
		t.Fatalf("all = %+v %v", reps, err)
	}
}

func TestForceCloseAllAccounts(t *testing.T) {
	ae := newAgentEnv(t)
	ae.openPosition(t, instNYSE, domain.Long)
	second := ae.account(t, "Second", domain.ModeDemo)
	// an account of another admin, not shared: never touched
	other := ae.user(t, "ad", domain.RoleAdmin)
	theirs, err := ae.svc.CreateAccount(ae.ctx, other, "Theirs", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{second.ID, theirs.ID} {
		if err := ae.store.Settings().Set(ae.ctx, agentStateKey(id), string(domain.AgentRunning), ae.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name string
		p    Principal
		want error
	}{
		{"API key", apiKey(ae.admin), domain.ErrForbidden},
		{"operator", ae.user(t, "op", domain.RoleOperator), domain.ErrForbidden},
		{"nobody", Principal{}, domain.ErrUnauthenticated},
	}
	for _, tt := range tests {
		if _, err := ae.svc.ForceCloseAll(ae.ctx, tt.p, passwordOf(tt.p)); !errors.Is(err, tt.want) {
			t.Fatalf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
		if _, err := ae.svc.ForceCloseAllPreview(ae.ctx, tt.p); !errors.Is(err, tt.want) {
			t.Fatalf("%s preview: err = %v, want %v", tt.name, err, tt.want)
		}
	}
	plans, err := ae.svc.ForceCloseAllPreview(ae.ctx, ae.admin)
	if err != nil || len(plans) != 2 || plans[0].Summary != "closes 1 position on Demo" || plans[1].Summary != "nothing of cactusTrade's is open on Second" {
		t.Fatalf("preview = %+v %v", plans, err)
	}
	reps, err := ae.svc.ForceCloseAll(ae.ctx, ae.admin, adminPassword)
	if err != nil || len(reps) != 2 || reps[0].Closing != 1 || ae.b.Closes != 1 {
		t.Fatalf("all = %+v %v", reps, err)
	}
	for _, id := range []string{ae.a.ID, second.ID} {
		if st := ae.svc.agentState(ae.ctx, id); st != domain.AgentStopped {
			t.Fatalf("%s is %s", id, st)
		}
	}
	if st := ae.svc.agentState(ae.ctx, theirs.ID); st != domain.AgentRunning {
		t.Fatalf("another admin's unshared account was stopped: %s", st)
	}
	if n := len(slices.DeleteFunc(ae.store.AuditEntries(), func(e domain.AuditEntry) bool { return e.Action != "agent.force_close" })); n != 2 {
		t.Fatalf("%d force-close audit entries, want one per account", n)
	}
}

func TestForceCloseConcurrentWithKillAndLimits(t *testing.T) {
	ae := newAgentEnv(t)
	for i := range 30 {
		ae.setAgent(t, domain.AgentRunning)
		var wg sync.WaitGroup
		gate := make(chan struct{})
		var fcErr, killErr error
		wg.Go(func() {
			<-gate
			_, fcErr = ae.svc.ForceClose(ae.ctx, ae.admin, ae.a.ID, adminPassword)
		})
		wg.Go(func() {
			<-gate
			_, killErr = ae.svc.Kill(ae.ctx, ae.admin, ae.a.ID, "race")
		})
		wg.Go(func() {
			<-gate
			l := domain.DefaultRiskLimits
			l.TradingBudget = domain.Cents(1_000_00 + i)
			_, _ = ae.svc.SetRiskLimits(ae.ctx, ae.admin, l) // refused or not, never stuck
		})
		wg.Go(func() {
			<-gate
			_, _ = ae.svc.AgentTick(ae.ctx, ae.a.ID)
		})
		close(gate)
		finished := make(chan struct{})
		go func() { wg.Wait(); close(finished) }()
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			t.Fatalf("round %d: deadlock: force close, kill, limits and a tick did not finish", i)
		}
		if fcErr != nil || killErr != nil {
			t.Fatalf("round %d: force close %v, kill %v", i, fcErr, killErr)
		}
		// a kill always wins: a force close never overwrites the killed state
		if st := ae.svc.agentState(ae.ctx, ae.a.ID); st != domain.AgentKilled {
			t.Fatalf("round %d: state %s after a kill", i, st)
		}
		if _, err := ae.svc.ResetKill(ae.ctx, ae.admin, ae.a.ID, ResetPhrase(ae.a.Name)); err != nil {
			t.Fatalf("round %d: reset: %v", i, err)
		}
	}
	noViolations(t, ae.b)
}
