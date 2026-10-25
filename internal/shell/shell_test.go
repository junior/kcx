package shell

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// shells lists the shells to test the snippets in, where they exist: macOS's
// /bin/bash is 3.2, the one the snippets must still run in.
func shells(t *testing.T) map[string]string {
	t.Helper()
	found := map[string]string{}
	for _, candidate := range []string{"/bin/bash", "bash", "zsh"} {
		p, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		kind := "bash"
		if strings.HasSuffix(candidate, "zsh") {
			kind = "zsh"
		}
		found[p] = kind
	}
	if len(found) == 0 {
		t.Skip("no shell to test with")
	}
	return found
}

func runSnippet(t *testing.T, shell, kind, snippet, after string, env []string) string {
	t.Helper()
	script := snippet + "\n" + after
	cmd := exec.Command(shell, "-c", script)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s (%s): %v\n%s", shell, kind, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestInitComputesKubeconfig(t *testing.T) {
	home := t.TempDir()
	for _, d := range []string{"configs", "temp"} {
		if err := os.MkdirAll(filepath.Join(home, ".kube", d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"configs/beta.yaml", "configs/alpha.yaml", "configs/.kcx.tmp", "configs/notes.txt", "temp/t1@imac.yaml"} {
		if err := os.WriteFile(filepath.Join(home, ".kube", f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want := strings.Join([]string{
		filepath.Join(home, ".kube", "config"),
		filepath.Join(home, ".kube", "configs", "alpha.yaml"),
		filepath.Join(home, ".kube", "configs", "beta.yaml"),
		filepath.Join(home, ".kube", "temp", "t1@imac.yaml"),
	}, ":")
	base := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	for sh, kind := range shells(t) {
		snippet, err := Init(kind)
		if err != nil {
			t.Fatal(err)
		}
		// Fresh shell: computed, and KCX_AUTO holds the same value.
		got := runSnippet(t, sh, kind, snippet, `printf '%s|%s' "$KUBECONFIG" "$KCX_AUTO"`, base)
		if got != want+"|"+want {
			t.Errorf("%s: fresh shell: %q, want %q", sh, got, want+"|"+want)
		}
		// A value the function set earlier (KUBECONFIG == KCX_AUTO) is recomputed.
		got = runSnippet(t, sh, kind, snippet, `printf '%s' "$KUBECONFIG"`, append(base, "KUBECONFIG=/old/list", "KCX_AUTO=/old/list"))
		if got != want {
			t.Errorf("%s: stale auto value not replaced: %q", sh, got)
		}
		// A deliberate KUBECONFIG is left alone.
		got = runSnippet(t, sh, kind, snippet, `printf '%s' "$KUBECONFIG"`, append(base, "KUBECONFIG=/mine/config"))
		if got != "/mine/config" {
			t.Errorf("%s: deliberate KUBECONFIG replaced: %q", sh, got)
		}
		// Inside a read-only shell or a run, nothing changes.
		for _, guard := range []string{"KCX_RO=x (READ-ONLY)", "KCX_RUN=x"} {
			got = runSnippet(t, sh, kind, snippet, `printf '%s' "$KUBECONFIG"`, append(base, "KUBECONFIG=/ro/config", guard))
			if got != "/ro/config" {
				t.Errorf("%s with %s: guarded KUBECONFIG replaced: %q", sh, guard, got)
			}
		}
		// The snippet parses and defines the function; the completion part too.
		comp, _ := Completion(kind)
		got = runSnippet(t, sh, kind, snippet+comp, `type _kcx_kubeconfig_env >/dev/null 2>&1 && echo defined`, base)
		if !strings.Contains(got, "defined") {
			t.Errorf("%s: function not defined: %q", sh, got)
		}
	}
}

func TestUnknownShell(t *testing.T) {
	if _, err := Init("fish"); err == nil {
		t.Fatal("fish is not supported yet")
	}
	if _, err := Completion("tcsh"); err == nil {
		t.Fatal("tcsh is not supported")
	}
}
