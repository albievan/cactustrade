package secretstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/albievan/cactustrade/internal/domain"
	"github.com/albievan/cactustrade/internal/ports"
	"github.com/albievan/cactustrade/internal/ports/fakes"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestCipher(t *testing.T) {
	c, err := NewCipher(key(1))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := c.Seal([]byte("my eToro user key"), []byte("etoro_demo_user_key"))
	if err != nil || bytes.Contains(ct, []byte("eToro")) {
		t.Fatal("seal")
	}
	if pt, err := c.Open(ct, []byte("etoro_demo_user_key")); err != nil || string(pt) != "my eToro user key" {
		t.Fatal("open")
	}
	if _, err := c.Open(ct, []byte("llm_api_key")); err == nil {
		t.Fatal("ciphertext moved to another name must fail")
	}
	other, _ := NewCipher(key(2))
	if _, err := other.Open(ct, []byte("etoro_demo_user_key")); err == nil {
		t.Fatal("wrong key must fail")
	}
	if _, err := c.Open([]byte{1, 2}, nil); err == nil {
		t.Fatal("short")
	}
	if _, err := NewCipher(key(1)[:16]); err == nil {
		t.Fatal("AES-128 key accepted")
	}
}

func TestLoadMasterKey(t *testing.T) {
	hexKey := strings.Repeat("ab", 32)
	if k, created, err := LoadMasterKey(hexKey, "", ""); err != nil || created || len(k) != 32 {
		t.Fatal("value")
	}
	if _, _, err := LoadMasterKey("zz", "", ""); err == nil {
		t.Fatal("bad value")
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "mk")
	_ = os.WriteFile(f, []byte(hexKey+"\n"), 0o600)
	if k, _, err := LoadMasterKey("", f, ""); err != nil || len(k) != 32 {
		t.Fatal("file")
	}
	if _, _, err := LoadMasterKey("", filepath.Join(dir, "missing"), ""); err == nil {
		t.Fatal("missing file")
	}
	gen := filepath.Join(dir, "data", "master.key")
	k1, created, err := LoadMasterKey("", "", gen)
	if err != nil || !created {
		t.Fatal("generate", err)
	}
	if st, _ := os.Stat(gen); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	k2, created, err := LoadMasterKey("", "", gen)
	if err != nil || created || !bytes.Equal(k1, k2) {
		t.Fatal("reload generated")
	}
	if _, _, err := LoadMasterKey("", "", ""); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatal("no source")
	}
	_ = os.WriteFile(gen, []byte("garbage"), 0o600)
	if _, _, err := LoadMasterKey("", "", gen); err == nil {
		t.Fatal("corrupt generated key")
	}
}

func TestDBAndEnv(t *testing.T) {
	ctx := context.Background()
	st := fakes.NewStore()
	c, _ := NewCipher(key(3))
	clk := &fakes.Clock{T: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	db := NewDB(st.Secrets(), c, clk)
	if err := db.Put(ctx, domain.CredLLMAPIKey, "llm-secret"); err != nil {
		t.Fatal(err)
	}
	rec, _ := st.Secrets().Get(ctx, "llm_api_key")
	if bytes.Contains(rec.Ciphertext, []byte("llm-secret")) {
		t.Fatal("stored in plaintext")
	}
	if v, err := db.Get(ctx, domain.CredLLMAPIKey); err != nil || v != "llm-secret" {
		t.Fatal("get")
	}
	if _, err := db.Get(ctx, domain.CredEtoroDemoAPIKey); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("missing")
	}
	// a ciphertext copied under another name does not open
	_ = st.Secrets().Put(ctx, ports.SecretRecord{Name: "etoro_real_user_key", Ciphertext: rec.Ciphertext})
	if _, err := db.Get(ctx, domain.CredEtoroRealUserKey); err == nil {
		t.Fatal("swapped ciphertext opened")
	}
	_ = db.Delete(ctx, domain.CredEtoroRealUserKey)

	env := WithEnv(db, map[domain.CredentialName]string{domain.CredEtoroDemoAPIKey: "pub-from-env"})
	if v, _ := env.Get(ctx, domain.CredEtoroDemoAPIKey); v != "pub-from-env" {
		t.Fatal("env get")
	}
	if v, _ := env.Get(ctx, domain.CredLLMAPIKey); v != "llm-secret" {
		t.Fatal("base get")
	}
	if err := env.Put(ctx, domain.CredEtoroDemoAPIKey, "x"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("env put must be refused")
	}
	if err := env.Delete(ctx, domain.CredEtoroDemoAPIKey); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("env delete must be refused")
	}
	if err := env.Put(ctx, domain.CredEtoroDemoUserKey, "pk-ui"); err != nil {
		t.Fatal(err)
	}
	s, err := env.Status(ctx)
	if err != nil || s[domain.CredEtoroDemoAPIKey].Source != ports.SourceEnv || s[domain.CredLLMAPIKey].Source != ports.SourceUI ||
		!s[domain.CredLLMAPIKey].UpdatedAt.Equal(clk.T) || len(s) != 3 {
		t.Fatalf("status %+v %v", s, err)
	}
	if err := env.Delete(ctx, domain.CredEtoroDemoUserKey); err != nil {
		t.Fatal(err)
	}
}

func TestFromEnvironment(t *testing.T) {
	files := map[string]string{"/run/secrets/pk": "pk-from-file\n"}
	read := func(p string) ([]byte, error) {
		if v, ok := files[p]; ok {
			return []byte(v), nil
		}
		return nil, os.ErrNotExist
	}
	env := map[string]string{"ETORO_DEMO_PUB": "  pub-value ", "ETORO_DEMO_PK_FILE": "/run/secrets/pk"}
	got, err := FromEnvironment(func(k string) string { return env[k] }, read)
	if err != nil || got[domain.CredEtoroDemoAPIKey] != "pub-value" || got[domain.CredEtoroDemoUserKey] != "pk-from-file" {
		t.Fatalf("got %v %v", got, err)
	}
	if got, _ := FromEnvironment(func(string) string { return "" }, read); len(got) != 0 {
		t.Fatal("empty env")
	}
	env = map[string]string{"ETORO_DEMO_PK_FILE": "/nope"}
	if _, err := FromEnvironment(func(k string) string { return env[k] }, read); err == nil {
		t.Fatal("unreadable file must fail")
	}
	env = map[string]string{"ETORO_DEMO_PUB": "bad\x01value"}
	if _, err := FromEnvironment(func(k string) string { return env[k] }, read); err == nil {
		t.Fatal("control characters must fail")
	}
}
