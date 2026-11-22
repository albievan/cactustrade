// Package system provides the real clock and the crypto random source.
package system

import (
	"crypto/rand"
	"encoding/base64"
	"time"

	"github.com/google/uuid"

	"github.com/albievan/cactustrade/internal/ports"
)

// Clock is the wall clock.
type Clock struct{}

var _ ports.Clock = Clock{}

// Now returns time.Now.
func (Clock) Now() time.Time { return time.Now() }

// Random draws from crypto/rand.
type Random struct{}

var _ ports.Random = Random{}

// Token returns 256 random bits, base64url without padding (43 characters).
func (Random) Token() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error (Go ≥ 1.24)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ID returns a random UUID (v4).
func (Random) ID() string { return uuid.NewString() }
