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
