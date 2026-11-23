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
| `MarketCalendar`, `MarketData`, `Broker` | phase 2 / 4 |
| `SignalModel` | phase 3 |
| `RiskEngine` | phase 4 |
| `Reviewer` | phase 5 |

The `Broker` port has no money-movement operation. Its `Open` takes a `domain.OrderIntent`,
which has no leverage, direction or settlement field.

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
- **Containers:** distroless, non-root (65532), read-only root filesystem, all capabilities
  dropped, `no-new-privileges`, and secrets as files.

## Observability

JSON logs (`log/slog`): one line per request with method, path, status, duration, client IP
and user. Every line carries the trading mode, and no line contains a body, cookie or token.
`/healthz` is liveness. `/readyz` checks that the database answers. `/metrics` serves
Prometheus HTTP, Go and process metrics, behind a bearer token when one is configured.
