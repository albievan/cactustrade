# cactusTrade

An autonomous share day-trading agent for **eToro accounts** (each user's own, shareable): long
real shares and, within a capped budget, short CFDs at leverage 1 — through the
official eToro Public API. One Go binary with the React web app embedded. Standalone it needs
nothing else (SQLite); in containers it runs with MariaDB on Docker Desktop, Podman, k3s or
Kubernetes using the same `deploy.sh` as the other cactus apps.

> **Risk warning.** cactusTrade is software, not financial advice. Trading shares can lose
> money, including all of the money you let it trade. Nothing in this project claims or
> implies that it is profitable. It runs in **demo mode by default**. Real money needs a
> deliberate, gated switch, and you use it entirely at your own risk (see `LICENSE`).

**Status: phases 1–2 done, phase 3 in progress (of 7).** Sign-in, roles, API keys, the encrypted write-only
credential store, deployment, and now a **read-only** eToro client: test connection, account
cash/equity/positions, market sessions (LSE, NYSE/NASDAQ) and a watchlist of eligible shares
with live quotes (WebSocket stream, REST fallback), and the phase-3 regime model, playbooks
and walk-forward backtester (`cactustrade backtest --instrument ID`). **It cannot trade yet:**
order operations are refused inside the client until phase 4 (demo orders). Backtests
describe the past only; they are not a forecast and not advice. See [`docs/PLAN.md`](docs/PLAN.md).

## Money guardrails (enforced in code, covered by tests)

- **No money movement.** The eToro client may call only the endpoints in
  [`allowlist.go`](internal/adapters/etoro/allowlist.go): market data, eligibility, costs,
  portfolio, order info, open, cancel, close and SL/TP. It has no transfer, deposit, withdrawal,
  balance-history, sub-account, copy-trading or social endpoints. A test parses the package
  source and fails the build if any other `/api/` path appears.
- **Leverage 1, `buy`, `settlementType: real`.** These are constants, not parameters.
- **Never spend more than is in the account.** Orders are capped at `min(available cash,
  remaining trading budget)`, using fresh balances, with cash reserved for pending orders.
  *(Enforced from phase 4.)*
- **The LLM can only approve, shrink or reject.** It has no tools and no path to the broker
  (depguard enforces this). It fails closed, exits always work, and orders are idempotent
  through persisted request ids. *(Phases 4–5.)*

The full list is in [`CLAUDE.md` §2](CLAUDE.md).

## Quick start: standalone (SQLite)

```sh
make build                 # needs Go 1.26 + Node 20/pnpm, or: make build-in-container
./bin/cactustrade          # https://localhost:8443 (self-signed certificate)
```

On first start, the data folder (`./data`) gets:

| File | What it is |
|---|---|
| `cactustrade.db` | The SQLite database |
| `master.key` | The AES-256 key that encrypts your eToro/LLM keys. **Back it up off the machine.** |
| `tls/` | A self-signed certificate. Set `CACTUSTRADE_TLS_CERT` / `CACTUSTRADE_TLS_KEY` to use your own. |

Open the page (`http://` is redirected to `https://`), accept the certificate warning, and sign
in as **`admin` / `admin`**. You must choose a new password (12+ characters) before anything
else works.

## Quick start: containers (MariaDB)

```sh
export ETORO_DEMO_PUB=…    # optional: eToro demo "Public API Key"
export ETORO_DEMO_PK=…     # optional: eToro demo "User Key"
./deploy.sh --install --dockerdesktop      # or --podman, --k3s, --k8s, plus --ssh-access user@host
```

`deploy.sh` builds the image (everything is built inside containers) and starts MariaDB 11
and the app. It generates the database passwords, the master key and the metrics
token into `SECRETS_DIR` (k8s: secret `cactustrade-secrets`), then prints the URL. Sign in
as `admin` / `admin` and choose a new password. Other actions:

```
./deploy.sh --version|--status|--backup-now|--upgrade [--to REF]|--uninstall|--delete-uninstall  --<target>
```

`.env` holds non-secret defaults. Put per-machine overrides in `.env.local`, for example
`HTTPS_PORT=8453` when port 8443 is taken. On k3s/k8s the pod speaks HTTP behind the cluster's
ingress, and HTTPS terminates at Traefik (`K8S_HTTPS`).

## eToro API keys

1. In eToro: **Settings → Trading → API Key Management → Create New Key**.
2. Choose the environment (**Demo** first; a key works in exactly one environment) and the
   permissions, then verify by SMS. Optionally restrict the key to your server's IP address.
3. Copy the **Public API Key** (sent as `x-api-key`) and the **User Key** (sent as `x-user-key`).
4. Give them to cactusTrade in one of two ways:
   - **Web UI → Credentials.** Admin only, and write-only: a key is never shown, logged or
     returned again.
   - **Environment (demo only).** Set `ETORO_DEMO_PUB` and `ETORO_DEMO_PK`, or the `_FILE`
     variants that name a file. Credentials set this way are read-only in the UI.

Even if a key allows more (transfers, copy trading…), cactusTrade cannot use those endpoints:
the client contains only the allowlisted paths. Real-money keys stay unused until the real-mode
gate in phase 7: at least 20 demo trading days, admin, a typed confirmation phrase, and a
banner on every page.

## Trading 212 accounts (view-only)

cactusTrade can **show** Trading 212 accounts next to your eToro accounts on the *Overview*
page (cash, equity, unrealised P&L, open positions and an equity chart). It does **not** trade
them: the agent, scanner, watchlist, themes and risk limits refuse a Trading 212 account, and
the Trading 212 client (`internal/adapters/trading212`) can only send GET requests to three
allowlisted read paths. Amounts stay in the account's own currency (often GBP or EUR);
different currencies are never added up.

To create a read-only key:

1. In the Trading 212 app: **Settings → API (Beta) → Generate API key**.
2. Give it **only read permissions**: account, portfolio, history. Restricting it to your
   server's IP address is optional. A key works in one environment: a demo (practice) key for a
   demo account, a live key for a real one.
3. In cactusTrade: **Trading Platform Accounts → New account**, platform *Trading 212*, then
   paste the **API key** and the **API secret** (write-only, encrypted, never shown again).

The overview reads every account when you open it; equity snapshots are recorded every 15
minutes and kept 400 days. See `docs/trading212-api.md` for the verified API facts.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CACTUSTRADE_DB_DRIVER` | `sqlite` | `sqlite` (standalone) or `mariadb` (containers) |
| `CACTUSTRADE_DATA_DIR` | `./data` (`/data` in the image) | Database, master key, certificate |
| `CACTUSTRADE_ADDR` | `:8443`, or `:8080` with TLS off | Listen address |
| `CACTUSTRADE_TLS` | `auto` | `auto` = your certificate or a self-signed one (plain HTTP on the same port redirects to HTTPS); `off` = behind a TLS proxy |
| `CACTUSTRADE_TLS_CERT` / `_KEY` | — | PEM certificate and key |
| `CACTUSTRADE_MASTER_KEY` / `_FILE` | generated (standalone only) | 64 hex characters |
| `CACTUSTRADE_SECRETS_DIR` | — | Folder holding `db_password`, `master_key`, `metrics_token` |
| `CACTUSTRADE_DB_HOST`, `_NAME`, `_USER`, `_PASSWORD[_FILE]` | `localhost`, `cactustrade`, `cactustrade` | MariaDB connection |
| `CACTUSTRADE_METRICS_TOKEN[_FILE]` | — | Bearer token that `/metrics` requires, when set |
| `CACTUSTRADE_COOKIE_SECURE` | `true` | Secure session cookie (keep it on) |
| `CACTUSTRADE_TRUSTED_PROXIES` | — | CIDRs allowed to set `X-Forwarded-For` |
| `CACTUSTRADE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` (JSON logs) |
| `ETORO_DEMO_PUB` / `ETORO_DEMO_PK` (`_FILE`) | — | eToro demo key pair |
| `CACTUSTRADE_PUBLIC_URL` | Docker: `https://<LAN address>:${HTTPS_PORT}` | The address in e-mailed links; empty = no password reset by e-mail |
| `CACTUSTRADE_SMTP_HOST`, `_PORT`, `_TLS`, `_USER`, `_FROM` | Docker: `mailpit`, `1025`, `none` | The fallback mail server, used while none is switched on under *Setup → E-mail*; empty host = none |
| `CACTUSTRADE_SMTP_PASSWORD` / `_FILE` | `${SECRETS_DIR}/smtp_password` | Its password (never in `.env`) |
| `COMPOSE_PROFILES`, `MAILPIT_PORT` | `mailpit`, `8035` | Docker: the Mailpit test mailbox at `http://localhost:8035` (this machine only, no sign-in) |

### E-mail

Set the real mail server in the app under **Setup → E-mail** (Gmail by default: turn on
2-Step Verification, create an *app password*, paste it; the page has a step-by-step guide
for Gmail, Microsoft 365 and other providers, and a test button). Until then, Docker installs
send through the bundled Mailpit test mailbox, so nothing leaves the machine. Each user sets
an address and chooses what to be e-mailed about (trades, watchlist changes, alerts) under
**Your account**; the same address receives password-reset links ("Forgot password?" on the
sign-in page). Mail waits in an outbox and is retried; the outbox is on the E-mail page.

## API

The API is OpenAPI-first ([`api/openapi/cactustrade.yaml`](api/openapi/cactustrade.yaml)).
Swagger UI is at `/api/docs` and the contract at `/api/openapi.yaml`. Programs use API keys
(`Authorization: Bearer ctk_…`) created on the *API keys* page. A key's role never exceeds its
owner's. Roles: `viewer` (read-only), `operator` (also start/stop/kill switch), `admin`.
Health and monitoring endpoints: `/healthz`, `/readyz`, `/metrics` (Prometheus).

## Development

```sh
make check          # gofumpt, golangci-lint (incl. layering), vet, tests + coverage gates,
                    # MariaDB integration tests, govulncheck, contract drift, web checks
make test           # unit tests (the SQLite repository suite and the allowlist guardrail)
make test-int       # the same repository suite against MariaDB 11 (Docker)
make check-in-container   # lint, tests and image build with only Docker installed
make etoro-spec     # re-download eToro's OpenAPI spec and check the allowlist against it
```

Rules for contributors and coding agents are in [`CLAUDE.md`](CLAUDE.md) and
[`.claude/agents/`](.claude/agents). Architecture: [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).
Verified eToro facts: [`docs/etoro-api.md`](docs/etoro-api.md).

## Licence

Public domain, on the same terms as SQLite (see [`LICENSE`](LICENSE)). Third-party modules
keep their own licences.
