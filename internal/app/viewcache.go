package app

import (
	"context"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// The portfolio view cache (PLAN 6.6a). It keeps the last portfolio read per account, so
// many open dashboards cannot crowd the supervisor's eToro quota: the supervisor stores its
// own read here (no extra call), and the view use cases (AgentStatusView, LiveSnapshot) read
// it, refreshing it at most every viewCacheTTL with one read per account in flight.
//
// GUARDRAIL (CLAUDE.md §2.2): only view use cases read this cache. Nothing that sizes,
// assesses or sends an order may read it: every entry reads eToro's cash fresh (agent.go,
// executor.go, risk.go and supervisor.go; TestViewCacheReadersAreViewsOnly enforces it).

// viewCacheTTL is how old a cached portfolio may be before a view reads eToro again.
const viewCacheTTL = 10 * time.Second

// viewCache holds the last portfolio per account and the read in flight, if any.
type viewCache struct {
	mu      sync.Mutex
	entries map[string]viewEntry
	flights map[string]*viewFlight
}

// viewEntry is a portfolio and when it was read.
type viewEntry struct {
	pf domain.Portfolio
	at time.Time
}

// viewFlight is one portfolio read that other views wait for instead of reading too.
type viewFlight struct {
	done chan struct{}
	pf   domain.Portfolio
	at   time.Time
	err  error
}

func newViewCache() *viewCache {
	return &viewCache{entries: map[string]viewEntry{}, flights: map[string]*viewFlight{}}
}

// put stores a portfolio read at at, unless a newer one is already stored.
func (c *viewCache) put(accountID string, pf domain.Portfolio, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok := c.entries[accountID]; ok && cur.at.After(at) {
		return
	}
	c.entries[accountID] = viewEntry{pf: pf, at: at}
}

// peek returns the cached portfolio of an account and when it was read (ok false: none).
func (c *viewCache) peek(accountID string) (domain.Portfolio, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[accountID]
	return e.pf, e.at, ok
}

// viewPortfolio returns an account's portfolio for a view: the cached one when it is younger
// than viewCacheTTL, else one read from eToro, shared with every view asking meanwhile (one
// read per account in flight). The read is bounded by agentViewTimeout and does not end when
// the first caller goes away. Never call it from an order path (see the guardrail above).
func (s *Service) viewPortfolio(ctx context.Context, v domain.AccountView) (domain.Portfolio, time.Time, error) {
	c := s.rt.views
	c.mu.Lock()
	if e, ok := c.entries[v.ID]; ok && s.d.Clock.Now().Sub(e.at) < viewCacheTTL {
		c.mu.Unlock()
		return e.pf, e.at, nil
	}
	if f, ok := c.flights[v.ID]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.pf, f.at, f.err
		case <-ctx.Done():
			return domain.Portfolio{}, time.Time{}, ctx.Err()
		}
	}
	f := &viewFlight{done: make(chan struct{})}
	c.flights[v.ID] = f
	c.mu.Unlock()

	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentViewTimeout)
	f.pf, f.err = s.d.Broker.Portfolio(brokerCtx(rctx, v))
	cancel()
	f.at = s.d.Clock.Now()

	// Stored and the flight ended under one lock: a view arriving now finds the new entry
	// instead of starting a second read.
	c.mu.Lock()
	if cur, ok := c.entries[v.ID]; f.err == nil && (!ok || !cur.at.After(f.at)) {
		c.entries[v.ID] = viewEntry{pf: f.pf, at: f.at}
	}
	delete(c.flights, v.ID)
	c.mu.Unlock()
	close(f.done)
	return f.pf, f.at, f.err
}
