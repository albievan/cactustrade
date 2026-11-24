package etoro

import (
	"context"
	"encoding/json"
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
			RealAsset: w.SettlementTypeID == RealAssetSettlementTypeID, CFD: w.SettlementTypeID == CFDSettlementTypeID,
			Long: w.IsBuy, Leverage: w.Leverage,
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

// leverageConfig finds the configuration for an unleveraged position: settlement "real" and
// direction long for a long, settlement "cfd" and direction short for a short. isPotential
// entries are not offered yet, so they do not count (fail closed).
func leverageConfig(cfgs []leverageConfigWire, settlement string, dirs ...string) (leverageConfigWire, bool) {
	for _, lc := range cfgs {
		if strings.EqualFold(lc.SettlementType, settlement) && slices.Contains(dirs, strings.ToLower(lc.Direction)) &&
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
			lc, okLong := leverageConfig(e.LeverageConfigs, OrderSettlementType, "long", "buy")
			sc, okShort := leverageConfig(e.LeverageConfigs, OrderSettlementTypeShort, "short", "sell")
			switch {
			case !e.AllowOpenPosition:
				el.Reason = "eToro does not allow opening positions in this instrument"
				el.ShortReason = el.Reason
			default:
				if okLong {
					el.RealLong, el.MinPositionAmount = true, toCents(lc.MinPositionAmount)
				} else {
					el.Reason = "not offered as a real share, long, at leverage 1"
				}
				if okShort {
					el.ShortCFD, el.MinShortAmount = true, toCents(sc.MinPositionAmount)
				} else {
					el.ShortReason = "not offered as a CFD short at leverage 1"
				}
			}
			out = append(out, el)
		}
		for _, id := range resp.NotFound {
			out = append(out, domain.Eligibility{Instrument: domain.InstrumentID(id), Reason: "instrument not found", ShortReason: "instrument not found", CheckedAt: now})
		}
	}
	return out, nil
}

// costRequest is a hypothetical order built ONLY from the guardrail constants (one of the two
// order shapes): nothing here can ask for leverage.
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

// Costs is eToro's cost breakdown for opening amount (USD cents) of id in direction d.
func (c *Client) Costs(ctx context.Context, id domain.InstrumentID, d domain.Direction, amount domain.Cents) ([]domain.CostItem, error) {
	if amount <= 0 {
		return nil, domain.Invalid("amount", "must be positive")
	}
	tr, st, ok := orderShape(d)
	if !ok {
		return nil, domain.Invalid("direction", "long or short")
	}
	body := costRequest{
		Action: OrderAction, Transaction: tr, InstrumentID: int64(id),
		SettlementType: st, OrderType: OrderType, Leverage: OrderLeverage,
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

// historyWindow is the longest lookback one trade-history request may have: eToro allows
// "1 year minus 1 day" (364 days, also in a leap year).
const historyWindow = 364 * 24 * time.Hour

// tradeWire is one closed trade of eToro's trade history. Pointers mark the fields whose
// absence matters: a missing fees figure is unknown, never 0.
type tradeWire struct {
	NetProfit         float64   `json:"netProfit"`
	CloseRate         float64   `json:"closeRate"`
	CloseTimestamp    time.Time `json:"closeTimestamp"`
	PositionID        int64     `json:"positionId"`
	InstrumentID      int64     `json:"instrumentId"`
	IsBuy             bool      `json:"isBuy"`
	Leverage          int       `json:"leverage"`
	OpenRate          float64   `json:"openRate"`
	OpenTimestamp     time.Time `json:"openTimestamp"`
	StopLossRate      float64   `json:"stopLossRate"`
	TakeProfitRate    float64   `json:"takeProfitRate"`
	OrderID           int64     `json:"orderId"`
	Investment        *float64  `json:"investment"`
	InitialInvestment *float64  `json:"initialInvestment"`
	Fees              *float64  `json:"fees"`
	Units             float64   `json:"units"`
}

func (w tradeWire) domain() domain.ClosedTrade {
	t := domain.ClosedTrade{
		PositionID: w.PositionID, Instrument: domain.InstrumentID(w.InstrumentID),
		OpenedAt: w.OpenTimestamp.UTC(), ClosedAt: w.CloseTimestamp.UTC(),
		OpenRate: toMicros(w.OpenRate), CloseRate: toMicros(w.CloseRate),
		NetProfit: toCents(w.NetProfit), Long: w.IsBuy, Leverage: w.Leverage,
		Units: toMicros(w.Units), OrderID: w.OrderID,
		StopLoss: toMicros(w.StopLossRate), TakeProfit: toMicros(w.TakeProfitRate),
	}
	if w.Fees != nil {
		t.Fees, t.FeesKnown = toCents(*w.Fees), true
	}
	switch {
	case w.InitialInvestment != nil && *w.InitialInvestment > 0:
		t.Investment = toCents(*w.InitialInvestment)
	case w.Investment != nil:
		t.Investment = toCents(*w.Investment)
	}
	return t
}

// TradeHistory lists the positions closed since the given day (read-only). eToro serves at
// most a 364-day lookback per request, so a longer range is read in successive windows,
// each from its own minDate and at most maxHistoryPages pages of historyPageSize. A trade is
// kept by the window its close falls in (and once, by position id), whether eToro answers a
// window up to its end or up to now.
func (c *Client) TradeHistory(ctx context.Context, since time.Time) ([]domain.ClosedTrade, error) {
	start := since.UTC().Truncate(24 * time.Hour)
	now := c.cfg.Now().UTC()
	seen := map[int64]bool{}
	var out []domain.ClosedTrade
	for from := start; ; from = from.Add(historyWindow) {
		until := from.Add(historyWindow)
		last := !until.Before(now)
		for page := 1; page <= maxHistoryPages; page++ {
			var resp []tradeWire
			q := url.Values{
				"minDate": {from.Format(time.DateOnly)}, "page": {strconv.Itoa(page)},
				"pageSize": {strconv.Itoa(historyPageSize)},
			}
			if err := c.call(ctx, OpTradeHistory, nil, q, nil, &resp); err != nil {
				return nil, err
			}
			for _, w := range resp {
				t := w.domain()
				if seen[t.PositionID] || (!last && !t.ClosedAt.Before(until)) {
					continue
				}
				seen[t.PositionID] = true
				out = append(out, t)
			}
			if len(resp) < historyPageSize {
				break
			}
		}
		if last {
			break
		}
	}
	return out, nil
}

// eToro order status ids (orders:lookup and close-orders): 1 Received, 2 Placed,
// 3 Filled, 4 Rejected, 5 PartiallyFilled, 6 PendingCancel, 7 Canceled, 8 Expired,
// 9 CanceledPartiallyFilled, 10 RejectedPartiallyFilled, 11 WaitingForMarket,
// 12 PendingTriggeredRate. Any other id is unknown (fail closed).
var orderStates = map[int]domain.OrderState{
	1: domain.OrderPending, 2: domain.OrderPending, 6: domain.OrderPending, 11: domain.OrderPending, 12: domain.OrderPending,
	3: domain.OrderFilled, 4: domain.OrderRejected, 5: domain.OrderPartial, 7: domain.OrderCancelled, 8: domain.OrderCancelled,
	9: domain.OrderPartial, 10: domain.OrderPartial,
}

// finalStatus are the terminal ids: the order can no longer execute.
var finalStatus = map[int]bool{3: true, 4: true, 7: true, 8: true, 9: true, 10: true}

// side reads eToro's asset side ("long"/"short"; "buy"/"sell" accepted too).
func side(s string) domain.Direction {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "long", "buy":
		return domain.Long
	case "short", "sell", "sellshort":
		return domain.Short
	}
	return ""
}

// lookupWire is eToro's orders:lookup answer, as far as cactusTrade reads it.
type lookupWire struct {
	OrderID int64 `json:"orderId"`
	Status  struct {
		ID           int     `json:"id"`
		Name         string  `json:"name"`
		ErrorMessage *string `json:"errorMessage"`
	} `json:"status"`
	Asset struct {
		InstrumentID   int64  `json:"instrumentId"`
		SettlementType string `json:"settlementType"`
		Leverage       int    `json:"leverage"`
		Side           string `json:"side"`
	} `json:"asset"`
	PositionExecutions []struct {
		PositionID int64  `json:"positionId"`
		State      string `json:"state"`
		// The fill's amount in the account currency: initialExposureAccountCurrency, else
		// marginAccountCurrency (the same at leverage 1), else investedAmountCurrency, which
		// is not the invested amount (it was 1 for a $100 position, 2026-11-24).
		Exposure       *float64 `json:"initialExposureAccountCurrency"`
		Margin         *float64 `json:"marginAccountCurrency"`
		Invested       *float64 `json:"investedAmountCurrency"`
		RemainingUnits float64  `json:"remainingUnits"`
		StopLossRate   *float64 `json:"stopLossRate"`
		OpeningData    struct {
			Units    *float64 `json:"units"`
			AvgPrice float64  `json:"avgPrice"`
		} `json:"openingData"`
	} `json:"positionExecutions"`
}

// firstPositive returns the first of the values that is present and above 0 (0 if none).
func firstPositive(vs ...*float64) float64 {
	for _, v := range vs {
		if v != nil && *v > 0 {
			return *v
		}
	}
	return 0
}

// Lookup finds an open order with what eToro actually opened: the asset's settlement type,
// leverage and side, and each resulting position with its units, amount, average price,
// stop-loss and whether eToro already closed it. With orderID > 0 it asks by eToro's order id
// (orderId=), else by the x-request-id it was sent with (referenceId=): on 2026-11-24 the
// demo answered 404 by referenceId for two filled orders and found them by orderId.
func (c *Client) Lookup(ctx context.Context, requestID string, orderID int64) (domain.OrderStatus, error) {
	st, _, err := c.LookupWithRaw(ctx, requestID, orderID)
	return st, err
}

// LookupWithRaw is Lookup that also returns eToro's answer exactly as received (read only),
// for the operator's diagnosis (cactustrade etoro-lookup). The answer holds order data only,
// never a key.
func (c *Client) LookupWithRaw(ctx context.Context, requestID string, orderID int64) (domain.OrderStatus, json.RawMessage, error) {
	q := url.Values{}
	switch {
	case orderID > 0:
		q.Set("orderId", strconv.FormatInt(orderID, 10))
	case requestID != "":
		q.Set("referenceId", requestID)
	default:
		return domain.OrderStatus{}, nil, domain.Invalid("requestId", "a request id or an eToro order id is required")
	}
	failed := domain.OrderStatus{RequestID: requestID, OrderID: orderID, State: domain.OrderUnknown}
	var raw json.RawMessage
	if err := c.call(ctx, OpOrderLookup, nil, q, nil, &raw); err != nil {
		return failed, nil, err
	}
	var resp lookupWire
	if err := json.Unmarshal(raw, &resp); err != nil {
		return failed, raw, fmt.Errorf("etoro %s: decoding the answer: %w: %w", OpOrderLookup, domain.ErrUnavailable, err)
	}
	if orderID > 0 && resp.OrderID != orderID {
		return failed, raw, fmt.Errorf("etoro %s: asked for order %d, eToro answered about order %d: %w", OpOrderLookup, orderID, resp.OrderID, domain.ErrUnavailable)
	}
	st := domain.OrderStatus{
		RequestID: requestID, OrderID: resp.OrderID, State: domain.OrderUnknown, Reason: resp.Status.Name,
		Instrument: domain.InstrumentID(resp.Asset.InstrumentID), Settlement: domain.Settlement(strings.ToLower(resp.Asset.SettlementType)),
		Leverage: resp.Asset.Leverage, Side: side(resp.Asset.Side),
	}
	if s, ok := orderStates[resp.Status.ID]; ok {
		st.State, st.Final = s, finalStatus[resp.Status.ID]
	}
	if resp.Status.ErrorMessage != nil && *resp.Status.ErrorMessage != "" {
		st.Reason = *resp.Status.ErrorMessage
	}
	for _, pe := range resp.PositionExecutions {
		units := pe.RemainingUnits
		if pe.OpeningData.Units != nil {
			units = *pe.OpeningData.Units
		}
		e := domain.PositionExecution{
			PositionID: pe.PositionID, Units: toMicros(units), Amount: toCents(firstPositive(pe.Exposure, pe.Margin, pe.Invested)),
			OpenRate: toMicros(pe.OpeningData.AvgPrice), Closed: strings.EqualFold(strings.TrimSpace(pe.State), "closed"),
		}
		if pe.StopLossRate != nil {
			e.StopLossRate = toMicros(*pe.StopLossRate)
		}
		st.Executions = append(st.Executions, e)
	}
	return st, raw, nil
}
