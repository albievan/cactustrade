package system

import (
	"testing"
	"time"
)

func TestSystem(t *testing.T) {
	if d := time.Since(Clock{}.Now()); d < 0 || d > time.Second {
		t.Fatal("clock")
	}
	r := Random{}
	a, b := r.Token(), r.Token()
	if len(a) != 43 || a == b {
		t.Fatalf("token %q %q", a, b)
	}
	if id := r.ID(); len(id) != 36 || id == r.ID() {
		t.Fatal("id")
	}
}
