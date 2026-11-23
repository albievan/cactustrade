package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSMTPSettings(t *testing.T) {
	ok := SMTPSettings{Enabled: true, Host: " SMTP.Gmail.com. ", Port: 587, Username: " me@gmail.com "}.Normalize()
	if ok.Host != "smtp.gmail.com" || ok.TLS != SMTPStartTLS || ok.From != "me@gmail.com" || ok.Validate() != nil {
		t.Fatalf("normalised = %+v (%v)", ok, ok.Validate())
	}
	if d := DefaultSMTP(); d.Enabled || d.Host != "smtp.gmail.com" || d.Validate() != nil {
		t.Fatalf("default = %+v", d)
	}
	cases := map[string]SMTPSettings{
		"tls":           {TLS: "ssl"},
		"port":          {TLS: SMTPTLS, Port: 70000},
		"long":          {TLS: SMTPTLS, Host: strings.Repeat("h", 300)},
		"newline":       {TLS: SMTPTLS, From: "a@b.c\r\nBcc: x@y.z"},
		"host":          {Enabled: true, TLS: SMTPTLS, Port: 465, From: "a@b.c"},
		"host with url": {Enabled: true, TLS: SMTPTLS, Host: "smtp://x", Port: 465, From: "a@b.c"},
		"no port":       {Enabled: true, TLS: SMTPTLS, Host: "x", From: "a@b.c"},
		"plain pw":      {Enabled: true, TLS: SMTPNone, Host: "mail.example", Port: 25, Username: "u", From: "a@b.c"},
		"from":          {Enabled: true, TLS: SMTPTLS, Host: "x", Port: 465, From: "not an address"},
	}
	for name, c := range cases {
		if err := c.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a password over plain text is fine to this machine; switched off may be incomplete
	if err := (SMTPSettings{Enabled: true, TLS: SMTPNone, Host: "localhost", Port: 1025, Username: "u", From: "a@b.c"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (SMTPSettings{TLS: SMTPStartTLS}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEmailSenderWarnings(t *testing.T) {
	g := SMTPSettings{Host: "smtp.gmail.com", Username: "me@gmail.com", From: "Bot <bot@example.com>"}
	if w := EmailSenderWarnings(g); len(w) != 1 || !strings.Contains(w[0], "Gmail") {
		t.Fatalf("gmail = %v", w)
	}
	m := g
	m.Host = "smtp.office365.com"
	if w := EmailSenderWarnings(m); len(w) != 1 || !strings.Contains(w[0], "Microsoft") {
		t.Fatalf("microsoft = %v", w)
	}
	for _, s := range []SMTPSettings{
		{Host: "smtp.gmail.com", Username: "me@gmail.com", From: "Me <ME@gmail.com>"},
		{Host: "smtp.gmail.com", From: "x@y.z"},
		{Host: "smtp.gmail.com", Username: "u", From: "bad"},
		{Host: "mail.example", Username: "u@x.y", From: "a@b.c"},
	} {
		if w := EmailSenderWarnings(s); w == nil || len(w) != 0 {
			t.Errorf("%+v warned %v", s, w)
		}
	}
}

func TestValidateEmailAndRetry(t *testing.T) {
	for _, a := range []string{"", "a@b.co"} {
		if err := ValidateEmail("e", a); err != nil {
			t.Errorf("%q: %v", a, err)
		}
	}
	for _, a := range []string{"x", "Name <a@b.co>", "a@b.co, c@d.co", strings.Repeat("a", 250) + "@b.co"} {
		if err := ValidateEmail("e", a); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted", a)
		}
	}
	want := []time.Duration{time.Minute, time.Minute, 2 * time.Minute, 4 * time.Minute}
	for i, w := range want {
		if got := MailRetryDelay(i); got != w {
			t.Errorf("delay(%d) = %v, want %v", i, got, w)
		}
	}
	if MailRetryDelay(50) != 4*time.Hour {
		t.Error("delay not capped")
	}
}

func TestCategoryOf(t *testing.T) {
	cases := map[NotifyCategory]Event{
		NotifyAlerts:    {Kind: "order.rejected", Severity: SeverityDanger},
		NotifyTrades:    {Kind: "order.filled", Severity: SeverityInfo},
		NotifyWatchlist: {Kind: "scanner.added", Severity: SeverityInfo},
		"":              {Kind: "system.restart", Severity: SeverityInfo},
	}
	for want, e := range cases {
		if got := CategoryOf(e); got != want {
			t.Errorf("%s: %q, want %q", e.Kind, got, want)
		}
	}
	if CategoryOf(Event{Kind: "x", Severity: SeverityWarning}) != NotifyAlerts || CategoryOf(Event{Kind: "position.closed"}) != NotifyTrades ||
		CategoryOf(Event{Kind: "watchlist.added"}) != NotifyWatchlist || CategoryOf(Event{Kind: "trade.x"}) != NotifyTrades {
		t.Error("categories")
	}
	if d := DefaultNotifyPrefs(); !d[NotifyTrades] || d[NotifyWatchlist] || !d[NotifyAlerts] {
		t.Errorf("defaults = %v", d)
	}
}

func TestLLMProfile(t *testing.T) {
	p := LLMProfile{LLMConfig: LLMConfig{Provider: "openai", Model: " gpt "}}.Normalize()
	if p.Name != "OpenAI · gpt" || p.Model != "gpt" || p.Validate() != nil {
		t.Fatalf("normalised = %+v", p)
	}
	if n := (LLMProfile{LLMConfig: LLMConfig{Provider: "anthropic"}}).Normalize(); n.Name != "Anthropic (Claude)" {
		t.Fatalf("anthropic name = %q", n.Name)
	}
	if n := (LLMProfile{LLMConfig: LLMConfig{Provider: "nope"}}).Normalize(); n.Name != "" || n.Validate() == nil {
		t.Fatal("unknown provider")
	}
	for _, name := range []string{strings.Repeat("n", 81), "a\x01b"} {
		if err := (LLMProfile{Name: name, LLMConfig: p.LLMConfig}).Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted", name)
		}
	}
	if LLMKeyName("abc") != "llm.abc.api_key" {
		t.Fatal("key name")
	}
}
