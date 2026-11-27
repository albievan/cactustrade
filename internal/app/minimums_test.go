package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// minEnv is an agent env with the agent stopped, eToro minimums of $25 (long) and $50 (short)
// on the watched share, and a $100 budget whose 5 % short allowance ($5) is too small.
func minEnv(t *testing.T) *agentEnv {
	t.Helper()
	ae := newAgentEnv(t)
	in, _, err := ae.store.Instruments().Get(ae.ctx, instNYSE)
	if err != nil {
		t.Fatal(err)
	}
	el := domain.Eligibility{Instrument: instNYSE, RealLong: true, MinPositionAmount: 25_00, ShortCFD: true, MinShortAmount: 50_00}
	if err := ae.store.Instruments().Upsert(ae.ctx, in, el, ae.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := ae.svc.StopAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
		t.Fatal(err)
	}
	ae.limits(t, func(l *domain.RiskLimits) { l.TradingBudget, l.MaxShort = 100_00, 500 })
	return ae
}

func TestTradeMinimumsApprove(t *testing.T) {
	ae := minEnv(t)
	before, _ := ae.svc.RiskLimits(ae.ctx, ae.admin)
	if v, err := ae.svc.TradeMinimums(ae.ctx, ae.admin); err != nil || v.Review != nil {
		t.Fatalf("before any check: %+v %v", v, err)
	}
	v, err := ae.svc.CheckTradeMinimums(ae.ctx, ae.admin)
	if err != nil {
		t.Fatal(err)
	}
	r := v.Review
	if r == nil || r.Status != domain.MinimumsPending || r.Minimums.Long != 25_00 || r.Minimums.Short != 50_00 || !v.CanDecide {
		t.Fatalf("review %+v / %+v", r, v)
	}
	// $50 needs 50 % of a $100 budget: over the 25 % cap, so the budget is proposed at $200.
	if p := r.Fit.Proposed; p.TradingBudget != 200_00 || p.MaxShort != domain.HardMaxShort {
		t.Fatalf("proposed %+v", p)
	}
	// Checking changes no limit: only the owner's approval does.
	if now, _ := ae.svc.RiskLimits(ae.ctx, ae.admin); now != before {
		t.Fatalf("limits changed by a check: %+v", now)
	}
	if _, err := ae.svc.DecideTradeMinimums(ae.ctx, ae.admin, "other", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("wrong id: %v", err)
	}
	got, err := ae.svc.DecideTradeMinimums(ae.ctx, ae.admin, r.ID, true)
	if err != nil || got.Review.Status != domain.MinimumsApproved || got.Review.DecidedBy != ae.admin.Username {
		t.Fatalf("approve: %+v %v", got, err)
	}
	if now, _ := ae.svc.RiskLimits(ae.ctx, ae.admin); now != r.Fit.Proposed {
		t.Fatalf("limits %+v, want the proposal %+v", now, r.Fit.Proposed)
	}
	if _, err := ae.svc.DecideTradeMinimums(ae.ctx, ae.admin, r.ID, true); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("second approval: %v", err)
	}
	// The limits now fit: a new check proposes nothing.
	if v, err := ae.svc.CheckTradeMinimums(ae.ctx, ae.admin); err != nil || v.Review.Status != domain.MinimumsFit {
		t.Fatalf("after approval: %+v %v", v.Review, err)
	}
}

func TestTradeMinimumsRefusals(t *testing.T) {
	t.Run("declined: nothing changes", func(t *testing.T) {
		ae := minEnv(t)
		before, _ := ae.svc.RiskLimits(ae.ctx, ae.admin)
		v, _ := ae.svc.CheckTradeMinimums(ae.ctx, ae.admin)
		got, err := ae.svc.DecideTradeMinimums(ae.ctx, ae.admin, v.Review.ID, false)
		if err != nil || got.Review.Status != domain.MinimumsDeclined {
			t.Fatalf("decline: %+v %v", got, err)
		}
		if now, _ := ae.svc.RiskLimits(ae.ctx, ae.admin); now != before {
			t.Fatalf("limits changed by a decline")
		}
	})
	t.Run("limits changed since the check", func(t *testing.T) {
		ae := minEnv(t)
		v, _ := ae.svc.CheckTradeMinimums(ae.ctx, ae.admin)
		ae.limits(t, func(l *domain.RiskLimits) { l.TradingBudget, l.MaxShort = 150_00, 500 })
		if _, err := ae.svc.DecideTradeMinimums(ae.ctx, ae.admin, v.Review.ID, true); !errors.Is(err, domain.ErrConflict) {
			t.Fatalf("stale review approved: %v", err)
		}
	})
	t.Run("agent running: approval refused", func(t *testing.T) {
		ae := minEnv(t)
		v, _ := ae.svc.CheckTradeMinimums(ae.ctx, ae.admin)
		before, _ := ae.svc.RiskLimits(ae.ctx, ae.admin)
		if _, err := ae.svc.StartAgent(ae.ctx, ae.admin, ae.a.ID); err != nil {
			t.Fatal(err)
		}
		if got, _ := ae.svc.TradeMinimums(ae.ctx, ae.admin); got.CanDecide || got.DecideHint == "" {
			t.Fatalf("can decide while the agent runs: %+v", got)
		}
		if _, err := ae.svc.DecideTradeMinimums(ae.ctx, ae.admin, v.Review.ID, true); err == nil {
			t.Fatal("approved while the agent runs")
		}
		if now, _ := ae.svc.RiskLimits(ae.ctx, ae.admin); now != before {
			t.Fatalf("limits changed")
		}
	})
	t.Run("not the owner", func(t *testing.T) {
		ae := minEnv(t)
		op := ae.user(t, "op2", domain.RoleOperator)
		if _, err := ae.svc.CheckTradeMinimums(ae.ctx, op); err == nil {
			t.Fatal("a non-owner checked")
		}
	})
	t.Run("api key: approval refused", func(t *testing.T) {
		ae := minEnv(t)
		v, _ := ae.svc.CheckTradeMinimums(ae.ctx, ae.admin)
		key := ae.admin
		key.Via = ViaAPIKey
		if _, err := ae.svc.DecideTradeMinimums(ae.ctx, key, v.Review.ID, true); err == nil {
			t.Fatal("approved with an API key")
		}
	})
	t.Run("nothing checked yet", func(t *testing.T) {
		ae := minEnv(t)
		in, _, _ := ae.store.Instruments().Get(ae.ctx, instNYSE)
		if err := ae.store.Instruments().Upsert(ae.ctx, in, domain.Eligibility{Instrument: instNYSE}, ae.clock.Now()); err != nil {
			t.Fatal(err)
		}
		v, err := ae.svc.CheckTradeMinimums(ae.ctx, ae.admin)
		if err != nil || v.Review.Status != domain.MinimumsBlocked || !strings.Contains(strings.Join(v.Review.Fit.Problems, ";"), "watchlist") {
			t.Fatalf("%+v %v", v.Review, err)
		}
	})
}

// TestTradeMinimumsAfterKeys: new eToro keys check the minimums at once (a pending review,
// limits unchanged).
func TestTradeMinimumsAfterKeys(t *testing.T) {
	ae := minEnv(t)
	before, _ := ae.svc.RiskLimits(ae.ctx, ae.admin)
	if err := ae.svc.SetAccountKeys(ae.ctx, ae.admin, ae.a.ID, "api-key-1", "user-key-1"); err != nil {
		t.Fatal(err)
	}
	v, err := ae.svc.TradeMinimums(ae.ctx, ae.admin)
	if err != nil || v.Review == nil || v.Review.Status != domain.MinimumsPending {
		t.Fatalf("after keys: %+v %v", v.Review, err)
	}
	if now, _ := ae.svc.RiskLimits(ae.ctx, ae.admin); now != before {
		t.Fatalf("limits changed by new keys")
	}
}
