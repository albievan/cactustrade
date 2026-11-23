// Package etoro is the eToro Public API client. This file is its
// allowlist: the ONLY eToro paths the client may call (CLAUDE.md §2.1). Each operation has a
// demo and a real path, because eToro does not put the "demo" segment in the same place for
// every endpoint. allowlist_test.go fails the build when any other /api/ path appears in this
// package, or when a money, transfer, copy-trading or social path is added here.
//
// Verified against openapi.json v1.419.0 on 2026-11-22 (docs/etoro-api.md).
package etoro

import "github.com/albievan/cactustrade/internal/domain"

// BaseURL is the eToro Public API host.
const BaseURL = "https://public-api.etoro.com"

// WebSocketURL is the eToro streaming endpoint (prices, private order/position updates).
const WebSocketURL = "wss://ws.etoro.com/ws"

// Order constants (CLAUDE.md §2.3). They are deliberately not parameters anywhere: exactly
// two order shapes exist, long = buy + real, short = sellShort + cfd, always leverage 1.
const (
	OrderAction              = "open"
	OrderTransaction         = "buy"       // a long: the real share
	OrderSettlementType      = "real"      // never "realFutures" or "marginTrade"
	OrderTransactionShort    = "sellShort" // a short (owner's decision D19)
	OrderSettlementTypeShort = "cfd"       // a short is a CFD on eToro
	OrderLeverage            = 1           // never leveraged
	OrderType                = "mkt"
	OrderCurrency            = "usd" // the only currency eToro accepts for orders
	StopLossType             = "fixed"
	// RealAssetSettlementTypeID / CFDSettlementTypeID are a position's settlementTypeID for
	// a real share (longs) and a CFD (shorts); any other value after a fill means the
	// position is closed at once and the instrument blocked.
	RealAssetSettlementTypeID = 1
	CFDSettlementTypeID       = 0
)

// orderShape maps a direction to its constant transaction and settlement type. Anything but
// long or short is refused.
func orderShape(d domain.Direction) (transaction, settlement string, ok bool) {
	switch d {
	case domain.Long:
		return OrderTransaction, OrderSettlementType, true
	case domain.Short:
		return OrderTransactionShort, OrderSettlementTypeShort, true
	}
	return "", "", false
}

// Op names an allowlisted eToro operation.
type Op string

// The operations cactusTrade may perform. There is no operation that moves money.
const (
	OpSearch          Op = "search"
	OpScreen          Op = "screen"     // field search with filters (industry, tradability)
	OpIndustries      Op = "industries" // eToro's stock industries
	OpInstruments     Op = "instruments"
	OpExchanges       Op = "exchanges"
	OpRates           Op = "rates"
	OpCandles         Op = "candles"
	OpEligibility     Op = "eligibility"
	OpCosts           Op = "costs"
	OpPortfolio       Op = "portfolio"
	OpOrderLookup     Op = "order_lookup"
	OpCloseOrderInfo  Op = "close_order_info"
	OpTradeHistory    Op = "trade_history"
	OpOpenOrder       Op = "open_order"
	OpCancelOrder     Op = "cancel_order"
	OpClosePosition   Op = "close_position"
	OpSetStopLossTake Op = "set_sl_tp"
)

// QuotaGroup is an eToro rate-limit pool (requests per rolling 60 s, per user key).
type QuotaGroup string

// Quota groups and their documented limits.
const (
	QuotaMarketData  QuotaGroup = "market_data" // 120
	QuotaPortfolio   QuotaGroup = "portfolio"   // 60
	QuotaDefault     QuotaGroup = "default"     // 60
	QuotaExecution   QuotaGroup = "execution"   // 20: open + close + cancel together
	QuotaEligibility QuotaGroup = "eligibility" // 20, dedicated
	QuotaCosts       QuotaGroup = "costs"       // 20, dedicated
)

// QuotaLimits are the documented per-minute limits.
var QuotaLimits = map[QuotaGroup]int{
	QuotaMarketData: 120, QuotaPortfolio: 60, QuotaDefault: 60,
	QuotaExecution: 20, QuotaEligibility: 20, QuotaCosts: 20,
}

// Endpoint is one allowlisted operation.
type Endpoint struct {
	Method string
	Demo   string // path template for demo mode
	Real   string // path template for real mode
	Quota  QuotaGroup
	// Mutates is true for operations that change the account (orders, positions).
	Mutates bool
}

// Path returns the template for the mode; anything but real is demo (fail safe).
func (e Endpoint) Path(m domain.TradingMode) string {
	if m == domain.ModeReal {
		return e.Real
	}
	return e.Demo
}

// Allowlist is the complete set of eToro endpoints the client may call.
var Allowlist = map[Op]Endpoint{
	// Free-text search by name or symbol (v2; same market-data quota as the v1 field search).
	OpSearch:      {Method: "GET", Demo: "/api/v2/market-data/instruments/search", Real: "/api/v2/market-data/instruments/search", Quota: QuotaMarketData},
	OpScreen:      {Method: "GET", Demo: "/api/v1/market-data/search", Real: "/api/v1/market-data/search", Quota: QuotaMarketData},
	OpIndustries:  {Method: "GET", Demo: "/api/v1/market-data/stocks-industries", Real: "/api/v1/market-data/stocks-industries", Quota: QuotaMarketData},
	OpInstruments: {Method: "GET", Demo: "/api/v2/market-data/instruments", Real: "/api/v2/market-data/instruments", Quota: QuotaMarketData},
	OpExchanges:   {Method: "GET", Demo: "/api/v1/market-data/exchanges", Real: "/api/v1/market-data/exchanges", Quota: QuotaMarketData},
	OpRates:       {Method: "GET", Demo: "/api/v2/market-data/rates", Real: "/api/v2/market-data/rates", Quota: QuotaMarketData},
	OpCandles: {
		Method: "GET", Quota: QuotaMarketData,
		Demo: "/api/v1/market-data/instruments/{instrumentId}/history/candles/{direction}/{interval}/{candlesCount}",
		Real: "/api/v1/market-data/instruments/{instrumentId}/history/candles/{direction}/{interval}/{candlesCount}",
	},
	OpEligibility:    {Method: "POST", Demo: "/api/v2/trading/info/demo/eligibility", Real: "/api/v2/trading/info/eligibility", Quota: QuotaEligibility},
	OpCosts:          {Method: "POST", Demo: "/api/v2/trading/info/demo/costs", Real: "/api/v2/trading/info/costs", Quota: QuotaCosts},
	OpPortfolio:      {Method: "GET", Demo: "/api/v1/trading/info/demo/pnl", Real: "/api/v1/trading/info/real/pnl", Quota: QuotaPortfolio},
	OpOrderLookup:    {Method: "GET", Demo: "/api/v2/trading/info/demo/orders:lookup", Real: "/api/v2/trading/info/orders:lookup", Quota: QuotaPortfolio},
	OpCloseOrderInfo: {Method: "GET", Demo: "/api/v1/trading/info/demo/close-orders/{orderId}", Real: "/api/v1/trading/info/real/close-orders/{orderId}", Quota: QuotaPortfolio},
	OpTradeHistory:   {Method: "GET", Demo: "/api/v1/trading/info/trade/demo/history", Real: "/api/v1/trading/info/trade/history", Quota: QuotaDefault},
	OpOpenOrder:      {Method: "POST", Demo: "/api/v2/trading/execution/demo/orders", Real: "/api/v2/trading/execution/orders", Quota: QuotaExecution, Mutates: true},
	OpCancelOrder:    {Method: "DELETE", Demo: "/api/v2/trading/execution/demo/orders/{orderId}", Real: "/api/v2/trading/execution/orders/{orderId}", Quota: QuotaExecution, Mutates: true},
	OpClosePosition: {
		Method: "POST", Quota: QuotaExecution, Mutates: true,
		Demo: "/api/v1/trading/execution/demo/market-close-orders/positions/{positionId}",
		Real: "/api/v1/trading/execution/market-close-orders/positions/{positionId}",
	},
	// Only ever used to set or TIGHTEN a long stop: eToro moves balance into margin when a
	// stop is deepened beyond its maximum.
	OpSetStopLossTake: {Method: "PATCH", Demo: "/api/v2/trading/demo/positions/{positionId}", Real: "/api/v2/trading/positions/{positionId}", Quota: QuotaDefault, Mutates: true},
}
