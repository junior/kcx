package ns

import (
	"context"
	"crypto/md5"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/junior/kcx/v2/internal/kubeconfig"
	"github.com/junior/kcx/v2/internal/layout"
)

func writeCluster(t *testing.T, path, name, server string) {
	t.Helper()
	content := "apiVersion: v1\nkind: Config\nclusters:\n- name: " + name + "\n  cluster:\n    server: " + server +
		"\n    insecure-skip-tls-verify: true\nusers:\n- name: " + name + "\n  user:\n    token: not-a-secret-" + name +
		"\ncontexts:\n- name: " + name + "\n  context:\n    cluster: " + name + "\n    user: " + name + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func lay(t *testing.T) layout.Layout {
	t.Helper()
	home := t.TempDir()
	l, err := layout.FromEnv(func(k string) string {
		if k == "HOME" {
			return home
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	return l
}

func sum(t *testing.T, p string) [16]byte {
	t.Helper()
	d, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return md5.Sum(d)
}

func TestSetWritesOnlyTheContextsFile(t *testing.T) {
	l := lay(t)
	writeCluster(t, l.PermFile("alpha"), "alpha", "https://alpha.example.test")
	writeCluster(t, l.PermFile("beta"), "beta", "https://beta.example.test")
	if err := kubeconfig.SetCurrent(l.State, "alpha"); err != nil {
		t.Fatal(err)
	}
	files := l.Files()
	state, beta := sum(t, l.State), sum(t, l.PermFile("beta"))
	ctx, n, err := Current(files)
	if err != nil || ctx != "alpha" || n != "default" {
		t.Fatalf("Current: %q %q %v", ctx, n, err)
	}
	if err := Set(files, "alpha", "team-a"); err != nil {
		t.Fatal(err)
	}
	if sum(t, l.State) != state || sum(t, l.PermFile("beta")) != beta {
		t.Fatal("Set touched a file other than alpha's")
	}
	_, n, _ = Current(files)
	if n != "team-a" {
		t.Fatalf("namespace after Set: %q", n)
	}
	if n, _ := Of(files, "beta"); n != "default" {
		t.Fatalf("Of(beta): %q", n)
	}
	if err := Set(files, "beta", "team-b"); err != nil {
		t.Fatal(err)
	}
	if n, _ := Of(files, "beta"); n != "team-b" {
		t.Fatalf("Of(beta) after Set: %q", n)
	}
	if sum(t, l.State) != state {
		t.Fatal("Set on beta touched the state file")
	}
	cfg, _ := kubeconfig.LoadFile(l.PermFile("beta"))
	if cfg.CurrentContext != "" {
		t.Fatalf("Set put a current-context into a managed file: %q", cfg.CurrentContext)
	}
	if err := Set(files, "nope", "x"); err == nil || !strings.Contains(err.Error(), "no context named nope") {
		t.Fatalf("Set unknown: %v", err)
	}
	// A no-op Set changes nothing.
	b := sum(t, l.PermFile("beta"))
	if err := Set(files, "beta", "team-b"); err != nil || sum(t, l.PermFile("beta")) != b {
		t.Fatal("a no-op Set rewrote the file")
	}
}

func TestPrevious(t *testing.T) {
	l := lay(t)
	if ReadPrevious(l.NSPrev, "alpha") != "" {
		t.Fatal("missing previous must be empty")
	}
	if err := WritePrevious(l.NSPrev, "alpha", "team-a"); err != nil {
		t.Fatal(err)
	}
	if ReadPrevious(l.NSPrev, "alpha") != "team-a" {
		t.Fatal("previous not read back")
	}
	info, _ := os.Stat(filepath.Join(l.NSPrev, "alpha"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	dinfo, _ := os.Stat(l.NSPrev)
	if dinfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o", dinfo.Mode().Perm())
	}
}

func TestListExistsProbe(t *testing.T) {
	var sawAuth string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		if sawAuth != "Bearer not-a-secret-alpha" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/namespaces":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"kube-system"}},{"metadata":{"name":"default"}}]}`))
		case "/api/v1/namespaces/default":
			w.WriteHeader(http.StatusOK)
		case "/api/v1/namespaces/nope":
			w.WriteHeader(http.StatusNotFound)
		case "/version":
			_, _ = w.Write([]byte(`{"gitVersion":"v1.37.1"}`))
		default:
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()
	l := lay(t)
	writeCluster(t, l.PermFile("alpha"), "alpha", srv.URL)
	writeCluster(t, l.PermFile("bad"), "bad", srv.URL) // its token is refused
	// A credential plugin that fails: the credential layer stops the request
	// before it leaves, which is a reason to fetch again, like a 401.
	broken := "apiVersion: v1\nkind: Config\nclusters:\n- name: broken\n  cluster:\n    server: " + srv.URL + "\n    insecure-skip-tls-verify: true\nusers:\n- name: broken\n  user:\n    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: /nonexistent/kcx-test-credential-plugin\n      interactiveMode: Never\ncontexts:\n- name: broken\n  context:\n    cluster: broken\n    user: broken\n"
	if err := os.WriteFile(l.PermFile("broken"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	files := l.Files()
	if r, err := Probe(context.Background(), files, "broken", 5*time.Second); r != ProbeCredentials || err == nil || !strings.Contains(err.Error(), "getting credentials") || strings.Contains(err.Error(), "/version") {
		t.Fatalf("Probe with a failing credential plugin: %v %v", r, err)
	}
	ctx := context.Background()
	got, err := List(ctx, files, "alpha", 5*time.Second)
	if err != nil || strings.Join(got, ",") != "default,kube-system" {
		t.Fatalf("List: %v %v", got, err)
	}
	if ok, err := Exists(ctx, files, "alpha", "default", 5*time.Second); err != nil || !ok {
		t.Fatalf("Exists default: %v %v", ok, err)
	}
	if ok, err := Exists(ctx, files, "alpha", "nope", 5*time.Second); err != nil || ok {
		t.Fatalf("Exists nope: %v %v", ok, err)
	}
	if ok, err := Exists(ctx, files, "alpha", "odd", 5*time.Second); err == nil || ok {
		t.Fatalf("Exists with an unexpected status must fail: %v %v", ok, err)
	}
	if r, err := Probe(ctx, files, "alpha", 5*time.Second); err != nil || r != ProbeOK {
		t.Fatalf("Probe ok: %v %v", r, err)
	}
	if r, err := Probe(ctx, files, "bad", 5*time.Second); err != nil || r != ProbeUnauthorized {
		t.Fatalf("Probe 401: %v %v", r, err)
	}
	if _, err := List(ctx, files, "bad", 5*time.Second); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("List with a refused token: %v", err)
	}
	// Unreachable: a server that is gone.
	gone := httptest.NewTLSServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close()
	writeCluster(t, l.PermFile("gone"), "gone", url)
	if r, err := Probe(ctx, l.Files(), "gone", 2*time.Second); r != ProbeUnreachable || err == nil {
		t.Fatalf("Probe unreachable: %v %v", r, err)
	}
	if _, err := Probe(ctx, files, "missing", time.Second); err == nil {
		t.Fatal("Probe of an unknown context must fail")
	}
}
