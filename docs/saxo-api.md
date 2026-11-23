# Saxo OpenAPI — verified facts

Read on **2026-11-23**. cactusTrade uses Saxo **read-only** (owner's decision 2026-11-23:
"Saxo and Tiger, view-only"). A new path needs a reviewed reason and an entry in
`internal/adapters/saxo/allowlist.go`; its test fails on any other path literal, any `/trade/`
path and any mutating HTTP method but the one token POST.

Sources: developer.saxo/openapi/learn/* (environments, OAuth code flow, rate limiting — as
summarised by the owner's brief of 2026-11-23) and Saxo's own public, Apache-2.0 sample
repositories, read with `gh api repos/SaxoBank/<repo>/contents/<path>` (facts only, no code
copied): `openapi-samples-js`, `openapi-samples-php`. The response field names are **not** in
the reference pages; every name used is listed below with where it was seen.

## Hosts

| Environment | cactusTrade mode | REST | Sign-in |
|---|---|---|---|
| SIM (simulation) | demo | `https://gateway.saxobank.com/sim/openapi` | `https://sim.logonvalidation.net` |
| LIVE | real | `https://gateway.saxobank.com/openapi` | `https://live.logonvalidation.net` |

Verified: `openapi-samples-js/assets/js/boilerplate.js` (`authUrl` `https://sim.logonvalidation.net/authorize`,
`apiHost` `gateway.saxobank.com`, `apiPath` `/sim/openapi`, "Change to /openapi when using a Live token");
`openapi-samples-php/authentication/oauth2-code-flow/server-config.php` (`tokenEndpoint`
`https://sim.logonvalidation.net/token`, `openApiBaseUrl` `https://gateway.saxobank.com/sim/openapi`).
App keys differ per environment; a Live app needs Saxo's approval.

## Sign-in (OAuth 2.0 authorization code grant)

- Browser to `{auth}/authorize?client_id=<app key>&response_type=code&state=<state>&redirect_uri=<uri>`
  — parameter names verified in `openapi-samples-php/authentication/oauth2-code-flow/index.php`.
- Code exchange: `POST {auth}/token`, form-encoded, `grant_type=authorization_code`, `code`,
  `client_id`, `client_secret` (verified: `demonstrate-code-flow.php` `getToken`); cactusTrade also
  sends `redirect_uri` (standard OAuth 2.0, as the brief says).
- Refresh: `POST {auth}/token` with `grant_type=refresh_token`, `refresh_token`, `client_id`,
  `client_secret` (verified: `demonstrate-code-flow.php` `refreshToken`).
- Answer fields `access_token`, `expires_in`, `refresh_token`, `refresh_token_expires_in` (verified:
  `demonstrate-code-flow.php`, also `openapi-samples-csharp/.../Models/Token.cs`). The sample warns
  `expires_in` can be negative in development; cactusTrade treats ≤ 0 as "already expired".
- **Rotation:** each refresh returns a new refresh token that replaces the old one. cactusTrade
  stores it (encrypted) *before* using the new access token; if storing fails it uses nothing.
- Lifetimes in Saxo's examples: `expires_in` 1200 s, `refresh_token_expires_in` 2400 s — an
  example, not a documented policy. cactusTrade refreshes early: when the access token has < 5 min
  left or the refresh token < 20 min. The 15-minute equity snapshot keeps a connection alive.
- A refused refresh (HTTP 400/401, e.g. `invalid_grant`) means the sign-in lapsed: the account
  shows "Reconnect to Saxo" and the token is not tried again until a new connect.
- **24-hour developer token** (SIM only): pasted instead of connecting; used as the bearer token;
  refused for a real (LIVE) account and once stored more than 24 hours ago.

The redirect URI to register in the Saxo developer portal is
`<CACTUSTRADE_PUBLIC_URL>/api/v1/oauth/saxo/callback` (shown on the account card).

## Endpoints cactusTrade may call (GET, bearer token)

| Path | Used for |
|---|---|
| `/port/v1/balances/me` | currency, total value, cash |
| `/port/v1/positions/me?FieldGroups=PositionBase,PositionView,DisplayAndFormat&$top=100&$skip=n` | open positions, 100 per page, at most 20 pages |

`/port/v1/positions?FieldGroups=PositionBase,PositionView,DisplayAndFormat` (with a ClientKey) is
verified in `openapi-samples-js/portfolio/position-netting-profiles/demo.js`; the `/me` forms and
`$top`/`$skip` paging are from the reference pages (the samples use `/netpositions/me`,
`/orders/me`, `/accounts/me` in `batch-request/demo.js`).

## Field names: verified vs not

| Field | Status | Where seen | If missing |
|---|---|---|---|
| balances `Currency` | verified | `basics/user-info/demo.js` (`responseJson.Currency`) | account refused (fail closed) |
| balances `TotalValue` | verified | `basics/user-info/demo.js` (`responseJson.TotalValue`) | account refused |
| balances `CurrencyDecimals` | verified, not used | `basics/user-info/demo.js`, `portfolio/margin/demo.js` | — (decimals come from cactusTrade's own table) |
| balances `CashBalance` | **not verified** | reference page only (brief) | account refused |
| balances `UnrealizedPositionsValue` | **not verified**, not used | — | — |
| positions `Data[]` | verified | `position-netting-profiles/demo.js` (`responseJson.Data.forEach`) | account refused |
| `PositionBase.Amount` | verified | `position-netting-profiles/demo.js` | position unknown |
| `PositionBase.AssetType` | verified | same | CFD flag false (CFD = AssetType starting with `Cfd`) |
| `PositionView.MarketValue` | verified | same | position unknown |
| `PositionView.AverageOpenPrice` | verified | same | average price from `OpenPrice`, else blank |
| `DisplayAndFormat.Description`, `.Currency`, `.Symbol`, `.Decimals` | verified | `position-netting-profiles/demo.js`; `portfolio/positions-orders/demo.js` comments (`{"Currency":"USD","Decimals":4,…,"Symbol":"XAGUSD"}`) | symbol `#<Uic>` or `?` |
| `CurrentPrice`, `MarketValueInBaseCurrency`, `ProfitLossOnTrade`, `ProfitLossOnTradeInBaseCurrency` | verified on **NetPositionView** only | `websockets/README.md` (netpositions snapshot) | assumed the same on PositionView; parsed defensively: price blank / position unknown |
| `PositionBase.Uic`, `.OpenPrice`, `.ExecutionTimeOpen` | **not verified** | reference pages only | symbol fallback / blank price / no open date |
| `Exposure` | **not verified**, not used | — | — |

Value and P&L in the account currency: `*InBaseCurrency` when present; else `MarketValue` /
`ProfitLossOnTrade` only when the instrument's currency (`DisplayAndFormat.Currency`) equals the
account currency; otherwise the position is **unknown** (no conversion, no guess). Error bodies:
`ErrorCode` / `ErrorInfo.ErrorCode` (verified: `openapi-samples-php` `processErrorResponse`, the
`"ErrorCode": "InvalidModelState"` example); the sign-in host answers OAuth `error` codes.
cactusTrade keeps only an identifier-shaped code, never the message.

## Rate limits

120 requests per minute per session per service group (reference page). Headers
`X-RateLimit-<Window>-Limit / -Remaining / -Reset`, Reset in **seconds from now** (verified:
`batch-request/batch-request-decomposer/index.html`, e.g. `X-RateLimit-RefDataMinute-Reset: 42`;
`x-ratelimit-appday-remaining` in `openapi-samples-php/error-handling/trigger-errors.php`). The
client spaces each endpoint to 1 request/s per account, holds an endpoint until the reset when a
window reaches 0 or on HTTP 429 (at most a minute), retries once when the wait is ≤ 15 s, and
retries a 5xx once. A 401 drops the cached access token and signs in again once.

## Why not trading

Owner's decision: view-only. The client has no `/trade/` path at all.
