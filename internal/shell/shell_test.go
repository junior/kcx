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

// layoutHome makes a home with two cluster files and returns it, with the
// KUBECONFIG list the layout gives.
func layoutHome(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	for _, f := range []string{"configs/alpha.yaml", "configs/beta.yaml"} {
		p := filepath.Join(home, ".kube", f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home, strings.Join([]string{
		filepath.Join(home, ".kube", "config"),
		filepath.Join(home, ".kube", "configs", "alpha.yaml"),
		filepath.Join(home, ".kube", "configs", "beta.yaml"),
	}, ":")
}

// Inside a kcx shell the private file comes first, tracked by KCX_SHELL_AUTO;
// KCX_AUTO is never set there, and a deliberate value stays.
func TestInitInsideKcxShell(t *testing.T) {
	home, layoutList := layoutHome(t)
	private := filepath.Join(t.TempDir(), "config.yaml")
	want := private + ":" + layoutList
	base := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "KCX_SHELL=" + private}
	for sh, kind := range shells(t) {
		snippet, _ := Init(kind)
		got := runSnippet(t, sh, kind, snippet, `printf '%s|%s|%s' "$KUBECONFIG" "$KCX_SHELL_AUTO" "${KCX_AUTO-unset}"`, base)
		if got != want+"|"+want+"|unset" {
			t.Errorf("%s: fresh kcx shell: %q", sh, got)
		}
		got = runSnippet(t, sh, kind, snippet, `printf '%s' "$KUBECONFIG"`, append(base, "KUBECONFIG=/old", "KCX_SHELL_AUTO=/old"))
		if got != want {
			t.Errorf("%s: stale kcx shell value not replaced: %q", sh, got)
		}
		got = runSnippet(t, sh, kind, snippet, `printf '%s' "$KUBECONFIG"`, append(base, "KUBECONFIG=/mine"))
		if got != "/mine" {
			t.Errorf("%s: deliberate KUBECONFIG in a kcx shell replaced: %q", sh, got)
		}
	}
}

// An inline copy of the function that knows nothing of KCX_SHELL (the one the
// dotfiles keep in ~/.zshenv) must leave a kcx shell's KUBECONFIG alone, or a
// `zsh -c` started inside the shell would run against the shared context.
func TestOlderInlineCopyLeavesKcxShellAlone(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	home, layoutList := layoutHome(t)
	private := filepath.Join(t.TempDir(), "config.yaml")
	list := private + ":" + layoutList
	older := `_kcx_kubeconfig_env() {
  emulate -L zsh
  [[ -n ${KCX_RO-} || -n ${KCX_RUN-} ]] && return 0
  [[ -z ${KUBECONFIG-} || $KUBECONFIG == ${KCX_AUTO-} ]] || return 0
  local LC_ALL=C
  local -a files
  files=( ~/.kube/config ~/.kube/configs/*.yaml(N.on) ~/.kube/temp/*.yaml(N.on) )
  export KUBECONFIG=${(j.:.)files} KCX_AUTO=${(j.:.)files}
}
_kcx_kubeconfig_env`
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "KCX_SHELL=" + private, "KUBECONFIG=" + list, "KCX_SHELL_AUTO=" + list}
	if got := runSnippet(t, zsh, "zsh", older, `printf '%s' "$KUBECONFIG"`, env); got != list {
		t.Fatalf("the older copy replaced a kcx shell's KUBECONFIG: %q", got)
	}
}

// The first prompt puts back the shell's own KUBECONFIG, whatever a later
// startup file set, in a kcx shell, a read-only shell and a run; once only.
func TestRestoreOnce(t *testing.T) {
	home, layoutList := layoutHome(t)
	dir := t.TempDir()
	private, ro, run := filepath.Join(dir, "shell.yaml"), filepath.Join(dir, "ro.yaml"), filepath.Join(dir, "run.yaml")
	base := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	cases := []struct {
		env  []string
		want string
	}{
		{[]string{"KCX_SHELL=" + private}, private + ":" + layoutList},
		{[]string{"KCX_RO=x (READ-ONLY)", "KCX_RO_CONFIG=" + ro}, ro},
		{[]string{"KCX_RUN=x", "KCX_RUN_CONFIG=" + run}, run + ":" + layoutList},
	}
	for sh, kind := range shells(t) {
		snippet, _ := Init(kind)
		for _, c := range cases {
			env := append(append([]string{}, base...), c.env...)
			// A startup file clobbers KUBECONFIG; the first prompt restores it;
			// a second prompt leaves a deliberate change alone.
			got := runSnippet(t, sh, kind, snippet, `export KUBECONFIG=/clobbered; _kcx_restore_once; first=$KUBECONFIG; export KUBECONFIG=/mine; _kcx_restore_once; printf '%s|%s' "$first" "$KUBECONFIG"`, env)
			if got != c.want+"|/mine" {
				t.Errorf("%s %v: %q, want %q", sh, c.env, got, c.want+"|/mine")
			}
		}
	}
}

// In an interactive shell the restore and the KUBECONFIG hook come first, so the
// prompt shows what is current; the restore only where there is something to restore.
func TestHooksComeFirst(t *testing.T) {
	home, _ := layoutHome(t)
	private := filepath.Join(t.TempDir(), "config.yaml")
	for sh, kind := range shells(t) {
		snippet, _ := Init(kind)
		var args []string
		var show string
		if kind == "zsh" {
			args = []string{"-f", "-i", "-c"}
			show = `precmd_functions=(other); ` + snippet + "\nprint -r -- ${(j.;.)precmd_functions}"
		} else {
			args = []string{"--norc", "--noprofile", "-i", "-c"}
			show = `PROMPT_COMMAND=other; ` + snippet + "\nprintf '%s\\n' \"$PROMPT_COMMAND\""
		}
		for _, c := range []struct {
			extra string
			want  string
		}{
			{"KCX_SHELL=" + private, "_kcx_restore_once;_kcx_kubeconfig_env;other"},
			{"", "_kcx_kubeconfig_env;other"},
		} {
			env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
			if c.extra != "" {
				env = append(env, c.extra)
			}
			cmd := exec.Command(sh, append(args, show)...)
			cmd.Env = env
			out, err := cmd.Output() // stdout only: an interactive shell without a terminal warns on stderr
			if err != nil {
				t.Fatalf("%s: %v", sh, err)
			}
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			// The zsh snippet also appends its one-shot completion registration.
			if got := lines[len(lines)-1]; !strings.HasPrefix(got+";", c.want+";") {
				t.Errorf("%s (%s) with %q: hooks %q, want them to start %q", sh, kind, c.extra, got, c.want)
			}
		}
	}
}
