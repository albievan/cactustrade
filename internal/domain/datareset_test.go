package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestDataResetChecks(t *testing.T) {
	ok := func() DataResetFacts {
		return DataResetFacts{
			Agents: map[string]AgentState{"a1": AgentStopped, "a2": AgentKilled, "t1": AgentStopped},
			Accounts: []ResetAccountFacts{
				{AccountID: "a1", Name: "Demo", MustReconcile: true, Reconciled: true},
				{AccountID: "a2", Name: "Second", MustReconcile: true, Reconciled: true},
				{AccountID: "t1", Name: "Practice 212"}, // Trading 212: no eToro reconciliation
				{AccountID: "x1", Name: "No keys"},
			},
			BackupReady: true,
		}
	}
	tests := []struct {
		name   string
		edit   func(*DataResetFacts)
		failed string // the one failing check ("" = all pass)
		detail string // part of its detail
	}{
		{"all pass, a killed agent allowed", func(*DataResetFacts) {}, "", ""},
		{"running agent", func(f *DataResetFacts) { f.Agents["a1"] = AgentRunning }, CheckResetAgents, "Demo is running"},
		{"paused agent", func(f *DataResetFacts) { f.Agents["t1"] = AgentPaused }, CheckResetAgents, "Practice 212 is paused"},
		{"agent of an unknown account named by id", func(f *DataResetFacts) { f.Agents["zz"] = AgentRunning }, CheckResetAgents, "zz is running"},
		{"live lease", func(f *DataResetFacts) { f.LiveLeases = 2 }, CheckResetLeases, "2 account lease(s)"},
		{"armed gate", func(f *DataResetFacts) { f.ArmedGates = []string{"Real Money Maker"} }, CheckResetGates, "Real Money Maker"},
		{"unreconciled", func(f *DataResetFacts) {
			f.Accounts[1].Reconciled, f.Accounts[1].Reason = false, "reading the portfolio failed"
		}, CheckResetReconciled, "Second: reading the portfolio failed"},
		{"unreconciled without a reason", func(f *DataResetFacts) { f.Accounts[0].Reconciled = false }, CheckResetReconciled, "Demo: not reconciled"},
		{"open position", func(f *DataResetFacts) { f.Accounts[0].OpenPositions = 3 }, CheckResetPositions, "Demo: 3 open"},
		{"open position on Trading 212", func(f *DataResetFacts) { f.Accounts[2].OpenPositions = 1 }, CheckResetPositions, "Practice 212: 1 open"},
		{"open order", func(f *DataResetFacts) { f.Accounts[1].OpenOrders = 1 }, CheckResetOrders, "Second: 1 not final"},
		{"order younger than 15 min", func(f *DataResetFacts) { f.Accounts[2].RecentOrders = 4 }, CheckResetRecentOrders, "Practice 212: 4"},
		{"no recipient", func(f *DataResetFacts) { f.BackupReady, f.BackupWhy = false, "add an age public key first" }, CheckResetBackup, "add an age public key first"},
		{"no backup without a reason", func(f *DataResetFacts) { f.BackupReady = false }, CheckResetBackup, "cannot be taken"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := ok()
			tc.edit(&f)
			checks := DataResetChecks(f)
			if len(checks) != 8 {
				t.Fatalf("%d checks", len(checks))
			}
			var failed []GateCheck
			for _, c := range checks {
				if c.Detail == "" {
					t.Fatalf("check %s has no detail", c.Name)
				}
				if !c.Pass {
					failed = append(failed, c)
				}
			}
			err := CanResetData(checks)
			if tc.failed == "" {
				if len(failed) != 0 || err != nil {
					t.Fatalf("failed %+v, err %v", failed, err)
				}
				return
			}
			if len(failed) != 1 || failed[0].Name != tc.failed || !strings.Contains(failed[0].Detail, tc.detail) {
				t.Fatalf("failed = %+v, want %s containing %q", failed, tc.failed, tc.detail)
			}
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("err = %v", err)
			}
			r, ok := errors.AsType[*DataResetRefusal](err)
			if !ok || len(r.Failed) != 1 || !strings.Contains(err.Error(), tc.failed) {
				t.Fatalf("refusal = %v", err)
			}
		})
	}
}

func TestCanResetDataNoChecks(t *testing.T) {
	if err := CanResetData(nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("no checks = %v", err)
	}
}

func TestDataResetConstants(t *testing.T) {
	if DataResetPhrase != "DELETE ALL TRADING DATA" || resetPhraseClash(DataResetPhrase) {
		t.Fatal("phrase")
	}
	if HistorySinceKey("abc") != "history.since.abc" || SettingDataResetAt != "data_reset.at" {
		t.Fatal("keys")
	}
	if DisarmDataReset == "" || len(DisarmDataReset) > MaxDisarmReason {
		t.Fatal("disarm reason")
	}
}

// resetPhraseClash reports whether phrase equals a kill-switch reset phrase ("RESET <name>")
// or an arming phrase: the data reset's phrase must be neither.
func resetPhraseClash(phrase string) bool {
	return strings.HasPrefix(phrase, "RESET ") || strings.HasPrefix(phrase, "ENABLE REAL TRADING ")
}

func TestDataResetWipedTables(t *testing.T) {
	a := DataResetWipedTables()
	a[0] = "users"
	if b := DataResetWipedTables(); b[0] != "agent_positions" || len(b) != 8 {
		t.Fatalf("shared slice: %v", b)
	}
}
