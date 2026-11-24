package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Trade history (PLAN 6.5): eToro's closed trades are read into the trades table (read-only
// for the broker: only the trade-history endpoint is called), and viewers read the history,
// daily figures, a summary and CSV exports from the database. Nothing here is used by an
// order path; the figures describe past trades only.

// History bounds.
const (
	// historyBackfill is how far back the first sync of an account reads (OD18: 90 days, to
	// save the default 60/min quota); later syncs start historyOverlap before the latest
	// stored close, so late or corrected trades are read again.
	historyBackfill = 90 * 24 * time.Hour
	historyOverlap  = 2 * 24 * time.Hour
	// historySyncTimeout bounds one account's sync in SyncTradesAll.
	historySyncTimeout = 2 * time.Minute

	tradesDefaultLimit = 100
	tradesMaxLimit     = 500
	historyDefaultDays = 30
	historyMaxDays     = 400
)

// TradeSync is the outcome of one account's trade sync.
type TradeSync struct {
	AccountID string
	Since     time.Time // the minDate asked from eToro
	// Read is how many trades eToro returned, Stored how many were written (Ours of them
	// opened by cactusTrade), Skipped how many were unusable (no position id or close time).
	Read, Stored, Ours, Skipped int
	Err                         error // set by SyncTradesAll when the sync failed
}

// SyncTrades reads an eToro account's closed trades from eToro and stores them (system; the
// history loop). It runs for demo and real accounts alike and only reads. The first sync
// reads historyBackfill back; later ones from historyOverlap before the latest stored close.
// A trade is ours when the position is one of the account's agent positions (OD1), with the
// request id of the order that opened it. A broker error changes nothing and is returned.
func (s *Service) SyncTrades(ctx context.Context, accountID string) (TradeSync, error) {
	a, err := s.d.Store.Accounts().Get(ctx, accountID)
	if err != nil {
		return TradeSync{}, fmt.Errorf("app.SyncTrades: %w", err)
	}
	out := TradeSync{AccountID: a.ID}
	switch {
	case !a.Tradable():
		return out, fmt.Errorf("app.SyncTrades: %s: %w", a.Name, domain.ErrViewOnly)
	case s.d.Broker == nil:
		return out, fmt.Errorf("app.SyncTrades: the eToro client is not configured: %w", domain.ErrUnavailable)
	case !s.hasKeys(ctx, a):
		return out, fmt.Errorf("app.SyncTrades: %s: %w: %w", a.Name, domain.ErrCredentialsMissing, domain.ErrUnavailable)
	}
	last, err := s.d.Store.Trades().LastClosedAt(ctx, a.ID)
	if err != nil {
		return out, fmt.Errorf("app.SyncTrades: %w", err)
	}
	now := s.d.Clock.Now().UTC()
	out.Since = now.Add(-historyBackfill)
	if !last.IsZero() {
		out.Since = last.Add(-historyOverlap)
	}
	trades, err := s.d.Broker.TradeHistory(ports.WithAccount(ctx, a), out.Since)
	if err != nil {
		return out, fmt.Errorf("app.SyncTrades: %s: %w", a.Name, err)
	}
	out.Read = len(trades)
	for _, ct := range trades {
		t := domain.Trade{AccountID: a.ID, Mode: a.Mode, ClosedTrade: ct, SyncedAt: now}
		switch ap, err := s.d.Store.AgentPositions().Get(ctx, a.ID, ct.PositionID); {
		case err == nil:
			t.Ours, t.OpenRequestID = true, ap.OpenRequestID
		case !errors.Is(err, domain.ErrNotFound):
			return out, fmt.Errorf("app.SyncTrades: %w", err) // never store a trade with a guessed owner
		}
		if !t.FeesKnown {
			t.Fees = 0
		}
		if err := t.Validate(); err != nil {
			out.Skipped++
			s.d.Log.WarnContext(ctx, "trade history: unusable trade skipped", "account", a.ID, "position", ct.PositionID, "err", err)
			continue
		}
		if err := s.d.Store.Trades().Upsert(ctx, t); err != nil {
			return out, fmt.Errorf("app.SyncTrades: %w", err)
		}
		out.Stored++
		if t.Ours {
			out.Ours++
		}
	}
	return out, nil
}

// SyncTradesAll syncs every eToro account that has keys, demo and real (system, read-only),
// one after the other (they share eToro's default quota), each bounded by
// historySyncTimeout. A failing account is logged and reported in its TradeSync; the error is
// only for a store failure listing the accounts or a cancelled ctx (shutdown).
func (s *Service) SyncTradesAll(ctx context.Context) ([]TradeSync, error) {
	as, err := s.d.Store.Accounts().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("app.SyncTradesAll: %w", err)
	}
	var out []TradeSync
	for _, a := range as {
		if err := ctx.Err(); err != nil {
			return out, fmt.Errorf("app.SyncTradesAll: %w", err)
		}
		if !a.Tradable() || !s.hasKeys(ctx, a) {
			continue
		}
		out = append(out, s.syncBounded(ctx, a.ID))
	}
	return out, nil
}

// syncBounded syncs one account within historySyncTimeout; a failure is logged and carried
// in the result.
func (s *Service) syncBounded(ctx context.Context, accountID string) TradeSync {
	actx, cancel := context.WithTimeout(ctx, historySyncTimeout)
	defer cancel()
	r, err := s.SyncTrades(actx, accountID)
	r.AccountID, r.Err = accountID, err
	if err != nil {
		s.d.Log.WarnContext(ctx, "trade history sync failed", "account", accountID, "err", err)
	}
	return r
}

// TradeQuery filters an account's trade history by close time: From ≤ closed < To (zero =
// no bound), Before for paging (exclusive), Ours (nil = every trade, OD15), Limit (0 = 100;
// 1–500).
type TradeQuery struct {
	From, To time.Time
	Before   time.Time
	Ours     *bool
	Limit    int
}

// TradeView is a stored trade with its share's symbol ("" when the instrument is not cached).
type TradeView struct {
	domain.Trade
	Symbol string
}

// Trades returns an account's closed trades, newest first (viewer; accountID "" = the
// current account). Positions cactusTrade did not open are listed too, with Ours false.
func (s *Service) Trades(ctx context.Context, p Principal, accountID string, q TradeQuery) ([]TradeView, error) {
	v, err := s.historyAccount(ctx, p, accountID)
	if err != nil {
		return nil, err
	}
	if q.Limit == 0 {
		q.Limit = tradesDefaultLimit
	}
	if q.Limit < 1 || q.Limit > tradesMaxLimit {
		return nil, domain.Invalid("limit", "must be 1–500")
	}
	if err := validRange(q.From, q.To); err != nil {
		return nil, err
	}
	ts, err := s.d.Store.Trades().List(ctx, v.ID, ports.TradeFilter{From: q.From, To: q.To, Before: q.Before, Ours: q.Ours, Limit: q.Limit})
	if err != nil {
		return nil, fmt.Errorf("app.Trades: %w", err)
	}
	names := s.symbols(ctx)
	out := make([]TradeView, 0, len(ts))
	for _, t := range ts {
		out = append(out, TradeView{Trade: t, Symbol: names(t.Instrument)})
	}
	return out, nil
}

// DailyHistory is an account's figures per risk day over a period.
type DailyHistory struct {
	Account domain.AccountView
	Days    int
	From    time.Time // the start of the first risk day
	To      time.Time // now
	Rows    []domain.DayPnL
}

// DailyPnL returns an account's last days risk days (0 = 30; 1–400), today included:
// realised P&L and fees of cactusTrade's closed trades (OD15) and the account's equity
// change, which includes deposits, withdrawals and every other position (viewer).
func (s *Service) DailyPnL(ctx context.Context, p Principal, accountID string, days int) (DailyHistory, error) {
	v, from, to, days, err := s.historyPeriod(ctx, p, accountID, days)
	if err != nil {
		return DailyHistory{}, err
	}
	ours := true
	trades, err := s.closedTrades(ctx, v.ID, from, to, &ours)
	if err != nil {
		return DailyHistory{}, err
	}
	snaps, err := s.snapshots(ctx, v.ID, from, to)
	if err != nil {
		return DailyHistory{}, err
	}
	return DailyHistory{Account: v, Days: days, From: from, To: to, Rows: domain.DailyPnL(trades, snaps)}, nil
}

// HistorySummary is the summary of an account's history over a period.
type HistorySummary struct {
	Account domain.AccountView
	Days    int
	From    time.Time
	To      time.Time
	// Stats are over cactusTrade's own closed trades (OD15); ForeignTrades counts the trades
	// of positions it did not open in the same period (listed, not counted).
	Stats         domain.TradeSummary
	ForeignTrades int
	// Drawdown is over the account's equity snapshots in the period (the whole account).
	Drawdown domain.DrawdownFigures
}

// HistorySummary sums an account's closed trades and its equity drawdown over the last days
// risk days (0 = 30; 1–400) (viewer).
func (s *Service) HistorySummary(ctx context.Context, p Principal, accountID string, days int) (HistorySummary, error) {
	v, from, to, days, err := s.historyPeriod(ctx, p, accountID, days)
	if err != nil {
		return HistorySummary{}, err
	}
	ours, foreign := true, false
	trades, err := s.closedTrades(ctx, v.ID, from, to, &ours)
	if err != nil {
		return HistorySummary{}, err
	}
	others, err := s.closedTrades(ctx, v.ID, from, to, &foreign)
	if err != nil {
		return HistorySummary{}, err
	}
	snaps, err := s.snapshots(ctx, v.ID, from, to)
	if err != nil {
		return HistorySummary{}, err
	}
	return HistorySummary{
		Account: v, Days: days, From: from, To: to, Stats: domain.TradeStats(trades), ForeignTrades: len(others),
		Drawdown: domain.Drawdown(snaps),
	}, nil
}

// ExportKind names a history export.
type ExportKind string

// Export kinds.
const (
	ExportTrades    ExportKind = "trades"
	ExportDaily     ExportKind = "daily"
	ExportEquity    ExportKind = "equity"
	ExportOrders    ExportKind = "orders"
	ExportDecisions ExportKind = "decisions"
)

// Export is a table for a CSV download. Each cell is one of: string, int, int64, bool,
// domain.Cents (USD, written as a decimal), domain.Micros (a decimal), domain.BasisPoints,
// time.Time (UTC RFC 3339; zero = empty) or nil (empty). The HTTP adapter formats them and
// neutralises formula cells (PLAN 6.7).
type Export struct {
	Kind    ExportKind
	Account domain.AccountView
	From    time.Time // zero = the account's whole history
	To      time.Time
	Columns []string
	Rows    [][]any
}

// unknownFees is the fees cell of a trade whose fees eToro did not report.
const unknownFees = "unknown"

// ExportRows returns an account's history of one kind between from (zero = the beginning)
// and to (zero = now) as rows for a CSV file (viewer; at most domain.ExportMaxRows rows, else
// domain.ErrTooLarge). Trades include positions cactusTrade did not open (column ours).
func (s *Service) ExportRows(ctx context.Context, p Principal, accountID string, kind ExportKind, from, to time.Time) (Export, error) {
	v, err := s.historyAccount(ctx, p, accountID)
	if err != nil {
		return Export{}, err
	}
	if to.IsZero() {
		to = s.d.Clock.Now().UTC()
	}
	if err := validRange(from, to); err != nil {
		return Export{}, err
	}
	out := Export{Kind: kind, Account: v, From: from, To: to}
	switch kind {
	case ExportTrades:
		err = s.exportTrades(ctx, &out)
	case ExportDaily:
		err = s.exportDaily(ctx, &out)
	case ExportEquity:
		err = s.exportEquity(ctx, &out)
	case ExportOrders:
		err = s.exportOrders(ctx, &out)
	case ExportDecisions:
		err = s.exportDecisions(ctx, &out)
	default:
		return Export{}, domain.Invalid("kind", "trades, daily, equity, orders or decisions")
	}
	if err != nil {
		return Export{}, err
	}
	return out, nil
}

func (s *Service) exportTrades(ctx context.Context, e *Export) error {
	ts, err := s.closedTradeRows(ctx, e.Account.ID, e.From, e.To, nil)
	if err != nil {
		return err
	}
	names := s.symbols(ctx)
	e.Columns = []string{
		"closed_at", "opened_at", "position_id", "instrument_id", "symbol", "direction", "leverage", "units", "invested_usd",
		"open_rate", "close_rate", "stop_loss_rate", "take_profit_rate", "net_profit_usd", "fees_usd", "ours", "open_request_id",
		"etoro_order_id", "mode",
	}
	for _, t := range ts {
		var fees any = unknownFees
		if t.FeesKnown {
			fees = t.Fees
		}
		e.Rows = append(e.Rows, []any{
			t.ClosedAt, t.OpenedAt, t.PositionID, int64(t.Instrument), names(t.Instrument), string(t.Direction()), t.Leverage, t.Units,
			t.Investment, t.OpenRate, t.CloseRate, t.StopLoss, t.TakeProfit, t.NetProfit, fees, t.Ours, t.OpenRequestID,
			t.OrderID, string(t.Mode),
		})
	}
	return nil
}

func (s *Service) exportDaily(ctx context.Context, e *Export) error {
	ours := true
	trades, err := s.closedTrades(ctx, e.Account.ID, e.From, e.To, &ours)
	if err != nil {
		return err
	}
	snaps, err := s.snapshots(ctx, e.Account.ID, e.From, e.To)
	if err != nil {
		return err
	}
	e.Columns = []string{
		"risk_day", "trades", "realised_usd", "fees_usd", "fees_unknown_trades", "equity_end_usd",
		"equity_change_usd_incl_deposits_withdrawals",
	}
	for _, d := range domain.DailyPnL(trades, snaps) {
		var end, change any
		if d.EquityKnown {
			end, change = d.EquityEnd, d.EquityChange
		}
		e.Rows = append(e.Rows, []any{string(d.Day), d.Trades, d.Realised, d.Fees, d.FeesUnknown, end, change})
	}
	return tooMany(len(e.Rows))
}

func (s *Service) exportEquity(ctx context.Context, e *Export) error {
	snaps, err := s.snapshots(ctx, e.Account.ID, e.From, e.To)
	if err != nil {
		return err
	}
	if err := tooMany(len(snaps)); err != nil {
		return err
	}
	e.Columns = []string{"at", "currency", "cash", "equity"}
	for _, sn := range snaps {
		e.Rows = append(e.Rows, []any{sn.At, sn.Currency, sn.Cash, sn.Equity})
	}
	return nil
}

func (s *Service) exportOrders(ctx context.Context, e *Export) error {
	// Orders are listed newest first without a time bound: read one more than the cap and
	// refuse when the range may reach past what was read (never a silently short file).
	orders, err := s.d.Store.Orders().List(ctx, e.Account.ID, domain.ExportMaxRows+1)
	if err != nil {
		return fmt.Errorf("app.ExportRows: %w", err)
	}
	if len(orders) > domain.ExportMaxRows && !orders[len(orders)-1].CreatedAt.Before(e.From) {
		return fmt.Errorf("app.ExportRows: orders: %w", domain.ErrTooLarge)
	}
	e.Columns = []string{
		"created_at", "request_id", "kind", "direction", "instrument_id", "amount_usd", "entry_rate", "stop_loss_rate",
		"take_profit_rate", "state", "reason", "sends", "position_id", "etoro_order_id", "sent_at", "updated_at", "mode",
	}
	for i := len(orders) - 1; i >= 0; i-- { // oldest first, like every export
		o := orders[i]
		if o.CreatedAt.Before(e.From) || !o.CreatedAt.Before(e.To) {
			continue
		}
		e.Rows = append(e.Rows, []any{
			o.CreatedAt, o.RequestID, string(o.Kind), string(o.Direction), int64(o.Instrument), o.Amount, o.Entry, o.StopLoss,
			o.TakeProfit, string(o.State), o.Reason, o.Sends, o.PositionID, o.EtoroOrderID, o.SentAt, o.UpdatedAt, string(o.Mode),
		})
	}
	return tooMany(len(e.Rows))
}

func (s *Service) exportDecisions(ctx context.Context, e *Export) error {
	ds, err := s.d.Store.Decisions().List(ctx, e.Account.ID, e.To, "", domain.ExportMaxRows+1)
	if err != nil {
		return fmt.Errorf("app.ExportRows: %w", err)
	}
	if len(ds) > domain.ExportMaxRows && !ds[len(ds)-1].At.Before(e.From) {
		return fmt.Errorf("app.ExportRows: decisions: %w", domain.ErrTooLarge)
	}
	e.Columns = []string{
		"at", "id", "instrument_id", "symbol", "direction", "playbook", "entry_rate", "stop_loss_rate", "take_profit_rate",
		"suggested_usd", "verdict", "verdict_size_bp", "verdict_reason", "risk_allowed", "risk_size_usd", "risk_reason",
		"outcome", "order_request_id",
	}
	for i := len(ds) - 1; i >= 0; i-- {
		d := ds[i]
		if d.At.Before(e.From) {
			continue
		}
		g := d.Signal
		e.Rows = append(e.Rows, []any{
			d.At, d.ID, int64(g.Instrument), g.Symbol, string(g.Direction), g.Playbook, g.Entry, g.StopLoss, g.TakeProfit,
			g.SuggestedSize, string(d.Verdict.Kind), d.Verdict.SizeBp, d.Verdict.Reason, d.Risk.Allowed, d.Risk.Size, d.Risk.Reason,
			string(d.Outcome), d.OrderRequestID,
		})
	}
	return tooMany(len(e.Rows))
}

// historyAccount resolves an eToro account the caller may view for its history: view
// permission and view access; a view-only platform is refused (ErrViewOnly), another
// account is not found.
func (s *Service) historyAccount(ctx context.Context, p Principal, accountID string) (domain.AccountView, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return domain.AccountView{}, err
	}
	return s.account(ctx, p, accountID, domain.AccessView)
}

// historyPeriod resolves the account and the period of the last days risk days.
func (s *Service) historyPeriod(ctx context.Context, p Principal, accountID string, days int) (domain.AccountView, time.Time, time.Time, int, error) {
	v, err := s.historyAccount(ctx, p, accountID)
	if err != nil {
		return domain.AccountView{}, time.Time{}, time.Time{}, 0, err
	}
	if days == 0 {
		days = historyDefaultDays
	}
	if days < 1 || days > historyMaxDays {
		return domain.AccountView{}, time.Time{}, time.Time{}, 0, domain.Invalid("days", "must be 1–400")
	}
	now := s.d.Clock.Now().UTC()
	from := domain.RiskDayStart(now).Add(-time.Duration(days-1) * 24 * time.Hour)
	return v, from, now, days, nil
}

func validRange(from, to time.Time) error {
	if !from.IsZero() && !to.IsZero() && !from.Before(to) {
		return domain.Invalid("from", "must be before to")
	}
	return nil
}

func tooMany(n int) error {
	if n > domain.ExportMaxRows {
		return fmt.Errorf("app.ExportRows: %d rows: %w", n, domain.ErrTooLarge)
	}
	return nil
}

// closedTradeRows reads an account's trades closed in [from, to) (ours nil = all), at most
// domain.ExportMaxRows (else ErrTooLarge), oldest first.
func (s *Service) closedTradeRows(ctx context.Context, accountID string, from, to time.Time, ours *bool) ([]domain.Trade, error) {
	ts, err := s.d.Store.Trades().List(ctx, accountID, ports.TradeFilter{From: from, To: to, Ours: ours, Limit: domain.ExportMaxRows + 1})
	if err != nil {
		return nil, fmt.Errorf("app.history: %w", err)
	}
	if err := tooMany(len(ts)); err != nil {
		return nil, err
	}
	for i, j := 0, len(ts)-1; i < j; i, j = i+1, j-1 {
		ts[i], ts[j] = ts[j], ts[i]
	}
	return ts, nil
}

// closedTrades is closedTradeRows as eToro's records.
func (s *Service) closedTrades(ctx context.Context, accountID string, from, to time.Time, ours *bool) ([]domain.ClosedTrade, error) {
	ts, err := s.closedTradeRows(ctx, accountID, from, to, ours)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ClosedTrade, len(ts))
	for i, t := range ts {
		out[i] = t.ClosedTrade
	}
	return out, nil
}

// snapshots reads an account's equity snapshots taken in [from, to), oldest first.
func (s *Service) snapshots(ctx context.Context, accountID string, from, to time.Time) ([]domain.EquitySnapshot, error) {
	all, err := s.d.Store.EquitySnapshots().List(ctx, []string{accountID}, from)
	if err != nil {
		return nil, fmt.Errorf("app.history: %w", err)
	}
	out := all[:0]
	for _, sn := range all {
		if sn.AccountID == accountID && sn.At.Before(to) {
			out = append(out, sn)
		}
	}
	return out, nil
}

// symbols names instruments from the instrument cache, remembering each answer for the
// call ("" when not cached).
func (s *Service) symbols(ctx context.Context) func(domain.InstrumentID) string {
	seen := map[domain.InstrumentID]string{}
	return func(id domain.InstrumentID) string {
		if sym, ok := seen[id]; ok {
			return sym
		}
		sym := ""
		if in, _, err := s.d.Store.Instruments().Get(ctx, id); err == nil {
			sym = in.Symbol
		}
		seen[id] = sym
		return sym
	}
}
