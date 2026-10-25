package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/junior/kcx/internal/kubeconfig"
	"github.com/junior/kcx/internal/temp"
)

// fakeEngine stands in for docker and kind: Create writes the kubeconfig kind
// would write, to the --kubeconfig path, and records every call.
type fakeEngine struct {
	endpoints map[string]string
	clusters  map[string][]string
	down      map[string]bool
	used      map[string][]int
	current   string
	creates   [][]string
	configs   []string
	deletes   []string
	failFirst bool // the first Create answers "port is already allocated"
}

func (f *fakeEngine) Endpoint(c string) (string, error) {
	ep, ok := f.endpoints[c]
	if !ok {
		return "", errors.New("no docker context named " + c)
	}
	return ep, nil
}

func (f *fakeEngine) Contexts() ([]temp.DockerContext, error) {
	var out []temp.DockerContext
	for n, ep := range f.endpoints {
		out = append(out, temp.DockerContext{Name: n, Endpoint: ep, Current: n == f.current})
	}
	return out, nil
}

func (f *fakeEngine) Clusters(c string) ([]string, error) {
	if f.down[c] {
		return nil, errors.New(c + " does not answer")
	}
	return f.clusters[c], nil
}

func (f *fakeEngine) UsedPorts(c string) ([]int, error) { return f.used[c], nil }

func (f *fakeEngine) Create(c string, args []string, stderr io.Writer) (string, error) {
	f.creates = append(f.creates, append([]string{c}, args...))
	kubeconfigPath, cfgPath, name := "", "", ""
	for i, a := range args {
		switch a {
		case "--kubeconfig":
			kubeconfigPath = args[i+1]
		case "--config":
			cfgPath = args[i+1]
		case "--name":
			name = args[i+1]
		}
	}
	if cfgPath != "" {
		data, _ := os.ReadFile(cfgPath)
		f.configs = append(f.configs, string(data))
	}
	if f.failFirst {
		f.failFirst = false
		io.WriteString(stderr, "ERROR: Bind for 0.0.0.0:41234 failed: port is already allocated\n")
		return "port is already allocated", errors.New("exit 1")
	}
	io.WriteString(stderr, "Creating cluster \""+name+"\" ...\n")
	content := "apiVersion: v1\nkind: Config\nclusters:\n- name: kind-" + name + "\n  cluster:\n    server: https://127.0.0.1:6443\nusers:\n- name: kind-" + name +
		"\n  user:\n    token: not-a-secret\ncontexts:\n- name: kind-" + name + "\n  context:\n    cluster: kind-" + name + "\n    user: kind-" + name + "\ncurrent-context: kind-" + name + "\n"
	if err := os.WriteFile(kubeconfigPath, []byte(content), 0o644); err != nil {
		return "", err
	}
	f.clusters[c] = append(f.clusters[c], name)
	return "", nil
}

func (f *fakeEngine) Delete(c, name, kubeconfig string, stderr io.Writer) error {
	f.deletes = append(f.deletes, c+"/"+name)
	var kept []string
	for _, n := range f.clusters[c] {
		if n != name {
			kept = append(kept, n)
		}
	}
	f.clusters[c] = kept
	_ = os.WriteFile(kubeconfig, []byte(""), 0o600) // kind empties the file
	return nil
}

func (f *fakeEngine) SSHHostname(alias string) (string, error) { return "lab.lan", nil }
func (f *fakeEngine) Resolve(host string) (string, error)      { return "10.1.0.29", nil }

func withFakeEngine(t *testing.T) *fakeEngine {
	t.Helper()
	f := &fakeEngine{
		endpoints: map[string]string{"desktop-linux": "unix:///var/run/docker.sock", "lab": "ssh://me@lab", "down": "ssh://down"},
		clusters:  map[string][]string{},
		down:      map[string]bool{"down": true},
		used:      map[string][]int{},
		current:   "desktop-linux",
	}
	oldEngine, oldLook := newEngine, lookPath
	newEngine = func() temp.Engine { return f }
	lookPath = func(name string) (string, error) {
		if name == "kind" || name == "docker" {
			return "/usr/local/bin/" + name, nil
		}
		return "", os.ErrNotExist
	}
	t.Cleanup(func() { newEngine, lookPath = oldEngine, oldLook })
	return f
}

func TestTempCreateLsRmPrune(t *testing.T) {
	e, l := fixture(t)
	f := withFakeEngine(t)
	// Local engine: no config, kind's own cluster; the file carries no current-context.
	code, out, errs := runKcx(t, e, "temp", "create", "t1")
	if code != 0 || !strings.Contains(out, "temp cluster t1 is up on desktop-linux: context kind-t1, server https://127.0.0.1:6443") || !strings.Contains(out, "kcx temp rm t1 deletes it") {
		t.Fatalf("temp create: %d %q %q", code, out, errs)
	}
	file := l.TempFile("t1", "desktop-linux")
	cfg, err := kubeconfig.LoadFile(file)
	if err != nil || cfg.CurrentContext != "" {
		t.Fatalf("temp file: %v %+v", err, cfg)
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	if len(f.creates) != 1 || strings.Contains(strings.Join(f.creates[0], " "), "--config") {
		t.Fatalf("local create must pass no config: %v", f.creates)
	}
	if cur, _ := (kubeconfig.Store{Files: l.Files()}).Current(); cur != "" {
		t.Fatal("create switched without --use")
	}
	// Remote engine with a preset: the config is aimed at the engine's address and a
	// free port; a collision gets one retry.
	if err := os.MkdirAll(l.Presets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(l.Presets, "calico.yaml"), []byte("kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnetworking:\n  disableDefaultCNI: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.failFirst = true
	code, out, errs = runKcx(t, e, "temp", "create", "t2", "--engine", "lab", "--config", "calico", "--use")
	if code != 0 || !strings.Contains(out, "switched to kind-t2") || !strings.Contains(errs, "is taken on lab; trying another") {
		t.Fatalf("remote create: %d %q %q", code, out, errs)
	}
	if len(f.configs) != 2 || !strings.Contains(f.configs[1], "apiServerAddress: 10.1.0.29") || !strings.Contains(f.configs[1], "disableDefaultCNI: true") {
		t.Fatalf("aimed configs: %v", f.configs)
	}
	if cur, _ := (kubeconfig.Store{Files: l.Files()}).Current(); cur != "kind-t2" {
		t.Fatalf("--use: %q", cur)
	}
	// Refusals.
	if code, _, errs := runKcx(t, e, "temp", "create", "t1"); code != 1 || !strings.Contains(errs, "exists already (kcx temp ls)") {
		t.Fatalf("duplicate: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, e, "temp", "create", "Bad_Name"); code != 1 || !strings.Contains(errs, "kind takes lowercase") {
		t.Fatalf("bad name: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, e, "temp", "create", "t3", "--config", "nope"); code != 1 || !strings.Contains(errs, "no kind preset or file nope; presets: calico") {
		t.Fatalf("missing preset: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, e, "temp", "create", "t3", "--engine", "ghost"); code != 1 || !strings.Contains(errs, "no docker context named ghost") {
		t.Fatalf("missing engine: %d %q", code, errs)
	}
	f.clusters["desktop-linux"] = append(f.clusters["desktop-linux"], "t3")
	if code, _, errs := runKcx(t, e, "temp", "create", "t3"); code != 1 || !strings.Contains(errs, "already runs on desktop-linux") {
		t.Fatalf("cluster already running: %d %q", code, errs)
	}
	if code, _, _ := runKcx(t, e, "temp"); code != 2 {
		t.Fatal("temp without a subcommand is a usage error")
	}
	// ls: states from the engines.
	code, out, _ = runKcx(t, e, "temp", "ls")
	if code != 0 || !strings.Contains(out, "NAME  ENGINE") || !strings.Contains(out, "t1    desktop-linux  up") || !strings.Contains(out, "t2    lab            up") {
		t.Fatalf("temp ls:\n%s", out)
	}
	// rm: kind delete, the file goes, the current context is cleared.
	code, out, _ = runKcx(t, e, "temp", "rm", "t2")
	if code != 0 || !strings.Contains(out, "kind-t2 was the current context; there is none now") || !strings.Contains(out, "removed temp cluster t2") {
		t.Fatalf("temp rm: %d %q", code, out)
	}
	if len(f.deletes) != 1 || f.deletes[0] != "lab/t2" {
		t.Fatalf("deletes: %v", f.deletes)
	}
	if _, err := os.Stat(l.TempFile("t2", "lab")); !os.IsNotExist(err) {
		t.Fatal("t2's file still there")
	}
	if code, _, errs := runKcx(t, e, "temp", "rm", "t2"); code != 1 || !strings.Contains(errs, "no temp cluster named t2") {
		t.Fatalf("rm again: %d %q", code, errs)
	}
	// An engine that does not answer keeps the file; prune keeps it too, and
	// removes a cluster that is gone.
	writeKubeconfig(t, l.TempFile("t9", "down"), "kind-t9", "kind-t9", "kind-t9", "https://10.0.0.9:41000", "")
	if code, _, errs := runKcx(t, e, "temp", "rm", "t9"); code != 1 || !strings.Contains(errs, "down does not answer; the kubeconfig stays") {
		t.Fatalf("rm on a dead engine: %d %q", code, errs)
	}
	f.clusters["desktop-linux"] = []string{"t3"} // t1 is gone from the engine
	code, out, errs = runKcx(t, e, "temp", "prune")
	if code != 0 || !strings.Contains(out, "removed the kubeconfig of t1, gone from desktop-linux") || !strings.Contains(errs, "down does not answer; kept") {
		t.Fatalf("prune: %d %q %q", code, out, errs)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("pruned file still there")
	}
	if code, out, _ := runKcx(t, e, "temp", "prune"); code != 0 || !strings.Contains(out, "nothing to prune") {
		t.Fatalf("prune again: %d %q", code, out)
	}
	// A temp cluster another tool made: rm removes the kubeconfig only, with a warning.
	writeKubeconfig(t, l.TempFile("k3d-x", ""), "k3d-x", "k3d-x", "k3d-x", "https://127.0.0.1:6550", "")
	code, out, errs = runKcx(t, e, "temp", "rm", "k3d-x")
	if code != 0 || !strings.Contains(errs, "made by another tool") || !strings.Contains(out, "removed temp cluster k3d-x") {
		t.Fatalf("rm of another tool's cluster: %d %q %q", code, out, errs)
	}
	// Without kind on PATH, create says so.
	lookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if code, _, errs := runKcx(t, e, "temp", "create", "t5"); code != 1 || !strings.Contains(errs, "kind is not installed") {
		t.Fatalf("no kind: %d %q", code, errs)
	}
}
