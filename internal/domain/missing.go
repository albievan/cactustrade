package domain

// Missing names a piece of setup an action needs and does not have yet (R22): the web UI
// shows a button next to the message that leads to the fix. The server decides what is
// missing and whether the caller may fix it; the web UI only renders it.
type Missing string

// What can be missing.
const (
	// MissingTradingBudget: the account's trading budget is 0 (Risk limits).
	MissingTradingBudget Missing = "trading_budget"
	// MissingAccountKeys: the account's platform keys are not set (Platform accounts).
	MissingAccountKeys Missing = "account_keys"
	// MissingAccount: the caller has no eToro account yet (Platform accounts).
	MissingAccount Missing = "account"
	// MissingWatchlist: the account's watchlist is empty, so the agent has nothing to trade.
	MissingWatchlist Missing = "watchlist"
	// MissingReconcile: the account is not reconciled with eToro (run a reconciliation).
	MissingReconcile Missing = "reconcile"
	// MissingKillReset: the kill switch is on and an admin must reset it.
	MissingKillReset Missing = "kill_reset"
	// MissingLLMProfile: reviewing is on without a usable default profile, or a profile has
	// no API key (LLM reviewer).
	MissingLLMProfile Missing = "llm_profile"
	// MissingEmailServer: no mail server is set up (E-mail).
	MissingEmailServer Missing = "email_server"
	// MissingPublicURL: CACTUSTRADE_PUBLIC_URL is not set (a deployment setting; no page).
	MissingPublicURL Missing = "public_url"
)

// MissingCodes lists every Missing value, in a stable order.
func MissingCodes() []Missing {
	return []Missing{
		MissingTradingBudget, MissingAccountKeys, MissingAccount, MissingWatchlist, MissingReconcile,
		MissingKillReset, MissingLLMProfile, MissingEmailServer, MissingPublicURL,
	}
}

// Valid reports whether m is one of MissingCodes.
func (m Missing) Valid() bool {
	for _, c := range MissingCodes() {
		if m == c {
			return true
		}
	}
	return false
}

// MissingError is a refusal because setup is missing. It wraps the error the refusal had
// before (a sentinel such as ErrConflict, or a *ValidationError), so every errors.Is and
// errors.As check — and with them the HTTP status — stays the same. Msg is the message for
// people, without field names; CanFix says whether the caller may fix it (set by the use case
// that refused, which knows the caller).
type MissingError struct {
	What   Missing
	Msg    string
	CanFix bool
	Err    error
}

func (e *MissingError) Error() string { return e.Msg }

// Unwrap returns the refusal this error explains.
func (e *MissingError) Unwrap() error { return e.Err }

// MissingSetup builds a *MissingError for what, explained by msg, wrapping err.
func MissingSetup(what Missing, msg string, err error) error {
	return &MissingError{What: what, Msg: msg, Err: err}
}
