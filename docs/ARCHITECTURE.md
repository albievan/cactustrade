# cactusTrade — architecture

## Shape

A hexagonal modular monolith: one process, one binary (`cmd/cactustrade`), the web app
embedded with `go:embed`.

```
            ┌──────────────── adapters (inbound) ───────────────┐
 browser ──▶│ http: OpenAPI server, auth, CSRF, rate limit, SPA │
 programs ─▶│      (api key)                                    │
            └───────────────────────┬───────────────────────────┘
                                    ▼
                          internal/app (use cases)
                     every call: Principal → permission
                                    │ ports only
            ┌───────────────────────┼─────────────────────────────┐
            ▼                       ▼                             ▼
   sqlstore (SQLite│MariaDB)   secretstore (AES-256-GCM,     etoro (allowlist; client
   goose migrations x2         env overlay)                  from phase 2)
                                                             llm (phase 5, no broker access)

 internal/strategy (pure: features, HMM, playbooks, backtester; no I/O, no ports)
```

Dependency rule, enforced by depguard: `domain ← app ← ports ← adapters ← cmd`.
`internal/domain` does no I/O and never reads the clock.

## Ports (internal/ports)

| Port | Phase 1 implementation |
|---|---|
| `Users`, `Sessions`, `APIKeys`, `Secrets`, `Audit`, `Settings` (the repository) | `sqlstore` |
| `SecretStore` | `secretstore.DB` + `secretstore.Env` overlay |
| `Cipher` | `secretstore.Cipher` (AES-256-GCM; the credential name is the AAD) |
| `PasswordHasher` | `argon2` (argon2id, 64 MiB, t=3) |
| `Clock`, `Random` | `system` |
| `EventBus` | `eventbus` (in-process, never blocks a publisher) |
| `MarketCalendar` | `calendar` (LSE, NYSE/NASDAQ, ASX; tables 2026–2027) |
| `MarketData` | `etoro.Feed` (WebSocket stream, REST fallback) |
| `Broker` | `etoro.Client`: reads, plus open / close / cancel for **demo accounts only** (phase 4.6); `fakes.SimBroker` simulates a demo account for executor tests |
| `Orders`, `AgentPositions`, `RiskStates`, `InstrumentBlocks`, `AgentLeases` (repository) | `sqlstore` (migration 00009): our orders keyed by `x-request-id`, the positions cactusTrade opened, the risk state per account (versioned), blocked instruments, the single-writer lease (D22) |
| `Holdings` | read-only holdings of platforms cactusTrade shows but does not trade: `trading212.Client` (GET account summary and positions only), `saxo.Client` (GET `/port/v1/balances/me`, `/port/v1/positions/me`), `tiger.Client` (signed POST of `prime_assets` / `assets` / `positions` only) — each with its own allowlist and allowlist test. eToro holdings are built in the app from `Broker.Portfolio` + instrument names |
| `OAuthConnector` | browser sign-in of a view-only platform: `saxo.Client` (authorize URL; code exchange and refresh token rotation at the sign-in host's `/token`, the client's only POST) |
| `EquitySnapshots` (repository) | `sqlstore` (`equity_snapshots`, migration 00008): equity every 15 minutes per account, kept 400 days |
| `SignalModel` | phase 3 |
| `Reviewer` | `adapters/llm` (Anthropic SDK; OpenAI-compatible for the others); profiles (with the admin's prices, OD13) in settings, keys `llm.<id>.api_key`. `Review` returns the verdict and the call's trace (request body as sent, raw response, served model, tokens, latency), filled even on error, never with the key |
| `LLMCalls`, `Decisions` (repository) | `sqlstore` (migration 00010): every LLM call (agent, scanner, test) and the decision log (one row per candidate the agent handled); both kept 400 days (OD12); `LLMCalls.Usage` sums them per profile and purpose |
| `Mailer` | `adapters/mail` (net/smtp: STARTTLS required / implicit TLS / plain to localhost; header-injection checks); `Outbox` and `PasswordResets` repositories |

The `Broker` port has no money-movement operation. Its `Open` takes a `domain.OrderIntent`,
which has no leverage or settlement field: its direction picks one of the adapter's two
constant order shapes (long = buy + real, short = sellShort + cfd; leverage 1 for both, D19).

There is no `RiskEngine` port (D20): the risk engine is the pure `domain.AssessEntry`, so no
wiring can swap in a laxer one.

### Execution (phase 4)

- Every account change (`Open`, `Close`, `Cancel`) takes the request id the caller persisted
  first (`orders.request_id`, CLAUDE.md §2.7) and sends it as `x-request-id`. The eToro client
  refuses a real-mode or non-eToro account, a non-UUID id and an intent without a stop
  **before any I/O** (`domain.ErrRealModeGated` for real).
- One request per call, never retried by the adapter: a network error, timeout, 5xx or
  unreadable answer is `domain.ErrOutcomeUnknown` (resolve with `Lookup` / `CloseOrder`, never
  resend blindly, OD2); a 4xx is `*domain.NotAcceptedError` (a 429 is `RateLimited` with
  `RetryAfter`, and pauses the account's execution quota group).
- The client never follows a redirect (custom key headers would be forwarded).
- `Lookup` reports what eToro executed (settlement, leverage, side, and per position units,
  amount, open rate, stop) so `OrderStatus.Conforms` can check a fill against the two shapes.
- `orders.Update(prev, next)` is a compare-and-set on (state, sends); `risk_state` carries a
  version (stale write → `ErrConflict`, so a kill is never overwritten by an older read);
  `agent_leases.Acquire` lets exactly one holder win.

### Trading runtime (phase 4)

Three paths act on an eToro demo account. Only the executor (`app/executor.go`) talks to the
`Broker` for account changes; the supervisor, the agent and the kill switch all go through it.

```
 start-up (cmd/cactustrade/trading.go, in this order, while the web server is already up)
   migrate → app.New → default admin / LLM profile migration / environment account
   → ReconcileAll (≤ 2 min)  → ResumeAgents (OD5)  → SuperviseLoop  → AgentLoop
 shutdown: HTTP drains (a kill in flight finishes) → loops stop → ReleaseLeases; positions stay (OD6)
```

- **Executor.** `placeEntry` stores the order row (`created`) first, re-checks the
  portfolio's age, marks it `sent` (stored), then calls `Open` with the row's id as
  `x-request-id`. It polls `orders:lookup` by that id (1, 2, 4, 8, 15, 30 s, then cancel at
  60 s, abandoned at 2 min when eToro never saw it). An open is never resent (review M3):
  after a 429 it waits out Retry-After and looks the order up; a lost answer is resolved by
  lookup (OD2). Closes (≤ 5) and cancels (≤ 3) resend with the same id.
  Every fill is checked against the two order shapes, leverage 1 and the stop we sent (± 1 %);
  a fill that does not conform is closed at once and its instrument blocked.
  `closePosition` and `cancelOrder` follow the same rules with their own stored ids.
- **Reconciliation** (`app/reconcile.go`) resolves every non-final order of ours by its id and
  matches the positions cactusTrade opened (`agent_positions`) with eToro. Positions opened
  outside cactusTrade are never closed (OD1); a non-conforming one keeps the account
  unreconciled. The result is an in-memory flag per account: entries need it, and "never
  reconciled in this process" is not reconciled. It runs at start-up before any loop, every
  5 minutes from the supervisor, and on demand (`POST /api/v1/agent/reconcile`).
- **Supervisor (D21).** `SuperviseLoop` runs every 10 s, one goroutine per eToro demo account
  with keys (2 min timeout; an account still busy is skipped). Whatever the agent's state, it
  takes the lease, reads the portfolio (or, when that fails, uses the recorded agent positions
  with the session rule only), folds the equity into the risk state (risk day from 22:00 UTC,
  high-water mark, daily latch), trips the kill switch on a drawdown, closes positions due
  (flatten 10 min before their own market's close, non-conforming, bad stop, killed) and
  cancels open orders pending for over a minute. It needs no LLM, no prices and no
  reconciliation.
- **Agent (entries).** `AgentLoop` wakes at each 5-minute bar close + 5 s (D26). An account
  ticks only when it is eToro, demo, running, not killed, reconciled, holds the lease and a
  market calendar is configured (D25). Per watchlist share in session: closed bars only →
  strategy → cost gate (OD8) → reviewer (OD7; only shrinks, D23) → fresh portfolio and quote
  → reservations from our own orders (D24) → `domain.AssessEntry` (D20) → decision stored as
  `pending` → `placeEntry`. Every candidate leaves exactly one decision (below).
- **Control.** Start, Pause, Stop, Kill, ResetKill, `SetRiskLimits`, the supervisor's kill and
  the OD5 resume take the account's *control* lock; one agent tick holds the *entry* lock.
  Order: control, then entry; a tick never waits for control. Kill stores the kill first,
  cancels the running tick, waits for the entry lock, then cancels our pending orders and
  closes our positions (each sent once; the supervisor follows them up). Limits change only
  while the agent is stopped. API keys may pause, stop and kill, never start or reset (OD11).
- **Lease (D22).** `agent_leases` holds one row per account: holder and expiry (30 s). The
  executor renews it before every send and resend; the supervisor before every pass. Each
  process has its own holder id (host name + a random UUID, `Config.LeaseHolder`), so two
  standalone binaries on one SQLite file, or an overlapping container during an upgrade, never
  both send. A graceful shutdown releases it; a crash leaves it to expire.
- **The web view.** `GET /api/v1/agent` shows state, reconciliation, kill reason, the risk-day
  figures and the distance to the daily floor and to the drawdown floor (`domain.DailyFloor`,
  `domain.DrawdownFloor`, the same arithmetic the engine uses), blocked instruments, today's
  flatten time per market, the lease and which controls the caller may use (decided by the
  use cases, not the UI). `GET /api/v1/orders` lists our orders. Agent endpoints answer 403
  (forbidden, view-only, real mode gated), 409 (state conflict) and 422 (no budget, wrong
  reset phrase).

| # | Decision (PLAN) |
|---|---|
| D20 | The risk engine is the pure `domain.AssessEntry`; there is no `RiskEngine` port to swap. |
| D21 | Exits never depend on the agent: the supervisor runs whatever the agent state. |
| D22 | Single writer per account: the lease must be held to send anything. |
| D23 | The reviewer's multiplier becomes integer basis points (floor) at the LLM edge; `ApplyVerdict` only shrinks. |
| D24 | Reservations come from our own non-final orders; eToro's `ordersForOpen` counts too (conservative). An `unknown` order blocks entries. |
| D25 | Entries need a calendar; without one every market is closed for entries. |
| D26 | Decisions on 5-minute bars, at each bar close + 5 s. |

### Decision log and LLM calls (phase 5)

```
 candidate (passed the signal step)
   → costs ─✗→ rejected_cost        → reviewer ─✗→ rejected_reviewer (LLM call logged first)
   → fresh prices / reservations / AssessEntry ─✗→ rejected_risk (a kill too)
   → Decisions.Add(pending, request id)  ─✗→ nothing sent (fail closed)
   → orders row → Open → Decisions.Update(ordered | order_failed, request id)
```

- **One decision per candidate** (`app/agent.go`, `decide` + `finishDecision`). A candidate is
  what the strategy proposed *and* passed the signal step's shape checks; a share skipped
  before that (session, candles, no signal) leaves none. Every exit after it writes exactly
  one row with its outcome: `rejected_cost`, `rejected_reviewer`, `rejected_risk`, `ordered`
  (the request reached eToro; the order's own state says what eToro did) or `order_failed`
  (allowed but not sent).
- **The decision comes first.** The agent chooses the request id, stores the decision as
  `pending` with it, and only then does the executor store the order row and send. A failed
  decision write means no order row and no send. The final update is not cancelled with the
  tick (a kill must not lose the record of an order sent); if it fails the row stays
  `pending` with the request id, and the tick reports the error.
- **Every LLM call is logged** (`app.logCall`): purpose `agent` (the account), `scanner` (the
  account) or `test` (no account). The adapter fills the provider's side; the app adds id,
  account, purpose, profile, time and the cost from the profile's prices (`LLMProfile.Cost`,
  rounded up, −1 = unknown). For the agent a failed log write is a reject; for the scanner and
  the test it is logged and ignored (nothing is traded there). A call answered after the time
  limit is logged with that error and not used.
- **Reading it.** `GET /api/v1/decisions` (viewer, account access; filter by outcome, page by
  `before`) joins each row with its order's current state. `GET /api/v1/decisions/{id}` adds
  the LLM call with its prompt and response; another account's decision is 404, and a call
  of another account is never attached. `GET /api/v1/llm/usage?days=` (admin) sums calls,
  failures, tokens and cost per profile and purpose; a cost is shown only when every call in
  the row had a price ("unknown" otherwise, with the known part apart).
- **Retention (OD12).** `PurgeDecisionLog` (every 10 minutes, with the other purges) deletes
  decisions and LLM calls older than 400 days.

## Accounts

Every eToro call is made for one account: the app checks the caller's access (owner / trade /
view, capped by the role: a viewer never trades) and passes the account to the adapter with
`ports.WithAccount`. The eToro client takes the mode and the key pair from it, keeps one rate
limiter per account (eToro's limits are per key) and refuses calls without an account. The
watchlist, risk limits, agent state and events are per account; system events have no
account and everyone sees them. The price stream authenticates with any account that has
keys (market data is the same for all).

## Platforms

An account has a `platform`: `etoro` (default), `trading212`, `saxo` or `tiger`. Only eToro
accounts are traded (`Account.Tradable()`). `app.account(...)` — the one resolver every
trading, agent, scanner, watchlist, themes and risk use case goes through — refuses any other
platform with `domain.ErrViewOnly` (403 "View-only account"), and a view-only account can never
be the "current" account. Managing an account (keys, sharing, rename, delete) and the
*Overview* (`app.Overview`, `app.EquityHistory`) work on every platform. As defence in depth the
eToro client and price stream refuse a non-eToro account before any I/O, and each view-only
client refuses an account of any other platform, so no key ever reaches another broker.

Credentials are per platform (`domain.PlatformCredentials`: key, label, help, multi-line,
optional, set-by-sign-in) and stored write-only under `<prefix>.<account id>.<key>`:
`etoro.*.api_key|user_key` (or `ETORO_DEMO_PUB` / `_PK` for the environment account),
`t212.*.api_key|api_secret`, `saxo.*.app_key|app_secret|refresh_token|dev_token`,
`tiger.*.tiger_id|account|private_key|license`. `GET /api/v1/platforms` gives the UI the specs;
`PUT /accounts/{id}/keys` takes `values` by key (the older `apiKey`/`userKey` set the first two).
Saxo's refresh token is set only by its sign-in: `GET /accounts/{id}/saxo/connect` issues a
random state (hashed, in memory, single-use, 10 minutes, bound to user and account) and the
public callback `/api/v1/oauth/saxo/callback` exchanges the code, then redirects to
`/accounts`. Saxo rotates the refresh token on every refresh: the client stores the new one
before it uses the new access token, refreshes early (5 min before the access token, 20 min
before the refresh token expires), serialises refreshes per account, and remembers a refused
token (no retries) until the owner connects again (`domain.ErrReconnect` → "Reconnect to
Saxo"). Amounts are minor units of the account currency per `domain.CurrencyDecimals` (JPY,
KRW: 0 decimals; others 2); a position without an account-currency value is `Unknown`.

## Two databases

`CACTUSTRADE_DB_DRIVER` selects `sqlite` (default; `modernc.org/sqlite`, pure Go, so the
binary stays static for Linux, macOS and Windows) or `mariadb` (`go-sql-driver/mysql`; every
container deployment). One SQL dialect subset serves both:

- `?` placeholders. Times are BIGINT Unix microseconds (UTC). Booleans are integers.
- No `RETURNING`, `ON CONFLICT` or `ON DUPLICATE KEY`. Upserts are update-then-insert in one
  transaction.
- MariaDB connects with `clientFoundRows` so "rows affected" means "rows matched", as in SQLite.
- Creating the default administrator is made atomic in both databases by inserting the
  `setup.completed` settings row: its primary key lets exactly one concurrent start win.

`migrations/sqlite` and `migrations/mariadb` hold the same versions (`TestMigrationParity`).
`internal/adapters/sqlstore/storetest` is one repository suite. It runs against SQLite in
`make test` and against MariaDB 11 (testcontainers) in `make test-int`, after a full up → down
→ up migration cycle.

## Security model

- **Sessions:** a 256-bit random token in an `HttpOnly; Secure; SameSite=Strict` cookie; only
  its SHA-256 is stored. Sessions last at most 12 hours and expire after 1 hour idle. A
  password change, disabling or deletion ends them.
- **CSRF:** every cookie-authenticated non-GET request must carry `X-CSRF-Token`, the
  per-session token returned by sign-in and `/auth/me`. A browser `Origin` header must match
  the host.
- **API keys:** `ctk_<id>_<secret>`; only SHA-256(secret) is stored and compared in constant
  time. A key's role never exceeds its owner's (demotion revokes keys above the new role).
  Keys cannot create keys or change credentials.
- **Rate limits:** sign-in is limited per client IP (20 failures / 15 min) and per
  username (5). A limited request is refused before any password check.
- **First run:** the server creates `admin` / `admin`, which must choose a new password
  before it can do anything else (403 "Password change required").
- **One port:** with TLS on, plain HTTP on the same port gets a 308 redirect to HTTPS.
- **Credentials:** write-only. Encrypted with AES-256-GCM under the master key (env, file or
  generated 0600). The ciphertext is bound to its name. Never logged, audited or returned.
- **Headers:** strict CSP (`default-src 'self'`), `X-Frame-Options: DENY`, `nosniff`,
  `no-referrer`, HSTS over TLS, and `Cache-Control: no-store` on the API.
- **Errors:** problem+json. Internal errors are logged, and the client gets a generic 500.
  A refusal because setup is missing (R22) wraps its sentinel in a `domain.MissingError`
  (same status), and the problem adds `missing` (a `domain.Missing` code) and `canFix`
  (decided by the use case, which knows the caller). The web app's `ProblemAlert` shows the
  `FixButton` for the code; React decides nothing.
- **Containers:** distroless, non-root (65532), read-only root filesystem, all capabilities
  dropped, `no-new-privileges`, and secrets as files.

## Observability

JSON logs (`log/slog`): one line per request with method, path, status, duration, client IP
and user. Every line carries the trading mode, and no line contains a body, cookie or token.
`/healthz` is liveness. `/readyz` checks that the database answers. `/metrics` serves
Prometheus HTTP, Go and process metrics, behind a bearer token when one is configured.
