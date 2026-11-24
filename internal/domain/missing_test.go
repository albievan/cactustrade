package domain

import (
	"errors"
	"fmt"
	"testing"
)

func TestMissingCodesValid(t *testing.T) {
	seen := map[Missing]bool{}
	for _, c := range MissingCodes() {
		if !c.Valid() {
			t.Errorf("%q not valid", c)
		}
		if seen[c] {
			t.Errorf("%q listed twice", c)
		}
		seen[c] = true
	}
	if len(seen) != 9 {
		t.Errorf("got %d codes, want 9", len(seen))
	}
	for _, bad := range []Missing{"", "budget", "TRADING_BUDGET"} {
		if bad.Valid() {
			t.Errorf("%q valid", bad)
		}
	}
}

func TestMissingErrorKeepsTheRefusal(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		is      []error
		field   string // a *ValidationError with this field must still be found
		message string
	}{
		{
			"budget", MissingSetup(MissingTradingBudget, "Set a trading budget", Invalid("tradingBudgetCents", "Set a trading budget")),
			[]error{ErrInvalid},
			"tradingBudgetCents", "Set a trading budget",
		},
		{"keys", MissingSetup(MissingAccountKeys, "No keys", ErrCredentialsMissing), []error{ErrCredentialsMissing}, "", "No keys"},
		{"kill", MissingSetup(MissingKillReset, "Kill switch on", ErrConflict), []error{ErrConflict}, "", "Kill switch on"},
		{"no account", MissingSetup(MissingAccount, "No account", ErrNoAccount), []error{ErrNoAccount, ErrNotFound}, "", "No account"},
		{
			"wrapped twice", fmt.Errorf("app.X: %w", MissingSetup(MissingReconcile, "Not reconciled", ErrConflict)),
			[]error{ErrConflict},
			"", "app.X: Not reconciled",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, s := range tt.is {
				if !errors.Is(tt.err, s) {
					t.Errorf("errors.Is(%v) false", s)
				}
			}
			me, ok := errors.AsType[*MissingError](tt.err)
			if !ok || !me.What.Valid() {
				t.Fatalf("no MissingError in %v", tt.err)
			}
			if tt.field != "" {
				ve, ok := errors.AsType[*ValidationError](tt.err)
				if !ok || ve.Field != tt.field {
					t.Errorf("ValidationError %+v, want field %s", ve, tt.field)
				}
			}
			if tt.err.Error() != tt.message {
				t.Errorf("Error() = %q, want %q", tt.err.Error(), tt.message)
			}
		})
	}
}
