package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

func TestAgentStatusView(t *testing.T) {
	ae := newAgentEnv(t)
	// one supervisor pass stores today's risk state (start-of-day equity, high-water mark)
	ae.supervise(t)
	v, err := ae.svc.AgentStatusView(ae.ctx, ae.admin, "")
	if err != nil {
		t.Fatal(err)
	}
	if v.Account.ID != ae.a.ID || v.State != domain.AgentRunning || !v.Reconciliation.OK || !v.EquityKnown || v.EquityError != "" {
		t.Fatalf("view = %+v", v)
	}
	eq := v.Equity
	if v.Today != domain.RiskDay(ae.clock.Now()) || v.Risk.State.Day != v.Today || !v.DailyFloorKnown {
		t.Fatalf("risk day %s / %s, floor known %v", v.Today, v.Risk.State.Day, v.DailyFloorKnown)
	}
	if want := domain.DailyFloor(v.Risk.State, v.Limits); v.DailyFloor != want || v.ToDailyFloor != eq-want {
		t.Fatalf("daily floor %d (%d to go), want %d", v.DailyFloor, v.ToDailyFloor, want)
	}
	if want := domain.DrawdownFloor(eq, v.Risk.State, v.Limits); v.DrawdownFloor != want || v.ToDrawdownFloor != eq-want || want <= 0 {
		t.Fatalf("drawdown floor %d (%d to go), want %d", v.DrawdownFloor, v.ToDrawdownFloor, want)
	}
	if v.ResetPhrase != "RESET "+ae.a.Name || !v.Lease.Mine || v.Lease.Other {
		t.Fatalf("phrase %q lease %+v", v.ResetPhrase, v.Lease)
	}
	if len(v.Flatten) != len(domain.Markets) {
		t.Fatalf("flatten = %+v", v.Flatten)
	}
	for _, f := range v.Flatten {
		if !f.Trading || !f.At.Equal(f.Closes.Add(-v.Limits.FlattenBeforeClose)) {
			t.Fatalf("flatten %+v", f)
		}
	}
}

func TestAgentStatusViewWithoutFigures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, ae *agentEnv)
		check func(t *testing.T, v AgentView)
	}{
		{"portfolio unreadable", func(_ *testing.T, ae *agentEnv) {
			ae.b.PortfolioErr = fmt.Errorf("HTTP 503: %w", domain.ErrUnavailable)
		}, func(t *testing.T, v AgentView) {
			if v.EquityKnown || v.EquityError == "" || v.DailyFloorKnown || v.DrawdownFloor != 0 {
				t.Fatalf("view = %+v", v)
			}
		}},
		{"no risk state yet: no daily floor", func(*testing.T, *agentEnv) {}, func(t *testing.T, v AgentView) {
			if !v.EquityKnown || v.DailyFloorKnown || v.Risk.Version != 0 || v.DrawdownFloor <= 0 {
				t.Fatalf("view = %+v", v)
			}
		}},
		{"killed, with a blocked instrument", func(t *testing.T, ae *agentEnv) {
			if err := ae.svc.trip(ae.ctx, ae.a, "test reason"); err != nil {
				t.Fatal(err)
			}
			if err := ae.store.InstrumentBlocks().Block(ae.ctx, domain.InstrumentBlock{AccountID: ae.a.ID, Instrument: instNYSE, Reason: "leverage 2", At: ae.clock.Now()}); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, v AgentView) {
			if v.State != domain.AgentKilled || !v.Risk.State.Killed || v.Risk.KillReason != "test reason" ||
				len(v.Blocked) != 1 || v.Blocked[0].Symbol != "AAPL" || v.Blocked[0].Reason != "leverage 2" {
				t.Fatalf("view = %+v", v)
			}
		}},
		{"no calendar", func(_ *testing.T, ae *agentEnv) { ae.svc.d.Calendar = nil }, func(t *testing.T, v AgentView) {
			for _, f := range v.Flatten {
				if f.Trading || f.Note == "" {
					t.Fatalf("flatten %+v", f)
				}
			}
		}},
		{"lease held by another process", func(t *testing.T, ae *agentEnv) {
			now := ae.clock.Now()
			_ = ae.store.AgentLeases().Release(ae.ctx, ae.a.ID, ae.svc.leaseHolder())
			if err := ae.store.AgentLeases().Acquire(ae.ctx, ae.a.ID, "other", now, now.Add(domain.LeaseTTL)); err != nil {
				t.Fatal(err)
			}
		}, func(t *testing.T, v AgentView) {
			if v.Lease.Mine || !v.Lease.Other {
				t.Fatalf("lease %+v", v.Lease)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			tt.setup(t, ae)
			v, err := ae.svc.AgentStatusView(ae.ctx, ae.admin, ae.a.ID)
			if err != nil {
				t.Fatal(err)
			}
			tt.check(t, v)
		})
	}
}

func TestAgentControlsFor(t *testing.T) {
	all := AgentControls{Start: true, Pause: true, Stop: true, Kill: true, Reset: true, ForceClose: true}
	tests := []struct {
		name string
		who  func(t *testing.T, ae *agentEnv) Principal
		want AgentControls
		err  error
	}{
		{"owner admin, web session", func(_ *testing.T, ae *agentEnv) Principal { return ae.admin }, all, nil},
		{"owner admin, API key", func(_ *testing.T, ae *agentEnv) Principal { return apiKey(ae.admin) }, AgentControls{Pause: true, Stop: true, Kill: true}, nil},
		{"operator with trade access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "op", domain.RoleOperator, domain.AccessTrade)
		}, AgentControls{Start: true, Pause: true, Stop: true, Kill: true}, nil},
		{"admin with view access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "ad", domain.RoleAdmin, domain.AccessView)
		}, AgentControls{}, nil},
		{"viewer with trade access", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "vi", domain.RoleViewer, domain.AccessTrade)
		}, AgentControls{}, nil},
		{"operator without access", func(t *testing.T, ae *agentEnv) Principal { return ae.user(t, "op", domain.RoleOperator) }, AgentControls{}, domain.ErrNotFound},
		{"nobody", func(*testing.T, *agentEnv) Principal { return Principal{} }, AgentControls{}, domain.ErrUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			v, err := ae.svc.AgentStatusView(ae.ctx, tt.who(t, ae), ae.a.ID)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want %v", err, tt.err)
				}
				return
			}
			if err != nil || v.Controls != tt.want {
				t.Fatalf("controls = %+v, want %+v (%v)", v.Controls, tt.want, err)
			}
		})
	}
}

func TestAgentControlsRealAccountGated(t *testing.T) {
	ae := newAgentEnv(t)
	realAcct := ae.account(t, "Real", domain.ModeReal)
	v, err := ae.svc.AgentStatusView(ae.ctx, ae.admin, realAcct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Controls.Gated || v.Controls.Start || !v.Controls.Kill {
		t.Fatalf("controls = %+v", v.Controls)
	}
}

func TestOrdersList(t *testing.T) {
	ae := newAgentEnv(t)
	ae.tick(t)
	tests := []struct {
		name  string
		who   func(t *testing.T) Principal
		limit int
		want  int
		err   error
	}{
		{"default limit", func(*testing.T) Principal { return ae.admin }, 0, 1, nil},
		{"limit 1", func(*testing.T) Principal { return ae.admin }, 1, 1, nil},
		{"limit too high", func(*testing.T) Principal { return ae.admin }, 501, 0, domain.ErrInvalid},
		{"negative limit", func(*testing.T) Principal { return ae.admin }, -1, 0, domain.ErrInvalid},
		{"viewer with view access", func(t *testing.T) Principal { return ae.shared(t, "vi", domain.RoleViewer, domain.AccessView) }, 0, 1, nil},
		{"no access", func(t *testing.T) Principal { return ae.user(t, "nope", domain.RoleOperator) }, 0, 0, domain.ErrNotFound},
		{"nobody", func(*testing.T) Principal { return Principal{} }, 0, 0, domain.ErrUnauthenticated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ae.svc.Orders(ae.ctx, tt.who(t), ae.a.ID, tt.limit)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want %v", err, tt.err)
				}
				return
			}
			if err != nil || len(got) != tt.want {
				t.Fatalf("orders = %+v, %v", got, err)
			}
			if o := got[0]; o.Symbol != "AAPL" || o.Kind != domain.OrderOpen || o.State != domain.OrderFilled || o.RequestID != ae.b.Intents[0].RequestID {
				t.Fatalf("order = %+v", o)
			}
		})
	}
}

func TestReconcileAccount(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T, ae *agentEnv) Principal
		ok     bool
		err    error
		reason string
	}{
		{"clean", func(_ *testing.T, ae *agentEnv) Principal { return ae.admin }, true, nil, ""},
		{"API key allowed", func(_ *testing.T, ae *agentEnv) Principal { return apiKey(ae.admin) }, true, nil, ""},
		{"broker down: not OK, no error", func(_ *testing.T, ae *agentEnv) Principal {
			ae.b.PortfolioErr = fmt.Errorf("HTTP 503: %w", domain.ErrUnavailable)
			return ae.admin
		}, false, nil, "reading the portfolio failed"},
		{"viewer refused", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "vi", domain.RoleViewer, domain.AccessTrade)
		}, false, domain.ErrForbidden, ""},
		{"operator with view access refused", func(t *testing.T, ae *agentEnv) Principal {
			return ae.shared(t, "op", domain.RoleOperator, domain.AccessView)
		}, false, domain.ErrForbidden, ""},
		{"supervisor busy", func(_ *testing.T, ae *agentEnv) Principal {
			ae.svc.rt.claim(ae.a.ID)
			return ae.admin
		}, false, domain.ErrConflict, ""},
		{"no keys", func(t *testing.T, ae *agentEnv) Principal {
			ak, _ := domain.AccountKeyNames(ae.a)
			if err := ae.sec.Delete(ae.ctx, ak); err != nil {
				t.Fatal(err)
			}
			return ae.admin
		}, false, domain.ErrUnavailable, ""},
		{"no eToro client", func(_ *testing.T, ae *agentEnv) Principal {
			ae.svc.d.Broker = nil
			return ae.admin
		}, false, domain.ErrUnavailable, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ae := newAgentEnv(t)
			p := tt.setup(t, ae)
			r, err := ae.svc.ReconcileAccount(ae.ctx, p, ae.a.ID)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("err = %v, want %v", err, tt.err)
				}
				return
			}
			if err != nil || r.OK != tt.ok || (tt.reason != "" && !strings.Contains(r.Reason, tt.reason)) || r.At.IsZero() {
				t.Fatalf("reconcile = %+v, %v", r, err)
			}
			if got := ae.svc.Reconciled(ae.a.ID); got.OK != tt.ok {
				t.Fatalf("stored reconciliation %+v", got)
			}
		})
	}
}

func TestReleaseLeases(t *testing.T) {
	ae := newAgentEnv(t)
	other := ae.account(t, "Other", domain.ModeDemo)
	now := ae.clock.Now()
	if err := ae.svc.holdLease(ae.ctx, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	if err := ae.store.AgentLeases().Acquire(ae.ctx, other.ID, "another process", now, now.Add(domain.LeaseTTL)); err != nil {
		t.Fatal(err)
	}
	if err := ae.svc.ReleaseLeases(ae.ctx); err != nil {
		t.Fatal(err)
	}
	// ours is free at once for another holder; the other process's lease is untouched
	if err := ae.store.AgentLeases().Acquire(ae.ctx, ae.a.ID, "successor", now, now.Add(domain.LeaseTTL)); err != nil {
		t.Fatalf("our lease was not released: %v", err)
	}
	if l, err := ae.store.AgentLeases().Get(ae.ctx, other.ID); err != nil || l.Holder != "another process" {
		t.Fatalf("other lease = %+v, %v", l, err)
	}
}
