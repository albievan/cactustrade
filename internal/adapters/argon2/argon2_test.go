package argon2

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
)

func TestHashVerify(t *testing.T) {
	h := New(Params{Memory: 1024, Time: 1, Threads: 1}) // cheap for tests
	enc, err := h.Hash("correct horse battery")
	if err != nil || !strings.HasPrefix(enc, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("Hash = %q %v", enc, err)
	}
	if enc2, _ := h.Hash("correct horse battery"); enc2 == enc {
		t.Fatal("salt must differ")
	}
	if ok, err := h.Verify(enc, "correct horse battery"); !ok || err != nil {
		t.Fatal("verify good")
	}
	if ok, _ := h.Verify(enc, "wrong"); ok {
		t.Fatal("verify bad")
	}
	// a hash made with other parameters still verifies
	if ok, _ := New(Default).Verify(enc, "correct horse battery"); !ok {
		t.Fatal("parameters must come from the hash")
	}
	for _, bad := range []string{
		"", "$bcrypt$x$y$z$w", "$argon2id$v=18$m=1024,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=x$c2FsdA$aGFzaA", "$argon2id$v=19$m=99999999,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=1024,t=1,p=1$!!$aGFzaA", "$argon2id$v=19$m=1024,t=1,p=1$c2FsdA$!!",
	} {
		if _, err := h.Verify(bad, "x"); err == nil {
			t.Errorf("Verify(%q) accepted", bad)
		}
	}
}

// blockingHasher holds every hash until release is closed.
type blockingHasher struct {
	started chan struct{}
	release chan struct{}
}

func (b blockingHasher) Hash(string) (string, error) {
	b.started <- struct{}{}
	<-b.release
	return "h", nil
}

func (b blockingHasher) Verify(string, string) (bool, error) {
	b.started <- struct{}{}
	<-b.release
	return true, nil
}

// TestLimited is PLAN 7.1.4: with every slot busy, a further hash waits its wait and is then
// refused with domain.ErrBusy; once a slot frees, hashing works again.
func TestLimited(t *testing.T) {
	b := blockingHasher{started: make(chan struct{}, 8), release: make(chan struct{})}
	l := NewLimited(b, 2, 20*time.Millisecond)
	done := make(chan error, 2)
	go func() { _, err := l.Hash("a"); done <- err }()
	go func() { _, err := l.Verify("x", "b"); done <- err }()
	<-b.started
	<-b.started
	if _, err := l.Hash("c"); !errors.Is(err, domain.ErrBusy) {
		t.Fatalf("Hash with no slot = %v, want ErrBusy", err)
	}
	if _, err := l.Verify("x", "c"); !errors.Is(err, domain.ErrBusy) {
		t.Fatalf("Verify with no slot = %v, want ErrBusy", err)
	}
	close(b.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if h, err := l.Hash("d"); err != nil || h != "h" {
		t.Fatalf("after release: %q %v", h, err)
	}
	if NewLimited(b, 0, 0).slots == nil || cap(NewLimited(b, 0, 0).slots) != 1 {
		t.Fatal("slots < 1 must mean 1")
	}
}
