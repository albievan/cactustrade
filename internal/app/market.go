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

// TestConnection calls eToro's portfolio endpoint with the current mode's keys (admin). A
// broker failure is a result (OK=false with eToro's reason), not an error.
func (s *Service) TestConnection(ctx context.Context, p Principal) (BrokerCheck, error) {
	if err := s.authorize(p, domain.PermManageSecrets); err != nil {
		return BrokerCheck{}, err
	}
	if err := s.needBroker(); err != nil {
		return BrokerCheck{}, err
	}
	mode := s.Mode(ctx)
	start := s.d.Clock.Now()
	pf, err := s.d.Broker.Portfolio(ctx)
	res := BrokerCheck{Mode: mode, Latency: s.d.Clock.Now().Sub(start), CheckedAt: start.UTC(), ConfiguredKeys: true}
	if err != nil {
		if !errors.Is(err, domain.ErrUnavailable) && !errors.Is(err, domain.ErrNotFound) {
			return BrokerCheck{}, fmt.Errorf("app.TestConnection: %w", err)
		}
		res.Error = err.Error()
		res.ConfiguredKeys = !errors.Is(err, domain.ErrCredentialsMissing)
		s.audit(ctx, p, "broker.test_connection", string(mode), "failed")
		return res, nil
	}
	res.OK, res.AvailableCash, res.Equity, res.Positions = true, pf.AvailableCash, pf.Equity(), len(pf.Positions)
	for _, pos := range pf.Positions {
		if !pos.Conforms() {
			res.NonConforming++
		}
	}
	s.audit(ctx, p, "broker.test_connection", string(mode), "ok")
	return res, nil
}

// Portfolio is the account as eToro reports it now (viewer).
func (s *Service) Portfolio(ctx context.Context, p Principal) (domain.Portfolio, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return domain.Portfolio{}, err
	}
	if err := s.needBroker(); err != nil {
		return domain.Portfolio{}, err
	}
	pf, err := s.d.Broker.Portfolio(ctx)
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
	for _, m := range []domain.Market{domain.MarketLSE, domain.MarketNYSE} {
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

// SearchInstruments searches eToro (admin: it is how the watchlist is built, and it spends
// the market-data quota).
func (s *Service) SearchInstruments(ctx context.Context, p Principal, query string) ([]domain.Instrument, error) {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return nil, err
	}
	if err := s.needBroker(); err != nil {
		return nil, err
	}
	res, err := s.d.Market.Search(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("app.SearchInstruments: %w", err)
	}
	return res, nil
}

// Watchlist lists the instruments the agent may trade (viewer).
func (s *Service) Watchlist(ctx context.Context, p Principal) ([]domain.WatchItem, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return nil, err
	}
	items, err := s.d.Store.Watchlist().List(ctx)
	if err != nil {
		return nil, fmt.Errorf("app.Watchlist: %w", err)
	}
	return items, nil
}

// AddToWatchlist adds a share after checking with eToro that it is a share on LSE or
// NYSE/NASDAQ and can be bought as a real share, long, at leverage 1 (admin). Anything else
// is refused with eToro's reason (fail closed).
func (s *Service) AddToWatchlist(ctx context.Context, p Principal, id domain.InstrumentID) (domain.WatchItem, error) {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return domain.WatchItem{}, err
	}
	if err := s.needBroker(); err != nil {
		return domain.WatchItem{}, err
	}
	if id <= 0 {
		return domain.WatchItem{}, domain.Invalid("instrumentId", "must be positive")
	}
	ins, err := s.d.Market.Instruments(ctx, []domain.InstrumentID{id})
	if err != nil {
		return domain.WatchItem{}, fmt.Errorf("app.AddToWatchlist: %w", err)
	}
	idx := slices.IndexFunc(ins, func(in domain.Instrument) bool { return in.ID == id })
	if idx < 0 {
		return domain.WatchItem{}, domain.Invalid("instrumentId", "eToro does not know this instrument")
	}
	in := ins[idx]
	if !domain.IsShare(in.Type) || in.Market == "" {
		return domain.WatchItem{}, domain.Invalid("instrumentId", "only shares listed in London (LSE) or New York (NYSE/NASDAQ) can be traded")
	}
	els, err := s.d.Broker.Eligibility(ctx, []domain.InstrumentID{id})
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
	if !el.RealLong {
		return domain.WatchItem{}, domain.Invalid("instrumentId", "not tradable as a real share at leverage 1: "+el.Reason)
	}
	if err := s.d.Store.Watchlist().Add(ctx, id, p.Username, now); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return domain.WatchItem{}, fmt.Errorf("%s is already on the watchlist: %w", in.Symbol, domain.ErrConflict)
		}
		return domain.WatchItem{}, fmt.Errorf("app.AddToWatchlist: %w", err)
	}
	s.audit(ctx, p, "watchlist.add", strconv.FormatInt(int64(id), 10), in.Symbol)
	s.emit(ctx, p.Username, "watchlist.added", domain.SeverityInfo, in.Symbol+" added to the watchlist", in.Name+" ("+string(in.Market)+")")
	return domain.WatchItem{Instrument: in, Eligibility: el, AddedBy: p.Username, AddedAt: now}, nil
}

// RemoveFromWatchlist removes an instrument (admin).
func (s *Service) RemoveFromWatchlist(ctx context.Context, p Principal, id domain.InstrumentID) error {
	if err := s.authorize(p, domain.PermManageSettings); err != nil {
		return err
	}
	in, _, _ := s.d.Store.Instruments().Get(ctx, id)
	if err := s.d.Store.Watchlist().Remove(ctx, id); err != nil {
		return fmt.Errorf("app.RemoveFromWatchlist: %w", err)
	}
	s.audit(ctx, p, "watchlist.remove", strconv.FormatInt(int64(id), 10), in.Symbol)
	s.emit(ctx, p.Username, "watchlist.removed", domain.SeverityInfo, cmp.Or(in.Symbol, "#"+strconv.FormatInt(int64(id), 10))+" removed from the watchlist", "")
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
	qs, err := s.d.Market.Quotes(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("app.WatchlistQuotes: %w", err)
	}
	return qs, nil
}
