package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// releaseRoot is the repository root.
func releaseRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// sh runs a command in dir with extra environment and returns its output and error.
func sh(t *testing.T, dir string, env []string, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// releaseKey makes a throw-away release key and its allowed-signers file.
func releaseKey(t *testing.T, dir, name string) (key, signers string) {
	t.Helper()
	key = filepath.Join(dir, name)
	if out, err := sh(t, dir, nil, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "test-release", "-f", key); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	signers = filepath.Join(dir, name+".signers")
	line := `cactustrade-release namespaces="cactustrade-release" ` + strings.TrimSpace(string(pub)) + "\n"
	if err := os.WriteFile(signers, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return key, signers
}

// writeExec writes an executable file (the test's stand-in release programs).
func writeExec(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

// TestReleaseSignAndVerify is PLAN 7.9 (OD31): the release scripts sum, sign and verify a
// dist folder; a changed binary, a changed SHA256SUMS or another key's signature fails; and
// setup.sh installs only a release that passes both checks.
func TestReleaseSignAndVerify(t *testing.T) {
	root := releaseRoot(t)
	scripts := filepath.Join(root, "scripts")
	work := t.TempDir()
	key, signers := releaseKey(t, work, "release")
	otherKey, _ := releaseKey(t, work, "other")

	// a dist folder: this machine's "binary" is a script that answers `version`
	dist := filepath.Join(work, "dist")
	if err := os.Mkdir(dist, 0o700); err != nil {
		t.Fatal(err)
	}
	asset := "cactustrade-" + runtime.GOOS + "-" + runtime.GOARCH
	bin := "#!/bin/sh\necho '9.8.7 (build 42)'\n"
	writeExec(t, filepath.Join(dist, asset), bin)
	for _, other := range []string{"cactustrade-windows-amd64.exe", "cactustrade-linux-riscv"} {
		if err := os.WriteFile(filepath.Join(dist, other), []byte("other "+other), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := sh(t, root, nil, filepath.Join(scripts, "release-sums.sh"), dist); err != nil {
		t.Fatalf("sums: %v\n%s", err, out)
	}
	sums, _ := os.ReadFile(filepath.Join(dist, "SHA256SUMS"))
	if !regexp.MustCompile(`(?m)^[0-9a-f]{64}  `+regexp.QuoteMeta(asset)+`$`).Match(sums) || strings.Count(string(sums), "\n") != 3 {
		t.Fatalf("SHA256SUMS:\n%s", sums)
	}
	verify := func() error {
		t.Helper()
		_, err := sh(t, root, nil, filepath.Join(scripts, "release-verify.sh"), dist, signers)
		return err
	}
	sign := func(k string) {
		t.Helper()
		if out, err := sh(t, root, nil, filepath.Join(scripts, "release-sign.sh"), k, dist); err != nil {
			t.Fatalf("sign: %v\n%s", err, out)
		}
	}
	if err := verify(); err == nil {
		t.Fatal("verified without a signature")
	}
	sign(key)
	if err := verify(); err != nil {
		t.Fatalf("a good release does not verify: %v", err)
	}
	// setup.sh installs it (from a local folder), keeping the old program
	home := t.TempDir()
	writeExec(t, filepath.Join(home, "cactustrade"), "#!/bin/sh\necho '1.0.0 (build 1)'\n")
	setup := func(env ...string) (string, error) {
		t.Helper()
		return sh(t, home, append([]string{"CACTUSTRADE_RELEASE_URL=file://" + dist}, env...), "sh", filepath.Join(root, "setup.sh"))
	}
	out, err := setup("CACTUSTRADE_RELEASE_SIGNERS=" + signers)
	if err != nil || !strings.Contains(out, "Release signature: valid.") || !strings.Contains(out, "Installed cactustrade 9.8.7 (build 42)") {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	installed := func() string {
		b, _ := os.ReadFile(filepath.Join(home, "cactustrade"))
		return string(b)
	}
	if installed() != bin {
		t.Fatal("not installed")
	}
	if _, err := os.Stat(filepath.Join(home, "cactustrade.old")); err != nil {
		t.Fatal("the old program was not kept")
	}

	refused := func(what string, env ...string) {
		t.Helper()
		before := installed()
		out, err := setup(env...)
		if err == nil || strings.Contains(out, "Installed cactustrade") || installed() != before {
			t.Errorf("%s: setup installed: %v\n%s", what, err, out)
		}
		if err := verify(); err == nil && what != "no published key" {
			t.Errorf("%s: release-verify passed", what)
		}
	}
	// the embedded signer is still empty (no key published): setup refuses outright
	refused("no published key")

	// a changed binary
	writeExec(t, filepath.Join(dist, asset), bin+"echo changed\n")
	refused("changed binary", "CACTUSTRADE_RELEASE_SIGNERS="+signers)
	writeExec(t, filepath.Join(dist, asset), bin)
	// a changed SHA256SUMS
	if err := os.WriteFile(filepath.Join(dist, "SHA256SUMS"), append(append([]byte{}, sums...), []byte(strings.Repeat("0", 64)+"  extra\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	refused("changed SHA256SUMS", "CACTUSTRADE_RELEASE_SIGNERS="+signers)
	if err := os.WriteFile(filepath.Join(dist, "SHA256SUMS"), sums, 0o600); err != nil {
		t.Fatal(err)
	}
	// signed with another key
	sign(otherKey)
	refused("another key", "CACTUSTRADE_RELEASE_SIGNERS="+signers)
	// no signature at all
	if err := os.Remove(filepath.Join(dist, "SHA256SUMS.sig")); err != nil {
		t.Fatal(err)
	}
	refused("unsigned", "CACTUSTRADE_RELEASE_SIGNERS="+signers)
	// signing without a key fails
	if _, err := sh(t, root, nil, filepath.Join(scripts, "release-sign.sh"), filepath.Join(work, "missing"), dist); err == nil {
		t.Fatal("signed without a key")
	}
	// the repository's own signers file has no key yet: verify refuses
	sign(key)
	if _, err := sh(t, root, nil, filepath.Join(scripts, "release-verify.sh"), dist); err == nil {
		t.Fatal("verified against the empty published signers")
	}
}

// TestReleaseSignerAgrees: setup.sh, setup.ps1 and scripts/allowed_signers publish the same
// release key line (today: none), and both setup scripts fetch the asset names make dist
// builds.
func TestReleaseSignerAgrees(t *testing.T) {
	root := releaseRoot(t)
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	var published string
	for line := range strings.Lines(read("scripts/allowed_signers")) {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "#") {
			if published != "" {
				t.Fatal("scripts/allowed_signers: one release key line only")
			}
			published = l
		}
	}
	shSigner := regexp.MustCompile(`(?m)^SIGNER='([^']*)'$`).FindStringSubmatch(read("setup.sh"))
	psSigner := regexp.MustCompile(`(?m)^\$SIGNER = '([^']*)'$`).FindStringSubmatch(read("setup.ps1"))
	if shSigner == nil || psSigner == nil {
		t.Fatal("no SIGNER line in setup.sh or setup.ps1")
	}
	if shSigner[1] != published || psSigner[1] != published {
		t.Fatalf("release key lines differ: allowed_signers %q, setup.sh %q, setup.ps1 %q", published, shSigner[1], psSigner[1])
	}
	if published != "" && !strings.HasPrefix(published, `cactustrade-release namespaces="cactustrade-release" ssh-`) {
		t.Fatalf("allowed_signers line %q", published)
	}
	mk := read("Makefile")
	for _, want := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64", `cactustrade-$$os-$$arch$$ext`} {
		if !strings.Contains(mk, want) {
			t.Errorf("make dist does not build %s", want)
		}
	}
	if !strings.Contains(read("setup.sh"), `asset="cactustrade-$os-$arch"`) || !strings.Contains(read("setup.ps1"), `$asset = "cactustrade-windows-$arch.exe"`) {
		t.Error("the setup scripts fetch other asset names than make dist builds")
	}
}
