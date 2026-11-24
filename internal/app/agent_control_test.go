package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

func apiKey(p Principal) Principal {
	p.Via, p.APIKeyID = ViaAPIKey, "key-"+p.Username
	return p
}

func TestAgentControlRoleAccessMatrix(t *testing.T) {
	type action string
	const (
		start  action = "start"
		pause  action = "pause"
		stop   action = "stop"
		kill   action = "kill"
		reset  action = "reset"
		limits action = "limits"
	)
	all := []action{start, pause, stop, kill, reset}
	tests := []struct {
		name    string
		who     func(t *testing.T, ae *agentEnv) Principal
		allowed []action
	}{
		{"owner admin, web session", func(_ *testing.T, ae *agentEnv) Principal { return ae.admin }, all},
		{"owner admin, API key (OD11)", func(_ *testing.T, ae *agentEnv) Principal { return apiKey(ae.admin) }, []action{pause, stop, kill}},
		{"operator with trade access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "op", domain.RoleOperator, domain.AccessTrade)
		}, []action{start, pause, stop, kill}},
		{"operator with trade access, API key", func(t *testing.T, ae *agentEnv) Principal {
			return apiKey(ae.shared(t, "op", domain.RoleOperator, domain.AccessTrade))
		}, []action{pause, stop, kill}},
		{"operator with view access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "op", domain.RoleOperator, domain.AccessView)
		}, nil},
		{"viewer with trade access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "vi", domain.RoleViewer, domain.AccessTrade)
		}, nil},
		{"another admin with trade access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "ad", domain.RoleAdmin, domain.AccessTrade)
		}, all},
		{"operator without access", func(t *testing.T, ae *agentEnv) Principal { return ae.user(t, "op", domain.RoleOperator) }, nil},
		{"nobody", func(*testing.T, *agentEnv) Principal { return Principal{} }, nil},
	}
	for _, tt := range tests {
		for _, act := range append(slices.Clone(all), limits) {
			t.Run(tt.name+"/"+string(act), func(t *testing.T) {
				ae := newAgentEnv(t)
				p := tt.who(t, ae)
				var err error
				switch act {
				case start:
					ae.setAgent(t, domain.AgentStopped)
					_, err = ae.svc.StartAgent(ae.ctx, p, ae.a.ID)
				case pause:
					_, err = ae.svc.PauseAgent(ae.ctx, p, ae.a.ID)
				case stop:
					_, err = ae.svc.StopAgent(ae.ctx, p, ae.a.ID)
				case kill:
					_, err = ae.svc.Kill(ae.ctx, p, ae.a.ID, "test")
				case reset:
					if err := ae.svc.trip(ae.ctx, ae.a, "test"); err != nil {
						t.Fatal(err)
					}
					_, err = ae.svc.ResetKill(ae.ctx, p, ae.a.ID, ResetPhrase(ae.a.Name))
				case limits:
					// only the owner changes limits, and only while stopped
					_, err = ae.svc.SetRiskLimits(ae.ctx, p, domain.DefaultRiskLimits)
					if !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrForbidden) && !errors.Is(err, domain.ErrNotFound) &&
						!errors.Is(err, domain.ErrNoAccount) && !errors.Is(err, domain.ErrUnauthenticated) {
						t.Fatalf("limits while running: %v", err)
					}
					return
				}
				want := slices.Contains(tt.allowed, act)
				switch {
				case want && err != nil:
					t.Fatalf("refused: %v", err)
				case !want && err == nil:
					t.Fatal("allowed")
				case !want && !errors.Is(err, domain.ErrForbidden) && !errors.Is(err, domain.ErrNotFound) && !errors.Is(err, domain.ErrUnauthenticated):
					t.Fatalf("refused with %v, want forbidden / not found", err)
				}
			})
		}
	}
}

// shared creates a user with role and shares the account with them.
func (ae *agentEnv) shared(t *testing.T, name string, role domain.Role, access domain.AccountAccess) Principal {
	t.Helper()
	p := ae.user(t, name, role)
	if err := ae.svc.ShareAccount(ae.ctx, ae.admin, ae.a.ID, name, access); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestStartAgentRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, ae *agentEnv)
		want  error
	}{
		{"real account", func(t *testing.T, ae *agentEnv) {
			a := ae.a
			a.Mode = domain.ModeReal
			if err := ae.store.Accounts().Update(ae.ctx, a); err != nil {
				t.Fatal(err)
			}
		}, domain.ErrRealModeGated},
		{"killed", func(t *testing.T, ae *agentEnv) {
			if _, err := ae.svc.persistKill(ae.ctx, ae.a.ID, "test", ae.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}, domain.ErrConflict},
		{"unreconciled", func(_ *testing.T, ae *agentEnv) { ae.svc.setReconciled(ae.ctx, ae.a, false, "test") }, domain.ErrConflict},
		{"budget 0", func(t *testing.T, ae *agentEnv) { ae.limits(t, func(l *domain.RiskLimits) { l.TradingBudget = 0 }) }, domain.ErrInvalid},
		{"no keys", func(t *testing.T, ae *agentEnv) {
			k, u := domain.AccountKeyNames(ae.a)
			_ = ae.sec.Delete(ae.ctx, k)
			_ = ae.sec.Delete(ae.ctx, u)
		}, domain.ErrCredentialsMissing},
		{"no eToro client", func(_ *testing.T, ae *agentEnv) { ae.svc.d.Broker = nil }, domain.ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			ae.setAgent(t, domain.AgentStopped)
			tt.setup(t, ae)
			if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if st := ae.svc.agentState(ae.ctx, ae.a.ID); st == domain.AgentRunning {
				t.Fatal("running after a refused start")
			}
		})
	}
	t.Run("start when running is a no-op", func(t *testing.T) {
		ae := newAgentEnv(t)
		st, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID)
		if err != nil || st.State != domain.AgentRunning {
			t.Fatalf("%+v %v", st, err)
		}
	})
}

func TestRiskLimitsLockedUnlessStopped(t *testing.T) {
	for _, st := range []domain.AgentState{domain.AgentRunning, domain.AgentPaused, domain.AgentKilled} {
		t.Run(string(st), func(t *testing.T) {
			ae := newAgentEnv(t)
			ae.setAgent(t, st)
			if _, err := ae.svc.SetRiskLimits(ae.ctx, ae.admin, domain.DefaultRiskLimits); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("err = %v, want conflict", err)
			}
		})
	}
}

func TestAgentPauseStop(t *testing.T) {
	ae := newAgentEnv(t)
	if st, err := ae.svc.PauseAgent(ae.ctx, ae.admin, ae.a.ID); err != nil || st.State != domain.AgentPaused {
		t.Fatalf("pause = %+v %v", st, err)
	}
	if st, err := ae.svc.PauseAgent(ae.ctx, ae.admin, ae.a.ID); err != nil || st.State != domain.AgentPaused {
		t.Fatalf("pause again = %+v %v", st, err)
	}
	if tr := ae.tick(t); ae.b.Opens != 0 || !strings.Contains(tr.Skipped, "paused") {
		t.Fatalf("tick while paused: %+v", tr)
	}
	if st, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil || st.State != domain.AgentStopped {
		t.Fatalf("stop = %+v %v", st, err)
	}
	if _, err := ae.svc.PauseAgent(ae.ctx, ae.admin, ae.a.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("pause a stopped agent: %v", err)
	}
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	if err := ae.svc.trip(ae.ctx, ae.a, "test"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []func(context.Context, Principal, string) (AgentStatus, error){ae.svc.PauseAgent, ae.svc.StopAgent} {
		if _, err := f(ae.ctx, ae.admin, ae.a.ID); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("pause/stop while killed: %v", err)
		}
	}
	kinds := ae.eventKinds(t)
	for _, k := range []string{"agent.started", "agent.paused", "agent.stopped", "agent.killed"} {
		if !slices.Contains(kinds, k) {
			t.Fatalf("event %s missing: %v", k, kinds)
		}
	}
}

func TestStartVersusSetRiskLimitsConcurrently(t *testing.T) {
	ae := newAgentEnv(t)
	for i := range 100 {
		ae.setAgent(t, domain.AgentStopped)
		before := len(ae.store.AuditEntries())
		budget := domain.Cents(1_000_00 + i)
		var wg sync.WaitGroup
		gate := make(chan struct{})
		var startErr, limErr error
		wg.Go(func() {
			<-gate
			_, startErr = ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID)
		})
		wg.Go(func() {
			<-gate
			l := domain.DefaultRiskLimits
			l.TradingBudget = budget
			_, limErr = ae.svc.SetRiskLimits(ae.ctx, ae.admin, l)
		})
		close(gate)
		wg.Wait()
		if startErr != nil {
			t.Fatalf("round %d: start: %v", i, startErr)
		}
		var order []string
		for _, e := range ae.store.AuditEntries()[before:] {
			if e.Action == "agent.start" || e.Action == "risk.limits_changed" {
				order = append(order, e.Action)
			}
		}
		switch {
		case limErr == nil && !slices.Equal(order, []string{"risk.limits_changed", "agent.start"}):
			t.Fatalf("round %d: limits written after the start: %v", i, order)
		case limErr != nil && (!errors.Is(limErr, domain.ErrConflict) || !slices.Equal(order, []string{"agent.start"})):
			t.Fatalf("round %d: %v, %v", i, limErr, order)
		}
		if ae.svc.agentState(ae.ctx, ae.a.ID) != domain.AgentRunning {
			t.Fatalf("round %d: not running", i)
		}
	}
}

// limitedBroker counts account changes (open, close, cancel) with the fake clock's time, as
// the eToro client's execution limiter would see them, and refuses a 21st in 60 s.
type limitedBroker struct {
	*fakes.SimBroker
	clock *fakes.Clock
	mu    sync.Mutex
	calls []time.Time
	over  int
}

func (l *limitedBroker) note() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	n := 0
	for _, c := range l.calls {
		if now.Sub(c) < time.Minute {
			n++
		}
	}
	if n >= 20 {
		l.over++
	}
	l.calls = append(l.calls, now)
}

func (l *limitedBroker) Open(ctx context.Context, in domain.OrderIntent) (domain.OrderStatus, error) {
	l.note()
	return l.SimBroker.Open(ctx, in)
}

func (l *limitedBroker) Close(ctx context.Context, pos int64, id domain.InstrumentID, req string) (domain.OrderStatus, error) {
	l.note()
	return l.SimBroker.Close(ctx, pos, id, req)
}

func (l *limitedBroker) Cancel(ctx context.Context, orderID int64, req string) error {
	l.note()
	return l.SimBroker.Cancel(ctx, orderID, req)
}

// pendingAtBroker opens a pending order at the broker and stores it as ours (pending).
func (ae *agentEnv) pendingAtBroker(t *testing.T, n int) {
	t.Helper()
	hook := ae.b.OnOpen
	ae.b.OnOpen = nil
	defer func() { ae.b.OnOpen = hook }()
	bctx := ports.WithAccount(ae.ctx, ae.a)
	for i := range n {
		o, err := domain.NewOrder(domain.Order{
			RequestID: fmt.Sprintf("00000000-0000-4000-9000-%012d", i), AccountID: ae.a.ID, Mode: domain.ModeDemo, Kind: domain.OrderOpen,
			Direction: domain.Long, Instrument: instASX, Amount: 100_00, Entry: 100_000_000, StopLoss: 97_000_000,
		}, ae.clock.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := ae.store.Orders().Create(ae.ctx, o); err != nil {
			t.Fatal(err)
		}
		in, _ := domain.IntentFor(o)
		ae.b.OpenScript = []fakes.SimOutcome{fakes.SimPending}
		st, err := ae.b.Open(bctx, in)
		if err != nil {
			t.Fatal(err)
		}
		next, _ := o.Transition(domain.OrderSent, ae.clock.Now(), "")
		next, _ = next.Transition(domain.OrderPending, ae.clock.Now(), "")
		next.EtoroOrderID = st.OrderID
		if err := ae.store.Orders().Update(ae.ctx, o, next); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKillFlattensWithinOneQuotaWindow(t *testing.T) {
	ae := newAgentEnv(t)
	for range 15 {
		ae.openPosition(t, instNYSE, domain.Long)
	}
	ae.pendingAtBroker(t, 5)
	lb := &limitedBroker{SimBroker: ae.b, clock: ae.clock}
	ae.svc.d.Broker = lb
	opens := ae.b.Opens
	start := ae.clock.Now()
	// an operator's API key may kill (OD11)
	op := apiKey(ae.shared(t, "op", domain.RoleOperator, domain.AccessTrade))
	rep, err := ae.svc.Kill(ae.ctx, op, ae.a.ID, "testing the switch")
	if err != nil || len(rep.Errors) > 0 {
		t.Fatalf("kill = %+v %v", rep, err)
	}
	if rep.Closing != 15 || rep.Cancelled != 5 || ae.b.Closes != 15 || ae.b.Cancels != 5 {
		t.Fatalf("report %+v, broker closes %d cancels %d", rep, ae.b.Closes, ae.b.Cancels)
	}
	if len(lb.calls) != 20 || lb.over != 0 || lb.calls[len(lb.calls)-1].Sub(start) >= time.Minute {
		t.Fatalf("%d calls (%d over the quota), last at +%s", len(lb.calls), lb.over, lb.calls[len(lb.calls)-1].Sub(start))
	}
	if len(ae.b.Positions) != 0 {
		t.Fatalf("positions left: %+v", ae.b.Positions)
	}
	rs, _ := ae.store.RiskStates().Get(ae.ctx, ae.a.ID)
	if !rs.State.Killed || !strings.Contains(rs.KillReason, "manual kill by op") || ae.svc.agentState(ae.ctx, ae.a.ID) != domain.AgentKilled {
		t.Fatalf("risk state %+v", rs)
	}
	// no Open after the kill, even from a tick or a start
	ae.tick(t)
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("start after kill: %v", err)
	}
	if ae.b.Opens != opens {
		t.Fatalf("opens after the kill: %d", ae.b.Opens-opens)
	}
	// killing again flattens again but announces nothing new
	rep, err = ae.svc.Kill(ae.ctx, ae.admin, ae.a.ID, "")
	if err != nil || !rep.Already {
		t.Fatalf("second kill = %+v %v", rep, err)
	}
	n := 0
	for _, k := range ae.eventKinds(t) {
		if k == "agent.killed" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d agent.killed events", n)
	}
	noViolations(t, ae.b)
}

func TestKillCancelsARunningTick(t *testing.T) {
	ae := newAgentEnv(t)
	entered := make(chan struct{})
	ae.reviewing(t, func(ctx context.Context, _ ports.ReviewInput) (domain.Verdict, error) {
		close(entered)
		<-ctx.Done() // the reviewer is slow: the kill cancels it
		return domain.Verdict{Kind: domain.VerdictApprove}, nil
	})
	done := make(chan AgentTrace)
	go func() {
		tr, _ := ae.svc.AgentTick(ae.ctx, ae.a.ID)
		done <- tr
	}()
	<-entered
	if _, err := ae.svc.Kill(ae.ctx, ae.admin, ae.a.ID, ""); err != nil {
		t.Fatal(err)
	}
	// Kill returned, so the tick has ended (Kill waits for it)
	select {
	case tr := <-done:
		if ae.b.Opens != 0 || tr.Entries != 0 {
			t.Fatalf("opened during the kill: %+v", tr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the tick did not end")
	}
}

func TestResetKill(t *testing.T) {
	ae := newAgentEnv(t)
	ae.b.Cash = 900_000_00
	if _, err := ae.svc.Kill(ae.ctx, ae.admin, ae.a.ID, ""); err != nil {
		t.Fatal(err)
	}
	op := ae.shared(t, "op", domain.RoleOperator, domain.AccessTrade)
	tests := []struct {
		name    string
		p       Principal
		confirm string
		want    error
	}{
		{"operator", op, ResetPhrase(ae.a.Name), domain.ErrForbidden},
		{"admin API key", apiKey(ae.admin), ResetPhrase(ae.a.Name), domain.ErrForbidden},
		{"no phrase", ae.admin, "", domain.ErrInvalid},
		{"wrong phrase", ae.admin, "RESET another", domain.ErrInvalid},
		{"lower case", ae.admin, strings.ToLower(ResetPhrase(ae.a.Name)), domain.ErrInvalid},
	}
	for _, tt := range tests {
		if _, err := ae.svc.ResetKill(ae.ctx, tt.p, ae.a.ID, tt.confirm); !errors.Is(err, tt.want) {
			t.Fatalf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
	}
	// eToro unreadable: nothing is reset
	ae.b.PortfolioErr = domain.ErrUnavailable
	if _, err := ae.svc.ResetKill(ae.ctx, ae.admin, ae.a.ID, ResetPhrase(ae.a.Name)); err == nil {
		t.Fatal("reset without the equity")
	}
	ae.b.PortfolioErr = nil
	st, err := ae.svc.ResetKill(ae.ctx, ae.admin, ae.a.ID, " "+ResetPhrase(ae.a.Name)+" ")
	if err != nil || st.State != domain.AgentStopped {
		t.Fatalf("reset = %+v %v", st, err)
	}
	rs, _ := ae.store.RiskStates().Get(ae.ctx, ae.a.ID)
	if rs.State.Killed || rs.KillReason != "" || !rs.KilledAt.IsZero() || rs.State.HighWaterMark != 900_000_00 {
		t.Fatalf("risk state after reset = %+v", rs)
	}
	if ae.svc.agentState(ae.ctx, ae.a.ID) != domain.AgentStopped {
		t.Fatal("agent not stopped after the reset")
	}
	if _, err := ae.svc.ResetKill(ae.ctx, ae.admin, ae.a.ID, ResetPhrase(ae.a.Name)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("reset when not killed: %v", err)
	}
	if !slices.ContainsFunc(ae.store.AuditEntries(), func(e domain.AuditEntry) bool { return e.Action == "agent.kill_reset" }) {
		t.Fatal("reset not audited")
	}
	if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatalf("start after the reset: %v", err)
	}
}

func TestResumeAgents(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, ae *agentEnv)
		want  domain.AgentState
		event string
	}{
		{"clean reconciliation: resumes", func(*testing.T, *agentEnv) {}, domain.AgentRunning, "agent.resumed"},
		{"not reconciled: paused", func(_ *testing.T, ae *agentEnv) { ae.svc.setReconciled(ae.ctx, ae.a, false, "mismatch") }, domain.AgentPaused, "agent.resume_refused"},
		{"never reconciled in this process: paused", func(_ *testing.T, ae *agentEnv) {
			ae.svc.rt.mu.Lock()
			delete(ae.svc.rt.reconciled, ae.a.ID)
			ae.svc.rt.mu.Unlock()
		}, domain.AgentPaused, "agent.resume_refused"},
		{"killed: stays killed", func(t *testing.T, ae *agentEnv) {
			if _, err := ae.svc.persistKill(ae.ctx, ae.a.ID, "test", ae.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}, domain.AgentKilled, "agent.resume_refused"},
		{"no keys: paused", func(_ *testing.T, ae *agentEnv) {
			k, _ := domain.AccountKeyNames(ae.a)
			_ = ae.sec.Delete(ae.ctx, k)
		}, domain.AgentPaused, "agent.resume_refused"},
		{"invalid limits: paused", func(t *testing.T, ae *agentEnv) {
			if err := ae.store.Settings().Set(ae.ctx, riskLimitsKey(ae.a.ID), `{"tradingBudgetCents":-1}`, ae.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}, domain.AgentPaused, "agent.resume_refused"},
		{"real account: paused", func(t *testing.T, ae *agentEnv) {
			a := ae.a
			a.Mode = domain.ModeReal
			if err := ae.store.Accounts().Update(ae.ctx, a); err != nil {
				t.Fatal(err)
			}
		}, domain.AgentPaused, "agent.resume_refused"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			tt.setup(t, ae)
			out, err := ae.svc.ResumeAgents(ae.ctx)
			if err != nil || len(out) != 1 || out[0].State != tt.want || (tt.want == domain.AgentRunning) != (out[0].Reason == "") {
				t.Fatalf("resume = %+v %v", out, err)
			}
			if st := ae.svc.agentState(ae.ctx, ae.a.ID); st != tt.want {
				t.Fatalf("stored state %s, want %s", st, tt.want)
			}
			if !slices.Contains(ae.eventKinds(t), tt.event) {
				t.Fatalf("no %s event: %v", tt.event, ae.eventKinds(t))
			}
		})
	}
	t.Run("stopped agents are left alone", func(t *testing.T) {
		ae := newAgentEnv(t)
		ae.setAgent(t, domain.AgentStopped)
		if out, err := ae.svc.ResumeAgents(ae.ctx); err != nil || len(out) != 0 {
			t.Fatalf("resume = %+v %v", out, err)
		}
	})
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 5, "abc"}, {"abcdef", 3, "abc"}, {"aé", 2, "a"}, {"éé", 3, "é"},
	}
	for _, tt := range tests {
		if got := truncate(tt.in, tt.n); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

// TestKillOutlivesItsRequest: a kill whose request is cancelled (the browser tab closed)
// still persists the kill and closes every position cactusTrade opened.
func TestKillOutlivesItsRequest(t *testing.T) {
	ae := newAgentEnv(t)
	for range 3 {
		ae.openPosition(t, instNYSE, domain.Long)
	}
	ctx, cancel := context.WithCancel(ae.ctx)
	cancel()
	rep, err := ae.svc.Kill(ctx, ae.admin, ae.a.ID, "tab closed")
	if err != nil || rep.Closing != 3 || ae.b.Closes != 3 || len(ae.b.Positions) != 0 {
		t.Fatalf("kill = %+v %v, closes %d, left %d", rep, err, ae.b.Closes, len(ae.b.Positions))
	}
	if ae.svc.agentState(ae.ctx, ae.a.ID) != domain.AgentKilled {
		t.Fatal("the kill was not stored")
	}
}

// TestAgentStateSince: the panel's "running for …" starts when the state changes, and a
// write of the same state (a resume after a restart) keeps the original time.
func TestAgentStateSince(t *testing.T) {
	ae := newAgentEnv(t)
	if err := ae.svc.setAgentState(ae.ctx, ae.a.ID, domain.AgentStopped); err != nil {
		t.Fatal(err)
	}
	ae.clock.T = ae.clock.T.Add(time.Minute)
	start := ae.clock.Now().UTC()
	if err := ae.svc.setAgentState(ae.ctx, ae.a.ID, domain.AgentRunning); err != nil {
		t.Fatal(err)
	}
	ae.clock.T = ae.clock.T.Add(2 * time.Hour)
	if err := ae.svc.setAgentState(ae.ctx, ae.a.ID, domain.AgentRunning); err != nil {
		t.Fatal(err)
	}
	if got := ae.svc.agentSince(ae.ctx, ae.a.ID); !got.Equal(start) {
		t.Fatalf("since = %s, want %s", got, start)
	}
	if err := ae.svc.setAgentState(ae.ctx, ae.a.ID, domain.AgentPaused); err != nil {
		t.Fatal(err)
	}
	if got := ae.svc.agentSince(ae.ctx, ae.a.ID); !got.Equal(ae.clock.Now().UTC()) {
		t.Fatalf("since after pause = %s", got)
	}
	v, err := ae.svc.AgentStatusView(ae.ctx, ae.admin, ae.a.ID)
	if err != nil || !v.StateSince.Equal(ae.clock.Now().UTC()) {
		t.Fatalf("view since = %s %v", v.StateSince, err)
	}
}
