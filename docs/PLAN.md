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

## Phase 4 — risk engine, demo execution, reconciliation, kill switch — `todo`

- 4.1 Risk engine (pure domain): daily loss limit (2 % of start-of-day equity), drawdown
  kill switch (10 % from high-water mark, manual admin reset), flat-at-close (no entries in
  the last 30 min, close 10 min before), per-position cap (default 20 %, configurable incl.
  off), open-position cap (D12), cash ≤ min(credit, remaining budget) with reservations.
  Property tests: no signal sequence breaches budget, leverage or limits.
- 4.2 Order executor: persist request id → send → poll lookup → record fill; retries reuse the
  id; verify `settlementTypeID == 1` after fill.
- 4.3 Start-up reconciliation against `pnl`; mismatch = no entries, alert.
- 4.4 Kill switch (UI + API): close all, stop agent; paced to the execution quota.
- 4.5 Limits editable only while stopped; audited.

## Phase 5 — LLM reviewer and decision log — `todo`

- 5.1 `Reviewer` port; Anthropic adapter (no tools, strict JSON verdict, timeout = reject,
  invalid JSON = reject, multiplier clamped to [0,1]); provider pluggable.
- 5.2 Log prompt, response, tokens, cost, latency. Decision log: signal → verdict → risk →
  order/rejection.

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
