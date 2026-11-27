package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// TestRealTradingOptInConfig (PLAN 7.23, OD28): CACTUSTRADE_REAL_TRADING is off unless it is
// exactly "on"; any other value stops the start-up; the app's configuration carries it.
func TestRealTradingOptInConfig(t *testing.T) {
	tests := []struct {
		value string
		want  bool
		bad   bool
	}{
		{"", false, false},
		{"off", false, false},
		{" off ", false, false},
		{"on", true, false},
		{"yes", false, true},
		{"true", false, true},
		{"1", false, true},
		{"ON", false, true},
		{"of", false, true},
	}
	for _, tt := range tests {
		c, err := loadConfig(nil, envOf(map[string]string{"CACTUSTRADE_REAL_TRADING": tt.value}))
		switch {
		case tt.bad && err == nil:
			t.Errorf("%q accepted", tt.value)
		case !tt.bad && (err != nil || c.RealTrading != tt.want || appConfig(c, "host").RealTrading != tt.want):
			t.Errorf("%q: %v %v", tt.value, c.RealTrading, err)
		}
	}
}

// TestDefaultConfigRefusesRealEntries (PLAN 7.23): with the deployment's default
// configuration a real-money account is gated — its agent cannot be started, the reason names
// the opt-in — even with its gate armed; and no system event announces real trading.
func TestDefaultConfigRefusesRealEntries(t *testing.T) {
	c, err := loadConfig(nil, envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	cfg := appConfig(c, "host")
	if cfg.RealTrading {
		t.Fatal("real trading is on by default")
	}
	ctx := context.Background()
	st := fakes.NewStore()
	clk := &fakes.Clock{T: time.Date(2026, 11, 23, 15, 0, 0, 0, time.UTC)}
	sec := &fakes.SecretStore{Now: clk.Now}
	svc, err := app.New(app.Deps{
		Store: st, Secrets: sec, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: clk,
		Broker: &fakes.SimBroker{Now: clk.Now, Cash: 1_000_00},
	}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetAgentLoop(true)
	if _, err := svc.EnsureDefaultAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := st.Users().GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	p := app.Principal{UserID: u.ID, Username: u.Username, Role: u.Role, Via: app.ViaSession, MustChangePassword: true}
	if err := svc.ChangePassword(ctx, p, "admin", "admin password 1"); err != nil {
		t.Fatal(err)
	}
	p.MustChangePassword = false
	v, err := svc.CreateAccount(ctx, p, "Live", domain.ModeReal)
	if err != nil {
		t.Fatal(err)
	}
	ak, uk := domain.AccountKeyNames(v.Account)
	for _, n := range []domain.CredentialName{ak, uk} {
		if err := sec.Put(ctx, n, "test-key"); err != nil {
			t.Fatal(err)
		}
	}
	gate := domain.LockedGate(v.ID).Arm("demo", u.ID, domain.GateEvidence{}, clk.Now())
	if _, err := st.RealGates().Put(ctx, gate); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartAgent(ctx, p, v.ID); !errors.Is(err, domain.ErrRealModeGated) || !strings.Contains(err.Error(), "CACTUSTRADE_REAL_TRADING") {
		t.Fatalf("StartAgent = %v", err)
	}
	av, err := svc.AgentStatusView(ctx, p, v.ID)
	if err != nil || !av.Controls.Gated || av.Controls.Start || !strings.Contains(av.StartBlocked, "CACTUSTRADE_REAL_TRADING") {
		t.Fatalf("controls = %+v %q %v", av.Controls, av.StartBlocked, err)
	}
	svc.AnnounceRealTrading(ctx)
	es, _ := st.Events().List(ctx, []string{v.ID}, 50)
	for _, e := range es {
		if e.Kind == "system.real_trading_on" {
			t.Fatalf("announced with the opt-in off: %+v", e)
		}
	}
}
