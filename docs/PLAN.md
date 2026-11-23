# cactusTrade — plan

Status keys: `todo` · `in_progress` · `done` · `blocked`. Every phase ends with a green
`make check` and **stops for the owner's review**. Decisions are recorded here and, when they
shape the system, in `docs/adr/`.

## Decisions on record

| # | Decision | Where |
|---|---|---|
| D1 | Hexagonal modular monolith, one binary `cactustrade`, web UI embedded | ADR-001 |
| D2 | **Two databases behind one `Repository` port:** SQLite (`modernc.org/sqlite`, pure Go, no cgo) for the standalone binary; **MariaDB 11 for every container deployment** (owner, 2026-11-22). Same schema in `migrations/sqlite` and `migrations/mariadb`, portable SQL, one shared repository test suite run against both. This replaces the brief's "no external database" for containers. | ADR-002 |
| D3 | eToro and LLM keys: write-only, AES-256-GCM under a master key from `CACTUSTRADE_MASTER_KEY` or `CACTUSTRADE_MASTER_KEY_FILE`; standalone generates `master.key` (0600) in the data dir on first start; containers get it from `deploy.sh` as a secret file. Vault adapter behind `SecretStore` in phase 7. | ADR-003 |
| D4 | eToro client = explicit allowlist table of (operation → demo path, real path); source-scanning test fails the build on any other `/api/` literal. Leverage 1, `buy`, `settlementType: real`, `orderType: mkt`, `orderCurrency: usd` are constants. | ADR-004 |
| D5 | Separate demo and real key pairs (eToro binds each key to one environment). | docs/etoro-api.md |
| D6 | Deployment mirrors cactusInv: `deploy.sh` (Docker Desktop, Podman, k3s, k8s, `--ssh-access`), `.env` + `.env.local`, secrets as files in `SECRETS_DIR` / a k8s Secret, distroless non-root image, read-only root FS. | ADR-005 |
| D7 | Licence: public domain, SQLite-style blessing (as cactusInv, cactusDB, cactusFS), plus a no-warranty / not-financial-advice note. | LICENSE |
| D8 | OpenAPI-first with `oapi-codegen` (std-http `ServerInterface`, Go 1.22 `ServeMux`; no web framework). Swagger UI page at `/api/docs` loads `swagger-ui-dist` from the embedded web bundle (no CDN). | ADR-001 |
| D9 | Auth: argon2id passwords; server-side sessions (random 256-bit token, only its SHA-256 stored) in an `HttpOnly; Secure; SameSite=Strict` cookie; CSRF double-submit token header on every cookie-authenticated mutation; API keys `ctk_<id>_<secret>` (SHA-256 stored) via `Authorization: Bearer`; login rate limit per IP and per username. Roles `admin` ⊃ `operator` ⊃ `viewer`, permissions in `internal/domain`. | ADR-006 |
| D10 | ~~First-run setup token~~ superseded by D17. | ADR-006 |
| D11 | TLS: `CACTUSTRADE_TLS=auto` (default standalone) uses `CACTUSTRADE_TLS_CERT/KEY` or generates a self-signed ECDSA cert in the data dir; `off` in containers, where the ingress / reverse proxy terminates TLS (as cactusInv). Cookies stay `Secure`; `CACTUSTRADE_COOKIE_SECURE=false` only for a plain-HTTP LAN test. | ADR-005 |
| D12 | Open-position cap (default 8) so the kill switch can flatten everything inside eToro's 20-per-minute execution quota. | docs/etoro-api.md |
| D13 | eToro demo keys may come from `ETORO_DEMO_PUB` (Public API Key → `x-api-key`) and `ETORO_DEMO_PK` (User Key → `x-user-key`), or their `_FILE` variants; they override stored values and are read-only in the UI. `deploy.sh` copies them into the secrets (owner, 2026-11-22). | ADR-003 |
| D14 | Go toolchain pinned to go1.26.11 (`toolchain` in go.mod, images): go1.26.0 has 10 standard-library CVEs (govulncheck). | go.mod |
| D15 | ~~Setup endpoint ordering~~ superseded by D17 (there is no setup endpoint). Login sent with a valid session cookie is CSRF-checked like any mutation. | ADR-006 |
| D16 | All money limits (trading budget, per-trade, daily loss) are entered and enforced in USD minor units; GBP is display-only (owner, 2026-11-23). | PLAN Q2 |
| D17 | **Default administrator admin/admin** (owner, 2026-11-23) replaces the setup token. On first start with no users the server creates `admin`/`admin` with `must_change_password`; until it is changed every use case except Me, change-password and sign-out answers 403 "Password change required" (also for API keys of that user). The new password follows the normal rules (≥ 12 characters, not the username). Sign-in stays rate-limited. Migration 00002. | ADR-006 |
| D19 | **Shorts** (owner, 2026-11-23, answering the risks: CFD, losses not capped by the amount invested, overnight fees, margin): allowed on demo and real accounts (real behind the phase-7 gate) as `sellShort` + `cfd` at leverage 1, each with a mandatory stop above the entry, closed before the session ends, and short exposure ≤ 25 % of the trading budget (a per-account limit that can be lowered, never raised past 25 %). | CLAUDE.md §2.3 |
| D20 | The risk engine is the pure `domain.AssessEntry` and is deliberately **not injectable**: the unused `ports.RiskEngine` is removed so no wiring can swap in a laxer engine. | ARCHITECTURE |
| D21 | **Exits never depend on the agent:** a supervisor runs for every eToro account whatever the agent state (running/paused/stopped/killed) and closes positions due (flatten, non-conforming, bad stop). It needs no LLM, no prices and no reconciliation; when the portfolio read fails it uses the locally recorded agent positions. | ARCHITECTURE |
| D22 | **Single writer per account:** a lease row (`agent_leases`, 30 s) must be held to send any order; a second process (two standalone binaries on one SQLite file, an overlapping container) sends nothing. | ARCHITECTURE |
| D23 | The reviewer's size multiplier is converted to integer basis points at the LLM adapter edge (floor); `domain.ApplyVerdict` can only shrink. | domain/llm.go |
| D24 | Cash reservations come from our own non-final orders (`orders` table); eToro's `ordersForOpen` is counted as well (double counting accepted: conservative). An order in state `unknown` keeps its reservation and blocks entries until resolved. | domain/orders.go |
| D25 | Entries need a calendar: without one, or on any calendar error, every market is closed for entries (unlike the scanner's test convenience). | app/agent.go |
| D26 | The agent decides on **5-minute bars** at each bar close + 5 s (same interval as the scanner; pending OD3). | app/agent.go |
| D18 | One port, both schemes: with TLS on, the listener sniffs the first byte; plain HTTP is answered with a 308 redirect to `https://<same host:port><path>` (a browser typing `host:8453` no longer gets "Client sent an HTTP request to an HTTPS server"). | ADR-005 |

### Dependencies (phase 1)

| Module | Licence | Why not the standard library |
|---|---|---|
| `modernc.org/sqlite` | BSD-3 | pure-Go SQLite (no cgo → static binaries for 3 OSes) |
| `github.com/go-sql-driver/mysql` | MPL-2.0 | MariaDB wire protocol |
| `github.com/pressly/goose/v3` | MIT | versioned up/down migrations, both dialects (as cactusInv) |
| `github.com/oapi-codegen/runtime` | Apache-2.0 | runtime for the generated OpenAPI server |
| `github.com/prometheus/client_golang` | Apache-2.0 | `/metrics` exposition format |
| `golang.org/x/crypto` | BSD-3 | argon2id |
| `github.com/google/uuid` | BSD-3 | `x-request-id` UUIDs |
| `github.com/anthropics/anthropic-sdk-go/v2` | Apache-2.0 | the LLM reviewer's Anthropic provider (official SDK: structured output, server-side refusal fallback). Other providers use their OpenAI-compatible HTTP API with the standard library. |
| `github.com/coder/websocket` | ISC | eToro's price stream (`wss://ws.etoro.com/ws`); the standard library has no WebSocket client. Already in the module graph via testcontainers. |
| `github.com/testcontainers/testcontainers-go` (+ `modules/mariadb`) | MIT | MariaDB integration tests only (test build) |

## Deviations from the brief (for the owner)

1. **MariaDB in containers** (owner's instruction) instead of embedded storage everywhere.
   Standalone still needs nothing but the binary.
2. **Demo and real have separate key pairs** — the brief assumed one key pair with a mode
   switch; eToro binds keys to an environment. The mode switch picks which pair is loaded.
3. **"Available cash"** is `clientPortfolio.credit` from the PnL endpoint ("available trading
   balance in USD"). The `/api/v1/balances` family is *not* allowlisted (it sits with the
   money endpoints and is not needed). Equity = credit + Σ(position amount + unrealised PnL).
4. **Costs** come from eToro's `costs` endpoint (spread, markup, fees, tax incl. stamp duty)
   rather than a hand-made model; it has its own 20/min quota, so results are cached per
   instrument and size bucket.

5. **Several eToro accounts, per user, shareable** (owner, 2026-11-23) instead of the brief's
   single account. Each account has its own encrypted key pair and demo/real mode; the
   watchlist, risk limits, agent state and events are per account; access is owner / trade /
   view, capped by the user's role. The deployment's `ETORO_DEMO_*` keys become the
   "Demo (environment)" account of the first admin. Migration 00005 replaced the phase-2
   global watchlist (it could only hold entries added before accounts existed).

## Open questions (do not block phase 1)

- Q1 LSE shares: quote unit (GBP vs GBX) in rates/candles, and whether `settlementType: real`
  is offered for every LSE share in the watchlist. Answer in phase 2 from eligibility + rates.
- ~~Q2~~ **Answered 2026-11-23 (owner):** budgets and limits are set and enforced in **USD**
  (the account, `credit` and `orderCurrency` are USD). The UI may show a GBP equivalent at
  eToro's rate for information only; it never feeds a decision. Recorded as D16.
- Q3 LLM provider default: Anthropic, model `claude-sonnet-5-5` for cost/latency (configurable).

---

## Phase 1 verification (2026-11-22)

- `make check` green: gofumpt, golangci-lint 0 issues (depguard layering), vet, `go test -race`
  (domain 100 %, app 86.9 %), MariaDB 11 repository suite (testcontainers, migrations
  up→down→up), govulncheck clean, contracts in sync, web lint/typecheck/tests/build.
- Allowlist guardrail mutation-tested: a planted `/api/v1/money/transfers` literal fails the build.
- Standalone smoke (SQLite, verified TLS): setup token, wrong token 401, second setup 409,
  login cookie `HttpOnly; Secure; SameSite=Strict`, CSRF 403 without token / cross-origin,
  RBAC 403, API key 401/403/200, rate limit 429 after 5 failures, audit, metrics, headers;
  only ciphertext and argon2id hashes on disk.
- `./deploy.sh --install --dockerdesktop` (MariaDB): healthy; in-container `healthcheck` over
  verified TLS; same smoke passes; `--version` (schema 1, mariadb), `--status`, `--backup-now`
  (no plaintext credential in the dump). `make build-in-container` cross-builds darwin/arm64.
- UI checked in a headless browser: sign-in, dashboard, credentials, Swagger UI (20 operations).
- **Not verified:** the eToro keys themselves — `ETORO_DEMO_PUB`/`ETORO_DEMO_PK` were not set
  in this session's environment, and phase 1 has no eToro client. Phase 2.5 ("test
  connection", read-only `GET …/demo/pnl`) is the first live call.

## Phase 0 — conventions and plan — `done`

| ID | Step | Status |
|---|---|---|
| 0.1 | Read cactusInv / cactusDB / cactusFS conventions (layout, CLAUDE.md, agents, deploy.sh, licence) | done |
| 0.2 | Fetch eToro `llms.txt`, guides and `openapi.json` v1.419.0; record verified facts in `docs/etoro-api.md` | done |
| 0.3 | `CLAUDE.md`, `AGENTS.md`, `.claude/agents/{planner,coder,reviewer}.md`, this plan | done |
| 0.4 | `git init`, public GitHub repo `albievan/cactustrade` (https://github.com/albievan/cactustrade), public-domain licence | done |

## Phase 1 — scaffold, auth/RBAC, secret store, UI shell — `done` (2026-11-22, awaiting review)

Acceptance: `make check` green; `make run` serves https://localhost:8443 with SQLite;
`./deploy.sh --install --dockerdesktop` runs app + MariaDB and `/readyz` is 200; first-run
setup → login → write-only credentials page → users/API keys work for the right roles.

| ID | Step | Acceptance | Status |
|---|---|---|---|
| 1.1 | Go module, Makefile (host + `*-in-container` targets), golangci (depguard layering), `.gitignore`, `.dockerignore` | `make lint` passes | done |
| 1.2 | Domain: roles/permissions, users, passwords policy, trading mode, credential names, errors | table tests, ≥80 % | done |
| 1.3 | Ports: all nine brief ports declared (`Broker`, `MarketData`, `SignalModel`, `Reviewer`, `RiskEngine`, `Clock`/`MarketCalendar`, `SecretStore`, repositories, `EventBus`) + fakes | compiles; fakes used by app tests | done |
| 1.4 | Migrations v1 in both dialects: users, sessions, api_keys, secrets, audit_log, settings, login_attempts; parity test | `TestMigrationParity` | done |
| 1.5 | `sqlstore` adapter (SQLite + MariaDB), shared repository suite | suite green on SQLite (`make test`) and MariaDB (`make test-int`) | done |
| 1.6 | Adapters: argon2id hasher, AES-256-GCM secret box, master-key loader, system clock, in-memory event bus | unit tests | done |
| 1.7 | App: setup (token-guarded), login/logout, sessions, API keys, users CRUD, credentials (write-only), audit, authorization | table tests, ≥80 % | done |
| 1.8 | OpenAPI `cactustrade.yaml` v1 + generated server; HTTP adapter: auth, CSRF, login rate limit, security headers, problem+json, `/healthz`, `/readyz`, `/metrics`, Swagger UI | handler tests | done |
| 1.9 | eToro allowlist table + guardrail test (no client yet) | `TestAllowlist*` | done |
| 1.10 | `cmd/cactustrade`: `serve`, `version`, `healthcheck`, `migrate status|up|down-to`, config from env/flags, TLS auto | `cactustrade version` | done |
| 1.11 | Web shell: login, first-run setup, dashboard placeholder with mode banner, credentials, users, API keys | `pnpm -C web check` | done |
| 1.12 | Dockerfile (distroless, non-root, read-only), `docker-compose.yml` (app + MariaDB), `deploy/k8s/cactustrade.yaml`, `.env`, `deploy.sh` | `--install --dockerdesktop` healthy | done |
| 1.13 | README (setup, risk warning, eToro keys), ADR-001…006 | reviewed | done |

## Owner requests 2026-11-23 (between phases 3 and 4)

| Id | Request | Decision | Status |
|---|---|---|---|
| R1 | Sign-in page like cactusDB, same logo | Split page: green panel saying what cactusTrade is, the form on "paper"; cactusDB's circuit-cactus logo and palette (owner asked to reuse the logo; the code is new). | done |
| R2 | Left navigation instead of the top menu | Sidebar with sections (Trading, Research, Settings, Admin); header keeps brand, mode, events, user. | done |
| R3 | Clear view of demo vs real | DEMO: teal "DEMO · practice money" pill and teal top stripe. REAL: red header band, red frame round the page and "REAL MONEY" pill. On every page, from the server's mode. | done |
| R4 | Events icon (top right) | Persisted event feed (`events` table): watchlist added/removed, scanner suggestions, orders, buys, sells, kill switch, limits, restarts. Bell with unread count per user; panel lists the newest. | done |
| R5 | Restart the app / reload settings | Admin: *Reload settings* re-reads credentials from the environment/secret files and the stored settings without a restart; *Restart* shuts down gracefully and re-executes the binary (containers: exits and the orchestrator restarts it). Audited. | done |
| R6 | LLM reviewer as its own menu item, choice of provider | Page *LLM reviewer*: provider (Anthropic, OpenAI, DeepSeek, OpenRouter, Kimi (Moonshot), Gemini, Grok (xAI), Ollama remote, Ollama local), model, base URL, write-only key, test button, and a plain explanation of what it does (it can only approve, shrink or reject a trade the model proposed; it never trades). | done |
| R7 | Each user can have their own eToro account and share it | `accounts` (owner, name, demo/real, keys per account) and `account_shares` (user, view or trade). The environment keys become the "Demo (environment)" account of the first admin. The agent, limits, watchlist and events are per account; a user picks the account in the header. Deviation from the brief's "one account" (owner, 2026-11-23). | done |
| R8 | The trader looks at other stocks, not only the watchlist | A scanner reviews a universe (eToro's tradable LSE/NYSE/NASDAQ shares, by popularity, paced inside the market-data quota) with the regime model and playbooks, and adds stocks that pass every rule to the watchlist (event; removable), up to a cap. | done |
| R10 | Shorts | See D19. Domain direction, eligibility for cfd/short/x1, risk engine (stop above entry, loss at stop, short cap), short playbooks, backtester, adapter constants. Orders themselves come with phase 4. | done (orders with phase 4) |
| R11 | Industries / themes (space, semiconductors…) | User-defined themes per account: a name, search words, an optional eToro stock industry, and picked shares. The scanner reviews the shares of the enabled themes. | done |
| R9 | Dashboard: which stocks are reviewed, against which rules | "Under review" board: every stock the scanner/agent looked at, when, its regime probabilities and each rule with pass/fail and the value. | done |
| R12 | E-mail, the cactusInv way | *Setup → E-mail* (admin): outgoing server with Gmail (app password) / Microsoft 365 / custom presets, a step-by-step guide, sender warnings, write-only password (never reused for another server or user), test e-mail. Fallback server from the deployment (`CACTUSTRADE_SMTP_*`, `smtp_password` secret; Docker: the Mailpit test mailbox). Outbox (migration 00007) with retries (1 min doubling to 4 h, given up after 10) — accepted mail is never sent twice. Each user sets an e-mail address and chooses trades / watchlist / alerts e-mails under *Your account* (only for accounts they can see; system alerts to admins). Password reset by e-mail on the sign-in page (single-use link, 30 min, ≤ 3 per user per hour, same answer for unknown users, rate-limited, needs `CACTUSTRADE_PUBLIC_URL`). | done |
| R13 | Watchlist lost after the accounts upgrade | Migration 00005 dropped the global watchlist (it was not empty after all). Restored the 9 rows from the pre-upgrade backup into the owner's account; lesson: a migration that drops data copies it first. Pages that need an account now say "No eToro account yet" with a link, instead of "Not found". | done |
| R14 | "Not found" on Themes | Same cause: the user had no eToro account yet (`domain.ErrNoAccount` → a 404 with the explanation). | done |
| R15 | Several LLMs, choose which one | Reviewer *profiles* (name, provider, model, address, time limit, own key `llm.<id>.api_key`); one is the default; the old single setup migrates into the first profile at start-up. "Load models" works with the key typed in, or the saved one only for the same provider and address. Provider errors are explained (e.g. 429 = the provider's quota, Gemini's array-shaped errors parsed). | done |
| R16 | A theme's tickers, removing them, an LLM per theme | Themes return their shares (symbol, name); chips with a remove button on the page. A theme can pick a reviewer profile (`themes.llm_profile_id`); none = the default. When reviewing is on, the scanner asks that reviewer before a share joins the watchlist; any failure is a "no" (fail closed), shown as a rule on the review board. | done |

## Phase 2 — eToro adapter (read-only) and market calendar — `done` (2.7 waits for the owner's demo keys)

| Step | What | Status |
|---|---|---|
| 2.1 | HTTP client (`internal/adapters/etoro/client.go`): keys from `SecretStore` per mode, new `x-request-id` per attempt, sliding-window limiter per quota group (`limiter.go`), 429 `Retry-After` pauses the group, jittered exponential backoff for reads, 15 s per attempt, 8 MiB response cap, errors never contain keys. **Mutating operations are refused before any I/O** (`ErrNotEnabled`) until phase 4. | done |
| 2.2 | Read operations: search (v2 free text), instruments, rates, candles, eligibility, costs (body built only from the guardrail constants), portfolio/PnL, orders:lookup, trade history. Spec-shaped synthetic fixtures in `testdata/` + `httptest` (92 % coverage). | done |
| 2.3 | Instrument cache + watchlist (migration 00003, both dialects, shared repository suite). Adding asks eToro: share on LSE/NYSE/NASDAQ and eligible as a real share, long, x1 — or refused with the reason. UI page *Watchlist* (search, add, remove, quotes every 15 s). | done |
| 2.4 | WebSocket price feed (`stream.go`): one connection, Authenticate with the mode's keys, `Subscribe {topics: instrument:<id>, snapshot: true}` on demand, rates parsed from string-or-number fields, binary heartbeats skipped, ping every 30 s, reconnect with backoff (and on a mode change), waits quietly while keys are missing. `Feed` serves stream quotes received ≤ 30 s ago and fetches only the others over REST. Message formats verified against api-portal.etoro.com/core/websocket (2026-11-23). | done |
| 2.5 | Test connection (`POST /api/v1/broker/test`, admin; one read-only PnL call) on the Credentials page; dashboard shows cash, equity, positions and market sessions. | done |
| 2.6 | `MarketCalendar` (`internal/adapters/calendar`): LSE 08:00–16:30 London, NYSE/NASDAQ 09:30–16:00 New York, DST from the embedded tz database, 2026–2027 holidays and half days verified against nyse.com, nasdaq.com, londonstockexchange.com and gov.uk (2026-11-23). Unknown year = closed + start-up warning. | done |
| 2.7 | Answer Q1 (LSE quote units, real settlement availability, `isPotential`) with the owner's demo keys; replace the synthetic fixtures with sanitised recordings. | blocked: needs the demo keys set |

## Phase 3 — regime model, playbooks, backtester — `in progress`

All in `internal/strategy`: pure, deterministic functions of closed bars (depguard: no I/O,
no ports, no adapters; floats allowed only here and at the eToro JSON edge).

| Step | What | Status |
|---|---|---|
| 3.1 | Features: log return, realised volatility (RMS of the last 20 returns), volume z-score. Feature t depends only on bars ≤ t (test). | done |
| 3.2 | Gaussian HMM (2–3 states, diagonal), Baum–Welch with deterministic volatility-quantile initialisation and variance floors; inference is the scaled forward filter only. Labels: highest-volatility state = high_volatility, then the higher mean-return/vol state = trending, else choppy. Property test: appending future observations never changes past filtered probabilities (25 random splits). | done |
| 3.3 | Playbooks (separate types): `TrendBreakout` (P(trending) ≥ 0.6, up-trend state, close above the 20-bar high; stop 3σ (≥ 0.2 %), target 2R), `MeanReversion` (P(choppy) ≥ 0.6, close ≥ 2 sd below the 20-bar mean; target the mean), no entries at P(high_volatility) ≥ 0.4. Size risks 0.5 % of equity at the stop, capped at 20 % of equity (the risk engine caps further in phase 4). `Engine` refits every 30 bars on the last 390 observations and filters in between. | done |
| 3.4 | `Backtest`: walk-forward with the same `Engine`; next-bar-open fills; exits only at prices the bar traded (stop at min(open, stop), target at max(open, target), stop first when both are in one bar); never across a session; costs from eToro's costs endpoint (spread half in/half out, fees and tax on the buy); amount ≤ cash. Fill rules tested with scripted signals. The phase-4 risk engine will be plugged into the same loop. | done |
| 3.5 | Report (`Report.String`): net-of-cost return vs buy-and-hold of the instrument, max drawdown, per-session Sharpe, win rate, trades, costs paid, and a verdict that says "lost money" / "worse than holding" plainly. `cactustrade backtest --instrument ID [--interval 1m --bars 1000 --equity 10000 --market NYSE]` runs it on eToro candles with the demo keys (read-only). | done |
| 3.6 | Run the backtest on the watchlist with real demo-account candles and record the (honest) results; tune nothing on the test period. | blocked: needs the demo keys |
| R17 | Trading 212 accounts | Owner's decision 2026-11-23: **read-only now, trading later.** The Trading 212 Public API (v0, beta, verified 2026-11-23 at docs.trading212.com) has no shorts/CFDs, no idempotency key on orders, no market data and no stop-loss on a market order, so trading needs rules agreed first. An account gets a `platform` (`etoro` default, `trading212`); a Trading 212 account stores its API key and secret (write-only) and is shown in the overview only: the agent, scanner, watchlist and orders refuse it. New adapter `internal/adapters/trading212` with its own allowlist (GET account summary, positions, order history) and an allowlist test; it implements a read-only `Holdings` port and has no mutating method. **Built 2026-11-23** (code, tests, docs): `domain.Platform`, `Account.Platform`, `ErrViewOnly`; migration 00008 (both dialects); `trading212` adapter (allowlist = exactly GET `/api/v0/equity/account/summary`, `/positions`, `/history/orders`; the "instruments" path is *not* allowlisted, nothing needs it); `app.account(...)` is the one place that refuses Trading 212 for every trading path (`app.anyAccount` for managing). Decisions: R17-a `CreateAccount` keeps its eToro signature, `CreateAccountOn` takes the platform; R17-b Trading 212 key names `t212.<id>.api_key` / `.api_secret`, stored through the existing apiKey/userKey fields; R17-c a Trading 212 account can never be the current account (the switcher lists eToro only) and its "trade" share means view; R17-d defence in depth: the eToro client and price stream refuse a non-eToro account before any I/O; R17-e the Trading 212 client waits at most 15 s for a rate-limit slot, retries a GET once on 429/5xx, and holds an endpoint until `x-ratelimit-reset` (≤ 1 min); R17-g the background scanner skips view-only accounts too (`ScanAll`, reviewer finding). R17-f depguard: the LLM adapter may not import `trading212`, `trading212` may not import `etoro` or `app`. Facts: `docs/trading212-api.md`. | done (2026-11-23; `make check` green incl. MariaDB `test-int` with 00008) |
| R18 | All positions across platforms + a graph | Page *Overview* (first under Trading): totals per currency (no FX conversion — amounts in different currencies are never added), one card per account (platform, demo/real, cash, equity, unrealised P&L, or the error), every open position with its platform, and an equity chart per account over time. Equity snapshots (`equity_snapshots`, migration 00008) are recorded every 15 minutes for each account with keys and kept 400 days. Chart is inline SVG (no new dependency). **Built 2026-11-23**: `app.Overview` (accounts read in parallel, 30 s each; missing keys = "no keys yet"; an error never fails the call), `app.EquityHistory` (1–400 days, default 30), `app.RecordEquity` (system, every 15 min from 1 min after start, purges > 400 days); `GET /api/v1/overview`, `GET /api/v1/overview/history?days=`; page *Overview* (first under Trading). Decisions: R18-a eToro holdings are built in the app from `Broker.Portfolio` + the instrument cache/`MarketData.Instruments` (names) and `Quotes` (current price; cosmetic, failures leave it blank) — no eToro change; R18-b amounts are minor units of the account currency (`MinorUnits` in the contract; 2-decimal currencies assumed); R18-c the chart draws **one chart per currency** (lines per account) so different currencies never share an axis; R18-d totals per currency are computed in the domain (`TotalsByCurrency`); R18-e owner's follow-up (2026-11-23): page renamed *Trading Platform Accounts* (nav "Platform accounts"), card grid with an icon button column, keys optional in the create form, version/build under the brand. | done (2026-11-23; `make check` green incl. MariaDB `test-int`) |
| R19 | Trade on the ASX — which platform? | eToro lists ASX shares (e.g. `CBA.ASX`, priced in AUD; eToro's market page, 2026-11-23); whether each is a real share long ×1 is decided per share by eToro's eligibility check when it is added to the watchlist (the same rule as today). Trading 212's API: ASX not verifiable (its pages refuse automated reads). moomoo/Futu OpenAPI: no Australian market (verified). Tiger OpenAPI: AU only on live "comprehensive" accounts, its paper accounts have no AU. IBKR and Saxo have APIs with paper/SIM accounts but their ASX/AU details could not be read. Decision: add ASX to cactusTrade through eToro — market `ASX` (10:00–16:00 Sydney, holidays table verified at asx.com.au), symbol suffix `.ASX`. Built: `domain.MarketASX`, `.ASX` suffix, calendar table (10:00–16:00 Sydney, 14:10 early closes, holidays 2026–2027 from asx.com.au), dashboard and messages. With three markets the scanner now reviews each share only while its own market is in session (reviewer finding; before, LSE shares were also reviewed after their close while NYSE traded). | done (2026-11-23) |

| R20 | UI: grids with icon buttons first, provider logos, version, foldable help | LLM profiles and *Trading Platform Accounts* (renamed from "eToro accounts"; platform chosen first, then that platform's keys) are grids of cards whose first column is a vertical group of icon buttons. LLM cards show the provider's mark (Bootstrap Icons brand glyph where one exists — Claude, OpenAI, Google — else a coloured monogram; no logos drawn by us). Version and build under the brand. *What it does* on the LLM page folds away (`InfoCard`, remembered per browser). | done |
## Phase 4 — risk engine, demo execution, reconciliation, kill switch — `in progress`

The owner asked in writing on 2026-11-23 for phases 4 and 5 to be completed together
("complete the next two phases"). That waives the stop-for-review between phases 4 and 5
only. 4.17 and 5.8 still need the owner.

Demo only. Real-mode accounts are refused in four places: the risk engine (4.1), the eToro
client before any I/O (4.6), the executor (4.7) and agent Start (4.11). Real trading stays
behind the phase-7 gate. Every step ends with a green `make check`. Order: domain → ports →
adapters → app → contract → wiring → web.

### Owner decisions (OD1–OD13)

OD1, OD5, OD6 and OD9 were answered by the owner on 2026-11-23 (marked **owner**). The others
are the **default until the owner answers**.

- **OD1 – Positions cactusTrade did not open (owner: leave them alone):** never closed by cactusTrade; counted against cash, budget and the open-position cap; a non-conforming one blocks entries and raises an alert; the kill switch closes only cactusTrade's positions. (The comment on `domain.Position.Conforms`, "closed at once", is corrected to match unless the owner chooses auto-close.) Needed by 4.8.
- **OD2 – Resending an open order after an unclear failure:** never; only after a 429, with the same id, and only after a lookup returns 404. Unresolved after 2 min → abandoned. Entries stay blocked while any order is unknown. Needed by 4.7.
- **OD3 – Agent bars and pace:** 5-minute bars, a decision at each bar close. Needed by 4.10.
- **OD4 – Risk-day boundary:** 22:00 UTC. Needed by 4.1.
- **OD5 – After a restart, agents that were running (owner: resume by itself):** resume automatically only after a clean start-up reconciliation of that account (and only demo, not killed, limits valid, keys present); otherwise they come back paused with exits active and an alert. Either way an event records it. Needed by 4.11.
- **OD6 – Shutdown with open positions (owner: keep them):** close nothing (a crash cannot either); positions carry eToro-side stops and the supervisor closes leftovers at the next chance, with an alert. Needed by 4.14.
- **OD7 – Reviewer required for entries?** Follows the existing on/off switch; when on, any failure is a reject. Needed by 4.10.
- **OD8 – Cost gate:** reject when eToro's costs ≥ the gross gain at take-profit, or when the costs call fails. Needed by 4.10.
- **OD9 – LSE entries (owner: none until checked):** off until Q1/U4 (GBX vs GBP stop units) is verified in 4.17; NYSE and ASX on, with the stop checked after every fill. Needed by 4.10.
- **OD10 – Kill reset:** admin, typed phrase, high-water mark reset to current equity, agent comes back stopped. Needed by 4.11.
- **OD11 – API keys:** kill, pause and stop allowed with an API key; start and reset need a web session. Needed by 4.11.
- **OD12 – Retention:** decisions and LLM calls (incl. prompt and response) kept 400 days, like equity snapshots. Needed by 5.5.
- **OD13 – LLM prices:** the admin enters prices per profile; unknown means cost "unknown"; no built-in price table. Needed by 5.2.

Unverified eToro facts, each handled fail-closed until 4.17 settles it: U1 resend semantics of
the same `x-request-id` on open; U2 delay until `orders:lookup` finds a new order; U3 no
reference lookup for closes, and a second close of a closed position; U4 LSE stop/rate units
(Q1); U5 whether the newest candle is still forming; U6 `isPotential`; U7 whether a 429 is
answered before processing; U8 cancelling a status-11 order; U9 partial fills of orders by
amount; U10 whether `credit` excludes cash held for pending orders; U11 how quickly a fill
shows in `pnl`.

| ID | Step | Files | Acceptance (tests that prove it) | Status |
|---|---|---|---|---|
| 4.1 | **Risk rules completed (domain).** `RiskDay(t)`: a trading day starts at 22:00 UTC, when no LSE/NYSE/ASX session is open. `RiskState.Observe(equity, day)` resets start-of-day equity and the latch when the day changes. `DayStopped` latch: once equity ≤ the daily floor, no entries until the next risk day. `DrawdownBreached(equity, st, l)` is used by `AssessEntry` and by the supervisor on every tick. New refusals in `RiskInput`: `Mode` ≠ demo ("real mode is gated, phase 7"); an instrument with an order in flight (`PendingInstruments`); eligibility older than `EligibilityMaxAge` (24 h); `Blocked` instrument. | `internal/domain/risk.go`, `risk_test.go` | `go test ./internal/domain -run 'AssessEntry\|RiskDay\|Observe'`. Refusal table gains one row per new rule. `TestAssessEntryProperties` adds: never allowed when real, latched, blocked, in flight or with stale eligibility. New property test `TestDailyLatch`: random equity paths never allow an entry after the floor until `RiskDay` changes. `TestRiskDay` covers DST on both sides of 22:00 UTC. | done |
| 4.2 | **Orders, exits, verdict (domain).** `Order` aggregate: kinds open/close/cancel. States created → sent → pending → filled / partially_filled / rejected / cancelled / unknown / abandoned. `Transition` refuses illegal moves, and final states stay final. `Reservations(orders)` returns reserved amount, count, short part and in-flight instruments. `IntentFor(order)` is the only place an `OrderIntent` is built (RequestID = order id). `ExitsDue(positions, sessions, now, limits)` gives the exits and why: flatten due, unknown market or calendar error, non-conforming, missing stop or stop on the wrong side. `Verdict.SizeMultiplier` becomes `SizeBp BasisPoints`; `ClampVerdict` and `ApplyVerdict(size, v)` shrink only; the LLM adapter's `parseVerdict` converts float → bp with floor. | `internal/domain/{orders.go,orders_test.go,exits.go,exits_test.go,trading.go,llm.go,llm_test.go}`, `internal/adapters/llm/llm.go` (+test), `internal/app/scanner.go` (compile fix) | `TestOrderTransitions` checks every pair of states. `TestReservations`. Property `TestApplyVerdictNeverEnlarges` (random sizes, bp, kinds, NaN/Inf at the edge). Property `TestExitsDue`: for any now ≥ close − FlattenBeforeClose every position is due, and every non-conforming position is always due. | done |
| 4.3 | **Calendar cross-check (test only).** For every 2026–2027 trading day of LSE, NYSE and ASX, including half days and DST changes: `FlattenDue` is true at close−10 min and false at close−11 min; `AssessEntry` refuses from close−30 min; no session contains the 22:00 UTC risk-day boundary. | `internal/adapters/calendar/flatten_test.go` | `go test ./internal/adapters/calendar -run 'Flatten\|RiskDay'` | done |
| 4.4 | **Ports and fakes.** `Broker.Close(ctx, positionID, instrument, requestID)`; new `Broker.CloseOrder(ctx, orderID)`. `OrderStatus` gains `Executions []PositionExecution{PositionID, Units, Amount, OpenRate, StopLossRate}` plus asset settlement, leverage and side. New errors `domain.ErrOutcomeUnknown` (sent, answer lost/5xx/timeout), `domain.ErrNotAccepted` (4xx/429; carries RetryAfter) and `domain.ErrRealModeGated`. Repositories `Orders`, `AgentPositions`, `RiskStates`, `InstrumentBlocks`, `AgentLeases`. Remove `ports.RiskEngine` (D20). Fakes: in-memory repositories; `fakes.SimBroker`, a simulated eToro account with scripted outcomes (filled, pending→filled, rejected, partial, lost answer, 429, 5xx, non-conforming fill, missing stop) that records every intent and can assert the order row exists when `Open` is called. `fakes.Broker` keeps refusing orders. | `internal/ports/ports.go`, `internal/ports/fakes/{fakes.go,broker.go,simbroker.go}`, `internal/domain/errors.go`, `docs/ARCHITECTURE.md` | `go build ./... && go test ./internal/ports/...` | done |
| 4.5 | **Migration 00009 + repositories.** Same version in both dialects; portable SQL; upserts in Go. Tables: `orders` (`request_id` PK, account_id FK, mode, kind, direction, instrument_id, amount_cents, entry/stop/take-profit micros, position_id, target_order_id, etoro_order_id, state, reason, sends, created/sent/updated_at); `agent_positions` (account_id, position_id PK, open_request_id, instrument_id, direction, opened_at, closed_at, close_request_id); `risk_state` (account_id PK, risk_day, start_of_day_equity, high_water_mark, day_stopped, killed, kill_reason, killed_at, updated_at); `instrument_blocks` (account_id, instrument_id PK, reason, at); `agent_leases` (account_id PK, holder, expires_at). Indexes on (account_id, state) and created_at. | `migrations/sqlite/00009_execution.sql`, `migrations/mariadb/00009_execution.sql`, `internal/adapters/sqlstore/{execution.go,sqlstore.go}`, `storetest/storetest.go` | `make test` and `make test-int`. `TestMigrationParity`. Shared suite: duplicate request id → ErrConflict; `Update` from a stale state → ErrConflict (no lost transition); `ListOpen` excludes final orders; risk-state upsert; two lease holders → exactly one wins; an expired lease is taken over; MariaDB up→down→up. | done |
| 4.6 | **eToro execution, demo only.** `execute.go`: the blanket `ErrNotEnabled` becomes "mutations only for demo accounts". A real-mode account is refused before any I/O. `x-request-id` = the given id (must be a UUID). Mutations never retry on network error, 5xx or timeout (→ `ErrOutcomeUnknown`). 429 → `ErrNotAccepted` + limiter blocked. Other 4xx → `ErrNotAccepted` with eToro's message. `Open`: body struct has exactly `action, transaction, instrumentId, settlementType, orderType, leverage, amount, orderCurrency, stopLossRate, takeProfitRate, stopLossType`, built from the constants and the intent; refused without a stop; a `referenceId` other than our id → `ErrOutcomeUnknown`. `Lookup` extended (asset, executions). `Close` sends body `{InstrumentID}` only (never `UnitsToDeduct`). `CloseOrder`. `Cancel`. | `internal/adapters/etoro/{execute.go,execute_test.go,client.go,read.go}`, `testdata/{open,lookup_pending,lookup_rejected,lookup_partial,lookup_short,close,close_order}.json` (synthetic, spec-shaped) | `go test ./internal/adapters/etoro/...`, coverage ≥ 90 %. Golden JSON bodies for long (`buy`/`real`/1) and short (`sellShort`/`cfd`/1); no other keys. Request header `x-request-id` == intent id. 500 and timeout → exactly one HTTP request. 429 → one request, limiter blocked. Real account → zero requests. The 21st execution call in 60 s waits (fake clock). Allowlist tests unchanged and green. | done |
| 4.7 | **Executor (app).** `placeEntry`: refuse unless eToro, demo, not killed and lease held. Persist the order (`created`); a store failure means nothing is sent. Re-check portfolio age ≤ `MaxPortfolioAge`. Mark `sent` (persisted) → `Open`. On a 429: lookup; if 404, resend the **same id** after Retry-After (≤ 3 sends). Outcome unknown → resolve by lookup, never resend (OD2). Poll `orders:lookup` at 1, 2, 4, 8, 15 and 30 s. Filled or partial → record `agent_positions` and verify each execution: real/long/×1 or cfd/short/×1, stop present and on the right side, close to the stop we sent. Otherwise close at once, block the instrument and alert. Rejected → release. Still pending at 60 s → cancel and poll. Lookup still 404 after 2 min → `abandoned` (never resent). `closePosition`: persist the close order → `Close` → poll `CloseOrder`. If the answer is lost, re-read the portfolio; if the position is still open, resend with the same id (≤ 5). `cancelOrder`. Events `order.*` (trade e-mails via R12). | `internal/app/executor.go`, `executor_test.go` | `go test -race ./internal/app -run Executor`. Table cases: row persisted **before** `Open` (SimBroker hook); 429 → two `Open` calls with one id; unknown outcome → one `Open`, resolved by lookup; 404 for 2 min → abandoned, no resend; rejected → reservation freed; pending → cancelled; leverage-2, CFD-long or no-stop fill → closed + blocked; real account → zero broker calls + `ErrRealModeGated`; persist failure → zero broker calls; lost close answer → resent with the same id; close works while killed. | todo |
| 4.8 | **Reconciliation.** `Reconcile(account)`: fresh portfolio. Resolve every non-final order by referenceId: `created` + lookup found → adopt; 404 and older than 2 min → abandoned. Match `agent_positions` with eToro: ours gone → closed locally. Foreign positions handled per OD1. Sets the in-memory `reconciled` flag with time and reason; any error or mismatch → not reconciled, warning event + alert. `ReconcileAll` at start-up for every eToro account with keys (read-only for real accounts), then every 5 min from the supervisor. | `internal/app/reconcile.go`, `reconcile_test.go` | `go test -race ./internal/app -run Reconcile`. Clean account → reconciled. A crash between persist and send is adopted or abandoned correctly. Foreign non-conforming position → not reconciled + event. Broker error → not reconciled. `AgentTick` before `ReconcileAll` → SimBroker `Opens == 0`. | todo |
| 4.9 | **Supervisor (exits, D21).** `Supervise(account)` every 10 s: lease → fresh portfolio (on failure, local agent positions) → `Observe` (risk day, high-water mark, latch, persisted) → `DrawdownBreached` → Kill. `ExitsDue` uses the calendar session of each instrument's own market → `closePosition`. Pending orders older than 60 s → cancel. Periodic reconcile. Never calls the LLM or needs prices. | `internal/app/supervisor.go`, `supervisor_test.go` | `go test -race ./internal/app -run Supervise`. An LSE, an NYSE and an ASX position (fake calendar incl. a half day) are closed at close−10 min, not at close−11 min. Exits still happen when killed, stopped, paused, unreconciled, when the portfolio read fails (local positions used), and when market data or the LLM fail. Drawdown → killed, all agent positions closed, no `Open`. Lease held by another process → no mutations. | todo |
| 4.10 | **Agent loop (entries).** `AgentTick(account)` runs only when running, lease held, reconciled, not killed, demo, calendar present (D25). Steps, one entry at a time per account (mutex): (1) watchlist shares whose market is in session (LSE per OD9). (2) Candles: 5 min, 400, unclosed bar dropped (D26/U5). (3) `strategy` evaluate, sized by min(budget, equity); shorts only if `MaxShort > 0`. (4) Cost check (OD8). (5) LLM verdict when reviewing is on: any error/timeout/invalid answer = reject; `ApplyVerdict`. (6) Fresh portfolio + fresh quote (entry = ask for a long, bid for a short; stop still on the right side). (7) Reservations from `orders`. (8) `domain.AssessEntry`. (9) `placeEntry`. Each step's outcome is kept in a trace struct (persisted in phase 5). | `internal/app/agent.go`, `agent_test.go` | `go test -race ./internal/app -run AgentTick`. Approve → `Open` amount ≤ min(fresh cash − reserved, remaining budget). LLM "reduce 0.5" → halved and then capped. An LLM multiplier 3 or "increase" never enlarges. Each of LLM error/timeout, stale quote, stale portfolio, unreconciled, killed, paused, stopped, real, no calendar, budget 0, order in flight → no `Open`. Short: stop > entry; open + pending shorts ≤ 25 % of budget. Every intent: direction ∈ {long, short}, RequestID persisted. | todo |
| 4.11 | **Agent control, kill switch, limits lock.** `StartAgent`: agent.control + trade access, web session only, eToro demo, keys, valid limits, budget > 0, reconciled, not killed. `PauseAgent` / `StopAgent`: entries stop, exits continue (D21). `Kill`: agent.kill + trade access, session or API key. It persists killed (`risk_state` + `agent.state`) **first**, cancels the entry pipeline's context, cancels pending orders, then closes every agent position through the executor, paced by the execution limiter. `ResetKill`: admin, web session, typed phrase `RESET <account name>`, high-water mark := current equity, state → stopped. A per-account mutex serialises Start / SetRiskLimits / Kill / Reset (limits only while stopped, no race). After a restart, agents that were running come back per OD5. All audited, with events. | `internal/app/{agent_control.go,risk.go}`, `agent_control_test.go` | `go test -race ./internal/app -run 'Agent\|Kill\|RiskLimits'`. Role × access matrix. Start refused for a real, killed or unreconciled account and for budget 0. SetRiskLimits while running, paused or killed → 409. 100× concurrent Start vs SetRiskLimits never leaves the agent running with limits written after the start. Kill with 15 positions + pending orders → all cancels and closes sent within one 60 s window of the fake limiter (≤ 20), no `Open` after Kill. API key: kill allowed, start and reset refused. Reset needs admin + phrase. | todo |
| 4.12 | **Sequence property test (app).** 300 seeded scenarios × 200 ticks, `math/rand/v2` (no new dependency). Random prices, fills, rejections, lost answers, 429s, LLM verdicts (incl. garbage), limits, shorts, with supervisor and agent running. After every tick: only the two order shapes; id persisted before send; Σ(open + reserved) ≤ min(budget, equity at decision); no `Open` > fresh cash − reserved; shorts ≤ MaxShort of budget ≤ 25 %; long stop < entry < short stop; no `Open` after kill, drawdown or the daily latch; no agent position open after its market's close − flatten; open orders are resent only after a 429, with the same id. | `internal/app/agent_property_test.go` | `go test -race ./internal/app -run AgentSequenceProperties`; fails if fewer than 500 entries were exercised | todo |
| 4.13 | **Contract and handlers.** Contract first in `cactustrade.yaml`, then `make gen`. `GET /api/v1/agent` (viewer): state, reconciled, kill reason, risk-day equity / high-water mark / floors, blocked instruments, next flatten per market. `POST /api/v1/agent/start` (operator, session), `/pause`, `/stop`, `/kill` (operator, session or key), `/reset` (admin, session, `{confirm}`), `/reconcile` (operator). `GET /api/v1/orders?limit=` (viewer). | `api/openapi/cactustrade.yaml`, `internal/adapters/http/{api.gen.go,handlers_agent.go,agent_test.go}`, `web/src/lib/schema.d.ts` | `make gen && go test ./internal/adapters/http -run Agent`. Status codes 200/403/409/422, CSRF on every mutation, API key refused for start and reset, contracts drift check green. | todo |
| 4.14 | **Wiring.** Start-up order: migrate → `app.New` → env account / LLM migration → **`ReconcileAll` before any supervisor or agent loop** (scanner and equity loops are read-only) → `supervisorLoop` (10 s; one goroutine with a timeout per account, so one account's exits never wait for another's) → `agentLoop` (bar close + 5 s). Lease holder = random id per process. Graceful shutdown stops the loops and releases leases; positions handled per OD6. | `cmd/cactustrade/main.go`, `main_test.go` | `go test ./cmd/cactustrade` checks that an ordering hook sees `ReconcileAll` before the first `Supervise`; `make build` | todo |
| 4.15 | **Web.** Dashboard *Agent* panel: state, reconciled, kill reason, distance to daily floor and drawdown; Start / Pause / Stop; red kill-switch button with a confirmation dialog; Reset (admin, typed phrase). Controls disabled with "real trading needs the phase-7 gate" on real accounts. Remove the "orders arrive in phase 4" note. New *Orders* page under Trading. Risk limits page explains paused and killed. | `web/src/components/AgentPanel.tsx`, `web/src/pages/{Dashboard.tsx,Orders.tsx,RiskLimits.tsx}`, `components/Layout.tsx`, `lib/api.ts`, tests | `pnpm -C web check`. Vitest: buttons by role, kill confirmation, real account disabled, limits form disabled unless stopped. | todo |
| 4.16 | **Docs.** ARCHITECTURE: execution, supervisor, lease, D20–D26. `docs/etoro-api.md`: execution facts plus the unverified list U1–U11. README risk note. | docs | `make check` | todo |
| 4.17 | **Live demo verification, run by the owner.** One minimal long and one short on an NYSE share in session. Record sanitised fixtures (open, lookup pending/filled, close, close-orders). Settle U1–U11. Kill switch with 2 positions. Restart mid-position → reconciliation adopts it. Never real. | `testdata/`, `docs/etoro-api.md` | Fixture tests replaced by recordings and green | blocked: needs the demo keys |

Guardrails touched (CLAUDE.md §2): §2.1 no new path (allowlist test unchanged). §2.2 4.1, 4.7 (portfolio age re-checked before send), 4.10, 4.12. §2.3 4.6 (golden bodies), 4.7 (check after fill), 4.1/4.10/4.12 (short stop and 25 % cap). §2.4 4.2, 4.7, 4.12 (reservations). §2.5 4.2 (`ApplyVerdict`) and depguard. §2.6 4.9, 4.10, 4.11. §2.7 4.7, 4.8, 4.14. Demo only: 4.1, 4.6, 4.7, 4.11.

### Steps 4.1–4.3: decisions (2026-11-23)

- **Risk day:** `domain.RiskDay(t)` returns a `RiskDate` "YYYY-MM-DD" named after the later of
  the two dates (22:00 UTC Sunday → Monday), so Sydney's session that opens at 23:00 UTC belongs
  to the same risk day as London and New York. Dates compare in time order as strings.
- **`Observe(equity, day, limits)`** takes the limits (the step said `Observe(equity, day)`):
  the daily latch is set in the same call that resets the day, so no caller can update the
  state and forget the latch. A *later* day resets start-of-day equity and the latch; an
  *earlier* day (clock going backwards) resets nothing (fail closed). `Killed` is never touched
  by `Observe`: the supervisor calls `DrawdownBreached` and trips the kill switch itself.
- **Extra refusals in `AssessEntry`** besides the step's (mode ≠ demo, latch, in flight, stale
  eligibility, blocked): the risk state must belong to today's risk day (otherwise yesterday's
  start-of-day equity would set today's floor), and `UnknownOrders > 0` (OD2/D24: entries wait
  for every order with an unknown outcome). A zero `Mode` is refused like real.
- **Order lifecycle:** `sent → sent` is a resend with the same id, capped by `MaxSends`
  (open 3, close 5, cancel 3); there is no edge `unknown → sent`, so an unclear outcome can never
  be resent (OD2). `created` may move straight to a broker state (reconciliation adopts it) or to
  `abandoned`. `partially_filled` is not final. Illegal moves return
  `domain.ErrIllegalTransition` (wraps `ErrConflict`) and leave the order unchanged.
- **`Reservations`:** only open orders reserve cash and count against the position cap; every
  non-final order of any kind (close and cancel too) puts its instrument "in flight", and an
  unknown order of any kind blocks entries. `Reservation.Apply(RiskInput)` is the one way the
  reservation reaches the engine.
- **`IntentFor`** refuses a non-demo order (a fifth demo-only gate; 4.4 may switch its error to
  `ErrRealModeGated`) and any state but created/sent. `OrderIntent` keeps exported fields (the
  adapter and fakes read them); "only `IntentFor` builds one" is checked by 4.12, not the compiler.
- **`ExitsDue`:** the caller passes only cactusTrade's own positions (OD1). Besides the step's
  reasons, a position is also due *before* its session opens (nothing is held overnight), and
  limits with a flatten time under 5 min fall back to the default. A stop is "on the wrong side"
  relative to the *open rate*; if stops are ever moved (break-even, trailing), this rule must
  compare with the current price instead.
- **Verdict (D23):** the LLM adapter converts the multiplier with `floor(f × 10 000)`; NaN,
  ±Inf, 0 and negatives are 0 bp (→ reject), ≥ 1 is 10 000. `ApplyVerdict` re-clamps and is
  overflow-safe for any `int64`. The API's `sizeMultiplier` on the LLM test endpoint is shown as
  `SizeBp / 10 000` (no contract change). `internal/app/scanner.go` needed no change: it only
  reads the verdict's kind.
- **Unrelated fix needed for a green `make check`:** the session cookie's `Max-Age` was computed
  with the wall clock (`time.Until`) against an expiry from the app's clock; with the HTTP
  tests' fixed clock (09:00 UTC) every sign-in cookie expired at once after 21:00 UTC. It is now
  `ExpiresAt − CreatedAt` of the session (`internal/adapters/http/handlers.go`); production
  behaviour is the same.

### Steps 4.4–4.6: decisions (2026-11-23)

- **4.4-a `Cancel` carries a request id too** (`Cancel(ctx, orderID, requestID)`; the step left
  it unchanged): every account change, cancels included, is sent with an `x-request-id` the
  caller persisted first (the `cancel` order row), so §2.7 holds without exceptions.
- **4.4-b `OrderStatus`:** `PositionIDs` is replaced by `Executions []PositionExecution`
  (`PositionID, Units, Amount, OpenRate, StopLossRate` and, for closes, `CloseRate`); new
  `Final` (eToro reports a terminal status), `Instrument`, `Settlement` (`domain.Settlement`:
  `real`/`cfd`, anything else kept as reported), `Leverage`, `Side`. `OrderStatus.Conforms(d)`
  (domain) is the fill check 4.7 uses: right side, settlement real for a long / cfd for a
  short, leverage 1, every execution with a stop on the losing side of its open rate.
- **4.4-c Errors:** `*domain.NotAcceptedError{RateLimited, RetryAfter, Reason, Err}` matches
  `ErrNotAccepted` and the broker error it wraps (e.g. `ErrNotFound` for a closed position);
  only `RateLimited` (a 429) allows an open to be resent (OD2). `ErrOutcomeUnknown` and
  `ErrNotAccepted` wrap `ErrUnavailable` (existing handlers fail closed); `ErrRealModeGated`
  wraps `ErrForbidden` and is now `IntentFor`'s real-mode error.
- **4.4-d Repository contracts:** `Orders.Update(prev, next)` is a compare-and-set on
  *(state, sends)*, not state alone, so two resends from one version cannot both win.
  `RiskStates.Put` is versioned (column `version`, an addition to the step's column list):
  a stale write is `ErrConflict`, so an `Observe` from an older read can never clear a kill.
  `AgentLeases.Acquire(account, holder, now, until)` / `Release` / `Get`;
  `AgentPositions.Add/Get/Update/ListOpen`; `InstrumentBlocks.Block/Unblock/List`. An unknown
  account is `ErrNotFound` in every repository (checked explicitly, same in both databases).
- **4.4-e Fakes:** `fakes.SimBroker` scripts opens and closes (fill, pending→fill, pending,
  reject, partial, lost answer, 429, 5xx, 4xx, leverage 2, wrong settlement, no stop), keeps
  cash/positions/pending amounts, refuses real accounts like the client, and records intents,
  close/cancel request ids and `Violations` (a failed `OnOpen` hook, or an open that reuses an
  id the broker already has). `fakes.RequireOrderRow(orders)` is the hook: the row must exist
  as a *sent open* equal to the intent. `fakes.Store.OrdersErr` simulates a failed persist.
  `fakes.Broker` still refuses every order.
- **4.5-a Schema:** unset times are 0, not NULL (`orders.sent_at`, `agent_positions.closed_at`,
  `risk_state.killed_at`), like the outbox. `agent_positions` key is (account, position);
  `instrument_blocks` has no foreign key to `instruments` (a block must work for any id). All
  five tables cascade with their account. The lease uses two autocommit statements (UPDATE
  renew/take-over, else INSERT) without a transaction, so no InnoDB gap lock is held between
  them; the primary key makes exactly one concurrent inserter win.
- **4.6-a Only `mutate` changes the account:** the read path (`call`, with retries and a fresh
  id per attempt) still refuses every mutating operation; `set_sl_tp` stays `ErrNotEnabled`
  (not in this step's scope, and deepening a stop can move balance into margin). An account
  with no mode is refused like real.
- **4.6-b Request ids** must be canonical 36-character UUIDs (own check: `uuid.Parse`
  accepts other encodings by its own documentation).
- **4.6-c `takeProfitRate` is omitted when 0** (the spec allows 0 but does not say it means
  "none"); the golden tests pin long with a target, short without, short with.
- **4.6-d Answers that do not prove acceptance are "unknown":** an open answer without an
  `orderId` or with another `referenceId`; a close answer without `orderForClose.orderID` or
  for another position; an empty, unreadable or oversized 2xx; a 1xx/3xx status. A context
  that ends while waiting for the execution quota is *not sent* (its own error, not unknown).
- **4.6-e Redirects are never followed** (security fix found while testing): Go's client
  forwards custom headers such as `x-api-key`/`x-user-key` on a redirect, even to another
  host, and turns a redirected POST into a GET outside the allowlist. Applies to reads too;
  a 3xx fails closed.
- **4.6-f Status ids** extended from the spec: 6 PendingCancel and 12 pending, 7 Canceled and
  8 Expired → cancelled, 9 CanceledPartiallyFilled and 10 RejectedPartiallyFilled → partial
  *and final*. `asset.side` is read as `long|short` (schema) or `buy|sell` (eToro's example).

## Phase 5 — LLM reviewer and decision log — `todo`

| ID | Step | Files | Acceptance | Status |
|---|---|---|---|---|
| 5.1 | Reviewer port and providers: no tools, strict JSON, timeout = reject, invalid JSON = reject, multiplier clamped (R6, R15, R16; bp conversion in 4.2) | `internal/adapters/llm`, `internal/domain/llm.go` | `TestParseVerdict`, `TestOpenAIEmptyAndTimeout`, `TestAnthropic`, `TestClampVerdict` | done |
| 5.2 | **Domain.** `LLMCall` (id, account, purpose agent/scanner/test, profile, provider, model served, prompt, response, input/output tokens, latency, cost micros, −1 = unknown, error, at). `LLMProfile` prices: USD per million input and output tokens as `Micros`, 0 = unknown; `Cost(in, out)` in integers. `Decision`: signal (playbook, direction, entry, stop, take-profit, suggested size, regime probabilities, reason), verdict (kind, bp, reason, call id), risk (allowed, size, reason), outcome (`rejected_reviewer`, `rejected_cost`, `rejected_risk`, `ordered`, `order_failed`), order request id. `Truncate` at 32 KiB, UTF-8 safe. | `internal/domain/{decisions.go,llm.go}` + tests | `go test ./internal/domain -run 'Decision\|LLMCost\|Truncate'`, including a property test that cost is monotonic and cannot overflow (tokens capped) | todo |
| 5.3 | **Port and adapter trace.** `Reviewer.Review` returns `(Verdict, LLMCall, error)`, and the call is filled even on error (prompt, latency, error, raw response). Anthropic: `resp.Usage.InputTokens/OutputTokens`, `resp.Model` (the fallback may serve). OpenAI-compatible: `usage.prompt_tokens/completion_tokens`, `model`. Update callers (scanner `llmCheck`, `TestLLM`). | `internal/ports/ports.go`, `internal/adapters/llm/llm.go` (+tests), `internal/app/{scanner.go,llm.go}`, fakes | `go test ./internal/adapters/llm`. For every provider the recorded request body has no `tools`, `tool_choice` or `functions` key; usage is parsed; a timeout returns a trace with an error; invalid JSON is an error with the raw response kept. | todo |
| 5.4 | **Migration 00010.** `llm_calls` (id PK, account_id `''` = system, no FK; purpose, profile_id, provider, model, prompt TEXT, response TEXT, tokens, cost_micros, latency_ms, error, at). `decisions` (id PK, account_id FK, at, instrument_id, symbol, direction, playbook, signal fields, probs TEXT JSON, llm_call_id, verdict, size_bp, verdict_reason, risk_allowed, risk_size_cents, risk_reason, outcome, order_request_id). Both dialects; repositories with `List(account, before, outcome, limit)`, `Get`, `DeleteBefore`. | `migrations/{sqlite,mariadb}/00010_decision_log.sql`, `internal/adapters/sqlstore/decisions.go`, `storetest` | `make test && make test-int`, `TestMigrationParity` | todo |
| 5.5 | **App decision log.** The agent pipeline (4.10) writes one `Decision` per candidate. It is persisted **before** the order row; a write failure means no order (fail closed). Then it is updated with the request id and outcome. Every LLM call is logged (agent, scanner, test); for agent entries a failed log write = reject. `Decisions`, `Decision` (view + account access), `LLMUsage(days)` (admin). Purge per OD12. | `internal/app/{decisions.go,agent.go,scanner.go,llm.go}` + tests | `go test -race ./internal/app -run Decision`. Every exit path of 4.10 writes exactly one decision with the right outcome; the decision exists before `Open`; store failure → no `Open`; another account's decision → 404; scanner calls logged as `scanner`. | todo |
| 5.6 | **Contract and handlers.** `GET /api/v1/decisions?limit&before&outcome`, `GET /api/v1/decisions/{id}` (with the prompt and response), `GET /api/v1/llm/usage?days=` (admin), LLM profile schema with prices. | `api/openapi/cactustrade.yaml`, `internal/adapters/http/handlers_decisions.go` + tests | `make gen && go test ./internal/adapters/http -run 'Decision\|LLMUsage'`, contracts drift check green | todo |
| 5.7 | **Web.** *Decision log* page under Trading: time, symbol, direction, playbook, verdict, risk, outcome, order state; outcome filter. A row expands to show signal, regime probabilities, reasons, and the prompt and response folded, with tokens, cost and latency. LLM page: price fields per profile and a usage card. | `web/src/pages/{DecisionLog.tsx,LlmReviewer.tsx}` + tests, `Layout.tsx`, `api.ts` | `pnpm -C web check` | todo |
| 5.8 | **Docs and verification.** ARCHITECTURE and PLAN. Owner run: a demo agent with a configured reviewer produces decision rows end to end. | docs | `make check`; owner run blocked on the demo and LLM keys | todo |

## Phase 6 — live dashboard, history, exports — `todo`

- 6.1 SSE stream: equity, cash, budget, PnL, positions, orders/fills feed, regimes, agent
  state, distance to limits.
- 6.2 History: trades, daily PnL, equity curve, fees, win rate, drawdown; CSV export.

## Phase 7 — hardening and real-mode gate — `todo`

- 7.1 Real-mode gate: ≥ 20 demo trading days, admin, typed phrase, banner everywhere, mode in
  every log line and order record.
- 7.2 Vault `SecretStore` adapter; encrypted backups (`deploy.sh --backup-now` today writes a
  gzip'd `mariadb-dump`, 0600, in `BACKUP_DIR`; to be replaced by an age-encrypted app backup
  as in cactusInv) and `--restore`.
- 7.3 Signed releases (`make dist`, `setup.sh`/`setup.ps1` as cactusInv), runbook
  (`docs/RUNBOOK.md`: API down, kill switch tripped, reconciliation mismatch), manual demo
  end-to-end test with the owner's keys.
