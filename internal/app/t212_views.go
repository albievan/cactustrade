package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// What the web app shows of a Trading 212 practice account besides the agent views (PLAN
// R45.9): the watchlist's instrument matches and the account list's price source. Nothing
// here sends anything to Trading 212.

// Broker match statuses of a watchlist share on a Trading 212 practice account.
const (
	MatchUnconfirmed = "unconfirmed" // the owner must confirm it before anything is traded
	MatchConfirmed   = "confirmed"   // tradable
	MatchUnusable    = "unusable"    // confirmed, but no longer tradable (Reason says why)
)

// BrokerMatch is the Trading 212 instrument matched to a watchlist share, as the watchlist
// shows it (D42).
type BrokerMatch struct {
	Instrument     domain.InstrumentID
	Ticker, ISIN   string
	Name, Currency string // from the stored catalogue ("" when no longer listed)
	Status         string // MatchUnconfirmed, MatchConfirmed or MatchUnusable
	Reason         string // why not tradable ("" when confirmed)
	ConfirmedAt    time.Time
}

// WatchlistMatches returns the Trading 212 matches of the shares on the caller's current
// account's watchlist, by eToro instrument (view access). An account that is not a Trading
// 212 practice account has none (an empty map). Only stored data is read, plus the account's
// currency once when no view has read it yet.
func (s *Service) WatchlistMatches(ctx context.Context, p Principal) (map[domain.InstrumentID]BrokerMatch, error) {
	if err := s.authorize(p, domain.PermView); err != nil {
		return nil, err
	}
	v, err := s.agentAccount(ctx, p, "", domain.AccessView)
	if err != nil {
		return nil, err
	}
	out := map[domain.InstrumentID]BrokerMatch{}
	if !v.T212Trading() {
		return out, nil
	}
	ms, err := s.d.Store.InstrumentMatches().List(ctx, v.ID)
	if err != nil {
		return nil, fmt.Errorf("app.WatchlistMatches: %w", err)
	}
	cur := s.viewCurrency(v.Account)
	for _, m := range ms {
		b := BrokerMatch{Instrument: m.Instrument, Ticker: m.Ticker, ISIN: m.ISIN, Status: MatchUnconfirmed, ConfirmedAt: m.ConfirmedAt}
		if t, err := s.d.Store.T212Instruments().Get(ctx, m.Ticker); err == nil {
			b.Name, b.Currency = t.Name, t.Currency
		}
		if !m.Confirmed() {
			b.Reason = "not confirmed yet: check both names and the ISIN, then confirm"
			out[m.Instrument] = b
			continue
		}
		b.Status = MatchConfirmed
		if cur == "" {
			if c, err := s.t212AccountCurrency(ctx, v.Account); err == nil {
				cur = c
			}
		}
		if in, _, err := s.d.Store.Instruments().Get(ctx, m.Instrument); err == nil && cur != "" {
			if el, _, err := s.t212Eligibility(ctx, v.Account, in, cur); err == nil && !el.RealLong {
				b.Status, b.Reason = MatchUnusable, strings.Trim(el.Reason, "()")
			}
		}
		out[m.Instrument] = b
	}
	return out, nil
}

// ResolveOrder is ResolveT212Order for the web app: the resolved order as the orders page
// shows it (owner of the account, web session; audited).
func (s *Service) ResolveOrder(ctx context.Context, p Principal, requestID string, brokerOrderID int64, notPlaced bool) (OrderView, error) {
	o, err := s.ResolveT212Order(ctx, p, requestID, brokerOrderID, notPlaced)
	if err != nil {
		return OrderView{}, err
	}
	v := OrderView{Order: o}
	if o.Instrument > 0 {
		in := s.instrumentTags(ctx)(o.Instrument)
		v.Symbol, v.Market = in.Symbol, in.Market
	}
	return v, nil
}

// SettleOrder is SettleT212Position for the web app: the buy whose position was settled, as
// the orders page shows it (owner of the account, web session; audited).
func (s *Service) SettleOrder(ctx context.Context, p Principal, requestID string) (OrderView, error) {
	o, err := s.SettleT212Position(ctx, p, requestID)
	if err != nil {
		return OrderView{}, err
	}
	v := OrderView{Order: o}
	if o.Instrument > 0 {
		in := s.instrumentTags(ctx)(o.Instrument)
		v.Symbol, v.Market = in.Symbol, in.Market
	}
	return v, nil
}

// AccountExtra is what the account list shows of an account besides itself.
type AccountExtra struct {
	// Currency of its money figures when known without a broker read (USD on eToro).
	Currency string
	// PriceSource is a Trading 212 practice account's eToro price source ("" = none).
	PriceSource string
}

// AccountExtras returns the extras of the given accounts, by id. It reads only stored data
// and this process's state (no broker call).
func (s *Service) AccountExtras(ctx context.Context, vs []domain.AccountView) map[string]AccountExtra {
	out := make(map[string]AccountExtra, len(vs))
	for _, v := range vs {
		x := AccountExtra{}
		switch {
		case v.OnPlatform() == domain.PlatformEtoro:
			x.Currency = "USD"
		case v.T212Trading():
			x.Currency = s.viewCurrency(v.Account)
			x.PriceSource, _ = s.d.Store.Settings().Get(ctx, priceSourceKey(v.ID))
		}
		out[v.ID] = x
	}
	return out
}
