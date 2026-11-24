package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// The decision log (PLAN 5.5): every candidate the agent handles leaves one domain.Decision,
// and every call to an LLM reviewer (agent, scanner, the admin's test) one domain.LLMCall.
// Both are kept domain.LLMRetentionDays (OD12). Nothing here sends anything to eToro.

// Decision log list bounds.
const (
	decisionsDefaultLimit = 100
	decisionsMaxLimit     = 500
	usageDefaultDays      = 30
)

// logCall completes an LLM call's trace (the adapter filled the provider's side) and stores
// it. The cost comes from the profile's prices (OD13: unknown without them). The write is not
// cancelled with ctx: a call that was made is logged even when a kill cancels the tick.
func (s *Service) logCall(ctx context.Context, c *domain.LLMCall, accountID string, purpose domain.LLMPurpose, pr domain.LLMProfile, at time.Time) error {
	c.ID, c.AccountID, c.Purpose, c.ProfileID, c.At = s.d.Random.ID(), accountID, purpose, pr.ID, at.UTC()
	if c.Provider == "" {
		c.Provider = pr.Provider
	}
	if c.Model == "" {
		c.Model = pr.Model
	}
	c.CostMicros = pr.Cost(c.InputTokens, c.OutputTokens)
	if c.Latency <= 0 {
		c.Latency = max(s.d.Clock.Now().Sub(at), 0) // the adapter measures it; a fake may not
	}
	if err := s.d.Store.LLMCalls().Add(context.WithoutCancel(ctx), *c); err != nil {
		return fmt.Errorf("logging the LLM call: %w", err)
	}
	return nil
}

// DecisionQuery filters the decision log: decisions made before Before (zero = the newest),
// with Outcome ("" = any), at most Limit (0 = 100; 1–500).
type DecisionQuery struct {
	Before  time.Time
	Outcome domain.DecisionOutcome
	Limit   int
}

// DecisionView is a decision with the state of its order ("" = no order, or the order row is
// gone).
type DecisionView struct {
	domain.Decision
	OrderState domain.OrderState
}

// DecisionDetail is a decision with the LLM call that reviewed it (nil = none or purged).
// The call holds the prompt and response as sent and received; never a key.
type DecisionDetail struct {
	DecisionView
	Call *domain.LLMCall
}

// Decisions returns an account's decision log, newest first (viewer; accountID "" = the
// current account).
func (s *Service) Decisions(ctx context.Context, p Principal, accountID string, q DecisionQuery) ([]DecisionView, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return nil, err
	}
	if q.Limit == 0 {
		q.Limit = decisionsDefaultLimit
	}
	if q.Limit < 1 || q.Limit > decisionsMaxLimit {
		return nil, domain.Invalid("limit", "must be 1–500")
	}
	if q.Outcome != "" && !q.Outcome.Valid() {
		return nil, domain.Invalid("outcome", "unknown outcome "+string(q.Outcome))
	}
	v, err := s.account(ctx, p, accountID, domain.AccessView)
	if err != nil {
		return nil, err
	}
	ds, err := s.d.Store.Decisions().List(ctx, v.ID, q.Before, q.Outcome, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("app.Decisions: %w", err)
	}
	out := make([]DecisionView, 0, len(ds))
	for _, d := range ds {
		out = append(out, DecisionView{Decision: d, OrderState: s.decisionOrderState(ctx, d)})
	}
	return out, nil
}

// decisionOrderState is the state of the decision's order, when it has one of the same
// account.
func (s *Service) decisionOrderState(ctx context.Context, d domain.Decision) domain.OrderState {
	if d.OrderRequestID == "" {
		return ""
	}
	o, err := s.d.Store.Orders().Get(ctx, d.OrderRequestID)
	if err != nil || o.AccountID != d.AccountID {
		return ""
	}
	return o.State
}

// Decision returns one decision of an account the caller can view, with its LLM call
// (prompt and response included). Another account's decision is not found.
func (s *Service) Decision(ctx context.Context, p Principal, accountID, id string) (DecisionDetail, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return DecisionDetail{}, err
	}
	v, err := s.account(ctx, p, accountID, domain.AccessView)
	if err != nil {
		return DecisionDetail{}, err
	}
	d, err := s.d.Store.Decisions().Get(ctx, v.ID, id)
	if err != nil {
		return DecisionDetail{}, fmt.Errorf("decision: %w", err)
	}
	out := DecisionDetail{DecisionView: DecisionView{Decision: d, OrderState: s.decisionOrderState(ctx, d)}}
	if d.Verdict.LLMCallID != "" {
		c, err := s.d.Store.LLMCalls().Get(ctx, d.Verdict.LLMCallID)
		switch {
		case err == nil && c.AccountID == d.AccountID:
			out.Call = &c
		case err != nil && !errors.Is(err, domain.ErrNotFound):
			return DecisionDetail{}, fmt.Errorf("app.Decision: %w", err)
		}
	}
	return out, nil
}

// LLMUsageRow is the usage of one profile for one purpose. ProfileName is the profile's
// current name ("" when it was deleted).
type LLMUsageRow struct {
	domain.LLMUsage
	ProfileName string
}

// LLMUsageReport sums the LLM calls of the last Days days (since Since): per profile and
// purpose, and in total.
type LLMUsageReport struct {
	Days  int
	Since time.Time
	Rows  []LLMUsageRow
	Total domain.LLMUsage
}

// LLMUsage reports the LLM calls of the last days days (admin; 0 = 30, at most
// domain.LLMRetentionDays): calls, failures, tokens and cost per profile and purpose. A cost
// is known only for calls whose profile had prices (OD13).
func (s *Service) LLMUsage(ctx context.Context, p Principal, days int) (LLMUsageReport, error) {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return LLMUsageReport{}, err
	}
	if days == 0 {
		days = usageDefaultDays
	}
	if days < 1 || days > domain.LLMRetentionDays {
		return LLMUsageReport{}, domain.Invalid("days", fmt.Sprintf("must be 1–%d", domain.LLMRetentionDays))
	}
	since := s.d.Clock.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	us, err := s.d.Store.LLMCalls().Usage(ctx, since)
	if err != nil {
		return LLMUsageReport{}, fmt.Errorf("app.LLMUsage: %w", err)
	}
	_, ps, err := s.loadLLM(ctx)
	if err != nil {
		return LLMUsageReport{}, fmt.Errorf("app.LLMUsage: %w", err)
	}
	out := LLMUsageReport{Days: days, Since: since, Rows: make([]LLMUsageRow, 0, len(us))}
	for _, u := range us {
		row := LLMUsageRow{LLMUsage: u}
		if i := slices.IndexFunc(ps, func(x domain.LLMProfile) bool { return x.ID == u.ProfileID }); i >= 0 {
			row.ProfileName = ps[i].Name
		}
		out.Rows = append(out.Rows, row)
		out.Total = out.Total.Add(u)
	}
	return out, nil
}

// PurgeDecisionLog deletes decisions and LLM calls older than domain.LLMRetentionDays (OD12;
// called periodically). It returns how many of each were deleted.
func (s *Service) PurgeDecisionLog(ctx context.Context) (decisions, calls int64, err error) {
	before := s.d.Clock.Now().UTC().Add(-domain.LLMRetentionDays * 24 * time.Hour)
	if decisions, err = s.d.Store.Decisions().DeleteBefore(ctx, before); err != nil {
		return 0, 0, fmt.Errorf("app.PurgeDecisionLog: %w", err)
	}
	if calls, err = s.d.Store.LLMCalls().DeleteBefore(ctx, before); err != nil {
		return decisions, 0, fmt.Errorf("app.PurgeDecisionLog: %w", err)
	}
	return decisions, calls, nil
}
