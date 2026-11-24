package domain

import (
	"strings"
	"time"
)

// StatementSchedule e-mails trading statements after every period (R30): the combined
// statement of its accounts and, as chosen, one per account and one per exchange, attached as
// PDFs to one e-mail per recipient.
type StatementSchedule struct {
	ID      string
	OwnerID string
	Period  StatementPeriod
	// AccountIDs are the accounts covered; empty = every account the owner may put on a
	// schedule, as it is when the statement is made.
	AccountIDs []string
	// Recipients are the addresses the statements go to (1 to MaxStatementRecipients). They
	// are typed by the owner: they need not be users of cactusTrade.
	Recipients []string
	// What each e-mail carries: the combined statement, one per account, one per exchange.
	IncludeCombined, IncludeAccounts, IncludeExchanges bool
	Enabled                                            bool
	// LastPeriod is the key of the newest period sent (or skipped) by the schedule, so a
	// period is never sent twice, across restarts too ("" = none yet).
	LastPeriod string
	LastSentAt time.Time // when LastPeriod was handled (zero = never)
	LastError  string    // why the last scheduled send failed ("" = it did not)
	// ManualSentAt is the last "Send now" (rate limit: one a minute per schedule).
	ManualSentAt time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Statement schedule bounds.
const (
	MaxStatementRecipients = 10
	MaxScheduleAccounts    = 50
	// ManualStatementInterval is the least time between two "Send now" of one schedule.
	ManualStatementInterval = time.Minute
)

// Normalize trims and lower-cases the recipients and drops empty entries; account ids are
// trimmed. Duplicates are kept for Validate to refuse.
func (s StatementSchedule) Normalize() StatementSchedule {
	rs := make([]string, 0, len(s.Recipients))
	for _, r := range s.Recipients {
		if r = strings.ToLower(strings.TrimSpace(r)); r != "" {
			rs = append(rs, r)
		}
	}
	ids := make([]string, 0, len(s.AccountIDs))
	for _, id := range s.AccountIDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	s.Recipients, s.AccountIDs = rs, ids
	return s
}

// Validate checks a normalised schedule.
func (s StatementSchedule) Validate() error {
	if !s.Period.Valid() {
		return Invalid("period", "daily, weekly or monthly")
	}
	switch n := len(s.Recipients); {
	case n == 0:
		return Invalid("recipients", "add at least one e-mail address")
	case n > MaxStatementRecipients:
		return Invalid("recipients", "at most 10 e-mail addresses")
	}
	seen := map[string]bool{}
	for _, r := range s.Recipients {
		if err := ValidateEmail("recipients", r); err != nil {
			return Invalid("recipients", r+" is not one plain e-mail address, such as you@example.com")
		}
		if seen[r] {
			return Invalid("recipients", r+" is listed twice")
		}
		seen[r] = true
	}
	if len(s.AccountIDs) > MaxScheduleAccounts {
		return Invalid("accountIds", "at most 50 accounts")
	}
	ids := map[string]bool{}
	for _, id := range s.AccountIDs {
		if len(id) > 64 || ids[id] {
			return Invalid("accountIds", "each account once")
		}
		ids[id] = true
	}
	if !s.IncludeCombined && !s.IncludeAccounts && !s.IncludeExchanges {
		return Invalid("include", "choose at least one statement: combined, per account or per exchange")
	}
	return nil
}
