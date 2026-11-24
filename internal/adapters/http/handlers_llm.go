package httpapi

import (
	"net/http"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

func toLlmProfile(p app.LLMProfileView) LlmProfile {
	return LlmProfile{
		Id: p.ID, Name: p.Name, Provider: string(p.Provider), Model: p.Model, BaseUrl: p.BaseURL,
		TimeoutSeconds: p.TimeoutSeconds, KeySet: p.KeySet,
		InputPricePerMTok: price(p.InputPrice), OutputPricePerMTok: price(p.OutputPrice),
	}
}

// fromLlmProfileInput converts the request; the prices arrive as decimal strings (USD per
// million tokens) and are stored as micros (OD13).
func fromLlmProfileInput(id string, in LlmProfileInput) (domain.LLMProfile, error) {
	ip, err := parsePrice("inputPricePerMTok", in.InputPricePerMTok)
	if err != nil {
		return domain.LLMProfile{}, err
	}
	op, err := parsePrice("outputPricePerMTok", in.OutputPricePerMTok)
	if err != nil {
		return domain.LLMProfile{}, err
	}
	return domain.LLMProfile{ID: id, Name: in.Name, LLMConfig: domain.LLMConfig{
		Provider: domain.LLMProviderID(in.Provider), Model: in.Model, BaseURL: in.BaseUrl, TimeoutSeconds: in.TimeoutSeconds,
	}, InputPrice: ip, OutputPrice: op}, nil
}

func toLlmSetup(st app.LLMSetup) LlmSetup {
	out := LlmSetup{
		Settings:              LlmSettings{Enabled: st.Settings.Enabled, ActiveProfileId: st.Settings.Active},
		Profiles:              make([]LlmProfile, 0, len(st.Profiles)),
		DefaultAnthropicModel: domain.DefaultAnthropicModel,
	}
	for _, p := range st.Profiles {
		out.Profiles = append(out.Profiles, toLlmProfile(p))
	}
	for _, p := range domain.LLMProviders {
		out.Providers = append(out.Providers, LlmProvider{Id: string(p.ID), Label: p.Label, DefaultBaseUrl: p.DefaultBaseURL, KeyRequired: p.KeyRequired, Note: p.Note})
	}
	return out
}

// GetLlmSetup returns the reviewer's profiles and the provider list.
func (s *Server) GetLlmSetup(w http.ResponseWriter, r *http.Request) {
	st, err := s.svc.LLMSetup(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toLlmSetup(st))
}

// SetLlmSettings switches reviewing on or off and picks the default profile.
func (s *Server) SetLlmSettings(w http.ResponseWriter, r *http.Request) {
	var req LlmSettings
	if !decode(w, r, &req) {
		return
	}
	st, err := s.svc.SetLLMSettings(r.Context(), state(r).principal, domain.LLMSettings{Enabled: req.Enabled, Active: req.ActiveProfileId})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toLlmSetup(st))
}

// CreateLlmProfile adds a profile.
func (s *Server) CreateLlmProfile(w http.ResponseWriter, r *http.Request) {
	var req LlmProfileInput
	if !decode(w, r, &req) {
		return
	}
	in, err := fromLlmProfileInput("", req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.svc.SaveLLMProfile(r.Context(), state(r).principal, in, req.ApiKey)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toLlmProfile(p))
}

// UpdateLlmProfile changes a profile.
func (s *Server) UpdateLlmProfile(w http.ResponseWriter, r *http.Request, id string) {
	var req LlmProfileInput
	if !decode(w, r, &req) {
		return
	}
	in, err := fromLlmProfileInput(id, req)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.svc.SaveLLMProfile(r.Context(), state(r).principal, in, req.ApiKey)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toLlmProfile(p))
}

// DeleteLlmProfile removes a profile.
func (s *Server) DeleteLlmProfile(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.svc.DeleteLLMProfile(r.Context(), state(r).principal, id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListLlmModels lists a provider's models.
func (s *Server) ListLlmModels(w http.ResponseWriter, r *http.Request) {
	var req LlmModelsRequest
	if !decode(w, r, &req) {
		return
	}
	in := app.LLMModelsRequest{Config: domain.LLMConfig{Provider: domain.LLMProviderID(req.Provider), BaseURL: req.BaseUrl}}
	if req.ApiKey != nil {
		in.Key = *req.ApiKey
	}
	if req.ProfileId != nil {
		in.ProfileID = *req.ProfileId
	}
	ms, err := s.svc.LLMModels(r.Context(), state(r).principal, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if ms == nil {
		ms = []string{}
	}
	writeJSON(w, http.StatusOK, ms)
}

// TestLlmProfile reviews a made-up trade with one profile.
func (s *Server) TestLlmProfile(w http.ResponseWriter, r *http.Request, id string) {
	res, err := s.svc.TestLLM(r.Context(), state(r).principal, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := LlmTestResult{
		Ok: res.OK, LatencyMs: res.Latency.Milliseconds(), InputTokens: ptr(res.InputTokens), OutputTokens: ptr(res.OutputTokens),
		CostUsd: usd(res.Cost),
	}
	if res.Model != "" {
		out.Model = ptr(res.Model)
	}
	if res.CallID != "" {
		out.CallId = ptr(res.CallID)
	}
	if res.OK {
		v := LlmTestResultVerdict(res.Verdict.Kind)
		out.Verdict, out.SizeMultiplier, out.Reason = &v, ptr(float32(res.Verdict.SizeBp)/float32(domain.FullSize)), ptr(res.Verdict.Reason)
	} else {
		out.Error = ptr(res.Error)
	}
	writeJSON(w, http.StatusOK, out)
}
