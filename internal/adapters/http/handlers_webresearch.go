package httpapi

import (
	"net/http"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// Web research for the watchlist (PLAN R29). Who may do what is decided by the use cases;
// the texts the model wrote (name, reason, sources) are passed on as plain JSON strings and
// the web UI shows them as text.

func optText(s string) *string {
	if s == "" {
		return nil
	}
	return ptr(s)
}

func toMissingItems(ms []app.MissingItem) []MissingItem {
	out := make([]MissingItem, 0, len(ms))
	for _, m := range ms {
		out = append(out, MissingItem{What: Missing(m.What), CanFix: m.CanFix})
	}
	return out
}

func toResearchConfig(c domain.ResearchConfig) ResearchConfig {
	return ResearchConfig{ProfileId: c.ProfileID, MaxShares: c.MaxShares, RunsPerDay: c.RunsPerDay}
}

func toResearchProposal(p domain.ResearchProposal) ResearchProposal {
	srcs := p.Sources
	if srcs == nil {
		srcs = []string{}
	}
	return ResearchProposal{
		Id: p.ID, InstrumentId: int64(p.Instrument), At: p.At, Symbol: p.Symbol, Exchange: ResearchProposalExchange(p.Exchange),
		Name: p.Name, Theme: optText(p.Theme), Reason: p.Reason, Sources: srcs,
	}
}

func toResearchAdded(a app.ResearchAdded) ResearchAddedShare {
	return ResearchAddedShare{InstrumentId: int64(a.Instrument), Symbol: a.Symbol, Name: a.Name, Theme: optText(a.Theme), Reason: a.Reason}
}

func toResearchSetup(st app.ResearchSetup) ResearchSetup {
	return ResearchSetup{
		Config: toResearchConfig(st.Config), ProfileId: optText(st.ProfileID), ProfileName: optText(st.ProfileName),
		Note: optText(st.Note), Missing: toMissingItems(st.Missing),
	}
}

// GetResearch returns the current account's Research card.
func (s *Server) GetResearch(w http.ResponseWriter, r *http.Request) {
	v, err := s.svc.Research(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := ResearchView{
		Mode: ResearchMode(v.ResearchMode), Config: toResearchConfig(v.Config), ProfileName: optText(v.ProfileName), Ready: v.Ready,
		Note: optText(v.Note), Missing: toMissingItems(v.Missing), RunsUsed: v.RunsUsed, Proposals: make([]ResearchProposal, 0, len(v.Proposals)),
		CanRun: v.CanRun, CanDecide: v.CanDecide,
	}
	if !v.LastRunAt.IsZero() {
		out.LastRunAt = ptr(v.LastRunAt)
	}
	for _, p := range v.Proposals {
		out.Proposals = append(out.Proposals, toResearchProposal(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// RunResearch runs web research for the current account now.
func (s *Server) RunResearch(w http.ResponseWriter, r *http.Request) {
	run, err := s.svc.ResearchNow(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := ResearchRun{
		Mode: ResearchMode(run.ResearchMode), Added: make([]ResearchAddedShare, 0, len(run.Added)), Proposed: make([]ResearchProposal, 0, len(run.Proposed)),
		Refused: make([]ResearchRefusal, 0, len(run.Refused)), Error: optText(run.Error), CallId: optText(run.CallID), Model: optText(run.Model),
		InputTokens: run.InputTokens, OutputTokens: run.OutputTokens, CostUsd: usd(run.Cost),
	}
	for _, a := range run.Added {
		out.Added = append(out.Added, toResearchAdded(a))
	}
	for _, p := range run.Proposed {
		out.Proposed = append(out.Proposed, toResearchProposal(p))
	}
	for _, x := range run.Refused {
		out.Refused = append(out.Refused, ResearchRefusal{Symbol: x.Symbol, Reason: x.Reason})
	}
	writeJSON(w, http.StatusOK, out)
}

// AcceptResearchProposal puts a proposal on the watchlist.
func (s *Server) AcceptResearchProposal(w http.ResponseWriter, r *http.Request, id string) {
	a, err := s.svc.AcceptResearchProposal(r.Context(), state(r).principal, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toResearchAdded(a))
}

// DismissResearchProposal drops a proposal.
func (s *Server) DismissResearchProposal(w http.ResponseWriter, r *http.Request, id string) {
	if err := s.svc.DismissResearchProposal(r.Context(), state(r).principal, id); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetResearchSettings returns the research settings.
func (s *Server) GetResearchSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.svc.ResearchSettings(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toResearchSetup(st))
}

// SetResearchSettings stores the research settings.
func (s *Server) SetResearchSettings(w http.ResponseWriter, r *http.Request) {
	var req ResearchConfig
	if !decode(w, r, &req) {
		return
	}
	st, err := s.svc.SetResearchSettings(r.Context(), state(r).principal, domain.ResearchConfig{
		ProfileID: req.ProfileId, MaxShares: req.MaxShares, RunsPerDay: req.RunsPerDay,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toResearchSetup(st))
}
