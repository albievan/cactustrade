// Package domain holds cactusTrade's business types and rules. It is pure Go: no I/O, no
// clock (time is always passed in), no imports outside the standard library.
package domain

import (
	"errors"
	"fmt"
	"time"
)

// Sentinel errors. Adapters map them to transport errors (problem+json) at the edge.
var (
	// ErrNotFound means the thing asked for does not exist (or is not visible to the caller).
	ErrNotFound = errors.New("not found")
	// ErrNoAccount means the caller has no eToro account yet (none owned, none shared); it
	// wraps ErrNotFound.
	ErrNoAccount = fmt.Errorf("no eToro account: %w", ErrNotFound)
	// ErrViewOnly means the account is on a platform cactusTrade only shows (Trading 212,
	// Saxo, Tiger Brokers): no agent, scanner, watchlist, themes, limits or orders. It wraps
	// ErrForbidden.
	ErrViewOnly = fmt.Errorf("view-only account (cactusTrade trades eToro accounts only): %w", ErrForbidden)
	// ErrReconnect means a platform's sign-in has lapsed (Saxo's refresh token expired or was
	// refused, or its 24-hour developer token is too old): nothing is read until the owner
	// connects again. It comes wrapped together with ErrUnavailable.
	ErrReconnect = errors.New("sign-in expired")
	// ErrConflict means the request clashes with the current state (duplicate, last admin…).
	ErrConflict = errors.New("conflict")
	// ErrIllegalTransition means an order was asked to move to a state its lifecycle does
	// not allow (or to be sent too often); it wraps ErrConflict.
	ErrIllegalTransition = fmt.Errorf("illegal order transition: %w", ErrConflict)
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
	// ErrOutcomeUnknown means a request that changes the account was (or may have been) sent,
	// but its answer was lost: a network error, a timeout, a 5xx or an unreadable 2xx. The
	// order may or may not exist at eToro; only a lookup can tell, and it is never resent
	// blindly (OD2). It wraps ErrUnavailable.
	ErrOutcomeUnknown = fmt.Errorf("broker outcome unknown: %w", ErrUnavailable)
	// ErrNotAccepted means the broker answered and did not accept the request (a 4xx,
	// including 429). Use errors.AsType[*NotAcceptedError] for the details. It wraps
	// ErrUnavailable.
	ErrNotAccepted = fmt.Errorf("not accepted by the broker: %w", ErrUnavailable)
	// ErrRealModeGated means an order for a real-money account was refused: real trading is
	// behind the phase-7 gate. It wraps ErrForbidden.
	ErrRealModeGated = fmt.Errorf("real mode is gated (phase 7): only demo accounts trade: %w", ErrForbidden)
)

// NotAcceptedError is a broker's refusal of a request that changes the account. Nothing was
// done at the broker. errors.Is(err, ErrNotAccepted) is true, and so is errors.Is for the
// broker's own error it wraps (e.g. ErrNotFound for an unknown position).
type NotAcceptedError struct {
	// RateLimited: the broker answered 429 before processing the request. Only then may an
	// open order be sent again, with the same request id, after RetryAfter (OD2).
	RateLimited bool
	RetryAfter  time.Duration
	Reason      string // the broker's message (never contains a key)
	Err         error  // the underlying broker error, if any
}

func (e *NotAcceptedError) Error() string {
	s := "not accepted by the broker"
	if e.RateLimited {
		s += fmt.Sprintf(" (rate limited, retry after %s)", e.RetryAfter)
	}
	if e.Reason != "" {
		s += ": " + e.Reason
	}
	return s
}

// Unwrap makes the error match ErrNotAccepted and the broker error it carries.
func (e *NotAcceptedError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrNotAccepted}
	}
	return []error{ErrNotAccepted, e.Err}
}

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
