package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// TestUserManagementNeedsSession is PLAN 7.1.1 (F1): an admin API key can neither create,
// update nor delete users; a web session can.
func TestUserManagementNeedsSession(t *testing.T) {
	ctx := context.Background()
	viewerRole := domain.RoleViewer
	disabled := true
	for _, via := range []struct {
		name string
		key  bool
		want error
	}{
		{"session", false, nil},
		{"api key", true, domain.ErrForbidden},
	} {
		for _, op := range []string{"create", "update", "delete", "update self", "delete self"} {
			t.Run(via.name+" "+op, func(t *testing.T) {
				e := newEnv(t)
				other := e.user(t, "olivia", domain.RoleOperator)
				p := e.admin
				if via.key {
					p.Via, p.APIKeyID = ViaAPIKey, "key-1"
				}
				var err error
				switch op {
				case "create":
					_, err = e.svc.CreateUser(ctx, p, "mallory", "mallory password!", domain.RoleAdmin)
				case "update":
					_, err = e.svc.UpdateUser(ctx, p, other.UserID, UserUpdate{Role: &viewerRole})
				case "delete":
					err = e.svc.DeleteUser(ctx, p, other.UserID)
				case "update self":
					_, err = e.svc.UpdateUser(ctx, p, p.UserID, UserUpdate{Disabled: &disabled})
				case "delete self":
					err = e.svc.DeleteUser(ctx, p, p.UserID)
				}
				want := via.want
				if !via.key {
					switch op {
					case "update self":
						want = domain.ErrConflict // the last enabled admin stays
					case "delete self":
						want = domain.ErrConflict // you cannot delete yourself
					}
				}
				if want == nil && err != nil || want != nil && !errors.Is(err, want) {
					t.Fatalf("%s via %s: err = %v, want %v", op, via.name, err, want)
				}
				if via.key {
					if u, gerr := e.store.Users().GetByUsername(ctx, "olivia"); gerr != nil || u.Role != domain.RoleOperator {
						t.Fatalf("the key changed olivia: %+v %v", u, gerr)
					}
					if _, gerr := e.store.Users().GetByUsername(ctx, "mallory"); !errors.Is(gerr, domain.ErrNotFound) {
						t.Fatalf("the key created mallory: %v", gerr)
					}
				}
			})
		}
	}
}

// quietEnv is an eToro demo account of an operator, with keys, for the PLAN 7.1.2 / 7.1.3
// tests.
func quietEnv(t *testing.T) (*env, Principal, domain.AccountView) {
	t.Helper()
	ctx := context.Background()
	e := newEnv(t)
	op := e.user(t, "olivia", domain.RoleOperator)
	a, err := e.svc.CreateAccount(ctx, op, "Olivia demo", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetAccountKeys(ctx, op, a.ID, "api-1", "user-1"); err != nil {
		t.Fatal(err)
	}
	return e, op, a
}

// busy puts the account into one of the states in which it must not be deleted or have its
// keys changed.
func busy(t *testing.T, e *env, accountID, how string) {
	t.Helper()
	ctx := context.Background()
	at := e.clock.T
	switch how {
	case "running", "paused", "killed":
		_ = e.store.Settings().Set(ctx, agentStateKey(accountID), how, at)
	case "stopped, open position":
		if err := e.store.AgentPositions().Add(ctx, domain.AgentPosition{
			AccountID: accountID, PositionID: 77, OpenRequestID: "req-open", Instrument: 1001, Direction: domain.Long, OpenedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	case "stopped, order pending":
		if err := e.store.Orders().Create(ctx, domain.Order{
			RequestID: "req-1", AccountID: accountID, Mode: domain.ModeDemo, Kind: domain.OrderClose, Direction: domain.Long,
			Instrument: 1001, PositionID: 77, State: domain.OrderSent, CreatedAt: at, UpdatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
	case "killed, close pending":
		busy(t, e, accountID, "killed")
		busy(t, e, accountID, "stopped, open position")
		busy(t, e, accountID, "stopped, order pending")
	}
}

var busyStates = []string{"running", "paused", "killed", "stopped, open position", "stopped, order pending", "killed, close pending"}

// TestDeleteAccountOnlyWhenQuiet is PLAN 7.1.2 (F2): an account is deleted only while its
// agent is stopped, with no open agent position and no order that is not final.
func TestDeleteAccountOnlyWhenQuiet(t *testing.T) {
	ctx := context.Background()
	for _, how := range busyStates {
		t.Run(how, func(t *testing.T) {
			e, op, a := quietEnv(t)
			busy(t, e, a.ID, how)
			err := e.svc.DeleteAccount(ctx, op, a.ID)
			if !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("DeleteAccount = %v, want ErrConflict", err)
			}
			if me, ok := errors.AsType[*domain.MissingError](err); strings.HasPrefix(how, "killed") != (ok && me.What == domain.MissingKillReset) {
				t.Fatalf("killed → MissingKillReset, else none: %v", err)
			}
			if _, err := e.store.Accounts().Get(ctx, a.ID); err != nil {
				t.Fatalf("the account is gone: %v", err)
			}
		})
	}
	t.Run("stopped and quiet", func(t *testing.T) {
		e, op, a := quietEnv(t)
		if err := e.svc.DeleteAccount(ctx, op, a.ID); err != nil {
			t.Fatal(err)
		}
	})
}

// TestKeyChangeOnlyWhenQuiet is PLAN 7.1.3 (F3): an eToro account's keys change only while
// it is quiet, and a change leaves the account unreconciled.
func TestKeyChangeOnlyWhenQuiet(t *testing.T) {
	ctx := context.Background()
	for _, how := range busyStates {
		t.Run(how, func(t *testing.T) {
			e, op, a := quietEnv(t)
			busy(t, e, a.ID, how)
			if err := e.svc.SetAccountKeys(ctx, op, a.ID, "api-2", ""); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("SetAccountKeys = %v, want ErrConflict", err)
			}
			if err := e.svc.SetAccountCredentials(ctx, op, a.ID, map[string]string{"user_key": "user-2"}); !errors.Is(err, domain.ErrConflict) {
				t.Fatalf("SetAccountCredentials = %v, want ErrConflict", err)
			}
			ak, uk := domain.AccountKeyNames(a.Account)
			if v, _ := e.sec.Get(ctx, ak); v != "api-1" {
				t.Fatalf("api key changed to %q", v)
			}
			if v, _ := e.sec.Get(ctx, uk); v != "user-1" {
				t.Fatalf("user key changed to %q", v)
			}
		})
	}
	t.Run("stopped and quiet: stored, unreconciled", func(t *testing.T) {
		e, op, a := quietEnv(t)
		e.svc.setReconciled(ctx, a.Account, true, "")
		if !e.svc.Reconciled(a.ID).OK {
			t.Fatal("setup")
		}
		if err := e.svc.SetAccountKeys(ctx, op, a.ID, "api-2", ""); err != nil {
			t.Fatal(err)
		}
		if rc := e.svc.Reconciled(a.ID); rc.OK || !strings.Contains(rc.Reason, "keys changed") {
			t.Fatalf("after a key change: %+v", rc)
		}
	})
	t.Run("view-only platform: no agent, no check", func(t *testing.T) {
		e := newEnv(t)
		isa, err := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformTrading212, "ISA", domain.ModeReal)
		if err != nil {
			t.Fatal(err)
		}
		_ = e.store.Settings().Set(ctx, agentStateKey(isa.ID), string(domain.AgentRunning), e.clock.T)
		if err := e.svc.SetAccountKeys(ctx, e.admin, isa.ID, "k", "s"); err != nil {
			t.Fatal(err)
		}
	})
}

// TestLLMModelsNeedsSession is PLAN 7.1.8 (F8): listing models sends a key to a URL, so it is
// for an admin's web session only.
func TestLLMModelsNeedsSession(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.svc.d.LLM = &fakeReviewer{}
	req := LLMModelsRequest{Config: domain.LLMConfig{Provider: "openai"}, Key: "sk-typed"}
	api := e.admin
	api.Via, api.APIKeyID = ViaAPIKey, "key-1"
	if _, err := e.svc.LLMModels(ctx, api, req); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("API key: %v", err)
	}
	if ms, err := e.svc.LLMModels(ctx, e.admin, req); err != nil || len(ms) != 2 {
		t.Fatalf("session: %v %v", ms, err)
	}
}

// TestLLMKeyNeedsHTTPS is PLAN 7.1.9 (F9): an API key only travels over https, except to
// Ollama on this machine or localhost; metadata and link-local addresses are refused.
func TestLLMKeyNeedsHTTPS(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		provider domain.LLMProviderID
		baseURL  string
		key      *string
		want     error
	}{
		{"https with a key", "openai", "https://proxy.example/v1", sp("sk-1"), nil},
		{"http with a key", "openai", "http://proxy.example/v1", sp("sk-1"), domain.ErrInvalid},
		{"http remote ollama without a key", "ollama", "http://gpu-box:11434/v1", nil, nil},
		{"http remote ollama with a key", "ollama", "http://gpu-box:11434/v1", sp("k"), domain.ErrInvalid},
		{"local ollama with a key over http", "ollama_local", "http://host.docker.internal:11434/v1", sp("k"), nil},
		{"localhost with a key over http", "openai", "http://localhost:8080/v1", sp("sk-1"), nil},
		{"loopback with a key over http", "openai", "http://127.0.0.1:8080/v1", sp("sk-1"), nil},
		{"metadata address", "ollama", "http://169.254.169.254/v1", nil, domain.ErrInvalid},
		{"metadata address over https", "openai", "https://169.254.169.254/v1", sp("sk-1"), domain.ErrInvalid},
		{"aws ipv6 metadata", "ollama", "http://[fd00:ec2::254]/v1", nil, domain.ErrInvalid},
		{"gcp metadata name", "ollama", "http://metadata.google.internal/v1", nil, domain.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			e.svc.d.LLM = &fakeReviewer{}
			p := domain.LLMProfile{Name: "p", LLMConfig: domain.LLMConfig{Provider: tt.provider, Model: "m", BaseURL: tt.baseURL}}
			_, err := e.svc.SaveLLMProfile(ctx, e.admin, p, tt.key)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("SaveLLMProfile = %v, want %v", err, tt.want)
			}
			if tt.key != nil {
				_, err = e.svc.LLMModels(ctx, e.admin, LLMModelsRequest{Config: p.LLMConfig, Key: *tt.key})
				if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
					t.Fatalf("LLMModels = %v, want %v", err, tt.want)
				}
			}
		})
	}
}

// TestActiveReviewerKeyOverHTTPFailsClosed is PLAN 7.1.9: a profile stored before the https
// rule, with a key and a plain-http address, is not used (the agent then rejects the trade).
func TestActiveReviewerKeyOverHTTPFailsClosed(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	legacy := domain.LLMProfile{ID: "p1", Name: "old", LLMConfig: domain.LLMConfig{Provider: "openai", Model: "m", BaseURL: "http://proxy.example/v1"}}
	if err := e.svc.saveLLM(ctx, domain.LLMSettings{Enabled: true, Active: "p1"}, []domain.LLMProfile{legacy}); err != nil {
		t.Fatal(err)
	}
	if err := e.sec.Put(ctx, domain.LLMKeyName("p1"), "sk-old"); err != nil {
		t.Fatal(err)
	}
	if _, key, on, err := e.svc.ActiveReviewer(ctx); err == nil || !on || key != "" {
		t.Fatalf("ActiveReviewer = key %q, on %v, err %v; want an error and no key", key, on, err)
	}
}

// TestSetMyProfileNeedsPassword is PLAN 7.1.11 (F11): a new e-mail address needs the current
// password; notification choices alone need none; a password sent along is always checked.
func TestSetMyProfileNeedsPassword(t *testing.T) {
	ctx := context.Background()
	notify := domain.NotifyPrefs{domain.NotifyAlerts: true}
	tests := []struct {
		name      string
		email, pw string
		want      error
		field     string
		stored    string
	}{
		{"same address, no password", "vera@example.com", "", nil, "", "vera@example.com"},
		{"new address, no password", "mallory@example.com", "", domain.ErrInvalid, "currentPassword", "vera@example.com"},
		{"new address, wrong password", "mallory@example.com", "wrong password!", domain.ErrInvalid, "currentPassword", "vera@example.com"},
		{"same address, wrong password", "vera@example.com", "wrong password!", domain.ErrInvalid, "currentPassword", "vera@example.com"},
		{"new address, right password", "vera2@example.com", "vera password!", nil, "", "vera2@example.com"},
		{"no address, right password", "", "vera password!", nil, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			vera := e.user(t, "vera", domain.RoleOperator)
			if _, err := e.svc.SetMyProfile(ctx, vera, Profile{Email: "vera@example.com"}, "vera password!"); err != nil {
				t.Fatal(err)
			}
			_, err := e.svc.SetMyProfile(ctx, vera, Profile{Email: tt.email, Notify: notify}, tt.pw)
			if tt.want == nil && err != nil || tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			if ve, ok := errors.AsType[*domain.ValidationError](err); tt.field != "" && (!ok || ve.Field != tt.field) {
				t.Fatalf("field: %v", err)
			}
			if p, _ := e.svc.MyProfile(ctx, vera); p.Email != tt.stored {
				t.Fatalf("stored %q, want %q", p.Email, tt.stored)
			}
		})
	}
}
