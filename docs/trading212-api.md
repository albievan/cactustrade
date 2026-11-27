# Trading 212 Public API — verified facts

Verified at docs.trading212.com on **2026-11-23** (API v0, beta). cactusTrade uses it
**read-only** (owner's decision 2026-11-23: view-only now, trading later). Re-verify before
adding anything; a new path needs a reviewed reason and an entry in
`internal/adapters/trading212/allowlist.go` (its test fails on any other path or method).

## Hosts and authentication

| Environment | Base URL |
|---|---|
| Demo (practice) | `https://demo.trading212.com/api/v0` |
| Live (real) | `https://live.trading212.com/api/v0` |

HTTP Basic: the **API key** is the user name, the **API secret** the password. A key belongs to
one environment. Keys are created in the app: Settings → API (Beta) → Generate API key, with
per-scope permissions (cactusTrade needs only account and portfolio; history is optional) and an
optional IP restriction.

## Endpoints cactusTrade may call (all GET)

| Path | Limit | Used for |
|---|---|---|
| `/equity/account/summary` | 1 request / 5 s | cash, total value, invested, P&L, currency |
| `/equity/positions` | 1 request / 1 s | open positions |
| `/equity/history/orders?limit≤50&cursor=` | 20 / min | allowlisted, not called yet (realised P&L history) |

`account/summary` → `{"cash":{"availableToTrade","inPies","reservedForOrders"},"currency":"GBP","id":int64,
"investments":{"currentValue","realizedProfitLoss","totalCost","unrealizedProfitLoss"},"totalValue"}`.

`positions` → array of `{"averagePricePaid","createdAt","currentPrice","instrument":{"currency","isin","name",
"ticker":"AAPL_US_EQ"},"quantity","quantityAvailableForTrading","quantityInPies","walletImpact":{"currency",
"currentValue","fxImpact"|null,"totalCost","unrealizedProfitLoss"}}`.

`history/orders` → `{"items":[{"fill":{…,"walletImpact":{"realisedProfitLoss",…}},"order":{…}}],"nextPagePath":str|null}`.

## Errors and rate limits

401, 403, 408 and 429 have no documented body. Headers `x-ratelimit-remaining` and
`x-ratelimit-reset` (Unix seconds). On a 429 cactusTrade holds the endpoint until the reset (at
most a minute) and retries a GET once if the wait is ≤ 15 s; it never retries anything else
(there is nothing else).

## Money

Amounts are in the **account's own currency** (often GBP or EUR), not USD; prices are in the
instrument's currency (e.g. GBX for London shares). cactusTrade converts floats to minor units
(hundredths, round half away from zero) and price/quantity to millionths at the JSON edge, and
never adds amounts in different currencies.

## Why not trading (yet)

No shorts/CFDs, no idempotency key on orders, no market data, no stop-loss on a market order:
the guardrails of `CLAUDE.md` §2 cannot be met as for eToro, so trading needs rules agreed with
the owner first.

## Orders (researched 2026-11-27, not used — R45)

`POST /equity/orders/market` `{ticker, quantity, extendedHours}` (50/min), `/limit` (`limitPrice`),
`/stop` (`stopPrice`), `/stop_limit` (`stopPrice`, `limitPrice`, `timeValidity` DAY|GOOD_TILL_CANCEL);
a sell is a negative quantity; `DELETE /equity/orders/{id}` cancels (not guaranteed mid-fill); no
close endpoint (sell the quantity). Key scope `orders:execute`; Invest and Stocks ISA only. **Not
idempotent** (no client order id; a repeated request may duplicate an order). No SL/TP on a market
order. No market data. `GET /equity/metadata/instruments` (1/50 s) gives ticker, ISIN, currency,
`maxOpenQuantity`; no minimum order value. No watchlist endpoint.
