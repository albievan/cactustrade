package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
