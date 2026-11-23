package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

func TestRiskLimits(t *testing.T) {
	ctx := context.Background()
	e, b := withBroker(t) // equity 106,100 cents
	viewer := e.user(t, "vera", domain.RoleViewer)
	l, err := e.svc.RiskLimits(ctx, viewer)
	if err != nil || l != domain.DefaultRiskLimits || l.TradingBudget != 0 {
		t.Fatalf("defaults = %+v %v", l, err)
	}
	next := domain.DefaultRiskLimits
	next.TradingBudget, next.DailyLoss = 50_000, 150
	if _, err := e.svc.SetRiskLimits(ctx, viewer, next); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer changed limits")
	}
	key := e.admin
	key.Via = ViaAPIKey
	if _, err := e.svc.SetRiskLimits(ctx, key, next); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("an API key changed limits")
	}
	if _, err := e.svc.SetRiskLimits(ctx, e.admin, next); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.svc.RiskLimits(ctx, viewer); got != next {
		t.Fatalf("stored = %+v", got)
	}
	audit := e.store.AuditEntries()
	if a := audit[len(audit)-1]; a.Action != "risk.limits_changed" || !strings.Contains(a.Details, `"dailyLossBp":150`) || !strings.Contains(a.Details, `"dailyLossBp":200`) {
		t.Fatalf("audit = %+v", a)
	}
	// budget above equity, invalid limits, eToro down
	tooBig := next
	tooBig.TradingBudget = 200_000
	if _, err := e.svc.SetRiskLimits(ctx, e.admin, tooBig); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("budget above equity: %v", err)
	}
	bad := next
	bad.MaxOpenPositions = 99
	if _, err := e.svc.SetRiskLimits(ctx, e.admin, bad); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid limits accepted")
	}
	b.Err = domain.ErrUnavailable
	if _, err := e.svc.SetRiskLimits(ctx, e.admin, next); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("budget set without checking equity")
	}
	zero := next
	zero.TradingBudget = 0
	if _, err := e.svc.SetRiskLimits(ctx, e.admin, zero); err != nil {
		t.Fatalf("a zero budget needs no eToro: %v", err)
	}
	// only while stopped
	for _, st := range []domain.AgentState{domain.AgentRunning, domain.AgentPaused, domain.AgentKilled} {
		_ = e.store.Settings().Set(ctx, settingAgentState, string(st), e.clock.T)
		if _, err := e.svc.SetRiskLimits(ctx, e.admin, zero); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("changed while %s: %v", st, err)
		}
		if got := e.svc.AgentState(ctx); got != st {
			t.Fatalf("state = %s", got)
		}
	}
	_ = e.store.Settings().Set(ctx, settingAgentState, "bogus", e.clock.T)
	if e.svc.AgentState(ctx) != domain.AgentStopped {
		t.Fatal("unknown agent state must read as stopped")
	}
	// corrupted stored limits fall back to the defaults with no budget
	for _, v := range []string{"{not json", `{"tradingBudgetCents":5,"dailyLossBp":0}`} {
		_ = e.store.Settings().Set(ctx, settingRiskLimits, v, e.clock.T)
		if got, _ := e.svc.RiskLimits(ctx, viewer); got != domain.DefaultRiskLimits {
			t.Fatalf("bad stored limits %q → %+v", v, got)
		}
	}
}
