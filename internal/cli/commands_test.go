package cli

import (
	"crypto/md5"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/junior/kcx/v2/internal/kubeconfig"
	"github.com/junior/kcx/v2/internal/layout"
)

// fixture builds a sandbox home with permanent clusters alpha and beta, written
// the way kcx writes them (no current-context), and returns its environment.
func fixture(t *testing.T) (env, layout.Layout) {
	t.Helper()
	home := t.TempDir()
	e := env{"HOME": home, "KCX_TRASH_DIR": filepath.Join(home, "trash")}
	l, err := layout.FromEnv(e.get)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"alpha", "beta"} {
		writeKubeconfig(t, l.PermFile(n), n, n, n, "https://"+n+".example.test:6443", "")
	}
	return e, l
}

func writeKubeconfig(t *testing.T, path, ctx, cluster, user, server, current string) {
	t.Helper()
	content := "apiVersion: v1\nkind: Config\nclusters:\n- name: " + cluster + "\n  cluster:\n    server: " + server +
		"\nusers:\n- name: " + user + "\n  user:\n    token: not-a-secret-" + user +
		"\ncontexts:\n- name: " + ctx + "\n  context:\n    cluster: " + cluster + "\n    user: " + user + "\n"
	if current != "" {
		content += "current-context: " + current + "\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func checksum(t *testing.T, path string) [16]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return md5.Sum(data)
}

func TestSwitchPreviousCurrent(t *testing.T) {
	e, l := fixture(t)
	a, b := checksum(t, l.PermFile("alpha")), checksum(t, l.PermFile("beta"))
	if code, _, errs := runKcx(t, e, "-c"); code != 1 || !strings.Contains(errs, "no current context") {
		t.Fatalf("-c with none: %d %q", code, errs)
	}
	if code, out, _ := runKcx(t, e, "alpha"); code != 0 || out != "kcx: switched to alpha\n" {
		t.Fatalf("switch: %d %q", code, out)
	}
	if code, out, _ := runKcx(t, e, "-c"); code != 0 || out != "alpha\n" {
		t.Fatalf("-c: %d %q", code, out)
	}
	if code, _, errs := runKcx(t, e, "-"); code != 1 || !strings.Contains(errs, "no previous context") {
		t.Fatalf("- without previous: %d %q", code, errs)
	}
	if code, _, _ := runKcx(t, e, "beta"); code != 0 {
		t.Fatal("switch to beta")
	}
	if code, out, _ := runKcx(t, e, "-"); code != 0 || out != "kcx: switched to alpha\n" {
		t.Fatalf("-: %d %q", code, out)
	}
	if kubeconfig.ReadPrevious(l.Prev) != "beta" {
		t.Fatal("previous should now be beta")
	}
	if checksum(t, l.PermFile("alpha")) != a || checksum(t, l.PermFile("beta")) != b {
		t.Fatal("switching rewrote a credential file")
	}
	if code, _, errs := runKcx(t, e, "gamma"); code != 1 || !strings.Contains(errs, "no context named gamma") {
		t.Fatalf("unknown: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, e, "alpha", "extra"); code != 2 {
		t.Fatalf("two args: %d %q", code, errs)
	}
	// Inside a run, switching is refused.
	run := env{"HOME": e["HOME"], EnvRun: "alpha", "KUBECONFIG": l.Kubeconfig()}
	if code, _, errs := runKcx(t, run, "beta"); code != 1 || !strings.Contains(errs, "inside kcx run") {
		t.Fatalf("run guard: %d %q", code, errs)
	}
}

func TestResolveByFileAndKindName(t *testing.T) {
	e, l := fixture(t)
	// A file whose context is called something else: the file name wins.
	writeKubeconfig(t, l.PermFile("eks-lab"), "arn:aws:eks:us-west-2:1:cluster/lab", "eks-lab", "arn:aws:eks:us-west-2:1:cluster/lab", "https://eks.example.test", "")
	if code, out, _ := runKcx(t, e, "eks-lab"); code != 0 || out != "kcx: switched to arn:aws:eks:us-west-2:1:cluster/lab\n" {
		t.Fatalf("resolve by file: %d %q", code, out)
	}
	writeKubeconfig(t, l.TempFile("t1", "imac"), "kind-t1", "kind-t1", "kind-t1", "https://10.0.0.1:41000", "")
	if code, out, _ := runKcx(t, e, "t1"); code != 0 || out != "kcx: switched to kind-t1\n" {
		t.Fatalf("resolve kind-NAME: %d %q", code, out)
	}
}

func TestLs(t *testing.T) {
	e, l := fixture(t)
	if code, _, _ := runKcx(t, e, "beta"); code != 0 {
		t.Fatal("switch")
	}
	// A stray from another tool in the state file, and a second alpha in a temp file.
	writeKubeconfig(t, filepath.Join(l.Home, "stray.yaml"), "minikube", "minikube", "minikube", "https://127.0.0.1:8443", "")
	stray, _ := kubeconfig.LoadFile(filepath.Join(l.Home, "stray.yaml"))
	state, _ := kubeconfig.LoadFile(l.State)
	state.Contexts["minikube"] = stray.Contexts["minikube"]
	state.Clusters["minikube"] = stray.Clusters["minikube"]
	state.AuthInfos["minikube"] = stray.AuthInfos["minikube"]
	if err := kubeconfig.WriteFile(l.State, state); err != nil {
		t.Fatal(err)
	}
	writeKubeconfig(t, l.TempFile("dup", "imac"), "alpha", "alpha", "alpha", "https://dup.example.test", "alpha")
	if err := os.WriteFile(filepath.Join(l.Perm, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runKcx(t, e, "ls")
	if code != 0 {
		t.Fatalf("ls: %d %s", code, errs)
	}
	wantOut := "   minikube  ~/.kube/config (written by another tool)  https://127.0.0.1:8443\n" +
		"   alpha     configs                                   https://alpha.example.test:6443\n" +
		"*  beta      configs                                   https://beta.example.test:6443\n" +
		"   alpha     temp, on imac                             https://dup.example.test\n"
	if out != wantOut {
		t.Fatalf("ls output:\n%s\nwant:\n%s", out, wantOut)
	}
	for _, want := range []string{
		"the cluster alpha is defined in " + l.PermFile("alpha") + " and in " + l.TempFile("dup", "imac") + "; kubectl uses the first",
		"holds 1 context(s) another tool wrote there",
		"set a current context of their own",
		"not in KUBECONFIG (kcx lists *.yaml files only): " + l.Perm + ": notes.txt",
	} {
		if !strings.Contains(errs, want) {
			t.Errorf("ls warnings miss %q in:\n%s", want, errs)
		}
	}
	code, out, _ = runKcx(t, e, "ls", "-q")
	if code != 0 || out != "alpha\nbeta\nminikube\n" {
		t.Fatalf("ls -q: %d %q", code, out)
	}
	if code, _, errs := runKcx(t, e, "ls", "-x"); code != 2 || !strings.Contains(errs, "kcx ls [-q] [--group G] [PATTERN]") {
		t.Fatalf("ls -x: %d %q", code, errs)
	}
	ro := env{"HOME": e["HOME"], EnvRO: "beta (READ-ONLY)"}
	if code, out, _ := runKcx(t, ro, "ls"); code != 0 || out != "beta (READ-ONLY)\n" {
		t.Fatalf("ls in RO: %d %q", code, out)
	}
	empty := env{"HOME": t.TempDir()}
	if code, _, errs := runKcx(t, empty, "ls"); code != 0 || !strings.Contains(errs, "no contexts yet") {
		t.Fatalf("ls empty: %d %q", code, errs)
	}
}

func TestAddFromFileStdinAndCommand(t *testing.T) {
	e, l := fixture(t)
	src := filepath.Join(t.TempDir(), "src.yaml")
	writeKubeconfig(t, src, "admin@lab", "lab", "admin", "https://lab.example.test", "admin@lab")
	code, out, errs := runKcx(t, e, "add", "lab", src)
	if code != 0 || !strings.Contains(out, "kcx: added lab ("+l.PermFile("lab")+")") {
		t.Fatalf("add: %d %q %q", code, out, errs)
	}
	if !strings.Contains(out, "kcx run lab -- CMD works now") {
		t.Fatalf("missing the hint for a shell that predates the file: %q", out)
	}
	cfg, _ := kubeconfig.LoadFile(l.PermFile("lab"))
	if _, ok := cfg.Contexts["lab"]; !ok || cfg.CurrentContext != "" {
		t.Fatalf("renamed context missing or current-context kept: %+v", cfg)
	}
	// The hint is not printed when the inherited KUBECONFIG already lists the file.
	eWith := env{"HOME": e["HOME"], "KUBECONFIG": l.Kubeconfig(), "KCX_TRASH_DIR": e["KCX_TRASH_DIR"]}
	if code, _, errs := runKcx(t, e, "add", "lab", src); code != 1 || !strings.Contains(errs, "exists already") {
		t.Fatalf("second add without -f: %d %q", code, errs)
	}
	code, out, _ = runKcx(t, eWith, "add", "lab", src, "-f")
	if code != 0 || strings.Contains(out, "works now") {
		t.Fatalf("add -f: %d %q", code, out)
	}
	entries, _ := os.ReadDir(e["KCX_TRASH_DIR"])
	if len(entries) != 1 {
		t.Fatalf("the replaced file did not go to the Trash: %v", entries)
	}
	// From standard input.
	var outb, errb strings.Builder
	data, _ := os.ReadFile(src)
	code = Main([]string{"add", "lab2", "-", "--server", "https://lab2.lan:6443"}, strings.NewReader(string(data)), &outb, &errb, e.get)
	if code != 0 {
		t.Fatalf("add from stdin: %d %s", code, errb.String())
	}
	cfg, _ = kubeconfig.LoadFile(l.PermFile("lab2"))
	if cfg.Clusters["lab2"].Server != "https://lab2.lan:6443" {
		t.Fatalf("--server: %+v", cfg.Clusters["lab2"])
	}
	// From a command that writes to KUBECONFIG, and one that takes {}.
	script := filepath.Join(t.TempDir(), "fake-cli")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n# writes the kubeconfig where KUBECONFIG points, like a cloud CLI\ncat \"$1\" > \"$KUBECONFIG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	code, out, errs = runKcx(t, e, "add", "lab3", "--use", "--", script, src)
	if code != 0 || !strings.Contains(out, "switched to lab3") {
		t.Fatalf("add -- CMD --use: %d %q %q", code, out, errs)
	}
	if cur, _ := (kubeconfig.Store{Files: l.Files()}).Current(); cur != "lab3" {
		t.Fatalf("--use did not switch: %q", cur)
	}
	withFile := filepath.Join(t.TempDir(), "fake-cli-file")
	if err := os.WriteFile(withFile, []byte("#!/bin/sh\n# takes the output file as an option\ncp \"$1\" \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runKcx(t, e, "add", "lab4", "--", withFile, src, "{}"); code != 0 {
		t.Fatalf("add with {}: %d %q", code, errs)
	}
	silent := filepath.Join(t.TempDir(), "silent")
	if err := os.WriteFile(silent, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runKcx(t, e, "add", "lab5", "--", silent); code != 1 || !strings.Contains(errs, "wrote no kubeconfig") {
		t.Fatalf("silent command: %d %q", code, errs)
	}
	failing := filepath.Join(t.TempDir(), "failing")
	if err := os.WriteFile(failing, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runKcx(t, e, "add", "lab6", "--", failing); code != 1 || !strings.Contains(errs, "exit 3") {
		t.Fatalf("failing command: %d %q", code, errs)
	}
	// Argument mistakes.
	for _, args := range [][]string{{"add"}, {"add", "x"}, {"add", "x", "f", "--"}, {"add", "x", "--bogus", "f"}, {"add", "bad/name", "f"}} {
		code, _, _ := runKcx(t, e, args...)
		if code == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
	// A lab node refuses permanent credentials.
	lab := env{"HOME": e["HOME"], "KCX_LAB_NODE": "1"}
	if code, _, errs := runKcx(t, lab, "add", "lab7", src); code != 1 || !strings.Contains(errs, "lab node") {
		t.Fatalf("lab guard: %d %q", code, errs)
	}
	lab["KCX_ALLOW_ON_LAB_NODE"] = "1"
	if code, _, _ := runKcx(t, lab, "add", "lab7", src); code != 0 {
		t.Fatal("KCX_ALLOW_ON_LAB_NODE=1 must override")
	}
}

func TestAddKeepNamesAndTemplates(t *testing.T) {
	e, l := fixture(t)
	src := filepath.Join(t.TempDir(), "eks.yaml")
	writeKubeconfig(t, src, "arn:aws:eks:us-west-2:1:cluster/lab", "arn:aws:eks:us-west-2:1:cluster/lab", "arn:aws:eks:us-west-2:1:cluster/lab", "https://eks.example.test", "")
	if code, _, errs := runKcx(t, e, "add", "eks-lab", src, "--keep-names"); code != 0 {
		t.Fatalf("--keep-names: %d %q", code, errs)
	}
	cfg, _ := kubeconfig.LoadFile(l.PermFile("eks-lab"))
	if _, ok := cfg.Contexts["arn:aws:eks:us-west-2:1:cluster/lab"]; !ok {
		t.Fatalf("context renamed despite --keep-names: %v", cfg.Contexts)
	}
	// The same source again with the user kept by name collides with eks-lab's user
	// entry: no two files may share an entry.
	if code, _, errs := runKcx(t, e, "add", "eks2", src, "--user-name", "{user}"); code != 1 || !strings.Contains(errs, "a user named arn:aws:eks:us-west-2:1:cluster/lab already exists") {
		t.Fatalf("expected the user collision: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, e, "add", "eks2", src, "--context-name", "{user}@{name}"); code != 0 {
		t.Fatalf("templates: %d %q", code, errs)
	}
	cfg, _ = kubeconfig.LoadFile(l.PermFile("eks2"))
	if _, ok := cfg.Contexts["arn:aws:eks:us-west-2:1:cluster/lab@eks2"]; !ok {
		t.Fatalf("templated context: %v", cfg.Contexts)
	}
	if _, ok := cfg.AuthInfos["eks2"]; !ok {
		t.Fatalf("the user should default to the short name: %v", cfg.AuthInfos)
	}
	// Both files resolve by their file name.
	if code, out, _ := runKcx(t, e, "eks2"); code != 0 || out != "kcx: switched to arn:aws:eks:us-west-2:1:cluster/lab@eks2\n" {
		t.Fatalf("switch by file: %d %q", code, out)
	}
}

func TestRmMvAdopt(t *testing.T) {
	e, l := fixture(t)
	if code, _, _ := runKcx(t, e, "alpha"); code != 0 {
		t.Fatal("switch")
	}
	// rm of the current cluster clears the current context and uses the Trash.
	code, out, _ := runKcx(t, e, "rm", "alpha")
	if code != 0 || !strings.Contains(out, "moved "+l.PermFile("alpha")+" to the Trash") || !strings.Contains(out, "no current context now") {
		t.Fatalf("rm: %d %q", code, out)
	}
	if _, err := os.Stat(l.PermFile("alpha")); !os.IsNotExist(err) {
		t.Fatal("alpha still there")
	}
	if code, _, errs := runKcx(t, e, "rm", "alpha"); code != 1 || !strings.Contains(errs, "no permanent cluster named alpha") {
		t.Fatalf("rm again: %d %q", code, errs)
	}
	writeKubeconfig(t, l.TempFile("t1", "imac"), "kind-t1", "kind-t1", "kind-t1", "https://10.0.0.1:41000", "")
	if code, _, errs := runKcx(t, e, "rm", "t1"); code != 1 || !strings.Contains(errs, "kcx temp rm t1") {
		t.Fatalf("rm temp: %d %q", code, errs)
	}
	// mv keeps the current context and the previous one pointing at the renamed cluster.
	if code, _, _ := runKcx(t, e, "beta"); code != 0 {
		t.Fatal("switch beta")
	}
	if err := kubeconfig.WritePrevious(l.Prev, "beta"); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runKcx(t, e, "mv", "beta", "gamma")
	if code != 0 || !strings.Contains(out, "renamed beta to gamma") {
		t.Fatalf("mv: %d %q %q", code, out, errs)
	}
	if _, err := os.Stat(l.PermFile("beta")); !os.IsNotExist(err) {
		t.Fatal("old file still there after mv")
	}
	if cur, _ := (kubeconfig.Store{Files: l.Files()}).Current(); cur != "gamma" {
		t.Fatalf("current after mv: %q", cur)
	}
	if kubeconfig.ReadPrevious(l.Prev) != "gamma" {
		t.Fatal("previous not renamed")
	}
	cfg, _ := kubeconfig.LoadFile(l.PermFile("gamma"))
	if cfg.AuthInfos["gamma"].Token != "not-a-secret-beta" {
		t.Fatal("mv lost the credentials")
	}
	if code, _, errs := runKcx(t, e, "mv", "gamma", "gamma"); code != 1 || !strings.Contains(errs, "exists already") {
		t.Fatalf("mv onto itself: %d %q", code, errs)
	}
	if code, _, _ := runKcx(t, e, "mv", "nope", "x"); code != 1 {
		t.Fatal("mv of a missing cluster must fail")
	}
	// adopt: two contexts in the state file share a cluster; adopting one keeps the
	// cluster there for the other, adopting the second takes it along.
	state, _ := kubeconfig.LoadFile(l.State)
	src := filepath.Join(t.TempDir(), "mini.yaml")
	writeKubeconfig(t, src, "mini", "mini", "mini", "https://127.0.0.1:8443", "")
	mini, _ := kubeconfig.LoadFile(src)
	state.Clusters["mini"] = mini.Clusters["mini"]
	state.AuthInfos["mini"] = mini.AuthInfos["mini"]
	state.Contexts["mini"] = mini.Contexts["mini"]
	second := *mini.Contexts["mini"]
	second.Namespace = "kube-system"
	state.Contexts["mini-sys"] = &second
	state.CurrentContext = "mini"
	if err := kubeconfig.WriteFile(l.State, state); err != nil {
		t.Fatal(err)
	}
	// The cluster entry stays in the state file for mini-sys, so the adopted copy
	// cannot also be called mini: the user gives the file another name.
	if code, _, errs := runKcx(t, e, "adopt", "mini"); code != 1 || !strings.Contains(errs, "a cluster named mini already exists in "+l.State) {
		t.Fatalf("adopt with a shared cluster under the same name: %d %q", code, errs)
	}
	code, out, errs = runKcx(t, e, "adopt", "mini", "mini1")
	if code != 0 || !strings.Contains(out, "mini now lives in "+l.PermFile("mini1")+" as mini1") {
		t.Fatalf("adopt: %d %q %q", code, out, errs)
	}
	state, _ = kubeconfig.LoadFile(l.State)
	if _, ok := state.Contexts["mini"]; ok {
		t.Fatal("adopted context still in the state file")
	}
	if _, ok := state.Clusters["mini"]; !ok {
		t.Fatal("the cluster must stay while mini-sys uses it")
	}
	if state.CurrentContext != "mini1" {
		t.Fatalf("current after adopt: %q (should follow the context into its own file)", state.CurrentContext)
	}
	code, out, errs = runKcx(t, e, "adopt", "mini-sys", "mini-system")
	if code != 0 {
		t.Fatalf("adopt second: %d %q %q", code, out, errs)
	}
	state, _ = kubeconfig.LoadFile(l.State)
	if len(state.Contexts) != 0 || len(state.Clusters) != 0 || len(state.AuthInfos) != 0 {
		t.Fatalf("state file not emptied: %+v", state)
	}
	cfg, _ = kubeconfig.LoadFile(l.PermFile("mini-system"))
	if cfg.Contexts["mini-system"].Namespace != "kube-system" {
		t.Fatal("the adopted context lost its namespace")
	}
	if code, _, errs := runKcx(t, e, "adopt", "nothing"); code != 1 || !strings.Contains(errs, "is not in ~/.kube/config") {
		t.Fatalf("adopt missing: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, e, "adopt", "mini", "bad/name"); code != 1 || !strings.Contains(errs, "is not in") {
		// mini is gone from the state file by now; the first check fires
		t.Fatalf("adopt bad name: %d %q", code, errs)
	}
}

func TestCompleteWords(t *testing.T) {
	e, l := fixture(t)
	code, out, _ := runKcx(t, e, "__complete")
	if code != 0 || !strings.HasPrefix(out, "alpha\nbeta\n") || !strings.Contains(out, "\nadopt\n") {
		t.Fatalf("__complete: %d %q", code, out)
	}
	code, out, _ = runKcx(t, e, "__complete", "rm")
	if code != 0 || out != "alpha\nbeta\n" {
		t.Fatalf("__complete rm: %d %q", code, out)
	}
	if code, out, _ := runKcx(t, e, "__complete", "add", "--context"); code != 0 || out != "" {
		t.Fatalf("__complete after --context: %d %q", code, out)
	}
	_ = l
}
