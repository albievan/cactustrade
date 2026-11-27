package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/albievan/cactustrade/internal/domain"
)

// ---- instruments -------------------------------------------------------------------

type instruments struct{ db *sql.DB }

const instrumentCols = "id, symbol, name, type, exchange_id, market, currency, eligible_real_long, " +
	"eligibility_reason, min_position_cents, eligible_short_cfd, short_eligibility_reason, min_short_cents, eligibility_checked_at"

func scanInstrument(row interface{ Scan(...any) error }) (domain.Instrument, domain.Eligibility, error) {
	var in domain.Instrument
	var el domain.Eligibility
	var id, minCents, minShort, checked int64
	var market string
	if err := row.Scan(&id, &in.Symbol, &in.Name, &in.Type, &in.ExchangeID, &market, &in.Currency,
		&el.RealLong, &el.Reason, &minCents, &el.ShortCFD, &el.ShortReason, &minShort, &checked); err != nil {
		return domain.Instrument{}, domain.Eligibility{}, notFound(err)
	}
	in.ID, in.Market = domain.InstrumentID(id), domain.Market(market)
	el.Instrument, el.Symbol, el.MinPositionAmount, el.MinShortAmount = in.ID, in.Symbol, domain.Cents(minCents), domain.Cents(minShort)
	if checked != 0 {
		el.CheckedAt = fromUS(checked)
	}
	return in, el, nil
}

func (r instruments) Upsert(ctx context.Context, in domain.Instrument, el domain.Eligibility, at time.Time) error {
	if el.Instrument != in.ID {
		return fmt.Errorf("instruments.Upsert: eligibility for %d given with instrument %d: %w", el.Instrument, in.ID, domain.ErrInvalid)
	}
	checked := int64(0)
	if !el.CheckedAt.IsZero() {
		checked = us(el.CheckedAt)
	}
	vals := []any{
		in.Symbol, in.Name, in.Type, in.ExchangeID, string(in.Market), in.Currency, el.RealLong,
		el.Reason, int64(el.MinPositionAmount), el.ShortCFD, clip(el.ShortReason, 200), int64(el.MinShortAmount), checked, us(at),
	}
	err := upsert(ctx, r.db,
		"UPDATE instruments SET symbol = ?, name = ?, type = ?, exchange_id = ?, market = ?, currency = ?, "+
			"eligible_real_long = ?, eligibility_reason = ?, min_position_cents = ?, eligible_short_cfd = ?, "+
			"short_eligibility_reason = ?, min_short_cents = ?, eligibility_checked_at = ?, "+
			"updated_at = ? WHERE id = ?", append(vals, int64(in.ID)),
		"INSERT INTO instruments ("+instrumentCols+", updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		append([]any{int64(in.ID)}, vals...))
	if err != nil {
		return fmt.Errorf("instruments.Upsert: %w", err)
	}
	return nil
}

func (r instruments) Get(ctx context.Context, id domain.InstrumentID) (domain.Instrument, domain.Eligibility, error) {
	return scanInstrument(r.db.QueryRowContext(ctx, "SELECT "+instrumentCols+" FROM instruments WHERE id = ?", int64(id)))
}

// ---- watchlist ---------------------------------------------------------------------

type watchlist struct{ db *sql.DB }

func (r watchlist) Add(ctx context.Context, accountID string, id domain.InstrumentID, addedBy string, at time.Time) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("watchlist.Add: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	// Check both parents explicitly: foreign-key errors differ between the two databases.
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM instruments WHERE id = ?) + (SELECT COUNT(*) FROM accounts WHERE id = ?)", int64(id), accountID).Scan(&n); err != nil {
		return fmt.Errorf("watchlist.Add: %w", err)
	}
	if n != 2 {
		err = domain.ErrNotFound
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO watchlist (account_id, instrument_id, added_by, added_at) VALUES (?, ?, ?, ?)", accountID, int64(id), addedBy, us(at))
	if isDuplicate(err) {
		err = domain.ErrConflict
		return err
	}
	if err != nil {
		return fmt.Errorf("watchlist.Add: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("watchlist.Add: %w", err)
	}
	return nil
}

func (r watchlist) Remove(ctx context.Context, accountID string, id domain.InstrumentID) error {
	return mustAffect(r.db.ExecContext(ctx, "DELETE FROM watchlist WHERE account_id = ? AND instrument_id = ?", accountID, int64(id)))
}

func (r watchlist) List(ctx context.Context, accountID string) ([]domain.WatchItem, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT i.id, i.symbol, i.name, i.type, i.exchange_id, i.market, i.currency, "+
		"i.eligible_real_long, i.eligibility_reason, i.min_position_cents, i.eligible_short_cfd, i.short_eligibility_reason, "+
		"i.min_short_cents, i.eligibility_checked_at, w.added_by, w.added_at "+
		"FROM watchlist w JOIN instruments i ON i.id = w.instrument_id WHERE w.account_id = ? ORDER BY i.symbol, i.id", accountID)
	if err != nil {
		return nil, fmt.Errorf("watchlist.List: %w", err)
	}
	defer rows.Close()
	var out []domain.WatchItem
	for rows.Next() {
		var w domain.WatchItem
		var addedAt int64
		in, el, err := scanInstrument(rowScanner(func(dst ...any) error {
			return rows.Scan(append(dst, &w.AddedBy, &addedAt)...)
		}))
		if err != nil {
			return nil, fmt.Errorf("watchlist.List: %w", err)
		}
		w.Instrument, w.Eligibility, w.AddedAt = in, el, fromUS(addedAt)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (r watchlist) Watched(ctx context.Context) ([]domain.InstrumentID, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT DISTINCT instrument_id FROM watchlist ORDER BY instrument_id")
	if err != nil {
		return nil, fmt.Errorf("watchlist.Watched: %w", err)
	}
	defer rows.Close()
	var out []domain.InstrumentID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("watchlist.Watched: %w", err)
		}
		out = append(out, domain.InstrumentID(id))
	}
	return out, rows.Err()
}

// rowScanner adapts a function to the Scan interface (to scan extra trailing columns).
type rowScanner func(dst ...any) error

func (f rowScanner) Scan(dst ...any) error { return f(dst...) }

// ---- events --------------------------------------------------------------------------

type events struct{ db *sql.DB }

func (r events) Append(ctx context.Context, e domain.Event) error {
	_, err := r.db.ExecContext(ctx, "INSERT INTO events (id, at, kind, severity, title, detail, actor, account_id, direction) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		e.ID, us(e.At), e.Kind, string(e.Severity), clip(e.Title, 255), clip(e.Detail, 2000), clip(e.Actor, 64), e.AccountID, string(e.Direction))
	if isDuplicate(err) {
		return domain.ErrConflict
	}
	if err != nil {
		return fmt.Errorf("events.Append: %w", err)
	}
	return nil
}

// accountFilter is "account_id IN (”, ?, ?…)" with its arguments.
func accountFilter(ids []string) (string, []any) {
	args := []any{""}
	for _, id := range ids {
		args = append(args, id)
	}
	return "account_id IN (?" + strings.Repeat(", ?", len(ids)) + ")", args
}

func (r events) List(ctx context.Context, accountIDs []string, limit int) ([]domain.Event, error) {
	where, args := accountFilter(accountIDs)
	rows, err := r.db.QueryContext(ctx, "SELECT id, at, kind, severity, title, detail, actor, account_id, direction FROM events WHERE "+where+
		" ORDER BY at DESC, id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("events.List: %w", err)
	}
	defer rows.Close()
	var out []domain.Event
	for rows.Next() {
		var e domain.Event
		var at int64
		var sev, dir string
		if err := rows.Scan(&e.ID, &at, &e.Kind, &sev, &e.Title, &e.Detail, &e.Actor, &e.AccountID, &dir); err != nil {
			return nil, fmt.Errorf("events.List: %w", err)
		}
		e.At, e.Severity, e.Direction = fromUS(at), domain.EventSeverity(sev), domain.Direction(dir)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r events) CountSince(ctx context.Context, accountIDs []string, since time.Time) (int, error) {
	where, args := accountFilter(accountIDs)
	var n int
	err := r.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE "+where+" AND at > ?", append(args, us(since))...).Scan(&n)
	return n, err
}

func (r events) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, "DELETE FROM events WHERE at < ?", us(before))
	if err != nil {
		return 0, fmt.Errorf("events.DeleteBefore: %w", err)
	}
	return res.RowsAffected()
}

// clip shortens s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
