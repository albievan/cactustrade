package domain

import "slices"

// Role is a user's or API key's access level. admin ⊃ operator ⊃ viewer.
type Role string

// Roles, from least to most privileged.
const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin    Role = "admin"
)

// Permission is one thing a principal may do.
type Permission string

// Permissions. Viewer: read-only. Operator: start/stop/kill switch. Admin: everything else
// (users, API keys of others, credentials, limits, trading budget, mode).
const (
	PermView             Permission = "view"              // dashboards, history, decision log
	PermAgentControl     Permission = "agent.control"     // start, pause, stop
	PermKillSwitch       Permission = "agent.kill"        // close everything and stop
	PermManageOwnAPIKeys Permission = "apikeys.own"       // create/revoke own API keys
	PermManageUsers      Permission = "users.manage"      // users, all API keys
	PermManageSecrets    Permission = "credentials.write" // eToro / LLM keys (write-only)
	PermManageSettings   Permission = "settings.write"    // limits, budget, watchlist
	PermSwitchMode       Permission = "mode.switch"       // demo ↔ real (gated, phase 7)
	PermViewAudit        Permission = "audit.view"        // audit log
)

var rolePerms = map[Role][]Permission{
	RoleViewer:   {PermView, PermManageOwnAPIKeys},
	RoleOperator: {PermView, PermManageOwnAPIKeys, PermAgentControl, PermKillSwitch},
	RoleAdmin: {
		PermView, PermManageOwnAPIKeys, PermAgentControl, PermKillSwitch, PermManageUsers,
		PermManageSecrets, PermManageSettings, PermSwitchMode, PermViewAudit,
	},
}

var roleRank = map[Role]int{RoleViewer: 1, RoleOperator: 2, RoleAdmin: 3}

// ParseRole validates a role name.
func ParseRole(s string) (Role, error) {
	r := Role(s)
	if _, ok := roleRank[r]; !ok {
		return "", Invalid("role", "must be admin, operator or viewer")
	}
	return r, nil
}

// Valid reports whether r is a known role.
func (r Role) Valid() bool { _, ok := roleRank[r]; return ok }

// Allows reports whether the role grants permission p. Unknown roles grant nothing.
func (r Role) Allows(p Permission) bool { return slices.Contains(rolePerms[r], p) }

// AtMost reports whether r is no more privileged than other (an API key can never exceed
// its owner). Unknown roles are never AtMost anything.
func (r Role) AtMost(other Role) bool {
	a, ok1 := roleRank[r]
	b, ok2 := roleRank[other]
	return ok1 && ok2 && a <= b
}

// Permissions lists what the role grants, in a stable order.
func (r Role) Permissions() []Permission { return slices.Clone(rolePerms[r]) }
