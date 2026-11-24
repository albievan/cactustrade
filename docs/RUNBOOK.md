# cactusTrade — runbook

What to watch and what to do. PLAN 7.9 adds the incident procedures (eToro API down, kill
switch tripped, reconciliation mismatch, lease held by a dead process, restore, disarm real
trading now); this file starts with the monitoring (PLAN 7.5).

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
