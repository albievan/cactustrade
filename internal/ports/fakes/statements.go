package fakes

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// StatementSchedules returns the in-memory statement schedules (R30).
func (s *Store) StatementSchedules() ports.StatementSchedules { return stmtRepo{s} }

type stmtRepo struct{ s *Store }

func cloneSchedule(x domain.StatementSchedule) domain.StatementSchedule {
	x.AccountIDs, x.Recipients = slices.Clone(x.AccountIDs), slices.Clone(x.Recipients)
	return x
}

func (r stmtRepo) Create(_ context.Context, x domain.StatementSchedule) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if r.s.stmts == nil {
		r.s.stmts = map[string]domain.StatementSchedule{}
	}
	if _, ok := r.s.stmts[x.ID]; ok {
		return domain.ErrConflict
	}
	if _, ok := r.s.users[x.OwnerID]; !ok {
		return domain.ErrNotFound
	}
	r.s.stmts[x.ID] = cloneSchedule(x)
	return nil
}

func (r stmtRepo) Get(_ context.Context, id string) (domain.StatementSchedule, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	x, ok := r.s.stmts[id]
	if !ok {
		return domain.StatementSchedule{}, domain.ErrNotFound
	}
	return cloneSchedule(x), nil
}

func (r stmtRepo) List(_ context.Context, ownerID string) ([]domain.StatementSchedule, error) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []domain.StatementSchedule
	for _, x := range r.s.stmts {
		if ownerID == "" || x.OwnerID == ownerID {
			out = append(out, cloneSchedule(x))
		}
	}
	slices.SortFunc(out, func(a, b domain.StatementSchedule) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (r stmtRepo) Update(_ context.Context, x domain.StatementSchedule) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	cur, ok := r.s.stmts[x.ID]
	if !ok {
		return domain.ErrNotFound
	}
	cur.Period, cur.AccountIDs, cur.Recipients = x.Period, slices.Clone(x.AccountIDs), slices.Clone(x.Recipients)
	cur.IncludeCombined, cur.IncludeAccounts, cur.IncludeExchanges, cur.Enabled = x.IncludeCombined, x.IncludeAccounts, x.IncludeExchanges, x.Enabled
	cur.LastPeriod, cur.UpdatedAt = x.LastPeriod, x.UpdatedAt
	r.s.stmts[x.ID] = cur
	return nil
}

func (r stmtRepo) Delete(_ context.Context, id string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	if _, ok := r.s.stmts[id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.s.stmts, id)
	return nil
}

func (r stmtRepo) MarkPeriod(_ context.Context, id, prev, key string, at time.Time, lastErr string) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	cur, ok := r.s.stmts[id]
	switch {
	case !ok:
		return domain.ErrNotFound
	case cur.LastPeriod != prev:
		return domain.ErrConflict
	}
	if len(lastErr) > 500 {
		lastErr = lastErr[:500]
	}
	cur.LastPeriod, cur.LastSentAt, cur.LastError = key, at, lastErr
	r.s.stmts[id] = cur
	return nil
}

func (r stmtRepo) MarkManual(_ context.Context, id string, prev, at time.Time) error {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	cur, ok := r.s.stmts[id]
	switch {
	case !ok:
		return domain.ErrNotFound
	case !cur.ManualSentAt.Equal(prev):
		return domain.ErrConflict
	}
	cur.ManualSentAt = at
	r.s.stmts[id] = cur
	return nil
}

// UsageFor sums the calls of the given accounts made in [from, to).
func (r llmCallRepo) UsageFor(_ context.Context, accountIDs []string, from, to time.Time) (domain.LLMUsage, error) {
	defer r.s.lockLog()()
	var u domain.LLMUsage
	for _, c := range r.s.log.calls {
		if !slices.Contains(accountIDs, c.AccountID) || c.At.Before(from) || !c.At.Before(to) {
			continue
		}
		x := domain.LLMUsage{Calls: 1, InputTokens: max(c.InputTokens, 0), OutputTokens: max(c.OutputTokens, 0)}
		if c.Error != "" {
			x.Failed = 1
		}
		if c.CostMicros >= 0 {
			x.Cost = c.CostMicros
		} else {
			x.CostUnknownCalls = 1
		}
		u = u.Add(x)
	}
	return u, nil
}

// StatementRenderer records the statements it renders and returns a small stand-in document
// ("%PDF-fake " + the title), or Size bytes when Size > 0; Err (when set) is returned instead.
type StatementRenderer struct {
	mu       sync.Mutex
	Rendered []domain.Statement
	Size     int
	Err      error
}

var _ ports.StatementRenderer = (*StatementRenderer)(nil)

// Render records st.
func (r *StatementRenderer) Render(_ context.Context, st domain.Statement) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return nil, r.Err
	}
	r.Rendered = append(r.Rendered, st)
	if r.Size > 0 {
		return make([]byte, r.Size), nil
	}
	return []byte("%PDF-fake " + st.Title), nil
}

// All returns the statements rendered so far.
func (r *StatementRenderer) All() []domain.Statement {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.Rendered)
}

// AnalysisRenderer records the analyses it renders and returns a small stand-in document
// ("%PDF-fake " + the title); Err (when set) is returned instead.
type AnalysisRenderer struct {
	mu       sync.Mutex
	Rendered []domain.Analysis
	Err      error
}

var _ ports.AnalysisRenderer = (*AnalysisRenderer)(nil)

// RenderAnalysis records a.
func (r *AnalysisRenderer) RenderAnalysis(_ context.Context, a domain.Analysis) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Err != nil {
		return nil, r.Err
	}
	r.Rendered = append(r.Rendered, a)
	return []byte("%PDF-fake " + a.Title), nil
}

// All returns the analyses rendered so far.
func (r *AnalysisRenderer) All() []domain.Analysis {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.Rendered)
}
