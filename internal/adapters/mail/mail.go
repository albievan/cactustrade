// Package mail is the outgoing e-mail adapter: it implements ports.Mailer with the standard
// library's SMTP client. It sends one plain-text message per call over implicit TLS, a
// required STARTTLS upgrade or (for a test server only) a plain connection. Passwords are
// never logged and never appear in a returned error.
package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	netmail "net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
)

// DefaultHostname is used in the EHLO greeting and the Message-ID when New gets none.
const DefaultHostname = "cactustrade.local"

// DefaultTimeout bounds a whole SMTP session when the context carries no deadline.
const DefaultTimeout = 30 * time.Second

// Mailer sends mail through an SMTP server. It is safe for concurrent use.
type Mailer struct {
	clock    func() time.Time
	hostname string
	// rootCAs replaces the system roots; nil (production) means the system roots. Only
	// tests set it, to trust their self-signed certificate.
	rootCAs *x509.CertPool
}

var _ ports.Mailer = (*Mailer)(nil)

// New returns a Mailer. clock dates the messages (time.Now when nil); hostname names this
// machine in the EHLO greeting and the Message-ID (DefaultHostname when empty).
func New(clock func() time.Time, hostname string) *Mailer {
	if clock == nil {
		clock = time.Now
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" || strings.ContainsAny(hostname, " \t\r\n<>@") {
		hostname = DefaultHostname
	}
	return &Mailer{clock: clock, hostname: hostname}
}

// Send delivers m through server, authenticating with server.Username and password when a
// user name is set. A nil error means the server accepted the message.
func (x *Mailer) Send(ctx context.Context, server domain.SMTPSettings, password string, m domain.Mail) error {
	err := x.send(ctx, server, password, m)
	if err == nil {
		return nil
	}
	ctxErr := ctx.Err()
	if dl, ok := ctx.Deadline(); ctxErr == nil && ok && !time.Now().Before(dl) {
		// The connection deadline (ctx's own) can fire just before ctx reports it.
		ctxErr = context.DeadlineExceeded
	}
	if ctxErr != nil && !errors.Is(err, ctxErr) {
		err = fmt.Errorf("%w (%w)", ctxErr, err)
	}
	return redact(err, password)
}

func (x *Mailer) send(ctx context.Context, server domain.SMTPSettings, password string, m domain.Mail) error {
	if server.TLS != domain.SMTPStartTLS && server.TLS != domain.SMTPTLS && server.TLS != domain.SMTPNone {
		return fmt.Errorf("mail: unknown TLS mode %q", server.TLS)
	}
	if server.Host == "" || server.Port <= 0 || server.Port > 65535 {
		return errors.New("mail: server host and port are required")
	}
	from, to, msg, err := x.build(server.From, m)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mail: %w", err)
	}

	addr := net.JoinHostPort(server.Host, strconv.Itoa(server.Port))
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("mail: connect to %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }() // a second close (after QUIT) only returns an error
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(DefaultTimeout)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("mail: set deadline: %w", err)
	}
	// Close the connection when ctx is cancelled mid-session; blocked reads then fail.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	tlsConfig := &tls.Config{ServerName: server.Host, MinVersion: tls.VersionTLS12, RootCAs: x.rootCAs}
	if server.TLS == domain.SMTPTLS {
		tc := tls.Client(conn, tlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("mail: TLS handshake with %s: %w", addr, err)
		}
		conn = tc
	}

	c, err := smtp.NewClient(conn, server.Host)
	if err != nil {
		return fmt.Errorf("mail: greeting from %s: %w", addr, err)
	}
	defer c.Close()
	if err := c.Hello(x.hostname); err != nil {
		return fmt.Errorf("mail: EHLO: %w", err)
	}
	if server.TLS == domain.SMTPStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("mail: %s does not offer STARTTLS, which is required", addr)
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("mail: STARTTLS: %w", err)
		}
	}
	if server.Username != "" {
		// PlainAuth refuses to send the password over an unencrypted non-local connection.
		if err := c.Auth(smtp.PlainAuth("", server.Username, password, server.Host)); err != nil {
			return fmt.Errorf("mail: authenticate as %s: %w", server.Username, err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("mail: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("mail: write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: message refused: %w", err)
	}
	// The server has accepted the message: a failed QUIT must not cause a second delivery.
	_ = c.Quit()
	return nil
}

// build validates the addresses and renders the RFC 5322 message. It returns the envelope
// sender and recipient.
func (x *Mailer) build(fromHeader string, m domain.Mail) (from, to string, msg []byte, err error) {
	for name, v := range map[string]string{"from": fromHeader, "to": m.To, "subject": m.Subject, "id": m.ID} {
		if strings.ContainsAny(v, "\r\n") {
			return "", "", nil, fmt.Errorf("mail: %s contains a line break", name)
		}
	}
	if m.ID == "" || strings.ContainsAny(m.ID, " \t<>@\"()[]\\,;:") {
		return "", "", nil, fmt.Errorf("mail: invalid message id %q", m.ID)
	}
	fa, err := netmail.ParseAddress(fromHeader)
	if err != nil {
		return "", "", nil, fmt.Errorf("mail: invalid From address: %w", err)
	}
	ta, err := netmail.ParseAddress(m.To)
	if err != nil {
		return "", "", nil, fmt.Errorf("mail: invalid To address: %w", err)
	}

	subject := m.Subject
	if !isASCII(subject) {
		subject = mime.QEncoding.Encode("utf-8", subject)
	}
	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", fa.String())
	header("To", ta.String())
	header("Subject", subject)
	header("Date", x.clock().Format(time.RFC1123Z))
	header("Message-ID", "<"+m.ID+"@"+x.hostname+">")
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")

	body := strings.ReplaceAll(m.Body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	qp := quotedprintable.NewWriter(&b) // text mode: writes line breaks as CRLF
	if _, err := qp.Write([]byte(body)); err != nil {
		return "", "", nil, fmt.Errorf("mail: encode body: %w", err)
	}
	if err := qp.Close(); err != nil {
		return "", "", nil, fmt.Errorf("mail: encode body: %w", err)
	}
	return fa.Address, ta.Address, b.Bytes(), nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// redact removes the password from an error message (a server could echo it back). The
// redacted error keeps no chain to the original, so the password cannot leak through it.
func redact(err error, password string) error {
	if password == "" || !strings.Contains(err.Error(), password) {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), password, "[redacted]"))
}
