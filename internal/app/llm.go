package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

const (
	settingLLMProfiles = "llm.profiles"
	settingLLMLegacy   = "llm.config" // the single setup before profiles (owner's request 2026-11-23)
)

type llmProfileJSON struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	BaseURL        string `json:"baseUrl"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

type llmStoreJSON struct {
	Enabled  bool             `json:"enabled"`
	Active   string           `json:"active"`
	Profiles []llmProfileJSON `json:"profiles"`
}

type llmLegacyJSON struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	BaseURL        string `json:"baseUrl"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

// LLMProfileView is a profile as the page shows it (never the key).
type LLMProfileView struct {
	domain.LLMProfile
	KeySet bool
}

// LLMSetup is what the LLM reviewer page shows.
type LLMSetup struct {
	Settings domain.LLMSettings
	Profiles []LLMProfileView
}

func (s *Service) loadLLM(ctx context.Context) (domain.LLMSettings, []domain.LLMProfile, error) {
	v, err := s.d.Store.Settings().Get(ctx, settingLLMProfiles)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.LLMSettings{}, nil, nil
	}
	if err != nil {
		return domain.LLMSettings{}, nil, err
	}
	var j llmStoreJSON
	if err := json.Unmarshal([]byte(v), &j); err != nil {
		return domain.LLMSettings{}, nil, fmt.Errorf("stored LLM profiles are unreadable: %w", err)
	}
	ps := make([]domain.LLMProfile, 0, len(j.Profiles))
	for _, p := range j.Profiles {
		ps = append(ps, domain.LLMProfile{ID: p.ID, Name: p.Name, LLMConfig: domain.LLMConfig{
			Provider: domain.LLMProviderID(p.Provider), Model: p.Model, BaseURL: p.BaseURL, TimeoutSeconds: p.TimeoutSeconds,
		}})
	}
	return domain.LLMSettings{Enabled: j.Enabled, Active: j.Active}, ps, nil
}

func (s *Service) saveLLM(ctx context.Context, st domain.LLMSettings, ps []domain.LLMProfile) error {
	j := llmStoreJSON{Enabled: st.Enabled, Active: st.Active, Profiles: make([]llmProfileJSON, 0, len(ps))}
	for _, p := range ps {
		j.Profiles = append(j.Profiles, llmProfileJSON{ID: p.ID, Name: p.Name, Provider: string(p.Provider), Model: p.Model, BaseURL: p.BaseURL, TimeoutSeconds: p.TimeoutSeconds})
	}
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return s.d.Store.Settings().Set(ctx, settingLLMProfiles, string(b), s.d.Clock.Now().UTC())
}

func (s *Service) llmKey(ctx context.Context, profileID string) (string, error) {
	k, err := s.d.Secrets.Get(ctx, domain.LLMKeyName(profileID))
	if errors.Is(err, domain.ErrNotFound) {
		return "", nil
	}
	return k, err
}

// MigrateLLMSetup turns the single reviewer setup of earlier builds (llm.config and the
// llm_api_key credential) into the first profile. Called at start-up; idempotent.
func (s *Service) MigrateLLMSetup(ctx context.Context) (bool, error) {
	if _, err := s.d.Store.Settings().Get(ctx, settingLLMProfiles); err == nil {
		return false, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return false, fmt.Errorf("app.MigrateLLMSetup: %w", err)
	}
	v, err := s.d.Store.Settings().Get(ctx, settingLLMLegacy)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("app.MigrateLLMSetup: %w", err)
	}
	old, ok := decodeLegacyLLM(v)
	if !ok {
		s.d.Log.WarnContext(ctx, "the old LLM reviewer setup is unreadable and was not migrated; set up a profile on the LLM reviewer page")
		return false, nil
	}
	p := domain.LLMProfile{ID: s.d.Random.ID(), LLMConfig: domain.LLMConfig{
		Provider: domain.LLMProviderID(old.Provider), Model: old.Model, BaseURL: old.BaseURL, TimeoutSeconds: old.TimeoutSeconds,
	}}.Normalize()
	if p.Name == "" {
		p.Name = "Reviewer"
	}
	key, err := s.d.Secrets.Get(ctx, domain.CredLLMAPIKey)
	switch {
	case err == nil && key != "":
		if err := s.d.Secrets.Put(ctx, domain.LLMKeyName(p.ID), key); err != nil {
			return false, fmt.Errorf("app.MigrateLLMSetup: %w", err)
		}
	case err != nil && !errors.Is(err, domain.ErrNotFound):
		return false, fmt.Errorf("app.MigrateLLMSetup: %w", err)
	}
	st := domain.LLMSettings{Active: p.ID, Enabled: old.Enabled && p.Validate() == nil}
	if err := s.saveLLM(ctx, st, []domain.LLMProfile{p}); err != nil {
		return false, fmt.Errorf("app.MigrateLLMSetup: %w", err)
	}
	if key != "" {
		if err := s.d.Secrets.Delete(ctx, domain.CredLLMAPIKey); err != nil && !errors.Is(err, domain.ErrNotFound) {
			return true, fmt.Errorf("app.MigrateLLMSetup: %w", err)
		}
	}
	return true, nil
}

// decodeLegacyLLM reads the pre-profile setup; ok is false when it is unreadable.
func decodeLegacyLLM(v string) (llmLegacyJSON, bool) {
	var old llmLegacyJSON
	return old, json.Unmarshal([]byte(v), &old) == nil
}

// ActiveReviewer returns the configuration and key the agent reviews trades with; ok is
// false when reviewing is off. A switched-on reviewer that is unusable is an error (the
// caller rejects the trade: fail closed).
func (s *Service) ActiveReviewer(ctx context.Context) (domain.LLMConfig, string, bool, error) {
	st, ps, err := s.loadLLM(ctx)
	if err != nil {
		return domain.LLMConfig{}, "", false, fmt.Errorf("app.ActiveReviewer: %w", err)
	}
	if !st.Enabled {
		return domain.LLMConfig{}, "", false, nil
	}
	i := slices.IndexFunc(ps, func(p domain.LLMProfile) bool { return p.ID == st.Active })
	if i < 0 {
		return domain.LLMConfig{}, "", true, errors.New("app.ActiveReviewer: the active reviewer profile is missing")
	}
	if err := ps[i].Validate(); err != nil {
		return domain.LLMConfig{}, "", true, fmt.Errorf("app.ActiveReviewer: %w", err)
	}
	key, err := s.llmKey(ctx, ps[i].ID)
	if err != nil {
		return domain.LLMConfig{}, "", true, fmt.Errorf("app.ActiveReviewer: %w", err)
	}
	c := ps[i].LLMConfig
	c.Enabled = true
	return c, key, true, nil
}

// LLMSetup returns the reviewer's profiles and switch (viewer).
func (s *Service) LLMSetup(ctx context.Context, p Principal) (LLMSetup, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return LLMSetup{}, err
	}
	st, ps, err := s.loadLLM(ctx)
	if err != nil {
		return LLMSetup{}, fmt.Errorf("app.LLMSetup: %w", err)
	}
	status, err := s.d.Secrets.Status(ctx)
	if err != nil {
		return LLMSetup{}, fmt.Errorf("app.LLMSetup: %w", err)
	}
	out := LLMSetup{Settings: st, Profiles: make([]LLMProfileView, 0, len(ps))}
	for _, pr := range ps {
		_, set := status[domain.LLMKeyName(pr.ID)]
		out.Profiles = append(out.Profiles, LLMProfileView{LLMProfile: pr, KeySet: set})
	}
	return out, nil
}

func (s *Service) llmAdmin(p Principal) error {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return err
	}
	if p.Via != ViaSession {
		return fmt.Errorf("only from the web UI: %w", domain.ErrForbidden)
	}
	return nil
}

// SetLLMSettings switches reviewing on or off and chooses the active profile (admin, web
// session; audited). Switching on needs a valid active profile with a key when the provider
// requires one.
func (s *Service) SetLLMSettings(ctx context.Context, p Principal, st domain.LLMSettings) (LLMSetup, error) {
	if err := s.llmAdmin(p); err != nil {
		return LLMSetup{}, err
	}
	_, ps, err := s.loadLLM(ctx)
	if err != nil {
		return LLMSetup{}, fmt.Errorf("app.SetLLMSettings: %w", err)
	}
	var active domain.LLMProfile
	if st.Active != "" {
		i := slices.IndexFunc(ps, func(x domain.LLMProfile) bool { return x.ID == st.Active })
		if i < 0 {
			return LLMSetup{}, domain.Invalid("activeProfileId", "no such profile")
		}
		active = ps[i]
	}
	if st.Enabled {
		if st.Active == "" {
			return LLMSetup{}, domain.Invalid("activeProfileId", "choose the profile to review with")
		}
		if err := active.Validate(); err != nil {
			return LLMSetup{}, err
		}
		if pr, _ := domain.LLMProviderByID(active.Provider); pr.KeyRequired {
			if k, err := s.llmKey(ctx, active.ID); err != nil {
				return LLMSetup{}, fmt.Errorf("app.SetLLMSettings: %w", err)
			} else if k == "" {
				return LLMSetup{}, domain.Invalid("activeProfileId", active.Name+" has no API key yet")
			}
		}
	}
	if err := s.saveLLM(ctx, st, ps); err != nil {
		return LLMSetup{}, fmt.Errorf("app.SetLLMSettings: %w", err)
	}
	state := "off"
	if st.Enabled {
		state = "on, using " + active.Name
	}
	s.audit(ctx, p, "llm.settings", st.Active, state)
	s.emit(ctx, p.Username, "llm.config_changed", domain.SeverityInfo, "LLM reviewer "+state, string(active.Provider)+" "+active.Model)
	return s.LLMSetup(ctx, p)
}

// SaveLLMProfile creates (ID "") or changes a profile (admin, web session; audited). key nil
// keeps the stored key, "" removes it, anything else replaces it. A stored key is never sent
// to another provider or address: changing those needs the key again.
func (s *Service) SaveLLMProfile(ctx context.Context, p Principal, in domain.LLMProfile, key *string) (LLMProfileView, error) {
	if err := s.llmAdmin(p); err != nil {
		return LLMProfileView{}, err
	}
	in = in.Normalize()
	if err := in.Validate(); err != nil {
		return LLMProfileView{}, err
	}
	if key != nil {
		*key = strings.TrimSpace(*key)
		if len(*key) > 4096 || strings.ContainsAny(*key, " \t\r\n") {
			return LLMProfileView{}, domain.Invalid("apiKey", "at most 4096 characters, no spaces")
		}
	}
	st, ps, err := s.loadLLM(ctx)
	if err != nil {
		return LLMProfileView{}, fmt.Errorf("app.SaveLLMProfile: %w", err)
	}
	created := in.ID == ""
	if created {
		if len(ps) >= domain.MaxLLMProfiles {
			return LLMProfileView{}, domain.Invalid("name", fmt.Sprintf("at most %d profiles", domain.MaxLLMProfiles))
		}
		in.ID = s.d.Random.ID()
		ps = append(ps, in)
	} else {
		i := slices.IndexFunc(ps, func(x domain.LLMProfile) bool { return x.ID == in.ID })
		if i < 0 {
			return LLMProfileView{}, fmt.Errorf("LLM profile: %w", domain.ErrNotFound)
		}
		old := ps[i]
		if key == nil && (old.Provider != in.Provider || old.Endpoint() != in.Endpoint()) {
			if k, err := s.llmKey(ctx, in.ID); err != nil {
				return LLMProfileView{}, fmt.Errorf("app.SaveLLMProfile: %w", err)
			} else if k != "" {
				return LLMProfileView{}, domain.Invalid("apiKey", "enter the key again: the stored one belongs to "+old.Endpoint()+" ("+string(old.Provider)+")")
			}
		}
		ps[i] = in
	}
	if key != nil {
		if *key == "" {
			if err := s.d.Secrets.Delete(ctx, domain.LLMKeyName(in.ID)); err != nil && !errors.Is(err, domain.ErrNotFound) {
				return LLMProfileView{}, fmt.Errorf("app.SaveLLMProfile: %w", err)
			}
		} else if err := s.d.Secrets.Put(ctx, domain.LLMKeyName(in.ID), *key); err != nil {
			return LLMProfileView{}, fmt.Errorf("app.SaveLLMProfile: %w", err)
		}
	}
	if st.Active == "" {
		st.Active = in.ID // the first profile becomes the active one
	}
	if st.Active == in.ID && st.Enabled && key != nil && *key == "" {
		if pr, _ := domain.LLMProviderByID(in.Provider); pr.KeyRequired {
			st.Enabled = false // a reviewer without a key would reject everything
		}
	}
	if err := s.saveLLM(ctx, st, ps); err != nil {
		return LLMProfileView{}, fmt.Errorf("app.SaveLLMProfile: %w", err)
	}
	action, detail := "llm.profile_update", in.Name+": "+string(in.Provider)+" "+in.Model
	if created {
		action = "llm.profile_create"
	}
	if key != nil {
		detail += ", key changed"
	}
	s.audit(ctx, p, action, in.ID, detail)
	k, err := s.llmKey(ctx, in.ID)
	if err != nil {
		return LLMProfileView{}, fmt.Errorf("app.SaveLLMProfile: %w", err)
	}
	return LLMProfileView{LLMProfile: in, KeySet: k != ""}, nil
}

// DeleteLLMProfile removes a profile and its key (admin, web session; audited). Deleting the
// active profile switches reviewing off.
func (s *Service) DeleteLLMProfile(ctx context.Context, p Principal, id string) error {
	if err := s.llmAdmin(p); err != nil {
		return err
	}
	st, ps, err := s.loadLLM(ctx)
	if err != nil {
		return fmt.Errorf("app.DeleteLLMProfile: %w", err)
	}
	i := slices.IndexFunc(ps, func(x domain.LLMProfile) bool { return x.ID == id })
	if i < 0 {
		return fmt.Errorf("LLM profile: %w", domain.ErrNotFound)
	}
	name := ps[i].Name
	ps = slices.Delete(ps, i, i+1)
	if st.Active == id {
		st = domain.LLMSettings{}
	}
	if err := s.saveLLM(ctx, st, ps); err != nil {
		return fmt.Errorf("app.DeleteLLMProfile: %w", err)
	}
	if err := s.d.Secrets.Delete(ctx, domain.LLMKeyName(id)); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("app.DeleteLLMProfile: %w", err)
	}
	s.audit(ctx, p, "llm.profile_delete", id, name)
	return nil
}

// LLMModelsRequest asks a provider for its models: with the key typed on the page, or else
// with the stored key of ProfileID (only when provider and address are the saved ones).
type LLMModelsRequest struct {
	ProfileID string
	Config    domain.LLMConfig
	Key       string
}

// LLMModels lists the models a provider offers (admin).
func (s *Service) LLMModels(ctx context.Context, p Principal, req LLMModelsRequest) ([]string, error) {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return nil, err
	}
	if s.d.Reviewer == nil {
		return nil, fmt.Errorf("the LLM reviewer is not available: %w", domain.ErrUnavailable)
	}
	c := req.Config
	if c.Model == "" {
		c.Model = "x" // listing needs no model; keep Validate's other checks
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	key := strings.TrimSpace(req.Key)
	if key == "" && req.ProfileID != "" {
		_, ps, err := s.loadLLM(ctx)
		if err != nil {
			return nil, fmt.Errorf("app.LLMModels: %w", err)
		}
		i := slices.IndexFunc(ps, func(x domain.LLMProfile) bool { return x.ID == req.ProfileID })
		if i < 0 {
			return nil, fmt.Errorf("LLM profile: %w", domain.ErrNotFound)
		}
		if ps[i].Provider != c.Provider || ps[i].Endpoint() != c.Endpoint() {
			return nil, domain.Invalid("apiKey", "enter the key: the stored one belongs to "+string(ps[i].Provider))
		}
		if key, err = s.llmKey(ctx, ps[i].ID); err != nil {
			return nil, fmt.Errorf("app.LLMModels: %w", err)
		}
	}
	if pr, _ := domain.LLMProviderByID(c.Provider); pr.KeyRequired && key == "" {
		return nil, domain.Invalid("apiKey", "enter the API key first")
	}
	ms, err := s.d.Reviewer.Models(ctx, c, key)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", domain.ErrUnavailable, err)
	}
	return ms, nil
}

// LLMTestResult is the reviewer's answer to a made-up trade.
type LLMTestResult struct {
	OK      bool
	Verdict domain.Verdict
	Error   string
	Latency time.Duration
}

// sampleReview is the made-up trade the test sends (nothing is traded).
var sampleReview = ports.ReviewInput{
	Candidate: domain.Candidate{
		Instrument: 1, Playbook: "trend_breakout", Reason: "TEST ONLY: close above the 20-bar high in a trending regime",
		Entry: 100_000_000, StopLossRate: 98_500_000, TakeProfitRate: 103_000_000, SuggestedSize: 50_000,
		RegimeProbs: map[domain.Regime]float64{domain.RegimeTrending: 0.78, domain.RegimeChoppy: 0.17, domain.RegimeHighVol: 0.05},
	},
	RecentCandles: []domain.Candle{{Close: 98_900_000}, {Close: 99_200_000}, {Close: 99_100_000}, {Close: 99_600_000}, {Close: 100_000_000}},
}

// TestLLM asks a stored profile to review a made-up trade (admin). A provider failure is a
// result (OK=false), not an error.
func (s *Service) TestLLM(ctx context.Context, p Principal, profileID string) (LLMTestResult, error) {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return LLMTestResult{}, err
	}
	if s.d.Reviewer == nil {
		return LLMTestResult{}, fmt.Errorf("the LLM reviewer is not available: %w", domain.ErrUnavailable)
	}
	_, ps, err := s.loadLLM(ctx)
	if err != nil {
		return LLMTestResult{}, fmt.Errorf("app.TestLLM: %w", err)
	}
	i := slices.IndexFunc(ps, func(x domain.LLMProfile) bool { return x.ID == profileID })
	if i < 0 {
		return LLMTestResult{}, fmt.Errorf("LLM profile: %w", domain.ErrNotFound)
	}
	pr := ps[i]
	if err := pr.Validate(); err != nil {
		return LLMTestResult{}, err // fix the setup first (400 with the field)
	}
	key, err := s.llmKey(ctx, pr.ID)
	if err != nil {
		return LLMTestResult{}, fmt.Errorf("app.TestLLM: %w", err)
	}
	if prov, _ := domain.LLMProviderByID(pr.Provider); prov.KeyRequired && key == "" {
		return LLMTestResult{}, domain.Invalid("apiKey", pr.Name+" has no API key yet")
	}
	start := s.d.Clock.Now()
	v, err := s.d.Reviewer.Review(ctx, pr.LLMConfig, key, sampleReview)
	res := LLMTestResult{Latency: s.d.Clock.Now().Sub(start)}
	if err != nil {
		if ctx.Err() != nil {
			return LLMTestResult{}, ctx.Err() // the caller went away
		}
		res.Error = err.Error()
		s.audit(ctx, p, "llm.test", pr.ID, "failed")
		return res, nil
	}
	res.OK, res.Verdict = true, domain.ClampVerdict(v)
	s.audit(ctx, p, "llm.test", pr.ID, "ok "+string(res.Verdict.Kind))
	return res, nil
}
