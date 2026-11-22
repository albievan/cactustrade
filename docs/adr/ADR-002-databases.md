# ADR-002: SQLite standalone, MariaDB in containers

**Status:** accepted (2026-11-22). The owner replaced the brief's "embedded storage
everywhere" with MariaDB for container deployments.

**Context.** The standalone binary must need nothing else. Container deployments follow the
cactusInv pattern: a MariaDB 11 service, `mariadb-dump` backups, and familiar operations.

**Decision.** `modernc.org/sqlite` (pure Go, BSD-3) for standalone; `go-sql-driver/mysql`
(MPL-2.0) for MariaDB. Both sit behind the same repository ports, with one portable SQL subset
(see ARCHITECTURE.md). There are two `goose` migration folders with identical versions. One
repository test suite runs against both.

**Alternatives.** cgo SQLite (`mattn/go-sqlite3`) was rejected: no static cross-builds. An
ORM was rejected: it is a heavy dependency, and the SQL stays simple. sqlc with two engines
was rejected: it would double the generated code for a small schema.

**Consequences.** Every schema change is written twice and tested twice. `TestMigrationParity`
and `make test-int` catch drift.
