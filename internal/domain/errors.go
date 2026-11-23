// Package domain holds cactusTrade's business types and rules. It is pure Go: no I/O, no
// clock (time is always passed in), no imports outside the standard library.
package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors. Adapters map them to transport errors (problem+json) at the edge.
var (
	// ErrNotFound means the thing asked for does not exist (or is not visible to the caller).
	ErrNotFound = errors.New("not found")
	// ErrNoAccount means the caller has no eToro account yet (none owned, none shared); it
	// wraps ErrNotFound.
	ErrNoAccount = fmt.Errorf("no eToro account: %w", ErrNotFound)
	// ErrConflict means the request clashes with the current state (duplicate, last admin…).
	ErrConflict = errors.New("conflict")
	// ErrUnauthenticated means no valid credentials were presented.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrForbidden means the caller is authenticated but lacks the permission.
	ErrForbidden = errors.New("forbidden")
	// ErrSetupDone means the first user was to be created but a user already exists.
	ErrSetupDone = errors.New("setup already completed")
	// ErrPasswordChangeRequired means the account still has the default password: it may
	// only change it or sign out.
	ErrPasswordChangeRequired = errors.New("password change required")
	// ErrCredentialsMissing means a needed credential (e.g. the eToro key pair of the
	// current mode) is not set. It always comes wrapped together with ErrUnavailable.
	ErrCredentialsMissing = errors.New("credentials not set")
	// ErrInvalid is wrapped by every ValidationError.
	ErrInvalid = errors.New("invalid input")
	// ErrUnavailable means a dependency (secret store, master key…) is not usable now.
	ErrUnavailable = errors.New("unavailable")
)

// ValidationError names the field that failed and why. errors.Is(err, ErrInvalid) is true.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return fmt.Sprintf("%s: %s", e.Field, e.Reason) }

// Unwrap makes every ValidationError match ErrInvalid.
func (e *ValidationError) Unwrap() error { return ErrInvalid }

// Invalid builds a ValidationError.
func Invalid(field, reason string) error { return &ValidationError{Field: field, Reason: reason} }
