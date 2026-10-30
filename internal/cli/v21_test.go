package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// kcx ns --unset drops the namespace from the context, which kubectl then reads
// as default; the old one becomes the previous namespace.
func TestNsUnset(t *testing.T) {
	e, l := fixture(t)
	if code, _, errs := runKcx(t, e, "alpha"); code != 0 {
		t.Fatalf("kcx alpha: %q", errs)
	}
	if code, _, errs := runKcx(t, e, "ns", "team", "--force"); code != 0 {
		t.Fatalf("kcx ns team: %q", errs)
	}
	code, stdout, errs := runKcx(t, e, "ns", "--unset")
	if code != 0 || stdout != "kcx: namespace of alpha unset; kubectl uses default\n" {
		t.Fatalf("ns --unset: %d %q %q", code, stdout, errs)
	}
	if namespaceIn(t, l.PermFile("alpha"), "alpha") != "" {
		t.Fatal("the namespace is still in alpha's file")
	}
	if prev, _ := os.ReadFile(filepath.Join(l.NSPrev, "alpha")); string(prev) != "team" {
		t.Fatalf("previous namespace: %q", prev)
	}
	if code, stdout, _ := runKcx(t, e, "ns", "--unset"); code != 0 || !strings.Contains(stdout, "alpha sets no namespace") {
		t.Fatalf("ns --unset twice: %d %q", code, stdout)
	}
	if code, _, _ := runKcx(t, e, "ns", "-"); code != 0 || namespaceIn(t, l.PermFile("alpha"), "alpha") != "team" {
		t.Fatalf("ns - after --unset: %d", code)
	}
	if code, stdout, _ := runKcx(t, e, "ns", "--current"); code != 0 || stdout != "team\n" {
		t.Fatalf("ns --current: %d %q", code, stdout)
	}
	if code, _, errs := runKcx(t, e, "ns", "--unset", "x"); code != 2 || !strings.Contains(errs, "takes no namespace") {
		t.Fatalf("ns --unset x: %d %q", code, errs)
	}
}

// kcx ls filters by profile group and by a substring or a glob.
func TestLsFilters(t *testing.T) {
	f := withProfiles(t, "")
	e := f.env
	for _, n := range []string{"a", "prod1"} {
		if code, _, errs := runKcx(t, e, "fetch", n); code != 0 {
			t.Fatalf("fetch %s: %q", n, errs)
		}
	}
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"-q", "--group", "lab"}, "a\n"},
		{[]string{"-q", "--group=production"}, "prod1\n"},
		{[]string{"-q", "al"}, "alpha\n"},
		{[]string{"-q", "a*"}, "a\nalpha\n"},
		{[]string{"-q", "--group", "lab", "a*"}, "a\n"},
		{[]string{"-q", "zzz"}, ""},
	}
	for _, c := range cases {
		code, stdout, errs := runKcx(t, e, append([]string{"ls"}, c.args...)...)
		if code != 0 || stdout != c.want {
			t.Errorf("ls %v: %d %q %q, want %q", c.args, code, stdout, errs, c.want)
		}
	}
	code, stdout, _ := runKcx(t, e, "ls", "--group", "lab")
	if code != 0 || strings.Count(stdout, "\n") != 1 || !strings.Contains(stdout, " a ") {
		t.Fatalf("ls --group lab: %d %q", code, stdout)
	}
	if code, _, errs := runKcx(t, e, "ls", "zzz"); code != 0 || !strings.Contains(errs, "no context matches") {
		t.Fatalf("ls zzz: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, e, "ls", "--group", "nope"); code != 1 || !strings.Contains(errs, "no group named nope") {
		t.Fatalf("ls --group nope: %d %q", code, errs)
	}
	if code, _, _ := runKcx(t, e, "ls", "["); code != 2 {
		t.Fatal("an invalid pattern is a usage error")
	}
	if code, _, _ := runKcx(t, e, "ls", "a", "b"); code != 2 {
		t.Fatal("two patterns are a usage error")
	}
	if code, stdout, _ := runKcx(t, e, "__complete", "ls", "--group"); code != 0 || stdout != "lab\nproduction\n" {
		t.Fatalf("__complete ls --group: %d %q", code, stdout)
	}
}

// kubectx's and kubens's long spellings work as their short forms.
func TestLongSpellings(t *testing.T) {
	needsPOSIX(t)
	e, _ := fixture(t)
	if code, _, errs := runKcx(t, e, "alpha"); code != 0 {
		t.Fatalf("kcx alpha: %q", errs)
	}
	if code, stdout, _ := runKcx(t, e, "--current"); code != 0 || stdout != "alpha\n" {
		t.Fatalf("--current: %d %q", code, stdout)
	}
	if code, stdout, _ := runKcx(t, e, "-V"); code != 0 || stdout != "kcx "+version+"\n" {
		t.Fatalf("-V: %d %q", code, stdout)
	}
	code, stdout, errs := runKcx(t, e, "--readonly", "alpha", "--", "sh", "-c", `printf '%s' "$KCX_RO"`)
	if code != 0 || stdout != "alpha (READ-ONLY)" {
		t.Fatalf("--readonly: %d %q %q", code, stdout, errs)
	}
	for _, w := range []string{"-s", "--shell"} {
		if code, _, errs := runKcx(t, e, w, "nope"); code != 1 || !strings.Contains(errs, "no context named nope") {
			t.Fatalf("%s nope: %d %q", w, code, errs)
		}
	}
	// Inside a read-only shell, --current is one of the queries that work.
	ro := env{"HOME": e["HOME"], EnvRO: "alpha (READ-ONLY)", EnvROConfig: "/nonexistent", "KUBECONFIG": "/nonexistent"}
	if code, _, errs := runKcx(t, ro, "--current"); code == 1 && strings.Contains(errs, "READ-ONLY shell") {
		t.Fatalf("--current refused in a read-only shell: %q", errs)
	}
}

// A child ended by a signal gives the status a shell would: 128 plus the signal.
func TestExitStatusOfASignal(t *testing.T) {
	needsPOSIX(t)
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX signals")
	}
	e, _ := fixture(t)
	if code, _, _ := runKcx(t, e, "run", "alpha", "--", "sh", "-c", "kill -TERM $$"); code != 128+int(syscall.SIGTERM) {
		t.Fatalf("status %d, want %d", code, 128+int(syscall.SIGTERM))
	}
}

// A terminate or a hang-up sent to kcx reaches the child; kcx waits for it,
// removes its private files and returns the child's status. Ctrl-C alone,
// which the terminal delivers to the child itself, does not end kcx.
func TestRunForwardsSignals(t *testing.T) {
	needsPOSIX(t)
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX signals")
	}
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "kcx")
	build := exec.Command("go", "build", "-o", bin, "./cmd/kcx")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	e, _ := fixture(t)
	for _, c := range []struct {
		sig    syscall.Signal
		status int
	}{
		{syscall.SIGTERM, 7},
		{syscall.SIGHUP, 9},
	} {
		dir := t.TempDir()
		script := `printf '%s' "$KCX_RUN_CONFIG" > ready; trap 'echo got-term > got; exit 7' TERM; trap 'echo got-hup > got; exit 9' HUP; while :; do sleep 0.1; done`
		cmd := exec.Command(bin, "run", "alpha", "--", "sh", "-c", script)
		cmd.Dir = dir
		cmd.Env = []string{"HOME=" + e["HOME"], "PATH=" + os.Getenv("PATH")}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		ready := filepath.Join(dir, "ready")
		for i := 0; i < 100; i++ {
			if data, err := os.ReadFile(ready); err == nil && len(data) > 0 {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		private, err := os.ReadFile(ready)
		if err != nil || len(private) == 0 {
			_ = cmd.Process.Kill()
			t.Fatalf("the child never started: %v %s", err, stderr.String())
		}
		if c.sig == syscall.SIGTERM {
			// Ctrl-C sent to kcx alone: kcx stays, the child keeps running.
			_ = cmd.Process.Signal(syscall.SIGINT)
			time.Sleep(300 * time.Millisecond)
			if _, err := os.Stat(filepath.Join(dir, "got")); err == nil {
				t.Fatal("an interrupt was passed on to the child")
			}
		}
		_ = cmd.Process.Signal(c.sig)
		err = cmd.Wait()
		status := 0
		if ee, ok := err.(*exec.ExitError); ok {
			status = ee.ExitCode()
		}
		if status != c.status {
			t.Fatalf("%v: kcx exited %d (%v), want the child's %d; %s", c.sig, status, err, c.status, stderr.String())
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "got")); !strings.HasPrefix(string(got), "got-") {
			t.Fatalf("%v: the child did not get it: %q", c.sig, got)
		}
		if _, err := os.Stat(filepath.Dir(string(private))); !os.IsNotExist(err) {
			t.Fatalf("%v: the run's private directory outlived it: %v", c.sig, err)
		}
	}
}
