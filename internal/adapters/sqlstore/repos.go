package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// ---- users -------------------------------------------------------------------------

type users struct{ db *sql.DB }

const userCols = "id, username, role, password_hash, disabled, created_at, updated_at"

func scanUser(row interface{ Scan(...any) error }) (domain.User, error) {
	var u domain.User
	var role string
	var disabled bool
	var c, up int64
	if err := row.Scan(&u.ID, &u.Username, &role, &u.PasswordHash, &disabled, &c, &up); err != nil {
		return domain.User{}, notFound(err)
	}
	u.Role, u.Disabled, u.CreatedAt, u.UpdatedAt = domain.Role(role), disabled, fromUS(c), fromUS(up)
	return u, nil
}

func insertUser(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, u domain.User,
) error {
	_, err := q.ExecContext(ctx, "INSERT INTO users ("+userCols+") VALUES (?, ?, ?, ?, ?, ?, ?)",
		u.ID, u.Username, string(u.Role), u.PasswordHash, u.Disabled, us(u.CreatedAt), us(u.UpdatedAt))
	if isDuplicate(err) {
		return domain.ErrConflict
	}
	return err
}

// CreateFirst claims the "setup.completed" settings row (its primary key makes the claim
// atomic in both databases), checks there is no user, and inserts the first one.
func (r users) CreateFirst(ctx context.Context, u domain.User) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("users.CreateFirst: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	_, err = tx.ExecContext(ctx, "INSERT INTO settings (k, v, updated_at) VALUES ('setup.completed', ?, ?)", u.ID, us(u.CreatedAt))
	if isDuplicate(err) {
		return domain.ErrSetupDone
	}
	if err != nil {
		return fmt.Errorf("users.CreateFirst: %w", err)
	}
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		return fmt.Errorf("users.CreateFirst: %w", err)
	}
	if n > 0 {
		err = domain.ErrSetupDone
		return err
	}
	if err = insertUser(ctx, tx, u); err != nil {
		return fmt.Errorf("users.CreateFirst: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("users.CreateFirst: %w", err)
	}
	return nil
}

func (r users) Create(ctx context.Context, u domain.User) error {
	if err := insertUser(ctx, r.db, u); err != nil {
		return fmt.Errorf("users.Create: %w", err)
	}
	return nil
}

func (r users) Get(ctx context.Context, id string) (domain.User, error) {
	return scanUser(r.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
}

func (r users) GetByUsername(ctx context.Context, name string) (domain.User, error) {
	return scanUser(r.db.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE username = ?", name))
}

func (r users) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+userCols+" FROM users ORDER BY username")
	if err != nil {
		return nil, fmt.Errorf("users.List: %w", err)
	}
	defer rows.Close()
	var out []domain.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("users.List: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r users) Update(ctx context.Context, u domain.User) error {
	err := mustAffect(r.db.ExecContext(ctx,
		"UPDATE users SET username = ?, role = ?, password_hash = ?, disabled = ?, updated_at = ? WHERE id = ?",
		u.Username, string(u.Role), u.PasswordHash, u.Disabled, us(u.UpdatedAt), u.ID))
	if isDuplicate(err) {
		return domain.ErrConflict
	}
	return err
}

func (r users) Delete(ctx context.Context, id string) error {
	return mustAffect(r.db.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id))
}

func (r users) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

func (r users) CountActiveAdmins(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0").Scan(&n)
	return n, err
}

// ---- sessions ----------------------------------------------------------------------

type sessions struct{ db *sql.DB }

func (r sessions) Create(ctx context.Context, s domain.Session) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, last_seen, expires_at) VALUES (?, ?, ?, ?, ?, ?)",
		s.TokenHash, s.UserID, s.CSRFToken, us(s.CreatedAt), us(s.LastSeen), us(s.ExpiresAt))
	if err != nil {
		return fmt.Errorf("sessions.Create: %w", err)
	}
	return nil
}

func (r sessions) Get(ctx context.Context, h string) (domain.Session, error) {
	var s domain.Session
	var c, l, e int64
	err := r.db.QueryRowContext(ctx,
		"SELECT token_hash, user_id, csrf_token, created_at, last_seen, expires_at FROM sessions WHERE token_hash = ?", h).
		Scan(&s.TokenHash, &s.UserID, &s.CSRFToken, &c, &l, &e)
	if err != nil {
		return domain.Session{}, notFound(err)
	}
	s.CreatedAt, s.LastSeen, s.ExpiresAt = fromUS(c), fromUS(l), fromUS(e)
	return s, nil
}

func (r sessions) Touch(ctx context.Context, h string, at time.Time) error {
	return mustAffect(r.db.ExecContext(ctx, "UPDATE sessions SET last_seen = ? WHERE token_hash = ?", us(at), h))
}

func (r sessions) Delete(ctx context.Context, h string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE token_hash = ?", h)
	return err
}

func (r sessions) DeleteForUser(ctx context.Context, uid string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ?", uid)
	return err
}

func (r sessions) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", us(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- API keys ----------------------------------------------------------------------

type apiKeys struct{ db *sql.DB }

const keyCols = "id, owner_id, name, role, secret_hash, created_at, last_used_at, expires_at"

func scanKey(row interface{ Scan(...any) error }) (domain.APIKey, error) {
	var k domain.APIKey
	var role string
	var c int64
	var last, exp sql.NullInt64
	if err := row.Scan(&k.ID, &k.OwnerID, &k.Name, &role, &k.SecretHash, &c, &last, &exp); err != nil {
		return domain.APIKey{}, notFound(err)
	}
	k.Role, k.CreatedAt, k.LastUsedAt, k.ExpiresAt = domain.Role(role), fromUS(c), fromNullUS(last), fromNullUS(exp)
	return k, nil
}

func (r apiKeys) Create(ctx context.Context, k domain.APIKey) error {
	_, err := r.db.ExecContext(ctx, "INSERT INTO api_keys ("+keyCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		k.ID, k.OwnerID, k.Name, string(k.Role), k.SecretHash, us(k.CreatedAt), nullUS(k.LastUsedAt), nullUS(k.ExpiresAt))
	if isDuplicate(err) {
		return domain.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("apiKeys.Create: %w", err)
	}
	return nil
}

func (r apiKeys) Get(ctx context.Context, id string) (domain.APIKey, error) {
	return scanKey(r.db.QueryRowContext(ctx, "SELECT "+keyCols+" FROM api_keys WHERE id = ?", id))
}

func (r apiKeys) List(ctx context.Context, owner string) ([]domain.APIKey, error) {
	q, args := "SELECT "+keyCols+" FROM api_keys ORDER BY created_at, id", []any{}
	if owner != "" {
		q, args = "SELECT "+keyCols+" FROM api_keys WHERE owner_id = ? ORDER BY created_at, id", []any{owner}
	}
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("apiKeys.List: %w", err)
	}
	defer rows.Close()
	var out []domain.APIKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, fmt.Errorf("apiKeys.List: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (r apiKeys) Touch(ctx context.Context, id string, at time.Time) error {
	return mustAffect(r.db.ExecContext(ctx, "UPDATE api_keys SET last_used_at = ? WHERE id = ?", us(at), id))
}

func (r apiKeys) Delete(ctx context.Context, id string) error {
	return mustAffect(r.db.ExecContext(ctx, "DELETE FROM api_keys WHERE id = ?", id))
}

func (r apiKeys) DeleteForOwner(ctx context.Context, owner string) error {
	_, err := r.db.ExecContext(ctx, "DELETE FROM api_keys WHERE owner_id = ?", owner)
	return err
}

// ---- secrets -----------------------------------------------------------------------

type secrets struct{ db *sql.DB }

func (r secrets) Put(ctx context.Context, s ports.SecretRecord) error {
	return upsert(ctx, r.db,
		"UPDATE secrets SET ciphertext = ?, updated_at = ? WHERE name = ?", []any{s.Ciphertext, us(s.UpdatedAt), s.Name},
		"INSERT INTO secrets (name, ciphertext, updated_at) VALUES (?, ?, ?)", []any{s.Name, s.Ciphertext, us(s.UpdatedAt)})
}

func (r secrets) Get(ctx context.Context, name string) (ports.SecretRecord, error) {
	var s ports.SecretRecord
	var at int64
	err := r.db.QueryRowContext(ctx, "SELECT name, ciphertext, updated_at FROM secrets WHERE name = ?", name).Scan(&s.Name, &s.Ciphertext, &at)
	if err != nil {
		return ports.SecretRecord{}, notFound(err)
	}
	s.UpdatedAt = fromUS(at)
	return s, nil
}

func (r secrets) List(ctx context.Context) ([]ports.SecretRecord, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT name, updated_at FROM secrets ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("secrets.List: %w", err)
	}
	defer rows.Close()
	var out []ports.SecretRecord
	for rows.Next() {
		var s ports.SecretRecord
		var at int64
		if err := rows.Scan(&s.Name, &at); err != nil {
			return nil, fmt.Errorf("secrets.List: %w", err)
		}
		s.UpdatedAt = fromUS(at)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r secrets) Delete(ctx context.Context, name string) error {
	return mustAffect(r.db.ExecContext(ctx, "DELETE FROM secrets WHERE name = ?", name))
}

// ---- audit -------------------------------------------------------------------------

type audit struct{ db *sql.DB }

func (r audit) Append(ctx context.Context, e domain.AuditEntry) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO audit_log (at, actor_id, actor, action, target, details, remote_ip) VALUES (?, ?, ?, ?, ?, ?, ?)",
		us(e.At), e.ActorID, e.Actor, e.Action, e.Target, e.Details, e.RemoteIP)
	if err != nil {
		return fmt.Errorf("audit.Append: %w", err)
	}
	return nil
}

func (r audit) List(ctx context.Context, limit int) ([]domain.AuditEntry, error) {
	rows, err := r.db.QueryContext(ctx,
		"SELECT id, at, actor_id, actor, action, target, details, remote_ip FROM audit_log ORDER BY id DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("audit.List: %w", err)
	}
	defer rows.Close()
	var out []domain.AuditEntry
	for rows.Next() {
		var e domain.AuditEntry
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.ActorID, &e.Actor, &e.Action, &e.Target, &e.Details, &e.RemoteIP); err != nil {
			return nil, fmt.Errorf("audit.List: %w", err)
		}
		e.At = fromUS(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- settings ----------------------------------------------------------------------

type settings struct{ db *sql.DB }

func (r settings) Get(ctx context.Context, k string) (string, error) {
	var v string
	if err := r.db.QueryRowContext(ctx, "SELECT v FROM settings WHERE k = ?", k).Scan(&v); err != nil {
		return "", notFound(err)
	}
	return v, nil
}

func (r settings) Set(ctx context.Context, k, v string, at time.Time) error {
	return upsert(ctx, r.db,
		"UPDATE settings SET v = ?, updated_at = ? WHERE k = ?", []any{v, us(at), k},
		"INSERT INTO settings (k, v, updated_at) VALUES (?, ?, ?)", []any{k, v, us(at)})
}

// upsert runs update; when it touched no row, insert — in one transaction. (MariaDB reports
// 0 affected rows for an UPDATE that changes nothing, so a duplicate on insert means the row
// exists with the same values: success.)
func upsert(ctx context.Context, db *sql.DB, upd string, updArgs []any, ins string, insArgs []any) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	err = mustAffect(tx.ExecContext(ctx, upd, updArgs...))
	if errors.Is(err, domain.ErrNotFound) {
		_, err = tx.ExecContext(ctx, ins, insArgs...)
		if isDuplicate(err) {
			err = nil
		}
	}
	if err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	return nil
}
