package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNotSQLite is returned by the SQLite-only backup operations on a MariaDB store (MariaDB is
// backed up with mariadb-dump by deploy.sh, PLAN 7.6b).
var ErrNotSQLite = errors.New("sqlstore: only a SQLite database is backed up by cactustrade itself")

// BackupTo writes a consistent copy of the SQLite database into the new file path with
// VACUUM INTO: one read transaction, so writers carry on and the copy is a single point in
// time. The file must not exist yet.
func (s *Store) BackupTo(ctx context.Context, path string) error {
	if s.driver != DriverSQLite {
		return ErrNotSQLite
	}
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return fmt.Errorf("sqlstore.BackupTo: %w", err)
	}
	return nil
}

// Checkpoint writes the SQLite write-ahead log into the database file and empties it, so the
// file alone holds every committed change (before the file is moved by a restore).
func (s *Store) Checkpoint(ctx context.Context) error {
	if s.driver != DriverSQLite {
		return ErrNotSQLite
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("sqlstore.Checkpoint: %w", err)
	}
	return nil
}

// IntegrityCheck runs SQLite's integrity check and fails unless it answers "ok".
func (s *Store) IntegrityCheck(ctx context.Context) error {
	if s.driver != DriverSQLite {
		return ErrNotSQLite
	}
	var res string
	if err := s.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&res); err != nil {
		return fmt.Errorf("sqlstore.IntegrityCheck: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("sqlstore.IntegrityCheck: %s", res)
	}
	return nil
}

// SchemaVersions returns the database's migration version and the newest one this build has.
func (s *Store) SchemaVersions(ctx context.Context) (current, newest int64, err error) {
	p, err := s.Migrator()
	if err != nil {
		return 0, 0, err
	}
	current, newest, err = p.GetVersions(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("sqlstore.SchemaVersions: %w", err)
	}
	return current, newest, nil
}

// LiveLeases counts the agent leases (D22) that have not expired at now: a process that holds
// one may send orders, so nothing may replace the database under it.
func (s *Store) LiveLeases(ctx context.Context, now time.Time) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM agent_leases WHERE expires_at > ?", us(now)).Scan(&n); err != nil {
		return 0, fmt.Errorf("sqlstore.LiveLeases: %w", err)
	}
	return n, nil
}

// PrepareRestored readies a restored database for its first start: the leases of the
// processes that ran when the backup was taken are dropped (they are gone), every session is
// ended (a sign-out made after the backup must not come back), and the setting restore.at
// records when the restore happened (PLAN 7.6a: a real-mode gate armed before it is disarmed,
// 7.12/7.13).
func (s *Store) PrepareRestored(ctx context.Context, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore.PrepareRestored: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{"DELETE FROM agent_leases", "DELETE FROM sessions"} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("sqlstore.PrepareRestored: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM settings WHERE k = ?", RestoreAtSetting); err != nil {
		return fmt.Errorf("sqlstore.PrepareRestored: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO settings (k, v, updated_at) VALUES (?, ?, ?)",
		RestoreAtSetting, at.UTC().Format(time.RFC3339Nano), us(at)); err != nil {
		return fmt.Errorf("sqlstore.PrepareRestored: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlstore.PrepareRestored: %w", err)
	}
	return nil
}

// RestoreAtSetting is the settings key holding the time of the latest restore (RFC 3339, UTC).
const RestoreAtSetting = "restore.at"
