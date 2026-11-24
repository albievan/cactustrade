package main

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"regexp"
	"sync"
	"time"

	httpapi "github.com/albievan/cactustrade/internal/adapters/http"
)

// tlsHandshake is the first byte of every TLS connection (record type "handshake").
const tlsHandshake = 0x16

// demux splits one listening port in two by each connection's first byte: TLS handshakes go
// to the HTTPS server, anything else to a tiny HTTP server that redirects to HTTPS. So a
// browser pointed at http://host:8443 lands on https://host:8443 instead of seeing
// "Client sent an HTTP request to an HTTPS server".
type demux struct {
	ln           net.Listener
	tls, plain   *subListener
	done         chan struct{}
	once         sync.Once
	sniffTimeout time.Duration
}

func newDemux(ln net.Listener, sniffTimeout time.Duration) *demux {
	d := &demux{ln: ln, done: make(chan struct{}), sniffTimeout: sniffTimeout}
	d.tls = &subListener{d: d, ch: make(chan net.Conn)}
	d.plain = &subListener{d: d, ch: make(chan net.Conn)}
	go d.loop()
	return d
}

func (d *demux) loop() {
	for {
		c, err := d.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				d.close()
				return
			}
			time.Sleep(50 * time.Millisecond) // e.g. too many open files: back off, keep serving
			continue
		}
		go d.route(c)
	}
}

// route reads the first byte (bounded by sniffTimeout, so an idle client cannot pin a
// goroutine) and hands the connection, byte included, to the matching server.
func (d *demux) route(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(d.sniffTimeout))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		_ = c.Close()
		return
	}
	dst := d.plain
	if first[0] == tlsHandshake {
		dst = d.tls
	}
	select {
	case dst.ch <- &peekedConn{Conn: c, r: br}:
	case <-d.done:
		_ = c.Close()
	}
}

func (d *demux) close() {
	d.once.Do(func() { close(d.done) })
}

// Close stops accepting on the shared port.
func (d *demux) Close() error {
	d.close()
	return d.ln.Close()
}

type subListener struct {
	d  *demux
	ch chan net.Conn
}

func (l *subListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.d.done:
		return nil, net.ErrClosed
	}
}

func (l *subListener) Close() error   { return l.d.Close() }
func (l *subListener) Addr() net.Addr { return l.d.ln.Addr() }

// peekedConn replays the sniffed byte before the rest of the stream.
type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// hostRE is a plain host[:port]: a DNS name, IPv4 or bracketed IPv6 address, nothing else.
var hostRE = regexp.MustCompile(`^([A-Za-z0-9.-]{1,253}|\[[0-9A-Fa-f:.]{2,45}\])(:[0-9]{1,5})?$`)

// redirectHandler is the plain-HTTP server's handler: the redirect with the same security
// headers as every other response (PLAN 7.4).
func redirectHandler() http.Handler {
	return httpapi.SecurityHeaders(http.HandlerFunc(redirectToHTTPS))
}

// redirectToHTTPS sends plain-HTTP requests to the same host, port and path over HTTPS. The
// target is the host the client itself asked for (validated), so it cannot be pointed at
// another site by anyone but that client.
func redirectToHTTPS(w http.ResponseWriter, r *http.Request) {
	if !hostRE.MatchString(r.Host) {
		http.Error(w, "use https", http.StatusBadRequest)
		return
	}
	w.Header().Set("Connection", "close")
	http.Redirect(w, r, "https://"+r.Host+r.URL.RequestURI(), http.StatusPermanentRedirect)
}
