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
#   --backup-now         dump the database now into BACKUP_DIR: gzip, then age-encrypted to
#                        CACTUSTRADE_BACKUP_RECIPIENT (.sql.gz.age); without a recipient a
#                        plain .sql.gz (mode 600) with a warning. Either way it holds the
#                        credentials only encrypted under the master key, which is never in it
#   --backup-schedule WHEN
#                        back up on a schedule on THIS machine (macOS launchd, Linux cron):
#                        "daily HH:MM" (local time), "hourly", "every Nh" (N = 1-24) or "off".
#                        Saved in .env.local with --backup-dir / --backup-keep; each run is
#                        --backup-now, logged to BACKUP_DIR/backup-schedule.log
#   --backup-prune       delete the oldest backups in BACKUP_DIR beyond BACKUP_KEEP
#   --restore FILE       replace the database with a backup (.sql.gz.age, .sql.gz or .sql):
#                        checks the file first, asks for 'restore', takes a backup, stops the
#                        app, restores, starts the app (it reconciles with eToro first)
#   --uninstall          stop and remove cactusTrade, KEEPING the database, data and secrets
#   --delete-uninstall   remove cactusTrade AND DELETE all its data (asks for confirmation)
#   --render             k3s/k8s: print the manifest that --install would apply (nothing is
#                        built, pushed or applied; a dry run)
#
# TARGET
#   --dockerdesktop      Docker Desktop, or Docker Engine on a Linux host (docker compose)
#   --podman             Podman (podman compose, with docker-compose v2 or podman-compose)
#   --k3s                a k3s node (image imported straight into k3s; no registry needed)
#   --k8s                any Kubernetes cluster (image pushed to K8S_REGISTRY from .env)
#
# OPTIONS
#   --ssh-access user@host   deploy to a remote machine over SSH, e.g. root@192.0.2.10
#   --yes                    do not ask for confirmation (--delete-uninstall, --upgrade, --restore)
#   --identity FILE          --restore: the age identity (private key) of an encrypted backup;
#                            default CACTUSTRADE_BACKUP_IDENTITY_FILE
#   --backup-dir DIR         the backup folder (any folder on this machine; default BACKUP_DIR
#                            from .env, ./backups); saved by --backup-schedule
#   --backup-keep N          keep the newest N backups (0 = all); saved by --backup-schedule
#   --dry-run                --restore: only check that the file decrypts and is a cactusTrade
#                            dump this version can restore; nothing is stopped or changed
#   -h, --help               show this help
#
# Settings come from .env next to this script; .env.local beside it (not in git) overrides
# them for this machine. eToro DEMO keys: export ETORO_DEMO_PUB (Public API Key) and
# ETORO_DEMO_PK (User Key) before --install; they are copied into the secrets, never .env.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

ACTION="" TARGET="" SSH_HOST="" ASSUME_YES=0 UPGRADE_TO="" DOWN_TO="" RESTORE_FILE="" IDENTITY_FILE="" DRY_RUN=0
SCHEDULE="" OPT_BACKUP_DIR="" OPT_BACKUP_KEEP=""
ENV_LOCAL="${CACTUSTRADE_ENV_LOCAL:-$ROOT/.env.local}"
REMOTE_DIR="${CACTUSTRADE_REMOTE_DIR:-/opt/cactustrade}"

die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }
ok() { printf '\033[32m✔\033[0m %s\n' "$*"; }
usage() { sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'; exit "${1:-0}"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --install | --uninstall | --delete-uninstall | --status | --backup-now | --upgrade | --version | --check-data | --render)
      [[ -z "$ACTION" ]] || die "choose one action"; ACTION="${1#--}" ;;
    --to) UPGRADE_TO="${2:-}"; shift; [[ -n "$UPGRADE_TO" && "$UPGRADE_TO" != --* ]] || die "--to needs a tag, branch or commit" ;;
    --migrate-down-to)
      # Used by --upgrade to undo a failed upgrade's migrations (with the newer version).
      [[ -z "$ACTION" ]] || die "choose one action"; ACTION=migrate-down-to; DOWN_TO="${2:-}"; shift
      [[ "$DOWN_TO" =~ ^[0-9]+$ ]] || die "--migrate-down-to needs a schema version" ;;
    --dockerdesktop | --docker | --podman | --k3s | --k8s)
      [[ -z "$TARGET" ]] || die "choose one target"; TARGET="${1#--}"; [[ "$TARGET" == docker ]] && TARGET=dockerdesktop ;;
    --ssh-access) SSH_HOST="${2:-}"; shift; [[ -n "$SSH_HOST" && "$SSH_HOST" != --* ]] || die "--ssh-access needs user@host" ;;
    --restore)
      [[ -z "$ACTION" ]] || die "choose one action"; ACTION=restore; RESTORE_FILE="${2:-}"; shift
      [[ -n "$RESTORE_FILE" && "$RESTORE_FILE" != --* ]] || die "--restore needs a backup file" ;;
    --backup-schedule)
      [[ -z "$ACTION" ]] || die "choose one action"; ACTION=backup-schedule; SCHEDULE="${2:-}"; shift
      [[ -n "$SCHEDULE" && "$SCHEDULE" != --* ]] || die "--backup-schedule needs \"daily HH:MM\", \"hourly\", \"every Nh\" or \"off\"" ;;
    --backup-prune) [[ -z "$ACTION" ]] || die "choose one action"; ACTION=backup-prune ;;
    --backup-dir) OPT_BACKUP_DIR="${2:-}"; shift; [[ -n "$OPT_BACKUP_DIR" && "$OPT_BACKUP_DIR" != --* ]] || die "--backup-dir needs a folder" ;;
    --backup-keep) OPT_BACKUP_KEEP="${2:-}"; shift; [[ "$OPT_BACKUP_KEEP" =~ ^[0-9]+$ ]] || die "--backup-keep needs a number (0 = keep all)" ;;
    --identity) IDENTITY_FILE="${2:-}"; shift; [[ -n "$IDENTITY_FILE" && "$IDENTITY_FILE" != --* ]] || die "--identity needs a file" ;;
    --dry-run) DRY_RUN=1 ;;
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
[[ ( -z "$IDENTITY_FILE" && $DRY_RUN -eq 0 ) || "$ACTION" == restore ]] || die "--identity and --dry-run go with --restore"
[[ -z "$SSH_HOST" || ( "$ACTION" != backup-schedule && "$ACTION" != backup-prune ) ]] || die "--backup-schedule and --backup-prune run on the machine itself: run them there, without --ssh-access"
export TARGET

# ---------------------------------------------------------------------------------------
# .env — parsed line by line (KEY=value, optional quotes), never executed.
# ---------------------------------------------------------------------------------------
load_env() {
  [[ -f .env ]] || die ".env not found in $ROOT"
  [[ -f "$ENV_LOCAL" ]] && read_env_file "$ENV_LOCAL"
  read_env_file .env
  # --backup-dir / --backup-keep win over the files for this run
  [[ -z "$OPT_BACKUP_DIR" ]] || export BACKUP_DIR="$OPT_BACKUP_DIR"
  [[ -z "$OPT_BACKUP_KEEP" ]] || export BACKUP_KEEP="$OPT_BACKUP_KEEP"
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
  backup_name="$(printf '%s\n' "$backup_out" | grep -oE 'cactustrade-[A-Za-z0-9._-]*\.sql\.gz(\.age)?' | head -1 || true)"
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
  die "the upgrade failed and could not be rolled back completely; restore ${backup_name} with --restore (README, \"Backups and restore\")"
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
# Backups (PLAN 7.6b, OD29): mariadb-dump | gzip | age. The dump and restore scripts in
# deploy/backup run inside the MariaDB container (make test-int runs the same files).
# ---------------------------------------------------------------------------------------
BACKUP_RECIPIENTS=()

# backup_recipients reads the age public keys from CACTUSTRADE_BACKUP_RECIPIENT (spaces or
# commas between several) or, when that is empty, the file CACTUSTRADE_BACKUP_RECIPIENT_FILE.
# None: the backup is today's plain gzip, with a warning (OD29).
backup_recipients() {
  local r line
  BACKUP_RECIPIENTS=()
  for r in $(printf '%s' "${CACTUSTRADE_BACKUP_RECIPIENT:-}" | tr ',' ' '); do BACKUP_RECIPIENTS+=("$r"); done
  if [[ ${#BACKUP_RECIPIENTS[@]} -eq 0 && -n "${CACTUSTRADE_BACKUP_RECIPIENT_FILE:-}" ]]; then
    [[ -f "$CACTUSTRADE_BACKUP_RECIPIENT_FILE" ]] || die "CACTUSTRADE_BACKUP_RECIPIENT_FILE: no such file: $CACTUSTRADE_BACKUP_RECIPIENT_FILE"
    while IFS= read -r line || [[ -n "$line" ]]; do
      line="${line%%#*}"
      line="$(printf '%s' "$line" | tr -d '[:space:]')"
      [[ -z "$line" ]] || BACKUP_RECIPIENTS+=("$line")
    done <"$CACTUSTRADE_BACKUP_RECIPIENT_FILE"
  fi
  for r in ${BACKUP_RECIPIENTS[@]+"${BACKUP_RECIPIENTS[@]}"}; do
    [[ "$r" =~ ^age1[0-9a-z]+$ ]] || die "not an age public key (age1...): $r"
  done
  if [[ ${#BACKUP_RECIPIENTS[@]} -eq 0 ]]; then
    warn "no CACTUSTRADE_BACKUP_RECIPIENT: this backup is NOT encrypted (gzip, mode 600). Put an age public key in .env.local (README, \"Backups and restore\")."
  fi
}

backup_file() {
  local f
  f="$(printf '%s/cactustrade-%s.sql.gz' "$BACKUP_DIR" "$(date -u +%Y%m%dT%H%M%SZ)")"
  [[ ${#BACKUP_RECIPIENTS[@]} -eq 0 ]] || f="${f}.age"
  printf '%s' "$f"
}

backup_done() {
  if [[ "$1" == *.age ]]; then
    ok "backup $(basename "$1") in $(abs_dir "$BACKUP_DIR"), encrypted to ${#BACKUP_RECIPIENTS[@]} age recipient(s) (a restore needs the age identity and the same master_key)"
  else
    ok "backup $(basename "$1") in $(abs_dir "$BACKUP_DIR"), NOT encrypted (a restore needs the same master_key)"
  fi
}

# backup_prune deletes the oldest backups in BACKUP_DIR beyond BACKUP_KEEP (unset or 0 =
# keep all). Only cactusTrade's own backup files (cactustrade-<time>.sql.gz[.age]) are ever
# touched; their names sort by time.
backup_prune() {
  local keep="${BACKUP_KEEP:-0}" files=() f n i
  [[ "$keep" =~ ^[0-9]+$ ]] || die "BACKUP_KEEP must be a number (0 = keep all): $keep"
  [[ "$keep" -gt 0 && -d "$BACKUP_DIR" ]] || return 0
  while IFS= read -r f; do files+=("$f"); done < <(find "$BACKUP_DIR" -maxdepth 1 -type f \( -name 'cactustrade-*.sql.gz' -o -name 'cactustrade-*.sql.gz.age' \) | LC_ALL=C sort)
  n=${#files[@]}
  [[ $n -gt $keep ]] || return 0
  for ((i = 0; i < n - keep; i++)); do
    rm -f -- "${files[$i]}"
    info "removed the old backup $(basename "${files[$i]}") (keeping the newest $keep)"
  done
}

# set_env_local sets KEY=value in .env.local (replacing the key's line, else appending).
set_env_local() {
  local key="$1" val="$2" tmp
  touch "$ENV_LOCAL"
  tmp="$(mktemp "${ENV_LOCAL}.XXXXXX")"
  grep -v -E "^${key}=" "$ENV_LOCAL" >"$tmp" || true
  printf '%s="%s"\n' "$key" "$val" >>"$tmp"
  mv "$tmp" "$ENV_LOCAL"
}

# scheduler is the machine's scheduler: launchd on macOS, cron elsewhere
# (CACTUSTRADE_SCHEDULER overrides, for tests).
scheduler() {
  if [[ -n "${CACTUSTRADE_SCHEDULER:-}" ]]; then printf '%s' "$CACTUSTRADE_SCHEDULER"
  elif [[ "$(uname -s)" == Darwin ]]; then printf launchd
  else printf cron; fi
}

LAUNCHD_LABEL=com.cactustrade.backup
launchd_plist() { printf '%s/Library/LaunchAgents/%s.plist' "$HOME" "$LAUNCHD_LABEL"; }
CRON_MARK="# cactustrade-backup"

# backup_schedule installs (or with "off" removes) the scheduled --backup-now of this target.
backup_schedule() {
  local when="$SCHEDULE" hour="" minute="" every="" dir log
  case "$when" in
    off) ;;
    hourly) every=1 ;;
    every\ *h) every="${when#every }"; every="${every%h}"
      [[ "$every" =~ ^[0-9]+$ && "$every" -ge 1 && "$every" -le 24 ]] || die "every Nh: N must be 1-24" ;;
    daily\ *) [[ "$when" =~ ^daily\ ([0-9]{1,2}):([0-9]{2})$ ]] || die "daily HH:MM, e.g. \"daily 02:30\""
      hour=$((10#${BASH_REMATCH[1]})); minute=$((10#${BASH_REMATCH[2]}))
      [[ $hour -le 23 && $minute -le 59 ]] || die "daily HH:MM: not a time of day: ${when#daily }" ;;
    *) die "--backup-schedule: \"daily HH:MM\", \"hourly\", \"every Nh\" or \"off\" (not \"$when\")" ;;
  esac
  dir="$(abs_dir "$BACKUP_DIR")"
  if [[ "$when" != off ]]; then
    mkdir -p "$dir" || die "cannot create the backup folder $dir"
    [[ -w "$dir" ]] || die "the backup folder is not writable: $dir"
  fi
  log="$dir/backup-schedule.log"
  set_env_local BACKUP_DIR "$dir"
  [[ -z "${BACKUP_KEEP:-}" ]] || set_env_local BACKUP_KEEP "$BACKUP_KEEP"
  set_env_local BACKUP_SCHEDULE "$when"
  local path_dirs="/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin" d
  d="$(command -v "$DOCKER" 2>/dev/null || true)"
  [[ -z "$d" ]] || path_dirs="$(dirname "$d"):$path_dirs"
  case "$(scheduler)" in
    launchd)
      local plist uid; plist="$(launchd_plist)"; uid="$(id -u)"
      launchctl bootout "gui/$uid/$LAUNCHD_LABEL" >/dev/null 2>&1 || true
      rm -f "$plist"
      if [[ "$when" == off ]]; then ok "scheduled backups are off"; return; fi
      mkdir -p "$(dirname "$plist")"
      {
        printf '<?xml version="1.0" encoding="UTF-8"?>\n<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">\n<plist version="1.0"><dict>\n'
        printf '  <key>Label</key><string>%s</string>\n' "$LAUNCHD_LABEL"
        printf '  <key>ProgramArguments</key><array><string>/bin/bash</string><string>%s/deploy.sh</string><string>--backup-now</string><string>--%s</string></array>\n' "$ROOT" "$TARGET"
        printf '  <key>EnvironmentVariables</key><dict><key>PATH</key><string>%s</string><key>CACTUSTRADE_ENV_LOCAL</key><string>%s</string></dict>\n' "$path_dirs" "$ENV_LOCAL"
        if [[ -n "$every" ]]; then
          printf '  <key>StartInterval</key><integer>%d</integer>\n' $((every * 3600))
        else
          printf '  <key>StartCalendarInterval</key><dict><key>Hour</key><integer>%d</integer><key>Minute</key><integer>%d</integer></dict>\n' "$hour" "$minute"
        fi
        printf '  <key>StandardOutPath</key><string>%s</string>\n  <key>StandardErrorPath</key><string>%s</string>\n' "$log" "$log"
        printf '</dict></plist>\n'
      } >"$plist"
      launchctl bootstrap "gui/$uid" "$plist" || die "launchctl could not load $plist"
      ok "scheduled backups: $when (this Mac's time), into $dir$( [[ -n "${BACKUP_KEEP:-}" && "${BACKUP_KEEP}" != 0 ]] && printf ', keeping the newest %s' "$BACKUP_KEEP"); log $log"
      ;;
    cron)
      local line="" cur
      cur="$(crontab -l 2>/dev/null | grep -v -F "$CRON_MARK" || true)"
      if [[ "$when" != off ]]; then
        if [[ -n "$every" ]]; then line="0 */$every * * *"; else line="$minute $hour * * *"; fi
        line="$line PATH=$path_dirs CACTUSTRADE_ENV_LOCAL=$ENV_LOCAL /bin/bash $ROOT/deploy.sh --backup-now --$TARGET >>$log 2>&1 $CRON_MARK"
      fi
      printf '%s\n%s\n' "$cur" "$line" | sed '/^$/d' | crontab - || die "crontab could not be changed"
      if [[ "$when" == off ]]; then ok "scheduled backups are off"; else ok "scheduled backups: $when (this machine's time), into $dir; log $log"; fi
      ;;
  esac
}

# backup_schedule_status prints the schedule, folder, retention and newest backup.
backup_schedule_status() {
  local newest
  newest="$(find "$BACKUP_DIR" -maxdepth 1 -type f \( -name 'cactustrade-*.sql.gz' -o -name 'cactustrade-*.sql.gz.age' \) 2>/dev/null | LC_ALL=C sort | tail -1)"
  printf 'backups: schedule %s, folder %s, keep %s, newest %s\n' "${BACKUP_SCHEDULE:-off}" "$(abs_dir "$BACKUP_DIR")" \
    "$( [[ "${BACKUP_KEEP:-0}" == 0 ]] && printf all || printf '%s' "$BACKUP_KEEP")" "$( [[ -n "$newest" ]] && basename "$newest" || printf none)"
}

# cactustrade_local prints a cactustrade binary on this machine for encrypt / decrypt:
# CACTUSTRADE_BIN, else bin/cactustrade (make build); nothing when there is none.
cactustrade_local() {
  if [[ -n "${CACTUSTRADE_BIN:-}" ]]; then
    printf '%s' "$CACTUSTRADE_BIN"
  elif [[ -x "$ROOT/bin/cactustrade" ]]; then
    printf '%s' "$ROOT/bin/cactustrade"
  fi
}

# backup_encrypt filters stdin to stdout: age-encrypted to BACKUP_RECIPIENTS (by a local
# cactustrade, the age tool, or cactustrade in the target's image), unchanged without any.
backup_encrypt() {
  if [[ ${#BACKUP_RECIPIENTS[@]} -eq 0 ]]; then
    cat
    return
  fi
  local args=() ageargs=() r bin
  for r in "${BACKUP_RECIPIENTS[@]}"; do
    args+=(--recipient "$r")
    ageargs+=(-r "$r")
  done
  bin="$(cactustrade_local)"
  if [[ -n "$bin" ]]; then
    "$bin" encrypt "${args[@]}"
  elif command -v age >/dev/null; then
    age "${ageargs[@]}"
  elif [[ "$TARGET" == dockerdesktop || "$TARGET" == podman ]]; then
    compose run --rm --no-deps -T app encrypt "${args[@]}"
  else
    kc -n "$K8S_NAMESPACE" exec -i deploy/cactustrade -- /cactustrade encrypt "${args[@]}"
  fi
}

# backup_decrypt filters an age file on stdin to its plaintext with the identity file $1. In
# a container or pod the identity goes in over stdin, ahead of the file (decrypt --identity -),
# so the private key is never a file, an argument or an environment variable there.
backup_decrypt() {
  local id="$1" bin
  bin="$(cactustrade_local)"
  if [[ -n "$bin" ]]; then
    "$bin" decrypt --identity "$id"
  elif command -v age >/dev/null; then
    age -d -i "$id"
  elif [[ "$TARGET" == dockerdesktop || "$TARGET" == podman ]]; then
    { grep -v '^[[:space:]]*$' "$id"; echo; cat; } | compose run --rm --no-deps -T app decrypt --identity -
  else
    { grep -v '^[[:space:]]*$' "$id"; echo; cat; } | kc -n "$K8S_NAMESPACE" exec -i deploy/cactustrade -- /cactustrade decrypt --identity -
  fi
}

# schema_newest prints the newest MariaDB migration of this checkout (what it installs).
schema_newest() {
  local f n max=0
  for f in "$ROOT"/migrations/mariadb/[0-9]*_*.sql; do
    n="$(basename "$f")"
    n="${n%%_*}"
    n=$((10#$n))
    [[ $n -le $max ]] || max=$n
  done
  printf '%s' "$max"
}

# dump_schema prints the newest goose version recorded in a plain dump (empty: none).
dump_schema() {
  # one INSERT (extended, possibly over several lines) of (id,version_id,is_applied,tstamp) rows
  sed -n '/^INSERT INTO `goose_db_version` VALUES/,/;$/p' "$1" | grep -oE '\([0-9]+,[0-9]+,' | cut -d, -f2 | sort -n | tail -1 || true
}

# restore (--restore FILE, PLAN 7.6b): check the file (decrypt, decompress, schema), ask,
# take a backup, stop the app, load (deploy/backup/mariadb-restore.sh), start the app, whose
# start-up reconciles every account with eToro before any agent or exit loop (PLAN 4.14).
restore() {
  [[ -f "$RESTORE_FILE" ]] || die "no such backup file: $RESTORE_FILE"
  [[ -z "$SSH_HOST" ]] || die "--restore runs where the backup is: copy it (and its identity) to the host and run deploy.sh --restore there (nothing was changed)"
  local id="${IDENTITY_FILE:-${CACTUSTRADE_BACKUP_IDENTITY_FILE:-}}" newest version answer backup_out rc=0
  mkdir -p "$BACKUP_DIR"
  RESTORE_PLAIN="$(umask 077 && mktemp "$BACKUP_DIR/.restore-XXXXXX")"
  trap 'rm -f "$RESTORE_PLAIN"' EXIT
  info "checking $(basename "$RESTORE_FILE")"
  case "$RESTORE_FILE" in
    *.age)
      [[ -n "$id" && -f "$id" ]] || die "an encrypted backup needs its age identity: --identity FILE or CACTUSTRADE_BACKUP_IDENTITY_FILE (nothing was changed)"
      backup_decrypt "$id" <"$RESTORE_FILE" | gzip -dc >"$RESTORE_PLAIN" ||
        die "the backup does not decrypt with that identity, or it is damaged (nothing was changed)"
      ;;
    *.gz) gzip -dc <"$RESTORE_FILE" >"$RESTORE_PLAIN" || die "the backup is damaged (nothing was changed)" ;;
    *.sql) cat <"$RESTORE_FILE" >"$RESTORE_PLAIN" ;;
    *) die "a backup is .sql.gz.age, .sql.gz or .sql (nothing was changed)" ;;
  esac
  version="$(dump_schema "$RESTORE_PLAIN")"
  newest="$(schema_newest)"
  [[ -n "$version" ]] || die "not a cactusTrade dump: it has no schema version (nothing was changed)"
  [[ "$version" -le "$newest" ]] ||
    die "the backup has schema $version, newer than this version's $newest: restore it with that cactusTrade version or newer (nothing was changed)"
  ok "$(basename "$RESTORE_FILE"): a cactusTrade dump with schema ${version} (this version: ${newest}), $(wc -c <"$RESTORE_PLAIN" | tr -d ' ') bytes"
  if [[ $DRY_RUN -eq 1 ]]; then
    echo "dry run: --restore would now ask for 'restore', take a backup of the current database, stop the app,"
    echo "load this dump into ${MARIADB_DATABASE} (the replaced tables kept in ${MARIADB_DATABASE}_prerestore), sign"
    echo "everyone out, and start the app, which reconciles every account with eToro first."
    echo "Nothing was stopped or changed."
    return 0
  fi
  if [[ $ASSUME_YES -ne 1 ]]; then
    [[ -t 0 ]] || die "re-run with --yes to restore non-interactively (nothing was changed)"
    read -r -p "This REPLACES the ${MARIADB_DATABASE} database (a backup is taken first). Type 'restore' to continue: " answer
    [[ "$answer" == restore ]] || die "aborted; nothing was changed"
  fi
  info "taking a backup of the current database first"
  backup_out="$(self --backup-now </dev/null 2>&1 | tr -d '\r')" || {
    echo "$backup_out"
    die "the backup failed, so nothing was changed"
  }
  printf '%s\n' "$backup_out"
  info "stopping the app"
  case "$TARGET" in dockerdesktop | podman) docker_stop_app ;; *) k8s_stop_app ;; esac
  info "restoring"
  case "$TARGET" in
    dockerdesktop | podman) docker_restore_load <"$RESTORE_PLAIN" || rc=$? ;;
    *) k8s_restore_load <"$RESTORE_PLAIN" || rc=$? ;;
  esac
  [[ $rc -eq 0 ]] || warn "the restore failed (exit $rc) and left the database as it was; starting the app again"
  info "starting the app (it reconciles every account with eToro before any agent or exit loop)"
  case "$TARGET" in
    dockerdesktop | podman) docker_start_app || die "the app did not become healthy after the restore" ;;
    *) k8s_start_app ;;
  esac
  [[ $rc -eq 0 ]] || die "nothing was restored"
  ok "restored $(basename "$RESTORE_FILE") (schema ${version}); everyone signs in again"
}

# ---------------------------------------------------------------------------------------
# Docker / Podman
# ---------------------------------------------------------------------------------------
compose() { "$DOCKER" compose --project-directory "$ROOT" -f "$ROOT/docker-compose.yml" "$@"; }

abs_dir() { case "$1" in /*) printf '%s' "${1%/}" ;; *) printf '%s/%s' "$ROOT" "${1#./}" ;; esac; }

NEW_INSTALL=0
FIRST_RUN_NOTE="    first run:     sign in as admin / admin; you must choose a new password straight away"

# lan_address: the address other machines on the network use for this host (best effort).
lan_address() {
  if [[ -n "${SSH_HOST:-}" ]]; then printf '%s' "${SSH_HOST#*@}"; return; fi
  local a=""
  if command -v ipconfig >/dev/null 2>&1; then a="$(ipconfig getifaddr en0 2>/dev/null || ipconfig getifaddr en1 2>/dev/null || true)"; fi
  if [[ -z "$a" ]] && command -v hostname >/dev/null 2>&1; then a="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"; fi
  printf '%s' "${a:-localhost}"
}

ensure_secrets_dir() {
  mkdir -p "$SECRETS_DIR"
  chmod 700 "$SECRETS_DIR"
  local s
  for s in db_password db_root_password metrics_token; do
    [[ -s "$SECRETS_DIR/$s" ]] || { random_secret >"$SECRETS_DIR/$s"; ok "generated $SECRETS_DIR/$s"; }
  done
  [[ -f "$SECRETS_DIR/smtp_password" ]] || : >"$SECRETS_DIR/smtp_password" # the fallback mail server's (empty = none)
  if [[ ! -s "$SECRETS_DIR/master_key" ]]; then
    NEW_INSTALL=1
    random_key >"$SECRETS_DIR/master_key"
    ok "generated $SECRETS_DIR/master_key (encrypts the eToro and LLM keys; BACK IT UP off this machine)"
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
  export CACTUSTRADE_PUBLIC_URL="${CACTUSTRADE_PUBLIC_URL:-https://$(lan_address):${HTTPS_PORT}}"
  info "starting cactusTrade (MariaDB + app${COMPOSE_PROFILES:+ + $COMPOSE_PROFILES})"
  compose up -d --remove-orphans
  info "waiting for the app to become healthy"
  local id state
  for _ in $(seq 1 90); do
    id="$(compose ps -q app 2>/dev/null || true)"
    state="$("$DOCKER" inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$id" 2>/dev/null || true)"
    if [[ "$state" == healthy ]]; then
      ok "cactusTrade is running at https://$(lan_address):${HTTPS_PORT} (also https://localhost:${HTTPS_PORT})"
      echo "    certificate:   self-signed unless you set your own; accept the browser warning once"
      [[ ",${COMPOSE_PROFILES:-}," == *",mailpit,"* ]] && echo "    test mailbox:  http://localhost:${MAILPIT_PORT} (every e-mail the app sends, until a real server is set under E-mail)"
      [[ $NEW_INSTALL -eq 1 ]] && echo "$FIRST_RUN_NOTE"
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
      "$DOCKER" run --rm -v "$(cd "$d" && pwd):/target" busybox:1.37@sha256:a7dbc319b1438115a207e11930dc4bb3e643dcb3ec0bdd694bb36cd8fad289ec sh -c 'rm -rf /target/* /target/.[!.]* 2>/dev/null; true'
      rmdir "$d" 2>/dev/null || true
    fi
  done
  rm -rf "$SECRETS_DIR"
  ok "cactusTrade and all of its data were removed"
}

docker_status() { docker_preflight; compose ps; backup_schedule_status; }

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

docker_backup_now() {
  docker_preflight
  backup_recipients
  mkdir -p "$BACKUP_DIR"
  local f; f="$(backup_file)"
  (umask 077 && compose exec -T mariadb sh -c "$(cat "$ROOT/deploy/backup/mariadb-dump.sh")" dump "$MARIADB_DATABASE" </dev/null |
    gzip | backup_encrypt >"$f") || { rm -f "$f"; die "the backup failed"; }
  backup_done "$f"
  backup_prune
}

docker_stop_app() { compose stop app </dev/null; }

docker_restore_load() {
  compose exec -T mariadb sh -c "$(cat "$ROOT/deploy/backup/mariadb-restore.sh")" restore "$MARIADB_DATABASE" "$(schema_newest)"
}

docker_start_app() {
  compose up -d app </dev/null
  local id state
  for _ in $(seq 1 90); do
    id="$(compose ps -q app 2>/dev/null || true)"
    state="$("$DOCKER" inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$id" 2>/dev/null || true)"
    [[ "$state" == healthy ]] && return 0
    sleep 2
  done
  compose logs --tail 50 app
  return 1
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

# render substitutes ${VAR} in the manifest template from the environment. Lines marked
# "# https" are kept only with K8S_HTTPS=true (the app's Service is ClusterIP: reachable only
# through the ingress), lines marked "# plain-http" only without it (PLAN 7.1.6).
render() {
  local tpl="$ROOT/deploy/k8s/cactustrade.yaml" drop=' # https$'
  [[ "${K8S_HTTPS:-false}" == true ]] && drop=' # plain-http$'
  if [[ -z "${K8S_STORAGE_CLASS:-}" ]]; then
    grep -v 'storageClassName: \${K8S_STORAGE_CLASS}' "$tpl"
  else
    cat "$tpl"
  fi | grep -Ev -- "$drop" | sed -E 's/ # (https|plain-http)$//' |
    perl -pe 's/\$\{([A-Za-z_][A-Za-z0-9_]*)\}/exists $ENV{$1} ? $ENV{$1} : die "unset variable $1\n"/ge'
}

# plain_http_warning says, loudly, that K8S_HTTPS=false publishes plain HTTP on every node.
plain_http_warning() {
  warn "K8S_HTTPS is not true: cactusTrade is published as PLAIN HTTP on port ${K8S_NODE_PORT} of EVERY node."
  warn "Passwords, session cookies and API keys cross the network UNENCRYPTED. Use this only on a trusted"
  warn "lab network; set K8S_HTTPS=true (the default) to serve HTTPS through the ingress controller instead."
}

# k8s_env exports the settings the manifest needs beyond .env (shared by --install and --render).
k8s_env() {
  # no Mailpit in the cluster: without a real fallback server mail waits for Setup → E-mail
  [[ "${CACTUSTRADE_SMTP_HOST:-}" == mailpit ]] && export CACTUSTRADE_SMTP_HOST=""
  if [[ -z "${CACTUSTRADE_PUBLIC_URL:-}" && "${K8S_HTTPS:-false}" == true && -n "${K8S_HTTPS_HOST:-}" ]]; then
    export CACTUSTRADE_PUBLIC_URL="https://${K8S_HTTPS_HOST}"
  fi
  export CACTUSTRADE_PUBLIC_URL="${CACTUSTRADE_PUBLIC_URL:-}" CACTUSTRADE_SMTP_HOST="${CACTUSTRADE_SMTP_HOST:-}"
  # Network (PLAN 7.1.16): who reaches the app (the ingress controller's pods) and who may set
  # X-Forwarded-For (their addresses; empty = nobody). Validated: they go into the manifest.
  local ns="${K8S_INGRESS_NAMESPACE:-kube-system}" sel="${K8S_INGRESS_POD_SELECTOR:-app.kubernetes.io/name=traefik}"
  local proxies="${K8S_TRUSTED_PROXIES:-}"
  [[ "$ns" =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] || die "K8S_INGRESS_NAMESPACE must be a namespace name (got '$ns')"
  [[ "$sel" =~ ^([A-Za-z0-9./_-]+)=([A-Za-z0-9._-]+)$ ]] ||
    die "K8S_INGRESS_POD_SELECTOR must be one label as key=value, such as app.kubernetes.io/name=traefik (got '$sel')"
  export K8S_INGRESS_NAMESPACE="$ns" K8S_INGRESS_POD_LABEL="${BASH_REMATCH[1]}: ${BASH_REMATCH[2]}"
  [[ "$proxies" =~ ^[0-9A-Fa-f:.,/\ ]*$ ]] || die "K8S_TRUSTED_PROXIES must be addresses or CIDRs separated by commas (got '$proxies')"
  export K8S_TRUSTED_PROXIES="$proxies"
  if [[ "${K8S_HTTPS:-false}" == true && -z "$proxies" ]]; then
    warn "K8S_TRUSTED_PROXIES is empty: X-Forwarded-For is ignored, so every client counts as the ingress"
    warn "controller's address for the sign-in and API rate limits. Set it to the ingress controller's pod"
    warn "addresses (docs/RUNBOOK.md, \"Trusted proxies on k3s/k8s\")."
  fi
}

# k8s_render prints the manifest --install would apply, touching no cluster.
k8s_render() {
  export IMAGE="${IMAGE:-$CACTUSTRADE_IMAGE}" IMAGE_PULL_POLICY="${IMAGE_PULL_POLICY:-IfNotPresent}"
  k8s_env
  [[ "${K8S_HTTPS:-false}" == true ]] || plain_http_warning
  render
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
  metrics_token: "$(random_secret)"
  etoro_demo_pub: ""
  etoro_demo_pk: ""
  smtp_password: ""
YAML
    NEW_INSTALL=1
    ok "generated the passwords and the master key in secret cactustrade-secrets"
  fi
  local s v
  for s in etoro_demo_pub etoro_demo_pk; do
    v="$(etoro_value "$s")"
    if [[ -n "$v" ]]; then secret_patch "$s" "$v"; ok "eToro demo key $s taken from the environment"
    elif ! kc -n "$K8S_NAMESPACE" get secret cactustrade-secrets -o "jsonpath={.data}" </dev/null | grep -q "\"$s\""; then secret_patch "$s" ""; fi
  done
  # installs from before e-mail support have no smtp_password key yet
  kc -n "$K8S_NAMESPACE" get secret cactustrade-secrets -o "jsonpath={.data}" </dev/null | grep -q '"smtp_password"' || secret_patch smtp_password ""
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
  k8s_env
  if [[ "${K8S_HTTPS:-false}" == true ]]; then
    # an install from before PLAN 7.1.6 has a NodePort Service: replace it, never keep it
    if [[ "$(kc -n "$K8S_NAMESPACE" get svc cactustrade -o 'jsonpath={.spec.type}' </dev/null 2>/dev/null || true)" == NodePort ]]; then
      info "replacing the NodePort Service with a ClusterIP one (HTTPS only through the ingress)"
      kc -n "$K8S_NAMESPACE" delete svc cactustrade </dev/null >/dev/null
    fi
  else
    plain_http_warning
  fi
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
    plain_http_warning
  fi
  [[ $NEW_INSTALL -eq 1 ]] && echo "$FIRST_RUN_NOTE"
  echo "    master_key:    copy it OFF the cluster: kubectl -n ${K8S_NAMESPACE} get secret cactustrade-secrets -o jsonpath='{.data.master_key}' | base64 -d"
}

k8s_uninstall() {
  info "removing cactusTrade workloads (volumes and secrets are kept)"
  kc -n "$K8S_NAMESPACE" delete deploy,svc,ingress,configmap,networkpolicy -l app.kubernetes.io/part-of=cactustrade --ignore-not-found
  ok "uninstalled; persistent volumes and secrets kept in namespace ${K8S_NAMESPACE}"
}

k8s_delete_uninstall() {
  confirm "This deletes namespace ${K8S_NAMESPACE} with the database, data and secrets (incl. the master key)."
  kc delete namespace "$K8S_NAMESPACE" --ignore-not-found --wait=true
  ok "cactusTrade and all of its data were removed"
}

k8s_status() { kc -n "$K8S_NAMESPACE" get deploy,pods,svc,pvc,ingress,networkpolicy; backup_schedule_status; }

k8s_version() {
  kc -n "$K8S_NAMESPACE" exec deploy/cactustrade -- /cactustrade version </dev/null || die "cactusTrade is not running in namespace ${K8S_NAMESPACE}"
  kc -n "$K8S_NAMESPACE" exec deploy/cactustrade -- /cactustrade migrate status </dev/null
}

k8s_migrate_down_to() { kc -n "$K8S_NAMESPACE" exec deploy/cactustrade -- /cactustrade migrate down-to "$DOWN_TO" </dev/null; }

k8s_backup_now() {
  backup_recipients
  mkdir -p "$BACKUP_DIR"
  local f; f="$(backup_file)"
  (umask 077 && kc -n "$K8S_NAMESPACE" exec -i deploy/mariadb -- sh -c "$(cat "$ROOT/deploy/backup/mariadb-dump.sh")" dump "$MARIADB_DATABASE" </dev/null |
    gzip | backup_encrypt >"$f") || { rm -f "$f"; die "the backup failed"; }
  backup_done "$f"
  backup_prune
}

k8s_stop_app() {
  kc -n "$K8S_NAMESPACE" scale deploy/cactustrade --replicas=0 </dev/null >/dev/null
  kc -n "$K8S_NAMESPACE" wait --for=delete pod -l app.kubernetes.io/name=cactustrade --timeout=120s </dev/null >/dev/null 2>&1 || true
}

k8s_restore_load() {
  kc -n "$K8S_NAMESPACE" exec -i deploy/mariadb -- sh -c "$(cat "$ROOT/deploy/backup/mariadb-restore.sh")" restore "$MARIADB_DATABASE" "$(schema_newest)"
}

k8s_start_app() {
  kc -n "$K8S_NAMESPACE" scale deploy/cactustrade --replicas=1 </dev/null >/dev/null
  rollout cactustrade
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
  case "$ACTION" in
    backup-schedule) backup_schedule; return ;;
    backup-prune) backup_prune; return ;;
  esac
  if [[ "$TARGET" == dockerdesktop || "$TARGET" == podman ]]; then
    if [[ -n "$SSH_HOST" && "$ACTION" != restore ]]; then remote_docker; return; fi
    case "$ACTION" in
      install) docker_install ;;
      uninstall) docker_uninstall ;;
      delete-uninstall) docker_delete_uninstall ;;
      status) docker_status ;;
      backup-now) docker_backup_now ;;
      restore) [[ $DRY_RUN -eq 1 ]] || docker_preflight; restore ;;
      version) docker_version ;;
      migrate-down-to) docker_migrate_down_to ;;
      check-data) docker_preflight; docker_data_guard ;;
      render) die "--render is for --k3s and --k8s" ;;
    esac
  else
    case "$ACTION" in
      render) k8s_render ;;
      install) k8s_install ;;
      uninstall) k8s_uninstall ;;
      delete-uninstall) k8s_delete_uninstall ;;
      status) k8s_status ;;
      backup-now) k8s_backup_now ;;
      restore) restore ;;
      version) k8s_version ;;
      migrate-down-to) k8s_migrate_down_to ;;
      check-data) : ;;
    esac
  fi
}
main
