# ADR-005: Deployment like cactusInv

**Status:** accepted (2026-11-22)

**Decision.** `deploy.sh ACTION TARGET [--ssh-access user@host] [--yes]` with the actions
install, upgrade (git fast-forward, backup first, verify, roll back on failure), version,
status, backup-now, uninstall and delete-uninstall. The targets are Docker Desktop/Engine,
Podman, k3s (image imported with `k3s ctr`) and k8s (image pushed to `K8S_REGISTRY`).
Settings come from `.env` plus `.env.local`; secrets are files in `SECRETS_DIR` or the k8s
Secret `cactustrade-secrets`, generated once.

The image is distroless and non-root, runs with a read-only root filesystem, and is built
entirely in containers. With Docker the app serves HTTPS itself (a self-signed certificate in
the data volume unless one is supplied). On k3s/k8s, TLS ends at the ingress (Traefik) and the
pod speaks HTTP.

**Consequences.** `--backup-now` writes a gzip'd `mariadb-dump` (mode 600). It contains
ciphertext and password hashes, never plaintext credentials. Encrypted app-level backups and
`--restore` come in phase 7.

**Update (PLAN 7.6, 2026-11-24).** Backups are age-encrypted (OD29): `--backup-now` writes
`mariadb-dump | gzip | age` to `CACTUSTRADE_BACKUP_RECIPIENT` (`.sql.gz.age`), and keeps the
plain gzip with a warning while no recipient is set. `--restore FILE` checks the file first
(decrypts, decompresses, schema not newer than this version; `--dry-run` stops there), asks
for `restore`, takes a backup, stops the app, loads the dump into a scratch database and swaps
it in with one `RENAME TABLE` (the replaced tables stay in `<db>_prerestore`), and starts the
app, which reconciles with eToro before anything else. The standalone binary has
`cactustrade backup` / `restore` (SQLite `VACUUM INTO`, age; refused while a server uses the
data folder). The master key is never in a backup. Images are pinned by digest (7.1.17).

**Update (R48, 2026-11-28).** The app backs itself up too (Setup → Backups): an in-process
scheduler, age-encrypted files into a folder fixed at deploy time (`CACTUSTRADE_BACKUP_DIR`:
`${BACKUP_DIR}/app` on Docker/Podman, a `backups` PVC on k3s/k8s), downloads from a web
session. On MariaDB it writes its own consistent logical dump in Go
(`sqlstore.DumpMariaDB`; the app image has no `mariadb-dump`), loadable by the same
`mariadb-restore.sh`, next to `deploy.sh --backup-now`, which keeps using `mariadb-dump` in
the database container. Both schedules can run side by side; `deploy.sh` prunes only
`BACKUP_DIR` itself, the app only its own folder.
