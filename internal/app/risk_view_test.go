package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// PLAN 6.14: GET /risk/limits says whether the caller may change the limits, by the same
// rules as SetRiskLimits; a change succeeds exactly when CanEdit is true.
func TestRiskLimitsView(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name   string
		setup  func(t *testing.T, e *env, acct string) Principal
		reason string // "" = may edit
	}{
		{"owner in a web session, agent stopped", func(_ *testing.T, e *env, _ string) Principal { return e.admin }, ""},
		{"owner with an API key", func(_ *testing.T, e *env, _ string) Principal { return apiKey(e.admin) }, msgLimitsNotSession},
		{"operator with trade access", func(t *testing.T, e *env, acct string) Principal {
			return shareWith(t, e, acct, "olga", domain.RoleOperator, domain.AccessTrade)
		}, msgLimitsNotOwner},
		{"admin with view access", func(t *testing.T, e *env, acct string) Principal {
			return shareWith(t, e, acct, "adam", domain.RoleAdmin, domain.AccessView)
		}, msgLimitsNotOwner},
		{"agent running", func(_ *testing.T, e *env, acct string) Principal {
			_ = e.store.Settings().Set(ctx, agentStateKey(acct), string(domain.AgentRunning), e.clock.T)
			return e.admin
		}, "Stop the agent to change the limits (it is running)"},
		{"agent paused", func(_ *testing.T, e *env, acct string) Principal {
			_ = e.store.Settings().Set(ctx, agentStateKey(acct), string(domain.AgentPaused), e.clock.T)
			return e.admin
		}, "Stop the agent to change the limits (it is paused)"},
		{"agent killed", func(_ *testing.T, e *env, acct string) Principal {
			_ = e.store.Settings().Set(ctx, agentStateKey(acct), string(domain.AgentKilled), e.clock.T)
			return e.admin
		}, msgLimitsKilled},
		{"kill latched in this process, state stopped", func(_ *testing.T, e *env, acct string) Principal {
			e.svc.rt.latchKill(acct)
			return e.admin
		}, msgLimitsKilled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, _ := withBroker(t)
			acct, err := e.svc.CurrentAccount(ctx, e.admin)
			if err != nil {
				t.Fatal(err)
			}
			p := tt.setup(t, e, acct.ID)
			v, err := e.svc.RiskLimitsView(ctx, p)
			if err != nil {
				t.Fatal(err)
			}
			if v.CanEdit != (tt.reason == "") || v.EditBlocked != tt.reason {
				t.Fatalf("view = %+v, want reason %q", v, tt.reason)
			}
			if v.Limits != domain.DefaultRiskLimits {
				t.Fatalf("limits = %+v", v.Limits)
			}
			// the use case agrees: a change succeeds exactly when CanEdit
			if _, err := e.svc.SetRiskLimits(ctx, p, domain.DefaultRiskLimits); (err == nil) != v.CanEdit {
				t.Fatalf("SetRiskLimits = %v with canEdit %v", err, v.CanEdit)
			}
		})
	}
	t.Run("no access", func(t *testing.T) {
		e, _ := withBroker(t)
		if _, err := e.svc.RiskLimitsView(ctx, e.user(t, "nina", domain.RoleViewer)); !errors.Is(err, domain.ErrNoAccount) {
			t.Fatalf("err = %v", err)
		}
		if _, err := e.svc.RiskLimitsView(ctx, Principal{}); err == nil {
			t.Fatal("anonymous caller")
		}
	})
	t.Run("the refusal names the kill switch", func(t *testing.T) {
		e, _ := withBroker(t)
		acct, _ := e.svc.CurrentAccount(ctx, e.admin)
		e.svc.rt.latchKill(acct.ID)
		if _, err := e.svc.SetRiskLimits(ctx, e.admin, domain.DefaultRiskLimits); !errors.Is(err, domain.ErrConflict) || !strings.Contains(err.Error(), "kill switch is on") {
			t.Fatalf("err = %v", err)
		}
	})
}

// shareWith creates a user with role and shares acct with them.
func shareWith(t *testing.T, e *env, acct, name string, role domain.Role, access domain.AccountAccess) Principal {
	t.Helper()
	p := e.user(t, name, role)
	if err := e.svc.ShareAccount(context.Background(), e.admin, acct, name, access); err != nil {
		t.Fatal(err)
	}
	return p
}
