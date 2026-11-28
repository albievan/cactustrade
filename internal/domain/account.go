package domain

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Platform is the broker an account lives on.
type Platform string

// Platforms. eToro is the default and the only one cactusTrade trades. Trading 212, Saxo and
// Tiger Brokers accounts are view-only (owner's decisions 2026-11-23: Trading 212 read-only
// now, trading later; "Saxo and Tiger, view-only").
const (
	PlatformEtoro      Platform = "etoro"
	PlatformTrading212 Platform = "trading212"
	PlatformSaxo       Platform = "saxo"
	PlatformTiger      Platform = "tiger"
)

// Platforms lists every platform, eToro first. Do not modify.
var Platforms = []Platform{PlatformEtoro, PlatformTrading212, PlatformSaxo, PlatformTiger}

// ParsePlatform validates a platform name; "" is eToro.
func ParsePlatform(s string) (Platform, error) {
	if s == "" {
		return PlatformEtoro, nil
	}
	for _, p := range Platforms {
		if string(p) == s {
			return p, nil
		}
	}
	return "", Invalid("platform", "etoro, trading212, saxo or tiger")
}

// Label names the platform for people.
func (p Platform) Label() string {
	switch p {
	case PlatformTrading212:
		return "Trading 212"
	case PlatformSaxo:
		return "Saxo"
	case PlatformTiger:
		return "Tiger Brokers"
	}
	return "eToro"
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
// viewing) through its eToro path: only eToro accounts. Trading 212 practice accounts trade
// through their own path (T212Trading, PLAN R45 T212-c); everything else is view-only
// (ErrViewOnly).
func (a Account) Tradable() bool { return a.OnPlatform() == PlatformEtoro }

// T212Trading reports whether cactusTrade may trade the account through its Trading 212 path:
// a Trading 212 practice (demo) account. A real Trading 212 account is never traded (owner's
// decision D41 g): it stays view-only whatever else is set.
func (a Account) T212Trading() bool {
	return a.OnPlatform() == PlatformTrading212 && a.Mode == ModeDemo
}

// Agentable reports whether the account can have an agent at all: an eToro account (Tradable)
// or a Trading 212 practice account (T212Trading).
func (a Account) Agentable() bool { return a.Tradable() || a.T212Trading() }

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

// AccountKeyNames are the credential names of an account's first two credentials: for eToro
// the API key (x-api-key) and the user key (x-user-key); for Trading 212 the API key and the
// API secret (HTTP Basic user and password). Saxo and Tiger accounts have more credentials:
// see PlatformCredentials and AccountCredentialName.
func AccountKeyNames(a Account) (apiKey, userKey CredentialName) {
	specs := PlatformCredentials(a.OnPlatform())
	k, _ := AccountCredentialName(a, specs[0].Key)
	u, _ := AccountCredentialName(a, specs[1].Key)
	return k, u
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
