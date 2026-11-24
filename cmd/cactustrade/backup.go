package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/albievan/cactustrade/internal/adapters/sqlstore"
)

// sqliteMagic starts every SQLite database file.
var sqliteMagic = []byte("SQLite format 3\x00")

// backupCmd is `cactustrade backup --out FILE [--recipient age1…]… [--data-dir DIR]`
// (standalone, PLAN 7.6a): a consistent copy of the SQLite database (VACUUM INTO, while the
// server keeps running), age-encrypted to the backup recipients. Without a recipient nothing
// is written (OD29). The master key is not in the backup: keep it (and the identity) apart.
func backupCmd(ctx context.Context, args []string, getenv func(string) string, out io.Writer, now func() time.Time) error {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	outPath := fs.String("out", "", "the encrypted backup file to write (must not exist)")
	dataDir := fs.String("data-dir", "", "data folder (default CACTUSTRADE_DATA_DIR or ./data)")
	var rflags multiFlag
	fs.Var(&rflags, "recipient", "age public key (age1…); repeatable; default "+envBackupRecipient)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outPath == "" || fs.NArg() > 0 {
		return errors.New("backup --out FILE [--recipient age1…]")
	}
	rs, err := backupRecipients(rflags, getenv)
	if err != nil {
		return err
	}
	cfg, err := standaloneConfig(*dataDir, getenv, "backup")
	if err != nil {
		return err
	}
	if _, err := os.Stat(cfg.DBPath); err != nil {
		return fmt.Errorf("backup: no database at %s: %w", cfg.DBPath, err)
	}
	store, err := sqlstore.OpenSQLite(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer store.Close()
	schema, _, err := store.SchemaVersions(ctx)
	if err != nil {
		return err
	}
	// The plain copy lives only in the private data folder, and only until it is encrypted.
	tmp, err := tempName(cfg.DataDir, ".backup-", ".db")
	if err != nil {
		return err
	}
	defer removeDB(tmp)
	if err := store.BackupTo(ctx, tmp); err != nil {
		return err
	}
	f, err := os.OpenFile(*outPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(*outPath)
		}
	}()
	src, err := os.Open(tmp)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	defer src.Close()
	if err := encryptStream(f, src, rs); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	ok = true
	_, err = fmt.Fprintf(out, "backup %s: schema %d, %s, encrypted to %d recipient(s); the master key is not in it (keep master.key safe)\n",
		*outPath, schema, now().UTC().Format(time.RFC3339), len(rs))
	return err
}

// restoreCmd is `cactustrade restore --in FILE --yes [--identity FILE] [--data-dir DIR]`
// (standalone, PLAN 7.6a). It refuses while a server uses the data folder (a fresh lock file
// or a live account lease) and replaces the database with the backup's after checking it:
// age decryption with the identity, a SQLite file that passes the integrity check, a schema
// this build knows (older ones are migrated up). The restored database has no leases and no
// sessions, and restore.at records the time. The replaced database stays beside it as
// cactustrade.db.pre-restore-<time>. The next start reconciles every account with eToro
// before anything else (PLAN 4.14); the master key in the data folder is kept as it is.
func restoreCmd(ctx context.Context, args []string, getenv func(string) string, out io.Writer, now func() time.Time) error {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	inPath := fs.String("in", "", "the encrypted backup file")
	idFile := fs.String("identity", "", "age identity file (the private key); default "+envBackupIdentityFile)
	dataDir := fs.String("data-dir", "", "data folder (default CACTUSTRADE_DATA_DIR or ./data)")
	yes := fs.Bool("yes", false, "replace the database (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *inPath == "" || fs.NArg() > 0 {
		return errors.New("restore --in FILE --yes [--identity FILE]")
	}
	if !*yes {
		return errors.New("restore replaces the database with the backup's: re-run with --yes")
	}
	ids, err := backupIdentities(*idFile, getenv)
	if err != nil {
		return err
	}
	cfg, err := standaloneConfig(*dataDir, getenv, "restore")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	lk, err := lockRestore(cfg.DataDir, now)
	if err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	defer lk.release()

	// The database being replaced: nobody may hold a lease on it, and its write-ahead log
	// goes into the file before the file is moved aside.
	_, statErr := os.Stat(cfg.DBPath)
	hadDB := statErr == nil
	if hadDB {
		if err := quiesceCurrent(ctx, cfg.DBPath, now()); err != nil {
			return err
		}
	}

	tmp, err := tempName(cfg.DataDir, ".restore-", ".db")
	if err != nil {
		return err
	}
	defer removeDB(tmp)
	schema, err := decryptBackup(ctx, *inPath, tmp, ids, now())
	if err != nil {
		return err
	}

	prev := ""
	if hadDB {
		prev = cfg.DBPath + ".pre-restore-" + now().UTC().Format("20060102T150405Z")
		if _, err := os.Stat(prev); err == nil { // two restores in one second: never overwrite
			if prev, err = tempName(cfg.DataDir, filepath.Base(prev)+"-", ""); err != nil {
				return err
			}
		}
		if err := os.Rename(cfg.DBPath, prev); err != nil {
			return fmt.Errorf("restore: keep the current database: %w", err)
		}
		for _, sfx := range []string{"-wal", "-shm"} { // emptied by the checkpoint
			_ = os.Remove(cfg.DBPath + sfx)
		}
	}
	if err := os.Rename(tmp, cfg.DBPath); err != nil {
		if prev != "" {
			_ = os.Rename(prev, cfg.DBPath)
		}
		return fmt.Errorf("restore: %w", err)
	}
	msg := fmt.Sprintf("restored %s (schema %d) into %s", *inPath, schema, cfg.DBPath)
	if prev != "" {
		msg += "; the replaced database is kept as " + filepath.Base(prev)
	}
	_, err = fmt.Fprintln(out, msg+"\nstart cactustrade: it reconciles every account with eToro before any agent or exit loop runs; everyone signs in again")
	return err
}

// quiesceCurrent refuses while an account lease on the current database is live, then
// checkpoints its write-ahead log.
func quiesceCurrent(ctx context.Context, path string, now time.Time) error {
	cur, err := sqlstore.OpenSQLite(ctx, path)
	if err != nil {
		return err
	}
	defer cur.Close()
	n, err := cur.LiveLeases(ctx, now)
	if err != nil && !isNoSuchTable(err) {
		return fmt.Errorf("restore: %w", err)
	}
	if n > 0 {
		return fmt.Errorf("restore: %d account lease(s) are live: %w", n, errServerRunning)
	}
	return cur.Checkpoint(ctx)
}

// isNoSuchTable reports a database without the leases table (an empty or very old one).
func isNoSuchTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// decryptBackup decrypts the backup into the new file tmp, checks it and readies it for its
// first start. It returns the schema version after migrating.
func decryptBackup(ctx context.Context, in, tmp string, ids []age.Identity, now time.Time) (int64, error) {
	src, err := os.Open(in)
	if err != nil {
		return 0, fmt.Errorf("restore: %w", err)
	}
	defer src.Close()
	dst, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, fmt.Errorf("restore: %w", err)
	}
	if err := decryptStream(dst, src, ids); err != nil {
		_ = dst.Close()
		return 0, fmt.Errorf("restore %s: %w", in, err)
	}
	if err := dst.Close(); err != nil {
		return 0, fmt.Errorf("restore: %w", err)
	}
	head := make([]byte, len(sqliteMagic))
	if f, err := os.Open(tmp); err == nil {
		_, _ = io.ReadFull(f, head)
		_ = f.Close()
	}
	if !bytes.Equal(head, sqliteMagic) {
		return 0, fmt.Errorf("restore %s: not a standalone (SQLite) backup; a MariaDB dump is restored with deploy.sh --restore", in)
	}
	st, err := sqlstore.OpenSQLite(ctx, tmp)
	if err != nil {
		return 0, err
	}
	defer st.Close()
	if err := st.IntegrityCheck(ctx); err != nil {
		return 0, fmt.Errorf("restore %s: %w", in, err)
	}
	cur, newest, err := st.SchemaVersions(ctx)
	if err != nil {
		return 0, fmt.Errorf("restore %s: not a cactusTrade database: %w", in, err)
	}
	switch {
	case cur < 1:
		return 0, fmt.Errorf("restore %s: not a cactusTrade database (no schema)", in)
	case cur > newest:
		return 0, fmt.Errorf("restore %s: schema %d is newer than this build's %d: restore it with that cactusTrade version or newer", in, cur, newest)
	}
	if err := st.Migrate(ctx); err != nil {
		return 0, err
	}
	if err := st.PrepareRestored(ctx, now); err != nil {
		return 0, err
	}
	if err := st.Checkpoint(ctx); err != nil {
		return 0, err
	}
	return newest, nil
}

// standaloneConfig is the configuration of a standalone command; MariaDB is refused (its
// backups are taken and restored by deploy.sh).
func standaloneConfig(dataDir string, getenv func(string) string, cmd string) (config, error) {
	var args []string
	if dataDir != "" {
		args = []string{"--data-dir", dataDir}
	}
	cfg, err := loadConfig(args, getenv)
	if err != nil {
		return cfg, err
	}
	if cfg.DBDriver != sqlstore.DriverSQLite {
		return cfg, fmt.Errorf("%s is for the standalone SQLite database; a MariaDB deployment uses ./deploy.sh --backup-now and --restore", cmd)
	}
	return cfg, nil
}

// tempName returns a new random file name in dir.
func tempName(dir, prefix, suffix string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return filepath.Join(dir, prefix+hex.EncodeToString(b)+suffix), nil
}

// removeDB removes a SQLite file and its side files (missing ones are fine).
func removeDB(path string) {
	for _, sfx := range []string{"", "-wal", "-shm", "-journal"} {
		_ = os.Remove(path + sfx)
	}
}
