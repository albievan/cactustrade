// Package trading212 is a READ-ONLY client for the Trading 212 Public API (v0, beta). This
// file is its allowlist: the ONLY Trading 212 paths the client may call, all of them GET.
// There is no order, pie or export path, and the client has no method that sends anything
// but a GET (owner's decision 2026-11-23: Trading 212 accounts are view-only for now).
// allowlist_test.go fails the build when any other /api/ or /equity/ path literal, or any
// other HTTP method, appears in this package.
//
// Verified against docs.trading212.com on 2026-11-23 (docs/trading212-api.md).
package trading212

import "time"

// Hosts. A demo (practice) account is served by the demo host, a real one by the live host;
// each key works for its own environment only.
const (
	DemoBaseURL = "https://demo.trading212.com"
	LiveBaseURL = "https://live.trading212.com"
)

// Op names an allowlisted Trading 212 operation.
type Op string

// The operations cactusTrade may perform: reading only.
const (
	OpAccountSummary Op = "account_summary"
	OpPositions      Op = "positions"
	// OpOrderHistory is allowlisted (read-only) for the realised P&L history; not called yet.
	OpOrderHistory Op = "order_history"
)

// Endpoint is one allowlisted GET with its documented rate limit, kept as the minimum
// spacing between two requests of the same account.
type Endpoint struct {
	Path  string
	Every time.Duration
}

// Allowlist is the complete set of Trading 212 endpoints the client may call. Every one is
// a GET; the client cannot send any other method.
var Allowlist = map[Op]Endpoint{
	OpAccountSummary: {Path: "/api/v0/equity/account/summary", Every: 5 * time.Second}, // 1 req / 5 s
	OpPositions:      {Path: "/api/v0/equity/positions", Every: time.Second},           // 1 req / 1 s
	OpOrderHistory:   {Path: "/api/v0/equity/history/orders", Every: 3 * time.Second},  // 20 req / min
}
