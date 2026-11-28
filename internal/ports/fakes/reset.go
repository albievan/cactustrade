package fakes

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// DataReset returns the in-memory data reset (PLAN R49).
func (s *Store) DataReset() ports.DataReset { return dataResetRepo{s} }

// dataResetRepo is the data reset.
type dataResetRepo struct{ s *Store }

// resetDerived are the settings prefixes a reset deletes; resetWatchDerived goes with the
// watchlist (as sqlstore's resetSettings).
var (
	resetDerived      = []string{"agent.aggressiveness_adapt.", "research.state.", "events.seen."}
	resetWatchDerived = []string{"watchlist.signals."}
)

// Counts returns the rows of the in-memory tables the reset wipes, plus those it keeps that
// the app reads (settings, real_gates, risk_state, watchlist).
func (r dataResetRepo) Counts(context.Context) (map[string]int64, error) {
	s := r.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ResetErr != nil {
		return nil, s.ResetErr
	}
	s.exec.init()
	s.log.init()
	positions, watch, trades := 0, 0, 0
	for _, m := range s.exec.positions {
		positions += len(m)
	}
	for _, m := range s.watch {
		watch += len(m)
	}
	for _, m := range s.trades {
		trades += len(m)
	}
	return map[string]int64{
		"orders": int64(len(s.exec.orders)), "agent_positions": int64(positions), "trades": int64(trades),
		"decisions": int64(len(s.log.decisions)), "llm_calls": int64(len(s.log.calls)),
		"equity_snapshots": int64(len(s.equity)), "events": int64(len(s.events)), "reviews": int64(len(s.reviews)),
		"settings": int64(len(s.settings)), "real_gates": int64(len(s.exec.gates)), "risk_state": int64(len(s.exec.risk)),
		"watchlist": int64(watch), "users": int64(len(s.users)), "accounts": int64(len(s.accounts)),
		"audit_log": int64(len(s.audit)),
	}, nil
}

// Reset wipes the in-memory trading data as sqlstore does. ResetErr fails it with nothing
// changed (a rolled-back transaction).
func (r dataResetRepo) Reset(_ context.Context, spec ports.ResetSpec) (_ map[string]int64, err error) {
	s := r.s
	defer func() { // after the unlock: the hook may use the store
		if err == nil && s.OnReset != nil {
			s.OnReset()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ResetErr != nil {
		return nil, s.ResetErr
	}
	if spec.At.IsZero() {
		return nil, domain.Invalid("at", "required")
	}
	for _, id := range spec.AccountIDs {
		if _, ok := s.accounts[id]; !ok {
			return nil, fmt.Errorf("fakes.Reset: account %q: %w", id, domain.ErrNotFound)
		}
	}
	s.exec.init()
	s.log.init()
	out := map[string]int64{}
	positions, trades := 0, 0
	for _, m := range s.exec.positions {
		positions += len(m)
	}
	for _, m := range s.trades {
		trades += len(m)
	}
	out["orders"], out["agent_positions"], out["trades"] = int64(len(s.exec.orders)), int64(positions), int64(trades)
	out["decisions"], out["llm_calls"] = int64(len(s.log.decisions)), int64(len(s.log.calls))
	out["equity_snapshots"], out["events"], out["reviews"] = int64(len(s.equity)), int64(len(s.events)), int64(len(s.reviews))
	s.exec.orders = map[string]domain.Order{}
	s.exec.positions = map[string]map[int64]domain.AgentPosition{}
	s.trades = nil
	s.log.decisions, s.log.calls = map[string]domain.Decision{}, map[string]domain.LLMCall{}
	s.equity, s.events, s.reviews = nil, nil, map[string]domain.Review{}
	prefixes := resetDerived
	if spec.WipeWatchlists {
		w := 0
		for _, m := range s.watch {
			w += len(m)
		}
		out["watchlist"] = int64(w)
		s.watch = map[string]map[domain.InstrumentID]domain.WatchItem{}
		prefixes = append(append([]string(nil), resetDerived...), resetWatchDerived...)
	}
	maps.DeleteFunc(s.settings, func(k, _ string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				out["settings"]++
				return true
			}
		}
		return false
	})
	at := spec.At.UTC()
	for id, g := range s.exec.gates {
		if g.State == domain.GateArmed {
			g = g.Disarm("", domain.DisarmDataReset, at)
			g.Version++
			s.exec.gates[id] = g
			out["real_gates_locked"]++
		}
	}
	stamp := at.Format(time.RFC3339Nano)
	s.settings[domain.SettingDataResetAt] = stamp
	for _, id := range spec.AccountIDs {
		s.settings[domain.HistorySinceKey(id)] = stamp
	}
	s.ResetCalls++
	return out, nil
}
