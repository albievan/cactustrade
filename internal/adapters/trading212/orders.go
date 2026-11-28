package trading212

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Trading 212 trading (PLAN R45.8, D41–D44): the reads and the three order shapes of
// ports.T212Broker, for practice accounts only. Every call for any other account is refused
// with domain.ErrRealModeGated before a key is read or a request sent, whatever permit ctx
// carries. A change is sent once and never retried (Trading 212 takes no request id, T14).

var _ ports.T212Broker = (*Client)(nil)

// The three order shapes (CLAUDE.md §2.3), as constants: a market buy or sell never trades
// outside the session, a stop is good till cancelled (T212-h).
const (
	extendedHours = false
	stopValidity  = "GOOD_TILL_CANCEL"
)

// marketBody is POST orders/market: a buy has a positive quantity, a sell a negative one.
type marketBody struct {
	Ticker        string  `json:"ticker"`
	Quantity      float64 `json:"quantity"`
	ExtendedHours bool    `json:"extendedHours"`
}

// stopBody is POST orders/stop: always a sell (a negative quantity).
type stopBody struct {
	Ticker       string  `json:"ticker"`
	Quantity     float64 `json:"quantity"`
	StopPrice    float64 `json:"stopPrice"`
	TimeValidity string  `json:"timeValidity"`
}

type orderWire struct {
	ID             int64    `json:"id"`
	CreatedAt      string   `json:"createdAt"`
	Status         string   `json:"status"`
	Side           string   `json:"side"`
	Type           string   `json:"type"`
	Ticker         string   `json:"ticker"`
	Quantity       *float64 `json:"quantity"`
	FilledQuantity *float64 `json:"filledQuantity"`
	FilledValue    *float64 `json:"filledValue"`
	StopPrice      *float64 `json:"stopPrice"`
	InitiatedFrom  string   `json:"initiatedFrom"`
	Instrument     *struct {
		ISIN     string `json:"isin"`
		Ticker   string `json:"ticker"`
		Currency string `json:"currency"`
	} `json:"instrument"`
}

type historyWire struct {
	Items []struct {
		Order orderWire `json:"order"`
		Fill  *struct {
			FilledAt     string   `json:"filledAt"`
			Price        *float64 `json:"price"`
			Quantity     *float64 `json:"quantity"`
			WalletImpact *struct {
				Taxes []struct {
					Name     string  `json:"name"`
					Quantity float64 `json:"quantity"`
					Currency string  `json:"currency"`
				} `json:"taxes"`
			} `json:"walletImpact"`
		} `json:"fill"`
	} `json:"items"`
	NextPagePath *string `json:"nextPagePath"`
}

type instrumentWire struct {
	Ticker          string   `json:"ticker"`
	ISIN            string   `json:"isin"`
	Name            string   `json:"name"`
	ShortName       string   `json:"shortName"`
	CurrencyCode    string   `json:"currencyCode"`
	Type            string   `json:"type"`
	MaxOpenQuantity *float64 `json:"maxOpenQuantity"`
}

// practice is the account of ctx when it is a Trading 212 practice account; anything else is
// refused before any key is read (D41 g).
func practice(ctx context.Context) (domain.Account, error) {
	a, err := account(ctx)
	if err != nil {
		return domain.Account{}, err
	}
	if !a.T212Trading() {
		return domain.Account{}, fmt.Errorf("%s: Trading 212 trades practice accounts only: %w", a.Name, domain.ErrRealModeGated)
	}
	return a, nil
}

// abs is |f|.
func abs(f float64) float64 { return math.Abs(f) }

// toOrder converts an order of Trading 212's to ours. Quantities are positive whatever the
// side; the side comes from "side", else from the sign of the quantity.
func (cv *conv) toOrder(w orderWire) domain.BrokerOrder {
	o := domain.BrokerOrder{ID: w.ID, Ticker: w.Ticker, Status: w.Status, InitiatedFrom: strings.ToUpper(strings.TrimSpace(w.InitiatedFrom))}
	if w.Instrument != nil {
		o.ISIN = w.Instrument.ISIN
		if o.Ticker == "" {
			o.Ticker = w.Instrument.Ticker
		}
	}
	switch strings.ToUpper(w.Side) {
	case "BUY":
		o.Side = domain.T212Buy
	case "SELL":
		o.Side = domain.T212Sell
	default:
		if w.Quantity != nil && *w.Quantity < 0 {
			o.Side = domain.T212Sell
		} else if w.Quantity != nil && *w.Quantity > 0 {
			o.Side = domain.T212Buy
		}
	}
	o.Type = domain.T212OrderType(strings.ToLower(strings.TrimSpace(w.Type)))
	if w.Quantity != nil {
		o.Quantity = cv.micros(abs(*w.Quantity))
	}
	if w.FilledQuantity != nil {
		o.FilledQuantity = cv.micros(abs(*w.FilledQuantity))
	}
	if w.StopPrice != nil {
		o.StopPrice = cv.micros(*w.StopPrice)
	}
	if w.FilledValue != nil && w.FilledQuantity != nil && *w.FilledQuantity != 0 {
		o.FillPrice = cv.micros(abs(*w.FilledValue / *w.FilledQuantity))
	}
	o.State, o.Final = domain.T212OrderState(w.Status)
	if t, err := time.Parse(time.RFC3339Nano, w.CreatedAt); err == nil {
		o.CreatedAt = t.UTC()
	}
	return o
}

// Account reads the account summary and its positions.
func (c *Client) Account(ctx context.Context) (domain.T212Account, error) {
	if _, err := practice(ctx); err != nil {
		return domain.T212Account{}, err
	}
	h, pos, err := c.holdings(ctx)
	if err != nil {
		return domain.T212Account{}, err
	}
	a := domain.T212Account{
		Currency: h.Currency, AvailableToTrade: h.Cash, Invested: h.Invested, Equity: h.Equity, AsOf: h.AsOf,
		ReservedForOrders: h.reserved,
	}
	var cv conv
	for _, p := range pos {
		x := domain.T212Position{
			Ticker: p.Instrument.Ticker, ISIN: p.Instrument.ISIN, Name: p.Instrument.Name, Currency: p.Instrument.Currency,
			Quantity: cv.micros(p.Quantity), QuantityAvailable: cv.micros(p.QuantityAvailableForTrading),
			AvgPrice: cv.micros(p.AveragePricePaid), CurrentPrice: cv.micros(p.CurrentPrice), OpenedAt: p.CreatedAt.UTC(),
		}
		if w := p.WalletImpact; w != nil {
			x.Value, x.Cost, x.PnL = cv.cents(w.CurrentValue), cv.cents(w.TotalCost), cv.cents(w.UnrealizedProfitLoss)
		}
		a.Positions = append(a.Positions, x)
	}
	if cv.err != nil {
		return domain.T212Account{}, fmt.Errorf("trading212 %s: %w: %w", OpPositions, cv.err, domain.ErrUnavailable)
	}
	return a, nil
}

// Instruments reads Trading 212's whole instrument catalogue.
func (c *Client) Instruments(ctx context.Context) ([]domain.T212Instrument, error) {
	if _, err := practice(ctx); err != nil {
		return nil, err
	}
	var ws []instrumentWire
	if err := c.read(ctx, request{op: OpInstruments}, &ws); err != nil {
		return nil, err
	}
	var cv conv
	now := c.cfg.Now().UTC()
	out := make([]domain.T212Instrument, 0, len(ws))
	for _, w := range ws {
		in := domain.T212Instrument{
			Ticker: w.Ticker, ISIN: w.ISIN, Name: w.Name, ShortName: w.ShortName, Currency: strings.ToUpper(strings.TrimSpace(w.CurrencyCode)),
			Type: strings.ToUpper(strings.TrimSpace(w.Type)), FetchedAt: now,
		}
		if w.MaxOpenQuantity != nil {
			in.MaxOpenQuantity = cv.micros(*w.MaxOpenQuantity)
		}
		out = append(out, in)
	}
	if cv.err != nil {
		return nil, fmt.Errorf("trading212 %s: %w: %w", OpInstruments, cv.err, domain.ErrUnavailable)
	}
	return out, nil
}

// OpenOrders lists the account's active orders.
func (c *Client) OpenOrders(ctx context.Context) ([]domain.BrokerOrder, error) {
	if _, err := practice(ctx); err != nil {
		return nil, err
	}
	var ws []orderWire
	if err := c.read(ctx, request{op: OpOrders}, &ws); err != nil {
		return nil, err
	}
	var cv conv
	out := make([]domain.BrokerOrder, 0, len(ws))
	for _, w := range ws {
		out = append(out, cv.toOrder(w))
	}
	if cv.err != nil {
		return nil, fmt.Errorf("trading212 %s: %w: %w", OpOrders, cv.err, domain.ErrUnavailable)
	}
	return out, nil
}

// Order reads one active order (domain.ErrNotFound when Trading 212 does not list it).
func (c *Client) Order(ctx context.Context, id int64) (domain.BrokerOrder, error) {
	if _, err := practice(ctx); err != nil {
		return domain.BrokerOrder{}, err
	}
	if id <= 0 {
		return domain.BrokerOrder{}, domain.Invalid("id", "must be positive")
	}
	var w orderWire
	if err := c.read(ctx, request{op: OpOrder, id: id}, &w); err != nil {
		return domain.BrokerOrder{}, err
	}
	var cv conv
	o := cv.toOrder(w)
	if cv.err != nil || o.ID != id {
		return domain.BrokerOrder{}, fmt.Errorf("trading212 %s: unexpected order in the answer: %w", OpOrder, domain.ErrUnavailable)
	}
	return o, nil
}

// History reads one page of the order history with fills, newest first. cursor is "" for the
// first page, else the previous page's Next: Trading 212's nextPagePath, used verbatim, which
// must be the allowlisted history path.
func (c *Client) History(ctx context.Context, ticker, cursor string) (domain.T212HistoryPage, error) {
	if _, err := practice(ctx); err != nil {
		return domain.T212HistoryPage{}, err
	}
	r := request{op: OpOrderHistory}
	if cursor == "" {
		r.query = url.Values{"limit": {"50"}}
		if ticker != "" {
			r.query.Set("ticker", ticker)
		}
	} else {
		raw, err := historyQuery(cursor)
		if err != nil {
			return domain.T212HistoryPage{}, err
		}
		r.rawQuery = raw
	}
	var w historyWire
	if err := c.read(ctx, r, &w); err != nil {
		return domain.T212HistoryPage{}, err
	}
	var cv conv
	page := domain.T212HistoryPage{Orders: make([]domain.BrokerOrder, 0, len(w.Items))}
	for _, it := range w.Items {
		o := cv.toOrder(it.Order)
		if f := it.Fill; f != nil {
			if f.Price != nil {
				o.FillPrice = cv.micros(*f.Price)
			}
			if f.Quantity != nil && o.FilledQuantity == 0 {
				o.FilledQuantity = cv.micros(abs(*f.Quantity))
			}
			if t, err := time.Parse(time.RFC3339Nano, f.FilledAt); err == nil {
				o.FilledAt = t.UTC()
			}
			if f.WalletImpact != nil {
				for _, tx := range f.WalletImpact.Taxes {
					o.Taxes = append(o.Taxes, domain.T212Tax{Name: tx.Name, Amount: cv.micros(tx.Quantity), Currency: tx.Currency})
				}
			}
		}
		page.Orders = append(page.Orders, o)
	}
	if cv.err != nil {
		return domain.T212HistoryPage{}, fmt.Errorf("trading212 %s: %w: %w", OpOrderHistory, cv.err, domain.ErrUnavailable)
	}
	if w.NextPagePath != nil && *w.NextPagePath != "" {
		if _, err := historyQuery(*w.NextPagePath); err != nil {
			return domain.T212HistoryPage{}, err
		}
		page.Next = *w.NextPagePath
	}
	return page, nil
}

// historyQuery checks a nextPagePath (the allowlisted history path, with a query only) and
// returns its query.
func historyQuery(next string) (string, error) {
	p := Allowlist[OpOrderHistory].Path
	rest, ok := strings.CutPrefix(next, p)
	if !ok || (rest != "" && !strings.HasPrefix(rest, "?")) {
		return "", fmt.Errorf("trading212 %s: the next page %q is not the order history: %w", OpOrderHistory, next, domain.ErrUnavailable)
	}
	q := strings.TrimPrefix(rest, "?")
	if _, err := url.ParseQuery(q); err != nil {
		return "", fmt.Errorf("trading212 %s: the next page's query is unreadable: %w", OpOrderHistory, domain.ErrUnavailable)
	}
	return q, nil
}

// quantity converts a share quantity (Micros) to the JSON number; a fraction is refused
// unless fractional (the account's switch, D43).
func quantity(q domain.Micros, fractional bool) (float64, error) {
	if q <= 0 {
		return 0, &domain.NotAcceptedError{Reason: "no quantity: not sent"}
	}
	if !fractional && q%1_000_000 != 0 {
		return 0, &domain.NotAcceptedError{Reason: "a fractional quantity while the fractional switch is off: not sent"}
	}
	return float64(q) / 1e6, nil
}

// Place sends one of the three order shapes once (never again). Before a sell the positions
// and the active orders are read fresh: a sell of more than Trading 212 makes available, or
// more than held net of the sell orders already resting there (a stop), is refused unsent.
func (c *Client) Place(ctx context.Context, in domain.T212Intent) (domain.BrokerOrder, error) {
	if _, err := practice(ctx); err != nil {
		return domain.BrokerOrder{}, err
	}
	if in.Ticker == "" {
		return domain.BrokerOrder{}, &domain.NotAcceptedError{Reason: "no ticker: not sent"}
	}
	q, err := quantity(in.Quantity, in.Fractional)
	if err != nil {
		return domain.BrokerOrder{}, err
	}
	var r request
	switch {
	case in.Side == domain.T212Buy && in.Type == domain.T212Market:
		r = request{op: OpMarketOrder, body: marketBody{Ticker: in.Ticker, Quantity: q, ExtendedHours: extendedHours}}
	case in.Side == domain.T212Sell && in.Type == domain.T212Market:
		r = request{op: OpMarketOrder, body: marketBody{Ticker: in.Ticker, Quantity: -q, ExtendedHours: extendedHours}}
	case in.Side == domain.T212Sell && in.Type == domain.T212StopOrder && in.StopPrice > 0:
		r = request{op: OpStopOrder, body: stopBody{Ticker: in.Ticker, Quantity: -q, StopPrice: float64(in.StopPrice) / 1e6, TimeValidity: stopValidity}}
	default:
		return domain.BrokerOrder{}, &domain.NotAcceptedError{Reason: "not one of the three order shapes (market buy, market sell, sell stop): not sent"}
	}
	if in.Side == domain.T212Sell {
		if err := c.sellable(ctx, in); err != nil {
			return domain.BrokerOrder{}, err
		}
	}
	var w orderWire
	if err := c.mutate(ctx, r, &w); err != nil {
		return domain.BrokerOrder{}, err
	}
	var cv conv
	o := cv.toOrder(w)
	if cv.err != nil || o.ID <= 0 {
		return domain.BrokerOrder{}, fmt.Errorf("trading212 %s: the answer has no usable order: %w", r.op, domain.ErrOutcomeUnknown)
	}
	return o, nil
}

// sellable refuses a sell of more than Trading 212 makes available for trading, or than the
// quantity held net of the sell orders already active in the ticker (no double sell, no
// short), from a fresh read of both. A failed read sends nothing.
func (c *Client) sellable(ctx context.Context, in domain.T212Intent) error {
	var pos []positionWire
	if err := c.read(ctx, request{op: OpPositions}, &pos); err != nil {
		return &domain.NotAcceptedError{Reason: "the positions could not be read before a sell: not sent", Err: err}
	}
	var ws []orderWire
	if err := c.read(ctx, request{op: OpOrders}, &ws); err != nil {
		return &domain.NotAcceptedError{Reason: "the active orders could not be read before a sell: not sent", Err: err}
	}
	var cv conv
	var held, avail, resting domain.Micros
	for _, p := range pos {
		if p.Instrument.Ticker == in.Ticker {
			held += cv.micros(p.Quantity)
			avail += cv.micros(p.QuantityAvailableForTrading)
		}
	}
	for _, w := range ws {
		if o := cv.toOrder(w); o.Ticker == in.Ticker && o.Side == domain.T212Sell {
			resting += max(o.Quantity-o.FilledQuantity, 0)
		}
	}
	switch {
	case cv.err != nil:
		return &domain.NotAcceptedError{Reason: "the positions or orders were unreadable before a sell: not sent", Err: cv.err}
	case in.Quantity > avail:
		return &domain.NotAcceptedError{Reason: fmt.Sprintf("a sell of %s is more than the %s available for trading: not sent", in.Quantity, avail)}
	case in.Quantity+resting > held:
		return &domain.NotAcceptedError{Reason: fmt.Sprintf("a sell of %s with %s already in sell orders is more than the %s held: not sent", in.Quantity, resting, held)}
	}
	return nil
}

// Cancel asks Trading 212 to cancel an active order once (domain.ErrNotFound when it does not
// know it). Accepted is not done (T10): the caller confirms with Order.
func (c *Client) Cancel(ctx context.Context, id int64) error {
	if _, err := practice(ctx); err != nil {
		return err
	}
	if id <= 0 {
		return &domain.NotAcceptedError{Reason: "no order id: not sent"}
	}
	return c.mutate(ctx, request{op: OpCancelOrder, id: id}, nil)
}

// orderPath fills an endpoint's "{id}".
func orderPath(path string, id int64) string {
	return strings.Replace(path, "{id}", strconv.FormatInt(id, 10), 1)
}

// errMutation classifies a mutation's failure: answered and refused (a 4xx but 404, 408 and
// 429) → NotAccepted; 404 → not found; anything that may have been processed → unknown.
func errMutation(op Op, status int, cause error) error {
	switch {
	case status == 404:
		return &domain.NotAcceptedError{Reason: fmt.Sprintf("Trading 212 %s: HTTP 404", op), Err: domain.ErrNotFound}
	case status >= 400 && status < 500 && status != 408 && status != 429:
		return &domain.NotAcceptedError{Reason: (&APIError{Op: op, Status: status}).Error()}
	case status > 0:
		return fmt.Errorf("trading212 %s: HTTP %d: %w", op, status, domain.ErrOutcomeUnknown)
	}
	return fmt.Errorf("trading212 %s: %w: %w", op, domain.ErrOutcomeUnknown, cause)
}
