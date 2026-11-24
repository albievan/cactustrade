package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// R30: trading-statement schedules, the outbox's attachments and LLM usage per account.

// StatementSchedules returns the statement schedules (R30).
func (s *Store) StatementSchedules() ports.StatementSchedules { return statementSchedules{s.db} }

type statementSchedules struct{ db *sql.DB }

const scheduleCols = "id, owner_id, period, account_ids, recipients, include_combined, include_accounts, include_exchanges, enabled, " +
	"last_period, last_sent_at, last_error, manual_sent_at, created_at, updated_at"

func encodeList(v []string) (string, error) {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func scanSchedule(sc interface{ Scan(...any) error }) (domain.StatementSchedule, error) {
	var x domain.StatementSchedule
	var period, ids, rcpt string
	var comb, accts, exch, enabled int
	var last, manual, created, updated int64
	if err := sc.Scan(&x.ID, &x.OwnerID, &period, &ids, &rcpt, &comb, &accts, &exch, &enabled, &x.LastPeriod, &last, &x.LastError,
		&manual, &created, &updated); err != nil {
		return domain.StatementSchedule{}, err
	}
	if err := json.Unmarshal([]byte(ids), &x.AccountIDs); err != nil {
		return domain.StatementSchedule{}, fmt.Errorf("account ids: %w", err)
	}
	if err := json.Unmarshal([]byte(rcpt), &x.Recipients); err != nil {
		return domain.StatementSchedule{}, fmt.Errorf("recipients: %w", err)
	}
	x.Period = domain.StatementPeriod(period)
	x.IncludeCombined, x.IncludeAccounts, x.IncludeExchanges, x.Enabled = comb != 0, accts != 0, exch != 0, enabled != 0
	x.LastSentAt, x.ManualSentAt = fromUSZ(last), fromUSZ(manual)
	x.CreatedAt, x.UpdatedAt = fromUS(created), fromUS(updated)
	return x, nil
}

func (r statementSchedules) Create(ctx context.Context, x domain.StatementSchedule) error {
	ids, err := encodeList(x.AccountIDs)
	if err != nil {
		return fmt.Errorf("statementSchedules.Create: %w", err)
	}
	rcpt, err := encodeList(x.Recipients)
	if err != nil {
		return fmt.Errorf("statementSchedules.Create: %w", err)
	}
	var n int
	if err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id = ?", x.OwnerID).Scan(&n); err != nil {
		return fmt.Errorf("statementSchedules.Create: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	_, err = r.db.ExecContext(ctx, "INSERT INTO statement_schedules ("+scheduleCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		x.ID, x.OwnerID, string(x.Period), ids, rcpt, b2i(x.IncludeCombined), b2i(x.IncludeAccounts), b2i(x.IncludeExchanges), b2i(x.Enabled),
		x.LastPeriod, usZ(x.LastSentAt), clip(x.LastError, 500), usZ(x.ManualSentAt), us(x.CreatedAt), us(x.UpdatedAt))
	if isDuplicate(err) {
		return domain.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("statementSchedules.Create: %w", err)
	}
	return nil
}

func (r statementSchedules) Get(ctx context.Context, id string) (domain.StatementSchedule, error) {
	x, err := scanSchedule(r.db.QueryRowContext(ctx, "SELECT "+scheduleCols+" FROM statement_schedules WHERE id = ?", id))
	if err != nil {
		return domain.StatementSchedule{}, notFound(err)
	}
	return x, nil
}

func (r statementSchedules) List(ctx context.Context, ownerID string) ([]domain.StatementSchedule, error) {
	q, args := "SELECT "+scheduleCols+" FROM statement_schedules ORDER BY created_at, id", []any{}
	if ownerID != "" {
		q, args = "SELECT "+scheduleCols+" FROM statement_schedules WHERE owner_id = ? ORDER BY created_at, id", []any{ownerID}
	}
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("statementSchedules.List: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.StatementSchedule
	for rows.Next() {
		x, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("statementSchedules.List: %w", err)
		}
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("statementSchedules.List: %w", err)
	}
	return out, nil
}

func (r statementSchedules) Update(ctx context.Context, x domain.StatementSchedule) error {
	ids, err := encodeList(x.AccountIDs)
	if err != nil {
		return fmt.Errorf("statementSchedules.Update: %w", err)
	}
	rcpt, err := encodeList(x.Recipients)
	if err != nil {
		return fmt.Errorf("statementSchedules.Update: %w", err)
	}
	if err := mustAffect(r.db.ExecContext(ctx, "UPDATE statement_schedules SET period = ?, account_ids = ?, recipients = ?, include_combined = ?, "+
		"include_accounts = ?, include_exchanges = ?, enabled = ?, last_period = ?, updated_at = ? WHERE id = ?",
		string(x.Period), ids, rcpt, b2i(x.IncludeCombined), b2i(x.IncludeAccounts), b2i(x.IncludeExchanges), b2i(x.Enabled),
		x.LastPeriod, us(x.UpdatedAt), x.ID)); err != nil {
		return fmt.Errorf("statementSchedules.Update: %w", err)
	}
	return nil
}

func (r statementSchedules) Delete(ctx context.Context, id string) error {
	if err := mustAffect(r.db.ExecContext(ctx, "DELETE FROM statement_schedules WHERE id = ?", id)); err != nil {
		return fmt.Errorf("statementSchedules.Delete: %w", err)
	}
	return nil
}

// cas runs a compare-and-set update: no row changed means ErrConflict when the schedule
// exists, else ErrNotFound.
func (r statementSchedules) cas(ctx context.Context, id string, res sql.Result, err error) error {
	switch err := mustAffect(res, err); {
	case err == nil:
		return nil
	case !errors.Is(err, domain.ErrNotFound):
		return err
	}
	if _, err := r.Get(ctx, id); err != nil {
		return err
	}
	return domain.ErrConflict
}

func (r statementSchedules) MarkPeriod(ctx context.Context, id, prev, key string, at time.Time, lastErr string) error {
	res, err := r.db.ExecContext(ctx, "UPDATE statement_schedules SET last_period = ?, last_sent_at = ?, last_error = ? WHERE id = ? AND last_period = ?",
		key, us(at), clip(lastErr, 500), id, prev)
	if err := r.cas(ctx, id, res, err); err != nil {
		return fmt.Errorf("statementSchedules.MarkPeriod: %w", err)
	}
	return nil
}

func (r statementSchedules) MarkManual(ctx context.Context, id string, prev, at time.Time) error {
	res, err := r.db.ExecContext(ctx, "UPDATE statement_schedules SET manual_sent_at = ? WHERE id = ? AND manual_sent_at = ?", us(at), id, usZ(prev))
	if err := r.cas(ctx, id, res, err); err != nil {
		return fmt.Errorf("statementSchedules.MarkManual: %w", err)
	}
	return nil
}

// ---- outbox attachments -------------------------------------------------------------------

// enqueueWithAttachments stores a mail and its files in one transaction.
func (r outbox) enqueueWithAttachments(ctx context.Context, m domain.OutboxMail) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, "INSERT INTO outbox ("+outboxCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		m.ID, m.To, m.Subject, m.Body, us(m.CreatedAt), us(m.NextAt), m.Attempts, int64(0), m.LastError); err != nil {
		return err
	}
	for i, a := range m.Attachments {
		if _, err = tx.ExecContext(ctx, "INSERT INTO outbox_attachments (mail_id, seq, filename, content_type, data) VALUES (?, ?, ?, ?, ?)",
			m.ID, i, a.Filename, a.ContentType, a.Data); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// attachments loads the files of the given mails (by mail id, in order).
func (r outbox) attachments(ctx context.Context, ms []domain.OutboxMail) error {
	for i := range ms {
		as, err := r.attachmentsOf(ctx, ms[i].ID)
		if err != nil {
			return err
		}
		ms[i].Attachments = as
	}
	return nil
}

func (r outbox) attachmentsOf(ctx context.Context, mailID string) ([]domain.Attachment, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT filename, content_type, data FROM outbox_attachments WHERE mail_id = ? ORDER BY seq", mailID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Attachment
	for rows.Next() {
		var a domain.Attachment
		if err := rows.Scan(&a.Filename, &a.ContentType, &a.Data); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- LLM usage per account ------------------------------------------------------------------

func (r llmCalls) UsageFor(ctx context.Context, accountIDs []string, from, to time.Time) (domain.LLMUsage, error) {
	ids := slices.Compact(slices.Sorted(slices.Values(accountIDs)))
	if len(ids) == 0 {
		return domain.LLMUsage{}, nil
	}
	args := make([]any, 0, len(ids)+2)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, us(from), us(to))
	var u domain.LLMUsage
	var failed, in, out, cost, unknown sql.NullInt64
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*), SUM(CASE WHEN error <> '' THEN 1 ELSE 0 END), SUM(input_tokens), SUM(output_tokens), "+
		"SUM(CASE WHEN cost_micros >= 0 THEN cost_micros ELSE 0 END), SUM(CASE WHEN cost_micros < 0 THEN 1 ELSE 0 END) "+
		"FROM llm_calls WHERE account_id IN (?"+strings.Repeat(", ?", len(ids)-1)+") AND at >= ? AND at < ?", args...).
		Scan(&u.Calls, &failed, &in, &out, &cost, &unknown)
	if err != nil {
		return domain.LLMUsage{}, fmt.Errorf("llmCalls.UsageFor: %w", err)
	}
	u.Failed, u.InputTokens, u.OutputTokens, u.CostUnknownCalls = failed.Int64, in.Int64, out.Int64, unknown.Int64
	u.Cost = domain.Micros(cost.Int64)
	return u, nil
}
