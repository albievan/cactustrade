package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// PLAN 6.13 (2): no agent can be started while this process's agent loop does not run, and
// the panel says why; pause, stop and kill stay available.
func TestStartWithoutLoop(t *testing.T) {
	tests := []struct {
		name    string
		set     func(s *Service)
		allowed bool
		reason  string
	}{
		{"start-up has not decided", func(s *Service) { s.loop.Store(loopStarting) }, false, "starting up"},
		{"resume failed", func(s *Service) { s.SetAgentLoop(false) }, false, "start-up resume failed; restart cactusTrade"},
		{"loop running", func(s *Service) { s.SetAgentLoop(true) }, true, ""},
		{"running again after a failure", func(s *Service) { s.SetAgentLoop(false); s.SetAgentLoop(true) }, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
				t.Fatal(err)
			}
			tt.set(ae.svc)
			v, err := ae.svc.AgentStatusView(ae.ctx, ae.admin, ae.a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if v.Controls.Start != tt.allowed || !strings.Contains(v.StartBlocked, tt.reason) || (tt.allowed && v.StartBlocked != "") {
				t.Fatalf("controls %+v blocked %q", v.Controls, v.StartBlocked)
			}
			if !v.Controls.Pause || !v.Controls.Stop || !v.Controls.Kill || !v.Controls.Reset {
				t.Fatalf("other controls changed: %+v", v.Controls)
			}
			_, err = ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID)
			switch {
			case tt.allowed && err != nil:
				t.Fatalf("refused: %v", err)
			case !tt.allowed && (!errors.Is(err, domain.ErrUnavailable) || !strings.Contains(err.Error(), tt.reason)):
				t.Fatalf("err = %v, want unavailable with %q", err, tt.reason)
			}
			want := domain.AgentStopped
			if tt.allowed {
				want = domain.AgentRunning
			}
			if st := ae.svc.agentState(ae.ctx, ae.a.ID); st != want {
				t.Fatalf("state %s, want %s", st, want)
			}
		})
	}
	t.Run("a caller who may not start sees no reason", func(t *testing.T) {
		ae := newAgentEnv(t)
		ae.svc.SetAgentLoop(false)
		v, err := ae.svc.AgentStatusView(ae.ctx, apiKey(ae.admin), ae.a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if v.Controls.Start || v.StartBlocked != "" {
			t.Fatalf("controls %+v blocked %q", v.Controls, v.StartBlocked)
		}
	})
	t.Run("the OD5 resume does not depend on the loop", func(t *testing.T) {
		ae := newAgentEnv(t) // running
		ae.svc.loop.Store(loopStarting)
		outs, err := ae.svc.ResumeAgents(ae.ctx)
		if err != nil || len(outs) != 1 || outs[0].State != domain.AgentRunning {
			t.Fatalf("resume = %+v %v", outs, err)
		}
	})
}

// PLAN 6.13 (3): ResetKill reads eToro's equity before it takes the control lock, so a Kill
// never waits for that read; the kill switch is checked again under the lock.
func TestResetKillDoesNotDelayAKill(t *testing.T) {
	tests := []struct {
		name string
		// during runs while ResetKill's portfolio read is blocked
		during func(t *testing.T, ae *agentEnv)
		want   error
	}{
		{"a kill during the read completes at once", func(t *testing.T, ae *agentEnv) {
			done := make(chan error, 1)
			go func() {
				_, err := ae.svc.Kill(ae.ctx, ae.admin, ae.a.ID, "again")
				done <- err
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("Kill waited for ResetKill's portfolio read")
				<-done // after the read is released
			}
		}, nil},
		{"another reset finished during the read", func(t *testing.T, ae *agentEnv) {
			if _, err := ae.svc.ResetKill(ae.ctx, ae.admin, ae.a.ID, ResetPhrase(ae.a.Name)); err != nil {
				t.Error(err)
			}
		}, domain.ErrConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			if _, err := ae.svc.Kill(ae.ctx, ae.admin, ae.a.ID, "test"); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			h := &hookBroker{SimBroker: ae.b}
			h.pf = func(ctx context.Context) (domain.Portfolio, error) {
				first := false
				once.Do(func() { first = true })
				if first {
					close(entered)
					<-release
				}
				return ae.b.Portfolio(ctx)
			}
			ae.svc.d.Broker = h
			resetDone := make(chan error, 1)
			go func() {
				_, err := ae.svc.ResetKill(ae.ctx, ae.admin, ae.a.ID, ResetPhrase(ae.a.Name))
				resetDone <- err
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("ResetKill did not read the portfolio")
			}
			tt.during(t, ae)
			close(release)
			if err := <-resetDone; !errors.Is(err, tt.want) {
				t.Fatalf("reset = %v, want %v", err, tt.want)
			}
			if killed, err := ae.svc.isKilled(ae.ctx, ae.a.ID); killed || err != nil {
				t.Fatalf("still killed after the reset: %v %v", killed, err)
			}
			if st := ae.svc.agentState(ae.ctx, ae.a.ID); st != domain.AgentStopped {
				t.Fatalf("state %s, want stopped", st)
			}
		})
	}
}
