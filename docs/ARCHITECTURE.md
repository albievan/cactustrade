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
| `SecretStore` | `secretstore.DB` (default) or `vault.Store` (`CACTUSTRADE_SECRETS=vault`, KV v2, standard library, PLAN 7.8), each under the `secretstore.Env` overlay |
| `Cipher` | `secretstore.Cipher` (AES-256-GCM; the credential name is the AAD) |
| `PasswordHasher` | `argon2` (argon2id, 64 MiB, t=3) |
| `Clock`, `Random` | `system` |
| `EventBus` | `eventbus` (in-process, never blocks a publisher) |
| `MarketCalendar` | `calendar` (LSE, NYSE/NASDAQ, ASX; tables 2026–2027) |
| `MarketData` | `etoro.Feed` (WebSocket stream, REST fallback) |
| `Broker` | `etoro.Client`: reads, plus open / close / cancel for **demo accounts only** (phase 4.6); `fakes.SimBroker` simulates a demo account for executor tests |
| `Orders`, `AgentPositions`, `RiskStates`, `InstrumentBlocks`, `AgentLeases` (repository) | `sqlstore` (migration 00009): our orders keyed by `x-request-id`, the positions cactusTrade opened, the risk state per account (versioned), blocked instruments, the single-writer lease (D22) |
| `T212Broker` | Trading 212 practice accounts only (PLAN R45, T212-a; adapter in R45.8): account, catalogue, open orders, one order, history; `Place` of a `domain.T212Intent` (market buy, sell stop, market sell of a held quantity) and `Cancel`; each sent at most once, never retried; a real Trading 212 account is refused before any key or request. `fakes.T212Sim` simulates a practice account for executor tests |
| `T212Instruments`, `InstrumentMatches` (repository) | Trading 212's instrument catalogue (one for all accounts, replaced atomically) and, per account, the confirmed or unconfirmed match of an eToro watchlist share to a Trading 212 ticker (D42); in-memory in `fakes` (R45.3), `sqlstore` from migration 00016 (R45.4) |
| `Holdings` | read-only holdings of platforms cactusTrade shows but does not trade: `trading212.Client` (GET account summary and positions only), `saxo.Client` (GET `/port/v1/balances/me`, `/port/v1/positions/me`), `tiger.Client` (signed POST of `prime_assets` / `assets` / `positions` only) — each with its own allowlist and allowlist test. eToro holdings are built in the app from `Broker.Portfolio` + instrument names |
| `OAuthConnector` | browser sign-in of a view-only platform: `saxo.Client` (authorize URL; code exchange and refresh token rotation at the sign-in host's `/token`, the client's only POST) |
| `EquitySnapshots` (repository) | `sqlstore` (`equity_snapshots`, migration 00008): equity every 15 minutes per account, kept 400 days |
| `SignalModel` | phase 3 |
| `Reviewer` | `adapters/llm` (Anthropic SDK; OpenAI-compatible for the others); profiles (with the admin's prices, OD13) in settings, keys `llm.<id>.api_key`. `Review` returns the verdict and the call's trace (request body as sent, raw response, served model, tokens, latency), filled even on error, never with the key |
| `Researcher` | `adapters/llm` (PLAN R29): web research for the watchlist. The provider's built-in web search is its only tool — Anthropic's `web_search_20250305` server tool (≤ 5 searches), OpenRouter's `web` plugin (no `tools` field); every other provider is "research not supported" and refused before any request. The answer is parsed strictly (`domain.ParseResearchAnswer`), checked again in the app, and each share goes through the watchlist's eligibility path (`checkAndAddToWatchlist`). It has no broker and never trades; state (runs, proposals) in settings `research.state.<account>` |
| `LLMCalls`, `Decisions` (repository) | `sqlstore` (migration 00010): every LLM call (agent, scanner, research, test) and the decision log (one row per candidate the agent handled); both kept 400 days (OD12); `LLMCalls.Usage` sums them per profile and purpose |
| `Trades` (repository) | `sqlstore` (`trades`, migration 00011): eToro's closed trades per account (read-only sync, PK account + position id), flagged `ours` when cactusTrade opened the position; kept as long as the account (OD16) |
| `Mailer` | `adapters/mail` (net/smtp: STARTTLS required / implicit TLS / plain to localhost; header-injection checks; MIME multipart/mixed with attachments, R30); `Outbox` (with `outbox_attachments`, migration 00013) and `PasswordResets` repositories |
| `StatementRenderer` | `adapters/pdf` (R30): a built `domain.Statement` as an A4 PDF with `codeberg.org/go-pdf/fpdf`; standard fonts (Windows-1252), the logo as vector shapes, no I/O (depguard) |
| `AnalysisRenderer` | `adapters/pdf` (R31): a built `domain.Analysis` as a one-page PDF, 210 mm wide, A4 tall when it fits, else as tall as the content (rounded up to 10 mm); vector charts in the Okabe–Ito palette |
| `StatementSchedules` (repository) | `sqlstore` (`statement_schedules`, migration 00013): per user; `MarkPeriod` is a compare-and-set on the last period sent |

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
  `x-request-id`. It polls `orders:lookup` (1, 2, 4, 8, 15, 30 s, then cancel at 60 s,
  abandoned at 2 min when eToro never saw it) by eToro's order id from the open's answer, and
  by our request id only when it has none (a lost or 429 answer): eToro's demo answered 404 by
  request id for filled orders (live findings 2026-11-24, `Broker.Lookup(ctx, requestID,
  orderID)`). An open is never resent (review M3):
  after a 429 it waits out Retry-After and looks the order up; a lost answer is resolved by
  lookup (OD2). Closes (≤ 5) and cancels (≤ 3) resend with the same id.
  Every fill is checked against the two order shapes, leverage 1 and the stop we sent (± 1 %);
  a fill that does not conform is closed at once and its instrument blocked. A fill whose
  position eToro already closed (execution `state: closed`, its take-profit or stop hit) is
  recorded as ours and settled as closed at once.
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
  cancels open orders pending for over a minute — looked up first, and at most one cancel per
  order while one is in flight or was accepted (refused ones: at most 3, a minute apart); a
  cancel still unconfirmed raises one warning, not one per pass. It needs no LLM, no prices
  and no reconciliation.
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
- **Force close (R24).** An admin (`agent.force_close`), in a web session, with trade access
  and the password typed again (the bounded argon2 hasher, D29; counted by the sign-in
  limiter, D28) closes what cactusTrade opened on a demo account at once: control lock →
  agent stopped (a killed agent stays killed) → running tick cancelled → entry lock → the
  same `flatten` as Kill (pending opens cancelled, our positions closed, OD1), detached from
  the request's context. No kill latch, no reset afterwards. A real account is refused before
  any read (S7). `POST /api/v1/agent/force-close-all` does every demo account the admin can
  trade, checking the password once. Audited, danger event (alert e-mail).
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

### Live view and history (phase 6)

Both are **views**: read-only for the broker (no new eToro path; the allowlist test is
unchanged), and nothing an order path decides reads them.

```
 supervisor's portfolio read ──put──► view cache (per account, value + read time)
 view use cases ──► viewPortfolio (≤ 1 eToro read per account per 10 s, shared flight)
 executor / reconcile / agent / supervisor ──publish──► event bus (never blocks)
 GET /api/v1/live ──► LiveSnapshot (cache + store only) every 5 s, and on agent.state /
                      account.portfolio (≤ 1 per second); event / order / tick / decision
                      messages filtered by LiveFilter; resync on overflow; bye on sign-out,
                      lost access, 30-min lifetime, shutdown
 agent tick ──publish──► agent.tick (summary) and decision (after the row is written):
                      bus only, never stored as events (R26: the dashboard's activity log)
 historyLoop (15 min) ──► SyncTradesAll ──► Broker.TradeHistory ──► trades (upsert in Go)
 GET /api/v1/history/{summary,daily,trades}, /history/export/{file}.csv ──► Trades, snapshots
```

- **The view cache never feeds a decision (guardrail §2.2).** `runtimeState.views` holds the
  last portfolio per account. The supervisor stores its own read there; the agent tick, fresh
  prices, the executor and the budget check keep reading eToro fresh.
  `TestEntriesNeverUseTheViewCache` and a go/ast source test (`TestViewCacheReadersAreViewsOnly`)
  enforce it.
- **Live figures** are `domain.LimitDistances` (integer cents and basis points): equity, cash,
  reserved, budget used / left, day P&L, the distances to the daily and drawdown floors, slots
  against the position cap, short exposure against its cap, and per market the time to
  no-entry and flatten. A distance at or below 0 means `AssessEntry` refuses an entry
  (`TestLimitDistancesAgreeWithAssessEntry`, 20 000 inputs).
- **The stream** (`handlers_live.go`) is one account per connection (OD14), cookie or API key,
  capped at 4 per user and 200 in total. Write deadlines are set before and cleared after each
  write, so the server's read and write timeouts do not end it (HTTP/1.1 and HTTP/2 tests).
  `Server.CloseStreams` runs on shutdown; a kill request in flight still completes.
- **The web dashboard** (`web/src/lib/live.ts`, `useLive`) uses `EventSource`: backoff 1 s to
  30 s, after 3 failures a 15-second refresh while it keeps retrying, closed after 60 s in a
  hidden tab, a stale badge after 30 s without a snapshot (or when the server marks the
  portfolio old). The agent panel's buttons are its own REST calls: **the kill switch works
  while the stream is down** (Vitest). React only formats; the bar widths are the one
  computation.
- **History** comes from `trades` and `equity_snapshots`: `domain.TradeStats` (count, wins,
  losses, breakeven, win rate in bp, absent without trades; gross, fees, net; unknown fees
  counted apart, never as 0), `DailyPnL` per risk day (22:00 UTC; the equity change includes
  deposits, withdrawals and other positions, and is labelled so) and `Drawdown`. The summary
  counts cactusTrade's own trades (OD15); positions opened elsewhere are listed and flagged.
- **CSV exports** (`encoding/csv`, at most 50 000 rows, 422 beyond: never cut short) write
  exact decimal amounts and UTC RFC 3339 times, and prefix any text cell a spreadsheet could
  read as a formula with `'` (OD17).
- **Wording.** The History page carries the fixed note "Figures describe past results only;
  they do not predict future results." `TestNoProfitabilityClaims` (`cmd/cactustrade`) scans
  `web/src`, `README.md`, `docs/` and `api/openapi` for banned phrases; each allowed line is
  listed in the test with its reason.

### Agent style: presets, aggressiveness, autonomy (R28)

- **Risk presets** (`domain.RiskPreset`, `PresetFor`, `ApplyPreset`, `MatchPreset`) are full
  sets of limits except the budget; `app.ApplyRiskPreset` goes through `SetRiskLimits`, so the
  owner/session/stopped/kill rules, the real ceilings and the RaisesAny disarm all apply.
- **Aggressiveness** (`domain.Aggressiveness`, stored per account as a setting) maps to strategy
  parameters only (`strategy.ConfigFor`: risk per trade, entry probability, largest suggested
  size); `app.strategyConfig` builds the account's config for the agent, the scanner and the
  live "what the agent may do" (`strategy.MayDo`). `domain.AssessEntry` still sizes every entry.
- **Autonomy** (`domain.Autonomy.Policy(mode)`) decides what runs by itself: research mode
  (`app.ResearchMode(ctx, accountID)` → `off | propose | auto_add`, read by the scanner and the
  web research), the scan interval (`ScanAll` every `app.ScanTick`, each account when its
  interval passed), owner-only trading at low, theme auto-assign and stale-share pruning at
  high, and at full the auto-start at the first market open of a risk day (demo only, inside
  `AgentLoop`, through `startable` under the control lock) plus the aggressiveness adjustment
  folded in when `observe` moves to a new risk day. No level touches a limit, a kill or the
  real-mode gate.

### Trading statements (R30)

A **view** like the history: built from stored records (trades, equity snapshots, agent
positions, orders, decisions, events, LLM calls) plus, for view-only platforms, their
holdings read when it is made. No eToro path; nothing an order path decides reads it.

```
 schedule (period, accounts, recipients, includes)      statementsLoop (cmd, every 5 min)
        │                                                       │
        ▼                                                       ▼
 app.SendDueStatements: LastCompleteWindow(period, now) > LastPeriod ?
   → statementInputs (per account: trades, snapshots, positions open at the end, activity)
   → domain.BuildStatement (combined | per account | per exchange; summary, no LLM)
   → ports.StatementRenderer (adapters/pdf) → one outbox mail per recipient, PDFs attached,
     id = sha256(schedule, period, recipient) → MarkPeriod (compare-and-set)
```

- **Periods** are whole risk days (22:00 UTC): daily (sent 22:30 UTC, Monday to Friday risk
  days), weekly (Monday to Friday, sent Saturday 06:00 UTC), monthly (calendar month, sent the
  1st 06:00 UTC). UTC throughout, so daylight saving time never moves a boundary.
- **Once per period.** The outbox refuses a second mail with the same id, and the period is
  recorded with a compare-and-set after the mails are queued: a restart, a lost record or a
  second process sends nothing twice. After a long stop only the newest period is sent. A
  new or re-enabled schedule starts with the next period.
- **Who.** A web session only (recipients are typed addresses). Accounts on a schedule: the
  owner's own (an admin: any account the admin can see), re-checked at every send. A
  schedule is managed by its owner or an admin; anyone else gets 404.
- **Statement figures** come from `domain.BuildStatement` only: trades of positions
  cactusTrade opened (others counted apart), equity per currency (never summed across
  currencies), unknown fees apart, a deterministic summary paragraph; the same records give
  the same statement.

### Analysis page (R31)

Also a **view** of stored records; it reads no broker, not even a view-only platform's
holdings.

```
 GET /api/v1/analysis[.pdf]?preset|from&to&accountId…   (viewer; view access per account, else 404)
   → app.Analysis: AnalysisWindowFor(preset, from, to, now) + chosen accounts
   → per account: trades, snapshots, themes, decisions (3 days before the period too, for
     the playbook join), events, LLM usage
   → domain.BuildAnalysis (groupings, equity curves per currency, risk, activity, narrative)
   → JSON, or ports.AnalysisRenderer (adapters/pdf) → one page, sized to fit
```

- **Groupings** by date (bucket: day ≤ 31 days, week ≤ 186, month beyond), exchange,
  trading account, theme (the share's theme now), direction, playbook (decision
  `order_request_id` = trade `open_request_id`) and outcome; per group trades, wins/losses,
  win rate (absent without trades), realised P&L, fees (unknown apart), average, best, worst.
- **Narrative** is deterministic (no LLM): six headed sections written from the analysis's
  own figures, neutral and never predictive; every amount in it appears in a table.

### Backups from the app (R48)

An in-process scheduler next to `statementsLoop`; it is no input to trading (a failed backup
raises a warning event and stops nothing).

```
 Setup → Backups (admin)                              backupLoop (cmd, every minute)
   schedule / recipients (web session, audited)              │
        │                                                     ▼
        ▼                                 app.RunDueBackup: domain.BackupSchedule.Due(last, now, zone)?
 settings backup.schedule / backup.recipients     → runBackup (one at a time, else 409)
                                                  → ports.Backups.Write (adapters/backup.Folder)
                                                  → backup.last, audit; prune (keep N) after success
```

- **Port `ports.Backups`** (Folder, Driver, ParseRecipients, Write, List, Remove, Open);
  `Deps.Backups` nil = unavailable. Adapter `internal/adapters/backup.Folder` on
  `*sqlstore.Store` (through a small `Source` interface: `BackupTo`, `DumpMariaDB`).
- **Formats.** SQLite: `VACUUM INTO` a random file in the 0700 data folder → age →
  `cactustrade-<UTC>.db.age` (restore: `cactustrade restore`). MariaDB:
  `Store.DumpMariaDB` — one connection, `START TRANSACTION WITH CONSISTENT SNAPSHOT, READ
  ONLY`, per table `DROP TABLE IF EXISTS` + `SHOW CREATE TABLE` + extended INSERTs (≤ 1 MiB;
  a value over 256 KiB is built in a user variable chunk by chunk, so no statement nears
  `max_allowed_packet`) — streamed through gzip and age straight into the folder:
  `cactustrade-<UTC>.sql.gz.age` (restore: `deploy.sh --restore`, the same script as a
  mariadb-dump file). The dump reads only the tables it lists (`dumpSelects`, pinned to the
  migrations by `TestDumpKnowsEveryTable`) and refuses any other table, view, trigger,
  routine or event (fail closed).
- **Files.** Written as `.partial-<rand>` and hard-linked to the final name, which is never
  overwritten (a clash adds `-xxxx`); nothing plain stays on disk. 0600, or 0640 with
  `CACTUSTRADE_BACKUP_GROUP_READ=1` (containers). `List`/`Open`/`Remove` go through `os.Root`
  and accept only bare names `domain.ParseBackupName` parses, regular files only (no
  symlinks, no folders). Pruning (`domain.PruneSelect`) touches only such names.
- **Folder** fixed at deploy time: `CACTUSTRADE_BACKUP_DIR` (standalone `<data
  dir>/backups`; Docker/Podman `${BACKUP_DIR}/app` at `/backups`, setgid to the host user's
  group; k3s/k8s the `backups` PVC). The app shows it and never changes it.
- **Keys.** age X25519/hybrid recipients only (OD29: none = no schedule, no "Back up now").
  The app's list wins over `CACTUSTRADE_BACKUP_RECIPIENT`; the audit log names keys by
  fingerprint. A key pair made on the page (`web/src/lib/agekey.ts`, WebCrypto X25519 +
  Bech32) keeps its private half in the browser.
- **Schedule** (pure, `domain.BackupSchedule`): off, hourly, every N hours on the hour from
  local midnight, daily at HH:MM, in an IANA zone; a time skipped by daylight saving runs at
  the jump, a repeated one once; missed slots run once (`Due(last, now)`).
- **Who.** Admin (`settings.write`); schedule, recipients and downloads from a web session
  only (OD40); status and "Back up now" also with an admin API key. Every change, run,
  failure, prune and download is audited.

### Data reset (R49)

System → "Reset all trading data" (admin, web session, password, typed `DELETE ALL TRADING
DATA`; `app.ResetAllData`, `GET|POST /api/v1/system/data-reset`). It wipes the trading data of
**every** account (OD39) and nothing else; it never calls the broker except to reconcile.

```
 phrase → password (sign-in limiter) → rt.resetting (no start, auto-start or arm; kills,
 disarms and exits go on) → ReconcileAll → domain.DataResetChecks → audit data.reset_started
 → runBackup(trigger reset; a failure stops here, nothing wiped) → every account's ctl lock
 (TryLock in id order, ≤ 5 s, else 409) → checks again → ports.DataReset.Reset (one
 transaction) → gate disarms latched, every account unreconciled → unlock → audit data.reset,
 warning event, e-mail to every admin and account owner → ReconcileAll
```

- **Checks** (pure, `domain.DataResetChecks`): every agent stopped or killed; no lease held by
  another process; no real-money gate stored armed (one no longer in force is locked first);
  every eToro account with keys reconciled OK by this run; no open agent position, no order
  that is not final, no order created in the last 15 min — on **every** platform (Trading
  212's orders live in the same tables); a backup can be taken (folder and recipient).
- **Wiped** (`sqlstore.resetWiped` = `domain.DataResetWipedTables`): `orders`,
  `agent_positions`, `trades`, `decisions`, `llm_calls`, `equity_snapshots`, `events`,
  `reviews`; with the box ticked also `watchlist`. Settings derived from them:
  `agent.aggressiveness_adapt.*`, `research.state.*`, `events.seen.*` (and
  `watchlist.signals.*` with the watchlists).
- **Kept** (`sqlstore.resetKept`): users, sessions, API keys, secrets, the audit log,
  accounts and shares, every other setting, risk limits, **`risk_state`** (kill switch,
  high-water mark: a reset never clears a kill), instrument blocks, instruments, Trading 212's
  catalogue and instrument matches, themes, statement schedules, the outbox, password-reset
  links, leases, `real_gates` rows (an armed one is set locked with `DisarmDataReset`) and
  `goose_db_version`. `TestResetClassifiesEveryTable` (SQLite) and its MariaDB twin fail for
  a table in neither list: **a migration that adds a table classifies it in `reset.go`** (and
  adds it to `dumpSelects`).
- **Afterwards.** `data_reset.at` counts like `restore.at` (`restoredAt` takes the later): a
  gate armed before it is never in force. `history.since.<account>` is the floor of
  `SyncTrades` (OD38): eToro's older trades are not read back in. The real-gate evidence is
  read from the wiped decisions, orders and agent positions, so every real account needs N
  new qualifying demo days (default 5) before it can be armed; the dialog warns per account.
  Rows written between the backup and the wipe (a few seconds of events or scans) are in
  neither.

### View preferences (R50)

A user's view choices in the web app (chart ranges, tabs, filters, sort, grouping, folded
cards, periods) are one JSON object per user in `settings` (`ui.prefs.<userId>`), read and
changed only by that user from a web session (`GET`/`PUT /api/v1/me/preferences`, a merge
patch). The rules (key pattern, value shapes, 200 keys, 60 KiB) are `domain.MergePreferences`;
the use cases are `app.MyPreferences` / `SetMyPreferences`. In the web app `PrefsProvider`
loads them before the pages render and `usePref(key, default)` replaces `useState` for a
view choice; saves are debounced (500 ms). They are never read by the agent, the risk
engine or authorization, and are not audited. A data reset (R49) keeps them.

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
  Keys cannot create keys, change credentials, or create, change or delete users (PLAN 7.1.1).
- **Rate limits (PLAN 7.1.4, 7.1.5, D28, D29):** every password check (sign-in, the current
  password in change-password and in an e-mail change) is counted *before* hashing, and taken
  back when it was right: 20 per client IP and 5 per (username, IP) in 15 min, then 429 with
  `Retry-After`. Across addresses a username is never locked out, only slowed down (1 s, 2 s,
  4 s, then 8 s per attempt after 5 recent ones). Password-reset requests (20 per IP / 15 min,
  5 per username / hour) and bad reset tokens (10 per IP / 15 min) have their own buckets. At
  most 3 argon2 hashes run at once (2 s wait, then 429 `ErrBusy`); the containers set
  `GOMEMLIMIT=400MiB` under a 512 MiB limit. The limiter holds at most 10 000 keys and is
  swept by `PurgeLoop`.
- **Accounts under the supervisor (PLAN 7.1.2, 7.1.3, D30):** an account is deleted, or its
  eToro keys changed, only while its agent is stopped and no agent position or non-final
  order of ours is open; new keys leave the account unreconciled until the next clean pass.
- **Outbound HTTP (PLAN 7.1.7, 7.1.9):** every client (eToro, Trading 212, Saxo, Tiger, LLM)
  refuses redirects. An LLM key travels only over https (except Ollama on this machine and
  loopback); LLM base URLs and SMTP hosts never name a link-local or cloud-metadata address.
- **First run:** the server creates `admin` / `admin`, which must choose a new password
  before it can do anything else (403 "Password change required").
- **One port:** with TLS on, plain HTTP on the same port gets a 308 redirect to HTTPS.
- **Credentials:** write-only. Encrypted with AES-256-GCM under the master key (env, file or
  generated 0600). The ciphertext is bound to its name. Never logged, audited or returned.
  eToro keys are set per account by its owner (web session); `/api/v1/credentials` is a
  read-only status of the deployment's credentials (D27).
- **Headers:** strict CSP (`default-src 'self'`), `X-Frame-Options: DENY`, `nosniff`,
  `no-referrer`, HSTS over TLS, and `Cache-Control: no-store` on the API.
- **Errors:** problem+json. Internal errors are logged, and the client gets a generic 500.
  A refusal because setup is missing (R22) wraps its sentinel in a `domain.MissingError`
  (same status), and the problem adds `missing` (a `domain.Missing` code) and `canFix`
  (decided by the use case, which knows the caller). The web app's `ProblemAlert` shows the
  `FixButton` for the code; React decides nothing.
- **Containers:** distroless, non-root (65532), read-only root filesystem, all capabilities
  dropped, `no-new-privileges`, and secrets as files.

## Real-mode gate (phase 7, ADR-007)

Real trading is **lifted behind two keys and a permit** since the owner's written consent (D39,
PLAN 7.19–7.23): `domain.RealTradingEnabled` is true, and a real-money **entry** is made only
while the account's gate is armed and in force, the deployment opt-in
`CACTUSTRADE_REAL_TRADING=on` (OD28, `app.Config.RealTrading`, default off) is set, the account
is reconciled and its limits are within `RealCeilings` (`realEntryBlocked`), and it carries a
fresh `domain.RealPermit` built by `PermitEntry(gate, account, now, restoredAt)` (valid 60 s,
never past the gate's expiry): `AssessEntry` (S1) and `IntentFor` (S2) need it, the executor's
`mayOpen` takes a new one right before the send, and the eToro client's `mutate` (S3; SimBroker
S4) sends a real `open` only with a valid entry permit in the context (`ports.WithPermit`), to
`Allowlist[op].Real` with the account's own keys and the same two body shapes. **Exits** of
what cactusTrade opened — the supervisor's closes (S6: a real account is supervised while armed
or holding our positions/orders), the kill flatten and force close (S7), cancels — carry
`PermitExit(account, ours)` and work whatever the gate and the opt-in (CLAUDE.md §2.6). The
controls (S10) are `gated` while `realEntryBlocked` says why; start and the OD5 resume (S8)
refuse the same way, and a real agent never resumes by itself after a restart (OD26).
`set_sl_tp` stays not executable on every account. The gate's machinery:

- **Domain** (`internal/domain/realgate.go`, `risk.go`): `DayFacts` → `QualifyingDays` (OD19
  strict, OD20: 20 completed risk days in the last 60, the latest ≤ 7 days old); `GateChecks`
  (real account, keys, reconciled, not killed, agent stopped, limits within `RealCeilings`;
  linked demo account of the same owner; evidence; admin owner in a web session; the phrase);
  `CanArm` (`*GateRefusal` naming failing checks, else `ErrRealTradingDisabled`); the
  `RealGate` aggregate (locked / armed, 30-day expiry, `Live(now, restoredAt)`);
  `DefaultRealRiskLimits`, `RealCeilings`, `ValidateFor(mode)`, `RaisesAny`.
- **Storage:** `real_gates` (migration 00012, both dialects), versioned like the risk state, so
  a disarm is never overwritten by an older arm; `Orders.ListSince` and
  `AgentPositions.ListSince` read the evidence window; the decisions are read one per risk day.
- **App** (`internal/app/realgate.go`): `RealGateStatus` (view), `ArmReal` (every check, then
  `CanArm`; stores the gate armed),
  `DisarmReal` (trade access + `agent.kill`, session or API key, idempotent; the agent stops,
  nothing is cancelled or closed), the automatic disarms (kill trip in `tripLocked`, key change
  in `SetAccountCredentials` before the first key is written, a raised limit in
  `SetRiskLimits`, a restore and the expiry on every read and in the supervisor loop's sweep,
  which runs beside the passes with a timeout), an in-memory disarm latch plus a stored kill
  when the disarm cannot be written (fail closed in every process), and `armLocked`, which only
  `ArmReal` calls after `CanArm` (tests also call it to build an armed account).
  `realEntryBlocked` / `realEntryPermit` are the entry side (above); `AnnounceRealTrading`
  records a danger system event at start-up while the opt-in is on. A kill stores the disarm before its flatten and announces it (event,
  e-mails) after. `Status.RealArmed` feeds the banner, `Status.RealArmedUnknown` says a gate
  could not be read (never shown as "not armed"); `TradingHealth` reports `RealGateArmed` per
  account and an unreadable gate as a problem.
- **Restore:** every restore locks every gate (`PrepareRestored`, `mariadb-restore.sh`; reason
  "a backup was restored"), whatever the clocks say; `restore.at` stays as a second check.
- **HTTP:** `GET /api/v1/accounts/{id}/real-gate`, `POST …/arm` (web session, password under
  the sign-in limiter, CSRF), `POST …/disarm` (session or key, CSRF for cookies, never rate
  limited). A failing check is 409; a build without the consent 403 "Real trading is not
  enabled in this build"; any other real-mode refusal 403 (the agent endpoints: "Real trading
  needs the phase-7 gate", with the reason).
- **Web:** *Setup → Real trading* shows the server's checklist, the evidence days, the demo
  account picker, the arm dialog (phrase + password) and Disarm; a red, non-dismissible
  banner on every signed-in page while any visible account is armed (OD27), or saying the
  state is unknown when a gate or the status cannot be read; the change-password page shows it
  too (its Status answer carries only the banner's facts), and the server renders the same
  banner into the Swagger UI page for a signed-in user.

## Observability

JSON logs (`log/slog`): one line per request with method, path, status, duration, client IP
and user. Every line carries the trading mode, and no line contains a body, cookie or token.
`/healthz` is liveness. `/readyz` checks that the database answers. `/metrics` serves
Prometheus HTTP, Go and process metrics and the trading metrics, always behind a bearer
token (`CACTUSTRADE_METRICS_TOKEN`, else generated into `<data>/metrics.token`; PLAN 7.1.14).

Trading metrics (PLAN 7.5). `internal/app` never imports Prometheus (depguard):
`app.TradingHealth(ctx)` returns a plain snapshot (per eToro account: agent state, killed,
reconciled, last supervisor pass and last clean pass, lease held, non-final orders by state,
real gate armed; the supervisor loop's last tick; the agent loop; LLM calls by purpose and
outcome), and a `prometheus.Collector` in the HTTP adapter reads it at scrape time. The
other counters live in `httpapi.Observers`, which `cmd` feeds: eToro requests by operation,
status class and mode (`etoro.Config.Observe`), client limiter waits per quota group
(`etoro.Config.ObserveWait`) and order transitions counted from the live bus
(`Observers.CountOrders`; a view only — an order never waits for it). Labels are account
ids or values from fixed sets, never a name, key, path or amount.
`GET /api/v1/system/health` (admin) is the same snapshot: 503, naming the account, when the
supervisor loop has not ticked or an account it covers has had no pass for 60 s
(`app.SupervisorStale`), or the agent loop is down. `/healthz` stays a plain liveness check,
so an orchestrator never restarts the process for a stalled account. Alert rules:
`docs/RUNBOOK.md`.
