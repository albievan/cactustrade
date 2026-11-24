package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// missingOf returns the *domain.MissingError in err, failing the test when there is none.
func missingOf(t *testing.T, err error) *domain.MissingError {
	t.Helper()
	me, ok := errors.AsType[*domain.MissingError](err)
	if !ok {
		t.Fatalf("err = %v, want a MissingError", err)
	}
	return me
}

// TestStartAgentMissing: every "missing setup" refusal of StartAgent names what is missing,
// keeps the sentinel it had before (so the HTTP status is unchanged), has a message without a
// field name, and says whether the caller may fix it (R22).
func TestStartAgentMissing(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T, ae *agentEnv)
		want     domain.Missing
		sentinel error
		admin    bool // the owner admin in a web session may fix it
		operator bool // an operator with trade access may fix it
	}{
		{
			"budget 0", func(t *testing.T, ae *agentEnv) { ae.limits(t, func(l *domain.RiskLimits) { l.TradingBudget = 0 }) },
			domain.MissingTradingBudget, domain.ErrInvalid, true, false,
		},
		{"no keys", func(_ *testing.T, ae *agentEnv) {
			k, u := domain.AccountKeyNames(ae.a)
			_ = ae.sec.Delete(ae.ctx, k)
			_ = ae.sec.Delete(ae.ctx, u)
		}, domain.MissingAccountKeys, domain.ErrCredentialsMissing, true, false},
		{"killed", func(t *testing.T, ae *agentEnv) {
			if _, err := ae.svc.persistKill(ae.ctx, ae.a.ID, "test", ae.clock.Now()); err != nil {
				t.Fatal(err)
			}
		}, domain.MissingKillReset, domain.ErrConflict, true, false},
		{
			"unreconciled", func(_ *testing.T, ae *agentEnv) { ae.svc.setReconciled(ae.ctx, ae.a, false, "test") },
			domain.MissingReconcile, domain.ErrConflict, true, true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			op := ae.shared(t, "op", domain.RoleOperator, domain.AccessTrade)
			ae.setAgent(t, domain.AgentStopped)
			tt.setup(t, ae)
			for _, who := range []struct {
				p      Principal
				canFix bool
			}{{ae.admin, tt.admin}, {op, tt.operator}} {
				_, err := ae.svc.StartAgent(ae.ctx, who.p, ae.a.ID)
				if !errors.Is(err, tt.sentinel) {
					t.Fatalf("%s: err = %v, want %v", who.p.Username, err, tt.sentinel)
				}
				me := missingOf(t, err)
				if me.What != tt.want || me.CanFix != who.canFix {
					t.Fatalf("%s: missing %s canFix %v, want %s %v", who.p.Username, me.What, me.CanFix, tt.want, who.canFix)
				}
				if me.Msg == "" || strings.Contains(me.Msg, "Cents") || strings.Contains(me.Msg, ": conflict") {
					t.Fatalf("message %q", me.Msg)
				}
			}
		})
	}
	t.Run("a field-less message for the budget", func(t *testing.T) {
		ae := newAgentEnv(t)
		ae.setAgent(t, domain.AgentStopped)
		ae.limits(t, func(l *domain.RiskLimits) { l.TradingBudget = 0 })
		_, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID)
		if me := missingOf(t, err); me.Msg != "Set a trading budget before starting the agent" {
			t.Fatalf("message %q", me.Msg)
		}
		if ve, ok := errors.AsType[*domain.ValidationError](err); !ok || ve.Field != "tradingBudgetCents" {
			t.Fatalf("validation error %+v", ve)
		}
	})
}

// TestMissingRefusals: the other "missing setup" refusals name what is missing and keep their
// sentinel.
func TestMissingRefusals(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		call     func(t *testing.T) error
		want     domain.Missing
		sentinel error
		canFix   bool
	}{
		{"pause while killed", func(t *testing.T) error {
			ae := newAgentEnv(t)
			if err := ae.svc.trip(ae.ctx, ae.a, "test"); err != nil {
				t.Fatal(err)
			}
			_, err := ae.svc.PauseAgent(ae.ctx, ae.admin, ae.a.ID)
			return err
		}, domain.MissingKillReset, domain.ErrConflict, true},
		{"reconcile without keys", func(t *testing.T) error {
			ae := newAgentEnv(t)
			k, _ := domain.AccountKeyNames(ae.a)
			_ = ae.sec.Delete(ae.ctx, k)
			_, err := ae.svc.ReconcileAccount(ae.ctx, ae.admin, ae.a.ID)
			if !errors.Is(err, domain.ErrUnavailable) {
				t.Fatalf("err = %v, want also unavailable", err)
			}
			return err
		}, domain.MissingAccountKeys, domain.ErrCredentialsMissing, true},
		{"no eToro account", func(t *testing.T) error {
			e := newEnv(t)
			_, err := e.svc.Watchlist(ctx, e.user(t, "olga", domain.RoleOperator))
			return err
		}, domain.MissingAccount, domain.ErrNoAccount, true},
		{"no eToro account, viewer", func(t *testing.T) error {
			e := newEnv(t)
			_, err := e.svc.RiskLimits(ctx, e.user(t, "vic", domain.RoleViewer))
			return err
		}, domain.MissingAccount, domain.ErrNotFound, false},
		{"test e-mail without a server", func(t *testing.T) error {
			e, _ := withMail(t)
			return e.svc.SendTestEmail(ctx, e.admin, "me@example.com")
		}, domain.MissingEmailServer, domain.ErrInvalid, true},
		{"password reset without a public address", func(t *testing.T) error {
			e, _ := withMail(t)
			return e.svc.RequestPasswordReset(ctx, "admin", "1.2.3.4")
		}, domain.MissingPublicURL, domain.ErrUnavailable, false},
		{"password reset without a mail server", func(t *testing.T) error {
			e, _ := withMail(t)
			e.svc.cfg.PublicURL = "https://trader.example"
			return e.svc.RequestPasswordReset(ctx, "admin", "1.2.3.4")
		}, domain.MissingEmailServer, domain.ErrUnavailable, false},
		{"LLM test without a key", func(t *testing.T) error {
			e := newEnv(t)
			e.svc.d.LLM = &fakeReviewer{}
			pr, err := e.svc.SaveLLMProfile(ctx, e.admin, domain.LLMProfile{Name: "Desk", LLMConfig: domain.LLMConfig{Provider: "openai", Model: "gpt"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.svc.TestLLM(ctx, e.admin, pr.ID)
			return err
		}, domain.MissingLLMProfile, domain.ErrInvalid, true},
		{"reviewing on with a keyless default", func(t *testing.T) error {
			e := newEnv(t)
			pr, err := e.svc.SaveLLMProfile(ctx, e.admin, domain.LLMProfile{Name: "Desk", LLMConfig: domain.LLMConfig{Provider: "openai", Model: "gpt"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.svc.SetLLMSettings(ctx, e.admin, domain.LLMSettings{Enabled: true, Active: pr.ID})
			return err
		}, domain.MissingLLMProfile, domain.ErrInvalid, true},
		{"Saxo sign-in without a public address", func(t *testing.T) error {
			e, _ := withBroker(t)
			e.svc.d.Connectors = map[domain.Platform]ports.OAuthConnector{domain.PlatformSaxo: &fakes.OAuthConnector{}}
			saxo, err := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformSaxo, "Saxo SIM", domain.ModeDemo)
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.svc.ConnectAccount(ctx, e.admin, domain.PlatformSaxo, saxo.ID)
			return err
		}, domain.MissingPublicURL, domain.ErrUnavailable, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(t)
			if !errors.Is(err, tt.sentinel) {
				t.Fatalf("err = %v, want %v", err, tt.sentinel)
			}
			me := missingOf(t, err)
			if me.What != tt.want || me.CanFix != tt.canFix || me.Msg == "" {
				t.Fatalf("missing %+v, want %s canFix %v", me, tt.want, tt.canFix)
			}
		})
	}
}

// TestAgentStatusViewMissing: the agent panel lists what must be set up before the agent can
// start, by StartAgent's rules plus an empty watchlist and an unusable reviewer (R22).
func TestAgentStatusViewMissing(t *testing.T) {
	approve := verdict(domain.Verdict{Kind: domain.VerdictApprove}, nil)
	tests := []struct {
		name    string
		stopped bool
		setup   func(t *testing.T, ae *agentEnv)
		want    []domain.Missing
	}{
		{"running: nothing listed", false, func(*testing.T, *agentEnv) {}, nil},
		{"stopped, all set", true, func(*testing.T, *agentEnv) {}, nil},
		{
			"budget 0", true, func(t *testing.T, ae *agentEnv) { ae.limits(t, func(l *domain.RiskLimits) { l.TradingBudget = 0 }) },
			[]domain.Missing{domain.MissingTradingBudget},
		},
		{"no keys", true, func(_ *testing.T, ae *agentEnv) {
			_, u := domain.AccountKeyNames(ae.a)
			_ = ae.sec.Delete(ae.ctx, u)
		}, []domain.Missing{domain.MissingAccountKeys}},
		{"killed", false, func(t *testing.T, ae *agentEnv) {
			if err := ae.svc.trip(ae.ctx, ae.a, "test"); err != nil {
				t.Fatal(err)
			}
		}, []domain.Missing{domain.MissingKillReset}},
		{
			"unreconciled", true, func(_ *testing.T, ae *agentEnv) { ae.svc.setReconciled(ae.ctx, ae.a, false, "test") },
			[]domain.Missing{domain.MissingReconcile},
		},
		{"empty watchlist", true, func(t *testing.T, ae *agentEnv) {
			if err := ae.store.Watchlist().Remove(ae.ctx, ae.a.ID, instNYSE); err != nil {
				t.Fatal(err)
			}
		}, []domain.Missing{domain.MissingWatchlist}},
		{"reviewing with a usable profile", true, func(t *testing.T, ae *agentEnv) { ae.reviewing(t, approve) }, nil},
		{"reviewing, the default lost its key", true, func(t *testing.T, ae *agentEnv) {
			ae.reviewing(t, approve)
			st, _, err := ae.svc.loadLLM(ae.ctx)
			if err != nil {
				t.Fatal(err)
			}
			_ = ae.sec.Delete(ae.ctx, domain.LLMKeyName(st.Active))
		}, []domain.Missing{domain.MissingLLMProfile}},
		{"reviewing, the default is gone", true, func(t *testing.T, ae *agentEnv) {
			ae.reviewing(t, approve)
			_ = ae.store.Settings().Set(ae.ctx, settingLLMProfiles, `{"enabled":true,"active":"gone","profiles":[]}`, ae.clock.Now())
		}, []domain.Missing{domain.MissingLLMProfile}},
		{"reviewing setup unreadable", true, func(t *testing.T, ae *agentEnv) {
			_ = ae.store.Settings().Set(ae.ctx, settingLLMProfiles, "garbage", ae.clock.Now())
		}, []domain.Missing{domain.MissingLLMProfile}},
		{"several, in StartAgent's order", true, func(t *testing.T, ae *agentEnv) {
			ae.limits(t, func(l *domain.RiskLimits) { l.TradingBudget = 0 })
			ae.svc.setReconciled(ae.ctx, ae.a, false, "test")
			if err := ae.svc.trip(ae.ctx, ae.a, "test"); err != nil {
				t.Fatal(err)
			}
		}, []domain.Missing{domain.MissingKillReset, domain.MissingTradingBudget, domain.MissingReconcile}},
		{"real account: nothing to start before phase 7", true, func(t *testing.T, ae *agentEnv) {
			ae.limits(t, func(l *domain.RiskLimits) { l.TradingBudget = 0 })
			a := ae.a
			a.Mode = domain.ModeReal
			if err := ae.store.Accounts().Update(ae.ctx, a); err != nil {
				t.Fatal(err)
			}
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			if tt.stopped {
				ae.setAgent(t, domain.AgentStopped)
			}
			tt.setup(t, ae)
			v, err := ae.svc.AgentStatusView(ae.ctx, ae.admin, ae.a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if v.Missing == nil {
				t.Fatal("Missing is nil (the contract needs an array)")
			}
			var got []domain.Missing
			for _, m := range v.Missing {
				got = append(got, m.What)
				if !m.CanFix {
					t.Errorf("%s: the owner admin cannot fix it", m.What)
				}
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("missing = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAgentStatusViewMissingCanFix: whether the caller may fix each item follows the use case
// that fixes it.
func TestAgentStatusViewMissingCanFix(t *testing.T) {
	all := []domain.Missing{domain.MissingKillReset, domain.MissingTradingBudget, domain.MissingReconcile, domain.MissingWatchlist, domain.MissingLLMProfile}
	tests := []struct {
		name string
		who  func(t *testing.T, ae *agentEnv) Principal
		can  []domain.Missing
	}{
		{"owner admin, web session", func(_ *testing.T, ae *agentEnv) Principal { return ae.admin }, all},
		{
			"owner admin, API key", func(_ *testing.T, ae *agentEnv) Principal { return apiKey(ae.admin) },
			[]domain.Missing{domain.MissingReconcile, domain.MissingWatchlist},
		},
		{"operator with trade access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "op", domain.RoleOperator, domain.AccessTrade)
		}, []domain.Missing{domain.MissingReconcile, domain.MissingWatchlist}},
		{"another admin with trade access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "ad", domain.RoleAdmin, domain.AccessTrade)
		}, []domain.Missing{domain.MissingKillReset, domain.MissingReconcile, domain.MissingWatchlist, domain.MissingLLMProfile}},
		{"viewer with view access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "vi", domain.RoleViewer, domain.AccessView)
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			ae.setAgent(t, domain.AgentStopped)
			ae.limits(t, func(l *domain.RiskLimits) { l.TradingBudget = 0 })
			ae.svc.setReconciled(ae.ctx, ae.a, false, "test")
			if err := ae.store.Watchlist().Remove(ae.ctx, ae.a.ID, instNYSE); err != nil {
				t.Fatal(err)
			}
			_ = ae.store.Settings().Set(ae.ctx, settingLLMProfiles, "garbage", ae.clock.Now())
			if err := ae.svc.trip(ae.ctx, ae.a, "test"); err != nil {
				t.Fatal(err)
			}
			v, err := ae.svc.AgentStatusView(ae.ctx, tt.who(t, ae), ae.a.ID)
			if err != nil {
				t.Fatal(err)
			}
			var got, can []domain.Missing
			for _, m := range v.Missing {
				got = append(got, m.What)
				if m.CanFix {
					can = append(can, m.What)
				}
			}
			if !slices.Equal(got, all) {
				t.Fatalf("missing = %v, want %v", got, all)
			}
			if !slices.Equal(can, tt.can) {
				t.Fatalf("can fix %v, want %v", can, tt.can)
			}
		})
	}
}

func TestCanFixDeploymentSetting(t *testing.T) {
	e := newEnv(t)
	for _, m := range []domain.Missing{domain.MissingPublicURL, "unknown"} {
		if e.svc.canFix(e.admin, domain.AccountView{}, m) {
			t.Errorf("%s fixable in the app", m)
		}
	}
	if !e.svc.canFix(e.admin, domain.AccountView{}, domain.MissingEmailServer) {
		t.Error("an admin cannot set up the mail server")
	}
}
