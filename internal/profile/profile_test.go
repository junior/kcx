package profile

import (
	"errors"
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
label    = "Production"
provider = "acme"
color    = "red"
readonly = true

[group.lab]
provider = "acme"

[cluster.prod1]
group   = "production"
site    = "austin"
aliases = ["main"]

[cluster.office2-lab]
group = "lab"
site  = "bastrop"

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
	// Names and groups come file by file, in the order written, not sorted.
	if strings.Join(set.Names(), ",") != "z,prod1,office2-lab,odd-one" {
		t.Fatalf("names: %v", set.Names())
	}
	if strings.Join(set.GroupNames(), ",") != "g,production,lab" {
		t.Fatalf("groups: %v", set.GroupNames())
	}
	if set.Groups["production"].Display() != "Production" || set.Groups["lab"].Display() != "lab" {
		t.Fatal("a group shows its label, or its name without one")
	}
	c, ok := set.Lookup("main")
	if !ok || c.Name != "prod1" {
		t.Fatal("alias lookup")
	}
	if !set.Readonly(c) {
		t.Fatal("prod1 inherits readonly from production")
	}
	if r, _ := set.Lookup("office2-lab"); set.Readonly(r) {
		t.Fatal("office2-lab is not read-only")
	}
	if o, _ := set.Lookup("odd-one"); !set.Readonly(o) {
		t.Fatal("odd-one sets readonly itself")
	}
	if strings.Join(set.InGroup("lab"), ",") != "office2-lab,odd-one" {
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
	c, _ := set.Lookup("prod1")
	host, user, err := set.Resolve(c, "", env)
	if err != nil || host != "prod1.k8s.acme.example" || user != "svc-jane" {
		t.Fatalf("resolve: %q %q %v", host, user, err)
	}
	envWith := func(k string) string {
		return map[string]string{"USER": "jane", "ACME_USER": "ops-jane"}[k]
	}
	_, user, _ = set.Resolve(c, "", envWith)
	if user != "ops-jane" {
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
		"dup-cluster":      {"[provider.x]\ncommand = [\"t\"]\n[group.g]\nprovider = \"x\"\n[cluster.prod1]\ngroup = \"g\"\n", "cluster prod1 is defined in"},
		"bad-color":        {"[provider.x]\ncommand = [\"t\"]\n[group.g]\nprovider = \"x\"\ncolor = \"plaid\"\n[cluster.q]\ngroup = \"g\"\n", "color \"plaid\" is not one of"},
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
		"alias-is-cluster": {"[provider.x]\ncommand = [\"t\"]\n[group.g]\nprovider = \"x\"\n[cluster.q]\ngroup = \"g\"\naliases = [\"prod1\"]\n", "alias prod1 is also a cluster"},
		"alias-dup":        {"[provider.x]\ncommand = [\"t\"]\n[group.g]\nprovider = \"x\"\n[cluster.q]\ngroup = \"g\"\naliases = [\"main\"]\n", "alias main points at both"},
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

// An entry that names an unset or empty variable is skipped, never rendered
// without it; with none left, the error names what was missing.
func TestRenderFirstSkipsUnsetVariables(t *testing.T) {
	env := func(k string) string { return map[string]string{"USER": "jane", "EMPTY": ""}[k] }
	none := func(string) string { return "" }
	for _, c := range []struct {
		name   string
		t      Template
		getenv func(string) string
		want   string
		err    string
	}{
		{"the first set entry wins", Template{"{env.ACME_USER}", "ADM{env.USER}"}, env, "ADMjane", ""},
		{"a prefix alone is no user", Template{"{env.ACME_USER}", "ADM{env.USER}"}, none, "", "ACME_USER and USER are not set"},
		{"empty counts as unset", Template{"{env.EMPTY}"}, env, "", "EMPTY is not set"},
		{"one variable of two missing", Template{"{env.USER}-{env.ACME_USER}"}, env, "", "ACME_USER is not set"},
		{"each name once, in order", Template{"{env.A}", "x{env.A}", "{env.B}{env.C}"}, none, "", "A, B and C are not set"},
		{"a later entry without variables", Template{"svc-{env.ACME_USER}", "fallback"}, env, "fallback", ""},
		{"no getenv", Template{"{env.USER}"}, nil, "", "USER is not set"},
		{"nothing to render", Template{}, env, "", ""},
		{"an empty entry", Template{""}, env, "", ""},
	} {
		got, err := RenderFirst(c.t, Vars{Name: "n", Getenv: c.getenv})
		msg := ""
		if err != nil {
			msg = err.Error()
			var unset *UnsetError
			if !errors.As(err, &unset) {
				t.Errorf("%s: %T, want *UnsetError", c.name, err)
			}
		}
		if got != c.want || msg != c.err {
			t.Errorf("%s: %q, %q; want %q, %q", c.name, got, msg, c.want, c.err)
		}
	}
	// A typo is an error even in an entry that would be skipped.
	if _, err := RenderFirst(Template{"{env.NOPE}{usr}", "x"}, Vars{Getenv: none}); err == nil || !strings.Contains(err.Error(), "unknown placeholder {usr}") {
		t.Fatalf("typo in a skipped entry: %v", err)
	}
}

func TestResolveNamesTheMissingVariable(t *testing.T) {
	dir := t.TempDir()
	set, err := Load([]string{writeProfile(t, dir, "acme.toml", acme)})
	if err != nil {
		t.Fatal(err)
	}
	none := func(string) string { return "" }
	c, _ := set.Lookup("prod1")
	if _, _, err := set.Resolve(c, "", none); err == nil || err.Error() != "cluster prod1: user: ACME_USER and USER are not set; or name the user with -u" {
		t.Fatalf("no user: %v", err)
	}
	if _, user, err := set.Resolve(c, "svc-jane", none); err != nil || user != "svc-jane" {
		t.Fatalf("-u wins: %q %v", user, err)
	}
	set.Providers["acme"].Host = Template{"{name}.{env.ACME_DOMAIN}"}
	if _, _, err := set.Resolve(c, "svc-jane", none); err == nil || err.Error() != "cluster prod1: host: ACME_DOMAIN is not set" {
		t.Fatalf("no host: %v", err)
	}
}
