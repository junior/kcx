package cli

import (
	"crypto/md5"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/junior/kcx/internal/kubeconfig"
	"github.com/junior/kcx/internal/layout"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// brokenUser gives every user of a kubeconfig file a credential plugin that
// fails, so a request through it dies in the credential layer.
func brokenUser(t *testing.T, path string) {
	t.Helper()
	cfg, err := kubeconfig.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range cfg.AuthInfos {
		u.Token = ""
		u.Exec = &clientcmdapi.ExecConfig{APIVersion: "client.authentication.k8s.io/v1", Command: "/nonexistent/kcx-test-credential-plugin", InteractiveMode: clientcmdapi.NeverExecInteractiveMode}
	}
	if err := kubeconfig.WriteFile(path, cfg); err != nil {
		t.Fatal(err)
	}
}

// The fake platform command: reads the password on stdin, counts attempts,
// writes a kubeconfig for the right password, answers 401 for a wrong one. The
// server it writes is https://HOST with TLS verification off, so a test server
// can stand in for the API server.
const fakeCLI = `#!/bin/sh
IFS= read -r pw
host="$2"; out="$6"
printf 'attempt\n' >> "$FAKE_ATTEMPTS"
if [ "$pw" != "good" ]; then echo "error: 401 Unauthorized" >&2; exit 1; fi
cat > "$out" <<EOF
apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://$host
    insecure-skip-tls-verify: true
users:
- name: u
  user: {token: not-a-secret}
contexts:
- name: u@c
  context: {cluster: c, user: u}
EOF
`

const fakeBroker = `#!/bin/sh
cache="$FAKE_CACHE"
case "$1" in
  get) [ -s "$cache" ] || { echo "nothing cached" >&2; exit 1; }; printf '%s' "$(cat "$cache")" ;;
  refresh) IFS= read -r pw; [ -n "$pw" ] || exit 1; printf '%s' "$pw" > "$cache" ;;
  forget) rm -f "$cache" ;;
  status) [ -s "$cache" ] && { echo "cached, 22h left"; exit 0; } || { echo empty; exit 1; } ;;
esac
`

type profileFixture struct {
	env      env
	lay      layout.Layout
	dir      string
	cache    string
	attempts string
}

// withProfiles builds a sandbox with the fake command and broker on PATH, a
// cached "good" secret, and a profile of clusters a (lab), b (lab) and prod1
// (production, read-only); extraHost is a cluster whose host is the given
// address, for a test server.
func withProfiles(t *testing.T, extraHost string) *profileFixture {
	t.Helper()
	e, l := fixture(t)
	dir := t.TempDir()
	f := &profileFixture{env: e, lay: l, dir: dir, cache: filepath.Join(dir, "cache"), attempts: filepath.Join(dir, "attempts")}
	for name, content := range map[string]string{"fake-cli": fakeCLI, "kcx-secret-fake": fakeBroker} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_CACHE", f.cache)
	t.Setenv("FAKE_ATTEMPTS", f.attempts)
	if err := os.WriteFile(f.cache, []byte("good"), 0o600); err != nil {
		t.Fatal(err)
	}
	extra := ""
	if extraHost != "" {
		extra = "[cluster.live]\ngroup = \"lab\"\nhost = \"" + extraHost + "\"\n"
	}
	profiles := filepath.Join(dir, "profiles")
	if err := os.MkdirAll(profiles, 0o700); err != nil {
		t.Fatal(err)
	}
	content := `
[provider.acme]
command  = ["fake-cli", "get-kubeconfig", "{host}", "-u", "{user}", "-c", "{out}"]
secret   = "fake"
host     = "{name}.example"
user     = ["{env.ACME_USER}", "svc-{env.USER}"]
rejected = "401"
[group.lab]
provider = "acme"
[group.production]
provider = "acme"
readonly = true
color = "red"
[cluster.a]
group = "lab"
aliases = ["aa"]
[cluster.b]
group = "lab"
[cluster.prod1]
group = "production"
` + extra
	if err := os.WriteFile(filepath.Join(profiles, "acme.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f.env["KCX_PROFILES"] = profiles
	f.env["USER"] = "jane"
	return f
}

func (f *profileFixture) attemptsAre(t *testing.T, n int) {
	t.Helper()
	data, _ := os.ReadFile(f.attempts)
	if got := strings.Count(string(data), "attempt"); got != n {
		t.Fatalf("%d attempts, want %d", got, n)
	}
}

func TestFetchCommands(t *testing.T) {
	f := withProfiles(t, "")
	e, l := f.env, f.lay
	code, out, errs := runKcx(t, e, "fetch", "a")
	if code != 0 || !strings.Contains(out, "kcx: fetched a ("+l.PermFile("a")+")") {
		t.Fatalf("fetch a: %d %q %q", code, out, errs)
	}
	if !strings.Contains(errs, "fetching a (a.example, user svc-jane)") {
		t.Fatalf("no fetch line on stderr: %q", errs)
	}
	cfg, _ := kubeconfig.LoadFile(l.PermFile("a"))
	if _, ok := cfg.Contexts["a"]; !ok || cfg.Clusters["a"].Server != "https://a.example" || cfg.CurrentContext != "" {
		t.Fatalf("imported file: %+v", cfg)
	}
	f.attemptsAre(t, 1)
	// A refresh replaces the file in place, nothing goes to the Trash.
	if code, _, errs := runKcx(t, e, "fetch", "a"); code != 0 {
		t.Fatalf("fetch again: %d %q", code, errs)
	}
	f.attemptsAre(t, 2)
	if entries, _ := os.ReadDir(e["KCX_TRASH_DIR"]); len(entries) != 0 {
		t.Fatal("a refresh must not use the Trash")
	}
	// --missing fetches b and prod1 but not a; --all then refreshes the three.
	code, out, _ = runKcx(t, e, "fetch", "--missing")
	if code != 0 || !strings.Contains(out, "fetched b (") || !strings.Contains(out, "fetched prod1 (") || strings.Contains(out, "fetched a (") || !strings.Contains(out, "fetched 2 of 2") {
		t.Fatalf("fetch --missing: %d %q", code, out)
	}
	f.attemptsAre(t, 4)
	code, out, _ = runKcx(t, e, "fetch", "--all")
	if code != 0 || !strings.Contains(out, "fetched 3 of 3") {
		t.Fatalf("fetch --all: %d %q", code, out)
	}
	code, out, _ = runKcx(t, e, "fetch", "--group", "production")
	if code != 0 || !strings.Contains(out, "fetched prod1 (") || strings.Contains(out, "fetched a (") {
		t.Fatalf("fetch --group: %d %q", code, out)
	}
	if code, _, errs := runKcx(t, e, "fetch", "nope"); code != 1 || !strings.Contains(errs, "no cluster named nope in the profiles") {
		t.Fatalf("fetch unknown: %d %q", code, errs)
	}
	if code, _, _ := runKcx(t, e, "fetch"); code != 2 {
		t.Fatal("fetch without arguments is a usage error")
	}
	if code, _, _ := runKcx(t, e, "fetch", "a", "b", "--use"); code != 2 {
		t.Fatal("--use with two clusters is a usage error")
	}
	// The alias works, and -u overrides the user.
	code, _, errs = runKcx(t, e, "fetch", "aa", "-u", "root", "--use")
	if code != 0 || !strings.Contains(errs, "user root") {
		t.Fatalf("fetch alias -u --use: %d %q", code, errs)
	}
	if cur, _ := (kubeconfig.Store{Files: l.Files()}).Current(); cur != "a" {
		t.Fatalf("--use did not switch: %q", cur)
	}
	// The current context survives a refresh of everything.
	if code, _, _ := runKcx(t, e, "fetch", "--all"); code != 0 {
		t.Fatal("fetch --all")
	}
	if cur, _ := (kubeconfig.Store{Files: l.Files()}).Current(); cur != "a" {
		t.Fatalf("fetch --all changed the current context: %q", cur)
	}
	// A refused secret, non-interactive: one attempt, the cache forgotten, the rest aborted.
	if err := os.WriteFile(f.cache, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(f.attempts)
	code, _, errs = runKcx(t, e, "fetch", "--all")
	after, _ := os.ReadFile(f.attempts)
	if code != 1 || strings.Count(string(after), "attempt")-strings.Count(string(before), "attempt") != 1 || !strings.Contains(errs, "the secret was refused") || !strings.Contains(errs, "aborting the remaining fetches") {
		t.Fatalf("refused fetch --all: %d %q", code, errs)
	}
	if _, err := os.Stat(f.cache); !os.IsNotExist(err) {
		t.Fatal("the refused secret stayed cached")
	}
	// Nothing cached, non-interactive: no attempt at all.
	before, _ = os.ReadFile(f.attempts)
	code, _, errs = runKcx(t, e, "fetch", "a")
	after, _ = os.ReadFile(f.attempts)
	if code != 1 || string(before) != string(after) || !strings.Contains(errs, "run kcx secret refresh fake from a terminal") {
		t.Fatalf("fetch without a secret: %d %q", code, errs)
	}
}

func TestSwitchFetchesFromProfile(t *testing.T) {
	f := withProfiles(t, "")
	e, l := f.env, f.lay
	// b exists only in the profile: kcx b fetches, then switches; the probe does
	// not run for a cluster that was just fetched.
	code, out, errs := runKcx(t, e, "b")
	if code != 0 || !strings.Contains(out, "fetched b (") || !strings.Contains(out, "switched to b") {
		t.Fatalf("kcx b: %d %q %q", code, out, errs)
	}
	f.attemptsAre(t, 1)
	// prod1 is read-only by default: the switch warns.
	code, out, errs = runKcx(t, e, "prod1")
	if code != 0 || !strings.Contains(out, "switched to prod1") || !strings.Contains(errs, "prod1 is read-only by default (group production)") {
		t.Fatalf("kcx prod1: %d %q %q", code, out, errs)
	}
	// A second kcx b: the file exists, the probe fails (a.example does not
	// resolve), and the switch happens anyway with a warning; no fetch.
	code, out, errs = runKcx(t, e, "b")
	if code != 0 || !strings.Contains(out, "switched to b") || !strings.Contains(errs, "did not answer the probe") || strings.Contains(out, "fetched") {
		t.Fatalf("kcx b again: %d %q %q", code, out, errs)
	}
	f.attemptsAre(t, 2)
	// --refresh fetches regardless; --no-probe skips the probe.
	code, out, errs = runKcx(t, e, "b", "--refresh")
	if code != 0 || !strings.Contains(out, "fetched b (") {
		t.Fatalf("kcx b --refresh: %d %q %q", code, out, errs)
	}
	f.attemptsAre(t, 3)
	code, _, errs = runKcx(t, e, "b", "--no-probe")
	if code != 0 || strings.Contains(errs, "probe") {
		t.Fatalf("kcx b --no-probe: %d %q", code, errs)
	}
	// The credential layer failing (a refresh token the platform revoked, a
	// credential plugin that failed) is what a fresh fetch repairs, as a 401 is.
	brokenUser(t, l.PermFile("b"))
	code, out, errs = runKcx(t, e, "b")
	if code != 0 || !strings.Contains(out, "the credentials of b failed (getting credentials:") || !strings.Contains(out, "fetched b (") || !strings.Contains(out, "switched to b") {
		t.Fatalf("kcx b with a failing credential plugin: %d %q %q", code, out, errs)
	}
	f.attemptsAre(t, 4)
	// When the fetch fails too, the error reports both failures.
	brokenUser(t, l.PermFile("b"))
	if err := os.WriteFile(f.cache, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errs = runKcx(t, e, "b")
	if code != 1 || !strings.Contains(errs, "b: the credentials failed (getting credentials:") || !strings.Contains(errs, "and fetching again failed:") {
		t.Fatalf("kcx b with both failing: %d %q", code, errs)
	}
	if err := os.WriteFile(f.cache, []byte("good"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out, errs := runKcx(t, e, "b"); code != 0 || !strings.Contains(out, "fetched b (") {
		t.Fatalf("kcx b after the secret is back: %d %q %q", code, out, errs)
	}
	// A plain cluster (alpha, not in any profile) switches with no probe and no fetch.
	code, out, errs = runKcx(t, e, "alpha")
	if code != 0 || out != "kcx: switched to alpha\n" || errs != "" {
		t.Fatalf("kcx alpha: %d %q %q", code, out, errs)
	}
	// probe = false in the settings: no probe for profile clusters either.
	if err := os.MkdirAll(filepath.Dir(l.Config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Config, []byte("[settings]\nprobe = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errs = runKcx(t, e, "b")
	if code != 0 || strings.Contains(errs, "probe") {
		t.Fatalf("probe = false: %d %q", code, errs)
	}
	// lab_node = true refuses a fetch.
	if err := os.WriteFile(l.Config, []byte("[settings]\nlab_node = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runKcx(t, e, "fetch", "a"); code != 1 || !strings.Contains(errs, "lab node") {
		t.Fatalf("lab_node: %d %q", code, errs)
	}
	// A broken settings file is reported, not ignored.
	if err := os.WriteFile(l.Config, []byte("[settings]\nlab_nod = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runKcx(t, e, "fetch", "a"); code != 1 || !strings.Contains(errs, "unknown key") {
		t.Fatalf("broken settings: %d %q", code, errs)
	}
}

func TestProbeTriggersFetch(t *testing.T) {
	// A server that refuses the token the fake command writes: the probe answers
	// 401 and kcx fetches again before switching.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	f := withProfiles(t, strings.TrimPrefix(srv.URL, "https://"))
	e := f.env
	if code, _, errs := runKcx(t, e, "fetch", "live"); code != 0 {
		t.Fatalf("fetch live: %d %q", code, errs)
	}
	f.attemptsAre(t, 1)
	code, out, errs := runKcx(t, e, "live")
	if code != 0 || !strings.Contains(out, "the credentials of live no longer work; fetching") || !strings.Contains(out, "switched to live") {
		t.Fatalf("kcx live: %d %q %q", code, out, errs)
	}
	f.attemptsAre(t, 2)
}

func TestNamespaces(t *testing.T) {
	calls := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/api/v1/namespaces":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"metadata":{"name":"kube-system"}},{"metadata":{"name":"team-a"}}]}`))
		case "/api/v1/namespaces/team-a":
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	f := withProfiles(t, strings.TrimPrefix(srv.URL, "https://"))
	e, l := f.env, f.lay
	if code, _, errs := runKcx(t, e, "live", "--no-probe"); code != 0 {
		t.Fatalf("switch to live: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, e, "ns", "-c"); code != 0 || errs != "" {
		t.Fatalf("ns -c: %d %q", code, errs)
	}
	code, out, _ := runKcx(t, e, "ns", "-c")
	if code != 0 || out != "default\n" {
		t.Fatalf("ns -c default: %d %q", code, out)
	}
	code, out, _ = runKcx(t, e, "ns")
	if code != 0 || out != "kube-system\nteam-a\n" {
		t.Fatalf("ns list: %d %q", code, out)
	}
	state := checksum(t, l.State)
	code, out, errs := runKcx(t, e, "ns", "team-a")
	if code != 0 || out != "kcx: namespace of live is team-a\n" {
		t.Fatalf("ns team-a: %d %q %q", code, out, errs)
	}
	if checksum(t, l.State) != state {
		t.Fatal("ns wrote the state file")
	}
	cfg, _ := kubeconfig.LoadFile(l.PermFile("live"))
	if cfg.Contexts["live"].Namespace != "team-a" || cfg.CurrentContext != "" {
		t.Fatalf("namespace not in the context's file, or a current-context appeared: %+v", cfg.Contexts["live"])
	}
	if code, _, errs := runKcx(t, e, "ns", "nope"); code != 1 || !strings.Contains(errs, "no namespace nope in live; --force sets it anyway") {
		t.Fatalf("ns nope: %d %q", code, errs)
	}
	if code, _, _ := runKcx(t, e, "ns", "forced", "--force"); code != 0 {
		t.Fatal("ns --force")
	}
	code, out, _ = runKcx(t, e, "ns", "-")
	if code != 0 || out != "kcx: namespace of live is team-a\n" {
		t.Fatalf("ns -: %d %q", code, out)
	}
	code, out, _ = runKcx(t, e, "ns", "-c")
	if code != 0 || out != "team-a\n" {
		t.Fatalf("ns -c after -: %d %q", code, out)
	}
	if code, _, _ := runKcx(t, e, "ns", "a", "b"); code != 2 {
		t.Fatal("two targets is a usage error")
	}
	// A context without a server answer: the check fails and says so.
	if code, _, _ := runKcx(t, e, "alpha"); code != 0 {
		t.Fatal("switch alpha")
	}
	if code, _, errs := runKcx(t, e, "ns", "x"); code != 1 || !strings.Contains(errs, "--force sets it anyway") {
		t.Fatalf("ns on an unreachable cluster: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, e, "ns", "-"); code != 1 || !strings.Contains(errs, "no previous namespace recorded for alpha") {
		t.Fatalf("ns - without previous: %d %q", code, errs)
	}
	// Inside a read-only shell, ns writes the read-only kubeconfig and nothing else.
	ro := filepath.Join(t.TempDir(), "readonly.yaml")
	writeKubeconfig(t, ro, "live (READ-ONLY)", "readonly", "readonly", "http://127.0.0.1:1", "live (READ-ONLY)")
	roEnv := env{"HOME": e["HOME"], EnvRO: "live (READ-ONLY)", EnvROConfig: ro, "KUBECONFIG": ro}
	code, out, errs = runKcx(t, roEnv, "ns", "team-ro")
	if code != 0 || out != "kcx: namespace of live (READ-ONLY) is team-ro\n" {
		t.Fatalf("ns in RO: %d %q %q", code, out, errs)
	}
	roCfg, _ := kubeconfig.LoadFile(ro)
	if roCfg.Contexts["live (READ-ONLY)"].Namespace != "team-ro" {
		t.Fatal("ns in RO did not write the read-only file")
	}
	if _, err := os.Stat(filepath.Join(l.NSPrev, "live (READ-ONLY)")); !os.IsNotExist(err) {
		t.Fatal("ns in RO must not record a previous namespace")
	}
	_ = calls
}

func TestProfileAndSecretCommands(t *testing.T) {
	f := withProfiles(t, "")
	e := f.env
	code, out, _ := runKcx(t, e, "profile", "ls")
	if code != 0 || !strings.Contains(out, "NAME   GROUP       SITE  PROVIDER  FETCHED  READ-ONLY") || !strings.Contains(out, "prod1  production  -     acme      -        yes") {
		t.Fatalf("profile ls:\n%s", out)
	}
	if code, _, _ := runKcx(t, e, "fetch", "a"); code != 0 {
		t.Fatal("fetch a")
	}
	code, out, _ = runKcx(t, e, "profile", "ls")
	if code != 0 || !strings.Contains(out, "a      lab         -     acme      yes      -") {
		t.Fatalf("profile ls after fetch:\n%s", out)
	}
	code, out, _ = runKcx(t, e, "profile", "check")
	if code != 0 || !strings.Contains(out, "ok: 3 cluster(s), 2 group(s), 1 provider(s) in 1 file(s)") {
		t.Fatalf("profile check: %d %q", code, out)
	}
	code, out, _ = runKcx(t, e, "secret", "status")
	if code != 0 || out != "fake: cached, 22h left\n" {
		t.Fatalf("secret status: %d %q", code, out)
	}
	code, out, _ = runKcx(t, e, "secret", "forget")
	if code != 0 || out != "kcx: fake: forgotten\n" {
		t.Fatalf("secret forget: %d %q", code, out)
	}
	code, out, _ = runKcx(t, e, "secret", "status", "fake")
	if code != 0 || out != "fake: empty\n" {
		t.Fatalf("secret status after forget: %d %q", code, out)
	}
	var outb, errb strings.Builder
	if code := Main([]string{"secret", "refresh"}, strings.NewReader("good\n"), &outb, &errb, e.get); code != 0 {
		t.Fatalf("secret refresh: %d %s", code, errb.String())
	}
	data, _ := os.ReadFile(f.cache)
	if string(data) != "good" {
		t.Fatalf("refresh did not cache: %q", data)
	}
	if code, _, _ := runKcx(t, e, "secret", "get"); code != 2 {
		t.Fatal("secret get is never a kcx command")
	}
	if code, _, errs := runKcx(t, e, "secret", "status", "ghost"); code != 1 || !strings.Contains(errs, "neither kcx-secret-ghost nor ghost") {
		t.Fatalf("secret status ghost: %d %q", code, errs)
	}
	// A broken profile stops every profile command with the file named.
	if err := os.WriteFile(filepath.Join(e["KCX_PROFILES"], "zz-broken.toml"), []byte("[cluster.q]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runKcx(t, e, "profile", "check"); code != 1 || !strings.Contains(errs, "zz-broken.toml") {
		t.Fatalf("broken profile: %d %q", code, errs)
	}
	// Without profiles, fetch says so, and a plain switch still works.
	bare := env{"HOME": e["HOME"], "KCX_PROFILES": t.TempDir()}
	if code, _, errs := runKcx(t, bare, "fetch", "a"); code != 1 || !strings.Contains(errs, "no profiles in") {
		t.Fatalf("fetch without profiles: %d %q", code, errs)
	}
	if code, _, _ := runKcx(t, bare, "alpha"); code != 0 {
		t.Fatal("switch without profiles")
	}
	_ = md5.Sum
}
