package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/strategy"
)

func TestThemeSharesAndReviewer(t *testing.T) {
	ctx := context.Background()
	e, b := withBroker(t)
	e.svc.d.Calendar = openCalendar{}
	r := &fakeReviewer{v: domain.Verdict{Kind: domain.VerdictReject, Reason: "too extended"}}
	e.svc.d.Reviewer = r
	def, err := e.svc.SaveLLMProfile(ctx, e.admin, domain.LLMProfile{Name: "Default", LLMConfig: domain.LLMConfig{Provider: "openai", Model: "gpt"}}, sp("k-def"))
	if err != nil {
		t.Fatal(err)
	}
	own, err := e.svc.SaveLLMProfile(ctx, e.admin, domain.LLMProfile{Name: "Space desk", LLMConfig: domain.LLMConfig{Provider: "deepseek", Model: "ds"}}, sp("k-space"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Bad", Instruments: []domain.InstrumentID{1}, LLMProfileID: "nope"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unknown LLM profile accepted on a theme")
	}
	th, err := e.svc.SaveTheme(ctx, e.admin, domain.Theme{Name: "Space", Instruments: []domain.InstrumentID{1, 2}, Enabled: true, LLMProfileID: own.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Shares) != 2 || th.Shares[0].Symbol != "AAPL" || th.Shares[1].Symbol != "BARC.L" || th.LLMProfileID != own.ID {
		t.Fatalf("theme view = %+v", th)
	}
	// a share missing from the cache still shows, by id
	view := e.svc.themeView(ctx, domain.Theme{Instruments: []domain.InstrumentID{424242}})
	if view.Shares[0].Symbol != "#424242" {
		t.Fatalf("missing share = %+v", view.Shares)
	}
	e.svc.evalOverride = func(id domain.InstrumentID, _ []domain.Candle) strategy.Decision {
		if id == 1 {
			return strategy.Decision{Candidate: &domain.Candidate{Instrument: 1, Direction: domain.Long, Playbook: "trend_breakout", Reason: "up"}}
		}
		return strategy.Decision{Reason: "none"}
	}
	b.Bars = map[domain.InstrumentID][]domain.Candle{1: bars(60), 2: bars(60)}
	review := func() domain.Review {
		t.Helper()
		if _, err := e.svc.ScanNow(ctx, e.admin); err != nil {
			t.Fatal(err)
		}
		rs, _, _ := e.svc.Reviews(ctx, e.admin, 0)
		for _, x := range rs {
			if x.Symbol == "AAPL" {
				return x
			}
		}
		t.Fatal("AAPL not reviewed")
		return domain.Review{}
	}
	// reviewing off: no LLM call, AAPL added
	if x := review(); x.Outcome != domain.ReviewAdded || r.calls != 0 {
		t.Fatalf("reviewer off = %+v (calls %d)", x, r.calls)
	}
	_ = e.svc.RemoveFromWatchlist(ctx, e.admin, 1)
	// on: the theme's own profile is asked and its "reject" keeps AAPL off the watchlist
	if _, err := e.svc.SetLLMSettings(ctx, e.admin, domain.LLMSettings{Enabled: true, Active: def.ID}); err != nil {
		t.Fatal(err)
	}
	x := review()
	last := x.Checks[len(x.Checks)-1]
	if x.Outcome != domain.ReviewSkipped || r.gotKey != "k-space" || last.Pass || !strings.Contains(last.Rule, "Space desk") {
		t.Fatalf("theme reviewer = %+v (key %q)", x, r.gotKey)
	}
	// no profile on the theme: the default answers; approve adds it
	th.LLMProfileID = ""
	if _, err := e.svc.SaveTheme(ctx, e.admin, th.Theme); err != nil {
		t.Fatal(err)
	}
	r.v = domain.Verdict{Kind: domain.VerdictApprove, Reason: "fine"}
	if x := review(); x.Outcome != domain.ReviewAdded || r.gotKey != "k-def" || !x.Checks[len(x.Checks)-1].Pass {
		t.Fatalf("default reviewer = %+v (key %q)", x, r.gotKey)
	}
	_ = e.svc.RemoveFromWatchlist(ctx, e.admin, 1)
	// a failing reviewer is a "no" (fail closed)
	r.err = errors.New("HTTP 429: quota")
	if x := review(); x.Outcome != domain.ReviewSkipped || !strings.Contains(x.Summary, "quota") {
		t.Fatalf("failing reviewer = %+v", x)
	}
	// a deleted theme profile falls back to the default
	th.LLMProfileID = own.ID
	_, _ = e.svc.SaveTheme(ctx, e.admin, th.Theme)
	_ = e.svc.DeleteLLMProfile(ctx, e.admin, own.ID)
	_, _ = e.svc.SetLLMSettings(ctx, e.admin, domain.LLMSettings{Enabled: true, Active: def.ID})
	r.err = nil
	if x := review(); x.Outcome != domain.ReviewAdded || r.gotKey != "k-def" {
		t.Fatalf("fallback after delete = %+v (key %q)", x, r.gotKey)
	}
	// a default without a valid setup is a "no"
	_ = e.svc.RemoveFromWatchlist(ctx, e.admin, 1)
	_ = e.store.Settings().Set(ctx, settingLLMProfiles, `{"enabled":true,"active":"gone","profiles":[]}`, e.clock.T)
	if x := review(); x.Outcome != domain.ReviewSkipped {
		t.Fatalf("missing default = %+v", x)
	}
	_ = e.store.Settings().Set(ctx, settingLLMProfiles, `{"enabled":true,"active":"a","profiles":[{"id":"a","name":"A","provider":"openai"}]}`, e.clock.T)
	if x := review(); x.Outcome != domain.ReviewSkipped {
		t.Fatalf("invalid default = %+v", x)
	}
}
