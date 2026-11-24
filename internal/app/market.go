package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// BrokerCheck is the result of "test connection": one read-only portfolio call.
type BrokerCheck struct {
	Mode           domain.TradingMode
	OK             bool
	Error          string // eToro's answer when not OK (never contains a key)
	Latency        time.Duration
	AvailableCash  domain.Cents
	Equity         domain.Cents
	Positions      int
	NonConforming  int // positions that are not real shares, long, x1 (closed in phase 4)
	CheckedAt      time.Time
	ConfiguredKeys bool
}

func (s *Service) needBroker() error {
	if s.d.Broker == nil || s.d.Market == nil {
		return fmt.Errorf("the eToro client is not configured: %w", domain.ErrUnavailable)
	}
	return nil
}

// TestConnection calls eToro's portfolio endpoint with an account's keys (owner; "" = the
// current account). A broker failure is a result (OK=false with eToro's reason), not an
// error.
func (s *Service) TestConnection(ctx context.Context, p Principal, accountID string) (BrokerCheck, error) {
	v, err := s.account(ctx, p, accountID, domain.AccessOwner)
	if err != nil {
		return BrokerCheck{}, err
	}
	if err := s.needBroker(); err != nil {
		return BrokerCheck{}, err
	}
	mode := accountMode(v.Account)
	start := s.d.Clock.Now()
	pf, err := s.d.Broker.Portfolio(brokerCtx(ctx, v))
	res := BrokerCheck{Mode: mode, Latency: s.d.Clock.Now().Sub(start), CheckedAt: start.UTC(), ConfiguredKeys: true}
	if err != nil {
		if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, domain.ErrNotFound) {
			return BrokerCheck{}, fmt.Errorf("app.TestConnection: %w", err)
		}
		res.Error = err.Error()
		res.ConfiguredKeys = !errors.Is(err, domain.ErrCredentialsMissing)
		s.audit(ctx, p, "broker.test_connection", v.ID, "failed")
		return res, nil
	}
	res.OK, res.AvailableCash, res.Equity, res.Positions = true, pf.AvailableCash, pf.Equity(), len(pf.Positions)
	for _, pos := range pf.Positions {
		if !pos.Conforms() {
			res.NonConforming++
		}
	}
	s.audit(ctx, p, "broker.test_connection", v.ID, "ok")
	return res, nil
}

// Portfolio is the current account as eToro reports it now (view).
func (s *Service) Portfolio(ctx context.Context, p Principal) (domain.Portfolio, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return domain.Portfolio{}, err
	}
	if err := s.needBroker(); err != nil {
		return domain.Portfolio{}, err
	}
	pf, err := s.d.Broker.Portfolio(brokerCtx(ctx, v))
	if err != nil {
		return domain.Portfolio{}, fmt.Errorf("app.Portfolio: %w", err)
	}
	return pf, nil
}

// MarketSession is one market's trading day as the calendar sees it.
type MarketSession struct {
	Market  domain.Market
	Trading bool // the market trades today (local date)
	Open    bool // in session right now
	Opens   time.Time
	Closes  time.Time
	Note    string // e.g. "no holiday table for 2028" (closed, fail closed)
}

// MarketStatus reports today's sessions for the supported markets (viewer).
func (s *Service) MarketStatus(ctx context.Context, p Principal) ([]MarketSession, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return nil, err
	}
	if s.d.Calendar == nil {
		return nil, fmt.Errorf("the market calendar is not configured: %w", domain.ErrUnavailable)
	}
	now := s.d.Clock.Now()
	var out []MarketSession
	for _, m := range domain.Markets {
		open, closeAt, trading, err := s.d.Calendar.Session(m, now)
		ms := MarketSession{Market: m, Trading: trading}
		if err != nil {
			ms.Note = err.Error()
			s.d.Log.WarnContext(ctx, "market calendar", "market", string(m), "err", err)
		}
		if trading {
			ms.Opens, ms.Closes = open.UTC(), closeAt.UTC()
			ms.Open = !now.Before(open) && now.Before(closeAt)
		}
		out = append(out, ms)
	}
	return out, nil
}

// SearchInstruments searches eToro with the current account's keys (trade access: it is how
// the watchlist is built, and it spends that key's market-data quota).
func (s *Service) SearchInstruments(ctx context.Context, p Principal, query string) ([]domain.Instrument, error) {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return nil, err
	}
	if err := s.needBroker(); err != nil {
		return nil, err
	}
	res, err := s.d.Market.Search(brokerCtx(ctx, v), query)
	if err != nil {
		return nil, fmt.Errorf("app.SearchInstruments: %w", err)
	}
	return res, nil
}

// Watchlist lists the instruments the current account's agent may trade (view).
func (s *Service) Watchlist(ctx context.Context, p Principal) ([]domain.WatchItem, error) {
	v, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return nil, err
	}
	items, err := s.d.Store.Watchlist().List(ctx, v.ID)
	if err != nil {
		return nil, fmt.Errorf("app.Watchlist: %w", err)
	}
	return items, nil
}

// WatchlistCount is how many shares an account's watchlist holds.
type WatchlistCount struct {
	Account domain.AccountView
	Count   int
	// Current: the caller's current account.
	Current bool
}

// WatchlistCounts gives the watchlist size of every eToro account the caller can view (R25),
// so an empty watchlist page can name the accounts that do have shares. Accounts the caller
// cannot see are never counted.
func (s *Service) WatchlistCounts(ctx context.Context, p Principal) ([]WatchlistCount, error) {
	vs, err := s.Accounts(ctx, p)
	if err != nil {
		return nil, err
	}
	cur := s.currentAccountID(ctx, p, vs)
	out := make([]WatchlistCount, 0, len(vs))
	for _, v := range vs {
		if !v.Tradable() {
			continue
		}
		items, err := s.d.Store.Watchlist().List(ctx, v.ID)
		if err != nil {
			return nil, fmt.Errorf("app.WatchlistCounts: %w", err)
		}
		out = append(out, WatchlistCount{Account: v, Count: len(items), Current: v.ID == cur})
	}
	return out, nil
}

// AddToWatchlist adds a share to the current account's watchlist after checking with eToro
// that it is a share on LSE, NYSE/NASDAQ or the ASX and can be bought as a real share, long, at
// leverage 1 (trade access). Anything else is refused with eToro's reason (fail closed).
func (s *Service) AddToWatchlist(ctx context.Context, p Principal, id domain.InstrumentID) (domain.WatchItem, error) {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return domain.WatchItem{}, err
	}
	return s.addToWatchlist(ctx, v, p.Username, id)
}

// addToWatchlist is shared by the UI and the import: the eligibility path, then an audit
// entry and a "watchlist.added" event.
func (s *Service) addToWatchlist(ctx context.Context, v domain.AccountView, actor string, id domain.InstrumentID) (domain.WatchItem, error) {
	w, err := s.checkAndAddToWatchlist(ctx, v, actor, id)
	if err != nil {
		return domain.WatchItem{}, err
	}
	s.audit(ctx, Principal{Username: actor}, "watchlist.add", v.ID+"/"+strconv.FormatInt(int64(id), 10), w.Instrument.Symbol)
	s.emitFor(ctx, v.ID, actor, "watchlist.added", domain.SeverityInfo, w.Instrument.Symbol+" added to the watchlist of "+v.Name, w.Instrument.Name+" ("+string(w.Instrument.Market)+")")
	return w, nil
}

// checkAndAddToWatchlist is the one eligibility path every share takes onto a watchlist (by
// hand, import, web research): eToro's instrument, a share on LSE, NYSE/NASDAQ or the ASX,
// eToro's eligibility at leverage 1 (cached either way), then the watchlist row. It neither
// audits nor emits; its callers do.
func (s *Service) checkAndAddToWatchlist(ctx context.Context, v domain.AccountView, actor string, id domain.InstrumentID) (domain.WatchItem, error) {
	if err := s.needBroker(); err != nil {
		return domain.WatchItem{}, err
	}
	bctx := brokerCtx(ctx, v)
	if id <= 0 {
		return domain.WatchItem{}, domain.Invalid("instrumentId", "must be positive")
	}
	ins, err := s.d.Market.Instruments(bctx, []domain.InstrumentID{id})
	if err != nil {
		return domain.WatchItem{}, fmt.Errorf("app.AddToWatchlist: %w", err)
	}
	idx := slices.IndexFunc(ins, func(in domain.Instrument) bool { return in.ID == id })
	if idx < 0 {
		return domain.WatchItem{}, domain.Invalid("instrumentId", "eToro does not know this instrument")
	}
	in := ins[idx]
	if !domain.IsShare(in.Type) || in.Market == "" {
		return domain.WatchItem{}, domain.Invalid("instrumentId", "only shares listed in London (LSE), New York (NYSE/NASDAQ) or Sydney (ASX) can be traded")
	}
	els, err := s.d.Broker.Eligibility(bctx, []domain.InstrumentID{id})
	if err != nil {
		return domain.WatchItem{}, fmt.Errorf("app.AddToWatchlist: %w", err)
	}
	el := domain.Eligibility{Instrument: id, Reason: "eToro gave no eligibility answer"}
	if i := slices.IndexFunc(els, func(e domain.Eligibility) bool { return e.Instrument == id }); i >= 0 {
		el = els[i]
	}
	now := s.d.Clock.Now().UTC()
	if el.CheckedAt.IsZero() {
		el.CheckedAt = now
	}
	// Cache the answer either way, so the UI can show why an instrument was refused.
	if err := s.d.Store.Instruments().Upsert(ctx, in, el, now); err != nil {
		return domain.WatchItem{}, fmt.Errorf("app.AddToWatchlist: %w", err)
	}
	if !el.RealLong && !el.ShortCFD {
		return domain.WatchItem{}, domain.Invalid("instrumentId", "not tradable at leverage 1, long ("+el.Reason+") or short ("+el.ShortReason+")")
	}
	if err := s.d.Store.Watchlist().Add(ctx, v.ID, id, actor, now); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return domain.WatchItem{}, fmt.Errorf("%s is already on the watchlist: %w", in.Symbol, domain.ErrConflict)
		}
		return domain.WatchItem{}, fmt.Errorf("app.AddToWatchlist: %w", err)
	}
	return domain.WatchItem{Instrument: in, Eligibility: el, AddedBy: actor, AddedAt: now}, nil
}

// RemoveFromWatchlist removes an instrument from the current account's list (trade access).
func (s *Service) RemoveFromWatchlist(ctx context.Context, p Principal, id domain.InstrumentID) error {
	v, err := s.account(ctx, p, "", domain.AccessTrade)
	if err != nil {
		return err
	}
	in, _, _ := s.d.Store.Instruments().Get(ctx, id)
	if err := s.d.Store.Watchlist().Remove(ctx, v.ID, id); err != nil {
		return fmt.Errorf("app.RemoveFromWatchlist: %w", err)
	}
	s.audit(ctx, p, "watchlist.remove", v.ID+"/"+strconv.FormatInt(int64(id), 10), in.Symbol)
	s.emitFor(ctx, v.ID, p.Username, "watchlist.removed", domain.SeverityInfo, cmp.Or(in.Symbol, "#"+strconv.FormatInt(int64(id), 10))+" removed from the watchlist of "+v.Name, "")
	return nil
}

// WatchlistQuotes returns the current bid/ask of every watchlist instrument (viewer).
func (s *Service) WatchlistQuotes(ctx context.Context, p Principal) ([]domain.Quote, error) {
	items, err := s.Watchlist(ctx, p)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return []domain.Quote{}, nil
	}
	if err := s.needBroker(); err != nil {
		return nil, err
	}
	ids := make([]domain.InstrumentID, len(items))
	for i, it := range items {
		ids[i] = it.Instrument.ID
	}
	acct, err := s.account(ctx, p, "", domain.AccessView)
	if err != nil {
		return nil, err
	}
	qs, err := s.d.Market.Quotes(brokerCtx(ctx, acct), ids)
	if err != nil {
		return nil, fmt.Errorf("app.WatchlistQuotes: %w", err)
	}
	return qs, nil
}
