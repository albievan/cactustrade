# cactusTrade: restore a MariaDB dump read from stdin (PLAN 7.6b). Runs INSIDE the MariaDB
# container, with the app stopped: deploy.sh passes this file as
# `sh -c "<this file>" restore DATABASE NEWEST_SCHEMA`, and `make test-int` runs it the same
# way. Exit codes: 2 bad arguments, 3 not a cactusTrade dump or a newer schema, 4 a server
# still holds an account lease.
#
#  1. Wait (RESTORE_LEASE_WAIT seconds, default 45) until no account lease on DATABASE is
#     live: a process that holds one may still send orders (D22).
#  2. Load the dump into the scratch database DATABASE_restore and check its schema version:
#     at least 1 and at most NEWEST_SCHEMA (this build's newest migration). DATABASE is not
#     touched until here.
#  3. Swap in one atomic RENAME TABLE: DATABASE's tables move to DATABASE_prerestore (kept
#     until the next restore), the restored ones to DATABASE.
#  4. Ready it for the first start: no leases, no sessions (everyone signs in again), every
#     real-mode gate locked (a restore never brings an arming back, whatever the clocks say),
#     and the setting restore.at (a second check: a gate armed before it is disarmed, OD24).
#     The app's start-up then reconciles every account with eToro before anything else
#     (PLAN 4.14).
set -eu
db="${1:?database name}"
newest="${2:?newest schema version}"
case "$db" in *[!A-Za-z0-9_]*) echo "error: bad database name" >&2; exit 2 ;; esac
case "$newest" in '' | *[!0-9]*) echo "error: bad schema version" >&2; exit 2 ;; esac
wait_s="${RESTORE_LEASE_WAIT:-45}"
case "$wait_s" in '' | *[!0-9]*) echo "error: bad RESTORE_LEASE_WAIT" >&2; exit 2 ;; esac
MYSQL_PWD="$(cat "${DB_ROOT_PASSWORD_FILE:-/run/secrets/db_root_password}")"
export MYSQL_PWD
sql() { mariadb -uroot --batch --skip-column-names "$@"; }
tmp="${db}_restore"
prev="${db}_prerestore"

has_table() { [ "$(sql -e "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '$1' AND table_name = '$2'")" != 0 ]; }

# 1. no live lease
i=0
while has_table "$db" agent_leases; do
  live="$(sql -e "SELECT COUNT(*) FROM \`$db\`.agent_leases WHERE expires_at > FLOOR(UNIX_TIMESTAMP(NOW(6)) * 1000000)")"
  [ "$live" = 0 ] && break
  if [ "$i" -ge "$wait_s" ]; then
    echo "error: $live account lease(s) on $db are still live: a cactusTrade process is running; stop it first" >&2
    exit 4
  fi
  i=$((i + 1))
  sleep 1
done

# 2. load into the scratch database and check the schema
sql -e "DROP DATABASE IF EXISTS \`$tmp\`; CREATE DATABASE \`$tmp\`"
if ! sql "$tmp"; then
  sql -e "DROP DATABASE IF EXISTS \`$tmp\`" || true
  echo "error: the dump did not load (nothing was changed)" >&2
  exit 3
fi
version=0
if has_table "$tmp" goose_db_version; then
  version="$(sql -e "SELECT COALESCE(MAX(version_id), 0) FROM \`$tmp\`.goose_db_version")"
fi
if [ "$version" -lt 1 ] || [ "$version" -gt "$newest" ]; then
  sql -e "DROP DATABASE IF EXISTS \`$tmp\`"
  if [ "$version" -lt 1 ]; then
    echo "error: not a cactusTrade dump (no schema version; nothing was changed)" >&2
  else
    echo "error: the dump has schema $version, newer than this version's $newest: restore it with that cactusTrade version or newer (nothing was changed)" >&2
  fi
  exit 3
fi

# 3. one atomic swap
renames=""
for t in $(sql -e "SELECT table_name FROM information_schema.tables WHERE table_schema = '$db'"); do
  renames="${renames:+$renames, }\`$db\`.\`$t\` TO \`$prev\`.\`$t\`"
done
for t in $(sql -e "SELECT table_name FROM information_schema.tables WHERE table_schema = '$tmp'"); do
  renames="${renames:+$renames, }\`$tmp\`.\`$t\` TO \`$db\`.\`$t\`"
done
sql -e "CREATE DATABASE IF NOT EXISTS \`$db\`; DROP DATABASE IF EXISTS \`$prev\`; CREATE DATABASE \`$prev\`; RENAME TABLE $renames; DROP DATABASE \`$tmp\`"

# 4. ready for the first start
now_us="$(sql -e "SELECT FLOOR(UNIX_TIMESTAMP(NOW(6)) * 1000000)")"
at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
lock_gates=""
if has_table "$db" real_gates; then # schema 12 on (an older dump has no gate to lock)
  lock_gates="UPDATE real_gates SET state = 'locked', disarmed_at = $now_us, disarmed_by = '', disarm_reason = 'a backup was restored', version = version + 1, updated_at = $now_us WHERE state = 'armed';"
fi
sql "$db" -e "START TRANSACTION; DELETE FROM agent_leases; DELETE FROM sessions; $lock_gates DELETE FROM settings WHERE k = 'restore.at'; INSERT INTO settings (k, v, updated_at) VALUES ('restore.at', '$at', $now_us); COMMIT"
echo "restored schema $version into $db (the replaced tables are kept in $prev until the next restore)"
