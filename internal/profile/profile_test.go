package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const acme = `
[provider.acme]
command  = ["acme", "get-kubeconfig", "{host}", "-u", "{user}", "-c", "{out}"]
secret   = "acme-secret"
host     = "{name}.k8s.acme.example"
user     = ["{env.ACME_USER}", "svc-{env.USER}"]
rejected = "401|access_denied"
timeout  = "90s"

[group.production]
provider = "acme"
color    = "red"
readonly = true

[group.lab]
provider = "acme"

[cluster.p1]
group   = "production"
site    = "austin"
aliases = ["prod1"]

[cluster.r4-lab]
group = "lab"
site  = "round-rock"

[cluster.odd-one]
group    = "lab"
host     = "k8s.odd.acme.example"
readonly = true
`

func writeProfile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFilesAndLoad(t *testing.T) {
	dir := t.TempDir()
	writeProfile(t, dir, "b.toml", acme)
	writeProfile(t, dir, "a.toml", "[provider.other]\ncommand = [\"true\"]\n[group.g]\nprovider = \"other\"\n[cluster.z]\ngroup = \"g\"\n")
	writeProfile(t, dir, ".hidden.toml", "[cluster.hidden]\n")
	writeProfile(t, dir, "notes.txt", "x")
	files, err := Files([]string{dir, filepath.Join(dir, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || !strings.HasSuffix(files[0], "a.toml") || !strings.HasSuffix(files[1], "b.toml") {
		t.Fatalf("files: %v", files)
	}
	set, err := Load(files)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(set.Names(), ",") != "odd-one,p1,r4-lab,z" {
		t.Fatalf("names: %v", set.Names())
	}
	c, ok := set.Lookup("prod1")
	if !ok || c.Name != "p1" {
		t.Fatal("alias lookup")
	}
	if !set.Readonly(c) {
		t.Fatal("p1 inherits readonly from production")
	}
	if r, _ := set.Lookup("r4-lab"); set.Readonly(r) {
		t.Fatal("r4-lab is not read-only")
	}
	if o, _ := set.Lookup("odd-one"); !set.Readonly(o) {
		t.Fatal("odd-one sets readonly itself")
	}
	if strings.Join(set.InGroup("lab"), ",") != "odd-one,r4-lab" {
		t.Fatalf("InGroup: %v", set.InGroup("lab"))
	}
	p := set.ProviderOf(c)
	if p.Name != "acme" || p.Kind != "command" || p.RejectedRE() == nil || p.TimeoutDuration().Seconds() != 90 {
		t.Fatalf("provider: %+v", p)
	}
	if !p.RejectedRE().MatchString("HTTP 401 Unauthorized") {
		t.Fatal("rejected pattern")
	}
}

func TestResolveAndCommand(t *testing.T) {
	dir := t.TempDir()
	set, err := Load([]string{writeProfile(t, dir, "acme.toml", acme)})
	if err != nil {
		t.Fatal(err)
	}
	env := func(k string) string {
		return map[string]string{"USER": "jane"}[k]
	}
	c, _ := set.Lookup("p1")
	host, user, err := set.Resolve(c, "", env)
	if err != nil || host != "p1.k8s.acme.example" || user != "svc-jane" {
		t.Fatalf("resolve: %q %q %v", host, user, err)
	}
	envWith := func(k string) string {
		return map[string]string{"USER": "jane", "ACME_USER": "adm-jane"}[k]
	}
	_, user, _ = set.Resolve(c, "", envWith)
	if user != "adm-jane" {
		t.Fatalf("the first non-empty template wins: %q", user)
	}
	_, user, _ = set.Resolve(c, "someone", envWith)
	if user != "someone" {
		t.Fatalf("-u wins: %q", user)
	}
	odd, _ := set.Lookup("odd-one")
	host, _, _ = set.Resolve(odd, "", env)
	if host != "k8s.odd.acme.example" {
		t.Fatalf("the cluster's host wins over the provider's template: %q", host)
	}
	args, err := set.Command(c, host, "svc-jane", "/tmp/out", env)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "acme get-kubeconfig k8s.odd.acme.example -u svc-jane -c /tmp/out" {
		t.Fatalf("command: %v", args)
	}
	if _, err := Render("{nope}", Vars{}); err == nil || !strings.Contains(err.Error(), "unknown placeholder {nope}") {
		t.Fatalf("unknown placeholder: %v", err)
	}
	if s, _ := Render("a{env.MISSING}b", Vars{Getenv: env}); s != "ab" {
		t.Fatalf("missing env renders empty: %q", s)
	}
}

func TestLoadRefusesMistakes(t *testing.T) {
	dir := t.TempDir()
	good := writeProfile(t, dir, "good.toml", acme)
	cases := map[string]struct{ content, want string }{
		"dup-cluster":      {"[provider.x]\ncommand = [\"t\"]\n[group.g]\nprovider = \"x\"\n[cluster.p1]\ngroup = \"g\"\n", "cluster p1 is defined in"},
		"dup-provider":     {"[provider.acme]\ncommand = [\"t\"]\n", "provider acme is defined in"},
		"unknown-key":      {"[provider.y]\ncommand = [\"t\"]\ncolour = \"red\"\n", "unknown key(s): provider.y.colour"},
		"no-group":         {"[cluster.lonely]\n", "has no group"},
		"missing-group":    {"[cluster.q]\ngroup = \"nope\"\n", "names group nope, which no profile defines"},
		"no-provider":      {"[group.bare]\n[cluster.q]\ngroup = \"bare\"\n", "has no provider"},
		"bad-provider":     {"[group.g2]\nprovider = \"ghost\"\n", "names provider ghost, which no profile defines"},
		"bad-kind":         {"[provider.k]\nkind = \"magic\"\ncommand = [\"t\"]\n", "kind must be command or oidc"},
		"no-command":       {"[provider.k]\n", "needs a command"},
		"bad-regexp":       {"[provider.k]\ncommand = [\"t\"]\nrejected = \"(\"\n", "rejected:"},
		"bad-timeout":      {"[provider.k]\ncommand = [\"t\"]\ntimeout = \"soon\"\n", "is not a duration"},
		"bad-name":         {"[provider.x]\ncommand = [\"t\"]\n[group.g]\nprovider = \"x\"\n[cluster.\"bad/name\"]\ngroup = \"g\"\n", "a name takes letters"},
		"alias-is-cluster": {"[provider.x]\ncommand = [\"t\"]\n[group.g]\nprovider = \"x\"\n[cluster.q]\ngroup = \"g\"\naliases = [\"p1\"]\n", "alias p1 is also a cluster"},
		"alias-dup":        {"[provider.x]\ncommand = [\"t\"]\n[group.g]\nprovider = \"x\"\n[cluster.q]\ngroup = \"g\"\naliases = [\"prod1\"]\n", "alias prod1 points at both"},
		"template-type":    {"[provider.k]\ncommand = [\"t\"]\nhost = 3\n", "a template is a string or a list of strings"},
		"syntax":           {"[provider.k\n", "zz-syntax.toml"},
	}
	for name, tc := range cases {
		bad := writeProfile(t, filepath.Join(dir, name), "zz-"+name+".toml", tc.content)
		_, err := Load([]string{good, bad})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: expected %q, got %v", name, tc.want, err)
		}
	}
	if _, err := Load([]string{good, filepath.Join(dir, "missing.toml")}); err == nil {
		t.Error("a named file that is missing must be an error")
	}
	// No profiles at all is a valid, empty set.
	set, err := Load(nil)
	if err != nil || len(set.Names()) != 0 {
		t.Fatalf("empty: %v %v", set, err)
	}
	if _, ok := set.Lookup("anything"); ok {
		t.Fatal("empty set found something")
	}
}
