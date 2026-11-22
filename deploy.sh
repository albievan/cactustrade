#!/usr/bin/env bash
# cactusTrade deployment — one script for Docker Desktop / Docker Engine, Podman, k3s and
# Kubernetes. Containers always run cactusTrade with MariaDB (standalone uses SQLite).
#
#   ./deploy.sh ACTION TARGET [--ssh-access user@host] [--yes]
#
# ACTION
#   --install            build and start (or reinstall) cactusTrade from this folder
#   --upgrade [--to REF] upgrade to the newest version: fetch it from git (the branch's
#                        upstream, or REF: a tag, branch or commit), show what changes, take a
#                        backup, install it and check it runs; if not, go back to this version
#   --version            show the running version and database schema
#   --status             show what is running
#   --backup-now         dump the database now into BACKUP_DIR (gzip, mode 600; it holds
#                        encrypted credentials and password hashes, not their plaintext)
#   --uninstall          stop and remove cactusTrade, KEEPING the database, data and secrets
#   --delete-uninstall   remove cactusTrade AND DELETE all its data (asks for confirmation)
#
# TARGET
#   --dockerdesktop      Docker Desktop, or Docker Engine on a Linux host (docker compose)
#   --podman             Podman (podman compose, with docker-compose v2 or podman-compose)
#   --k3s                a k3s node (image imported straight into k3s; no registry needed)
#   --k8s                any Kubernetes cluster (image pushed to K8S_REGISTRY from .env)
#
# OPTIONS
#   --ssh-access user@host   deploy to a remote machine over SSH, e.g. root@192.0.2.10
#   --yes                    do not ask for confirmation (--delete-uninstall, --upgrade)
#   -h, --help               show this help
#
# Settings come from .env next to this script; .env.local beside it (not in git) overrides
# them for this machine. eToro DEMO keys: export ETORO_DEMO_PUB (Public API Key) and
# ETORO_DEMO_PK (User Key) before --install; they are copied into the secrets, never .env.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

ACTION="" TARGET="" SSH_HOST="" ASSUME_YES=0 UPGRADE_TO="" DOWN_TO=""
REMOTE_DIR="${CACTUSTRADE_REMOTE_DIR:-/opt/cactustrade}"

die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }
ok() { printf '\033[32m✔\033[0m %s\n' "$*"; }
usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit "${1:-0}"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --install | --uninstall | --delete-uninstall | --status | --backup-now | --upgrade | --version | --check-data)
      [[ -z "$ACTION" ]] || die "choose one action"; ACTION="${1#--}" ;;
    --to) UPGRADE_TO="${2:-}"; shift; [[ -n "$UPGRADE_TO" && "$UPGRADE_TO" != --* ]] || die "--to needs a tag, branch or commit" ;;
    --migrate-down-to)
      # Used by --upgrade to undo a failed upgrade's migrations (with the newer version).
      [[ -z "$ACTION" ]] || die "choose one action"; ACTION=migrate-down-to; DOWN_TO="${2:-}"; shift
      [[ "$DOWN_TO" =~ ^[0-9]+$ ]] || die "--migrate-down-to needs a schema version" ;;
    --dockerdesktop | --docker | --podman | --k3s | --k8s)
      [[ -z "$TARGET" ]] || die "choose one target"; TARGET="${1#--}"; [[ "$TARGET" == docker ]] && TARGET=dockerdesktop ;;
    --ssh-access) SSH_HOST="${2:-}"; shift; [[ -n "$SSH_HOST" && "$SSH_HOST" != --* ]] || die "--ssh-access needs user@host" ;;
    --yes | -y) ASSUME_YES=1 ;;
    -h | --help) usage 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
  shift
done
[[ -n "$ACTION" ]] || usage 1
[[ -n "$TARGET" ]] || die "choose a target: --dockerdesktop, --podman, --k3s or --k8s"
DOCKER=docker
[[ "$TARGET" == podman ]] && DOCKER=podman
[[ -z "$UPGRADE_TO" || "$ACTION" == upgrade ]] || die "--to goes with --upgrade"
export TARGET

# ---------------------------------------------------------------------------------------
# .env — parsed line by line (KEY=value, optional quotes), never executed.
# ---------------------------------------------------------------------------------------
load_env() {
  [[ -f .env ]] || die ".env not found in $ROOT"
  [[ -f .env.local ]] && read_env_file .env.local
  read_env_file .env
}
read_env_file() {
  local line key val
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ "$line" =~ ^[[:space:]]*(#|$) ]] && continue
    [[ "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]] || continue
    key="${BASH_REMATCH[1]}" val="${BASH_REMATCH[2]}"
    if [[ "$val" =~ ^\"(.*)\"$ || "$val" =~ ^\'(.*)\'$ ]]; then val="${BASH_REMATCH[1]}"; fi
    [[ -n "${!key+x}" ]] || export "$key=$val" # explicit environment variables win
  done <"$1"
}

confirm() {
  [[ $ASSUME_YES -eq 1 ]] && return 0
  [[ -t 0 ]] || die "$1 — re-run with --yes to confirm non-interactively"
  read -r -p "$1 Type 'delete' to continue: " answer
  [[ "$answer" == delete ]] || die "aborted"
}

ssh_run() { ssh -o BatchMode=yes -o ConnectTimeout=15 "$SSH_HOST" "$@"; }

# ---------------------------------------------------------------------------------------
# --upgrade: every step is this script run again (--version, --backup-now, --install,
# --migrate-down-to), so the install is the NEW version's deploy.sh.
# ---------------------------------------------------------------------------------------
self() {
  local ssh=()
  [[ -z "$SSH_HOST" ]] || ssh=(--ssh-access "$SSH_HOST")
  "$ROOT/deploy.sh" "$@" "--$TARGET" ${ssh[@]+"${ssh[@]}"}
}

running_version() { self --version </dev/null 2>/dev/null | tr -d '\r'; }

schema_head() {
  git -C "$ROOT" ls-tree --name-only "$1" migrations/mariadb/ | sed -n 's#^migrations/mariadb/0*\([0-9][0-9]*\)_.*#\1#p' | sort -n | tail -1
}

upgrade() {
  command -v git >/dev/null || die "--upgrade needs git"
  git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1 || die "--upgrade needs the git checkout cactusTrade is installed from"
  local branch remote target old running run_line run_schema new_version new_build
  local old_head new_head changes count backup_out backup_name answer
  branch="$(git -C "$ROOT" symbolic-ref --quiet --short HEAD)" || die "this checkout is not on a branch; check out main first (nothing was changed)"
  git -C "$ROOT" diff --quiet HEAD -- || die "tracked files have uncommitted changes; commit or stash them first (nothing was changed)"

  info "reading the running version"
  running="$(running_version)" || true
  run_line="$(printf '%s\n' "$running" | grep '(build ' | tail -1 || true)"
  run_schema="$(printf '%s\n' "$running" | sed -n 's/^schema \([0-9][0-9]*\),.*/\1/p' | tail -1)"
  [[ -n "$run_line" ]] || die "cactusTrade is not running on this target: install it with --install first (nothing was changed)"

  remote="$(git -C "$ROOT" config "branch.${branch}.remote" || echo origin)"
  info "fetching the newest version from ${remote}"
  git -C "$ROOT" fetch --quiet --tags "$remote" || die "cannot fetch from ${remote} (nothing was changed)"
  if [[ -n "$UPGRADE_TO" ]]; then
    target="$(git -C "$ROOT" rev-parse --verify --quiet "${UPGRADE_TO}^{commit}" ||
      git -C "$ROOT" rev-parse --verify --quiet "${remote}/${UPGRADE_TO}^{commit}")" || die "no such version: $UPGRADE_TO (nothing was changed)"
  else
    target="$(git -C "$ROOT" rev-parse --verify --quiet '@{upstream}^{commit}')" || die "branch $branch follows no remote branch; name the version with --to (nothing was changed)"
  fi
  old="$(git -C "$ROOT" rev-parse HEAD)"
  git -C "$ROOT" merge-base --is-ancestor "$old" "$target" ||
    die "$(git -C "$ROOT" rev-parse --short "$target") is not newer than this checkout (nothing was changed)"

  new_version="$(git -C "$ROOT" show "${target}:VERSION" | tr -d '[:space:]')" new_build="$(git -C "$ROOT" rev-list --count "$target")"
  if [[ "$old" == "$target" && "$run_line" == "$(tr -d '[:space:]' <"$ROOT/VERSION") (build $(git -C "$ROOT" rev-list --count HEAD))" ]]; then
    ok "already the newest version: $run_line"
    return 0
  fi
  old_head="$(schema_head "$old")" new_head="$(schema_head "$target")"
  [[ "$run_schema" =~ ^[0-9]+$ ]] && old_head="$run_schema"
  count="$(git -C "$ROOT" rev-list --count "${old}..${target}")"
  changes="$(git -C "$ROOT" log --no-decorate --format='    %h %s' "${old}..${target}" | head -25 || true)"
  echo
  echo "  running now:  ${run_line}${run_schema:+, database schema $run_schema}"
  echo "  upgrade to:   ${new_version} (build ${new_build}), commit $(git -C "$ROOT" rev-parse --short "$target")"
  [[ "$count" -gt 0 ]] && { echo "  changes (${count}):"; echo "$changes"; }
  if [[ "${new_head:-0}" -gt "${old_head:-0}" ]]; then
    echo "  database:     schema ${old_head} → ${new_head} (changed when the new version starts)"
  else
    echo "  database:     no changes"
  fi
  echo
  if [[ $ASSUME_YES -ne 1 ]]; then
    [[ -t 0 ]] || die "re-run with --yes to upgrade non-interactively (nothing was changed)"
    read -r -p "A backup is taken first. Type 'upgrade' to continue: " answer
    [[ "$answer" == upgrade ]] || die "aborted; nothing was changed"
  fi

  self --check-data </dev/null || die "the data folders do not match the running install (nothing was changed)"
  info "taking a backup first"
  backup_out="$(self --backup-now </dev/null 2>&1 | tr -d '\r')" || { echo "$backup_out"; die "the backup failed, so nothing was changed"; }
  backup_name="$(printf '%s\n' "$backup_out" | grep -o 'cactustrade-[A-Za-z0-9._-]*\.sql\.gz' | head -1 || true)"
  [[ -n "$backup_name" ]] || { echo "$backup_out"; die "the backup's name could not be read, so nothing was changed"; }
  ok "backup ${backup_name}"

  if [[ "$old" != "$target" ]]; then
    git -C "$ROOT" merge --ff-only --quiet "$target" || die "git could not move to $(git -C "$ROOT" rev-parse --short "$target") (nothing was changed; backup ${backup_name})"
  fi
  info "installing ${new_version} (build ${new_build})"
  if self --install </dev/null && upgrade_verify "${new_version} (build ${new_build})"; then
    ok "upgraded: ${run_line} → ${new_version} (build ${new_build}); the backup taken first is ${backup_name}"
    return 0
  fi
  warn "the upgrade did not complete; going back to ${run_line}"
  if [[ "${new_head:-0}" -gt "${old_head:-0}" ]]; then
    self --migrate-down-to "$old_head" </dev/null || warn "the database changes could not be undone automatically"
  fi
  git -C "$ROOT" reset --keep --quiet "$old" || die "git could not go back; run: git reset --keep $old && ./deploy.sh --install --$TARGET (backup ${backup_name})"
  if self --install </dev/null && upgrade_verify "$run_line"; then
    die "the upgrade failed and ${run_line} is running again (backup ${backup_name}); see above for why"
  fi
  die "the upgrade failed and could not be rolled back completely; restore ${backup_name} (README, \"Restoring a backup\")"
}

upgrade_verify() {
  local got
  for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
    got="$(running_version | grep '(build ' | tail -1 || true)"
    [[ "$got" == "$1" ]] && return 0
    sleep 5
  done
  warn "the running version is '${got:-unknown}', not $1"
  return 1
}

if [[ "$ACTION" == upgrade ]]; then upgrade; exit $?; fi

load_env
[[ "${MARIADB_DATABASE:-}" =~ ^[A-Za-z0-9_]{1,57}$ ]] || die "MARIADB_DATABASE must be 1-57 letters, digits or _"
[[ "${MARIADB_USER:-}" =~ ^[A-Za-z0-9_]{1,80}$ ]] || die "MARIADB_USER must be 1-80 letters, digits or _"

export CACTUSTRADE_VERSION CACTUSTRADE_BUILD
CACTUSTRADE_VERSION="$(tr -d '[:space:]' <"$ROOT/VERSION" 2>/dev/null || echo dev)"
CACTUSTRADE_BUILD="${CACTUSTRADE_BUILD:-$(git -C "$ROOT" rev-list --count HEAD 2>/dev/null || echo 0)}"

image_tag() {
  local v
  v="$(git -C "$ROOT" describe --tags --always --dirty 2>/dev/null || date +%Y%m%d%H%M%S)"
  [[ "$v" == *-dirty ]] && v="${v}-$(date +%Y%m%d%H%M%S)"
  echo "${CACTUSTRADE_IMAGE%%:*}:${v}"
}

# 32 random bytes as 64 hex characters (the AES-256 master key).
random_key() { od -An -N32 -tx1 /dev/urandom | tr -d ' \n'; }
random_secret() { od -An -N24 -tx1 /dev/urandom | tr -d ' \n'; }

# etoro_value prints the eToro demo key for a secret file from the environment, if set.
etoro_value() {
  case "$1" in
    etoro_demo_pub) printf '%s' "${ETORO_DEMO_PUB:-}" ;;
    etoro_demo_pk) printf '%s' "${ETORO_DEMO_PK:-}" ;;
  esac
}

# ---------------------------------------------------------------------------------------
# Docker / Podman
# ---------------------------------------------------------------------------------------
compose() { "$DOCKER" compose --project-directory "$ROOT" -f "$ROOT/docker-compose.yml" "$@"; }

abs_dir() { case "$1" in /*) printf '%s' "${1%/}" ;; *) printf '%s/%s' "$ROOT" "${1#./}" ;; esac; }

NEW_SETUP=0
ensure_secrets_dir() {
  mkdir -p "$SECRETS_DIR"
  chmod 700 "$SECRETS_DIR"
  local s
  for s in db_password db_root_password metrics_token; do
    [[ -s "$SECRETS_DIR/$s" ]] || { random_secret >"$SECRETS_DIR/$s"; ok "generated $SECRETS_DIR/$s"; }
  done
  if [[ ! -s "$SECRETS_DIR/master_key" ]]; then
    random_key >"$SECRETS_DIR/master_key"
    ok "generated $SECRETS_DIR/master_key (encrypts the eToro and LLM keys; BACK IT UP off this machine)"
  fi
  if [[ ! -s "$SECRETS_DIR/setup_token" ]]; then
    random_secret >"$SECRETS_DIR/setup_token"
    NEW_SETUP=1
  fi
  for s in etoro_demo_pub etoro_demo_pk; do
    local v; v="$(etoro_value "$s")"
    if [[ -n "$v" ]]; then
      printf '%s' "$v" >"$SECRETS_DIR/$s"
      ok "eToro demo key $s taken from the environment"
    elif [[ ! -f "$SECRETS_DIR/$s" ]]; then
      : >"$SECRETS_DIR/$s"
    fi
  done
  # MariaDB and the app run as other users and must read the files (the folder is 700).
  chmod 644 "$SECRETS_DIR"/*
}

docker_preflight() {
  if [[ "$DOCKER" == podman ]]; then
    command -v podman >/dev/null || die "podman is not installed"
    podman info >/dev/null 2>&1 || die "podman does not work for this user (podman info fails)"
    podman compose version >/dev/null 2>&1 || die "podman compose needs docker-compose (v2) or podman-compose installed"
    return 0
  fi
  command -v docker >/dev/null || die "docker is not installed"
  docker info >/dev/null 2>&1 || die "the Docker daemon is not running (start Docker Desktop)"
  docker compose version >/dev/null 2>&1 || die "docker compose v2 is required"
}

same_dir() { [[ "$(printf '%s' "${1%/}" | tr -s /)" == "$(printf '%s' "${2%/}" | tr -s /)" ]]; }

# docker_data_guard stops when the running database is in another folder than
# MARIADB_DATA_DIR says: an .env written for another host would otherwise start an empty
# database with new keys beside the real one.
docker_data_guard() {
  local id cur want
  id="$(compose ps -q mariadb 2>/dev/null || true)"
  [[ -n "$id" ]] || return 0
  cur="$("$DOCKER" inspect -f '{{range .Mounts}}{{if eq .Destination "/var/lib/mysql"}}{{.Source}}{{end}}{{end}}' "$id" 2>/dev/null || true)"
  want="$(abs_dir "$MARIADB_DATA_DIR")"
  [[ -z "$cur" ]] || same_dir "$cur" "$want" ||
    die "the running database is in $cur but MARIADB_DATA_DIR is $want: set MARIADB_DATA_DIR (and DATA_DIR, SECRETS_DIR) to this install's folders (nothing was changed)"
}

docker_install() {
  docker_preflight
  docker_data_guard
  mkdir -p "$MARIADB_DATA_DIR" "$DATA_DIR" "$BACKUP_DIR"
  ensure_secrets_dir
  info "building cactusTrade ${CACTUSTRADE_VERSION} (build ${CACTUSTRADE_BUILD})"
  compose build app
  info "starting cactusTrade (MariaDB + app)"
  compose up -d --remove-orphans
  info "waiting for the app to become healthy"
  local id state
  for _ in $(seq 1 90); do
    id="$(compose ps -q app 2>/dev/null || true)"
    state="$("$DOCKER" inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$id" 2>/dev/null || true)"
    if [[ "$state" == healthy ]]; then
      ok "cactusTrade is running at https://localhost:${HTTPS_PORT} (self-signed certificate unless you set your own)"
      if [[ $NEW_SETUP -eq 1 ]]; then
        echo "    first run:     open the page and create the administrator with this setup token:"
        echo "                   $(cat "$SECRETS_DIR/setup_token")"
      else
        echo "    setup token:   $(abs_dir "$SECRETS_DIR")/setup_token (only needed before the first administrator exists)"
      fi
      echo "    master_key:    keep a copy of $(abs_dir "$SECRETS_DIR")/master_key OFF this machine"
      return 0
    fi
    sleep 2
  done
  compose logs --tail 50 app
  die "cactusTrade did not become healthy"
}

docker_uninstall() {
  docker_preflight
  info "stopping and removing containers (data is kept)"
  compose down --remove-orphans
  ok "uninstalled; data kept in $DATA_DIR, $MARIADB_DATA_DIR and $SECRETS_DIR"
}

docker_delete_uninstall() {
  docker_preflight
  confirm "This deletes the database, the app data, the backups and the secrets (incl. the master key)."
  compose down --volumes --remove-orphans --rmi local || true
  local d
  for d in "$MARIADB_DATA_DIR" "$DATA_DIR" "$BACKUP_DIR"; do
    if [[ -d "$d" ]]; then
      # Files are owned by container users; delete them from inside a container.
      "$DOCKER" run --rm -v "$(cd "$d" && pwd):/target" busybox:1.37 sh -c 'rm -rf /target/* /target/.[!.]* 2>/dev/null; true'
      rmdir "$d" 2>/dev/null || true
    fi
  done
  rm -rf "$SECRETS_DIR"
  ok "cactusTrade and all of its data were removed"
}

docker_status() { docker_preflight; compose ps; }

docker_version() {
  docker_preflight
  compose exec -T app /cactustrade version </dev/null || die "cactusTrade is not running here"
  compose exec -T app /cactustrade migrate status </dev/null
}

docker_migrate_down_to() {
  docker_preflight
  compose stop app </dev/null
  compose run --rm --no-deps -T app migrate down-to "$DOWN_TO" </dev/null
}

DUMP_SQL='MYSQL_PWD="$(cat /run/secrets/db_root_password)" exec mariadb-dump -uroot --single-transaction --routines --triggers'

backup_file() { printf '%s/cactustrade-%s.sql.gz' "$BACKUP_DIR" "$(date -u +%Y%m%dT%H%M%SZ)"; }

docker_backup_now() {
  docker_preflight
  mkdir -p "$BACKUP_DIR"
  local f; f="$(backup_file)"
  (umask 077 && compose exec -T mariadb sh -c "$DUMP_SQL $MARIADB_DATABASE" </dev/null | gzip >"$f") || { rm -f "$f"; die "the backup failed"; }
  ok "backup $(basename "$f") in $(abs_dir "$BACKUP_DIR") (restore needs the same master_key)"
}

# ---------------------------------------------------------------------------------------
# k3s / Kubernetes
# ---------------------------------------------------------------------------------------
kc() {
  local cmd
  if [[ "$TARGET" == k3s ]]; then cmd="k3s kubectl"; else cmd="kubectl"; fi
  if [[ -n "$SSH_HOST" ]]; then
    if [[ "$TARGET" == k3s && -z "${KC_PREFIX+x}" ]]; then
      KC_PREFIX=""; [[ "$(ssh_run id -u)" == 0 ]] || KC_PREFIX="sudo -n "
    fi
    ssh_run "${KC_PREFIX:-}$cmd $(printf '%q ' "$@")"
  else
    if [[ "$TARGET" == k3s ]] && ! command -v k3s >/dev/null; then cmd=kubectl; fi
    if [[ "$cmd" == "k3s kubectl" && $EUID -ne 0 ]]; then sudo k3s kubectl "$@"; else $cmd "$@"; fi
  fi
}

rollout() {
  local name="$1"
  if kc -n "$K8S_NAMESPACE" rollout status "deploy/$name" --timeout=300s </dev/null; then return 0; fi
  warn "deploy/$name is not ready after 5 minutes; what the cluster says:"
  kc -n "$K8S_NAMESPACE" get pods,pvc -o wide </dev/null || true
  kc -n "$K8S_NAMESPACE" describe pods -l "app.kubernetes.io/name=$name" </dev/null | sed -n '/^Events:/,$p' | tail -25 || true
  kc -n "$K8S_NAMESPACE" logs "deploy/$name" --all-containers --tail=40 </dev/null || true
  die "deploy/$name did not become ready (ImagePullBackOff/ErrImageNeverPull = image not imported; Pending = volume or resources; CrashLoopBackOff = see its log)"
}

# render substitutes ${VAR} in the manifest template from the environment.
render() {
  local tpl="$ROOT/deploy/k8s/cactustrade.yaml"
  if [[ -z "${K8S_STORAGE_CLASS:-}" ]]; then
    grep -v 'storageClassName: \${K8S_STORAGE_CLASS}' "$tpl"
  else
    cat "$tpl"
  fi | perl -pe 's/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/exists $ENV{$1} ? $ENV{$1} : die "unset variable $1\n"/ge'
}

target_arch() {
  local m
  if [[ -n "$SSH_HOST" ]]; then m="$(ssh_run uname -m)"; else m="$(uname -m)"; fi
  case "$m" in x86_64 | amd64) echo amd64 ;; aarch64 | arm64) echo arm64 ;; *) die "unsupported architecture $m" ;; esac
}

build_image() {
  command -v docker >/dev/null || die "docker is needed to build the image"
  local arch; arch="$(target_arch)"
  info "building ${IMAGE} for linux/${arch}"
  TARGET_ARCH="$arch"
  docker buildx build --platform "linux/${arch}" --provenance=false --sbom=false \
    --build-arg "VERSION=${CACTUSTRADE_VERSION}" --build-arg "BUILD=${CACTUSTRADE_BUILD}" -t "$IMAGE" --load "$ROOT"
}

publish_image() {
  if [[ "$TARGET" == k3s ]]; then
    info "importing the image into k3s"
    local ctr="k3s ctr"
    if [[ -n "$SSH_HOST" ]]; then
      [[ "$(ssh_run id -u)" == 0 ]] || ctr="sudo -n k3s ctr"
    elif [[ $EUID -ne 0 ]]; then
      ctr="sudo k3s ctr"
    fi
    local save=(docker save --platform "linux/${TARGET_ARCH}" "$IMAGE")
    if [[ -n "$SSH_HOST" ]]; then
      "${save[@]}" | ssh_run "$ctr images import -" || die "importing the image on ${SSH_HOST} failed (the SSH user needs root or passwordless sudo for k3s)"
    else
      "${save[@]}" | $ctr images import - || die "importing the image into k3s failed"
    fi
    ok "image ${IMAGE} is on the node"
  else
    [[ -n "${K8S_REGISTRY:-}" ]] || die "set K8S_REGISTRY in .env to push the image for --k8s"
    local remote="${K8S_REGISTRY%/}/${IMAGE}"
    docker tag "$IMAGE" "$remote"
    info "pushing $remote"
    docker push "$remote"
    IMAGE="$remote"
  fi
}

# secret_patch sets one key of cactustrade-secrets (value via stdin, never a command line).
secret_patch() {
  printf '{"data":{"%s":"%s"}}' "$1" "$(printf '%s' "$2" | base64 | tr -d '\n')" |
    kc -n "$K8S_NAMESPACE" patch secret cactustrade-secrets --type merge --patch-file /dev/stdin >/dev/null
}

secret_has() { kc -n "$K8S_NAMESPACE" get secret cactustrade-secrets -o "jsonpath={.data.$1}" </dev/null 2>/dev/null | grep -q .; }

k8s_secrets() {
  if ! kc -n "$K8S_NAMESPACE" get secret cactustrade-secrets >/dev/null 2>&1 </dev/null; then
    kc apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Secret
metadata:
  name: cactustrade-secrets
  namespace: ${K8S_NAMESPACE}
  labels: { app.kubernetes.io/part-of: cactustrade }
type: Opaque
stringData:
  db_password: "$(random_secret)"
  db_root_password: "$(random_secret)"
  master_key: "$(random_key)"
  setup_token: "$(random_secret)"
  metrics_token: "$(random_secret)"
  etoro_demo_pub: ""
  etoro_demo_pk: ""
YAML
    NEW_SETUP=1
    ok "generated the passwords and the master key in secret cactustrade-secrets"
  fi
  local s v
  for s in etoro_demo_pub etoro_demo_pk; do
    v="$(etoro_value "$s")"
    if [[ -n "$v" ]]; then secret_patch "$s" "$v"; ok "eToro demo key $s taken from the environment"
    elif ! kc -n "$K8S_NAMESPACE" get secret cactustrade-secrets -o "jsonpath={.data}" </dev/null | grep -q "\"$s\""; then secret_patch "$s" ""; fi
  done
}

k8s_https() {
  if [[ "${K8S_HTTPS:-false}" != true ]]; then
    kc -n "$K8S_NAMESPACE" delete ingress cactustrade --ignore-not-found </dev/null >/dev/null
    return 0
  fi
  local host="${K8S_HTTPS_HOST:-}" cert="${K8S_TLS_CERT:-}" key="${K8S_TLS_KEY:-}" use_tls=""
  [[ -z "$host" || "$host" =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$ ]] ||
    die "K8S_HTTPS_HOST must be a DNS name or empty (got '$host')"
  if [[ -n "$cert" || -n "$key" ]]; then
    [[ -f "$cert" && -f "$key" ]] || die "K8S_TLS_CERT and K8S_TLS_KEY must both name PEM files"
    kc apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Secret
metadata:
  name: cactustrade-tls
  namespace: ${K8S_NAMESPACE}
  labels: { app.kubernetes.io/part-of: cactustrade }
type: kubernetes.io/tls
data:
  tls.crt: $(base64 <"$cert" | tr -d '\n')
  tls.key: $(base64 <"$key" | tr -d '\n')
YAML
    use_tls=1
  elif kc -n "$K8S_NAMESPACE" get secret cactustrade-tls </dev/null >/dev/null 2>&1; then
    use_tls=1
  fi
  local tls="{}" rule="{" hosts=""
  if [[ -n "$host" ]]; then hosts=", hosts: [${host}]"; rule="{ host: ${host},"; fi
  if [[ -n "$use_tls" ]]; then tls="{ secretName: cactustrade-tls${hosts} }"; fi
  info "serving https://${host:-<node-ip>}/ through the ingress controller"
  kc apply -f - >/dev/null <<YAML
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: cactustrade
  namespace: ${K8S_NAMESPACE}
  labels: { app.kubernetes.io/part-of: cactustrade }
  annotations:
    traefik.ingress.kubernetes.io/router.entrypoints: websecure
    traefik.ingress.kubernetes.io/router.tls: "true"
spec:
  tls: [${tls}]
  rules:
    - ${rule} http: { paths: [{ path: /, pathType: Prefix, backend: { service: { name: cactustrade, port: { number: 80 } } } }] } }
YAML
}

k8s_install() {
  IMAGE="$(image_tag)"
  if [[ "$TARGET" == k3s ]]; then IMAGE_PULL_POLICY=Never; else IMAGE_PULL_POLICY=IfNotPresent; fi
  export IMAGE IMAGE_PULL_POLICY
  build_image
  publish_image
  export IMAGE
  info "applying manifests to namespace ${K8S_NAMESPACE}"
  kc create namespace "$K8S_NAMESPACE" --dry-run=client -o yaml | kc apply -f - >/dev/null
  k8s_secrets
  render | kc apply -f -
  k8s_https
  info "waiting for the rollout"
  rollout mariadb
  rollout cactustrade
  local host="${SSH_HOST#*@}"; [[ -n "$host" ]] || host="<node-ip>"
  if [[ "${K8S_HTTPS:-false}" == true ]]; then
    ok "cactusTrade is running at https://${K8S_HTTPS_HOST:-$host}/"
  else
    ok "cactusTrade is running at http://${host}:${K8S_NODE_PORT} (sign-in needs HTTPS unless CACTUSTRADE_COOKIE_SECURE=false)"
  fi
  if [[ $NEW_SETUP -eq 1 ]]; then
    echo "    first run:     create the administrator with the setup token:"
    echo "                   kubectl -n ${K8S_NAMESPACE} get secret cactustrade-secrets -o jsonpath='{.data.setup_token}' | base64 -d"
  fi
  echo "    master_key:    copy it OFF the cluster: kubectl -n ${K8S_NAMESPACE} get secret cactustrade-secrets -o jsonpath='{.data.master_key}' | base64 -d"
}

k8s_uninstall() {
  info "removing cactusTrade workloads (volumes and secrets are kept)"
  kc -n "$K8S_NAMESPACE" delete deploy,svc,ingress,configmap -l app.kubernetes.io/part-of=cactustrade --ignore-not-found
  ok "uninstalled; persistent volumes and secrets kept in namespace ${K8S_NAMESPACE}"
}

k8s_delete_uninstall() {
  confirm "This deletes namespace ${K8S_NAMESPACE} with the database, data and secrets (incl. the master key)."
  kc delete namespace "$K8S_NAMESPACE" --ignore-not-found --wait=true
  ok "cactusTrade and all of its data were removed"
}

k8s_status() { kc -n "$K8S_NAMESPACE" get deploy,pods,svc,pvc,ingress; }

k8s_version() {
  kc -n "$K8S_NAMESPACE" exec deploy/cactustrade -- /cactustrade version </dev/null || die "cactusTrade is not running in namespace ${K8S_NAMESPACE}"
  kc -n "$K8S_NAMESPACE" exec deploy/cactustrade -- /cactustrade migrate status </dev/null
}

k8s_migrate_down_to() { kc -n "$K8S_NAMESPACE" exec deploy/cactustrade -- /cactustrade migrate down-to "$DOWN_TO" </dev/null; }

k8s_backup_now() {
  mkdir -p "$BACKUP_DIR"
  local f; f="$(backup_file)"
  (umask 077 && kc -n "$K8S_NAMESPACE" exec -i deploy/mariadb -- sh -c "$DUMP_SQL $MARIADB_DATABASE" </dev/null | gzip >"$f") || { rm -f "$f"; die "the backup failed"; }
  ok "backup $(basename "$f") in $(abs_dir "$BACKUP_DIR") on this machine (restore needs the same master_key)"
}

# ---------------------------------------------------------------------------------------
# Remote Docker: ship the project and run this script on the host.
# ---------------------------------------------------------------------------------------
remote_docker() {
  info "copying cactusTrade to ${SSH_HOST}:${REMOTE_DIR}"
  ssh_run "mkdir -p '$REMOTE_DIR'"
  tar -C "$ROOT" --exclude=./data --exclude=./secrets --exclude=./backups --exclude=./.git --exclude=./.env.local \
    --exclude=./web/node_modules --exclude=./web/dist --exclude=./bin --exclude=./.deploy -czf - . | ssh_run "tar -C '$REMOTE_DIR' -xzf -"
  local args=("--$ACTION" "--$TARGET") pre="CACTUSTRADE_BUILD=${CACTUSTRADE_BUILD}; export CACTUSTRADE_BUILD; "
  [[ "$ACTION" == migrate-down-to ]] && args=(--migrate-down-to "$DOWN_TO" "--$TARGET")
  [[ $ASSUME_YES -eq 1 ]] && args+=(--yes)
  # eToro demo keys travel over stdin into a private file on the host, never a command line.
  if [[ -n "${ETORO_DEMO_PUB:-}${ETORO_DEMO_PK:-}" ]]; then
    printf 'ETORO_DEMO_PUB=%s\nETORO_DEMO_PK=%s\n' "${ETORO_DEMO_PUB:-}" "${ETORO_DEMO_PK:-}" |
      ssh_run "mkdir -p '$REMOTE_DIR/.deploy' && umask 077 && cat > '$REMOTE_DIR/.deploy/etoro'"
    pre="${pre}set -a; . .deploy/etoro; set +a; rm -f .deploy/etoro; "
  fi
  ssh -t "$SSH_HOST" "cd '$REMOTE_DIR' && ${pre}./deploy.sh $(printf '%q ' "${args[@]}")"
}

# ---------------------------------------------------------------------------------------
main() {
  if [[ "$TARGET" == dockerdesktop || "$TARGET" == podman ]]; then
    if [[ -n "$SSH_HOST" ]]; then remote_docker; return; fi
    case "$ACTION" in
      install) docker_install ;;
      uninstall) docker_uninstall ;;
      delete-uninstall) docker_delete_uninstall ;;
      status) docker_status ;;
      backup-now) docker_backup_now ;;
      version) docker_version ;;
      migrate-down-to) docker_migrate_down_to ;;
      check-data) docker_preflight; docker_data_guard ;;
    esac
  else
    case "$ACTION" in
      install) k8s_install ;;
      uninstall) k8s_uninstall ;;
      delete-uninstall) k8s_delete_uninstall ;;
      status) k8s_status ;;
      backup-now) k8s_backup_now ;;
      version) k8s_version ;;
      migrate-down-to) k8s_migrate_down_to ;;
      check-data) : ;;
    esac
  fi
}
main
