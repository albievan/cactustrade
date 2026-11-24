# cactusTrade — runbook

What to watch and what to do: the monitoring (PLAN 7.5), then the incident procedures (PLAN
7.9): eToro API down, kill switch tripped, reconciliation mismatch, lease held by a dead
process, restore, Vault unavailable, **disarm real trading now** (planned with the gate), and
the operating notes (trusted proxies on k3s/k8s, releases).

Two rules hold in every procedure: **never hold a position overnight** (if cactusTrade cannot
close one before its market's session ends, close it yourself in eToro's web app), and
**never make cactusTrade send while you are unsure what it already sent** (pause or kill
first, reconcile, then resume).

## Monitoring

### Endpoints

| Endpoint | Who | Answers |
|---|---|---|
| `GET /healthz` | anyone | 200 while the process serves HTTP (container liveness; never depends on eToro or an account) |
| `GET /readyz` | anyone | 200 while the database answers, else 503 |
| `GET /metrics` | bearer token | Prometheus metrics; 401 without the token. The token is `CACTUSTRADE_METRICS_TOKEN` (`deploy.sh` puts it in `SECRETS_DIR/metrics_token`, k8s secret `cactustrade-secrets`); when unset, the server generates `<data>/metrics.token` on first start (PLAN 7.1.14) |
| `GET /api/v1/system/health` | admin (session or admin API key) | 200 when every trading loop is alive; **503** naming the account when the supervisor loop has not ticked, or an account it covers has had no supervisor pass for 60 s, or the agent loop is down |

Prometheus scrape configuration (the token from a file, never inline):

```yaml
scrape_configs:
  - job_name: cactustrade
    scheme: https            # http when TLS is terminated in front of the app
    authorization:
      type: Bearer
      credentials_file: /etc/prometheus/cactustrade_metrics_token
    tls_config:
      ca_file: /etc/prometheus/cactustrade_ca.pem   # the app's certificate when self-signed
    static_configs:
      - targets: ["trader.example:8443"]
```

### Trading metrics

Labels are account ids (never names) or values from fixed sets. Times are Unix seconds;
0 means "none in this process" (after a restart every account starts at 0).

| Metric | Labels | Meaning |
|---|---|---|
| `cactustrade_trading_healthy` | — | 1 when `/api/v1/system/health` would answer 200 |
| `cactustrade_account_healthy` | account | 1 when the account's supervisor pass is recent and its state could be read |
| `cactustrade_account_info` | account, mode | always 1; `mode` is `demo` or `real` |
| `cactustrade_account_supervised` | account | 1 when the supervisor covers the account (eToro demo account with both keys) |
| `cactustrade_agent_state` | account, state | 1 for the current state (`stopped`, `running`, `paused`, `killed`) |
| `cactustrade_agent_killed` | account | 1 when the kill switch is on (a read error counts as on) |
| `cactustrade_account_reconciled` | account | 1 when the latest reconciliation in this process was OK (entries need it) |
| `cactustrade_supervisor_last_pass_timestamp_seconds` | account | end of the latest supervisor pass that ran (also with broker errors) |
| `cactustrade_supervisor_last_clean_pass_timestamp_seconds` | account | end of the latest pass without any error |
| `cactustrade_supervisor_last_tick_timestamp_seconds` | — | the supervisor loop's latest tick |
| `cactustrade_lease_held` | account | 1 when this process holds the single-writer lease (D22) |
| `cactustrade_orders_non_final` | account, state | our orders in `created`, `sent`, `pending`, `partially_filled`, `unknown` |
| `cactustrade_real_gate_armed` | account | 1 when real trading is armed (0 until the owner's consent, PLAN 7.18) |
| `cactustrade_agent_loop_running` | — | 0 when the start-up resume failed and the agent loop does not run |
| `cactustrade_order_transitions_total` | account, kind, state | stored order transitions (creation included), counted from the live bus |
| `cactustrade_etoro_requests_total` | op, class, mode | requests that left for eToro; class `2xx`, `3xx`, `4xx`, `429`, `5xx` or `error` (no answer) |
| `cactustrade_etoro_limiter_waits_total` / `_wait_seconds_total` | quota | times / seconds the client-side limiter held a request back |
| `cactustrade_llm_calls_total` | purpose, outcome | LLM reviewer calls (`agent`, `scanner`, `test`; `ok` or `error`) |
| `cactustrade_live_bus_dropped_total` | — | live events a full subscriber missed (dashboards resync; the order counter may undercount) |
| `cactustrade_http_requests_total`, `_request_duration_seconds`, `_rate_limited_total`, `cactustrade_live_streams` | route, … | the HTTP API |

### Alert rules

Load into Prometheus (`rule_files:`). Severities: `page` = act now (exits or the single
writer may be at risk), `ticket` = look today, `info` = for the record.

```yaml
groups:
  - name: cactustrade
    rules:
      - alert: CactusTradeDown
        expr: up{job="cactustrade"} == 0
        for: 2m
        labels: { severity: page }
        annotations:
          summary: cactusTrade does not answer /metrics (process down or token wrong)

      - alert: CactusTradeSupervisorStalled
        # An account the supervisor covers has had no pass for over 60 s: exits (flatten
        # before the close, kill-switch closes, stale-order cancels) are not running for it.
        expr: |
          cactustrade_account_supervised == 1
          and on (account)
          (time() - cactustrade_supervisor_last_pass_timestamp_seconds) > 60
        for: 1m
        labels: { severity: page }
        annotations:
          summary: "No supervisor pass for account {{ $labels.account }}"
          description: GET /api/v1/system/health names the reason (lease held elsewhere, pass failing).

      - alert: CactusTradeSupervisorLoopStalled
        expr: time() - cactustrade_supervisor_last_tick_timestamp_seconds > 60
        for: 1m
        labels: { severity: page }
        annotations:
          summary: The supervisor loop has not ticked for over 60 s

      - alert: CactusTradeTradingUnhealthy
        expr: cactustrade_trading_healthy == 0
        for: 2m
        labels: { severity: page }
        annotations:
          summary: GET /api/v1/system/health answers 503 (see its problems)

      - alert: CactusTradeLeaseNotHeld
        # The supervisor covers the account but this process does not hold its lease: another
        # process (a second binary on the same database, an overlapping pod) is the writer,
        # or a dead one's lease has not expired yet (30 s).
        expr: |
          cactustrade_account_supervised == 1
          and on (account) cactustrade_lease_held == 0
        for: 2m
        labels: { severity: page }
        annotations:
          summary: "Account {{ $labels.account }}: this process does not hold the single-writer lease"

      - alert: CactusTradeAgentLoopDown
        expr: cactustrade_agent_loop_running == 0
        for: 1m
        labels: { severity: ticket }
        annotations:
          summary: The start-up resume failed; no agent can run until cactusTrade restarts

      - alert: CactusTradeKillSwitchOn
        expr: cactustrade_agent_killed == 1
        labels: { severity: ticket }
        annotations:
          summary: "Kill switch on for account {{ $labels.account }}; an admin must reset it"

      - alert: CactusTradeUnreconciled
        expr: |
          cactustrade_account_supervised == 1
          and on (account) cactustrade_account_reconciled == 0
        for: 10m
        labels: { severity: ticket }
        annotations:
          summary: "Account {{ $labels.account }} is not reconciled: no new entries"

      - alert: CactusTradeOrderOutcomeUnknown
        expr: cactustrade_orders_non_final{state="unknown"} > 0
        for: 5m
        labels: { severity: ticket }
        annotations:
          summary: "Account {{ $labels.account }} has orders whose outcome is unknown: entries are blocked until they resolve"

      - alert: CactusTradeOrdersNotSent
        expr: sum by (account) (cactustrade_orders_non_final{state=~"created|sent"}) > 0
        for: 10m
        labels: { severity: ticket }
        annotations:
          summary: "Account {{ $labels.account }} has orders stuck in created/sent for 10 minutes"

      - alert: CactusTradeEtoroErrors
        expr: |
          sum(rate(cactustrade_etoro_requests_total{class=~"5xx|error"}[5m]))
          / sum(rate(cactustrade_etoro_requests_total[5m])) > 0.2
        for: 5m
        labels: { severity: ticket }
        annotations:
          summary: Over 20 % of eToro requests fail or get no answer (new entries stop: fail closed)

      - alert: CactusTradeEtoroRefusals
        expr: sum by (op) (increase(cactustrade_etoro_requests_total{class="4xx"}[15m])) > 10
        labels: { severity: ticket }
        annotations:
          summary: "eToro refuses {{ $labels.op }} requests (keys expired or revoked?)"

      - alert: CactusTradeEtoroRateLimited
        expr: sum(increase(cactustrade_etoro_requests_total{class="429"}[10m])) > 5
        labels: { severity: ticket }
        annotations:
          summary: eToro answers 429 although the client keeps under the documented quotas

      - alert: CactusTradeLimiterBusy
        expr: sum by (quota) (rate(cactustrade_etoro_limiter_wait_seconds_total[5m])) > 0.5
        for: 10m
        labels: { severity: info }
        annotations:
          summary: "Requests in quota group {{ $labels.quota }} wait on the client limiter half the time"

      - alert: CactusTradeLLMFailing
        expr: sum by (purpose) (increase(cactustrade_llm_calls_total{outcome="error"}[30m])) > 5
        labels: { severity: info }
        annotations:
          summary: "LLM reviewer calls for {{ $labels.purpose }} fail (each failure is a rejection: fail closed)"

      - alert: CactusTradeLiveBusDrops
        expr: increase(cactustrade_live_bus_dropped_total[10m]) > 100
        labels: { severity: info }
        annotations:
          summary: Live events are dropped (slow dashboards; the order-transition counter may undercount)

      - alert: CactusTradeRealArmed
        expr: cactustrade_real_gate_armed == 1
        labels: { severity: page }
        annotations:
          summary: "Real-money trading is armed for account {{ $labels.account }}"
```

### First checks for any trading alert

1. `GET /api/v1/system/health` as an admin: `problems` names the account and the reason
   (for example "another process holds the lease", "the pass failed: …").
2. The JSON logs: every line about an account carries `account_id` and `mode` (PLAN 7.2);
   filter by the account id from the alert.
3. Exits never depend on the agent (D21): a stalled supervisor is the one alert where
   positions may stay open past the session close. If it does not recover within a few
   minutes, close the positions in the eToro web app and restart cactusTrade; on start it
   reconciles before anything else.

## Incident procedures

In the commands below, `$CT` is the server address (for example `https://trader.example:8443`)
and `$CT_KEY` an API key of an operator with trade access on the account (*API keys* page;
`Authorization: Bearer ctk_…`). `?accountId=…` picks the account (default: the caller's current
one). An API key may pause, stop and kill, never start or reset (OD11). Pause, stop and kill are
never rate-limited (D32).

```sh
# the agent's state, reconciliation, kill reason, lease and controls
curl -fsS -H "Authorization: Bearer $CT_KEY" "$CT/api/v1/agent?accountId=$ACCT"
# stop new entries (exits continue)
curl -fsS -X POST -H "Authorization: Bearer $CT_KEY" "$CT/api/v1/agent/pause?accountId=$ACCT"
# kill: cancel our pending orders and close every position cactusTrade opened
curl -fsS -X POST -H "Authorization: Bearer $CT_KEY" -H 'Content-Type: application/json' \
  -d '{"reason":"runbook"}' "$CT/api/v1/agent/kill?accountId=$ACCT"
# reconcile with eToro now (read-only on eToro)
curl -fsS -X POST -H "Authorization: Bearer $CT_KEY" "$CT/api/v1/agent/reconcile?accountId=$ACCT"
```

### eToro API down or refusing

Alerts: `CactusTradeEtoroErrors`, `CactusTradeEtoroRefusals`, `CactusTradeEtoroRateLimited`,
`CactusTradeSupervisorStalled` (with "the pass failed" in `/api/v1/system/health`).

What cactusTrade does on its own: it **fails closed**. A broker error or a stale price means no
new entry; the supervisor keeps trying the exits every 10 s (flatten 10 minutes before each
market's close, kill-switch closes, stale-order cancels); nothing is sent twice (every order has
its stored request id, and an open is never resent).

1. Is it eToro or us? `cactustrade_etoro_requests_total` by `class`: `5xx` / `error` = eToro or
   the network; `4xx` = our request or our keys; `429` = quota. Check eToro's API portal and
   status page, and the JSON logs filtered by the `account_id` from the alert.
2. **4xx on every call (keys expired or revoked):** create new keys in eToro and set them under
   *Platform accounts* (owner). A key change needs the account quiescent (agent stopped, no
   open agent position, no non-final order, D30): if positions are open, they must be closed
   first (kill, or in eToro's web app). The account is then unreconciled until the next pass.
3. **429:** the client keeps under eToro's documented quotas; persistent 429s mean the quota
   changed. Pause the agent, note the quota group (`cactustrade_etoro_limiter_waits_total`),
   and report it (PLAN 4.17 / `docs/etoro-api.md`).
4. **Outage near the session close with positions open:** pause the agent (entries stop). If the
   flatten time (close − 10 min) passes and `GET /api/v1/agent` still lists open positions of
   ours, **close them in eToro's web app** — no position may be held overnight.
5. When eToro answers again: reconcile now (above); the account must show `reconciliation.ok: true`
   before the agent can enter again. Resume (start) from the web UI.

### Kill switch tripped

Alert: `CactusTradeKillSwitchOn`. Causes: an operator's kill, or the supervisor's own trip when
the account's equity fell through the drawdown floor (risk limits). The kill reason is in
`GET /api/v1/agent` (`killReason`), the *Events* list and the audit log.

What cactusTrade did: stored the kill first, cancelled our pending orders, sent a close for
every position it opened (never for positions opened outside it, OD1); the supervisor keeps
closing what is left. The agent stays killed until an admin resets it.

1. Check that everything closed: `GET /api/v1/agent` (`openPositions` 0, `pendingOrders` 0)
   and the eToro portfolio. If closes keep failing, follow *eToro API down*, and close
   by hand before the session ends.
2. Find out why before anything else: a drawdown trip means the limits did their job; a manual
   kill has its reason and actor in the audit log.
3. Reset only when the cause is understood: an **admin, in a web session**, types
   `RESET <account name>` on the agent panel (`POST /api/v1/agent/reset`, API keys refused).
   The high-water mark becomes the equity eToro reports now and the agent comes back
   **stopped**: starting it again is a separate, deliberate step.

### Reconciliation mismatch

Alert: `CactusTradeUnreconciled` (also `CactusTradeOrderOutcomeUnknown`). While unreconciled
there are **no new entries**; exits continue. The reason is `reconciliation.reason` in
`GET /api/v1/agent` and the `reconcile.failed` event.

| Reason (start of the text) | What to do |
|---|---|
| `reading the portfolio failed` / `looking up order … failed` | eToro or the network: *eToro API down*. The next pass (every 5 min) or "reconcile now" clears it. |
| `position N (not opened by cactusTrade) is not a real share long or a CFD short at leverage 1` | A position someone opened by hand in a shape cactusTrade never uses. cactusTrade never closes foreign positions (OD1): **close it in eToro's web app**, then reconcile now. |
| `keys changed` | New eToro keys: reconcile now. If it fails, the keys belong to another eToro account than before: put the right keys back. |
| `reading our orders failed`, `reading the agent positions failed`, `adopting …` / `reopening …` failed | The database: check `/readyz`, disk space and the MariaDB / SQLite logs; reconcile again when it answers. |
| `order … : …` (an order still `unknown`) | eToro has not confirmed an order we sent. Look it up in eToro's order history by its reference id (the request id in the *Orders* page). Reconcile again; an open eToro never saw is abandoned after 2 minutes on its own. |

Then reconcile now and check `reconciliation.ok: true`. Never edit the `orders` or `agent_positions`
tables by hand: the request ids are what keeps a retry from sending twice.

### Lease held by a dead process

Alert: `CactusTradeLeaseNotHeld`. One process at a time holds an account's lease (D22, 30 s,
renewed before every send). A crashed process's lease **expires by itself within 30 seconds**;
this alert fires only after 2 minutes, so a live process is holding it.

1. Who holds it: `GET /api/v1/agent` shows `lease.mine` / `lease.other` and `lease.expiresAt`
   (an expiry that keeps moving forward means a live holder). The holder itself (host name + a
   random id per process) is the `holder` column of `agent_leases` (read it; never edit it).
2. Standalone: another `cactustrade serve` on the same data folder or SQLite file (each server
   keeps `<data>/locks/serve-*.lock` fresh; `ps`/Task Manager). Docker: `docker ps` for a second
   app container (an old compose project, a manual `docker run`). k3s/k8s: `kubectl -n
   cactustrade get pods` — the app has `replicas: 1` and `Recreate`; a second pod is a manual
   change.
3. Stop the extra process. The lease expires within 30 s and the right one takes it on its
   next pass.
4. **Never delete `agent_leases` rows** while any cactusTrade process could still be alive: two
   writers can send the same decision twice. If you must, stop *every* process first (then
   there is nothing to delete: a stopped process released its lease, a dead one's expired).

### Restore

The procedures are in the README, *Backups and restore* (standalone: `cactustrade restore`;
containers: `./deploy.sh --restore FILE`, with `--dry-run` to check the file first). Both refuse
while a server uses the database or holds a lease, and keep the replaced database. After a
restore:

1. Everyone signs in again (sessions are not restored).
2. The server reconciles every account with eToro **before** any agent, supervisor pass or
   order (PLAN 4.14). Positions opened after the backup was taken are matched against eToro:
   check the reconciliation of each account and the eToro portfolio before starting an agent.
3. A real-trading arm older than the restore is disarmed by the gate (`restore.at`, PLAN 7.12 /
   7.13): arm again only through the full gate.

### Vault unavailable (only with `CACTUSTRADE_SECRETS=vault`)

Logs: "Vault is not usable yet" (at start) or "Vault is unavailable: using the credentials last
read from it". A running server keeps the keys it already read, so its exits keep working; a
server that **starts** while Vault is sealed or down has no keys: nothing trades and nothing is
closed by cactusTrade until Vault answers.

1. Unseal / restore Vault; check the token (file rotated by Vault Agent? Kubernetes role
   still bound?). cactusTrade re-reads the token file or signs in again on a 403 and renews
   its token two thirds into its lease.
2. If positions are open and Vault will not be back before the session close: close them in
   eToro's web app.
3. Do not restart cactusTrade during a Vault outage unless you must: the restarted process
   cannot read any key.

### Disarm real trading now (planned procedure)

**Today real trading is impossible in this build** (`domain.RealTradingEnabled = false`,
PLAN 7.10; every real-mode site S1–S11 refuses). The gate that arms a real account arrives with
PLAN 7.13–7.15 and the lift only after the owner's written consent (7.18); this procedure is the
planned one and is confirmed when 7.16 documents the gate. Do the steps in this order; each one
alone stops new real entries, and **exits continue** after every one of them.

1. **Kill the real account** (fastest; operator with trade access, session or API key, never
   rate-limited): `POST /api/v1/agent/kill?accountId=<real account>` (above). Our pending orders
   are cancelled and our positions closed.
2. **Disarm** (trade access and the `agent.kill` permission, session or API key, never
   rate-limited, D32): the *Disarm* button under *Settings → Real trading*, or
   ```sh
   curl -fsS -X POST -H "Authorization: Bearer $CT_KEY" -H 'Content-Type: application/json' \
     -d '{"reason":"runbook: disarm now"}' "$CT/api/v1/accounts/$REAL_ACCT/real-gate/disarm"
   ```
   The agent goes to stopped; arming again needs the full gate (admin, owner, password, phrase,
   evidence).
3. **Switch the deployment off:** `CACTUSTRADE_REAL_TRADING=off` (the default, OD28; `deploy.sh`
   never sets it) and restart. Real entries are then refused even on an armed account; real
   exits never depend on it.
4. **Cut eToro access** if cactusTrade itself is in doubt: delete or regenerate the real API
   key in eToro's settings. From then on, close any real position in eToro's web app.
5. Check: the red "REAL MONEY" banner is gone, `cactustrade_real_gate_armed` is 0 for every
   account (alert `CactusTradeRealArmed` resolves), the audit log has the disarm with your
   reason, and the eToro portfolio shows no position cactusTrade opened.

## Operating notes

### Trusted proxies on k3s/k8s

The app takes the client address from `X-Forwarded-For` only when the TCP peer is in
`CACTUSTRADE_TRUSTED_PROXIES` (k3s/k8s: `K8S_TRUSTED_PROXIES` in `.env.local`). The default is
**empty: nobody is trusted**, so every client counts as the ingress controller's address for the
sign-in limiter and the API rate limits (safe, but one client's failures slow everyone down;
`deploy.sh` warns). Trust the ingress controller's pods and nothing else:

```sh
# where the ingress controller runs (k3s: Traefik in kube-system)
kubectl -n kube-system get pods -l app.kubernetes.io/name=traefik -o wide      # IP and NODE
# the pod address range of those nodes (survives a Traefik restart, unlike the pod IP)
kubectl get nodes -o custom-columns=NAME:.metadata.name,PODCIDR:.spec.podCIDR
```

Set `K8S_TRUSTED_PROXIES` to the pod CIDR of the node(s) running the ingress controller (single
node k3s: usually `10.42.0.0/24`), or to the pods' own IPs (they change when the pods restart),
then `./deploy.sh --upgrade --k3s`. This is safe because the NetworkPolicy `cactustrade` lets only
the ingress controller's pods reach the app (`K8S_INGRESS_NAMESPACE`,
`K8S_INGRESS_POD_SELECTOR`); **it relies on a network plugin that enforces NetworkPolicy** (k3s's
built-in one does unless started with `--disable-network-policy`; on k8s check yours). Check:
sign in from another machine; the audit log shows that machine's address, not a `10.42.x.x` one.

A Prometheus in another namespace needs its own NetworkPolicy to reach `/metrics` (port 8080 on
the pods labelled `app.kubernetes.io/name: cactustrade`).

### Releases

`make dist` builds `dist/cactustrade-{linux,darwin,windows}-{amd64,arm64}[.exe]` with the web app
embedded, writes `SHA256SUMS`, signs it with the release SSH key (OD31, `ssh-keygen -Y sign`,
namespace `cactustrade-release`, key `RELEASE_KEY`, default
`~/.config/cactustrade/release_ed25519`) and verifies the result as `setup.sh` will
(`scripts/release-verify.sh`, against `scripts/allowed_signers`). Publishing is the owner's:
upload the six binaries, `SHA256SUMS` and `SHA256SUMS.sig` as the GitHub release
`v<VERSION>`. `setup.sh` / `setup.ps1` install only a release whose `SHA256SUMS` carries that
signature and lists the binary's checksum.

**No release key is published yet** (an owner step): create it once, off any shared machine,
and keep a backup offline:

```sh
ssh-keygen -t ed25519 -C cactustrade-release -f ~/.config/cactustrade/release_ed25519
```

then put `cactustrade-release namespaces="cactustrade-release" <the .pub line>` into
`scripts/allowed_signers` and as `SIGNER` into `setup.sh` and `setup.ps1` (a test checks the
three agree). Until then both setup scripts refuse to install anything.
