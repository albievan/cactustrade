package argon2

import (
	"strings"
	"testing"
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
