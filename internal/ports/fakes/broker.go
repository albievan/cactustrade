package fakes

import (
	"context"
	"fmt"
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
	// SeeAccount makes Portfolio record the account in the context (LastAccount).
	SeeAccount  bool
	LastAccount domain.Account
	// Bars are the candles per instrument; Inds and Screened feed Industries / Screen.
	Bars      map[domain.InstrumentID][]domain.Candle
	Inds      []domain.Industry
	Screened  map[int64][]domain.Instrument
	EligCalls int
}

var _ ports.Research = (*Broker)(nil)

// Industries returns Inds.
func (b *Broker) Industries(context.Context) ([]domain.Industry, error) { return b.Inds, b.fail() }

// Screen returns Screened[industry].
func (b *Broker) Screen(_ context.Context, industry int64, _ int) ([]domain.Instrument, error) {
	return b.Screened[industry], b.fail()
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
func (b *Broker) Portfolio(ctx context.Context) (domain.Portfolio, error) {
	if b.SeeAccount {
		b.mu.Lock()
		b.LastAccount, _ = ports.AccountFrom(ctx)
		b.mu.Unlock()
	}
	if err := b.fail(); err != nil {
		return domain.Portfolio{}, err
	}
	return b.Account, nil
}

// Eligibility returns Eligible entries (unknown ids: not eligible, "instrument not found").
func (b *Broker) Eligibility(_ context.Context, ids []domain.InstrumentID) ([]domain.Eligibility, error) {
	b.mu.Lock()
	b.EligCalls++
	b.mu.Unlock()
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
func (b *Broker) Costs(_ context.Context, _ domain.InstrumentID, _ domain.Direction, _ domain.Cents) ([]domain.CostItem, error) {
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

// Candles returns Bars[id], or an error when there are none.
func (b *Broker) Candles(_ context.Context, id domain.InstrumentID, _ time.Duration, _ int) ([]domain.Candle, error) {
	if err := b.fail(); err != nil {
		return nil, err
	}
	bs, ok := b.Bars[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return bs, nil
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

// Holdings is an in-memory read-only ports.Holdings. ByAccount answers per account id; an
// account without an entry has no keys (ErrCredentialsMissing), like the real client.
type Holdings struct {
	mu        sync.Mutex
	ByAccount map[string]domain.Holdings
	Errs      map[string]error
	Calls     int
}

var _ ports.Holdings = (*Holdings)(nil)

// Holdings answers for the account in ctx; without one it refuses.
func (h *Holdings) Holdings(ctx context.Context) (domain.Holdings, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Calls++
	a, ok := ports.AccountFrom(ctx)
	if !ok {
		return domain.Holdings{}, domain.ErrUnavailable
	}
	if err := h.Errs[a.ID]; err != nil {
		return domain.Holdings{}, err
	}
	x, ok := h.ByAccount[a.ID]
	if !ok {
		return domain.Holdings{}, fmt.Errorf("keys: %w: %w", domain.ErrCredentialsMissing, domain.ErrUnavailable)
	}
	return x, nil
}

// OAuthConnector is a fake browser sign-in. AuthorizeURL answers
// "https://auth.example/authorize?redirect_uri=…&state=…"; Exchange records the code and
// fails with Err when set.
type OAuthConnector struct {
	mu        sync.Mutex
	Err       error
	Exchanged []string // "accountID:code:redirectURI"
}

var _ ports.OAuthConnector = (*OAuthConnector)(nil)

// AuthorizeURL returns a fixed address carrying state.
func (o *OAuthConnector) AuthorizeURL(ctx context.Context, redirectURI, state string) (string, error) {
	if _, ok := ports.AccountFrom(ctx); !ok {
		return "", domain.ErrUnavailable
	}
	return "https://auth.example/authorize?redirect_uri=" + redirectURI + "&state=" + state, nil
}

// Exchange records the code for the account in ctx.
func (o *OAuthConnector) Exchange(ctx context.Context, code, redirectURI string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	a, ok := ports.AccountFrom(ctx)
	if !ok {
		return domain.ErrUnavailable
	}
	if o.Err != nil {
		return o.Err
	}
	o.Exchanged = append(o.Exchanged, a.ID+":"+code+":"+redirectURI)
	return nil
}
