package domain

import (
	"errors"
	"math"
	"math/big"
	"math/rand/v2"
	"testing"
)

func TestLLMConfigValidate(t *testing.T) {
	ok := []LLMConfig{
		{Provider: "anthropic"},
		{Provider: "openai", Model: "some-model"},
		{Provider: "ollama", Model: "llama", BaseURL: "http://gpu:11434/v1"},
		{Provider: "ollama_local", Model: "llama", TimeoutSeconds: 120},
	}
	for _, c := range ok {
		if err := c.Validate(); err != nil {
			t.Errorf("%+v: %v", c, err)
		}
	}
	bad := []LLMConfig{
		{Provider: "nope"},
		{Provider: "openai"},
		{Provider: "openai", Model: "a b"},
		{Provider: "ollama", Model: "llama"},
		{Provider: "openai", Model: "m", BaseURL: "ftp://x"},
		{Provider: "openai", Model: "m", BaseURL: "https://u:p@x/v1"},
		{Provider: "openai", Model: "m", BaseURL: "https://x/v1?k=1"},
		{Provider: "openai", Model: "m", TimeoutSeconds: 9999},
	}
	for _, c := range bad {
		if err := c.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v accepted", c)
		}
	}
	if (LLMConfig{Provider: "openai"}).Endpoint() != "https://api.openai.com/v1" || (LLMConfig{Provider: "openai", BaseURL: "https://proxy/v1/"}).Endpoint() != "https://proxy/v1" {
		t.Fatal("endpoint")
	}
	if _, ok := LLMProviderByID("x"); ok {
		t.Fatal("unknown provider found")
	}
}

func TestClampVerdict(t *testing.T) {
	for _, c := range []struct {
		in   Verdict
		kind VerdictKind
		bp   BasisPoints
	}{
		{Verdict{Kind: VerdictApprove, SizeBp: 30_000}, VerdictApprove, FullSize},
		{Verdict{Kind: VerdictApprove}, VerdictApprove, FullSize},
		{Verdict{Kind: VerdictReduce, SizeBp: 5_000}, VerdictReduce, 5_000},
		{Verdict{Kind: VerdictReduce, SizeBp: 70_000}, VerdictReduce, FullSize},
		{Verdict{Kind: VerdictReduce, SizeBp: 0}, VerdictReject, 0},
		{Verdict{Kind: VerdictReduce, SizeBp: -5}, VerdictReject, 0},
		{Verdict{Kind: "enlarge", SizeBp: 20_000}, VerdictReject, 0},
		{Verdict{Kind: VerdictReject, SizeBp: FullSize}, VerdictReject, 0},
	} {
		got := ClampVerdict(c.in)
		if got.Kind != c.kind || got.SizeBp != c.bp {
			t.Errorf("%+v → %+v", c.in, got)
		}
	}
}

func TestApplyVerdict(t *testing.T) {
	for _, c := range []struct {
		size Cents
		v    Verdict
		want Cents
	}{
		{100_000, Verdict{Kind: VerdictApprove, SizeBp: 30_000}, 100_000},
		{100_000, Verdict{Kind: VerdictReduce, SizeBp: 5_000}, 50_000},
		{100_001, Verdict{Kind: VerdictReduce, SizeBp: 5_000}, 50_000}, // rounded down
		{99, Verdict{Kind: VerdictReduce, SizeBp: 1}, 0},
		{100_000, Verdict{Kind: VerdictReduce, SizeBp: 30_000}, 100_000},
		{100_000, Verdict{Kind: VerdictReject, SizeBp: FullSize}, 0},
		{100_000, Verdict{Kind: "increase", SizeBp: 30_000}, 0},
		{-5, Verdict{Kind: VerdictApprove}, 0},
		{math.MaxInt64, Verdict{Kind: VerdictReduce, SizeBp: 9_999}, math.MaxInt64/10_000*9_999 + (math.MaxInt64%10_000)*9_999/10_000},
		{math.MaxInt64, Verdict{Kind: VerdictApprove}, math.MaxInt64},
	} {
		if got := ApplyVerdict(c.size, c.v); got != c.want {
			t.Errorf("ApplyVerdict(%d, %+v) = %d, want %d", c.size, c.v, got, c.want)
		}
	}
}

// TestApplyVerdictNeverEnlarges: for random sizes, basis points and kinds (including ones the
// reviewer must never use), the size after the verdict is never negative and never larger
// than before, a rejection is always 0, and a reduction is at most the stated share. NaN and
// infinities never reach the domain: the LLM adapter converts them (its own property test).
func TestApplyVerdictNeverEnlarges(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	kinds := []VerdictKind{VerdictApprove, VerdictReduce, VerdictReject, "increase", "APPROVE", ""}
	for i := range 100_000 {
		size := Cents(r.Int64())
		if i%3 == 0 {
			size = Cents(r.Int64N(10_000_000) - 1_000)
		}
		bp := BasisPoints(r.Int64N(60_000) - 20_000)
		if i%7 == 0 {
			bp = BasisPoints(r.Int64())
			if r.IntN(2) == 0 {
				bp = -bp
			}
		}
		v := Verdict{Kind: kinds[r.IntN(len(kinds))], SizeBp: bp}
		got := ApplyVerdict(size, v)
		switch {
		case got < 0 || got > max(size, 0):
			t.Fatalf("ApplyVerdict(%d, %+v) = %d", size, v, got)
		case v.Kind != VerdictApprove && v.Kind != VerdictReduce && got != 0:
			t.Fatalf("a %q verdict left %d", v.Kind, got)
		case v.Kind == VerdictReduce && bp > 0 && bp < FullSize && size > 0:
			// exactly: got × 10 000 ≤ size × bp, and got is the floor (no more than 1 short)
			lhs := new(big.Int).Mul(big.NewInt(int64(got)), big.NewInt(int64(FullSize)))
			rhs := new(big.Int).Mul(big.NewInt(int64(size)), big.NewInt(int64(bp)))
			if lhs.Cmp(rhs) > 0 || new(big.Int).Sub(rhs, lhs).Cmp(big.NewInt(int64(FullSize))) >= 0 {
				t.Fatalf("reduce %d bp of %d = %d", bp, size, got)
			}
		}
	}
}
