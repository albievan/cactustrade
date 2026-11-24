package httpapi

import (
	"net/http"

	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
)

// The decision log and the LLM usage (PLAN 5.6). Who may read what is decided by the use
// cases: the decision log needs view access to the account, the usage the admin's
// settings permission. Neither ever carries a key.

// usd formats USD micros; nil when the cost is unknown (domain.CostUnknown).
func usd(m domain.Micros) *UsdCost {
	if m < 0 {
		return nil
	}
	return ptr(m.String())
}

// price formats a price per million tokens ("" = unknown).
func price(m domain.Micros) LlmPrice {
	if m <= 0 {
		return ""
	}
	return m.String()
}

// parsePrice reads a price per million tokens ("" = unknown).
func parsePrice(field, s string) (domain.Micros, error) {
	if s == "" {
		return 0, nil
	}
	m, err := domain.ParseMicros(s)
	if err != nil {
		return 0, domain.Invalid(field, "USD per million tokens, e.g. 3 or 0.25 (empty = unknown)")
	}
	return m, nil
}

func toDecision(v app.DecisionView) Decision {
	d := v.Decision
	out := Decision{
		Id: d.ID, AccountId: d.AccountID, At: d.At, InstrumentId: int64(d.Signal.Instrument), Symbol: d.Signal.Symbol,
		Direction: string(d.Signal.Direction), Playbook: d.Signal.Playbook, SignalReason: d.Signal.Reason,
		Entry: dec(d.Signal.Entry), StopLoss: dec(d.Signal.StopLoss), SuggestedSizeCents: Cents(d.Signal.SuggestedSize),
		RegimeProbs: make(map[string]float64, len(d.Signal.RegimeProbs)),
		Verdict:     string(d.Verdict.Kind), SizeBp: int(d.Verdict.SizeBp), VerdictReason: d.Verdict.Reason, LlmCallId: d.Verdict.LLMCallID,
		RiskAllowed: d.Risk.Allowed, RiskSizeCents: Cents(d.Risk.Size), RiskReason: d.Risk.Reason,
		Outcome: string(d.Outcome), OrderRequestId: d.OrderRequestID, OrderState: string(v.OrderState),
	}
	if d.Signal.TakeProfit > 0 {
		out.TakeProfit = ptr(dec(d.Signal.TakeProfit))
	}
	for k, p := range d.Signal.RegimeProbs {
		out.RegimeProbs[string(k)] = p
	}
	return out
}

func toLlmCall(c domain.LLMCall) LlmCall {
	return LlmCall{
		Id: c.ID, Purpose: string(c.Purpose), ProfileId: c.ProfileID, Provider: string(c.Provider), Model: c.Model,
		Prompt: c.Prompt, Response: c.Response, InputTokens: c.InputTokens, OutputTokens: c.OutputTokens,
		LatencyMs: c.Latency.Milliseconds(), CostUsd: usd(c.CostMicros), Error: c.Error, At: c.At,
	}
}

// ListDecisions returns an account's decision log.
func (s *Server) ListDecisions(w http.ResponseWriter, r *http.Request, params ListDecisionsParams) {
	q := app.DecisionQuery{}
	if params.Limit != nil {
		if q.Limit = *params.Limit; q.Limit == 0 {
			q.Limit = -1 // an explicit 0 is out of range, not "the default"
		}
	}
	if params.Before != nil {
		q.Before = *params.Before
	}
	if params.Outcome != nil {
		q.Outcome = domain.DecisionOutcome(*params.Outcome)
	}
	ds, err := s.svc.Decisions(r.Context(), state(r).principal, accountParam(params.AccountId), q)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]Decision, 0, len(ds))
	for _, d := range ds {
		out = append(out, toDecision(d))
	}
	writeJSON(w, http.StatusOK, out)
}

// GetDecision returns one decision with its LLM call.
func (s *Server) GetDecision(w http.ResponseWriter, r *http.Request, id string, params GetDecisionParams) {
	d, err := s.svc.Decision(r.Context(), state(r).principal, accountParam(params.AccountId), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := DecisionDetail{Decision: toDecision(d.DecisionView)}
	if d.Call != nil {
		out.Call = ptr(toLlmCall(*d.Call))
	}
	writeJSON(w, http.StatusOK, out)
}

func toUsageRow(u domain.LLMUsage, name string) LlmUsageRow {
	out := LlmUsageRow{
		ProfileId: u.ProfileID, ProfileName: name, Purpose: string(u.Purpose), Calls: u.Calls, Failed: u.Failed,
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, KnownCostUsd: u.Cost.String(), CostUnknownCalls: u.CostUnknownCalls,
	}
	if u.CostKnown() {
		out.CostUsd = ptr(u.Cost.String())
	}
	return out
}

// GetLlmUsage sums the LLM calls of the last days.
func (s *Server) GetLlmUsage(w http.ResponseWriter, r *http.Request, params GetLlmUsageParams) {
	days := 0
	if params.Days != nil {
		if days = *params.Days; days == 0 {
			days = -1 // an explicit 0 is out of range, not "the default"
		}
	}
	rep, err := s.svc.LLMUsage(r.Context(), state(r).principal, days)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := LlmUsage{Days: rep.Days, Since: rep.Since, Rows: make([]LlmUsageRow, 0, len(rep.Rows)), Total: toUsageRow(rep.Total, "")}
	for _, u := range rep.Rows {
		out.Rows = append(out.Rows, toUsageRow(u.LLMUsage, u.ProfileName))
	}
	writeJSON(w, http.StatusOK, out)
}
