// Package tiger is a READ-ONLY client for the Tiger Brokers Open API (owner's decision
// 2026-11-23: "Saxo and Tiger, view-only"). Every Tiger call is a signed POST of an envelope
// to one gateway, so this allowlist is by method name: the client may send only the methods
// below, all of which read. There is no order method (place_order, modify_order,
// cancel_order…) and no fund transfer; allowlist_test.go fails the build on any other method
// name or URL literal in this package.
//
// The gateway is a constant. Tiger's SDK can instead fetch the server address at run time from
// another domain ("domain garden"); cactusTrade does not, because a remotely supplied address
// could redirect signed requests (docs/tiger-api.md).
//
// Facts read 2026-11-23 from docs.itigerup.com and the MIT-licensed
// github.com/tigerfintech/openapi-go-sdk (not imported: our own implementation).
package tiger

import "time"

// GatewayURL is Tiger's Open API gateway (paper and live accounts alike; the account number
// says which).
const GatewayURL = "https://openapi.tigerfintech.com/gateway"

// Method is an allowlisted Tiger API method.
type Method string

// The methods cactusTrade may call: reading only.
const (
	MethodPrimeAssets Method = "prime_assets" // assets of a prime / paper account
	MethodAssets      Method = "assets"       // the older per-account assets, the fallback
	MethodPositions   Method = "positions"
)

// Allowlist is the complete set of methods with the minimum spacing between two calls of the
// same account (Tiger allows 60 a minute for these).
var Allowlist = map[Method]time.Duration{
	MethodPrimeAssets: time.Second,
	MethodAssets:      time.Second,
	MethodPositions:   time.Second,
}
