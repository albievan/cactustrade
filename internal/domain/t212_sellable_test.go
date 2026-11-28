package domain

import (
	"math/rand/v2"
	"testing"
)

// TestT212SellableNeverTheOwnersShares: whatever cactusTrade bought and has sold so far, what
// Trading 212 makes available (the owner's own shares in the ticker included) and what our
// resting sells may still sell, a new sell never sells more than bought − sold − resting, nor
// more than available − resting, is never negative and is whole steps (D44).
func TestT212SellableNeverTheOwnersShares(t *testing.T) {
	r := rand.New(rand.NewPCG(45, 17))
	for range 50_000 {
		bought := Micros(r.Int64N(20)) * wholeShare
		sold := Micros(r.Int64N(int64(bought)/int64(wholeShare)+2)) * wholeShare
		if r.IntN(4) == 0 {
			sold = Micros(r.Int64N(int64(bought) + 1)) // a fractional fill
		}
		owners := Micros(r.Int64N(30)) * wholeShare // the owner's own shares in the same ticker
		available := max(bought-sold, 0) + owners
		if r.IntN(3) == 0 {
			available = Micros(r.Int64N(int64(available) + 1)) // some held back by Trading 212
		}
		resting := Micros(r.Int64N(5)) * wholeShare
		step := []Micros{wholeShare, wholeShare / 10, 0}[r.IntN(3)]
		q := T212Sellable(bought, sold, available, resting, step)
		switch {
		case q < 0:
			t.Fatalf("negative %s", q)
		case q > 0 && q > bought-sold-resting:
			t.Fatalf("sells %s: bought %s, sold %s, resting %s (the owner's shares)", q, bought, sold, resting)
		case q > 0 && q > available-resting:
			t.Fatalf("sells %s: available %s, resting %s", q, available, resting)
		case step > 0 && q%step != 0:
			t.Fatalf("%s is not a multiple of %s", q, step)
		}
	}
}

func TestT212SellableTable(t *testing.T) {
	const s = wholeShare
	cases := []struct {
		name                                 string
		bought, sold, available, resting, st Micros
		want                                 Micros
	}{
		{"all of ours", 5 * s, 0, 5 * s, 0, s, 5 * s},
		{"after a partial sale, the owner holding 10 more", 5 * s, 2 * s, 13 * s, 0, s, 3 * s},
		{"less available than ours", 5 * s, 0, 3 * s, 0, s, 3 * s},
		{"a resting stop", 5 * s, 1 * s, 14 * s, 4 * s, s, 0},
		{"a fraction sold: whole shares only", 5 * s, s / 2, 20 * s, 0, s, 4 * s},
		{"oversold", 5 * s, 6 * s, 20 * s, 0, s, 0},
		{"no step", 5 * s, s / 2, 20 * s, 0, 0, 4*s + s/2},
	}
	for _, c := range cases {
		if got := T212Sellable(c.bought, c.sold, c.available, c.resting, c.st); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	if u := (AgentPosition{Quantity: 5 * s, Sold: 7 * s}).Unsold(); u != 0 {
		t.Errorf("Unsold of an oversold position = %s", u)
	}
	if u := (AgentPosition{Quantity: 5 * s, Sold: 2 * s}).Unsold(); u != 3*s {
		t.Errorf("Unsold = %s", u)
	}
}
