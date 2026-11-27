package app

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// fxIDs caches the eToro instrument id of each currency pair (e.g. "AUDUSD") the cost gate
// converts with, found once by an exact symbol match in eToro's instrument search.
type fxIDs struct {
	mu  sync.Mutex
	ids map[string]domain.InstrumentID
}

func newFXIDs() *fxIDs { return &fxIDs{ids: map[string]domain.InstrumentID{}} }

// fxRates returns eToro's own rate to USD for each non-USD currency of costs (OD8: an ASX
// overnight fee in AUD, an LSE one in GBX). A currency cactusTrade does not convert is left
// out, so the cost gate refuses its line; a failed lookup or a stale rate is an error, and
// the cost gate refuses (fail closed). bctx carries the account.
func (s *Service) fxRates(bctx context.Context, costs []domain.CostItem, maxAge time.Duration) (domain.FXRates, error) {
	type need struct {
		pair  string
		units int64
	}
	wanted := map[string]need{}
	for _, c := range costs {
		cur := domain.NormCurrency(c.Currency)
		if cur == "" || cur == "USD" {
			continue
		}
		if pair, units, ok := domain.FXPair(cur); ok {
			wanted[cur] = need{pair, units}
		}
	}
	if len(wanted) == 0 {
		return nil, nil
	}
	if s.d.Market == nil {
		return nil, domain.ErrUnavailable
	}
	byPair := map[string]domain.InstrumentID{}
	var ids []domain.InstrumentID
	for _, n := range wanted {
		if _, ok := byPair[n.pair]; ok {
			continue
		}
		id, err := s.fxInstrument(bctx, n.pair)
		if err != nil {
			return nil, err
		}
		byPair[n.pair] = id
		ids = append(ids, id)
	}
	qs, err := s.d.Market.Quotes(bctx, ids)
	if err != nil {
		return nil, fmt.Errorf("reading eToro's exchange rates failed: %w", err)
	}
	now := s.d.Clock.Now()
	out := domain.FXRates{}
	for cur, n := range wanted {
		id := byPair[n.pair]
		i := slices.IndexFunc(qs, func(q domain.Quote) bool { return q.Instrument == id })
		if i < 0 {
			return nil, fmt.Errorf("eToro returned no %s rate", n.pair)
		}
		r, err := domain.FXRateFromQuote(qs[i], n.units, now, maxAge)
		if err != nil {
			return nil, fmt.Errorf("eToro's %s rate: %w", n.pair, err)
		}
		out[cur] = r
	}
	return out, nil
}

// fxInstrument finds eToro's instrument for the currency pair symbol (e.g. "AUDUSD"): the one
// search result whose symbol is exactly that. None, or more than one, is an error.
func (s *Service) fxInstrument(bctx context.Context, pair string) (domain.InstrumentID, error) {
	s.fx.mu.Lock()
	id, ok := s.fx.ids[pair]
	s.fx.mu.Unlock()
	if ok {
		return id, nil
	}
	found, err := s.d.Market.Search(bctx, pair)
	if err != nil {
		return 0, fmt.Errorf("searching eToro for %s failed: %w", pair, err)
	}
	var match []domain.InstrumentID
	for _, in := range found {
		if in.Symbol == pair {
			match = append(match, in.ID)
		}
	}
	if len(match) != 1 {
		return 0, fmt.Errorf("eToro's search found %d instruments with the symbol %s, not exactly one", len(match), pair)
	}
	s.fx.mu.Lock()
	s.fx.ids[pair] = match[0]
	s.fx.mu.Unlock()
	return match[0], nil
}
