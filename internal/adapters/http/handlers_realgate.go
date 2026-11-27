package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// The real-trading gate (PLAN 7.14). Who may do what is decided by the use cases; this file
// maps them, runs the arming password under the sign-in limiter, and words the refusals.
// Disarm is never rate limited (neverThrottled, D32).

// failGate maps the gate's errors: failing checks are 409 naming them, every other real-mode
// refusal is 403 "Real trading is not enabled in this build"; the rest as the agent
// endpoints (invalid input 422).
func (s *Server) failGate(w http.ResponseWriter, r *http.Request, err error) {
	if gr, ok := errors.AsType[*domain.GateRefusal](err); ok {
		details := make([]string, 0, len(gr.Failed))
		for _, c := range gr.Failed {
			details = append(details, c.Detail)
		}
		problemFor(w, r, err, http.StatusConflict, "Real trading cannot be armed", strings.Join(details, "; "), "")
		return
	}
	if errors.Is(err, domain.ErrRealModeGated) {
		problemFor(w, r, err, http.StatusForbidden, "Real trading is not enabled in this build",
			"real trading needs the owner's written consent (PLAN 7.18): this build refuses to arm it", "")
		return
	}
	s.failAgent(w, r, err)
}

func toRealGate(g domain.RealGate) RealGate {
	out := RealGate{AccountId: g.AccountID, State: RealGateState(g.State), EvidenceDays: g.Evidence.Count, Version: g.Version}
	if out.State == "" {
		out.State = Locked
	}
	if g.DemoAccountID != "" {
		out.DemoAccountId = ptr(g.DemoAccountID)
	}
	if !g.ArmedAt.IsZero() {
		out.ArmedAt, out.ArmedBy, out.ExpiresAt = ptr(g.ArmedAt), ptr(g.ArmedBy), ptr(g.ExpiresAt)
	}
	if !g.DisarmedAt.IsZero() {
		out.DisarmedAt, out.DisarmReason = ptr(g.DisarmedAt), ptr(g.DisarmReason)
		if g.DisarmedBy != "" {
			out.DisarmedBy = ptr(g.DisarmedBy)
		}
	}
	return out
}

func toRealGateStatus(v app.RealGateView) RealGateStatus {
	out := RealGateStatus{
		Account: toAccount(v.Account), Gate: toRealGate(v.Gate), Armed: v.Armed, Enabled: v.Enabled,
		DemoCandidates: make([]Account, 0, len(v.DemoCandidates)), Checks: make([]GateCheck, 0, len(v.Checks)),
		Evidence: GateEvidence{Count: v.Evidence.Count, Days: make([]string, 0, len(v.Evidence.Days))},
		Rule:     EvidenceRule{MinDays: v.Rule.MinDays, WindowDays: v.Rule.WindowDays, MaxAgeDays: v.Rule.MaxAgeDays},
		Phrase:   v.Phrase, CanArm: v.CanArm, CanDisarm: v.CanDisarm, CanSetDays: v.CanSetDays, MinDaysFloor: domain.MinEvidenceDaysFloor,
	}
	if v.DemoAccountID != "" {
		out.DemoAccountId = ptr(v.DemoAccountID)
	}
	for _, c := range v.DemoCandidates {
		out.DemoCandidates = append(out.DemoCandidates, toAccount(c))
	}
	for _, c := range v.Checks {
		out.Checks = append(out.Checks, GateCheck{Name: GateCheckName(c.Name), Pass: c.Pass, Detail: c.Detail})
	}
	for _, d := range v.Evidence.Days {
		out.Evidence.Days = append(out.Evidence.Days, string(d))
	}
	if v.Evidence.Count > 0 {
		out.Evidence.First, out.Evidence.Last = ptr(string(v.Evidence.First)), ptr(string(v.Evidence.Last))
	}
	return out
}

// GetRealGate returns a real-money account's gate and its checklist (view access).
func (s *Server) GetRealGate(w http.ResponseWriter, r *http.Request, accountID string, params GetRealGateParams) {
	demo := ""
	if params.DemoAccountId != nil {
		demo = *params.DemoAccountId
	}
	v, err := s.svc.RealGateStatus(r.Context(), state(r).principal, accountID, demo)
	if err != nil {
		s.failGate(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRealGateStatus(v))
}

// SetRealGateDemoDays sets how many qualifying demo days arming needs (D36).
func (s *Server) SetRealGateDemoDays(w http.ResponseWriter, r *http.Request, accountID string) {
	var req SetRealGateDemoDaysJSONRequestBody
	if !decode(w, r, &req) {
		return
	}
	v, err := s.svc.SetDemoDaysRequired(r.Context(), state(r).principal, accountID, req.MinDays)
	if err != nil {
		s.failGate(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRealGateStatus(v))
}

// ArmRealGate asks to arm real trading (admin owner, web session, password, phrase). In this
// build it is always refused.
func (s *Server) ArmRealGate(w http.ResponseWriter, r *http.Request, accountID string) {
	var req ArmRealRequest
	if !decode(w, r, &req) {
		return
	}
	var g domain.RealGate
	ok, err := s.passwordChecked(w, r, func() (err error) {
		g, err = s.svc.ArmReal(r.Context(), state(r).principal, accountID, req.DemoAccountId, req.Confirm, req.Password)
		return err
	})
	switch {
	case !ok:
	case err != nil:
		s.failGate(w, r, err)
	default:
		writeJSON(w, http.StatusOK, toRealGate(g))
	}
}

// DisarmRealGate disarms real trading now (trade access and agent.kill, session or API key).
func (s *Server) DisarmRealGate(w http.ResponseWriter, r *http.Request, accountID string) {
	var req DisarmRealRequest
	if !decode(w, r, &req) {
		return
	}
	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	g, err := s.svc.DisarmReal(r.Context(), state(r).principal, accountID, reason)
	if err != nil {
		s.failGate(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRealGate(g))
}
