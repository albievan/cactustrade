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

### Execution as implemented (phase 4.6, spec v1.419.0, demo only)

- **Open** `POST …/demo/orders` body has exactly `action, transaction, instrumentId,
  settlementType, orderType, leverage, amount, orderCurrency, stopLossRate, takeProfitRate,
  stopLossType`; `takeProfitRate` is left out when there is no target (the spec allows 0 but
  does not say whether 0 means "none"). The answer's `referenceId` must equal our
  `x-request-id` and carry an `orderId`, otherwise the outcome counts as unknown.
- **Close** `POST …/demo/market-close-orders/positions/{positionId}` body is
  `{"InstrumentID": <id>}` only (`UnitsToDeduct` never sent: always the whole position). The
  answer's `orderForClose.orderID` is polled with `GET …/demo/close-orders/{orderId}` (status
  ids as below; `positions[]` carries `units`, `amount`, `rate` once closed).
- **Cancel** `DELETE …/demo/orders/{orderId}`; 200 = request accepted; confirm with the lookup
  (7 Canceled / 9 CanceledPartiallyFilled; 6 PendingCancel still in flight). The spec calls it
  idempotent for an order already closed or cancelled.
- **Status ids** (lookup and close-orders): 1 Received, 2 Placed, 6 PendingCancel,
  11 WaitingForMarket, 12 PendingTriggeredRate → pending; 3 Filled; 4 Rejected; 5
  PartiallyFilled → partial; 7 Canceled, 8 Expired → cancelled; 9 CanceledPartiallyFilled and
  10 RejectedPartiallyFilled → partial *and final*. Terminal: 3, 4, 7, 8, 9, 10. Anything else
  → unknown.
- **Lookup** `asset.side` is `long|short` in the schema but `buy` in eToro's own example; both
  spellings are read. `asset.settlementType`, `asset.leverage` and each
  `positionExecutions[]` (`investedAmountCurrency`, `openingData.units` or `remainingUnits`,
  `openingData.avgPrice`, `stopLossRate`) are returned for the fill check.
- No retries for account changes; 5xx/timeout/network error = outcome unknown; 4xx = not
  accepted; 429 pauses the execution group for `Retry-After` (default 5 s, at most 60 s). Reads
  keep their retries with a fresh `x-request-id` per attempt. Redirects are never followed.
- Still unverified (U1–U11 in `docs/PLAN.md`): e.g. what eToro does with a resent
  `x-request-id` (U1) and whether a 429 is answered before processing (U7).

### How the app uses execution (phase 4.7–4.14, demo only)

- **Request ids.** Every open, close and cancel is a row in `orders` (state `created`) before
  anything is sent; the row's UUID is the `x-request-id`, and `orders:lookup?referenceId=` finds
  the open again after a lost answer. The row moves to `sent` (stored) before the HTTP call.
- **Polling.** An open is looked up at 1, 2, 4, 8, 15 and 30 s after the send; still pending at
  60 s → a cancel (its own stored id); still unknown to eToro (404) 2 min after the send →
  `abandoned`, never resent. A close is read by eToro's `orderForClose.orderID` at 1–30 s.
- **Resends.** **An open is never resent** (PLAN decision review-M3, stricter than OD2, until
  U1 and U7 are verified): one send per open. After a 429 the executor waits `Retry-After` and
  looks the order up by its `referenceId`: found → followed like any open; 404 → the normal
  poll schedule, `abandoned` 2 min after the send, its cash reserved until then. A close
  answered with 429 or lost (and the position still in the portfolio) is resent with the same
  id (≤ 5 sends); a cancel ≤ 3.
- **Fill check.** Every execution must be the long shape (real, long, ×1) or the short shape
  (cfd, short, ×1), on our instrument, with a stop on the losing side within 1 % of the stop we
  sent. Otherwise the position is closed at once, the instrument blocked and an alert raised.
- **Pacing.** The client's execution limiter (20 per 60 s per key) paces everything. The
  position cap (open positions plus pending orders) is **8 by default and at most 15**
  (`HardMaxOpenPositions`, D12): at the hard limit a kill with 15 positions and 5 pending
  orders sends exactly 20 requests in one window.
- **Real accounts.** Refused before any I/O by the eToro client, the executor, `IntentFor`, the
  risk engine and agent Start. Reconciliation only reads them.

### Unverified until the owner's live demo run (PLAN 4.17)

Each is handled fail-closed today. 4.17 records fixtures and settles them.

| # | Question | How cactusTrade behaves meanwhile |
|---|---|---|
| U1 | What eToro does with an open resent with the same `x-request-id` (deduplicate, refuse, or a second order) | An open is never resent (review-M3): one send; a lost or 429 answer is settled by lookup only |
| U2 | How long until `orders:lookup` finds a new order | A 404 within 2 min of the send is "in flight": cash stays reserved and the instrument blocked; after 2 min → abandoned |
| U3 | No lookup by reference for closes; what a second close of a closed position returns | Closes are read by eToro's order id; a lost close answer is settled by re-reading the portfolio; "position not found" and absent from the portfolio → recorded closed |
| U4 | LSE stop and rate units (GBX vs GBP, Q1) | No LSE entries (OD9); every fill's stop must be within 1 % of ours (a ×100 mix-up fails) |
| U5 | Whether the newest candle is still forming | Every bar that has not closed at the decision time is dropped; the newest closed bar must be at most two intervals old |
| U6 | What `isPotential` means in eligibility | Counted as "not offered" |
| U7 | Whether a 429 is answered before the request is processed | An open answered 429 is not sent again; the lookup after `Retry-After` adopts it if eToro processed it anyway, else it is abandoned after 2 min |
| U8 | Whether a status-11 (waiting for market) order can be cancelled | A cancel is sent at 60 s and confirmed by lookup; unconfirmed after 2 min → abandoned and retried by the supervisor at most once a minute; entries in that instrument stay blocked |
| U9 | Partial fills of orders by amount | The filled part is recorded and checked like any fill; a partial that eToro reports final ends `cancelled` with its positions kept |
| U10 | Whether `credit` already excludes cash held for pending orders | Our own non-final orders *and* eToro's `ordersForOpen` are both subtracted (double counting, conservative, D24) |
| U11 | How quickly a fill shows in `pnl` | A recorded position missing from the portfolio is taken for closed only 2 min after it was recorded |

### Trade history as implemented (phase 6.3, read-only)

`GET …/trade/demo/history` (real: `…/trade/history`), default quota group. Query `minDate`
(a date), `page`, `pageSize` (100). cactusTrade reads windows of 364 days from the requested
start ("1 year − 1 day", the spec's limit), at most 50 pages per window, and keeps each trade
once per `positionId`, in the window its close falls in. Any failed page fails the whole read
(nothing partial is stored). Fields read per closed trade:

| Field | Use |
|---|---|
| `positionId`, `instrumentId`, `orderId` | identity; `orderId` is eToro's id of the open (0 = not reported) |
| `isBuy`, `leverage` | direction (long / short) and leverage |
| `openTimestamp`, `closeTimestamp` | times (UTC); a trade without a close time or position id is skipped with a warning |
| `openRate`, `closeRate`, `stopLossRate`, `takeProfitRate` | rates (exact decimals, micros) |
| `units` | units (micros) |
| `initialInvestment`, else `investment` | the amount invested (cents; `initialInvestment` when present and > 0) |
| `netProfit` | the net result in USD (cents). **Assumed to be after fees (RU5, unverified)**: gross = net + known fees |
| `fees` | USD (cents). **Missing or null = unknown**: kept apart (`fees_known` false) and never counted as 0 |

The first sync of an account reads 90 days (OD18); later ones start 2 days before the latest
stored close. Unverified until a live run: whether eToro answers a window up to its end or up
to now (the per-position de-duplication makes both give the same result).

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
- **Market** is inferred from the symbol: `XXX.L` = LSE; `XXX.ASX` = ASX (eToro lists e.g.
  `CBA.ASX`, priced in AUD — etoro.com/markets/cba.asx, 2026-11-23; whether a given ASX share is
  offered as a real share long x1 is left to the eligibility check, so a CFD-only listing is
  refused); plain tickers and US share-class suffixes (`BRK.B`) = NYSE/NASDAQ; any other
  suffix is unsupported. Orders are sized by stop distance as a fraction of the price, so a
  quote currency other than USD (GBX/GBP, AUD) does not change the size; P&L and amounts
  are in the account's USD as eToro reports them. The exchanges endpoint
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
