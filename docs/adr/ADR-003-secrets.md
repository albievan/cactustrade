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
Database backups contain ciphertext only. A HashiCorp Vault `SecretStore` adapter is planned
for phase 7.
