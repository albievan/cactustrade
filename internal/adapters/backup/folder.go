package backup

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// Source is the database a Folder backs up (*sqlstore.Store).
type Source interface {
	// Driver is "sqlite" or "mariadb".
	Driver() string
	// BackupTo writes a consistent SQLite copy into the new file path (VACUUM INTO).
	BackupTo(ctx context.Context, path string) error
	// DumpMariaDB writes a consistent logical MariaDB dump to w.
	DumpMariaDB(ctx context.Context, w io.Writer) error
}

// Database drivers (as sqlstore names them).
const (
	driverSQLite  = "sqlite"
	driverMariaDB = "mariadb"
)

// partialPrefix starts the name of a backup being written; it never parses as a backup.
const partialPrefix = ".partial-"

// Folder is the backup folder (ports.Backups). SQLite: VACUUM INTO a random file in the
// private data folder, then age into the backup folder. MariaDB: the logical dump → gzip →
// age straight into the backup folder (nothing plain on disk). A file is written under a
// partial name and linked to its final name, which is never overwritten.
type Folder struct {
	src       Source
	dataDir   string
	dir       string
	groupRead bool
}

var _ ports.Backups = (*Folder)(nil)

// NewFolder returns the backup folder dir (absolute) for the database src; dataDir is the
// private data folder (0700) that holds SQLite's plain copy until it is encrypted. With
// groupRead the files are 0640 (containers: the host user's group reads them for
// deploy.sh --restore; the files are encrypted), else 0600.
func NewFolder(src Source, dataDir, dir string, groupRead bool) *Folder {
	return &Folder{src: src, dataDir: dataDir, dir: dir, groupRead: groupRead}
}

// Folder returns the folder's path.
func (f *Folder) Folder() string { return f.dir }

// Driver returns the database driver.
func (f *Folder) Driver() string { return f.src.Driver() }

// ParseRecipients checks the recipients (X25519 or hybrid age public keys).
func (f *Folder) ParseRecipients(rs []string) error {
	if _, err := ParseRecipients(rs); err != nil {
		return domain.Invalid("recipients", "each must be an age public key (age1…): "+err.Error())
	}
	return nil
}

// fileMode is the mode of a backup file.
func (f *Folder) fileMode() os.FileMode {
	if f.groupRead {
		return 0o640
	}
	return 0o600
}

// randomHex returns n random bytes as hex.
func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Write takes a backup now, encrypted to the recipients, named for at.
func (f *Folder) Write(ctx context.Context, recipients []string, at time.Time) (domain.BackupFile, error) {
	rs, err := ParseRecipients(recipients)
	if err != nil {
		return domain.BackupFile{}, domain.Invalid("recipients", "each must be an age public key (age1…): "+err.Error())
	}
	ext := domain.BackupExtSQLite
	switch f.src.Driver() {
	case driverSQLite:
	case driverMariaDB:
		ext = domain.BackupExtMariaDB
	default:
		return domain.BackupFile{}, fmt.Errorf("backup.Write: unknown database driver %q", f.src.Driver())
	}
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return domain.BackupFile{}, fmt.Errorf("backup.Write: the backup folder: %w", err)
	}
	defer root.Close()
	rnd, err := randomHex(8)
	if err != nil {
		return domain.BackupFile{}, fmt.Errorf("backup.Write: %w", err)
	}
	partial := partialPrefix + rnd
	pf, err := root.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, f.fileMode())
	if err != nil {
		return domain.BackupFile{}, fmt.Errorf("backup.Write: %w", err)
	}
	closed := false
	defer func() { // the partial file goes in every case (it is linked to the final name)
		if !closed {
			_ = pf.Close()
		}
		_ = root.Remove(partial)
	}()
	if ext == domain.BackupExtSQLite {
		err = f.writeSQLite(ctx, pf, rs)
	} else {
		err = f.writeMariaDB(ctx, pf, rs)
	}
	if err != nil {
		return domain.BackupFile{}, err
	}
	if err := pf.Sync(); err != nil {
		return domain.BackupFile{}, fmt.Errorf("backup.Write: %w", err)
	}
	closed = true
	if err := pf.Close(); err != nil {
		return domain.BackupFile{}, fmt.Errorf("backup.Write: %w", err)
	}
	if runtime.GOOS != "windows" { // the mode without the umask
		if err := root.Chmod(partial, f.fileMode()); err != nil {
			return domain.BackupFile{}, fmt.Errorf("backup.Write: %w", err)
		}
	}
	name, err := settle(root, partial, at, ext)
	if err != nil {
		return domain.BackupFile{}, err
	}
	fi, err := root.Lstat(name)
	if err != nil {
		return domain.BackupFile{}, fmt.Errorf("backup.Write: %w", err)
	}
	p, _ := domain.ParseBackupName(name)
	return domain.BackupFile{Name: name, Size: fi.Size(), At: p.At}, nil
}

// settle gives the written partial file its final name without ever replacing a file: a
// hard link fails when the name exists (then a random suffix is tried). Where hard links are
// not supported, a checked rename is the fallback (one run at a time, R48.5).
func settle(root *os.Root, partial string, at time.Time, ext string) (string, error) {
	for i := range 8 {
		suffix := ""
		if i > 0 {
			s, err := randomHex(2)
			if err != nil {
				return "", fmt.Errorf("backup.Write: %w", err)
			}
			suffix = s
		}
		name, err := domain.BackupName(at, ext, suffix)
		if err != nil {
			return "", fmt.Errorf("backup.Write: %w", err)
		}
		err = root.Link(partial, name)
		switch {
		case err == nil:
			return name, nil
		case errors.Is(err, fs.ErrExist):
			continue
		}
		if _, lerr := root.Lstat(name); lerr == nil || !errors.Is(lerr, fs.ErrNotExist) {
			continue
		}
		if err := root.Rename(partial, name); err != nil {
			return "", fmt.Errorf("backup.Write: %w", err)
		}
		return name, nil
	}
	return "", errors.New("backup.Write: no free file name")
}

// writeSQLite copies the database into a random file in the data folder (private, 0700),
// encrypts the copy into dst and removes it.
func (f *Folder) writeSQLite(ctx context.Context, dst io.Writer, rs []age.Recipient) error {
	data, err := os.OpenRoot(f.dataDir)
	if err != nil {
		return fmt.Errorf("backup.Write: the data folder: %w", err)
	}
	defer data.Close()
	rnd, err := randomHex(8)
	if err != nil {
		return fmt.Errorf("backup.Write: %w", err)
	}
	base := ".backup-" + rnd + ".db"
	defer func() {
		for _, sfx := range []string{"", "-wal", "-shm", "-journal"} {
			_ = data.Remove(base + sfx)
		}
	}()
	if err := f.src.BackupTo(ctx, filepath.Join(f.dataDir, base)); err != nil {
		return fmt.Errorf("backup.Write: %w", err)
	}
	src, err := data.Open(base)
	if err != nil {
		return fmt.Errorf("backup.Write: %w", err)
	}
	defer src.Close()
	if err := Encrypt(dst, src, rs); err != nil {
		return fmt.Errorf("backup.Write: %w", err)
	}
	return nil
}

// writeMariaDB streams the dump through gzip and age into dst.
func (f *Folder) writeMariaDB(ctx context.Context, dst io.Writer, rs []age.Recipient) error {
	aw, err := age.Encrypt(dst, rs...)
	if err != nil {
		return fmt.Errorf("backup.Write: encrypt: %w", err)
	}
	zw := gzip.NewWriter(aw)
	if err := f.src.DumpMariaDB(ctx, zw); err != nil {
		return fmt.Errorf("backup.Write: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("backup.Write: %w", err)
	}
	if err := aw.Close(); err != nil {
		return fmt.Errorf("backup.Write: encrypt: %w", err)
	}
	return nil
}

// List returns the backup files, newest first: regular files (no symlinks, no folders)
// whose names parse.
func (f *Folder) List(_ context.Context) ([]domain.BackupFile, error) {
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return nil, fmt.Errorf("backup.List: %w", err)
	}
	defer root.Close()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("backup.List: %w", err)
	}
	var out []domain.BackupFile
	for _, e := range entries {
		p, ok := domain.ParseBackupName(e.Name())
		if !ok {
			continue
		}
		fi, err := root.Lstat(e.Name())
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		out = append(out, domain.BackupFile{Name: e.Name(), Size: fi.Size(), At: p.At})
	}
	slices.SortFunc(out, func(a, b domain.BackupFile) int {
		if c := b.At.Compare(a.At); c != 0 {
			return c
		}
		return strings.Compare(b.Name, a.Name)
	})
	return out, nil
}

// lookup checks that name is a bare backup file name of a regular file in the folder.
func lookup(root *os.Root, name string) (os.FileInfo, error) {
	if _, ok := domain.ParseBackupName(name); !ok || filepath.Base(name) != name {
		return nil, fmt.Errorf("backup file %q: %w", name, domain.ErrNotFound)
	}
	fi, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("backup file %q: %w", name, domain.ErrNotFound)
		}
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("backup file %q is not a regular file: %w", name, domain.ErrNotFound)
	}
	return fi, nil
}

// Remove deletes one backup file.
func (f *Folder) Remove(_ context.Context, name string) error {
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return fmt.Errorf("backup.Remove: %w", err)
	}
	defer root.Close()
	if _, err := lookup(root, name); err != nil {
		return fmt.Errorf("backup.Remove: %w", err)
	}
	if err := root.Remove(name); err != nil {
		return fmt.Errorf("backup.Remove: %w", err)
	}
	return nil
}

// Open opens one backup file for reading.
func (f *Folder) Open(_ context.Context, name string) (io.ReadCloser, domain.BackupFile, error) {
	root, err := os.OpenRoot(f.dir)
	if err != nil {
		return nil, domain.BackupFile{}, fmt.Errorf("backup.Open: %w", err)
	}
	defer root.Close()
	fi, err := lookup(root, name)
	if err != nil {
		return nil, domain.BackupFile{}, fmt.Errorf("backup.Open: %w", err)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, domain.BackupFile{}, fmt.Errorf("backup.Open: %w", err)
	}
	if st, err := file.Stat(); err != nil || !os.SameFile(fi, st) {
		_ = file.Close()
		return nil, domain.BackupFile{}, fmt.Errorf("backup.Open: %q changed while opening: %w", name, domain.ErrNotFound)
	}
	p, _ := domain.ParseBackupName(name)
	return file, domain.BackupFile{Name: name, Size: fi.Size(), At: p.At}, nil
}
