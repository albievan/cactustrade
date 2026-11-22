// Package argon2 implements ports.PasswordHasher with argon2id (RFC 9106), encoded in the
// PHC string format: $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>.
package argon2

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/albievan/cactustrade/internal/ports"
)

// Params are the argon2id cost parameters.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
}

// Default is RFC 9106's second recommended option: 64 MiB, 3 passes.
var Default = Params{Memory: 64 * 1024, Time: 3, Threads: 2}

// Hasher hashes with fixed parameters and verifies any parameters found in a hash.
type Hasher struct{ p Params }

var _ ports.PasswordHasher = Hasher{}

// New returns a hasher with p.
func New(p Params) Hasher { return Hasher{p: p} }

var b64 = base64.RawStdEncoding

// Hash returns the encoded hash of password with a random 16-byte salt.
func (h Hasher) Hash(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("argon2: salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, h.p.Time, h.p.Memory, h.p.Threads, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, h.p.Memory, h.p.Time, h.p.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify checks password against an encoded hash in constant time.
func (h Hasher) Verify(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("argon2: not an argon2id hash")
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return false, errors.New("argon2: unsupported version")
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return false, errors.New("argon2: bad parameters")
	}
	if p.Memory > 1<<21 || p.Time > 16 || p.Threads == 0 { // refuse absurd costs (≤ 2 GiB)
		return false, errors.New("argon2: parameters out of range")
	}
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, errors.New("argon2: bad salt")
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 64 {
		return false, errors.New("argon2: bad hash")
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
