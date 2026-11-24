//go:build integration

package sqlstore_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/testcontainers/testcontainers-go/modules/mariadb"

	"github.com/albievan/cactustrade/internal/adapters/sqlstore"
	"github.com/albievan/cactustrade/internal/adapters/sqlstore/storetest"
)

const itPassword = "test-only-password"

// startMariaDB starts a fresh MariaDB 11 (root has the same test password) and opens it.
func startMariaDB(t *testing.T) (*mariadb.MariaDBContainer, *sqlstore.Store) {
	t.Helper()
	ctx := context.Background()
	c, err := mariadb.Run(ctx, "mariadb:11",
		mariadb.WithDatabase("cactustrade"), mariadb.WithUsername("cactustrade"), mariadb.WithPassword(itPassword))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := c.MappedPort(ctx, "3306/tcp")
	if err != nil {
		t.Fatal(err)
	}
	octx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	s, err := sqlstore.OpenMariaDB(octx, sqlstore.MariaDBConfig{
		Host: host + ":" + port.Port(), Name: "cactustrade", User: "cactustrade", Password: itPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// deploy.sh's scripts read the root password from a file (a secret in production)
	if err := c.CopyToContainer(ctx, []byte(itPassword), "/tmp/db_root_password", 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mariadb-dump.sh", "mariadb-restore.sh"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "backup", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.CopyToContainer(ctx, b, "/tmp/"+name, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return c, s
}

// script runs one of deploy.sh's backup scripts in the container the way deploy.sh does
// (`sh -c "<script>" name args…`), with stdin and stdout redirected to files in the container.
func script(t *testing.T, c *mariadb.MariaDBContainer, name, stdin, stdout string, env string, args ...string) (int, string) {
	t.Helper()
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = "'" + a + "'"
	}
	cmd := fmt.Sprintf(`export DB_ROOT_PASSWORD_FILE=/tmp/db_root_password %s; sh -c "$(cat /tmp/%s)" %s %s`, env, name, strings.TrimSuffix(name, ".sh"), strings.Join(q, " "))
	if stdin != "" {
		cmd += " < " + stdin
	}
	if stdout != "" {
		cmd += " > " + stdout
	}
	code, r, err := c.Exec(context.Background(), []string{"sh", "-c", cmd})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(r)
	return code, string(out)
}

// tableNames lists the tables of the connection's database.
func tableNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return names
}

// counts returns the row count of every table in the store's database.
func counts(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	m := map[string]int64{}
	for _, n := range tableNames(t, db) {
		var c int64
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `"+n+"`").Scan(&c); err != nil {
			t.Fatal(err)
		}
		m[n] = c
	}
	return m
}

// TestMariaDBBackupRestore is PLAN 7.6b: deploy.sh's dump script → gzip → age → age →
// gunzip → deploy.sh's restore script into a fresh MariaDB container; the row counts and the
// goose version are equal, foreign keys still cascade, leases and sessions are gone and
// restore.at is set. A dump from a newer schema, a dump that is not ours and a live lease
// are refused without touching the database.
func TestMariaDBBackupRestore(t *testing.T) {
	ctx := context.Background()
	src, a := startMariaDB(t)
	if err := a.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	storetest.Run(t, a) // fills every table the suite knows
	now := time.Now()
	if _, err := a.DB().ExecContext(ctx, "INSERT INTO agent_leases (account_id, holder, expires_at) SELECT id, 'old', ? FROM accounts WHERE id NOT IN (SELECT account_id FROM agent_leases) LIMIT 1",
		now.Add(time.Hour).UnixMicro()); err != nil {
		t.Fatal(err)
	}
	before := counts(t, a.DB())
	verA, newest, err := a.SchemaVersions(ctx)
	if err != nil || verA != newest {
		t.Fatalf("source schema %d/%d %v", verA, newest, err)
	}

	if code, out := script(t, src, "mariadb-dump.sh", "", "/tmp/dump.sql", "", "cactustrade"); code != 0 {
		t.Fatalf("dump: %d %s", code, out)
	}
	rc, err := src.CopyFileFromContainer(ctx, "/tmp/dump.sql")
	if err != nil {
		t.Fatal(err)
	}
	dump, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Contains(dump, []byte("goose_db_version")) {
		t.Fatalf("dump %d bytes %v", len(dump), err)
	}

	// the backup file deploy.sh writes: gzip, then age
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	var file bytes.Buffer
	w, err := age.Encrypt(&file, id.Recipient())
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(w)
	if _, err := zw.Write(dump); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(file.Bytes(), []byte("goose_db_version")) {
		t.Fatal("the backup file is readable without the identity")
	}
	r, err := age.Decrypt(&file, id)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(plain, dump) {
		t.Fatalf("round trip through gzip and age: %v", err)
	}

	dst, b := startMariaDB(t)
	if err := dst.CopyToContainer(ctx, plain, "/tmp/dump.sql", 0o644); err != nil {
		t.Fatal(err)
	}
	newestArg := fmt.Sprint(newest)

	// refusals first: the empty target database stays empty
	if code, out := script(t, dst, "mariadb-restore.sh", "/tmp/dump.sql", "", "", "cactustrade", fmt.Sprint(newest-1)); code != 3 || !strings.Contains(out, "newer") {
		t.Fatalf("newer schema: %d %s", code, out)
	}
	if err := dst.CopyToContainer(ctx, []byte("CREATE TABLE other (x INT);\n"), "/tmp/other.sql", 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := script(t, dst, "mariadb-restore.sh", "/tmp/other.sql", "", "", "cactustrade", newestArg); code != 3 || !strings.Contains(out, "not a cactusTrade dump") {
		t.Fatalf("foreign dump: %d %s", code, out)
	}
	if code, out := script(t, dst, "mariadb-restore.sh", "/tmp/dump.sql", "", "", "cactus-trade", newestArg); code != 2 {
		t.Fatalf("bad name: %d %s", code, out)
	}
	if n := counts(t, b.DB()); len(n) != 0 {
		t.Fatalf("a refused restore changed the database: %v", n)
	}

	// the restore
	if code, out := script(t, dst, "mariadb-restore.sh", "/tmp/dump.sql", "", "", "cactustrade", newestArg); code != 0 || !strings.Contains(out, "restored schema") {
		t.Fatalf("restore: %d %s", code, out)
	}
	after := counts(t, b.DB())
	for tbl, n := range before {
		want := n
		switch tbl {
		case "sessions", "agent_leases":
			want = 0
		}
		if tbl == "settings" {
			var k int64
			if err := a.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM settings WHERE k = ?", sqlstore.RestoreAtSetting).Scan(&k); err != nil {
				t.Fatal(err)
			}
			want = n - k + 1
		}
		if after[tbl] != want {
			t.Errorf("table %s: %d rows after the restore, want %d (before %d)", tbl, after[tbl], want, n)
		}
	}
	if len(after) != len(before) {
		t.Errorf("tables after %v, before %v", after, before)
	}
	verB, _, err := b.SchemaVersions(ctx)
	if err != nil || verB != verA {
		t.Fatalf("goose version after the restore %d, want %d (%v)", verB, verA, err)
	}
	at, err := b.Settings().Get(ctx, sqlstore.RestoreAtSetting)
	if err != nil {
		t.Fatal(err)
	}
	if ts, err := time.Parse(time.RFC3339, at); err != nil || time.Since(ts) > 10*time.Minute {
		t.Fatalf("restore.at %q %v", at, err)
	}
	// foreign keys point at the restored tables and still cascade
	var foreign int
	if err := b.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.referential_constraints WHERE constraint_schema = 'cactustrade' AND unique_constraint_schema <> 'cactustrade'").Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	if foreign != 0 {
		t.Fatalf("%d foreign keys point outside the database", foreign)
	}
	var fks int
	if err := b.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.referential_constraints WHERE constraint_schema = 'cactustrade'").Scan(&fks); err != nil || fks == 0 {
		t.Fatalf("no foreign keys after the restore: %d %v", fks, err)
	}
	var acct string
	if err := b.DB().QueryRowContext(ctx, "SELECT a.id FROM accounts a WHERE EXISTS (SELECT 1 FROM orders o WHERE o.account_id = a.id) LIMIT 1").Scan(&acct); err != nil {
		t.Fatal(err)
	}
	if _, err := b.DB().ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", acct); err != nil {
		t.Fatal(err)
	}
	var orphans int
	if err := b.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM orders WHERE account_id = ?", acct).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("delete did not cascade: %d %v", orphans, err)
	}
	// the app keeps working on the restored database (migrations are a no-op)
	if err := b.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	// a live lease (a server still running) blocks the next restore; nothing changes
	if _, err := b.DB().ExecContext(ctx, "INSERT INTO agent_leases (account_id, holder, expires_at) SELECT id, 'running', ? FROM accounts LIMIT 1",
		time.Now().Add(time.Hour).UnixMicro()); err != nil {
		t.Fatal(err)
	}
	mid := counts(t, b.DB())
	if code, out := script(t, dst, "mariadb-restore.sh", "/tmp/dump.sql", "", "RESTORE_LEASE_WAIT=1", "cactustrade", newestArg); code != 4 || !strings.Contains(out, "lease") {
		t.Fatalf("restore with a live lease: %d %s", code, out)
	}
	if got := counts(t, b.DB()); fmt.Sprint(got) != fmt.Sprint(mid) {
		t.Fatalf("a refused restore changed the database: %v → %v", mid, got)
	}
	// once it expired the restore goes through and keeps the replaced tables aside
	if _, err := b.DB().ExecContext(ctx, "UPDATE agent_leases SET expires_at = 0"); err != nil {
		t.Fatal(err)
	}
	if code, out := script(t, dst, "mariadb-restore.sh", "/tmp/dump.sql", "", "", "cactustrade", newestArg); code != 0 || !strings.Contains(out, "cactustrade_prerestore") {
		t.Fatalf("second restore: %d %s", code, out)
	}
	if got := counts(t, b.DB()); got["accounts"] != before["accounts"] {
		t.Fatalf("accounts after the second restore %d, want %d", got["accounts"], before["accounts"])
	}
}
