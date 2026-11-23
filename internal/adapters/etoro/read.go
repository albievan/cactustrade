package etoro

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

// Batch limits documented by eToro.
const (
	maxRateIDs        = 1000
	maxEligibilityIDs = 100
	maxInstrumentIDs  = 100
	maxCandles        = 1000
	searchLimit       = 20
	historyPageSize   = 100
	maxHistoryPages   = 50
	maxInstrumentPage = 10
)

// --- conversions at the JSON edge (floats end here) ------------------------------------

func toMicros(f float64) domain.Micros { return domain.Micros(math.Round(f * 1e6)) }
func toCents(f float64) domain.Cents   { return domain.Cents(math.Round(f * 100)) }

func joinIDs(ids []domain.InstrumentID) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(int64(id), 10)
	}
	return strings.Join(parts, ",")
}

// --- market data ---------------------------------------------------------------------

type rateWire struct {
	InstrumentID int64      `json:"instrumentId"`
	Bid          *float64   `json:"bid"`
	Ask          *float64   `json:"ask"`
	Date         *time.Time `json:"date"`
	QuoteType    string     `json:"quoteType"`
}

// Quotes returns the current bid/ask. Instruments eToro has no price for are left out
// (callers treat a missing quote as stale).
func (c *Client) Quotes(ctx context.Context, ids []domain.InstrumentID) ([]domain.Quote, error) {
	var out []domain.Quote
	for chunk := range slices.Chunk(ids, maxRateIDs) {
		var resp struct {
			Results []struct {
				Rate rateWire `json:"rate"`
			} `json:"results"`
		}
		q := url.Values{"instrumentIds": {joinIDs(chunk)}}
		if err := c.call(ctx, OpRates, nil, q, nil, &resp); err != nil {
			return nil, err
		}
		for _, r := range resp.Results {
			w := r.Rate
			if w.Bid == nil || w.Ask == nil || w.Date == nil || *w.Bid <= 0 || *w.Ask <= 0 {
				continue
			}
			out = append(out, domain.Quote{
				Instrument: domain.InstrumentID(w.InstrumentID), Bid: toMicros(*w.Bid), Ask: toMicros(*w.Ask),
				At: w.Date.UTC(), Delayed: !strings.EqualFold(w.QuoteType, "realtime"),
			})
		}
	}
	return out, nil
}

var intervals = map[time.Duration]string{
	time.Minute: "OneMinute", 5 * time.Minute: "FiveMinutes", 10 * time.Minute: "TenMinutes",
	15 * time.Minute: "FifteenMinutes", 30 * time.Minute: "ThirtyMinutes", time.Hour: "OneHour",
	4 * time.Hour: "FourHours", 24 * time.Hour: "OneDay", 7 * 24 * time.Hour: "OneWeek",
}

// Candles returns the latest count bars, oldest first.
func (c *Client) Candles(ctx context.Context, id domain.InstrumentID, interval time.Duration, count int) ([]domain.Candle, error) {
	iv, ok := intervals[interval]
	if !ok {
		return nil, domain.Invalid("interval", "must be 1m, 5m, 10m, 15m, 30m, 1h, 4h, 1d or 1w")
	}
	if count < 1 || count > maxCandles {
		return nil, domain.Invalid("count", "must be 1-1000")
	}
	var resp struct {
		Candles []struct {
			Candles []struct {
				FromDate time.Time `json:"fromDate"`
				Open     float64   `json:"open"`
				High     float64   `json:"high"`
				Low      float64   `json:"low"`
				Close    float64   `json:"close"`
				Volume   float64   `json:"volume"`
			} `json:"candles"`
		} `json:"candles"`
	}
	path := map[string]string{
		"instrumentId": strconv.FormatInt(int64(id), 10), "direction": "desc",
		"interval": iv, "candlesCount": strconv.Itoa(count),
	}
	if err := c.call(ctx, OpCandles, path, nil, nil, &resp); err != nil {
		return nil, err
	}
	var out []domain.Candle
	for _, g := range resp.Candles {
		for _, k := range g.Candles {
			out = append(out, domain.Candle{
				Start: k.FromDate.UTC(), Open: toMicros(k.Open), High: toMicros(k.High),
				Low: toMicros(k.Low), Close: toMicros(k.Close), Volume: int64(math.Round(k.Volume)),
			})
		}
	}
	slices.SortFunc(out, func(a, b domain.Candle) int { return a.Start.Compare(b.Start) })
	return out, nil
}

type instrumentWire struct {
	InstrumentID int64  `json:"instrumentId"`
	DisplayName  string `json:"displayName"`
	Type         string `json:"type"`
	Symbol       string `json:"symbol"`
	ExchangeID   int64  `json:"exchangeId"`
}

func (w instrumentWire) domain() domain.Instrument {
	in := domain.Instrument{
		ID: domain.InstrumentID(w.InstrumentID), Symbol: w.Symbol, Name: w.DisplayName,
		Type: w.Type, ExchangeID: w.ExchangeID,
	}
	if domain.IsShare(w.Type) {
		in.Market, _ = domain.MarketForSymbol(w.Symbol)
	}
	return in
}

// Search finds instruments by name or symbol (up to 20, eToro's relevance order).
func (c *Client) Search(ctx context.Context, query string) ([]domain.Instrument, error) {
	query = strings.TrimSpace(query)
	if n := len([]rune(query)); n < 1 || n > 100 {
		return nil, domain.Invalid("query", "must be 1-100 characters")
	}
	var resp struct {
		Results []instrumentWire `json:"results"`
	}
	q := url.Values{"query": {query}, "limit": {strconv.Itoa(searchLimit)}}
	if err := c.call(ctx, OpSearch, nil, q, nil, &resp); err != nil {
		return nil, err
	}
	out := make([]domain.Instrument, 0, len(resp.Results))
	for _, w := range resp.Results {
		out = append(out, w.domain())
	}
	return out, nil
}

// Instruments returns metadata for the given ids.
func (c *Client) Instruments(ctx context.Context, ids []domain.InstrumentID) ([]domain.Instrument, error) {
	var out []domain.Instrument
	for chunk := range slices.Chunk(ids, maxInstrumentIDs) {
		token := ""
		for range maxInstrumentPage {
			var resp struct {
				Results    []instrumentWire `json:"results"`
				Pagination struct {
					HasNext       bool    `json:"hasNext"`
					NextPageToken *string `json:"nextPageToken"`
				} `json:"pagination"`
			}
			q := url.Values{"instrumentsIds": {joinIDs(chunk)}, "pageSize": {strconv.Itoa(maxInstrumentIDs)}}
			if token != "" {
				q.Set("pageToken", token)
			}
			if err := c.call(ctx, OpInstruments, nil, q, nil, &resp); err != nil {
				return nil, err
			}
			for _, w := range resp.Results {
				out = append(out, w.domain())
			}
			if !resp.Pagination.HasNext || resp.Pagination.NextPageToken == nil || *resp.Pagination.NextPageToken == "" {
				break
			}
			token = *resp.Pagination.NextPageToken
		}
	}
	return out, nil
}

// --- account (read-only) ---------------------------------------------------------------

type positionWire struct {
	PositionID       int64     `json:"positionID"`
	OpenDateTime     time.Time `json:"openDateTime"`
	OpenRate         float64   `json:"openRate"`
	InstrumentID     int64     `json:"instrumentID"`
	IsBuy            bool      `json:"isBuy"`
	TakeProfitRate   float64   `json:"takeProfitRate"`
	StopLossRate     float64   `json:"stopLossRate"`
	Amount           float64   `json:"amount"`
	Leverage         int       `json:"leverage"`
	Units            float64   `json:"units"`
	SettlementTypeID int       `json:"settlementTypeID"`
	UnrealizedPnL    *struct {
		PnL float64 `json:"pnL"`
	} `json:"unrealizedPnL"`
}

type pendingWire struct {
	Amount float64 `json:"amount"`
}

// Portfolio reads cash, positions and money committed to pending orders.
func (c *Client) Portfolio(ctx context.Context) (domain.Portfolio, error) {
	var resp struct {
		ClientPortfolio *struct {
			Credit        *float64       `json:"credit"`
			Positions     []positionWire `json:"positions"`
			Orders        []pendingWire  `json:"orders"`
			OrdersForOpen []pendingWire  `json:"ordersForOpen"`
		} `json:"clientPortfolio"`
	}
	if err := c.call(ctx, OpPortfolio, nil, nil, nil, &resp); err != nil {
		return domain.Portfolio{}, err
	}
	cp := resp.ClientPortfolio
	if cp == nil || cp.Credit == nil {
		// Without the available cash nothing can be sized safely: fail closed.
		return domain.Portfolio{}, fmt.Errorf("etoro %s: no clientPortfolio.credit in the response: %w", OpPortfolio, domain.ErrUnavailable)
	}
	p := domain.Portfolio{AvailableCash: toCents(*cp.Credit), AsOf: c.cfg.Now().UTC()}
	for _, o := range slices.Concat(cp.OrdersForOpen, cp.Orders) {
		p.PendingOpen += toCents(o.Amount)
	}
	for _, w := range cp.Positions {
		pos := domain.Position{
			ID: w.PositionID, Instrument: domain.InstrumentID(w.InstrumentID), OpenedAt: w.OpenDateTime.UTC(),
			OpenRate: toMicros(w.OpenRate), Units: toMicros(w.Units), Amount: toCents(w.Amount),
			StopLossRate: toMicros(w.StopLossRate), TakeProfitRate: toMicros(w.TakeProfitRate),
			RealAsset: w.SettlementTypeID == RealAssetSettlementTypeID, Long: w.IsBuy, Leverage: w.Leverage,
		}
		if w.UnrealizedPnL != nil {
			pos.UnrealisedPnL = toCents(w.UnrealizedPnL.PnL)
		}
		p.Positions = append(p.Positions, pos)
	}
	return p, nil
}

type leverageConfigWire struct {
	SettlementType     string  `json:"settlementType"`
	Direction          string  `json:"direction"`
	LeverageValues     []int   `json:"leverageValues"`
	IsPotential        bool    `json:"isPotential"`
	MinPositionAmount  float64 `json:"minPositionAmount"`
	AllowEditStopLoss  bool    `json:"allowEditStopLoss"`
	AllowStopLossTakeP bool    `json:"allowStopLossTakeProfit"`
}

// realLongConfig finds the configuration for a real (non-CFD) long position at leverage 1.
// isPotential entries are not offered yet, so they do not count (fail closed).
func realLongConfig(cfgs []leverageConfigWire) (leverageConfigWire, bool) {
	for _, lc := range cfgs {
		dir := strings.ToLower(lc.Direction)
		if strings.EqualFold(lc.SettlementType, OrderSettlementType) && (dir == "long" || dir == "buy") &&
			slices.Contains(lc.LeverageValues, OrderLeverage) && !lc.IsPotential {
			return lc, true
		}
	}
	return leverageConfigWire{}, false
}

// Eligibility asks eToro whether each instrument can be opened as a real, long, x1 share.
func (c *Client) Eligibility(ctx context.Context, ids []domain.InstrumentID) ([]domain.Eligibility, error) {
	now := c.cfg.Now().UTC()
	var out []domain.Eligibility
	for chunk := range slices.Chunk(ids, maxEligibilityIDs) {
		var resp struct {
			Eligibilities []struct {
				InstrumentID      int64                `json:"instrumentId"`
				Symbol            string               `json:"symbol"`
				AllowOpenPosition bool                 `json:"allowOpenPosition"`
				LeverageConfigs   []leverageConfigWire `json:"leverageConfigs"`
			} `json:"eligibilities"`
			NotFound []int64 `json:"notFoundInstrumentIds"`
		}
		body := map[string]any{"instrumentIds": chunk}
		if err := c.call(ctx, OpEligibility, nil, nil, body, &resp); err != nil {
			return nil, err
		}
		for _, e := range resp.Eligibilities {
			el := domain.Eligibility{Instrument: domain.InstrumentID(e.InstrumentID), Symbol: e.Symbol, CheckedAt: now}
			lc, ok := realLongConfig(e.LeverageConfigs)
			switch {
			case !e.AllowOpenPosition:
				el.Reason = "eToro does not allow opening positions in this instrument"
			case !ok:
				el.Reason = "not offered as a real share, long, at leverage 1"
			default:
				el.RealLong, el.MinPositionAmount = true, toCents(lc.MinPositionAmount)
			}
			out = append(out, el)
		}
		for _, id := range resp.NotFound {
			out = append(out, domain.Eligibility{Instrument: domain.InstrumentID(id), Reason: "instrument not found", CheckedAt: now})
		}
	}
	return out, nil
}

// costRequest is a hypothetical buy built ONLY from the guardrail constants: nothing here
// can ask for leverage, a short or a CFD.
type costRequest struct {
	Action         string  `json:"action"`
	Transaction    string  `json:"transaction"`
	InstrumentID   int64   `json:"instrumentId"`
	SettlementType string  `json:"settlementType"`
	OrderType      string  `json:"orderType"`
	Leverage       int     `json:"leverage"`
	Amount         float64 `json:"amount"`
	OrderCurrency  string  `json:"orderCurrency"`
}

// Costs is eToro's cost breakdown for buying amount (USD cents) of id.
func (c *Client) Costs(ctx context.Context, id domain.InstrumentID, amount domain.Cents) ([]domain.CostItem, error) {
	if amount <= 0 {
		return nil, domain.Invalid("amount", "must be positive")
	}
	body := costRequest{
		Action: OrderAction, Transaction: OrderTransaction, InstrumentID: int64(id),
		SettlementType: OrderSettlementType, OrderType: OrderType, Leverage: OrderLeverage,
		Amount: float64(amount) / 100, OrderCurrency: OrderCurrency,
	}
	var resp struct {
		Costs []struct {
			CostType string  `json:"costType"`
			Value    float64 `json:"value"`
			Currency string  `json:"currency"`
		} `json:"costs"`
	}
	if err := c.call(ctx, OpCosts, nil, nil, body, &resp); err != nil {
		return nil, err
	}
	out := make([]domain.CostItem, 0, len(resp.Costs))
	for _, k := range resp.Costs {
		out = append(out, domain.CostItem{Type: k.CostType, Value: toMicros(k.Value), Currency: k.Currency})
	}
	return out, nil
}

// TradeHistory lists positions closed since the given day.
func (c *Client) TradeHistory(ctx context.Context, since time.Time) ([]domain.ClosedTrade, error) {
	var out []domain.ClosedTrade
	for page := 1; page <= maxHistoryPages; page++ {
		var resp []struct {
			NetProfit      float64   `json:"netProfit"`
			CloseRate      float64   `json:"closeRate"`
			CloseTimestamp time.Time `json:"closeTimestamp"`
			PositionID     int64     `json:"positionId"`
			InstrumentID   int64     `json:"instrumentId"`
			IsBuy          bool      `json:"isBuy"`
			Leverage       int       `json:"leverage"`
			OpenRate       float64   `json:"openRate"`
			OpenTimestamp  time.Time `json:"openTimestamp"`
		}
		q := url.Values{
			"minDate": {since.UTC().Format(time.DateOnly)}, "page": {strconv.Itoa(page)},
			"pageSize": {strconv.Itoa(historyPageSize)},
		}
		if err := c.call(ctx, OpTradeHistory, nil, q, nil, &resp); err != nil {
			return nil, err
		}
		for _, t := range resp {
			out = append(out, domain.ClosedTrade{
				PositionID: t.PositionID, Instrument: domain.InstrumentID(t.InstrumentID),
				OpenedAt: t.OpenTimestamp.UTC(), ClosedAt: t.CloseTimestamp.UTC(),
				OpenRate: toMicros(t.OpenRate), CloseRate: toMicros(t.CloseRate),
				NetProfit: toCents(t.NetProfit), Long: t.IsBuy, Leverage: t.Leverage,
			})
		}
		if len(resp) < historyPageSize {
			break
		}
	}
	return out, nil
}

// eToro order status ids (orders:lookup).
var orderStates = map[int]domain.OrderState{
	1: domain.OrderPending, 2: domain.OrderPending, 11: domain.OrderPending, 12: domain.OrderPending,
	3: domain.OrderFilled, 5: domain.OrderPartial, 10: domain.OrderPartial, 4: domain.OrderRejected,
}

// Lookup finds an order by the x-request-id it was sent with (eToro's referenceId).
func (c *Client) Lookup(ctx context.Context, requestID string) (domain.OrderStatus, error) {
	if requestID == "" {
		return domain.OrderStatus{}, domain.Invalid("requestId", "is required")
	}
	var resp struct {
		OrderID int64 `json:"orderId"`
		Status  struct {
			ID           int     `json:"id"`
			Name         string  `json:"name"`
			ErrorMessage *string `json:"errorMessage"`
		} `json:"status"`
		PositionExecutions []struct {
			PositionID int64 `json:"positionId"`
		} `json:"positionExecutions"`
	}
	if err := c.call(ctx, OpOrderLookup, nil, url.Values{"referenceId": {requestID}}, nil, &resp); err != nil {
		return domain.OrderStatus{RequestID: requestID, State: domain.OrderUnknown}, err
	}
	st := domain.OrderStatus{RequestID: requestID, OrderID: resp.OrderID, State: domain.OrderUnknown, Reason: resp.Status.Name}
	if s, ok := orderStates[resp.Status.ID]; ok {
		st.State = s
	}
	if resp.Status.ErrorMessage != nil && *resp.Status.ErrorMessage != "" {
		st.Reason = *resp.Status.ErrorMessage
	}
	for _, pe := range resp.PositionExecutions {
		st.PositionIDs = append(st.PositionIDs, pe.PositionID)
	}
	return st, nil
}

// --- account changes: phase 4 ------------------------------------------------------------

// Open is not enabled before phase 4 (demo orders).
func (c *Client) Open(context.Context, domain.OrderIntent) (domain.OrderStatus, error) {
	return domain.OrderStatus{}, fmt.Errorf("etoro %s: %w", OpOpenOrder, ErrNotEnabled)
}

// Cancel is not enabled before phase 4.
func (c *Client) Cancel(context.Context, int64) error {
	return fmt.Errorf("etoro %s: %w", OpCancelOrder, ErrNotEnabled)
}

// Close is not enabled before phase 4.
func (c *Client) Close(context.Context, int64, domain.InstrumentID) (domain.OrderStatus, error) {
	return domain.OrderStatus{}, fmt.Errorf("etoro %s: %w", OpClosePosition, ErrNotEnabled)
}
