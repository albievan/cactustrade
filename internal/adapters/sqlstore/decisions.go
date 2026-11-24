package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// LLMCalls returns the log of LLM calls.
func (s *Store) LLMCalls() ports.LLMCalls { return llmCalls{s.db} }

// Decisions returns the decision log.
func (s *Store) Decisions() ports.Decisions { return decisions{s.db} }

// ---- llm calls -------------------------------------------------------------------------

type llmCalls struct{ db *sql.DB }

const llmCallCols = "id, account_id, purpose, profile_id, provider, model, prompt, response, input_tokens, output_tokens, " +
	"cost_micros, latency_ms, error, at"

// maxError bounds a stored error message.
const maxError = 500

func (r llmCalls) Add(ctx context.Context, c domain.LLMCall) error {
	if c.ID == "" || !c.Purpose.Valid() {
		return fmt.Errorf("llmCalls.Add: id and a known purpose are required: %w", domain.ErrInvalid)
	}
	_, err := r.db.ExecContext(ctx, "INSERT INTO llm_calls ("+llmCallCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		c.ID, c.AccountID, string(c.Purpose), c.ProfileID, domain.Truncate(string(c.Provider), 32), domain.Truncate(c.Model, 200),
		domain.Truncate(c.Prompt, domain.MaxLLMText), domain.Truncate(c.Response, domain.MaxLLMText),
		max(c.InputTokens, 0), max(c.OutputTokens, 0), int64(max(c.CostMicros, domain.CostUnknown)), max(c.Latency.Milliseconds(), 0),
		domain.Truncate(c.Error, maxError), us(c.At))
	if isDuplicate(err) {
		return fmt.Errorf("llmCalls.Add: id %s: %w", c.ID, domain.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("llmCalls.Add: %w", err)
	}
	return nil
}

func (r llmCalls) Get(ctx context.Context, id string) (domain.LLMCall, error) {
	var c domain.LLMCall
	var purpose, provider string
	var cost, latency, at int64
	err := r.db.QueryRowContext(ctx, "SELECT "+llmCallCols+" FROM llm_calls WHERE id = ?", id).Scan(
		&c.ID, &c.AccountID, &purpose, &c.ProfileID, &provider, &c.Model, &c.Prompt, &c.Response,
		&c.InputTokens, &c.OutputTokens, &cost, &latency, &c.Error, &at)
	if err != nil {
		return domain.LLMCall{}, fmt.Errorf("llmCalls.Get: %w", notFound(err))
	}
	c.Purpose, c.Provider = domain.LLMPurpose(purpose), domain.LLMProviderID(provider)
	c.CostMicros, c.Latency, c.At = domain.Micros(cost), time.Duration(latency)*time.Millisecond, fromUS(at)
	return c, nil
}

func (r llmCalls) Usage(ctx context.Context, since time.Time) ([]domain.LLMUsage, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT profile_id, purpose, COUNT(*), "+
		"SUM(CASE WHEN error <> '' THEN 1 ELSE 0 END), SUM(input_tokens), SUM(output_tokens), "+
		"SUM(CASE WHEN cost_micros >= 0 THEN cost_micros ELSE 0 END), SUM(CASE WHEN cost_micros < 0 THEN 1 ELSE 0 END) "+
		"FROM llm_calls WHERE at >= ? GROUP BY profile_id, purpose ORDER BY profile_id, purpose", us(since))
	if err != nil {
		return nil, fmt.Errorf("llmCalls.Usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.LLMUsage
	for rows.Next() {
		var u domain.LLMUsage
		var purpose string
		var cost int64
		if err := rows.Scan(&u.ProfileID, &purpose, &u.Calls, &u.Failed, &u.InputTokens, &u.OutputTokens, &cost, &u.CostUnknownCalls); err != nil {
			return nil, fmt.Errorf("llmCalls.Usage: %w", err)
		}
		u.Purpose, u.Cost = domain.LLMPurpose(purpose), domain.Micros(cost)
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("llmCalls.Usage: %w", err)
	}
	return out, nil
}

func (r llmCalls) DeleteBefore(ctx context.Context, t time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, "DELETE FROM llm_calls WHERE at < ?", us(t))
	if err != nil {
		return 0, fmt.Errorf("llmCalls.DeleteBefore: %w", err)
	}
	return res.RowsAffected()
}

// ---- decisions -------------------------------------------------------------------------

type decisions struct{ db *sql.DB }

const decisionCols = "id, account_id, at, instrument_id, symbol, direction, playbook, signal_reason, entry_micros, stop_micros, " +
	"take_profit_micros, suggested_cents, probs, llm_call_id, verdict, size_bp, verdict_reason, risk_allowed, risk_size_cents, " +
	"risk_reason, outcome, order_request_id"

// encodeProbs stores the regime probabilities as a JSON object. Values that are not finite
// are left out: JSON has no NaN, and the log must never block a decision.
func encodeProbs(p map[domain.Regime]float64) (string, error) {
	clean := make(map[domain.Regime]float64, len(p))
	for k, v := range p {
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			clean[k] = v
		}
	}
	b, err := json.Marshal(clean)
	return string(b), err
}

func decodeProbs(s string) (map[domain.Regime]float64, error) {
	var p map[domain.Regime]float64
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		return nil, fmt.Errorf("regime probabilities: %w", err)
	}
	if len(p) == 0 {
		return nil, nil
	}
	return p, nil
}

func (r decisions) Add(ctx context.Context, d domain.Decision) error {
	d = d.Clip()
	if err := d.Validate(); err != nil {
		return fmt.Errorf("decisions.Add: %w", err)
	}
	ok, err := accountExists(ctx, r.db, d.AccountID)
	if err != nil {
		return fmt.Errorf("decisions.Add: %w", err)
	}
	if !ok {
		return fmt.Errorf("decisions.Add: account %s: %w", d.AccountID, domain.ErrNotFound)
	}
	probs, err := encodeProbs(d.Signal.RegimeProbs)
	if err != nil {
		return fmt.Errorf("decisions.Add: %w", err)
	}
	s, v, k := d.Signal, d.Verdict, d.Risk
	_, err = r.db.ExecContext(ctx, "INSERT INTO decisions ("+decisionCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		d.ID, d.AccountID, us(d.At), int64(s.Instrument), s.Symbol, string(s.Direction), domain.Truncate(s.Playbook, 64), s.Reason,
		int64(s.Entry), int64(s.StopLoss), int64(s.TakeProfit), int64(s.SuggestedSize), probs,
		v.LLMCallID, string(v.Kind), int64(v.SizeBp), v.Reason, b2i(k.Allowed), int64(k.Size), k.Reason,
		string(d.Outcome), d.OrderRequestID)
	if isDuplicate(err) {
		return fmt.Errorf("decisions.Add: id %s: %w", d.ID, domain.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("decisions.Add: %w", err)
	}
	return nil
}

func (r decisions) Update(ctx context.Context, d domain.Decision) error {
	d = d.Clip()
	if err := d.Validate(); err != nil {
		return fmt.Errorf("decisions.Update: %w", err)
	}
	v, k := d.Verdict, d.Risk
	res, err := r.db.ExecContext(ctx, "UPDATE decisions SET llm_call_id = ?, verdict = ?, size_bp = ?, verdict_reason = ?, "+
		"risk_allowed = ?, risk_size_cents = ?, risk_reason = ?, outcome = ?, order_request_id = ? WHERE id = ? AND account_id = ?",
		v.LLMCallID, string(v.Kind), int64(v.SizeBp), v.Reason, b2i(k.Allowed), int64(k.Size), k.Reason,
		string(d.Outcome), d.OrderRequestID, d.ID, d.AccountID)
	if err := mustAffect(res, err); err != nil {
		return fmt.Errorf("decisions.Update: %w", err)
	}
	return nil
}

func scanDecision(sc interface{ Scan(...any) error }) (domain.Decision, error) {
	var d domain.Decision
	var at, inst, entry, stop, tp, suggested, bp, allowed, riskSize int64
	var dir, probs, verdict, outcome string
	err := sc.Scan(&d.ID, &d.AccountID, &at, &inst, &d.Signal.Symbol, &dir, &d.Signal.Playbook, &d.Signal.Reason,
		&entry, &stop, &tp, &suggested, &probs, &d.Verdict.LLMCallID, &verdict, &bp, &d.Verdict.Reason,
		&allowed, &riskSize, &d.Risk.Reason, &outcome, &d.OrderRequestID)
	if err != nil {
		return domain.Decision{}, err
	}
	if d.Signal.RegimeProbs, err = decodeProbs(probs); err != nil {
		return domain.Decision{}, err
	}
	d.At, d.Signal.Instrument, d.Signal.Direction = fromUS(at), domain.InstrumentID(inst), domain.Direction(dir)
	d.Signal.Entry, d.Signal.StopLoss, d.Signal.TakeProfit = domain.Micros(entry), domain.Micros(stop), domain.Micros(tp)
	d.Signal.SuggestedSize = domain.Cents(suggested)
	d.Verdict.Kind, d.Verdict.SizeBp = domain.VerdictKind(verdict), domain.BasisPoints(bp)
	d.Risk.Allowed, d.Risk.Size = allowed != 0, domain.Cents(riskSize)
	d.Outcome = domain.DecisionOutcome(outcome)
	return d, nil
}

func (r decisions) Get(ctx context.Context, accountID, id string) (domain.Decision, error) {
	d, err := scanDecision(r.db.QueryRowContext(ctx, "SELECT "+decisionCols+" FROM decisions WHERE id = ? AND account_id = ?", id, accountID))
	if err != nil {
		return domain.Decision{}, fmt.Errorf("decisions.Get: %w", notFound(err))
	}
	return d, nil
}

func (r decisions) List(ctx context.Context, accountID string, before time.Time, outcome domain.DecisionOutcome, limit int) ([]domain.Decision, error) {
	q, args := "SELECT "+decisionCols+" FROM decisions WHERE account_id = ?", []any{accountID}
	if !before.IsZero() {
		q, args = q+" AND at < ?", append(args, us(before))
	}
	if outcome != "" {
		q, args = q+" AND outcome = ?", append(args, string(outcome))
	}
	rows, err := r.db.QueryContext(ctx, q+" ORDER BY at DESC, id DESC LIMIT ?", append(args, max(limit, 0))...)
	if err != nil {
		return nil, fmt.Errorf("decisions.List: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Decision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("decisions.List: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("decisions.List: %w", err)
	}
	return out, nil
}

func (r decisions) DeleteBefore(ctx context.Context, t time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, "DELETE FROM decisions WHERE at < ?", us(t))
	if err != nil {
		return 0, fmt.Errorf("decisions.DeleteBefore: %w", err)
	}
	return res.RowsAffected()
}
