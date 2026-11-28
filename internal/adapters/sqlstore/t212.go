package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// T212Instruments returns Trading 212's instrument catalogue (migration 00016).
func (s *Store) T212Instruments() ports.T212Instruments { return t212Instruments{s.db} }

// InstrumentMatches returns the accounts' instrument matches (migration 00016).
func (s *Store) InstrumentMatches() ports.InstrumentMatches { return instrumentMatches{s.db} }

// ---- Trading 212 catalogue -------------------------------------------------------------

type t212Instruments struct{ db *sql.DB }

const t212Cols = "ticker, isin, name, short_name, currency, type, max_open_quantity_micros, fetched_at"

// t212Batch is how many instruments one INSERT carries (8 placeholders each, well under
// SQLite's 32 766 and MariaDB's 65 535).
const t212Batch = 500

// t212InsertSQL is the INSERT of n catalogue rows: placeholders only, no value is ever part
// of the statement.
func t212InsertSQL(n int) string {
	return "INSERT INTO t212_instruments (" + t212Cols + ") VALUES " +
		strings.TrimSuffix(strings.Repeat("(?, ?, ?, ?, ?, ?, ?, ?), ", n), ", ")
}

// ReplaceAll deletes the catalogue and inserts ins in one transaction: any failure rolls it
// back, so the old catalogue stays.
func (r t212Instruments) ReplaceAll(ctx context.Context, ins []domain.T212Instrument, at time.Time) (err error) {
	for _, in := range ins {
		if in.Ticker == "" || len(in.Ticker) > maxTicker {
			return fmt.Errorf("t212Instruments.ReplaceAll: ticker %q: %w", clip(in.Ticker, maxTicker), domain.Invalid("ticker", "1-64 characters"))
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("t212Instruments.ReplaceAll: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.ExecContext(ctx, "DELETE FROM t212_instruments"); err != nil {
		return fmt.Errorf("t212Instruments.ReplaceAll: %w", err)
	}
	fetched := us(at)
	for start := 0; start < len(ins); start += t212Batch {
		part := ins[start:min(start+t212Batch, len(ins))]
		args := make([]any, 0, 8*len(part))
		for _, in := range part {
			args = append(args, in.Ticker, clip(in.ISIN, 16), clip(in.Name, 255), clip(strings.ToUpper(strings.TrimSpace(in.ShortName)), 64),
				clip(in.Currency, 8), clip(in.Type, 32), int64(in.MaxOpenQuantity), fetched)
		}
		if _, err = tx.ExecContext(ctx, t212InsertSQL(len(part)), args...); err != nil {
			if isDuplicate(err) {
				err = fmt.Errorf("t212Instruments.ReplaceAll: a ticker is listed twice: %w", domain.ErrConflict)
				return err
			}
			return fmt.Errorf("t212Instruments.ReplaceAll: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("t212Instruments.ReplaceAll: %w", err)
	}
	return nil
}

func scanT212(sc interface{ Scan(...any) error }) (domain.T212Instrument, error) {
	var in domain.T212Instrument
	var maxOpen, fetched int64
	if err := sc.Scan(&in.Ticker, &in.ISIN, &in.Name, &in.ShortName, &in.Currency, &in.Type, &maxOpen, &fetched); err != nil {
		return domain.T212Instrument{}, err
	}
	in.MaxOpenQuantity, in.FetchedAt = domain.Micros(maxOpen), fromUS(fetched)
	return in, nil
}

func (r t212Instruments) Get(ctx context.Context, ticker string) (domain.T212Instrument, error) {
	in, err := scanT212(r.db.QueryRowContext(ctx, "SELECT "+t212Cols+" FROM t212_instruments WHERE ticker = ?", ticker))
	if err != nil {
		return domain.T212Instrument{}, fmt.Errorf("t212Instruments.Get: %w", notFound(err))
	}
	return in, nil
}

func (r t212Instruments) ByShortName(ctx context.Context, shortName string) ([]domain.T212Instrument, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+t212Cols+" FROM t212_instruments WHERE short_name = ? ORDER BY ticker",
		strings.ToUpper(strings.TrimSpace(shortName)))
	if err != nil {
		return nil, fmt.Errorf("t212Instruments.ByShortName: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.T212Instrument
	for rows.Next() {
		in, err := scanT212(rows)
		if err != nil {
			return nil, fmt.Errorf("t212Instruments.ByShortName: %w", err)
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("t212Instruments.ByShortName: %w", err)
	}
	return out, nil
}

func (r t212Instruments) FetchedAt(ctx context.Context) (time.Time, error) {
	var at sql.NullInt64
	if err := r.db.QueryRowContext(ctx, "SELECT MAX(fetched_at) FROM t212_instruments").Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("t212Instruments.FetchedAt: %w", err)
	}
	if !at.Valid {
		return time.Time{}, fmt.Errorf("t212Instruments.FetchedAt: the catalogue is empty: %w", domain.ErrNotFound)
	}
	return fromUS(at.Int64), nil
}

// ---- instrument matches ----------------------------------------------------------------

type instrumentMatches struct{ db *sql.DB }

const matchCols = "account_id, instrument_id, ticker, isin, method, confirmed_by, confirmed_at, updated_at"

// Put upserts in Go (update, else insert) in one transaction (CLAUDE.md §3: no ON CONFLICT).
func (r instrumentMatches) Put(ctx context.Context, m domain.InstrumentMatch) error {
	ok, err := accountExists(ctx, r.db, m.AccountID)
	if err != nil {
		return fmt.Errorf("instrumentMatches.Put: %w", err)
	}
	if !ok {
		return fmt.Errorf("instrumentMatches.Put: account %s: %w", m.AccountID, domain.ErrNotFound)
	}
	if len(m.Ticker) > maxTicker {
		return fmt.Errorf("instrumentMatches.Put: ticker: %w", domain.ErrInvalid)
	}
	vals := []any{m.Ticker, clip(m.ISIN, 16), clip(m.Method, 16), clip(m.ConfirmedBy, 64), usZ(m.ConfirmedAt), us(m.UpdatedAt)}
	err = upsert(ctx, r.db,
		"UPDATE instrument_matches SET ticker = ?, isin = ?, method = ?, confirmed_by = ?, confirmed_at = ?, updated_at = ? WHERE account_id = ? AND instrument_id = ?",
		append(vals[:len(vals):len(vals)], m.AccountID, int64(m.Instrument)),
		"INSERT INTO instrument_matches (ticker, isin, method, confirmed_by, confirmed_at, updated_at, account_id, instrument_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		append(vals[:len(vals):len(vals)], m.AccountID, int64(m.Instrument)))
	if err != nil {
		return fmt.Errorf("instrumentMatches.Put: %w", err)
	}
	return nil
}

func scanMatch(sc interface{ Scan(...any) error }) (domain.InstrumentMatch, error) {
	var m domain.InstrumentMatch
	var id, confirmed, updated int64
	if err := sc.Scan(&m.AccountID, &id, &m.Ticker, &m.ISIN, &m.Method, &m.ConfirmedBy, &confirmed, &updated); err != nil {
		return domain.InstrumentMatch{}, err
	}
	m.Instrument, m.ConfirmedAt, m.UpdatedAt = domain.InstrumentID(id), fromUSZ(confirmed), fromUS(updated)
	return m, nil
}

func (r instrumentMatches) Get(ctx context.Context, accountID string, id domain.InstrumentID) (domain.InstrumentMatch, error) {
	m, err := scanMatch(r.db.QueryRowContext(ctx, "SELECT "+matchCols+" FROM instrument_matches WHERE account_id = ? AND instrument_id = ?", accountID, int64(id)))
	if err != nil {
		return domain.InstrumentMatch{}, fmt.Errorf("instrumentMatches.Get: %w", notFound(err))
	}
	return m, nil
}

func (r instrumentMatches) List(ctx context.Context, accountID string) ([]domain.InstrumentMatch, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+matchCols+" FROM instrument_matches WHERE account_id = ? ORDER BY instrument_id", accountID)
	if err != nil {
		return nil, fmt.Errorf("instrumentMatches.List: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.InstrumentMatch
	for rows.Next() {
		m, err := scanMatch(rows)
		if err != nil {
			return nil, fmt.Errorf("instrumentMatches.List: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("instrumentMatches.List: %w", err)
	}
	return out, nil
}

func (r instrumentMatches) Delete(ctx context.Context, accountID string, id domain.InstrumentID) error {
	if err := mustAffect(r.db.ExecContext(ctx, "DELETE FROM instrument_matches WHERE account_id = ? AND instrument_id = ?", accountID, int64(id))); err != nil {
		return fmt.Errorf("instrumentMatches.Delete: %w", err)
	}
	return nil
}
