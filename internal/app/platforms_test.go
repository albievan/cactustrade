package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// TestSaxoAndTigerAccountsNeverTrade: Saxo and Tiger accounts (owner's decision 2026-11-23,
// view-only) are refused by every trading, agent, scanner, watchlist and theme path through
// the one central check, are never the current account, and can still be managed.
func TestSaxoAndTigerAccountsNeverTrade(t *testing.T) {
	for _, pl := range []domain.Platform{domain.PlatformSaxo, domain.PlatformTiger} {
		t.Run(string(pl), func(t *testing.T) {
			ctx := context.Background()
			e, _ := withBroker(t) // admin owns the eToro "Main demo"
			v, err := e.svc.CreateAccountOn(ctx, e.admin, pl, "View "+string(pl), domain.ModeDemo)
			if err != nil || v.Platform != pl || v.Tradable() {
				t.Fatalf("created = %+v %v", v, err)
			}
			if cur, _ := e.svc.CurrentAccount(ctx, e.admin); cur.Platform != domain.PlatformEtoro {
				t.Fatalf("current = %+v", cur)
			}
			byID := []struct {
				name string
				call func() error
			}{
				{"switch to it", func() error { _, err := e.svc.SetCurrentAccount(ctx, e.admin, v.ID); return err }},
				{"test connection", func() error { _, err := e.svc.TestConnection(ctx, e.admin, v.ID); return err }},
				{"trade access", func() error { _, err := e.svc.account(ctx, e.admin, v.ID, domain.AccessTrade); return err }},
				{"view access", func() error { _, err := e.svc.account(ctx, e.admin, v.ID, domain.AccessView); return err }},
			}
			for _, tt := range byID {
				if err := tt.call(); !errors.Is(err, domain.ErrViewOnly) || !errors.Is(err, domain.ErrForbidden) {
					t.Errorf("%s: %v", tt.name, err)
				}
			}
			op := e.user(t, "olivia", domain.RoleOperator)
			if _, err := e.svc.CreateAccountOn(ctx, op, pl, "Only this", domain.ModeReal); err != nil {
				t.Fatal(err)
			}
			current := []struct {
				name string
				call func() error
			}{
				{"portfolio", func() error { _, err := e.svc.Portfolio(ctx, op); return err }},
				{"watchlist", func() error { _, err := e.svc.Watchlist(ctx, op); return err }},
				{"add to watchlist", func() error { _, err := e.svc.AddToWatchlist(ctx, op, 1); return err }},
				{"quotes", func() error { _, err := e.svc.WatchlistQuotes(ctx, op); return err }},
				{"risk limits", func() error { _, err := e.svc.RiskLimits(ctx, op); return err }},
				{"scanner", func() error { return e.svc.SetScanner(ctx, op, true) }},
				{"scan", func() error { _, err := e.svc.ScanNow(ctx, op); return err }},
				{"themes", func() error { _, err := e.svc.Themes(ctx, op); return err }},
				{"test connection", func() error { _, err := e.svc.TestConnection(ctx, op, ""); return err }},
			}
			for _, tt := range current {
				if err := tt.call(); !errors.Is(err, domain.ErrNoAccount) {
					t.Errorf("%s: %v", tt.name, err)
				}
			}
			// managing still works
			if r, err := e.svc.RenameAccount(ctx, e.admin, v.ID, "Renamed"); err != nil || r.Name != "Renamed" {
				t.Fatalf("rename = %+v %v", r, err)
			}
			if err := e.svc.ShareAccount(ctx, e.admin, v.ID, "olivia", domain.AccessTrade); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.account(ctx, op, v.ID, domain.AccessTrade); !errors.Is(err, domain.ErrViewOnly) {
				t.Fatalf("trade share: %v", err)
			}
		})
	}
}

const testPEM = "-----BEGIN PRIVATE KEY-----\nMIIBVgIBADANBgkqhkiG9w0BAQEFAASCAUAwggE8AgEAAkEA\n-----END PRIVATE KEY-----"

func TestAccountCredentials(t *testing.T) {
	ctx := context.Background()
	e, _ := withBroker(t)
	e.svc.d.Connectors = map[domain.Platform]ports.OAuthConnector{domain.PlatformSaxo: &fakes.OAuthConnector{}}
	e.svc.cfg.PublicURL = "https://trader.example:8453/"
	saxo, _ := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformSaxo, "Saxo SIM", domain.ModeDemo)
	tiger, _ := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformTiger, "Tiger paper", domain.ModeDemo)
	viewer := e.user(t, "vera", domain.RoleViewer)
	_ = e.svc.ShareAccount(ctx, e.admin, tiger.ID, "vera", domain.AccessView)

	tests := []struct {
		name    string
		p       Principal
		id      string
		values  map[string]string
		wantErr error
	}{
		{"tiger: all", e.admin, tiger.ID, map[string]string{domain.TigerID: "20150001", domain.TigerAccount: "21234567", domain.TigerPrivateKey: strings.ReplaceAll(testPEM, "\n", "\r\n") + "\r\n"}, nil},
		{"tiger: empty keeps", e.admin, tiger.ID, map[string]string{domain.TigerID: ""}, nil},
		{"tiger: unknown key", e.admin, tiger.ID, map[string]string{"api_key": "x"}, domain.ErrInvalid},
		{"tiger: newline in a one-line key", e.admin, tiger.ID, map[string]string{domain.TigerAccount: "a\nb"}, domain.ErrInvalid},
		{"tiger: viewer", viewer, tiger.ID, map[string]string{domain.TigerID: "x"}, domain.ErrForbidden},
		{"tiger: API key, not the web UI", Principal{UserID: e.admin.UserID, Username: "admin", Role: domain.RoleAdmin, Via: ViaAPIKey}, tiger.ID, map[string]string{domain.TigerID: "x"}, domain.ErrForbidden},
		{"saxo: app key and secret", e.admin, saxo.ID, map[string]string{domain.SaxoAppKey: "app", domain.SaxoAppSecret: "shh"}, nil},
		{"saxo: the refresh token is the flow's", e.admin, saxo.ID, map[string]string{domain.SaxoRefreshToken: "rt"}, domain.ErrInvalid},
	}
	for _, tt := range tests {
		if err := e.svc.SetAccountCredentials(ctx, tt.p, tt.id, tt.values); !errors.Is(err, tt.wantErr) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	if v, _ := e.sec.Get(ctx, domain.CredentialName("tiger."+tiger.ID+".private_key")); v != testPEM {
		t.Fatalf("PEM stored as %q", v)
	}
	if v, _ := e.sec.Get(ctx, domain.CredentialName("tiger."+tiger.ID+".tiger_id")); v != "20150001" {
		t.Fatal("an empty value replaced the stored one")
	}
	if _, err := e.sec.Get(ctx, domain.CredentialName("saxo."+saxo.ID+".refresh_token")); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("the refresh token was typed in")
	}

	k, err := e.svc.AccountKeysStatus(ctx, viewer, tiger.ID)
	if err != nil || !k.Complete || len(k.Credentials) != 4 || k.Connectable || k.ConnectRedirectURI != "" || !k.APIKeySet || !k.UserKeySet {
		t.Fatalf("tiger status = %+v %v", k, err)
	}
	if c := k.Credentials[3]; c.Key != domain.TigerLicense || c.Set || !c.Optional {
		t.Fatalf("licence = %+v", c)
	}
	if c := k.Credentials[2]; !c.Set || !c.Multiline || !c.UpdatedAt.Equal(e.clock.Now()) {
		t.Fatalf("private key = %+v", c)
	}
	k, _ = e.svc.AccountKeysStatus(ctx, e.admin, saxo.ID)
	if k.Complete || !k.Connectable || k.ConnectRedirectURI != "https://trader.example:8453/api/v1/oauth/saxo/callback" {
		t.Fatalf("saxo before connecting = %+v", k)
	}
	// the 24-hour developer token alone is enough
	if err := e.svc.SetAccountCredentials(ctx, e.admin, saxo.ID, map[string]string{domain.SaxoDevToken: "eyJ.dev"}); err != nil {
		t.Fatal(err)
	}
	if k, _ = e.svc.AccountKeysStatus(ctx, e.admin, saxo.ID); !k.Complete {
		t.Fatalf("saxo with a developer token = %+v", k)
	}
	// the older two-field API sets the first two credentials of any platform
	if err := e.svc.SetAccountKeys(ctx, e.admin, saxo.ID, "app2", ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := e.sec.Get(ctx, domain.CredentialName("saxo."+saxo.ID+".app_key")); v != "app2" {
		t.Fatal("SetAccountKeys on Saxo")
	}
	// deleting removes every credential of the account
	_ = e.sec.Put(ctx, domain.CredentialName("saxo."+saxo.ID+".refresh_token"), "rt")
	if err := e.svc.DeleteAccount(ctx, e.admin, saxo.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range domain.PlatformCredentials(domain.PlatformSaxo) {
		if _, err := e.sec.Get(ctx, domain.CredentialName("saxo."+saxo.ID+"."+c.Key)); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s survived its account", c.Key)
		}
	}
}

func TestConnectAccount(t *testing.T) {
	ctx := context.Background()
	e, _ := withBroker(t)
	conn := &fakes.OAuthConnector{}
	e.svc.d.Connectors = map[domain.Platform]ports.OAuthConnector{domain.PlatformSaxo: conn}
	saxo, _ := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformSaxo, "Saxo SIM", domain.ModeDemo)
	tiger, _ := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformTiger, "Tiger", domain.ModeDemo)
	op := e.user(t, "olivia", domain.RoleOperator)
	_ = e.svc.ShareAccount(ctx, e.admin, saxo.ID, "olivia", domain.AccessTrade)

	// no public URL: the redirect address is unknown
	if _, err := e.svc.ConnectAccount(ctx, e.admin, domain.PlatformSaxo, saxo.ID); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("no public URL: %v", err)
	}
	e.svc.cfg.PublicURL = "https://trader.example:8453"
	refused := []struct {
		name string
		p    Principal
		id   string
		want error
	}{
		{"not the owner", op, saxo.ID, domain.ErrForbidden},
		{"API key", Principal{UserID: e.admin.UserID, Username: "admin", Role: domain.RoleAdmin, Via: ViaAPIKey}, saxo.ID, domain.ErrForbidden},
		{"no browser sign-in", e.admin, tiger.ID, domain.ErrInvalid},
		{"unknown account", e.admin, "nope", domain.ErrNotFound},
	}
	for _, tt := range refused {
		if _, err := e.svc.ConnectAccount(ctx, tt.p, domain.PlatformSaxo, tt.id); !errors.Is(err, tt.want) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
	// a started sign-in: the state and PKCE challenge in Saxo's address, the nonce for the
	// browser's cookie
	type flow struct{ state, challenge, nonce string }
	param := func(u, name string) string {
		i := strings.LastIndex(u, name+"=")
		if i < 0 {
			return ""
		}
		v := u[i+len(name)+1:]
		if j := strings.IndexByte(v, '&'); j >= 0 {
			v = v[:j]
		}
		return v
	}
	startAs := func(p Principal, id string) flow {
		t.Helper()
		cs, err := e.svc.ConnectAccount(ctx, p, domain.PlatformSaxo, id)
		if err != nil || cs.RedirectURI != "https://trader.example:8453/api/v1/oauth/saxo/callback" || !strings.Contains(cs.AuthorizeURL, "state=") || cs.BrowserNonce == "" {
			t.Fatalf("connect = %+v %v", cs, err)
		}
		return flow{state: param(cs.AuthorizeURL, "state"), challenge: param(cs.AuthorizeURL, "code_challenge"), nonce: cs.BrowserNonce}
	}
	start := func() flow { t.Helper(); return startAs(e.admin, saxo.ID) }
	ok := func(f flow) OAuthCallback { return OAuthCallback{Code: "c", State: f.state, BrowserNonce: f.nonce} }

	f := start()
	if s := fmt.Sprint(e.svc.oauth.m); strings.Contains(s, f.state) || strings.Contains(s, f.nonce) {
		t.Fatal("the state or the nonce is kept in clear")
	}
	if f.nonce == f.state || f.challenge == "" || strings.Contains(f.challenge, f.state) {
		t.Fatalf("flow = %+v", f)
	}
	a, err := e.svc.CompleteConnect(ctx, domain.PlatformSaxo, OAuthCallback{Code: "the-code", State: f.state, BrowserNonce: f.nonce, RemoteIP: "10.0.0.9"})
	if err != nil || a.ID != saxo.ID || len(conn.Exchanged) != 1 || conn.Exchanged[0] != saxo.ID+":the-code:https://trader.example:8453/api/v1/oauth/saxo/callback" {
		t.Fatalf("complete = %+v %v %v", a, err, conn.Exchanged)
	}
	// PKCE: the exchange proves the sign-in with the verifier of the challenge Saxo saw
	if v := conn.Verifiers[0]; !domain.ValidPKCEVerifier(v) || domain.PKCEChallenge(v) != f.challenge || v == f.state || v == f.nonce {
		t.Fatalf("verifier %q for challenge %q", v, f.challenge)
	}
	if _, err := e.svc.CompleteConnect(ctx, domain.PlatformSaxo, OAuthCallback{Code: "the-code", State: f.state, BrowserNonce: f.nonce}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a used state: %v", err)
	}

	cases := []struct {
		name   string
		pl     domain.Platform
		cb     func(f flow) OAuthCallback
		before func()
		want   error
	}{
		{"no state", domain.PlatformSaxo, func(f flow) OAuthCallback { c := ok(f); c.State = ""; return c }, nil, domain.ErrInvalid},
		{"unknown state", domain.PlatformSaxo, func(f flow) OAuthCallback { c := ok(f); c.State = "forged"; return c }, nil, domain.ErrInvalid},
		{"other platform", domain.PlatformTiger, ok, nil, domain.ErrInvalid},
		{"expired", domain.PlatformSaxo, ok, func() { e.clock.Advance(OAuthStateTTL) }, domain.ErrInvalid},
		// PLAN 7.1.13: the callback must come back to the browser that started the sign-in
		{"no nonce cookie", domain.PlatformSaxo, func(f flow) OAuthCallback { c := ok(f); c.BrowserNonce = ""; return c }, nil, domain.ErrInvalid},
		{"another browser's cookie", domain.PlatformSaxo, func(f flow) OAuthCallback { c := ok(f); c.BrowserNonce = f.state; return c }, nil, domain.ErrInvalid},
		{"an oversized cookie", domain.PlatformSaxo, func(f flow) OAuthCallback { c := ok(f); c.BrowserNonce = strings.Repeat("n", 513); return c }, nil, domain.ErrInvalid},
		{"provider refused", domain.PlatformSaxo, func(f flow) OAuthCallback {
			c := ok(f)
			c.Code, c.ProviderError = "", "access_denied\n<script>"
			return c
		}, nil, ErrConnectRefused},
		{"a refusal without the cookie", domain.PlatformSaxo, func(f flow) OAuthCallback {
			return OAuthCallback{State: f.state, ProviderError: "access_denied"}
		}, nil, domain.ErrInvalid},
		{"bad code", domain.PlatformSaxo, func(f flow) OAuthCallback { c := ok(f); c.Code = "a b"; return c }, nil, domain.ErrInvalid},
		{"exchange fails", domain.PlatformSaxo, ok, func() {
			conn.Err = fmt.Errorf("saxo token: HTTP 400: %w", domain.ErrUnavailable)
		}, ErrConnectRefused},
		{"user disabled", domain.PlatformSaxo, ok, func() {
			u, _ := e.store.Users().Get(ctx, op.UserID)
			u.Disabled = true
			_ = e.store.Users().Update(ctx, u)
		}, domain.ErrForbidden},
	}
	for _, tt := range cases {
		conn.Err = nil
		n := len(conn.Exchanged)
		var f flow
		if tt.name == "user disabled" { // olivia owns this one
			own, _ := e.svc.CreateAccountOn(ctx, op, domain.PlatformSaxo, "Olivia Saxo", domain.ModeDemo)
			f = startAs(op, own.ID)
		} else {
			f = start()
		}
		if tt.before != nil {
			tt.before()
		}
		cb := tt.cb(f)
		_, err := e.svc.CompleteConnect(ctx, tt.pl, cb)
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: %v", tt.name, err)
		}
		if errors.Is(tt.want, ErrConnectRefused) && strings.Contains(err.Error(), "<script>") {
			t.Errorf("%s: provider text not clipped: %v", tt.name, err)
		}
		if len(conn.Exchanged) != n {
			t.Errorf("%s: exchanged anyway", tt.name)
		}
		// whatever happened, the state is used up: the right cookie cannot finish it later
		if cb.State == f.state {
			if _, err := e.svc.CompleteConnect(ctx, domain.PlatformSaxo, ok(f)); !errors.Is(err, domain.ErrInvalid) || len(conn.Exchanged) != n {
				t.Errorf("%s: the state was not used up: %v", tt.name, err)
			}
		}
	}
	// the account was deleted while signing in
	f = start()
	if err := e.svc.DeleteAccount(ctx, e.admin, saxo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.CompleteConnect(ctx, domain.PlatformSaxo, ok(f)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("deleted account: %v", err)
	}
	// without a connector the callback says so
	other, _ := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformSaxo, "Saxo 2", domain.ModeDemo)
	f = startAs(e.admin, other.ID)
	e.svc.d.Connectors = nil
	if _, err := e.svc.CompleteConnect(ctx, domain.PlatformSaxo, ok(f)); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("no connector: %v", err)
	}
}

// TestConnectNeedsValidVerifier: a random source that cannot give a PKCE verifier stops the
// sign-in before anything is stored or sent (fail closed).
func TestConnectNeedsValidVerifier(t *testing.T) {
	ctx := context.Background()
	e, _ := withBroker(t)
	conn := &fakes.OAuthConnector{}
	e.svc.d.Connectors = map[domain.Platform]ports.OAuthConnector{domain.PlatformSaxo: conn}
	e.svc.cfg.PublicURL = "https://trader.example"
	saxo, _ := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformSaxo, "Saxo SIM", domain.ModeDemo)
	e.svc.d.Random = &shortRandom{}
	if _, err := e.svc.ConnectAccount(ctx, e.admin, domain.PlatformSaxo, saxo.ID); !errors.Is(err, domain.ErrUnavailable) || len(e.svc.oauth.m) != 0 {
		t.Fatalf("connect with short tokens = %v (%d pending)", err, len(e.svc.oauth.m))
	}
}

type shortRandom struct{ fakes.Random }

func (*shortRandom) Token() string { return "short" }

func TestOAuthStatesBounded(t *testing.T) {
	o := newOAuthStates()
	now := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	for i := range maxOAuthStates {
		if !o.put(fmt.Sprint(i), oauthState{expires: now.Add(time.Minute)}, now) {
			t.Fatalf("refused at %d", i)
		}
	}
	if o.put("one more", oauthState{expires: now.Add(time.Minute)}, now) {
		t.Fatal("unbounded")
	}
	if !o.put("later", oauthState{expires: now.Add(2 * time.Minute)}, now.Add(time.Minute)) || len(o.m) != 1 {
		t.Fatalf("expired states not dropped: %d", len(o.m))
	}
	if clip("abc\x00é", 10) != "abc" || clip("abcdef", 3) != "abc" {
		t.Fatal("clip")
	}
	if validCode(strings.Repeat("a", 2049)) || validCode("") || !validCode("a-b_c.d") {
		t.Fatal("validCode")
	}
}

func TestOverviewReconnect(t *testing.T) {
	ctx := context.Background()
	e, _ := withBroker(t)
	saxo, _ := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformSaxo, "Saxo", domain.ModeDemo)
	tiger, _ := e.svc.CreateAccountOn(ctx, e.admin, domain.PlatformTiger, "Tiger", domain.ModeReal)
	jpy := domain.Holdings{
		Platform: domain.PlatformTiger, Currency: "JPY", Cash: 150_000, Equity: 1_000_000,
		Positions: []domain.Holding{{Symbol: "7203", Quantity: 100_000_000, Unknown: true}},
	}
	e.svc.d.Holdings = map[domain.Platform]ports.Holdings{
		domain.PlatformSaxo: &fakes.Holdings{Errs: map[string]error{
			saxo.ID: fmt.Errorf("app.holdings: Saxo refresh token refused: %w: %w", domain.ErrReconnect, domain.ErrUnavailable),
		}},
		domain.PlatformTiger: &fakes.Holdings{ByAccount: map[string]domain.Holdings{tiger.ID: jpy}},
	}
	ov, err := e.svc.Overview(ctx, e.admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range ov.Accounts {
		switch a.Account.ID {
		case saxo.ID:
			if !a.Reconnect || a.Holdings != nil || a.NoKeys || a.Error != "Reconnect to Saxo: Saxo refresh token refused" {
				t.Fatalf("saxo = %+v", a)
			}
		case tiger.ID:
			if a.Holdings == nil || a.Holdings.Currency != "JPY" || !a.Holdings.Positions[0].Unknown {
				t.Fatalf("tiger = %+v", a)
			}
		}
	}
	found := false
	for _, tot := range ov.Totals {
		found = found || (tot.Currency == "JPY" && tot.Equity == 1_000_000)
	}
	if !found {
		t.Fatalf("totals = %+v", ov.Totals)
	}
	if n := e.svc.RecordEquity(ctx); n != 2 { // eToro + Tiger; Saxo needs reconnecting
		t.Fatalf("recorded %d", n)
	}
}
