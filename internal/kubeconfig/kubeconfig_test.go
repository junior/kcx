package kubeconfig

import (
	"crypto/md5"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/tools/clientcmd"

	"github.com/junior/kcx/v2/internal/layout"
)

func noenv(string) string { return "" }

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sum(t *testing.T, path string) [16]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return md5.Sum(data)
}

// oneCluster is a kubeconfig the way a cloud CLI writes it: one context, a token.
func oneCluster(ctx, cluster, user, server string) string {
	return "apiVersion: v1\nkind: Config\nclusters:\n- name: " + cluster + "\n  cluster:\n    server: " + server +
		"\nusers:\n- name: " + user + "\n  user:\n    token: not-a-secret-" + user +
		"\ncontexts:\n- name: " + ctx + "\n  context:\n    cluster: " + cluster + "\n    user: " + user + "\ncurrent-context: " + ctx + "\n"
}

func testLayout(t *testing.T) layout.Layout {
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

func TestPrepareRenamesAndStripsCurrent(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.yaml")
	write(t, src, oneCluster("admin@lab", "lab-cluster", "admin", "https://lab.example.test:6443"))
	cfg, err := Prepare(src, "lab", ImportOptions{Rename: true})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentContext != "" {
		t.Fatalf("current-context kept: %q", cfg.CurrentContext)
	}
	if _, ok := cfg.Clusters["lab"]; !ok || len(cfg.Clusters) != 1 {
		t.Fatalf("clusters: %v", cfg.Clusters)
	}
	if _, ok := cfg.AuthInfos["lab"]; !ok || len(cfg.AuthInfos) != 1 {
		t.Fatalf("users: %v", cfg.AuthInfos)
	}
	c, ok := cfg.Contexts["lab"]
	if !ok || c.Cluster != "lab" || c.AuthInfo != "lab" {
		t.Fatalf("context: %+v", cfg.Contexts)
	}
	if cfg.AuthInfos["lab"].Token != "not-a-secret-admin" {
		t.Fatal("the token did not come along")
	}
}

func TestPrepareTemplatesAndKeepNames(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.yaml")
	write(t, src, oneCluster("arn:aws:eks:us-west-2:1:cluster/lab", "arn:aws:eks:us-west-2:1:cluster/lab", "arn:aws:eks:us-west-2:1:cluster/lab", "https://eks.example.test"))
	cfg, err := Prepare(src, "eks-lab", ImportOptions{Rename: true, ContextName: "{user}@{name}", UserName: "{user}"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Contexts["arn:aws:eks:us-west-2:1:cluster/lab@eks-lab"]; !ok {
		t.Fatalf("templated context name missing: %v", cfg.Contexts)
	}
	if _, ok := cfg.AuthInfos["arn:aws:eks:us-west-2:1:cluster/lab"]; !ok {
		t.Fatalf("templated user name missing: %v", cfg.AuthInfos)
	}
	if _, ok := cfg.Clusters["eks-lab"]; !ok {
		t.Fatal("the cluster must always take the short name")
	}
	cfg, err = Prepare(src, "eks-lab", ImportOptions{Rename: true, KeepNames: true})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := cfg.Contexts["arn:aws:eks:us-west-2:1:cluster/lab"]
	if !ok || c.Cluster != "eks-lab" || c.AuthInfo != "arn:aws:eks:us-west-2:1:cluster/lab" {
		t.Fatalf("--keep-names: %+v", cfg.Contexts)
	}
	// No rename at all (temp add): everything keeps its name.
	cfg, err = Prepare(src, "ignored", ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Clusters["arn:aws:eks:us-west-2:1:cluster/lab"]; !ok {
		t.Fatalf("no-rename kept nothing: %v", cfg.Clusters)
	}
}

func TestPrepareSeveralContextsAndServer(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "two.yaml")
	write(t, src, `apiVersion: v1
kind: Config
clusters:
- name: a
  cluster: {server: https://a.example.test}
- name: b
  cluster: {server: https://b.example.test}
users:
- name: u
  user: {token: not-a-secret}
contexts:
- name: ctx-a
  context: {cluster: a, user: u}
- name: ctx-b
  context: {cluster: b, user: u}
current-context: ctx-a
`)
	if _, err := Prepare(src, "x", ImportOptions{Rename: true}); err == nil || !strings.Contains(err.Error(), "holds 2 contexts") {
		t.Fatalf("expected the two-contexts refusal, got %v", err)
	}
	if _, err := Prepare(src, "x", ImportOptions{Rename: true, Context: "nope"}); err == nil || !strings.Contains(err.Error(), "no context nope") {
		t.Fatalf("expected the unknown-context refusal, got %v", err)
	}
	cfg, err := Prepare(src, "bee", ImportOptions{Rename: true, Context: "ctx-b", Server: "https://bee.lan:6443"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Clusters["bee"].Server != "https://bee.lan:6443" || len(cfg.Contexts) != 1 {
		t.Fatalf("server override or minify failed: %+v", cfg.Clusters["bee"])
	}
	empty := filepath.Join(dir, "empty.yaml")
	write(t, empty, "apiVersion: v1\nkind: Config\n")
	if _, err := Prepare(empty, "x", ImportOptions{Rename: true}); err == nil || !strings.Contains(err.Error(), "holds no context") {
		t.Fatalf("expected the no-context refusal, got %v", err)
	}
}

func TestPrepareEmbedsCertificateFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "ca.crt"), "CA PEM")
	write(t, filepath.Join(dir, "client.crt"), "CLIENT PEM")
	write(t, filepath.Join(dir, "client.key"), "KEY PEM")
	src := filepath.Join(dir, "certs.yaml")
	write(t, src, `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://c.example.test
    certificate-authority: ca.crt
users:
- name: u
  user:
    client-certificate: client.crt
    client-key: client.key
contexts:
- name: c
  context: {cluster: c, user: u}
`)
	cfg, err := Prepare(src, "c", ImportOptions{Rename: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.Clusters["c"].CertificateAuthorityData) != "CA PEM" || cfg.Clusters["c"].CertificateAuthority != "" {
		t.Fatalf("CA not embedded: %+v", cfg.Clusters["c"])
	}
	u := cfg.AuthInfos["c"]
	if string(u.ClientCertificateData) != "CLIENT PEM" || string(u.ClientKeyData) != "KEY PEM" || u.ClientCertificate != "" {
		t.Fatalf("client cert not embedded: %+v", u)
	}
}

func TestPrepareKeepsExecAndAuthProvider(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "auth.yaml")
	write(t, src, `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: https://c.example.test}
users:
- name: u
  user:
    auth-provider:
      name: oidc
      config:
        client-id: kubernetes
        id-token: not-a-real-token
        idp-issuer-url: https://idp.example.test
        refresh-token: not-a-real-refresh
contexts:
- name: c
  context: {cluster: c, user: u, namespace: team}
`)
	cfg, err := Prepare(src, "oidc1", ImportOptions{Rename: true})
	if err != nil {
		t.Fatal(err)
	}
	ap := cfg.AuthInfos["oidc1"].AuthProvider
	if ap == nil || ap.Name != "oidc" || ap.Config["refresh-token"] != "not-a-real-refresh" {
		t.Fatalf("auth-provider lost: %+v", cfg.AuthInfos["oidc1"])
	}
	if cfg.Contexts["oidc1"].Namespace != "team" {
		t.Fatal("the context's namespace did not come along")
	}
}

func TestImportCollisionsReplaceAndTrash(t *testing.T) {
	l := testLayout(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.yaml")
	write(t, src, oneCluster("c", "c", "c", "https://one.example.test"))
	cfg, err := Prepare(src, "one", ImportOptions{Rename: true})
	if err != nil {
		t.Fatal(err)
	}
	dest := l.PermFile("one")
	ctx, err := Import(cfg, dest, l.Files(), l.State, noenv, ImportOptions{})
	if err != nil || ctx != "one" {
		t.Fatalf("import: %v %q", err, ctx)
	}
	info, _ := os.Stat(dest)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	// The same entries under another file name collide.
	if _, err := Import(cfg, l.PermFile("two"), l.Files(), l.State, noenv, ImportOptions{}); err == nil || !strings.Contains(err.Error(), "already exists in") {
		t.Fatalf("expected a collision, got %v", err)
	}
	// The destination exists: refused without Replace, replaced with it, old file to the Trash with TrashOld.
	if _, err := Import(cfg, dest, l.Files(), l.State, noenv, ImportOptions{}); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("expected the exists refusal, got %v", err)
	}
	trash := filepath.Join(t.TempDir(), "trash")
	env := func(k string) string {
		if k == "KCX_TRASH_DIR" {
			return trash
		}
		return ""
	}
	cfg.Clusters["one"].Server = "https://one-renewed.example.test"
	if _, err := Import(cfg, dest, l.Files(), l.State, env, ImportOptions{Replace: true, TrashOld: true}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(trash)
	if len(entries) != 1 {
		t.Fatalf("the old file did not reach the Trash: %v", entries)
	}
	back, _ := LoadFile(dest)
	if back.Clusters["one"].Server != "https://one-renewed.example.test" {
		t.Fatal("the replacement did not land")
	}
	if _, err := Import(cfg, dest, l.Files(), l.State, env, ImportOptions{Replace: true}); err != nil {
		t.Fatal(err)
	}
	entries, _ = os.ReadDir(trash)
	if len(entries) != 1 {
		t.Fatal("Replace without TrashOld must not touch the Trash")
	}
	if left := layout.DirFiles(l.Perm); len(left) != 1 {
		t.Fatalf("stray files: %v", left)
	}
}

func TestStateFileOnlyOnSwitch(t *testing.T) {
	l := testLayout(t)
	write(t, l.PermFile("alpha"), oneCluster("alpha", "alpha", "alpha", "https://alpha.example.test"))
	write(t, l.PermFile("beta"), oneCluster("beta", "beta", "beta", "https://beta.example.test"))
	// Managed files carry no current-context; strip what the fixture set.
	for _, n := range []string{"alpha", "beta"} {
		cfg, _ := LoadFile(l.PermFile(n))
		cfg.CurrentContext = ""
		if err := WriteFile(l.PermFile(n), cfg); err != nil {
			t.Fatal(err)
		}
	}
	a, b := sum(t, l.PermFile("alpha")), sum(t, l.PermFile("beta"))
	s := Store{Files: l.Files()}
	ctxs, err := s.Contexts()
	if err != nil || strings.Join(ctxs, ",") != "alpha,beta" {
		t.Fatalf("contexts: %v %v", ctxs, err)
	}
	if cur, _ := s.Current(); cur != "" {
		t.Fatalf("current before any switch: %q", cur)
	}
	if err := SetCurrent(l.State, "beta"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.Current(); cur != "beta" {
		t.Fatalf("current: %q", cur)
	}
	if sum(t, l.PermFile("alpha")) != a || sum(t, l.PermFile("beta")) != b {
		t.Fatal("a switch rewrote a credential file")
	}
	if err := WritePrevious(l.Prev, "alpha"); err != nil {
		t.Fatal(err)
	}
	if ReadPrevious(l.Prev) != "alpha" {
		t.Fatal("previous not read back")
	}
	info, _ := os.Stat(l.Prev)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("previous file mode %o", info.Mode().Perm())
	}
	if err := ClearCurrent(l.State); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.Current(); cur != "" {
		t.Fatalf("current after clear: %q", cur)
	}
	if ReadPrevious(filepath.Join(l.Dir, "nope")) != "" {
		t.Fatal("missing previous file must read as empty")
	}
}

func TestRemoveFromFileAndServerOf(t *testing.T) {
	l := testLayout(t)
	write(t, l.State, `apiVersion: v1
kind: Config
clusters:
- name: shared
  cluster: {server: https://shared.example.test}
- name: alone
  cluster: {server: https://alone.example.test}
users:
- name: u
  user: {token: not-a-secret}
contexts:
- name: one
  context: {cluster: shared, user: u}
- name: two
  context: {cluster: shared, user: u}
- name: three
  context: {cluster: alone, user: u}
current-context: one
`)
	cfg, _ := LoadFile(l.State)
	if ServerOf(cfg, "three") != "https://alone.example.test" || ServerOf(cfg, "nope") != "-" {
		t.Fatal("ServerOf")
	}
	if err := RemoveFromFile(l.State, "one"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = LoadFile(l.State)
	if _, ok := cfg.Contexts["one"]; ok {
		t.Fatal("context one still there")
	}
	if _, ok := cfg.Clusters["shared"]; !ok {
		t.Fatal("cluster shared removed although two still uses it")
	}
	if _, ok := cfg.AuthInfos["u"]; !ok {
		t.Fatal("user u removed although still used")
	}
	if err := RemoveFromFile(l.State, "three"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = LoadFile(l.State)
	if _, ok := cfg.Clusters["alone"]; ok {
		t.Fatal("cluster alone should have gone with its last context")
	}
	if cfg.CurrentContext != "one" {
		t.Fatal("RemoveFromFile must not touch the current context; the caller decides")
	}
	// The written file still loads with kubectl's own loader.
	if _, err := clientcmd.LoadFromFile(l.State); err != nil {
		t.Fatal(err)
	}
}
