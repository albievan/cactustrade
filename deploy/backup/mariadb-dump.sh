# cactusTrade: dump the MariaDB database to stdout (PLAN 7.6b). Runs INSIDE the MariaDB
# container: deploy.sh passes this file as `sh -c "<this file>" dump DATABASE`, and
# `make test-int` runs it the same way. One consistent snapshot (--single-transaction), no
# CREATE DATABASE / USE, so the dump loads into any database name.
set -eu
db="${1:?database name}"
case "$db" in *[!A-Za-z0-9_]*) echo "error: bad database name" >&2; exit 2 ;; esac
MYSQL_PWD="$(cat "${DB_ROOT_PASSWORD_FILE:-/run/secrets/db_root_password}")"
export MYSQL_PWD
exec mariadb-dump -uroot --single-transaction --routines --triggers --hex-blob "$db"
