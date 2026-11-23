package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// ---- outbox ---------------------------------------------------------------------------

type outbox struct{ db *sql.DB }

const outboxCols = "id, to_addr, subject, body, created_at, next_at, attempts, sent_at, last_error"

func (r outbox) list(ctx context.Context, query string, args ...any) ([]domain.OutboxMail, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.OutboxMail
	for rows.Next() {
		var m domain.OutboxMail
		var c, n, s int64
		if err := rows.Scan(&m.ID, &m.To, &m.Subject, &m.Body, &c, &n, &m.Attempts, &s, &m.LastError); err != nil {
			return nil, err
		}
		m.CreatedAt, m.NextAt = fromUS(c), fromUS(n)
		if s > 0 {
			m.SentAt = fromUS(s)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r outbox) Enqueue(ctx context.Context, m domain.OutboxMail) error {
	_, err := r.db.ExecContext(ctx, "INSERT INTO outbox ("+outboxCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		m.ID, m.To, m.Subject, m.Body, us(m.CreatedAt), us(m.NextAt), m.Attempts, int64(0), m.LastError)
	if isDuplicate(err) {
		return domain.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("outbox.Enqueue: %w", err)
	}
	return nil
}

func (r outbox) Due(ctx context.Context, now time.Time, limit int) ([]domain.OutboxMail, error) {
	out, err := r.list(ctx, "SELECT "+outboxCols+" FROM outbox WHERE sent_at = 0 AND next_at <= ? AND attempts < ? ORDER BY next_at, id LIMIT ?",
		us(now), domain.MaxMailAttempts, limit)
	if err != nil {
		return nil, fmt.Errorf("outbox.Due: %w", err)
	}
	return out, nil
}

func (r outbox) MarkSent(ctx context.Context, id string, at time.Time) error {
	if err := mustAffect(r.db.ExecContext(ctx, "UPDATE outbox SET sent_at = ?, attempts = attempts + 1, last_error = '' WHERE id = ?", us(at), id)); err != nil {
		return fmt.Errorf("outbox.MarkSent: %w", err)
	}
	return nil
}

func (r outbox) MarkFailed(ctx context.Context, id string, attempts int, next time.Time, lastErr string) error {
	if len(lastErr) > 500 {
		lastErr = lastErr[:500]
	}
	if err := mustAffect(r.db.ExecContext(ctx, "UPDATE outbox SET attempts = ?, next_at = ?, last_error = ? WHERE id = ? AND sent_at = 0",
		attempts, us(next), lastErr, id)); err != nil {
		return fmt.Errorf("outbox.MarkFailed: %w", err)
	}
	return nil
}

func (r outbox) List(ctx context.Context, limit int) ([]domain.OutboxMail, error) {
	out, err := r.list(ctx, "SELECT "+outboxCols+" FROM outbox ORDER BY created_at DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, fmt.Errorf("outbox.List: %w", err)
	}
	return out, nil
}

func (r outbox) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, "DELETE FROM outbox WHERE created_at < ?", us(before))
	if err != nil {
		return 0, fmt.Errorf("outbox.DeleteBefore: %w", err)
	}
	return res.RowsAffected()
}

// ---- password resets ------------------------------------------------------------------

type passwordResets struct{ db *sql.DB }

func (r passwordResets) Create(ctx context.Context, p domain.PasswordReset) error {
	_, err := r.db.ExecContext(ctx, "INSERT INTO password_resets (token_hash, user_id, created_at, expires_at, used_at) VALUES (?, ?, ?, ?, 0)",
		p.TokenHash, p.UserID, us(p.CreatedAt), us(p.ExpiresAt))
	if isDuplicate(err) {
		return domain.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("passwordResets.Create: %w", err)
	}
	return nil
}

func (r passwordResets) Get(ctx context.Context, tokenHash string) (domain.PasswordReset, error) {
	var p domain.PasswordReset
	var c, e, u int64
	err := r.db.QueryRowContext(ctx, "SELECT token_hash, user_id, created_at, expires_at, used_at FROM password_resets WHERE token_hash = ?", tokenHash).
		Scan(&p.TokenHash, &p.UserID, &c, &e, &u)
	if err != nil {
		return domain.PasswordReset{}, notFound(err)
	}
	p.CreatedAt, p.ExpiresAt = fromUS(c), fromUS(e)
	if u > 0 {
		p.UsedAt = fromUS(u)
	}
	return p, nil
}

func (r passwordResets) Use(ctx context.Context, tokenHash string, at time.Time) error {
	return mustAffect(r.db.ExecContext(ctx, "UPDATE password_resets SET used_at = ? WHERE token_hash = ? AND used_at = 0", us(at), tokenHash))
}

func (r passwordResets) CountSince(ctx context.Context, userID string, since time.Time) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM password_resets WHERE user_id = ? AND created_at >= ?", userID, us(since)).Scan(&n); err != nil {
		return 0, fmt.Errorf("passwordResets.CountSince: %w", err)
	}
	return n, nil
}

func (r passwordResets) DeleteForUser(ctx context.Context, userID string) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM password_resets WHERE user_id = ?", userID); err != nil {
		return fmt.Errorf("passwordResets.DeleteForUser: %w", err)
	}
	return nil
}

func (r passwordResets) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, "DELETE FROM password_resets WHERE expires_at < ?", us(before))
	if err != nil {
		return 0, fmt.Errorf("passwordResets.DeleteBefore: %w", err)
	}
	return res.RowsAffected()
}
