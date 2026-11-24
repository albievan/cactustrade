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

// docOf returns the document of a rendered manifest with this kind and name.
func docOf(t *testing.T, manifest, kind, name string) string {
	t.Helper()
	for doc := range strings.SplitSeq(manifest, "\n---\n") {
		if strings.Contains(doc, "kind: "+kind+"\n") && strings.Contains(doc, "\n  name: "+name+"\n") {
			return doc
		}
	}
	t.Fatalf("no %s %s in the manifest", kind, name)
	return ""
}

// TestDeployRenderNetwork is PLAN 7.1.16 (F16): NetworkPolicies (MariaDB 3306 only from the
// app; the app only from the ingress controller with HTTPS), trusted proxies from .env (empty
// by default, with a warning), and a hardened MariaDB.
func TestDeployRenderNetwork(t *testing.T) {
	m, warn := renderManifest(t, "K8S_HTTPS=true", "K8S_TRUSTED_PROXIES=", "K8S_INGRESS_NAMESPACE=", "K8S_INGRESS_POD_SELECTOR=")
	db := docOf(t, m, "NetworkPolicy", "mariadb")
	for _, want := range []string{
		"podSelector: { matchLabels: { app.kubernetes.io/name: mariadb } }", "policyTypes: [Ingress]",
		"- from: [{ podSelector: { matchLabels: { app.kubernetes.io/name: cactustrade } } }]",
		"ports: [{ protocol: TCP, port: 3306 }]",
	} {
		if !strings.Contains(db, want) {
			t.Errorf("MariaDB policy without %q:\n%s", want, db)
		}
	}
	if strings.Count(db, "from:") != 1 || strings.Contains(db, "namespaceSelector") || strings.Contains(db, "ipBlock") {
		t.Errorf("MariaDB policy lets in more than the app:\n%s", db)
	}
	app := docOf(t, m, "NetworkPolicy", "cactustrade")
	if !strings.Contains(app, "from: [{ namespaceSelector: { matchLabels: { kubernetes.io/metadata.name: kube-system } }, podSelector: { matchLabels: { app.kubernetes.io/name: traefik } } }]") ||
		!strings.Contains(app, "port: 8080") || strings.Contains(app, "ipBlock") {
		t.Errorf("app policy (k3s defaults):\n%s", app)
	}
	if !strings.Contains(docOf(t, m, "ConfigMap", "cactustrade-config"), `CACTUSTRADE_TRUSTED_PROXIES: ""`) || strings.Contains(m, "10.42.0.0/16") {
		t.Error("trusted proxies must default to none")
	}
	if !strings.Contains(warn, "K8S_TRUSTED_PROXIES is empty") {
		t.Errorf("no warning about the empty trusted proxies: %q", warn)
	}
	maria := docOf(t, m, "Deployment", "mariadb")
	for _, want := range []string{
		"runAsNonRoot: true", "runAsUser: 999", "fsGroup: 999", "seccompProfile: { type: RuntimeDefault }",
		"allowPrivilegeEscalation: false", "readOnlyRootFilesystem: true", "capabilities: { drop: [ALL] }",
		"mountPath: /run/mysqld", "mountPath: /tmp",
	} {
		if !strings.Contains(maria, want) {
			t.Errorf("MariaDB Deployment without %q", want)
		}
	}

	// another ingress controller and its addresses
	m, warn = renderManifest(t, "K8S_HTTPS=true", "K8S_TRUSTED_PROXIES=10.42.0.0/24, 10.42.1.7",
		"K8S_INGRESS_NAMESPACE=ingress-nginx", "K8S_INGRESS_POD_SELECTOR=app.kubernetes.io/name=ingress-nginx")
	app = docOf(t, m, "NetworkPolicy", "cactustrade")
	if !strings.Contains(app, "kubernetes.io/metadata.name: ingress-nginx") || !strings.Contains(app, "app.kubernetes.io/name: ingress-nginx") {
		t.Errorf("app policy (ingress-nginx):\n%s", app)
	}
	if !strings.Contains(m, `CACTUSTRADE_TRUSTED_PROXIES: "10.42.0.0/24, 10.42.1.7"`) || strings.Contains(warn, "K8S_TRUSTED_PROXIES is empty") {
		t.Errorf("trusted proxies from .env: %q", warn)
	}

	// plain HTTP on a NodePort: the app takes any client, MariaDB still only the app
	m, _ = renderManifest(t, "K8S_HTTPS=false", "K8S_NODE_PORT=30443")
	if app := docOf(t, m, "NetworkPolicy", "cactustrade"); strings.Contains(app, "from:") || !strings.Contains(app, "port: 8080") {
		t.Errorf("plain-HTTP app policy:\n%s", app)
	}
	if db := docOf(t, m, "NetworkPolicy", "mariadb"); !strings.Contains(db, "app.kubernetes.io/name: cactustrade") {
		t.Errorf("plain-HTTP MariaDB policy:\n%s", db)
	}
}

// TestDeployRenderRefusesInjection: the values that go into the manifest are checked.
func TestDeployRenderRefusesInjection(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, env := range [][]string{
		{"K8S_TRUSTED_PROXIES=10.0.0.0/8\"\n  EVIL: x"},
		{"K8S_INGRESS_NAMESPACE=Kube System"},
		{"K8S_INGRESS_POD_SELECTOR=app: traefik } }, evil"},
		{"K8S_INGRESS_POD_SELECTOR=app"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "deploy.sh"), "--render", "--k3s")
		cmd.Env = append(append(os.Environ(), "K8S_HTTPS=true"), env...)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		cancel()
		if err == nil || strings.Contains(out.String(), "EVIL") || strings.Contains(out.String(), "evil") {
			t.Errorf("%q rendered: %v\n%s", env, err, out.String())
		}
	}
}

// TestComposeMariaDBHardened is PLAN 7.1.16: MariaDB runs as its own user without
// capabilities, privilege gain or a writable root; volume-init hands it its folder; Mailpit
// drops every capability too.
func TestComposeMariaDBHardened(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	services := map[string]string{}
	var cur string
	for line := range strings.Lines(string(b)) {
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") && strings.HasSuffix(strings.TrimSpace(line), ":") {
			cur = strings.TrimSuffix(strings.TrimSpace(line), ":")
		} else if !strings.HasPrefix(line, " ") {
			cur = ""
		}
		if cur != "" {
			services[cur] += line
		}
	}
	for _, want := range []string{`user: "999:999"`, "read_only: true", "security_opt: [no-new-privileges:true]", "cap_drop: [ALL]", "/run/mysqld:", "volume-init: { condition: service_completed_successfully }"} {
		if !strings.Contains(services["mariadb"], want) {
			t.Errorf("compose mariadb without %q:\n%s", want, services["mariadb"])
		}
	}
	if !strings.Contains(services["volume-init"], "chown -R 999:999 /mariadb") || !strings.Contains(services["volume-init"], "${MARIADB_DATA_DIR}:/mariadb") {
		t.Errorf("volume-init does not hand MariaDB its folder:\n%s", services["volume-init"])
	}
	for _, s := range []string{"app", "mailpit"} {
		if !strings.Contains(services[s], "cap_drop: [ALL]") || !strings.Contains(services[s], "no-new-privileges:true") {
			t.Errorf("compose %s is not hardened", s)
		}
	}
}
