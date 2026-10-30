package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

type env map[string]string

func (e env) get(k string) string { return e[k] }

func runKcx(t *testing.T, e env, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Main(args, strings.NewReader(""), &out, &errb, e.get)
	return code, out.String(), errb.String()
}

func TestResolveVersion(t *testing.T) {
	info := func(v string, ok bool) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Path: "github.com/junior/kcx/v2", Version: v}}, ok
		}
	}
	for _, c := range []struct {
		stamp, module string
		ok            bool
		want          string
	}{
		{"2.2.0", "v2.2.0", true, "2.2.0"}, // the release build's stamp wins
		{"", "v2.2.0", true, "2.2.0"},      // go install github.com/junior/kcx/v2/cmd/kcx@v2.2.0
		{"", "v2.2.1-0.20261030010203-abcdef123456", true, "2.2.1-0.20261030010203-abcdef123456"}, // @main
		{"", "(devel)", true, "dev"}, // go build in a tree without git
		{"", "", false, "dev"},
	} {
		if got := resolveVersion(c.stamp, info(c.module, c.ok)); got != c.want {
			t.Errorf("resolveVersion(%q, %q) = %q, want %q", c.stamp, c.module, got, c.want)
		}
	}
}

func TestVersionAndHelp(t *testing.T) {
	e := env{"HOME": t.TempDir()}
	code, out, _ := runKcx(t, e, "-v")
	if code != 0 || out != "kcx "+version+"\n" {
		t.Fatalf("-v: %d %q", code, out)
	}
	code, out, _ = runKcx(t, e, "-h")
	if code != 0 || !strings.Contains(out, "kcx env") {
		t.Fatalf("-h: %d %q", code, out)
	}
	// A bare word is a switch attempt, so an unknown one names the missing context.
	code, _, errs := runKcx(t, e, "bogus")
	if code != 1 || !strings.Contains(errs, "no context named bogus") {
		t.Fatalf("bogus: %d %q", code, errs)
	}
	code, _, errs = runKcx(t, e, "--bogus")
	if code != 2 || !strings.Contains(errs, "unknown option --bogus") {
		t.Fatalf("--bogus: %d %q", code, errs)
	}
}

func TestNoHome(t *testing.T) {
	code, _, errs := runKcx(t, env{}, "-v")
	if code != 1 || !strings.Contains(errs, "HOME is not set") && !strings.Contains(errs, "no home directory") {
		t.Fatalf("no HOME: %d %q", code, errs)
	}
}

func TestEnv(t *testing.T) {
	home := t.TempDir()
	perm := filepath.Join(home, ".kube", "configs")
	if err := os.MkdirAll(perm, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(perm, "it's.yaml"), []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(home, ".kube", "config")
	e := env{"HOME": home, "KUBECONFIG": "/somewhere/else"}
	code, out, _ := runKcx(t, e, "env")
	// The layout wins over an inherited KUBECONFIG; the state file is listed first
	// even before it exists; a quote in a name is escaped for the shell.
	list := state + string(os.PathListSeparator) + perm + string(filepath.Separator) + "it'\\''s.yaml"
	want := "export KUBECONFIG='" + list + "' KCX_AUTO='" + list + "'\n"
	if code != 0 || out != want {
		t.Fatalf("env:\n%q\nwant\n%q", out, want)
	}
	// Inside a read-only shell the read-only kubeconfig comes back, nothing else.
	ro := env{"HOME": home, "KUBECONFIG": "/tmp/ro.json", EnvRO: "x (READ-ONLY)", EnvROConfig: "/tmp/ro.json"}
	code, out, _ = runKcx(t, ro, "env")
	if code != 0 || out != "export KUBECONFIG='/tmp/ro.json'\n" {
		t.Fatalf("env in RO shell: %d %q", code, out)
	}
	// Inside a run, the run's own list.
	run := env{"HOME": home, "KUBECONFIG": "/tmp/run.json:" + state, EnvRun: "x"}
	code, out, _ = runKcx(t, run, "env")
	if code != 0 || out != "export KUBECONFIG='/tmp/run.json:"+state+"'\n" {
		t.Fatalf("env in run: %d %q", code, out)
	}
	if code, _, errs := runKcx(t, e, "env", "extra"); code != 2 || !strings.Contains(errs, "kcx env [pwsh]") {
		t.Fatalf("env extra: %d %q", code, errs)
	}
	// PowerShell's form: $env: assignments, single quotes doubled inside.
	code, out, _ = runKcx(t, ro, "env", "pwsh")
	if code != 0 || out != "$env:KUBECONFIG = '/tmp/ro.json'\n" {
		t.Fatalf("env pwsh in RO shell: %d %q", code, out)
	}
	quoted := env{"HOME": filepath.Join(t.TempDir(), "o'neil")}
	code, out, _ = runKcx(t, quoted, "env", "pwsh")
	want = filepath.Join(quoted["HOME"], ".kube", "config")
	want = "$env:KUBECONFIG = '" + strings.ReplaceAll(want, "'", "''") + "'\n$env:KCX_AUTO = '" + strings.ReplaceAll(want, "'", "''") + "'\n"
	if code != 0 || out != want {
		t.Fatalf("env pwsh: %d %q, want %q", code, out, want)
	}
	if code, out, _ = runKcx(t, quoted, "env", "bash"); code != 0 || !strings.HasPrefix(out, "export KUBECONFIG=") {
		t.Fatalf("env bash: %d %q", code, out)
	}
}

func TestPickShell(t *testing.T) {
	look := func(found ...string) func(string) (string, error) {
		return func(name string) (string, error) {
			for _, f := range found {
				if f == name {
					return `C:\found\` + name, nil
				}
			}
			return "", os.ErrNotExist
		}
	}
	vars := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for _, c := range []struct {
		goos  string
		env   map[string]string
		found []string
		want  string
	}{
		{"darwin", map[string]string{"SHELL": "/bin/zsh"}, nil, "/bin/zsh"},
		{"linux", nil, nil, "/bin/sh"},
		// Windows: Git Bash's MSYS path cannot start, its base name can.
		{"windows", map[string]string{"SHELL": "/usr/bin/bash"}, []string{"bash", "pwsh.exe"}, `C:\found\bash`},
		{"windows", nil, []string{"pwsh.exe", "powershell.exe"}, `C:\found\pwsh.exe`},
		{"windows", nil, []string{"powershell.exe"}, `C:\found\powershell.exe`},
		{"windows", map[string]string{"COMSPEC": `C:\Windows\system32\cmd.exe`}, []string{`C:\Windows\system32\cmd.exe`}, `C:\found\C:\Windows\system32\cmd.exe`},
		{"windows", nil, nil, "cmd.exe"},
	} {
		if got := pickShell(c.goos, vars(c.env), look(c.found...)); got != c.want {
			t.Errorf("pickShell(%s, %v, %v) = %q, want %q", c.goos, c.env, c.found, got, c.want)
		}
	}
}

func TestReadOnlyShellGuard(t *testing.T) {
	ro := env{"HOME": t.TempDir(), EnvRO: "prod (READ-ONLY)"}
	code, _, errs := runKcx(t, ro, "add", "x", "f")
	if code != 1 || !strings.Contains(errs, "READ-ONLY shell for prod (READ-ONLY)") {
		t.Fatalf("guard: %d %q", code, errs)
	}
	if code, _, _ := runKcx(t, ro, "-v"); code != 0 {
		t.Fatal("-v must work inside a read-only shell")
	}
}

func TestComplete(t *testing.T) {
	code, out, _ := runKcx(t, env{"HOME": t.TempDir()}, "__complete")
	if code != 0 || !strings.Contains(out, "env\n") {
		t.Fatalf("__complete: %d %q", code, out)
	}
}
