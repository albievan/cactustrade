package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

const settingLLM = "llm.config"

type llmJSON struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	BaseURL        string `json:"baseUrl"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

// LLMSetup is what the LLM reviewer page shows (never the key).
type LLMSetup struct {
	Config domain.LLMConfig
	KeySet bool
}

func (s *Service) llmConfig(ctx context.Context) domain.LLMConfig {
	def := domain.LLMConfig{Provider: "anthropic"}
	v, err := s.d.Store.Settings().Get(ctx, settingLLM)
	if err != nil {
		return def
	}
	var j llmJSON
	if json.Unmarshal([]byte(v), &j) != nil {
		return def
	}
	c := domain.LLMConfig{Enabled: j.Enabled, Provider: domain.LLMProviderID(j.Provider), Model: j.Model, BaseURL: j.BaseURL, TimeoutSeconds: j.TimeoutSeconds}
	if c.Validate() != nil {
		c.Enabled = false // an invalid stored setup never runs
	}
	return c
}

func (s *Service) llmKey(ctx context.Context) (string, error) {
	k, err := s.d.Secrets.Get(ctx, domain.CredLLMAPIKey)
	if errors.Is(err, domain.ErrNotFound) {
		return "", nil
	}
	return k, err
}

// LLMSetup returns the reviewer's configuration (viewer).
func (s *Service) LLMSetup(ctx context.Context, p Principal) (LLMSetup, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return LLMSetup{}, err
	}
	k, err := s.llmKey(ctx)
	if err != nil {
		return LLMSetup{}, fmt.Errorf("app.LLMSetup: %w", err)
	}
	return LLMSetup{Config: s.llmConfig(ctx), KeySet: k != ""}, nil
}

// SetLLMConfig changes the reviewer's configuration (admin, web session; audited).
func (s *Service) SetLLMConfig(ctx context.Context, p Principal, c domain.LLMConfig) (domain.LLMConfig, error) {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return domain.LLMConfig{}, err
	}
	if p.Via != ViaSession {
		return domain.LLMConfig{}, fmt.Errorf("only from the web UI: %w", domain.ErrForbidden)
	}
	if err := c.Validate(); err != nil {
		return domain.LLMConfig{}, err
	}
	b, _ := json.Marshal(llmJSON{Enabled: c.Enabled, Provider: string(c.Provider), Model: c.Model, BaseURL: c.BaseURL, TimeoutSeconds: c.TimeoutSeconds})
	if err := s.d.Store.Settings().Set(ctx, settingLLM, string(b), s.d.Clock.Now().UTC()); err != nil {
		return domain.LLMConfig{}, fmt.Errorf("app.SetLLMConfig: %w", err)
	}
	s.audit(ctx, p, "llm.config_changed", string(c.Provider), string(b))
	state := "off"
	if c.Enabled {
		state = "on"
	}
	s.emit(ctx, p.Username, "llm.config_changed", domain.SeverityInfo, "LLM reviewer set to "+string(c.Provider)+" ("+state+")", c.Model)
	return c, nil
}

// LLMModels lists the models a provider offers with the stored key (admin).
func (s *Service) LLMModels(ctx context.Context, p Principal, c domain.LLMConfig) ([]string, error) {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return nil, err
	}
	if s.d.Reviewer == nil {
		return nil, fmt.Errorf("the LLM reviewer is not available: %w", domain.ErrUnavailable)
	}
	if c.Model == "" {
		c.Model = "x" // listing needs no model; keep Validate's other checks
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	key, err := s.llmKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("app.LLMModels: %w", err)
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

// TestLLM asks the stored reviewer setup to review a made-up trade (admin). A provider
// failure is a result (OK=false), not an error.
func (s *Service) TestLLM(ctx context.Context, p Principal) (LLMTestResult, error) {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return LLMTestResult{}, err
	}
	if s.d.Reviewer == nil {
		return LLMTestResult{}, fmt.Errorf("the LLM reviewer is not available: %w", domain.ErrUnavailable)
	}
	c := s.llmConfig(ctx)
	if err := c.Validate(); err != nil {
		return LLMTestResult{}, err // fix the setup first (400 with the field)
	}
	key, err := s.llmKey(ctx)
	if err != nil {
		return LLMTestResult{}, fmt.Errorf("app.TestLLM: %w", err)
	}
	start := s.d.Clock.Now()
	v, err := s.d.Reviewer.Review(ctx, c, key, sampleReview)
	res := LLMTestResult{Latency: s.d.Clock.Now().Sub(start)}
	if err != nil {
		if ctx.Err() != nil {
			return LLMTestResult{}, ctx.Err() // the caller went away
		}
		res.Error = err.Error()
		s.audit(ctx, p, "llm.test", string(c.Provider), "failed")
		return res, nil
	}
	res.OK, res.Verdict = true, domain.ClampVerdict(v)
	s.audit(ctx, p, "llm.test", string(c.Provider), "ok "+string(res.Verdict.Kind))
	return res, nil
}
