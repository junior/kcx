package fetch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/junior/kcx/v2/internal/profile"
)

// fakeCLI stands in for a platform's get-kubeconfig command: it reads the
// password from standard input, records every call (arguments, environment,
// password) in files the test inspects, and answers like the real thing: a
// kubeconfig written to the -c file for the right password, "401 Unauthorized"
// for the wrong one, a network error for host net.example, a hang for
// slow.example.
const fakeCLI = `#!/bin/sh
IFS= read -r pw
host="$2"; out="$6"
printf '%s\n' "$pw" >> "$FAKE_LOG"
printf 'attempt\n' >> "$FAKE_ATTEMPTS"
env > "$FAKE_ENV"
printf '%s\n' "$@" > "$FAKE_ARGS"
case "$host" in
  net.example) echo "dial tcp: i/o timeout" >&2; exit 1 ;;
  slow.example) sleep 30; exit 1 ;;
  nofile.example) exit 0 ;;
esac
if [ "$pw" != "good" ]; then echo "error: 401 Unauthorized" >&2; exit 1; fi
cat > "$out" <<EOF
apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: https://$host}
users:
- name: u
  user: {token: not-a-secret}
contexts:
- name: c
  context: {cluster: c, user: u}
EOF
`

// fakeBroker is the four-verb secret provider over a cache file.
const fakeBroker = `#!/bin/sh
[ -n "${FAKE_UNAVAILABLE-}" ] && exit 3
cache="$FAKE_CACHE"
case "$1" in
  get) [ -s "$cache" ] || { echo "nothing cached" >&2; exit 1; }; printf '%s' "$(cat "$cache")" ;;
  refresh) [ -n "${FAKE_REFRESHES-}" ] && printf 'refresh\n' >> "$FAKE_REFRESHES"; [ -n "${FAKE_REFRESH_DELAY-}" ] && sleep "$FAKE_REFRESH_DELAY"; IFS= read -r pw; [ -n "$pw" ] || exit 1; printf '%s' "$pw" > "$cache"; printf 'refreshed\n' >> "$FAKE_LOG" ;;
  forget) rm -f "$cache"; printf 'forgot\n' >> "$FAKE_LOG" ;;
  status) [ -s "$cache" ] && { echo cached; exit 0; } || { echo empty; exit 1; } ;;
esac
`

type harness struct {
	dir, cache, log, attempts, envFile, argsFile string
	set                                          *profile.Set
	out                                          bytes.Buffer
	errb                                         bytes.Buffer
}

func newHarness(t *testing.T, timeout string) *harness {
	t.Helper()
	needsPOSIX(t)
	dir := t.TempDir()
	h := &harness{dir: dir, cache: filepath.Join(dir, "cache"), log: filepath.Join(dir, "log"),
		attempts: filepath.Join(dir, "attempts"), envFile: filepath.Join(dir, "env"), argsFile: filepath.Join(dir, "args")}
	cli := filepath.Join(dir, "fake-cli")
	if err := os.WriteFile(cli, []byte(fakeCLI), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kcx-secret-fake"), []byte(fakeBroker), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CACHE", h.cache)
	t.Setenv("FAKE_LOG", h.log)
	t.Setenv("FAKE_ATTEMPTS", h.attempts)
	t.Setenv("FAKE_ENV", h.envFile)
	t.Setenv("FAKE_ARGS", h.argsFile)
	prof := filepath.Join(dir, "acme.toml")
	content := `
[provider.acme]
command  = ["` + cli + `", "get-kubeconfig", "{host}", "-u", "{user}", "-c", "{out}"]
secret   = "fake"
host     = "{name}.example"
user     = "svc-{env.USER}"
rejected = "401|access_denied"
` + func() string {
		if timeout != "" {
			return "timeout = \"" + timeout + "\"\n"
		}
		return ""
	}() + `
[provider.nosecret]
command  = ["` + cli + `", "get-kubeconfig", "{host}", "-u", "{user}", "-c", "{out}"]
host     = "{name}.example"
user     = "nobody"
[group.lab]
provider = "acme"
[group.open]
provider = "nosecret"
[cluster.a]
group = "lab"
[cluster.b]
group = "lab"
[cluster.c]
group = "lab"
[cluster.net]
group = "lab"
[cluster.slow]
group = "lab"
[cluster.nofile]
group = "lab"
[cluster.free]
group = "open"
`
	if err := os.WriteFile(prof, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := profile.Load([]string{prof})
	if err != nil {
		t.Fatal(err)
	}
	h.set = set
	return h
}

func (h *harness) runner(interactive bool, stdin string) *Runner {
	return &Runner{
		Stdin: strings.NewReader(stdin), Stdout: &h.out, Stderr: &h.errb,
		Interactive: interactive, SecretCache: true,
		Getenv: func(k string) string {
			return map[string]string{"USER": "jane"}[k]
		},
		LookPath: func(n string) (string, error) {
			p := filepath.Join(h.dir, n)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
			return "", os.ErrNotExist
		},
		Environ: []string{"PATH=" + os.Getenv("PATH"), "FAKE_CACHE=" + h.cache, "FAKE_LOG=" + h.log,
			"FAKE_ATTEMPTS=" + h.attempts, "FAKE_ENV=" + h.envFile, "FAKE_ARGS=" + h.argsFile, "KUBECONFIG=/real/files"},
	}
}

func (h *harness) cacheIs(t *testing.T, want string) {
	t.Helper()
	data, _ := os.ReadFile(h.cache)
	if string(data) != want {
		t.Fatalf("cache holds %q, want %q", data, want)
	}
}

func (h *harness) attemptsAre(t *testing.T, n int) {
	t.Helper()
	data, _ := os.ReadFile(h.attempts)
	if got := strings.Count(string(data), "attempt"); got != n {
		t.Fatalf("%d login attempts, want %d (log: %q)", got, n, data)
	}
}

func (h *harness) logHas(t *testing.T, s string) bool {
	t.Helper()
	data, _ := os.ReadFile(h.log)
	return strings.Contains(string(data), s)
}

func (h *harness) req(name string) Request {
	c, _ := h.set.Lookup(name)
	return Request{Set: h.set, Cluster: c, Out: filepath.Join(h.dir, name+".out")}
}

func TestCachedGood(t *testing.T) {
	h := newHarness(t, "")
	_ = os.WriteFile(h.cache, []byte("good"), 0o600)
	if err := h.runner(false, "").Fetch(context.Background(), h.req("a")); err != nil {
		t.Fatal(err)
	}
	h.attemptsAre(t, 1)
	h.cacheIs(t, "good")
	data, _ := os.ReadFile(filepath.Join(h.dir, "a.out"))
	if !strings.Contains(string(data), "server: https://a.example") {
		t.Fatalf("kubeconfig not written: %q", data)
	}
	// The secret reached the command through stdin only.
	env, _ := os.ReadFile(h.envFile)
	args, _ := os.ReadFile(h.argsFile)
	if strings.Contains(string(env), "good") || strings.Contains(string(args), "good") {
		t.Fatal("the secret leaked into the environment or the arguments")
	}
	if !strings.Contains(string(env), "KUBECONFIG="+filepath.Join(h.dir, "a.out")) || !strings.Contains(string(env), "KCX_OUT=") {
		t.Fatalf("KUBECONFIG/KCX_OUT not set for the command: %s", env)
	}
	if strings.Contains(string(env), "/real/files") {
		t.Fatal("the inherited KUBECONFIG leaked into the command")
	}
	if !strings.Contains(string(args), "svc-jane") || !strings.Contains(string(args), "a.example") {
		t.Fatalf("args: %s", args)
	}
	if !h.logHas(t, "good\n") {
		t.Fatal("the command did not receive a newline-terminated secret")
	}
}

func TestNothingCached(t *testing.T) {
	h := newHarness(t, "")
	// Non-interactive: no prompt, no attempt.
	err := h.runner(false, "").Fetch(context.Background(), h.req("a"))
	if err == nil || !strings.Contains(err.Error(), "run kcx secret refresh fake from a terminal") {
		t.Fatalf("non-interactive without a secret: %v", err)
	}
	h.attemptsAre(t, 0)
	// Interactive: refresh reads the new secret, then one attempt.
	if err := h.runner(true, "good\n").Fetch(context.Background(), h.req("a")); err != nil {
		t.Fatal(err)
	}
	h.attemptsAre(t, 1)
	h.cacheIs(t, "good")
}

func TestRejectedThenRefreshed(t *testing.T) {
	h := newHarness(t, "")
	_ = os.WriteFile(h.cache, []byte("stale"), 0o600)
	// stale -> 401 -> forget -> refresh (reads good) -> retry ok: two attempts.
	if err := h.runner(true, "good\n").Fetch(context.Background(), h.req("a")); err != nil {
		t.Fatalf("refresh then retry: %v (%s)", err, h.errb.String())
	}
	h.attemptsAre(t, 2)
	h.cacheIs(t, "good")
	if !h.logHas(t, "forgot") || !h.logHas(t, "refreshed") {
		t.Fatal("forget and refresh must both have run")
	}
}

func TestRejectedTwiceStops(t *testing.T) {
	h := newHarness(t, "")
	_ = os.WriteFile(h.cache, []byte("stale"), 0o600)
	err := h.runner(true, "still-wrong\n").Fetch(context.Background(), h.req("a"))
	if err == nil || !strings.Contains(err.Error(), "the secret was refused") {
		t.Fatalf("second refusal: %v", err)
	}
	h.attemptsAre(t, 2) // never a third
	if _, err := os.Stat(h.cache); !os.IsNotExist(err) {
		t.Fatal("the refused secret must not stay cached")
	}
}

func TestRejectedNonInteractive(t *testing.T) {
	h := newHarness(t, "")
	_ = os.WriteFile(h.cache, []byte("stale"), 0o600)
	err := h.runner(false, "good\n").Fetch(context.Background(), h.req("a"))
	if err == nil || !strings.Contains(err.Error(), "the secret was refused") {
		t.Fatalf("non-interactive refusal: %v", err)
	}
	h.attemptsAre(t, 1)
	if _, err := os.Stat(h.cache); !os.IsNotExist(err) {
		t.Fatal("the refused secret must be forgotten")
	}
	if h.logHas(t, "refreshed") {
		t.Fatal("nobody was there to answer a refresh")
	}
}

func TestRefreshEmptyStops(t *testing.T) {
	h := newHarness(t, "")
	_ = os.WriteFile(h.cache, []byte("stale"), 0o600)
	err := h.runner(true, "\n").Fetch(context.Background(), h.req("a"))
	if err == nil {
		t.Fatal("an empty refresh must fail the fetch")
	}
	h.attemptsAre(t, 1)
}

func TestNetworkErrorKeepsCache(t *testing.T) {
	h := newHarness(t, "")
	_ = os.WriteFile(h.cache, []byte("good"), 0o600)
	err := h.runner(true, "").Fetch(context.Background(), h.req("net"))
	if err == nil || !strings.Contains(err.Error(), "exit 1") || strings.Contains(err.Error(), "refused") {
		t.Fatalf("network failure: %v", err)
	}
	h.attemptsAre(t, 1)
	h.cacheIs(t, "good")
}

func TestTimeoutAndNoFile(t *testing.T) {
	h := newHarness(t, "2s")
	_ = os.WriteFile(h.cache, []byte("good"), 0o600)
	// The fake hangs in a sleep of its own, which outlives the shell killed at
	// the timeout and holds its output open: kcx returns anyway, well before the
	// sleep ends.
	start := time.Now()
	err := h.runner(false, "").Fetch(context.Background(), h.req("slow"))
	if err == nil || !strings.Contains(err.Error(), "timed out after 2s") {
		t.Fatalf("timeout: %v", err)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Fatalf("the timed-out fetch took %s: something kept waiting for the hung process", took)
	}
	h.cacheIs(t, "good")
	err = h.runner(false, "").Fetch(context.Background(), h.req("nofile"))
	if err == nil || !strings.Contains(err.Error(), "wrote no kubeconfig") {
		t.Fatalf("no file: %v", err)
	}
}

// A person can take minutes to look a password up: the refresh prompt is not on
// the command's clock, which starts once the secret is there.
func TestSlowRefreshKeepsTheCommandsTime(t *testing.T) {
	h := newHarness(t, "2s")
	t.Setenv("FAKE_REFRESH_DELAY", "3")
	if err := h.runner(true, "good\n").Fetch(context.Background(), h.req("a")); err != nil {
		t.Fatalf("a refresh slower than the timeout must not fail the fetch: %v", err)
	}
	h.cacheIs(t, "good")
	h.attemptsAre(t, 1)
}

// A provider that cannot run here (exit 3) lets the command ask the person
// itself: the terminal is its standard input, no timeout, and a refusal lets
// it ask once more, never a third time. With no one there, nothing runs.
func TestUnavailableProviderLetsTheCommandAsk(t *testing.T) {
	h := newHarness(t, "1s")
	t.Setenv("FAKE_UNAVAILABLE", "1")
	r := h.runner(true, "good\n")
	r.Environ = append(r.Environ, "FAKE_UNAVAILABLE=1")
	if err := r.Fetch(context.Background(), h.req("a")); err != nil {
		t.Fatalf("the command asking itself: %v", err)
	}
	h.attemptsAre(t, 1)
	if !strings.Contains(h.errb.String(), "secret provider fake cannot run here: fake-cli asks for the secret itself") {
		t.Fatalf("no word that the command asks: %q", h.errb.String())
	}
	if log, _ := os.ReadFile(h.log); string(log) != "good\n" {
		t.Fatalf("the command read %q", log)
	}

	// Refused twice: the second ask is the last. The answers come from a file,
	// as from a terminal, one line to each run of the command.
	_ = os.Remove(h.attempts)
	answers := filepath.Join(t.TempDir(), "answers")
	if err := os.WriteFile(answers, []byte("bad\nbad\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(answers)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r = h.runner(true, "")
	r.Stdin = f
	r.Environ = append(r.Environ, "FAKE_UNAVAILABLE=1")
	err = r.Fetch(context.Background(), h.req("a"))
	if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("refused twice: %v", err)
	}
	h.attemptsAre(t, 2)
	if !strings.Contains(h.errb.String(), "fake-cli asks once more") {
		t.Fatalf("no word of the second ask: %q", h.errb.String())
	}

	// No one there: nothing runs.
	_ = os.Remove(h.attempts)
	r = h.runner(false, "")
	r.Environ = append(r.Environ, "FAKE_UNAVAILABLE=1")
	err = r.Fetch(context.Background(), h.req("a"))
	if err == nil || !strings.Contains(err.Error(), "cannot run here, and there is no terminal") {
		t.Fatalf("non-interactive: %v", err)
	}
	h.attemptsAre(t, 0)
}

func TestNoSecretProvider(t *testing.T) {
	h := newHarness(t, "")
	// The command gets an empty stdin and, in this fake, refuses. The nosecret
	// provider has no rejected pattern, so that is a plain failure, not a refusal:
	// nothing to forget, nothing to retry.
	err := h.runner(false, "").Fetch(context.Background(), h.req("free"))
	if err == nil || !strings.Contains(err.Error(), "exit 1") || strings.Contains(err.Error(), "refused") {
		t.Fatalf("no-secret provider: %v", err)
	}
	h.attemptsAre(t, 1)
}

func TestSecretCacheOff(t *testing.T) {
	h := newHarness(t, "")
	_ = os.WriteFile(h.cache, []byte("good"), 0o600)
	r := h.runner(true, "")
	r.SecretCache = false
	if err := r.Fetch(context.Background(), h.req("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.cache); !os.IsNotExist(err) {
		t.Fatal("secret_cache = false must forget after the fetch")
	}
}

// One refresh per provider in a run: after a refresh that failed, the rest of
// fetch --all fails at once instead of asking for every cluster (kob_secret
// itself refuses a second reveal soon after a failed one).
func TestOneRefreshPerRun(t *testing.T) {
	h := newHarness(t, "")
	refreshes := filepath.Join(t.TempDir(), "refreshes")
	t.Setenv("FAKE_REFRESHES", refreshes)
	var clusters []*profile.Cluster
	for _, n := range []string{"a", "b", "c"} {
		c, _ := h.set.Lookup(n)
		clusters = append(clusters, c)
	}
	// Nothing cached, someone there, and an empty answer: the refresh fails.
	done, failed, aborted := h.runner(true, "").Many(context.Background(), h.set, clusters, "", func(c *profile.Cluster) string {
		return filepath.Join(h.dir, c.Name+".out")
	}, nil)
	if aborted || len(done) != 0 || len(failed) != 3 {
		t.Fatalf("Many: done=%v failed=%v aborted=%v", done, failed, aborted)
	}
	if data, _ := os.ReadFile(refreshes); strings.Count(string(data), "refresh") != 1 {
		t.Fatalf("%q: want one refresh in the run", data)
	}
	for _, n := range []string{"b", "c"} {
		if !strings.Contains(failed[n].Error(), "refresh failed earlier in this run; run kcx secret refresh fake") {
			t.Fatalf("%s: %v", n, failed[n])
		}
	}
	h.attemptsAre(t, 0)

	// A new run asks again, and a refresh that works lets the run go on.
	_ = os.Remove(refreshes)
	done, failed, _ = h.runner(true, "good\n").Many(context.Background(), h.set, clusters, "", func(c *profile.Cluster) string {
		return filepath.Join(h.dir, c.Name+".out")
	}, nil)
	if strings.Join(done, ",") != "a,b,c" || len(failed) != 0 {
		t.Fatalf("Many after a good refresh: done=%v failed=%v", done, failed)
	}
	if data, _ := os.ReadFile(refreshes); strings.Count(string(data), "refresh") != 1 {
		t.Fatalf("%q: want one refresh, then the cache", data)
	}
}

func TestManyAbortsOnRefusal(t *testing.T) {
	h := newHarness(t, "")
	_ = os.WriteFile(h.cache, []byte("good"), 0o600)
	var clusters []*profile.Cluster
	for _, n := range []string{"a", "net", "b"} {
		c, _ := h.set.Lookup(n)
		clusters = append(clusters, c)
	}
	r := h.runner(false, "")
	done, failed, aborted := r.Many(context.Background(), h.set, clusters, "", func(c *profile.Cluster) string {
		return filepath.Join(h.dir, c.Name+".out")
	}, nil)
	if aborted || strings.Join(done, ",") != "a,b" || len(failed) != 1 || failed["net"] == nil {
		t.Fatalf("Many with a network failure: done=%v failed=%v aborted=%v", done, failed, aborted)
	}
	h.attemptsAre(t, 3)
	// Now the cache goes stale half way: the refusal aborts the rest.
	_ = os.WriteFile(h.cache, []byte("good"), 0o600)
	h2 := newHarness(t, "")
	_ = os.WriteFile(h2.cache, []byte("stale"), 0o600)
	clusters = nil
	for _, n := range []string{"a", "b", "c"} {
		c, _ := h2.set.Lookup(n)
		clusters = append(clusters, c)
	}
	done, failed, aborted = h2.runner(false, "").Many(context.Background(), h2.set, clusters, "", func(c *profile.Cluster) string {
		return filepath.Join(h2.dir, c.Name+".out")
	}, nil)
	if !aborted || len(done) != 0 || len(failed) != 1 {
		t.Fatalf("Many with a refusal: done=%v failed=%v aborted=%v", done, failed, aborted)
	}
	h2.attemptsAre(t, 1)
	if !strings.Contains(h2.errb.String(), "aborting the remaining fetches (b c)") {
		t.Fatalf("no abort message: %s", h2.errb.String())
	}
}

func TestUserOverrideAndUnknownKind(t *testing.T) {
	h := newHarness(t, "")
	_ = os.WriteFile(h.cache, []byte("good"), 0o600)
	req := h.req("a")
	req.User = "someone-else"
	if err := h.runner(false, "").Fetch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(h.argsFile)
	if !strings.Contains(string(args), "someone-else") {
		t.Fatalf("-u not applied: %s", args)
	}
	h.set.Providers["acme"].Kind = "oidc"
	if err := h.runner(false, "").Fetch(context.Background(), h.req("a")); err == nil || !strings.Contains(err.Error(), "kind oidc") {
		t.Fatalf("oidc kind must be refused in this build: %v", err)
	}
}
