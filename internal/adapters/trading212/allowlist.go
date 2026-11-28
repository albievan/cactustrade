// Package trading212 is the client for the Trading 212 Public API (v0, beta). This file is
// its allowlist: the ONLY Trading 212 methods and paths the client may use (CLAUDE.md §2.1,
// D41, D43). Reads: the account summary, the positions, the order history, the active orders,
// one order and the instrument metadata. Changes, on practice accounts only: a market order, a
// stop order and the cancel of an order. There is no limit, stop-limit, pie, export or money
// path. allowlist_test.go fails the build when any other path literal, or any HTTP method
// outside this file, appears in this package.
//
// Verified against docs.trading212.com on 2026-11-23 and 2026-11-28 (docs/trading212-api.md).
package trading212

import (
	"net/http"
	"time"
)

// Hosts. A demo (practice) account is served by the demo host, a real one by the live host;
// each key works for its own environment only. Orders go to the demo host only.
const (
	DemoBaseURL = "https://demo.trading212.com"
	LiveBaseURL = "https://live.trading212.com"
)

// Op names an allowlisted Trading 212 operation.
type Op string

// The operations cactusTrade may perform.
const (
	OpAccountSummary Op = "account_summary"
	OpPositions      Op = "positions"
	OpOrderHistory   Op = "order_history"
	OpInstruments    Op = "instruments"
	OpOrders         Op = "orders"
	OpOrder          Op = "order"
	// The three changes (practice accounts only, PLAN R45.8).
	OpMarketOrder Op = "market_order"
	OpStopOrder   Op = "stop_order"
	OpCancelOrder Op = "cancel_order"
)

// Endpoint is one allowlisted request: its method, its path ("{id}" stands for an order id)
// and its documented rate limit, kept as the minimum spacing between two requests of the same
// account.
type Endpoint struct {
	Method string
	Path   string
	Every  time.Duration
	// Mutates: the request changes the account (sent once, never retried, practice only).
	Mutates bool
}

// Allowlist is the complete set of Trading 212 requests the client may send: exactly 9.
var Allowlist = map[Op]Endpoint{
	OpAccountSummary: {Method: http.MethodGet, Path: "/api/v0/equity/account/summary", Every: 5 * time.Second},                       // 1 / 5 s
	OpPositions:      {Method: http.MethodGet, Path: "/api/v0/equity/positions", Every: time.Second},                                 // 1 / 1 s
	OpOrderHistory:   {Method: http.MethodGet, Path: "/api/v0/equity/history/orders", Every: 3 * time.Second},                        // 20 / min
	OpInstruments:    {Method: http.MethodGet, Path: "/api/v0/equity/metadata/instruments", Every: 50 * time.Second},                 // 1 / 50 s
	OpOrders:         {Method: http.MethodGet, Path: "/api/v0/equity/orders", Every: 5 * time.Second},                                // 1 / 5 s
	OpOrder:          {Method: http.MethodGet, Path: "/api/v0/equity/orders/{id}", Every: time.Second},                               // 1 / 1 s
	OpMarketOrder:    {Method: http.MethodPost, Path: "/api/v0/equity/orders/market", Every: 1200 * time.Millisecond, Mutates: true}, // 50 / min
	OpStopOrder:      {Method: http.MethodPost, Path: "/api/v0/equity/orders/stop", Every: 2 * time.Second, Mutates: true},           // 1 / 2 s
	OpCancelOrder:    {Method: http.MethodDelete, Path: "/api/v0/equity/orders/{id}", Every: 1200 * time.Millisecond, Mutates: true}, // 50 / min
}
