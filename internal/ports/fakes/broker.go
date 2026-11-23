package fakes

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Broker is an in-memory ports.Broker and ports.MarketData. Set Err to make every call
// fail. It never places orders: Open/Cancel/Close fail like the phase-2 client.
type Broker struct {
	mu          sync.Mutex
	Err         error
	Account     domain.Portfolio
	Known       map[domain.InstrumentID]domain.Instrument
	Eligible    map[domain.InstrumentID]domain.Eligibility
	Prices      map[domain.InstrumentID]domain.Quote
	SearchCalls int
}

var (
	_ ports.Broker     = (*Broker)(nil)
	_ ports.MarketData = (*Broker)(nil)
)

func (b *Broker) fail() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Err
}

// Portfolio returns Account.
func (b *Broker) Portfolio(context.Context) (domain.Portfolio, error) {
	if err := b.fail(); err != nil {
		return domain.Portfolio{}, err
	}
	return b.Account, nil
}

// Eligibility returns Eligible entries (unknown ids: not eligible, "instrument not found").
func (b *Broker) Eligibility(_ context.Context, ids []domain.InstrumentID) ([]domain.Eligibility, error) {
	if err := b.fail(); err != nil {
		return nil, err
	}
	var out []domain.Eligibility
	for _, id := range ids {
		el, ok := b.Eligible[id]
		if !ok {
			el = domain.Eligibility{Instrument: id, Reason: "instrument not found"}
		}
		out = append(out, el)
	}
	return out, nil
}

// Costs returns a fixed spread line.
func (b *Broker) Costs(_ context.Context, _ domain.InstrumentID, _ domain.Cents) ([]domain.CostItem, error) {
	if err := b.fail(); err != nil {
		return nil, err
	}
	return []domain.CostItem{{Type: "marketSpread", Value: 100000, Currency: "USD"}}, nil
}

// TradeHistory returns nothing.
func (b *Broker) TradeHistory(context.Context, time.Time) ([]domain.ClosedTrade, error) {
	return nil, b.fail()
}

// Open is refused.
func (b *Broker) Open(context.Context, domain.OrderIntent) (domain.OrderStatus, error) {
	return domain.OrderStatus{}, domain.ErrUnavailable
}

// Lookup reports unknown.
func (b *Broker) Lookup(_ context.Context, id string) (domain.OrderStatus, error) {
	return domain.OrderStatus{RequestID: id, State: domain.OrderUnknown}, domain.ErrNotFound
}

// Cancel is refused.
func (b *Broker) Cancel(context.Context, int64) error { return domain.ErrUnavailable }

// Close is refused.
func (b *Broker) Close(context.Context, int64, domain.InstrumentID) (domain.OrderStatus, error) {
	return domain.OrderStatus{}, domain.ErrUnavailable
}

// Quotes returns Prices for the ids that have one.
func (b *Broker) Quotes(_ context.Context, ids []domain.InstrumentID) ([]domain.Quote, error) {
	if err := b.fail(); err != nil {
		return nil, err
	}
	var out []domain.Quote
	for _, id := range ids {
		if q, ok := b.Prices[id]; ok {
			out = append(out, q)
		}
	}
	return out, nil
}

// Candles returns nothing.
func (b *Broker) Candles(context.Context, domain.InstrumentID, time.Duration, int) ([]domain.Candle, error) {
	return nil, b.fail()
}

// Search returns every Known instrument (sorted by id).
func (b *Broker) Search(context.Context, string) ([]domain.Instrument, error) {
	b.mu.Lock()
	b.SearchCalls++
	b.mu.Unlock()
	if err := b.fail(); err != nil {
		return nil, err
	}
	var out []domain.Instrument
	for _, in := range b.Known {
		out = append(out, in)
	}
	slices.SortFunc(out, func(a, c domain.Instrument) int { return int(a.ID - c.ID) })
	return out, nil
}

// Instruments returns the Known ones among ids.
func (b *Broker) Instruments(_ context.Context, ids []domain.InstrumentID) ([]domain.Instrument, error) {
	if err := b.fail(); err != nil {
		return nil, err
	}
	var out []domain.Instrument
	for _, id := range ids {
		if in, ok := b.Known[id]; ok {
			out = append(out, in)
		}
	}
	return out, nil
}
