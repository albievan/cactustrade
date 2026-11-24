package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/albievan/cactustrade/internal/adapters/secretstore"
	"github.com/albievan/cactustrade/internal/adapters/sqlstore"
	"github.com/albievan/cactustrade/internal/adapters/system"
	"github.com/albievan/cactustrade/internal/app"
	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// backupEnv is a standalone data folder with a populated database and a master key, plus an
// age identity for the backups.
type backupEnv struct {
	dir      string
	key      []byte // the master key
	keyText  string // as stored in master.key
	id       *age.X25519Identity
	idFile   string
	acct     domain.Account
	secretNm domain.CredentialName
}

const testSecretValue = "etoro-test-user-key-value"

func (e *backupEnv) env(extra ...string) func(string) string {
	m := map[string]string{"CACTUSTRADE_DATA_DIR": e.dir, envBackupRecipient: e.id.Recipient().String()}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i]] = extra[i+1]
	}
	return envOf(m)
}

// newBackupEnv creates the data folder through the real start-up pieces: migrations, the
// generated master key, the default admin, an eToro account with a stored key, a session and
// a live lease (as a running server leaves them).
func newBackupEnv(t *testing.T) *backupEnv {
	t.Helper()
	ctx := context.Background()
	e := &backupEnv{dir: t.TempDir()}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	e.id = id
	e.idFile = filepath.Join(t.TempDir(), "backup.agekey")
	if err := os.WriteFile(e.idFile, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(nil, e.env())
	if err != nil {
		t.Fatal(err)
	}
	st, err := sqlstore.OpenSQLite(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	secrets, _, err := openSecrets(cfg, st, envOf(nil), system.Clock{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(e.dir, "master.key"))
	if err != nil {
		t.Fatal(err)
	}
	e.keyText = strings.TrimSpace(string(b))
	if e.key, err = hex.DecodeString(e.keyText); err != nil || len(e.key) != 32 {
		t.Fatalf("master key %v", err)
	}
	svc, err := app.New(app.Deps{
		Store: st, Secrets: secrets, Hasher: fakes.Hasher{}, Random: system.Random{}, Clock: system.Clock{},
		Broker: &fakes.SimBroker{Now: time.Now}, Market: &fakes.Broker{},
	}, app.Config{Version: "test", LeaseHolder: "backup-test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.EnsureDefaultAdmin(ctx); err != nil {
		t.Fatal(err)
	}
	admin, err := st.Users().GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	admin.MustChangePassword = false
	if err := st.Users().Update(ctx, admin); err != nil {
		t.Fatal(err)
	}
	p := app.Principal{UserID: admin.ID, Username: admin.Username, Role: domain.RoleAdmin, Via: app.ViaSession}
	v, err := svc.CreateAccount(ctx, p, "Demo", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	e.acct = v.Account
	ak, uk := domain.AccountKeyNames(v.Account)
	if err := secrets.Put(ctx, ak, "etoro-test-api-key-value"); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Put(ctx, uk, testSecretValue); err != nil {
		t.Fatal(err)
	}
	e.secretNm = uk
	now := time.Now()
	if err := st.Sessions().Create(ctx, domain.Session{
		TokenHash: "token-hash", UserID: admin.ID, CSRFToken: "csrf", CreatedAt: now, LastSeen: now, ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AgentLeases().Acquire(ctx, e.acct.ID, "old-process", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return e
}

// tableCounts returns the row count of every table in a SQLite database file.
func tableCounts(t *testing.T, path string) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	st, err := sqlstore.OpenSQLite(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rows, err := st.DB().QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, n := range names {
		var c int64
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+n+`"`).Scan(&c); err != nil {
			t.Fatal(err)
		}
		counts[n] = c
	}
	return counts
}

func schemaOf(t *testing.T, path string) int64 {
	t.Helper()
	st, err := sqlstore.OpenSQLite(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cur, _, err := st.SchemaVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

func (e *backupEnv) backup(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "cactustrade.db.age")
	var msg bytes.Buffer
	if err := run(context.Background(), []string{"backup", "--out", out}, e.env(), &msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.String(), "master key is not in it") {
		t.Fatalf("backup said %q", msg.String())
	}
	st, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v", st.Mode().Perm())
	}
	return out
}

// TestBackupRestoreRoundTrip is PLAN 7.6a: row counts and the schema survive; a stored
// secret still decrypts with the master key; the decrypted archive does not contain the
// master key; leases and sessions are gone and restore.at is set.
func TestBackupRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	e := newBackupEnv(t)
	dbA := filepath.Join(e.dir, "cactustrade.db")
	before := tableCounts(t, dbA)
	schemaBefore := schemaOf(t, dbA)
	out := e.backup(t)

	// the archive is age, and its plaintext has no master key in any form
	enc, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(enc, []byte("age-encryption.org/v1")) {
		t.Fatal("the backup is not an age file")
	}
	var plain bytes.Buffer
	if err := decryptStream(&plain, bytes.NewReader(enc), []age.Identity{e.id}); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(plain.Bytes(), sqliteMagic) {
		t.Fatal("the plaintext is not a SQLite database")
	}
	for _, form := range [][]byte{e.key, []byte(e.keyText), []byte(strings.ToUpper(e.keyText))} {
		if bytes.Contains(plain.Bytes(), form) {
			t.Fatal("the backup contains the master key")
		}
	}
	if bytes.Contains(plain.Bytes(), []byte(testSecretValue)) {
		t.Fatal("the backup contains a credential in plaintext")
	}

	// restore into a fresh data folder that has the same master key
	dirB := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirB, "master.key"), []byte(e.keyText+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	envB := envOf(map[string]string{"CACTUSTRADE_DATA_DIR": dirB, envBackupIdentityFile: e.idFile})
	var msg bytes.Buffer
	if err := run(ctx, []string{"restore", "--in", out, "--yes"}, envB, &msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.String(), "reconciles every account") {
		t.Fatalf("restore said %q", msg.String())
	}
	dbB := filepath.Join(dirB, "cactustrade.db")
	after := tableCounts(t, dbB)
	if got := schemaOf(t, dbB); got != schemaBefore {
		t.Fatalf("schema %d, want %d", got, schemaBefore)
	}
	for tbl, n := range before {
		want := n
		switch tbl {
		case "sessions", "agent_leases":
			want = 0
		case "settings":
			want = n + 1 // restore.at
		}
		if after[tbl] != want {
			t.Errorf("table %s: %d rows after the restore, want %d (before %d)", tbl, after[tbl], want, n)
		}
	}
	if len(after) != len(before) {
		t.Errorf("tables %v, want %v", after, before)
	}

	cfgB, err := loadConfig(nil, envB)
	if err != nil {
		t.Fatal(err)
	}
	stB, err := sqlstore.OpenSQLite(ctx, cfgB.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stB.Close()
	secrets, _, err := openSecrets(cfgB, stB, envOf(nil), system.Clock{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if v, err := secrets.Get(ctx, e.secretNm); err != nil || v != testSecretValue {
		t.Fatalf("stored secret after the restore: %q %v", v, err)
	}
	at, err := stB.Settings().Get(ctx, sqlstore.RestoreAtSetting)
	if err != nil {
		t.Fatal(err)
	}
	if ts, err := time.Parse(time.RFC3339Nano, at); err != nil || time.Since(ts) > time.Minute {
		t.Fatalf("restore.at %q %v", at, err)
	}
	if n, _ := os.ReadDir(filepath.Join(dirB, lockDirName)); len(n) != 0 {
		t.Fatalf("lock files left: %v", n)
	}
	if left, _ := filepath.Glob(filepath.Join(dirB, ".restore-*")); len(left) != 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	if left, _ := filepath.Glob(filepath.Join(e.dir, ".backup-*")); len(left) != 0 {
		t.Fatalf("plain backup copies left: %v", left)
	}

	// a second restore keeps the replaced database beside the new one
	stB.Close()
	msg.Reset()
	if err := run(ctx, []string{"restore", "--in", out, "--yes"}, envB, &msg); err != nil {
		t.Fatal(err)
	}
	if prev, _ := filepath.Glob(filepath.Join(dirB, "cactustrade.db.pre-restore-*")); len(prev) != 1 || !strings.Contains(msg.String(), "kept as") {
		t.Fatalf("replaced database not kept: %v %q", prev, msg.String())
	}
	// two more in the same second: each replaced database is kept, none overwritten
	fixed := func() time.Time { return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC) }
	for range 2 {
		if err := restoreCmd(ctx, []string{"--in", out, "--yes"}, envB, &bytes.Buffer{}, fixed); err != nil {
			t.Fatal(err)
		}
	}
	if prev, _ := filepath.Glob(filepath.Join(dirB, "cactustrade.db.pre-restore-*")); len(prev) != 3 {
		t.Fatalf("replaced databases: %v", prev)
	}
}

// TestBackupNeedsARecipient is OD29: no recipient, no backup (and no file).
func TestBackupNeedsARecipient(t *testing.T) {
	e := newBackupEnv(t)
	out := filepath.Join(t.TempDir(), "b.age")
	err := run(context.Background(), []string{"backup", "--out", out}, envOf(map[string]string{"CACTUSTRADE_DATA_DIR": e.dir}), &bytes.Buffer{})
	if !errors.Is(err, errNoRecipient) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a file was written without a recipient")
	}
}

func TestBackupAndRestoreArguments(t *testing.T) {
	e := newBackupEnv(t)
	out := e.backup(t)
	maria := []string{"CACTUSTRADE_DB_DRIVER", "mariadb", "CACTUSTRADE_DB_PASSWORD", "pw", envBackupIdentityFile, e.idFile}
	tests := []struct {
		name string
		args []string
		env  func(string) string
		want string
	}{
		{"backup without --out", []string{"backup"}, e.env(), "backup --out"},
		{"backup over an existing file", []string{"backup", "--out", out}, e.env(), "exists"},
		{"backup with a bad recipient", []string{"backup", "--out", out + "2", "--recipient", "age1nonsense"}, e.env(), "recipient"},
		{"backup on MariaDB", []string{"backup", "--out", out + "3"}, e.env(maria...), "deploy.sh"},
		{"backup without a database", []string{"backup", "--out", out + "4", "--data-dir", t.TempDir()}, e.env(), "no database"},
		{"restore without --yes", []string{"restore", "--in", out}, e.env(envBackupIdentityFile, e.idFile), "--yes"},
		{"restore without --in", []string{"restore", "--yes"}, e.env(envBackupIdentityFile, e.idFile), "restore --in"},
		{"restore without an identity", []string{"restore", "--in", out, "--yes"}, e.env(), "identity"},
		{"restore on MariaDB", []string{"restore", "--in", out, "--yes"}, e.env(maria...), "deploy.sh"},
		{"restore of a missing file", []string{"restore", "--in", out + ".missing", "--yes", "--data-dir", t.TempDir()}, e.env(envBackupIdentityFile, e.idFile), "no such file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(context.Background(), tt.args, tt.env, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// TestRestoreRefusesBadBackups: a wrong identity, a file that is not a standalone backup, a
// database without our schema and one from a newer build are refused, and the current
// database stays as it was.
func TestRestoreRefusesBadBackups(t *testing.T) {
	ctx := context.Background()
	e := newBackupEnv(t)
	out := e.backup(t)
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	otherFile := filepath.Join(t.TempDir(), "other.agekey")
	if err := os.WriteFile(otherFile, []byte(other.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	encryptTo := func(t *testing.T, plain []byte) string {
		t.Helper()
		f := filepath.Join(t.TempDir(), "x.age")
		var b bytes.Buffer
		if err := encryptStream(&b, bytes.NewReader(plain), []age.Recipient{e.id.Recipient()}); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	sqliteFile := func(t *testing.T, setup func(*sqlstore.Store)) []byte {
		t.Helper()
		p := filepath.Join(t.TempDir(), "x.db")
		st, err := sqlstore.OpenSQLite(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		setup(st)
		cp := filepath.Join(t.TempDir(), "copy.db")
		if err := st.BackupTo(ctx, cp); err != nil {
			t.Fatal(err)
		}
		_ = st.Close()
		b, err := os.ReadFile(cp)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	newer := sqliteFile(t, func(st *sqlstore.Store) {
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().ExecContext(ctx, "INSERT INTO goose_db_version (version_id, is_applied) VALUES (99999, 1)"); err != nil {
			t.Fatal(err)
		}
	})
	empty := sqliteFile(t, func(st *sqlstore.Store) {
		if _, err := st.DB().ExecContext(ctx, "CREATE TABLE other (x INTEGER)"); err != nil {
			t.Fatal(err)
		}
	})
	corrupt := slices.Clone(sqliteFile(t, func(st *sqlstore.Store) {
		if err := st.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}))
	for i := 4096; i < len(corrupt) && i < 4096*6; i++ {
		corrupt[i] ^= 0x5a
	}
	tampered, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	tampered[len(tampered)-10] ^= 1
	tamperedFile := filepath.Join(t.TempDir(), "t.age")
	if err := os.WriteFile(tamperedFile, tampered, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, in, identity, want string
	}{
		{"wrong identity", out, otherFile, "did not match"},
		{"not age", e.idFile, e.idFile, "decrypt"},
		{"tampered", tamperedFile, e.idFile, "decrypt"},
		{"a MariaDB dump", encryptTo(t, []byte("-- MariaDB dump 10.19\nCREATE TABLE x (y INT);\n")), e.idFile, "deploy.sh --restore"},
		{"no schema", encryptTo(t, empty), e.idFile, "not a cactusTrade database"},
		{"newer schema", encryptTo(t, newer), e.idFile, "newer than this build"},
		{"corrupt", encryptTo(t, corrupt), e.idFile, "restore"},
	}
	dbPath := filepath.Join(e.dir, "cactustrade.db")
	before := tableCounts(t, dbPath)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// the backup env's own database has a live lease: drop it so only the backup is judged
			clearLeases(t, dbPath)
			err := run(ctx, []string{"restore", "--in", tt.in, "--yes", "--identity", tt.identity}, e.env(), &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			after := tableCounts(t, dbPath)
			before["agent_leases"] = 0
			if fmt.Sprint(after) != fmt.Sprint(before) {
				t.Fatalf("the current database changed: %v → %v", before, after)
			}
			if left, _ := filepath.Glob(filepath.Join(e.dir, ".restore-*")); len(left) != 0 {
				t.Fatalf("temporary files left: %v", left)
			}
		})
	}
}

func clearLeases(t *testing.T, dbPath string) {
	t.Helper()
	st, err := sqlstore.OpenSQLite(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DB().ExecContext(context.Background(), "DELETE FROM agent_leases"); err != nil {
		t.Fatal(err)
	}
}

// TestRestoreRefusedWhileServing is PLAN 7.6a: a restore is refused while a server runs on
// the data folder (its lock file), while any account lease is live, and a server does not
// start while a restore runs. A lock left by a dead process does not block.
func TestRestoreRefusedWhileServing(t *testing.T) {
	ctx := context.Background()
	e := newBackupEnv(t)
	out := e.backup(t)
	env := e.env(envBackupIdentityFile, e.idFile, "CACTUSTRADE_TLS", "off", "CACTUSTRADE_ADDR", "127.0.0.1:0")
	restore := func() error {
		return run(ctx, []string{"restore", "--in", out, "--yes"}, env, &bytes.Buffer{})
	}

	// a live lease (a server on another machine, or one that just died): refused
	if err := restore(); !errors.Is(err, errServerRunning) || !strings.Contains(err.Error(), "lease") {
		t.Fatalf("restore with a live lease: %v", err)
	}
	clearLeases(t, filepath.Join(e.dir, "cactustrade.db"))

	// a real server on the data folder
	cfg, err := loadConfig(nil, env)
	if err != nil {
		t.Fatal(err)
	}
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- serve(sctx, cfg, env) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if n, _ := freshLocks(e.dir, func(n string) bool { return strings.HasPrefix(n, serveLockPrefix) }, time.Now); n == 1 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("the server did not lock the data folder: %v", <-done)
		}
		time.Sleep(10 * time.Millisecond)
	}
	err = restore()
	cancel()
	if serr := <-done; serr != nil && !errors.Is(serr, context.Canceled) {
		t.Fatalf("serve: %v", serr)
	}
	if !errors.Is(err, errServerRunning) {
		t.Fatalf("restore while serving: %v", err)
	}
	if n, _ := freshLocks(e.dir, func(string) bool { return true }, time.Now); n != 0 {
		t.Fatalf("%d lock files left after the server and the refused restore", n)
	}

	// a restore in progress: the server refuses to start
	lk, err := lockRestore(e.dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := serve(ctx, cfg, env); !errors.Is(err, errRestoreRunning) {
		t.Fatalf("serve during a restore: %v", err)
	}
	if err := restore(); !errors.Is(err, errRestoreRunning) {
		t.Fatalf("two restores at once: %v", err)
	}
	lk.release()

	// a lock left by a process that died long ago does not block
	stale := filepath.Join(e.dir, lockDirName, serveLockPrefix+"dead.lock")
	if err := os.WriteFile(stale, []byte("pid 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatalf("restore with a stale lock: %v", err)
	}
}

// TestDataLockHeartbeat: the lock stays fresh while held (a long restore or server) and a
// stale restore lock is taken over.
func TestDataLockHeartbeat(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	lk, err := lockServe(dir, clock)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.release()
	mu.Lock()
	now = now.Add(lockFresh - time.Second)
	mu.Unlock()
	if _, err := lockRestore(dir, clock); !errors.Is(err, errServerRunning) {
		t.Fatalf("restore beside a fresh server lock: %v", err)
	}
	mu.Lock()
	now = now.Add(2 * lockFresh) // no heartbeat ran in between (5 s ticker): stale
	mu.Unlock()
	rl, err := lockRestore(dir, clock)
	if err != nil {
		t.Fatalf("restore beside a stale server lock: %v", err)
	}
	rl.release()
	// a stale restore lock is replaced
	p := filepath.Join(dir, lockDirName, restoreLockName)
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := clock().Add(-time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	rl, err = lockRestore(dir, clock)
	if err != nil {
		t.Fatalf("stale restore lock: %v", err)
	}
	rl.release()
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("restore lock not removed")
	}
}

// reconcileFirst is the real service, recording whether ReconcileAll ran before the loops.
type reconcileFirst struct {
	*app.Service
	acct  string
	mu    sync.Mutex
	calls []string
}

func (r *reconcileFirst) note(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *reconcileFirst) ReconcileAll(ctx context.Context) ([]app.Reconciliation, error) {
	if r.Reconciled(r.acct).OK {
		r.note("already reconciled")
	}
	r.note("reconcile")
	return r.Service.ReconcileAll(ctx)
}

func (r *reconcileFirst) SuperviseLoop(ctx context.Context) {
	r.note("supervise")
	r.Service.SuperviseLoop(ctx)
}

func (r *reconcileFirst) AgentLoop(ctx context.Context) {
	r.note("agent")
	r.Service.AgentLoop(ctx)
}

// TestRestoreThenStartReconcilesFirst: after a restore the account is not reconciled, and
// the start-up reconciles it with eToro before the supervisor or the agent loop runs (PLAN
// 4.14); the lease of the process that took the backup is gone.
func TestRestoreThenStartReconcilesFirst(t *testing.T) {
	ctx := context.Background()
	e := newBackupEnv(t)
	out := e.backup(t)
	dirB := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirB, "master.key"), []byte(e.keyText), 0o600); err != nil {
		t.Fatal(err)
	}
	envB := envOf(map[string]string{"CACTUSTRADE_DATA_DIR": dirB, envBackupIdentityFile: e.idFile})
	if err := run(ctx, []string{"restore", "--in", out, "--yes"}, envB, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(nil, envB)
	if err != nil {
		t.Fatal(err)
	}
	st, err := sqlstore.OpenSQLite(ctx, cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.AgentLeases().Get(ctx, e.acct.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("lease from the backup: %v", err)
	}
	secrets, _, err := openSecrets(cfg, st, envOf(nil), system.Clock{}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	b := &fakes.SimBroker{Now: time.Now, Cash: 10_000_00}
	svc, err := app.New(app.Deps{
		Store: st, Secrets: secrets, Hasher: fakes.Hasher{}, Random: system.Random{}, Clock: system.Clock{},
		Broker: b, Market: &fakes.Broker{},
		Sleep: func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() },
	}, app.Config{Version: "test", LeaseHolder: "after-restore"})
	if err != nil {
		t.Fatal(err)
	}
	if svc.Reconciled(e.acct.ID).OK {
		t.Fatal("reconciled straight after the restore")
	}
	r := &reconcileFirst{Service: svc, acct: e.acct.ID}
	var tr trader = r
	stop := startTrading(ctx, tr, slog.New(slog.DiscardHandler), prodSchedule())
	deadline := time.Now().Add(10 * time.Second)
	for {
		if l, err := st.AgentLeases().Get(ctx, e.acct.ID); err == nil && l.Holder == "after-restore" {
			break
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatal("the supervisor did not run after the restore")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	r.mu.Lock()
	calls := slices.Clone(r.calls)
	r.mu.Unlock()
	if len(calls) < 2 || calls[0] != "reconcile" || slices.Contains(calls, "already reconciled") {
		t.Fatalf("calls after a restore: %v", calls)
	}
	if !svc.Reconciled(e.acct.ID).OK {
		t.Fatalf("not reconciled: %+v", svc.Reconciled(e.acct.ID))
	}
	if b.Opens != 0 || b.Closes != 0 {
		t.Fatalf("orders after a restore: opens %d closes %d", b.Opens, b.Closes)
	}
}

// TestEncryptDecryptCommands: the age filter deploy.sh uses (PLAN 7.6b).
func TestEncryptDecryptCommands(t *testing.T) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	id2, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	idFile := filepath.Join(dir, "id")
	id2File := filepath.Join(dir, "id2")
	rcptFile := filepath.Join(dir, "recipients")
	for f, v := range map[string]string{idFile: id.String(), id2File: id2.String(), rcptFile: "# backup keys\n" + id.Recipient().String() + "\n" + id2.Recipient().String() + "\n"} {
		if err := os.WriteFile(f, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plain := []byte("-- MariaDB dump\nINSERT INTO settings VALUES ('k','v',1);\n")
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		decrypt string // identity file that must open it
	}{
		{"flag", []string{"--recipient", id.Recipient().String()}, nil, idFile},
		{"environment, two keys", nil, map[string]string{envBackupRecipient: id.Recipient().String() + ", " + id2.Recipient().String()}, id2File},
		{"recipients file", nil, map[string]string{envBackupRecipientFile: rcptFile}, idFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var enc, dec bytes.Buffer
			if err := encryptCmd(tt.args, envOf(tt.env), bytes.NewReader(plain), &enc); err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(enc.Bytes(), []byte("INSERT")) {
				t.Fatal("not encrypted")
			}
			if err := decryptCmd([]string{"--identity", tt.decrypt}, envOf(nil), bytes.NewReader(enc.Bytes()), &dec); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(dec.Bytes(), plain) {
				t.Fatal("round trip changed the data")
			}
			dec.Reset()
			if err := decryptCmd(nil, envOf(map[string]string{envBackupIdentityFile: tt.decrypt}), bytes.NewReader(enc.Bytes()), &dec); err != nil || !bytes.Equal(dec.Bytes(), plain) {
				t.Fatalf("identity from the environment: %v", err)
			}
			// --identity -: the identity, an empty line, then the file (deploy.sh in a container)
			idText, err := os.ReadFile(tt.decrypt)
			if err != nil {
				t.Fatal(err)
			}
			dec.Reset()
			stdin := io.MultiReader(strings.NewReader("# key\n"+strings.TrimSpace(string(idText))+"\n\n"), bytes.NewReader(enc.Bytes()))
			if err := decryptCmd([]string{"--identity", "-"}, envOf(nil), stdin, &dec); err != nil || !bytes.Equal(dec.Bytes(), plain) {
				t.Fatalf("identity from stdin: %v", err)
			}
		})
	}
	bad := []struct {
		name string
		f    func() error
		want string
	}{
		{"no recipient", func() error { return encryptCmd(nil, envOf(nil), strings.NewReader("x"), &bytes.Buffer{}) }, "set " + envBackupRecipient},
		{"bad recipient", func() error {
			return encryptCmd([]string{"--recipient", "ssh-ed25519 AAAA"}, envOf(nil), strings.NewReader("x"), &bytes.Buffer{})
		}, "recipient"},
		{"missing recipients file", func() error {
			return encryptCmd(nil, envOf(map[string]string{envBackupRecipientFile: filepath.Join(dir, "none")}), strings.NewReader("x"), &bytes.Buffer{})
		}, envBackupRecipientFile},
		{"encrypt extra argument", func() error { return encryptCmd([]string{"x"}, envOf(nil), strings.NewReader("x"), &bytes.Buffer{}) }, "unexpected"},
		{"encrypt bad flag", func() error {
			return encryptCmd([]string{"--nope"}, envOf(nil), strings.NewReader("x"), &bytes.Buffer{})
		}, "nope"},
		{"no identity", func() error { return decryptCmd(nil, envOf(nil), strings.NewReader("x"), &bytes.Buffer{}) }, "identity"},
		{"missing identity file", func() error {
			return decryptCmd([]string{"--identity", filepath.Join(dir, "none")}, envOf(nil), strings.NewReader("x"), &bytes.Buffer{})
		}, "identity"},
		{"not an identity", func() error {
			return decryptCmd([]string{"--identity", rcptFile}, envOf(nil), strings.NewReader("x"), &bytes.Buffer{})
		}, "identity"},
		{"decrypt extra argument", func() error { return decryptCmd([]string{"x"}, envOf(nil), strings.NewReader("x"), &bytes.Buffer{}) }, "unexpected"},
		{"decrypt bad flag", func() error {
			return decryptCmd([]string{"--nope"}, envOf(nil), strings.NewReader("x"), &bytes.Buffer{})
		}, "nope"},
		{"not age", func() error {
			return decryptCmd([]string{"--identity", idFile}, envOf(nil), strings.NewReader("plain text"), &bytes.Buffer{})
		}, "decrypt"},
		{"stdin identity without the empty line", func() error {
			return decryptCmd([]string{"--identity", "-"}, envOf(nil), strings.NewReader(id.String()), &bytes.Buffer{})
		}, "empty line"},
		{"stdin identity that is not one", func() error {
			return decryptCmd([]string{"--identity", "-"}, envOf(nil), strings.NewReader("nonsense\n\nage"), &bytes.Buffer{})
		}, "identity (stdin)"},
		{"stdin identity, wrong key", func() error {
			var enc bytes.Buffer
			if err := encryptStream(&enc, strings.NewReader("x"), []age.Recipient{id2.Recipient()}); err != nil {
				return err
			}
			return decryptCmd([]string{"--identity", "-"}, envOf(nil), io.MultiReader(strings.NewReader(id.String()+"\n\n"), &enc), &bytes.Buffer{})
		}, "did not match"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.f(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

var _ ports.SecretStore = (*secretstore.Env)(nil)
