package app

import (
	"context"
	"errors"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

type fakeReviewer struct {
	v      domain.Verdict
	err    error
	gotKey string
	gotCfg domain.LLMConfig
}

func (f *fakeReviewer) Review(_ context.Context, c domain.LLMConfig, k string, _ ports.ReviewInput) (domain.Verdict, error) {
	f.gotCfg, f.gotKey = c, k
	return f.v, f.err
}

func (f *fakeReviewer) Models(_ context.Context, _ domain.LLMConfig, k string) ([]string, error) {
	f.gotKey = k
	return []string{"m1", "m2"}, f.err
}

func TestLLMSetup(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	viewer := e.user(t, "vera", domain.RoleViewer)
	st, err := e.svc.LLMSetup(ctx, viewer)
	if err != nil || st.Config.Provider != "anthropic" || st.Config.Enabled || st.KeySet {
		t.Fatalf("default setup = %+v %v", st, err)
	}
	if _, err := e.svc.TestLLM(ctx, e.admin); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("test without a reviewer")
	}
	if _, err := e.svc.LLMModels(ctx, e.admin, domain.LLMConfig{Provider: "openai"}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("models without a reviewer")
	}
	r := &fakeReviewer{v: domain.Verdict{Kind: domain.VerdictReduce, SizeMultiplier: 4, Reason: "r"}}
	e.svc.d.Reviewer = r
	cfg := domain.LLMConfig{Enabled: true, Provider: "deepseek", Model: "deepseek-chat"}
	if _, err := e.svc.SetLLMConfig(ctx, viewer, cfg); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer changed the reviewer")
	}
	key := e.admin
	key.Via = ViaAPIKey
	if _, err := e.svc.SetLLMConfig(ctx, key, cfg); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("API key changed the reviewer")
	}
	if _, err := e.svc.SetLLMConfig(ctx, e.admin, domain.LLMConfig{Provider: "openai"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid setup accepted")
	}
	if _, err := e.svc.SetLLMConfig(ctx, e.admin, cfg); err != nil {
		t.Fatal(err)
	}
	_ = e.sec.Put(ctx, domain.CredLLMAPIKey, "sk-1")
	if st, _ := e.svc.LLMSetup(ctx, viewer); st.Config != cfg || !st.KeySet {
		t.Fatalf("stored = %+v", st)
	}
	res, err := e.svc.TestLLM(ctx, e.admin)
	if err != nil || !res.OK || res.Verdict.Kind != domain.VerdictReduce || res.Verdict.SizeMultiplier != 1 || r.gotKey != "sk-1" || r.gotCfg != cfg {
		t.Fatalf("test = %+v %v (multiplier must be clamped to 1)", res, err)
	}
	ms, err := e.svc.LLMModels(ctx, e.admin, domain.LLMConfig{Provider: "openai"})
	if err != nil || len(ms) != 2 {
		t.Fatalf("models = %v %v", ms, err)
	}
	if _, err := e.svc.LLMModels(ctx, e.admin, domain.LLMConfig{Provider: "nope"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("models for an unknown provider")
	}
	r.err = errors.New("HTTP 401: bad key")
	if res, err := e.svc.TestLLM(ctx, e.admin); err != nil || res.OK || res.Error == "" {
		t.Fatalf("failed test = %+v %v", res, err)
	}
	if _, err := e.svc.LLMModels(ctx, e.admin, domain.LLMConfig{Provider: "openai"}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("models failure")
	}
	if _, err := e.svc.TestLLM(ctx, viewer); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer tested")
	}
	// a stored setup that no longer validates is never enabled
	_ = e.store.Settings().Set(ctx, settingLLM, `{"enabled":true,"provider":"openai"}`, e.clock.T)
	if st, _ := e.svc.LLMSetup(ctx, viewer); st.Config.Enabled {
		t.Fatal("invalid stored setup enabled")
	}
	if _, err := e.svc.TestLLM(ctx, e.admin); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("test of an invalid setup")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_ = e.store.Settings().Set(ctx, settingLLM, `{"enabled":true,"provider":"openai","model":"m"}`, e.clock.T)
	if _, err := e.svc.TestLLM(cctx, e.admin); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled test: %v", err)
	}
	_ = e.store.Settings().Set(ctx, settingLLM, `garbage`, e.clock.T)
	if st, _ := e.svc.LLMSetup(ctx, viewer); st.Config.Provider != "anthropic" {
		t.Fatal("garbage setup")
	}
}
