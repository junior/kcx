// Package temp manages temp kind clusters on any Docker engine: one kubeconfig
// file per cluster under ~/.kube/temp, named NAME@ENGINE.yaml, deleted with the
// cluster. On a remote engine (a docker context over ssh) the API server listens
// on the engine's network address, which kind also puts in its certificate, so
// the cluster works from the machine that made it.
package temp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/junior/kcx/internal/layout"
)

// Engine is what kcx asks a Docker engine and kind to do; a fake stands in for
// it in tests.
type Engine interface {
	// Endpoint returns the docker context's endpoint (unix://..., ssh://..., tcp://...).
	Endpoint(context string) (string, error)
	// Contexts lists the docker contexts: name and endpoint.
	Contexts() ([]DockerContext, error)
	// Clusters lists kind's clusters on the engine; an error means the engine does not answer.
	Clusters(context string) ([]string, error)
	// UsedPorts lists the host ports containers on the engine publish.
	UsedPorts(context string) ([]int, error)
	// Create runs kind create cluster with args on the engine; its output goes to
	// stderr and is returned for inspection.
	Create(context string, args []string, stderr io.Writer) (string, error)
	// Delete runs kind delete cluster on the engine.
	Delete(context, name, kubeconfig string, stderr io.Writer) error
	// SSHHostname resolves an ssh alias to the HostName its configuration gives it.
	SSHHostname(alias string) (string, error)
	// Resolve turns a host name into an IPv4 address.
	Resolve(host string) (string, error)
}

// DockerContext is one docker context.
type DockerContext struct {
	Name, Endpoint string
	Current        bool
}

// Docker is the Engine backed by the docker, kind and ssh commands.
type Docker struct{}

func (Docker) Endpoint(context string) (string, error) {
	out, err := exec.Command("docker", "context", "inspect", context).Output()
	if err != nil {
		return "", fmt.Errorf("no docker context named %s (docker context ls)", context)
	}
	var ctxs []struct {
		Endpoints map[string]struct {
			Host string `json:"Host"`
		} `json:"Endpoints"`
	}
	if err := json.Unmarshal(out, &ctxs); err != nil || len(ctxs) == 0 {
		return "", fmt.Errorf("docker context inspect %s: unreadable answer", context)
	}
	ep := ctxs[0].Endpoints["docker"].Host
	if ep == "" {
		return "", fmt.Errorf("docker context %s has no docker endpoint", context)
	}
	return ep, nil
}

func (Docker) Contexts() ([]DockerContext, error) {
	out, err := exec.Command("docker", "context", "ls", "--format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("docker context ls: %w", err)
	}
	var list []DockerContext
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var c struct {
			Name           string `json:"Name"`
			DockerEndpoint string `json:"DockerEndpoint"`
			Current        bool   `json:"Current"`
		}
		if err := dec.Decode(&c); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("docker context ls: %w", err)
		}
		list = append(list, DockerContext{Name: c.Name, Endpoint: c.DockerEndpoint, Current: c.Current})
	}
	return list, nil
}

func (Docker) Clusters(context string) ([]string, error) {
	cmd := exec.Command("kind", "get", "clusters")
	cmd.Env = append(os.Environ(), "DOCKER_CONTEXT="+context)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s does not answer", context)
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimSpace(l); l != "" && l != "No kind clusters found." {
			names = append(names, l)
		}
	}
	return names, nil
}

func (Docker) UsedPorts(context string) ([]int, error) {
	cmd := exec.Command("docker", "ps", "--format", "json")
	cmd.Env = append(os.Environ(), "DOCKER_CONTEXT="+context)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker ps on %s: %w", context, err)
	}
	var ports []int
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var c struct {
			Ports string `json:"Ports"`
		}
		if err := dec.Decode(&c); err != nil {
			break
		}
		ports = append(ports, parsePublishedPorts(c.Ports)...)
	}
	return ports, nil
}

var publishedPort = regexp.MustCompile(`:(\d+)->`)

// parsePublishedPorts reads the host ports out of docker ps's Ports column
// ("0.0.0.0:41234->6443/tcp, :::41234->6443/tcp").
func parsePublishedPorts(s string) []int {
	var out []int
	for _, m := range publishedPort.FindAllStringSubmatch(s, -1) {
		if p, err := strconv.Atoi(m[1]); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func (Docker) Create(context string, args []string, stderr io.Writer) (string, error) {
	cmd := exec.Command("kind", append([]string{"create", "cluster"}, args...)...)
	cmd.Env = append(os.Environ(), "DOCKER_CONTEXT="+context)
	var captured bytes.Buffer
	cmd.Stdout = io.MultiWriter(stderr, &captured) // kind writes its progress to stderr; keep ours together
	cmd.Stderr = io.MultiWriter(stderr, &captured)
	err := cmd.Run()
	return captured.String(), err
}

func (Docker) Delete(context, name, kubeconfig string, stderr io.Writer) error {
	cmd := exec.Command("kind", "delete", "cluster", "--name", name, "--kubeconfig", kubeconfig)
	cmd.Env = append(os.Environ(), "DOCKER_CONTEXT="+context)
	cmd.Stdout, cmd.Stderr = stderr, stderr
	return cmd.Run()
}

func (Docker) SSHHostname(alias string) (string, error) {
	out, err := exec.Command("ssh", "-G", alias).Output()
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) == 2 && f[0] == "hostname" {
			return f[1], nil
		}
	}
	return "", fmt.Errorf("ssh -G %s names no hostname", alias)
}

func (Docker) Resolve(host string) (string, error) {
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return "", fmt.Errorf("%s has no IPv4 address", host)
}

// Address returns the IPv4 address other machines reach the engine at, or ""
// when the engine is local (a unix socket or a named pipe).
func Address(e Engine, context string) (string, error) {
	ep, err := e.Endpoint(context)
	if err != nil {
		return "", err
	}
	host, local, err := hostOf(ep)
	if err != nil {
		return "", fmt.Errorf("engine %s: %w", context, err)
	}
	if local {
		return "", nil
	}
	if strings.HasPrefix(ep, "ssh://") {
		// an ssh alias: the address is whatever its HostName says
		if h, err := e.SSHHostname(host); err == nil && h != "" {
			host = h
		}
	}
	if net.ParseIP(host) != nil {
		if v4 := net.ParseIP(host).To4(); v4 != nil {
			return v4.String(), nil
		}
		return "", fmt.Errorf("engine %s: %s is not an IPv4 address", context, host)
	}
	ip, err := e.Resolve(host)
	if err != nil {
		return "", fmt.Errorf("engine %s: cannot resolve %s to an IPv4 address: %w", context, host, err)
	}
	return ip, nil
}

// hostOf extracts the host from a docker endpoint. local is true for unix
// sockets and named pipes.
func hostOf(endpoint string) (host string, local bool, err error) {
	switch {
	case strings.HasPrefix(endpoint, "unix://"), strings.HasPrefix(endpoint, "npipe://"):
		return "", true, nil
	case strings.HasPrefix(endpoint, "ssh://"):
		rest := strings.TrimPrefix(endpoint, "ssh://")
		if i := strings.IndexByte(rest, '@'); i >= 0 {
			rest = rest[i+1:]
		}
		rest = strings.SplitN(rest, "/", 2)[0]
		host = strings.SplitN(rest, ":", 2)[0]
	case strings.HasPrefix(endpoint, "tcp://"):
		rest := strings.TrimPrefix(endpoint, "tcp://")
		rest = strings.SplitN(rest, "/", 2)[0]
		host = strings.SplitN(rest, ":", 2)[0]
	default:
		return "", false, fmt.Errorf("an endpoint kcx does not know: %s", endpoint)
	}
	if host == "" {
		return "", false, fmt.Errorf("no host in %s", endpoint)
	}
	return host, false, nil
}

// FreePort picks a host port in 41000-48999 that no container on the engine
// publishes.
func FreePort(e Engine, context string) (int, error) {
	used, err := e.UsedPorts(context)
	if err != nil {
		return 0, err
	}
	taken := map[int]bool{}
	for _, p := range used {
		taken[p] = true
	}
	for i := 0; i < 50; i++ {
		p := 41000 + rand.Intn(8000)
		if !taken[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port on %s", context)
}

// Presets lists the kind presets in dir (NAME.yaml), sorted.
func Presets(dir string) []string {
	var out []string
	for _, f := range layout.DirFiles(dir) {
		out = append(out, strings.TrimSuffix(filepath.Base(f), ".yaml"))
	}
	sort.Strings(out)
	return out
}

// AimConfig returns a kind config for a remote engine: the preset (or kind's
// default one-node cluster when preset is "") with the API server bound to addr
// and port. It reports the port the preset itself asked for, when it did.
func AimConfig(presetPath, addr string, port int) ([]byte, error) {
	cfg := map[string]any{"kind": "Cluster", "apiVersion": "kind.x-k8s.io/v1alpha4"}
	if presetPath != "" {
		data, err := os.ReadFile(presetPath)
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", presetPath, err)
		}
	}
	networking, _ := cfg["networking"].(map[string]any)
	if networking == nil {
		networking = map[string]any{}
	}
	networking["apiServerAddress"] = addr
	networking["apiServerPort"] = port
	cfg["networking"] = networking
	return yaml.Marshal(cfg)
}

// PresetAddressAndPort reads what a preset says about the API server, so a
// loopback address can be reported and a fixed port honoured.
func PresetAddressAndPort(presetPath string) (addr string, port int, err error) {
	data, err := os.ReadFile(presetPath)
	if err != nil {
		return "", 0, err
	}
	var cfg struct {
		Networking struct {
			APIServerAddress string `json:"apiServerAddress"`
			APIServerPort    int    `json:"apiServerPort"`
		} `json:"networking"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", 0, fmt.Errorf("%s: %w", presetPath, err)
	}
	return cfg.Networking.APIServerAddress, cfg.Networking.APIServerPort, nil
}

// IsLoopback says whether a preset's address would bind the API server to the
// engine's own loopback, which nothing else can reach.
func IsLoopback(addr string) bool {
	return addr == "localhost" || addr == "::1" || strings.HasPrefix(addr, "127.")
}

var portCollision = regexp.MustCompile(`(?i)already allocated|address already in use`)

// PortCollision says whether kind's output means the chosen port was taken.
func PortCollision(output string) bool { return portCollision.MatchString(output) }

// State of a temp kubeconfig: up, gone (its engine answers without it), unknown
// (the engine does not answer), or other (not made by kcx temp create).
type State struct {
	Name, Engine, State, File string
}

// States reports every temp file's state, asking each engine once.
func States(e Engine, tempDir string) []State {
	var out []State
	asked := map[string][]string{}
	failed := map[string]bool{}
	for _, f := range layout.DirFiles(tempDir) {
		name, engine := layout.SplitTemp(f)
		if engine == "" {
			out = append(out, State{name, "-", "other", f})
			continue
		}
		if _, done := asked[engine]; !done && !failed[engine] {
			list, err := e.Clusters(engine)
			if err != nil {
				failed[engine] = true
			} else {
				asked[engine] = list
			}
		}
		switch {
		case failed[engine]:
			out = append(out, State{name, engine, "unknown", f})
		case contains(asked[engine], name):
			out = append(out, State{name, engine, "up", f})
		default:
			out = append(out, State{name, engine, "gone", f})
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// FindFile returns the temp file of cluster name (NAME@ENGINE.yaml or NAME.yaml),
// "" when none, and an error when two exist.
func FindFile(tempDir, name string) (string, error) {
	found := ""
	for _, f := range layout.DirFiles(tempDir) {
		if n, _ := layout.SplitTemp(f); n != name {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("two kubeconfigs for temp cluster %s: %s %s", name, found, f)
		}
		found = f
	}
	return found, nil
}
