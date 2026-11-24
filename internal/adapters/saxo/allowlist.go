// Package saxo is a READ-ONLY client for the Saxo OpenAPI (owner's decision 2026-11-23:
// "Saxo and Tiger, view-only"). This file is its allowlist: the ONLY Saxo paths the client
// may call. The API host is called with GET on two portfolio paths, never a /trade/ path; the
// only POST goes to the sign-in host's /token (OAuth 2.0 code exchange and refresh).
// allowlist_test.go fails the build on any other path literal, any /trade/ path, or any other
// mutating HTTP method.
//
// Facts verified 2026-11-23 (docs/saxo-api.md).
package saxo

import "time"

// Hosts. A demo account is served by Saxo's simulation environment (SIM), a real one by
// LIVE; app keys differ per environment.
const (
	SimBaseURL  = "https://gateway.saxobank.com/sim/openapi"
	LiveBaseURL = "https://gateway.saxobank.com/openapi"
	SimAuthURL  = "https://sim.logonvalidation.net"
	LiveAuthURL = "https://live.logonvalidation.net"
)

// The sign-in host's paths: the browser goes to AuthorizePath; the client POSTs (form
// encoded) to TokenPath only.
const (
	AuthorizePath = "/authorize"
	TokenPath     = "/token"
)

// Op names an allowlisted Saxo read.
type Op string

// The operations cactusTrade may perform: reading only.
const (
	OpBalances  Op = "balances"
	OpPositions Op = "positions"
)

// Endpoint is one allowlisted GET with the minimum spacing between two requests of the same
// account (Saxo allows 120 requests a minute per session and service group; both paths are
// in the portfolio group, so one per second each stays well under).
type Endpoint struct {
	Path  string
	Every time.Duration
}

// Allowlist is the complete set of Saxo API endpoints the client may call, all GET.
var Allowlist = map[Op]Endpoint{
	OpBalances:  {Path: "/port/v1/balances/me", Every: time.Second},
	OpPositions: {Path: "/port/v1/positions/me", Every: time.Second},
}
