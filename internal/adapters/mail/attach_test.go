package mail

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	netmail "net/mail"
	"strings"
	"testing"

	"github.com/albievan/cactustrade/internal/domain"
)

// TestAttachmentsDecodeBackIdentical: a mail with attachments is multipart/mixed (R30): the
// text part first, then each file as application/pdf in base64 with its name; every file
// decodes back to exactly the bytes sent, and no line is longer than RFC 5322 allows.
func TestAttachmentsDecodeBackIdentical(t *testing.T) {
	s := startFake(t, &fakeSMTP{}, false)
	big := make([]byte, 200_000)
	for i := range big {
		big[i] = byte(i * 7) // every byte value, also ".", CR and LF
	}
	m := testMail()
	m.Body = "Your weekly statement (£, A$) is attached.\n.dot line"
	m.Attachments = []domain.Attachment{
		{Filename: "cactusTrade-statement-2026-W48-weekly-combined.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4\n%\xe2\xe3\n")},
		{Filename: "cactusTrade-statement-2026-W48-weekly-account-demo.pdf", ContentType: "application/pdf", Data: big},
	}
	if err := newTestMailer().Send(ctxT(t), server(s.port(t), domain.SMTPNone), "", m); err != nil {
		t.Fatalf("Send: %v", err)
	}
	_, _, data, _, _, _ := s.snapshot()
	for _, l := range strings.Split(data, "\r\n") {
		if len(l) > 998 || strings.ContainsAny(l, "\r\n") {
			t.Fatalf("line too long or bare line break: %q", l[:min(len(l), 80)])
		}
	}
	msg, err := netmail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if msg.Header.Get("MIME-Version") != "1.0" || msg.Header.Get("Subject") != m.Subject {
		t.Fatalf("headers %v", msg.Header)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/mixed" || params["boundary"] == "" {
		t.Fatalf("Content-Type %q: %v", msg.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	text, err := mr.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	// multipart.Reader decodes quoted-printable itself and drops that header: check the raw
	// message for it.
	if text.Header.Get("Content-Type") != "text/plain; charset=utf-8" || !strings.Contains(data, "Content-Transfer-Encoding: quoted-printable\r\n") {
		t.Fatalf("text part %v", text.Header)
	}
	body, _ := io.ReadAll(text)
	if string(body) != "Your weekly statement (£, A$) is attached.\r\n.dot line\r\n" {
		raw, _ := io.ReadAll(quotedprintable.NewReader(bytes.NewReader(body)))
		t.Fatalf("text %q (%q)", body, raw)
	}
	for i, want := range m.Attachments {
		p, err := mr.NextPart()
		if err != nil {
			t.Fatalf("part %d: %v", i, err)
		}
		ct, cp, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
		disp, dp, _ := mime.ParseMediaType(p.Header.Get("Content-Disposition"))
		if ct != "application/pdf" || cp["name"] != want.Filename || disp != "attachment" || dp["filename"] != want.Filename ||
			p.FileName() != want.Filename || p.Header.Get("Content-Transfer-Encoding") != "base64" {
			t.Fatalf("part %d headers %v", i, p.Header)
		}
		got, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, p))
		if err != nil || !bytes.Equal(got, want.Data) {
			t.Fatalf("part %d: %d bytes (want %d) %v", i, len(got), len(want.Data), err)
		}
	}
	if _, err := mr.NextPart(); err != io.EOF {
		t.Fatalf("more parts: %v", err)
	}
}

// TestRefusesUnsafeAttachmentsAndSendsNothing: a header-injecting or path-like file name, a
// type other than PDF and more than 10 MiB are refused before any connection.
func TestRefusesUnsafeAttachmentsAndSendsNothing(t *testing.T) {
	s := startFake(t, &fakeSMTP{}, false)
	cases := map[string]domain.Attachment{
		"CRLF in name":  {Filename: "a.pdf\r\nX-Evil: 1", ContentType: "application/pdf", Data: []byte("x")},
		"quote in name": {Filename: `a".pdf`, ContentType: "application/pdf", Data: []byte("x")},
		"path":          {Filename: "../a.pdf", ContentType: "application/pdf", Data: []byte("x")},
		"type":          {Filename: "a.pdf", ContentType: "text/html", Data: []byte("x")},
		"type params":   {Filename: "a.pdf", ContentType: "application/pdf; x=\"\r\n\"", Data: []byte("x")},
		"too large":     {Filename: "a.pdf", ContentType: "application/pdf", Data: make([]byte, domain.MaxMailAttachmentBytes+1)},
	}
	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			m := testMail()
			m.Attachments = []domain.Attachment{a}
			err := newTestMailer().Send(ctxT(t), server(s.port(t), domain.SMTPNone), "", m)
			if err == nil || !strings.HasPrefix(err.Error(), "mail: ") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if _, _, _, _, _, sessions := s.snapshot(); sessions != 0 {
		t.Errorf("server saw %d sessions, want 0", sessions)
	}
}
