package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Orders returns our orders to eToro.
func (s *Store) Orders() ports.Orders { return orders{s.db} }

// AgentPositions returns the positions cactusTrade opened.
func (s *Store) AgentPositions() ports.AgentPositions { return agentPositions{s.db} }

// RiskStates returns the accounts' risk states.
func (s *Store) RiskStates() ports.RiskStates { return riskStates{s.db} }

// InstrumentBlocks returns the blocked instruments.
func (s *Store) InstrumentBlocks() ports.InstrumentBlocks { return instrumentBlocks{s.db} }

// AgentLeases returns the single-writer leases.
func (s *Store) AgentLeases() ports.AgentLeases { return agentLeases{s.db} }

// usZ stores a time that may be unset: the zero time is 0.
func usZ(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return us(t)
}

// fromUSZ reads a time stored by usZ.
func fromUSZ(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return fromUS(v)
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// accountExists reports whether the account exists (repositories answer ErrNotFound for an
// unknown account instead of a foreign-key error, the same in both databases).
func accountExists(ctx context.Context, db *sql.DB, id string) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM accounts WHERE id = ?", id).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// ---- orders ----------------------------------------------------------------------------

type orders struct{ db *sql.DB }

// orderCols: etoro_order_id holds the broker's order id for both platforms (migration 00016
// kept its name).
const orderCols = "request_id, account_id, mode, kind, direction, instrument_id, amount_cents, entry_micros, stop_micros, take_profit_micros, " +
	"position_id, target_order_id, etoro_order_id, state, reason, sends, created_at, sent_at, updated_at, platform, ticker, quantity_micros, filled_micros"

// maxTicker is the longest Trading 212 ticker stored (VARCHAR(64)).
const maxTicker = 64

// platformOf is the platform stored for p ("" is eToro).
func platformOf(p domain.Platform) string {
	if p == "" {
		return string(domain.PlatformEtoro)
	}
	return string(p)
}

const maxReason = 500

func (r orders) Create(ctx context.Context, o domain.Order) error {
	ok, err := accountExists(ctx, r.db, o.AccountID)
	if err != nil {
		return fmt.Errorf("orders.Create: %w", err)
	}
	if !ok {
		return fmt.Errorf("orders.Create: account %s: %w", o.AccountID, domain.ErrNotFound)
	}
	if len(o.Ticker) > maxTicker {
		return fmt.Errorf("orders.Create: ticker: %w", domain.ErrInvalid)
	}
	_, err = r.db.ExecContext(ctx, "INSERT INTO orders ("+orderCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		o.RequestID, o.AccountID, string(o.Mode), string(o.Kind), string(o.Direction), int64(o.Instrument), int64(o.Amount),
		int64(o.Entry), int64(o.StopLoss), int64(o.TakeProfit), o.PositionID, o.TargetOrderID, o.EtoroOrderID,
		string(o.State), clip(o.Reason, maxReason), o.Sends, us(o.CreatedAt), usZ(o.SentAt), us(o.UpdatedAt),
		platformOf(o.Platform), o.Ticker, int64(o.Quantity), int64(o.Filled))
	if isDuplicate(err) {
		return fmt.Errorf("orders.Create: request id %s: %w", o.RequestID, domain.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("orders.Create: %w", err)
	}
	return nil
}

func scanOrder(sc interface{ Scan(...any) error }) (domain.Order, error) {
	var o domain.Order
	var mode, kind, dir, state, platform string
	var inst, amount, entry, stop, tp, created, sent, updated, qty, filled int64
	err := sc.Scan(&o.RequestID, &o.AccountID, &mode, &kind, &dir, &inst, &amount, &entry, &stop, &tp,
		&o.PositionID, &o.TargetOrderID, &o.EtoroOrderID, &state, &o.Reason, &o.Sends, &created, &sent, &updated,
		&platform, &o.Ticker, &qty, &filled)
	if err != nil {
		return domain.Order{}, err
	}
	o.Platform, o.Quantity, o.Filled = domain.Platform(platform), domain.Micros(qty), domain.Micros(filled)
	o.Mode, o.Kind, o.Direction, o.State = domain.TradingMode(mode), domain.OrderKind(kind), domain.Direction(dir), domain.OrderState(state)
	o.Instrument, o.Amount = domain.InstrumentID(inst), domain.Cents(amount)
	o.Entry, o.StopLoss, o.TakeProfit = domain.Micros(entry), domain.Micros(stop), domain.Micros(tp)
	o.CreatedAt, o.SentAt, o.UpdatedAt = fromUS(created), fromUSZ(sent), fromUS(updated)
	return o, nil
}

func (r orders) Get(ctx context.Context, requestID string) (domain.Order, error) {
	o, err := scanOrder(r.db.QueryRowContext(ctx, "SELECT "+orderCols+" FROM orders WHERE request_id = ?", requestID))
	if err != nil {
		return domain.Order{}, fmt.Errorf("orders.Get: %w", notFound(err))
	}
	return o, nil
}

// Update is a compare-and-set on (state, sends): a writer that read an older version of the
// order changes nothing and gets ErrConflict, so no transition is ever lost.
func (r orders) Update(ctx context.Context, prev, next domain.Order) error {
	if next.RequestID != prev.RequestID {
		return fmt.Errorf("orders.Update: request id changed: %w", domain.ErrInvalid)
	}
	res, err := r.db.ExecContext(ctx, "UPDATE orders SET state = ?, reason = ?, sends = ?, sent_at = ?, updated_at = ?, position_id = ?, etoro_order_id = ?, filled_micros = ? "+
		"WHERE request_id = ? AND state = ? AND sends = ?",
		string(next.State), clip(next.Reason, maxReason), next.Sends, usZ(next.SentAt), us(next.UpdatedAt), next.PositionID, next.EtoroOrderID, int64(next.Filled),
		prev.RequestID, string(prev.State), prev.Sends)
	err = mustAffect(res, err)
	if !errors.Is(err, domain.ErrNotFound) {
		if err != nil {
			return fmt.Errorf("orders.Update: %w", err)
		}
		return nil
	}
	if _, gerr := r.Get(ctx, prev.RequestID); gerr != nil {
		return fmt.Errorf("orders.Update: %w", gerr)
	}
	return fmt.Errorf("orders.Update: order %s is no longer %s after %d sends: %w", prev.RequestID, prev.State, prev.Sends, domain.ErrConflict)
}

func (r orders) list(ctx context.Context, op, query string, args ...any) ([]domain.Order, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("orders.%s: %w", op, err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.Order
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, fmt.Errorf("orders.%s: %w", op, err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("orders.%s: %w", op, err)
	}
	return out, nil
}

func (r orders) ListOpen(ctx context.Context, accountID string) ([]domain.Order, error) {
	args := []any{accountID}
	var marks []string
	for _, s := range domain.OrderStates {
		if !s.Final() {
			args = append(args, string(s))
			marks = append(marks, "?")
		}
	}
	return r.list(ctx, "ListOpen", "SELECT "+orderCols+" FROM orders WHERE account_id = ? AND state IN ("+strings.Join(marks, ", ")+
		") ORDER BY created_at, request_id", args...)
}

func (r orders) List(ctx context.Context, accountID string, limit int) ([]domain.Order, error) {
	return r.list(ctx, "List", "SELECT "+orderCols+" FROM orders WHERE account_id = ? ORDER BY created_at DESC, request_id DESC LIMIT ?",
		accountID, max(limit, 0))
}

// ListSince returns the account's orders created at or after since, oldest first.
func (r orders) ListSince(ctx context.Context, accountID string, since time.Time) ([]domain.Order, error) {
	return r.list(ctx, "ListSince", "SELECT "+orderCols+" FROM orders WHERE account_id = ? AND created_at >= ? ORDER BY created_at, request_id",
		accountID, us(since))
}

// ---- agent positions -------------------------------------------------------------------

type agentPositions struct{ db *sql.DB }

const agentPosCols = "account_id, position_id, open_request_id, instrument_id, direction, opened_at, closed_at, close_request_id, ticker, quantity_micros"

// agentPosSelect reads a position with its Sold: the share quantity our own Trading 212 stops
// and sales of it have filled (migration 00017), summed from the orders, so it is exactly what
// was stored with each order's state (D44). eToro positions have no such orders (0).
const agentPosSelect = "SELECT p.account_id, p.position_id, p.open_request_id, p.instrument_id, p.direction, p.opened_at, p.closed_at, p.close_request_id, " +
	"p.ticker, p.quantity_micros, (SELECT COALESCE(SUM(o.filled_micros), 0) FROM orders o WHERE o.account_id = p.account_id AND o.position_id = p.position_id " +
	"AND o.platform = 'trading212' AND o.kind IN ('stop', 'close')) FROM agent_positions p "

func (r agentPositions) Add(ctx context.Context, p domain.AgentPosition) error {
	ok, err := accountExists(ctx, r.db, p.AccountID)
	if err != nil {
		return fmt.Errorf("agentPositions.Add: %w", err)
	}
	if !ok {
		return fmt.Errorf("agentPositions.Add: account %s: %w", p.AccountID, domain.ErrNotFound)
	}
	if len(p.Ticker) > maxTicker {
		return fmt.Errorf("agentPositions.Add: ticker: %w", domain.ErrInvalid)
	}
	_, err = r.db.ExecContext(ctx, "INSERT INTO agent_positions ("+agentPosCols+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		p.AccountID, p.PositionID, p.OpenRequestID, int64(p.Instrument), string(p.Direction), us(p.OpenedAt), usZ(p.ClosedAt), p.CloseRequestID,
		p.Ticker, int64(p.Quantity))
	if isDuplicate(err) {
		return fmt.Errorf("agentPositions.Add: position %d: %w", p.PositionID, domain.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("agentPositions.Add: %w", err)
	}
	return nil
}

func scanAgentPos(sc interface{ Scan(...any) error }) (domain.AgentPosition, error) {
	var p domain.AgentPosition
	var inst, opened, closed, qty, sold int64
	var dir string
	if err := sc.Scan(&p.AccountID, &p.PositionID, &p.OpenRequestID, &inst, &dir, &opened, &closed, &p.CloseRequestID, &p.Ticker, &qty, &sold); err != nil {
		return domain.AgentPosition{}, err
	}
	p.Quantity, p.Sold = domain.Micros(qty), domain.Micros(sold)
	p.Instrument, p.Direction, p.OpenedAt, p.ClosedAt = domain.InstrumentID(inst), domain.Direction(dir), fromUS(opened), fromUSZ(closed)
	return p, nil
}

func (r agentPositions) Get(ctx context.Context, accountID string, positionID int64) (domain.AgentPosition, error) {
	p, err := scanAgentPos(r.db.QueryRowContext(ctx, agentPosSelect+"WHERE p.account_id = ? AND p.position_id = ?", accountID, positionID))
	if err != nil {
		return domain.AgentPosition{}, fmt.Errorf("agentPositions.Get: %w", notFound(err))
	}
	return p, nil
}

func (r agentPositions) Update(ctx context.Context, p domain.AgentPosition) error {
	err := mustAffect(r.db.ExecContext(ctx, "UPDATE agent_positions SET closed_at = ?, close_request_id = ? WHERE account_id = ? AND position_id = ?",
		usZ(p.ClosedAt), p.CloseRequestID, p.AccountID, p.PositionID))
	if err != nil {
		return fmt.Errorf("agentPositions.Update: %w", err)
	}
	return nil
}

// SetCloseRequest stores only the close order of a position (never its closed time).
func (r agentPositions) SetCloseRequest(ctx context.Context, accountID string, positionID int64, requestID string) error {
	err := mustAffect(r.db.ExecContext(ctx, "UPDATE agent_positions SET close_request_id = ? WHERE account_id = ? AND position_id = ?",
		requestID, accountID, positionID))
	if err != nil {
		return fmt.Errorf("agentPositions.SetCloseRequest: %w", err)
	}
	return nil
}

func (r agentPositions) ListOpen(ctx context.Context, accountID string) ([]domain.AgentPosition, error) {
	return r.list(ctx, "ListOpen", agentPosSelect+"WHERE p.account_id = ? AND p.closed_at = 0 ORDER BY p.opened_at, p.position_id", accountID)
}

// ListSince returns the account's positions opened at or after since, oldest first.
func (r agentPositions) ListSince(ctx context.Context, accountID string, since time.Time) ([]domain.AgentPosition, error) {
	return r.list(ctx, "ListSince", agentPosSelect+"WHERE p.account_id = ? AND p.opened_at >= ? ORDER BY p.opened_at, p.position_id",
		accountID, us(since))
}

func (r agentPositions) list(ctx context.Context, op, q string, args ...any) ([]domain.AgentPosition, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("agentPositions.%s: %w", op, err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.AgentPosition
	for rows.Next() {
		p, err := scanAgentPos(rows)
		if err != nil {
			return nil, fmt.Errorf("agentPositions.%s: %w", op, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agentPositions.%s: %w", op, err)
	}
	return out, nil
}

// ---- risk state ------------------------------------------------------------------------

type riskStates struct{ db *sql.DB }

func (r riskStates) Get(ctx context.Context, accountID string) (domain.AccountRiskState, error) {
	var s domain.AccountRiskState
	var day string
	var sod, hwm, stopped, killed, killedAt, updated int64
	err := r.db.QueryRowContext(ctx, "SELECT account_id, risk_day, start_of_day_equity, high_water_mark, day_stopped, killed, kill_reason, killed_at, version, updated_at "+
		"FROM risk_state WHERE account_id = ?", accountID).Scan(&s.AccountID, &day, &sod, &hwm, &stopped, &killed, &s.KillReason, &killedAt, &s.Version, &updated)
	if err != nil {
		return domain.AccountRiskState{}, fmt.Errorf("riskStates.Get: %w", notFound(err))
	}
	s.State = domain.RiskState{Day: domain.RiskDate(day), StartOfDayEquity: domain.Cents(sod), HighWaterMark: domain.Cents(hwm), DayStopped: stopped != 0, Killed: killed != 0}
	s.KilledAt, s.UpdatedAt = fromUSZ(killedAt), fromUS(updated)
	return s, nil
}

// Put inserts the first version or updates the stored one only if its version is still
// s.Version (compare-and-set): a stale writer gets ErrConflict and changes nothing.
func (r riskStates) Put(ctx context.Context, s domain.AccountRiskState) (domain.AccountRiskState, error) {
	st := s.State
	vals := []any{
		string(st.Day), int64(st.StartOfDayEquity), int64(st.HighWaterMark), b2i(st.DayStopped), b2i(st.Killed),
		clip(s.KillReason, maxReason), usZ(s.KilledAt), us(s.UpdatedAt),
	}
	if s.Version == 0 {
		ok, err := accountExists(ctx, r.db, s.AccountID)
		if err != nil {
			return domain.AccountRiskState{}, fmt.Errorf("riskStates.Put: %w", err)
		}
		if !ok {
			return domain.AccountRiskState{}, fmt.Errorf("riskStates.Put: account %s: %w", s.AccountID, domain.ErrNotFound)
		}
		_, err = r.db.ExecContext(ctx, "INSERT INTO risk_state (risk_day, start_of_day_equity, high_water_mark, day_stopped, killed, kill_reason, killed_at, updated_at, account_id, version) "+
			"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1)", append(vals, s.AccountID)...)
		if isDuplicate(err) {
			return domain.AccountRiskState{}, fmt.Errorf("riskStates.Put: stored by someone else: %w", domain.ErrConflict)
		}
		if err != nil {
			return domain.AccountRiskState{}, fmt.Errorf("riskStates.Put: %w", err)
		}
	} else {
		res, err := r.db.ExecContext(ctx, "UPDATE risk_state SET risk_day = ?, start_of_day_equity = ?, high_water_mark = ?, day_stopped = ?, killed = ?, "+
			"kill_reason = ?, killed_at = ?, updated_at = ?, version = version + 1 WHERE account_id = ? AND version = ?", append(vals, s.AccountID, s.Version)...)
		if err := mustAffect(res, err); errors.Is(err, domain.ErrNotFound) {
			return domain.AccountRiskState{}, fmt.Errorf("riskStates.Put: version %d is stale: %w", s.Version, domain.ErrConflict)
		} else if err != nil {
			return domain.AccountRiskState{}, fmt.Errorf("riskStates.Put: %w", err)
		}
	}
	s.Version++
	s.State.Reserved, s.State.ReservedOrders, s.State.ReservedShort = 0, 0, 0
	return s, nil
}

// ---- instrument blocks -----------------------------------------------------------------

type instrumentBlocks struct{ db *sql.DB }

func (r instrumentBlocks) Block(ctx context.Context, b domain.InstrumentBlock) error {
	ok, err := accountExists(ctx, r.db, b.AccountID)
	if err != nil {
		return fmt.Errorf("instrumentBlocks.Block: %w", err)
	}
	if !ok {
		return fmt.Errorf("instrumentBlocks.Block: account %s: %w", b.AccountID, domain.ErrNotFound)
	}
	reason := clip(b.Reason, maxReason)
	err = upsert(ctx, r.db,
		"UPDATE instrument_blocks SET reason = ?, at = ? WHERE account_id = ? AND instrument_id = ?", []any{reason, us(b.At), b.AccountID, int64(b.Instrument)},
		"INSERT INTO instrument_blocks (account_id, instrument_id, reason, at) VALUES (?, ?, ?, ?)", []any{b.AccountID, int64(b.Instrument), reason, us(b.At)})
	if err != nil {
		return fmt.Errorf("instrumentBlocks.Block: %w", err)
	}
	return nil
}

func (r instrumentBlocks) Unblock(ctx context.Context, accountID string, id domain.InstrumentID) error {
	if err := mustAffect(r.db.ExecContext(ctx, "DELETE FROM instrument_blocks WHERE account_id = ? AND instrument_id = ?", accountID, int64(id))); err != nil {
		return fmt.Errorf("instrumentBlocks.Unblock: %w", err)
	}
	return nil
}

func (r instrumentBlocks) List(ctx context.Context, accountID string) ([]domain.InstrumentBlock, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT account_id, instrument_id, reason, at FROM instrument_blocks WHERE account_id = ? ORDER BY instrument_id", accountID)
	if err != nil {
		return nil, fmt.Errorf("instrumentBlocks.List: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []domain.InstrumentBlock
	for rows.Next() {
		var b domain.InstrumentBlock
		var id, at int64
		if err := rows.Scan(&b.AccountID, &id, &b.Reason, &at); err != nil {
			return nil, fmt.Errorf("instrumentBlocks.List: %w", err)
		}
		b.Instrument, b.At = domain.InstrumentID(id), fromUS(at)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("instrumentBlocks.List: %w", err)
	}
	return out, nil
}

// ---- agent leases ----------------------------------------------------------------------

type agentLeases struct{ db *sql.DB }

// Acquire renews the holder's lease or takes over an expired one with one conditional UPDATE
// (atomic in both databases); when there is no row it INSERTs, and the primary key lets
// exactly one of several concurrent holders win. Each statement commits on its own, so no
// transaction holds a gap lock between them (MariaDB).
func (r agentLeases) Acquire(ctx context.Context, accountID, holder string, now, until time.Time) error {
	res, err := r.db.ExecContext(ctx, "UPDATE agent_leases SET holder = ?, expires_at = ? WHERE account_id = ? AND (holder = ? OR expires_at <= ?)",
		holder, us(until), accountID, holder, us(now))
	err = mustAffect(res, err)
	if err == nil {
		return nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return fmt.Errorf("agentLeases.Acquire: %w", err)
	}
	ok, err := accountExists(ctx, r.db, accountID)
	if err != nil {
		return fmt.Errorf("agentLeases.Acquire: %w", err)
	}
	if !ok {
		return fmt.Errorf("agentLeases.Acquire: account %s: %w", accountID, domain.ErrNotFound)
	}
	_, err = r.db.ExecContext(ctx, "INSERT INTO agent_leases (account_id, holder, expires_at) VALUES (?, ?, ?)", accountID, holder, us(until))
	if isDuplicate(err) {
		return fmt.Errorf("agentLeases.Acquire: held by another process: %w", domain.ErrConflict)
	}
	if err != nil {
		return fmt.Errorf("agentLeases.Acquire: %w", err)
	}
	return nil
}

func (r agentLeases) Release(ctx context.Context, accountID, holder string) error {
	if _, err := r.db.ExecContext(ctx, "DELETE FROM agent_leases WHERE account_id = ? AND holder = ?", accountID, holder); err != nil {
		return fmt.Errorf("agentLeases.Release: %w", err)
	}
	return nil
}

func (r agentLeases) Get(ctx context.Context, accountID string) (domain.AgentLease, error) {
	var l domain.AgentLease
	var exp int64
	err := r.db.QueryRowContext(ctx, "SELECT account_id, holder, expires_at FROM agent_leases WHERE account_id = ?", accountID).Scan(&l.AccountID, &l.Holder, &exp)
	if err != nil {
		return domain.AgentLease{}, fmt.Errorf("agentLeases.Get: %w", notFound(err))
	}
	l.ExpiresAt = fromUS(exp)
	return l, nil
}
