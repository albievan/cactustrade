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
	calls  int
}

func (f *fakeReviewer) Review(_ context.Context, c domain.LLMConfig, k string, _ ports.ReviewInput) (domain.Verdict, domain.LLMCall, error) {
	f.gotCfg, f.gotKey = c, k
	f.calls++
	call := domain.LLMCall{Provider: c.Provider, Model: "served-" + c.Model, Prompt: "{}", InputTokens: 100, OutputTokens: 20}
	if f.err != nil {
		call.Error = f.err.Error()
	}
	return f.v, call, f.err
}

func (f *fakeReviewer) Models(_ context.Context, _ domain.LLMConfig, k string) ([]string, error) {
	f.gotKey = k
	return []string{"m1", "m2"}, f.err
}

func sp(s string) *string { return &s }

func TestLLMProfiles(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	viewer := e.user(t, "vera", domain.RoleViewer)
	st, err := e.svc.LLMSetup(ctx, viewer)
	if err != nil || st.Settings.Enabled || len(st.Profiles) != 0 {
		t.Fatalf("empty setup = %+v %v", st, err)
	}
	if _, err := e.svc.TestLLM(ctx, e.admin, "x"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("test without a reviewer")
	}
	if _, err := e.svc.LLMModels(ctx, e.admin, LLMModelsRequest{Config: domain.LLMConfig{Provider: "openai"}}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("models without a reviewer")
	}
	r := &fakeReviewer{v: domain.Verdict{Kind: domain.VerdictReduce, SizeBp: 40_000, Reason: "r"}}
	e.svc.d.LLM = r

	ds := domain.LLMProfile{Name: "DeepSeek chat", LLMConfig: domain.LLMConfig{Provider: "deepseek", Model: "deepseek-chat"}}
	if _, err := e.svc.SaveLLMProfile(ctx, viewer, ds, nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer added a profile")
	}
	api := e.admin
	api.Via = ViaAPIKey
	if _, err := e.svc.SaveLLMProfile(ctx, api, ds, nil); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("API key added a profile")
	}
	if _, err := e.svc.SaveLLMProfile(ctx, e.admin, domain.LLMProfile{LLMConfig: domain.LLMConfig{Provider: "openai"}}, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("profile without a model accepted")
	}
	if _, err := e.svc.SaveLLMProfile(ctx, e.admin, ds, sp("bad key")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("key with a space accepted")
	}
	p1, err := e.svc.SaveLLMProfile(ctx, e.admin, ds, sp("sk-1"))
	if err != nil || p1.ID == "" || !p1.KeySet {
		t.Fatalf("create = %+v %v", p1, err)
	}
	// the first profile becomes the default; a nameless one is named after provider and model
	p2, err := e.svc.SaveLLMProfile(ctx, e.admin, domain.LLMProfile{LLMConfig: domain.LLMConfig{Provider: "openrouter", Model: "x/y"}}, nil)
	if err != nil || p2.Name != "OpenRouter · x/y" || p2.KeySet {
		t.Fatalf("second = %+v %v", p2, err)
	}
	st, _ = e.svc.LLMSetup(ctx, viewer)
	if len(st.Profiles) != 2 || st.Settings.Active != p1.ID || st.Settings.Enabled {
		t.Fatalf("setup = %+v", st)
	}

	// switching on needs a profile with a key
	if _, err := e.svc.SetLLMSettings(ctx, e.admin, domain.LLMSettings{Enabled: true}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("on without a profile")
	}
	if _, err := e.svc.SetLLMSettings(ctx, e.admin, domain.LLMSettings{Enabled: true, Active: p2.ID}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("on with a keyless profile")
	}
	if _, err := e.svc.SetLLMSettings(ctx, e.admin, domain.LLMSettings{Active: "nope"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unknown default")
	}
	if _, err := e.svc.SetLLMSettings(ctx, viewer, domain.LLMSettings{}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer switched")
	}
	if _, err := e.svc.SetLLMSettings(ctx, e.admin, domain.LLMSettings{Enabled: true, Active: p1.ID}); err != nil {
		t.Fatal(err)
	}
	cfg, key, on, err := e.svc.ActiveReviewer(ctx)
	if err != nil || !on || key != "sk-1" || cfg.Provider != "deepseek" || !cfg.Enabled {
		t.Fatalf("active = %+v %q %v %v", cfg, key, on, err)
	}

	// test with the stored key; the multiplier is clamped
	res, err := e.svc.TestLLM(ctx, e.admin, p1.ID)
	if err != nil || !res.OK || res.Verdict.SizeBp != domain.FullSize || r.gotKey != "sk-1" || r.gotCfg.Model != "deepseek-chat" ||
		res.Model != "served-deepseek-chat" || res.InputTokens != 100 || res.OutputTokens != 20 {
		t.Fatalf("test = %+v %v", res, err)
	}
	if _, err := e.svc.TestLLM(ctx, e.admin, p2.ID); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("test of a keyless profile")
	}
	if _, err := e.svc.TestLLM(ctx, e.admin, "nope"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("test of a missing profile")
	}
	if _, err := e.svc.TestLLM(ctx, viewer, p1.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer tested")
	}

	// models: typed key, stored key (same provider only), none
	if ms, err := e.svc.LLMModels(ctx, e.admin, LLMModelsRequest{Config: domain.LLMConfig{Provider: "openai"}, Key: "typed"}); err != nil || len(ms) != 2 || r.gotKey != "typed" {
		t.Fatalf("models with a typed key = %v %v", ms, err)
	}
	if _, err := e.svc.LLMModels(ctx, e.admin, LLMModelsRequest{ProfileID: p1.ID, Config: domain.LLMConfig{Provider: "deepseek"}}); err != nil || r.gotKey != "sk-1" {
		t.Fatalf("models with the stored key: %v", err)
	}
	if _, err := e.svc.LLMModels(ctx, e.admin, LLMModelsRequest{ProfileID: p1.ID, Config: domain.LLMConfig{Provider: "openai"}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("stored key sent to another provider")
	}
	if _, err := e.svc.LLMModels(ctx, e.admin, LLMModelsRequest{ProfileID: "nope", Config: domain.LLMConfig{Provider: "openai"}}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("models for a missing profile")
	}
	if _, err := e.svc.LLMModels(ctx, e.admin, LLMModelsRequest{Config: domain.LLMConfig{Provider: "openai"}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("models without any key")
	}
	if _, err := e.svc.LLMModels(ctx, e.admin, LLMModelsRequest{Config: domain.LLMConfig{Provider: "nope"}}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("models for an unknown provider")
	}

	// changing provider keeps no stored key: it must be entered again
	moved := ds
	moved.ID, moved.Provider, moved.Model = p1.ID, "openai", "gpt"
	if _, err := e.svc.SaveLLMProfile(ctx, e.admin, moved, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("stored key moved to another provider")
	}
	if _, err := e.svc.SaveLLMProfile(ctx, e.admin, moved, sp("sk-2")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.SaveLLMProfile(ctx, e.admin, domain.LLMProfile{ID: "nope", Name: "n", LLMConfig: ds.LLMConfig}, nil); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("update of a missing profile")
	}
	// removing the default's key switches reviewing off
	if _, err := e.svc.SaveLLMProfile(ctx, e.admin, moved, sp("")); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.LLMSetup(ctx, viewer); st.Settings.Enabled || st.Profiles[0].KeySet {
		t.Fatalf("after key removal = %+v", st)
	}

	// failures are results, a cancelled caller is an error
	_, _ = e.svc.SaveLLMProfile(ctx, e.admin, moved, sp("sk-3"))
	r.err = errors.New("HTTP 401: bad key")
	if res, err := e.svc.TestLLM(ctx, e.admin, p1.ID); err != nil || res.OK || res.Error == "" {
		t.Fatalf("failed test = %+v %v", res, err)
	}
	if _, err := e.svc.LLMModels(ctx, e.admin, LLMModelsRequest{Config: domain.LLMConfig{Provider: "openai"}, Key: "k"}); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("models failure")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.svc.TestLLM(cctx, e.admin, p1.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled test: %v", err)
	}

	// deleting the default switches reviewing off and removes the key
	_, _ = e.svc.SetLLMSettings(ctx, e.admin, domain.LLMSettings{Enabled: true, Active: p1.ID})
	if err := e.svc.DeleteLLMProfile(ctx, viewer, p1.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer deleted")
	}
	if err := e.svc.DeleteLLMProfile(ctx, e.admin, p1.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DeleteLLMProfile(ctx, e.admin, p1.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("deleted twice")
	}
	if _, err := e.sec.Get(ctx, domain.LLMKeyName(p1.ID)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("key left behind")
	}
	st, _ = e.svc.LLMSetup(ctx, viewer)
	if st.Settings.Enabled || st.Settings.Active != "" || len(st.Profiles) != 1 {
		t.Fatalf("after delete = %+v", st)
	}
	if _, _, on, err := e.svc.ActiveReviewer(ctx); on || err != nil {
		t.Fatal("reviewer on after delete")
	}

	// unreadable stored profiles are an error, not a silent default
	_ = e.store.Settings().Set(ctx, settingLLMProfiles, `garbage`, e.clock.T)
	if _, err := e.svc.LLMSetup(ctx, viewer); err == nil {
		t.Fatal("garbage profiles accepted")
	}
	if _, _, _, err := e.svc.ActiveReviewer(ctx); err == nil {
		t.Fatal("garbage profiles used")
	}
}

func TestActiveReviewerBrokenDefault(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	_ = e.store.Settings().Set(ctx, settingLLMProfiles, `{"enabled":true,"active":"gone","profiles":[]}`, e.clock.T)
	if _, _, on, err := e.svc.ActiveReviewer(ctx); !on || err == nil {
		t.Fatal("missing default must fail closed")
	}
	_ = e.store.Settings().Set(ctx, settingLLMProfiles, `{"enabled":true,"active":"a","profiles":[{"id":"a","name":"A","provider":"openai"}]}`, e.clock.T)
	if _, _, on, err := e.svc.ActiveReviewer(ctx); !on || !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("invalid default must fail closed")
	}
}

func TestMigrateLLMSetup(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if ok, err := e.svc.MigrateLLMSetup(ctx); ok || err != nil {
		t.Fatal("nothing to migrate")
	}
	_ = e.store.Settings().Set(ctx, settingLLMLegacy, `{"enabled":true,"provider":"openrouter","model":"a/b","timeoutSeconds":30}`, e.clock.T)
	_ = e.sec.Put(ctx, domain.CredLLMAPIKey, "sk-old")
	ok, err := e.svc.MigrateLLMSetup(ctx)
	if !ok || err != nil {
		t.Fatalf("migrate = %v %v", ok, err)
	}
	st, err := e.svc.LLMSetup(ctx, e.admin)
	if err != nil || len(st.Profiles) != 1 || !st.Settings.Enabled || st.Settings.Active != st.Profiles[0].ID ||
		st.Profiles[0].Model != "a/b" || st.Profiles[0].TimeoutSeconds != 30 || !st.Profiles[0].KeySet {
		t.Fatalf("migrated = %+v %v", st, err)
	}
	if _, err := e.sec.Get(ctx, domain.CredLLMAPIKey); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("old key kept")
	}
	if ok, err := e.svc.MigrateLLMSetup(ctx); ok || err != nil {
		t.Fatal("migrated twice")
	}
	// an unreadable legacy setup is left alone
	e2 := newEnv(t)
	_ = e2.store.Settings().Set(ctx, settingLLMLegacy, `garbage`, e2.clock.T)
	if ok, err := e2.svc.MigrateLLMSetup(ctx); ok || err != nil {
		t.Fatal("garbage migrated")
	}
}

// TestVerdictOnly: the agent tick's adapter returns the traced reviewer's verdict and error
// and drops the trace; nil stays nil.
func TestVerdictOnly(t *testing.T) {
	if VerdictOnly(nil) != nil {
		t.Fatal("nil reviewer wrapped")
	}
	for _, tt := range []struct {
		name string
		r    *fakeReviewer
	}{
		{"verdict", &fakeReviewer{v: domain.Verdict{Kind: domain.VerdictReduce, SizeBp: 2_500, Reason: "r"}}},
		{"error", &fakeReviewer{err: errors.New("HTTP 500")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v, err := VerdictOnly(tt.r).Review(context.Background(), domain.LLMConfig{Provider: "openai", Model: "m"}, "k", ports.ReviewInput{})
			if v != tt.r.v || !errors.Is(err, tt.r.err) || (tt.r.err == nil) != (err == nil) || tt.r.calls != 1 || tt.r.gotKey != "k" {
				t.Fatalf("VerdictOnly = %+v %v", v, err)
			}
		})
	}
}

// TestLLMProfilePricesStored: a profile's prices survive the settings round trip, invalid
// prices are refused, and profiles stored before prices existed read as unknown (0).
func TestLLMProfilePricesStored(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	in := domain.LLMProfile{
		Name: "Priced", LLMConfig: domain.LLMConfig{Provider: "openai", Model: "m"},
		InputPrice: 3_000_000, OutputPrice: 15_000_000,
	}
	p, err := e.svc.SaveLLMProfile(ctx, e.admin, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.svc.LLMSetup(ctx, e.admin)
	if err != nil || len(st.Profiles) != 1 || st.Profiles[0].InputPrice != 3_000_000 || st.Profiles[0].OutputPrice != 15_000_000 {
		t.Fatalf("stored = %+v %v", st, err)
	}
	if got := st.Profiles[0].Cost(1_000, 1_000); got != 18_000 {
		t.Fatalf("cost = %d", got)
	}
	bad := in
	bad.ID, bad.InputPrice = p.ID, -1
	if _, err := e.svc.SaveLLMProfile(ctx, e.admin, bad, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("negative price: %v", err)
	}
	_ = e.store.Settings().Set(ctx, settingLLMProfiles, `{"enabled":false,"active":"a","profiles":[{"id":"a","name":"A","provider":"openai","model":"m"}]}`, e.clock.T)
	if st, err := e.svc.LLMSetup(ctx, e.admin); err != nil || st.Profiles[0].InputPrice != 0 || st.Profiles[0].Cost(1, 1) != domain.CostUnknown {
		t.Fatalf("old profile = %+v %v", st, err)
	}
}
