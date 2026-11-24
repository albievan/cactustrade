package fakes

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Trades returns the in-memory closed trades (PLAN 6.4).
func (s *Store) Trades() ports.Trades { return tradeRepo{s} }

type tradeRepo struct{ s *Store }

func (r tradeRepo) lock() func() {
	r.s.mu.Lock()
	if r.s.trades == nil {
		r.s.trades = map[string]map[int64]domain.Trade{}
	}
	return r.s.mu.Unlock
}

func (r tradeRepo) Upsert(_ context.Context, t domain.Trade) error {
	defer r.lock()()
	if r.s.TradesErr != nil {
		return r.s.TradesErr
	}
	if err := t.Validate(); err != nil {
		return err
	}
	if _, ok := r.s.accounts[t.AccountID]; !ok {
		return domain.ErrNotFound
	}
	if r.s.trades[t.AccountID] == nil {
		r.s.trades[t.AccountID] = map[int64]domain.Trade{}
	}
	t.OpenedAt, t.ClosedAt, t.SyncedAt = t.OpenedAt.Truncate(time.Microsecond), t.ClosedAt.Truncate(time.Microsecond), t.SyncedAt.Truncate(time.Microsecond)
	r.s.trades[t.AccountID][t.PositionID] = t
	return nil
}

func (r tradeRepo) List(_ context.Context, accountID string, f ports.TradeFilter) ([]domain.Trade, error) {
	defer r.lock()()
	if r.s.TradesErr != nil {
		return nil, r.s.TradesErr
	}
	var out []domain.Trade
	for _, t := range r.s.trades[accountID] {
		switch {
		case !f.From.IsZero() && t.ClosedAt.Before(f.From),
			!f.To.IsZero() && !t.ClosedAt.Before(f.To),
			!f.Before.IsZero() && !t.ClosedAt.Before(f.Before),
			f.Ours != nil && t.Ours != *f.Ours:
			continue
		}
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b domain.Trade) int {
		return cmp.Or(b.ClosedAt.Compare(a.ClosedAt), cmp.Compare(b.PositionID, a.PositionID))
	})
	return out[:min(len(out), max(f.Limit, 0))], nil
}

func (r tradeRepo) LastClosedAt(_ context.Context, accountID string) (time.Time, error) {
	defer r.lock()()
	if r.s.TradesErr != nil {
		return time.Time{}, r.s.TradesErr
	}
	var last time.Time
	for _, t := range r.s.trades[accountID] {
		if t.ClosedAt.After(last) {
			last = t.ClosedAt
		}
	}
	return last, nil
}
