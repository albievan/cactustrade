package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

// renderManifest runs `deploy.sh --render --k3s` (no cluster is touched) with env added to
// the environment, and returns the manifest and the warnings.
func renderManifest(t *testing.T, env ...string) (manifest, warnings string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "deploy.sh"), "--render", "--k3s")
	cmd.Env = append(os.Environ(), env...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("deploy.sh --render: %v\n%s", err, errb.String())
	}
	return out.String(), errb.String()
}

// serviceOf returns the cactustrade Service document of a rendered manifest.
func serviceOf(t *testing.T, manifest string) string {
	t.Helper()
	for doc := range strings.SplitSeq(manifest, "\n---\n") {
		if strings.Contains(doc, "kind: Service\n") && strings.Contains(doc, "\n  name: cactustrade\n") {
			return doc
		}
	}
	t.Fatalf("no cactustrade Service in:\n%s", manifest)
	return ""
}

// TestDeployRenderService is PLAN 7.1.6 (F6): with HTTPS through the ingress the app's
// Service is ClusterIP (the pod's plain HTTP is reachable only inside the cluster); a
// NodePort exists only in the explicit no-HTTPS mode, with a loud warning.
func TestDeployRenderService(t *testing.T) {
	m, warn := renderManifest(t, "K8S_HTTPS=true", "K8S_NODE_PORT=30443")
	svc := serviceOf(t, m)
	for _, bad := range []string{"NodePort", "nodePort", "externalTrafficPolicy"} {
		if strings.Contains(svc, bad) {
			t.Errorf("HTTPS mode: the Service has %s:\n%s", bad, svc)
		}
	}
	if !strings.Contains(svc, "type: ClusterIP") || strings.Contains(warn, "PLAIN HTTP") {
		t.Errorf("HTTPS mode: want ClusterIP and no warning:\n%s\n%s", svc, warn)
	}
	for line := range strings.Lines(m) {
		if l := strings.TrimRight(line, "\n"); strings.HasSuffix(l, " # https") || strings.HasSuffix(l, " # plain-http") {
			t.Errorf("render marker left: %q", l)
		}
	}
	if !strings.Contains(m, `GOMEMLIMIT: "400MiB"`) {
		t.Error("GOMEMLIMIT missing from the ConfigMap (PLAN 7.1.4)")
	}

	m, warn = renderManifest(t, "K8S_HTTPS=false", "K8S_NODE_PORT=30443")
	svc = serviceOf(t, m)
	if !strings.Contains(svc, "type: NodePort") || !strings.Contains(svc, "nodePort: 30443") || strings.Contains(svc, "ClusterIP") {
		t.Errorf("no-HTTPS mode: want a NodePort:\n%s", svc)
	}
	if !strings.Contains(warn, "PLAIN HTTP") {
		t.Errorf("no-HTTPS mode without the warning: %q", warn)
	}
}

// TestComposeMemoryLimit is PLAN 7.1.4: the container sets GOMEMLIMIT under its memory limit.
func TestComposeMemoryLimit(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); !strings.Contains(s, "GOMEMLIMIT: 400MiB") || !strings.Contains(s, "mem_limit: 512m") {
		t.Fatal("docker-compose.yml: the app needs GOMEMLIMIT: 400MiB under mem_limit: 512m")
	}
}

// TestImagesPinnedByDigest is PLAN 7.1.17 (part of `make audit`): every third-party image the
// build, compose, k8s, deploy.sh and the Makefile use is pinned by digest (the tag stays for
// the reader), and the web build runs on Node 22.
func TestImagesPinnedByDigest(t *testing.T) {
	root := filepath.Join("..", "..")
	pinned := regexp.MustCompile(`^[a-z0-9][a-z0-9./_-]*(:[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}$`)
	refs := map[string][]string{} // file → image references
	add := func(file, ref string) { refs[file] = append(refs[file], ref) }
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	stages := map[string]bool{"scratch": true}
	for line := range strings.Lines(read("Dockerfile")) {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "FROM" {
			if !stages[f[1]] {
				add("Dockerfile", f[1])
			}
			if len(f) == 4 && strings.EqualFold(f[2], "AS") {
				stages[f[3]] = true
			}
		}
	}
	k8s, err := filepath.Glob(filepath.Join(root, "deploy", "k8s", "*.yaml"))
	if err != nil || len(k8s) == 0 {
		t.Fatalf("k8s manifests: %v", err)
	}
	names := []string{"docker-compose.yml"}
	for _, f := range k8s {
		names = append(names, filepath.Join("deploy", "k8s", filepath.Base(f)))
	}
	imageLine := regexp.MustCompile(`^\s*image:\s*(\S+)`)
	for _, name := range names {
		for line := range strings.Lines(read(name)) {
			if m := imageLine.FindStringSubmatch(line); m != nil && !strings.HasPrefix(m[1], "${") {
				add(name, m[1])
			}
		}
	}
	for _, m := range regexp.MustCompile(`\b(busybox:\S+)`).FindAllStringSubmatch(read("deploy.sh"), -1) {
		add("deploy.sh", m[1])
	}
	for _, m := range regexp.MustCompile(`(?m)^[A-Z_]+_IMAGE\s*\?=\s*(\S+)`).FindAllStringSubmatch(read("Makefile"), -1) {
		add("Makefile", m[1])
	}
	n := 0
	for file, rs := range refs {
		for _, r := range rs {
			n++
			if !pinned.MatchString(r) {
				t.Errorf("%s: image %s is not pinned by digest (image@sha256:…)", file, r)
			}
		}
	}
	for _, file := range append([]string{"Dockerfile", "deploy.sh", "Makefile"}, names...) {
		if len(refs[file]) == 0 {
			t.Errorf("%s: no image reference found (the scan is out of date)", file)
		}
	}
	if n < 10 {
		t.Fatalf("found only %d image references: %v", n, refs)
	}
	if !strings.Contains(read("Dockerfile"), "FROM node:22-") {
		t.Error("the web build must run on Node 22 (PLAN 7.1.17)")
	}
}

// buildSelf builds this command into a temporary folder (deploy.sh's local cactustrade for
// encrypt / decrypt, CACTUSTRADE_BIN).
func buildSelf(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cactustrade")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// mariadbDump is the shape of mariadb-dump's output for the goose table (one extended INSERT
// over several lines, as MariaDB 11 writes it), with schema as the newest version.
func mariadbDump(schema int) string {
	return fmt.Sprintf("-- MariaDB dump 10.19\n/*!40101 SET NAMES utf8mb4 */;\nDROP TABLE IF EXISTS `goose_db_version`;\n"+
		"LOCK TABLES `goose_db_version` WRITE;\nINSERT INTO `goose_db_version` VALUES\n(1,0,1,'2026-11-24 12:32:33'),\n"+
		"(2,1,1,'2026-11-24 12:32:33'),\n(3,%d,1,'2026-11-24 12:32:33');\nUNLOCK TABLES;\n", schema)
}

func gzipped(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	if _, err := zw.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestDeployRestoreDryRun is PLAN 7.6b's documented dry run: `deploy.sh --restore FILE
// --dry-run --dockerdesktop` decrypts and checks a backup without touching any container,
// and refuses a wrong identity, a newer schema and a file that is not ours.
func TestDeployRestoreDryRun(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bin := buildSelf(t)
	dir := t.TempDir()
	backups := filepath.Join(dir, "backups")
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	idFile := write("id.agekey", []byte("# created: test\n# public key: "+id.Recipient().String()+"\n"+id.String()+"\n"))
	otherFile := write("other.agekey", []byte(other.String()+"\n"))
	var enc bytes.Buffer
	if err := encryptStream(&enc, bytes.NewReader(gzipped(t, mariadbDump(11))), []age.Recipient{id.Recipient()}); err != nil {
		t.Fatal(err)
	}
	good := write("cactustrade-20261124T120000Z.sql.gz.age", enc.Bytes())
	tests := []struct {
		name string
		args []string
		ok   bool
		want string
	}{
		{"encrypted", []string{"--restore", good, "--identity", idFile}, true, "schema 11"},
		{"plain gzip", []string{"--restore", write("plain.sql.gz", gzipped(t, mariadbDump(3)))}, true, "schema 3"},
		{"wrong identity", []string{"--restore", good, "--identity", otherFile}, false, "does not decrypt"},
		{"no identity", []string{"--restore", good}, false, "needs its age identity"},
		{"newer schema", []string{"--restore", write("newer.sql.gz", gzipped(t, mariadbDump(99999)))}, false, "newer than this version"},
		{"not ours", []string{"--restore", write("other.sql", []byte("CREATE TABLE x (y INT);\n"))}, false, "not a cactusTrade dump"},
		{"damaged", []string{"--restore", write("bad.sql.gz", []byte("not gzip"))}, false, "damaged"},
		{"unknown kind", []string{"--restore", write("x.zip", []byte("x"))}, false, ".sql.gz.age"},
		{"identity without restore", []string{"--status", "--identity", idFile}, false, "go with --restore"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			args := append([]string{filepath.Join(root, "deploy.sh")}, tt.args...)
			cmd := exec.CommandContext(ctx, "bash", append(args, "--dry-run", "--dockerdesktop")...)
			// no Docker is reachable: a dry run must not need it
			cmd.Env = append(os.Environ(), "CACTUSTRADE_BIN="+bin, "BACKUP_DIR="+backups,
				"CACTUSTRADE_BACKUP_IDENTITY_FILE=", "DOCKER_HOST=unix:///nonexistent")
			out, err := cmd.CombinedOutput()
			if (err == nil) != tt.ok || !strings.Contains(string(out), tt.want) {
				t.Fatalf("err %v, want ok=%v and %q in:\n%s", err, tt.ok, tt.want, out)
			}
			if tt.ok && !strings.Contains(string(out), "Nothing was stopped or changed") {
				t.Fatalf("dry run output:\n%s", out)
			}
			if left, _ := filepath.Glob(filepath.Join(backups, ".restore-*")); len(left) != 0 {
				t.Fatalf("decrypted copy left behind: %v", left)
			}
		})
	}
}

// TestDeployRealTradingOptIn (PLAN 7.23, OD28): the rendered manifest carries
// CACTUSTRADE_REAL_TRADING as .env gives it (off), or as the owner set it (on, with a loud
// warning); any other value stops deploy.sh; and deploy.sh itself never turns it on.
func TestDeployRealTradingOptIn(t *testing.T) {
	noLocal := "CACTUSTRADE_ENV_LOCAL=" + filepath.Join(t.TempDir(), "none") // this machine's .env.local is not the default
	m, warn := renderManifest(t, noLocal)
	if !strings.Contains(m, `CACTUSTRADE_REAL_TRADING: "off"`) || strings.Contains(warn, "REAL-MONEY") {
		t.Fatalf("default: the manifest does not keep real trading off (warnings %q)", warn)
	}
	m, warn = renderManifest(t, noLocal, "CACTUSTRADE_REAL_TRADING=on")
	if !strings.Contains(m, `CACTUSTRADE_REAL_TRADING: "on"`) || !strings.Contains(warn, "ALLOWS REAL-MONEY ENTRIES") {
		t.Fatalf("owner's opt-in: not passed through or not warned (warnings %q)", warn)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(context.Background(), "bash", filepath.Join(root, "deploy.sh"), "--render", "--k3s")
	cmd.Env = append(os.Environ(), noLocal, "CACTUSTRADE_REAL_TRADING=yes")
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "must be on or off") {
		t.Fatalf("an invalid value was accepted: %v\n%s", err, out)
	}
	src, err := os.ReadFile(filepath.Join(root, "deploy.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// an assignment of "on" (a warning that names the setting is fine), or a write to .env.local
	if regexp.MustCompile(`(?m)^\s*(export\s+|local\s+)?CACTUSTRADE_REAL_TRADING=["']?on|set_env_local\s+["']?CACTUSTRADE_REAL_TRADING`).Match(src) {
		t.Fatal("deploy.sh sets CACTUSTRADE_REAL_TRADING")
	}
	env, err := os.ReadFile(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^CACTUSTRADE_REAL_TRADING=off$`).Match(env) {
		t.Fatal(".env must keep CACTUSTRADE_REAL_TRADING=off")
	}
}
