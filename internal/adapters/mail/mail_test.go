package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

const testPassword = "s3cret-pa55word"

// fakeSMTP is a minimal SMTP server for one or more sessions on 127.0.0.1.
type fakeSMTP struct {
	ln         net.Listener
	tlsConfig  *tls.Config // for STARTTLS
	startTLS   bool        // advertise STARTTLS
	auth       bool        // advertise AUTH PLAIN
	rejectRcpt bool
	rejectData bool
	stall      bool // greet, then never answer

	mu       sync.Mutex
	sessions int
	tlsUsed  bool
	authLine string
	from     string
	to       string
	data     string
}

func (s *fakeSMTP) port(t *testing.T) int {
	t.Helper()
	return s.ln.Addr().(*net.TCPAddr).Port
}

func startFake(t *testing.T, s *fakeSMTP, implicitTLS bool) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if implicitTLS {
		ln = tls.NewListener(ln, s.tlsConfig)
	}
	s.ln = ln
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, implicitTLS)
		}
	}()
	return s
}

func (s *fakeSMTP) serve(conn net.Conn, implicitTLS bool) {
	defer func() { _ = conn.Close() }()
	s.mu.Lock()
	s.sessions++
	if implicitTLS {
		s.tlsUsed = true
	}
	s.mu.Unlock()
	r := bufio.NewReader(conn)
	reply := func(l string) { _, _ = fmt.Fprintf(conn, "%s\r\n", l) }
	reply("220 fake ESMTP")
	if s.stall {
		_, _ = io.Copy(io.Discard, conn)
		return
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch verb {
		case "EHLO":
			reply("250-fake")
			if s.startTLS {
				reply("250-STARTTLS")
			}
			if s.auth {
				reply("250-AUTH PLAIN")
			}
			reply("250 8BITMIME")
		case "STARTTLS":
			reply("220 go ahead")
			tc := tls.Server(conn, s.tlsConfig)
			if err := tc.Handshake(); err != nil {
				return
			}
			conn = tc
			r = bufio.NewReader(tc)
			s.mu.Lock()
			s.tlsUsed = true
			s.mu.Unlock()
		case "AUTH":
			s.mu.Lock()
			s.authLine = line
			s.mu.Unlock()
			reply("235 ok")
		case "MAIL":
			s.mu.Lock()
			s.from = line
			s.mu.Unlock()
			reply("250 ok")
		case "RCPT":
			if s.rejectRcpt {
				reply("550 no such user")
				continue
			}
			s.mu.Lock()
			s.to = line
			s.mu.Unlock()
			reply("250 ok")
		case "DATA":
			reply("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(strings.TrimPrefix(l, "."))
			}
			s.mu.Lock()
			s.data = b.String()
			s.mu.Unlock()
			if s.rejectData {
				reply("554 rejected")
				continue
			}
			reply("250 queued")
		case "QUIT":
			reply("221 bye")
			return
		case "RSET", "NOOP":
			reply("250 ok")
		default:
			reply("502 not implemented")
		}
	}
}

func (s *fakeSMTP) snapshot() (from, to, data, authLine string, tlsUsed bool, sessions int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.from, s.to, s.data, s.authLine, s.tlsUsed, s.sessions
}

// testCert returns a server TLS config with a self-signed certificate for 127.0.0.1 and a
// pool trusting it.
func testCert(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	cfg := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}
	return cfg, pool
}

var fixedNow = time.Date(2026, 11, 23, 14, 30, 0, 0, time.UTC)

func newTestMailer() *Mailer {
	return New(func() time.Time { return fixedNow }, "test.host")
}

func server(port int, mode string) domain.SMTPSettings {
	return domain.SMTPSettings{
		Enabled: true, Host: "127.0.0.1", Port: port, TLS: mode,
		From: "cactusTrade <trader@example.com>",
	}
}

func testMail() domain.Mail {
	return domain.Mail{
		ID:      "m-123",
		To:      "Owner <owner@example.com>",
		Subject: "Order filled",
		Body:    "Bought 3 AAPL.\nLine two = done\r\n.leading dot",
	}
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestSendPlainNoAuth(t *testing.T) {
	s := startFake(t, &fakeSMTP{}, false)
	if err := newTestMailer().Send(ctxT(t), server(s.port(t), domain.SMTPNone), "", testMail()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	from, to, data, authLine, _, _ := s.snapshot()
	if from != "MAIL FROM:<trader@example.com> BODY=8BITMIME" && from != "MAIL FROM:<trader@example.com>" {
		t.Errorf("envelope from = %q", from)
	}
	if to != "RCPT TO:<owner@example.com>" {
		t.Errorf("envelope to = %q", to)
	}
	if authLine != "" {
		t.Errorf("unexpected AUTH %q", authLine)
	}
	for _, want := range []string{
		"From: \"cactusTrade\" <trader@example.com>\r\n",
		"To: \"Owner\" <owner@example.com>\r\n",
		"Subject: Order filled\r\n",
		"Date: Mon, 23 Nov 2026 14:30:00 +0000\r\n",
		"Message-ID: <m-123@test.host>\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=utf-8\r\n",
		"Content-Transfer-Encoding: quoted-printable\r\n",
		"\r\n\r\nBought 3 AAPL.\r\nLine two =3D done\r\n.leading dot\r\n",
	} {
		if !strings.Contains(data, want) {
			t.Errorf("message lacks %q:\n%s", want, data)
		}
	}
}

func TestSendAuthOverPlainLocalhost(t *testing.T) {
	s := startFake(t, &fakeSMTP{auth: true}, false)
	srv := server(s.port(t), domain.SMTPNone)
	srv.Username = "trader@example.com"
	if err := newTestMailer().Send(ctxT(t), srv, testPassword, testMail()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, _, _, authLine, _, _ := s.snapshot()
	want := "AUTH PLAIN " + base64.StdEncoding.EncodeToString([]byte("\x00trader@example.com\x00"+testPassword))
	if authLine != want {
		t.Errorf("AUTH = %q, want %q", authLine, want)
	}
}

func TestStartTLSRequiredButNotOffered(t *testing.T) {
	s := startFake(t, &fakeSMTP{auth: true}, false)
	srv := server(s.port(t), domain.SMTPStartTLS)
	srv.Username = "u"
	err := newTestMailer().Send(ctxT(t), srv, testPassword, testMail())
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v, want STARTTLS required", err)
	}
	from, _, _, authLine, _, _ := s.snapshot()
	if from != "" || authLine != "" {
		t.Errorf("sent something: from=%q auth=%q", from, authLine)
	}
}

func TestStartTLS(t *testing.T) {
	cfg, pool := testCert(t)
	s := startFake(t, &fakeSMTP{startTLS: true, auth: true, tlsConfig: cfg}, false)
	m := newTestMailer()
	m.rootCAs = pool
	srv := server(s.port(t), domain.SMTPStartTLS)
	srv.Username = "u"
	if err := m.Send(ctxT(t), srv, testPassword, testMail()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, to, data, authLine, tlsUsed, _ := s.snapshot()
	if !tlsUsed || authLine == "" || to == "" || !strings.Contains(data, "Subject: Order filled") {
		t.Errorf("tls=%v auth=%q to=%q data=%q", tlsUsed, authLine, to, data)
	}
}

func TestStartTLSUntrustedCertificate(t *testing.T) {
	cfg, _ := testCert(t)
	s := startFake(t, &fakeSMTP{startTLS: true, tlsConfig: cfg}, false)
	err := newTestMailer().Send(ctxT(t), server(s.port(t), domain.SMTPStartTLS), "", testMail())
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v, want certificate failure", err)
	}
}

func TestImplicitTLS(t *testing.T) {
	cfg, pool := testCert(t)
	s := startFake(t, &fakeSMTP{auth: true, tlsConfig: cfg}, true)
	m := newTestMailer()
	m.rootCAs = pool
	srv := server(s.port(t), domain.SMTPTLS)
	srv.Username = "u"
	if err := m.Send(ctxT(t), srv, testPassword, testMail()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, _, data, authLine, tlsUsed, _ := s.snapshot()
	if !tlsUsed || authLine == "" || !strings.Contains(data, "Message-ID: <m-123@test.host>") {
		t.Errorf("tls=%v auth=%q data=%q", tlsUsed, authLine, data)
	}
}

func TestImplicitTLSUntrusted(t *testing.T) {
	cfg, _ := testCert(t)
	s := startFake(t, &fakeSMTP{tlsConfig: cfg}, true)
	err := newTestMailer().Send(ctxT(t), server(s.port(t), domain.SMTPTLS), "", testMail())
	if err == nil || !strings.Contains(err.Error(), "TLS handshake") {
		t.Fatalf("err = %v, want handshake failure", err)
	}
}

func TestRefusesBadInputAndSendsNothing(t *testing.T) {
	s := startFake(t, &fakeSMTP{}, false)
	port := s.port(t)
	cases := map[string]func(*domain.SMTPSettings, *domain.Mail){
		"CR in subject":   func(_ *domain.SMTPSettings, m *domain.Mail) { m.Subject = "Hi\rBcc: x@evil.test" },
		"LF in subject":   func(_ *domain.SMTPSettings, m *domain.Mail) { m.Subject = "Hi\nBcc: x@evil.test" },
		"LF in to":        func(_ *domain.SMTPSettings, m *domain.Mail) { m.To = "a@example.com\nBcc: x@evil.test" },
		"bad to":          func(_ *domain.SMTPSettings, m *domain.Mail) { m.To = "not an address" },
		"bad from":        func(s *domain.SMTPSettings, _ *domain.Mail) { s.From = "nobody" },
		"CRLF in from":    func(s *domain.SMTPSettings, _ *domain.Mail) { s.From = "a@example.com\r\nX: y" },
		"empty id":        func(_ *domain.SMTPSettings, m *domain.Mail) { m.ID = "" },
		"bad id":          func(_ *domain.SMTPSettings, m *domain.Mail) { m.ID = "a>b" },
		"unknown tls":     func(s *domain.SMTPSettings, _ *domain.Mail) { s.TLS = "ssl" },
		"no host":         func(s *domain.SMTPSettings, _ *domain.Mail) { s.Host = "" },
		"port out of use": func(s *domain.SMTPSettings, _ *domain.Mail) { s.Port = 70000 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			srv, m := server(port, domain.SMTPNone), testMail()
			mutate(&srv, &m)
			err := newTestMailer().Send(ctxT(t), srv, testPassword, m)
			if err == nil || !strings.HasPrefix(err.Error(), "mail: ") {
				t.Fatalf("err = %v, want a mail: error", err)
			}
		})
	}
	if _, _, _, _, _, sessions := s.snapshot(); sessions != 0 {
		t.Errorf("server saw %d sessions, want 0", sessions)
	}
}

func TestNonASCIISubjectIsQEncoded(t *testing.T) {
	s := startFake(t, &fakeSMTP{}, false)
	m := testMail()
	m.Subject = "Gekauft: Müller AG"
	m.Body = "Grüße"
	if err := newTestMailer().Send(ctxT(t), server(s.port(t), domain.SMTPNone), "", m); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, _, data, _, _, _ := s.snapshot()
	if !strings.Contains(data, "Subject: =?utf-8?q?Gekauft:_M=C3=BCller_AG?=\r\n") {
		t.Errorf("subject not Q-encoded:\n%s", data)
	}
	if !strings.Contains(data, "\r\n\r\nGr=C3=BC=C3=9Fe\r\n") {
		t.Errorf("body not quoted-printable:\n%s", data)
	}
}

func TestCancelledContext(t *testing.T) {
	s := startFake(t, &fakeSMTP{}, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := newTestMailer().Send(ctx, server(s.port(t), domain.SMTPNone), "", testMail())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestCancelMidSession(t *testing.T) {
	s := startFake(t, &fakeSMTP{stall: true}, false)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	err := newTestMailer().Send(ctx, server(s.port(t), domain.SMTPNone), "", testMail())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("cancel took %v", d)
	}
}

func TestDeadlineMidSession(t *testing.T) {
	s := startFake(t, &fakeSMTP{stall: true}, false)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := newTestMailer().Send(ctx, server(s.port(t), domain.SMTPNone), "", testMail())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestConnectRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	err = newTestMailer().Send(ctxT(t), server(port, domain.SMTPNone), "", testMail())
	if err == nil || !strings.Contains(err.Error(), "mail: connect") {
		t.Fatalf("err = %v, want connect error", err)
	}
}

func TestRcptRejectedHidesPassword(t *testing.T) {
	s := startFake(t, &fakeSMTP{auth: true, rejectRcpt: true}, false)
	srv := server(s.port(t), domain.SMTPNone)
	srv.Username = "trader@example.com"
	err := newTestMailer().Send(ctxT(t), srv, testPassword, testMail())
	if err == nil || !strings.Contains(err.Error(), "RCPT TO") || !strings.Contains(err.Error(), "550") {
		t.Fatalf("err = %v, want RCPT 550", err)
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Fatalf("error leaks the password: %v", err)
	}
}

func TestDataRejected(t *testing.T) {
	s := startFake(t, &fakeSMTP{rejectData: true}, false)
	err := newTestMailer().Send(ctxT(t), server(s.port(t), domain.SMTPNone), "", testMail())
	if err == nil || !strings.Contains(err.Error(), "554") {
		t.Fatalf("err = %v, want 554", err)
	}
}

func TestRedact(t *testing.T) {
	err := redact(errors.New("mail: server said bad password "+testPassword), testPassword)
	if strings.Contains(err.Error(), testPassword) || !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("redact = %v", err)
	}
	orig := errors.New("plain")
	if got := redact(orig, ""); got.Error() != orig.Error() {
		t.Errorf("redact without password changed the error")
	}
}

func TestNewDefaults(t *testing.T) {
	m := New(nil, "")
	if m.hostname != DefaultHostname || m.clock == nil {
		t.Errorf("New defaults: %+v", m)
	}
	if New(nil, "bad host").hostname != DefaultHostname {
		t.Error("an invalid hostname must fall back to the default")
	}
	if New(nil, " box.example ").hostname != "box.example" {
		t.Error("hostname not trimmed")
	}
}
