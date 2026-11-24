package httpapi

import (
	"errors"
	"net/http"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// The agent endpoints (PLAN 4.13). Who may do what is decided by the use cases (OD11: an API
// key may pause, stop and kill, never start or reset); CSRF is checked by the middleware for
// every cookie-authenticated mutation.

func accountParam(id *AccountIdQuery) string {
	if id == nil {
		return ""
	}
	return *id
}

// failAgent maps the agent use cases' errors: invalid input is 422 (the request was
// well-formed but cannot be acted on: no budget, wrong reset phrase), missing eToro keys 503,
// the rest as everywhere else (403 forbidden / view-only / real mode gated, 409 conflict).
func (s *Server) failAgent(w http.ResponseWriter, r *http.Request, err error) {
	var ve *domain.ValidationError
	switch {
	case errors.As(err, &ve):
		problemFor(w, r, err, http.StatusUnprocessableEntity, "Cannot do that now", ve.Error(), ve.Field)
	case errors.Is(err, domain.ErrRealModeGated):
		problemFor(w, r, err, http.StatusForbidden, "Real trading needs the phase-7 gate", detail(err, domain.ErrForbidden), "")
	case errors.Is(err, domain.ErrCredentialsMissing) && !errors.Is(err, domain.ErrUnavailable):
		problemFor(w, r, err, http.StatusServiceUnavailable, "Unavailable", detail(err, domain.ErrCredentialsMissing), "")
	default:
		s.fail(w, r, err)
	}
}

func toAgentStatus(st app.AgentStatus) AgentStatusResult {
	return AgentStatusResult{AccountId: st.AccountID, State: AgentState(st.State)}
}

func toReconciliation(rc app.Reconciliation) Reconciliation {
	out := Reconciliation{AccountId: rc.AccountID, Ok: rc.OK, Reason: rc.Reason}
	if !rc.At.IsZero() {
		out.At = ptr(rc.At)
	}
	return out
}

// toControls is the controls the app decided, with why start is blocked ("" = it is not).
func toControls(c app.AgentControls, startBlocked string) AgentControls {
	out := AgentControls{
		Start: c.Start, Pause: c.Pause, Stop: c.Stop, Kill: c.Kill, Reset: c.Reset, Gated: c.Gated, ForceClose: c.ForceClose,
	}
	if startBlocked != "" {
		out.StartBlockedReason = ptr(startBlocked)
	}
	if c.ForceCloseBlocked != "" {
		out.ForceCloseBlockedReason = ptr(c.ForceCloseBlocked)
	}
	return out
}

func toAgentPanel(v app.AgentView) AgentPanel {
	out := AgentPanel{
		Account: toAccount(v.Account), State: AgentState(v.State), Reconciliation: toReconciliation(v.Reconciliation),
		Limits: toRiskLimits(v.Limits), Killed: v.Risk.State.Killed || v.State == domain.AgentKilled,
		DayStopped: v.Risk.State.DayStopped && v.Risk.State.Day == v.Today, RiskDay: string(v.Risk.State.Day), Today: string(v.Today),
		StartOfDayEquityCents: Cents(v.Risk.State.StartOfDayEquity), HighWaterMarkCents: Cents(v.Risk.State.HighWaterMark),
		EquityKnown: v.EquityKnown, Blocked: make([]BlockedInstrument, 0, len(v.Blocked)), Flatten: make([]FlattenTime, 0, len(v.Flatten)),
		Lease:       AgentLease{Mine: v.Lease.Mine, Other: v.Lease.Other},
		Controls:    toControls(v.Controls, v.StartBlocked),
		ResetPhrase: v.ResetPhrase, OpenPositions: v.OpenPositions, PendingOrders: v.Pending,
		Missing: make([]MissingItem, 0, len(v.Missing)),
	}
	for _, m := range v.Missing {
		out.Missing = append(out.Missing, MissingItem{What: Missing(m.What), CanFix: m.CanFix})
	}
	if v.Risk.KillReason != "" {
		out.KillReason = ptr(v.Risk.KillReason)
	}
	if !v.Risk.KilledAt.IsZero() {
		out.KilledAt = ptr(v.Risk.KilledAt)
	}
	if v.EquityKnown {
		out.EquityCents = ptr(Cents(v.Equity))
		out.DrawdownFloorCents, out.ToDrawdownFloorCents = ptr(Cents(v.DrawdownFloor)), ptr(Cents(v.ToDrawdownFloor))
		if v.DailyFloorKnown {
			out.DailyFloorCents, out.ToDailyFloorCents = ptr(Cents(v.DailyFloor)), ptr(Cents(v.ToDailyFloor))
		}
	}
	if v.EquityError != "" {
		out.EquityError = ptr(v.EquityError)
	}
	if !v.Lease.ExpiresAt.IsZero() {
		out.Lease.ExpiresAt = ptr(v.Lease.ExpiresAt)
	}
	for _, b := range v.Blocked {
		out.Blocked = append(out.Blocked, BlockedInstrument{InstrumentId: int64(b.Instrument), Symbol: b.Symbol, Reason: b.Reason, At: b.At})
	}
	for _, f := range v.Flatten {
		x := FlattenTime{Market: Market(f.Market), Trading: f.Trading}
		if f.Trading {
			x.Closes, x.FlattenAt = ptr(f.Closes), ptr(f.At)
		}
		if f.Note != "" {
			x.Note = ptr(f.Note)
		}
		out.Flatten = append(out.Flatten, x)
	}
	return out
}

// GetAgent returns the account's agent panel.
func (s *Server) GetAgent(w http.ResponseWriter, r *http.Request, params GetAgentParams) {
	v, err := s.svc.AgentStatusView(r.Context(), state(r).principal, accountParam(params.AccountId))
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAgentPanel(v))
}

// StartAgent starts the agent (web session only).
func (s *Server) StartAgent(w http.ResponseWriter, r *http.Request, params StartAgentParams) {
	st, err := s.svc.StartAgent(r.Context(), state(r).principal, accountParam(params.AccountId))
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAgentStatus(st))
}

// PauseAgent pauses the agent.
func (s *Server) PauseAgent(w http.ResponseWriter, r *http.Request, params PauseAgentParams) {
	st, err := s.svc.PauseAgent(r.Context(), state(r).principal, accountParam(params.AccountId))
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAgentStatus(st))
}

// StopAgent stops the agent.
func (s *Server) StopAgent(w http.ResponseWriter, r *http.Request, params StopAgentParams) {
	st, err := s.svc.StopAgent(r.Context(), state(r).principal, accountParam(params.AccountId))
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAgentStatus(st))
}

// KillAgent pulls the kill switch. The body ({"reason": …}) is optional.
func (s *Server) KillAgent(w http.ResponseWriter, r *http.Request, params KillAgentParams) {
	var req KillRequest
	if r.ContentLength != 0 && r.Header.Get("Content-Type") != "" && !decode(w, r, &req) {
		return
	}
	reason := ""
	if req.Reason != nil {
		reason = *req.Reason
	}
	rep, err := s.svc.Kill(r.Context(), state(r).principal, accountParam(params.AccountId), reason)
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	out := KillReport{AccountId: rep.AccountID, Already: rep.Already, Cancelled: rep.Cancelled, Closing: rep.Closing, Errors: rep.Errors}
	if out.Errors == nil {
		out.Errors = []string{}
	}
	writeJSON(w, http.StatusOK, out)
}

// ResetKill resets the kill switch (admin, web session, typed phrase).
func (s *Server) ResetKill(w http.ResponseWriter, r *http.Request, params ResetKillParams) {
	var req ResetKillRequest
	if !decode(w, r, &req) {
		return
	}
	st, err := s.svc.ResetKill(r.Context(), state(r).principal, accountParam(params.AccountId), req.Confirm)
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAgentStatus(st))
}

// ReconcileAgent reconciles the account with eToro now.
func (s *Server) ReconcileAgent(w http.ResponseWriter, r *http.Request, params ReconcileAgentParams) {
	rc, err := s.svc.ReconcileAccount(r.Context(), state(r).principal, accountParam(params.AccountId))
	if err != nil {
		s.failAgent(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toReconciliation(rc))
}

// ListOrders lists the account's orders, newest first.
func (s *Server) ListOrders(w http.ResponseWriter, r *http.Request, params ListOrdersParams) {
	limit := 0
	if params.Limit != nil {
		if limit = *params.Limit; limit == 0 {
			limit = -1 // an explicit 0 is out of range, not "the default"
		}
	}
	orders, err := s.svc.Orders(r.Context(), state(r).principal, accountParam(params.AccountId), limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]Order, 0, len(orders))
	for _, o := range orders {
		out = append(out, toOrder(o))
	}
	writeJSON(w, http.StatusOK, out)
}

func toOrder(o app.OrderView) Order {
	x := Order{
		RequestId: o.RequestID, Mode: TradingMode(o.Mode), Kind: string(o.Kind), Direction: string(o.Direction),
		InstrumentId: int64(o.Instrument), Symbol: o.Symbol, AmountCents: Cents(o.Amount), State: string(o.State),
		Reason: o.Reason, Sends: o.Sends, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
	if o.Entry > 0 {
		x.Entry = ptr(dec(o.Entry))
	}
	if o.StopLoss > 0 {
		x.StopLoss = ptr(dec(o.StopLoss))
	}
	if o.TakeProfit > 0 {
		x.TakeProfit = ptr(dec(o.TakeProfit))
	}
	if o.PositionID > 0 {
		x.PositionId = ptr(o.PositionID)
	}
	if o.EtoroOrderID > 0 {
		x.EtoroOrderId = ptr(o.EtoroOrderID)
	}
	if !o.SentAt.IsZero() {
		x.SentAt = ptr(o.SentAt)
	}
	return x
}
