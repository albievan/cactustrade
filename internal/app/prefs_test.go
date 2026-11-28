package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

func rawPatch(kv ...string) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = json.RawMessage(kv[i+1])
	}
	return m
}

func TestMyPreferencesCallers(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	viewer := e.user(t, "vera", domain.RoleViewer)
	for _, tc := range []struct {
		name string
		p    Principal
		want error
	}{
		{"admin session", e.admin, nil},
		{"viewer session (no permission needed: own view choices)", viewer, nil},
		{"anonymous", Principal{}, domain.ErrUnauthenticated},
		{"API key", Principal{UserID: e.admin.UserID, Username: "admin", Role: domain.RoleAdmin, Via: ViaAPIKey, APIKeyID: "k1"}, domain.ErrForbidden},
		{"default password not yet changed", Principal{UserID: viewer.UserID, Role: domain.RoleViewer, Via: ViaSession, MustChangePassword: true}, domain.ErrPasswordChangeRequired},
	} {
		_, gerr := e.svc.MyPreferences(ctx, tc.p)
		_, serr := e.svc.SetMyPreferences(ctx, tc.p, rawPatch("dashboard.liveInvest.range", `"max"`))
		for _, err := range []error{gerr, serr} {
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
			}
		}
	}
}

func TestMyPreferencesOwnOnly(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	olga := e.user(t, "olga", domain.RoleOperator)

	if p, err := e.svc.MyPreferences(ctx, olga); err != nil || len(p) != 0 {
		t.Fatalf("never saved: %v %v", p, err)
	}
	steps := []struct {
		name  string
		who   Principal
		patch map[string]json.RawMessage
		want  string // the caller's stored preferences after the step; "" = refused
	}{
		{
			"admin sets max", e.admin, rawPatch("dashboard.liveInvest.range", `"max"`, "orders.feed.side", `"long"`),
			`{"dashboard.liveInvest.range":"max","orders.feed.side":"long"}`,
		},
		{
			"olga sets her own", olga, rawPatch("dashboard.liveInvest.range", `"week"`),
			`{"dashboard.liveInvest.range":"week"}`,
		},
		{
			"admin: last write wins, null deletes", e.admin, rawPatch("dashboard.liveInvest.range", `"year"`, "orders.feed.side", `null`),
			`{"dashboard.liveInvest.range":"year"}`,
		},
		{"admin: a bad key refuses everything", e.admin, rawPatch("history.days", `90`, "../olga", `"x"`), ""},
		{"admin: a nested value refuses everything", e.admin, rawPatch("history.days", `90`, "orders.table.sort", `{"a":{"b":1}}`), ""},
		{
			"admin: an object of scalars", e.admin, rawPatch("orders.table.sort", `{"key":"time","desc":true}`),
			`{"dashboard.liveInvest.range":"year","orders.table.sort":{"key":"time","desc":true}}`,
		},
	}
	for _, st := range steps {
		got, err := e.svc.SetMyPreferences(ctx, st.who, st.patch)
		if st.want == "" {
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("%s: %v, want ErrInvalid", st.name, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if enc, _ := got.Encode(); enc != st.want {
			t.Fatalf("%s: answered %s, want %s", st.name, enc, st.want)
		}
		again, _ := e.svc.MyPreferences(ctx, st.who)
		if enc, _ := again.Encode(); enc != st.want {
			t.Fatalf("%s: read back %s, want %s", st.name, enc, st.want)
		}
	}
	// each user sees only their own; stored under ui.prefs.<their id>
	if p, _ := e.svc.MyPreferences(ctx, olga); string(p["dashboard.liveInvest.range"]) != `"week"` || len(p) != 1 {
		t.Fatalf("olga's prefs %v", p)
	}
	if v, err := e.store.Settings().Get(ctx, "ui.prefs."+olga.UserID); err != nil || v != `{"dashboard.liveInvest.range":"week"}` {
		t.Fatalf("stored %q %v", v, err)
	}
	// not audited: view choices only
	for _, a := range e.store.AuditEntries() {
		if strings.Contains(a.Action, "pref") {
			t.Fatalf("audited: %+v", a)
		}
	}
}

func TestMyPreferencesSurvivesDataResetPrefixes(t *testing.T) {
	// R49's wiped settings prefixes must never match a preferences key.
	for _, wiped := range []string{"agent.aggressiveness_adapt.", "research.state.", "events.seen.", "watchlist.signals."} {
		if strings.HasPrefix(PrefsKeyPrefix, wiped) || strings.HasPrefix(wiped, PrefsKeyPrefix) {
			t.Fatalf("prefs prefix %q overlaps a reset prefix %q", PrefsKeyPrefix, wiped)
		}
	}
}

func TestMyPreferencesUnreadableAndStoreErrors(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// unreadable stored JSON: answered as none, and a write starts afresh
	if err := e.store.Settings().Set(ctx, prefsKey(e.admin.UserID), "[broken", e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if p, err := e.svc.MyPreferences(ctx, e.admin); err != nil || len(p) != 0 {
		t.Fatalf("unreadable: %v %v", p, err)
	}
	if p, err := e.svc.SetMyPreferences(ctx, e.admin, rawPatch("history.days", `7`)); err != nil || len(p) != 1 {
		t.Fatalf("write over unreadable: %v %v", p, err)
	}

	// a failing store: errors, nothing pretended
	boom := errors.New("disk on fire")
	for _, tc := range []struct {
		name           string
		getErr, setErr error
	}{
		{"read fails", boom, nil},
		{"write fails", nil, boom},
	} {
		st := &failingSettingsStore{Store: fakes.NewStore(), getErr: tc.getErr, setErr: tc.setErr}
		clk := &fakes.Clock{T: time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)}
		svc, err := New(Deps{Store: st, Secrets: &fakes.SecretStore{Now: clk.Now}, Hasher: fakes.Hasher{}, Random: &fakes.Random{}, Clock: clk}, Config{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.SetMyPreferences(ctx, e.admin, rawPatch("history.days", `7`)); !errors.Is(err, boom) {
			t.Errorf("%s: set %v", tc.name, err)
		}
		if tc.getErr != nil {
			if _, err := svc.MyPreferences(ctx, e.admin); !errors.Is(err, boom) {
				t.Errorf("%s: get %v", tc.name, err)
			}
		}
	}
}

// TestMyPreferencesConcurrentPatches: two tabs saving different keys at once both land.
func TestMyPreferencesConcurrentPatches(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	keys := []string{"a.one", "a.two", "a.three", "a.four", "a.five", "a.six", "a.seven", "a.eight"}
	for _, k := range keys {
		wg.Go(func() {
			if _, err := e.svc.SetMyPreferences(ctx, e.admin, rawPatch(k, `true`)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if p, _ := e.svc.MyPreferences(ctx, e.admin); len(p) != len(keys) {
		t.Fatalf("lost a write: %v", p)
	}
}

type failingSettingsStore struct {
	*fakes.Store
	getErr, setErr error
}

func (s *failingSettingsStore) Settings() ports.Settings { return failingSettings{s} }

type failingSettings struct{ s *failingSettingsStore }

func (f failingSettings) Get(ctx context.Context, k string) (string, error) {
	if f.s.getErr != nil {
		return "", f.s.getErr
	}
	return f.s.Store.Settings().Get(ctx, k)
}

func (f failingSettings) Set(ctx context.Context, k, v string, at time.Time) error {
	if f.s.setErr != nil {
		return f.s.setErr
	}
	return f.s.Store.Settings().Set(ctx, k, v, at)
}
