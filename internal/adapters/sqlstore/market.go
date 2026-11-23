package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// ---- instruments -------------------------------------------------------------------

type instruments struct{ db *sql.DB }

const instrumentCols = "id, symbol, name, type, exchange_id, market, currency, eligible_real_long, " +
	"eligibility_reason, min_position_cents, eligibility_checked_at"

func scanInstrument(row interface{ Scan(...any) error }) (domain.Instrument, domain.Eligibility, error) {
	var in domain.Instrument
	var el domain.Eligibility
	var id, minCents, checked int64
	var market string
	if err := row.Scan(&id, &in.Symbol, &in.Name, &in.Type, &in.ExchangeID, &market, &in.Currency,
		&el.RealLong, &el.Reason, &minCents, &checked); err != nil {
		return domain.Instrument{}, domain.Eligibility{}, notFound(err)
	}
	in.ID, in.Market = domain.InstrumentID(id), domain.Market(market)
	el.Instrument, el.Symbol, el.MinPositionAmount = in.ID, in.Symbol, domain.Cents(minCents)
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
		el.Reason, int64(el.MinPositionAmount), checked, us(at),
	}
	err := upsert(ctx, r.db,
		"UPDATE instruments SET symbol = ?, name = ?, type = ?, exchange_id = ?, market = ?, currency = ?, "+
			"eligible_real_long = ?, eligibility_reason = ?, min_position_cents = ?, eligibility_checked_at = ?, "+
			"updated_at = ? WHERE id = ?", append(vals, int64(in.ID)),
		"INSERT INTO instruments ("+instrumentCols+", updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
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

func (r watchlist) Add(ctx context.Context, id domain.InstrumentID, addedBy string, at time.Time) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("watchlist.Add: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	// Check the instrument explicitly: foreign-key errors differ between the two databases.
	var n int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM instruments WHERE id = ?", int64(id)).Scan(&n); err != nil {
		return fmt.Errorf("watchlist.Add: %w", err)
	}
	if n == 0 {
		err = domain.ErrNotFound
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO watchlist (instrument_id, added_by, added_at) VALUES (?, ?, ?)", int64(id), addedBy, us(at))
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

func (r watchlist) Remove(ctx context.Context, id domain.InstrumentID) error {
	return mustAffect(r.db.ExecContext(ctx, "DELETE FROM watchlist WHERE instrument_id = ?", int64(id)))
}

func (r watchlist) List(ctx context.Context) ([]domain.WatchItem, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT i.id, i.symbol, i.name, i.type, i.exchange_id, i.market, i.currency, "+
		"i.eligible_real_long, i.eligibility_reason, i.min_position_cents, i.eligibility_checked_at, w.added_by, w.added_at "+
		"FROM watchlist w JOIN instruments i ON i.id = w.instrument_id ORDER BY i.symbol, i.id")
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
	if err := rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("watchlist.List: %w", err)
	}
	return out, nil
}

// rowScanner adapts a function to the Scan interface (to scan extra trailing columns).
type rowScanner func(dst ...any) error

func (f rowScanner) Scan(dst ...any) error { return f(dst...) }
