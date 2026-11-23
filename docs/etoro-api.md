# eToro Public API — verified facts

Verified 2026-11-22 against `https://api-portal.etoro.com/llms.txt`, the guides it links
(authentication, rate limits, market orders) and `openapi.json` **v1.419.0**. Re-verify with
`make etoro-spec` (downloads the spec to `.cache/etoro/`, prints the path list and diffs it
against `internal/adapters/etoro/allowlist.go`). The live docs win over this file; update it
when they change.

## Hosts and authentication

| Item | Value |
|---|---|
| REST base | `https://public-api.etoro.com` |
| WebSocket | `wss://ws.etoro.com/ws` |
| Headers | `x-api-key` (application key) + `x-user-key` (user key) + `x-request-id` (UUID, **required on every operation**) |
| Alternative | OAuth2 bearer — not used by cactusTrade |
| Errors | `401` bad/missing key; `422 {"errorCode":"RequestIdRequired"}` without `x-request-id`; body `{errorCode, errorMessage}` |
| Keys | eToro → Settings → Trading → API Key Management → Create New Key. **Each key is bound to one environment (Demo or Real)** and to Read or Write permission; optional IP allow-list and expiry; SMS verification |

**Consequence:** demo and real need *separate* key pairs. cactusTrade stores them as separate
secrets (`etoro_demo_*`, `etoro_real_*`) and only ever loads the pair of the current mode.

## Demo vs real

Demo and real share the host. Most demo paths add a `demo` segment, but **not always in the
same position** (`/trading/info/demo/pnl` vs `/trading/info/real/pnl`;
`/trading/demo/positions/{id}` vs `/trading/positions/{id}`). The adapter therefore keeps an
explicit demo/real path pair per operation in `allowlist.go`; the mode is chosen in one place.

## Operations cactusTrade uses (the allowlist)

| Operation | Demo | Real | Quota |
|---|---|---|---|
| Instrument search (free text, ≤50) | `GET /api/v2/market-data/instruments/search` | same | market data 120/60s |
| Industries (stock sectors; item fields undocumented: first `*id*` int and `*name*` string are read) | `GET /api/v1/market-data/stocks-industries` | same | market data |
| Screen (field search with filters: `industryNameId`, `isCurrentlyTradable`; sort `-popularityUniques7Day`) | `GET /api/v1/market-data/search` | same | market data |
| Instrument metadata | `GET /api/v2/market-data/instruments` | same | market data |
| Exchanges | `GET /api/v1/market-data/exchanges` | same | market data |
| Bid/ask rates (≤1000 ids) | `GET /api/v2/market-data/rates` | same | market data |
| Candles (≤1000, asc/desc; OneMinute…OneWeek) | `GET /api/v1/market-data/instruments/{instrumentId}/history/candles/{direction}/{interval}/{candlesCount}` | same | market data |
| Eligibility (≤100 ids) | `POST /api/v2/trading/info/demo/eligibility` | `POST /api/v2/trading/info/eligibility` | dedicated 20/60s |
| Cost breakdown (spread, markup, fees, **tax**) | `POST /api/v2/trading/info/demo/costs` | `POST /api/v2/trading/info/costs` | dedicated 20/60s |
| Portfolio + PnL + `credit` | `GET /api/v1/trading/info/demo/pnl` | `GET /api/v1/trading/info/real/pnl` | portfolio 60/60s |
| Order lookup (`orderId` or `referenceId`) | `GET /api/v2/trading/info/demo/orders:lookup` | `GET /api/v2/trading/info/orders:lookup` | portfolio 60/60s |
| Close-order status | `GET /api/v1/trading/info/demo/close-orders/{orderId}` | `GET /api/v1/trading/info/real/close-orders/{orderId}` | portfolio 60/60s |
| Trade history (`minDate`) | `GET /api/v1/trading/info/trade/demo/history` | `GET /api/v1/trading/info/trade/history` | default 60/60s |
| Open order | `POST /api/v2/trading/execution/demo/orders` | `POST /api/v2/trading/execution/orders` | **execution 20/60s shared** |
| Cancel open order | `DELETE /api/v2/trading/execution/demo/orders/{orderId}` | `DELETE /api/v2/trading/execution/orders/{orderId}` | execution |
| Close position (full or `UnitsToDeduct`) | `POST /api/v1/trading/execution/demo/market-close-orders/positions/{positionId}` | `POST /api/v1/trading/execution/market-close-orders/positions/{positionId}` | execution |
| Set/tighten SL/TP | `PATCH /api/v2/trading/demo/positions/{positionId}` | `PATCH /api/v2/trading/positions/{positionId}` | default 60/60s |

### Open order body (v2)

`{action:"open", transaction:"buy", instrumentId, settlementType:"real", orderType:"mkt",
leverage:1, amount:<USD>, orderCurrency:"usd", stopLossRate, takeProfitRate,
stopLossType:"fixed"}`. `amount`/`units` are mutually exclusive; **only USD** order currency
is supported. `stopLossRate`/`takeProfitRate` are absolute rates and can be set at entry.
Response `{token, orderId, referenceId}` — **200 = accepted for processing, not executed**.
`referenceId` echoes our `x-request-id`. Poll `orders:lookup`: status 3 Filled,
5 PartiallyFilled, 4 Rejected, 10 RejectedPartiallyFilled, 1/2/11/12 pending.
`positionExecutions` lists the resulting `positionId`s.

### Shorts (owner's decision D19)

A short is `transaction: "sellShort"` with `settlementType: "cfd"` at `leverage: 1` — the
second and last order shape the adapter knows (`orderShape`). Eligibility needs a
`leverageConfigs` entry with `settlementType: cfd`, `direction: short|sell`, `1` in
`leverageValues` and `isPotential: false`. A conforming short position has
`settlementTypeID: 0` (CFD), `isBuy: false`, leverage 1. CFDs carry overnight fees and
margin; cactusTrade closes every position before the session ends.

### Share vs CFD

Settlement is chosen with `settlementType` (`cfd | real | realFutures | marginTrade`).
`real` = "the real instrument held in full value". On v2 it is optional, and **what an
omitted value resolves to is not documented**, so cactusTrade always sends `real` (a
constant) and checks the eligibility response's `leverageConfigs[]` for an entry with
`settlementType: real`, `direction: long`, leverage 1 before the first trade. After a fill the
position's `settlementTypeID` must be `1` (Real Asset); anything else is closed at once and
the instrument is blocked (fail closed).

### PATCH SL/TP warning

The spec says: "deepening a stop loss beyond the allowed maximum stop loss will cause funds
to be transferred from the account balance into the position margin." cactusTrade only ever
**tightens** a long stop (raises it), never deepens it.

## How the phase-2 client reads the answers (2026-11-23, spec v1.419.0)

- **Ids in queries** (`rates.instrumentIds`, `instruments.instrumentsIds`) are `style: form,
  explode: false`: one comma-separated value.
- **Rates**: `bid`, `ask` and `date` are nullable; an instrument without all three is left
  out (callers treat a missing quote as stale). `quoteType` is `realtime` or `delayed`.
- **Candles**: requested `desc` (newest N) and sorted oldest-first by the client.
- **Portfolio**: `credit` is the available cash (missing → error, fail closed). Money committed
  to pending orders is `ordersForOpen[].amount` + `orders[].amount` (entry orders). A
  position's `amount` is USD; `openRate` is in the **instrument's currency**
  (`openConversionRate` converts to USD) — for LSE that is GBP or GBX (open question Q1).
- **Eligibility** is "real long x1" only when `allowOpenPosition` and a `leverageConfigs`
  entry has `settlementType: real`, `direction: long|buy`, `1` in `leverageValues` and
  `isPotential: false`. The meaning of `isPotential` is not documented, so it counts as
  "not offered" (fail closed) until checked live (2.7).
- **Market** is inferred from the symbol: `XXX.L` = LSE; plain tickers and US share-class
  suffixes (`BRK.B`) = NYSE/NASDAQ; any other suffix is unsupported. The exchanges endpoint
  has no documented response schema, so exchange ids are stored but not relied on.
- **Errors**: gateway `{errorCode, errorMessage}`, API `{success:false, error:{code,message}}`
  and RFC 7807 problem details are all parsed. 404 → not found; anything else → unavailable
  (fail closed). 429 and 5xx are retried (reads only) with `Retry-After` / jittered backoff;
  a 429 also pauses that quota group in the client-side limiter.

## Rate limits

Rolling 60-second window per user key. `429` with `errorCode: TooManyRequests` and
`Retry-After` (seconds); every response carries `RateLimit-Limit`, `RateLimit-Remaining`,
`RateLimit-Reset`, `RateLimit-Policy: <limit>;w=<window>`. Groups used by cactusTrade:
market data 120, portfolio 60, default 60, execution 20 (open + close + cancel together),
eligibility 20, costs 20.

**Consequence:** the kill switch can close at most ~20 positions a minute. The risk engine
caps open positions (default 8) so that a full flatten fits in one window with room for
retries.

## WebSocket

`wss://ws.etoro.com/ws` (verified 2026-11-23 against api-portal.etoro.com/core/websocket/
authentication, topics, control-channel and instrument-rates pages).

- Request envelope `{id, operation, data}`. **Authenticate** first, also for public market
  data: `data: {userKey, apiKey}`. Reply `{id, operation, success}`; on failure also
  `errorCode` (InvalidKey, Unauthorized, Forbidden, TooManyRequests, …) and `errorMessage`.
  Subscribing before authenticating fails with `SessionNotAuthenticated`.
- **Subscribe** `data: {topics: ["instrument:<id>"], snapshot: true}` (snapshot sends the
  current rate at once; market-data topics only). **Unsubscribe** `data: {topics}`.
  Other topics: `candle:<id>`, `private:demo`, `private` / `private:real` (not used yet).
- **Push** `{messages: [{topic, content, id, type}]}`; `content` is a JSON *string*. Rates
  (`Trading.Instrument.Rate`): `Ask`, `Bid`, `LastExecution`, `Date` — numbers arrive as
  quoted strings. A `Trading.Instrument.MarketStatus` type is documented as illustrative.
- Not documented: heartbeat/ping, topics per connection, connections per key, reconnect
  advice. The client pings with WebSocket control frames every 30 s and reconnects with
  backoff (1 s … 1 min).

## Account currency and FX

The trading account is **USD** (`accountCurrencyId: 1`); orders, eligibility and `credit`
("available trading balance in USD") are USD. GBP funding is converted by eToro on deposit
(outside the API and outside cactusTrade). LSE shares are quoted in GBP/GBX; their USD
exposure and the conversion cost are not described in the pages read, so cactusTrade asks the
`costs` endpoint for each candidate (spread, markup, transaction fee, tax such as UK stamp
duty) instead of modelling them itself. Open question for phase 2: GBX quote units on LSE.

## Never in the client (deny list, for the allowlist test)

`/api/v1/money/*` (transfers, transferable balance, cash-account transactions),
`/api/v1/balances*` (not needed: `credit` and positions come from `pnl`),
`/api/v1/sub-accounts/*` (incl. money-transfers), `/api/v2/trading/copy*`,
`/api/v1|v2/agent-portfolios*`, `/api/v1/sso/*`, `/api/v1/watchlists*`, `/api/v2/watchlists`,
`/api/v1/curated-lists`, `/api/v1/price-alerts*`, `/api/v1/notifications/*`,
`/api/v1/user-info/*`, `/api/v1/pi-data/*`, `/api/v2/portfolios/*`, `/api/v1/portfolios/*`,
`/api/v1/clubs`, `/api/v1/me`, `/api/v1/keydata/*`, `/api/v1/data/*`, the aggregate-portfolio
endpoints, and the whole Partners API (onboarding, KYC, deposits, withdrawals).
