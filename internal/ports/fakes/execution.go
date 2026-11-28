package fakes

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// execState holds the execution repositories of the in-memory Store (guarded by Store.mu).
type execState struct {
	orders    map[string]domain.Order
	positions map[string]map[int64]domain.AgentPosition // account → position
	risk      map[string]domain.AccountRiskState
	blocks    map[string]map[domain.InstrumentID]domain.InstrumentBlock
	leases    map[string]domain.AgentLease
	gates     map[string]domain.RealGate
	// t212 is Trading 212's instrument catalogue by ticker; matches the instrument matches
	// per account (R45).
	t212    map[string]domain.T212Instrument
	matches map[string]map[domain.InstrumentID]domain.InstrumentMatch
}

func (e *execState) init() {
	if e.orders == nil {
		e.orders = map[string]domain.Order{}
		e.positions = map[string]map[int64]domain.AgentPosition{}
		e.risk = map[string]domain.AccountRiskState{}
		e.blocks = map[string]map[domain.InstrumentID]domain.InstrumentBlock{}
		e.leases = map[string]domain.AgentLease{}
		e.gates = map[string]domain.RealGate{}
		e.t212 = map[string]domain.T212Instrument{}
		e.matches = map[string]map[domain.InstrumentID]domain.InstrumentMatch{}
	}
}

// deleteAccount cascades an account's deletion, like the database's foreign keys.
func (e *execState) deleteAccount(id string) {
	e.init()
	maps.DeleteFunc(e.orders, func(_ string, o domain.Order) bool { return o.AccountID == id })
	delete(e.positions, id)
	delete(e.risk, id)
	delete(e.blocks, id)
	delete(e.leases, id)
	delete(e.gates, id)
	delete(e.matches, id)
}

// T212Instruments returns the in-memory Trading 212 catalogue (R45).
func (s *Store) T212Instruments() ports.T212Instruments { return t212Repo{s} }

// InstrumentMatches returns the in-memory instrument matches (R45).
func (s *Store) InstrumentMatches() ports.InstrumentMatches { return matchRepo{s} }

type t212Repo struct{ s *Store }

func (r t212Repo) ReplaceAll(_ context.Context, ins []domain.T212Instrument, at time.Time) error {
	defer r.s.lock()()
	if r.s.T212Err != nil {
		return r.s.T212Err
	}
	next := make(map[string]domain.T212Instrument, len(ins))
	for _, in := range ins {
		switch _, dup := next[in.Ticker]; {
		case in.Ticker == "" || len(in.Ticker) > 64:
			return domain.Invalid("ticker", "1-64 characters")
		case dup:
			return domain.ErrConflict
		}
		in.ShortName = strings.ToUpper(strings.TrimSpace(in.ShortName))
		in.FetchedAt = at.UTC()
		next[in.Ticker] = in
	}
	r.s.exec.t212 = next
	return nil
}

func (r t212Repo) Get(_ context.Context, ticker string) (domain.T212Instrument, error) {
	defer r.s.lock()()
	if r.s.T212Err != nil {
		return domain.T212Instrument{}, r.s.T212Err
	}
	in, ok := r.s.exec.t212[ticker]
	if !ok {
		return domain.T212Instrument{}, domain.ErrNotFound
	}
	return in, nil
}

func (r t212Repo) ByShortName(_ context.Context, shortName string) ([]domain.T212Instrument, error) {
	defer r.s.lock()()
	if r.s.T212Err != nil {
		return nil, r.s.T212Err
	}
	key := strings.ToUpper(strings.TrimSpace(shortName))
	var out []domain.T212Instrument
	for _, in := range r.s.exec.t212 {
		if in.ShortName == key {
			out = append(out, in)
		}
	}
	slices.SortFunc(out, func(a, b domain.T212Instrument) int { return cmp.Compare(a.Ticker, b.Ticker) })
	return out, nil
}

func (r t212Repo) FetchedAt(context.Context) (time.Time, error) {
	defer r.s.lock()()
	if r.s.T212Err != nil {
		return time.Time{}, r.s.T212Err
	}
	for _, in := range r.s.exec.t212 {
		return in.FetchedAt, nil // all share one fetch time
	}
	return time.Time{}, domain.ErrNotFound
}

type matchRepo struct{ s *Store }

func (r matchRepo) Put(_ context.Context, m domain.InstrumentMatch) error {
	defer r.s.lock()()
	if _, ok := r.s.accounts[m.AccountID]; !ok {
		return domain.ErrNotFound
	}
	if r.s.exec.matches[m.AccountID] == nil {
		r.s.exec.matches[m.AccountID] = map[domain.InstrumentID]domain.InstrumentMatch{}
	}
	r.s.exec.matches[m.AccountID][m.Instrument] = m
	return nil
}

func (r matchRepo) Get(_ context.Context, accountID string, id domain.InstrumentID) (domain.InstrumentMatch, error) {
	defer r.s.lock()()
	m, ok := r.s.exec.matches[accountID][id]
	if !ok {
		return domain.InstrumentMatch{}, domain.ErrNotFound
	}
	return m, nil
}

func (r matchRepo) List(_ context.Context, accountID string) ([]domain.InstrumentMatch, error) {
	defer r.s.lock()()
	out := slices.Collect(maps.Values(r.s.exec.matches[accountID]))
	slices.SortFunc(out, func(a, b domain.InstrumentMatch) int { return cmp.Compare(a.Instrument, b.Instrument) })
	return out, nil
}

func (r matchRepo) Delete(_ context.Context, accountID string, id domain.InstrumentID) error {
	defer r.s.lock()()
	if _, ok := r.s.exec.matches[accountID][id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.s.exec.matches[accountID], id)
	return nil
}

// Orders returns the in-memory orders repository.
func (s *Store) Orders() ports.Orders { return orderRepo{s} }

// AgentPositions returns the in-memory agent positions.
func (s *Store) AgentPositions() ports.AgentPositions { return agentPosRepo{s} }

// RiskStates returns the in-memory risk states.
func (s *Store) RiskStates() ports.RiskStates { return riskRepo{s} }

// InstrumentBlocks returns the in-memory instrument blocks.
func (s *Store) InstrumentBlocks() ports.InstrumentBlocks { return blockRepo{s} }

// RealGates returns the in-memory real-mode gates (PLAN 7.12).
func (s *Store) RealGates() ports.RealGates { return gateRepo{s} }

// AgentLeases returns the in-memory leases.
func (s *Store) AgentLeases() ports.AgentLeases { return leaseRepo{s} }

// lock takes the store's mutex and makes sure the execution maps exist.
func (s *Store) lock() func() {
	s.mu.Lock()
	s.exec.init()
	return s.mu.Unlock
}

type orderRepo struct{ s *Store }

func (r orderRepo) Create(_ context.Context, o domain.Order) error {
	defer r.s.lock()()
	if r.s.OrdersErr != nil {
		return r.s.OrdersErr
	}
	if _, ok := r.s.accounts[o.AccountID]; !ok {
		return domain.ErrNotFound
	}
	if _, ok := r.s.exec.orders[o.RequestID]; ok {
		return domain.ErrConflict
	}
	o.Platform = o.OnPlatform() // stored as the SQL store does: "" reads back as eToro
	r.s.exec.orders[o.RequestID] = o
	return nil
}

func (r orderRepo) Get(_ context.Context, id string) (domain.Order, error) {
	defer r.s.lock()()
	o, ok := r.s.exec.orders[id]
	if !ok {
		return domain.Order{}, domain.ErrNotFound
	}
	return o, nil
}

func (r orderRepo) Update(_ context.Context, prev, next domain.Order) error {
	defer r.s.lock()()
	if r.s.OrdersErr != nil {
		return r.s.OrdersErr
	}
	cur, ok := r.s.exec.orders[prev.RequestID]
	switch {
	case !ok || next.RequestID != prev.RequestID:
		return domain.ErrNotFound
	case cur.State != prev.State || cur.Sends != prev.Sends:
		return domain.ErrConflict
	}
	cur.State, cur.Reason, cur.Sends = next.State, next.Reason, next.Sends
	cur.SentAt, cur.UpdatedAt = next.SentAt, next.UpdatedAt
	cur.PositionID, cur.EtoroOrderID = next.PositionID, next.EtoroOrderID
	r.s.exec.orders[prev.RequestID] = cur
	return nil
}

func (r orderRepo) ListOpen(_ context.Context, accountID string) ([]domain.Order, error) {
	defer r.s.lock()()
	var out []domain.Order
	for _, o := range r.s.exec.orders {
		if o.AccountID == accountID && !o.State.Final() {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b domain.Order) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.RequestID, b.RequestID))
	})
	return out, nil
}

func (r orderRepo) List(_ context.Context, accountID string, limit int) ([]domain.Order, error) {
	defer r.s.lock()()
	var out []domain.Order
	for _, o := range r.s.exec.orders {
		if o.AccountID == accountID {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b domain.Order) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.RequestID, a.RequestID))
	})
	if limit >= 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r orderRepo) ListSince(_ context.Context, accountID string, since time.Time) ([]domain.Order, error) {
	defer r.s.lock()()
	var out []domain.Order
	for _, o := range r.s.exec.orders {
		if o.AccountID == accountID && !o.CreatedAt.Before(since) {
			out = append(out, o)
		}
	}
	slices.SortFunc(out, func(a, b domain.Order) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.RequestID, b.RequestID))
	})
	return out, nil
}

type agentPosRepo struct{ s *Store }

func (r agentPosRepo) Add(_ context.Context, p domain.AgentPosition) error {
	defer r.s.lock()()
	if _, ok := r.s.accounts[p.AccountID]; !ok {
		return domain.ErrNotFound
	}
	if _, ok := r.s.exec.positions[p.AccountID][p.PositionID]; ok {
		return domain.ErrConflict
	}
	if r.s.exec.positions[p.AccountID] == nil {
		r.s.exec.positions[p.AccountID] = map[int64]domain.AgentPosition{}
	}
	r.s.exec.positions[p.AccountID][p.PositionID] = p
	return nil
}

func (r agentPosRepo) Get(_ context.Context, accountID string, id int64) (domain.AgentPosition, error) {
	defer r.s.lock()()
	p, ok := r.s.exec.positions[accountID][id]
	if !ok {
		return domain.AgentPosition{}, domain.ErrNotFound
	}
	return p, nil
}

func (r agentPosRepo) Update(_ context.Context, p domain.AgentPosition) error {
	defer r.s.lock()()
	cur, ok := r.s.exec.positions[p.AccountID][p.PositionID]
	if !ok {
		return domain.ErrNotFound
	}
	cur.ClosedAt, cur.CloseRequestID = p.ClosedAt, p.CloseRequestID
	r.s.exec.positions[p.AccountID][p.PositionID] = cur
	return nil
}

func (r agentPosRepo) ListOpen(_ context.Context, accountID string) ([]domain.AgentPosition, error) {
	defer r.s.lock()()
	var out []domain.AgentPosition
	for _, p := range r.s.exec.positions[accountID] {
		if p.Open() {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b domain.AgentPosition) int {
		return cmp.Or(a.OpenedAt.Compare(b.OpenedAt), cmp.Compare(a.PositionID, b.PositionID))
	})
	return out, nil
}

func (r agentPosRepo) ListSince(_ context.Context, accountID string, since time.Time) ([]domain.AgentPosition, error) {
	defer r.s.lock()()
	var out []domain.AgentPosition
	for _, p := range r.s.exec.positions[accountID] {
		if !p.OpenedAt.Before(since) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b domain.AgentPosition) int {
		return cmp.Or(a.OpenedAt.Compare(b.OpenedAt), cmp.Compare(a.PositionID, b.PositionID))
	})
	return out, nil
}

type riskRepo struct{ s *Store }

func (r riskRepo) Get(_ context.Context, accountID string) (domain.AccountRiskState, error) {
	defer r.s.lock()()
	st, ok := r.s.exec.risk[accountID]
	if !ok {
		return domain.AccountRiskState{}, domain.ErrNotFound
	}
	return st, nil
}

func (r riskRepo) Put(_ context.Context, st domain.AccountRiskState) (domain.AccountRiskState, error) {
	defer r.s.lock()()
	if _, ok := r.s.accounts[st.AccountID]; !ok {
		return domain.AccountRiskState{}, domain.ErrNotFound
	}
	cur, ok := r.s.exec.risk[st.AccountID]
	if (ok && cur.Version != st.Version) || (!ok && st.Version != 0) {
		return domain.AccountRiskState{}, domain.ErrConflict
	}
	// Reservations are never stored: they come from the orders.
	st.State.Reserved, st.State.ReservedOrders, st.State.ReservedShort = 0, 0, 0
	st.Version++
	r.s.exec.risk[st.AccountID] = st
	return st, nil
}

type blockRepo struct{ s *Store }

func (r blockRepo) Block(_ context.Context, b domain.InstrumentBlock) error {
	defer r.s.lock()()
	if _, ok := r.s.accounts[b.AccountID]; !ok {
		return domain.ErrNotFound
	}
	if r.s.exec.blocks[b.AccountID] == nil {
		r.s.exec.blocks[b.AccountID] = map[domain.InstrumentID]domain.InstrumentBlock{}
	}
	r.s.exec.blocks[b.AccountID][b.Instrument] = b
	return nil
}

func (r blockRepo) Unblock(_ context.Context, accountID string, id domain.InstrumentID) error {
	defer r.s.lock()()
	if _, ok := r.s.exec.blocks[accountID][id]; !ok {
		return domain.ErrNotFound
	}
	delete(r.s.exec.blocks[accountID], id)
	return nil
}

func (r blockRepo) List(_ context.Context, accountID string) ([]domain.InstrumentBlock, error) {
	defer r.s.lock()()
	out := slices.Collect(maps.Values(r.s.exec.blocks[accountID]))
	slices.SortFunc(out, func(a, b domain.InstrumentBlock) int { return cmp.Compare(a.Instrument, b.Instrument) })
	return out, nil
}

type leaseRepo struct{ s *Store }

func (r leaseRepo) Acquire(_ context.Context, accountID, holder string, now, until time.Time) error {
	defer r.s.lock()()
	if _, ok := r.s.accounts[accountID]; !ok {
		return domain.ErrNotFound
	}
	if cur, ok := r.s.exec.leases[accountID]; ok && cur.Holder != holder && cur.ExpiresAt.After(now) {
		return domain.ErrConflict
	}
	r.s.exec.leases[accountID] = domain.AgentLease{AccountID: accountID, Holder: holder, ExpiresAt: until}
	return nil
}

func (r leaseRepo) Release(_ context.Context, accountID, holder string) error {
	defer r.s.lock()()
	if cur, ok := r.s.exec.leases[accountID]; ok && cur.Holder == holder {
		delete(r.s.exec.leases, accountID)
	}
	return nil
}

func (r leaseRepo) Get(_ context.Context, accountID string) (domain.AgentLease, error) {
	defer r.s.lock()()
	l, ok := r.s.exec.leases[accountID]
	if !ok {
		return domain.AgentLease{}, domain.ErrNotFound
	}
	return l, nil
}

type gateRepo struct{ s *Store }

func (r gateRepo) Get(_ context.Context, accountID string) (domain.RealGate, error) {
	defer r.s.lock()()
	if r.s.GatesErr != nil {
		return domain.RealGate{}, r.s.GatesErr
	}
	g, ok := r.s.exec.gates[accountID]
	if !ok {
		return domain.RealGate{}, domain.ErrNotFound
	}
	g.Evidence.Days = slices.Clone(g.Evidence.Days)
	return g, nil
}

func (r gateRepo) Put(_ context.Context, g domain.RealGate) (domain.RealGate, error) {
	defer r.s.lock()()
	if r.s.GatesErr != nil {
		return domain.RealGate{}, r.s.GatesErr
	}
	if err := g.Validate(); err != nil {
		return domain.RealGate{}, err
	}
	if _, ok := r.s.accounts[g.AccountID]; !ok {
		return domain.RealGate{}, domain.ErrNotFound
	}
	cur, ok := r.s.exec.gates[g.AccountID]
	if (ok && cur.Version != g.Version) || (!ok && g.Version != 0) {
		return domain.RealGate{}, domain.ErrConflict
	}
	g.Version++
	g.Evidence.Days = slices.Clone(g.Evidence.Days)
	r.s.exec.gates[g.AccountID] = g
	return g, nil
}
