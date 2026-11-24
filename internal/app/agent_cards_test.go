package app

import (
	"errors"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

func TestAgentCards(t *testing.T) {
	ae := newAgentEnv(t) // "Demo": running, reconciled, current
	second := ae.account(t, "Second", domain.ModeDemo)
	realAcct := ae.account(t, "Real", domain.ModeReal)
	if _, err := ae.svc.CreateAccountOn(ae.ctx, ae.admin, domain.PlatformSaxo, "Saxo", domain.ModeDemo); err != nil {
		t.Fatal(err)
	}
	ae.openPosition(t, instNYSE, domain.Long)
	if err := ae.svc.trip(ae.ctx, realAcct, "test"); err != nil {
		t.Fatal(err)
	}

	cards, err := ae.svc.AgentCards(ae.ctx, ae.admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 3 {
		t.Fatalf("%d cards, want the 3 eToro accounts (not Saxo): %+v", len(cards), cards)
	}
	by := map[string]AgentCard{}
	for _, c := range cards {
		by[c.Account.Name] = c
	}
	demo, sec, rl := by["Demo"], by["Second"], by["Real"]
	if !demo.Current || sec.Current || rl.Current {
		t.Fatalf("current: %v %v %v", demo.Current, sec.Current, rl.Current)
	}
	if demo.State != domain.AgentRunning || !demo.Reconciliation.OK || demo.LimitsEditable ||
		demo.LimitsBlocked != "Stop the agent to change the limits (it is running)" || demo.OpenPositions != 1 {
		t.Fatalf("demo card %+v", demo)
	}
	if sec.Account.ID != second.ID || sec.State != domain.AgentStopped || sec.Reconciliation.OK || !sec.LimitsEditable || sec.LimitsBlocked != "" {
		t.Fatalf("second card %+v", sec)
	}
	if !rl.Killed || rl.State != domain.AgentKilled || rl.LimitsEditable || !rl.Controls.Gated || rl.Controls.ForceClose || rl.Controls.ForceCloseBlocked == "" {
		t.Fatalf("real card %+v", rl)
	}
	// the controls are the panel's own
	for _, c := range cards {
		v, err := ae.svc.AgentStatusView(ae.ctx, ae.admin, c.Account.ID)
		if err != nil || v.Controls != c.Controls {
			t.Fatalf("%s: card controls %+v, panel %+v (%v)", c.Account.Name, c.Controls, v.Controls, err)
		}
	}

	// a viewer sees the shared account's card without controls; an API key cannot edit limits
	vi := ae.shared(t, "vi", domain.RoleViewer, domain.AccessTrade)
	cards, err = ae.svc.AgentCards(ae.ctx, vi)
	if err != nil || len(cards) != 1 || cards[0].Controls != (AgentControls{}) || cards[0].LimitsEditable ||
		cards[0].LimitsBlocked != msgLimitsNotOwner || !cards[0].Current {
		t.Fatalf("viewer cards %+v %v", cards, err)
	}
	cards, err = ae.svc.AgentCards(ae.ctx, apiKey(ae.admin))
	if err != nil || len(cards) != 3 || cards[0].LimitsBlocked != msgLimitsNotSession {
		t.Fatalf("API key cards %+v %v", cards, err)
	}
	if _, err := ae.svc.AgentCards(ae.ctx, Principal{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("nobody: %v", err)
	}
}

func TestWatchlistCounts(t *testing.T) {
	ae := newAgentEnv(t, instNYSE, instLSE) // "Demo", current, owned by admin
	empty := ae.account(t, "Empty", domain.ModeDemo)
	// albie's account with shares, not shared with admin, then shared
	albie := ae.user(t, "albie", domain.RoleAdmin)
	theirs, err := ae.svc.CreateAccount(ae.ctx, albie, "Albie demo", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	if err := ae.store.Watchlist().Add(ae.ctx, theirs.ID, instASX, "albie", ae.clock.Now()); err != nil {
		t.Fatal(err)
	}
	counts := func(p Principal) map[string]WatchlistCount {
		t.Helper()
		cs, err := ae.svc.WatchlistCounts(ae.ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]WatchlistCount{}
		for _, c := range cs {
			out[c.Account.Name] = c
		}
		return out
	}
	got := counts(ae.admin)
	if len(got) != 2 || got["Demo"].Count != 2 || !got["Demo"].Current || got["Empty"].Count != 0 || got["Empty"].Account.ID != empty.ID {
		t.Fatalf("admin counts %+v", got)
	}
	if _, seen := got["Albie demo"]; seen {
		t.Fatal("an unshared account was counted")
	}
	if err := ae.svc.ShareAccount(ae.ctx, albie, theirs.ID, "admin", domain.AccessView); err != nil {
		t.Fatal(err)
	}
	if got := counts(ae.admin); got["Albie demo"].Count != 1 || got["Albie demo"].Current {
		t.Fatalf("after sharing %+v", got)
	}
	if _, err := ae.svc.WatchlistCounts(ae.ctx, Principal{}); !errors.Is(err, domain.ErrUnauthenticated) {
		t.Fatalf("nobody: %v", err)
	}
}
