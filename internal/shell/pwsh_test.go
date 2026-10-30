package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runPwsh runs the PowerShell snippet for kube, then after, in a pwsh of its own
// with env added to a clean environment, and returns the KEY=VALUE lines that
// after prints.
func runPwsh(t *testing.T, pwsh, kube, after string, env ...string) map[string]string {
	t.Helper()
	snippet, err := Init("pwsh", kube)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "t.ps1")
	if err := os.WriteFile(script, []byte(snippet+"\n"+after), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-File", script)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "KUBECONFIG" || strings.HasPrefix(strings.ToUpper(k), "KCX_") {
			continue
		}
		cmd.Env = append(cmd.Env, kv)
	}
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pwsh: %v\n%s", err, out)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	return got
}

func TestPwshInit(t *testing.T) {
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("no pwsh")
	}
	kube := filepath.Join(t.TempDir(), "o'neil", ".kube")
	for _, d := range []string{"configs", "temp"} {
		if err := os.MkdirAll(filepath.Join(kube, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Bytewise order, as kcx sorts: Z before a-x before a (a culture-aware sort
	// puts them the other way round); no dotfiles, nothing but *.yaml.
	for _, f := range []string{"configs/a.yaml", "configs/Z.yaml", "configs/a-x.yaml", "configs/.kcx.tmp.yaml", "configs/notes.txt", "temp/t1@docker.yaml"} {
		if err := os.WriteFile(filepath.Join(kube, filepath.FromSlash(f)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sep := string(os.PathListSeparator)
	list := strings.Join([]string{
		filepath.Join(kube, "config"),
		filepath.Join(kube, "configs", "Z.yaml"),
		filepath.Join(kube, "configs", "a-x.yaml"),
		filepath.Join(kube, "configs", "a.yaml"),
		filepath.Join(kube, "temp", "t1@docker.yaml"),
	}, sep)

	// The plain case, and a KUBECONFIG set on purpose, which stays.
	got := runPwsh(t, pwsh, kube, `
"kube=" + $env:KUBECONFIG
"auto=" + $env:KCX_AUTO
$env:KUBECONFIG = 'mine'
_kcx_kubeconfig_env
"mine=" + $env:KUBECONFIG
`)
	if got["kube"] != list || got["auto"] != list {
		t.Fatalf("KUBECONFIG:\n%q\nwant\n%q (KCX_AUTO %q)", got["kube"], list, got["auto"])
	}
	if got["mine"] != "mine" {
		t.Fatalf("a KUBECONFIG set on purpose was replaced: %q", got["mine"])
	}

	// A kcx shell: its private file first, tracked by KCX_SHELL_AUTO; and the
	// first prompt puts it back after another tool's profile changed it.
	private := filepath.Join(t.TempDir(), "config.yaml")
	got = runPwsh(t, pwsh, kube, `
"kube=" + $env:KUBECONFIG
"shellauto=" + $env:KCX_SHELL_AUTO
$env:KUBECONFIG = 'another-tool'
prompt | Out-Null
"restored=" + $env:KUBECONFIG
`, "KCX_SHELL="+private)
	if want := private + sep + list; got["kube"] != want || got["shellauto"] != want || got["restored"] != want {
		t.Fatalf("kcx shell: %v, want %q", got, want)
	}

	// A read-only shell: the snippet leaves KUBECONFIG alone, and the first
	// prompt puts the read-only kubeconfig back.
	got = runPwsh(t, pwsh, kube, `
"kube=" + $env:KUBECONFIG
$env:KUBECONFIG = 'another-tool'
prompt | Out-Null
"restored=" + $env:KUBECONFIG
`, "KCX_RO=prod (READ-ONLY)", "KCX_RO_CONFIG=/ro.yaml", "KUBECONFIG=/ro.yaml")
	if got["kube"] != "/ro.yaml" || got["restored"] != "/ro.yaml" {
		t.Fatalf("read-only shell: %v", got)
	}

	// The prompt in place before (a theme's) sees the last command's $? and
	// $LASTEXITCODE, not those of the hook that ran first.
	got = runPwsh(t, pwsh, kube, `
$global:_kcx_prompt = { $global:seenOk = $?; $global:seenCode = $global:LASTEXITCODE; 'PS> ' }
$global:LASTEXITCODE = 3
Write-Error 'a failed command' -ErrorAction SilentlyContinue
prompt | Out-Null
"ok=" + $global:seenOk
"code=" + $global:seenCode
$global:LASTEXITCODE = 0
'fine' | Out-Null
prompt | Out-Null
"ok2=" + $global:seenOk
`)
	if got["ok"] != "False" || got["code"] != "3" || got["ok2"] != "True" {
		t.Fatalf("the prompt saw the hook's status, not the command's: %v", got)
	}
}
