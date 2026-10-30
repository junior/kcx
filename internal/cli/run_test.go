package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/junior/kcx/v2/internal/kubeconfig"
	"github.com/junior/kcx/v2/internal/ns"
)

func TestRunShadowAndExitCode(t *testing.T) {
	needsPOSIX(t)
	e, l := fixture(t)
	if code, _, _ := runKcx(t, e, "beta"); code != 0 {
		t.Fatal("switch beta")
	}
	code, out, errs := runKcx(t, e, "run", "alpha", "--", "sh", "-c", `printf '%s|%s' "$KUBECONFIG" "$KCX_RUN"`)
	if code != 0 {
		t.Fatalf("run: %d %q", code, errs)
	}
	parts := strings.SplitN(out, "|", 2)
	if len(parts) != 2 || parts[1] != "alpha" {
		t.Fatalf("KCX_RUN: %q", out)
	}
	files := strings.Split(parts[0], ":")
	if len(files) != 4 || !strings.HasSuffix(files[0], "current.yaml") || files[1] != l.State || files[2] != l.PermFile("alpha") {
		t.Fatalf("KUBECONFIG inside the run: %v", files)
	}
	// The shared current context did not move.
	if cur, _ := (kubeconfig.Store{Files: l.Files()}).Current(); cur != "beta" {
		t.Fatalf("run changed the shared current context to %q", cur)
	}
	// The shadow file is gone after the run.
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Fatal("the private state file outlived the run")
	}
	// -n sets the run's namespace in the shadow, and only there.
	code, out, _ = runKcx(t, e, "run", "alpha", "-n", "team", "--", "sh", "-c", `cat "${KUBECONFIG%%:*}"`)
	if code != 0 || !strings.Contains(out, "namespace: team") || !strings.Contains(out, "current-context: alpha") || strings.Contains(out, "not-a-secret") {
		t.Fatalf("shadow content: %d %q", code, out)
	}
	cfg, _ := kubeconfig.LoadFile(l.PermFile("alpha"))
	if cfg.Contexts["alpha"].Namespace != "" {
		t.Fatal("the real file took the run's namespace")
	}
	// The child's exit status is kcx's, with no message of kcx's own.
	code, _, errs = runKcx(t, e, "run", "alpha", "--", "sh", "-c", "exit 3")
	if code != 3 || errs != "" {
		t.Fatalf("exit code: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, e, "run", "nope", "--", "true"); code != 1 || !strings.Contains(errs, "no context named nope") {
		t.Fatalf("run unknown: %d %q", code, errs)
	}
	for _, args := range [][]string{{"run"}, {"run", "alpha"}, {"run", "alpha", "--"}, {"run", "--", "true"}, {"run", "alpha", "--bogus", "--", "true"}} {
		if code, _, _ := runKcx(t, e, args...); code != 2 {
			t.Errorf("%v: expected a usage error, got %d", args, code)
		}
	}
	if code, _, errs := runKcx(t, e, "run", "alpha", "--", "no-such-program-kcx"); code != 1 || !strings.Contains(errs, "cannot run no-such-program-kcx") {
		t.Fatalf("missing program: %d %q", code, errs)
	}
}

func TestShadowIsolatesNamespaceAndSwitch(t *testing.T) {
	e, l := fixture(t)
	a := &App{getenv: e.get, lay: l}
	shadow := filepath.Join(t.TempDir(), "current.yaml")
	if err := a.writeShadow(shadow, "alpha", ""); err != nil {
		t.Fatal(err)
	}
	files := append([]string{shadow}, l.Files()...)
	before := checksum(t, l.PermFile("alpha"))
	// A namespace change inside the run lands in the shadow: the first file wins.
	if err := ns.Set(files, "alpha", "run-ns"); err != nil {
		t.Fatal(err)
	}
	if checksum(t, l.PermFile("alpha")) != before {
		t.Fatal("ns inside a run rewrote the real file")
	}
	got, _ := ns.Of(files, "alpha")
	if got != "run-ns" {
		t.Fatalf("namespace through the shadow: %q", got)
	}
	// So does a switch: the shadow is the first existing file.
	if err := kubeconfig.SetCurrent(shadow, "beta"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := (kubeconfig.Store{Files: files}).Current(); cur != "beta" {
		t.Fatalf("switch inside the run: %q", cur)
	}
	if cur, _ := (kubeconfig.Store{Files: l.Files()}).Current(); cur != "" {
		t.Fatalf("the shared current context moved: %q", cur)
	}
	// The shadow resolves the real credentials.
	merged, _ := (kubeconfig.Store{Files: files}).Load()
	if merged.AuthInfos["alpha"].Token != "not-a-secret-alpha" {
		t.Fatal("the shadow context lost the real user")
	}
	if err := a.writeShadow(shadow, "nope", ""); err == nil {
		t.Fatal("a shadow of an unknown context must fail")
	}
}

func TestReadonlyRunAndShellEnv(t *testing.T) {
	needsPOSIX(t)
	e, l := fixture(t)
	code, out, errs := runKcx(t, e, "-r", "alpha", "--", "sh", "-c", `printf '%s|%s|' "$KCX_RO" "$KCX_RO_CONFIG"; cat "$KUBECONFIG"; printf '|%s' "${KCX_RUN-unset}"`)
	if code != 0 {
		t.Fatalf("-r one-shot: %d %q", code, errs)
	}
	if !strings.HasPrefix(out, "alpha (READ-ONLY)|") {
		t.Fatalf("KCX_RO: %q", out)
	}
	if !strings.Contains(out, "server: https://127.0.0.1:") || !strings.Contains(out, "certificate-authority-data: ") || !strings.Contains(out, "token: ") ||
		strings.Contains(out, "not-a-secret") || !strings.Contains(out, "current-context: alpha (READ-ONLY)") || !strings.HasSuffix(out, "|unset") {
		t.Fatalf("read-only kubeconfig handed to the command: %q", out)
	}
	if !strings.Contains(errs, "read-only run on alpha (READ-ONLY)") {
		t.Fatalf("no read-only notice: %q", errs)
	}
	// The read-only file is gone afterwards.
	parts := strings.Split(out, "|")
	if _, err := os.Stat(parts[1]); !os.IsNotExist(err) {
		t.Fatal("the read-only kubeconfig outlived the run")
	}
	// -n sets the namespace in the read-only kubeconfig.
	code, out, _ = runKcx(t, e, "-r", "alpha", "-n", "team", "--", "sh", "-c", `cat "$KUBECONFIG"`)
	if code != 0 || !strings.Contains(out, "namespace: team") {
		t.Fatalf("-r -n: %d %q", code, out)
	}
	// The exit status comes through; without a name and without a terminal, a usage error.
	// The shell's kubeconfig is the owner's to write, so a namespace change made
	// inside the shell lands there and nowhere else.
	if code, out, _ := runKcx(t, e, "-r", "alpha", "--", "sh", "-c", `test -w "$KUBECONFIG" && echo writable`); code != 0 || !strings.HasSuffix(out, "writable\n") {
		t.Fatalf("-r kubeconfig writable: %d %q", code, out)
	}
	// A startup file that evaluates kcx init runs inside the read-only shell too:
	// init prints its snippet there (whose own guard keeps the read-only
	// KUBECONFIG) instead of a refusal, while a switch is still refused.
	roEnv := env{"HOME": e["HOME"], EnvRO: "alpha (READ-ONLY)", EnvROConfig: l.PermFile("alpha"), "KUBECONFIG": l.PermFile("alpha")}
	code, out, errs = runKcx(t, roEnv, "init", "zsh")
	if code != 0 || !strings.Contains(out, "_kcx_kubeconfig_env()") || errs != "" {
		t.Fatalf("init inside a read-only shell: %d %q %q", code, out, errs)
	}
	if code, _, errs := runKcx(t, roEnv, "alpha"); code != 1 || !strings.Contains(errs, "only kcx ls, env, init, completion, -c and ns work here") {
		t.Fatalf("switch inside a read-only shell: %d %q", code, errs)
	}
	if code, _, _ := runKcx(t, e, "-r", "alpha", "--", "sh", "-c", "exit 7"); code != 7 {
		t.Fatalf("exit code through -r: %d", code)
	}
	if code, _, errs := runKcx(t, e, "-r"); code != 2 || !strings.Contains(errs, "name the context") {
		t.Fatalf("-r without a name: %d %q", code, errs)
	}
	if code, _, errs := runKcx(t, e, "-r", "nope", "--", "true"); code != 1 || !strings.Contains(errs, "no context named nope") {
		t.Fatalf("-r unknown: %d %q", code, errs)
	}
	_ = l
}

func TestRunReadonlyByDefault(t *testing.T) {
	needsPOSIX(t)
	f := withProfiles(t, "")
	e := f.env
	if code, _, errs := runKcx(t, e, "fetch", "prod1"); code != 0 {
		t.Fatalf("fetch prod1: %d %q", code, errs)
	}
	// prod1 is read-only by default: run goes behind the proxy and says so.
	code, out, errs := runKcx(t, e, "run", "prod1", "--", "sh", "-c", `printf '%s|%s' "${KCX_RO-unset}" "${KCX_RUN-unset}"`)
	if code != 0 || out != "prod1 (READ-ONLY)|unset" || !strings.Contains(errs, "prod1 is read-only by default (group production): running behind the read-only proxy") {
		t.Fatalf("run prod1: %d %q %q", code, out, errs)
	}
	// --rw runs with the real credentials.
	code, out, _ = runKcx(t, e, "run", "prod1", "--rw", "--", "sh", "-c", `printf '%s|%s' "${KCX_RO-unset}" "${KCX_RUN-unset}"`)
	if code != 0 || out != "unset|prod1" {
		t.Fatalf("run prod1 --rw: %d %q", code, out)
	}
	// A profile cluster without a file is fetched for the run.
	code, out, errs = runKcx(t, e, "run", "b", "--", "sh", "-c", `printf '%s' "$KCX_RUN"`)
	if code != 0 || !strings.Contains(out, "kcx: fetched b (") || !strings.HasSuffix(out, "\nb") || !strings.Contains(errs, "fetching b") {
		t.Fatalf("run fetches b: %d %q %q", code, out, errs)
	}
}

func TestInitCompletionDoctor(t *testing.T) {
	e, l := fixture(t)
	code, out, _ := runKcx(t, e, "init", "zsh")
	if code != 0 || !strings.Contains(out, "_kcx_kubeconfig_env()") || !strings.Contains(out, "compdef _kcx kcx") {
		t.Fatalf("init zsh: %d %q", code, out)
	}
	code, out, _ = runKcx(t, e, "init", "bash")
	if code != 0 || !strings.Contains(out, "PROMPT_COMMAND") || !strings.Contains(out, "complete -F _kcx_complete kcx") {
		t.Fatalf("init bash: %d %q", code, out)
	}
	code, out, _ = runKcx(t, e, "completion", "bash")
	if code != 0 || strings.Contains(out, "PROMPT_COMMAND") || !strings.Contains(out, "complete -F") {
		t.Fatalf("completion bash: %d %q", code, out)
	}
	if code, _, errs := runKcx(t, e, "init", "fish"); code != 2 || !strings.Contains(errs, "not a shell kcx knows") {
		t.Fatalf("init fish: %d %q", code, errs)
	}
	if code, _, _ := runKcx(t, e, "init"); code != 2 {
		t.Fatal("init without a shell is a usage error")
	}
	code, out, errs := runKcx(t, e, "doctor")
	if code != 0 || !strings.Contains(out, "ok    "+l.State+" is the state file, private") || !strings.Contains(out, "note  KUBECONFIG is not set in this shell") {
		t.Fatalf("doctor on a clean layout: %d %q %q", code, out, errs)
	}
	if err := os.Chmod(l.PermFile("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeKubeconfig(t, l.PermFile("stray"), "stray", "stray", "stray", "https://s.example.test", "stray")
	code, out, errs = runKcx(t, e, "doctor")
	modeLine, problems := "FIX   "+l.PermFile("alpha")+" is readable by others (mode 644)", "2 problem(s) to fix"
	if runtime.GOOS == "windows" { // no file modes there: the stray context alone
		modeLine, problems = "", "1 problem(s) to fix"
	}
	if code != 1 || !strings.Contains(out, modeLine) || !strings.Contains(out, "sets a current context of its own") || !strings.Contains(errs, problems) {
		t.Fatalf("doctor with problems: %d %q %q", code, out, errs)
	}
	_ = os.Chmod(l.PermFile("alpha"), 0o600)
	_ = os.Remove(l.PermFile("stray"))
	withKube := env{"HOME": e["HOME"], "KUBECONFIG": l.Kubeconfig(), EnvAuto: l.Kubeconfig()}
	code, out, _ = runKcx(t, withKube, "doctor")
	if code != 0 || !strings.Contains(out, "ok    KUBECONFIG matches the layout") {
		t.Fatalf("doctor with the layout's KUBECONFIG: %d %q", code, out)
	}
	// Profiles are checked too: a provider whose command is not on PATH is a problem.
	f := withProfiles(t, "")
	code, out, _ = runKcx(t, f.env, "doctor")
	if code != 0 || !strings.Contains(out, "ok    provider acme: fake-cli found") || !strings.Contains(out, "secret provider fake found") {
		t.Fatalf("doctor with profiles: %d %q", code, out)
	}
	if err := os.WriteFile(filepath.Join(f.env["KCX_PROFILES"], "zz.toml"), []byte("[provider.ghost]\ncommand = [\"no-such-cli\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runKcx(t, f.env, "doctor")
	if code != 1 || !strings.Contains(out, "FIX   provider ghost: no-such-cli is not on PATH") {
		t.Fatalf("doctor with a missing command: %d %q", code, out)
	}
}

func TestPickFallsBackToList(t *testing.T) {
	e, _ := fixture(t)
	// No terminal in tests: a bare kcx lists, like kcx ls.
	code, out, _ := runKcx(t, e, "ls")
	code2, out2, _ := runKcx(t, e)
	if code != 0 || code2 != 0 || out != out2 {
		t.Fatalf("bare kcx without a picker must list:\n%s\nvs\n%s", out2, out)
	}
}
