# Tiger Brokers Open API — verified facts

Read on **2026-11-23** from docs.itigerup.com (Chinese) and Tiger's official, MIT-licensed Go
SDK `github.com/tigerfintech/openapi-go-sdk` (files read with
`gh api repos/tigerfintech/openapi-go-sdk/contents/<path>`; facts only, the SDK is **not**
imported or vendored — cactusTrade has its own client, no new dependency) and, for two points,
the Python SDK `tigerfintech/openapi-python-sdk`. cactusTrade uses Tiger **read-only** (owner's
decision 2026-11-23: "Saxo and Tiger, view-only").

## Gateway

One gateway for paper and live accounts: `https://openapi.tigerfintech.com/gateway` (SDK
`config/client_config.go`, `defaultServerURL`). It is a constant in cactusTrade.

**Not implemented on purpose:** the SDK's dynamic "domain garden" (`config/domain.go`,
`QueryDomains`): with `EnableDynamicDomain` it GETs `https://cg.play-analytics.com` and takes the
server address from the answer, keyed by licence. Fetching where to send signed requests from a
third-party domain at run time is a credential-redirect risk, so cactusTrade never does it. The
allowlist test fails on any URL literal but the gateway.

## Request envelope (every call is a POST)

JSON body (`Content-Type: application/json;charset=UTF-8`) of string parameters (SDK
`client/http_client.go` `buildCommonParams`, `doHTTPPost`):

| Parameter | Value |
|---|---|
| `tiger_id` | the developer's Tiger ID |
| `method` | the API method (`prime_assets`, `assets`, `positions`) |
| `charset` | `UTF-8` |
| `sign_type` | `RSA` |
| `timestamp` | `2006-01-02 15:04:05`, UTC (the Go SDK; the Python SDK uses local time) |
| `version` | `2.0` |
| `biz_content` | the method's parameters as a JSON string, here `{"account":"<number>"}` (+ `"segment":true` for `assets`) |
| `sign` | see below |

`sign` (SDK `signer/sign_content.go`, `signer/signer.go`): all other parameters as `k=v`, sorted by
name, joined with `&`, values not escaped; SHA-1 of that; RSA PKCS#1 v1.5 signature with the
developer's private key; standard base64. The key may be PKCS#1 or PKCS#8 PEM, or the bare
base64 DER (SDK `LoadPrivateKey`); cactusTrade accepts all three. SHA-1 is Tiger's protocol
choice (`sign_type` `RSA`), not ours.

The SDK also sends `language` and `device_id`, and an `Authorization` token for the TBHK
licence's market-data token; cactusTrade sends neither (not needed to read assets and positions).
The SDK can verify a response `sign` with Tiger's public key; cactusTrade does not (it reads, the
answer is parsed defensively, and TLS authenticates the gateway).

**Licence** (`TBHK`, `TBSG`, …; Tiger AU's code is not documented): in both SDKs the licence only
selects the server in the domain-garden lookup (Go `config/domain.go`; Python
`tiger_open_client.py`: "It only selects a per-licence …"); it is **not** a request parameter.
cactusTrade stores it as an optional, free-text credential for reference and does not send it.

## Response envelope

`{"code": int, "message": str, "data": …, "timestamp": int, "sign": str}` (SDK
`client/api_response.go`). `code` 0 = success. `data` is sometimes a JSON string holding the JSON
(SDK `UnmarshalData`); cactusTrade accepts both. Error categories (SDK `client/errors.go`
`ClassifyErrorCode`): 5 rate limit; 1000–1009 common parameter; 1010–1099 business parameter;
1100–1199 global account; 1200–1299 prime account; 1300–2099 simulation account; 2400–3999
token; 4000–4999 permission. cactusTrade reports the code and Tiger's message with the account's
Tiger ID, account number and key removed (at most 160 characters).

## Methods cactusTrade may call (the allowlist)

| Method | `data` | Used for |
|---|---|---|
| `prime_assets` | `{accountId, updateTimestamp, segments:[…]}` | cash, net liquidation, P&L of the securities segment |
| `assets` | `{items:[…]}` | fallback when `prime_assets` answers a business error (1010–2399) or has no `S` segment |
| `positions` | `{items:[…]}` | open positions |

No other method name can be sent (`place_order`, `modify_order`, `cancel_order`, fund transfers,
token refresh… are absent); the allowlist test fails on any such literal.

## Field names (all verified in the SDK's `model` package)

`prime_assets` segment — `model/trade.go` `PrimeAssetSegment` (camelCase): `category`, `currency`,
`cashBalance`, `netLiquidation`, `unrealizedPL`, `realizedPL` (also `grossPositionValue`,
`cashAvailableForTrade`, `currencyAssets`…, not used). The securities segment is
`category` **`S`** (`C` = commodities): Python SDK `tigeropen/trade/domain/prime_account.py`
("'S' for stocks, 'C' for commodity futures") and the push proto `AssetData.segType` comment.

`assets` item — `model/trade.go` `Asset`: `account`, `currency`, `cashValue`, `netLiquidation`,
`unrealizedPnL`, `realizedPnL`.

`positions` item — `model/position.go` `Position`: `symbol`, `name`, `market`, `currency`,
`secType`, `positionQty` (float; newer), `position` (int) with `positionScale` (quantity =
position / 10^scale; older), `averageCost`, `latestPrice`, `marketValue`, `unrealizedPnl`.
**`marketPrice` does not exist** in the SDK model; `latestPrice` is used.

A missing currency, cash or net liquidation refuses the account (fail closed). Position values
are in the position's own currency: a position in another currency than the account's is shown
as **unknown** (no conversion). Unrealised P&L of the account is Tiger's own segment figure.

## Rate limits

60 requests per minute for these methods (docs). The client spaces each method to one call per
second per account; on HTTP 429 or business code 5 it holds the method for 10 s and retries once
(waits ≤ 15 s); a 5xx is retried once.

## Paper accounts

Paper and live accounts use the same gateway; the account number says which (a demo account
in cactusTrade holds a paper account number). Tiger paper accounts have **no Australian market**
(docs.itigerup.com); the UI says so.
