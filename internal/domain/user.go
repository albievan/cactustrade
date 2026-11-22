package domain

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// User is a person who signs in to the web UI.
type User struct {
	ID           string
	Username     string
	Role         Role
	PasswordHash string
	Disabled     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Password length bounds. Long passphrases are encouraged; the upper bound only stops
// hashing abuse.
const (
	MinPasswordLen = 12
	MaxPasswordLen = 128
)

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

// NormalizeUsername lower-cases and trims a username.
func NormalizeUsername(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ValidateUsername checks a normalised username: 2–32 of a-z 0-9 . _ - starting alphanumeric.
func ValidateUsername(s string) error {
	if !usernameRE.MatchString(s) {
		return Invalid("username", "2-32 characters: a-z, 0-9, '.', '_' or '-', starting with a letter or digit")
	}
	return nil
}

// ValidatePassword checks length (in characters) and that it is not the username.
func ValidatePassword(password, username string) error {
	n := utf8.RuneCountInString(password)
	switch {
	case n < MinPasswordLen:
		return Invalid("password", "must be at least 12 characters")
	case n > MaxPasswordLen:
		return Invalid("password", "must be at most 128 characters")
	case strings.EqualFold(strings.TrimSpace(password), username):
		return Invalid("password", "must not be the username")
	}
	return nil
}
