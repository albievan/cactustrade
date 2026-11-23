package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// ---- themes ---------------------------------------------------------------------------

type themes struct{ db *sql.DB }

const themeCols = "id, account_id, name, keywords, industry_id, enabled, llm_profile_id, created_at, updated_at"

func (r themes) scan(ctx context.Context, row interface{ Scan(...any) error }) (domain.Theme, error) {
	var t domain.Theme
	var kws string
	var c, u int64
	if err := row.Scan(&t.ID, &t.AccountID, &t.Name, &kws, &t.IndustryID, &t.Enabled, &t.LLMProfileID, &c, &u); err != nil {
		return domain.Theme{}, notFound(err)
	}
	if kws != "" {
		t.Keywords = strings.Split(kws, "\n")
	}
	t.CreatedAt, t.UpdatedAt = fromUS(c), fromUS(u)
	return t, nil
}

func (r themes) instruments(ctx context.Context, id string) ([]domain.InstrumentID, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT instrument_id FROM theme_instruments WHERE theme_id = ? ORDER BY instrument_id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.InstrumentID
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, domain.InstrumentID(n))
	}
	return out, rows.Err()
}

func (r themes) setInstruments(ctx context.Context, tx *sql.Tx, t domain.Theme) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM theme_instruments WHERE theme_id = ?", t.ID); err != nil {
		return err
	}
	for _, id := range t.Instruments {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM instruments WHERE id = ?", int64(id)).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("instrument %d is not cached: %w", id, domain.ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO theme_instruments (theme_id, instrument_id) VALUES (?, ?)", t.ID, int64(id)); err != nil && !isDuplicate(err) {
			return err
		}
	}
	return nil
}

func (r themes) inTx(ctx context.Context, f func(tx *sql.Tx) error) (err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (r themes) Create(ctx context.Context, t domain.Theme) error {
	err := r.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO themes ("+themeCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			t.ID, t.AccountID, t.Name, strings.Join(t.Keywords, "\n"), t.IndustryID, t.Enabled, t.LLMProfileID, us(t.CreatedAt), us(t.UpdatedAt))
		if isDuplicate(err) {
			return domain.ErrConflict
		}
		if err != nil {
			return err
		}
		return r.setInstruments(ctx, tx, t)
	})
	if err != nil {
		return fmt.Errorf("themes.Create: %w", err)
	}
	return nil
}

func (r themes) Get(ctx context.Context, id string) (domain.Theme, error) {
	t, err := r.scan(ctx, r.db.QueryRowContext(ctx, "SELECT "+themeCols+" FROM themes WHERE id = ?", id))
	if err != nil {
		return domain.Theme{}, err
	}
	if t.Instruments, err = r.instruments(ctx, id); err != nil {
		return domain.Theme{}, fmt.Errorf("themes.Get: %w", err)
	}
	return t, nil
}

func (r themes) rows(ctx context.Context, accountID string) ([]domain.Theme, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT "+themeCols+" FROM themes WHERE account_id = ? ORDER BY name, id", accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Theme
	for rows.Next() {
		t, err := r.scan(ctx, rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (r themes) List(ctx context.Context, accountID string) ([]domain.Theme, error) {
	out, err := r.rows(ctx, accountID) // rows closed before the per-theme queries
	if err != nil {
		return nil, fmt.Errorf("themes.List: %w", err)
	}
	for i := range out {
		if out[i].Instruments, err = r.instruments(ctx, out[i].ID); err != nil {
			return nil, fmt.Errorf("themes.List: %w", err)
		}
	}
	return out, nil
}

func (r themes) Update(ctx context.Context, t domain.Theme) error {
	err := r.inTx(ctx, func(tx *sql.Tx) error {
		if err := mustAffect(tx.ExecContext(ctx, "UPDATE themes SET name = ?, keywords = ?, industry_id = ?, enabled = ?, llm_profile_id = ?, updated_at = ? WHERE id = ?",
			t.Name, strings.Join(t.Keywords, "\n"), t.IndustryID, t.Enabled, t.LLMProfileID, us(t.UpdatedAt), t.ID)); err != nil {
			return err
		}
		return r.setInstruments(ctx, tx, t)
	})
	if err != nil {
		return fmt.Errorf("themes.Update: %w", err)
	}
	return nil
}

func (r themes) Delete(ctx context.Context, id string) error {
	return mustAffect(r.db.ExecContext(ctx, "DELETE FROM themes WHERE id = ?", id))
}

// ---- reviews --------------------------------------------------------------------------

type reviews struct{ db *sql.DB }

type checkJSON struct {
	Rule   string `json:"rule"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

func (r reviews) Upsert(ctx context.Context, v domain.Review) error {
	probs, _ := json.Marshal(v.Probs)
	cs := make([]checkJSON, len(v.Checks))
	for i, c := range v.Checks {
		cs[i] = checkJSON{c.Rule, c.Pass, clip(c.Detail, 300)}
	}
	checks, _ := json.Marshal(cs)
	vals := []any{us(v.At), clip(v.Symbol, 64), clip(v.Theme, 255), string(v.Outcome), string(v.Direction), clip(string(probs), 255), string(checks), clip(v.Summary, 1000)}
	err := upsert(ctx, r.db,
		"UPDATE reviews SET at = ?, symbol = ?, theme = ?, outcome = ?, direction = ?, probs = ?, checks = ?, summary = ? WHERE account_id = ? AND instrument_id = ?",
		append(vals, v.AccountID, int64(v.Instrument)),
		"INSERT INTO reviews (at, symbol, theme, outcome, direction, probs, checks, summary, account_id, instrument_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		append(vals, v.AccountID, int64(v.Instrument)))
	if err != nil {
		return fmt.Errorf("reviews.Upsert: %w", err)
	}
	return nil
}

func (r reviews) List(ctx context.Context, accountID string, limit int) ([]domain.Review, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT account_id, instrument_id, at, symbol, theme, outcome, direction, probs, checks, summary "+
		"FROM reviews WHERE account_id = ? ORDER BY at DESC, instrument_id LIMIT ?", accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("reviews.List: %w", err)
	}
	defer rows.Close()
	var out []domain.Review
	for rows.Next() {
		var v domain.Review
		var id, at int64
		var outcome, dir, probs, checks string
		if err := rows.Scan(&v.AccountID, &id, &at, &v.Symbol, &v.Theme, &outcome, &dir, &probs, &checks, &v.Summary); err != nil {
			return nil, fmt.Errorf("reviews.List: %w", err)
		}
		v.Instrument, v.At, v.Outcome, v.Direction = domain.InstrumentID(id), fromUS(at), domain.ReviewOutcome(outcome), domain.Direction(dir)
		_ = json.Unmarshal([]byte(probs), &v.Probs)
		var cs []checkJSON
		_ = json.Unmarshal([]byte(checks), &cs)
		for _, c := range cs {
			v.Checks = append(v.Checks, domain.ReviewCheck{Rule: c.Rule, Pass: c.Pass, Detail: c.Detail})
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r reviews) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, "DELETE FROM reviews WHERE at < ?", us(before))
	if err != nil {
		return 0, fmt.Errorf("reviews.DeleteBefore: %w", err)
	}
	return res.RowsAffected()
}
