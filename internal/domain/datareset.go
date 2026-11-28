package domain

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Reset all trading data (PLAN R49). An admin can wipe every account's orders, agent
// positions, trades, decisions, LLM calls, equity snapshots, events and LLM reviews (OD39:
// all accounts), after an encrypted backup (R48). The reset is refused unless nothing of
// cactusTrade's can be in flight: every agent stopped or killed, no other process holding a
// lease, no real-money gate armed, every eToro account with keys freshly reconciled with no
// open position and no order that is not final, and no order younger than DataResetQuiet.
// The rules are pure: the app gathers the facts.

// DataResetPhrase is what an admin types to reset all trading data. It is not "RESET ALL
// DATA", which is the kill-switch reset phrase of an account named "ALL DATA".
const DataResetPhrase = "DELETE ALL TRADING DATA"

// SettingDataResetAt is the settings key holding the time of the latest data reset (RFC
// 3339, UTC). Like SettingRestoreAt, a real-money gate armed before it is not in force.
const SettingDataResetAt = "data_reset.at"

// SettingHistorySincePrefix starts the settings key of an account's trade-history floor
// (OD38): eToro's trades closed before the reset are not read back in.
const SettingHistorySincePrefix = "history.since."

// HistorySinceKey is the settings key of the account's trade-history floor (OD38).
func HistorySinceKey(accountID string) string { return SettingHistorySincePrefix + accountID }

// DataResetWipedTables names the tables a reset empties (the watchlist only when asked): a
// new slice on every call. The SQL adapter's list must equal it (TestResetWipedMatchesDomain).
func DataResetWipedTables() []string {
	return []string{"agent_positions", "decisions", "equity_snapshots", "events", "llm_calls", "orders", "reviews", "trades"}
}

// DisarmDataReset is the reason recorded on a real-money gate locked by a data reset.
const DisarmDataReset = "all trading data was reset"

// DataResetQuiet is how long after the newest order a reset is refused (CLAUDE.md §2.7: an
// order younger than this may still be unknown to the broker's order lookup, and its row is
// what adopts the position it opens).
const DataResetQuiet = 15 * time.Minute

// Data reset check names, in checklist order.
const (
	CheckResetAgents       = "reset_agents_stopped"
	CheckResetLeases       = "reset_no_live_lease"
	CheckResetGates        = "reset_no_armed_gate"
	CheckResetReconciled   = "reset_reconciled"
	CheckResetPositions    = "reset_no_open_position"
	CheckResetOrders       = "reset_no_open_order"
	CheckResetRecentOrders = "reset_no_recent_order"
	CheckResetBackup       = "reset_backup_ready"
)

// ResetAccountFacts is one account as the data reset sees it.
type ResetAccountFacts struct {
	AccountID string
	Name      string
	// MustReconcile: an eToro account with keys, which must have reconciled OK just now.
	MustReconcile bool
	Reconciled    bool
	Reason        string // why it is not reconciled ("" when it is)
	// OpenPositions counts cactusTrade's positions not closed yet, OpenOrders its orders that
	// are not final, RecentOrders its orders created less than DataResetQuiet ago (every
	// platform: Trading 212's orders live in the same tables).
	OpenPositions, OpenOrders, RecentOrders int
}

// DataResetFacts is everything the data reset's checks look at.
type DataResetFacts struct {
	// Agents is each account's agent state, by account id.
	Agents map[string]AgentState
	// LiveLeases counts the account leases another process holds and that have not expired.
	LiveLeases int
	// ArmedGates names the real-money accounts whose gate is stored armed (in force or not).
	ArmedGates []string
	Accounts   []ResetAccountFacts
	// BackupReady: a backup can be taken now (a backup folder and an age recipient); else
	// BackupWhy says why not.
	BackupReady bool
	BackupWhy   string
}

// DataResetChecks is the data reset's checklist (PLAN R49.1), named like GateChecks: every
// check must pass before anything is backed up or wiped. A killed agent passes (it trades
// nothing until its kill is reset, and the reset keeps the kill).
func DataResetChecks(f DataResetFacts) []GateCheck {
	names := map[string]string{}
	for _, a := range f.Accounts {
		names[a.AccountID] = a.Name
	}
	name := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
	c := func(check string, bad []string, ok, prefix string) GateCheck {
		if len(bad) == 0 {
			return GateCheck{Name: check, Pass: true, Detail: ok}
		}
		sort.Strings(bad)
		return GateCheck{Name: check, Detail: prefix + strings.Join(bad, "; ")}
	}
	var agents []string
	for id, st := range f.Agents {
		if st == AgentRunning || st == AgentPaused {
			agents = append(agents, fmt.Sprintf("%s is %s", name(id), st))
		}
	}
	var leases []string
	if f.LiveLeases > 0 {
		leases = append(leases, fmt.Sprintf("%d account lease(s) held by another cactusTrade process", f.LiveLeases))
	}
	gates := append([]string(nil), f.ArmedGates...)
	var unrec, pos, ord, recent []string
	for _, a := range f.Accounts {
		if a.MustReconcile && !a.Reconciled {
			why := a.Reason
			if why == "" {
				why = "not reconciled"
			}
			unrec = append(unrec, a.Name+": "+why)
		}
		if a.OpenPositions > 0 {
			pos = append(pos, fmt.Sprintf("%s: %d open", a.Name, a.OpenPositions))
		}
		if a.OpenOrders > 0 {
			ord = append(ord, fmt.Sprintf("%s: %d not final", a.Name, a.OpenOrders))
		}
		if a.RecentOrders > 0 {
			recent = append(recent, fmt.Sprintf("%s: %d", a.Name, a.RecentOrders))
		}
	}
	var backup []string
	if !f.BackupReady {
		why := f.BackupWhy
		if why == "" {
			why = "a backup cannot be taken"
		}
		backup = append(backup, why)
	}
	return []GateCheck{
		c(CheckResetAgents, agents, "every agent is stopped or killed", "stop these agents first: "),
		c(CheckResetLeases, leases, "no other process holds an account", ""),
		c(CheckResetGates, gates, "no real-money gate is armed", "disarm real trading first on: "),
		c(CheckResetReconciled, unrec, "every eToro account with keys is reconciled", "not reconciled with the broker: "),
		c(CheckResetPositions, pos, "cactusTrade has no open position", "close cactusTrade's positions first (Force close all): "),
		c(CheckResetOrders, ord, "cactusTrade has no order in flight", "wait for these orders to finish: "),
		c(CheckResetRecentOrders, recent, "no order in the last 15 minutes", "orders in the last 15 minutes (wait until they are older): "),
		c(CheckResetBackup, backup, "an encrypted backup can be taken", "no backup can be taken: "),
	}
}

// DataResetRefusal is a data reset refused by failing checks. It matches ErrConflict (the
// system's state does not allow it).
type DataResetRefusal struct {
	Failed []GateCheck
}

func (e *DataResetRefusal) Error() string {
	names := make([]string, 0, len(e.Failed))
	for _, c := range e.Failed {
		names = append(names, c.Name+" ("+c.Detail+")")
	}
	return "all trading data cannot be reset: " + strings.Join(names, "; ")
}

// Unwrap makes the refusal match ErrConflict.
func (e *DataResetRefusal) Unwrap() error { return ErrConflict }

// CanResetData is nil when every check passes; otherwise (or with no checks at all) a
// *DataResetRefusal naming the failing checks.
func CanResetData(checks []GateCheck) error {
	var failed []GateCheck
	for _, c := range checks {
		if !c.Pass {
			failed = append(failed, c)
		}
	}
	if len(failed) > 0 || len(checks) == 0 {
		return &DataResetRefusal{Failed: failed}
	}
	return nil
}
