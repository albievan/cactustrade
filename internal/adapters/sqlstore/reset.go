package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Reset all trading data (PLAN R49): one transaction of literal DELETEs (no dynamic SQL, as
// the dump), the same in both dialects. Every table of the schema is either wiped or kept;
// TestResetClassifiesEveryTable fails for a table in neither list, so a migration that adds a
// table must classify it here (and add it to dumpSelects).

// resetTable is one table the reset knows: its name, its row count and, for a wiped table
// (or the watchlist, wiped on request), the statement that empties it.
type resetTable struct {
	name, count, wipe string
}

// resetWiped are the tables a reset empties: the trading data of every account (OD39).
var resetWiped = []resetTable{
	{"agent_positions", "SELECT COUNT(*) FROM agent_positions", "DELETE FROM agent_positions"},
	{"decisions", "SELECT COUNT(*) FROM decisions", "DELETE FROM decisions"},
	{"equity_snapshots", "SELECT COUNT(*) FROM equity_snapshots", "DELETE FROM equity_snapshots"},
	{"events", "SELECT COUNT(*) FROM events", "DELETE FROM events"},
	{"llm_calls", "SELECT COUNT(*) FROM llm_calls", "DELETE FROM llm_calls"},
	{"orders", "SELECT COUNT(*) FROM orders", "DELETE FROM orders"},
	{"reviews", "SELECT COUNT(*) FROM reviews", "DELETE FROM reviews"},
	{"trades", "SELECT COUNT(*) FROM trades", "DELETE FROM trades"},
}

// resetKept are the tables a reset keeps. The watchlist is emptied only when asked
// (ResetSpec.WipeWatchlists); settings lose only the derived keys of resetSettings; armed
// real-money gates are locked, their rows kept; risk_state (kill switch, high-water mark) is
// never touched: a reset can never clear a kill or a drawdown mark (CLAUDE.md §2.6).
var resetKept = []resetTable{
	{"account_shares", "SELECT COUNT(*) FROM account_shares", ""},
	{"accounts", "SELECT COUNT(*) FROM accounts", ""},
	{"agent_leases", "SELECT COUNT(*) FROM agent_leases", ""},
	{"api_keys", "SELECT COUNT(*) FROM api_keys", ""},
	{"audit_log", "SELECT COUNT(*) FROM audit_log", ""},
	{"goose_db_version", "SELECT COUNT(*) FROM goose_db_version", ""},
	{"instrument_blocks", "SELECT COUNT(*) FROM instrument_blocks", ""},
	{"instrument_matches", "SELECT COUNT(*) FROM instrument_matches", ""}, // owner-confirmed Trading 212 matches
	{"instruments", "SELECT COUNT(*) FROM instruments", ""},
	{"outbox", "SELECT COUNT(*) FROM outbox", ""},
	{"outbox_attachments", "SELECT COUNT(*) FROM outbox_attachments", ""},
	{"password_resets", "SELECT COUNT(*) FROM password_resets", ""},
	{"real_gates", "SELECT COUNT(*) FROM real_gates", ""},
	{"risk_state", "SELECT COUNT(*) FROM risk_state", ""},
	{"secrets", "SELECT COUNT(*) FROM secrets", ""},
	{"sessions", "SELECT COUNT(*) FROM sessions", ""},
	{"settings", "SELECT COUNT(*) FROM settings", ""},
	{"statement_schedules", "SELECT COUNT(*) FROM statement_schedules", ""},
	{"t212_instruments", "SELECT COUNT(*) FROM t212_instruments", ""}, // Trading 212's catalogue
	{"theme_instruments", "SELECT COUNT(*) FROM theme_instruments", ""},
	{"themes", "SELECT COUNT(*) FROM themes", ""},
	{"users", "SELECT COUNT(*) FROM users", ""},
	{"watchlist", "SELECT COUNT(*) FROM watchlist", "DELETE FROM watchlist"},
}

// resetSettings are the LIKE patterns (escape '!') of the settings derived from the wiped
// data: the adapted aggressiveness, web research's state and the events seen. The
// watchlist's signals go with the watchlist (resetWatchSettings).
var resetSettings = []string{
	"agent.aggressiveness!_adapt.%",
	"research.state.%",
	"events.seen.%",
}

// resetWatchSettings are the settings wiped with the watchlist.
var resetWatchSettings = []string{"watchlist.signals.%"}

// Reset counters that are not tables.
const (
	resetGatesLocked = "real_gates_locked"
	resetSettingsKey = "settings"
)

// DataReset returns the data reset (PLAN R49).
func (s *Store) DataReset() ports.DataReset { return dataReset{s.db} }

type dataReset struct{ db *sql.DB }

// Counts returns the rows of every table in resetWiped and resetKept.
func (r dataReset) Counts(ctx context.Context) (map[string]int64, error) {
	out := make(map[string]int64, len(resetWiped)+len(resetKept))
	for _, t := range append(append([]resetTable(nil), resetWiped...), resetKept...) {
		var n int64
		if err := r.db.QueryRowContext(ctx, t.count).Scan(&n); err != nil {
			return nil, fmt.Errorf("dataReset.Counts: %s: %w", t.name, err)
		}
		out[t.name] = n
	}
	return out, nil
}

// Reset wipes as spec says in one transaction; any failure rolls everything back.
func (r dataReset) Reset(ctx context.Context, spec ports.ResetSpec) (out map[string]int64, err error) {
	if spec.At.IsZero() {
		return nil, fmt.Errorf("dataReset.Reset: %w", domain.Invalid("at", "required"))
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("dataReset.Reset: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	out = map[string]int64{}
	wipe := func(name, stmt string, args ...any) error {
		res, err := tx.ExecContext(ctx, stmt, args...)
		if err != nil {
			return fmt.Errorf("dataReset.Reset: %s: %w", name, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("dataReset.Reset: %s: %w", name, err)
		}
		out[name] += n
		return nil
	}
	for _, t := range resetWiped {
		if err := wipe(t.name, t.wipe); err != nil {
			return nil, err
		}
	}
	patterns := resetSettings
	if spec.WipeWatchlists {
		for _, t := range resetKept {
			if t.wipe == "" {
				continue
			}
			if err := wipe(t.name, t.wipe); err != nil {
				return nil, err
			}
		}
		patterns = append(append([]string(nil), resetSettings...), resetWatchSettings...)
	}
	for _, p := range patterns {
		if err := wipe(resetSettingsKey, "DELETE FROM settings WHERE k LIKE ? ESCAPE '!'", p); err != nil {
			return nil, err
		}
	}
	at := spec.At.UTC()
	if err := wipe(resetGatesLocked, "UPDATE real_gates SET state = ?, disarmed_at = ?, disarmed_by = '', disarm_reason = ?, "+
		"updated_at = ?, version = version + 1 WHERE state = ?",
		string(domain.GateLocked), us(at), domain.DisarmDataReset, us(at), string(domain.GateArmed)); err != nil {
		return nil, err
	}
	stamp := at.Format(time.RFC3339Nano)
	put := func(k string) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE k = ?", k); err != nil {
			return fmt.Errorf("dataReset.Reset: setting %s: %w", k, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO settings (k, v, updated_at) VALUES (?, ?, ?)", k, stamp, us(at)); err != nil {
			return fmt.Errorf("dataReset.Reset: setting %s: %w", k, err)
		}
		return nil
	}
	if err := put(domain.SettingDataResetAt); err != nil {
		return nil, err
	}
	// Each account's trade-history floor (OD38). An unknown account fails the whole reset
	// (nothing is wiped): the caller passes the accounts it read.
	for _, id := range spec.AccountIDs {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts WHERE id = ?", id).Scan(&n); err != nil {
			return nil, fmt.Errorf("dataReset.Reset: %w", err)
		}
		if n == 0 {
			return nil, fmt.Errorf("dataReset.Reset: account %q: %w", id, domain.ErrNotFound)
		}
		if err := put(domain.HistorySinceKey(id)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("dataReset.Reset: commit: %w", err)
	}
	return out, nil
}
