# cactusTrade — Engineering Rules for Claude Code

You are a senior engineer on cactusTrade: a standalone, autonomous day-trading application
that trades **shares long (real shares) and short (CFD, leverage 1, capped)** on eToro accounts
(each user's own, shareable with other users; owner's decisions 2026-11-23) through the
official eToro Public API.
Go (hexagonal, container-native, standalone), **SQLite** standalone / **MariaDB** in
containers, React web app. Execute `docs/PLAN.md` one phase at a time and keep `make check`
green. Stop for the owner's review at the end of every phase.

Details: `docs/PLAN.md` (what / status), `docs/ARCHITECTURE.md` (how), `docs/adr/`
(decisions), `docs/etoro-api.md` (verified eToro facts). The owner's original brief
(`cactusTrade-claude-code-prompt.md`) is kept locally, not in git; its requirements live in
§2 here and in `docs/PLAN.md`.

| Property | Value |
|---|---|
| Module | `github.com/albievan/cactustrade` |
| Artifact | ONE binary `cactustrade` (Linux, macOS, Windows; web UI embedded), ONE distroless image |
| Database | `CACTUSTRADE_DB_DRIVER=sqlite` (default, pure-Go `modernc.org/sqlite`, file in the data dir) or `mariadb` (MariaDB 11, used by every container deployment). `goose` migrations in `migrations/sqlite` and `migrations/mariadb`, kept in lock-step |
| API | OpenAPI 3.0 design-first `api/openapi/cactustrade.yaml`, `oapi-codegen` std-http server, Swagger UI at `/api/docs` |
| Secrets | eToro and LLM keys: write-only, AES-256-GCM in the database under a master key (`CACTUSTRADE_MASTER_KEY` / `_FILE`); deployment secrets as files in `/run/secrets`; Vault optional (phase 7) |
| Deploy | `deploy.sh` → Docker Desktop, Podman, k3s or k8s; defaults in `.env`, per machine in `.env.local` |
| Licence | public domain (SQLite-style blessing), see `LICENSE` |
| Frontend | React 18 + TS + Vite 5 + Bootstrap 5.3 in `web/`, embedded with `go:embed` |

## 1. Operating mode
1. Read `docs/PLAN.md`, `docs/ARCHITECTURE.md` and the files you will touch first.
2. Plan before code: update `docs/PLAN.md` before multi-package changes.
3. Small verified steps: change → `make check` → fix → next.
4. Never mark a step `done` with failing checks. Stop for review after each phase.
5. Agents in `.claude/agents/`: `planner` (plans, no code), `coder` (one step), `reviewer`
   (read-only, ranked findings). Use them for multi-package work.
6. Finish with the report format in §8.

## 2. Money guardrails — NON-NEGOTIABLE (brief §3)
Never weaken, bypass or make configurable anything here without the owner's written consent.
Each rule has a test; a change that breaks one is a BLOCKER.

1. **No money movement.** The eToro adapter (`internal/adapters/etoro`) talks only to the
   endpoints in its `allowlist.go`: market data, instrument search/eligibility, costs,
   portfolio/PnL/order info, open order, close position, cancel order, position SL/TP.
   Money, transfer, balance-history, cash-account, sub-account, copy-trading, agent-portfolio,
   SSO, watchlist, social and Partners-API paths must not exist in the client at all.
   `allowlist_test.go` fails the build when any other `/api/` path literal appears.
2. **Never spend more than is in the account.** Before every order fetch available cash from
   eToro (no cache older than a few seconds) and reject when
   `amount > min(available cash, remaining trading budget)`. The trading budget is set in the
   UI and can never exceed account equity.
3. **Leverage is always 1. Exactly two order shapes exist, both constants in the adapter:**
   a long is `buy` + settlementType `real` (the share itself); a short is `sellShort` +
   settlementType `cfd` (owner's written decision, 2026-11-23: shorts on demo and real
   accounts, real still behind the phase-7 gate). Every short must carry a stop-loss above
   the entry, is closed before the session ends like every position, and all shorts together
   (open + pending) may use at most 25 % of the account's trading budget. Check eligibility
   (real/long/x1 for a long, cfd/short/x1 for a short) before the first trade in an
   instrument; refuse any other settlement type, direction or leverage.
4. **No margin, no orders that could together exceed cash:** reserve cash for every open order.
5. **The LLM never calls the broker.** No tools, no credentials, no network path to eToro;
   it only returns a structured verdict and can only shrink or reject.
6. **Fail closed.** Broker error, stale prices, LLM timeout, clock uncertainty, failed
   reconciliation or any unhandled state = no new orders. Exits must still work.
7. **Idempotency.** Every order carries a unique `x-request-id` persisted *before* sending; a
   retry reuses it. On start-up reconcile against the eToro portfolio before anything else.

**Demo by default.** Real mode only through the gated switch (≥ N qualifying days of demo
history, N set per real account by its admin, 5–60, default 20 — owner's decision D36,
2026-11-27; admin, typed confirmation phrase, banner on every page). Never run real-mode orders
yourself. Never put real credentials in code, tests, fixtures, logs or commits.

## 3. Architecture — non-negotiable
- `internal/domain` (pure Go, no I/O, no `time.Now()`), `internal/app` (use cases),
  `internal/ports` (interfaces + `fakes/`), `internal/adapters/*`, `cmd/cactustrade` (the only
  composition root). Imports point inward only (depguard enforces it).
- Ports: `Broker`, `MarketData`, `SignalModel`, `Reviewer`, `RiskEngine`, `Clock`,
  `MarketCalendar`, `SecretStore`, `Repository` (per aggregate), `EventBus`.
- **Both databases, always.** Every repository change lands in `migrations/sqlite` *and*
  `migrations/mariadb` with the same version number, uses SQL both accept (`?` placeholders,
  no `RETURNING`, no `ON CONFLICT`/`ON DUPLICATE KEY` — upsert in Go), and passes the shared
  repository test suite against SQLite (`make test`) and MariaDB (`make test-int`).
- Money is `int64` minor units (USD cents for the eToro account); never `float64` in domain
  logic. Floats are allowed only inside the statistical model (HMM) and at the eToro JSON edge.
- No business, risk or authorization logic in handlers, SQL or React.
- Standard library first. A new dependency needs a reason in `docs/PLAN.md` and a licence in
  MIT, BSD, ISC, Apache-2.0, MPL-2.0 or public domain.

## 4. eToro rules
- The live docs (`https://api-portal.etoro.com/llms.txt`, `openapi.json`) are the source of
  truth. `docs/etoro-api.md` records what was verified and when. If the docs contradict the
  brief, or something is impossible through the API, **stop and tell the owner**.
- Demo/real comes from the account a call is for (`ports.WithAccount`; the eToro client
  refuses any call without one); paths come from the allowlist table, which holds both
  variants, and keys from that account's own pair.
- A 200 on create-order means *accepted*, not filled: poll `orders:lookup` by `referenceId`
  (our `x-request-id`). Positions close by `positionId`.
- Client-side rate limiter per eToro quota group; honour `Retry-After`; exponential backoff.
- No third-party eToro SDKs. Recorded-fixture tests and a fake broker.

## 5. Quality bar — `make check`
gofumpt · golangci-lint · go vet · `go test -race` with ≥ 80 % coverage on domain and app ·
MariaDB integration tests (`make test-int`, testcontainers) · govulncheck · contract drift ·
migration parity · `pnpm -C web check`. Never bypass a gate (`//nolint`, `t.Skip`, lowered
thresholds, deleted tests). Property tests on the risk engine from phase 4.

## 6. Commands
```
make gen          # oapi-codegen + web API types
make check        # the quality bar
make test         # unit tests (SQLite repository suite included)
make test-int     # MariaDB integration tests (Docker)
make build        # bin/cactustrade with the web app embedded
make build-in-container   # the same, needing only Docker
make run          # standalone: SQLite in ./data, TLS self-signed, https://localhost:8443
./deploy.sh --install|--upgrade|--version|--status|--backup-now|--uninstall|--delete-uninstall \
            --dockerdesktop|--podman|--k3s|--k8s [--ssh-access user@host] [--yes]
```

## 7. Never
- Log secrets, API keys, session tokens or passwords — log IDs. Secrets are write-only.
- Put secrets in `.env` (non-secret defaults only), the image, fixtures or commits.
- Edit a committed migration — add a new one (to both dialects).
- Use `float64` for money; hold a position overnight; send an order without a persisted
  request id; let the LLM originate or enlarge a trade.
- Claim profitability anywhere — code comments, UI or docs.

## 8. Report format
```
## Report: <goal>
**Status:** done | partial | blocked
### What was done        (PLAN ids, files)
### Decisions made       (and where recorded)
### Verification         (make check output summary, coverage)
### Open questions / risks
### Next suggested step
```
