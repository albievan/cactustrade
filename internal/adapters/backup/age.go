// Package backup is the app's backup folder (R48, ports.Backups): consistent copies of the
// database, age-encrypted to public keys (OD29) and written into the folder fixed at deploy
// time. It also holds the age helpers the backup, restore, encrypt and decrypt commands use.
package backup

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

// ErrNoRecipient means no age recipient was given: a backup is never written unencrypted.
var ErrNoRecipient = errors.New("no backup recipient (an age public key, age1…)")

// ParseRecipientsText parses age recipients, one per line (empty lines and # comments are
// ignored). Only X25519 and hybrid (post-quantum) public keys are accepted: no SSH keys, no
// plugins, no passphrases.
func ParseRecipientsText(text string) ([]age.Recipient, error) {
	if strings.TrimSpace(text) == "" {
		return nil, ErrNoRecipient
	}
	rs, err := age.ParseRecipients(strings.NewReader(text))
	if err != nil {
		return nil, err
	}
	for _, r := range rs {
		switch r.(type) {
		case *age.X25519Recipient, *age.HybridRecipient:
		default:
			return nil, fmt.Errorf("recipient type %T is not accepted (X25519 or hybrid only)", r)
		}
	}
	if len(rs) == 0 {
		return nil, ErrNoRecipient
	}
	return rs, nil
}

// ParseRecipients parses one recipient per entry (see ParseRecipientsText). An entry may
// not span lines.
func ParseRecipients(entries []string) ([]age.Recipient, error) {
	for _, e := range entries {
		if strings.ContainsAny(e, "\r\n") {
			return nil, errors.New("one recipient per entry")
		}
	}
	return ParseRecipientsText(strings.Join(entries, "\n"))
}

// Encrypt age-encrypts src into dst for the recipients.
func Encrypt(dst io.Writer, src io.Reader, rs []age.Recipient) error {
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

// Decrypt decrypts the age stream src into dst with the identities. A wrong identity, a
// stream that is not age, and a truncated or altered one all fail.
func Decrypt(dst io.Writer, src io.Reader, ids []age.Identity) error {
	r, err := age.Decrypt(src, ids...)
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	if _, err := io.Copy(dst, r); err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	return nil
}
