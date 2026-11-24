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
| R21 | Saxo and Tiger Brokers accounts (view-only) | Owner's decision 2026-11-23: **"Saxo and Tiger, view-only".** Built like R17: platforms `saxo` and `tiger` (no migration: `accounts.platform` is VARCHAR(16)); `Tradable()` stays eToro only, so the one central check (`app.account`) refuses them on every trading, agent, scanner, watchlist and theme path; they appear on the *Overview* with equity snapshots and the chart. **IBKR and moomoo not added:** moomoo/Futu OpenAPI has no Australian market; IBKR's docs could not be read and its API needs a local gateway with a daily login. New adapters `internal/adapters/saxo` (allowlist: GET `/port/v1/balances/me`, `/port/v1/positions/me`; the only POST is the sign-in host's `/token`; facts `docs/saxo-api.md`) and `internal/adapters/tiger` (allowlist by method: `prime_assets`, `assets`, `positions`; one signed POST to the constant gateway; facts `docs/tiger-api.md`), each implementing the read-only `Holdings` port, with allowlist tests that fail on any other path/method literal (`/trade/`, `place_order`, …), 15 s timeouts, response caps, per-account limiters, and tests proving errors and logs carry no secret (coverage saxo 97.6 %, tiger 96.1 %). Decisions: R21-a credentials generalised as `domain.PlatformCredentials(p) []CredentialSpec{Key, Label, Help, Multiline, Optional, SetByFlow}` stored as `<prefix>.<id>.<key>`; eToro and Trading 212 names unchanged (`AccountKeyNames` = the first two); `PUT /accounts/{id}/keys` takes `values` by key, `apiKey`/`userKey` still set the first two (both at once = 400); `GET /keys` adds `complete`, `credentials` (set / not set, `updatedAt`, never values), `connectable`, `connectRedirectUri`; new `GET /api/v1/platforms`; R21-b Saxo sign-in: `GET /accounts/{id}/saxo/connect` (owner, web session) issues a random state kept **hashed in memory** (single-use, 10 min, bound to user and account; ≤ 1000 pending) — no table, so no migration; a restart only means connecting again (one replica); public `GET /api/v1/oauth/saxo/callback` redirects to `/accounts?connected=<id>` or `?connect_error=expired|refused|failed` (fixed words only), forged states rate-limited per address; redirect URI = `CACTUSTRADE_PUBLIC_URL` + callback path, shown on the card; new port `OAuthConnector`; R21-c Saxo tokens: the rotated refresh token is stored before the new access token is used (store failure = nothing used); refresh early (< 5 min access, < 20 min refresh left); refreshes serialised per account; a refused refresh token is remembered and not retried (`domain.ErrReconnect` → overview "Reconnect to Saxo", `reconnect: true`); the 24-hour developer token works on SIM only and is refused once stored ≥ 24 h ago (age from `SecretMeta.UpdatedAt`); R21-d unverified response fields are parsed as pointers: account totals without them fail closed, a position without an account-currency value is `Holding.Unknown` (shown "unknown", left out of Invested/P&L) — no FX, no guess; R21-e minor units per ISO currency: `domain.CurrencyDecimals` (JPY, KRW 0; default 2), mirrored in the web `money()`; R21-f Tiger: our own signer (SHA1withRSA PKCS#1 v1.5, sorted `k=v&…`, base64; PEM PKCS#8/PKCS#1 or bare base64 keys), constant gateway (the SDK's "domain garden" lookup is a credential-redirect risk, not implemented), the licence is stored but **not sent** (both SDKs use it only for that lookup), `language`/`device_id`/token not sent, response signature not verified; `prime_assets` falls back to `assets` on business codes 1010–2399 or without an `S` segment, never on rate-limit/permission codes; R21-g depguard: `saxo` and `tiger` may not import `etoro`, each other, `trading212`, `app` (and `tiger` not `github.com/tigerfintech`); the LLM adapter may not import either; R21-h the session cookie's Max-Age is now measured on the service clock (`app.Service.Now`), the clock that set the session's expiry — it was `time.Until` (wall clock), which made every session-based HTTP test fail after 10:00 UTC on 2026-11-23 (fixed test clock); production behaviour is unchanged. **Open (owner):** Tiger's protocol requires SHA-1 (`sign_type` RSA); gosec flags `crypto/sha1` (G401/G505) in `internal/adapters/tiger/sign.go`. A reviewed, path-scoped exclusion in `.golangci.yml` (like the existing reviewed ones) was proposed but not added: it needs the owner's consent. | done (2026-11-24; the owner gave written consent for a gosec G401/G505 exclusion limited to `internal/adapters/tiger/sign.go`, because Tiger signs requests with SHA1withRSA) |
## Phase 4 — risk engine, demo execution, reconciliation, kill switch — `done except 4.17 (owner's live demo run)`

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
| 4.7 | **Executor (app).** `placeEntry`: refuse unless eToro, demo, not killed and lease held. Persist the order (`created`); a store failure means nothing is sent. Re-check portfolio age ≤ `MaxPortfolioAge`. Mark `sent` (persisted) → `Open`. On a 429: lookup; if 404, resend the **same id** after Retry-After (≤ 3 sends). Outcome unknown → resolve by lookup, never resend (OD2). Poll `orders:lookup` at 1, 2, 4, 8, 15 and 30 s. Filled or partial → record `agent_positions` and verify each execution: real/long/×1 or cfd/short/×1, stop present and on the right side, close to the stop we sent. Otherwise close at once, block the instrument and alert. Rejected → release. Still pending at 60 s → cancel and poll. Lookup still 404 after 2 min → `abandoned` (never resent). `closePosition`: persist the close order → `Close` → poll `CloseOrder`. If the answer is lost, re-read the portfolio; if the position is still open, resend with the same id (≤ 5). `cancelOrder`. Events `order.*` (trade e-mails via R12). | `internal/app/executor.go`, `executor_test.go` | `go test -race ./internal/app -run Executor`. Table cases: row persisted **before** `Open` (SimBroker hook); 429 → two `Open` calls with one id; unknown outcome → one `Open`, resolved by lookup; 404 for 2 min → abandoned, no resend; rejected → reservation freed; pending → cancelled; leverage-2, CFD-long or no-stop fill → closed + blocked; real account → zero broker calls + `ErrRealModeGated`; persist failure → zero broker calls; lost close answer → resent with the same id; close works while killed. | done |
| 4.8 | **Reconciliation.** `Reconcile(account)`: fresh portfolio. Resolve every non-final order by referenceId: `created` + lookup found → adopt; 404 and older than 2 min → abandoned. Match `agent_positions` with eToro: ours gone → closed locally. Foreign positions handled per OD1. Sets the in-memory `reconciled` flag with time and reason; any error or mismatch → not reconciled, warning event + alert. `ReconcileAll` at start-up for every eToro account with keys (read-only for real accounts), then every 5 min from the supervisor. | `internal/app/reconcile.go`, `reconcile_test.go` | `go test -race ./internal/app -run Reconcile`. Clean account → reconciled. A crash between persist and send is adopted or abandoned correctly. Foreign non-conforming position → not reconciled + event. Broker error → not reconciled. `AgentTick` before `ReconcileAll` → SimBroker `Opens == 0`. | done |
| 4.9 | **Supervisor (exits, D21).** `Supervise(account)` every 10 s: lease → fresh portfolio (on failure, local agent positions) → `Observe` (risk day, high-water mark, latch, persisted) → `DrawdownBreached` → Kill. `ExitsDue` uses the calendar session of each instrument's own market → `closePosition`. Pending orders older than 60 s → cancel. Periodic reconcile. Never calls the LLM or needs prices. | `internal/app/supervisor.go`, `supervisor_test.go` | `go test -race ./internal/app -run Supervise`. An LSE, an NYSE and an ASX position (fake calendar incl. a half day) are closed at close−10 min, not at close−11 min. Exits still happen when killed, stopped, paused, unreconciled, when the portfolio read fails (local positions used), and when market data or the LLM fail. Drawdown → killed, all agent positions closed, no `Open`. Lease held by another process → no mutations. | done |
| 4.10 | **Agent loop (entries).** `AgentTick(account)` runs only when running, lease held, reconciled, not killed, demo, calendar present (D25). Steps, one entry at a time per account (mutex): (1) watchlist shares whose market is in session (LSE per OD9). (2) Candles: 5 min, 400, unclosed bar dropped (D26/U5). (3) `strategy` evaluate, sized by min(budget, equity); shorts only if `MaxShort > 0`. (4) Cost check (OD8). (5) LLM verdict when reviewing is on: any error/timeout/invalid answer = reject; `ApplyVerdict`. (6) Fresh portfolio + fresh quote (entry = ask for a long, bid for a short; stop still on the right side). (7) Reservations from `orders`. (8) `domain.AssessEntry`. (9) `placeEntry`. Each step's outcome is kept in a trace struct (persisted in phase 5). | `internal/app/agent.go`, `agent_test.go` | `go test -race ./internal/app -run AgentTick`. Approve → `Open` amount ≤ min(fresh cash − reserved, remaining budget). LLM "reduce 0.5" → halved and then capped. An LLM multiplier 3 or "increase" never enlarges. Each of LLM error/timeout, stale quote, stale portfolio, unreconciled, killed, paused, stopped, real, no calendar, budget 0, order in flight → no `Open`. Short: stop > entry; open + pending shorts ≤ 25 % of budget. Every intent: direction ∈ {long, short}, RequestID persisted. | done |
| 4.11 | **Agent control, kill switch, limits lock.** `StartAgent`: agent.control + trade access, web session only, eToro demo, keys, valid limits, budget > 0, reconciled, not killed. `PauseAgent` / `StopAgent`: entries stop, exits continue (D21). `Kill`: agent.kill + trade access, session or API key. It persists killed (`risk_state` + `agent.state`) **first**, cancels the entry pipeline's context, cancels pending orders, then closes every agent position through the executor, paced by the execution limiter. `ResetKill`: admin, web session, typed phrase `RESET <account name>`, high-water mark := current equity, state → stopped. A per-account mutex serialises Start / SetRiskLimits / Kill / Reset (limits only while stopped, no race). After a restart, agents that were running come back per OD5. All audited, with events. | `internal/app/{agent_control.go,risk.go}`, `agent_control_test.go` | `go test -race ./internal/app -run 'Agent\|Kill\|RiskLimits'`. Role × access matrix. Start refused for a real, killed or unreconciled account and for budget 0. SetRiskLimits while running, paused or killed → 409. 100× concurrent Start vs SetRiskLimits never leaves the agent running with limits written after the start. Kill with 15 positions + pending orders → all cancels and closes sent within one 60 s window of the fake limiter (≤ 20), no `Open` after Kill. API key: kill allowed, start and reset refused. Reset needs admin + phrase. | done |
| 4.12 | **Sequence property test (app).** 300 seeded scenarios × 200 ticks, `math/rand/v2` (no new dependency). Random prices, fills, rejections, lost answers, 429s, LLM verdicts (incl. garbage), limits, shorts, with supervisor and agent running. After every tick: only the two order shapes; id persisted before send; Σ(open + reserved) ≤ min(budget, equity at decision); no `Open` > fresh cash − reserved; shorts ≤ MaxShort of budget ≤ 25 %; long stop < entry < short stop; no `Open` after kill, drawdown or the daily latch; no agent position open after its market's close − flatten; open orders are resent only after a 429, with the same id. | `internal/app/agent_property_test.go` | `go test -race ./internal/app -run AgentSequenceProperties`; fails if fewer than 500 entries were exercised | done |
| 4.13 | **Contract and handlers.** Contract first in `cactustrade.yaml`, then `make gen`. `GET /api/v1/agent` (viewer): state, reconciled, kill reason, risk-day equity / high-water mark / floors, blocked instruments, next flatten per market. `POST /api/v1/agent/start` (operator, session), `/pause`, `/stop`, `/kill` (operator, session or key), `/reset` (admin, session, `{confirm}`), `/reconcile` (operator). `GET /api/v1/orders?limit=` (viewer). | `api/openapi/cactustrade.yaml`, `internal/adapters/http/{api.gen.go,handlers_agent.go,agent_test.go}`, `web/src/lib/schema.d.ts` | `make gen && go test ./internal/adapters/http -run Agent`. Status codes 200/403/409/422, CSRF on every mutation, API key refused for start and reset, contracts drift check green. | done |
| 4.14 | **Wiring.** Start-up order: migrate → `app.New` → env account / LLM migration → **`ReconcileAll` before any supervisor or agent loop** (scanner and equity loops are read-only) → `supervisorLoop` (10 s; one goroutine with a timeout per account, so one account's exits never wait for another's) → `agentLoop` (bar close + 5 s). Lease holder = random id per process. Graceful shutdown stops the loops and releases leases; positions handled per OD6. | `cmd/cactustrade/main.go`, `main_test.go` | `go test ./cmd/cactustrade` checks that an ordering hook sees `ReconcileAll` before the first `Supervise`; `make build` | done |
| 4.15 | **Web.** Dashboard *Agent* panel: state, reconciled, kill reason, distance to daily floor and drawdown; Start / Pause / Stop; red kill-switch button with a confirmation dialog; Reset (admin, typed phrase). Controls disabled with "real trading needs the phase-7 gate" on real accounts. Remove the "orders arrive in phase 4" note. New *Orders* page under Trading. Risk limits page explains paused and killed. | `web/src/components/AgentPanel.tsx`, `web/src/pages/{Dashboard.tsx,Orders.tsx,RiskLimits.tsx}`, `components/Layout.tsx`, `lib/api.ts`, tests | `pnpm -C web check`. Vitest: buttons by role, kill confirmation, real account disabled, limits form disabled unless stopped. | done |
| 4.16 | **Docs.** ARCHITECTURE: execution, supervisor, lease, D20–D26. `docs/etoro-api.md`: execution facts plus the unverified list U1–U11. README risk note. | docs | `make check` | done |
| 4.17 | **Live demo verification, run by the owner.** One minimal long and one short on an NYSE share in session. Record sanitised fixtures (open, lookup pending/filled, close, close-orders). Settle U1–U11. Kill switch with 2 positions. Restart mid-position → reconciliation adopts it. Never real. | `testdata/`, `docs/etoro-api.md` | Fixture tests replaced by recordings and green | blocked: the owner's live demo run (needs the demo keys) |

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

### Steps 4.7–4.9: decisions (2026-11-23)

- **4.7-a Poll schedule** (offsets from the latest send): lookups at 1, 2, 4, 8, 15, 30 s as
  the step says, then 60 s (still pending → cancel, with its own persisted request id), 62,
  66, 75 s (to see the cancel), 90, 105 and 120 s (still 404 → `abandoned`). Close orders are
  read at 1, 2, 4, 8, 15, 30 s. Waits go through `Deps.Sleep` (nil = a real timer), so tests
  move a fake clock and never sleep.
- **4.7-b Extra entry gate:** `placeEntry` also needs the in-memory reconciled flag (4.8), in
  addition to eToro + demo + not killed + lease. An unreadable kill switch refuses entries.
  This makes 4.8's "an entry before `ReconcileAll` never reaches eToro" hold in the executor
  itself, not only in `AssessEntry` (4.10).
- **4.7-c Portfolio age** is checked after the order row is stored and before it is marked
  sent; a stale figure makes the order `abandoned` ("not sent"). An invalid intent likewise.
- **4.7-d Fill check:** `OrderStatus.Conforms` (shape, leverage 1, stop on the losing side),
  plus: eToro's instrument must be ours, and each execution's stop within **1 % (100 bp)** of
  the stop we sent (a GBX/GBP mix-up, U4, is off by ×100). A failing fill is recorded as an
  agent position, closed at once (polled), its instrument blocked, and an
  `order.nonconforming` danger event raised. Only newly recorded executions are checked, so
  repeated lookups never close twice.
- **4.7-e Open outcomes:** any error that is neither "not accepted" nor "real mode gated" is
  treated as unknown (never resent, OD2). A 429 followed by a failing lookup → unknown. Three
  429s → `rejected` (eToro never took it). A partial fill that eToro reports final → our
  `cancelled` (positions kept); `rejected` after a partial fill → `cancelled`.
- **4.7-f Cancel orders** are `filled` ("cancel accepted") when eToro answers 200; the
  target's own state follows from its next lookup. 429 → same id again (≤ 3).
- **4.7-g Closes:** a non-final close for the same position is not sent twice: with an eToro
  order id it is read (`CloseOrder`); without one it counts as in flight for 2 min, then it is
  `abandoned` and a new close (new id) is sent. 429 → same id (≤ 5). Lost answer → wait 2 s
  and read the portfolio (up to 3 reads): gone → `filled`; open → same id again (≤ 5);
  unreadable → `unknown` (the supervisor tries again). eToro "position not found" + absent from
  the portfolio → the close is `rejected` and the position recorded closed.
- **4.7-h Lease:** renewed before every resend as well as before the first send; lost → the
  order stays `sent` and reconciliation resolves it. `Config.LeaseHolder` names the process
  (4.14 sets it); empty = a random id chosen on first use (not in `New`: an existing e-mail
  test swaps `Random` after `New` and depends on its sequence).
- **4.7-i Concurrent writers:** when the supervisor or reconciliation stores a newer version of
  an order first (compare-and-set conflict), the executor follows the stored version.
- **4.8-a In-flight orders:** an open order eToro does not know yet (404) is abandoned only
  2 min after its send — also in state `created` (as the step says). A younger one is "in
  flight" and does **not** make the account unreconciled: its cash stays reserved and its
  instrument blocked (D24). Lookup or `CloseOrder` read errors do.
- **4.8-b Two portfolio reads:** one before the orders are resolved, a fresh one after (fills
  adopted in between must be in it). An agent position missing from the portfolio is closed
  locally only when it was recorded at least 2 min before the read (U11, fill delay).
  A position recorded as closed that eToro still has is reopened locally, so the supervisor
  closes it.
- **4.8-c Close and cancel orders:** a close with an eToro id is read by id; without one,
  position gone → `filled`, still open and older than 2 min → `abandoned` (the supervisor
  sends a new close). A cancel whose target is final → `filled`; target still open and older
  than 2 min → `abandoned`.
- **4.8-d Results:** `Reconcile` returns the `Reconciliation{OK, At, Reason}` and, for a
  broker or store failure, an error as well; a mismatch is not OK without an error.
  `Reconciled(account)` answers "never reconciled" as not OK — 4.11's OD5 resume reads it.
  `reconcile.failed` (warning, e-mailed as an alert) only when the state or reason changes;
  `reconcile.ok` on recovery. `ReconcileAll` covers eToro accounts with both keys; real
  accounts are only read (the executor refuses them before any I/O).
- **4.9-a Portfolio read fails:** the new `domain.SessionExitsDue` applies only the session
  rule to the recorded agent positions; the stop and shape checks wait for the next
  successful read, so a transient read error never flattens a position that is in session.
  No `Observe` and no drawdown check without a fresh portfolio.
- **4.9-b Kill:** `trip` persists `risk_state.killed` (versioned, retried on conflict) and
  `agent.state = killed` first, then audits (`agent.kill`, actor system) and raises
  `agent.killed`; the same pass closes every agent position (new `domain.ExitKilled`) and
  cancels every pending open at once. Later passes keep closing while killed. 4.11's `Kill`
  reuses `trip`. **Owner note:** the supervisor observes every eToro demo account with keys,
  whatever its agent state, so a 10 % fall of the account's equity (e.g. the owner's own
  positions, or a withdrawal) trips the kill switch even if the agent never ran.
- **4.9-c Closes are sent without polling** in the supervisor (a kill with many positions is
  not held up by the first); the next pass reads each close order once. Open orders pending
  for over 60 s are looked up once and cancelled if still pending, at most one cancel per
  minute per order. The daily latch raises `risk.daily_stop` once per risk day.
- **4.9-d Markets** come from the instrument cache only (every agent instrument is on a
  watchlist, so cached): no market data call. An uncached instrument, an unsupported market,
  a calendar error or no calendar → no session → closed (fail closed).
- **4.9-e Loop:** `SuperviseLoop(ctx)` every 10 s supervises each eToro demo account with keys
  in its own goroutine (2 min timeout); an account whose previous pass still runs is skipped.
  Real accounts are skipped (nothing of ours can be there). Not wired into `cmd` yet (4.14).

### Steps 4.10–4.12: decisions (2026-11-24)

- **4.9-x (owner informed, stays: fail-safe) The supervisor watches the drawdown of every eToro
  demo account with keys, even when its agent never ran.** A fall of the account's equity —
  including positions cactusTrade did not open, or a withdrawal — can trip the kill switch. The
  kill only stops entries and closes cactusTrade's own positions and cancels its own pending
  orders (OD1). Every `agent.killed` event now ends with that sentence ("New entries are
  stopped. Only the positions cactusTrade opened are closed and its pending orders cancelled;
  positions opened outside cactusTrade are left alone. An admin must reset the kill switch."),
  and the drawdown reason says the equity covers every position, also those not opened by
  cactusTrade (`app.killEffect`, `supervisor.go`).
- **4.10-a Trace:** `AgentTick` returns an `AgentTrace` (skipped reason, entries, kill, errors)
  with one `InstrumentTrace` per watchlist share. Each step adds a `TraceCheck{Step, Pass,
  Detail}`; the steps are `session, candles, signal, costs, review, prices, reservations, risk,
  order`. Nothing is persisted yet (phase 5).
- **4.10-b Gate order:** view-only → real mode → no eToro client or market data → no calendar
  (D25) → agent not running → kill switch on (or unreadable) → not reconciled → lease. After the
  gate: no budget, empty watchlist, unreadable portfolio or no equity skip the tick. The agent
  state is read again between instruments and just before `placeEntry`: a pause, stop or kill
  ends the round. Several entries per tick are allowed, each with its own fresh portfolio and
  reservations.
- **4.10-c Session and markets:** the share's own market must be in session and before
  close − `NoEntryBeforeClose` (checked early to save quota; `AssessEntry` checks it again). LSE
  is refused by the new `domain.EntriesAllowed` (OD9); NYSE and ASX are allowed. A share with a
  position already open is skipped before its candles are read.
- **4.10-d Candles:** 400 five-minute bars. `domain.ClosedBars` drops every bar that has not
  closed at `now` (U5). `domain.BarsFresh` requires the newest closed bar to have ended at most
  two intervals ago; otherwise the share is skipped (halted share, stopped feed).
- **4.10-e Cost gate (OD8):** `domain.CostCheck` refuses when the costs are ≥ the gross gain at
  the take-profit. It also refuses when there is no take-profit, the take-profit is on the
  losing side, eToro returns no cost lines, or any line is not in USD. Negative lines (rebates)
  are ignored, costs are rounded up, and the arithmetic cannot overflow. The gate runs on the
  candidate's entry and size. If the risk engine shrinks the size, it runs once more with the
  final size and the fresh entry (one more costs call): fixed fees weigh more on a smaller
  order.
- **4.10-f Reviewer (OD7):** the active (default) profile is used. When reviewing is on, each
  of these is a reject: no reviewer, a broken profile, an error, a timeout, or an answer that
  arrives after the time limit. The verdict goes through `domain.ApplyVerdict`, so it can only
  shrink. The reviewer sees the size after the strategy, the open positions and today's P&L.
- **4.10-g Fresh prices:** the entry is the ask for a long and the bid for a short. A delayed
  quote, or one older than `MaxQuoteAge`, is refused. The stop must still be on the losing side
  of the fresh entry, and the take-profit on the winning side.
- **4.10-h Risk state:** the tick folds the fresh equity into the risk state itself (same
  versioned write as the supervisor), so `AssessEntry` always sees today's risk day. When the
  engine reports a drawdown, the agent trips the kill switch after it has released its entry
  lock (Kill holds the control lock and waits for that entry lock).
- **4.10-i Cancelled before send:** `sendOpen` checks the context before every `Open`. A
  cancelled entry (kill, shutdown) becomes `abandoned` ("cancelled before it was sent") and
  never reaches eToro.
- **4.10-j `AgentLoop`** (not wired; 4.14) wakes at `domain.NextDecision` (bar close + 5 s,
  OD3/D26). Each running demo account ticks in its own goroutine with a 4-minute timeout; an
  account whose tick still runs is skipped.
- **4.11-a Locks:** every account has two locks. The **control** lock is taken by Start, Pause,
  Stop, `SetRiskLimits`, Kill, Reset, the supervisor's trip and the OD5 resume. The **entry**
  lock is held by one agent tick at a time. They are always taken control first, then entry;
  a tick never waits for the control lock. Pause and Stop take the control lock too and are
  refused while killed, so a stop can never overwrite the killed state. `SetRiskLimits` reads
  eToro's equity while holding the control lock, so a Kill during that read waits up to the
  read's timeout.
- **4.11-b Pause/Stop** do not cancel an entry already being sent (it completes and is followed
  up); the next instrument or round sees the new state. Only Kill cancels the entry pipeline.
- **4.11-c Kill:** persist the kill (risk state, then agent state) → cancel the running tick →
  wait for the entry lock → settle and cancel our pending open orders → close every agent
  position. Closes and cancels are sent once each, without polling (the supervisor follows
  them up). A kill with 15 positions and 5 pending orders therefore sends exactly 20 requests
  within one 60 s window. In production the eToro client's execution limiter (4.6) paces them;
  the app test uses a counting broker that flags a 21st request within 60 s. Killing again
  flattens again but raises no new event. If persisting the kill fails, Kill still flattens and
  returns the error. A real account can be killed (stored only; nothing of ours can be there).
- **4.11-d Permissions (OD11):**
  - Start: `agent.control`, trade access, web session.
  - Pause and Stop: `agent.control`, trade access, web session or API key.
  - Kill: `agent.kill`, trade access, web session or API key.
  - Reset: `agent.kill`, trade access, role admin, web session, and the phrase
    `RESET <account name>` (exact and case-sensitive; spaces around it are trimmed).
- **4.11-e Reset (OD10):** needs a fresh equity read; without one nothing is reset. The
  high-water mark becomes that equity, and the kill reason and time are cleared. Start-of-day
  equity and the daily latch are not touched. The agent comes back stopped. Audited as
  `agent.kill_reset`, with a warning event.
- **4.11-f Start** also needs the eToro client and market data. It is a no-op when the agent is
  already running, and it may start a paused agent.
- **4.11-g Resume (OD5):** `ResumeAgents(ctx)` handles every account whose stored agent state is
  `running`. The agent resumes only when the account would pass Start's checks: demo, keys
  present, not killed, valid limits with a budget, and reconciled cleanly *in this process*.
  - Resumed: audit `agent.resume`, info event `agent.resumed`.
  - Not resumed: the agent is `paused` (`killed` when the kill switch is on), with audit
    `agent.resume_refused` and a warning event (an alert e-mail).
  4.14 must call it after `ReconcileAll` and before the agent loop.
- **4.12-a Sequence:** each tick runs the supervisor and then the agent one after the other, not
  in two goroutines, so a seed replays exactly. Concurrency is covered by 4.11's tests (Start
  against `SetRiskLimits` ×100, Kill during a running tick) and by `-race`. The 300 scenarios
  run as parallel subtests: about 4 s under `-race`. On the current code they make about
  2 000 entries and exercise every behaviour counted (shorts, 429 resends, manual kills,
  drawdown trips, resets, daily latches, stop-outs, unreadable portfolios, limit changes).
- **4.12-b Scope:** closes in the scenarios end in a fill, a lost answer, a 429 or a 5xx, with at
  most 3 failures in a row. A close stuck pending or refused is covered by the executor tests;
  in the scenarios it would keep a position open on purpose. The flatten and kill invariants are
  checked after every supervisor pass that could read the portfolio; a tick with an unreadable
  portfolio is checked from the next tick.
- **4.12-c Invariants added to the step's list:**
  - the entry equals the fresh ask or bid;
  - an order is never larger than its candidate, and keeps its direction and stop;
  - the open-position cap holds;
  - the stored order's send count equals the number of `Open` calls for it;
  - every position at eToro is recorded as ours or is the owner's own, and the owner's own
    position is never closed.
  The test was checked against three planted bugs: sizing from the requested amount, no
  supervisor flatten, and resending after a lost answer. Each made it fail.
- **4.12-d Simulator caveat:** `fakes.SimBroker` does not take a pending order's amount out of
  cash, but counts it in `PendingOpen` and therefore in equity. While an order is pending,
  equity and the high-water mark are inflated by its amount. This errs towards a kill, not
  towards spending more. Whether eToro's `credit` behaves the same way is U10 (4.17).

### Steps 4.13–4.16: decisions (2026-11-24)

- **4.13-a New use cases** (new file `internal/app/agent_view.go`; files under review untouched):
  `AgentStatusView` (viewer: one read-only portfolio read, 10 s timeout; without it the
  equity-based figures are left out with `equityError`), `Orders` (viewer, limit 1–500,
  default 100, symbol from the instrument cache), `ReconcileAccount` (agent.control + trade
  access, session or API key; refused with 409 while a supervisor pass holds the account, so
  the two never reconcile at once; a pass that finds a problem is 200 with `ok: false`) and
  `ReleaseLeases` (4.14). New pure helpers `domain.DrawdownFloor` (property test: agrees with
  `DrawdownBreached` on 20 000 random cases) and `domain.FlattenAt` (same 5-minute minimum as
  `ExitsDue`), in the new `internal/domain/floors.go`.
- **4.13-b `controls`** in `GET /api/v1/agent` are computed by the app with the same rules as
  the use cases (permission, trade access, web session for start and reset, admin for reset,
  `gated` for a real account): the UI only shows them (no authorization logic in React).
- **4.13-c Every agent endpoint takes an optional `accountId` query** (default: the caller's
  current account), so a script with an API key can kill a named account. Kill takes an
  optional `{reason}` body; reset needs `{confirm}`.
- **4.13-d Status codes:** the agent handlers map a `ValidationError` to **422** (well-formed
  but not actionable: no budget, invalid stored limits, wrong reset phrase) through their own
  `failAgent`; the rest of the API keeps 400 for validation (changing it globally would break
  existing clients and tests). `ErrRealModeGated` → 403 "Real trading needs the phase-7 gate";
  `ErrForbidden`/`ErrViewOnly` → 403; `ErrConflict` → 409; missing keys → 503 (Start returns
  `ErrCredentialsMissing` without `ErrUnavailable`; the handler maps it, `agent_control.go`
  is unchanged). `GET /orders?limit=0|501` is 400 like the other list endpoints. CSRF is the
  existing middleware (every cookie-authenticated non-GET), tested per endpoint.
- **4.13-e Enums:** the new `Order.kind/direction/state` are plain strings with the values in
  the description: as enums their values (`short`, `open`, `pending`…) would collide with
  existing generated constants and rename them.
- **4.14-a The trading start-up runs in its own goroutine after the listener is open**
  (`cmd/cactustrade/trading.go`, `startTrading`): the web server, and so the kill switch, is up
  while eToro is being reconciled. `ReconcileAll` is bounded by 2 min; accounts it did not
  reach stay unreconciled (no entries, and their agents come back paused by OD5). Order:
  `ReconcileAll` → `ResumeAgents` → `SuperviseLoop` → `AgentLoop`. If `ResumeAgents` itself
  fails (store error) the supervisor still starts but the agent loop does not until the next
  start (fail closed: exits work, no entries).
- **4.14-b Shutdown:** the HTTP server drains first (a kill in flight finishes), then the loops
  are cancelled and waited for, then `ReleaseLeases` (5 s) gives up this process's leases so a
  restarted process (UI restart, upgrade) takes over at once. Nothing is closed (OD6). A
  server error takes the same path.
- **4.14-c Lease holder** = host name (cut to fit) + `-` + a random UUID per process, ≤ 64
  characters (the column width).
- **4.14-d Tests:** an ordering hook (`trader` interface) proves reconcile → resume → loops →
  release, also when the reconciliation or the resume fails and when shutdown comes during
  the start-up; a run with the real `app.Service` on fakes shows the account reconciled before
  the first supervisor pass and the lease released at shutdown.
- **4.15-a Web:** `AgentPanel` on the Dashboard (state, reconciliation, kill reason, equity,
  distance to the daily floor and to the drawdown floor, open positions / orders, blocked
  shares, flatten time per market, lease warning). Controls are compact icon buttons in the
  first column with `title` and `aria-label` (as on the LLM page); the red kill switch and the
  admin reset open a React-drawn Bootstrap modal (never `window.confirm`), the reset only with
  the typed phrase. On a real account every control is disabled with "Real trading needs the
  phase-7 gate". The Dashboard's "orders arrive in phase 4" note is replaced by "cactusTrade
  closes only the ones it opened". New *Orders* page (a table) under Trading. The Risk limits
  page explains running / paused / stopped / killed in a foldable `InfoCard` and keeps the form
  disabled unless the agent is stopped.
- **4.16 Docs:** ARCHITECTURE "Trading runtime" (executor, reconciliation, supervisor, agent,
  locks, lease, start-up/shutdown, D20–D26); `docs/etoro-api.md` "How the app uses execution"
  and the U1–U11 table; README status and a risk note before starting the agent.

## Phase 5 — LLM reviewer and decision log — `in_progress`

### Phase 4 review fixes (2026-11-24) — `done`

An independent review of the phase-4 money path found 2 blockers, 4 majors and 5 minors. Each
fix has a test that failed before it (`internal/domain/review_test.go`,
`internal/app/review_test.go`, `internal/ports/fakes/simbroker_risks_test.go`). Only
domain/app/fakes code changed; no contract, migration, handler, web or dependency change.

| ID | Finding | Fix | Test |
|---|---|---|---|
| B1 | A filled open order stopped reserving cash at once, though eToro's portfolio may not show the fill yet (U11): a second entry in the same tick was sized from cash that had not paid for the first. | `domain.Reservation.WithUnseenFills`: a filled open order (or one cancelled after a partial fill) keeps its full amount reserved, its instrument in flight and a place under the position cap until a portfolio read from no earlier than the fill shows every position it opened, or `domain.FillVisibleAfter` (2 min, the reconcile window) has passed. The agent uses it at step 7 (`app.reservations`: open orders + the 200 latest orders + agent positions). Double counting is the accepted direction. | `TestUnseenFills`, `TestAgentTickReservesUnseenFills`; 4.12 now checks every open against the *actual* account (`SimBroker.Actual`) with a random `PortfolioLag` (a planted removal of the fix fails the budget, short and position-cap invariants). |
| B2 | When recording an agent position failed, the order still became `filled` (final): our position was treated as foreign for ever and never flattened. | `recordFills` returns `errFillsNotRecorded`; the order is not moved on and the next lookup (executor poll, supervisor, reconcile) retries. A lookup reporting filled/partial without `positionExecutions` counts as pending. Reconciliation adopts an unrecorded position (`domain.AdoptUnrecorded`, event `position.adopted`) when one of our open orders that is filled, cancelled or abandoned explains it: same instrument and direction, sent within 24 h, sent no later than the position opened, the order's stored position id when it has one, and the adopted positions add up to at most the order's amount + 1 %. Each position goes to the latest such order. This also covers an order abandoned after two minutes of 404s that eToro holds (U2). | `TestAdoptUnrecorded`, `TestExecutorRecordFailureKeepsTheOrderOpen`, `TestExecutorFilledWithoutExecutions`, `TestReconcileAdoptsUnrecordedFills` (the owner's own positions — opened before the send, or the other direction — are not adopted). |
| M1 | `consider` read the clock once; after a slow reviewer (> 1 s) `AssessEntry` refused every entry as stale. | The clock is read again right after the fresh portfolio and quote; that time is used for the session (looked up again), the observed risk state, the reservations and `RiskInput.Now`. | `TestAgentTickSlowReviewer` (+3 s review still enters), `TestAgentTickSlowReviewerStaleQuote` (a quote older than `MaxQuoteAge` after the slow call is refused). |
| M2 | A close that eToro ends partly filled and final (status 9/10) was recorded as fully closed. | `applyCloseStatus` re-reads the portfolio: position gone → `filled` and closed; still there (or unknown) → the close order becomes `cancelled` ("partly closed; the rest … is closed again", event `order.partial_close`) and the position stays ours and open. `closePosition` sends a new close at once when a close in flight turns out final without closing the position. | `TestExecutorPartialCloseKeepsThePositionOpen`, `TestSupervisePartialCloseClosesTheRest`. |
| M3 | After a 429 the open was resent; if eToro processed it anyway (U7) and does not dedupe the id (U1), the order doubles. | **Decision review-M3: open resend disabled until U1/U7 are verified (stricter than the OD2 default).** `MaxSends(open) = 1` (close 5, cancel 3 unchanged). After a 429 the executor waits Retry-After, then looks the order up: found → followed/adopted; 404 → the normal poll schedule (offsets already past are skipped, the 120 s one always runs) and `abandoned` after 2 min. The order stays `sent` and reserved until final (and B1 applies after a fill). | `TestOpenSendLimit`, `TestExecutorNeverResendsAnOpen` (lookup only after Retry-After, reservation held while in flight), executor table rows updated; 4.12 invariant "opens are never resent". |
| M4 | The simulator could not play eToro's unverified behaviours. | `fakes.SimBroker`: `LookupLag` (404 for a while after acceptance, U2), `PortfolioLag` (young fills missing from `Portfolio`, their cash still shown, U11; `Actual()` gives the real account), `SimFilledNoExecutions` (+`NoExecLookups`), `SimRateLimitedProcessed` (a 429 that was processed, U7), `SimPartialClose` (status 9/10, half closed), `DuplicateFills` (a repeated request id fills again silently, U1; still a violation). Counters: `Outcomes`, `LaggedLookups`, `HiddenFills`, `Duplicates`. | `TestSimBroker{LookupLag,PortfolioLag,FilledWithoutExecutions,RateLimitedButProcessed,PartialClose,DuplicateFills}`; 4.12 arms all of them and fails if any was never exercised (`propStats.unexercised`). |
| m1 | `allowance × entry / stop distance` could overflow int64. | `AssessEntry` uses the existing saturating `mulDiv` (`math/bits`). | `TestAssessEntryHugeValues` (5 000 random huge inputs checked with `math/big`), `TestMulDivSaturates`. |
| m2 | `SetRiskLimits` read an unreadable agent state as "stopped". | `limitsChangeable` reads the setting directly: only "never stored" counts as stopped; any other error refuses (`ErrUnavailable`); a latched kill refuses too. | `TestSetRiskLimitsUnreadableAgentState`. |
| m3 | If both kill writes failed, the agent could re-enter after the database recovered. | In-memory kill latch per account in `runtimeState`, set by `tripLocked` before it persists; `isKilled` (and so `agentGate`, `startable`, `placeEntry`, the supervisor) checks it; only `ResetKill` clears it. | `TestKillLatchWhenTheWritesFail`. |
| m4 | State writes after a Kill cancelled the context ran on a cancelled context, losing eToro's answer. | `sendOpen` stores every outcome after the request left with `context.WithoutCancel(ctx)`. | `TestSendOpenStoresTheAnswerAfterACancel` (pending and lost-answer cases, with a store that fails on a cancelled context like a driver). |
| m5 | `SetRiskLimits` read eToro's portfolio while holding the control lock, delaying a Kill. | Equity is read before the lock; the state is checked again inside it (a Kill in between makes the change fail with 409). PLAN 4.11-a's note that a Kill may wait for that read no longer applies. | `TestSetRiskLimitsDoesNotDelayAKill`. |

Decisions made while fixing (recorded here):

- **review-a Absence proves a close only for a position old enough** (found by the M4 simulator,
  seed 240): after a lost close answer the executor re-reads the portfolio, and a fill younger
  than `FillVisibleAfter` may simply not show yet (U11). `positionOpen` now reports "unknown"
  for a missing position recorded less than 2 min before the read, and reconciliation's
  `settleClose` treats such a close as still in flight. The close is then resolved by the
  existing path (unknown → abandoned after 2 min → a new close). Cost: a lost close answer on a
  position younger than 2 min is resolved up to 2 min later. Test:
  `TestExecutorLostCloseAnswerWhileTheFillIsHidden`, plus a young-position row in
  `TestExecutorCloseEdges`; two older tests now age their positions past the window first.
- **review-b A pending open is cancelled only when eToro itself reports it pending or partly
  filled** (never when it reports a fill whose executions are not listed yet).
- **review-c An accepted (pending) order whose lookups answer 404 is not abandoned** (pending →
  abandoned is illegal, unchanged): it stays reserved and the supervisor follows it. The U2
  adoption case is therefore an order whose answer was lost (unknown → abandoned).
- **review-d Adoption is not part of the 4.12 scenarios**: their lookup lag stays below
  `lookupGiveUp` (1–20 s), because an adopted position would legitimately be "not recorded as
  ours" until the next reconciliation. Adoption is covered by the unit tests above.
- **review-e 4.12 runs extra supervisor passes** (10 s apart, at most 5) when a pass met a
  partial close, as the 10-second supervisor loop does in production, before the flatten
  invariant is checked. The 4.12 list of invariants now reads "opens are never resent"
  instead of "resent only after a 429, with the same id".

Verification: `make check` green in the worktree (gofumpt, golangci-lint 0 issues, vet,
`go test -race` with coverage domain 100.0 % / app 87.7 %, MariaDB `test-int`, govulncheck, contract drift,
`pnpm -C web check`).

## Phase 5 — LLM reviewer and decision log — `todo`

| ID | Step | Files | Acceptance | Status |
|---|---|---|---|---|
| 5.1 | Reviewer port and providers: no tools, strict JSON, timeout = reject, invalid JSON = reject, multiplier clamped (R6, R15, R16; bp conversion in 4.2) | `internal/adapters/llm`, `internal/domain/llm.go` | `TestParseVerdict`, `TestOpenAIEmptyAndTimeout`, `TestAnthropic`, `TestClampVerdict` | done |
| 5.2 | **Domain.** `LLMCall` (id, account, purpose agent/scanner/test, profile, provider, model served, prompt, response, input/output tokens, latency, cost micros, −1 = unknown, error, at). `LLMProfile` prices: USD per million input and output tokens as `Micros`, 0 = unknown; `Cost(in, out)` in integers. `Decision`: signal (playbook, direction, entry, stop, take-profit, suggested size, regime probabilities, reason), verdict (kind, bp, reason, call id), risk (allowed, size, reason), outcome (`rejected_reviewer`, `rejected_cost`, `rejected_risk`, `ordered`, `order_failed`), order request id. `Truncate` at 32 KiB, UTF-8 safe. | `internal/domain/{decisions.go,llm.go}` + tests | `go test ./internal/domain -run 'Decision\|LLMCost\|Truncate'`, including a property test that cost is monotonic and cannot overflow (tokens capped) | done |
| 5.3 | **Port and adapter trace.** `Reviewer.Review` returns `(Verdict, LLMCall, error)`, and the call is filled even on error (prompt, latency, error, raw response). Anthropic: `resp.Usage.InputTokens/OutputTokens`, `resp.Model` (the fallback may serve). OpenAI-compatible: `usage.prompt_tokens/completion_tokens`, `model`. Update callers (scanner `llmCheck`, `TestLLM`). | `internal/ports/ports.go`, `internal/adapters/llm/llm.go` (+tests), `internal/app/{scanner.go,llm.go}`, fakes | `go test ./internal/adapters/llm`. For every provider the recorded request body has no `tools`, `tool_choice` or `functions` key; usage is parsed; a timeout returns a trace with an error; invalid JSON is an error with the raw response kept. | done |
| 5.4 | **Migration 00010.** `llm_calls` (id PK, account_id `''` = system, no FK; purpose, profile_id, provider, model, prompt TEXT, response TEXT, tokens, cost_micros, latency_ms, error, at). `decisions` (id PK, account_id FK, at, instrument_id, symbol, direction, playbook, signal fields, probs TEXT JSON, llm_call_id, verdict, size_bp, verdict_reason, risk_allowed, risk_size_cents, risk_reason, outcome, order_request_id). Both dialects; repositories with `List(account, before, outcome, limit)`, `Get`, `DeleteBefore`. | `migrations/{sqlite,mariadb}/00010_decision_log.sql`, `internal/adapters/sqlstore/decisions.go`, `storetest` | `make test && make test-int`, `TestMigrationParity` | done |
| 5.5 | **App decision log.** The agent pipeline (4.10) writes one `Decision` per candidate. It is persisted **before** the order row; a write failure means no order (fail closed). Then it is updated with the request id and outcome. Every LLM call is logged (agent, scanner, test); for agent entries a failed log write = reject. `Decisions`, `Decision` (view + account access), `LLMUsage(days)` (admin). Purge per OD12. | `internal/app/{decisions.go,agent.go,scanner.go,llm.go}` + tests | `go test -race ./internal/app -run Decision`. Every exit path of 4.10 writes exactly one decision with the right outcome; the decision exists before `Open`; store failure → no `Open`; another account's decision → 404; scanner calls logged as `scanner`. | todo |
| 5.6 | **Contract and handlers.** `GET /api/v1/decisions?limit&before&outcome`, `GET /api/v1/decisions/{id}` (with the prompt and response), `GET /api/v1/llm/usage?days=` (admin), LLM profile schema with prices. | `api/openapi/cactustrade.yaml`, `internal/adapters/http/handlers_decisions.go` + tests | `make gen && go test ./internal/adapters/http -run 'Decision\|LLMUsage'`, contracts drift check green | todo |
| 5.7 | **Web.** *Decision log* page under Trading: time, symbol, direction, playbook, verdict, risk, outcome, order state; outcome filter. A row expands to show signal, regime probabilities, reasons, and the prompt and response folded, with tokens, cost and latency. LLM page: price fields per profile and a usage card. | `web/src/pages/{DecisionLog.tsx,LlmReviewer.tsx}` + tests, `Layout.tsx`, `api.ts` | `pnpm -C web check` | todo |
| 5.8 | **Docs and verification.** ARCHITECTURE and PLAN. Owner run: a demo agent with a configured reviewer produces decision rows end to end. | docs | `make check`; owner run blocked on the demo and LLM keys | todo |

### Steps 5.2–5.4: decisions (2026-11-24)

- **5.2-a Prices** are `LLMProfile.InputPrice` / `OutputPrice` (USD micros per million tokens,
  0 = unknown, at most USD 1 000 000), stored in the profile JSON (`inputPriceMicros`,
  `outputPriceMicros`; older profiles read as unknown). `Cost(in, out)` is `CostUnknown` (−1)
  when **either** price is unknown (OD13), else the exact sum in 128-bit integers, rounded **up**
  (a call is never shown cheaper), token counts clamped to [0, 2^40], result capped at
  `MaxInt64`. Property test against `math/big`: exact, never negative, never falls.
- **5.2-b `Decision`** gained an outcome **`pending`**: 5.5 writes the decision *before* the
  order row and only then knows the request id and the outcome, so the first write needs a
  state that is none of the five final ones. `Validate`: known outcome and direction, verdict
  kind ∈ {none, approve, reduce, reject}, 0 ≤ bp ≤ 10 000, sizes ≥ 0, and `ordered` needs a
  request id and the risk engine's approval. `Clip` cuts reasons to 500 bytes, symbol to 32.
- **5.2-c `Truncate(s, n)`** first replaces invalid UTF-8 with U+FFFD (MariaDB's utf8mb4 would
  refuse it), never splits a character, and ends a cut string with "…" inside the n bytes.
- **5.3-a The trace's prompt is the request body exactly as sent** (JSON: model, system and user
  message, output settings) and the response the raw response body. For Anthropic both are
  captured by a per-call SDK middleware (last attempt; headers, which carry the key, are never
  read); before anything is sent the prompt is the marshalled params. This makes "no `tools`,
  `tool_choice` or `functions`" checkable on the stored record itself; the test checks it for
  all nine providers and that the stored prompt equals the body the server received.
- **5.3-b The adapter fills the provider side** (provider, model served, prompt, response,
  tokens, latency, error); the caller fills id, account, purpose, profile, cost and time. The
  key is redacted (`[redacted]`) from the error, prompt, response and model in case a provider
  echoes it; keys under 8 bytes are not real keys and are left alone (else a test key "k"
  would garble every message).
- **5.3-c Transitional adapter for the agent tick** (agent.go is being changed elsewhere):
  `Deps.LLM ports.Reviewer` is the traced reviewer (scanner, admin test, model list);
  `Deps.Reviewer app.VerdictReviewer` keeps the two-value shape the tick calls and is wired
  as `app.VerdictOnly(LLM)` in `cmd/cactustrade`. Step 5.5 moves the tick to `Deps.LLM` and
  removes `VerdictReviewer`/`VerdictOnly`. The scanner and the test drop the trace until 5.5
  logs it; `LLMTestResult` now also returns the served model and token counts.
- **5.4-a Migration 00010:** prompt/response are `TEXT` (SQLite) / `MEDIUMTEXT` (MariaDB; 32 KiB
  fits in `TEXT` too, `MEDIUMTEXT` leaves headroom for a multi-byte cut). `llm_calls` has no
  foreign key (a call outlives its account, for the usage figures); `decisions` cascade with
  the account. Latency is stored in whole milliseconds. Regime probabilities are a JSON object;
  non-finite values are dropped (JSON has no NaN, and the log must never block a decision).
- **5.4-b Repositories:** `LLMCalls{Add, Get, DeleteBefore}`; `Decisions{Add, Update, Get,
  List, DeleteBefore}`. `Get` and `Update` take the account id, so another account's decision
  is `ErrNotFound` in SQL, not only in the app. `Update` changes only verdict, risk, outcome
  and request id (the signal is immutable). `List` is newest first with ties broken by id
  descending; `before` is exclusive. The usage aggregation for `LLMUsage(days)` is left to 5.5.
  The fake store has the same repositories plus `DecisionsErr` / `LLMCallsErr` for 5.5's
  fail-closed tests.

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
