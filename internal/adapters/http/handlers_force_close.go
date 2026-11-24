package httpapi

import (
	"errors"
	"net/http"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// The agents list (R23) and the force close (R24). Who may do what is decided by the use
// cases; this file only maps them, and runs the password check under the sign-in limiter.

// ListAgents lists the agent of every eToro account the caller can see (R23).
func (s *Server) ListAgents(w http.ResponseWriter, r *http.Request) {
	cs, err := s.svc.AgentCards(r.Context(), state(r).principal)
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	out := make([]AgentCard, 0, len(cs))
	for _, c := range cs {
		x := AgentCard{
			Account: toAccount(c.Account), Current: c.Current, State: AgentState(c.State), StateSince: optTime(c.StateSince), Killed: c.Killed,
			Reconciliation: toReconciliation(c.Reconciliation), LimitsEditable: c.LimitsEditable,
			Controls: toControls(c.Controls, c.StartBlocked), OpenPositions: c.OpenPositions, PendingOrders: c.Pending,
		}
		if c.LimitsBlocked != "" {
			x.LimitsBlockedReason = ptr(c.LimitsBlocked)
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, out)
}

func toForceClosePlan(p app.ForceClosePlan) ForceClosePlan {
	return ForceClosePlan{AccountId: p.AccountID, Name: p.Name, Positions: p.Positions, Orders: p.Orders, Summary: p.Summary}
}

func toForceCloseReport(r app.ForceCloseReport) ForceCloseReport {
	out := ForceCloseReport{
		AccountId: r.AccountID, Name: r.Name, Cancelled: r.Cancelled, Closing: r.Closing, State: AgentState(r.State), Errors: r.Errors,
	}
	if out.Errors == nil {
		out.Errors = []string{}
	}
	return out
}

// ForceClosePreview says what a force close of the account would do (R24).
func (s *Server) ForceClosePreview(w http.ResponseWriter, r *http.Request, params ForceClosePreviewParams) {
	p, err := s.svc.ForceClosePreview(r.Context(), state(r).principal, accountParam(params.AccountId))
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toForceClosePlan(p))
}

// ForceCloseAllPreview says what a force close of every account would do (R24).
func (s *Server) ForceCloseAllPreview(w http.ResponseWriter, r *http.Request) {
	ps, err := s.svc.ForceCloseAllPreview(r.Context(), state(r).principal)
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	out := make([]ForceClosePlan, 0, len(ps))
	for _, p := range ps {
		out = append(out, toForceClosePlan(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// passwordChecked runs a use case that checks the caller's password again (R24) under the
// sign-in limiter (D28): the attempt is counted before the hash, a wrong password stays
// counted, any other outcome is refunded, and a right one counts as a good sign-in. ok false:
// the answer (429, or the client went away) is written and run was not called.
func (s *Server) passwordChecked(w http.ResponseWriter, r *http.Request, run func() error) (ok bool, err error) {
	st := state(r)
	if !s.signInAttempt(w, r, st.ip, st.principal.Username) {
		return false, nil
	}
	err = run()
	switch {
	case err == nil:
		s.limiter.Success(st.ip, st.principal.Username)
	case !errors.Is(err, domain.ErrWrongPassword):
		s.limiter.Refund(st.ip, st.principal.Username)
	}
	return true, err
}

// ForceCloseAgent force-closes one account (admin, web session, password; R24).
func (s *Server) ForceCloseAgent(w http.ResponseWriter, r *http.Request, params ForceCloseAgentParams) {
	var req ForceCloseRequest
	if !decode(w, r, &req) {
		return
	}
	var rep app.ForceCloseReport
	ok, err := s.passwordChecked(w, r, func() (err error) {
		rep, err = s.svc.ForceClose(r.Context(), state(r).principal, accountParam(params.AccountId), req.Password)
		return err
	})
	switch {
	case !ok:
	case err != nil:
		s.failAgent(w, r, err)
	default:
		writeJSON(w, http.StatusOK, toForceCloseReport(rep))
	}
}

// ForceCloseAll force-closes every eToro demo account the caller can trade (R24). When an
// agent could not be stopped the reports are still returned (that account's errors say so).
func (s *Server) ForceCloseAll(w http.ResponseWriter, r *http.Request) {
	var req ForceCloseRequest
	if !decode(w, r, &req) {
		return
	}
	var reps []app.ForceCloseReport
	ok, err := s.passwordChecked(w, r, func() (err error) {
		reps, err = s.svc.ForceCloseAll(r.Context(), state(r).principal, req.Password)
		return err
	})
	switch {
	case !ok:
		return
	case err != nil && reps == nil:
		s.failAgent(w, r, err)
		return
	case err != nil:
		s.log.ErrorContext(r.Context(), "force close of all accounts: an agent could not be stopped", "err", err)
	}
	out := make([]ForceCloseReport, 0, len(reps))
	for _, rep := range reps {
		out = append(out, toForceCloseReport(rep))
	}
	writeJSON(w, http.StatusOK, out)
}

// ListWatchlistCounts gives the watchlist size of each eToro account the caller can see
// (R25).
func (s *Server) ListWatchlistCounts(w http.ResponseWriter, r *http.Request) {
	cs, err := s.svc.WatchlistCounts(r.Context(), state(r).principal)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]WatchlistCount, 0, len(cs))
	for _, c := range cs {
		out = append(out, WatchlistCount{Account: toAccount(c.Account), Count: c.Count, Current: c.Current})
	}
	writeJSON(w, http.StatusOK, out)
}
