package temp

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fake struct {
	endpoints map[string]string
	clusters  map[string][]string
	down      map[string]bool
	used      map[string][]int
	hostnames map[string]string
	ips       map[string]string
}

func (f *fake) Endpoint(c string) (string, error) {
	ep, ok := f.endpoints[c]
	if !ok {
		return "", errors.New("no docker context named " + c)
	}
	return ep, nil
}
func (f *fake) Contexts() ([]DockerContext, error) { return nil, nil }
func (f *fake) Clusters(c string) ([]string, error) {
	if f.down[c] {
		return nil, errors.New(c + " does not answer")
	}
	return f.clusters[c], nil
}
func (f *fake) UsedPorts(c string) ([]int, error) { return f.used[c], nil }
func (f *fake) Create(string, []string, io.Writer) (string, error) {
	return "", nil
}
func (f *fake) Delete(string, string, string, io.Writer) error { return nil }
func (f *fake) SSHHostname(alias string) (string, error) {
	if h, ok := f.hostnames[alias]; ok {
		return h, nil
	}
	return "", errors.New("no such alias")
}
func (f *fake) Resolve(host string) (string, error) {
	if ip, ok := f.ips[host]; ok {
		return ip, nil
	}
	return "", errors.New("cannot resolve " + host)
}

func TestAddress(t *testing.T) {
	e := &fake{
		endpoints: map[string]string{
			"desktop-linux": "unix:///Users/me/.docker/run/docker.sock",
			"pipe":          "npipe:////./pipe/docker_engine",
			"lab":           "ssh://me@lab-alias",
			"labport":       "ssh://lab-alias:2222/",
			"direct":        "tcp://10.1.0.29:2376",
			"named":         "tcp://engine.lan:2376/path",
			"odd":           "http://x",
		},
		hostnames: map[string]string{"lab-alias": "lab.lan"},
		ips:       map[string]string{"lab.lan": "10.1.0.29", "engine.lan": "10.1.0.30"},
	}
	cases := map[string]string{"desktop-linux": "", "pipe": "", "lab": "10.1.0.29", "labport": "10.1.0.29", "direct": "10.1.0.29", "named": "10.1.0.30"}
	for ctx, want := range cases {
		got, err := Address(e, ctx)
		if err != nil || got != want {
			t.Errorf("Address(%s) = %q, %v; want %q", ctx, got, err, want)
		}
	}
	if _, err := Address(e, "odd"); err == nil || !strings.Contains(err.Error(), "an endpoint kcx does not know") {
		t.Errorf("odd endpoint: %v", err)
	}
	if _, err := Address(e, "missing"); err == nil {
		t.Error("missing context must fail")
	}
	// An ssh alias without a configured HostName resolves by its own name.
	e.endpoints["bare"] = "ssh://bare.lan"
	e.ips["bare.lan"] = "10.1.0.31"
	if got, _ := Address(e, "bare"); got != "10.1.0.31" {
		t.Errorf("bare alias: %q", got)
	}
}

func TestFreePortAndPublished(t *testing.T) {
	ports := parsePublishedPorts("0.0.0.0:41234->6443/tcp, :::41234->6443/tcp, 127.0.0.1:8080->80/tcp")
	if len(ports) != 3 || ports[0] != 41234 || ports[2] != 8080 {
		t.Fatalf("parsePublishedPorts: %v", ports)
	}
	used := make([]int, 0, 8000)
	for p := 41000; p < 49000; p++ {
		if p != 45000 {
			used = append(used, p)
		}
	}
	e := &fake{used: map[string][]int{"lab": used}}
	// Only 45000 is free: the picker finds it or gives up honestly.
	found := false
	for i := 0; i < 20 && !found; i++ {
		p, err := FreePort(e, "lab")
		if err == nil {
			if p != 45000 {
				t.Fatalf("FreePort picked a used port %d", p)
			}
			found = true
		}
	}
	e.used["full"] = append([]int{45000}, used...)
	if _, err := FreePort(e, "full"); err == nil {
		t.Fatal("no free port must fail")
	}
}

func TestAimConfigAndPresets(t *testing.T) {
	dir := t.TempDir()
	preset := filepath.Join(dir, "calico.yaml")
	if err := os.WriteFile(preset, []byte("kind: Cluster\napiVersion: kind.x-k8s.io/v1alpha4\nnetworking:\n  disableDefaultCNI: true\n  podSubnet: 192.168.0.0/16\nnodes:\n- role: control-plane\n- role: worker\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := AimConfig(preset, "10.1.0.29", 41234)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"apiServerAddress: 10.1.0.29", "apiServerPort: 41234", "disableDefaultCNI: true", "podSubnet: 192.168.0.0/16", "role: worker"} {
		if !strings.Contains(s, want) {
			t.Errorf("aimed preset misses %q:\n%s", want, s)
		}
	}
	out, err = AimConfig("", "10.1.0.29", 41234)
	if err != nil || !strings.Contains(string(out), "kind: Cluster") || !strings.Contains(string(out), "apiServerPort: 41234") {
		t.Fatalf("default config: %v\n%s", err, out)
	}
	addr, port, err := PresetAddressAndPort(preset)
	if err != nil || addr != "" || port != 0 {
		t.Fatalf("preset without networking address: %q %d %v", addr, port, err)
	}
	fixed := filepath.Join(dir, "fixed.yaml")
	if err := os.WriteFile(fixed, []byte("kind: Cluster\nnetworking:\n  apiServerAddress: 127.0.0.1\n  apiServerPort: 6443\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr, port, _ = PresetAddressAndPort(fixed)
	if addr != "127.0.0.1" || port != 6443 || !IsLoopback(addr) || IsLoopback("10.1.0.29") {
		t.Fatalf("fixed preset: %q %d", addr, port)
	}
	if got := Presets(dir); strings.Join(got, ",") != "calico,fixed" {
		t.Fatalf("Presets: %v", got)
	}
	if !PortCollision("ERROR: ... Bind for 0.0.0.0:41234 failed: port is already allocated") || !PortCollision("address already in use") || PortCollision("all good") {
		t.Fatal("PortCollision")
	}
}

func TestStatesAndFindFile(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"t1@lab.yaml", "t2@lab.yaml", "t3@down.yaml", "k3d-x.yaml"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e := &fake{clusters: map[string][]string{"lab": {"t1"}}, down: map[string]bool{"down": true}}
	states := States(e, dir)
	want := "k3d-x/-/other t1/lab/up t2/lab/gone t3/down/unknown"
	var got []string
	for _, s := range states {
		got = append(got, s.Name+"/"+s.Engine+"/"+s.State)
	}
	if strings.Join(got, " ") != want {
		t.Fatalf("States: %v", got)
	}
	f, err := FindFile(dir, "t2")
	if err != nil || !strings.HasSuffix(f, "t2@lab.yaml") {
		t.Fatalf("FindFile: %q %v", f, err)
	}
	if f, _ := FindFile(dir, "nope"); f != "" {
		t.Fatal("FindFile of a missing cluster must be empty")
	}
	if err := os.WriteFile(filepath.Join(dir, "t2@other.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := FindFile(dir, "t2"); err == nil || !strings.Contains(err.Error(), "two kubeconfigs") {
		t.Fatalf("two files: %v", err)
	}
}
