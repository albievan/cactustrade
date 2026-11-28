package fakes

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math"
	"math/big"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// T212Outcome scripts what the simulated Trading 212 account does with one Place.
type T212Outcome string

// Scripted outcomes of Place (PLAN R45.3).
const (
	// T212Fill: accepted; a market order fills at once (in session), a stop stays active
	// until TriggerStop or a cancel.
	T212Fill T212Outcome = "fill"
	// T212Pending: accepted and active (NEW) until Fill, TriggerStop or a cancel.
	T212Pending T212Outcome = "pending"
	// T212Reject: accepted, then rejected by Trading 212 (REJECTED, in the history).
	T212Reject T212Outcome = "reject"
	// T212PartialFill: accepted; half the quantity (whole shares, rounded down) fills, the
	// rest stays active (PARTIALLY_FILLED).
	T212PartialFill T212Outcome = "partial"
	// T212Refused: a 400; nothing is recorded at Trading 212 (*domain.NotAcceptedError).
	T212Refused T212Outcome = "refused"
	// T212LostApplied: processed like T212Fill, but the answer is lost
	// (domain.ErrOutcomeUnknown).
	T212LostApplied T212Outcome = "lost_applied"
	// T212LostNotApplied: the answer is lost and Trading 212 never recorded the order.
	T212LostNotApplied T212Outcome = "lost_not_applied"
	// T212RateLimited: a 429 (an unknown outcome, T6b); not processed.
	T212RateLimited T212Outcome = "rate_limited"
)

// T212SimDefaultPrice is the fill price of a ticker without one in Prices (100.00).
const T212SimDefaultPrice domain.Micros = 100_000_000

// T212Sim is a simulated Trading 212 practice account for executor tests: a ports.T212Broker
// that accepts, fills, rejects or loses orders as scripted, keeps cash and positions, and
// records every request. Like the adapter it refuses any account that is not a Trading 212
// practice account (ErrRealModeGated) before anything, a sell above the held quantity (net
// of the sell orders already resting, a stop included) and an unswitched fraction
// (NotAccepted, no order). Set the exported fields before use.
type T212Sim struct {
	mu sync.Mutex
	// Now stamps orders, fills and the account (default time.Now).
	Now func() time.Time
	// Currency is the account currency (default GBP); Cash its free cash in minor units.
	Currency string
	Cash     domain.Cents
	// Held are the account's positions by ticker (including the owner's own shares).
	Held map[string]domain.T212Position
	// Catalogue is what Instruments returns; an instrument's Currency sets its price scale
	// (default: the account currency).
	Catalogue []domain.T212Instrument
	// Prices are the market fill prices per ticker (default T212SimDefaultPrice).
	Prices map[string]domain.Micros
	// Script is consumed one outcome per Place; when empty the outcome is T212Fill.
	Script []T212Outcome
	// MarketClosed: a market order is accepted and queued (NEW) until OpenMarket (T16).
	MarketClosed bool
	// CancelFillsFirst: a cancel is accepted (200), but the order fills while it is being
	// cancelled (T10).
	CancelFillsFirst bool
	// OnPlace runs at the start of every Place for an allowed account, e.g.
	// RequireT212OrderRow: an error is recorded in Violations (the order is still processed).
	OnPlace func(ctx context.Context, in domain.T212Intent) error
	// ReadErr makes every read fail; PlaceErr / CancelErr make Place / Cancel fail before
	// anything is recorded at Trading 212.
	ReadErr   error
	PlaceErr  error
	CancelErr error
	// PageSize is the history page size (default 50).
	PageSize int

	// Records.
	Intents    []domain.T212Intent // every Place call for an allowed account, in order
	Places     int
	CancelIDs  []int64 // every Cancel call for an allowed account, in order
	Reads      int
	Outcomes   map[T212Outcome]int
	Violations []string

	nextID  int64
	active  map[int64]*domain.BrokerOrder
	history []domain.BrokerOrder // newest last
}

var _ ports.T212Broker = (*T212Sim)(nil)

func (s *T212Sim) init() {
	if s.active == nil {
		s.active = map[int64]*domain.BrokerOrder{}
	}
	if s.Held == nil {
		s.Held = map[string]domain.T212Position{}
	}
	if s.Outcomes == nil {
		s.Outcomes = map[T212Outcome]int{}
	}
	if s.Currency == "" {
		s.Currency = "GBP"
	}
}

func (s *T212Sim) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

// gate refuses like the adapter (T212-n): no account, or an account that is not a Trading
// 212 practice account, whatever permit ctx carries.
func (s *T212Sim) gate(ctx context.Context, op string) error {
	a, ok := ports.AccountFrom(ctx)
	switch {
	case !ok || a.ID == "":
		s.Violations = append(s.Violations, op+" without an account")
		return domain.ErrUnavailable
	case !a.T212Trading():
		s.Violations = append(s.Violations, op+" for an account that is not a Trading 212 practice account")
		return domain.ErrRealModeGated
	}
	return nil
}

func (s *T212Sim) read(ctx context.Context, op string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if err := s.gate(ctx, op); err != nil {
		return err
	}
	s.Reads++
	return s.ReadErr
}

// scale is account minor units per unit of ticker's price currency.
func (s *T212Sim) scale(ticker string) int64 {
	cur := s.Currency
	if i := slices.IndexFunc(s.Catalogue, func(t domain.T212Instrument) bool { return t.Ticker == ticker }); i >= 0 && s.Catalogue[i].Currency != "" {
		cur = s.Catalogue[i].Currency
	}
	sc, err := domain.T212PriceScale(cur, s.Currency)
	if err != nil {
		return 100
	}
	return sc
}

// value is quantity × price in account minor units, rounded up (a buy) or down (a sell).
func (s *T212Sim) value(ticker string, q, price domain.Micros, up bool) domain.Cents {
	num := new(big.Int).Mul(big.NewInt(int64(q)), big.NewInt(int64(price)))
	num.Mul(num, big.NewInt(s.scale(ticker)))
	den := big.NewInt(1_000_000_000_000)
	quo, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if up && rem.Sign() > 0 {
		quo.Add(quo, big.NewInt(1))
	}
	if !quo.IsInt64() {
		return math.MaxInt64
	}
	return domain.Cents(quo.Int64())
}

func (s *T212Sim) price(ticker string) domain.Micros {
	if p, ok := s.Prices[ticker]; ok && p > 0 {
		return p
	}
	return T212SimDefaultPrice
}

// Account returns the cash, the positions (at their current prices) and the equity.
func (s *T212Sim) Account(ctx context.Context) (domain.T212Account, error) {
	if err := s.read(ctx, "account"); err != nil {
		return domain.T212Account{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := domain.T212Account{Currency: s.Currency, AvailableToTrade: s.Cash, AsOf: s.now()}
	for _, t := range slices.Sorted(maps.Keys(s.Held)) {
		p := s.Held[t]
		p.CurrentPrice = s.price(t)
		p.Value = s.value(t, p.Quantity, p.CurrentPrice, false)
		p.PnL = p.Value - p.Cost
		a.Positions = append(a.Positions, p)
		a.Invested += p.Cost
		a.Equity += p.Value
	}
	for _, o := range s.active {
		if o.Side == domain.T212Buy {
			a.ReservedForOrders += s.value(o.Ticker, o.Quantity-o.FilledQuantity, s.price(o.Ticker), true)
		}
	}
	a.Equity += s.Cash + a.ReservedForOrders
	return a, nil
}

// Instruments returns Catalogue.
func (s *T212Sim) Instruments(ctx context.Context) ([]domain.T212Instrument, error) {
	if err := s.read(ctx, "instruments"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.Catalogue), nil
}

// OpenOrders lists the active orders by id.
func (s *T212Sim) OpenOrders(ctx context.Context) ([]domain.BrokerOrder, error) {
	if err := s.read(ctx, "open orders"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.BrokerOrder, 0, len(s.active))
	for _, o := range s.active {
		out = append(out, *o)
	}
	slices.SortFunc(out, func(a, b domain.BrokerOrder) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// Order reads one active order (ErrNotFound once it is filled, cancelled or rejected: T4).
func (s *T212Sim) Order(ctx context.Context, id int64) (domain.BrokerOrder, error) {
	if err := s.read(ctx, "order"); err != nil {
		return domain.BrokerOrder{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.active[id]
	if !ok {
		return domain.BrokerOrder{}, fmt.Errorf("trading212 order %d: %w", id, domain.ErrNotFound)
	}
	return *o, nil
}

// History returns one page of the finished orders, newest first; cursor is the index of the
// page's first order.
func (s *T212Sim) History(ctx context.Context, ticker, cursor string) (domain.T212HistoryPage, error) {
	if err := s.read(ctx, "history"); err != nil {
		return domain.T212HistoryPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var all []domain.BrokerOrder
	for i := len(s.history) - 1; i >= 0; i-- {
		if ticker == "" || s.history[i].Ticker == ticker {
			all = append(all, s.history[i])
		}
	}
	start := 0
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 || n > len(all) {
			return domain.T212HistoryPage{}, domain.Invalid("cursor", "unknown")
		}
		start = n
	}
	size := cmp.Or(s.PageSize, 50)
	end := min(start+size, len(all))
	page := domain.T212HistoryPage{Orders: slices.Clone(all[start:end])}
	if end < len(all) {
		page.Next = strconv.Itoa(end)
	}
	return page, nil
}

// Place processes the intent as scripted.
func (s *T212Sim) Place(ctx context.Context, in domain.T212Intent) (domain.BrokerOrder, error) {
	s.mu.Lock()
	s.init()
	if err := s.gate(ctx, "place"); err != nil {
		s.mu.Unlock()
		return domain.BrokerOrder{}, err
	}
	hook := s.OnPlace
	s.Intents = append(s.Intents, in)
	s.Places++
	s.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, in); err != nil {
			s.mu.Lock()
			s.Violations = append(s.Violations, "place: "+err.Error())
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.PlaceErr != nil {
		return domain.BrokerOrder{}, s.PlaceErr
	}
	// The adapter's own refusals: nothing is sent.
	if why := s.refusal(in); why != "" {
		s.Violations = append(s.Violations, "place: "+why)
		return domain.BrokerOrder{}, &domain.NotAcceptedError{Reason: why}
	}
	out := T212Fill
	if len(s.Script) > 0 {
		out, s.Script = s.Script[0], s.Script[1:]
	}
	s.Outcomes[out]++
	switch out {
	case T212Refused:
		return domain.BrokerOrder{}, &domain.NotAcceptedError{Reason: "failed validation"}
	case T212LostNotApplied:
		return domain.BrokerOrder{}, fmt.Errorf("trading212 place: connection reset: %w", domain.ErrOutcomeUnknown)
	case T212RateLimited:
		return domain.BrokerOrder{}, fmt.Errorf("trading212 place: HTTP 429: %w", domain.ErrOutcomeUnknown)
	}
	o := s.accept(in)
	switch {
	case out == T212Reject:
		s.finish(o, "REJECTED")
	case out == T212Pending, in.Type == domain.T212StopOrder, s.MarketClosed:
		// active until filled, triggered, opened or cancelled
	case out == T212PartialFill:
		if half := (in.Quantity / 2) / 1_000_000 * 1_000_000; half > 0 {
			s.fill(o, half, s.price(in.Ticker))
		}
	default:
		s.fill(o, in.Quantity, s.price(in.Ticker))
	}
	if out == T212LostApplied {
		return domain.BrokerOrder{}, fmt.Errorf("trading212 place: timeout: %w", domain.ErrOutcomeUnknown)
	}
	return *o, nil
}

// refusal is why the adapter would not send in at all ("" = it would).
func (s *T212Sim) refusal(in domain.T212Intent) string {
	switch {
	case in.Quantity <= 0 || in.Ticker == "":
		return "no ticker or quantity"
	case !in.Fractional && in.Quantity%1_000_000 != 0:
		return "a fraction while the fractional switch is off"
	case in.Side != domain.T212Buy && in.Side != domain.T212Sell:
		return "unknown side"
	case in.Type != domain.T212Market && in.Type != domain.T212StopOrder:
		return "unknown order type"
	case in.Side == domain.T212Buy && in.Type != domain.T212Market:
		return "a buy is a market order only"
	case in.Type == domain.T212StopOrder && in.StopPrice <= 0:
		return "a stop without a stop price"
	case in.Side == domain.T212Sell && in.Quantity > s.Held[in.Ticker].QuantityAvailable:
		return fmt.Sprintf("a sell of %s is more than the %s available", in.Quantity, s.Held[in.Ticker].QuantityAvailable)
	case in.Side == domain.T212Sell && in.Quantity+s.restingSells(in.Ticker) > s.Held[in.Ticker].Quantity:
		return fmt.Sprintf("a sell of %s and the %s already resting in sell orders are more than the %s held (a double sell)",
			in.Quantity, s.restingSells(in.Ticker), s.Held[in.Ticker].Quantity)
	}
	return ""
}

// restingSells is the quantity still to fill of the active sell orders (stops included) in
// ticker: like the adapter, the sim refuses a sell that would, with them, sell more than held.
func (s *T212Sim) restingSells(ticker string) domain.Micros {
	var q domain.Micros
	for _, o := range s.active {
		if o.Ticker == ticker && o.Side == domain.T212Sell {
			q += o.Quantity - o.FilledQuantity
		}
	}
	return q
}

func (s *T212Sim) accept(in domain.T212Intent) *domain.BrokerOrder {
	s.nextID++
	o := &domain.BrokerOrder{
		ID: 9_000 + s.nextID, Ticker: in.Ticker, Side: in.Side, Type: in.Type, Quantity: in.Quantity,
		StopPrice: in.StopPrice, Status: "NEW", State: domain.OrderPending, InitiatedFrom: domain.T212InitiatedFromAPI,
		CreatedAt: s.now(),
	}
	if i := slices.IndexFunc(s.Catalogue, func(t domain.T212Instrument) bool { return t.Ticker == in.Ticker }); i >= 0 {
		o.ISIN = s.Catalogue[i].ISIN
	}
	s.active[o.ID] = o
	return o
}

// fill executes q more of o at price; a buy short of cash is rejected instead.
func (s *T212Sim) fill(o *domain.BrokerOrder, q, price domain.Micros) {
	q = min(q, o.Quantity-o.FilledQuantity)
	if q <= 0 {
		return
	}
	p := s.Held[o.Ticker]
	if o.Side == domain.T212Buy {
		cost := s.value(o.Ticker, q, price, true)
		if cost > s.Cash {
			s.finish(o, "REJECTED")
			return
		}
		s.Cash -= cost
		p.Ticker, p.Cost = o.Ticker, p.Cost+cost
		p.AvgPrice = price
		if p.OpenedAt.IsZero() {
			p.OpenedAt = s.now()
		}
		p.Quantity += q
		p.QuantityAvailable += q
		p.ISIN = o.ISIN
	} else {
		q = min(q, p.Quantity) // never short
		if q <= 0 {
			s.finish(o, "REJECTED")
			return
		}
		s.Cash += s.value(o.Ticker, q, price, false)
		if p.Quantity > 0 {
			p.Cost -= domain.Cents(int64(p.Cost) * int64(q) / int64(p.Quantity))
		}
		p.Quantity -= q
		p.QuantityAvailable = max(p.QuantityAvailable-q, 0)
	}
	if p.Quantity > 0 {
		s.Held[o.Ticker] = p
	} else {
		delete(s.Held, o.Ticker)
	}
	// average fill price over what filled so far
	if o.FilledQuantity > 0 {
		prev := new(big.Int).Mul(big.NewInt(int64(o.FillPrice)), big.NewInt(int64(o.FilledQuantity)))
		prev.Add(prev, new(big.Int).Mul(big.NewInt(int64(price)), big.NewInt(int64(q))))
		prev.Quo(prev, big.NewInt(int64(o.FilledQuantity+q)))
		o.FillPrice = domain.Micros(prev.Int64())
	} else {
		o.FillPrice = price
	}
	o.FilledQuantity += q
	o.FilledAt = s.now()
	if o.FilledQuantity >= o.Quantity {
		s.finish(o, "FILLED")
		return
	}
	o.Status, o.State, o.Final = "PARTIALLY_FILLED", domain.OrderPartial, false
}

// finish ends o with status and moves it to the history.
func (s *T212Sim) finish(o *domain.BrokerOrder, status string) {
	o.Status = status
	o.State, o.Final = domain.T212OrderState(status)
	delete(s.active, o.ID)
	s.history = append(s.history, *o)
}

// Cancel cancels an active order (ErrNotFound when it is not active). With CancelFillsFirst
// the cancel is accepted, but the order fills first.
func (s *T212Sim) Cancel(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	if err := s.gate(ctx, "cancel"); err != nil {
		return err
	}
	s.CancelIDs = append(s.CancelIDs, id)
	if s.CancelErr != nil {
		return s.CancelErr
	}
	o, ok := s.active[id]
	if !ok {
		return fmt.Errorf("trading212 cancel %d: %w", id, domain.ErrNotFound)
	}
	if s.CancelFillsFirst {
		s.fill(o, o.Quantity, s.fillPrice(o))
		return nil
	}
	s.finish(o, "CANCELLED")
	return nil
}

func (s *T212Sim) fillPrice(o *domain.BrokerOrder) domain.Micros {
	if o.Type == domain.T212StopOrder {
		return o.StopPrice
	}
	return s.price(o.Ticker)
}

// Fill fills the rest of active order id at its price (a stop at its stop price).
func (s *T212Sim) Fill(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	o, ok := s.active[id]
	if ok {
		s.fill(o, o.Quantity, s.fillPrice(o))
	}
	return ok
}

// TriggerStop moves ticker's last price to price: every active sell stop at or above it
// becomes a market order and fills at price (slippage below the stop is possible). It returns
// how many fired.
func (s *T212Sim) TriggerStop(ticker string, price domain.Micros) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	n := 0
	for _, id := range slices.Sorted(maps.Keys(s.active)) {
		o := s.active[id]
		if o.Ticker == ticker && o.Type == domain.T212StopOrder && price <= o.StopPrice {
			s.fill(o, o.Quantity, price)
			n++
		}
	}
	return n
}

// OpenMarket ends MarketClosed and fills every queued market order (T16).
func (s *T212Sim) OpenMarket() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.init()
	s.MarketClosed = false
	for _, id := range slices.Sorted(maps.Keys(s.active)) {
		if o := s.active[id]; o.Type == domain.T212Market {
			s.fill(o, o.Quantity, s.price(o.Ticker))
		}
	}
}

// RequireT212OrderRow is an OnPlace hook for the executor: the intent's order must already
// be persisted, as sent, on Trading 212, with the intent's ticker, quantity, and the kind its
// side and type come from (CLAUDE.md §2.7: persisted before it is sent).
func RequireT212OrderRow(orders ports.Orders) func(context.Context, domain.T212Intent) error {
	return func(ctx context.Context, in domain.T212Intent) error {
		o, err := orders.Get(ctx, in.RequestID)
		if err != nil {
			return fmt.Errorf("no order row for %s: %w", in.RequestID, err)
		}
		kind := map[[2]string]domain.OrderKind{
			{string(domain.T212Buy), string(domain.T212Market)}:     domain.OrderOpen,
			{string(domain.T212Sell), string(domain.T212StopOrder)}: domain.OrderStop,
			{string(domain.T212Sell), string(domain.T212Market)}:    domain.OrderClose,
		}[[2]string{string(in.Side), string(in.Type)}]
		switch {
		case o.OnPlatform() != domain.PlatformTrading212 || o.State != domain.OrderSent:
			return fmt.Errorf("order %s is a %s order in state %s, not a sent Trading 212 order", in.RequestID, o.OnPlatform(), o.State)
		case o.Kind != kind || o.Ticker != in.Ticker || o.Quantity != in.Quantity || (kind == domain.OrderStop && o.StopLoss != in.StopPrice):
			return fmt.Errorf("order %s differs from the intent sent", in.RequestID)
		}
		return nil
	}
}
