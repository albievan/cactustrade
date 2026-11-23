package sqlstore

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/albievan/cactustrade/internal/domain"
)

type accounts struct{ db *sql.DB }

const accountSelect = "SELECT a.id, a.name, a.owner_id, COALESCE(u.username, ''), a.mode, a.env_keys, a.created_at, a.updated_at " +
	"FROM accounts a LEFT JOIN users u ON u.id = a.owner_id"

func scanAccount(row interface{ Scan(...any) error }, extra ...any) (domain.Account, error) {
	var a domain.Account
	var mode string
	var c, u int64
	if err := row.Scan(append([]any{&a.ID, &a.Name, &a.OwnerID, &a.OwnerName, &mode, &a.EnvKeys, &c, &u}, extra...)...); err != nil {
		return domain.Account{}, notFound(err)
	}
	a.Mode, a.CreatedAt, a.UpdatedAt = domain.TradingMode(mode), fromUS(c), fromUS(u)
	return a, nil
}

func (r accounts) Create(ctx context.Context, a domain.Account) error {
	_, err := r.db.ExecContext(ctx, "INSERT INTO accounts (id, name, owner_id, mode, env_keys, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		a.ID, a.Name, a.OwnerID, string(a.Mode), a.EnvKeys, us(a.CreatedAt), us(a.UpdatedAt))
	if isDuplicate(err) {
		return domain.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("accounts.Create: %w", err)
	}
	return nil
}

func (r accounts) Get(ctx context.Context, id string) (domain.Account, error) {
	return scanAccount(r.db.QueryRowContext(ctx, accountSelect+" WHERE a.id = ?", id))
}

func (r accounts) list(ctx context.Context, q string, args ...any) ([]domain.Account, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("accounts.List: %w", err)
	}
	defer rows.Close()
	var out []domain.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("accounts.List: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r accounts) List(ctx context.Context) ([]domain.Account, error) {
	return r.list(ctx, accountSelect+" ORDER BY a.name, a.id")
}

func (r accounts) ForUser(ctx context.Context, userID string) ([]domain.AccountView, error) {
	q := "SELECT a.id, a.name, a.owner_id, COALESCE(u.username, ''), a.mode, a.env_keys, a.created_at, a.updated_at, s.access " +
		"FROM accounts a LEFT JOIN users u ON u.id = a.owner_id " +
		"LEFT JOIN account_shares s ON s.account_id = a.id AND s.user_id = ? " +
		"WHERE a.owner_id = ? OR s.user_id IS NOT NULL ORDER BY a.name, a.id"
	rows, err := r.db.QueryContext(ctx, q, userID, userID)
	if err != nil {
		return nil, fmt.Errorf("accounts.ForUser: %w", err)
	}
	defer rows.Close()
	var out []domain.AccountView
	for rows.Next() {
		var access sql.NullString
		a, err := scanAccount(rows, &access)
		if err != nil {
			return nil, fmt.Errorf("accounts.ForUser: %w", err)
		}
		v := domain.AccountView{Account: a, Access: domain.AccountAccess(access.String)}
		if a.OwnerID == userID {
			v.Access = domain.AccessOwner
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r accounts) Update(ctx context.Context, a domain.Account) error {
	return mustAffect(r.db.ExecContext(ctx, "UPDATE accounts SET name = ?, mode = ?, updated_at = ? WHERE id = ?",
		a.Name, string(a.Mode), us(a.UpdatedAt), a.ID))
}

func (r accounts) Delete(ctx context.Context, id string) error {
	return mustAffect(r.db.ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", id))
}

func (r accounts) Shares(ctx context.Context, accountID string) ([]domain.AccountShare, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT s.account_id, s.user_id, COALESCE(u.username, ''), s.access, s.created_at "+
		"FROM account_shares s LEFT JOIN users u ON u.id = s.user_id WHERE s.account_id = ? ORDER BY u.username", accountID)
	if err != nil {
		return nil, fmt.Errorf("accounts.Shares: %w", err)
	}
	defer rows.Close()
	var out []domain.AccountShare
	for rows.Next() {
		var s domain.AccountShare
		var access string
		var c int64
		if err := rows.Scan(&s.AccountID, &s.UserID, &s.Username, &access, &c); err != nil {
			return nil, fmt.Errorf("accounts.Shares: %w", err)
		}
		s.Access, s.CreatedAt = domain.AccountAccess(access), fromUS(c)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r accounts) SetShare(ctx context.Context, s domain.AccountShare) error {
	err := upsert(ctx, r.db,
		"UPDATE account_shares SET access = ? WHERE account_id = ? AND user_id = ?", []any{string(s.Access), s.AccountID, s.UserID},
		"INSERT INTO account_shares (account_id, user_id, access, created_at) VALUES (?, ?, ?, ?)", []any{s.AccountID, s.UserID, string(s.Access), us(s.CreatedAt)})
	if err != nil {
		return fmt.Errorf("accounts.SetShare: %w", err)
	}
	return nil
}

func (r accounts) RemoveShare(ctx context.Context, accountID, userID string) error {
	return mustAffect(r.db.ExecContext(ctx, "DELETE FROM account_shares WHERE account_id = ? AND user_id = ?", accountID, userID))
}

func (r accounts) CountOwnedBy(ctx context.Context, userID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts WHERE owner_id = ?", userID).Scan(&n)
	return n, err
}
