package httpapi

import (
	"net/http"

	"github.com/albievan/cactustrade/internal/domain"
)

func toLlmConfig(c domain.LLMConfig) LlmConfig {
	return LlmConfig{Enabled: c.Enabled, Provider: string(c.Provider), Model: c.Model, BaseUrl: c.BaseURL, TimeoutSeconds: c.TimeoutSeconds}
}

func fromLlmConfig(c LlmConfig) domain.LLMConfig {
	return domain.LLMConfig{Enabled: c.Enabled, Provider: domain.LLMProviderID(c.Provider), Model: c.Model, BaseURL: c.BaseUrl, TimeoutSeconds: c.TimeoutSeconds}
}

// GetLlmSetup returns the reviewer's setup and the provider list.
func (s *Server) GetLlmSetup(w http.ResponseWriter, r *http.Request) {
	st, err := s.svc.LLMSetup(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := LlmSetup{Config: toLlmConfig(st.Config), KeySet: st.KeySet, DefaultAnthropicModel: domain.DefaultAnthropicModel}
	for _, p := range domain.LLMProviders {
		out.Providers = append(out.Providers, LlmProvider{Id: string(p.ID), Label: p.Label, DefaultBaseUrl: p.DefaultBaseURL, KeyRequired: p.KeyRequired, Note: p.Note})
	}
	writeJSON(w, http.StatusOK, out)
}

// SetLlmConfig stores the reviewer's setup.
func (s *Server) SetLlmConfig(w http.ResponseWriter, r *http.Request) {
	var req LlmConfig
	if !decode(w, r, &req) {
		return
	}
	c, err := s.svc.SetLLMConfig(r.Context(), state(r).principal, fromLlmConfig(req))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toLlmConfig(c))
}

// ListLlmModels lists a provider's models.
func (s *Server) ListLlmModels(w http.ResponseWriter, r *http.Request) {
	var req LlmConfig
	if !decode(w, r, &req) {
		return
	}
	ms, err := s.svc.LLMModels(r.Context(), state(r).principal, fromLlmConfig(req))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if ms == nil {
		ms = []string{}
	}
	writeJSON(w, http.StatusOK, ms)
}

// TestLlm reviews a made-up trade.
func (s *Server) TestLlm(w http.ResponseWriter, r *http.Request) {
	res, err := s.svc.TestLLM(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := LlmTestResult{Ok: res.OK, LatencyMs: res.Latency.Milliseconds()}
	if res.OK {
		v := LlmTestResultVerdict(res.Verdict.Kind)
		out.Verdict, out.SizeMultiplier, out.Reason = &v, ptr(float32(res.Verdict.SizeMultiplier)), ptr(res.Verdict.Reason)
	} else {
		out.Error = ptr(res.Error)
	}
	writeJSON(w, http.StatusOK, out)
}
