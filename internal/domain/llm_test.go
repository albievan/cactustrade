package domain

import (
	"errors"
	"math"
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
		mult float64
	}{
		{Verdict{Kind: VerdictApprove, SizeMultiplier: 3}, VerdictApprove, 1},
		{Verdict{Kind: VerdictReduce, SizeMultiplier: 0.5}, VerdictReduce, 0.5},
		{Verdict{Kind: VerdictReduce, SizeMultiplier: 7}, VerdictReduce, 1},
		{Verdict{Kind: VerdictReduce, SizeMultiplier: 0}, VerdictReject, 0},
		{Verdict{Kind: VerdictReduce, SizeMultiplier: math.NaN()}, VerdictReject, 0},
		{Verdict{Kind: "enlarge", SizeMultiplier: 2}, VerdictReject, 0},
		{Verdict{Kind: VerdictReject, SizeMultiplier: 1}, VerdictReject, 0},
	} {
		got := ClampVerdict(c.in)
		if got.Kind != c.kind || got.SizeMultiplier != c.mult {
			t.Errorf("%+v → %+v", c.in, got)
		}
	}
}
