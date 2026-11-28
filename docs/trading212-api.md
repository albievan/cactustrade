# Trading 212 Public API — verified facts

Verified at docs.trading212.com on **2026-11-23** (API v0, beta). cactusTrade reads every
Trading 212 account and, since R45.8 (2026-11-28, D41–D44), may send the three order shapes and
cancels to **practice (demo) accounts only**; a real Trading 212 account stays read-only. Re-verify before
adding anything; a new path needs a reviewed reason and an entry in
`internal/adapters/trading212/allowlist.go` (its test fails on any other path or method).

## Hosts and authentication

| Environment | Base URL |
|---|---|
| Demo (practice) | `https://demo.trading212.com/api/v0` |
| Live (real) | `https://live.trading212.com/api/v0` |

HTTP Basic: the **API key** is the user name, the **API secret** the password. A key belongs to
one environment. Keys are created in the app: Settings → API (Beta) → Generate API key, with
per-scope permissions and an optional IP restriction. A **real** account's key needs only
account and portfolio (read-only, the overview). A **practice** account that cactusTrade trades
needs account, portfolio, metadata, orders:read, history:orders **and orders:execute**.

## Account reads (all GET; any Trading 212 account)

| Path | Limit | Used for |
|---|---|---|
| `/equity/account/summary` | 1 request / 5 s | cash, total value, invested, P&L, currency |
| `/equity/positions` | 1 request / 1 s | open positions |
| `/equity/history/orders?limit≤50&cursor=` | 20 / min | fills and order matching (R45; practice accounts) |

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

## Why only practice accounts, and how

No shorts/CFDs, no idempotency key on orders, no market data, no stop-loss on a market order:
the guardrails of `CLAUDE.md` §2 cannot be met as for eToro. The owner agreed the rules for
Trading 212 in writing (D41–D44, 2026-11-28; CLAUDE.md §2 amended). cactusTrade trades
**practice accounts only** (R45, ADR-008): wired since R45.10; the owner's practice run
(R45.13) settles the open points below. Real Trading 212 accounts stay view-only.

## Orders (re-verified 2026-11-28 at docs.trading212.com, API v0 beta — used by R45)

Trading under D41–D44 (PLAN R45). R45.8 (2026-11-28) put exactly these requests in
`allowlist.go` (9 in all with the three reads above; `TestAllowlistExact`); the client sends the
changes for practice accounts only, once, never retried. These are the documented shapes; recorded answers from the owner's practice run
(R45.13) replace the synthetic fixtures.

| Method and path | Limit | Scope | Use (R45) |
|---|---|---|---|
| `POST /api/v0/equity/orders/market` | 50 / min | orders:execute | buy; close (sell) |
| `POST /api/v0/equity/orders/stop` | 1 / 2 s | orders:execute | sell-stop after a fill |
| `DELETE /api/v0/equity/orders/{id}` | 50 / min | orders:execute | cancel (the stop before a close; a stale buy) |
| `GET /api/v0/equity/orders` | 1 / 5 s | orders:read | active orders (stop check, matching) |
| `GET /api/v0/equity/orders/{id}` | 1 / 1 s | orders:read | one active order, or 404 |
| `GET /api/v0/equity/history/orders` | 20 / min | history:orders | fills, matching (`cursor`, `ticker`, `limit` ≤ 50; follow `nextPagePath` verbatim) |
| `GET /api/v0/equity/metadata/instruments` | 1 / 50 s | metadata | ticker, ISIN, currency, maxOpenQuantity (refreshed by Trading 212 every 10 min) |

Not used: `POST /equity/orders/limit`, `/stop_limit` (D41), pies, `GET /equity/metadata/exchanges`
(1 / 30 s; cactusTrade uses its own calendar).

- **Market:** `{ticker, quantity, extendedHours}`. A negative quantity sells; `extendedHours`
  defaults to false (cactusTrade always sends false). A market order sent while the market is
  closed is **queued until the next open**, and slippage is possible: send only in session.
- **Stop:** `{ticker, quantity, stopPrice, timeValidity: DAY | GOOD_TILL_CANCEL}`. It becomes a
  market order once the last traded price reaches `stopPrice`.
- **Not idempotent:** "Sending the same request multiple times may result in duplicate orders."
  There is no client order id.
- **Errors:** 400 failed validation, 401, 403 (e.g. "Scope( orders:execute ) missing"), 408, 429.
- **Order** (the answer to every POST, and the item of the order lists): `id` (int64),
  `createdAt`, `currency`, `extendedHours`, `filledQuantity`, `filledValue`, `initiatedFrom`
  (API|IOS|ANDROID|WEB|SYSTEM|AUTOINVEST|INSTRUMENT_AUTOINVEST),
  `instrument{currency,isin,name,ticker}`, `limitPrice`, `quantity`, `side` (BUY|SELL),
  `status`, `stopPrice`, `strategy` (QUANTITY|VALUE), `ticker`, `timeInForce`, `type`
  (LIMIT|STOP|MARKET|STOP_LIMIT), `value`. The request field is `timeValidity`, the response
  field is `timeInForce`.
- **Status:** LOCAL, UNCONFIRMED, CONFIRMED, NEW, CANCELLING, CANCELLED, PARTIALLY_FILLED,
  FILLED, REJECTED, REPLACING, REPLACED.
- **DELETE:** 200 with no body = cancel request accepted (it may still fill while the order is
  filling); 404 = unknown.
- **History item:** `{order: Order, fill: {filledAt, id, price, quantity, tradingMethod
  (TOTV|OTC), type, walletImpact{currency, fxRate, netValue, realisedProfitLoss,
  taxes[{chargedAt, currency, name, quantity}]}}}`. Tax names: COMMISSION_TURNOVER,
  CURRENCY_CONVERSION_FEE, FINRA_FEE, FRENCH_TRANSACTION_TAX, PTM_LEVY, STAMP_DUTY,
  STAMP_DUTY_RESERVE_TAX, TRANSACTION_FEE.
- **Instrument metadata:** `addedOn`, `currencyCode`, `extendedHours`, `isin`,
  `maxOpenQuantity`, `name`, `shortName`, `ticker`, `type` (STOCK, ETF, …),
  `workingScheduleId`. There is **no `minTradeQuantity`** and no minimum order value.
- **Limits:** at most 50 pending orders per ticker per account. Orders only in the primary
  account currency (multi-currency accounts not supported); Invest and Stocks ISA only; orders
  by value are not supported through the API.
- **Scopes:** account, portfolio, metadata, orders:read, orders:execute, history:orders
  (others exist: history:transactions, history:dividends, pies:read, pies:write — not needed).
- **Rate-limit headers:** `x-ratelimit-limit`, `-period`, `-remaining`, `-reset` (Unix
  seconds), `-used`. Limits are per account.
- **Terms:** https://www.trading212.com/legal-documentation/API-Terms_EN.pdf (read by the
  owner, D41 h).

**Not documented — fail closed until the owner's practice run (R45.13):**
- whether the demo (practice) environment accepts API orders (T7);
- quantity decimals, and stops on fractional quantities (T8: whole shares until verified, D43);
- stop price units for GBX shares, and price precision (T2b);
- what `GET /equity/orders/{id}` answers after a fill (T4, assumed 404);
- how long before a fill shows in history (T5b);
- whether a 429 is answered before processing (T6b, treated as an unknown outcome);
- what happens on a sell larger than the holding (T11; the positions endpoint has
  `quantityAvailableForTrading`, and cactusTrade refuses such a sell itself);
- GOOD_TILL_CANCEL across sessions (T12);
- `quantityAvailableForTrading` while a stop is open (T13).

## Used by cactusTrade (R45.10, 2026-11-28)

| Call | When |
|---|---|
| `GET account/summary` + `GET positions` | every supervisor pass (10 s), before every entry and every sale (fresh), reconciliation, views (cached 10 s) |
| `GET orders` | stop confirmation (5, 10, 20, 30 s after a stop), the supervisor's stop check, matching an unknown order, reconciliation |
| `GET orders/{id}` | following a buy or a sale; confirming a cancelled stop |
| `GET history/orders` | fills (price, taxes), matching an unknown order, a vanished position |
| `GET metadata/instruments` | the catalogue loop (30 s after the start, then every 12 h) and the owner's Refresh (at most 1 / 50 s) |
| `POST orders/market` | a buy (quantity > 0) or a sale of the unsold quantity (quantity < 0), in session only, once |
| `POST orders/stop` | the sell stop after a fill (GOOD_TILL_CANCEL), once |
| `DELETE orders/{id}` | a stop before a sale, a stale buy or sale (≤ 3 per order) |

`cactustrade t212-probe --account ID [--ticker T] [--raw] [--record DIR]` reads (GET only) the
summary, positions, active orders, the head of the instrument list and the first history page
of a practice account, and records sanitised answers for fixtures.
