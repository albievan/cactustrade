package fakes

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// logState holds the decision log and the LLM calls of the in-memory Store (guarded by
// Store.mu).
type logState struct {
	calls     map[string]domain.LLMCall
	decisions map[string]domain.Decision
}

func (l *logState) init() {
	if l.calls == nil {
		l.calls = map[string]domain.LLMCall{}
		l.decisions = map[string]domain.Decision{}
	}
}

// LLMCalls returns the in-memory LLM call log.
func (s *Store) LLMCalls() ports.LLMCalls { return llmCallRepo{s} }

// Decisions returns the in-memory decision log.
func (s *Store) Decisions() ports.Decisions { return decisionRepo{s} }

func (s *Store) lockLog() func() {
	s.mu.Lock()
	s.log.init()
	return s.mu.Unlock
}

type llmCallRepo struct{ s *Store }

func (r llmCallRepo) Add(_ context.Context, c domain.LLMCall) error {
	defer r.s.lockLog()()
	if r.s.LLMCallsErr != nil {
		return r.s.LLMCallsErr
	}
	if c.ID == "" || !c.Purpose.Valid() {
		return domain.ErrInvalid
	}
	if _, ok := r.s.log.calls[c.ID]; ok {
		return domain.ErrConflict
	}
	c.Prompt, c.Response = domain.Truncate(c.Prompt, domain.MaxLLMText), domain.Truncate(c.Response, domain.MaxLLMText)
	c.Error = domain.Truncate(c.Error, 500)
	c.Latency = c.Latency.Truncate(time.Millisecond)
	r.s.log.calls[c.ID] = c
	return nil
}

func (r llmCallRepo) Get(_ context.Context, id string) (domain.LLMCall, error) {
	defer r.s.lockLog()()
	c, ok := r.s.log.calls[id]
	if !ok {
		return domain.LLMCall{}, domain.ErrNotFound
	}
	return c, nil
}

func (r llmCallRepo) DeleteBefore(_ context.Context, t time.Time) (int64, error) {
	defer r.s.lockLog()()
	n := len(r.s.log.calls)
	maps.DeleteFunc(r.s.log.calls, func(_ string, c domain.LLMCall) bool { return c.At.Before(t) })
	return int64(n - len(r.s.log.calls)), nil
}

type decisionRepo struct{ s *Store }

func (r decisionRepo) Add(_ context.Context, d domain.Decision) error {
	defer r.s.lockLog()()
	if r.s.DecisionsErr != nil {
		return r.s.DecisionsErr
	}
	d = d.Clip()
	if err := d.Validate(); err != nil {
		return err
	}
	if _, ok := r.s.accounts[d.AccountID]; !ok {
		return domain.ErrNotFound
	}
	if _, ok := r.s.log.decisions[d.ID]; ok {
		return domain.ErrConflict
	}
	d.Signal.RegimeProbs = maps.Clone(d.Signal.RegimeProbs)
	r.s.log.decisions[d.ID] = d
	return nil
}

func (r decisionRepo) Update(_ context.Context, d domain.Decision) error {
	defer r.s.lockLog()()
	if r.s.DecisionsErr != nil {
		return r.s.DecisionsErr
	}
	d = d.Clip()
	if err := d.Validate(); err != nil {
		return err
	}
	old, ok := r.s.log.decisions[d.ID]
	if !ok || old.AccountID != d.AccountID {
		return domain.ErrNotFound
	}
	old.Verdict, old.Risk, old.Outcome, old.OrderRequestID = d.Verdict, d.Risk, d.Outcome, d.OrderRequestID
	r.s.log.decisions[d.ID] = old
	return nil
}

func (r decisionRepo) Get(_ context.Context, accountID, id string) (domain.Decision, error) {
	defer r.s.lockLog()()
	d, ok := r.s.log.decisions[id]
	if !ok || d.AccountID != accountID {
		return domain.Decision{}, domain.ErrNotFound
	}
	d.Signal.RegimeProbs = maps.Clone(d.Signal.RegimeProbs)
	return d, nil
}

func (r decisionRepo) List(_ context.Context, accountID string, before time.Time, outcome domain.DecisionOutcome, limit int) ([]domain.Decision, error) {
	defer r.s.lockLog()()
	var out []domain.Decision
	for _, d := range r.s.log.decisions {
		if d.AccountID == accountID && (before.IsZero() || d.At.Before(before)) && (outcome == "" || d.Outcome == outcome) {
			d.Signal.RegimeProbs = maps.Clone(d.Signal.RegimeProbs)
			out = append(out, d)
		}
	}
	slices.SortFunc(out, func(a, b domain.Decision) int {
		return cmp.Or(b.At.Compare(a.At), cmp.Compare(b.ID, a.ID))
	})
	return out[:min(len(out), max(limit, 0))], nil
}

func (r decisionRepo) DeleteBefore(_ context.Context, t time.Time) (int64, error) {
	defer r.s.lockLog()()
	n := len(r.s.log.decisions)
	maps.DeleteFunc(r.s.log.decisions, func(_ string, d domain.Decision) bool { return d.At.Before(t) })
	return int64(n - len(r.s.log.decisions)), nil
}

// deleteAccount cascades an account's deletion to its decisions (LLM calls stay, as in the
// database: they have no foreign key).
func (l *logState) deleteAccount(id string) {
	l.init()
	maps.DeleteFunc(l.decisions, func(_ string, d domain.Decision) bool { return d.AccountID == id })
}
