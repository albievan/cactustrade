package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
)

// Backups are age-encrypted (OD29, PLAN 7.6): to one or more recipients (public keys, so the
// machine that takes the backup cannot read it), decrypted with the matching identity file,
// which belongs off this machine. The master key is never part of a backup.

const (
	envBackupRecipient     = "CACTUSTRADE_BACKUP_RECIPIENT"
	envBackupRecipientFile = "CACTUSTRADE_BACKUP_RECIPIENT_FILE"
	envBackupIdentityFile  = "CACTUSTRADE_BACKUP_IDENTITY_FILE"
)

var errNoRecipient = errors.New("backups are encrypted: set " + envBackupRecipient +
	" (an age public key, age1…; several separated by spaces), " + envBackupRecipientFile + ", or pass --recipient")

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, " ") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// backupRecipients returns the age recipients from --recipient flags, else from
// CACTUSTRADE_BACKUP_RECIPIENT, else from the file CACTUSTRADE_BACKUP_RECIPIENT_FILE.
// None is an error: an app backup is never written unencrypted (OD29).
func backupRecipients(flags []string, getenv func(string) string) ([]age.Recipient, error) {
	text := strings.Join(flags, "\n")
	if text == "" {
		text = strings.Join(strings.FieldsFunc(getenv(envBackupRecipient), func(r rune) bool {
			return r == ',' || r == ' ' || r == '\t' || r == '\n'
		}), "\n")
	}
	if text == "" {
		if f := getenv(envBackupRecipientFile); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", envBackupRecipientFile, err)
			}
			text = string(b)
		}
	}
	if strings.TrimSpace(text) == "" {
		return nil, errNoRecipient
	}
	rs, err := age.ParseRecipients(strings.NewReader(text))
	if err != nil {
		return nil, fmt.Errorf("backup recipient: %w", err)
	}
	return rs, nil
}

// backupIdentities reads the age identities (private keys) from path, or from
// CACTUSTRADE_BACKUP_IDENTITY_FILE when path is empty.
func backupIdentities(path string, getenv func(string) string) ([]age.Identity, error) {
	if path == "" {
		path = getenv(envBackupIdentityFile)
	}
	if path == "" {
		return nil, errors.New("decrypting a backup needs its age identity file: --identity FILE or " + envBackupIdentityFile)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("backup identity: %w", err)
	}
	defer f.Close()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("backup identity %s: %w", path, err)
	}
	return ids, nil
}

// encryptStream age-encrypts src into dst for the recipients.
func encryptStream(dst io.Writer, src io.Reader, rs []age.Recipient) error {
	w, err := age.Encrypt(dst, rs...)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	if _, err := io.Copy(w, src); err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	return nil
}

// decryptStream decrypts the age file src into dst with the identities. A wrong identity, a
// file that is not age, and a truncated or altered file all fail.
func decryptStream(dst io.Writer, src io.Reader, ids []age.Identity) error {
	r, err := age.Decrypt(src, ids...)
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	if _, err := io.Copy(dst, r); err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	return nil
}

// encryptCmd is `cactustrade encrypt [--recipient age1…]…`: stdin → age → stdout. deploy.sh
// pipes the MariaDB dump through it (PLAN 7.6b).
func encryptCmd(args []string, getenv func(string) string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("encrypt", flag.ContinueOnError)
	var rflags multiFlag
	fs.Var(&rflags, "recipient", "age public key (age1…); repeatable; default "+envBackupRecipient)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("encrypt: unexpected argument %q", fs.Arg(0))
	}
	rs, err := backupRecipients(rflags, getenv)
	if err != nil {
		return err
	}
	return encryptStream(out, in, rs)
}

// decryptCmd is `cactustrade decrypt --identity FILE`: stdin (age) → stdout. With
// `--identity -` stdin starts with the identity, ended by an empty line, and the age file
// follows: deploy.sh uses that to decrypt in a container or pod without the private key ever
// being a file there, an argument or an environment variable.
func decryptCmd(args []string, getenv func(string) string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("decrypt", flag.ContinueOnError)
	idFile := fs.String("identity", "", "age identity file (the private key), or - for stdin; default "+envBackupIdentityFile)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("decrypt: unexpected argument %q", fs.Arg(0))
	}
	if *idFile == "-" {
		br := bufio.NewReader(in)
		var block strings.Builder
		for {
			line, err := br.ReadString('\n')
			if strings.TrimSpace(line) == "" && err == nil {
				break
			}
			block.WriteString(line)
			if err != nil {
				return errors.New("decrypt: --identity - needs the identity, an empty line, then the age file on stdin")
			}
		}
		ids, err := age.ParseIdentities(strings.NewReader(block.String()))
		if err != nil {
			return fmt.Errorf("backup identity (stdin): %w", err)
		}
		return decryptStream(out, br, ids)
	}
	ids, err := backupIdentities(*idFile, getenv)
	if err != nil {
		return err
	}
	return decryptStream(out, in, ids)
}
