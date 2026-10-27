package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/junior/kcx/internal/kubeconfig"
)

// fakeShell stands in for $SHELL: it records the variables a kcx shell, a
// read-only shell or a run sets, saves a copy of the shell's private file, and
// exits.
const fakeShell = `#!/bin/sh
printf '%s\n%s\n%s\n%s\n%s\n' "${KCX_SHELL-unset}" "${KUBECONFIG-unset}" "${KCX_SHELL_AUTO-unset}" "${KCX_AUTO-unset}" "${KCX_RO-unset}" > "$KCX_TEST_OUT/env"
if [ -n "${KCX_SHELL-}" ]; then cp "$KCX_SHELL" "$KCX_TEST_OUT/private.yaml"; fi
`

// withFakeShell writes the stand-in shell and points the test's output at a
// fresh directory; it returns the env with SHELL set, and that directory.
func withFakeShell(t *testing.T, e env) (env, string) {
	t.Helper()
	dir := t.TempDir()
	sh := filepath.Join(dir, "fake-shell")
	if err := os.WriteFile(sh, []byte(fakeShell), 0o755); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KCX_TEST_OUT", out)
	withShell := env{}
	for k, v := range e {
		withShell[k] = v
	}
	withShell["SHELL"] = sh
	return withShell, out
}

// recorded reads what the stand-in shell saw: KCX_SHELL, KUBECONFIG,
// KCX_SHELL_AUTO, KCX_AUTO and KCX_RO, "unset" for an absent one.
func recorded(t *testing.T, out string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(out, "env"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func currentOf(t *testing.T, path string) string {
	t.Helper()
	cfg, err := kubeconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.CurrentContext
}

func namespaceIn(t *testing.T, path, ctx string) string {
	t.Helper()
	cfg, err := kubeconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, ok := cfg.Contexts[ctx]
	if !ok {
		t.Fatalf("%s has no context %s", path, ctx)
	}
	return c.Namespace
}

func TestKcxShellStarts(t *testing.T) {
	e, l := fixture(t)
	if code, _, errs := runKcx(t, e, "alpha"); code != 0 {
		t.Fatalf("kcx alpha: %q", errs)
	}
	se, out := withFakeShell(t, e)
	code, stdout, errs := runKcx(t, se, "shell", "beta", "-n", "team")
	if code != 0 || !strings.Contains(stdout, "kcx shell for beta:") || !strings.Contains(stdout, "kcx shell closed") {
		t.Fatalf("kcx shell beta: %d %q %q", code, stdout, errs)
	}
	got := recorded(t, out)
	private, kube, auto, kcxAuto := got[0], got[1], got[2], got[3]
	if filepath.Base(private) != "config.yaml" || !strings.HasPrefix(filepath.Base(filepath.Dir(private)), "kcx-shell.") {
		t.Fatalf("KCX_SHELL: %q", private)
	}
	if kube != private+string(os.PathListSeparator)+l.Kubeconfig() || auto != kube {
		t.Fatalf("KUBECONFIG %q, KCX_SHELL_AUTO %q", kube, auto)
	}
	if kcxAuto != "unset" {
		t.Fatalf("KCX_AUTO must not be set in a kcx shell: %q", kcxAuto)
	}
	// The private file: every context, the starting one current with its own
	// namespace, and no credentials.
	saved := filepath.Join(out, "private.yaml")
	data, _ := os.ReadFile(saved)
	if strings.Contains(string(data), "not-a-secret") || strings.Contains(string(data), "server:") {
		t.Fatalf("the private file holds more than contexts:\n%s", data)
	}
	if currentOf(t, saved) != "beta" || namespaceIn(t, saved, "beta") != "team" || namespaceIn(t, saved, "alpha") != "" {
		t.Fatalf("private file:\n%s", data)
	}
	// Gone after the shell, and the shared current context untouched.
	if _, err := os.Stat(filepath.Dir(private)); !os.IsNotExist(err) {
		t.Fatalf("the shell's directory outlived it: %v", err)
	}
	if currentOf(t, l.State) != "alpha" {
		t.Fatal("kcx shell changed the shared current context")
	}
	if code, _, errs := runKcx(t, se, "shell", "nope"); code != 1 || !strings.Contains(errs, "no context named nope") {
		t.Fatalf("kcx shell nope: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, se, "shell", "alpha", "--frobnicate"); code != 2 {
		t.Fatalf("unknown option: %d %q", code, errs)
	}
}

// Inside a kcx shell: switches, the previous context, namespaces and listings
// are the shell's own; the shared state is never touched.
func TestInsideKcxShell(t *testing.T) {
	e, l := fixture(t)
	if code, _, errs := runKcx(t, e, "alpha"); code != 0 {
		t.Fatalf("kcx alpha: %q", errs)
	}
	se, out := withFakeShell(t, e)
	if code, _, errs := runKcx(t, se, "shell", "beta"); code != 0 {
		t.Fatalf("kcx shell beta: %q", errs)
	}
	// Carry on with a copy of the private file, as if inside the shell.
	dir := t.TempDir()
	private := filepath.Join(dir, "config.yaml")
	data, _ := os.ReadFile(filepath.Join(out, "private.yaml"))
	if err := os.WriteFile(private, data, 0o600); err != nil {
		t.Fatal(err)
	}
	list := private + string(os.PathListSeparator) + l.Kubeconfig()
	in := env{"HOME": e["HOME"], EnvShell: private, "KUBECONFIG": list, EnvShellAuto: list}

	code, stdout, errs := runKcx(t, in, "alpha")
	if code != 0 || stdout != "kcx: switched to alpha in this kcx shell\n" {
		t.Fatalf("switch inside: %d %q %q", code, stdout, errs)
	}
	if currentOf(t, private) != "alpha" || currentOf(t, l.State) != "alpha" {
		t.Fatal("the switch must land in the private file only")
	}
	if prev, _ := os.ReadFile(filepath.Join(dir, "kubectx")); string(prev) != "beta" {
		t.Fatalf("the shell's previous context: %q", prev)
	}
	if _, err := os.Stat(l.Prev); !os.IsNotExist(err) {
		t.Fatal("the shared previous-context file was written")
	}
	if code, stdout, _ := runKcx(t, in, "-"); code != 0 || !strings.Contains(stdout, "switched to beta in this kcx shell") {
		t.Fatalf("kcx - inside: %d %q", code, stdout)
	}
	if code, stdout, _ := runKcx(t, in, "-c"); code != 0 || stdout != "beta\n" {
		t.Fatalf("kcx -c inside: %d %q", code, stdout)
	}
	// A namespace change stays in the shell.
	if code, _, errs := runKcx(t, in, "ns", "team", "--force"); code != 0 {
		t.Fatalf("ns inside: %q", errs)
	}
	if namespaceIn(t, private, "beta") != "team" || namespaceIn(t, l.PermFile("beta"), "beta") != "" {
		t.Fatal("the namespace change must land in the private file only")
	}
	if _, err := os.Stat(filepath.Join(l.NSPrev, "beta")); !os.IsNotExist(err) {
		t.Fatal("the shared previous-namespace file was written")
	}
	// Listings show each context once, mark the shell's current one, and see
	// nothing odd in the private file's copies.
	code, stdout, errs = runKcx(t, in, "ls")
	if code != 0 || strings.Count(stdout, "\n") != 2 || !strings.Contains(stdout, "*  beta ") || errs != "" {
		t.Fatalf("ls inside: %d %q %q", code, stdout, errs)
	}
	code, stdout, _ = runKcx(t, in, "env")
	if code != 0 || stdout != "export KUBECONFIG="+shellQuote(list)+" "+EnvShellAuto+"="+shellQuote(list)+"\n" {
		t.Fatalf("env inside: %d %q", code, stdout)
	}
	if code, stdout, _ := runKcx(t, in, "doctor"); !strings.Contains(stdout, "ok    KUBECONFIG matches the layout") {
		t.Fatalf("doctor inside: %d %q", code, stdout)
	}
	if code, _, errs := runKcx(t, in, "shell", "alpha"); code != 1 || !strings.Contains(errs, "already a kcx shell") {
		t.Fatalf("nested kcx shell: %d %q", code, errs)
	}
	// A run from inside is a run of its own: it starts from the shell's
	// namespace and carries none of the shell's variables.
	code, stdout, errs = runKcx(t, in, "run", "beta", "--", "sh", "-c", `printf '%s|' "${KCX_SHELL-unset}"; kubectl_ns=$(sed -n 's/^    namespace: //p' "$KCX_RUN_CONFIG"); printf '%s' "$kubectl_ns"`)
	if code != 0 || stdout != "unset|team" {
		t.Fatalf("run inside: %d %q %q", code, stdout, errs)
	}
	// A cluster added meanwhile gets its copy when the shell switches to it.
	writeKubeconfig(t, l.PermFile("gamma"), "gamma", "gamma", "gamma", "https://gamma.example.test:6443", "")
	if code, stdout, errs := runKcx(t, in, "gamma"); code != 0 || !strings.Contains(stdout, "switched to gamma in this kcx shell") {
		t.Fatalf("switch to a new cluster inside: %d %q %q", code, stdout, errs)
	}
	if currentOf(t, private) != "gamma" || namespaceIn(t, private, "gamma") != "" {
		t.Fatal("the new cluster's copy")
	}
	if currentOf(t, l.State) != "alpha" {
		t.Fatal("the shared current context moved")
	}
}

// A cluster that is read-only by default opens a read-only shell, unless --rw.
func TestKcxShellReadonlyDefault(t *testing.T) {
	f := withProfiles(t, "")
	se, out := withFakeShell(t, f.env)
	code, _, errs := runKcx(t, se, "shell", "prod1")
	if code != 0 || !strings.Contains(errs, "prod1 is read-only by default (group production): a read-only shell") {
		t.Fatalf("kcx shell prod1: %d %q", code, errs)
	}
	if got := recorded(t, out); got[4] != "prod1 (READ-ONLY)" || got[0] != "unset" {
		t.Fatalf("expected a read-only shell: %q", got)
	}
	code, _, errs = runKcx(t, se, "shell", "prod1", "--rw")
	if code != 0 {
		t.Fatalf("kcx shell prod1 --rw: %d %q", code, errs)
	}
	if got := recorded(t, out); got[4] != "unset" || got[0] == "unset" {
		t.Fatalf("expected a kcx shell: %q", got)
	}
}

// A read-only shell starts in the context's own namespace, as kubectl would.
func TestReadonlyKeepsTheNamespace(t *testing.T) {
	e, l := fixture(t)
	if code, _, errs := runKcx(t, e, "alpha"); code != 0 {
		t.Fatalf("kcx alpha: %q", errs)
	}
	if code, _, errs := runKcx(t, e, "ns", "team", "--force"); code != 0 {
		t.Fatalf("kcx ns: %q", errs)
	}
	if namespaceIn(t, l.PermFile("alpha"), "alpha") != "team" {
		t.Fatal("setup: alpha's namespace")
	}
	code, stdout, errs := runKcx(t, e, "-r", "alpha", "--", "sh", "-c", `sed -n 's/^    namespace: //p' "$KUBECONFIG"`)
	if code != 0 || strings.TrimSpace(stdout) != "team" {
		t.Fatalf("-r namespace: %d %q %q", code, stdout, errs)
	}
	// -n still wins.
	code, stdout, _ = runKcx(t, e, "-r", "alpha", "-n", "other", "--", "sh", "-c", `sed -n 's/^    namespace: //p' "$KUBECONFIG"`)
	if code != 0 || strings.TrimSpace(stdout) != "other" {
		t.Fatalf("-r -n: %d %q", code, stdout)
	}
}

// A run tells where its private file is, so kcx env and ls inside know it.
func TestRunConfig(t *testing.T) {
	e, l := fixture(t)
	code, stdout, errs := runKcx(t, e, "run", "alpha", "--", "sh", "-c", `printf '%s' "$KCX_RUN_CONFIG"`)
	if code != 0 || !strings.HasSuffix(stdout, "current.yaml") {
		t.Fatalf("KCX_RUN_CONFIG: %d %q %q", code, stdout, errs)
	}
	shadow := filepath.Join(t.TempDir(), "current.yaml")
	writeKubeconfigContextOnly(t, shadow, "alpha", "alpha", "alpha")
	in := env{"HOME": e["HOME"], EnvRun: "alpha", EnvRunConfig: shadow, "KUBECONFIG": shadow + string(os.PathListSeparator) + l.Kubeconfig()}
	want := shadow + string(os.PathListSeparator) + l.Kubeconfig()
	if code, stdout, _ := runKcx(t, in, "env"); code != 0 || stdout != "export KUBECONFIG="+shellQuote(want)+"\n" {
		t.Fatalf("env in a run: %d %q", code, stdout)
	}
	if code, stdout, errs := runKcx(t, in, "ls"); code != 0 || strings.Count(stdout, "\n") != 2 || !strings.Contains(stdout, "*  alpha ") || errs != "" {
		t.Fatalf("ls in a run: %d %q %q", code, stdout, errs)
	}
}

// writeKubeconfigContextOnly writes a file with one context, current, and no
// cluster or user: a run's private file.
func writeKubeconfigContextOnly(t *testing.T, path, ctx, cluster, user string) {
	t.Helper()
	content := "apiVersion: v1\nkind: Config\ncontexts:\n- name: " + ctx + "\n  context:\n    cluster: " + cluster + "\n    user: " + user + "\ncurrent-context: " + ctx + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// kcx --unset leaves no current context; kcx - brings it back. Not inside a
// kcx shell or a run.
func TestUnset(t *testing.T) {
	e, l := fixture(t)
	if code, _, errs := runKcx(t, e, "alpha"); code != 0 {
		t.Fatalf("kcx alpha: %q", errs)
	}
	code, stdout, errs := runKcx(t, e, "--unset")
	if code != 0 || !strings.Contains(stdout, "no current context now (it was alpha") || currentOf(t, l.State) != "" {
		t.Fatalf("--unset: %d %q %q", code, stdout, errs)
	}
	if code, stdout, _ := runKcx(t, e, "--unset"); code != 0 || !strings.Contains(stdout, "no current context to unset") {
		t.Fatalf("--unset twice: %d %q", code, stdout)
	}
	if code, stdout, _ := runKcx(t, e, "-"); code != 0 || !strings.Contains(stdout, "switched to alpha") {
		t.Fatalf("kcx - after --unset: %d %q", code, stdout)
	}
	if code, _, _ := runKcx(t, e, "--unset", "x"); code != 2 {
		t.Fatal("--unset takes no arguments")
	}
	// A cluster file with a current context of its own keeps one current: said so.
	writeKubeconfig(t, l.PermFile("stray"), "stray", "stray", "stray", "https://stray.example.test", "stray")
	if code, _, errs := runKcx(t, e, "--unset"); code != 0 || !strings.Contains(errs, "stray is still current: a cluster file sets it") {
		t.Fatalf("--unset with a stray file: %d %q", code, errs)
	}
	private := filepath.Join(t.TempDir(), "config.yaml")
	writeKubeconfigContextOnly(t, private, "alpha", "alpha", "alpha")
	if code, _, errs := runKcx(t, env{"HOME": e["HOME"], EnvShell: private}, "--unset"); code != 1 || !strings.Contains(errs, "a kcx shell always has a context of its own") {
		t.Fatalf("--unset in a kcx shell: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, env{"HOME": e["HOME"], EnvRun: "alpha"}, "--unset"); code != 1 || !strings.Contains(errs, "inside kcx run") {
		t.Fatalf("--unset in a run: %d %q", code, errs)
	}
}
