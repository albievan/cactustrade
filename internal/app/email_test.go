package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

// longRandom gives real-length tokens (the fake Random's are too short for reset links).
type longRandom struct{ fakes.Random }

func (r *longRandom) Token() string { return strings.Repeat("t", 40) + r.Random.Token() }

func withMail(t *testing.T) (*env, *fakes.Mailer) {
	t.Helper()
	e := newEnv(t)
	m := &fakes.Mailer{}
	e.svc.d.Mailer = m
	e.svc.d.Random = &longRandom{}
	return e, m
}

func gmail(enabled bool) domain.SMTPSettings {
	return domain.SMTPSettings{Enabled: enabled, Host: "smtp.gmail.com", Port: 587, Username: "me@gmail.com", TLS: "starttls"}
}

func TestEmailSetup(t *testing.T) {
	ctx := context.Background()
	e, m := withMail(t)
	viewer := e.user(t, "vera", domain.RoleViewer)
	st, err := e.svc.EmailSetup(ctx, e.admin)
	if err != nil || st.Settings.Host != "smtp.gmail.com" || st.Settings.Enabled || st.PasswordSet || st.Fallback != "" {
		t.Fatalf("default = %+v %v", st, err)
	}
	if _, err := e.svc.EmailSetup(ctx, viewer); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer read the mail server")
	}
	if _, err := e.svc.SetEmailSetup(ctx, viewer, gmail(true), sp("pw")); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("viewer changed the mail server")
	}
	if _, err := e.svc.SetEmailSetup(ctx, e.admin, gmail(true), nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("user without password accepted")
	}
	if _, err := e.svc.SetEmailSetup(ctx, e.admin, gmail(true), sp("a\nb")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("multi-line password accepted")
	}
	bad := gmail(true)
	bad.TLS = "none"
	if _, err := e.svc.SetEmailSetup(ctx, e.admin, bad, sp("pw")); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("password over plain text accepted")
	}
	g := gmail(true)
	g.From = "Trader <alerts@example.com>"
	st, err = e.svc.SetEmailSetup(ctx, e.admin, g, sp("app-password"))
	if err != nil || !st.PasswordSet || len(st.Warnings) != 1 || st.Settings.From != g.From {
		t.Fatalf("saved = %+v %v", st, err)
	}
	// the stored password never goes to another account or server
	other := g
	other.Username = "else@gmail.com"
	if _, err := e.svc.SetEmailSetup(ctx, e.admin, other, nil); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("stored password reused for another user")
	}
	if _, err := e.svc.SetEmailSetup(ctx, e.admin, g, nil); err != nil {
		t.Fatalf("same server, password kept: %v", err)
	}
	for _, a := range e.store.AuditEntries() {
		if strings.Contains(a.Details, "app-password") {
			t.Fatal("password in the audit log")
		}
	}

	// test e-mail
	if err := e.svc.SendTestEmail(ctx, e.admin, "not an address"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("bad address accepted")
	}
	if err := e.svc.SendTestEmail(ctx, e.admin, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("empty address accepted")
	}
	if err := e.svc.SendTestEmail(ctx, e.admin, "me@example.com"); err != nil || m.Count() != 1 || m.Server.Host != "smtp.gmail.com" {
		t.Fatalf("test e-mail: %v (sent %d)", err, m.Count())
	}
	m.Err = errors.New("535 bad credentials")
	var me *MailError
	if err := e.svc.SendTestEmail(ctx, e.admin, "me@example.com"); !errors.As(err, &me) {
		t.Fatalf("refused test = %v", err)
	}
	m.Err = nil

	// switched off with no fallback: nothing to send through
	if _, err := e.svc.SetEmailSetup(ctx, e.admin, gmail(false), sp("")); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SendTestEmail(ctx, e.admin, "me@example.com"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("test without a server")
	}
	// the deployment's fallback is used while the in-app server is off
	e.svc.d.MailFallback = func() (domain.SMTPSettings, string, bool) {
		return domain.SMTPSettings{Enabled: true, Host: "mailpit", Port: 1025, TLS: "none", From: "t <t@mailpit>"}, "", true
	}
	if st, _ := e.svc.EmailSetup(ctx, e.admin); st.Fallback != "mailpit:1025 (none)" {
		t.Fatalf("fallback = %q", st.Fallback)
	}
	if err := e.svc.SendTestEmail(ctx, e.admin, "me@example.com"); err != nil || m.Server.Host != "mailpit" {
		t.Fatalf("fallback test: %v %+v", err, m.Server)
	}
	e.svc.d.Mailer = nil
	if err := e.svc.SendTestEmail(ctx, e.admin, "me@example.com"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("test without a mailer")
	}
	_ = e.store.Settings().Set(ctx, settingSMTP, "garbage", e.clock.T)
	if _, err := e.svc.EmailSetup(ctx, e.admin); err == nil {
		t.Fatal("garbage settings read")
	}
}

func TestNotificationsAndOutbox(t *testing.T) {
	ctx := context.Background()
	e, m := withMail(t)
	b := &fakes.Broker{
		Known:    map[domain.InstrumentID]domain.Instrument{1: {ID: 1, Symbol: "AAPL", Type: "Stocks", Market: domain.MarketNYSE}},
		Eligible: map[domain.InstrumentID]domain.Eligibility{1: {Instrument: 1, RealLong: true}},
	}
	e.svc.d.Broker, e.svc.d.Market = b, b
	acct, err := e.svc.CreateAccount(ctx, e.admin, "Main", domain.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	vera := e.user(t, "vera", domain.RoleOperator)
	_ = e.svc.ShareAccount(ctx, e.admin, acct.ID, "vera", domain.AccessView)
	_ = e.user(t, "nobody", domain.RoleViewer) // no access to the account: never mailed

	// profiles
	if _, err := e.svc.SetMyProfile(ctx, e.admin, Profile{Email: "bad"}, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("bad e-mail accepted")
	}
	api := vera
	api.Via = ViaAPIKey
	if _, err := e.svc.SetMyProfile(ctx, api, Profile{}, ""); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("API key changed a profile")
	}
	fresh := e.admin
	fresh.MustChangePassword = true
	if _, err := e.svc.SetMyProfile(ctx, fresh, Profile{}, ""); !errors.Is(err, domain.ErrPasswordChangeRequired) {
		t.Fatal("default password changed a profile")
	}
	if p, err := e.svc.MyProfile(ctx, vera); err != nil || p.Email != "" || !p.Notify[domain.NotifyTrades] || p.Notify[domain.NotifyWatchlist] {
		t.Fatalf("default profile = %+v %v", p, err)
	}
	if _, err := e.svc.SetMyProfile(ctx, e.admin, Profile{Email: "admin@example.com", Notify: domain.NotifyPrefs{domain.NotifyWatchlist: true, domain.NotifyAlerts: true}}, "admin password 1"); err != nil {
		t.Fatal(err)
	}
	if p, err := e.svc.SetMyProfile(ctx, vera, Profile{Email: "vera@example.com", Notify: domain.NotifyPrefs{domain.NotifyTrades: true}}, "vera password!"); err != nil || p.Email != "vera@example.com" {
		t.Fatalf("vera = %+v %v", p, err)
	}
	_ = e.store.Settings().Set(ctx, notifyKey("garbage-user"), "garbage", e.clock.T)
	if p := e.svc.notifyPrefs(ctx, "garbage-user"); !p[domain.NotifyTrades] {
		t.Fatal("garbage prefs not defaulted")
	}

	// a watchlist change: admin asked for it, vera did not
	if _, err := e.svc.AddToWatchlist(ctx, e.admin, 1); err != nil {
		t.Fatal(err)
	}
	// a trade event reaches vera only; a system-wide alert reaches administrators only
	e.svc.emitFor(ctx, acct.ID, "agent", "order.filled", domain.SeverityInfo, "Bought AAPL", "100 USD")
	e.svc.emit(ctx, "system", "killswitch", domain.SeverityDanger, "Kill switch on", "")
	e.svc.emit(ctx, "system", "system.restart", domain.SeverityInfo, "Restarted", "") // no category: never mailed
	out, err := e.svc.Outbox(ctx, e.admin, 0)
	if err != nil || len(out) != 3 {
		t.Fatalf("outbox = %+v %v", out, err)
	}
	to := map[string][]string{}
	for _, o := range out {
		to[o.To] = append(to[o.To], o.Subject)
	}
	if len(to["admin@example.com"]) != 2 || len(to["vera@example.com"]) != 1 || !strings.Contains(to["vera@example.com"][0], "Bought AAPL") {
		t.Fatalf("recipients = %v", to)
	}
	if _, err := e.svc.Outbox(ctx, vera, 0); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("operator read the outbox")
	}

	// delivery: no server yet → nothing sent, mail waits
	if n, err := e.svc.DeliverMail(ctx); n != 0 || err != nil {
		t.Fatalf("deliver without a server = %d %v", n, err)
	}
	_, _ = e.svc.SetEmailSetup(ctx, e.admin, gmail(true), sp("pw"))
	m.Err = errors.New("421 try later")
	if n, _ := e.svc.DeliverMail(ctx); n != 0 {
		t.Fatal("sent while refused")
	}
	out, _ = e.svc.Outbox(ctx, e.admin, 0)
	if out[0].Attempts != 1 || out[0].LastError == "" || !out[0].NextAt.After(e.clock.T) {
		t.Fatalf("after a failure = %+v", out[0])
	}
	if n, _ := e.svc.DeliverMail(ctx); n != 0 || m.Count() != 0 {
		t.Fatal("retried before the backoff")
	}
	m.Err = nil
	e.clock.T = e.clock.T.Add(2 * time.Minute)
	if n, err := e.svc.DeliverMail(ctx); n != 3 || err != nil || m.Count() != 3 {
		t.Fatalf("deliver = %d %v (sent %d)", n, err, m.Count())
	}
	if n, _ := e.svc.DeliverMail(ctx); n != 0 || m.Count() != 3 {
		t.Fatal("accepted mail sent again")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	e.svc.enqueue(ctx, "x@example.com", "s", "b")
	if _, err := e.svc.DeliverMail(cctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled delivery = %v", err)
	}
	// purge after 30 days
	e.clock.T = e.clock.T.Add(31 * 24 * time.Hour)
	if n, err := e.svc.PurgeOldMail(ctx); err != nil || n != 4 {
		t.Fatalf("purge = %d %v", n, err)
	}
	e.svc.d.Mailer = nil
	if n, err := e.svc.DeliverMail(ctx); n != 0 || err != nil {
		t.Fatal("delivery without a mailer")
	}
}

func TestPasswordReset(t *testing.T) {
	ctx := context.Background()
	e, m := withMail(t)
	vera := e.user(t, "vera", domain.RoleOperator)
	if e.svc.PasswordResetAvailable(ctx) {
		t.Fatal("available without a public address")
	}
	if err := e.svc.RequestPasswordReset(ctx, "vera", "1.2.3.4"); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("reset without setup")
	}
	e.svc.cfg.PublicURL = "https://trader.example:8453/"
	if e.svc.PasswordResetAvailable(ctx) {
		t.Fatal("available without a mail server")
	}
	_, _ = e.svc.SetEmailSetup(ctx, e.admin, gmail(true), sp("pw"))
	if !e.svc.PasswordResetAvailable(ctx) {
		t.Fatal("not available")
	}
	// unknown user and user without e-mail: same answer, nothing sent
	for _, u := range []string{"ghost", "vera"} {
		if err := e.svc.RequestPasswordReset(ctx, u, "1.2.3.4"); err != nil {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if out, _ := e.svc.Outbox(ctx, e.admin, 0); len(out) != 0 {
		t.Fatal("mail without an address")
	}
	_, _ = e.svc.SetMyProfile(ctx, vera, Profile{Email: "vera@example.com"}, "vera password!")
	if err := e.svc.RequestPasswordReset(ctx, " Vera ", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if n, err := e.svc.DeliverMail(ctx); n != 1 || err != nil {
		t.Fatalf("deliver = %d %v", n, err)
	}
	body := m.Sent[0].Body
	i := strings.Index(body, "https://trader.example:8453/reset-password#token=")
	if i < 0 {
		t.Fatalf("no link in %q", body)
	}
	token := strings.Fields(body[i+len("https://trader.example:8453/reset-password#token="):])[0]

	if err := e.svc.ResetPassword(ctx, "short", "new password 123", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("short token accepted")
	}
	if err := e.svc.ResetPassword(ctx, strings.Repeat("x", 50), "new password 123", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("unknown token accepted")
	}
	if err := e.svc.ResetPassword(ctx, token, "short", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("weak password accepted")
	}
	if err := e.svc.ResetPassword(ctx, token, "new password 123", "1.2.3.4"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Login(ctx, "vera", "new password 123", "1.2.3.4"); err != nil {
		t.Fatalf("login with the new password: %v", err)
	}
	if err := e.svc.ResetPassword(ctx, token, "another password 1", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("link used twice")
	}
	// expiry and the hourly cap
	for range domain.PasswordResetPerHour + 2 {
		_ = e.svc.RequestPasswordReset(ctx, "vera", "")
	}
	if out, _ := e.svc.Outbox(ctx, e.admin, 0); len(out) != 1+domain.PasswordResetPerHour {
		t.Fatalf("hourly cap: %d mails", len(out))
	}
	_, _ = e.svc.DeliverMail(ctx)
	last := m.Sent[len(m.Sent)-1].Body
	i = strings.Index(last, "#token=")
	tok2 := strings.Fields(last[i+len("#token="):])[0]
	e.clock.T = e.clock.T.Add(domain.PasswordResetTTL + time.Second)
	if err := e.svc.ResetPassword(ctx, tok2, "another password 1", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatal("expired link accepted")
	}
	// disabled users get nothing
	u, _ := e.store.Users().GetByUsername(ctx, "vera")
	u.Disabled = true
	_ = e.store.Users().Update(ctx, u)
	e.clock.T = e.clock.T.Add(2 * time.Hour)
	before := len(m.Sent)
	_ = e.svc.RequestPasswordReset(ctx, "vera", "")
	_, _ = e.svc.DeliverMail(ctx)
	if len(m.Sent) != before {
		t.Fatal("disabled user mailed")
	}
}
