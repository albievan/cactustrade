package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Platform is the broker an account lives on.
type Platform string

// Platforms. eToro is the default and the only one cactusTrade trades; Trading 212 accounts
// are view-only (owner's decision 2026-11-23: read-only now, trading later).
const (
	PlatformEtoro      Platform = "etoro"
	PlatformTrading212 Platform = "trading212"
)

// ParsePlatform validates a platform name; "" is eToro.
func ParsePlatform(s string) (Platform, error) {
	switch Platform(s) {
	case "", PlatformEtoro:
		return PlatformEtoro, nil
	case PlatformTrading212:
		return PlatformTrading212, nil
	}
	return "", Invalid("platform", "etoro or trading212")
}

// Account is one broker account (one key pair, demo or real) that a user owns and may share
// with other users (owner's request, 2026-11-23).
type Account struct {
	ID        string
	Name      string
	OwnerID   string
	OwnerName string // filled on reads
	Mode      TradingMode
	Platform  Platform // "" reads as eToro
	// EnvKeys: the keys are the deployment's ETORO_DEMO_PUB / ETORO_DEMO_PK (or the legacy
	// stored demo pair), not a per-account pair.
	EnvKeys   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// OnPlatform is the account's platform ("" = eToro).
func (a Account) OnPlatform() Platform {
	if a.Platform == "" {
		return PlatformEtoro
	}
	return a.Platform
}

// Tradable reports whether cactusTrade may trade the account (or use it for anything but
// viewing): only eToro accounts. Everything else is view-only (ErrViewOnly).
func (a Account) Tradable() bool { return a.OnPlatform() == PlatformEtoro }

// EnvAccountID is the account that uses the deployment's environment keys.
const EnvAccountID = "env-demo"

// AccountAccess is what a user may do with an account.
type AccountAccess string

// Access levels, weakest first. The user's role still caps them: a viewer never trades.
const (
	AccessNone  AccountAccess = ""
	AccessView  AccountAccess = "view"  // portfolio, watchlist, limits, events
	AccessTrade AccountAccess = "trade" // also start/stop/kill the agent, edit the watchlist
	AccessOwner AccountAccess = "owner" // also keys, limits, sharing, rename, delete
)

func (a AccountAccess) rank() int {
	switch a {
	case AccessView:
		return 1
	case AccessTrade:
		return 2
	case AccessOwner:
		return 3
	}
	return 0
}

// AtLeast reports whether a grants at least b.
func (a AccountAccess) AtLeast(b AccountAccess) bool { return a.rank() >= b.rank() && a.rank() > 0 }

// Shareable reports whether a can be given to another user (owner cannot).
func (a AccountAccess) Shareable() bool { return a == AccessView || a == AccessTrade }

// AccountView is an account as one user sees it.
type AccountView struct {
	Account
	Access AccountAccess
}

// AccountShare gives a user access to someone else's account.
type AccountShare struct {
	AccountID string
	UserID    string
	Username  string // filled on reads
	Access    AccountAccess
	CreatedAt time.Time
}

// AccountKeyNames are the credential names of an account's key pair: for eToro the API key
// (x-api-key) and the user key (x-user-key); for Trading 212 the API key and the API secret
// (HTTP Basic user and password).
func AccountKeyNames(a Account) (apiKey, userKey CredentialName) {
	if a.OnPlatform() == PlatformTrading212 {
		return CredentialName("t212." + a.ID + ".api_key"), CredentialName("t212." + a.ID + ".api_secret")
	}
	if a.EnvKeys {
		return CredEtoroDemoAPIKey, CredEtoroDemoUserKey
	}
	return CredentialName("etoro." + a.ID + ".api_key"), CredentialName("etoro." + a.ID + ".user_key")
}

// ValidateAccountName checks a display name: 1–60 characters, no control characters.
func ValidateAccountName(s string) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if n < 1 || n > 60 {
		return "", Invalid("name", "1-60 characters")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", Invalid("name", "no control characters")
		}
	}
	return s, nil
}
