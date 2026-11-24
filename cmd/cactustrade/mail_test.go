package main

import "testing"

// TestMailFallbackTestMailbox is PLAN 7.1.15: the bundled Mailpit is marked as a test
// mailbox (no password resets through it) unless the deployment opts in explicitly.
func TestMailFallbackTestMailbox(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		ok      bool
		test    bool
		wantErr bool
	}{
		{name: "none", env: map[string]string{}},
		{name: "Mailpit", env: map[string]string{"CACTUSTRADE_SMTP_HOST": "mailpit", "CACTUSTRADE_SMTP_PORT": "1025", "CACTUSTRADE_SMTP_TLS": "none"}, ok: true, test: true},
		{name: "Mailpit, upper case and qualified", env: map[string]string{"CACTUSTRADE_SMTP_HOST": "MailPit.cactustrade.svc", "CACTUSTRADE_SMTP_PORT": "1025", "CACTUSTRADE_SMTP_TLS": "none"}, ok: true, test: true},
		{name: "Mailpit with the opt-in", env: map[string]string{"CACTUSTRADE_SMTP_HOST": "mailpit", "CACTUSTRADE_SMTP_PORT": "1025", "CACTUSTRADE_SMTP_TLS": "none", testMailboxResetsEnv: "true"}, ok: true},
		{name: "a real server", env: map[string]string{"CACTUSTRADE_SMTP_HOST": "smtp.example.com"}, ok: true},
		{name: "a server named like Mailpit only in part", env: map[string]string{"CACTUSTRADE_SMTP_HOST": "mailpit-relay.example.com"}, ok: true},
		{name: "a bad opt-in", env: map[string]string{"CACTUSTRADE_SMTP_HOST": "mailpit", testMailboxResetsEnv: "maybe"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m mailFallback
			err := m.load(func(k string) string { return tt.env[k] })
			if (err != nil) != tt.wantErr {
				t.Fatalf("load = %v", err)
			}
			fb, ok := m.get()
			if ok != tt.ok || fb.TestMailbox != tt.test {
				t.Fatalf("get = %+v %v, want ok %v test mailbox %v", fb, ok, tt.ok, tt.test)
			}
		})
	}
}
