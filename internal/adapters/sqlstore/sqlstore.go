// Package sqlstore implements the repository ports on SQLite (standalone; pure-Go
// modernc.org/sqlite) and MariaDB (containers; go-sql-driver/mysql) with one set of
// portable SQL: `?` placeholders, BIGINT Unix-microsecond times, no RETURNING, no upsert
// syntax (upserts are update-then-insert in a transaction).
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"
	"modernc.org/sqlite"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/migrations"
)

// Drivers.
const (
	DriverSQLite  = "sqlite"
	DriverMariaDB = "mariadb"
)

// Store is a ports.Store on a *sql.DB.
type Store struct {
	db     *sql.DB
	driver string
}

var _ ports.Store = (*Store)(nil)

// MariaDBConfig holds the connection settings for MariaDB.
type MariaDBConfig struct {
	Host     string // host or host:port
	Name     string
	User     string
	Password string
}

// SQLiteDSN builds the DSN for a database file: WAL, foreign keys, a busy timeout and
// IMMEDIATE transactions (so writers queue instead of failing).
func SQLiteDSN(path string) string {
	return "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_txlock=immediate"
}

// OpenSQLite opens (creating if needed) a SQLite database.
func OpenSQLite(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", SQLiteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("sqlstore: open sqlite: %w", err)
	}
	db.SetMaxOpenConns(4)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlstore: ping sqlite: %w", err)
	}
	return &Store{db: db, driver: DriverSQLite}, nil
}

// OpenMariaDB connects to MariaDB, retrying until ctx ends (the database container may still
// be starting).
func OpenMariaDB(ctx context.Context, c MariaDBConfig) (*Store, error) {
	mc := mysql.NewConfig()
	mc.Net = "tcp"
	mc.Addr = c.Host
	if !strings.Contains(c.Host, ":") {
		mc.Addr = c.Host + ":3306"
	}
	mc.DBName, mc.User, mc.Passwd = c.Name, c.User, c.Password
	mc.Collation = "utf8mb4_unicode_ci"
	mc.Timeout, mc.ReadTimeout, mc.WriteTimeout = 10*time.Second, 30*time.Second, 30*time.Second
	mc.MultiStatements = true // goose runs a migration file as one statement batch
	mc.ClientFoundRows = true // affected rows = matched rows, as in SQLite
	connector, err := mysql.NewConnector(mc)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: mariadb config: %w", err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(16)
	db.SetConnMaxLifetime(30 * time.Minute)
	for {
		err = db.PingContext(ctx)
		if err == nil {
			return &Store{db: db, driver: DriverMariaDB}, nil
		}
		// A server answer (bad password, unknown database…) will not fix itself: stop.
		if _, ok := errors.AsType[*mysql.MySQLError](err); ok {
			_ = db.Close()
			return nil, fmt.Errorf("sqlstore: mariadb: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = db.Close()
			return nil, fmt.Errorf("sqlstore: mariadb not reachable: %w", err)
		case <-time.After(2 * time.Second):
		}
	}
}

// DB exposes the handle (tests, backups).
func (s *Store) DB() *sql.DB { return s.db }

// Driver returns "sqlite" or "mariadb".
func (s *Store) Driver() string { return s.driver }

// Ping checks the connection.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close closes the handle.
func (s *Store) Close() error { return s.db.Close() }

// Migrator returns a goose provider for this store's dialect.
func (s *Store) Migrator() (*goose.Provider, error) {
	dialect, dir := goose.DialectSQLite3, "sqlite"
	if s.driver == DriverMariaDB {
		dialect, dir = goose.DialectMySQL, "mariadb"
	}
	sub, err := fs.Sub(migrations.FS, dir)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: migrations: %w", err)
	}
	p, err := goose.NewProvider(dialect, s.db, sub)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: migrations: %w", err)
	}
	return p, nil
}

// Migrate applies every pending migration.
func (s *Store) Migrate(ctx context.Context) error {
	p, err := s.Migrator()
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("sqlstore: migrate up: %w", err)
	}
	return nil
}

// isDuplicate reports a unique/primary-key violation in either driver.
func isDuplicate(err error) bool {
	if me, ok := errors.AsType[*mysql.MySQLError](err); ok {
		return me.Number == 1062
	}
	if se, ok := errors.AsType[*sqlite.Error](err); ok {
		// SQLITE_CONSTRAINT_UNIQUE (2067), SQLITE_CONSTRAINT_PRIMARYKEY (1555)
		return se.Code() == 2067 || se.Code() == 1555
	}
	return false
}

func us(t time.Time) int64 { return t.UTC().UnixMicro() }

func fromUS(v int64) time.Time { return time.UnixMicro(v).UTC() }

func nullUS(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: us(*t), Valid: true}
}

func fromNullUS(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromUS(v.Int64)
	return &t
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

func mustAffect(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Instruments returns the instrument cache.
func (s *Store) Instruments() ports.Instruments { return instruments{s.db} }

// Themes returns the themes repository.
func (s *Store) Themes() ports.Themes { return themes{s.db} }

// Reviews returns the scanner's reviews.
func (s *Store) Reviews() ports.Reviews { return reviews{s.db} }

// Outbox returns the mail outbox.
func (s *Store) Outbox() ports.Outbox { return outbox{s.db} }

// PasswordResets returns the password-reset links.
func (s *Store) PasswordResets() ports.PasswordResets { return passwordResets{s.db} }

// Accounts returns the eToro accounts repository.
func (s *Store) Accounts() ports.Accounts { return accounts{s.db} }

// Events returns the event feed.
func (s *Store) Events() ports.Events { return events{s.db} }

// Watchlist returns the watchlist repository.
func (s *Store) Watchlist() ports.Watchlist { return watchlist{s.db} }

// Users returns the user repository.
func (s *Store) Users() ports.Users { return users{s.db} }

// Sessions returns the session repository.
func (s *Store) Sessions() ports.Sessions { return sessions{s.db} }

// APIKeys returns the API key repository.
func (s *Store) APIKeys() ports.APIKeys { return apiKeys{s.db} }

// Secrets returns the encrypted-secret repository.
func (s *Store) Secrets() ports.Secrets { return secrets{s.db} }

// Audit returns the audit repository.
func (s *Store) Audit() ports.Audit { return audit{s.db} }

// Settings returns the settings repository.
func (s *Store) Settings() ports.Settings { return settings{s.db} }
