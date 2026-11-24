# ADR-003: Write-only credentials, AES-256-GCM under a master key

**Status:** accepted (2026-11-22)

**Decision.** The eToro key pairs (demo and real, separately) and the LLM key are stored in
the `secrets` table, sealed with AES-256-GCM. Each value has a random 96-bit nonce, and the
credential name is the associated data, so a ciphertext cannot be moved under another name.
The master key comes from `CACTUSTRADE_MASTER_KEY`, `_FILE`, or the deployment secret
`master_key`. Standalone, the binary generates `master.key` (0600) in the data folder on first
start. The UI and API can set and delete credentials but never read them. Only adapters get the
values, through `SecretStore.Get`.

`ETORO_DEMO_PUB` / `ETORO_DEMO_PK` (owner's request, 2026-11-22), or their `_FILE` variants,
override the stored demo pair. In the UI they are shown as "set by the environment" and are
read-only there.

**Consequences.** Losing the master key makes the stored credentials unreadable, so they must
be entered again. `deploy.sh` and the startup log both tell the operator to back it up.
Database backups contain ciphertext only. An optional HashiCorp Vault `SecretStore` followed in
phase 7 (update below).

## Update 2026-11-24: optional Vault store (PLAN 7.8, OD30)

**Decision.** `CACTUSTRADE_SECRETS=vault` keeps the same credentials in HashiCorp Vault's KV
version 2 engine instead of the `secrets` table; `db` stays the default. The client
(`internal/adapters/vault`) uses only the standard library (no Vault SDK; depguard denies one)
and implements the same `SecretStore` port, so nothing above it changes: write-only for the UI
and API, `Get` only for the adapters, the environment overlay (`ETORO_DEMO_PUB` / `_PK`) still
wins.

- One KV v2 secret per credential, `<mount>/<prefix>/<name>` = `{"value": "…"}`
  (`CACTUSTRADE_VAULT_MOUNT`, default `secret`; `CACTUSTRADE_VAULT_PREFIX`, default
  `cactustrade`). Delete removes every version (metadata delete). `Status` takes
  `updated_time` from the metadata (the Saxo 24-hour token check needs it).
- Token: `CACTUSTRADE_VAULT_TOKEN_FILE` (a Vault Agent sink or an operator's file; default
  `vault_token` in the secrets folder) **or** Kubernetes auth (`CACTUSTRADE_VAULT_K8S_ROLE`, the
  pod's service-account JWT, `CACTUSTRADE_VAULT_K8S_MOUNT`). The token's TTL comes from the login
  or `lookup-self`; it is renewed (`renew-self`) two thirds into its lease, and fetched again when
  a renewal is refused or leaves under 30 s. A 403 re-reads / signs in once and retries once.
- `https` only (plain `http` to a loopback address for a local dev Vault); system roots plus
  `CACTUSTRADE_VAULT_CA_FILE`; TLS ≥ 1.2; **redirects are refused** (a standby node's redirect
  would carry the token elsewhere: point the address at the active node or its load balancer).
  Tokens, the JWT and values are never logged or put into an error; only the TTL is logged.
- **Fail closed, exits still work:** Vault not answering at start is a warning, not a stop (the
  kill switch and the web UI must work); a credential this process has not read yet cannot be
  read meanwhile, so that account does not trade. During an outage the value the process last
  read or wrote is used (memory only; one warning naming the credential), so a running session
  keeps its eToro keys and can still close its positions before the session ends. A deleted
  value, or one Vault reports gone, is forgotten. A wrong setting stops the start.
- The master key is not needed while the credentials live in Vault (none is generated).
- `cactustrade secrets migrate --to vault [--dry-run]` copies every credential from the database
  (with the master key; never generates one) into Vault, reads each back, prints names only and
  leaves the database copies in place.
- Not shipped in `deploy.sh` / the k8s manifest: the app pod has
  `automountServiceAccountToken: false`; Kubernetes auth needs a projected service-account token
  volume and a Vault role, which are the operator's to add (README).

**Consequences.** Vault becomes a dependency when chosen: a process that starts while Vault is
sealed or down has no keys (no trading, no exits) until it answers; a running one carries on
with the keys it already read. Revoking cactusTrade's Vault token therefore does not cut off
eToro until a restart (revoke the eToro keys for that). Backups of the database no longer hold
the credentials (not even encrypted); Vault's own backup covers them.
