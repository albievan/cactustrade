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

**Status: phases 1–2 done, phase 3 done except its live check (3.6), phases 4 and 5 done except the owner's live runs (4.17, 5.8), phase 6 done and awaiting the owner's review, of 7.** Sign-in,
roles, API keys, the encrypted write-only credential store, deployment, the eToro client,
market sessions (LSE, NYSE/NASDAQ, ASX), watchlists, the regime model, playbooks and
walk-forward backtester (`cactustrade backtest --instrument ID`), and now **demo execution**:
the risk engine, the agent (entries at each 5-minute bar close), the supervisor (exits), the
kill switch, reconciliation with eToro and the Orders page, plus the **decision log** (every
idea the agent had, what the LLM reviewer and the risk engine said, and the order it led to,
with the reviewer's prompt and answer) and the LLM usage and cost per profile, from prices you
enter (unknown without them). Phase 6 adds the **live dashboard** and the **History** page
with CSV exports (see below). **Demo accounts only:** a real-money
account is refused in five places until the phase-7 gate. The execution code has been tested
against a simulated eToro account; it has **not yet run against eToro's demo** (PLAN 4.17,
the owner's run) and several eToro behaviours are still unverified
([`docs/etoro-api.md`](docs/etoro-api.md), U1–U11). Backtests describe the past only; they are
not a forecast and not advice. See [`docs/PLAN.md`](docs/PLAN.md).

> **Before you start the agent.** On a demo account the agent opens and closes positions by
> itself. It can lose the account's demo money; the limits only bound how fast. The
> supervisor also watches the account's whole equity: a fall of the drawdown limit (default
> 10 %) — including from positions you opened yourself, or a withdrawal — trips the kill
> switch, which stops entries and closes the positions **cactusTrade** opened (never yours).
> Shorts are CFDs: their loss is not capped by the amount invested, and they carry overnight
> fees and margin; each has a stop above the entry and every position is closed before the
> session ends. A position eToro has not stopped out keeps its eToro-side stop when
> cactusTrade is stopped or shut down; nothing closes it until cactusTrade runs again.

## Money guardrails (enforced in code, covered by tests)

- **No money movement.** The eToro client may call only the endpoints in
  [`allowlist.go`](internal/adapters/etoro/allowlist.go): market data, eligibility, costs,
  portfolio, order info, open, cancel, close and SL/TP. It has no transfer, deposit, withdrawal,
  balance-history, sub-account, copy-trading or social endpoints. A test parses the package
  source and fails the build if any other `/api/` path appears.
- **Leverage 1, exactly two order shapes.** A long is `buy` + `settlementType: real` (the
  share); a short is `sellShort` + `cfd`, with a stop above the entry, all shorts together at
  most 25 % of the trading budget. These are constants, not parameters.
- **Never spend more than is in the account.** Orders are capped at `min(available cash,
  remaining trading budget)`, using fresh balances, with cash reserved for pending orders.
- **The LLM can only approve, shrink or reject.** It has no tools and no path to the broker
  (depguard enforces this). It fails closed, exits always work, and orders are idempotent
  through request ids stored before they are sent. Start-up reconciles with eToro before
  anything trades.
- **No order without its reason.** Each candidate's decision (and the LLM call that reviewed
  it) is stored before the order row; if that write fails, nothing is sent. Decisions and LLM
  calls, prompts and answers included, are kept 400 days.

The full list is in [`CLAUDE.md` §2](CLAUDE.md).

## Live dashboard and history

- **Dashboard.** The current account's figures arrive over a live stream (server-sent events,
  `GET /api/v1/live`): equity, cash left after reservations, trading budget used and left, the
  day's P&L, the distance to every risk limit as a bar, the open positions (cactusTrade's own
  and the others, with stop, target and the time cactusTrade closes its own), an orders and
  fills feed, the latest regime per watchlist share and the agent's state. A **stale** badge
  shows when nothing arrived for 30 seconds. If the stream fails, the page refreshes every
  15 seconds instead and keeps retrying; the kill switch and the agent's buttons never depend
  on the stream. Nothing on the dashboard places an order.
- **History** (Trading → History). Closed trades are read from eToro every 15 minutes
  (read-only; the first read covers 90 days). The page shows neutral counts and sums of the
  trades of positions cactusTrade opened (trades, wins / losses / breakeven, win rate, fees,
  realised P&L, maximum drawdown), the figures per risk day, the equity curve with its
  drawdowns and the trades table (cactusTrade's or all, by date, paged). Fees eToro did not
  report are shown as *unknown*, never as 0. Figures describe past results only; they do not
  predict future results.
- **CSV exports** of trades, daily figures, equity snapshots, orders and decisions (viewers and
  API keys may export): UTC times, exact dollar amounts, and any cell a spreadsheet could take
  for a formula prefixed with `'`. At most 50 000 rows; a larger export is refused, never cut.

## Quick start: standalone (SQLite)

```sh
make build                 # needs Go 1.26 + Node 22/pnpm, or: make build-in-container
./bin/cactustrade          # https://localhost:8443 (self-signed certificate)
```

Or install a published release (Linux, macOS, Windows; amd64 or arm64) into the current
folder. The setup scripts install only a release whose `SHA256SUMS` carries the release
signature (`ssh-keygen -Y`, OpenSSH 8.1+) and lists the program's checksum; they keep the
previous program as `cactustrade.old`. *No release key is published yet: until the owner
publishes one (docs/RUNBOOK.md, "Releases"), both scripts refuse and you build from source.*

```sh
curl -fsSL https://raw.githubusercontent.com/albievan/cactustrade/main/setup.sh | sh        # Linux, macOS
irm https://raw.githubusercontent.com/albievan/cactustrade/main/setup.ps1 | iex             # Windows PowerShell
```

On first start, the data folder (`./data`) gets:

| File | What it is |
|---|---|
| `cactustrade.db` | The SQLite database |
| `master.key` | The AES-256 key that encrypts your eToro/LLM keys. **Back it up off the machine.** |
| `tls/` | A self-signed certificate. Set `CACTUSTRADE_TLS_CERT` / `CACTUSTRADE_TLS_KEY` to use your own. |
| `locks/` | A lock file while the server runs (a restore refuses then), removed when it stops. |

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
./deploy.sh --restore FILE [--identity FILE] [--dry-run]  --<target>
```

`.env` holds non-secret defaults. Put per-machine overrides in `.env.local`, for example
`HTTPS_PORT=8453` when port 8443 is taken. On k3s/k8s the pod speaks HTTP behind the cluster's
ingress, and HTTPS terminates at Traefik (`K8S_HTTPS=true`, the default): the app's Service is
then `ClusterIP`, reachable only through the ingress. `K8S_HTTPS=false` publishes plain HTTP on
`K8S_NODE_PORT` of every node instead (a trusted lab network only; `deploy.sh` warns).
`./deploy.sh --render --k3s` prints the manifest `--install` would apply, without touching a
cluster.

Network and container hardening (PLAN 7.1.16): on k3s/k8s two NetworkPolicies let MariaDB
take connections on 3306 only from the app pod, and the app only from the ingress controller's
pods (`K8S_INGRESS_NAMESPACE`, `K8S_INGRESS_POD_SELECTOR`; k3s: Traefik in `kube-system`). They
need a network plugin that enforces NetworkPolicy (k3s's built-in one does). Only the
addresses in `K8S_TRUSTED_PROXIES` may set `X-Forwarded-For`; the default is nobody
(`deploy.sh` warns) — set it to the ingress controller's pod addresses as described in
[`docs/RUNBOOK.md`](docs/RUNBOOK.md) ("Trusted proxies on k3s/k8s"). MariaDB runs as its own
user (999) with no capabilities, no privilege gain and a read-only root file system in k8s and
compose; compose's `volume-init` hands it `MARIADB_DATA_DIR`.

## Backups and restore

Backups are encrypted with [age](https://age-encryption.org) to a public key you choose; the
private key (the *identity*) belongs off this machine. A backup never contains the master key:
keep a copy of `master.key` / `SECRETS_DIR/master_key` apart as well, or the stored eToro and
LLM keys cannot be read after a restore. Create a key pair once, with the `age-keygen` tool
or any age implementation, and keep the identity file safe:

```sh
age-keygen -o backup.agekey     # prints "Public key: age1…"
```

**Scheduled backups (containers).** `deploy.sh` can back up on a schedule on the machine that
runs cactusTrade (macOS: a launchd job; Linux: one crontab line), into any folder you choose
(an external disk or a synced folder too), keeping the newest N:

```sh
./deploy.sh --backup-schedule "daily 02:30" --backup-dir ~/cactustrade-backups --backup-keep 14 --dockerdesktop
./deploy.sh --backup-schedule "every 6h" --dockerdesktop     # or "hourly"; the folder and N are remembered
./deploy.sh --backup-schedule off --dockerdesktop             # stop
./deploy.sh --status --dockerdesktop                          # shows the schedule, folder and newest backup
```

Each run is `--backup-now` (encrypted as below), logged to `backup-schedule.log` in the folder;
times are the machine's local time, and a Mac must be awake and logged in. The folder, `N` and
the schedule are saved in `.env.local` (`BACKUP_DIR`, `BACKUP_KEEP`, `BACKUP_SCHEDULE`);
`--backup-prune` applies `N` now. Only cactusTrade's own `cactustrade-<time>.sql.gz[.age]` files
are ever removed. On macOS a folder under Documents, Desktop or an external disk may need
*Full Disk Access* for `/bin/bash` (System Settings → Privacy & Security).

**Standalone (SQLite).** The server may keep running while you back up:

```sh
CACTUSTRADE_BACKUP_RECIPIENT=age1… ./bin/cactustrade backup --out cactustrade-2026-11-24.db.age
```

Without a recipient nothing is written. To restore, stop the server first (a restore refuses
while one uses the data folder or holds an account lease), then:

```sh
./bin/cactustrade restore --in cactustrade-2026-11-24.db.age --identity backup.agekey --yes
```

The file must decrypt with the identity, be a cactusTrade database that passes SQLite's
integrity check, and have a schema this version knows (an older one is migrated up). The
replaced database stays beside it as `cactustrade.db.pre-restore-<time>`. Everyone signs in
again, and on the next start cactusTrade reconciles every account with eToro before any agent
or exit loop runs.

**Containers (MariaDB).** Put the public key in `.env.local` as
`CACTUSTRADE_BACKUP_RECIPIENT=age1…` (or a recipients file in
`CACTUSTRADE_BACKUP_RECIPIENT_FILE`). `./deploy.sh --backup-now --<target>` then writes
`BACKUP_DIR/cactustrade-<time>.sql.gz.age`; without a recipient it writes a plain
`.sql.gz` (mode 600) and warns. `--upgrade` takes one first.

```sh
./deploy.sh --restore backups/cactustrade-20261124T120000Z.sql.gz.age --identity backup.agekey --dry-run --dockerdesktop
./deploy.sh --restore backups/cactustrade-20261124T120000Z.sql.gz.age --identity backup.agekey --dockerdesktop
```

`--dry-run` only checks that the file decrypts, decompresses and is a cactusTrade dump this
version can restore. Without it, `--restore` asks you to type `restore`, takes a backup of the
current database, stops the app, waits for any account lease to expire, loads the dump into a
scratch database, checks its schema and swaps it in (the replaced tables stay in
`<database>_prerestore` until the next restore), signs everyone out and starts the app, which
reconciles with eToro first. Decryption uses `bin/cactustrade` or `age` on this machine when
present, else the app's own image; the identity is passed over stdin, never as a file,
argument or variable inside a container. With `--ssh-access`, copy the backup to the host and
run `--restore` there.

## Real money

> **Real money is behind two keys** (the owner's written consent, D39, enabled the build:
> PLAN 7.19–7.23). An entry on a real-money eToro account is made only while **all** of these
> hold: (1) the account's real-trading gate is **armed** on its *Real trading* page (Setup) by
> an admin who owns it, with the password and the phrase `ENABLE REAL TRADING <account name>`,
> after the qualifying demo trading days on a linked demo account (the admin's number, 1–60,
> default 5, in the last 60 risk days) and with limits within the real-money ceilings; (2) the
> deployment allows it: **`CACTUSTRADE_REAL_TRADING=on`** (off by default; `.env` keeps it off,
> `deploy.sh` never sets it — put it in `.env.local` or the environment yourself); (3) the
> account is reconciled, its agent was started by hand (a real agent never resumes by itself
> after a restart), and each entry takes a fresh permit. Exits — closing what cactusTrade
> opened before the session ends, the kill switch, force close — work on a real account
> whatever the gate. Anyone with trade access can **disarm** at any time (also with an API
> key); a kill-switch trip, a key change, raising a limit, a restore and 30 days each disarm by
> themselves. Trading real money can lose it; demo results describe the past and do not
> predict real ones.

## eToro API keys

1. In eToro: **Settings → Trading → API Key Management → Create New Key**.
2. Choose the environment (**Demo** first; a key works in exactly one environment) and the
   permissions, then verify by SMS. Optionally restrict the key to your server's IP address.
3. Copy the **Public API Key** (sent as `x-api-key`) and the **User Key** (sent as `x-user-key`).
4. Give them to cactusTrade in one of two ways:
   - **Web UI → Platform accounts → your account → Keys.** The account's owner, and
     write-only: a key is never shown, logged or returned again. Keys change only while the
     account's agent is stopped with no position or order of cactusTrade's open; the account
     is then reconciled again before the agent can start.
   - **Environment (demo only).** Set `ETORO_DEMO_PUB` and `ETORO_DEMO_PK`, or the `_FILE`
     variants that name a file. Credentials set this way are read-only in the UI.

Even if a key allows more (transfers, copy trading…), cactusTrade cannot use those endpoints:
the client contains only the allowlisted paths. A real-money key sends orders only under the
two keys of *Real money* above (entries) or to close and cancel what cactusTrade opened (exits);
it needs eToro's **Write** permission for that.

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

## Saxo and Tiger Brokers accounts (view-only)

Like Trading 212, Saxo and Tiger Brokers accounts are **shown, never traded**: they appear on
the *Overview* page (cash, total value, open positions, equity chart) and every trading,
agent, scanner, watchlist and theme path refuses them. Each client has its own allowlist and
a test that fails the build on anything else: Saxo may only GET `/port/v1/balances/me` and
`/port/v1/positions/me` (plus the sign-in host's `/token`); Tiger may only call the
`prime_assets`, `assets` and `positions` methods. Amounts stay in the account's own currency
(JPY and KRW in whole units). A position whose value the platform does not report in the
account currency is shown as *unknown*, never converted or guessed.

**Saxo** (`docs/saxo-api.md`):

1. On developer.saxo create a **Code Flow** app: a SIM app for a demo account, a Live app for a
   real one (Live apps need Saxo's approval).
2. Set `CACTUSTRADE_PUBLIC_URL` and register the redirect address shown on the account card:
   `<CACTUSTRADE_PUBLIC_URL>/api/v1/oauth/saxo/callback`.
3. **Trading Platform Accounts → New account**, platform *Saxo*: paste the **app key** and
   **app secret**, then press **Connect to Saxo** and sign in at Saxo. The refresh token is
   stored encrypted and renewed automatically (every 15-minute snapshot keeps it alive); if it
   lapses, the account says *Reconnect to Saxo*.
4. Alternatively, for a SIM account only: paste a **24-hour developer token** (it stops working
   after 24 hours; the card shows its age).

**Tiger Brokers** (`docs/tiger-api.md`):

1. In the Tiger developer console (Open API) note your **tiger_id** and **account number**.
2. Generate an RSA key pair, upload the **public** key in the console.
3. **Trading Platform Accounts → New account**, platform *Tiger Brokers*: paste the Tiger ID,
   the account number and the **private key** (PEM, multi-line; write-only). A paper account
   number for a demo account. Tiger paper accounts have no Australian market.

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
| `CACTUSTRADE_METRICS_TOKEN[_FILE]` | generated into `<data>/metrics.token` | Bearer token that `/metrics` always requires (generated on first start when unset; read it from the file) |
| `CACTUSTRADE_COOKIE_SECURE` | `true` | Secure session cookie `__Host-cactustrade_session` (keep it on; `false` names it `cactustrade_session` for a plain-HTTP test) |
| `CACTUSTRADE_TRUSTED_PROXIES` | — | CIDRs allowed to set `X-Forwarded-For` |
| `CACTUSTRADE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` (JSON logs) |
| `ETORO_DEMO_PUB` / `ETORO_DEMO_PK` (`_FILE`) | — | eToro demo key pair |
| `CACTUSTRADE_PUBLIC_URL` | Docker: `https://<LAN address>:${HTTPS_PORT}` | The address in e-mailed links and Saxo's sign-in redirect; empty = no password reset by e-mail and no "Connect to Saxo" |
| `CACTUSTRADE_SMTP_HOST`, `_PORT`, `_TLS`, `_USER`, `_FROM` | Docker: `mailpit`, `1025`, `none` | The fallback mail server, used while none is switched on under *Setup → E-mail*; empty host = none |
| `CACTUSTRADE_SMTP_PASSWORD` / `_FILE` | `${SECRETS_DIR}/smtp_password` | Its password (never in `.env`) |
| `CACTUSTRADE_BACKUP_RECIPIENT` / `_RECIPIENT_FILE` | — | age public key(s) backups are encrypted to (see *Backups and restore*); none = no app backup, and a plain gzip with a warning from `deploy.sh` |
| `CACTUSTRADE_BACKUP_IDENTITY_FILE` | — | The age identity (private key) a restore decrypts with, instead of `--identity` |
| `COMPOSE_PROFILES`, `MAILPIT_PORT` | `mailpit`, `8035` | Docker: the Mailpit test mailbox at `http://localhost:8035` (this machine only, no sign-in) |
| `CACTUSTRADE_SMTP_TEST_MAILBOX_RESETS` | `false` | While the only mail server is the Mailpit test mailbox (host `mailpit`), "Forgot password" is off: its mail never reaches the user. `true` sends reset links there anyway (a single-machine trial only) |
| `CACTUSTRADE_SECRETS` | `db` | Where the eToro, LLM, mail and Saxo credentials are kept: `db` (encrypted in the database under the master key) or `vault` (see *Vault*) |
| `CACTUSTRADE_VAULT_ADDR`, `_MOUNT`, `_PREFIX`, `_NAMESPACE` | —, `secret`, `cactustrade`, — | Vault's address (`https://`; `http://` only to this machine), the KV v2 mount, the folder under it, a Vault Enterprise namespace |
| `CACTUSTRADE_VAULT_TOKEN_FILE` | `${SECRETS_DIR}/vault_token` when it exists | A file holding the Vault token (Vault Agent's sink, or your own) |
| `CACTUSTRADE_VAULT_K8S_ROLE`, `_K8S_MOUNT`, `_K8S_JWT_FILE` | —, `kubernetes`, the pod's service-account token | Vault's Kubernetes auth instead of a token file |
| `CACTUSTRADE_VAULT_CA_FILE` | — | PEM certificates trusted for Vault besides the system's |
| `K8S_INGRESS_NAMESPACE`, `K8S_INGRESS_POD_SELECTOR` | `kube-system`, `app.kubernetes.io/name=traefik` | k3s/k8s: the ingress controller whose pods alone may reach the app (NetworkPolicy) |
| `K8S_TRUSTED_PROXIES` | — (nobody) | k3s/k8s: the ingress controller's pod addresses / CIDRs, written into `CACTUSTRADE_TRUSTED_PROXIES` (RUNBOOK) |

### Vault (optional)

By default the credentials are encrypted in the database. With `CACTUSTRADE_SECRETS=vault`
they live in HashiCorp Vault's KV version 2 engine instead, one secret per credential at
`<mount>/<prefix>/<name>` holding `{"value": …}` (ADR-003). cactusTrade needs a policy with
`create`, `read`, `update`, `delete` and `list` on `<mount>/data/<prefix>/*` and
`<mount>/metadata/<prefix>/*`, nothing else. It renews its token before it expires and never
follows a redirect (point the address at the active node or its load balancer). Move existing
credentials once, with the server stopped or running:

```sh
CACTUSTRADE_VAULT_ADDR=https://vault.example:8200 CACTUSTRADE_VAULT_TOKEN_FILE=./vault_token \
  ./cactustrade secrets migrate --to vault --dry-run     # lists the names; then without --dry-run
```

It copies every stored credential (reading each one back), prints only the names, and leaves
the database copies in place; then set `CACTUSTRADE_SECRETS=vault` and restart. The master key
is not needed while Vault holds the credentials. If Vault is down when cactusTrade starts, it
starts without keys (nothing trades, the kill switch and web UI work); a running server keeps
the keys it has already read. The k8s manifest has no service-account token in the pod; for
Kubernetes auth add a projected token volume and point `CACTUSTRADE_VAULT_K8S_JWT_FILE` at it.

### E-mail

Set the real mail server in the app under **Setup → E-mail** (Gmail by default: turn on
2-Step Verification, create an *app password*, paste it; the page has a step-by-step guide
for Gmail, Microsoft 365 and other providers, and a test button). Until then, Docker installs
send through the bundled Mailpit test mailbox, so nothing leaves the machine — and
"Forgot password?" is off meanwhile, since a reset link in Mailpit never reaches the user
(PLAN 7.1.15; the public status reports it unavailable, a reset request answers why, and the
E-mail page tells the admin; `CACTUSTRADE_SMTP_TEST_MAILBOX_RESETS=true`
overrides it for a single-machine trial). Each user sets an address and chooses what to be
e-mailed about (trades, watchlist changes, alerts) under **Your account**; the same address
receives password-reset links ("Forgot password?" on the sign-in page). Mail waits in an outbox
and is retried; the outbox is on the E-mail page.

**Trading statements** (*Setup → System → Trading statements*): schedules that e-mail a PDF
statement daily (Monday to Friday, 22:30 UTC), weekly (Saturday 06:00 UTC) or monthly (the
1st, 06:00 UTC) to up to 10 addresses — the combined statement of the accounts you own and,
as you choose, one per account and one per exchange, attached to one e-mail per recipient.
Each statement opens with a summary written from its figures (no LLM) and shows equity per
currency (never added across currencies), cactusTrade's closed trades, open positions at the
end, drawdown, risk events, agent activity and LLM use. *Preview* downloads a PDF without
e-mailing it; *Send now* e-mails the last complete period. Each period is sent once, also
across restarts. Statements describe past records only.

## API

The API is OpenAPI-first ([`api/openapi/cactustrade.yaml`](api/openapi/cactustrade.yaml)).
Swagger UI is at `/api/docs` and the contract at `/api/openapi.yaml`. Programs use API keys
(`Authorization: Bearer ctk_…`) created on the *API keys* page. A key's role never exceeds its
owner's. Roles: `viewer` (read-only), `operator` (also start/stop/kill switch), `admin`.
Health and monitoring endpoints: `/healthz`, `/readyz`, `/metrics` (Prometheus, always
behind the metrics token) and `GET /api/v1/system/health` (admin: 503 when a trading loop
is not alive, naming the account). Alert rules: [`docs/RUNBOOK.md`](docs/RUNBOOK.md).

Every `/api/` route is rate-limited per user or API key and per address: reads 600/min,
other methods 60/min, CSV exports 10/min, live-stream connects 30/min; over a limit the
answer is 429 with `Retry-After`. **Pause, stop and the kill switch are never rate-limited.**
Request bodies are capped at 1 MiB (JSON bodies at 64 KiB; more is 413). Every log line about
an account names its `account_id` and `mode` (`demo` / `real`).

> **The first start after upgrading to the `__Host-` cookie (PLAN 7.4) signs everyone out
> once.** The session cookie is now called `__Host-cactustrade_session` (the browser then
> only accepts it over HTTPS, for the whole site, from this host only). The old
> `cactustrade_session` cookie is no longer accepted and is cleared; sign in again.

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
