package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// RealGates returns the real-mode gates (migration 00012, PLAN 7.12).
func (s *Store) RealGates() ports.RealGates { return realGates{s.db} }

type realGates struct{ db *sql.DB }

// evidenceJSON is how a gate's evidence is stored (the qualifying risk days).
type evidenceJSON struct {
	Days []string `json:"days"`
}

const realGateCols = "account_id, state, demo_account_id, armed_at, armed_by, expires_at, evidence, disarmed_at, disarmed_by, disarm_reason, version, updated_at"

func (r realGates) Get(ctx context.Context, accountID string) (domain.RealGate, error) {
	var g domain.RealGate
	var state, evidence string
	var armedAt, expiresAt, disarmedAt, updated int64
	err := r.db.QueryRowContext(ctx, "SELECT "+realGateCols+" FROM real_gates WHERE account_id = ?", accountID).Scan(
		&g.AccountID, &state, &g.DemoAccountID, &armedAt, &g.ArmedBy, &expiresAt, &evidence, &disarmedAt, &g.DisarmedBy,
		&g.DisarmReason, &g.Version, &updated)
	if err != nil {
		return domain.RealGate{}, fmt.Errorf("realGates.Get: %w", notFound(err))
	}
	var ev evidenceJSON
	if err := json.Unmarshal([]byte(evidence), &ev); err != nil {
		return domain.RealGate{}, fmt.Errorf("realGates.Get: the stored evidence is unreadable: %w", err)
	}
	g.State = domain.GateState(state)
	g.ArmedAt, g.ExpiresAt, g.DisarmedAt, g.UpdatedAt = fromUSZ(armedAt), fromUSZ(expiresAt), fromUSZ(disarmedAt), fromUS(updated)
	for _, d := range ev.Days {
		g.Evidence.Days = append(g.Evidence.Days, domain.RiskDate(d))
	}
	g.Evidence.Count = len(g.Evidence.Days)
	if g.Evidence.Count > 0 {
		g.Evidence.First, g.Evidence.Last = g.Evidence.Days[0], g.Evidence.Days[g.Evidence.Count-1]
	}
	return g, nil
}

// Put inserts the first version or updates the stored one only if its version is still
// g.Version (compare-and-set): a stale writer gets ErrConflict and changes nothing.
func (r realGates) Put(ctx context.Context, g domain.RealGate) (domain.RealGate, error) {
	if err := g.Validate(); err != nil {
		return domain.RealGate{}, fmt.Errorf("realGates.Put: %w", err)
	}
	ev := evidenceJSON{Days: make([]string, 0, len(g.Evidence.Days))}
	for _, d := range g.Evidence.Days {
		ev.Days = append(ev.Days, string(d))
	}
	evidence, err := json.Marshal(ev)
	if err != nil {
		return domain.RealGate{}, fmt.Errorf("realGates.Put: %w", err)
	}
	vals := []any{
		string(g.State), g.DemoAccountID, usZ(g.ArmedAt), g.ArmedBy, usZ(g.ExpiresAt), string(evidence),
		usZ(g.DisarmedAt), g.DisarmedBy, g.DisarmReason, us(g.UpdatedAt),
	}
	if g.Version == 0 {
		ok, err := accountExists(ctx, r.db, g.AccountID)
		if err != nil {
			return domain.RealGate{}, fmt.Errorf("realGates.Put: %w", err)
		}
		if !ok {
			return domain.RealGate{}, fmt.Errorf("realGates.Put: account %s: %w", g.AccountID, domain.ErrNotFound)
		}
		_, err = r.db.ExecContext(ctx, "INSERT INTO real_gates (state, demo_account_id, armed_at, armed_by, expires_at, evidence, "+
			"disarmed_at, disarmed_by, disarm_reason, updated_at, account_id, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)",
			append(vals, g.AccountID)...)
		if isDuplicate(err) {
			return domain.RealGate{}, fmt.Errorf("realGates.Put: stored by someone else: %w", domain.ErrConflict)
		}
		if err != nil {
			return domain.RealGate{}, fmt.Errorf("realGates.Put: %w", err)
		}
	} else {
		res, err := r.db.ExecContext(ctx, "UPDATE real_gates SET state = ?, demo_account_id = ?, armed_at = ?, armed_by = ?, expires_at = ?, "+
			"evidence = ?, disarmed_at = ?, disarmed_by = ?, disarm_reason = ?, updated_at = ?, version = version + 1 "+
			"WHERE account_id = ? AND version = ?", append(vals, g.AccountID, g.Version)...)
		if err := mustAffect(res, err); errors.Is(err, domain.ErrNotFound) {
			return domain.RealGate{}, fmt.Errorf("realGates.Put: version %d is stale: %w", g.Version, domain.ErrConflict)
		} else if err != nil {
			return domain.RealGate{}, fmt.Errorf("realGates.Put: %w", err)
		}
	}
	g.Version++
	return g, nil
}
