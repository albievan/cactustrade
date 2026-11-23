package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestDemuxServesBothSchemesOnOnePort: HTTPS is served, plain HTTP on the same port is
// redirected to HTTPS (same host, port, path and query), and an idle client is dropped.
func TestDemuxServesBothSchemesOnOnePort(t *testing.T) {
	dir := t.TempDir()
	tc, _, err := tlsConfig("", "", dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := newDemux(ln, 200*time.Millisecond)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "secure")
	}), TLSConfig: tc, ReadHeaderTimeout: time.Second}
	redirect := &http.Server{Handler: http.HandlerFunc(redirectToHTTPS), ReadHeaderTimeout: time.Second}
	go func() { _ = srv.ServeTLS(d.tls, "", "") }()
	go func() { _ = redirect.Serve(d.plain) }()
	t.Cleanup(func() { _ = srv.Close(); _ = redirect.Close() })
	addr := ln.Addr().String()

	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := plain.Get("http://" + addr + "/login?x=1")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusPermanentRedirect || resp.Header.Get("Location") != "https://"+addr+"/login?x=1" {
		t.Fatalf("redirect = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}

	pool := x509.NewCertPool()
	pool.AddCert(tc.Certificates[0].Leaf)
	if tc.Certificates[0].Leaf == nil {
		leaf, err := x509.ParseCertificate(tc.Certificates[0].Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		pool.AddCert(leaf)
	}
	secure := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "localhost", MinVersion: tls.VersionTLS12}}}
	resp, err = secure.Get("https://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "secure" {
		t.Fatalf("https body = %q", body)
	}

	// a client that connects and says nothing is closed after the sniff timeout
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("idle connection was not closed")
	}
	_ = c.Close()

	// closing stops both sub-listeners
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.plain.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close = %v", err)
	}
	if d.tls.Addr().String() != addr {
		t.Fatal("Addr")
	}
}

func TestRedirectWithoutHost(t *testing.T) {
	r, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	for host, want := range map[string]int{
		"": 400, "evil.example/x": 400, "a b": 400, "host:8453:1": 400,
		"192.168.1.171:8453": 308, "[::1]:8453": 308, "cactus.local": 308,
	} {
		r.Host = host
		w := &recorder{h: http.Header{}}
		redirectToHTTPS(w, r)
		if w.code != want {
			t.Errorf("Host %q: code = %d, want %d", host, w.code, want)
		}
	}
}

type recorder struct {
	h    http.Header
	code int
}

func (r *recorder) Header() http.Header         { return r.h }
func (r *recorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *recorder) WriteHeader(c int)           { r.code = c }
