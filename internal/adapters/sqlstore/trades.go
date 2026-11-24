package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Trades returns the closed trades read from eToro (migration 00011).
func (s *Store) Trades() ports.Trades { return trades{s.db} }

type trades struct{ db *sql.DB }

const tradeCols = "account_id, position_id, mode, instrument_id, direction, leverage, ours, open_request_id, opened_at, closed_at, " +
	"units_micros, invested_cents, open_micros, close_micros, stop_micros, take_profit_micros, net_profit_cents, fees_cents, " +
	"fees_known, etoro_order_id, synced_at"

func (r trades) Upsert(ctx context.Context, t domain.Trade) error {
	if err := t.Validate(); err != nil {
		return fmt.Errorf("trades.Upsert: %w", err)
	}
	ok, err := accountExists(ctx, r.db, t.AccountID)
	if err != nil {
		return fmt.Errorf("trades.Upsert: %w", err)
	}
	if !ok {
		return fmt.Errorf("trades.Upsert: account %s: %w", t.AccountID, domain.ErrNotFound)
	}
	vals := []any{
		string(t.Mode), int64(t.Instrument), string(t.Direction()), t.Leverage, b2i(t.Ours), clip(t.OpenRequestID, 64),
		usZ(t.OpenedAt), us(t.ClosedAt), int64(t.Units), int64(t.Investment), int64(t.OpenRate), int64(t.CloseRate),
		int64(t.StopLoss), int64(t.TakeProfit), int64(t.NetProfit), int64(t.Fees), b2i(t.FeesKnown), t.OrderID, us(t.SyncedAt),
	}
	key := []any{t.AccountID, t.PositionID}
	err = upsert(ctx, r.db,
		"UPDATE trades SET mode = ?, instrument_id = ?, direction = ?, leverage = ?, ours = ?, open_request_id = ?, opened_at = ?, "+
			"closed_at = ?, units_micros = ?, invested_cents = ?, open_micros = ?, close_micros = ?, stop_micros = ?, "+
			"take_profit_micros = ?, net_profit_cents = ?, fees_cents = ?, fees_known = ?, etoro_order_id = ?, synced_at = ? "+
			"WHERE account_id = ? AND position_id = ?", append(vals, key...),
		"INSERT INTO trades ("+tradeCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		append(append([]any{}, key...), vals...))
	if err != nil {
		return fmt.Errorf("trades.Upsert: %w", err)
	}
	return nil
}

func scanTrade(sc interface{ Scan(...any) error }) (domain.Trade, error) {
	var t domain.Trade
	var mode, dir string
	var inst, ours, opened, closed, units, invested, open, cls, stop, tp, net, fees, feesKnown, synced int64
	err := sc.Scan(&t.AccountID, &t.PositionID, &mode, &inst, &dir, &t.Leverage, &ours, &t.OpenRequestID, &opened, &closed,
		&units, &invested, &open, &cls, &stop, &tp, &net, &fees, &feesKnown, &t.OrderID, &synced)
	if err != nil {
		return domain.Trade{}, err
	}
	t.Mode, t.Instrument, t.Long, t.Ours = domain.TradingMode(mode), domain.InstrumentID(inst), domain.Direction(dir) == domain.Long, ours != 0
	t.OpenedAt, t.ClosedAt, t.SyncedAt = fromUSZ(opened), fromUS(closed), fromUS(synced)
	t.Units, t.Investment, t.OpenRate, t.CloseRate = domain.Micros(units), domain.Cents(invested), domain.Micros(open), domain.Micros(cls)
	t.StopLoss, t.TakeProfit, t.NetProfit = domain.Micros(stop), domain.Micros(tp), domain.Cents(net)
	t.Fees, t.FeesKnown = domain.Cents(fees), feesKnown != 0
	return t, nil
}

func (r trades) List(ctx context.Context, accountID string, f ports.TradeFilter) ([]domain.Trade, error) {
	q, args := "SELECT "+tradeCols+" FROM trades WHERE account_id = ?", []any{accountID}
	if !f.From.IsZero() {
		q, args = q+" AND closed_at >= ?", append(args, us(f.From))
	}
	if !f.To.IsZero() {
		q, args = q+" AND closed_at < ?", append(args, us(f.To))
	}
	if !f.Before.IsZero() {
		q, args = q+" AND closed_at < ?", append(args, us(f.Before))
	}
	if f.Ours != nil {
		q, args = q+" AND ours = ?", append(args, b2i(*f.Ours))
	}
	rows, err := r.db.QueryContext(ctx, q+" ORDER BY closed_at DESC, position_id DESC LIMIT ?", append(args, max(f.Limit, 0))...)
	if err != nil {
		return nil, fmt.Errorf("trades.List: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Trade
	for rows.Next() {
		t, err := scanTrade(rows)
		if err != nil {
			return nil, fmt.Errorf("trades.List: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("trades.List: %w", err)
	}
	return out, nil
}

func (r trades) LastClosedAt(ctx context.Context, accountID string) (time.Time, error) {
	var v sql.NullInt64
	if err := r.db.QueryRowContext(ctx, "SELECT MAX(closed_at) FROM trades WHERE account_id = ?", accountID).Scan(&v); err != nil {
		return time.Time{}, fmt.Errorf("trades.LastClosedAt: %w", err)
	}
	if !v.Valid {
		return time.Time{}, nil
	}
	return fromUS(v.Int64), nil
}
