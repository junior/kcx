package secret

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestScriptResolve(t *testing.T) {
	root := t.TempDir()
	touch := func(p string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	sysroot := filepath.Join(root, "Windows")
	winPS := touch(filepath.Join(sysroot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
	progs := filepath.Join(root, "Program Files")
	pwsh7 := touch(filepath.Join(progs, "PowerShell", "7", "pwsh.exe"))
	home := filepath.Join(root, "home")
	script := touch(filepath.Join(home, ".kob-tools", "kob_secret.ps1"))
	pathDir := filepath.Join(root, "bin")
	onPath := touch(filepath.Join(pathDir, "other.ps1"))
	env := map[string]string{"SystemRoot": sysroot, "ProgramFiles": progs, "PATH": pathDir}
	opts := func(goos, ps string) Options {
		return Options{GOOS: goos, PowerShell: ps, Home: home, Getenv: func(k string) string { return env[k] }}
	}

	// Windows PowerShell by its absolute path; get, status and forget run with
	// -NonInteractive, refresh without it.
	p, err := ResolveWith(script, opts("windows", ""))
	if err != nil {
		t.Fatal(err)
	}
	e := p.(*Exec)
	for verb, want := range map[string][]string{
		"get":     {winPS, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", script, "get"},
		"status":  {winPS, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", script, "status"},
		"forget":  {winPS, "-NoLogo", "-NoProfile", "-NonInteractive", "-File", script, "forget"},
		"refresh": {winPS, "-NoLogo", "-NoProfile", "-File", script, "refresh"},
	} {
		cmd := e.command(context.Background(), verb)
		if cmd.Path != winPS || strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
			t.Errorf("%s: %s %q, want %q", verb, cmd.Path, cmd.Args, want)
		}
	}
	if !e.Caches() || e.Name() != script {
		t.Fatalf("a script provider caches, under its own name: %q", e.Name())
	}

	// The script by ~ or by a file name in a PATH directory; PowerShell 7 or an
	// absolute path by the setting.
	for _, c := range []struct {
		name, ps, wantScript, wantShell string
	}{
		{"~/.kob-tools/kob_secret.ps1", "windows", script, winPS},
		{`~\.kob-tools\kob_secret.ps1`, "", script, winPS}, // Windows' separator: on Windows only
		{"other.ps1", "pwsh", onPath, pwsh7},
		{script, pwsh7, script, pwsh7},
	} {
		if strings.Contains(c.name, `\`) && runtime.GOOS != "windows" {
			continue
		}
		p, err := ResolveWith(c.name, opts("windows", c.ps))
		if err != nil {
			t.Errorf("%s with %q: %v", c.name, c.ps, err)
			continue
		}
		if e := p.(*Exec); e.path != c.wantScript || e.shell != c.wantShell {
			t.Errorf("%s with %q: script %s shell %s", c.name, c.ps, e.path, e.shell)
		}
	}

	// What does not resolve, and says why.
	for _, c := range []struct {
		name, goos, ps, want string
	}{
		{`tools\kob_secret.ps1`, "windows", "", "absolute path"},
		{"missing.ps1", "windows", "", "not in any PATH directory"},
		{filepath.Join(home, "nope.ps1"), "windows", "", "nope.ps1"},
		{script, "windows", "powershell7", `"pwsh" or an absolute path`},
		{script, "linux", "", `powershell = "pwsh"`},
		{script, "windows", filepath.Join(root, "no-such-pwsh.exe"), "no PowerShell at"},
	} {
		if _, err := ResolveWith(c.name, opts(c.goos, c.ps)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s (%s, %q): %v, want an error with %q", c.name, c.goos, c.ps, err, c.want)
		}
	}
}

// fakeScript is a four-verb provider in PowerShell, as kob_secret.ps1 is: get
// writes the cached bytes with no newline; refresh reads a line from standard
// input; exit 3 from every verb when FAKE_PS_UNAVAILABLE is set.
const fakeScript = `param([string]$verb)
if ($env:FAKE_PS_UNAVAILABLE) { [Console]::Error.WriteLine('fake: this PowerShell will not run me'); exit 3 }
$cache = $env:FAKE_PS_CACHE
switch ($verb) {
  'get' {
    if (-not (Test-Path -LiteralPath $cache)) { [Console]::Error.WriteLine('nothing cached'); exit 1 }
    $bytes = [IO.File]::ReadAllBytes($cache)
    $out = [Console]::OpenStandardOutput()
    $out.Write($bytes, 0, $bytes.Length)
    $out.Flush()
    exit 0
  }
  'refresh' {
    $line = [Console]::In.ReadLine()
    if (-not $line) { exit 1 }
    [IO.File]::WriteAllText($cache, $line)
    exit 0
  }
  'forget' {
    if ($env:FAKE_PS_FORGOT) { Add-Content -LiteralPath $env:FAKE_PS_FORGOT -Value ('[' + $env:KCX_SECRET_REJECTED + ']') }
    Remove-Item -LiteralPath $cache -ErrorAction Ignore; exit 0
  }
  'status' { if (Test-Path -LiteralPath $cache) { 'cached'; exit 0 } else { 'empty'; exit 1 } }
  default { exit 2 }
}
`

// The script provider run by a real PowerShell: Windows PowerShell on Windows,
// pwsh elsewhere when it is installed.
func TestScriptProviderRuns(t *testing.T) {
	ps := ""
	if runtime.GOOS != "windows" {
		if _, err := exec.LookPath("pwsh"); err != nil {
			t.Skip("no pwsh")
		}
		ps = "pwsh"
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake.ps1")
	if err := os.WriteFile(script, []byte(fakeScript), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_PS_CACHE", filepath.Join(dir, "cache"))
	t.Setenv("FAKE_PS_UNAVAILABLE", "")
	p, err := ResolveWith(script, Options{PowerShell: ps})
	if err != nil {
		t.Fatal(err)
	}
	p.(*Exec).Stderr = io.Discard
	ctx := context.Background()
	if s, err := p.Status(ctx); err != nil || s != "empty" {
		t.Fatalf("status before: %q %v", s, err)
	}
	if _, wait, err := p.Get(ctx); err == nil && wait() == nil {
		t.Fatal("get with nothing cached must fail")
	}
	if err := p.Refresh(ctx, strings.NewReader("s3cret\n"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if s, err := p.Status(ctx); err != nil || s != "cached" {
		t.Fatalf("status after the refresh: %q %v", s, err)
	}
	r, wait, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if err := wait(); err != nil || string(got) != "s3cret" {
		t.Fatalf("get: %q %v (want the bytes alone, no newline)", got, err)
	}
	// A plain forget carries no rejection, even with one in kcx's own
	// environment; a forget after a refusal carries KCX_SECRET_REJECTED=1.
	forgot := filepath.Join(dir, "forgot")
	t.Setenv("FAKE_PS_FORGOT", forgot)
	t.Setenv(EnvRejected, "1")
	if err := p.Forget(ctx); err != nil {
		t.Fatal(err)
	}
	if s, _ := p.Status(ctx); s != "empty" {
		t.Fatalf("status after forget: %q", s)
	}
	if err := p.(*Exec).ForgetRejected(ctx); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(forgot); strings.Join(strings.Fields(string(data)), " ") != "[] [1]" {
		t.Fatalf("what forget saw of %s: %q", EnvRejected, data)
	}

	// Exit 3: the provider cannot run here.
	t.Setenv("FAKE_PS_UNAVAILABLE", "1")
	if _, wait, err := p.Get(ctx); err == nil {
		if err := wait(); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("get: %v, want ErrUnavailable", err)
		}
	} else if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("get: %v, want ErrUnavailable", err)
	}
	if err := p.Refresh(ctx, strings.NewReader("x\n"), io.Discard, io.Discard); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("refresh: %v, want ErrUnavailable", err)
	}
	if s, err := p.Status(ctx); err != nil || !strings.Contains(s, "ask for the secret itself") {
		t.Fatalf("status: %q %v", s, err)
	}
	if err := p.Forget(ctx); err != nil {
		t.Fatalf("forget: %v", err)
	}
}
