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
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/testcontainers/testcontainers-go/modules/mariadb"

	"github.com/albievan/cactustrade/internal/adapters/sqlstore"
	"github.com/albievan/cactustrade/internal/adapters/sqlstore/storetest"
	"github.com/albievan/cactustrade/internal/domain"
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
	// A gate stored armed an hour in the future (a skewed clock): the restore must lock it,
	// whatever restore.at says (review minor 2).
	var gated string
	if err := a.DB().QueryRowContext(ctx, "SELECT id FROM accounts WHERE id NOT IN (SELECT account_id FROM real_gates) LIMIT 1").Scan(&gated); err != nil {
		t.Fatal(err)
	}
	ev := domain.GateEvidence{Days: []domain.RiskDate{"2026-11-20"}, Count: 1, First: "2026-11-20", Last: "2026-11-20"}
	if _, err := a.RealGates().Put(ctx, domain.LockedGate(gated).Arm("demo-x", "u-x", ev, now.Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
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
	var stillArmed int
	if err := b.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM real_gates WHERE state = 'armed'").Scan(&stillArmed); err != nil || stillArmed != 0 {
		t.Fatalf("%d gates still armed after the restore (%v)", stillArmed, err)
	}
	if g, err := b.RealGates().Get(ctx, gated); err != nil || g.State != domain.GateLocked || g.DisarmReason != domain.DisarmRestored || g.DisarmedAt.IsZero() || g.Version < 2 {
		t.Fatalf("gate after the restore: %+v %v", g, err)
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

// sealAndOpen is the in-app backup file's round trip: gzip → age → decrypt → gunzip.
func sealAndOpen(t *testing.T, plain []byte) []byte {
	t.Helper()
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
	if _, err := zw.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := age.Decrypt(&file, id)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(out, plain) {
		t.Fatalf("round trip through gzip and age: %v", err)
	}
	return out
}

// checksums returns CHECKSUM TABLE of every table but the skipped ones, by table name.
func checksums(t *testing.T, db *sql.DB, skip ...string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for _, n := range tableNames(t, db) {
		if slices.Contains(skip, n) {
			continue
		}
		var name string
		var sum sql.NullString
		if err := db.QueryRowContext(context.Background(), "CHECKSUM TABLE `"+n+"`").Scan(&name, &sum); err != nil {
			t.Fatal(err)
		}
		m[n] = sum.String
	}
	return m
}

// orphans counts, for every foreign key of the database, the child rows whose parent is
// missing (a dump that is not one instant would leave some, its load does not check them).
func orphans(t *testing.T, db *sql.DB) map[string]int {
	t.Helper()
	ctx := context.Background()
	type fk struct{ t, c, rt, rc string }
	fks := func() []fk {
		rows, err := db.QueryContext(ctx, "SELECT table_name, column_name, referenced_table_name, referenced_column_name FROM information_schema.key_column_usage "+
			"WHERE table_schema = DATABASE() AND referenced_table_name IS NOT NULL")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []fk
		for rows.Next() {
			var f fk
			if err := rows.Scan(&f.t, &f.c, &f.rt, &f.rc); err != nil {
				t.Fatal(err)
			}
			out = append(out, f)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}()
	if len(fks) == 0 {
		t.Fatal("no foreign keys")
	}
	out := map[string]int{}
	for _, f := range fks {
		var n int
		q := fmt.Sprintf("SELECT COUNT(*) FROM `%s` c WHERE c.`%s` IS NOT NULL AND NOT EXISTS (SELECT 1 FROM `%s` p WHERE p.`%s` = c.`%s`)", f.t, f.c, f.rt, f.rc, f.c)
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			out[f.t+"."+f.c] = n
		}
	}
	return out
}

// restoreInto loads a plain dump into the container with deploy.sh's restore script.
func restoreInto(t *testing.T, c *mariadb.MariaDBContainer, plain []byte, newest int64) {
	t.Helper()
	if err := c.CopyToContainer(context.Background(), plain, "/tmp/godump.sql", 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out := script(t, c, "mariadb-restore.sh", "/tmp/godump.sql", "", "", "cactustrade", fmt.Sprint(newest)); code != 0 || !strings.Contains(out, "restored schema") {
		t.Fatalf("restore of the Go dump: %d %s", code, out)
	}
}

// TestMariaDBGoDumpRestore is R48.3: the app's own dump (DumpMariaDB) → gzip → age → decrypt
// → gunzip → deploy.sh's restore script into a fresh MariaDB. Row counts, CHECKSUM TABLE of
// every table (but those the restore resets) and the goose version are equal, deploy.sh's
// dump_schema reads the version, a row with every awkward value and a row near
// max_allowed_packet come back byte for byte, and a dump taken while a writer inserts loads
// with every foreign key intact.
func TestMariaDBGoDumpRestore(t *testing.T) {
	ctx := context.Background()
	_, a := startMariaDB(t)
	if err := a.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	storetest.Run(t, a)
	db := a.DB()

	// Every awkward value: text with every character that needs escaping, a blob with every
	// byte, an empty blob, an empty text, NULLs, the extreme BIGINTs.
	every := make([]byte, 256)
	for i := range every {
		every[i] = byte(i)
	}
	awkwardText := "quote ' double \" backslash \\ NUL \x00 newline \n cr \r ctrl-Z \x1a tab \t UTF-8 Zürich 東京 🌵 '); DROP TABLE users; --"
	if _, err := db.ExecContext(ctx, "INSERT INTO llm_calls (id, purpose, prompt, response, input_tokens, output_tokens, at) VALUES "+
		"('awkward', 'test', ?, '', -9223372036854775808, 9223372036854775807, 0)", awkwardText); err != nil {
		t.Fatal(err)
	}
	var mailID string
	if err := db.QueryRowContext(ctx, "SELECT id FROM outbox LIMIT 1").Scan(&mailID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO outbox_attachments (mail_id, seq, filename, content_type, data) VALUES (?, 997, ?, 'x', ?), (?, 998, '', '', ?)",
		mailID, awkwardText[:90], every, mailID, []byte{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO api_keys (id, owner_id, name, role, secret_hash, created_at, last_used_at, expires_at) "+
		"SELECT 'k-nulls', id, 'nulls', 'viewer', REPEAT('0', 64), 1, NULL, NULL FROM users LIMIT 1"); err != nil {
		t.Fatal(err)
	}

	// A row near max_allowed_packet: an attachment of the packet size less 1 MiB.
	var maxPacket int64
	if err := db.QueryRowContext(ctx, "SELECT @@max_allowed_packet").Scan(&maxPacket); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, maxPacket-(1<<20))
	for i := range big {
		big[i] = byte(i*7 + i/251)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO outbox_attachments (mail_id, seq, filename, content_type, data) VALUES (?, 999, 'big.pdf', 'application/pdf', ?)", mailID, big); err != nil {
		t.Fatal(err)
	}
	bigText := strings.Repeat("ü'\\\n", 1<<20) // 5 MiB of characters that need escaping
	if _, err := db.ExecContext(ctx, "INSERT INTO llm_calls (id, purpose, prompt, response, at) VALUES ('big-call', 'test', ?, '', 1)", bigText); err != nil {
		t.Fatal(err)
	}

	// The restore locks armed gates; lock them here so real_gates compares equal.
	if _, err := db.ExecContext(ctx, "UPDATE real_gates SET state = 'locked' WHERE state = 'armed'"); err != nil {
		t.Fatal(err)
	}
	before := counts(t, db)
	verA, newest, err := a.SchemaVersions(ctx)
	if err != nil || verA != newest {
		t.Fatalf("source schema %d/%d %v", verA, newest, err)
	}
	skip := []string{"sessions", "agent_leases", "settings"}
	sumsA := checksums(t, db, skip...)

	var dump bytes.Buffer
	if err := a.DumpMariaDB(ctx, &dump); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(dump.Bytes(), []byte("CREATE DATABASE")) || bytes.Contains(dump.Bytes(), []byte("\nUSE ")) {
		t.Fatal("the dump names a database")
	}
	for line := range strings.SplitSeq(dump.String(), "\n") {
		if int64(len(line)) > maxPacket/4 {
			t.Fatalf("a dump line of %d bytes (max_allowed_packet %d)", len(line), maxPacket)
		}
	}
	plain := sealAndOpen(t, dump.Bytes())

	// deploy.sh's dump_schema reads the version from the Go dump
	dumpFile := filepath.Join(t.TempDir(), "dump.sql")
	if err := os.WriteFile(dumpFile, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	deploySh := filepath.Join("..", "..", "..", "deploy.sh")
	cmd := exec.CommandContext(ctx, "bash", "-c", `eval "$(sed -n '/^dump_schema() {/,/^}/p' "$1")"; dump_schema "$2"`, "x", deploySh, dumpFile)
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) != fmt.Sprint(verA) {
		t.Fatalf("deploy.sh dump_schema read %q (%v), want %d", out, err, verA)
	}

	dst, b := startMariaDB(t)
	restoreInto(t, dst, plain, newest)
	after := counts(t, b.DB())
	for tbl, n := range before {
		want := n
		switch tbl {
		case "sessions", "agent_leases":
			want = 0
		case "settings":
			var k int64
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM settings WHERE k = ?", sqlstore.RestoreAtSetting).Scan(&k); err != nil {
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
	sumsB := checksums(t, b.DB(), skip...)
	for tbl, s := range sumsA {
		if sumsB[tbl] != s {
			t.Errorf("CHECKSUM TABLE %s: %s after the restore, %s before", tbl, sumsB[tbl], s)
		}
	}
	verB, _, err := b.SchemaVersions(ctx)
	if err != nil || verB != verA {
		t.Fatalf("goose version after the restore %d, want %d (%v)", verB, verA, err)
	}
	var gotText string
	var gotBlob, gotBig []byte
	if err := b.DB().QueryRowContext(ctx, "SELECT c.prompt, a.data FROM llm_calls c, outbox_attachments a WHERE c.id = 'awkward' AND a.seq = 997").Scan(&gotText, &gotBlob); err != nil || gotText != awkwardText || !bytes.Equal(gotBlob, every) {
		t.Fatalf("awkward row: %q %x %v", gotText, gotBlob, err)
	}
	if err := b.DB().QueryRowContext(ctx, "SELECT data FROM outbox_attachments WHERE seq = 999").Scan(&gotBig); err != nil || !bytes.Equal(gotBig, big) {
		t.Fatalf("the row near max_allowed_packet: %d bytes, want %d (%v)", len(gotBig), len(big), err)
	}
	if err := b.DB().QueryRowContext(ctx, "SELECT prompt FROM llm_calls WHERE id = 'big-call'").Scan(&gotText); err != nil || gotText != bigText {
		t.Fatalf("the large text: %d bytes, want %d (%v)", len(gotText), len(bigText), err)
	}
	var nulls int
	if err := b.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM api_keys WHERE id = 'k-nulls' AND last_used_at IS NULL AND expires_at IS NULL").Scan(&nulls); err != nil || nulls != 1 {
		t.Fatalf("NULLs after the restore: %d %v", nulls, err)
	}
	if err := b.Migrate(ctx); err != nil { // the app keeps working on it
		t.Fatal(err)
	}

	// A dump while a writer inserts mails with attachments (parent and child in one
	// transaction): every foreign key is intact after loading it.
	wctx, stop := context.WithCancel(ctx)
	done := make(chan int)
	go func() {
		n := 0
		defer func() { done <- n }()
		for wctx.Err() == nil {
			tx, err := db.BeginTx(wctx, nil)
			if err != nil {
				return
			}
			id := fmt.Sprintf("w-%d", n)
			if _, err := tx.ExecContext(wctx, "INSERT INTO outbox (id, to_addr, subject, body, created_at, next_at) VALUES (?, 'a@example.com', 's', 'b', 1, 1)", id); err != nil {
				_ = tx.Rollback()
				return
			}
			if _, err := tx.ExecContext(wctx, "INSERT INTO outbox_attachments (mail_id, seq, filename, content_type, data) VALUES (?, 0, 'x.pdf', 'application/pdf', ?)", id, every); err != nil {
				_ = tx.Rollback()
				return
			}
			if tx.Commit() == nil {
				n++
			}
		}
	}()
	time.Sleep(300 * time.Millisecond)
	var live bytes.Buffer
	derr := a.DumpMariaDB(ctx, &live)
	time.Sleep(200 * time.Millisecond)
	stop()
	written := <-done
	if derr != nil {
		t.Fatal(derr)
	}
	if written == 0 {
		t.Fatal("the writer wrote nothing")
	}
	restoreInto(t, dst, sealAndOpen(t, live.Bytes()), newest)
	if o := orphans(t, b.DB()); len(o) != 0 {
		t.Fatalf("orphans after loading a dump taken during writes: %v", o)
	}
	var mails, atts int
	if err := b.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox WHERE id LIKE 'w-%'").Scan(&mails); err != nil {
		t.Fatal(err)
	}
	if err := b.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox_attachments WHERE mail_id LIKE 'w-%'").Scan(&atts); err != nil {
		t.Fatal(err)
	}
	if mails != atts {
		t.Fatalf("the dump is not one instant: %d mails, %d attachments", mails, atts)
	}
	t.Logf("writer committed %d mails; the dump holds %d", written, mails)

	// Fail closed: a table the dump does not know and a view are refused (it would miss them).
	if _, err := db.ExecContext(ctx, "CREATE TABLE stranger (id INT)"); err != nil {
		t.Fatal(err)
	}
	if err := a.DumpMariaDB(ctx, io.Discard); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("a dump with an unknown table: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE stranger"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE VIEW v_users AS SELECT id FROM users"); err != nil {
		t.Fatal(err)
	}
	if err := a.DumpMariaDB(ctx, io.Discard); err == nil || !strings.Contains(err.Error(), "view") {
		t.Fatalf("a dump with a view: %v", err)
	}
}
