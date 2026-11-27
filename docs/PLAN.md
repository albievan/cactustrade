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
| D27 | **eToro keys are set per account only** (PLAN 7.1.3, 2026-11-24): the global `PUT/DELETE /api/v1/credentials/{name}` and `app.SetCredential`/`DeleteCredential` are removed. Reasons: no web page used them (the Credentials list was API-only), every key is per account now, and the global names were the environment account's keys (written there without any account ownership) or real keys no account used. `GET /api/v1/credentials` stays as a read-only status of the deployment's credentials; the environment account's pair comes from `ETORO_DEMO_PUB`/`ETORO_DEMO_PK`, or its owner sets it under Platform accounts. | app/credentials.go, contract |
| D28 | **Sign-in limiter per (user, IP) with a per-user slow-down** (PLAN 7.1.5): hard limits only per IP (20) and per (user, IP) (5) per 15 min; across IPs a user's attempts add a delay (1/2/4/8 s after 5) instead of a global lock. Attempts are counted before hashing and refunded when right (or when the server was busy). Reset requests, reset confirms and OAuth callbacks have their own buckets. ≤ 10 000 keys (sweep, then drop the longest idle), swept by `PurgeLoop`. | adapters/http/limiter.go |
| D29 | **At most 3 password hashes at once** (`argon2.Limited`, 2 s wait → `domain.ErrBusy` → 429 + `Retry-After: 1`), wired around the argon2id hasher in `cmd`; `GOMEMLIMIT=400MiB` with a 512 MiB container / pod limit (PLAN 7.1.4). | adapters/argon2, cmd, deploy |
| D30 | **Quiescent account** (PLAN 7.1.2, 7.1.3): deleting an account or changing an eToro account's keys needs the agent stopped (killed → `MissingKillReset`), 0 open agent positions and 0 non-final orders, checked under the account's `ctl` lock; read errors refuse. New keys leave the account unreconciled ("keys changed") without an alert. View-only platforms have no agent and are not checked for key changes. | app/accounts.go |
| D31 | **Every log line about an account names it and its mode** (PLAN 7.2, 2026-11-24): `ports.AccountLogAttrs(ctx)` gives `account_id` and `mode` from `ports.WithAccount`, else from a per-request note (`ports.WithAccountNote`, set by the HTTP adapter; `ports.NoteAccount` from `app.anyAccount`, the first account resolved wins, and a note never acts as `WithAccount` for an adapter). `adapters/logctx.Handler` wraps the JSON handler in `cmd` and adds both to every record logged with a context (an attribute the call set itself is kept, not repeated). The supervisor, agent, executor, reconciliation, scanner and equity loops log with the account's context; every order transition is logged at Info with the order's own `account_id` and `mode`. A source test (`logctx_test.go`) fails on any `Debug/Info/Warn/Error` log call without a context in `internal/app` and `internal/adapters/etoro`. Only ids and the mode are logged. | adapters/logctx, ports/account.go |
| D32 | **API rate limits** (PLAN 7.3, OD33 numbers, 2026-11-24): GCRA token buckets (one time per key) per class — reads 600/min, other methods 60/min, `GET /history/export/{file}` 10/min, `GET /live` 30/min — for the address **and** the principal (user id, or API key id): a request takes a token from both or from neither. Applied per route inside the mux (the route pattern is known, authentication has run) and to the `/api/` fallback and `/api/openapi.yaml`; the web app, `/healthz`, `/readyz` and `/metrics` are not limited. **Never throttled, by route pattern:** `POST /agent/kill`, `/agent/pause`, `/agent/stop` and (7.14's) `POST /accounts/{accountId}/real-gate/disarm`. 429 problem+json with `Retry-After` (whole seconds, rounded up); metric `cactustrade_http_rate_limited_total{class}`. ≤ 10 000 keys (sweep refilled keys, then drop the fullest), swept by `PurgeLoop`. The sign-in limiter (D28) and the hash bound (D29) are unchanged and still apply on top. `Config.RateLimits` exists for tests only (zero = OD33). | adapters/http/ratelimit.go |
| D33 | **Headers, cookie and bodies** (PLAN 7.4, 2026-11-24): `Cross-Origin-Resource-Policy: same-origin` added; COOP `same-origin` was already set; `Permissions-Policy` turns off 14 features; `'unsafe-inline'` only in the Swagger page's `style-src` (never `script-src`), checked per route class. `SecurityHeaders` is exported and also wraps `cmd`'s HTTP→HTTPS 308 redirect. **Session cookie `__Host-cactustrade_session`** (OD32 default) whenever cookies are Secure; with `CACTUSTRADE_COOKIE_SECURE=false` it stays `cactustrade_session` (browsers refuse `__Host-` without Secure). **This signs everyone out once**: the old cookie is no longer accepted and is cleared when seen (README). Bodies: every request body is capped at 1 MiB (413 at once by `Content-Length`, else `MaxBytesReader`); the JSON decoder keeps its stricter 64 KiB cap (no endpoint needs more), now answered 413 instead of 400. | adapters/http/{middleware,problem,server}.go, cmd/cactustrade/demux.go |
| D34 | **Trading health and metrics** (PLAN 7.5, 2026-11-24; may be renumbered at merge): `app.TradingHealth(ctx)` is a plain snapshot (no Prometheus in the app, depguard) read by a `prometheus.Collector` in the HTTP adapter at scrape time; it is not a use case for a principal (the metrics token or `SystemHealth`, admin = `audit.view`, guard it). Per eToro account: mode, supervised (same rule as the loop, now one helper `supervisable`), agent state, killed (a read error = killed), reconciled, last pass (the pass ran: lease held, no store error; broker errors allowed) and last clean pass, lease held by this process, non-final orders by state, real gate armed (always false until 7.13); loop tick, agent loop, LLM calls by purpose and outcome (counted in `logCall`, also when the log write fails). **Unhealthy** = the supervisor loop has not ticked for 60 s (`SupervisorStale`), or a supervised account has had no completed pass for 60 s (without one: since it was first wanted, else since the start), or the agent loop is down; a skipped pass (lease held elsewhere) does not count, and its reason is shown. `GET /api/v1/system/health` answers 503 with the same body naming the account (names only for accounts the caller can see); `/healthz` is unchanged (an orchestrator must not restart the process for one stalled account). Counters in `httpapi.Observers`, wired by `cmd`: `etoro.Config.Observe` (every request that left: op, status class, mode) and `ObserveWait` (limiter waits per quota group) — plain hooks, so the eToro adapter does not import Prometheus either; order transitions counted **from the live bus** (subscribed before trading starts, buffer 4096; a view only, so the order path is untouched — a drop shows in `cactustrade_live_bus_dropped_total`). Labels: account ids or fixed sets only (an unknown mode reads demo, an invalid order kind/state is not counted). | adapters/http/metrics.go, app/health.go, docs/RUNBOOK.md |
| D35 | **`/metrics` always needs a token** (PLAN 7.1.14, F14, 2026-11-24; may be renumbered at merge): when `CACTUSTRADE_METRICS_TOKEN[_FILE]` / `SECRETS_DIR/metrics_token` is unset, `serve` generates 32 random bytes (hex) into `<data>/metrics.token` (0600, O_EXCL; an empty file is an error) in every mode, and logs the file's path, never the token. The HTTP adapter answers 401 to every `/metrics` request when no token is configured (fail closed), so a test or embedding that forgets the token cannot open it. | cmd/cactustrade/config.go, adapters/http/server.go |
| D36 | **The demo days needed for real trading are the account admin's to set** (owner, 2026-11-27, in writing: "allow the admin of the account to set how long the demo needs to run before you can start trading with real money, currently it's 20"). A change to a CLAUDE.md §2 rule, made with the owner's written consent: OD20's `MinDays` is per real account (`realgate.min_days.<account>`), default 20, set by an admin who owns the account in a web session (the arming user, OD22), **5–60** (`domain.MinEvidenceDaysFloor`: the demo period can never be switched off; at most the 60-day window). What counts as a day (OD19), the window and the 7-day freshness are unchanged. Every change is audited (`realgate.demo_days_changed old→new`), announced as a warning, and disarms an armed gate (`DisarmDays`). R41. | `internal/domain/realgate.go`, `internal/app/realgate.go`, contract `PUT /accounts/{id}/real-gate/demo-days`, `web/src/pages/RealTrading.tsx` |
| D37 | **Demo days for real trading: 1–60, default 5** (owner, 2026-11-27, in writing: "i'm happy with a minimum of 1 day demo trading, so make the qualifying demo days between 1-60 days default 5 days"). Amends D36 with the owner's written consent to a CLAUDE.md §2 rule: `domain.MinEvidenceDaysFloor` 5 → 1 (the demo period still cannot be switched off), `DefaultEvidenceRule.MinDays` 20 → 5. A stored per-account number is kept. What counts as a day (OD19), the 60-day window and the 7-day freshness are unchanged. The status carries `defaultMinDays`. R42. | `internal/domain/realgate.go`, contract `RealGateStatus.defaultMinDays`, `web/src/pages/RealTrading.tsx`, `CLAUDE.md` §2 |
| D38 | **Read the user's eToro watchlists for an import** (owner, 2026-11-27, in writing: "allow to import watchlists from the trading platforms"). A change to CLAUDE.md §2.1, made with the owner's written consent: exactly one watchlist path, `GET /api/v1/watchlists` (own lists, `ensureBuiltinWatchlists=false`, up to 1000 items each), quota group `watchlists` (60/min); every other watchlist path (create, rename, delete, items, public lists, the single-list GET) stays forbidden, pinned by `TestWatchlistReadOnly`. Trading 212, Saxo and Tiger have no watchlists in their APIs (researched 2026-11-27). R46. | `internal/adapters/etoro/allowlist.go`, `watchlists.go`, `CLAUDE.md` §2.1 |
| D39 | **The owner's written consent to real-money trading** (owner, 2026-11-27, in writing: "I give written consent to enable real-money trading on Real Money Maker. I accept the OD19–OD28 defaults and the real-money limit ceilings as they are in the plan. I accept the open 4.17 items. I understand losses are real."). Completes 7.18 for the account **Real Money Maker** only: OD19–OD28 accepted as written (OD25 real defaults: budget 0, daily loss 100 bp / ceiling 200, drawdown 500 / 1000, max position 1000 / 2000, max open 4 / 8, shorts 0 % / ceiling 25 %; OD26 never auto-resume; OD28 second key `CACTUSTRADE_REAL_TRADING=on`, off by default); the open 4.17 items accepted. RU1 settled (spec v1.427.0, 2026-11-27: every allowlisted real path present, v2 orders not deprecated); RU2–RU10 are read where the API allows (`etoro-probe --real-read`, read-only reader) and the rest settle at the owner's first real run (7.24). Claude never places a real-money order. | PLAN 7.18–7.24 |

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
| `codeberg.org/go-pdf/fpdf` v0.12.0 | MIT | Trading statements as PDF (R30). The standard library has no PDF writer; fpdf is pure Go, its root package imports the standard library only, and it is the maintained continuation of `github.com/go-pdf/fpdf` (archived; checked with `go list -m -json` and the repository, 2026-11-24). |
| `filippo.io/age` v1.3.2 (+ `filippo.io/hpke` v0.4.0, indirect) | BSD-3-Clause (both) | Backup encryption (PLAN 7.6, OD29): the age format to public-key recipients, so the machine that writes a backup cannot read it. The standard library has the primitives but no file format; age is the reviewed, interoperable one (the `age` CLI decrypts our files). |

All licences are checked by `make audit` (PLAN 7.7, `tools/licencecheck`): 81 Go modules
compiled into cactusTrade or its tests (MIT, BSD-2/3, ISC, Apache-2.0, MPL-2.0, public domain;
2026-11-24), and the web app's production packages. Web (production): `react`, `react-dom`
18.3 (MIT), `react-router-dom` **7.18.6** (MIT; upgraded from 6.30 in 7.7 because `pnpm audit`
reported GHSA-wrjc-x8rr-h8h6 and GHSA-337j-9hxr-rhxg, fixed only in 7.18), `bootstrap` 5.3.8 and
`bootstrap-icons` (MIT), `swagger-ui-dist` 5.29.0 (Apache-2.0; its `@scarf/scarf` install
telemetry is switched off with `scarfSettings.enabled = false`).

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

## Phase 2 — eToro adapter (read-only) and market calendar — `done`

| Step | What | Status |
|---|---|---|
| 2.1 | HTTP client (`internal/adapters/etoro/client.go`): keys from `SecretStore` per mode, new `x-request-id` per attempt, sliding-window limiter per quota group (`limiter.go`), 429 `Retry-After` pauses the group, jittered exponential backoff for reads, 15 s per attempt, 8 MiB response cap, errors never contain keys. **Mutating operations are refused before any I/O** (`ErrNotEnabled`) until phase 4. | done |
| 2.2 | Read operations: search (v2 free text), instruments, rates, candles, eligibility, costs (body built only from the guardrail constants), portfolio/PnL, orders:lookup, trade history. Spec-shaped synthetic fixtures in `testdata/` + `httptest` (92 % coverage). | done |
| 2.3 | Instrument cache + watchlist (migration 00003, both dialects, shared repository suite). Adding asks eToro: share on LSE/NYSE/NASDAQ and eligible as a real share, long, x1 — or refused with the reason. UI page *Watchlist* (search, add, remove, quotes every 15 s). | done |
| 2.4 | WebSocket price feed (`stream.go`): one connection, Authenticate with the mode's keys, `Subscribe {topics: instrument:<id>, snapshot: true}` on demand, rates parsed from string-or-number fields, binary heartbeats skipped, ping every 30 s, reconnect with backoff (and on a mode change), waits quietly while keys are missing. `Feed` serves stream quotes received ≤ 30 s ago and fetches only the others over REST. Message formats verified against api-portal.etoro.com/core/websocket (2026-11-23). | done |
| 2.5 | Test connection (`POST /api/v1/broker/test`, admin; one read-only PnL call) on the Credentials page; dashboard shows cash, equity, positions and market sessions. | done |
| 2.6 | `MarketCalendar` (`internal/adapters/calendar`): LSE 08:00–16:30 London, NYSE/NASDAQ 09:30–16:00 New York, DST from the embedded tz database, 2026–2027 holidays and half days verified against nyse.com, nasdaq.com, londonstockexchange.com and gov.uk (2026-11-23). Unknown year = closed + start-up warning. | done |
| 2.7 | Answer Q1 (LSE quote units, real settlement availability, `isPotential`) with the owner's demo keys; replace the synthetic fixtures with sanitised recordings. | done (2026-11-24: settled with `etoro-probe` on the env-demo account; see "Step 2.7 findings") |

### Step 2.7 findings (2026-11-24, `etoro-probe` with the owner's demo keys, read only)

Recorded, sanitised answers: `internal/adapters/etoro/testdata/recorded/probe-2026-11-24/` (BARC.L, AAPL, CBA.ASX: search, instrument, rates, 1-minute and 1-day candles, eligibility, costs of a $100 long and short).

- **Q1 LSE units: pence (GBX).** BARC.L candles and the close are ~401 (Barclays trades at ~£4.01), and eToro lists its LSE overnight fee in GBX. Rates, candles and stop-loss rates for LSE shares are all in pence; cactusTrade sizes by the stop distance as a fraction of the price and sends stops in eToro's own rate units, so the units are consistent end to end. OD9 (no LSE entries until checked) is now **checked**: re-enabling LSE entries is the owner's call (`domain.EntriesAllowed`).
- **Real settlement and isPotential:** BARC.L, AAPL and CBA.ASX each offer `real`/long/[1] and `cfd`/short with leverage 1 among [1,2,5,10,20]; `isPotential` is false on every config; minimum position $10. A CFD long (leverage 2-20) is also offered and never used.
- **Costs:** LSE buys carry SDRT (UK stamp duty, $0.50 on $100); shorts a $0.15 transaction fee; spreads 2-7 cents per $100; overnight fees in the instrument's currency (GBX/USD/AUD) are 0 for a real long.
- **Bug found and fixed: rates.** The live rates answer holds the fields flat in each result (`{"results":[{"ask":354.0,"bid":353.76,"date":"2026-11-24T22:05:09.84",...}]}`), not nested under `rate` as the synthetic fixture had it, and the timestamp has no zone (UTC). Every REST quote was therefore dropped as unusable (fail closed: stale = no entry; the WebSocket stream still served quotes, which is why the demo orders of 2026-11-24 were placed). `Quotes` now reads both shapes and zone-less timestamps (`etoroTime`); `TestQuotesRecorded` uses the three recorded answers.

## Phase 3 — regime model, playbooks, backtester — `done`

All in `internal/strategy`: pure, deterministic functions of closed bars (depguard: no I/O,
no ports, no adapters; floats allowed only here and at the eToro JSON edge).

| Step | What | Status |
|---|---|---|
| 3.1 | Features: log return, realised volatility (RMS of the last 20 returns), volume z-score. Feature t depends only on bars ≤ t (test). | done |
| 3.2 | Gaussian HMM (2–3 states, diagonal), Baum–Welch with deterministic volatility-quantile initialisation and variance floors; inference is the scaled forward filter only. Labels: highest-volatility state = high_volatility, then the higher mean-return/vol state = trending, else choppy. Property test: appending future observations never changes past filtered probabilities (25 random splits). | done |
| 3.3 | Playbooks (separate types): `TrendBreakout` (P(trending) ≥ 0.6, up-trend state, close above the 20-bar high; stop 3σ (≥ 0.2 %), target 2R), `MeanReversion` (P(choppy) ≥ 0.6, close ≥ 2 sd below the 20-bar mean; target the mean), no entries at P(high_volatility) ≥ 0.4. Size risks 0.5 % of equity at the stop, capped at 20 % of equity (the risk engine caps further in phase 4). `Engine` refits every 30 bars on the last 390 observations and filters in between. | done |
| 3.4 | `Backtest`: walk-forward with the same `Engine`; next-bar-open fills; exits only at prices the bar traded (stop at min(open, stop), target at max(open, target), stop first when both are in one bar); never across a session; costs from eToro's costs endpoint (spread half in/half out, fees and tax on the buy); amount ≤ cash. Fill rules tested with scripted signals. The phase-4 risk engine will be plugged into the same loop. | done |
| 3.5 | Report (`Report.String`): net-of-cost return vs buy-and-hold of the instrument, max drawdown, per-session Sharpe, win rate, trades, costs paid, and a verdict that says "lost money" / "worse than holding" plainly. `cactustrade backtest --instrument ID [--interval 1m --bars 1000 --equity 10000 --market NYSE]` runs it on eToro candles with the demo keys (read-only). | done |
| 3.6 | Run the backtest on the watchlist with real demo-account candles and record the (honest) results; tune nothing on the test period. | done (2026-11-24: results in "Step 3.6 results"; nothing tuned) |
| R17 | Trading 212 accounts | Owner's decision 2026-11-23: **read-only now, trading later.** The Trading 212 Public API (v0, beta, verified 2026-11-23 at docs.trading212.com) has no shorts/CFDs, no idempotency key on orders, no market data and no stop-loss on a market order, so trading needs rules agreed first. An account gets a `platform` (`etoro` default, `trading212`); a Trading 212 account stores its API key and secret (write-only) and is shown in the overview only: the agent, scanner, watchlist and orders refuse it. New adapter `internal/adapters/trading212` with its own allowlist (GET account summary, positions, order history) and an allowlist test; it implements a read-only `Holdings` port and has no mutating method. **Built 2026-11-23** (code, tests, docs): `domain.Platform`, `Account.Platform`, `ErrViewOnly`; migration 00008 (both dialects); `trading212` adapter (allowlist = exactly GET `/api/v0/equity/account/summary`, `/positions`, `/history/orders`; the "instruments" path is *not* allowlisted, nothing needs it); `app.account(...)` is the one place that refuses Trading 212 for every trading path (`app.anyAccount` for managing). Decisions: R17-a `CreateAccount` keeps its eToro signature, `CreateAccountOn` takes the platform; R17-b Trading 212 key names `t212.<id>.api_key` / `.api_secret`, stored through the existing apiKey/userKey fields; R17-c a Trading 212 account can never be the current account (the switcher lists eToro only) and its "trade" share means view; R17-d defence in depth: the eToro client and price stream refuse a non-eToro account before any I/O; R17-e the Trading 212 client waits at most 15 s for a rate-limit slot, retries a GET once on 429/5xx, and holds an endpoint until `x-ratelimit-reset` (≤ 1 min); R17-g the background scanner skips view-only accounts too (`ScanAll`, reviewer finding). R17-f depguard: the LLM adapter may not import `trading212`, `trading212` may not import `etoro` or `app`. Facts: `docs/trading212-api.md`. | done (2026-11-23; `make check` green incl. MariaDB `test-int` with 00008) |
| R18 | All positions across platforms + a graph | Page *Overview* (first under Trading): totals per currency (no FX conversion — amounts in different currencies are never added), one card per account (platform, demo/real, cash, equity, unrealised P&L, or the error), every open position with its platform, and an equity chart per account over time. Equity snapshots (`equity_snapshots`, migration 00008) are recorded every 15 minutes for each account with keys and kept 400 days. Chart is inline SVG (no new dependency). **Built 2026-11-23**: `app.Overview` (accounts read in parallel, 30 s each; missing keys = "no keys yet"; an error never fails the call), `app.EquityHistory` (1–400 days, default 30), `app.RecordEquity` (system, every 15 min from 1 min after start, purges > 400 days); `GET /api/v1/overview`, `GET /api/v1/overview/history?days=`; page *Overview* (first under Trading). Decisions: R18-a eToro holdings are built in the app from `Broker.Portfolio` + the instrument cache/`MarketData.Instruments` (names) and `Quotes` (current price; cosmetic, failures leave it blank) — no eToro change; R18-b amounts are minor units of the account currency (`MinorUnits` in the contract; 2-decimal currencies assumed); R18-c the chart draws **one chart per currency** (lines per account) so different currencies never share an axis; R18-d totals per currency are computed in the domain (`TotalsByCurrency`); R18-e owner's follow-up (2026-11-23): page renamed *Trading Platform Accounts* (nav "Platform accounts"), card grid with an icon button column, keys optional in the create form, version/build under the brand. | done (2026-11-23; `make check` green incl. MariaDB `test-int`) |
| R19 | Trade on the ASX — which platform? | eToro lists ASX shares (e.g. `CBA.ASX`, priced in AUD; eToro's market page, 2026-11-23); whether each is a real share long ×1 is decided per share by eToro's eligibility check when it is added to the watchlist (the same rule as today). Trading 212's API: ASX not verifiable (its pages refuse automated reads). moomoo/Futu OpenAPI: no Australian market (verified). Tiger OpenAPI: AU only on live "comprehensive" accounts, its paper accounts have no AU. IBKR and Saxo have APIs with paper/SIM accounts but their ASX/AU details could not be read. Decision: add ASX to cactusTrade through eToro — market `ASX` (10:00–16:00 Sydney, holidays table verified at asx.com.au), symbol suffix `.ASX`. Built: `domain.MarketASX`, `.ASX` suffix, calendar table (10:00–16:00 Sydney, 14:10 early closes, holidays 2026–2027 from asx.com.au), dashboard and messages. With three markets the scanner now reviews each share only while its own market is in session (reviewer finding; before, LSE shares were also reviewed after their close while NYSE traded). | done (2026-11-23) |

| R20 | UI: grids with icon buttons first, provider logos, version, foldable help | LLM profiles and *Trading Platform Accounts* (renamed from "eToro accounts"; platform chosen first, then that platform's keys) are grids of cards whose first column is a vertical group of icon buttons. LLM cards show the provider's mark (Bootstrap Icons brand glyph where one exists — Claude, OpenAI, Google — else a coloured monogram; no logos drawn by us). Version and build under the brand. *What it does* on the LLM page folds away (`InfoCard`, remembered per browser). | done |
| R21 | Saxo and Tiger Brokers accounts (view-only) | Owner's decision 2026-11-23: **"Saxo and Tiger, view-only".** Built like R17: platforms `saxo` and `tiger` (no migration: `accounts.platform` is VARCHAR(16)); `Tradable()` stays eToro only, so the one central check (`app.account`) refuses them on every trading, agent, scanner, watchlist and theme path; they appear on the *Overview* with equity snapshots and the chart. **IBKR and moomoo not added:** moomoo/Futu OpenAPI has no Australian market; IBKR's docs could not be read and its API needs a local gateway with a daily login. New adapters `internal/adapters/saxo` (allowlist: GET `/port/v1/balances/me`, `/port/v1/positions/me`; the only POST is the sign-in host's `/token`; facts `docs/saxo-api.md`) and `internal/adapters/tiger` (allowlist by method: `prime_assets`, `assets`, `positions`; one signed POST to the constant gateway; facts `docs/tiger-api.md`), each implementing the read-only `Holdings` port, with allowlist tests that fail on any other path/method literal (`/trade/`, `place_order`, …), 15 s timeouts, response caps, per-account limiters, and tests proving errors and logs carry no secret (coverage saxo 97.6 %, tiger 96.1 %). Decisions: R21-a credentials generalised as `domain.PlatformCredentials(p) []CredentialSpec{Key, Label, Help, Multiline, Optional, SetByFlow}` stored as `<prefix>.<id>.<key>`; eToro and Trading 212 names unchanged (`AccountKeyNames` = the first two); `PUT /accounts/{id}/keys` takes `values` by key, `apiKey`/`userKey` still set the first two (both at once = 400); `GET /keys` adds `complete`, `credentials` (set / not set, `updatedAt`, never values), `connectable`, `connectRedirectUri`; new `GET /api/v1/platforms`; R21-b Saxo sign-in: `GET /accounts/{id}/saxo/connect` (owner, web session) issues a random state kept **hashed in memory** (single-use, 10 min, bound to user and account; ≤ 1000 pending) — no table, so no migration; a restart only means connecting again (one replica); public `GET /api/v1/oauth/saxo/callback` redirects to `/accounts?connected=<id>` or `?connect_error=expired|refused|failed` (fixed words only), forged states rate-limited per address; redirect URI = `CACTUSTRADE_PUBLIC_URL` + callback path, shown on the card; new port `OAuthConnector`; R21-c Saxo tokens: the rotated refresh token is stored before the new access token is used (store failure = nothing used); refresh early (< 5 min access, < 20 min refresh left); refreshes serialised per account; a refused refresh token is remembered and not retried (`domain.ErrReconnect` → overview "Reconnect to Saxo", `reconnect: true`); the 24-hour developer token works on SIM only and is refused once stored ≥ 24 h ago (age from `SecretMeta.UpdatedAt`); R21-d unverified response fields are parsed as pointers: account totals without them fail closed, a position without an account-currency value is `Holding.Unknown` (shown "unknown", left out of Invested/P&L) — no FX, no guess; R21-e minor units per ISO currency: `domain.CurrencyDecimals` (JPY, KRW 0; default 2), mirrored in the web `money()`; R21-f Tiger: our own signer (SHA1withRSA PKCS#1 v1.5, sorted `k=v&…`, base64; PEM PKCS#8/PKCS#1 or bare base64 keys), constant gateway (the SDK's "domain garden" lookup is a credential-redirect risk, not implemented), the licence is stored but **not sent** (both SDKs use it only for that lookup), `language`/`device_id`/token not sent, response signature not verified; `prime_assets` falls back to `assets` on business codes 1010–2399 or without an `S` segment, never on rate-limit/permission codes; R21-g depguard: `saxo` and `tiger` may not import `etoro`, each other, `trading212`, `app` (and `tiger` not `github.com/tigerfintech`); the LLM adapter may not import either; R21-h the session cookie's Max-Age is now measured on the service clock (`app.Service.Now`), the clock that set the session's expiry — it was `time.Until` (wall clock), which made every session-based HTTP test fail after 10:00 UTC on 2026-11-23 (fixed test clock); production behaviour is unchanged. **Open (owner):** Tiger's protocol requires SHA-1 (`sign_type` RSA); gosec flags `crypto/sha1` (G401/G505) in `internal/adapters/tiger/sign.go`. A reviewed, path-scoped exclusion in `.golangci.yml` (like the existing reviewed ones) was proposed but not added: it needs the owner's consent. | done (2026-11-24; the owner gave written consent for a gosec G401/G505 exclusion limited to `internal/adapters/tiger/sign.go`, because Tiger signs requests with SHA1withRSA) |
| R22 | A compact fix button wherever something is missing (owner, 2026-11-24: "add a compressed button when something is missing"; e.g. Start said "tradingBudgetCents: set a trading budget…" with no way to act) | The server decides what is missing; React only renders it. `domain.Missing` codes `trading_budget`, `account_keys`, `account`, `watchlist`, `reconcile`, `kill_reset`, `llm_profile`, `email_server`, `public_url`; `domain.MissingError{What, Msg, CanFix, Err}` wraps the refusal's old sentinel or `*ValidationError`, so every `errors.Is`/`errors.As` and HTTP status is unchanged. Used at: `startable` (keys 503, kill switch 409, budget 422, not reconciled 409), pause/stop while killed (409), reconcile without keys (503), no eToro account (404), test e-mail without a server (400), password reset without `CACTUSTRADE_PUBLIC_URL` / mail server (503), Saxo sign-in without `CACTUSTRADE_PUBLIC_URL` (503), LLM test and "reviewing on" with a keyless profile (400). Problem+json gains optional `missing` and `canFix`; the detail is the message for people ("Set a trading budget before starting the agent", no field prefix; `field` is still sent). `AgentPanel.missing` (`[{what, canFix}]`) lists, while the agent is not running and the account is not gated, what `startable` would refuse plus an empty watchlist and a switched-on reviewer without a usable default (missing, invalid or keyless profile, or unreadable setup). Web: `FixButton` (code → icon, short label, title, route or the page's own action), `ProblemAlert` (message + its FixButton) on the agent panel, Accounts, E-mail, LLM reviewer, Watchlist, Themes, Risk limits, Orders, Decision log; `NeedAccount` uses the FixButton; the panel shows a "Before the agent can start" row. Decisions: R22-a `canFix` follows the use case that fixes it (`app.canFix`: budget and keys = owner in a web session; reconcile = `controls.pause`; kill reset = `controls.reset`; watchlist = trade access; LLM and e-mail = settings in a web session; account = agent.control; `public_url` never — it is a deployment setting, shown as a help tooltip, never a link); R22-b reconcile and kill reset run in the panel (reconcile call, the existing reset dialog), keys link to `/accounts?keys=<id>` which opens that card's keys, the budget links to `/risk` (the existing route; there is no `/risk-limits`); on the E-mail page the e-mail fix switches the form on and focuses the host, on the LLM page the LLM fix opens the profile's editor; R22-c `MissingError.Error()` is the message for people, so the OD5 resume reason and events read better; R22-d the ErrNoAccount detail now says "Platform accounts" (the page's name since R18-e); R22-e no new dependency, no migration. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| R23 | Agent controls on the Risk limits page (owner, 2026-11-24: stop the agents, change the limits and start them again without leaving the page) | An *Agents* section above the limits form: one card per eToro account the caller can view (`row row-cols-1 row-cols-md-2 row-cols-xxl-3 g-3`), the first column a vertical icon-button group (Start / Pause / Stop, plus Force close when the server allows it, each with `title` and `aria-label`), then name, demo/REAL, state, reconciled, "limits locked/editable" (the reason as tooltip) and open positions / orders. The current account's card is highlighted (`aria-current`); the form keeps editing it; another card's name switches the current account. Decisions: R23-a a cheap list endpoint **`GET /api/v1/agents`** (`app.AgentCards`) was added: `GET /agent` reads eToro's equity per account, so N cards would cost N portfolio reads; the list reads only the database and this process's state. Its `controls` come from the same `controlsFor` as the panel (test: card controls == panel controls); `limitsEditable`/`limitsBlockedReason` from `limitsEditBlocked`, factored out of `RiskLimitsView` (SetRiskLimits' rules). R23-b the web client's start/pause/stop take an optional `accountId` (the API had it since 4.13-c); a card's name calls `PUT /accounts/current` and refreshes the status — no page reload, so the page's memory survives. R23-c "Start the agent again" is remembered in component state only: an agent the user stopped from this page while it was `running`; the button shows after a successful save of that (current) account's limits and disappears once started (lost on reload, by design: no server state). R23-d "Force close all" appears when ≥ 2 cards allow a force close. R23-e no authorization in React: buttons follow `controls`; the RiskLimits source-ban test is kept. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| R24 | Force close, admin only, password required (owner, 2026-11-24) | Cancels cactusTrade's pending orders, closes every position cactusTrade opened (OD1: never the owner's own), and moves the agent to stopped — no kill latch, no admin reset afterwards. `POST /api/v1/agent/force-close?accountId=` `{password}` and `POST /api/v1/agent/force-close-all` `{password}`; `GET` on both paths is the preview the confirm dialog shows (server-built summary, e.g. "closes 3 positions and cancels 1 order on Demo"). Buttons (`bi-door-closed`, outline-danger) in the R23 cards and the Dashboard AgentPanel's first column, only when `controls.forceClose`. Decisions: R24-a **new permission `agent.force_close`** (admin only) instead of reusing `settings.write`: it closes positions, which has nothing to do with settings, and grants nothing else; plus a web session (never an API key), trade access (which also needs `agent.control`, `effective`) and a demo account. R24-b order of checks: permission → session → account and trade access → demo (a real account → `ErrRealModeGated`, 403 "Real trading needs the phase-7 gate", **before the password and before any read: 0 broker calls**; no real-mode refusal lifted, S7 extended) → password → locks. The preview has the same rights minus the password. R24-c password: `app.checkPassword` uses `Deps.Hasher.Verify`, i.e. the `argon2.Limited` wrapper from `cmd` (D29: busy → 429); the handler (`passwordChecked`) counts the attempt with the sign-in limiter before hashing (D28: per-user delay, 429 after 5 per (user, IP)); a wrong password is the new `domain.ErrWrongPassword` (wraps `ErrForbidden` → 403 "the password is not correct") and stays counted; every other outcome is refunded; a right one is `limiter.Success`. Audited `agent.force_close_refused`. The password is never logged (HTTP test with a captured debug logger). R24-d execution `forceClose`: the whole action runs on `context.WithoutCancel` (a browser going away must not stop the closes half-way; note: Kill itself still runs on the request's context, so a client disconnecting mid-kill cancels its remaining closes until the supervisor's next pass — the kill is stored first, so the supervisor flattens; left as it is, flagged for the owner); control lock → agent stopped unless killed (a killed agent stays killed, its latch untouched) → the running tick cancelled → entry lock (Kill's order) → `flatten`. `flattenKilled` became `flatten(ctx, a, exit, why, rep)`, shared by Kill and force close; an unexported `SupervisorReport.flatten` makes `superviseOrders` cancel pending opens at once with that reason. Each request is sent once, paced by the eToro client's execution limiter; exits are never blocked. When the stop cannot be stored the account is flattened all the same and the error returned (500, like Kill). New `domain.ExitForceClosed` ("force_close") is the closes' reason. R24-e audit `agent.force_close` ("3 closes, 1 cancel sent; the agent is stopped"), danger event `agent.force_closed` → alert e-mail through R12's `notify`. R24-f all accounts: every eToro demo account the caller can trade (real ones left out), the password checked once, one report and one audit entry per account. R24-g `controls.forceClose` + `forceCloseBlockedReason` (set on a real account for a caller who could otherwise use it; the panel and the card show it as a note). Tests: role × via × access matrix, wrong password 403 + counted (6th attempt 429), refusals refunded, 3 own + 1 foreign + 1 pending → 3 closes, 1 cancel, foreign untouched, stopped, no latch, e-mail queued, start again without reset; real → 0 broker calls; 30 rounds of ForceClose ∥ Kill ∥ SetRiskLimits ∥ AgentTick under `-race` (no deadlock, a kill always wins); all-accounts. **Open for 7.3:** classify both force-close POSTs with kill/pause/stop (never throttled by the API rate limits); the password limiter still applies. No migration, no new dependency. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| R25 | Clearer empty watchlist (owner, 2026-11-24: signed in as admin, the watchlist of albie's unshared account looked lost) | An empty watchlist now says "<account name> has no shares on its watchlist", names the caller's other accounts that have shares, each a link-button that switches account (`PUT /accounts/current`, no reload), and explains that each eToro account has its own watchlist and that an account another user owns appears only once its owner shares it. Decisions: R25-a counts server-side in a small endpoint **`GET /api/v1/watchlist/counts`** (`app.WatchlistCounts`: every eToro account the caller can view, count + current) rather than in the accounts list, which is used everywhere and would cost one more query per account on every call. R25-b accounts the caller cannot see are never counted or named (the owner's case: admin sees albie's account only once albie shares it) — tested. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| R26 | Live agent activity log on the Dashboard (owner, 2026-11-24: "show me a live event log on the dashboard page of what the agents are doing, i want the latest event to be at the top of the screen") | An *Agent activity* card, full width, right under the headline figures (above the agent panel): newest first; each line the time (HH:MM:SS, the user's clock), an icon and colour by kind (CSS variables, light and dark), the account's name and a short plain-English text ("Tick: checked 12 shares, 1 idea, 1 order sent · 8 no signal, 3 not in session", "AAPL long idea (trend breakout) — reviewer approved — risk allowed $480.00 — order sent", "Kill switch on Demo by albie", events' own titles for orders, fills, closes and the scanner); a tick's shares fold out ("MSFT: no signal: …"). Filter row All / Orders / Ideas / Agent / Alerts (compact toggle buttons, `aria-pressed`, title and aria-label), a pause toggle that freezes the list and counts what arrived ("3 new"), an empty state that says entries appear as the agent works; ≤ 300 lines in memory. Server: bus-only `agent.tick` (`app.TickSummary` from `AgentTrace.Summary()`: checked, ideas, orders, skipped, shares stopped per step, each share's step and last detail ≤ 200 bytes, killed, error count; published at the end of `AgentTick`, after the kill trip) and `decision` (`app.DecisionView`, published in `finishDecision` after the write was attempted, the order state from the trace, no extra read). Decisions: R26-a **new stream message names `tick` (`LiveTick`, `LiveTickShare`) and `decision` (`LiveDecision{accountId, decision: Decision}`)**, not a kind inside `event`: `LiveEvent` carries a stored `Event` with an id the bell counts; contract first, `make gen`. R26-b neither is stored as an events row (no bell, no e-mail, no retention); ticks are therefore live only and a reload shows no past rounds (the decision log and events hold what matters). R26-c filtering exactly as `order`: only the stream's own account and only when it is in the caller's `LiveFilter` (`BusAccount` names both new types); `Publish` stays non-blocking (no goroutine), the 4.12 property test unchanged and green. R26-d **no `GET /api/v1/activity`**: the client merges `GET /events?limit=100` and `GET /decisions?limit=50` itself (a keyed merge, newest first, cap 300); for that `Event` gained an optional `accountId` (absent = system-wide; the list was already limited to accounts the caller can view), so the log keeps the current account's and system-wide events, like the stream. The history is read again on `resync` and every 15 s while the page polls. R26-e a decision's line sorts by the decision's time; a newer message about the same thing (a pending decision followed by its outcome) replaces the line. R26-f raw `order` transitions are not log lines: the stored `order.*`/`position.*` events already say sent, filled, rejected and closed, so the log would repeat them; the *Orders and fills* card stays. Its "Events since the page opened" list was removed (the log replaces it; `Live.events` became `Live.activity`). R26-g the wording, icons and filter groups are presentation in `web/src/lib/activity.ts`; nothing in React decides or authorizes. No migration, no new dependency, no eToro path. Tests: Go `TestPublishTickAndDecisions` (entry, reviewer reject, no signal, paused: one tick per tick with the account, counts and reasons; one decision message per stored decision, checked stored when published; tick last; nothing stored as an event), `TestPublishTickNeverBlocksAnOrder` (a full subscriber: the order is sent, events dropped), `TestAgentTraceSummary`, `TestLiveStreamTicksAndDecisions` (a viewer of A gets A's tick and decision, never B's), `/events` carries `accountId`; Vitest `ActivityLog.test.tsx` (wording, newest first, only this account and system-wide, live message on top, filters, pause with count, cap 300 and replace by key, empty state, refetch on refresh, a failed read shown) and a Dashboard stream test (tick and decision on top, the log above the agent panel). | done (2026-11-24; `make check` green incl. MariaDB test-int and `make audit`) |
| R27 | Watchlist themes, auto-assign, filter, JSON export/import and holdings (owner, 2026-11-24: "on the watchlist page add the ability to assign the item to a theme by adding a dropdown after the eligibility column or add a function to automatically assign items to a theme that represents it the closest then add a filter by theme below the search and allow me to export my watchlist and theme list to json and allow me to import it from the json file. it will be helpful to be able to see what stock, number of shares and value of shares the user holds and under which exchange e.g. LSE or ASX etc") | Watchlist table: Symbol, Name, **Exchange** (badge: LSE / NYSE / NASDAQ / ASX), Bid, Ask, Eligibility, **Theme** (dropdown "— none —" + the account's themes when the server says `canEdit`, else the theme's name), **Holding** (units, value, P&L, "cactusTrade" / "yours" / "mixed", "short"), remove (icon). Below the search: theme filter chips All / each theme / No theme (`aria-pressed`). Tools (compact icon group, only with `canEdit`): Auto-assign themes, Export (download link), Import (file picker). A *Holdings* card above the table: total value and P&L held on watchlist shares, and the holdings not on the watchlist (chips). Contract first, `make gen`: `WatchItem` gains optional `themeId` and `holding`; new `GET /api/v1/watchlist/overview` (`canEdit`, `holdings`), `POST /api/v1/watchlist/theme-assignments`, `GET /api/v1/watchlist/theme-proposals`, `GET /api/v1/watchlist/export`, `POST /api/v1/watchlist/import`. Domain `internal/domain/watchlist.go` (`ScoreTheme`, `BestTheme`, `ValidateWatchlistFile`, `HoldingsByInstrument`); app `watchlist_themes.go` (`AssignThemes`, `ProposeThemes`, `ExportWatchlist`, `ImportWatchlist`) and `watchlist_view.go` (`WatchlistEntries`, `WatchlistOverview`). Decisions: **R27-a** the theme model is kept: a share belongs to a theme through `Theme.Instruments` (picked shares); one theme per watchlist share — assigning adds it to that theme and removes it from every other theme of the account, "" removes it from all; older data with a share in two themes shows the first theme by name until reassigned. All assignments are validated (on the watchlist, theme of the account, ≤ 50 shares per theme, ≤ 200 per request) before anything is stored; themes are then stored one by one (no cross-theme transaction; a failure mid-way leaves a consistent subset, a retry finishes it). **R27-b** taking a theme's last share away may leave a theme with no search words, industry or shares (allowed here, unlike the Themes form): it then finds nothing until edited. **R27-c** auto-assign is deterministic, no LLM: per search word, 5 when it is the symbol (without market suffix), 3 when it starts a word of the name, 2 when it starts a word of the industry name; 4 when the share is in the theme's eToro industry; best score wins, ties by theme name (case-insensitive) then id. A share's industry is found by one read-only eToro screen (`Research.Screen`, 100 most popular) per distinct theme industry plus one `Industries` read for names, only when a share has no theme; a failed read becomes a note and the industry is not scored. It proposes only (GET, changes nothing); accepting (all or one) calls `theme-assignments`. **R27-d** `canEdit` = effective trade access to the current account (`AccountView.Access`, already capped by role); the existing add/remove UI condition is unchanged. **R27-e** holdings come from the portfolio view cache through `viewPortfolio` (PLAN 6.6a): fresh cache = no eToro call, else one read per account per 10 s shared with every view — never one per row; no keys / no broker / a failed read = holdings unknown with a reason, the list still answers. `watchlist_view.go` is added to the view-cache readers allowlist in `TestViewCacheReadersAreViewsOnly` and its use cases to the identifiers the order-path files may not use (guardrail §2.2 unchanged). "Ours" = an open `agent_positions` row for the position id. **R27-f** the file: `{version: 1, exportedAt, account: {name}, watchlist: [{instrumentId, symbol, market}], themes: [{name, keywords, industryId, instruments, llmProfileName?}]}` — no ids of accounts, themes or profiles, no keys. Import: body ≤ 1 MiB (`decodeLimit`; plain JSON bodies stay 64 KiB), unknown fields and trailing values refused, version 1, ≤ 200 shares, ≤ 50 themes, each theme by `NormalizeTheme`, positive unique ids, unique theme names (case-insensitive), one theme per share inside the file; a refused file changes nothing. Shares already on the watchlist are skipped without an eToro call (so a second import of the same file asks eToro nothing for shares); a market other than LSE/NYSE/ASX is refused before eToro; every other share goes through `addToWatchlist` (the hand path: eToro's instrument + eligibility); refusals and an unreachable eToro are reported per share, a store failure ends the import (500). Themes go through `saveTheme` (same name case-insensitive = update keeping its enabled flag; identical = unchanged; the LLM profile by name, unknown = default + a note); then the imported themes' shares leave every other theme (noted). Trade access for export and import, import also web session only (API key 403); audited `watchlist.export` / `watchlist.import`, event `watchlist.imported`. **R27-g** rate classes: `GET /watchlist/export` and `GET /watchlist/theme-proposals` count as exports (10/min, eToro screens behind proposals); import is a mutation. No migration (the theme model already stores picked shares in both dialects), no new dependency, no eToro path. Tests: domain tables (`TestScoreTheme`, `TestBestTheme` incl. order independence, `TestValidateWatchlistFile` incl. malicious/oversize cases, `TestHoldingsByInstrument`); app `TestAssignThemesMovesTheShare`, `TestProposeThemes` (table, deterministic, nothing stored), `TestExportImportWatchlist` (no ids/keys in the file, ineligible/foreign-market shares reported and never added, idempotent second import with no eligibility calls for shares, update by name moves shares, refused files change nothing, eToro down), `TestWatchlistThemesAccess` (owner / trade / view / viewer role / API key / not shared), `TestWatchlistHoldingsFromTheViewCache` (0 reads on a fresh cache, 1 shared read after the TTL, failure = unknown); HTTP `TestWatchlistThemesExportImport` (headers, 100 KiB ok, > 1 MiB 413, bad version, unknown field, two values, 201 shares, no CSRF, text/plain 415, API key may export but not import); Vitest (dropdown right after Eligibility, chips, export link, import result and a non-JSON file, proposals accepted only on click, holdings columns, read-only view). | done (2026-11-24) |
| R28 | Risk presets, aggressiveness, autonomy and a clearer Regimes card (owner, 2026-11-24: "i want a preset risk limits e.g. low, low-meduim, medium, medium-high, high. make [the Regimes card on the Dashboard] this more understandable. … i want the platform to have a bit more autonomouse e.g. low-autonomouse, meduim-autonomouse, high-autonomouse and full-autonomouse - the default must me medium-autonomouse trading. i need the agents to be more aggressive when trading at a scale e.g. low, medium, high and full"; the last word was cut off, "full" assumed) | Three per-account choices plus a clearer Regimes card; **no guardrail of CLAUDE.md §2 is weakened by any preset or level** (leverage 1, the two order shapes, cash/budget caps, shorts ≤ 25 % of the budget with a stop above the entry, flat before the close, the LLM only shrinks or rejects, fail closed, the real gate, the admin-only kill reset). Contract first, `make gen`: `GET /api/v1/risk/presets` (`RiskPresetList`), `PUT /api/v1/risk/limits` now takes `RiskLimitsChange` (`{preset}` alone or every limit) and answers `RiskLimitsView` with `preset` (a preset id or `custom`, also on GET), `GET/PUT /api/v1/agent/style` (`AgentStyle`, `AgentStyleChange`), `LiveRegime` gains `mood`, `stance`, `mayDo`, `LiveSnapshot` gains `regimeLegend`, `Review.outcome` gains `proposed`, `ReviewBoard` gains `canAccept`. Code: `internal/domain/style.go` (presets, aggressiveness, autonomy policy, research mode, `AggressivenessAdapt`, `RegimeLegend`), `internal/strategy/style.go` (`ConfigFor`, `MayDo`), `internal/app/style.go` (`AgentStyle`, `SetAgentStyle`, `RiskPresets`, `ApplyRiskPreset`, **`ResearchMode(ctx, accountID) domain.ResearchMode`** and `AutonomyPolicy(ctx, accountID)` for the web-research agent), `internal/app/autonomy.go` (scan interval, theme auto-assign, stale pruning, full auto-start), `internal/adapters/http/handlers_style.go`; web `RiskPresets.tsx`, `AgentStylePanel.tsx` (both on the Risk limits page), `LiveCards.tsx` `RegimeList` ("Market mood"), `ReviewBoard.tsx` (accept a proposal). **Final preset table** (R28-a; every row passes `Validate`, ≤ the hard limits; the budget is never part of a preset; no-entry/flatten windows = max(current, default 30/10 min), portfolio/quote age = min(current, default 5 s/30 s)): low = daily loss 50 bp, drawdown 300 bp, largest position 500 bp, 3 open positions, shorts 0; low_medium = 100 / 500 / 1000 / 5 / 0; medium = 200 / 1000 / 2000 / 8 / 1000; medium_high = 300 / 1500 / 2500 / 12 / 2000; high = 400 / 2000 / 3000 / 15 / 2500. On a real account each value is capped at `RealCeilings` (200 / 1000 / 2000 / 8 / 2500), so medium_high and high clamp; a preset that raises any limit disarms an armed real account through the existing `RaisesAny` (tested). The brief's defaults (shorts 2500 bp) match no preset and show as "custom". **Final aggressiveness table** (R28-b; strategy parameters only, `HighVolStop` 0.40 and every stop rule unchanged at every level, the "largest position" limit still caps the size): low = risk per trade 0.25 %, entry probability 0.70, largest suggested size 10 % of equity; medium (default, today's behaviour) = 0.5 % / 0.60 / 20 %; high = 1 % / 0.55 / 30 %; full = 1.5 % / 0.50 / 40 %. **Autonomy** (R28-c; default medium): low = the agent trades only shares a person put on the watchlist (`AddedBy` not `scanner`/`research`), the scanner and research only propose (`ResearchMode=propose`, review outcome `proposed`, accepted by adding the share by hand from the review board); medium = today's behaviour plus research (`auto_add`, with events); high = medium + scanner every 5 min instead of 15 (`ScanAll` runs every `app.ScanTick` = 5 min, each account when its interval passed), new automatic shares get the closest theme (R27 scoring), automatic shares with no signal for 10 trading days of their market leave the watchlist (never a share a person added, never one with our position or open order, nothing without a calendar); full = high + a stopped demo agent starts itself at the first market open of each risk day (once per risk day, so an agent the owner stops stays stopped until the next day; never killed, paused or real (OD26); only through `startable` under the control lock, so keys, valid limits, a budget and a clean reconciliation are required) + aggressiveness one step down after a risk day on which the daily-loss latch was set and one step back up after 3 clean risk days on which the agent ran (never above the chosen level; days the agent did not run neither count nor break the run). Decisions: R28-d aggressiveness, autonomy and the adjustment are settings (`agent.aggressiveness.<id>`, `agent.autonomy.<id>`, `agent.aggressiveness_adapt.<id>`), the last scanner signal per share `watchlist.signals.<id>`, the auto-start day `agent.autostart.<id>`: **no migration** (the settings table exists in both dialects); an unknown or unreadable autonomy/aggressiveness reads as medium (full needs an explicit readable choice), an unreadable adjustment as the most careful level, an unreadable agent state never auto-starts. R28-e style changes follow `SetRiskLimits`' rules (owner, web session, stopped, no kill; re-checked under the control lock), reset the adjustment, are audited (`agent.style_changed`) with an event; auto actions are audited as actor `autonomy` (`agent.auto_start`, `theme.auto_assign`, `watchlist.auto_remove`, `agent.aggressiveness_adapted`) with events. R28-f a PUT with `preset` plus any limit, or a partial custom set, is 400. R28-g the Regimes card is now "Market mood": the server sends the labels, the legend and "what the agent may do" at the account's effective thresholds (`strategy.MayDo`, same gates as `Engine.Evaluate`); React only maps a regime to an icon/colour; stacked bar with `role=img` and an aria-label of the percentages; "updated HH:MM"; an InfoCard in two sentences. R28-h `cmd/cactustrade/main.go` changed one line (the scanner loop ticks every `app.ScanTick`) and `real_refusal_test.go` lists the new mode references (presets capped on real, demo-only auto-start). Tests: domain `TestRiskPresetsAreValidAndOrdered`, `TestApplyPreset`, property `TestApplyPresetProperties`, `TestAggressiveness`, `TestAggressivenessAdapt`, `TestAutonomyPolicy`; strategy `TestConfigFor`, property `TestAggressivenessNeverSuggestsAboveItsCap`, `TestMayDo`; app `TestAgentStyle`, `TestApplyRiskPresetUseCase`, `TestRiskPresetOnARealAccount` (clamp, raise disarms, real never auto-starts), `TestResearchMode`, `TestScannerByAutonomy`, `TestScannerPrunesStaleAutomaticShares`, `TestScanAllFollowsTheAutonomyInterval`, `TestLowAutonomyTradesOwnerSharesOnly`, `TestAgentUsesTheAccountsAggressiveness`, `TestFullAutonomyStartsAtTheOpen`, `TestFullAutonomyStartsOncePerTradingDay`, `TestFullAutonomyAdaptsAggressiveness`, `TestLiveRegimesSayWhatTheAgentMayDo`, and the reduced 4.12 run `TestStyleNeverChangesAGuardrail` (5 presets × 4 aggressiveness × 4 autonomy, 60 ticks each, larger proposals at bolder levels, every 4.12 invariant checked) plus `TestLowAutonomyNeverTradesScannerShares`; HTTP `TestRiskPresetsAndAgentStyle`, `TestReviewBoardCanAcceptAndLiveRegimeWords`; Vitest `AgentStyle.test.tsx` and a RiskLimits preset/custom test. No new dependency, no eToro path. | done (2026-11-24; `make check` green incl. MariaDB test-int and `make audit`) |
| R29 | Web research for the watchlist (owner, 2026-11-24: "i also want an agent to look at web information about stocks and add them to the watchlist and trade with those stocks") | A separate component, **not** the reviewer: port `ports.Researcher.Research(ctx, cfg, key, ResearchRequest) (ResearchResult, LLMCall, error)`, implemented by the LLM adapter (`internal/adapters/llm/research.go`). The request carries the account's enabled themes (names, search words), the exchanges allowed (LSE, NYSE, NASDAQ, ASX), the symbols already on the watchlist or proposed, today's date and a maximum (default 10, ≤ 20); never credentials, figures, orders or positions. The model's only tool is its provider's built-in web search; it answers one JSON array `[{symbol, exchange, name, theme?, reason ≤ 300, sources ≤ 3}]`. Use cases in `internal/app/web_research.go` (`ResearchNow`, `ResearchAll`, `Research`, `AcceptResearchProposal`, `DismissResearchProposal`, `ResearchSettings`, `SetResearchSettings`) and the stand-in `internal/app/research_mode.go`; domain `internal/domain/web_research.go`. Contract first, `make gen`: `GET /api/v1/research`, `POST /api/v1/research/run`, `POST /api/v1/research/proposals/{id}/accept`, `DELETE /api/v1/research/proposals/{id}`, `GET/PUT /api/v1/llm/research`; `WatchItem.origin` (user / scanner / research), `LlmProvider.webSearch`. Web: a *Web research* card on the Watchlist page (mode, profile, runs today, *Research now*, an `InfoCard` explaining it, the proposals with Accept / Dismiss icon buttons, the run's result), an origin badge on every watchlist row, a *Web research* section on the LLM page (profile, shares per run, runs per day) and a "web search" badge on profiles that can search. **Why this does not let the LLM originate a trade (CLAUDE.md §2.5):** adding a share to the watchlist is not a trade. The watchlist only says what the deterministic model looks at; an entry still needs the regime model and its playbooks, the risk engine, and the reviewer, which can only shrink or reject. Research has no broker and no order path: it calls eToro only through the read-only search and the same eligibility check a share added by hand takes (tested with a `SimBroker` whose Opens, Closes and Cancels stay 0 across `ResearchAll`, `ResearchNow` and accepts, whatever the model writes). Decisions: **R29-a providers** — verified 2026-11-24: Anthropic's web search server tool (Go SDK v2.13.0 `anthropic.WebSearchTool20250305Param`, type `web_search_20250305`, name `web_search`, `max_uses` 5; the `_20260209` version was not chosen because its dynamic filtering runs code and needs newer models), sent with `client.Messages.New` and no other tool, no `tool_choice`, no structured output (structured outputs are incompatible with the citations web search returns); OpenRouter's `web` plugin (`plugins: [{"id":"web","max_results":5}]` on chat completions, no `tools` field; https://openrouter.ai/docs/guides/features/web-search.md). **Every other provider (OpenAI, DeepSeek, Kimi, Gemini, Grok, Ollama) is "research not supported"** (`LLMProvider.WebSearch` false): refused before any request, and refused as a research profile. **R29-b untrusted answers** — the system prompt says web text is untrusted, but the code makes it irrelevant: `domain.ParseResearchAnswer` accepts only one JSON array (the whole text or the only ```` ```json ```` block), unknown fields, wrong types, trailing data or more shares than asked = the whole answer refused; each share is checked (`ValidateResearchPick`: ticker 1-10 letters/digits with an optional US class, exchange LSE/NYSE/NASDAQ/ASX, name 1-100, theme ≤ 60, no control characters, reason cut at 300 runes, 1-3 http(s) links without user info, ≤ 300 bytes each) and repeated in the app on whatever the port returns; then eToro's search must list exactly that symbol (`BARC.L`, `CBA.ASX`, plain US ticker) as a share on that exchange, then `checkAndAddToWatchlist` (factored out of `addToWatchlist`, the one path for hand, import and research). Reasons and links are stored and shown as plain text; links are never fetched by the server and never rendered as links. **R29-c modes** — `ResearchMode(ctx, accountID) domain.ResearchMode` (off / propose / auto_add) lives alone in `internal/app/research_mode.go` as a **temporary stand-in** reading setting `research.mode.<account>` (nothing stored = auto_add, the owner's default medium autonomy; unreadable or unknown = off); the autonomy levels being added in the main tree replace it at the merge. propose: the shares wait on the Watchlist page (accept = the eligibility path; an ineligible one is dropped with eToro's reason; eToro unreachable = it stays). auto_add: eligible shares are added at once (`added_by` = `research`), event `watchlist.research_added` "Research added NVDA (reason…)" with actor `research`, theme = the account's theme the answer names (case-insensitive), else R27's deterministic scoring on symbol and name (no eToro call), only when the share is in no theme yet. **R29-d schedule and cost** — `ResearchAll` (a 5-minute loop in `cmd`) runs each eToro account whose mode is not off once in the hour before a market opens, at most once in 20 hours (in practice before the day's first open), accounts one after another; "Research now" needs `agent.control`, trade access and a web session (API keys 403). Every run is counted before the LLM is asked: at most *runs per day* in 24 hours (default 3, 0 = off, ≤ 24), at least 10 minutes apart, one at a time per account (409 otherwise). Output capped at 4 096 tokens and ≤ 5 searches; timeout = the profile's (default 120 s, ≤ 300 s). Every call is logged as purpose `research` with tokens and cost (the profile's token prices; the provider's search fees come on top and are not in the cost). A provider failure is an event `research.failed` (warning) and nothing is added. **R29-e research profile** — setting `research.config` `{profileId, maxShares, runsPerDay}` on the LLM page (admin, web session, audited); `profileId` "" = the default reviewer profile; a profile whose provider cannot search, a missing profile or key = research off with the `llm_profile` missing item and a sentence saying why (FixButton). **R29-f storage, no migration** — runs, the last scheduled run and the waiting proposals (≤ 20, oldest dropped first, the whole value kept ≤ 60 000 bytes for MariaDB's TEXT) are one JSON setting `research.state.<account>`, changed under an in-process lock; an unreadable state refuses runs (fail closed). With several replicas two runs could overlap once; the runs-per-day count still bounds the cost. **R29-g origin** — `WatchItem.origin` is derived from `added_by` (`scanner`, `research`, else a person); a user named "scanner" or "research" would be shown with that origin (cosmetic, noted). No new dependency, no migration, no eToro path (search and eligibility were already allowlisted). Tests: domain `TestParseResearchAnswer` (garbage, prose, two fences, unknown "order" fields, wrong types, trailing data, too many, oversize, injection text kept as data, bad exchanges/symbols/links refused one by one), `TestValidateResearchPick`, `TestResearchSymbolsAndExchanges`, `TestResearchModeConfigOrigin`; adapter `TestResearchRequestHasWebSearchOnly` (per provider: the exact body sent, the web search the only tool, no `tool_choice`/`functions`/`response_format`, no key; every other provider refused with no request), `TestResearchUnusableAnswers`, `TestResearchRefusedBeforeSending`; app `TestResearchAutoAdd` (eligible added with origin research and themes, ineligible / unknown / wrong exchange / duplicate refused, call logged with cost, events), `TestResearchPropose`, `TestResearchRefusals` (table: off, 0 runs, bad settings, profile without web search, no profile, no key, no researcher, viewer, API key, view share — no LLM call), `TestResearchPacingAndFailures`, `TestResearchAllNeverTrades` (schedule; Opens == 0), `TestResearchSettings`, `TestResearchModeDefault`, `TestResearchStateStaysSmall`; HTTP `TestWebResearchEndpoints`; Vitest `ResearchCard.test.tsx` (plain text, no links or markup, accept/dismiss, run result, failure, missing profile with fix, rights) and `ResearchSettings.test.tsx`. | done (2026-11-24; `make check` green incl. MariaDB test-int and `make audit`) |
### Step 3.6 results (2026-11-24, `backtest --watchlist --account env-demo`, demo candles, nothing tuned)

The existing walk-forward backtest (regime model + playbooks, eToro's costs endpoint, next-bar fills, never across a session), $10,000 per share, the 55 shares of the env-demo watchlist (mostly NYSE/NASDAQ, a few LSE/ASX), 1,000 bars each:

| Bars | Period | Shares lost money | Trades | Costs paid | Net result | Mean net return | Mean buy-and-hold |
|---|---|---|---|---|---|---|---|
| 1-minute (≈ 1-2 sessions) | to 2026-11-24 | 51 of 55 (2 worse than holding, 1 no trades, 1 positive) | 550 | $5,510.55 | −$6,000.76 | −1.09 % | −2.58 % |
| 5-minute (the agent's interval, ≈ 13 sessions) | 2026-08-17 … 2026-11-24 | 47 of 55 (3 worse than holding, 1 no trades, 4 positive) | 645 | $6,515.95 | −$6,621.79 | −1.20 % | −6.24 % |

Plainly: **the strategy lost money on almost every share in both runs.** Costs (spread, fees, stamp duty) were about as large as the whole net loss, i.e. the signals did not earn their costs. The mean loss was smaller than holding the shares over the same falling period, which says nothing about the future. A few shares ended slightly positive (5-minute: ARM +0.56 %, SMTC +0.38 %, AMZN +0.12 %, NVDA +0.10 %); with so few trades that is noise. 1,000 bars is a small sample; no parameter was changed in response. Raw JSON: kept by the owner's run; command: `cactustrade backtest --watchlist --account <id> [--interval 5m] --json`.

| R30 | Trading statements as PDF, e-mailed daily, weekly and monthly (owner, 2026-11-24: "on the system settings page add a daily, weekly and monthly summary of tradings in a professional pdf and set email notifications for daily, weekly and monthly etc. each notification i want to select the trading account and the email it will send the 'statement' to - i want the system to summarise what happened during the trade period and i want a combined trade statement of all exchanges and accounts and then separate statements per trading account and exchange to be emailed.") | A *Trading statements* section on the System page: a grid of schedule cards (icon buttons first: Send now, Preview, Edit, Delete via `ConfirmDialog`; period, accounts, recipients, PDFs, last sent, next send, last error), an add/edit form (period, accounts as checkboxes — none = all, recipient chips ≤ 10, which PDFs, on/off), a preview form (period, a day, combined / one account / one exchange) and an `InfoCard` saying what is sent and when; no mail server → warning with `FixButton email_server`. Domain (pure, `internal/domain/statement*.go`): `StatementWindow` (`WindowFor`, `LastCompleteWindow`, `NextSendAt`, `Title`, `Key`), `BuildStatement` (sections: accounts, equity per account and per currency, cactusTrade's closed trades with wins/losses/breakeven, win rate (absent without trades), realised P&L, fees reported and unknown, largest win/loss, trades of other positions counted apart, per exchange and per account, positions open at the end, view-only holdings, drawdown, kill switch and daily-stop events, decisions by outcome, orders by state, LLM calls/tokens/cost, notes, summary), `StatementSchedule` (validation), `FormatMoney` (£, €, ¥, A$, thousands separators), `StatementFileName`; `domain.Mail.Attachments` with `ValidateAttachments`. Ports: `StatementRenderer`, `StatementSchedules` (repository), `LLMCalls.UsageFor`. Adapters: `internal/adapters/pdf` (fpdf), `mail` (multipart/mixed), `sqlstore` (migration 00013 in both dialects: `outbox_attachments`, `statement_schedules`; shared suite `runStatements`). App: `internal/app/statements.go`. Contract first (`make gen`): `GET/POST /api/v1/statements/schedules`, `PUT/DELETE /api/v1/statements/schedules/{id}`, `POST …/{id}/send-now`, `GET /api/v1/statements/preview.pdf?period&from&scope&accountId&exchange&scheduleId`. `cmd`: `statementsLoop` (2 min after start, then every 5 min), `pdf.New()` wired. Decisions: **R30-a** no LLM anywhere in a statement: `BuildStatement` is deterministic (input order does not matter; tested) and the summary paragraph is assembled from its own figures in neutral words ("closed 12 trades on 2 accounts (LSE 3, NYSE 9): …"), so a statement is auditable and reproducible. **R30-b** periods are whole risk days in UTC (22:00 boundary): daily = one risk day, sent 22:30 UTC, and **only Monday to Friday risk days are sent** (no supported market trades on a Saturday or Sunday risk day; a preview of any day works); weekly = the Monday to Friday risk days of the ISO week (Sunday 22:00 to Friday 22:00 UTC), sent Saturday 06:00 UTC; monthly = the calendar month's risk days (the last day of the previous month 22:00 to the last day 22:00 UTC), sent the 1st 06:00 UTC. Keys `2026-11-24`, `2026-W48`, `2026-11`. **R30-c** once per period: each scheduled e-mail's outbox id is `sha256(schedule, period key, recipient)` (a second enqueue is `ErrConflict`), and `last_period` is then set with a compare-and-set; a restart, a lost record or a second replica sends nothing twice. After a long stop only the newest period is sent (no backlog). A new schedule, a changed period or a re-enabled schedule starts at the next period (`last_period` = the last complete one); *Send now* sends the last complete period with fresh ids, leaves `last_period` alone, at most once a minute per schedule (stored `manual_sent_at`, compare-and-set; else 429). A failure that sending again cannot fix (no account left, more than 10 MiB) is recorded as `last_error` with the period; any other failure (store, renderer) leaves the period open for the next round. **R30-d** one e-mail **per recipient** (each with every chosen PDF attached), not one e-mail with all recipients: typed addresses may belong to different people, who should not see each other's addresses. **R30-e** who: a web session only, for every statement endpoint (an API key gets 403: the recipients are addresses the user typed). A schedule's accounts are ones its owner owns, or for an admin any account the admin can see (`ForUser`), checked on save and again at every send (a deleted, unshared or no longer owned account drops out); no accounts = all such accounts at send time. Schedules are listed, edited, sent, previewed and deleted by their owner; an admin sees and manages every user's (accounts still judged by the owner); anyone else gets 404. Recipients: `domain.ValidateEmail`, lower-cased, unique, 1–10; they need not be users (R12's preferences are for event mail). Every create, update, delete, send now and scheduled send is audited (`statement.*`, with the recipients). **R30-f** what a statement covers: trade figures are cactusTrade's own closed trades (OD15, US dollars, as History); trades of positions it did not open are counted apart; equity is the first and last snapshot in the period per account, totals per currency only (never across currencies, said on the PDF and in the summary); unknown fees stay unknown; positions open at the end are cactusTrade's agent positions opened before the end and not closed by then (read back 31 days plus every open one); view-only platforms (Trading 212, Saxo, Tiger) add equity (snapshots) and holdings **as read when the statement is made** (labelled so; a failed read is shown with its reason); kill switch = `agent.killed` events, daily stop = `risk.daily_stop` events of the account in the period; decisions and events are read up to 20 000 per account (beyond: "lower bounds" note); LLM usage is summed per account in the period (`LLMCalls.UsageFor`, a new query on both databases). An exchange statement holds the trades and positions on that exchange across the accounts; equity, drawdown, risk events, agent activity and LLM use belong to accounts and are left out (said in its notes). Exchanges come from the cached instrument's market, else the symbol's suffix, else "Other"; per-exchange PDFs are made for exchanges with trades or positions in the period. **R30-g** PDF: `codeberg.org/go-pdf/fpdf` v0.12.0 (MIT; the maintained continuation of `github.com/go-pdf/fpdf`, which is archived; root package uses the standard library only). A4; header with the cactusTrade logo drawn as vector shapes from `web/public/logo.svg` (no image file, no fetch), "cactusTrade", period and scope; the period, scope and account names; a tinted summary box; tables with right-aligned numbers and negatives in red; "Page n of N"; a footer with the generated time and "software, not financial advice — trading can lose money" on every page. Text uses the standard Helvetica fonts in Windows-1252 (£, €, ¥, A$, Western European names; any other character prints as "?"): no font file to embed, and the text stays extractable. Deterministic bytes (fixed creation date from the statement, sorted catalog). Golden test with a standard-library reader (header, xref/trailer, page objects, Flate streams inflated, shown strings decoded) checks the key figures and that `internal/adapters/pdf/testdata/statement-weekly-combined.pdf` (the committed sample, `-update` rewrites it) has the same text. depguard: the `pdf` adapter may not import a broker, the store, mail, the LLM adapter, `app`, `net` or `net/http`. **R30-h** attachments: `domain.Attachment{Filename, ContentType, Data}`; only `application/pdf`; file names `cactusTrade-statement-<key>-<period>-<scope>[-<name>].pdf` (ASCII letters, digits, `.`, `-`, `_`; checked again by the outbox and the mail adapter, so no header needs quoting); at most 60 files and 10 MiB per mail; the outbox stores them in `outbox_attachments` (BLOB / MEDIUMBLOB) in the same transaction as the mail, and they go with it in the 30-day purge (`ON DELETE CASCADE`). The mail adapter writes multipart/mixed (text part quoted-printable, files base64 in 76-character lines). **R30-i** the System page is now in the menu for every user (under *Setup*): version and database for all, *Reload settings* and *Restart* still for admins only (the API was already admin-only), *Trading statements* for everyone who owns an account. **R30-j** rate classes: the preview and *Send now* count as exports (10/min); the other statement routes are reads or mutations. Tests: domain `TestWindowFor` (daily, weekly incl. across months and years, monthly incl. February 2028 and December, London/New York daylight saving dates), `TestLastCompleteWindowAndNextSend` (22:29/22:30, weekends skipped, Saturday 05:59/06:00, the 1st 05:59/06:00, the last evening of a month), `TestFormatMoney`, `TestBuildStatementCombined` (USD + GBP never summed, unknown fees, foreign trades, largest win/loss, summary sentences, order independence), `TestBuildStatementScopes`, `TestStatementFileName`, `TestStatementScheduleValidate`, `TestValidateAttachments`; storetest `outbox_attachments` (every byte value round-trips; refused file stores nothing; purge cascades), `llm_usage_for`, `statement_schedules` (CRUD, compare-and-sets, cascade with the owner) on SQLite and MariaDB; mail `TestAttachmentsDecodeBackIdentical`, `TestRefusesUnsafeAttachmentsAndSendsNothing`; pdf `TestGoldenStatement`, `TestScopesAndPageBreaks` (header repeated over page breaks), `TestEncoding`; app `TestStatementFiguresFromFixtures`, `TestStatementScheduleAccess` (access matrix), `TestSendDueStatementsOnce` (once, after a restart, after a lost record, one send after three missed weeks, off/on), `TestSendDueStatementsFailures`, `TestSendStatementNow` (no mail server = `email_server` fix, once a minute, outbox delivers the PDFs), `TestPreviewOfASchedule`; HTTP `TestStatementEndpoints` (CSRF, 503 with the fix, 429, PDF headers `application/pdf` + attachment + `no-store`, bad parameters, API key 403, rate classes); cmd `TestStatementsLoop`; Vitest `TradingStatements.test.tsx` (cards with icon buttons first, preview links, mail-server fix for who can fix it, add with account checkboxes and recipient chips, 10-recipient cap, send now, edit, delete confirm, a refused send with its fix, the preview form, System page for non-admins) and a delete confirmation in `Confirmations.test.tsx`. New dependency `codeberg.org/go-pdf/fpdf` (dependency table). No eToro path, no money guardrail touched. Migration 00013 is the next free number in this tree: if another branch also adds 00013, renumber at merge (both dialects). | done (2026-11-24; `make check` green incl. MariaDB test-int and `make audit`) |
| R31 | Analysis page and one-page PDF (owner, 2026-11-24: "add an analysing menu and page to summarise all trades by date, exchange, trading account, theme etc. i want a well documented analysis of the period and want to be able to export it to a one page pdf and if you need to you can size the pdf to fit the analysis period, i want easy to understand summaries and graphs etc.") | Left menu *Analysis* under Trading (icon `bi-graph-up-arrow`, route `/analysis`): period presets (Today, This week, This month, Last month, Everything = 400 days, Custom with two date fields), account chips (none = all you can see; `aria-pressed`), an *Export PDF* compact icon link; KPI tiles; the narrative under headings; inline SVG charts (P&L by date bucket, equity per currency reusing `EquityCharts` with drawdown shading, P&L bars by exchange / account / theme / playbook, a win/loss/breakeven split bar) in the Okabe–Ito palette through CSS variables for light and dark; a sortable table per grouping (`aria-sort`), recorded equity per account and per currency; an `InfoCard` on how the figures are made; an empty state. Domain (pure): `internal/domain/analysis_period.go` (`AnalysisPreset`, `AnalysisWindowFor`, `BucketFor`, `AnalysisWindow.Title`, `AnalysisFileName`) and `analysis.go` (`BuildAnalysis`), reusing R30's building blocks (`StatementTrade`, `TradeStats`, `equityOf`, `equityTotals`, `Drawdown`, `FormatMoney`, `FormatPercent`, decision labels; `statementSummary`'s equity sentence moved into the shared `equitySentence`, wording unchanged). Port `ports.AnalysisRenderer` (+ `fakes.AnalysisRenderer`), `app.Deps.Analysis`; app `internal/app/analysis.go` (`Analysis`, `AnalysisPDF`); adapter `internal/adapters/pdf/analysis.go` (`RenderAnalysis`, `AnalysisPageHeight`); HTTP `handlers_analysis.go`; `cmd` wires `pdf.New()`. Contract first (`make gen`): `GET /api/v1/analysis` and `GET /api/v1/analysis.pdf` (`preset`, `from`, `to`, `accountId` repeated), schemas `Analysis*`. No schema change, no migration, no new dependency, no eToro path, no money guardrail touched. Decisions: **R31-a** no LLM: the narrative (Overview, What went well, What went badly, Costs, Risk & discipline, Activity) is assembled from the analysis's own figures in neutral words and ends "Past results say nothing about future results."; a test checks every amount and percentage in it is a figure of a table and that it holds no banned or predictive wording. **R31-b** periods are whole risk days (22:00 UTC); *this week* = Monday to Friday of the ISO week (as R30's weekly statement), *this/last month* = calendar months, *Everything* = the last 400 risk days (equity and decisions are kept 400 days), *custom* = first to last risk day inclusive, not after today, at most 400 days back; no preset = this month, or custom when a range is given. Date buckets: one per risk day up to 31 days (Saturday and Sunday risk days only when a trade closed on them), one per ISO week (`2026-W48`, "w/c 23 Nov") up to 186 days, one per calendar month beyond; empty buckets are kept in the chart, tables list buckets with trades. **R31-c** a trade's theme is its share's theme **at analysis time**: the first of its account's themes holding the share, by name (case-insensitive, then id), as the watchlist shows it (R27-a); else "No theme". Themes are grouped by name across accounts. Playbook = the `Signal.Playbook` of the account's decision whose `OrderRequestID` equals the trade's `OpenRequestID` (decisions are read from 3 days before the period), else "Not recorded". **R31-d** PDF page size rule: always one page, 210 mm wide (A4 width, portrait); 297 mm tall when the content plus a 14 mm footer fits, otherwise the content plus footer rounded up to the next 10 mm. The analysis is laid out once on a 5 000 mm measuring page and again on the chosen page (deterministic, same bytes for the same analysis); auto page breaks are off; each grouping lists at most 40 rows in the PDF ("… and N more (see the Analysis page)"), which bounds the height (the renderer refuses a page over 5 000 mm). A one-day, one-account analysis is A4; the committed sample (a month on four accounts in USD, GBP and AUD) is 210 × 390 mm. Layout: header with the vector logo, period and scope; 10 KPI tiles; the narrative with run-in headings; a three-column chart grid (P&L by bucket, one equity line per currency with its drawdown, a win/loss/breakeven ring); the groupings in two columns with an inline P&L bar per row; recorded equity beside risk & activity; notes; footer "software, not financial advice — trading can lose money". Colours: Okabe–Ito blue for gains, vermillion for losses, grey breakeven; every bar also has its figure as text. Text in Helvetica / Windows-1252 as R30 (£, €, ¥, A$; other characters print "?"). The PDF's group tables show trades, W/L/B, win rate and P&L; fees, averages, best and worst per group are on the page and in the JSON, their totals in the PDF's tiles. **R31-e** who: viewer permission, view access to every chosen account; an account the caller cannot see is 404 (also for an admin: accounts come from `Accounts`, i.e. owned or shared); at most 50 `accountId`; API keys may read both (no recipients or secrets involved, unlike R30). View-only platforms add equity snapshots only (no holdings are read: an analysis is of recorded history). Trade figures are cactusTrade's own closed trades in US dollars (OD15), other trades counted apart with cactusTrade's share of all closed trades (OD1); equity per currency, never added across currencies; activity counts read at most 20 000 decisions/events per account (beyond: "lower bounds"). **R31-f** rate classes: `GET /api/v1/analysis.pdf` is an export (10/min); the JSON is a read. Tests: domain `TestBucketFor`, `TestAnalysisWindowFor` (every preset, after 22:00 UTC, January's last month, custom limits and errors, titles, file name), `TestBuildAnalysisGroupings` (each dimension, theme first-by-name and "No theme", playbook join incl. a decision before the period, empty date buckets, no win rate without trades), `TestAnalysisDateBuckets` (weekly/monthly keys, weekend day with a trade), `TestAnalysisEquityPerCurrency` (USD and GBP never added; slot sums; drawdown; ≤ 240 points), `TestAnalysisActivityAndRisk`, `TestBuildAnalysisOrderIndependent`, `TestAnalysisNarrative` (headings, figures, wording guard, traceability), `TestAnalysisEmpty`; app `TestAnalysisFromStoredRecords` (stored records → groupings, PDF through the renderer, no renderer = unavailable), `TestAnalysisAccess` (access matrix: another user's account 404, viewer on a shared account, API key, too many accounts, bad ranges); HTTP `TestAnalysisEndpoints` (JSON, repeated `accountId`, absent win rate/average/share, 404/400, PDF headers `application/pdf` + attachment + `no-store`, API key, rate classes); pdf `TestGoldenAnalysis` (1 page, size = the rule, key figures, deterministic bytes, committed sample `internal/adapters/pdf/testdata/analysis-month.pdf`, `-update` rewrites it), `TestAnalysisShortPeriodIsA4`, `TestAnalysisPageGrows` (more themes → taller single page, whole 10 mm, row cap shown), `TestAnalysisEmptyAndCancelled`; Vitest `Analysis.test.tsx` (figures, narrative, charts, tables, presets, account chips → repeated `accountId`, PDF link, custom range, sorting, empty state, refused request) and the menu item in `Layout.test.tsx` (and `/analysis` in the real-money banner's route coverage, `RealBanner.test.tsx`). | done (2026-11-24; `make check` green incl. MariaDB test-int and `make audit`; not committed) |
| R32 | **Orders: direction, filters, grouping, plain wording** (owner, 2026-11-25: "on the dashboard page i want to see the different orders e.g. Long and Short … filter by any of the headings on the order page e.g. Direction … group or filter by"; "make it more obvious what is meant by state accepted and reason cancel accepted … reason should be more explicit and i want to see the latest at the top"). Dashboard "Orders and fills": a Long/Short badge per line, All / Long / Short buttons with counts, identical requests in a row folded into one line ("×203", with the time span). Orders page: a filter under every heading (a dropdown of the values present for kind, share, direction, state, sends, reason; a contains-search for the rest), a search over every column, group by kind, share, direction, state, reason or day (collapsible, with a count, the opens' amount and long/short per group), sort by any heading; the latest order at the top by default; the latest 500 orders. Server wording (`app.OrderView`): a cancel eToro took reads **"accepted by eToro"**; `ReasonLabel` says what was asked, why ("the order was still not filled after 1 minute (cactusTrade cancels an entry order eToro has not filled by then, so its cash is not held up)", or the kill switch, the session end) and that an accepted cancel does not mean the order was cancelled. Stored reasons are unchanged (old rows are reworded on read). Filtering and grouping are presentation only (`web/src/lib/tableView.ts`). | `internal/app/agent_view.go`, `internal/adapters/http/handlers_agent.go`, contract, `web/src/pages/Orders.tsx`, `web/src/components/{LiveCards,DirectionBadge}.tsx`, `web/src/lib/tableView.ts` | `TestOrderLabels`, `TestOrderReasonLabel`, `TestOrderLabelsInTheContract`; Vitest `Orders.test.tsx` (newest first, sort, filter, search, group/fold), `OrderFeed.test.tsx` (direction, long/short only, folding) | done |
| R33 | **Shorts were never possible: the short side of eligibility was not stored** (owner, 2026-11-25: "what do i need to do to get the agent to short stock, can you get the agent to start shorting as well"). Found: of 113 short candidates on env-demo, 78 were refused by the risk engine as "not confirmed shortable as a CFD at leverage 1" with no reason. eToro says cfd/short/×1 is offered (probe 2.7), the adapter read it, but `instruments` had no columns for it, so every cached eligibility read back `ShortCFD=false`. Fix: migration 00014 (both dialects) adds `eligible_short_cfd`, `short_eligibility_reason`, `min_short_cents` and marks every cached check unchecked so the next scan asks eToro again; `sqlstore` reads and writes both sides. Nothing in the risk rules changed (25 % short cap, stop above entry, flat before the close). Other refusals seen (unchanged, by design): stale prices, a stop already passed, sizes below eToro's $10 minimum (the account's $500 budget with 1 % daily-loss and 1 % drawdown limits leaves very small sizes), and the reviewer rejecting shorts against an up-trend. | `migrations/{sqlite,mariadb}/00014_short_eligibility.sql`, `internal/adapters/sqlstore/market.go`, storetest | shared store suite on SQLite and MariaDB: both sides round-trip (fails on the old schema) | done |
| R34 | **"Under review" by exchange** (owner, 2026-11-26: "change the under review so that it includes tabs for each exchange e.g. when i click on a exchange i want to see what's under review for that exchange"). Each review carries its exchange (`market`, and `exchange` worded by the server via `domain.Market.Label`, e.g. "New York (NYSE/NASDAQ)"), looked up from the cached instrument when the board is read (not stored; uncached = "Other"). The board has tabs All / London (LSE) / New York (NYSE/NASDAQ) / Sydney (ASX) / Other, only for exchanges present, each with its count and how many have a signal; the chosen tab is remembered per browser; an Exchange column; the board reads the latest 200 reviews (was 100) so smaller exchanges are not crowded out. | `internal/domain/{trading,research}.go`, `internal/app/scanner.go`, `handlers_research.go`, contract, `web/src/components/ReviewBoard.tsx` | `TestMarketLabel`, `TestScanner` (market on the review); Vitest `ReviewBoard.test.tsx` (tabs, order, counts, filtering, remembered tab, fallback to All) | done |
| R35 | **Analysis tiles drill into their details** (owner, 2026-11-26: "on the analysis page when i click on any of the tiles i need to be able to drill into the details"). The analysis now lists the period's closed trades behind the figures (`domain.AnalysisTradeRow`, newest first, at most 2,000, `tradesCapped`): account, share, exchange, theme, playbook, direction, opened/closed, rates, invested, fees (unknown kept apart), realised P&L, result, and whether cactusTrade opened it (the others are listed, flagged and never counted). Each tile is a button (keyboard too) that opens a details panel under the tiles: Closed trades → the trade table; Win rate → wins/losses/rate per exchange, account, theme, playbook, direction or day, and the trades grouped by result; Realised P&L → before fees / fees / P&L per group, trades best first; Average → average, best and worst per group; Fees → fees per group, trades by fees; Largest drawdown → equity charts and the equity table; Opened by cactusTrade → all trades grouped by who opened them; LLM cost → calls, failures, tokens, known and unknown cost, links to the LLM reviewer page (prices) and the Decision log. The trade table filters by any column, searches, groups (collapsible, with counts and P&L) and sorts. | `internal/domain/analysis.go`, `handlers_analysis.go`, contract, `web/src/components/AnalysisDrill.tsx`, `web/src/pages/Analysis.tsx` | `TestAnalysisTradeRows` (order, fields, foreign flagged and not counted, cap); Vitest `Analysis.test.tsx` (each tile's panel, breakdown switch, filter, best-first, close) | done |
| R36 | **LSE entries on; foreign cost lines converted with eToro's own rate** (owner, 2026-11-27: "enable LSE" and "convert the fee using etoro own aud/usd"). Every ASX entry was refused because eToro lists the overnight fee in AUD (LSE: GBX), and the cost gate (OD8) took only USD. `domain.CostCheck` now takes `FXRates` (USD micros per unit): AUD via eToro's AUDUSD, GBP via GBPUSD, GBX = GBPUSD ÷ 100 (`domain.FXPair`), at the **ask** (never understates a cost) and rounded up; the rate must be no older than `app.FXMaxAge` (15 min; live, eToro stamped GBPUSD a minute back, so the account's seconds-long price limit refused every conversion; the trade's own price keeps that limit) and not delayed (`domain.FXRateFromQuote`). The pair's instrument is found once by an exact symbol match in eToro's instrument search (allowlisted market data; cached in memory). Any other currency, a missing pair, a failed lookup or a stale rate = refusal (fail closed). The decision trace names each conversion. `domain.EntriesAllowed` allows LSE (OD9 settled: prices, candles and stops are all GBX). | `internal/domain/fx.go`, `agent.go`, `internal/app/fx.go`, `agent.go` | `TestCostCheck` (AUD/GBX converted, rounding, no rate, unconverted currency), `TestFXPair`, `TestFXRateFromQuote`, `TestToUSDMicros`, `TestEntriesAllowed`; `TestAgentConvertsForeignCosts` (LSE entry with GBX + AUD lines, search cached, stale rate refused), `TestAgentTickRefusals` (AUD with no pair, EUR) | done |
| R37 | **Orders show the full date and their exchange** (owner, 2026-11-27: "add the full date e.g. 26/11/2026 20:50 then add the exchange of the order"). The contract's `Order` gains `market` and `exchange` (required; from the cached instrument, worded by `Market.Label()`, empty when unknown), filled for the orders list, the live snapshot and live order messages (the stream keeps each instrument's symbol and exchange from the latest snapshot; live regimes carry the market). The Dashboard's Orders and fills card and the Orders page write times as `dd/mm/yyyy hh:mm` (local, `web/src/lib/format.ts`); the card shows an exchange badge, the Orders page an Exchange column to filter and group by. The Dashboard's "How the live view works" note now sits above Market mood. | contract, `internal/app/agent_view.go`, `live.go`, `history.go`, `handlers_agent.go`, `handlers_live.go`, `web/src/lib/format.ts`, `LiveCards.tsx`, `Orders.tsx`, `Dashboard.tsx` | `TestOrdersView` / `TestLiveSnapshot` (market NYSE); Vitest `format.test.ts`, `OrderFeed.test.tsx` (date and exchange), `Orders.test.tsx` (exchange column, filter and group) | done |
| R38 | **No money without the owner; trade minimums checked and proposed, the owner approves** (owner, 2026-11-27: "make sure that the system cannot add money to the account without approval from the user. after adding a new trading platform i want to know what the minimum trade values are for long and shorts … then adjust the risk values to accommodate for the minimum values - then the user must approve the new minimum values"). (1) cactusTrade never moves money: the eToro client has no money/transfer path (`allowlist_test.go`); the trading budget, the money the agent may use, is written only by `SetRiskLimits` (owner, web session, agent stopped, at most the equity, audited) — `TestOnlySetRiskLimitsWritesTheLimits` fails the build if anything else writes the limits; presets keep the budget, API keys and the LLM cannot change limits. (2) `domain.MinimumsFrom`: the platform's smallest long (real, x1) and short (CFD, x1) from eToro's eligibility of the watched shares (highest minimum, so every share can trade at it; lowest shown). (3) `domain.FitMinimums`: what the limits allow now (largest long = min(budget, max position × equity); largest short = short allowance × budget) and the smallest changes that make one minimum trade fit — position % first, short allowance up to the hard 25 %, the budget only when no percentage can, never above the equity, refused when the account's mode does not allow it (real ceilings). (4) `app.CheckTradeMinimums` (owner; also run when an eToro account gets new keys) stores the review (`risk.minimums.<account>`) and raises a warning event; it changes nothing. `DecideTradeMinimums` approves through `SetRiskLimits` (all its rules; refused when the limits changed since the check, or decided) or declines; audited. (5) Risk limits page: a Trade minimums card (minimums, room, proposal, Approve/Decline, "Check again"). | `internal/domain/minimums.go`, `internal/app/minimums.go`, `accounts.go`, `money_guard_test.go`, `handlers_minimums.go`, contract (`/risk/minimums`, `/check`, `/{reviewId}/decision`), `web/src/components/TradeMinimums.tsx`, `RiskLimits.tsx` | `TestMinimumsFrom`, `TestFitMinimums`; `TestTradeMinimumsApprove`, `TestTradeMinimumsRefusals` (decline, stale, agent running, not owner, API key, nothing checked), `TestTradeMinimumsAfterKeys`, `TestOnlySetRiskLimitsWritesTheLimits`; `TestTradeMinimumsEndpoints`; Vitest `TradeMinimums.test.tsx` | done |
| R39 | **Dashboard: a live graph of the investments on top; the activity log filters long and/or short** (owner, 2026-11-27: "on the Dashboard page i want a live graph of my investments. this must be at the top of the dashboard. add a filter to the [Agent activity] here i can filter on long and/or short orders"). `LiveInvestChart` (first card of the live dashboard): one point per live snapshot (every 5 s) of what the open positions are worth (amount + open P&L) long and short, the open P&L and equity (today's stored equity snapshots seed the equity line); views Invested / Open P&L / Equity; inline SVG, theme colours; points kept in the tab (sessionStorage, capped). Events carry the direction of the order or position they are about (`domain.Event.Direction`, migration 00015 in both dialects, contract `Event.direction`; every order/position event via `orderEvent`), decisions already did; the Agent activity card has Long / Short toggles (both on = everything; one on = only that side's ideas, orders and positions; the last side on cannot be switched off). | migrations `00015_event_direction.sql`, `internal/domain/session.go`, `internal/app/events.go`, `executor.go`, `reconcile.go`, `sqlstore/market.go`, contract, `handlers_system.go`, `web/src/components/LiveInvestChart.tsx`, `ActivityLog.tsx`, `lib/activity.ts`, `Dashboard.tsx` | storetest events (direction round-trip, both databases), `TestAgentConvertsForeignCosts` (order events are long); Vitest `LiveInvestChart.test.tsx` (sums, points, drawing), `ActivityLog.test.tsx` (long/short filter) | done |
| R40 | **Scheduled database backups into a folder the owner chooses** (owner, 2026-11-27: "add database backup schedule and give the user the ability to define the backup folder"; owner's choice: deploy.sh + the machine's scheduler, not the web UI, because the container can only write to folders mounted at deploy time). `deploy.sh --backup-schedule "daily HH:MM" \| "hourly" \| "every Nh" \| off` installs a launchd job (macOS, `~/Library/LaunchAgents/com.cactustrade.backup.plist`) or one marked crontab line (Linux) that runs `deploy.sh --backup-now --<target>` (age-encrypted as before, OD29); `--backup-dir DIR` (any folder) and `--backup-keep N` (newest N kept; 0 = all) are saved in `.env.local` with the schedule; every `--backup-now` (scheduled, manual, before an upgrade) then prunes only `cactustrade-<time>.sql.gz[.age]` beyond N; `--backup-prune` does it now; `--status` shows schedule, folder, N and the newest backup. Refused over `--ssh-access` (run it on the machine itself). | `deploy.sh`, `README.md` | `TestBackupScheduleLaunchd`, `TestBackupScheduleCron`, `TestBackupScheduleRefusals`, `TestBackupPrune` (fake launchctl/crontab, throwaway HOME and .env.local) | done |
| R41 | **Real trading: the account's admin sets the demo days needed** (D36; owner, 2026-11-27). See D36. The Real trading page shows a "Qualifying demo days needed before arming" field (5–60, default 20) to the admin who owns the account; the checklist and the evidence line follow the account's number. | `internal/domain/realgate.go`, `internal/app/realgate.go`, `handlers_realgate.go`, contract, `web/src/pages/RealTrading.tsx`, `api.ts`, `CLAUDE.md` §2 | `TestEvidenceRuleWithMinDays`; `TestSetDemoDaysRequired` (range, who, API key, demo account, audit, checklist follows), `TestRealGateAutoDisarm` (a change disarms); `TestRealGateEndpoints` (status fields, CSRF, range 422, PUT); Vitest `RealTrading.test.tsx` (editor, range, hidden for others) | done |
| R42 | **Real trading: demo days 1–60, default 5** (D37; owner, 2026-11-27). See D37. | `internal/domain/realgate.go`, `handlers_realgate.go`, contract, `RealTrading.tsx`, `CLAUDE.md` §2 | `TestEvidenceRuleWithMinDays` (1 and 5 accepted, 0 refused); `TestSetDemoDaysRequired` (default 5, set 2); `TestRealGateEndpoints` (`minDaysFloor` 1, `defaultMinDays` 5, 0 → 422); Vitest (range text) | done |
| R43 | **Dashboard graph: Day, Week, Month, Year, Max** (owner, 2026-11-27: "i want to be able to see day, week month, year and max data"). Day is the live 5-second points (R39); Week/Month/Year/Max (7/30/365/400 days) plot the stored equity snapshots (every 15 min, kept 400 days) from `GET /overview/history?days=N`: Equity, and Invested as equity − cash (the long/short split and the open P&L are not stored, so Open P&L is Day only). Long series are thinned to 600 points; the axis shows dates. Presentation only. | `web/src/components/LiveInvestChart.tsx`, `web/src/lib/format.ts` | Vitest `LiveInvestChart.test.tsx` (ranges fetch 7/400 days, P&L off, dates on the axis, `thin`), `format.test.ts` (`dayDate`) | done |
| R44 | **Account tiles show the figures** (owner, 2026-11-27: "add details to the tiles e.g. Balance, No of Shorts, No of Longs, Holding shares and holding share value, holdin shorts and holding short values, P&L values"). Each tile on the Accounts page shows, from the overview (every platform): balance (cash, equity), shares held long (count, value, P&L), shorts held (count, value, P&L), open and (when reported) realised P&L, in the account's currency, with the time. The server counts and sums by side (`domain.Holdings.Summary`, contract `Holdings.summary`); a position without a reported value is counted, not summed, and the tile says so. | `internal/domain/holdings.go`, `handlers_overview.go`, contract `HoldingsSummary`, `web/src/components/AccountFigures.tsx`, `Accounts.tsx` | `TestHoldingsSummary`; overview endpoint test (summary); Vitest `AccountFigures.test.tsx` | done |
| R45 | **Trade Trading 212 accounts** (owner, 2026-11-27: "allow to start trading with Trading 212 accounts"). Researched 2026-11-27 (docs.trading212.com, API v0 beta): orders exist (`POST /equity/orders/market|limit|stop|stop_limit`, `DELETE /equity/orders/{id}`, scope `orders:execute`), Invest and Stocks ISA only. **Blocked on CLAUDE.md §2: the API cannot meet the guardrails as eToro does, so the owner must agree the rules in writing first.** (a) §2.7 idempotency: no client order id; the docs say a repeated request may duplicate an order. Proposed: an order is sent once and never retried; a lost answer stops new orders until it is matched (ticker, quantity, time) in `GET /equity/orders` / `history/orders`. (b) §2.3 order shapes: no shorts/CFD on T212, so long only (buy market, quantity in shares, leverage 1); the 25 % short cap does not apply. (c) Stop-loss: a market order carries none; proposed a separate sell stop order placed right after the fill (not linked by T212: it has to be cancelled before the close sell), and no entry while it cannot be placed. (d) No market data in the API: signals from eToro's candles of the same instrument (matched by ISIN), or no T212 trading. (e) Close = sell the held quantity at market; cancel can fail mid-fill. (f) Minimum order value / fractional rules are not documented (R38 cannot check them). (g) The owner's T212 account is real money, so even when built it stays behind the real-money gate (phase 7); a T212 practice (demo) key is needed to trade on demo; whether demo accepts orders is not documented. (h) The T212 API Terms PDF must be read for any limit on automated trading. | `internal/adapters/trading212` (new order allowlist), `domain` order shapes, `app` | — | blocked (owner's written rules a–h) |
| R46 | **Import watchlists from the trading platforms** (owner, 2026-11-27; D38). Only eToro has watchlists in its API. Watchlist page → cloud button → pick one of the account's eToro watchlists (the default first) → Import: every share takes the hand-add path (`checkAndAddToWatchlist`: eToro instrument, LSE/NYSE/NASDAQ/ASX, eligibility at leverage 1); refused ones are listed; people on the list are left out; at most 200 per import (a note says so); a repeat import changes nothing. Trade access, web session only; audited `watchlist.import_platform`, event `watchlist.imported`. Nothing is written to eToro. Port `ports.PlatformWatchlists`; contract `GET /watchlist/platform`, `POST /watchlist/platform/{id}/import`. | `internal/adapters/etoro/watchlists.go`, `internal/app/platform_watchlists.go`, `handlers_watchlist.go`, contract, `web/src/pages/Watchlist.tsx` | `TestWatchlistReadOnly`, `TestWatchlistsReadOnly` (one GET, query, people counted, duplicates dropped); `TestImportPlatformWatchlist` (check path, refusals, repeat, API key, viewer, eToro down, audit), `TestImportPlatformWatchlistCap`; `TestPlatformWatchlistEndpoints` (CSRF, 404); Vitest `Watchlist.test.tsx` (pick, import, result) | done |
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
- **OD9 – LSE entries (owner: none until checked):** off until Q1/U4 (GBX vs GBP stop units) is verified in 4.17; NYSE and ASX on, with the stop checked after every fill. Needed by 4.10. **Settled 2026-11-27 (owner: "enable LSE")**: Q1 showed pence end to end, LSE entries are on (R36).
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
| 4.17 | **Live demo verification, run by the owner.** One minimal long and one short on an NYSE share in session. Record sanitised fixtures (open, lookup pending/filled, close, close-orders). Settle U1–U11. Kill switch with 2 positions. Restart mid-position → reconciliation adopts it. Never real. | `testdata/`, `docs/etoro-api.md` | Fixture tests replaced by recordings and green | partly settled (2026-11-24, "Live findings 2026-11-24": first live opens; lookup by `orderId`, amount fields, closed executions; U2/U8/U11 partly settled). U4 settled (LSE rates and stops in pence, 2.7) and U6 observed (`isPotential` false on every offered config of BARC.L, AAPL, CBA.ASX; still counted as "not offered" when true). **2026-11-27 (owner's run, decisions/orders/events/audit in the demo database):** shorts done (7 sellShort/cfd opens, e.g. AAPL 14:30, SGE.L 15:16, all closed by stop, target or the kill); LSE entries live (REL.L long, SGE.L short; GBX cost lines converted, R36). **Kill drill passed:** manual kill 15:40:37 UTC with 4 positions open (REL.L, INGM, SGE.L short, VSH): 4 close requests 15:40:37.9–38.3, all 4 closed by 15:40:45 (~8 s), no entry until the reset 15:43:28 and the start 15:43:30. **Restart mid-position passed:** deploy of build 79 at 16:37 UTC with 4 positions open (IBM, ONTO, SMTC, VSH): reconciled at 16:37:08, all 4 kept as cactusTrade's, the agent resumed running. Still open: close/close-order fixture recordings, U1/U3/U5/U7/U9/U10 |

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

## Phase 5 — LLM reviewer and decision log — `done`

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

- **review-B2-owner (owner: adopt with alert, 2026-11-24).** Reconciliation keeps adopting an
  unrecorded position that one of our own lost, abandoned or unrecorded orders explains
  (`domain.AdoptUnrecorded`), and every adoption raises the `position.adopted` alert event.

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

### Live findings 2026-11-24 (demo account, first live orders) — `done` (2026-11-24; `make check` green incl. MariaDB test-int)

The agent's first two demo opens (GOOG and AAPL, $100 each; eToro order ids 396359836 and
396377388) were filled by eToro within 0.4 s. `orders:lookup?referenceId=<our x-request-id>`
answered **404** before and after the fill; `orders:lookup?orderId=` works (recorded,
sanitised: `internal/adapters/etoro/testdata/recorded/lookup_filled_by_order_id.json`).
Consequences: the rows stayed `pending`, no agent position was recorded, so the positions
counted as foreign (OD1) and would not have been flattened (eToro's take-profit closed them);
the supervisor sent a cancel every minute (151 rows, each "accepted" by eToro); then every
pass failed with "pending → abandoned: illegal order transition" (review-c forbids it, but
`settleOrder` still tried). The fill's `investedAmountCurrency` was 1 for $100. Each fix has
a test that failed before it (`internal/app/live_findings_test.go`,
`internal/adapters/etoro/recorded_test.go`, `internal/ports/fakes/simbroker_risks_test.go`).
Facts recorded in `docs/etoro-api.md` ("Live findings", U2/U8/U11).

| ID | Fix | Test |
|---|---|---|
| LF1 | **Lookup by eToro's order id.** `ports.Broker.Lookup(ctx, requestID, orderID)`: the eToro adapter asks `orderId=` when orderID > 0, else `referenceId=`, and refuses an answer about another order id. Every caller passes the stored `EtoroOrderID` (executor `sendOpen`/`resolveOpen`, reconciliation `settleOrder`, which the supervisor uses); only an open whose answer was lost or rate limited has none. `LookupRaw` is gone: `cactustrade etoro-lookup` (`--order-id` or `--request-id`) goes through `LookupWithRaw` (the same parsing) and prints the parsed status plus eToro's raw answer. | `TestLookupRecordedFilledByOrderID`, `TestLookupWrongOrder`, `TestLookupWithRaw`, `TestExecutorLooksUpByOrderID`, `TestPrintLookup` |
| LF2 | **Amount and closed executions.** An execution's amount is `initialExposureAccountCurrency`, else (0 or absent) `marginAccountCurrency`, else `investedAmountCurrency`. `settlementType` "REAL" is read in any case. New `domain.PositionExecution.Closed` (`state: closed`, any case): `recordFills` records the position as ours and settles it closed at once (`position.closed`), never sends it a close; a non-conforming closed fill still blocks the instrument and raises the danger alert ("already closed by eToro"). | `TestLookupAmountFields`, `TestExecutorClosedExecutions` |
| LF3 | **No cancel storm.** The supervisor looks an order up first, then cancels only when `cancelBlocked` allows it, from the persisted cancels for that eToro order id (`cancelHistory`: `Orders.ListSince` from the oldest open order, so no 200-row window): never while one is in flight or **was accepted** (an accepted cancel is never repeated: eToro's lookup must say cancelled or filled); refused/abandoned ones at most `maxCancels` = 3 in all, a minute apart. The executor's 60 s cancel uses the same check. An accepted cancel still unconfirmed after a minute raises **one** `order.cancel_unconfirmed` warning (whether the lookups say pending or keep failing); the cap raises one `order.cancel_refused`. | `TestLiveOneCancelPerOrder` (10 minutes pending → exactly 1 cancel; also while killed; refused → 3) |
| LF4 | **Stuck rows resolve; no endless illegal transition.** With LF1 the stuck rows resolve on the next supervisor pass (lookup by order id → filled with a closed execution → agent position recorded and closed → order filled). An accepted (pending/partial) order whose lookups keep answering 404 is held — in `settleOrder` and in the executor's `resolveOpen` — with **one** `order.unresolved` warning, its cash reserved and its instrument blocked; it is not a reconciliation problem (in flight, D24). | `TestLiveStuckOrderResolvesByOrderID`, `TestLiveUnresolvablePendingOrderHolds`, `TestExecutorHoldsAnAcceptedOrderItCannotFind` |
| LF5 | **Wording.** Contract `Order.kindLabel` and `Order.stateLabel` (required), computed in the app (`app.OrderView.KindLabel/StateLabel`): "Buy GOOG", "Short AAPL", "Cancel request for GOOG (buy order 396359836)", "Close request for GOOG (position 3649965576)"; an open reads filled / partially filled, a cancel eToro took "accepted", a close "done" ("partly done"), a refused cancel or close "refused". `make gen`; the orders feed (`LiveCards`) and the Orders page show the labels as they are (no wording logic in React); the badge colour still follows `state`. | `TestOrderLabels`, `TestOrderLabelsInTheContract`, `Orders.test.tsx`, `Dashboard.test.tsx` |
| LF6 | **Simulator.** `fakes.SimBroker.RefLookupNotFound` (by request id always 404; by order id found; counter `RefNotFound`) and `IgnoreCancels` (200, nothing happens). 4.12 arms `RefLookupNotFound` in a third of the scenarios and fails if no lookup by request id was ever answered 404; every invariant unchanged. A planted regression (lookups by request id only) fails 4.12 ("position … at the broker is not recorded as ours"). | `TestSimBrokerRefLookupNotFound`, `TestSimBrokerIgnoredCancels`, `TestAgentSequenceProperties` |

Decisions made while fixing (recorded here):

- **LF-a An accepted cancel is never repeated** for the same eToro order, whatever the lookup
  says afterwards, until the order is final; the owner gets one warning and the order's cash
  stays reserved (fail closed). Reading "until the lookup changes" as "until it says cancelled
  or filled" (the only changes that end the wait). A cancel eToro refused (4xx) or that was
  abandoned unconfirmed may be retried, at most 3 in all, a minute apart.
- **LF-b One warning per order per process** (`runtimeState.warned`, in memory): after a
  restart a still-stuck order warns once more. No migration.
- **LF-c An accepted order that cannot be found is held, not unreconciled:** like a young
  order in flight (4.8-a), it reserves its cash and blocks its instrument; reconciliation stays
  OK so exits and other entries go on. With LF1 this needs eToro to answer 404 by order id,
  which was not seen live.
- **LF-d 4.12 and the lookup by request id:** in the scenarios that play
  `RefLookupNotFound`, the outcomes "lost answer" and "429 that was processed" are not drawn:
  such an open has no eToro order id, so its fill is found only by reconciliation's adoption
  (review B2, within `ReconcileEvery`), after the tick that checks every broker position is
  ours (same reasoning as review-d). `TestExecutorLooksUpByOrderID` covers that path
  (abandoned after 2 min, then adopted). **Open risk for the owner:** with eToro's live
  behaviour, a lost open answer leaves its fill unrecorded for up to 5 minutes (until the next
  reconciliation adopts it); its cash shows as spent in the portfolio once eToro lists it.
- **LF-e Labels in the app, not the domain:** they are presentation for the API (no decision
  reads them); `OrderView` methods, so the REST list and the live stream word orders the same.
- The stored state values are unchanged (a cancel eToro took is still `filled` in the
  database and the CSV export); only the new labels change.

Not changed: no migration, no new dependency, no eToro path (allowlist untouched), no
guardrail loosened.

### Console tooling for 2.7 and 3.6 (2026-11-24) — `done` (the owner's runs are pending)

Two read-only console commands so the owner's stored demo keys can settle 2.7 and run 3.6.
Both run inside the deployed container, e.g. `docker exec cactustrade-app-1 /cactustrade …`.

| ID | What | Test |
|---|---|---|
| CT1 | **`cactustrade etoro-probe --account ID --symbol SYM [--raw] [--record DIR]`.** Search (exact symbol), instrument metadata, rates, the last 3 one-minute and one-day candles, eligibility, and the costs of a $100 long (and of a $100 short when eToro offers a CFD short at x1). Prints the parsed instrument (id, symbol, market, exchange id, every field of eToro's answer whose name mentions "currency" — the spec's instrument has none), the quote as read and the bid/ask numbers exactly as sent, every `leverageConfigs` entry with all its fields (incl. `isPotential`), and for an LSE share a Q1 hint (≈100× the GBP price means GBX). `--raw` prints each answer, `--record DIR` writes each answer as `<symbol>_<step>.json` (0600, folder 0700); both **sanitised**. A failed step is reported and the others still run; the exit status is non-zero. The probe reaches eToro only through `probeReader` (Search, Instruments, Quotes, Candles, Eligibility, Costs) — no method that places, closes or cancels exists on it. | `TestProbeReaderIsReadOnly` (method sets pinned to the reads; each op allowlisted and non-mutating), `TestProbe`, `TestProbeFailuresAndShort`, `TestRecordName` |
| CT2 | **`cactustrade backtest --account ID`, `--watchlist`, `--json`.** The backtest reads with that account's stored keys (default unchanged: the price stream's demo account). `--watchlist --account ID` backtests every share on the account's watchlist, each in its own market's sessions (market from the cache, else the symbol) and each from the same `--equity`; non-shares and unsupported markets are skipped, a failing share is reported and the rest still run. The text output is each share's `Report.String()` and an aggregate; `--json` writes only a JSON summary to stdout (per-share figures, verdict and the text report, plus the aggregate). Same `strategy.Backtest`, same costs rule, nothing tuned. New `strategy.Report.Verdict()` gives the verdict class the report already worded (`String` uses it; wording unchanged). | `TestRunBacktests`, `TestAggregateBacktests`, `TestReportVerdict`, `TestBacktestCommandValidates` (unchanged), `TestResearchCommandsRefuseBeforeAnyRequest` |
| CT3 | **Sanitiser** (`cmd/cactustrade/sanitize.go`). Personal keys (`accountId`, `gcid`, `cid`, any key ending in `cid`, `username`, `userId`, names, e-mail, phone…; any case, `_`/`-` ignored, at any depth) → 0 for a number, else `"redacted"`; token-looking keys (`token`, `apiKey`, `userKey`, `secret`, `password`, `authorization`…) → `"redacted"`; token-looking string values (a JWT, or ≥ 32 base64/hex characters that are not a UUID) → `"redacted"`. Numbers keep eToro's exact text (`json.Number`). An answer that is not one JSON value is an error and is never printed or written. | `TestSanitizeJSON`, `TestProbe` (no account id, gcid, user name or token reaches stdout or a file) |
| CT4 | **Adapter hook** `etoro.Config.RecordAnswer(op, body)`: called with a copy of each 2xx answer to a **read** (in `attempt`, and only when `!ep.Mutates`); account changes go through `mutate` and never reach it; refused answers are not passed. Never sees a key or header. | `TestRecordAnswerReadsOnly` (rates recorded as received; a 200 cancel and a 401 read are not) |

Decisions (recorded here):

- **CT-a Demo accounts only.** Both commands refuse a real-money account, a view-only
  platform account (Trading 212, Saxo, Tiger) or an account without both keys before any
  request (`researchAccount`, `cmd/cactustrade/research.go`; `ErrRealModeGated` for real).
  The brief's research rule (demo keys) is kept; reading with real keys would be a 7.18+ change.
  `TestRealRefusalModeReferences` lists the two new mode references (the backtest's moved there).
- **CT-b `--raw` prints sanitised answers too**, so nothing personal reaches a terminal that
  may be pasted elsewhere; prices and every market field are untouched.
- **CT-c `--json` is JSON only on stdout**; logs of both commands go to stderr (before, the
  backtest logged JSON lines to stdout). The per-share text reports are inside the JSON.
- **CT-d The watchlist aggregate** sums independent runs (each share from the same starting
  equity): counts per verdict, trades, costs paid, the net result in USD summed over the
  tested shares and the mean return vs the mean buy-and-hold. Its verdict follows the report's
  order: nothing tested, no trades, lost money in total, mean below holding, else "positive on
  this data only; a backtest does not predict future results".
- **CT-e Pacing** is the client's limiter (market data 120/min, costs 20/min per key): a large
  watchlist waits rather than hitting 429.
- **CT-f Recordings** are written by the owner's run and copied into
  `internal/adapters/etoro/testdata/recorded/` later (2.7); no fixture changes in this step.

Usage (the owner):

```
docker exec cactustrade-app-1 /cactustrade etoro-probe --account ID --symbol BARC.L --raw
docker exec cactustrade-app-1 /cactustrade etoro-probe --account ID --symbol AAPL --record /data/probe
docker exec cactustrade-app-1 /cactustrade backtest --watchlist --account ID
docker exec cactustrade-app-1 /cactustrade backtest --watchlist --account ID --json > backtest.json
docker exec cactustrade-app-1 /cactustrade backtest --instrument 1001 --account ID --market NYSE
```

Not changed: no migration, no new dependency, no eToro path (allowlist and its test
untouched), no guardrail loosened, no order sent by either command.

**Verification (2026-11-24):** `make check` green: gofumpt, golangci-lint 0 issues, vet,
`go test -race` with coverage **domain 98.1 %, app 89.7 %**, MariaDB `test-int` ok,
govulncheck and `make audit` clean, contracts in sync, `pnpm -C web check` (216 tests). One
earlier run failed once in `Dashboard.test.tsx` ("reconnects with backoff…", a timer
assertion; `web/` untouched by this step); it passed in 3 isolated reruns and the next full
`make check`. Flaky under load: worth a look by the owner.

### Phase 5 steps

| ID | Step | Files | Acceptance | Status |
|---|---|---|---|---|
| 5.1 | Reviewer port and providers: no tools, strict JSON, timeout = reject, invalid JSON = reject, multiplier clamped (R6, R15, R16; bp conversion in 4.2) | `internal/adapters/llm`, `internal/domain/llm.go` | `TestParseVerdict`, `TestOpenAIEmptyAndTimeout`, `TestAnthropic`, `TestClampVerdict` | done |
| 5.2 | **Domain.** `LLMCall` (id, account, purpose agent/scanner/test, profile, provider, model served, prompt, response, input/output tokens, latency, cost micros, −1 = unknown, error, at). `LLMProfile` prices: USD per million input and output tokens as `Micros`, 0 = unknown; `Cost(in, out)` in integers. `Decision`: signal (playbook, direction, entry, stop, take-profit, suggested size, regime probabilities, reason), verdict (kind, bp, reason, call id), risk (allowed, size, reason), outcome (`rejected_reviewer`, `rejected_cost`, `rejected_risk`, `ordered`, `order_failed`), order request id. `Truncate` at 32 KiB, UTF-8 safe. | `internal/domain/{decisions.go,llm.go}` + tests | `go test ./internal/domain -run 'Decision\|LLMCost\|Truncate'`, including a property test that cost is monotonic and cannot overflow (tokens capped) | done |
| 5.3 | **Port and adapter trace.** `Reviewer.Review` returns `(Verdict, LLMCall, error)`, and the call is filled even on error (prompt, latency, error, raw response). Anthropic: `resp.Usage.InputTokens/OutputTokens`, `resp.Model` (the fallback may serve). OpenAI-compatible: `usage.prompt_tokens/completion_tokens`, `model`. Update callers (scanner `llmCheck`, `TestLLM`). | `internal/ports/ports.go`, `internal/adapters/llm/llm.go` (+tests), `internal/app/{scanner.go,llm.go}`, fakes | `go test ./internal/adapters/llm`. For every provider the recorded request body has no `tools`, `tool_choice` or `functions` key; usage is parsed; a timeout returns a trace with an error; invalid JSON is an error with the raw response kept. | done |
| 5.4 | **Migration 00010.** `llm_calls` (id PK, account_id `''` = system, no FK; purpose, profile_id, provider, model, prompt TEXT, response TEXT, tokens, cost_micros, latency_ms, error, at). `decisions` (id PK, account_id FK, at, instrument_id, symbol, direction, playbook, signal fields, probs TEXT JSON, llm_call_id, verdict, size_bp, verdict_reason, risk_allowed, risk_size_cents, risk_reason, outcome, order_request_id). Both dialects; repositories with `List(account, before, outcome, limit)`, `Get`, `DeleteBefore`. | `migrations/{sqlite,mariadb}/00010_decision_log.sql`, `internal/adapters/sqlstore/decisions.go`, `storetest` | `make test && make test-int`, `TestMigrationParity` | done |
| 5.5 | **App decision log.** The agent pipeline (4.10) writes one `Decision` per candidate. It is persisted **before** the order row; a write failure means no order (fail closed). Then it is updated with the request id and outcome. Every LLM call is logged (agent, scanner, test); for agent entries a failed log write = reject. `Decisions`, `Decision` (view + account access), `LLMUsage(days)` (admin). Purge per OD12. | `internal/app/{decisions.go,agent.go,scanner.go,llm.go}` + tests | `go test -race ./internal/app -run Decision`. Every exit path of 4.10 writes exactly one decision with the right outcome; the decision exists before `Open`; store failure → no `Open`; another account's decision → 404; scanner calls logged as `scanner`. | done |
| 5.6 | **Contract and handlers.** `GET /api/v1/decisions?limit&before&outcome`, `GET /api/v1/decisions/{id}` (with the prompt and response), `GET /api/v1/llm/usage?days=` (admin), LLM profile schema with prices. | `api/openapi/cactustrade.yaml`, `internal/adapters/http/handlers_decisions.go` + tests | `make gen && go test ./internal/adapters/http -run 'Decision\|LLMUsage'`, contracts drift check green | done |
| 5.7 | **Web.** *Decision log* page under Trading: time, symbol, direction, playbook, verdict, risk, outcome, order state; outcome filter. A row expands to show signal, regime probabilities, reasons, and the prompt and response folded, with tokens, cost and latency. LLM page: price fields per profile and a usage card. | `web/src/pages/{DecisionLog.tsx,LlmReviewer.tsx}` + tests, `Layout.tsx`, `api.ts` | `pnpm -C web check` | done |
| 5.8 | **Docs and verification.** ARCHITECTURE and PLAN. Owner run: a demo agent with a configured reviewer produces decision rows end to end. | docs | `make check`; owner run blocked on the demo and LLM keys | done (2026-11-24: the owner's demo agent on env-demo with the OpenRouter reviewer logged 19 decisions end to end — 2 `ordered` (GOOG, AAPL: reviewer approve → risk allowed → order filled), 17 `rejected_risk` — and 19 agent LLM calls with tokens, no errors) |

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

### Steps 5.5–5.8: decisions (2026-11-24)

- **5.5-a The tick is on the traced reviewer.** `Deps.LLM ports.Reviewer` is the only reviewer;
  `Deps.Reviewer`, `app.VerdictReviewer` and `app.VerdictOnly` are deleted (and their
  wiring in `cmd/cactustrade`).
- **5.5-b What is a candidate.** One decision per candidate that passed step 3 (the signal's
  shape checks: right instrument, a valid direction, shorts allowed, size/entry/stop set, stop
  on the losing side). A share that ends before that (session, open position, candles, no
  signal, a malformed candidate) leaves no decision: it is a trace entry only, as nothing was
  proposed that could be reviewed or sized.
- **5.5-c Outcomes per exit path** (`app/agent.go`, `decide`; table test
  `TestAgentTickDecisionOutcomes`): costs (either cost gate, incl. the second after the engine
  shrank the size) → `rejected_cost`; reviewer unusable, missing, erroring, timing out,
  rejecting, answering garbage, or its call not loggable → `rejected_reviewer` (verdict
  `reject` with the reason); fresh portfolio/quote, session, reservations, risk-state or
  block reads, `AssessEntry` refusal or kill → `rejected_risk`; the agent paused/stopped before
  the send, the pending decision not storable, or `placeEntry` failing before anything left →
  `order_failed`; anything that reached eToro (`Sends > 0`) → `ordered`, whatever eToro then
  did (the order's own state is shown next to it). Reasons for outcomes that are not the risk
  engine's own answer (costs, stale prices, "not sent: …") are written into `riskReason`:
  the decision has no other free-text field and the UI shows it as "why it ended".
- **5.5-d The request id is chosen before the decision.** The agent draws the order's request id,
  stores the decision `pending` with it, then passes it to `placeEntry`
  (`entryRequest.RequestID`), which stores the order row and sends. So the decision exists
  before the order row (test `TestAgentTickDecisionBeforeOrderRow` with a store that fails an
  order `Create` without its pending decision) and before `Open` (`fakes.RequireDecision`
  OnOpen hook, on in every agent test). If the pending write fails: no order row, no send,
  outcome `order_failed` (stored by a second attempt if the store recovered), trace error.
  The final write is an `Update` (an `Add` after an `ErrConflict`), not cancelled with the
  tick; if it fails the row stays `pending` with the request id and the tick reports it.
- **5.5-e LLM calls.** `app.logCall` fills id, account, purpose, profile, time, cost
  (`LLMProfile.Cost`) and, when the adapter did not, latency; it writes with
  `context.WithoutCancel`. Agent: the call is logged before its verdict is used; a failed log
  write is a reject. Scanner (purpose `scanner`, the account) and the admin's test (purpose
  `test`, no account): a failed log write is a warning only. A late answer (after the time
  limit) is logged with that error. `LLMTestResult` gains `Cost` and `CallID`.
- **5.5-f Read use cases.** `Decisions(accountID, {Before, Outcome, Limit})` (view permission +
  view access; limit 1–500, default 100; unknown outcome 400) joins each decision with the
  current state of its order (same account only). `Decision(accountID, id)` adds the LLM call
  when it is still kept and belongs to the same account. Another account's decision is 404
  (the repository's `Get` takes the account). `LLMUsage(days)` (settings permission = admin;
  1–400, default 30) uses the new `LLMCalls.Usage(since)` (SQL `GROUP BY profile_id, purpose`,
  known costs summed, unknown ones counted; storetest covers both databases) and names each
  row with the profile's current name ("" for a deleted profile). `PurgeDecisionLog` runs in
  the existing 10-minute purge loop (`httpapi.PurgeLoop`) and deletes both tables' rows older
  than `domain.LLMRetentionDays` = 400 (OD12).
- **5.5-g 4.12 extended.** Each scenario's store is wrapped so an open order row stored before
  its pending decision is a violation; `checkOpen` requires the pending decision for every
  intent; after every tick, the decisions logged equal the candidates that passed step 3 (by
  id), none is left `pending`, `ordered` ⇔ the named order was sent. A planted bug (the
  pending decision removed before the order) makes it fail.
- **5.5-h 4.13's `AgentStatusView` does not expose reservations** (only counts: open agent
  positions and unfinished orders), so it needs no change to use `app.reservations`.
- **5.6-a Contract.** `GET /api/v1/decisions?accountId&limit&before&outcome`,
  `GET /api/v1/decisions/{id}?accountId` (`DecisionDetail{decision, call?}`; the call has the
  prompt and response as stored, never a key or header), `GET /api/v1/llm/usage?days=`
  (`LlmUsage{days, since, rows, total}`). Outcome and purpose stay plain strings (4.13-e).
  Costs are `UsdCost` decimal strings of USD micros; **absent = unknown**. A usage row has
  `knownCostUsd` (always) and `costUsd` (only when every call had a price), plus
  `costUnknownCalls`; the UI shows "unknown" or "at least $x (n unpriced)", never 0.
- **5.6-b Prices on the profile.** `LlmProfile` and `LlmProfileInput` carry
  `inputPricePerMTok` / `outputPricePerMTok` (USD per million tokens, decimal strings, ""
  or "0" = unknown), parsed by the new `domain.ParseMicros` and stored as micros. They are
  **required** on input, so a save cannot drop them by omission; the web form always sends the
  values it shows. (Profile saves are web-session-only, so there are no API-key scripts to
  break.) This fixes the 5.4 note that saving reset prices to 0. Invalid prices are 400 with
  the field.
- **5.6-c The test result** gains `model` (the one that answered), `inputTokens`,
  `outputTokens`, `costUsd` (absent = unknown) and `callId`.
- **5.7-a Web.** New *Decision log* page under Trading (after Orders): a table (a log, like
  Orders) with time, share, direction, playbook, verdict, risk, outcome and order state, an
  outcome filter, a refresh button and "Older decisions" paging by `before`. The first column
  is a compact icon button (chevron, `aria-expanded`, `aria-label`) that opens the row: the
  signal, the regime probabilities, the risk engine's answer and the order, the reviewer's
  verdict and reason, and the LLM call's model, tokens, cost and latency with the prompt and
  response folded in `<details>`. The explanation is a foldable `InfoCard`. The LLM page's
  profile cards show the prices, the profile form has two price fields, an admin sees a
  *Usage* card (period 7/30/90/400 days) and a test shows the served model, tokens and cost.
- **5.7-b Pagination caveat.** `before` is exclusive on the time; two decisions with exactly
  the same microsecond on a page boundary could hide one. Accepted: one tick's decisions are
  apart by at least the reads between them.
- **5.8-a Docs.** ARCHITECTURE "Decision log and LLM calls (phase 5)", the port table, and the
  executor's resend rule corrected to review M3 (opens are never resent). README status,
  feature list and a guardrail line. No new dependency, no new eToro path, no migration
  (00010 already had both tables).

Verification (2026-11-24): `make check` green — gofumpt, golangci-lint 0 issues, vet,
`go test -race` with coverage domain 99.9 % / app 88.0 %, MariaDB `test-int` (incl.
`llm_calls`, `decisions`, `decision_log_cascade` and the new usage aggregation), govulncheck
(no vulnerabilities), contract drift in sync, `pnpm -C web check` (14 files, 44 tests).


### Pre-deploy review (2026-11-24): fixed and open items

Fixed before deploy: the kill switch stays enabled while another panel action runs
(MAJOR); `placeEntry` refuses an entry without the decision's request id; a hidden tab
does not poll the agent status (each poll is one eToro portfolio read). Five open MINORs,
for phase 6 (steps 6.6a, 6.13, 6.14, 6.15): cache the status equity per account (~10 s) so many tabs cannot crowd the
supervisor's quota; refuse StartAgent (or show it) when the agent loop is not running after
a failed ResumeAgents; read equity before the control lock in ResetKill (as m5 did for
SetRiskLimits); return `canEdit` from GET /risk-limits instead of computing it in React;
replace the remaining `window.confirm` calls (System, ApiKeys, Themes, Users, Accounts)
with dialogs.

## Phase 6 — live dashboard, history, exports — `done (awaiting owner review)`

Read-only for the broker: no new eToro path, no change to any order path. New migration 00011
(`trades`, both dialects). No new dependency (`encoding/csv`, `net/http` SSE). Order: domain →
ports/adapter → migration/repository → app → contract+handlers → wiring → web → docs.
Guardrails touched: §2.2 (the view cache must never feed a decision: 6.6a), §2.6 (the stream
and the cache are views; exits and the kill switch must not depend on them: 6.9), §2.1
(allowlist test unchanged: 6.3). No profitability claims anywhere (6.16).

### Owner decisions (OD14–OD33)

Each is the **default until the owner answers**. OD14–OD18 shape phase 6; OD19–OD33 phase 7.

- **OD14 – Stream scope:** one account per stream (the dashboard's account). The Overview keeps its REST calls. Default until the owner answers.
- **OD15 – Foreign positions in history:** listed, flagged "not opened by cactusTrade". The summary statistics default to cactusTrade's own trades. Default until the owner answers.
- **OD16 – Trade retention:** kept for as long as the account exists (accounting record), not 400 days. Default until the owner answers.
- **OD17 – CSV format:** comma-separated, UTC RFC 3339 times, USD as decimal strings, formula cells prefixed with `'`. Default until the owner answers.
- **OD18 – First trade-history backfill:** 90 days, to save the default 60/min quota. Default until the owner answers.
- **OD19 – What counts as a demo trading day (strict):** a risk day (22:00 UTC boundary) on which the linked demo account has all of: at least one `decisions` row (the agent was running and evaluated a candidate); at least one open order with `mode = demo` that was sent and reached `filled` or `partially_filled`; every position opened that day closed within the same risk day. Looser alternative: "a decision was logged" only. Default until the owner answers.
- **OD20 – Evidence window:** 20 qualifying days within the last 60 risk days, the latest at most 7 days old. Default until the owner answers. **The number of days is now the account admin's to set, 1–60, default 5 (D36, D37, 2026-11-27).**
- **OD21 – Linked demo account:** same owner as the real account, chosen when arming. Default until the owner answers.
- **OD22 – Who arms:** an admin who also owns the real account, in a web session (never an API key), with the password re-entered and the phrase `ENABLE REAL TRADING <account name>`. Default until the owner answers.
- **OD23 – Who disarms:** anyone with trade access and the `agent.kill` permission, from a session or an API key. Disarming moves the agent to stopped; exits continue. Default until the owner answers.
- **OD24 – Arming expires** after 30 days; automatic disarm also on a kill-switch trip, a key change, raising any limit, or a restore. Default until the owner answers.
- **OD25 – Real default limits / ceilings:** trading budget 0; daily loss 100 bp (ceiling 200 bp); drawdown 500 bp (ceiling 1000 bp); max position 1000 bp (ceiling 2000 bp); max open positions 4 (ceiling 8); shorts 0 % (ceiling 25 %, D19); optional deployment cap on the real trading budget. Default until the owner answers.
- **OD26 – Real agents after a restart** never resume on their own; they come back paused with an alert (stricter than OD5). Default until the owner answers.
- **OD27 – Banner** on every signed-in page; the sign-in page shows nothing about accounts. Default until the owner answers.
- **OD28 – Second key:** deployment opt-in `CACTUSTRADE_REAL_TRADING=on`, off by default. Exits on real positions never depend on it. Default until the owner answers.
- **OD29 – Backups:** age-encrypted; a recipient key is required for app backups. `deploy.sh` keeps today's 0600 gzip with a warning when no recipient is set. Default until the owner answers.
- **OD30 – Vault:** optional, standard-library client, off by default. Default until the owner answers.
- **OD31 – Release signing:** `ssh-keygen -Y sign` (no new dependency). Default until the owner answers.
- **OD32 – Cookie prefix** `__Host-`: yes, but it signs everyone out once. Default until the owner answers.
- **OD33 – Rate-limit numbers** as in 7.3; kill, pause, stop and disarm are never throttled. Default until the owner answers.
- **OD34 – Disarm when the arming admin loses the right:** should a gate disarm when the admin who armed it is demoted or disabled? Today: no (open question from the real-gate review, 2026-11-24).
- **OD35 – Disarm when the linked demo account goes:** should a gate disarm when its linked demo account is deleted? Today: no; the arming record keeps the id and only a new arm fails its check.
- **OD36 – Disarm on a rename:** should a gate disarm when the real account is renamed (the typed phrase names it)? Today: no.
- **OD37 – Banner for admins who cannot see the account:** should every admin get the real-money banner, also for real accounts not shared with them? Today: only users who can see the account (OD27); every admin is e-mailed on arm and disarm.

| ID | Step | Files | Acceptance tests | Status |
|---|---|---|---|---|
| 6.1 | **Domain: live figures.** `LimitDistances(pf Portfolio, st AccountRiskState, l RiskLimits, r Reservation, now, sessions)` → equity, cash, reserved, budget used / remaining, day P&L (equity − start-of-day equity), distance to the daily floor and the drawdown floor, open + pending vs the position cap, short exposure vs `MaxShort`, time to no-entry and flatten per market. Integer cents/bp only, saturating arithmetic, reusing `DailyFloor`, `DrawdownFloor`, `FlattenAt` and `Reservation`. | `internal/domain/live.go`, `live_test.go` | `go test ./internal/domain -run 'LimitDistances'`. Table tests. Property `TestLimitDistancesAgreeWithAssessEntry` (20 000 random inputs): if any distance is ≤ 0 then `AssessEntry` refuses a minimal entry, and if `AssessEntry` allows one then every distance is > 0. Huge values checked against `math/big`. | done |
| 6.2 | **Domain: history statistics.** `ClosedTrade` gains `Fees, Investment Cents`, `Units` (micros), `OrderID`, `StopLoss, TakeProfit Micros`. `TradeStats(trades)` → count, wins, losses, breakeven, win rate (bp; none when there are no trades), gross, fees, net. `DailyPnL(trades, snapshots)` grouped by `RiskDay` (22:00 UTC): realised, fees, equity change, labelled as including deposits and withdrawals. `Drawdown(snapshots)` → peak, max drawdown (cents and bp), current drawdown. | `internal/domain/{trading.go,history.go,history_test.go}` | `go test ./internal/domain -run 'TradeStats\|DailyPnL\|Drawdown'`. Properties: wins+losses+breakeven = count; net = Σ net; drawdown ≥ 0 and ≤ peak; a non-decreasing series has drawdown 0; trades at 21:59:59 and 22:00:00 UTC fall on different days; no overflow (saturating). | done |
| 6.3 | **eToro adapter: read the full trade history (read-only).** Parse `fees`, `investment`, `initialInvestment`, `units`, `orderId`, `stopLossRate`, `takeProfitRate`. Split lookbacks over 1 year − 1 day into windows (spec), at most 50 pages per window. Missing `fees` = unknown (kept apart, never 0). | `internal/adapters/etoro/{read.go,client_test.go}`, `testdata/history*.json` (synthetic, spec-shaped) | `go test ./internal/adapters/etoro/...`, coverage ≥ 90 %. The fixture with fees parses to cents. A 400-day request makes 2 windows. A missing `fees` field is flagged unknown. `TestAllowlist*` unchanged and green (no new path). | done |
| 6.4 | **Migration 00011 `trades` + repository.** `trades` (account_id FK cascade, position_id, PK (account_id, position_id); mode, instrument_id, direction, leverage, ours, open_request_id, opened_at, closed_at, units_micros, invested_cents, open/close/stop/tp micros, net_profit_cents, fees_cents, fees_known, etoro_order_id, synced_at; index (account_id, closed_at)). `ports.Trades{Upsert (update-then-insert in Go), List(account, from, to, ours filter, before, limit), LastClosedAt}`. No purge (OD16). | `migrations/{sqlite,mariadb}/00011_trades.sql`, `internal/ports/ports.go`, `internal/ports/fakes/*`, `internal/adapters/sqlstore/trades.go`, `storetest/trades.go` | `make test && make test-int`, `TestMigrationParity`. Shared suite: upsert twice = one row with the newer values; `List` newest first, `before` exclusive, filters; other account → empty; cascade on account delete; MariaDB up→down→up. | done |
| 6.5 | **App: trade sync and history use cases.** `SyncTrades(ctx, accountID)` (system, read-only; eToro accounts with keys, demo and real): since `LastClosedAt − 2 days` (first run: OD18). `ours` = position in `agent_positions`, plus `open_request_id`. `SyncTradesAll` with per-account timeout. Viewer use cases with view access: `Trades(p, acct, from, to, ours, before, limit 1–500)`, `DailyPnL(p, acct, days 1–400)`, `HistorySummary(p, acct, days)` (`TradeStats` + `Drawdown` over `equity_snapshots`), `ExportRows(p, acct, kind, from, to)` (≤ 50 000 rows, kinds trades / daily / equity / orders / decisions). | `internal/app/history.go`, `history_test.go` | `go test -race ./internal/app -run 'History\|SyncTrades\|Export'`. Tables: another account → 404; viewer allowed; trade access not needed; a broker error leaves the stored rows unchanged and returns the error; syncing twice is idempotent; ours / foreign marked correctly (OD1); a view-only platform → `ErrViewOnly`; export over the cap → 422. | done |
| 6.6a | **App: portfolio view cache (MINOR 1).** `runtimeState` keeps the last portfolio per account (value + read time). The supervisor stores its read there (no extra eToro call); `viewPortfolio` refreshes only when the cache is older than 10 s, with one read per account in flight (`sync.Mutex` + waiter; no new dependency). `AgentStatusView` uses it. **Only view use cases may read it.** | `internal/app/{viewcache.go,agent_view.go,supervisor.go}`, `viewcache_test.go` | `go test -race ./internal/app -run 'ViewCache\|AgentStatusView\|EntriesNeverUseTheViewCache'`. 50 concurrent `AgentStatusView` calls in 10 s → ≤ 1 `Portfolio` read. **Guardrail §2.2:** `TestEntriesNeverUseTheViewCache` primes the cache with 100× the cash; `AgentTick`'s `Open` amount is still ≤ the SimBroker's fresh cash − reserved. A source test fails if `agent.go`, `executor.go`, `supervisor.go` (except the store call) or `risk.go` read the cache. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| 6.6b | **App: event bus and live snapshot.** `Deps.Bus ports.EventBus` (nil = no-op). `emitFor` publishes after the DB write. Bus-only events: `order.updated` after every stored order transition (executor and reconcile), `agent.state` on every state write, `account.portfolio` after the supervisor's read. `LiveSnapshot(p, acct)` = `LimitDistances` + positions (ours flag, stop, take-profit, flatten time) + open orders + the last 20 orders + the latest regime per watchlist share (from `reviews`) + agent state, reconciliation, kill, latch, lease + freshness (`portfolioAt`, `stale` after 30 s). `LiveFilter(p)` = the account ids the caller may view. | `internal/app/{live.go,events.go,executor.go,reconcile.go,agent_control.go}`, `live_test.go`, `internal/ports/fakes` (recording bus) | `go test -race ./internal/app -run 'Live\|Publish'`. Every executor table case publishes the same transitions it stores, in order. A bus that drops events never blocks an order (full subscriber). `LiveFilter` excludes unshared accounts and view-only rules are kept. The snapshot uses only cached or stored data. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| 6.7 | **Contract and handlers: history and exports.** Contract first: `GET /api/v1/history/summary?accountId&days`, `/history/trades?accountId&from&to&ours&before&limit`, `/history/daily?accountId&days`, `/history/export/{kind}.csv?accountId&from&to` (kinds trades / daily / equity / orders / decisions). Then `make gen` and the handlers. CSV: `encoding/csv`; money as decimal strings from cents; times UTC RFC 3339; any cell starting with `= + - @ \t \r` prefixed with `'`; `Content-Disposition: attachment`, `nosniff`, `no-store`; written while streaming rows. | `api/openapi/cactustrade.yaml`, `internal/adapters/http/{api.gen.go,handlers_history.go,csv.go,history_test.go}`, `web/src/lib/schema.d.ts` | `make gen && go test ./internal/adapters/http -run 'History\|CSV'`. Status codes 200/400/403/404/422; an API key (viewer) may export; the injection table (`=1+1`, `-2`, `@SUM`, tab) is neutralised; amounts are exact for ±9e15 cents; contract drift green. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| 6.8 | **Live stream: contract and server timeouts.** Contract `GET /api/v1/live?accountId` (`text/event-stream`; components `LiveSnapshot`, `LiveEvent`, `LiveOrder`; event names `snapshot`, `event`, `order`, `resync`, `bye`, documented in the description). In `cmd`, factor `newHTTPServer(cfg, handler)` so tests use the production timeouts. | `api/openapi/cactustrade.yaml`, `api.gen.go`, `cmd/cactustrade/{main.go,server.go}`, `schema.d.ts` | `make gen`, contract drift green, `go build ./...` (handler stub answers 503 until 6.9). | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| 6.9 | **Live stream: handler.** Cookie or API key; no CSRF (GET); view access to the account. `http.ResponseController`: `SetReadDeadline(zero)` once, `SetWriteDeadline(now + 10 s)` before every write, `Flush`. Headers `text/event-stream`, `Cache-Control: no-store`, `X-Accel-Buffering: no`. On connect: a full snapshot. Then `event` / `order` messages from the bus filtered by `LiveFilter`, a snapshot every 5 s (cache only), `: ping` every 15 s. Session and access re-checked every 30 s (expired, revoked, password changed or share removed → `bye` and close). Stream lifetime ≤ 30 min (the client reconnects). Bus overflow → `resync`. Caps: 4 per user, 200 total → 429. `Server.CloseStreams()` is registered with `srv.RegisterOnShutdown`. The request histogram skips the route; gauge `cactustrade_live_streams`. | `internal/adapters/http/{handlers_live.go,live_test.go,middleware.go,server.go}`, `cmd/cactustrade/{main.go,main_test.go}` | `go test -race ./internal/adapters/http -run Live && go test ./cmd/cactustrade -run 'Stream\|Shutdown'`. Over `httptest` TLS with `newHTTPServer`, timeouts scaled to 200/300 ms, on HTTP/1.1 **and** HTTP/2: events still arrive after 3× ReadTimeout and 3× WriteTimeout. `Shutdown` returns in < 1 s with 3 open streams, and a kill request in flight still completes (4.14-b). A revoked session gets `bye` within one recheck. A viewer of A never receives B's events. The 5th stream → 429. API key works. No body, cookie or token is logged. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| 6.10 | **Wiring.** `eventbus.New()` into `app.Deps.Bus` (dropped count as a metric); `historyLoop` (every 15 min, 2 min after start, read-only, may run before `ReconcileAll`); the `trades` sync covers real accounts read-only. | `cmd/cactustrade/{main.go,trading.go,main_test.go}` | `go test ./cmd/cactustrade`: the 4.14 ordering hook still sees `ReconcileAll` before the first `Supervise`; history sync is read-only (SimBroker `Opens == 0`, no close or cancel); `make build`. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| 6.11 | **Web: live dashboard.** `useLive(accountId)`: `EventSource`; backoff 1 s … 30 s; after 3 failures, polling every 15 s; the stream closes after 60 s hidden. The Dashboard shows equity, cash, budget used / remaining, day P&L, positions (ours / not ours, stop, flatten time), an orders and fills feed, regimes per share, agent state and limit distances as bars (all from the server; no arithmetic in React beyond formatting). `AgentPanel` reads the stream instead of its 15 s poll. A "stale" badge appears after 30 s without a snapshot. | `web/src/lib/live.ts`, `web/src/pages/Dashboard.tsx`, `components/{AgentPanel.tsx,LiveCards.tsx}`, tests | `pnpm -C web check`. Vitest with a fake `EventSource`: snapshot renders; an `order` event updates the feed; `resync` refetches; stale badge; fallback polling after 3 errors; kill button still enabled while the stream is down. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| 6.12 | **Web: History page** (under Trading, after Decision log). Neutral summary cards (trades, wins / losses / breakeven, win rate, fees, realised P&L, max drawdown), daily P&L table + inline-SVG bars, equity curve with drawdown shading (reuse `EquityChart`), trades table (ours / all, date range, paging), CSV buttons, and a fixed note "Figures describe past results only; they do not predict future results." | `web/src/pages/{History.tsx,History.test.tsx}`, `components/{Layout.tsx,EquityChart.tsx}`, `lib/api.ts` | `pnpm -C web check`. Vitest: empty history shows "no closed trades" (no 0 % win rate); unknown fees show "unknown"; export links carry `accountId` and the range. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| 6.13 | **MINORs 2 and 3 (app).** (2) `Service.SetAgentLoop(running bool)`, set by `startTrading`. While it is false, `startable` refuses with `ErrUnavailable` ("the agent loop is not running: start-up resume failed; restart cactusTrade") and `controls.start` is false with that reason (`AgentView.StartBlocked`). (3) `ResetKill` reads equity **before** the control lock and checks the kill again inside it (as m5). | `internal/app/{agent_control.go,agent_view.go}`, `cmd/cactustrade/trading.go`, tests; `api/openapi` (`AgentControls.startBlockedReason`), `handlers_agent.go` | `go test -race ./internal/app -run 'StartWithoutLoop\|ResetKillDoesNotDelayAKill'`, `go test ./cmd/cactustrade` (a failed resume → Start refused), `make gen`, drift green. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| 6.14 | **MINOR 4: `canEdit` from the server.** `GET /risk/limits` returns `canEdit` and `editBlockedReason` from the same rules as `SetRiskLimits` (permission, session, `limitsChangeable`). `RiskLimits.tsx` only shows them. | `api/openapi/cactustrade.yaml`, `internal/app/risk.go`, `handlers*.go`, `web/src/pages/RiskLimits.tsx` + tests | `make gen && go test ./internal/app -run RiskLimitsView && pnpm -C web check`. Vitest: the form is disabled with the server's reason; no role check left in `RiskLimits.tsx`. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| 6.15 | **MINOR 5: no `window.confirm`.** Extract `AgentPanel`'s modal into `ConfirmDialog` and use it in System, ApiKeys, Themes, Users and Accounts. ESLint `no-restricted-properties` bans `window.confirm`, `alert` and `prompt`. | `web/src/components/{ConfirmDialog.tsx,AgentPanel.tsx}`, `web/src/pages/{System,ApiKeys,Themes,Users,Accounts}.tsx` + tests, `web/eslint.config.*` | `pnpm -C web check`. A planted `window.confirm` fails lint. Vitest per page: cancel does nothing, confirm calls the API once. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| 6.16 | **Wording guard and docs.** `TestNoProfitabilityClaims` scans `web/src`, `README.md`, `docs/`, `api/openapi` for a banned-phrase list (profitable, guaranteed, beat the market, make money, winning strategy, "returns of"…). Allowed lines are listed in the test with a reason (e.g. this rule itself). ARCHITECTURE "Live view and history (phase 6)"; `docs/etoro-api.md` corrected (resends per M3; position cap 8 default / 15 hard; trade-history fields); README. | `internal/docs_test.go` (or `cmd/cactustrade/docs_test.go`), docs | `make check`. A planted "profitable strategy" in a page fails. | done (2026-11-24; `make check` green incl. MariaDB test-int) |
| 6.17 | **Phase verification and owner review.** `make check` green (coverage domain/app ≥ 80 %, MariaDB `test-int` incl. `trades`); smoke: dashboard live for 35 min across a stream lifetime, CSV opens in a spreadsheet without formulas, kill works while streams are open. Stop for the owner. | PLAN | `make check` | done (`make check` green 2026-11-24); the 35-minute smoke run is the owner's |

### Steps 6.1–6.5: decisions (2026-11-24)

No new dependency, no new eToro path (the allowlist and its tests are unchanged), no change to
any order path. New migration 00011 (`trades`) in both dialects.

- **6.1-a `LimitDistances(pf, st, l, r, now, sessions map[Market]MarketSession) LiveFigures`.**
  Sessions are per market (the step said "per market"); the figures are equity, cash,
  reserved, cash left, budget (min(budget, equity), 0 without one), budget used / left, day
  P&L and the distance to the daily floor (only when the stored risk state is today's risk
  day with a start-of-day equity: `DayKnown`; else 0, which AssessEntry refuses anyway), the
  drawdown floor and its distance, open + pending vs the position cap, short exposure vs
  `MaxShort` of the budget, and per market the no-entry and flatten times and the time left
  (never negative). The distances that gate an entry are cash left, budget left, the two
  floor distances, slots left and the market's time to no-entry, plus short left for a short.
  The property test (20 000 inputs, a 1-cent entry with no eToro minimum and no per-position
  cap, which is a size limit and not a distance) holds both ways.
- **6.1-b Exact sums.** Every sum is exact (a split hi·2³² + lo accumulator, no unsigned
  conversions) and clamped to the int64 range once; floors take the clamped equity.
  **`BasisPoints.of` (domain/risk.go) is now computed in 128 bits** (`mulDivTrunc`): the old
  `c × bp / 10 000` wrapped for amounts above ~9.2 × 10¹⁴ cents. Results are identical
  wherever the old product did not overflow, so `AssessEntry`, `Observe` and the floors behave
  the same in every realistic range (their tests are unchanged and green). Checked against
  `math/big` on 20 000 inputs over the whole int64 range.
- **6.2-a Names.** `TradeStats(trades) TradeSummary`, `DailyPnL(trades, snapshots) []DayPnL`,
  `Drawdown(snapshots) DrawdownFigures`; the stored aggregate is `domain.Trade` (eToro's
  `ClosedTrade` + account, mode, ours, open request id, synced at). `ClosedTrade` gained
  `Fees`, `FeesKnown`, `Investment`, `Units`, `OrderID`, `StopLoss`, `TakeProfit` and
  `Direction()`.
- **6.2-b Fees and gross.** Unknown fees are counted in `FeesUnknown` and never as 0. Net is
  the sum of eToro's `netProfit`; **gross = net + known fees, assuming `netProfit` is after
  fees (RU5, unverified)**. The win rate is absent (`HasWinRate` false) without trades.
- **6.2-c Daily figures.** Grouped by `RiskDay` of the close (22:00 UTC). A day's equity change
  is its last snapshot minus the last snapshot of the previous day that has one (the first
  day: minus its own first snapshot); it includes deposits, withdrawals and positions
  cactusTrade did not open, and is labelled so (CSV column
  `equity_change_usd_incl_deposits_withdrawals`).
- **6.2-d Drawdown.** Negative equity counts as 0 (a drawdown is never more than its peak).
  `MaxDrawdown` (cents) and `MaxDrawdownBp` are separate maxima and may come from different
  falls. New helpers `RiskDayStart(t)` (the 22:00 UTC that began t's risk day),
  `ExportMaxRows` = 50 000 and the sentinel **`domain.ErrTooLarge`** (6.7 maps it to 422; until
  then no endpoint returns it).
- **6.3-a Windows.** `TradeHistory(since)` reads windows of 364 days ("1 year − 1 day", also
  in a leap year) from `since` (UTC date). A trade is kept by the window its close falls in
  (the last window up to now) and once per position id, so the result is the same whether
  eToro answers a window up to its end or up to now (**unverified which; for 4.17/RU**). At
  most 50 pages of 100 per window; a 50th full page ends that window without a flag (the first
  sync reads 90 days, later ones a few days, far below 5 000 trades). Any failing page fails
  the whole read, so nothing partial is stored.
- **6.3-b Fields.** A missing or null `fees` is unknown (`FeesKnown` false, fees 0).
  `Investment` = `initialInvestment` when present and > 0, else `investment`. Fixture
  `testdata/history_fees.json` (synthetic, spec-shaped).
- **6.4-a Port.** `Trades{Upsert(t), List(account, TradeFilter{From, To, Ours *bool, Before,
  Limit}), LastClosedAt(account)}`: `From ≤ closed < To`, `Before` exclusive, newest close
  first with ties by position id descending (the 5.7-b paging caveat applies), `LastClosedAt`
  zero when there is none. `Upsert` validates (`Trade.Validate`) and answers `ErrNotFound` for
  an unknown account. No purge (OD16); rows go with their account (FK cascade). The fake
  brokers gained a scripted `History` (`fakes.Broker` also `HistoryErr`, `HistoryCalls`).
- **6.5-a Sync.** `SyncTrades(ctx, account)` (system): eToro accounts with keys, demo and real,
  read-only (`TradeHistory` only; a SimBroker test proves no open, close or cancel). Refuses a
  view-only platform (`ErrViewOnly`), no client (`ErrUnavailable`), no keys
  (`ErrCredentialsMissing` + `ErrUnavailable`). Since = latest stored close − 2 days, else now
  − 90 days (OD18). `ours` = the position is in `agent_positions` (with its open request id);
  a store error while checking that aborts the sync (a trade is never stored with a guessed
  owner). Trades without a position id or close time are skipped with a warning.
  `SyncTradesAll` runs the accounts one after another (they share the default 60/min quota),
  2 min each, and returns the context's error on shutdown.
- **6.5-b Views.** `Trades`, `DailyPnL`, `HistorySummary`, `ExportRows` need view permission
  and view access only (viewer, API key included); another account is 404, a view-only
  platform `ErrViewOnly`. A period of N days is the last N risk days including today. Daily
  realised figures and the summary statistics are over cactusTrade's own trades (OD15);
  `HistorySummary.ForeignTrades` counts the others in the period; the drawdown is over the
  whole account's snapshots. More than 50 000 trades in a period is `ErrTooLarge`.
- **6.5-c Exports.** `Export{Kind, Columns, Rows [][]any}` with typed cells (string, int,
  int64, bool, `Cents`, `Micros`, `BasisPoints`, `time.Time`, nil) for 6.7's CSV writer;
  oldest first; `to` zero = now, `from` zero = all. Trades include foreign ones (column
  `ours`) and write unknown fees as `unknown`. Orders and decisions have no time-bounded
  list, so 50 001 are read newest first and the export is refused (`ErrTooLarge`) when the
  range may reach past them: never a silently short file.

Verification (2026-11-24): `make check` green — gofumpt, golangci-lint 0 issues, vet,
`go test -race` with coverage domain 99.8 % / app 88.7 %, MariaDB `test-int` (incl. `trades`,
`trades_cascade`, migrations up→down→up), govulncheck (no vulnerabilities), contracts in sync,
`pnpm -C web check`. `internal/adapters/etoro` coverage 95.2 %.

### Steps 6.6a, 6.6b, 6.7: decisions (2026-11-24)

No new dependency, no migration, no eToro path (the allowlist and its tests are unchanged),
no change to what an order path decides. Contract changes are additive (four GET routes,
schemas `HistoryTrade`, `HistorySummary`, `HistoryDrawdown`, `HistoryDay`, `DailyHistory`,
parameter `HistoryDays`, tag `history`).

- **6.6a-a The cache** (`internal/app/viewcache.go`) is `runtimeState.views`: the last
  portfolio per account with its read time (the clock's time when the read returned), and at
  most one read per account in flight (a `sync.Mutex` and a `done` channel per flight; no
  `singleflight` dependency). A view reads eToro only when the entry is ≥ 10 s old; waiters
  share the flight's result, error included. The shared read runs on
  `context.WithoutCancel(ctx)` bounded by `agentViewTimeout`, so one closed tab does not fail
  the others; a waiter whose own context ends leaves without its answer. An error is never
  cached. An older read never replaces a newer one. The new entry is stored and the flight
  removed under one lock (no second read in the gap).
- **6.6a-b Guardrail §2.2.** The supervisor stores its own read (`s.rt.views.put`, once) and
  never reads the cache back; the agent tick, `freshPrices`, the executor and
  `SetRiskLimits`' budget check keep reading eToro fresh. `TestEntriesNeverUseTheViewCache`
  primes the cache with 100× the SimBroker's cash (equity held in a foreign position's gain
  so the budget is unchanged) and asserts every `Open` amount ≤ fresh cash − reserved and that
  the tick does not touch the cache. `TestViewCacheReadersAreViewsOnly` parses `agent.go`,
  `executor.go`, `risk.go` and `supervisor.go` (go/ast, no `os`: depguard) and fails on any
  cache reader (`viewPortfolio`, `peek`, the cache's maps, `viewEquity`, `AgentStatusView`,
  `LiveSnapshot`, `WarmLiveView`) or any use of `.views` other than supervisor.go's one
  `.views.put(`; it also fails if any other non-view file calls them.
  `TestViewCacheSourceTestCatchesReads` proves it catches planted reads. Declaring and
  building the field in executor.go reads nothing and is allowed.
- **6.6a-c `AgentStatusView`** reads the equity through the cache (doc: "within the last
  10 s"); no contract change.
- **6.6b-a Bus events** (`internal/app/events.go`): `event` (Data `domain.Event`, after the
  write is attempted: a live viewer still sees a kill when the database is down),
  `order.updated` (Data `domain.Order`, after every successful `Orders().Create` (new helper
  `createOrder`) and `Orders().Update` in `move`; reconcile and the supervisor use `move`, so
  they are covered), `agent.state` (Data `AgentStatus`, in `setAgentState` after a successful
  write; `tripLocked` now writes through `setAgentState` and also publishes `killed` when the
  write failed, because the kill is latched in memory), `account.portfolio` (Data
  `PortfolioRead{AccountID, At, Equity}`, after the supervisor's read). `BusAccount(e)` names
  an event's account for 6.9's filter. `Deps.Bus` nil = no bus. The `ports.EventBus` doc now
  says `Publish` must never block; `eventbus` and the new `fakes.Bus` (recording) drop for a
  full subscriber. No goroutine per publish (it would reorder transitions).
- **6.6b-b Tests.** `newExecEnv` wraps the order store with a recorder and installs a
  `fakes.Bus`; every case of `TestExecutorPlaceEntry`, `TestExecutorClosePosition` and
  `TestExecutorCancelOrder` asserts the published order versions equal the stored ones, in
  order; `TestPublishReconcileTransitions` does so through `Reconcile`.
  `TestPublishNeverBlocksAnOrder` places an order with a never-read, full subscriber.
- **6.6b-c `LiveSnapshot(ctx, p, account)`** (view access) never calls eToro: the portfolio
  comes from the view cache only (`PortfolioKnown`, `PortfolioAt`, `Stale` when missing or
  older than 30 s); without one there are no figures or positions. Figures are
  `domain.LimitDistances` with the stored risk state, the limits, `reservations` (stored
  orders) and today's session per market. Positions carry `Ours`, the open request id, the
  symbol and market, stop and take-profit, and the flatten time for ours. Open orders, the
  last 20 orders, the latest regime per watchlist share (the most likely regime of its latest
  review with probabilities, ties by name), agent state, reconciliation, kill (stored or
  latched) with its reason, the daily latch of today and the lease.
- **6.6b-d `WarmLiveView(ctx, p, account)`** (new, view access) fills the cache through
  `viewPortfolio` (≤ 1 eToro read per account per 10 s, shared with every view) for accounts
  the supervisor does not read (real accounts, before the first pass). 6.9 calls it on
  connect; the 5-s snapshots stay cache-only.
- **6.6b-e `LiveFilter(ctx, p)`** = the ids of `Accounts(p)` (owned and shared, the role and
  API-key rules of `Accounts` kept; default password refused). System-wide events (account
  "") are left to the stream's choice.
- **6.7-a Route `/history/export/{file}`** with `file` an enum of `trades.csv`, `daily.csv`,
  `equity.csv`, `orders.csv`, `decisions.csv`: Go's `ServeMux` refuses a wildcard that is
  not a whole path segment (`{kind}.csv` would panic at start-up). The URLs are the ones the
  step named. An unknown file is 404.
- **6.7-b Status codes.** `domain.ErrTooLarge` → 422 "Too large" (problem+json, before any
  byte of the file); invalid days, limit, range or time → 400; another or unknown account →
  404; a view-only platform → 403; signed out → 401. Summary and daily also list 422 (more
  than 50 000 trades in the period). A viewer API key may read and export.
- **6.7-c CSV** (`csv.go`): `encoding/csv`, header row, rows written as they are formatted.
  Money from cents with exactly two places and micros trimmed, both by integer arithmetic
  exact over the whole int64 range; times `RFC3339Nano` in UTC (whole seconds print without
  a fraction); unknown cells empty, unknown fees `unknown`. **Formula neutralising applies
  to every text cell** (strings and any other type, header included): a leading `= + - @`
  tab or CR gets `'`. Numeric cells written from integers are not prefixed: they contain
  only digits, one `-` and one `.`, which a spreadsheet reads as a number, never a formula,
  and a `'` would turn every loss into text (OD17 "formula cells"). Headers:
  `Content-Type: text/csv; charset=utf-8`, `Content-Disposition: attachment;
  filename="cactustrade-<kind>-<account id>.csv"` (id reduced to `[A-Za-z0-9_-]`),
  `X-Content-Type-Options: nosniff`, `Cache-Control: no-store`. A write error after the
  status is logged (kind, account id, error; no row data).
- **Open (closed by 6.10):** the 6.10 wiring still has to pass `eventbus.New()` as `Deps.Bus`; until then the
  production bus is nil (no events, nothing blocked). `internal/adapters/http` coverage
  79.6 %.

Verification (2026-11-24): `make check` green: gofumpt, golangci-lint 0 issues, vet,
`go test -race` with coverage domain 99.8 % / app 89.0 %, MariaDB `test-int` (the first run's
container did not publish port 3306, a Docker flake; the rerun and the full `make check`
passed), govulncheck (no vulnerabilities), contracts in sync, `pnpm -C web check` (18 files,
89 tests).

### Steps 6.8, 6.9, 6.10: decisions (2026-11-24)

No new dependency, no migration, no eToro path (the allowlist and its tests are unchanged),
no change to what an order path decides. Contract changes are additive: `GET /api/v1/live`
(tag `live`) and the schemas `LiveSnapshot`, `LiveFigures`, `LiveMarketClock`,
`LivePosition`, `LiveRegime`, `LiveEvent`, `LiveOrder`, `LiveNotice`.

- **6.8-a Generated message types.** The stream's 200 is `text/event-stream` (a string), so
  nothing references the message schemas and oapi-codegen pruned them. `oapi-codegen.yaml`
  now sets `skip-prune: true`; the only types it adds are the eight `Live*` ones (checked by
  diffing the generated file), and `make contracts` uses the same config.
- **6.8-b `newHTTPServer(cfg, handler, log)`** (`cmd/cactustrade/server.go`) builds the
  production server from `prodTimeouts()` (10 s header, 30 s read, 60 s write, 120 s idle,
  64 KiB headers); `newHTTPServerWith(addr, handler, timeouts, log)` is what tests use with
  scaled timeouts, so they run the same construction. It also registers
  `handler.CloseStreams` with `RegisterOnShutdown`, so the handler is `*httpapi.Server`:
  `httpapi.New` now returns `*Server` (it implements `http.Handler`; existing callers are
  unchanged).
- **6.9-a Message shapes.** `snapshot` = `LiveSnapshot`, `event` = `LiveEvent{accountId,
  event}`, `order` = `LiveOrder{accountId, order}` (the symbol from the latest snapshot;
  empty when the snapshot did not name the instrument), `resync` / `bye` = `LiveNotice{reason}`.
  bye reasons: `signed_out`, `no_access`, `unavailable` (the recheck could not be made: fail
  closed), `lifetime`, `shutdown`. One account per stream (OD14): the account is resolved
  once (`accountId` absent = the current account at connect) and kept.
- **6.9-b Connect order.** Signed out → 401; then the caps (`429` with `Retry-After: 30`;
  after `CloseStreams` new streams get 503); then the bus subscription (before the first
  snapshot, so nothing in between is missed); then `WarmLiveView` bounded to 5 s (its error
  is logged at debug level and ignored: the snapshot is marked stale); then `LiveSnapshot`,
  whose authorization error is answered as problem+json before any stream byte (404 for
  another user's account, 403 view-only / password change); then `LiveFilter`.
- **6.9-c Filtering.** An `event` is forwarded when `BusAccount` is the stream's account and
  that account is in the caller's `LiveFilter`, or when it is system-wide (account "", in
  everyone's feed already); `order` only for the stream's account in the filter; `agent.state`
  and `account.portfolio` of that account trigger a snapshot (at once, or `liveSnapshotGap`
  = 1 s after the previous one). Everything else is dropped.
- **6.9-d Deadlines.** `SetReadDeadline(zero)` once after the header; before every write
  `SetWriteDeadline(now + 10 s)`, then `Flush`, then **`SetWriteDeadline(zero)`**: no write is
  pending until the next one, and on HTTP/2 a deadline that passes resets even an idle stream
  (the 15 s ping gap is longer than 10 s). `TestStreamOutlivesServerTimeouts` (HTTP/1.1 and
  HTTP/2 over httptest TLS, server timeouts 200/300 ms, the stream's own write timeout
  100 ms, 1 s of silence) fails on HTTP/2 when the clear is removed, and on both protocols
  when all deadline handling is removed. Go 1.26's HTTP/1.1 server clears the read deadline
  itself when it starts the background read, so the explicit `SetReadDeadline(zero)` is
  defensive (kept as the step specifies; not separately detectable). The 10 s write deadline
  bounds a stalled client, it is not what keeps a stream alive.
- **6.9-e Overflow → resync.** The port has no per-subscriber drop signal, so the stream
  treats a buffer found full on receipt (`len ≥ cap − 1` after the receive, buffer 256) as
  "an event may have been dropped": it discards what is buffered, sends `resync` and a fresh
  snapshot. Sound (a drop always leaves the buffer full until the stream's next receive) and
  conservative (a full buffer without a drop also resyncs; the client only refetches).
- **6.9-f Recheck (30 s).** The request's API key or session cookie is resolved again exactly
  as the middleware does (`APIKeyPrincipal` / `SessionPrincipal`), then `LiveFilter`. A
  session recheck refreshes the session's idle timer like any request: an open dashboard is
  in use (and its reconnect every 30 min would refresh it anyway). Signed out, expired,
  revoked key, password changed (sessions end) → bye `signed_out`; share removed, role or
  password-change rule → bye `no_access`; a store error → bye `unavailable`. Snapshots after
  a recheck use the refreshed principal. A snapshot that fails for lost access also ends the
  stream; any other snapshot error is logged and skipped (the client's stale badge shows it).
- **6.9-g Limits** are `httpapi.Config.Live` (`LiveLimits`; zero fields = 5 s snapshot, 15 s
  ping, 30 s recheck, 30 min lifetime, 10 s write, 5 s warm, 4 per user (API keys count
  for their owner), 200 total, buffer 256), so tests scale them. Gauge
  `cactustrade_live_streams`; the latency histogram skips `GET /api/v1/live` (the request
  counter still counts it). The access log line is the usual one (method, path, status,
  duration, IP, user): no query string, body, cookie or token (tested with an API key and a
  session).
- **6.9-h Shutdown.** `CloseStreams` closes one channel: every stream sends bye `shutdown`
  and returns, and new streams are refused. `TestShutdownWithOpenStreams` (HTTP/1.1 and
  HTTP/2): 3 open streams and a kill request held in flight for 150 ms; `Shutdown` returns
  in < 1 s, after the kill completed (200, kill stored), and every stream got bye.
- **6.10-a Bus.** `eventbus.New()` is `Deps.Bus`; its drop count is the counter
  `cactustrade_live_bus_dropped_total` (a `CounterFunc` over `Bus.Dropped`).
- **6.10-b History loop.** `historyLoop` runs inside `startTrading`'s wait group (so `stop`
  waits for it before the leases are released) but independently of the 4.14 sequence: it
  only reads closed trades (`SyncTradesAll`: demo and real accounts, OD18's 90-day first
  backfill), so it may run before `ReconcileAll`. `startTrading` takes a `tradingSchedule`
  (`prodSchedule()`: first after 2 min, then every 15 min). The `trader` interface gained
  `SyncTradesAll`. Tests: `TestStartTradingHistoryLoop` (ordering hook: reconcile → resume →
  loops unchanged with the history loop running from t = 0; release last; a failed sync is
  tried again), `TestHistoryLoopIsReadOnly` (real use cases on the SimBroker: the trade is
  stored, `Opens`, `Closes`, `Cancels` 0, no violation, reconciliation before the first
  supervisor pass).

Verification (2026-11-24): `make check` green: gofumpt, golangci-lint 0 issues, vet,
`go test -race` with coverage domain 99.8 % / app 89.0 %, MariaDB `test-int`, govulncheck
(no vulnerabilities), contracts in sync, `pnpm -C web check` (18 files, 89 tests); `make
build`. `internal/adapters/http` coverage 79.7 %, `cmd/cactustrade` 49.7 %. The stream and
shutdown tests passed 5 runs in a row under `-race`.

### Steps 6.13–6.15: decisions (2026-11-24)

The pre-deploy review's MINORs 2–5. No new dependency, no migration, no eToro path, no
change to an order path or a guardrail; contract changes are additive (an optional
`AgentControls.startBlockedReason`; GET /risk/limits answers `RiskLimitsView`, i.e. the
same limits plus `canEdit` and an optional `editBlockedReason`).

- **6.13-a The loop flag has three states, not two.** `Service.SetAgentLoop(running bool)`
  stores an `atomic.Int32` on the Service (no package state): *starting* (the zero value,
  until start-up decides), *running* (`startTrading` calls `SetAgentLoop(true)` just before
  the agent loop starts) and *down* (`SetAgentLoop(false)` when `ResumeAgents` failed).
  While starting, Start is refused with "cactusTrade is still starting up (reconciling with
  eToro); try again in a minute"; while down with the step's message "the agent loop is not
  running: start-up resume failed; restart cactusTrade". Both are `ErrUnavailable` (503).
  Fail closed: an agent switched on during start-up would otherwise be marked running with no
  loop to tick it. Shutdown does not reset the flag: `main` shuts the web server down before it stops the
  loops, so no Start can arrive after the loop ended.
- **6.13-b The check is in `StartAgent`, not in `startable`.** `startable` is also the OD5
  resume's rule, and the resume runs before the loop starts; putting the check there would
  pause every agent on every restart. `TestStartWithoutLoop` covers that the resume is
  unaffected.
- **6.13-c `AgentView.StartBlocked`** is set only when the caller could otherwise start
  (`controls.start` would be true): then `controls.start` is false and
  `controls.startBlockedReason` carries the reason. Pause, stop, kill and reset are
  untouched. The panel shows the reason in the Start button's title and in a warning line
  ("Exits and the kill switch still work").
- **6.13-d `ResetKill`** checks the kill switch, reads eToro's equity, and only then takes
  the control lock and checks the kill switch again (a reset that finished meanwhile makes
  this one a 409). A kill pulled during the read is a no-op on an already killed account
  (`persistKill` keeps the first kill), so the reset still applies, exactly as a kill just
  before the lock did before; `TestResetKillDoesNotDelayAKill` proves the kill no longer
  waits for the read.
- **6.13-e Tests:** the app, HTTP and composition-root test services call
  `SetAgentLoop(true)` (as after a clean start); `TestStartAgentNeedsTheAgentLoop`
  (`cmd/cactustrade`) runs `startTrading` with a failing resume and gets the refusal and the
  panel reason; the ordering test checks `SetAgentLoop` is called once, before the loop.
- **6.14-a `app.RiskLimitsView(p)`** returns the limits plus `CanEdit` / `EditBlocked`,
  decided in the order `SetRiskLimits` checks: owner access, web session, then
  `limitsBlocked` (the rules of `limitsChangeable`, which now also returns a message for
  people). `TestRiskLimitsView` asserts for every case that `SetRiskLimits` succeeds exactly
  when `CanEdit` is true. Whether a budget fits the equity is still checked only on the PUT
  (it needs an eToro read; the view stays free of broker calls).
- **6.14-b A killed agent state now gives the kill message** ("the kill switch is on: an
  admin must reset it before the limits can change", 409) instead of "stop the agent (it is
  killed)"; the status code is unchanged.
- **6.14-c The response is flat** (`allOf` RiskLimits + `canEdit`, `editBlockedReason`), so
  existing GET readers keep working; the PUT body is still `RiskLimits` (unknown fields are
  refused), so `RiskLimits.tsx` splits the two fields off before saving. The page refetches
  when the account or the agent state changes; it has no role, access, session or state
  check left (a Vitest scans its source for them).
- **6.15-a `components/ConfirmDialog`** (title, body as children, confirm label, optional
  busy label, icon, variant, `confirmDisabled`, `busy`; Escape / close / Cancel = cancel)
  is the old AgentPanel modal plus its footer. It runs `onConfirm` once: the button is
  disabled while the returned promise runs. AgentPanel's kill and reset dialogs use it.
- **6.15-b Pages.** System (restart), ApiKeys (revoke), Themes (delete), Users (delete, and
  the password reset, which was a `window.prompt`, now a password field in the dialog) and
  Accounts (delete) ask in it. Following the owner's convention, the touched actions became
  compact icon buttons with `title` and `aria-label` in the first column (ApiKeys and Users
  tables, Themes cards; Accounts already had them).
- **6.15-c ESLint** bans `window|globalThis|self.confirm|alert|prompt`
  (`no-restricted-properties`) and the bare globals (`no-restricted-globals`).
  `src/lint.test.ts` lints planted calls with the project's config and expects one error
  each.
- **Open, not in scope:** `Themes.tsx` still derives `canEdit` from the account's access in
  React (the same pattern 6.14 removed from Risk limits); a later step could return it from
  `GET /themes`.

### Steps 6.11, 6.12, 6.16: decisions (2026-11-24)

No new dependency, no migration, no contract change, no eToro path, no change to an order
path or a guardrail. Web only, plus one Go test and the docs.

- **6.11-a `useLive(accountId)`** (`web/src/lib/live.ts`). One `EventSource` per account
  (`/api/v1/live?accountId=`; the dashboard keys it by account, so a switch starts afresh).
  An error, or a bye `unavailable` / `shutdown`, closes the source and reconnects after 1, 2,
  4 … 30 s (the browser's own retry is never used: the source is closed first). A bye
  `lifetime` reconnects at once without counting as a failure; `signed_out` and `no_access`
  end the view for good with a message. The failure count resets on the first **snapshot**,
  not on `open` (a stream that opens and closes at once still backs off).
- **6.11-b Fallback.** There is no REST snapshot endpoint and none was added: after 3 failures
  the hook ticks `refresh` every 15 s (the agent panel refetches `GET /agent`, which reads
  equity through the 10 s view cache) and reads the orders feed with `GET /orders?limit=20`,
  while the stream keeps being retried at the capped backoff. The figure cards keep the last
  snapshot and show the stale badge. The first snapshot ends the fallback.
- **6.11-c Hidden tab.** 60 s after the tab is hidden the stream closes and polling stops
  (state "paused"); showing the tab refetches and reconnects at once.
- **6.11-d Stale badge** when no snapshot arrived for 30 s (counted from the connect before
  the first one), or when the server's snapshot says `stale` (no portfolio, or one older than
  30 s); its title says which.
- **6.11-e AgentPanel** takes an optional `live`. With it the panel has no 15 s interval; it
  refetches when the stream's agent facts change (state, kill and its reason, daily latch,
  reconciliation, foreign lease) and on every `refresh` tick. Without it (no stream) it polls
  as before. **Its buttons are its own REST calls**: the Dashboard test fails the stream three
  times, then pulls the kill switch and sees the POST.
- **6.11-f The Dashboard no longer calls `GET /broker/portfolio`** (one direct eToro read per
  page open): positions come from the snapshot. The old "not a real long x1" badge (React
  judging an order shape, and wrong for D19 shorts) became plain facts: long / short, `CFD`
  when not the real asset, `xN` when the leverage is not 1.
- **6.11-g Arithmetic in React** is limited to presentation: a bar's width (part / whole,
  clamped; for the floors the whole is start-of-day − daily floor and high-water mark −
  drawdown floor), the colour of a bar (≤ 0 = "no new entries" as the contract defines it,
  < 25 % = warning) and the stale timer. Every figure is the server's.
- **6.12-a Ranges.** Period buttons 1W / 1M / 3M / 1Y (7, 30, 90, 365 risk days) drive the
  summary, the daily figures and the chart. The trades table has its own From / To date
  fields (the user's calendar days; To is inclusive, sent as the next local midnight). The CSV
  links carry `accountId` and those dates when set, otherwise the period's `from` / `to` as the
  server returned them in the summary (no date arithmetic for the period in React).
- **6.12-b Equity curve** from `GET /overview/history?days=` filtered to the account (no new
  endpoint). `EquityCharts` gained an optional `drawdown`: the area between the running peak
  and the equity is shaded (a picture; the drawdown figures are the summary's). The Overview
  is unchanged (no shading).
- **6.12-c Wording and unknowns.** No trades: "No closed trades in this period.", the win rate
  "—" with "no closed trades", wins / losses and net "—" (never 0 %). Fees: all unknown →
  "unknown"; some unknown → the known sum with "unknown for N trades" (per day: "+ unknown");
  a trade's unknown fee → "unknown". Foreign trades are listed with "not opened by
  cactusTrade" and the summary says how many were not counted (OD15). The fixed note
  "Figures describe past results only; they do not predict future results." sits at the top.
- **6.12-d Paging** asks for 50 trades and pages with `before` = the last row's `closedAt`
  (the 5.7-b caveat applies: trades closed in the same instant across a page boundary may be
  skipped).
- **6.16-a The guard** is `cmd/cactustrade/wording_test.go` (`internal/app` and
  `internal/domain` may not import `os`). It scans `.md .ts .tsx .css .html .yaml .yml .json`
  under `web/src` (not `node_modules` / `dist`), `README.md`, `docs/` and `api/openapi` for
  20 case-insensitive patterns (`bannedWording`: the step's list plus similar claims about
  results, risk and income; kept only in the test so that this plan does not need an allow
  entry per pattern). An
  allowed line is (file, text it contains, reason); an entry without a reason or matching no
  line fails the test. Four entries today: the README risk warning, this plan's rule (twice)
  and the ARCHITECTURE line naming the guard. Planted phrases (nine, in an `fstest.MapFS`
  page) each fail; a `web/src/pages/Planted.tsx` with the step's example phrase was also
  planted in the real tree and failed the test (then removed).
- **6.16-b Docs.** `docs/etoro-api.md`: resends per review-M3 (an open is sent once; U1 and
  U7 rows updated), the position cap (8 by default, at most 15), a "Trade history as
  implemented" section with every field read and its use. ARCHITECTURE: the `Trades` port
  and "Live view and history (phase 6)". README: status and "Live dashboard and history".

Verification (2026-11-24): `make check` green: gofumpt, golangci-lint 0 issues, vet,
`go test -race` with coverage domain 99.8 % / app 89.0 %, MariaDB `test-int`, govulncheck
(no vulnerabilities), contracts in sync, `pnpm -C web check` (21 files, 116 tests; the live
Dashboard and History tests passed 6 runs in a row), `TestNoProfitabilityClaims` green.

### Phase 6 verification (6.17, 2026-11-24) — awaiting the owner's review

All of 6.1–6.16 are done; `make check` is green as above (coverage domain 99.8 %, app
89.0 %; MariaDB `test-int` includes `trades`, its cascade and migrations up→down→up). No new
dependency, one new migration (00011 `trades`, both dialects), no new eToro path (the
allowlist and its tests are unchanged), no order path changed; the view cache, the stream and
the history are views only (`TestEntriesNeverUseTheViewCache`, `TestHistoryLoopIsReadOnly`,
`TestShutdownWithOpenStreams`).

**The owner's smoke run (not done by an agent; demo account only):**

1. Open the Dashboard on a demo account and leave it open for **35 minutes**, across one
   stream lifetime (30 min): the badge goes `live` → (bye `lifetime`) → `live` again within a
   second or two, without a reload; figures keep updating; no `stale` badge while cactusTrade
   runs normally.
2. With the dashboard open in two tabs, pull the **kill switch** from one: it completes, both
   tabs show the agent `killed` without a reload. Repeat with the stream stopped (e.g. block
   `/api/v1/live` in the browser's dev tools): after three failures the badge says "stream
   down · refreshing every 15 s" and the kill switch still works.
3. History → download each CSV (trades, daily, equity, orders, decisions) and open it in a
   spreadsheet: no cell is evaluated as a formula, amounts are plain numbers, times UTC.
4. Hide the dashboard tab for over a minute, then show it: the stream reconnects.
5. Check the History page's wording and the unknown fees against a real demo history
   (RU5: whether `netProfit` is after fees is still unverified).

Open for the owner: OD14–OD18 remain defaults until answered; `Themes.tsx` still derives
`canEdit` in React (6.15 note); the trade pager's same-instant caveat (6.12-d).

## Phase 7 — hardening and the real-mode gate — `done up to 7.23 (7.18 = D39); 7.24 needs the owner (first real run)`

Order: hardening (7.1–7.9) → gate machinery with real trading still **impossible** (7.10–7.17)
→ **owner's written consent (7.18, done: D39)** → lifting the refusals (7.19–7.23, done
2026-11-27) → owner's first real run (7.24, blocked: owner only). Claude never runs a real-mode order (CLAUDE.md §2).
New dependency: `filippo.io/age` (BSD-3-Clause, 7.6). New migration 00012 (`real_gates`).

### Unverified eToro facts for real mode (RU1–RU10, settle before 7.18)

U1–U11 (demo, step 4.17) must be settled first.

- **RU1:** the real paths for open, close, cancel, lookup and close-orders come only from spec v1.419.0, as cached on 2026-11-23. They have never been called. Re-run `make etoro-spec`. The spec also has `/api/v3/trading/execution/orders`, so v2 may be deprecated.
- **RU2:** real key rules: Write permission, SMS, IP allow-list and expiry.
- **RU3:** real eligibility for `real`/long/×1 shares and `cfd`/short/×1, including the CFD appropriateness test and the owner's jurisdiction.
- **RU4:** whether `credit` on a real account includes **unsettled** sale proceeds (settlement of real shares). This decides §2.2.
- **RU5:** real fees: ticket fees, FX on non-USD shares, stamp duty, CFD overnight fees. The `costs` endpoint is meant to cover them; whether `netProfit` is after `fees` is unknown.
- **RU6:** the minimum order amount on real.
- **RU7:** whether a stop set **at open** on a real CFD short beyond eToro's maximum stop distance moves balance into margin, as the PATCH warning describes. If it does, that is money movement.
- **RU8:** the same quota groups on real.
- **RU9:** extended-hours fills.
- **RU10:** whether real `orders:lookup` takes `referenceId` the same way as demo.

**RU results, 2026-11-27** (`etoro-probe --real-read` on Real Money Maker, AAPL and BARC.L, read only; D39):
- RU1 settled: spec v1.427.0 has every allowlisted real path; v2 create-order is not deprecated (v3 exists, async).
- RU3 settled: real eligibility answers `real`/long/×1 **yes** and `cfd`/short/×1 **yes** (`isPotential` false), minimum $10 each; the owner's CFD appropriateness is passed.
- RU5 settled for what `costs` reports: long AAPL $100 → spread $0.02; short → transactionFee $0.15 + spread; long BARC.L → spread $0.03 + **sdrt $0.50** (UK stamp duty 0.5 %); overnight fees in GBX (converted, R36). Every line goes through the cost gate.
- RU6 settled: minimum position $10 long and short (R38 minimums apply).
- RU7 settled for cactusTrade: a real short's `maxStopLossPercentage` is 50 %; our stops are a few % above entry, far inside it, so no stop is "beyond the maximum"; real shorts are 0 % by default anyway (OD25).
- RU8 partly: the same read quota groups answered on real without 429; execution quota is seen at 7.24.
- RU2 (key permissions: the owner's real keys need **Write** for trading), RU4 (unsettled proceeds in `credit`), RU9 (cactusTrade trades only in session), RU10 (`orders:lookup` by `referenceId` on real) settle at the owner's first real run (7.24), accepted under D39.


**Owner, 2026-11-25:** "i still need to see how the platform performes before i allow real money trading". Real trading stays off; 7.18 waits for an observation period on demo (the Analysis page, R31, and the statements, R30, are the record). 4.17 evidence so far: kill drill run by the owner 2026-11-25 08:51 UTC with **no** positions open (the 2-position drill, a short, and a restart mid-position are still open: they need the agent trading in session).

### Real-mode refusal sites (refusing until 7.18; lifted 7.19–7.23 behind the gate, the opt-in and a permit; tests that prove it)

| # | Site | Refuses (since the lift) | Tests | Lifted in |
|---|---|---|---|---|
| S1 | `domain.AssessEntry` (`risk.go`) | a real entry without an entry permit valid for the account at `Now`, or limits over `RealCeilings`; any mode but demo/real | `TestAssessEntryRefuses` "real mode without a permit" / "no mode", `TestRealRefusalDomain`, `TestRealEntryPermitProperties` | 7.19 — **done** |
| S2 | `domain.IntentFor(o, permit)` (`orders.go`) | a real intent without an entry permit valid when the order was created | `TestIntentFor`, `TestRealRefusalDomain`, `TestRealEntryPermitProperties` | 7.19 — **done** |
| S3 | `etoro.Client.mutate` (`execute.go`) | a real change without a permit in ctx for the account and op (open: entry; close/cancel: exit or entry), or with the deployment's demo keys; before I/O | `TestRealRefusalEtoroClient`, `TestRealMutationsRefusedWithoutPermit`, `TestRealMutationsWithPermit` | 7.20 — **done** |
| S4 | `fakes.SimBroker.gate` | mirrors S3 | `TestSimBrokerRealPermits`, `TestSimBrokerGatesAndReads` | 7.20 — **done** |
| S5 | `app.mayMutate` → `mayExit` (closes/cancels of ours: exit permit, whatever the gate) / `mayOpen` (entries: `realEntryPermit`) | a real entry while `realEntryBlocked` says why; an exit of anything not ours | `TestExecutorRefusals`, `TestRealRefusalApp`, `TestRealExitsWhateverTheGate`, `TestRealEntryWithPermit` | 7.21 (exits), 7.22 (opens) — **done** |
| S6 | `app.supervisable` / `superviseTick` / `supervise` | a real account that is not armed and holds nothing of ours (skipped) | `TestRealSupervisable`, `TestRealExitsWhateverTheGate`, `TestSlowGateSweepDoesNotDelayExits` | 7.21 — **done** |
| S7 | `app.flatten` (kill, force close) and `forceClosable` | nothing on real any more (exits) | `TestRealKillClosesAllOurs`, `TestForceCloseRealAccount` | 7.21 — **done** |
| S8 | `app.startable` (Start) + `resume` (OD5) | start while `realEntryBlocked`; a real agent **never** resumes by itself (OD26) | `TestRealRefusalApp`, `TestRealAgentsNeverAutoResume` | 7.22 — **done** |
| S9 | `app.agentGate` + `agentTickAll` (+ `decide`: a permit for `AssessEntry`) | ticks while `realEntryBlocked` | `TestRealRefusalApp`, `TestRealEntryWithPermit`, `TestAgentSequencePropertiesReal` | 7.22 — **done** |
| S10 | `app.controlsFor` (`Gated` = `realEntryBlocked` says why; only Start is off) | the start control | `TestRealRefusalApp`, `TestAgentCards`, `TestDefaultConfigRefusesRealEntries` | 7.22 — **done** |
| S11 | HTTP `ErrRealModeGated` → 403 (`handlers_agent.go`, `handlers_realgate.go`) | API (start while blocked; arming in a build without consent) | `TestRealRefusalHTTP`, `TestRealGateEndpoints` | stays |
| — | **Not lifted:** `set_sl_tp` (`executable` map, `ErrNotEnabled`, on every account); `cmd backtest` refusing a real account; `streamAccount` preferring demo keys; full autonomy never auto-starts a real agent (OD26) | — | `TestRealMutationsRefusedWithoutPermit` (`set_sl_tp`), existing | never in phase 7 |

**Pre-consent proof (7.17), converted by the lift (7.19–7.23):** before D39 every site refused
with a gate *armed*, a reconciled real account with keys, real limits, its agent forced to
running and `CACTUSTRADE_REAL_TRADING=on` in the environment. Since the lift the same tests
prove that every **entry** site refuses whenever one key is missing: `TestRealRefusalDomain`
(S1, S2: no permit, another account's, expired, exit-only; a locked, never-armed, expired or
restored gate cannot build one), `TestRealRefusalEtoroClient` (S3: no permit in ctx, every
mutating allowlist op, 0 requests), `TestRealRefusalApp` (S4, S5 entries, S8–S10 with: armed but
opt-in off; opt-in on but never armed; not reconciled; limits over the ceilings; expired;
restored; disarmed in this process — 0 `Open`), `TestRealRefusalHTTP` (S11: armed, opt-in off →
403 start with the reason; kill and force close available). Exits are proved by
`TestRealExitsWhateverTheGate`, `TestRealKillClosesAllOurs` and `TestForceCloseRealAccount`.
`TestRealRefusalModeReferences` (`cmd/cactustrade`) lists every `ModeReal`/`ModeDemo` reference
in non-test code with its role (each lifted line reviewed and annotated) and fails on a new one.

| ID | Step | Files | Acceptance tests | Status |
|---|---|---|---|---|
| 7.1 | **Security review pass (reviewer agent, read-only).** Scope: auth/sessions/CSRF, the SSE stream, CSV exports, authorization per account, secrets in logs/errors/fixtures, allowlists of all 4 broker adapters, depguard layering, SSRF (LLM base URL, SMTP host, Saxo redirect), headers, container settings, `deploy.sh`. Ranked findings go into PLAN ("Phase 7 security review") with a fix step `7.1.n` per BLOCKER/MAJOR (planned after the review). | PLAN | Findings table exists; every BLOCKER/MAJOR has a step id; `make check` green | done (findings: "Phase 7 security review"; 7.1.1–7.1.17 fixed; 7.1.14 with 7.5, 7.1.17 with 7.7, 7.1.13/15/16 with 7.8–7.9) |
| 7.2 | **Mode and account in every log line.** A `slog.Handler` wrapper adds `account_id` and `mode` from `ports.AccountFrom(ctx)` to each record logged with a context. The request log adds the current account's mode. A source test bans non-`Context` log calls in `internal/app` and `internal/adapters/etoro`. Orders already store `mode`; `trades` has it (6.4). | `internal/adapters/logctx/` (new) or `cmd/cactustrade/log.go`, `internal/app/*.go` (call sites), tests | `go test ./...`: executor and supervisor test logs carry `mode=demo` / `mode=real` and the account id; a planted `s.d.Log.Info(` fails the source test; no key / token / cookie in any captured line. | done (D31; `TestLogLinesNameAccountAndMode`, `TestRequestLogNamesAccountAndMode`, `TestNoContextFreeLogCalls`; a planted `s.d.Log.Info(` in `internal/app/system.go` failed the source test, checked by hand and reverted) |
| 7.3 | **API rate limits.** Token buckets per principal (user id / API key id) and per IP on `/api/*`: reads 600/min, mutations 60/min, exports 10/min, stream connects 30/min (OD33). **Kill, pause, stop and disarm are never throttled** (§2.6). Bounded memory (≤ 10 000 keys, purged by `PurgeLoop`). 429 problem+json with `Retry-After`; metric. | `internal/adapters/http/{ratelimit.go,ratelimit_test.go,server.go}` | `go test ./internal/adapters/http -run RateLimit`: limits per class; with every bucket exhausted, `POST /agent/kill` is still 200; 10 001 keys keep memory bounded. | done (D32; `TestRateLimit*`: per class, both buckets or none, kill/pause/stop 200 with every bucket of the address, user and API key exhausted, 10 001 keys ≤ 10 000) |
| 7.4 | **Headers, cookies, body limits.** Add `Permissions-Policy`, COOP `same-origin`, CORP `same-origin`; check the CSP has no `unsafe-inline` outside Swagger; `MaxBytesReader` 1 MiB on JSON bodies; optional `__Host-` cookie prefix (OD32). | `internal/adapters/http/{middleware.go,server.go,handlers.go}`, `server_test.go` | Header table test over route classes (SPA, API, Swagger, SSE, CSV, `/metrics`, 308 redirect); a 2 MiB body → 413. | done (D33; `TestSecurityHeadersByRouteClass` over SPA, API, Swagger, SSE, CSV, `/metrics`, 308; `TestBodyLimits`: 2 MiB → 413, also chunked and on the kill route; `TestHostPrefixedSessionCookie`; `TestRedirectHasSecurityHeaders`. **The `__Host-` cookie signs everyone out once** (README). Also fixed a pre-existing web flake: `Dashboard.test.tsx` "caps the backoff at 30 s" checked a 1 ms boundary under `shouldAdvanceTime` and failed about every second `make check`; it now checks 29 s / 30 s (64 s uncapped, so the cap is still proven)) |
| 7.5 | **Observability.** The app exposes `TradingHealth()` (no prometheus import, depguard). The HTTP adapter registers a Collector that reads it at scrape time: agent state, killed, reconciled, last successful supervisor pass, lease held, non-final orders by state, real-gate armed (7.12) per account id. Plus counters: order transitions (from the bus), eToro requests by op/status class/mode (`etoro.Config.Observe` hook), limiter waits, LLM calls by purpose/outcome, bus drops. `GET /api/v1/system/health` (admin): every loop alive (supervisor pass < 60 s). Runbook alert rules. | `internal/app/health.go`, `internal/adapters/http/metrics.go`, `internal/adapters/etoro/client.go`, `cmd/cactustrade/main.go`, tests | `go test ./internal/adapters/http -run Metrics`: the series are present; labels are bounded (account ids only); no label or value matches a key pattern; a stalled supervisor → health 503 with the account. | done (D34; `TestMetricsTradingSeries` (every series and value, label sets from a fixed table, account labels only real account ids, no label or value matches the key pattern, planted eToro keys / metrics token / password / CSRF token absent), `TestMetricsStalledSupervisorHealth` (200 → 61 s without a pass → 503 naming the account in `problems`, `account_healthy 0`, `trading_healthy 0` → a pass → 200; viewer / operator 403, anonymous 401), `TestTradingHealth*`, `TestSystemHealth`, `TestObserveEveryRequest`, `TestObserveLimiterWaits`, `TestStatusClass`. Alert rules in the new `docs/RUNBOOK.md` (7.9 adds the incident procedures). `real_gate_armed` is always 0 until 7.13 fills `AccountHealth.RealGateArmed`) |
| 7.6a | **Encrypted app backup and restore (standalone).** `cactustrade backup --out F`: SQLite `VACUUM INTO` a temp file, then age-encrypted to `CACTUSTRADE_BACKUP_RECIPIENT` (refused without a recipient, OD29). `cactustrade restore --in F --yes`: refuses while a server holds the data dir (lock file + live lease), checks the schema version, writes setting `restore.at` (7.12 disarms every real gate older than it). The master key is never in a backup. | `cmd/cactustrade/{backup.go,backup_test.go}`, `internal/adapters/sqlstore/backup.go`, go.mod | `go test ./cmd/cactustrade -run 'Backup\|Restore'`: round trip keeps row counts and migration version; a stored secret still decrypts with the master key; the archive (decrypted) does not contain the master key bytes; a wrong age identity fails; restore while serving → refused. | done (2026-11-24; `make check` green incl. MariaDB test-int and `make audit`) |
| 7.6b | **MariaDB backup and restore + restore test.** `deploy.sh --backup-now` pipes `mariadb-dump` through `cactustrade encrypt` (age) when a recipient is set (else today's 0600 gzip with a warning). `deploy.sh --restore FILE` stops the app, restores after a typed confirmation, then starts the app (start-up reconciliation runs first, 4.14). | `deploy.sh`, `cmd/cactustrade/encrypt.go`, `internal/adapters/sqlstore/integration_test.go` (test-int), README | `make test-int`: dump → restore into a fresh MariaDB container → counts and `goose` version equal; `deploy.sh --restore` dry-run on Docker Desktop documented in PLAN. | done (2026-11-24; `make check` green incl. MariaDB test-int and `make audit`) |
| 7.7 | **Dependency audit.** `make audit`: `go mod verify`, govulncheck, a licence check over `go list -m -json all` + each module's LICENSE (allowed: MIT, BSD-2/3, ISC, Apache-2.0, MPL-2.0, public domain/Unlicense; others fail with the module name), `pnpm licenses list --prod` with the same rule, `pnpm audit --prod --audit-level=moderate`. Joins `make check`. PLAN dependency table brought up to date. | `tools/licencecheck/` (Go, stdlib only), `Makefile`, PLAN | `make audit` green; the licence checker's own test fails on a GPL-3.0 fixture module. | done (2026-11-24; `make check` green incl. MariaDB test-int and `make audit`) |
| 7.8 | **Vault `SecretStore` (optional, OD30).** KV v2 over `net/http` (no SDK). Token from a file or Kubernetes auth (JWT file); CA file; no redirects; write-only (Get only for the app's own use, never returned by the API); env overlay still wins. `CACTUSTRADE_SECRETS=db\|vault` (default db). `cactustrade secrets migrate --to vault`. | `internal/adapters/vault/`, `cmd/cactustrade/main.go`, `.golangci.yml` (depguard), ADR-003 update | `go test ./internal/adapters/vault`: fake Vault (`httptest`) for get/put/delete/list, 403/404/5xx, token renewal; no token in any error or log; a redirect is refused; coverage ≥ 90 %. | done (2026-11-24; decisions: "Steps 7.8–7.9 and 7.1.13–7.1.16"; vault coverage 97.2 %) |
| 7.9 | **Releases and runbook.** `make dist` (linux/darwin/windows × amd64/arm64, `SHA256SUMS`, signature per OD31), `setup.sh` / `setup.ps1` as cactusInv. `docs/RUNBOOK.md`: eToro API down, kill switch tripped, reconciliation mismatch, lease held by a dead process, restore, **disarm real trading now**. | `Makefile`, `scripts/`, `docs/RUNBOOK.md` | `make dist` builds 6 binaries; signature verifies in a script test; `make check`. | done (2026-11-24; `make dist` built 6 binaries with a throw-away key; the owner publishes the release key, RUNBOOK "Releases") |
| 7.10 | **Domain: demo evidence and the gate (real still impossible).** `const RealTradingEnabled = false` ("needs the owner's written consent, PLAN 7.18"). `DayFacts{Day RiskDate, Decisions, FilledOpens, OpenedPositions, ClosedSameDay}` → `QualifyingDays(facts, rule)` per OD19. `GateEvidence{Days, Count, First, Last}`. `GateChecks(in) []GateCheck{Name, Pass, Detail}`: real account (eToro, mode real, keys, reconciled, not killed, agent stopped, limits valid and ≤ real ceilings); linked demo account (eToro, mode demo, same owner, OD21); ≥ 20 qualifying days in the last 60 risk days, the latest ≤ 7 days old (OD20); arming user is admin and owner (OD22); phrase `ENABLE REAL TRADING <account name>`. `CanArm` = every check passes **and** `RealTradingEnabled`, else `ErrRealModeGated`. `RealGate` aggregate (locked / armed, linked demo id, armed at/by, evidence, disarmed at/by/reason, expires at (OD24), version). | `internal/domain/{realgate.go,realgate_test.go}` | `go test ./internal/domain -run 'Gate\|QualifyingDays\|RealTradingDisabled'`. `TestRealTradingDisabledInThisBuild` asserts the constant is false (changing it changes this test: a visible, reviewed edit). Properties: a day without a decision, without a filled demo open, or with a position held past its risk day never qualifies; adding facts never lowers the count; 19 days → fails; 20 days but the latest 8 days old → fails; every check passing still gives `ErrRealModeGated`. | done (2026-11-24; `TestRealTradingDisabledInThisBuild`, `TestQualifyingDays*`, `TestGateChecks`, `TestGateProperties`, `TestRealGate`; decisions below) |
| 7.11 | **Domain: real-mode limits (OD25).** `DefaultRealRiskLimits` and `RealCeilings`; `RiskLimits.ValidateFor(mode)`. Raising any limit on a real account is a disarm reason (`RaisesAny(old, new)`). | `internal/domain/{risk.go,risk_test.go}` | `go test ./internal/domain -run 'RealLimits\|RaisesAny'`: every field over the ceiling is refused; a real account's MaxShort > 25 % refused (D19 unchanged); `RaisesAny` property against field-by-field comparison. | done (2026-11-24; `TestRealLimits`, `TestRaisesAny`) |
| 7.12 | **Migration 00012 `real_gates` + repositories.** `real_gates` (account_id PK FK cascade, state, demo_account_id, armed_at, armed_by, expires_at, evidence TEXT JSON, disarmed_at, disarmed_by, disarm_reason, version, updated_at). `RealGates{Get, Put(versioned)}`; `Orders.ListSince(account, since)`, `AgentPositions.ListSince(account, since)`, `Decisions.CountByDay`? → no: the app folds rows from `Decisions.List` paged (≤ 60 days) into `DayFacts`. | `migrations/{sqlite,mariadb}/00012_real_gate.sql`, `internal/ports/ports.go`, fakes, `internal/adapters/sqlstore/realgate.go`, `storetest/realgate.go` | `make test && make test-int`, `TestMigrationParity`; shared suite: a stale version → `ErrConflict` (a disarm is never overwritten by an older arm); cascade; `ListSince` bounds. | done (2026-11-24; shared suite `real_gates`, `evidence_windows`, `real_gates_cascade` on SQLite and MariaDB) |
| 7.13 | **App: gate use cases (arming still refused).** `RealGateStatus(p, realAcct)` (view access): checks + evidence days. `ArmReal(p, realAcct, demoAcct, confirm, password)`: `mode.switch` permission (admin), web session, owner, password re-entered (argon2 verify, counted by the sign-in limiter), phrase, `GateChecks`, then `CanArm` → **`ErrRealModeGated` while the constant is false**. Audit `real.arm_refused` with the failing checks. Internal `s.armLocked` (persist, audit `real.arm`, danger event, e-mail admins + owner) exists for tests only via `CanArm`. `DisarmReal(p, acct, reason)`: trade access + `agent.kill`, session or API key; idempotent; agent → stopped if running/paused (entries stop, **exits continue**); audit, danger event, e-mail. Auto-disarm: kill trip, key change, `RaisesAny`, `restore.at` newer than `armed_at`, expiry. `Status` adds `RealArmed []{id, name}` for visible accounts. | `internal/app/{realgate.go,realgate_test.go,agent_control.go,supervisor.go,risk.go,accounts.go,system.go}` | `go test -race ./internal/app -run 'RealGate\|ArmReal\|DisarmReal'`. Role × via × owner matrix; wrong phrase 422; wrong password 403 and limited after 5; 19 qualifying days → refused with that check; 20 days → `ErrRealModeGated` and **no row stored as armed**; using `armLocked` directly: a key change, a kill, a limit raise, a restore and expiry each disarm; disarm from an API key works; disarm never cancels a close in flight. | done (2026-11-24; `TestArmRealRoleViaOwnerMatrix`, `TestArmRealRefusals`, `TestRealGateStatus`, `TestDisarmReal`, `TestRealGateAutoDisarm`, `TestRealGateDisarmLatched`, `TestRealGateBannerAndHealth`, `TestRealGateMail`, `TestDemoEvidenceReadsADayAtATime`) |
| 7.14 | **Contract and handlers.** `GET /api/v1/accounts/{id}/real-gate`, `POST …/real-gate/arm` `{demoAccountId, confirm, password}` (session only), `POST …/real-gate/disarm` `{reason}` (session or key); `SystemStatus.realArmed`. `ErrRealModeGated` → 403 "Real trading is not enabled in this build". | `api/openapi/cactustrade.yaml`, `internal/adapters/http/{handlers_realgate.go,realgate_test.go}`, `schema.d.ts` | `make gen && go test ./internal/adapters/http -run RealGate`: 200/403/409/422; CSRF on both mutations; API key refused for arm, allowed for disarm; the password never appears in logs (captured handler); drift green. | done (2026-11-24; `TestRealGateEndpoints`, `TestRealGateAPIKeys`, `TestRealGateWrongPasswordsAreLimited`, `TestRateLimitNeverThrottlesDisarm`; drift green) |
| 7.15 | **Web: Real trading page and banner.** Admin page *Real trading* (Settings): one card per real account with the server's checklist (pass/fail + detail), the evidence days, the demo account picker, an arm dialog (typed phrase + password; shows the server's refusal: "not enabled in this build"), and Disarm for trade-access users (`ConfirmDialog`). **Banner on every authenticated page** from `status.realArmed`: red, not dismissible, "REAL MONEY — real trading armed on: …", in addition to R3's frame (OD27). | `web/src/pages/RealTrading.tsx` + test, `components/{Layout.tsx,RealBanner.tsx}` + tests, `lib/{api.ts,mode.tsx}` | `pnpm -C web check`. Vitest renders `Layout` with every route: the banner is present when `realArmed` is non-empty and absent otherwise; the arm button needs both fields; no gate logic in React (only server checks shown). | done (2026-11-24; `RealTrading.test.tsx`, `RealBanner.test.tsx` over every route of `App.tsx`) |
| 7.16 | **Docs.** ARCHITECTURE "Real-mode gate"; ADR-007 (gate, evidence rule, two keys + consent); README warning; RUNBOOK disarm. | docs | `make check` | done (2026-11-24; ARCHITECTURE "Real-mode gate", ADR-007, README "Real money"; the RUNBOOK disarm procedure is 7.9's, see decisions) |
| 7.17 | **Real refusal matrix (pre-consent proof).** One test per layer with a real account that has keys, a gate made `armed` through `armLocked`, is reconciled, has its agent forced to running, valid limits and a deployment opt-in set: S1–S11 all still refuse; `AgentTick` → 0 `Open`; `placeEntry`, `closePosition`, `cancelOrder` → 0 broker calls; `Supervise` skipped; `flatten` and `ForceClose` → 0 calls; `controls.gated = true`; HTTP 403; `etoro.Client` → 0 HTTP requests; a source test lists every reference to `ModeReal`/`ModeDemo` in non-test code and fails on any new site not in this table. | `internal/app/real_refusal_test.go`, `internal/domain/real_refusal_test.go`, `internal/adapters/etoro/real_refusal_test.go`, `internal/adapters/http/realgate_test.go` | `go test -race ./... -run RealRefusal`; `make check`. | done (2026-11-24; `go test -race ./... -run RealRefusal`; "Pre-consent proof" under the refusal table) |
| 7.18 | **OWNER: written consent to enable real trading.** Only the owner can complete this. Preconditions, all `done`: 2.7 (done 2026-11-24), 3.6 (done 2026-11-24), 4.17 (U1–U11 settled; open), 5.8 (done 2026-11-24); RU1–RU10 settled read-only with the owner's real **read** keys (owner run); every 7.1 BLOCKER/MAJOR fixed; OD19–OD28 answered. Record the owner's words, date and the accepted limits as a new D-number. Nothing after this row starts before it. | PLAN, `docs/adr/ADR-007` | The owner's written consent in PLAN | **done 2026-11-27: D39** (the owner's written consent; earlier text kept below). 2026-11-27 the owner asked "now enable to trade with real money"; recorded as the request, not yet the consent: still open are 4.17 (U1/U3/U5/U7/U9/U10, close recordings), RU1–RU10 (owner's real read keys), the owner's answers to OD19–OD28 (accept the defaults or change them) and the accepted real limits; the in-app gate then still needs 20 qualifying demo days (OD19/OD20; first demo day 2026-11-24) |
| 7.19 | *(after 7.18)* **Domain lift.** `RealTradingEnabled = true` (the consent quoted in the commit). `RealPermit` built only by `PermitEntry(gate, account, now)` (armed, not expired, same account, ≤ 60 s old) and `PermitExit(account, ours)`. `AssessEntry` allows real only with a valid entry permit and limits within `RealCeilings`; `IntentFor(o, permit)`. | `internal/domain/{realgate.go,risk.go,orders.go}` + tests | Properties: real is never allowed without a valid entry permit (missing, other account, expired, exit-only, locked or expired gate); demo behaviour unchanged (existing properties green). | done (2026-11-27; `TestRealTradingEnabledByOwnerConsent` (was `TestRealTradingDisabledInThisBuild`), `TestPermitEntryAndExit`, `TestRealEntryPermitProperties`, `TestRealRefusalDomain`, `TestGateChecks`/`TestGateProperties` (CanArm arms when all pass), `TestArmRealArms`; decisions "Steps 7.19–7.23") |
| 7.20 | *(after 7.18)* **Adapter lift.** `ports.WithPermit(ctx, p)`; `mutate` accepts real only with a permit for this account and op class (open needs entry; close/cancel accept exit or entry); real path from the allowlist, real keys only. SimBroker mirrors it. | `internal/adapters/etoro/{execute.go,execute_test.go}`, `internal/ports/{ports.go,fakes/simbroker.go}` | Golden real paths equal `Allowlist[op].Real`; bodies byte-identical in shape to demo (two shapes, leverage 1); without a permit → 0 requests; allowlist tests unchanged. | done (2026-11-27; `TestRealMutationsWithPermit` (real paths = `Allowlist[op].Real`, bodies byte-identical to demo, real keys), `TestRealMutationsRefusedWithoutPermit`, `TestRealRefusalEtoroClient`, `TestSimBrokerRealPermits`; allowlist tests unchanged) |
| 7.21 | *(after 7.18)* **App lift: exits first.** `mayExit` for real when the position/order is ours, **whatever the gate state** (a disarmed account still flattens). Supervisor includes real accounts that are armed or hold our positions/non-final orders; `flatten` for real (kill and force close). | `internal/app/{executor.go,supervisor.go,agent_control.go}` + tests | A disarmed real account with our position is closed at close − 10 min; a kill on an armed real account closes all of ours; 0 `Open` on any path. | done (2026-11-27; `TestRealExitsWhateverTheGate` (never armed / disarmed / opt-in off / expired: closed at close − 10 min, 0 `Open`), `TestRealKillClosesAllOurs`, `TestForceCloseRealAccount`, `TestRealSupervisable`) |
| 7.22 | *(after 7.18)* **App lift: entries.** `mayOpen`, `startable`, `agentGate`, `agentTickAll`, `controlsFor`: real requires an armed gate + `CACTUSTRADE_REAL_TRADING=on` (OD28) + reconciled + real limits; a permit per entry. Real agents never auto-resume (OD26). 4.12 extended: scenarios with an armed real account and random disarm / kill / opt-in-off / expiry events. | `internal/app/{executor.go,agent.go,agent_control.go,agent_view.go}`, `agent_property_test.go` | `go test -race ./internal/app -run 'AgentSequenceProperties\|Real'`: no `Open` after a disarm, opt-in off or expiry; exits continue; every real `Open` carries a valid permit; demo invariants unchanged. | done (2026-11-27; `TestRealEntryWithPermit`, `TestRealAgentsNeverAutoResume`, `TestRealRefusalApp`, `TestAgentSequencePropertiesReal` (40 seeds × 90 ticks: random disarm/kill/opt-in-off/expiry/restore and recovery; every real `Open` carries a valid permit, none while a key is missing, exits continue), `TestAgentSequenceProperties` unchanged) |
| 7.23 | *(after 7.18)* **Wiring.** `CACTUSTRADE_REAL_TRADING` (default off; `.env` off; `deploy.sh` never sets it); a start-up log line and a system event when on. | `cmd/cactustrade/*`, `.env`, `deploy.sh`, docs | `go test ./cmd/cactustrade`; default config → entries on real refused. | done (2026-11-27; `TestRealTradingOptInConfig`, `TestDefaultConfigRefusesRealEntries`, `TestAnnounceRealTrading`, `TestDeployRealTradingOptIn`; README "Real money", RUNBOOK "Disarm real trading now", ARCHITECTURE, ADR-007 "Lift") |
| 7.24 | **OWNER: first real run** (never by Claude). The smallest long the owner chooses on an NYSE share in session; sanitised real fixtures; disarm drill and kill drill with 1 position. | `testdata/`, `docs/etoro-api.md` | Real fixtures green; RU list closed | blocked: owner only |

### Phase 7 security review (7.1, 2026-11-24)

**D17 re-confirmed by the owner (2026-11-24):** asked about the review's finding that a fresh
install's admin/admin can be taken by anyone on the LAN before the owner signs in, the owner
chose "Keep admin/admin as is" and accepts the risk of that window. No change.

Reviewer agent pass, read-only (scope as in 7.1). Each fix below started with a test that
failed on the code as it was, then the fix. No BLOCKER was found.
Every MAJOR (F1–F6) and the MINORs F7–F12 are fixed; F14 was fixed with 7.5, F17 with 7.7, F13, F15 and F16 with 7.8–7.9 (2026-11-24). `make check`
green on 2026-11-24 (incl. MariaDB `test-int`); coverage domain 98.0 %, app 89.0 %.
Dry run of the k8s rendering: `./deploy.sh --render --k3s` (HTTPS: Service `ClusterIP`) and
`K8S_HTTPS=false ./deploy.sh --render --k3s` (NodePort + warning on stderr), covered by
`TestDeployRenderService`.

| # | Sev | Finding | Where | Fix step |
|---|---|---|---|---|
| F1 | MAJOR | An admin **API key** could create, update and delete users: `CreateUser`, `UpdateUser`, `DeleteUser` only authorised the permission, never `p.Via`. | `internal/app/users.go` | 7.1.1 |
| F2 | MAJOR | `DeleteAccount` refused only while the agent was running or paused: a killed agent with closes pending, or a stopped one with open agent positions / non-final orders, could lose its account row (and keys) under the supervisor. | `internal/app/accounts.go` | 7.1.2 |
| F3 | MAJOR | eToro keys could be swapped with positions open (a different eToro account behind the same id), and the account stayed "reconciled"; the global `PUT/DELETE /api/v1/credentials/{name}` wrote the env account's eToro keys without any account ownership. | `internal/app/{accounts.go,credentials.go}` | 7.1.3 |
| F4 | MAJOR | Unauthenticated memory exhaustion: every sign-in runs argon2id (64 MiB) with no bound on concurrency, and the limiter counted a failure only *after* hashing (50 parallel requests all hash). | `internal/adapters/http/handlers.go`, `internal/adapters/argon2` | 7.1.4 |
| F5 | MAJOR | Anyone could lock out any user: 5 failures for a username from anywhere refused that user everywhere; reset requests shared the sign-in IP counter; the reset-confirm limit was one global key; the limiter map was never swept or capped. | `internal/adapters/http/{limiter.go,handlers_email.go,server.go}` | 7.1.5 |
| F6 | MAJOR | k8s exposed the pod's plain HTTP on a NodePort even with `K8S_HTTPS=true` (ingress). | `deploy/k8s/cactustrade.yaml`, `deploy.sh` | 7.1.6 |
| F7 | MINOR | Saxo, Tiger, Trading 212 and the LLM client followed redirects (headers with keys travel to another host). | adapters, `cmd/cactustrade/main.go` | 7.1.7 |
| F8 | MINOR | `LLMModels` (sends a typed or stored key to a URL) was reachable with an admin API key. | `internal/app/llm.go` | 7.1.8 |
| F9 | MINOR | SSRF: an LLM key could be sent over plain `http` to any host; LLM base URLs and SMTP hosts could be link-local / cloud metadata addresses. | `internal/domain/{llm.go,email.go}`, `internal/app/llm.go` | 7.1.9 |
| F10 | MINOR | `ChangePassword` checked the current password with no rate limit (a stolen session could guess it). | `internal/adapters/http/handlers.go` | 7.1.10 |
| F11 | MINOR | `SetMyProfile` changed the e-mail address (where reset links go) without the current password: a stolen session could take the account over. | `internal/app/email.go`, contract, `web/src/pages/Account.tsx` | 7.1.11 |
| F12 | MINOR | depguard: no rule kept `internal/app` out of the eToro adapter (or the LLM adapter); `net`, `net/smtp`, `os/exec`, `crypto/tls` not denied in domain, app and strategy. | `.golangci.yml` | 7.1.12 |
| F13 | MINOR | Saxo OAuth `state` is not bound to the browser (no nonce cookie) and no PKCE. | `internal/app/connect.go`, `internal/adapters/saxo/oauth.go` | 7.1.13 (done) |
| F14 | MINOR | `/metrics` is open without a token in standalone mode. | `cmd/cactustrade/config.go` | 7.1.14 (done) |
| F15 | MINOR | The bundled Mailpit counts as a working mail server, so "reset by e-mail" is offered although the mail never reaches the user. | `internal/app/email.go` | 7.1.15 (done) |
| F16 | MINOR | k8s: no NetworkPolicy; trusted proxies hard-coded to k3s's pod CIDR; MariaDB without TLS / hardening options. | `deploy/k8s/cactustrade.yaml` | 7.1.16 (done) |
| F17 | MINOR | Images by tag, not digest; Node 20 in the web build. | `Dockerfile`, compose, k8s | 7.1.17 (fixed with 7.7) |
| — | note | The default admin/admin (D17) is unchanged: the owner is being asked about it. | — | — |

| ID | Step | Files | Acceptance tests | Status |
|---|---|---|---|---|
| 7.1.1 | User management needs a web session: `CreateUser`, `UpdateUser`, `DeleteUser` go through `userAdmin(p, target)`: permission, `ViaSession`, and (defence in depth) a key never targets its own owner. | `internal/app/users.go`, tests | `TestUserManagementNeedsSession`: key vs session × create/update/delete. | done |
| 7.1.2 | `DeleteAccount` only when the agent is stopped with 0 open agent positions and 0 non-final orders (`quiescent`); a killed agent → `MissingKillReset` (409), else 409 with the counts. | `internal/app/accounts.go`, tests | stopped + 1 open position → `ErrConflict`; killed with a close pending → `ErrConflict` (+ `MissingKillReset`). | done |
| 7.1.3 | eToro key change only while `quiescent`; afterwards the account is unreconciled ("keys changed"). The global credential writes are removed (decision D27). | `internal/app/{accounts.go,credentials.go}`, contract, handlers, `web/src/lib/api.ts` | key change with an open position → 409; after a change `Reconciled(id).OK == false`; `PUT`/`DELETE /api/v1/credentials/…` → 404/405 (`TestGlobalCredentialWritesRemoved`). | done |
| 7.1.4 | `argon2.Limited` (3 slots, 2 s wait → `domain.ErrBusy` → 429 + `Retry-After`); the limiter records an attempt before hashing (refunded on success); `GOMEMLIMIT=400MiB` in k8s and compose. | `internal/adapters/argon2`, `internal/adapters/http/{handlers.go,problem.go}`, `internal/domain/errors.go`, `cmd/cactustrade/main.go`, deploy files | 50 concurrent sign-ins never run more than 3 hashes at once; the rest get 429 with `Retry-After`. | done |
| 7.1.5 | Limiter per (user, IP) with a per-user slow-down instead of a global lock; reset requests and reset confirms in their own buckets (per user hourly, per IP); `Sweep` + 10 000-key cap from `PurgeLoop`. | `internal/adapters/http/{limiter.go,handlers.go,handlers_email.go,server.go}`, `cmd/cactustrade/main.go` | 5 reset requests for admin do not block admin's sign-in from another IP; bad reset tokens from IP A do not block IP B; 5 failures from IP A do not block the user from IP B; 10 001 keys stay ≤ 10 000. | done |
| 7.1.6 | k8s Service is `ClusterIP` when `K8S_HTTPS=true`; NodePort only in the explicit no-HTTPS mode, with a loud warning. `deploy.sh --render` prints the manifest without touching a cluster. | `deploy/k8s/cactustrade.yaml`, `deploy.sh`, `cmd/cactustrade/deploy_test.go` | `go test ./cmd/cactustrade -run DeployRender`: https → ClusterIP, no nodePort; http → NodePort + warning. | done |
| 7.1.7 | Every non-eToro HTTP client refuses redirects (`CheckRedirect` → `http.ErrUseLastResponse`). | `internal/adapters/{saxo,tiger,trading212,llm}` | one redirect test per client: the redirect target gets 0 requests. | done |
| 7.1.8 | `LLMModels` uses `llmAdmin` (web session). | `internal/app/llm.go` | API key → 403. | done |
| 7.1.9 | With a key: `https` base URL required (except `ollama_local` and localhost); link-local and metadata addresses refused for LLM base URLs and SMTP hosts; key-to-endpoint binding unchanged. | `internal/domain/{llm.go,email.go,netguard.go}`, `internal/app/llm.go` | table tests in domain and app. | done |
| 7.1.10 | `ChangePassword` runs the sign-in limiter on the current-password check. | `internal/adapters/http/handlers.go` | 5 wrong current passwords → the 6th is 429. | done |
| 7.1.11 | Changing the e-mail address needs the current password (`ProfileInput.currentPassword`; limiter as 7.1.10); the Account page asks for it and shows refusals with `ProblemAlert`. | contract, `internal/app/email.go`, `internal/adapters/http/handlers_email.go`, `web/src/pages/Account.tsx` + test | without / with a wrong password → 400 `currentPassword`; notify-only changes need none. | done |
| 7.1.12 | depguard: an `etoro` rule (no `internal/app`), `internal/app` denied for the LLM adapter; `net`, `net/smtp`, `os/exec`, `crypto/tls` denied in domain, app and strategy. | `.golangci.yml` | a planted import fails `make lint` (checked by hand, reverted). | done |
| 7.1.13 | Saxo OAuth: a nonce cookie binding `state` to the browser, and PKCE (S256). | `internal/app/connect.go`, `internal/adapters/saxo/oauth.go`, HTTP | a callback without the cookie → refused; the token request carries the verifier. | done (2026-11-24; Saxo accepting PKCE for code-flow apps unverified, docs/saxo-api.md) |
| 7.1.14 | `/metrics` needs the metrics token in standalone mode too (generated into the data dir when unset). | `cmd/cactustrade/config.go`, README | `/metrics` without a token → 401 in every mode. | done (with 7.5, D35; `TestMetricsNeedToken`: no / wrong token → 401, and 401 for every request when none is configured; `TestEnsureMetricsToken`) |
| 7.1.15 | Mailpit as the only mail server = "reset unavailable" (public status and the request say why). | `internal/app/email.go` | with Mailpit only, `passwordResetAvailable` is false. | done (2026-11-24; opt-in `CACTUSTRADE_SMTP_TEST_MAILBOX_RESETS`) |
| 7.1.16 | k8s NetworkPolicy (app ↔ MariaDB, ingress → app only), trusted proxies from `.env`, MariaDB hardening. | `deploy/k8s/cactustrade.yaml`, `deploy.sh`, `.env` | `deploy.sh --render` contains the policies; documented dry-run. | done (2026-11-24; dry run documented; MariaDB TLS not done, see decisions) |
| 7.1.17 | Images pinned by digest; Node 22 for the web build (with 7.7). | `Dockerfile`, compose, k8s | `make audit` (7.7). | done (2026-11-24; `make check` green incl. MariaDB test-int and `make audit`) |

### Steps 7.6–7.7: decisions (2026-11-24)

**7.6a: standalone backup and restore** (`cmd/cactustrade/{backup.go,encrypt.go,lock.go}`,
`internal/adapters/sqlstore/backup.go`).

- `cactustrade backup --out F [--recipient age1…]…`: `VACUUM INTO` a random temporary file in
  the (0700) data folder while the server keeps running, then age to the recipients, written
  `O_EXCL` with mode 0600 (an existing file is never overwritten); the plain copy is removed
  in every case. Recipients: `--recipient` flags, else `CACTUSTRADE_BACKUP_RECIPIENT` (spaces
  or commas between several), else the recipients file `CACTUSTRADE_BACKUP_RECIPIENT_FILE`.
  **No recipient → nothing is written** (OD29). X25519 (and age's hybrid) recipients only; SSH
  keys and plugins are not accepted (age's own advice for applications).
- The payload is the bare SQLite file (no envelope): the restore recognises it by SQLite's
  magic, which also turns a MariaDB dump away with a pointer to `deploy.sh --restore`.
- **The master key is never in a backup**: it lives in `master.key` / a secret file, never in
  the database; `TestBackupRestoreRoundTrip` decrypts the archive and checks that neither the
  raw key, its hex form (either case) nor a stored credential's plaintext appear, and that the
  credential still decrypts after a restore into a fresh folder holding the same master key.
- `cactustrade restore --in F --yes [--identity FILE]` (identity also from
  `CACTUSTRADE_BACKUP_IDENTITY_FILE`), standalone only (MariaDB → `deploy.sh`). Order: restore
  lock → refuse while a server uses the folder (a fresh `serve-*.lock`) → refuse while any
  account lease on the current database is live (D22: covers a process on another machine or
  one that just died) → checkpoint its WAL → decrypt to a temporary file → SQLite magic →
  `PRAGMA integrity_check` → schema ≥ 1 and ≤ this build's newest (older: migrated up now) →
  `PrepareRestored` → swap. Any refusal leaves the current database untouched (tested for a
  wrong identity, a tampered file, a non-age file, a MariaDB dump, no schema, a newer schema,
  a corrupt database).
- **Data-folder locks** instead of `flock` (portable to Windows, no syscall package):
  `<data>/locks/serve-<random>.lock` for each server (SQLite only) and `restore.lock`, each
  refreshed every 5 s and live while younger than 20 s; a stale one (crashed process) is
  ignored or replaced. Both sides write their own file first and then look for the other's,
  so of two that start together one gives up. `serve` refuses to start during a restore.
- `PrepareRestored` (portable SQL, used by the SQLite restore; the MariaDB script does the
  same): deletes every `agent_leases` row (the holders are gone), deletes every session
  (**everyone signs in again**: a sign-out or password change made after the backup must not
  come back), and writes setting **`restore.at`** (RFC 3339 UTC) for 7.12/7.13 to disarm any
  real gate armed before it (OD24).
- The replaced database is kept as `cactustrade.db.pre-restore-<UTC time>` (same protection as
  the database itself); deleting it is the operator's call.
- **Reconciliation first after a restore:** reconciliation state is per process (never
  stored), so a restored database starts unreconciled and `startTrading` keeps the 4.14 order.
  `TestRestoreThenStartReconcilesFirst` runs the real service on a restored SQLite database:
  not reconciled before, `ReconcileAll` is the first call, the supervisor takes the lease only
  after it, no order is sent.

**7.6b: MariaDB backup and restore** (`deploy.sh`, `deploy/backup/{mariadb-dump.sh,
mariadb-restore.sh}`, `cmd/cactustrade/encrypt.go`, `internal/adapters/sqlstore/backup_integration_test.go`).

- The SQL now lives in two POSIX `sh` scripts that run **inside the MariaDB container**
  (`sh -c "<file>" dump|restore …`); `make test-int` copies the very same files into
  testcontainers, so the tested restore is the shipped one. The dump adds `--hex-blob`.
- `--backup-now`: `mariadb-dump | gzip | age` → `cactustrade-<time>.sql.gz.age` with a recipient
  from `.env.local` (`CACTUSTRADE_BACKUP_RECIPIENT` / `_FILE`, validated as `age1…`); without
  one, today's 0600 `.sql.gz` with a loud warning (OD29). `--upgrade` reads either name.
- `cactustrade encrypt` / `decrypt` are the age filter. Order of use in `deploy.sh`: a local
  `CACTUSTRADE_BIN` or `bin/cactustrade`, else the `age` tool, else the app's image
  (`compose run --rm --no-deps -T app …`) or pod (`kubectl exec`). **`decrypt --identity -`**
  reads the identity from stdin up to an empty line, then the file: the private key is never
  a file, an argument or an environment variable inside a container or pod.
- `--restore FILE [--identity F] [--dry-run] [--yes]`: decrypts and decompresses into a 0600
  temporary file in `BACKUP_DIR` (removed on exit), reads the dump's goose version (the
  extended `INSERT` spans lines in MariaDB 11's output) and refuses none or newer than this
  checkout's newest migration — **all before anything is stopped**. `--dry-run` stops there.
  Then: typed `restore` (or `--yes`), a backup of the current database (as `--upgrade`), app
  stopped (`compose stop app` / scale to 0), the restore script, app started and waited for
  (healthy / rollout). A failed load starts the app again on the unchanged database.
  `--ssh-access` is refused for `--restore` (the file and identity are local; copy them over).
- `mariadb-restore.sh`: waits up to `RESTORE_LEASE_WAIT` (45 s) for live leases to expire (exit 4
  otherwise); loads into `<db>_restore`; checks the schema (exit 3); then **one atomic
  `RENAME TABLE`** moves the current tables to `<db>_prerestore` (kept until the next restore)
  and the restored ones in; foreign keys follow (checked: none points outside the database,
  a delete still cascades); then the same `PrepareRestored` statements.
- Test-int `TestMariaDBBackupRestore`: the shared suite fills MariaDB A; dump script → gzip →
  age → age → gunzip → restore script into a fresh container B: every table's row count and
  the goose version equal (sessions and leases 0, settings + `restore.at`); refusals (newer
  schema, foreign dump, bad name, live lease) leave B unchanged; a second restore keeps
  `_prerestore`.
- **Documented dry run** (Docker Desktop target; a throw-away age key and a dump with schema
  11; no container is touched, `TestDeployRestoreDryRun` runs the same without Docker):

  ```
  $ ./deploy.sh --restore backups/cactustrade-20261124T120000Z.sql.gz.age --identity id.agekey --dry-run --dockerdesktop
  ==> checking cactustrade-20261124T120000Z.sql.gz.age
  ✔ cactustrade-20261124T120000Z.sql.gz.age: a cactusTrade dump with schema 11 (this version: 11), 124 bytes
  dry run: --restore would now ask for 'restore', take a backup of the current database, stop the app,
  load this dump into cactustrade (the replaced tables kept in cactustrade_prerestore), sign
  everyone out, and start the app, which reconciles every account with eToro first.
  Nothing was stopped or changed.
  ```

  A live `--restore` on an installed Docker Desktop deployment is the owner's to run (this
  step deployed nothing).

**7.7: dependency audit** (`tools/licencecheck/`, `Makefile`).

- `make audit` = `govulncheck` (shared with `vuln`) + `go mod verify` + the licence check over
  Go and web + `pnpm audit --prod --audit-level=moderate` + `TestImagesPinnedByDigest`; it is
  part of `make check`. `make fmt`/`fmt-check` now cover `tools/`.
- `tools/licencecheck` (standard library only, enforced by `TestStandardLibraryOnly`): reads
  `go list -m -json all`, the module paths of `go list -deps -test -tags integration ./...`
  and `pnpm licenses list --prod --json`. **Judged: every Go module a package of which is
  compiled into cactusTrade or its tests (81 today, incl. the main module), plus every web
  production package.** The rest of the module graph (141 modules: goose's other database
  drivers, dependencies' own test requirements) is reported as skipped: never compiled here,
  and checking it would make the result depend on what happens to be in the module cache. A
  built module that is not downloaded fails. Licence files in a module's root are classified by
  their wording (MPL-2.0's mention of the GNU licences as "Secondary Licenses" is not GPL); an
  unrecognised licence file fails, except two reviewed non-code files listed with reasons in
  `nonCode` (go-digest's `LICENSE.docs`, modernc memory's `LICENSE-LOGO`). npm SPDX expressions:
  `OR` needs one allowed side, `AND` all; `WITH`, `LicenseRef-…` and unknown are refused.
  CC0-1.0 counts as public domain. `TestGPL3ModuleFails` is the acceptance test.
- `pnpm audit` found two moderate advisories in `react-router` 6.30.6 (open redirect via
  backslash in `<Link>`/`useNavigate`; SSR hydration, unused here) with fixes only in 7.18:
  **`react-router-dom` upgraded to 7.18.6** (same API for our `BrowserRouter`, `Routes`,
  `Link`, `NavLink`, `Navigate`, `MemoryRouter`; `pnpm -C web check` green, no code change).
- `swagger-ui-dist` pulls `@scarf/scarf`, whose install script sends usage telemetry: switched
  off with `scarfSettings.enabled = false` in `web/package.json`.
- A flaky web test met on the way (6.11's "caps the backoff at 30 s" failed once under load):
  its fake timers also move with real time (`shouldAdvanceTime`), so the 1 ms margin around
  30 s is now 1 s on each side (29 s: no retry yet; 31 s: retried). It still fails for a
  64 s backoff and for one under 29 s.

**7.1.17: images by digest, Node 22.** `Dockerfile` (`node:22-alpine`, `golang:1.26.11-alpine`,
`distroless/static-debian12:nonroot`), compose (`mariadb:11`, `busybox:1.37`,
`axllent/mailpit:v1.21`), k8s (`mariadb:11`), `deploy.sh` (`busybox`) and the Makefile's
in-container images are `tag@sha256:<multi-arch index digest>` (resolved 2026-11-24 with
`docker buildx imagetools inspect`). `TestImagesPinnedByDigest` fails on any unpinned image in
those files (checked by un-pinning one by hand). Updating an image = new digest in the same
places. The testcontainers images in `make test-int` stay by tag (test only).

### Steps 7.10–7.17: decisions (2026-11-24)

`make check` green on 2026-11-24 (incl. MariaDB `test-int` with 00012 and `make audit`); coverage
domain 97.9 %, app 89.4 %. Real trading stays **impossible**: `domain.RealTradingEnabled = false`; no refusal site S1–S11
was lifted, and the matrix above proves it with a gate armed. No new dependency, no new eToro
path (the allowlist and its tests are unchanged). New migration 00012 (`real_gates`) in both
dialects. Every OD19–OD28 default applies (the owner has not answered them yet).

**7.10 domain gate** (`internal/domain/realgate.go`).
- `QualifyingDays(facts, today, rule)` takes today's risk day: only *completed* risk days count
  (today's is not over — a position may still be held past it), within the `WindowDays` days
  before today; the latest must be at most `MaxAgeDays` old (`GateEvidence.Sufficient`).
- OD19 strict, read literally plus one: a day also needs at least one position opened (a fill
  with no recorded position is not evidence). Facts given twice for one day are added up
  before judging (stricter: a held position on either disqualifies the day); the property test
  checks that adding a *new* day never lowers the count.
- `FoldDayFacts(decisionDays, orders, positions)`: open orders in demo mode with `Sends ≥ 1`
  and state filled / partially_filled, on the risk day they were sent.
- `GateChecks` returns ten named checks (`real_account` … `confirm_phrase`); the arming user's
  check covers admin, owner **and** web session. `CanArm`: a failing check →
  `*GateRefusal` (matches `ErrRealModeGated` and `ErrConflict`; HTTP 409 naming the checks);
  all passing → `ErrRealTradingDisabled` (wraps `ErrRealModeGated`; HTTP 403 "Real trading is
  not enabled in this build"). `TestRealTradingDisabledInThisBuild` guards the constant.
- `RealGate`: `Arm` / `Disarm` keep the arming record (who, when, evidence) after a disarm for
  the audit; `Live(now, restoredAt)` is false at or after the expiry and when a restore
  happened at or after the arming. `domain.SettingRestoreAt` is now the one name of
  `restore.at` (sqlstore uses it).

**7.11 real limits** (`internal/domain/risk.go`). `LimitCeilings` with `RealCeilings` (OD25);
`ValidateFor(real)` also requires a position cap (0 = off would exceed every ceiling);
`WithBudgetCap` is the optional deployment cap (not wired: 7.23). `RaisesAny` counts a larger
budget, loss, drawdown, position cap (or the cap switched off), position count, short share,
portfolio or quote age, and a *shorter* no-entry or flatten window. In the app, a real
account's limits default to `DefaultRealRiskLimits` (`riskLimitsFor`) and `SetRiskLimits`
validates with `ValidateFor(mode)`; stored real limits over the ceilings (set before 7.11) are
shown as stored and fail the gate's `real_limits` check (never silently reset).

**7.12 storage.** `real_gates` as planned; `demo_account_id` has **no** foreign key (the arming
record outlives a deleted demo account; the gate's check fails for it); `evidence` is JSON
`{"days":[…]}`, the count derived on read. Two indexes for the evidence window:
`orders(account_id, created_at)`, `agent_positions(account_id, opened_at)`. `Orders.ListSince`,
`AgentPositions.ListSince` (closed positions included), `RealGates{Get, Put}` versioned like
`risk_state` (shared suite: a stale arm after a disarm is `ErrConflict`, cascade, bounds).

**7.13 app** (`internal/app/realgate.go`).
- Order in `ArmReal`: `mode.switch` → web session → owner (`AccessOwner`) → password
  (`checkPassword`, now taking the refusal's audit action: `real.arm_refused`) → phrase (422,
  audited) → checklist → `CanArm`. Every refusal after the password is audited with the failing
  check names. `armLocked` is called only after `CanArm` passes, i.e. never in this build; the
  tests call it directly to build the worst case.
- The demo evidence reads the decision log **one row per risk day** (`List(before, limit 1)`,
  then jump to that day's start): at most 61 reads, whatever the volume.
- `RealGateStatus` (view access) drops the phrase check from the list and shows the phrase;
  `CanArm` = the arming-user check; `CanDisarm` = `agent.kill` + trade access. Demo candidates
  are the owner's eToro demo accounts the caller can see.
- `DisarmReal` and every automatic disarm go through `disarmLocked` (control lock held):
  **latched in memory first** (a gate armed before the latch is not in force even if the write
  fails — `real.disarm_failed` is audited), the running entry tick cancelled, the gate stored
  locked, a running or paused agent stopped (also when the gate was already locked), nothing
  cancelled or closed. Only a change is audited (`real.disarm`), announced (danger event) and
  e-mailed. Idempotent; runs on `WithoutCancel` like the kill.
- Automatic disarm (OD24): `tripLocked` (manual kill and the supervisor's drawdown trip),
  `SetAccountCredentials` (under the lock it already takes), `SetRiskLimits` when `RaisesAny`;
  expiry and restore are checked on **every read** (never in force afterwards) and persisted by
  `sweepRealGates` on each supervisor tick and by the status read, each with `TryLock` (a busy
  account is swept next time). An unreadable `restore.at` is "not in force".
- E-mail: `emitFor` mails the owner and shares who chose alerts; `mailGateChange` adds every
  admin and the owner who did not get it that way, **whatever their notification choices**.
- `Status.RealArmed` (accounts the caller can see, in force) feeds the banner;
  `AccountHealth.RealGateArmed` (and the `real_gate_armed` metric) now come from the gate.
- OD26 needs no change: a real agent marked running comes back paused through `startable` (S8).

**7.14 contract and HTTP.** `GET /api/v1/accounts/{accountId}/real-gate[?demoAccountId=]`,
`POST …/real-gate/arm` `{demoAccountId, confirm, password}`, `POST …/real-gate/disarm`
`{reason?}` (the never-throttled pattern of D32); `SystemStatus.realArmed` (optional in the
schema, always sent). Risk days are plain `YYYY-MM-DD` strings (not `format: date`). The
"not enabled in this build" title is used by the gate endpoints only; the agent endpoints keep
"Real trading needs the phase-7 gate" (`TestAgentRolesAndRealAccounts` unchanged). The arm
runs under `passwordChecked` (sign-in limiter; 429 after 5 wrong).

**7.15 web.** *Setup → Real trading* is in everyone's menu (disarm is for operators too); the
page lists the real eToro accounts and shows only what the server returns (checks, evidence,
`canArm`, `canDisarm`, refusals); a source test bans role / access / threshold logic in it.
`RealBanner` sits in `Layout` above R3's real strip; a test renders it on every route declared
in `App.tsx` (checked against its source) with and without armed accounts.

**7.16 docs.** ARCHITECTURE "Real-mode gate", ADR-007, README "Real money". **RUNBOOK not
edited here:** 7.9 (another worktree) owns `docs/RUNBOOK.md` including "disarm real trading
now"; the procedure is `POST /api/v1/accounts/<id>/real-gate/disarm` with `{"reason":"…"}`
(web UI: Real trading → Disarm; or an API key with `agent.kill` and trade access), never rate
limited.

**7.17 matrix.** See "Pre-consent proof" under the refusal table. The OD28 opt-in does not exist
before 7.23, so the tests set `CACTUSTRADE_REAL_TRADING=on` in the environment to show nothing
reads it. Reads of a real account (its portfolio for a view) stay allowed and are not counted;
mutating calls are. `TestRealRefusalModeReferences` lives in `cmd/cactustrade` (depguard bans
`os` in `internal/app`, also in tests), compares per-file multisets of trimmed lines (a second
copy of a listed line fails too), and skips `storetest` (the shared suite's fixtures); a planted
`var _ = domain.ModeReal` in `internal/app/executor.go` failed it (checked by hand, reverted).

### Steps 7.19–7.23: decisions (2026-11-27)

**Review of the lift (reviewer agent, 2026-11-27): 3 MAJOR, 3 MINOR, all fixed before the first commit.**
- M1: `decide` took its time before the entry permit was issued, so on a real clock every real entry was refused (fail closed). `realEntryPermit(ctx, a, now)` now issues the permit at the caller's time; `fakes.Clock.Step` gives a clock that moves; `TestRealEntryWithRunningClock` (fails with the old code).
- M2: OD26 rested on a stored "paused": a failed write or an unreadable state could leave a real agent "running" after a restart. Now structural: a real account ticks, enters and gets a permit only after a person's Start in this process (`runtimeState.realStarted`, cleared first by any move away from running); `TestRealAgentStoredRunningDoesNotTrade`.
- M3: the lift allowed every real account; D39 covers Real Money Maker only. `domain.RealConsentFor` (one id, D39) + gate check `owner_consent` + `realEntryBlocked`; `TestRealConsents`, `TestRealOnlyConsentedAccounts`. Another real account needs a new written consent and a reviewed edit.
- m1: `TestRealPermitCallSites` pins who may build or attach a permit. m2: the gate is read once in `agentGate` and `startable`. m3: `.env.local` on the owner's Mac already had `CACTUSTRADE_REAL_TRADING=on` (written 17:21 the same day, before the lift); the owner asked the same evening to "enable the real money trading now".

The real-money lift after the owner's written consent (D39). No new dependency, no migration.
Claude ran no real-mode order and called no real eToro endpoint: every test uses fakes.

- **Permit (7.19).** `domain.RealPermit` has unexported fields; only `PermitEntry(gate, account,
  now, restoredAt)` and `PermitExit(account, ours)` build one. `PermitEntry` takes the restore
  time as well (the plan wrote `PermitEntry(gate, account, now)`): `RealGate.Live` needs it, and
  a gate older than a restore must not build a permit. An entry permit is good for 60 s
  (`RealPermitTTL`), never past the gate's expiry, for its account only, and also allows exits;
  an exit permit allows closes and cancels only and does not expire (a close resent after a
  429 is the same exit). `RiskInput` gained `AccountID` and `Permit`; `IntentFor(o, permit)`
  checks the permit at the order's `CreatedAt` (the domain reads no clock). `ErrNoRealPermit`
  wraps `ErrRealModeGated`, so every existing refusal mapping (403) holds.
- **Fresh permit per entry (7.22).** The agent's `decide` takes a permit for `AssessEntry`; the
  executor's `mayOpen` takes **another** right before the order row and the send, so a disarm,
  an expiry or the opt-in going off between the assessment and the send stops the order
  (`realEntryBlocked` re-read: opt-in, `RealTradingEnabled`, the gate live incl. this process's
  disarm latch, reconciled, `RiskLimits.ValidateFor(real)`).
- **Exits (7.21).** `mayExit` gives a real close/cancel an exit permit whenever the position or
  order is the account's own (`AgentPosition.AccountID`, `Order.AccountID`; OD1), whatever the
  gate or the opt-in. A real account is supervised while its gate is stored armed or it holds
  open agent positions or unfinished orders (a read error counts as "needs it"); that check runs
  in the account's own supervisor goroutine, so a slow store never holds up another account
  (`TestSlowGateSweepDoesNotDelayExits` now expects the real pass's gate read beside the
  sweep's). Kill flatten and force close (single and all-accounts) include real accounts.
- **Controls (7.22).** `Gated` now means "no real entry may be made here now" and only turns
  Start off (with `startBlockedReason` = the reason); pause, stop, kill and force close stay on
  for real accounts. The web panel and cards no longer disable those buttons when gated (they
  did, which would have hidden the kill switch on a real account). `forceCloseBlockedReason` is
  kept in the contract and always empty.
- **OD26.** `resume` refuses every real agent after a restart (paused, warning event naming
  OD26), even when it would be startable; `autoStartAll` stays demo-only.
- **Opt-in (7.23).** `CACTUSTRADE_REAL_TRADING` is read only by `cmd/cactustrade`
  (`realTradingEnv`): exactly `on` turns it on, unset/`off` off, anything else stops the
  start-up. It reaches the app as `app.Config.RealTrading`; the app never reads the environment.
  While on: a start-up warning log line and `AnnounceRealTrading` (audit
  `system.real_trading_on`, danger system event). `.env` has `CACTUSTRADE_REAL_TRADING=off`;
  compose and the k8s manifest pass the value through (default off) so the owner can turn it on
  in `.env.local`; `deploy.sh` validates it (on/off), warns loudly when on, and never sets it
  (`TestDeployRealTradingOptIn` also scans `deploy.sh` for an assignment of `on`).
  **Note:** this machine's `.env.local` (not in git, not touched) already contains
  `CACTUSTRADE_REAL_TRADING=on`, so a container deployment from here passes `on` through; real
  entries still need an armed gate on the account.
- **eToro client.** A real `open` needs an entry permit valid at the client's own clock; a real
  account with `EnvKeys` (the deployment's demo keys) is refused for every change ("real keys
  only"). Each real change logs a warning line "eToro REAL-MONEY account change" (op and request
  id only). `set_sl_tp` stays not executable everywhere.
- **HTTP.** `failGate`: `ErrRealTradingDisabled` → 403 "Real trading is not enabled in this
  build"; any other real-mode refusal on the gate endpoints → 403 "Real trading is not armed".
  `ErrRealModeGated`'s text lost "only demo accounts trade".
- **Tests converted, none deleted.** `TestRealTradingDisabledInThisBuild` →
  `TestRealTradingEnabledByOwnerConsent`; `TestForceCloseRealAccountRefused` →
  `TestForceCloseRealAccount`; `TestRealRefusal*` now refuse without a key/permit (above);
  `TestArmReal*` arm when every check passes (`TestArmRealArms` new); the HTTP arm test now
  arms (200), shows the banner and disarms; `TestForceCloseRefusalsDoNotCount` uses unknown
  accounts as its non-password refusals. `modeSites` was rewritten line by line for every
  changed `ModeReal`/`ModeDemo` reference, each annotated with its site.

### Steps 7.8–7.9 and 7.1.13–7.1.16: decisions (2026-11-24)

No new dependency, no migration. The other phase-7 worktree (7.10–7.17) owns the real gate;
nothing here touches it (the RUNBOOK's disarm procedure is written as *planned*).

**7.8: Vault `SecretStore`** (`internal/adapters/vault/`, `cmd/cactustrade/{config.go,main.go,
secrets.go}`, `.golangci.yml`, ADR-003 update, README *Vault*).

- Off by default (`CACTUSTRADE_SECRETS=db`); `vault` swaps the database store for
  `vault.Store` under the same `secretstore.Env` overlay (the environment still wins). The master
  key is neither loaded nor generated in Vault mode.
- KV v2, one secret per credential `<mount>/<prefix>/<name>` = `{"value": …}` (defaults
  `secret` / `cactustrade`); names, mount and prefix are checked (`[A-Za-z0-9._-]`, no `.`/`..`
  segments) before anything is sent; delete = metadata delete (all versions; a missing one is
  not an error, like every caller already treated `ErrNotFound`); `Status` reads
  `updated_time` per name (the Saxo 24-hour token check needs it; N+1 requests, a handful of
  names). `Source` stays `ui` (the contract's enum is unchanged).
- Token: a file (`CACTUSTRADE_VAULT_TOKEN_FILE`, default `<secrets>/vault_token`) looked up with
  `lookup-self` for its TTL, or Kubernetes auth (`CACTUSTRADE_VAULT_K8S_ROLE`, JWT file, mount).
  Renewed with `renew-self` two thirds into the lease; a refused renewal, an unusable answer, a
  lease under 30 s or an expired token fetch a new one; a 403 on a request drops the token and
  retries once. The lock covers the token only, not the requests.
- `https` only (`http` to loopback for a dev Vault), system roots + `CACTUSTRADE_VAULT_CA_FILE`,
  TLS ≥ 1.2, **redirects refused** (`ErrRedirect`; the target gets 0 requests), answers capped
  at 1 MiB, errors carry method, path, status and Vault's first message (clipped to safe
  characters) — never a header, token, JWT or value; only the TTL is logged.
- **Exits survive a Vault outage:** decided beyond the step's text because CLAUDE.md §2.6 says
  exits must still work. The store remembers (in memory) the value it last read or wrote per
  name and serves it while Vault fails with `ErrUnavailable` (one warning per outage, naming the
  credential); `ErrNotFound` and a delete forget it; a name never read since the start fails.
  Consequence recorded in ADR-003: revoking cactusTrade's Vault token does not cut eToro access
  until a restart (revoke the eToro key for that).
- Start-up: a bad setting stops `serve`; Vault not answering is a warning (the kill switch and
  the UI must work; credentials are unavailable, nothing trades).
- `cactustrade secrets migrate --to vault [--dry-run]`: reads every stored credential with the
  master key (never generates one), writes it to Vault, reads it back, prints names only, leaves
  the database copies. No `--to db` (not asked for).
- depguard rule `vault`: no use cases, no broker/LLM adapters, no `github.com/hashicorp` /
  `github.com/openbao` SDK; the LLM rule also denies the Vault adapter.
- Not wired into `deploy.sh` or the manifests (OD30: optional); the README says what a k8s
  deployment needs (projected token volume, role, policy).
- Tests: `go test ./internal/adapters/vault` **97.2 %** — `TestKV` (get/put/delete/list, header-only
  token), `TestErrors` (5xx, 403, unreadable, oversized, metadata failures, lookup failures),
  `TestForbiddenRefetchesTokenOnce`, `TestKubernetesLoginAndRenewal`, `TestKubernetesLoginRefused`,
  `TestRedirectRefused`, `TestVaultDownAndCancelled`, `TestTLSWithCAFile`, `TestNamespaceAndMount`,
  `TestOutageServesLastValue`, `TestInvalidNamesSendNothing`, `TestNewValidates`; every failure
  path checks errors and the captured log for planted tokens, JWT and values. `cmd`:
  `TestVaultConfig`, `TestServeSecretsInVault`, `TestSecretsMigrateToVault`.

**7.9: releases and runbook** (`Makefile`, `scripts/release-{sums,sign,verify}.sh`,
`scripts/allowed_signers`, `setup.sh`, `setup.ps1`, `docs/RUNBOOK.md`, README).

- `make dist` (not part of `make check`): `web-check`, then `dist/cactustrade-{linux,darwin,
  windows}-{amd64,arm64}[.exe]` (`CGO_ENABLED=0`, `-trimpath`, version/build ldflags), `SHA256SUMS`,
  `ssh-keygen -Y sign -n cactustrade-release` with `RELEASE_KEY` (default
  `~/.config/cactustrade/release_ed25519`), then `release-verify.sh` against `RELEASE_SIGNERS`
  (default `scripts/allowed_signers`) exactly as `setup.sh` checks. `dist/` is git-ignored (not
  committed, unlike cactusInv).
- **No release key exists yet — an owner step.** Claude did not create the owner's signing key.
  `scripts/allowed_signers` has comments only and `SIGNER` is empty in both setup scripts, so
  they **refuse to install** and `make dist` with the defaults fails at the verify step until the
  owner creates the key and publishes its public line in the three places (RUNBOOK "Releases";
  `TestReleaseSignerAgrees` keeps them equal).
- `setup.sh` / `setup.ps1` follow cactusInv's (download into the current folder, keep
  `cactustrade.old`, run `version`), but **fail closed on the signature** on every platform:
  OpenSSH's `ssh-keygen -Y verify` (8.1+, on macOS, Linux and the Windows OpenSSH client) instead of
  cactusInv's "OpenSSL 3 or checksum only". `setup.ps1` feeds `SHA256SUMS` to ssh-keygen by
  `-RedirectStandardInput` (a PowerShell pipe would re-encode it). `CACTUSTRADE_RELEASE_URL`
  (mirror / `file://`) and `CACTUSTRADE_RELEASE_SIGNERS` (another trust anchor, with a loud
  warning) exist for tests and mirrors. `setup.ps1` was not run (no PowerShell here); only its
  constants are checked by the test.
- Acceptance run: `make dist RELEASE_KEY=<throw-away key> RELEASE_SIGNERS=<its signers>` built the
  6 binaries (25–27 MiB each), wrote and signed `SHA256SUMS`, and `release-verify` passed;
  `dist/cactustrade-darwin-arm64 version` answered; `dist/` was deleted afterwards.
  `TestReleaseSignAndVerify` runs the scripts with a throw-away key: good release verifies and
  `setup.sh` installs it (old program kept); a changed binary, a changed `SHA256SUMS`, another
  key's signature, no signature, or no published key → refused and nothing replaced.
- `docs/RUNBOOK.md`: incident procedures (eToro API down or refusing, kill switch tripped,
  reconciliation mismatch with a reason → action table, lease held by a dead process, restore,
  Vault unavailable, **disarm real trading now** as the *planned* procedure: kill → disarm
  (7.14's endpoint) → `CACTUSTRADE_REAL_TRADING=off` → revoke the real eToro key → check; marked to
  be confirmed by 7.16), plus operating notes (trusted proxies on k3s/k8s, releases).

**7.1.13: Saxo OAuth bound to the browser, and PKCE** (`internal/app/connect.go`,
`internal/adapters/saxo/oauth.go`, `internal/adapters/http/{handlers.go,handlers_accounts.go}`,
`internal/ports`, `internal/domain/oauth.go`, `docs/saxo-api.md`).

- Both, as planned: `ConnectAccount` draws state, a browser nonce and a PKCE verifier (three
  `Random.Token()`s; a verifier RFC 7636 would refuse stops the connect, fail closed). The state
  keeps the nonce's hash and the verifier (memory only). The HTTP adapter sets the nonce as
  `__Host-cactustrade_oauth` (HttpOnly, Secure, **SameSite=Lax** — Saxo's redirect back is a
  cross-site top-level navigation, a Strict cookie would not come along —, Path=/, Max-Age 600;
  `cactustrade_oauth` without `__Host-` when cookies are not Secure), never in the body.
- `CompleteConnect(ctx, platform, OAuthCallback{…})` (a struct now, instead of five strings):
  state taken (single use) → platform and expiry → **nonce (constant-time hash compare)** →
  provider error → code → … → `Exchange(code, redirect, verifier)`. A missing or foreign cookie
  is a `state` validation error, so it counts in the OAuth limiter like a forged state, and the
  state is used up. The callback clears the cookie on every path (also when rate-limited).
- Saxo: `code_challenge` + `code_challenge_method=S256` on the authorize address (names verified
  in Saxo's PKCE sample), `code_verifier` on the token request. **Unverified** whether Saxo's
  code-flow apps accept PKCE (recorded in `docs/saxo-api.md`): the owner's first connect settles
  it; a refusal fails closed.
- One browser = one pending Saxo sign-in: starting a second connect replaces the cookie, so the
  first one's callback is refused (connect again).
- `fakes.Random.Token()` now returns 43 characters (`tok` + 40 digits), like the real one.
- Tests: `TestConnectAccount` (no cookie, another browser's cookie, oversized cookie, a refusal
  without the cookie, the state used up whatever happened, verifier ↔ challenge),
  `TestConnectNeedsValidVerifier`, `TestConnectPKCE`, `TestPKCEChallenge` (RFC 7636 App. B),
  `TestValidPKCEVerifier`, `TestPlatformEndpoints` (cookie attributes, no nonce in the body, a
  callback from a cookie-less client refused with 0 exchanges, cookie cleared).

**7.1.15: Mailpit is not a reset channel** (`internal/app/{app.go,email.go}`,
`cmd/cactustrade/mail.go`, compose, `.env`, k8s ConfigMap, README).

- `Deps.MailFallback` returns `app.FallbackMail{Server, Password, TestMailbox}`. `cmd` marks the
  fallback a test mailbox when its host is `mailpit` (or `mailpit.<…>`, case-insensitive: the
  compose service) unless `CACTUSTRADE_SMTP_TEST_MAILBOX_RESETS=true` (default `false` in `.env`,
  compose and the k8s ConfigMap).
- Mail still goes through it (notifications, test e-mails: unchanged). Only password reset is
  off while it is the server in use: `PasswordResetAvailable` false (public status), a reset
  request answers 503 `MissingError{email_server}` saying the only server is a test mailbox that
  never reaches the user, and the admin's E-mail page shows "…, a test mailbox: password reset by
  e-mail is off" in the fallback text (no contract change). A real server switched on in the app
  wins over the fallback, as before.
- Tests: `TestPasswordResetTestMailbox` (Mailpit only, opt-in, real fallback, in-app server,
  none), `TestMailFallbackTestMailbox` (`cmd`).

**7.1.16: k8s NetworkPolicy, trusted proxies, MariaDB hardening** (`deploy/k8s/cactustrade.yaml`,
`deploy.sh`, `docker-compose.yml`, `.env`, README, RUNBOOK).

- NetworkPolicies `mariadb` (ingress 3306 only from pods `app.kubernetes.io/name: cactustrade`)
  and `cactustrade` (ingress 8080 only from the ingress controller's pods: namespace
  `K8S_INGRESS_NAMESPACE` + one label `K8S_INGRESS_POD_SELECTOR`, defaults `kube-system` /
  `app.kubernetes.io/name=traefik`; in the plain-HTTP NodePort mode the `from` line is dropped
  by the existing `# https` marker, i.e. open as the Service is). No default-deny for the whole
  namespace (the two policies isolate both pods) and no egress limits (eToro, LLM, mail, DNS are
  outside). `deploy.sh --uninstall` and `--status` include `networkpolicy`. Backups and restores
  use `kubectl exec` (not network traffic) and keep working.
- **Trusted proxies:** `CACTUSTRADE_TRUSTED_PROXIES` comes from `K8S_TRUSTED_PROXIES`; the safe
  default is **empty = nobody** (was the whole k3s pod network `10.42.0.0/16`). Consequence for
  the owner's existing k3s install: until `K8S_TRUSTED_PROXIES` is set, every client counts as
  Traefik's address for the sign-in limiter and the API rate limits; `deploy.sh` warns on every
  HTTPS render/install. RUNBOOK "Trusted proxies on k3s/k8s" shows how to find the ingress
  pods' IPs and their nodes' pod CIDR (recommended, survives Traefik restarts; safe because
  only the ingress pods reach the app where NetworkPolicy is enforced). Values going into the
  manifest are validated (namespace name, `key=value` label, address/CIDR characters):
  `TestDeployRenderRefusesInjection`.
- **MariaDB hardening, k8s and compose:** runs as the image's `mysql` user 999 from the start
  (`runAsNonRoot`, `fsGroup: 999`, `fsGroupChangePolicy: OnRootMismatch`; compose `user:
  "999:999"`), read-only root file system with `/run/mysqld` (memory) and `/tmp` writable,
  `allowPrivilegeEscalation: false` / `no-new-privileges:true`, all capabilities dropped,
  seccomp `RuntimeDefault` (k8s). Compose's `volume-init` now also `chown -R 999:999` +
  `chmod 700` `MARIADB_DATA_DIR` (and runs with `network_mode: none`); MariaDB waits for it.
  Checked by hand before writing it: `mariadb:11` (the pinned digest) as `--user 999:999
  --read-only --tmpfs /run/mysqld --tmpfs /tmp --cap-drop ALL --security-opt
  no-new-privileges` initialises a fresh volume, passes `healthcheck.sh --connect
  --innodb_initialized`, takes writes and `mariadb-dump` works; Mailpit with `--cap-drop ALL
  --read-only` runs too (compose now drops its capabilities).
- **Not done: MariaDB TLS** (F16 mentioned it). Inside the cluster the NetworkPolicy keeps 3306
  to the app pod, and in compose the database has no published port; TLS would need certificate
  issuing and rotation for both. Left as a possible later step, not planned.
- **Documented dry run** (no cluster was touched): `K8S_TRUSTED_PROXIES=10.42.0.0/24 ./deploy.sh
  --render --k3s` and `K8S_HTTPS=false ./deploy.sh --render --k3s` both parse as 10 documents
  (…, `NetworkPolicy/mariadb`, `NetworkPolicy/cactustrade`); the HTTPS app policy's `from` is
  `kube-system` + `app.kubernetes.io/name: traefik`, the plain-HTTP one has none;
  `kubectl apply --dry-run=client --validate=false -f <rendered>` → `networkpolicy…/mariadb created
  (dry run)`, `networkpolicy…/cactustrade created (dry run)` and every other object. Not applied to
  the owner's k3s: that is the owner's `./deploy.sh --upgrade --k3s` (set `K8S_TRUSTED_PROXIES`
  first). Risk to check then: a network plugin that blocks the kubelet's probes under a
  NetworkPolicy (node-local traffic is normally allowed; not verified on a cluster here). If the
  probes fail after the upgrade, add the node addresses as an `ipBlock` to the `cactustrade`
  policy.
- Tests: `TestDeployRenderNetwork` (both policies, defaults, ingress-nginx values, plain HTTP,
  trusted proxies default empty with the warning, MariaDB securityContext and mounts),
  `TestDeployRenderRefusesInjection`, `TestComposeMariaDBHardened`; `TestDeployRenderService`
  unchanged and green.

**Verification (2026-11-24):** `make check` green in this worktree: gofumpt, golangci-lint 0
issues (incl. the new depguard rule), vet, `go test -race` with coverage **domain 98.1 %, app
89.4 %**, MariaDB `test-int` ok, govulncheck "No vulnerabilities found", `make audit` (modules
verified, licences ok, pnpm audit clean, images pinned), contracts in sync, `pnpm -C web check`
(128 tests). `make dist` run once with a throw-away key (above).

### Real-gate review (2026-11-24)

Status: `done` (2026-11-24). An independent review of 7.10–7.17. Each finding below got a
test that failed first, then the fix. Real trading stays **impossible**:
`domain.RealTradingEnabled` is still false, no refusal site S1–S11 was lifted, the eToro
allowlist is unchanged, there is no new dependency and no migration.

| # | Sev. | Finding | Fix | Test (failed first) |
|---|---|---|---|---|
| R1 | MAJOR | `SetAccountCredentials` wrote the two eToro keys one at a time and returned on the first failed write. The disarm and the unreconcile ran only after the loop, so a change that failed part-way left the gate armed and the account reconciled, with one key possibly new. | The unreconcile and `disarmIfArmed(…, DisarmKeys)` now run **before** the first `Secrets.Put`, once validation has passed and something is to be written. The ctl lock is held and the agent is quiescent. A change that then writes nothing at all still disarms (fail closed). | `TestKeyChangeFailingPartWayDisarms`: write 1 or 2 fails → gate locked for the key change, account unreconciled; an invalid key changes nothing. Failed with the old order. |
| R2 | minor | The banner failed open: a gate read error read as "not armed", a failed `Status` showed nothing, and the change-password page and Swagger UI had no banner. | `realGateState` returns the error, and `Status.RealArmedUnknown` / contract `realArmedUnknown` carry it. `TradingHealth` reports an unreadable gate as a problem. `ModeProvider` remembers a failed status read, and `RealBanner` then shows a red "real-trading state unknown" banner (with any names still known). `ChangeDefaultPassword` is wrapped in `ModeProvider` and renders `RealBanner`. **`Status` now answers a default-password user with the banner's facts only** (no version, build, start time or account), because the page has no other source; every other use case stays refused, and the three tests asserting the old 403 now assert that narrowed answer. The server renders the same banner (escaped, inline style, `Cache-Control: no-store`) right after `<body>` of the Swagger UI page for a signed-in visitor; anonymous visitors get the page unchanged. | `TestRealGateStateUnknown`, `TestStatusForDefaultPassword`, `TestRealBannerUnknownAndSwagger` (status, armed and escaped, anonymous, unknown), `RealBanner.test.tsx` (unknown on every route, a failed first load, last known names), `ChangeDefaultPassword.test.tsx` (armed, none, unknown). |
| R3 | minor | Restore detection compared clocks (`restore.at` against `armed_at`). A skewed clock could let a restored arming through. | Every restore locks every gate: `PrepareRestored` (standalone, in the restore transaction) and `deploy/backup/mariadb-restore.sh` (one transaction with the lease and session clean-up; skipped for a dump older than 00012). Both run `UPDATE real_gates SET state='locked', disarmed_at=<now µs>, disarmed_by='', disarm_reason='a backup was restored', version=version+1, updated_at=<now µs> WHERE state='armed'`. New `domain.DisarmRestored`. The `restore.at` comparison stays as a second check. | `TestBackupRestoreRoundTrip` (a gate armed an hour in the future is locked with that reason) and `TestMariaDBBackupRestore` (test-int: none armed after the restore, reason, version bumped). The SQLite one failed before the fix. |
| R4 | minor | The disarm latch was per process: when the gate write failed, other replicas still saw the gate armed. | `disarmQuietly` stores a **kill** when the gate write fails (`storeKillLocked`: latch, risk state, agent state, cancel the tick; audited `agent.kill` with the reason "the real-trading disarm could not be stored (…)"). It skips this when this process already holds a kill (a kill being tripped stores its own). Consequence: an admin must reset the kill switch after the database trouble, and the supervisor flattens our positions there (on a real account only from 7.21). `tripLocked` was split into `storeKillLocked` + `announceKill`, so the gate is disarmed once per trip, without recursion. | `TestRealGateDisarmWriteFailureKills`: gate write fails → kill stored; a fresh `Service` on the same store (no latches) sees the account killed; a kill whose disarm fails is audited once. Failed before the fix. |
| R5 | minor | Disarm work ran ahead of exit work: the kill's gate disarm sent its event and e-mails before the flatten, and the supervisor tick swept gates before starting the passes. | `tripLocked` latches and stores the disarm and returns an `announce` func. `Kill` runs it (deferred) after `flatten`. The supervisor's drawdown trip (`tripQuietly`) runs it at the end of its pass, after the closes; a real account is not supervised before 7.21 (S6), so no test can reach that path yet. `trip` (the agent tick's kill) runs it on return. The tick now starts the passes first, then `sweepRealGatesAside` runs `sweepRealGates` in its own goroutine (in the loop's WaitGroup) with a 30 s timeout. At most one sweep runs at a time (`runtimeState.sweeping`). | `TestTripAnnouncesTheDisarmAfterwards` (stored before return, no event or e-mail until `announce`; `Kill` sends them), `TestSlowGateSweepDoesNotDelayExits` (a gate read blocked forever: the tick returns, the demo pass closes its due position, the sweep has a deadline, a second tick starts no second sweep). The sweep test failed before the fix. |
| R6 | minor | Tests were weaker than their names. | `TestRealRefusalModeReferences` says in its comment that it is a tripwire for review and that the behaviour matrix is the guard. It also matches `isRealAccount`, `accountMode` and `.Mode ==` / `.Mode !=`; the 10 new hits are in `modeSites` with a reason each (reads, limits, display, the gate's own narrowing; none lets real through). `TestRealGateEndpoints` seeds 19 qualifying days before the step now named "19 days: one short of the evidence", checks the count, then adds the 20th. | the source test itself; `TestRealGateEndpoints`. |
| R7 | minor | The RUNBOOK disarm procedure was stale. | It is no longer "planned". The menu is *Setup → Real trading*. Step 1 says a kill on a real account stores the kill and disarms today but flattens only **from 7.21**. Step 3 (`CACTUSTRADE_REAL_TRADING`) is marked **from 7.23** and is skipped until then. A failed disarm stores a kill, the "state unknown" banner means "treat it as armed", and the restore section says every gate is locked. ARCHITECTURE "Real-mode gate" is updated to match. | docs |

**Open owner questions (listed, not implemented):** OD34–OD37 above.
- Should a gate disarm when the admin who armed it is demoted or disabled?
- Should a gate disarm when the linked demo account is deleted?
- Should a gate disarm when the real account is renamed?
- Should admins who cannot see the account get the banner?

**Verification (2026-11-24):** `make check` green: gofumpt, golangci-lint 0 issues, vet,
`go test -race` with coverage **domain 97.9 %, app 89.6 %**, MariaDB `test-int` ok (including
the restore script locking the gates), govulncheck "No vulnerabilities found", `make audit`
(modules verified, licences ok, pnpm audit clean, images pinned), contracts in sync,
`pnpm -C web check` (194 tests). Three fixes were checked by temporarily reverting them: R1's,
R4's and R5's tests failed and passed again once the fix was restored. R3's SQLite test failed
before its fix. R2's tests use the new `realArmedUnknown` contract field.

