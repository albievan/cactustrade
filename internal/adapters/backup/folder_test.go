package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/albievan/cactustrade/internal/adapters/sqlstore"
	"github.com/albievan/cactustrade/internal/domain"
)

// Known-answer vector shared with web/src/lib/agekey.test.ts (R48.9): the X25519 scalar
// 0x01 0x02 … 0x20 as an age identity and its recipient. A test value, never a real key.
const (
	vectorIdentity  = "AGE-SECRET-KEY-1QYPQXPQ9QCRSSZG2PVXQ6RS0ZQG3YYC5Z5TPWXQERGD3C8G7RUSQGPQYEE"
	vectorRecipient = "age1q73he0q5yzfu3d64msd3p6rvksnrwjk3d2598mgtmlqt9wrdr37q2vrn72"
)

func TestAgeKeyVector(t *testing.T) {
	id, err := age.ParseX25519Identity(vectorIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if got := id.Recipient().String(); got != vectorRecipient {
		t.Fatalf("recipient %s, want %s", got, vectorRecipient)
	}
	if _, err := ParseRecipients([]string{vectorRecipient}); err != nil {
		t.Fatal(err)
	}
}

func TestParseRecipients(t *testing.T) {
	x, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	h, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		in   []string
		ok   bool
	}{
		{"x25519", []string{x.Recipient().String()}, true},
		{"hybrid", []string{h.Recipient().String()}, true},
		{"two", []string{x.Recipient().String(), h.Recipient().String()}, true},
		{"none", nil, false},
		{"blank", []string{" "}, false},
		{"comment only", []string{"# nothing"}, false},
		{"identity, not recipient", []string{vectorIdentity}, false},
		{"ssh key", []string{"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHk8Q6KXlUqWm0rWw5f3H5h0j3j6Zr0l3f5n6x7p8q9r test"}, false},
		{"garbage", []string{"age1notakey"}, false},
		{"two lines in one entry", []string{x.Recipient().String() + "\n" + x.Recipient().String()}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseRecipients(tt.in)
			if (err == nil) != tt.ok {
				t.Fatalf("ParseRecipients = %v, want ok=%v", err, tt.ok)
			}
			ferr := (&Folder{}).ParseRecipients(tt.in)
			if (ferr == nil) != tt.ok || (ferr != nil && !errors.Is(ferr, domain.ErrInvalid)) {
				t.Fatalf("Folder.ParseRecipients = %v", ferr)
			}
		})
	}
}

// fakeSource is a MariaDB-like source whose dump is given (and may fail halfway).
type fakeSource struct {
	driver string
	dump   string
	fail   error
}

func (s fakeSource) Driver() string { return s.driver }

func (s fakeSource) BackupTo(context.Context, string) error { return errors.New("not SQLite") }

func (s fakeSource) DumpMariaDB(_ context.Context, w io.Writer) error {
	if _, err := io.WriteString(w, s.dump); err != nil {
		return err
	}
	return s.fail
}

// folders returns a private data folder and a backup folder.
func folders(t *testing.T) (dataDir, dir string) {
	t.Helper()
	base := t.TempDir()
	dataDir, dir = filepath.Join(base, "data"), filepath.Join(base, "backups")
	for _, d := range []string{dataDir, dir} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dataDir, dir
}

// names lists a folder's entries.
func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func decryptAll(t *testing.T, r io.Reader, id age.Identity) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := Decrypt(&out, r, []age.Identity{id}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestFolderSQLiteRoundTrip(t *testing.T) {
	ctx := context.Background()
	dataDir, dir := folders(t)
	store, err := sqlstore.OpenSQLite(ctx, filepath.Join(dataDir, "cactustrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	f := NewFolder(store, dataDir, dir, false)
	if f.Folder() != dir || f.Driver() != "sqlite" {
		t.Fatalf("folder %s driver %s", f.Folder(), f.Driver())
	}
	at := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	file, err := f.Write(ctx, []string{id.Recipient().String()}, at)
	if err != nil {
		t.Fatal(err)
	}
	if file.Name != "cactustrade-20270102T030405Z.db.age" || file.Size == 0 || !file.At.Equal(at) {
		t.Fatalf("file %+v", file)
	}
	rc, got, err := f.Open(ctx, file.Name)
	if err != nil || got != file {
		t.Fatalf("open %+v %v", got, err)
	}
	plain := decryptAll(t, rc, id)
	_ = rc.Close()
	if !bytes.HasPrefix(plain, []byte("SQLite format 3\x00")) {
		t.Fatal("the backup is not a SQLite database")
	}
	raw, err := os.ReadFile(filepath.Join(dir, file.Name))
	if err != nil || bytes.Contains(raw, []byte("SQLite format 3")) {
		t.Fatal("the backup file is readable without the identity")
	}
	for _, n := range names(t, dataDir) { // the plain copy is gone
		if strings.HasPrefix(n, ".backup-") {
			t.Fatalf("plain copy left in the data folder: %s", n)
		}
	}
	if n := names(t, dir); len(n) != 1 {
		t.Fatalf("backup folder holds %v", n)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, file.Name))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v %v, want 0600", fi.Mode().Perm(), err)
		}
	}
	// the same second again: a new name, the first file untouched
	second, err := f.Write(ctx, []string{id.Recipient().String()}, at)
	if err != nil || second.Name == file.Name || !strings.HasPrefix(second.Name, "cactustrade-20270102T030405Z-") {
		t.Fatalf("second %+v %v", second, err)
	}
	if again, err := os.ReadFile(filepath.Join(dir, file.Name)); err != nil || !bytes.Equal(again, raw) {
		t.Fatal("an existing backup was overwritten")
	}
	list, err := f.List(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %v", list, err)
	}
	if err := f.Remove(ctx, second.Name); err != nil {
		t.Fatal(err)
	}
	if err := f.Remove(ctx, second.Name); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
}

func TestFolderMariaDBStream(t *testing.T) {
	ctx := context.Background()
	dataDir, dir := folders(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	dump := "SET NAMES utf8mb4;\nINSERT INTO `goose_db_version` VALUES (1,0,1,'x');\n"
	f := NewFolder(fakeSource{driver: "mariadb", dump: dump}, dataDir, dir, true)
	file, err := f.Write(ctx, []string{id.Recipient().String()}, time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if file.Name != "cactustrade-20270102T030405Z.sql.gz.age" {
		t.Fatalf("name %s", file.Name)
	}
	rc, _, err := f.Open(ctx, file.Name)
	if err != nil {
		t.Fatal(err)
	}
	gz := decryptAll(t, rc, id)
	_ = rc.Close()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil || string(plain) != dump {
		t.Fatalf("dump %q %v", plain, err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, file.Name))
		if err != nil || fi.Mode().Perm() != 0o640 {
			t.Fatalf("mode %v %v, want 0640 with group read", fi.Mode().Perm(), err)
		}
	}
	if n := names(t, dataDir); len(n) != 0 {
		t.Fatalf("a MariaDB backup wrote into the data folder: %v", n)
	}
}

func TestFolderWriteFailures(t *testing.T) {
	ctx := context.Background()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	rcpt := []string{id.Recipient().String()}
	at := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name string
		src  Source
		rs   []string
		want error
	}{
		{"no recipient: nothing written", fakeSource{driver: "mariadb", dump: "x"}, nil, domain.ErrInvalid},
		{"bad recipient", fakeSource{driver: "mariadb", dump: "x"}, []string{"age1nope"}, domain.ErrInvalid},
		{"a dump failing halfway leaves no partial", fakeSource{driver: "mariadb", dump: strings.Repeat("x", 1<<20), fail: errors.New("connection lost")}, rcpt, nil},
		{"SQLite copy fails", fakeSource{driver: "sqlite"}, rcpt, nil},
		{"unknown driver", fakeSource{driver: "postgres"}, rcpt, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataDir, dir := folders(t)
			_, err := NewFolder(tt.src, dataDir, dir, false).Write(ctx, tt.rs, at)
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("Write = %v, want an error (%v)", err, tt.want)
			}
			if n := append(names(t, dir), names(t, dataDir)...); len(n) != 0 {
				t.Fatalf("left behind: %v", n)
			}
		})
	}
	if _, err := NewFolder(fakeSource{driver: "mariadb"}, t.TempDir(), filepath.Join(t.TempDir(), "missing"), false).Write(ctx, rcpt, at); err == nil {
		t.Fatal("a missing backup folder must fail")
	}
}

func TestFolderRefusesPathsAndStrangers(t *testing.T) {
	ctx := context.Background()
	dataDir, dir := folders(t)
	good := "cactustrade-20270102T030405Z.db.age"
	write := func(p string) {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, good))
	write(filepath.Join(dir, "notes.txt"))
	write(filepath.Join(dir, ".partial-0011223344556677"))
	outside := filepath.Join(filepath.Dir(dir), "cactustrade-20270101T000000Z.db.age")
	write(outside)
	if err := os.Mkdir(filepath.Join(dir, "cactustrade-20270103T000000Z.db.age"), 0o700); err != nil {
		t.Fatal(err)
	}
	symlinked := "cactustrade-20270104T000000Z.db.age"
	symErr := os.Symlink(outside, filepath.Join(dir, symlinked))
	f := NewFolder(fakeSource{driver: "sqlite"}, dataDir, dir, false)

	list, err := f.List(ctx)
	if err != nil || len(list) != 1 || list[0].Name != good {
		t.Fatalf("List = %v %v, want only %s", list, err, good)
	}
	bad := []string{
		"../" + filepath.Base(outside), outside, "notes.txt", ".partial-0011223344556677",
		"cactustrade-20270103T000000Z.db.age", "", ".", "..", "sub/" + good,
	}
	if symErr == nil {
		bad = append(bad, symlinked)
	} else if runtime.GOOS != "windows" {
		t.Fatalf("symlink: %v", symErr)
	}
	for _, name := range bad {
		if rc, _, err := f.Open(ctx, name); !errors.Is(err, domain.ErrNotFound) {
			if rc != nil {
				_ = rc.Close()
			}
			t.Errorf("Open(%q) = %v, want not found", name, err)
		}
		if err := f.Remove(ctx, name); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("Remove(%q) = %v, want not found", name, err)
		}
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("a file outside the folder was removed")
	}
	if _, err := NewFolder(fakeSource{}, dataDir, filepath.Join(dir, "missing"), false).List(ctx); err == nil {
		t.Fatal("listing a missing folder must fail")
	}
}
