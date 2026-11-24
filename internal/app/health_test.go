package app

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// tick runs one supervisor loop tick and waits for its passes.
func (x *execEnv) tick() {
	var wg sync.WaitGroup
	x.svc.superviseTick(x.ctx, &wg)
	wg.Wait()
}

func (x *execEnv) health(t *testing.T) (TradingHealth, AccountHealth) {
	t.Helper()
	h := x.svc.TradingHealth(x.ctx)
	for _, a := range h.Accounts {
		if a.AccountID == x.a.ID {
			return h, a
		}
	}
	t.Fatalf("account %s missing from %+v", x.a.ID, h)
	return h, AccountHealth{}
}

// TestTradingHealth (PLAN 7.5): every loop alive = the supervisor loop ticked and each
// supervised account had a pass within SupervisorStale; otherwise the problem names the
// account.
func TestTradingHealth(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, x *execEnv)
		// want is a substring of the problems ("" = healthy).
		want      string
		wantPass  bool // the account has a last pass
		wantClean bool // ... and a clean one
		wantLease bool
	}{
		{name: "just started, no pass yet", setup: func(*testing.T, *execEnv) {}},
		{
			name: "no pass for over 60 s",
			setup: func(_ *testing.T, x *execEnv) {
				x.clock.Advance(SupervisorStale + time.Second)
			},
			want: "the supervisor loop has not run since the start 1m1s ago",
		},
		{
			name: "loop ticking, account wanted but never passed",
			setup: func(_ *testing.T, x *execEnv) {
				x.svc.health.want(x.a.ID, x.clock.Now())
				x.clock.Advance(SupervisorStale + time.Second)
				x.svc.health.tick(x.clock.Now())
			},
			want: "account " + "%ID%" + ": no supervisor pass for 1m1s",
		},
		{
			name: "recent pass",
			setup: func(_ *testing.T, x *execEnv) {
				x.tick()
				x.clock.Advance(20 * time.Second) // within the 30 s lease
			},
			wantPass: true, wantClean: true, wantLease: true,
		},
		{
			name: "stalled supervisor",
			setup: func(_ *testing.T, x *execEnv) {
				x.tick()
				x.clock.Advance(SupervisorStale + time.Second)
				x.svc.health.tick(x.clock.Now()) // the loop ticks, the account's pass hangs
			},
			want:     "account %ID%: the last supervisor pass was 1m1s ago",
			wantPass: true, wantClean: true, wantLease: false, // the lease expired meanwhile
		},
		{
			name: "lease held by another process",
			setup: func(t *testing.T, x *execEnv) {
				now := x.clock.Now()
				if err := x.store.AgentLeases().Acquire(x.ctx, x.a.ID, "other-process", now, now.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				for range 7 {
					x.tick()
					x.clock.Advance(SupervisorInterval)
				}
			},
			want: "no supervisor pass for 1m10s (another process holds the lease",
		},
		{
			name: "portfolio unreadable: the pass ran, not cleanly",
			setup: func(_ *testing.T, x *execEnv) {
				x.b.PortfolioErr = domain.ErrUnavailable
				x.tick()
			},
			wantPass: true, wantLease: true,
		},
		{
			name:  "agent loop down",
			setup: func(_ *testing.T, x *execEnv) { x.svc.SetAgentLoop(false) },
			want:  msgLoopDown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newExecEnv(t)
			tt.setup(t, x)
			h, a := x.health(t)
			want := strings.ReplaceAll(tt.want, "%ID%", x.a.ID)
			got := strings.Join(h.Problems, " | ")
			switch {
			case want == "" && !h.OK():
				t.Fatalf("problems: %s", got)
			case want != "" && (h.OK() || !strings.Contains(got, want)):
				t.Fatalf("problems %q, want %q", got, want)
			}
			if !a.Supervised || a.Mode != domain.ModeDemo || !a.Reconciled || a.Killed || a.RealGateArmed {
				t.Fatalf("account %+v", a)
			}
			if !a.LastPass.IsZero() != tt.wantPass || !a.LastCleanPass.IsZero() != tt.wantClean || a.LeaseHeld != tt.wantLease {
				t.Fatalf("pass %v clean %v lease %v", a.LastPass, a.LastCleanPass, a.LeaseHeld)
			}
		})
	}
}

// TestTradingHealthAccounts: only eToro accounts are listed; a real one (gated) and one
// without keys are not supervised and never stale; orders count by non-final state; a kill
// shows.
func TestTradingHealthAccounts(t *testing.T) {
	x := newExecEnv(t)
	real := x.account(t, "Real", domain.ModeReal)
	noKeys, err := x.svc.CreateAccount(x.ctx, x.admin, "No keys", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	x.storedOrder(t, openSpec(instNYSE), x.clock.Now(), domain.OrderPending)
	x.storedOrder(t, openSpec(instLSE), x.clock.Now(), domain.OrderUnknown)
	x.storedOrder(t, openSpec(instASX), x.clock.Now(), domain.OrderFilled)
	x.setAgent(t, domain.AgentKilled)
	for range 7 {
		x.tick()
		x.clock.Advance(SupervisorInterval)
	}
	h, a := x.health(t)
	if !h.OK() {
		t.Fatalf("problems %v", h.Problems)
	}
	if a.AgentState != domain.AgentKilled || !a.Killed {
		t.Fatalf("kill not shown: %+v", a)
	}
	want := map[domain.OrderState]int{domain.OrderCreated: 0, domain.OrderSent: 0, domain.OrderPending: 1, domain.OrderPartial: 0, domain.OrderUnknown: 1}
	if len(a.Orders) != len(want) {
		t.Fatalf("orders %v", a.Orders)
	}
	for st, n := range want {
		if a.Orders[st] != n {
			t.Fatalf("orders %v, want %v", a.Orders, want)
		}
	}
	byID := map[string]AccountHealth{}
	for _, ah := range h.Accounts {
		byID[ah.AccountID] = ah
	}
	if r := byID[real.ID]; r.Supervised || r.Mode != domain.ModeReal || r.Problem != "" || r.Name != "" {
		t.Fatalf("real %+v", r)
	}
	if n := byID[noKeys.ID]; n.Supervised || n.Problem != "" {
		t.Fatalf("no keys %+v", n)
	}
	if len(h.Accounts) != 3 {
		t.Fatalf("%d accounts", len(h.Accounts))
	}
}

func TestTradingHealthStoreDown(t *testing.T) {
	x := newExecEnv(t)
	x.svc.d.Store = listFails{x.store}
	h := x.svc.TradingHealth(x.ctx)
	if h.OK() || !slices.Contains(h.Problems, "reading the accounts failed") || len(h.Accounts) != 0 {
		t.Fatalf("%+v", h)
	}
}

func TestTradingHealthCountsLLMCalls(t *testing.T) {
	x := newExecEnv(t)
	pr := domain.LLMProfile{ID: "p1"}
	for _, c := range []struct {
		purpose domain.LLMPurpose
		err     string
	}{{domain.LLMPurposeAgent, ""}, {domain.LLMPurposeAgent, ""}, {domain.LLMPurposeAgent, "timeout"}, {domain.LLMPurposeTest, "refused"}} {
		call := domain.LLMCall{Error: c.err}
		if err := x.svc.logCall(x.ctx, &call, x.a.ID, c.purpose, pr, x.clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	got := x.svc.TradingHealth(x.ctx).LLMCalls
	want := []LLMCallCount{
		{domain.LLMPurposeAgent, LLMOutcomeError, 1}, {domain.LLMPurposeAgent, LLMOutcomeOK, 2}, {domain.LLMPurposeTest, LLMOutcomeError, 1},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestSystemHealth: admin only (an admin API key too); names only for visible accounts.
func TestSystemHealth(t *testing.T) {
	x := newExecEnv(t)
	other := x.user(t, "olga", domain.RoleAdmin)
	tests := []struct {
		name     string
		p        Principal
		err      error
		wantName string
	}{
		{"anonymous", Principal{}, domain.ErrUnauthenticated, ""},
		{"viewer", x.user(t, "vera", domain.RoleViewer), domain.ErrForbidden, ""},
		{"operator", x.user(t, "otto", domain.RoleOperator), domain.ErrForbidden, ""},
		{"owner admin", x.admin, nil, "Demo"},
		{"owner admin by API key", Principal{UserID: x.admin.UserID, Username: "admin", Role: domain.RoleAdmin, Via: ViaAPIKey, APIKeyID: "k1"}, nil, "Demo"},
		{"another admin", other, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := x.svc.SystemHealth(x.ctx, tt.p)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err %v, want %v", err, tt.err)
			}
			if err != nil {
				return
			}
			if len(h.Accounts) != 1 || h.Accounts[0].AccountID != x.a.ID || h.Accounts[0].Name != tt.wantName {
				t.Fatalf("%+v", h.Accounts)
			}
		})
	}
}
