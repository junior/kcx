package cli

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/junior/kcx/v2/internal/kubeconfig"
	"github.com/junior/kcx/v2/internal/layout"
	"github.com/junior/kcx/v2/internal/profile"
	"github.com/junior/kcx/v2/internal/secret"
)

// cmdDoctor checks the layout, the files, the settings, the profiles and the
// programs kcx relies on, and says what to do about each finding. Problems exit
// 1; notes do not. --fix first repairs what needs no decision: the modes of the
// layout's directories and files, and a current context a cluster file sets of
// its own. Findings of one kind across many files fold into one line.
func (a *App) cmdDoctor(args []string) error {
	fix := false
	for _, arg := range args {
		if arg != "--fix" {
			return usagef("kcx doctor [--fix]")
		}
		fix = true
	}
	if fix {
		if err := a.doctorFix(); err != nil {
			return err
		}
	}
	problems := 0
	ok := func(format string, v ...any) { fmt.Fprintf(a.stdout, "ok    "+format+"\n", v...) }
	note := func(format string, v ...any) { fmt.Fprintf(a.stdout, "note  "+format+"\n", v...) }
	problem := func(format string, v ...any) {
		problems++
		fmt.Fprintf(a.stdout, "FIX   "+format+"\n", v...)
	}

	// The layout. File modes mean nothing on Windows, where the profile
	// directory's permissions keep ~/.kube private.
	unixModes := runtime.GOOS != "windows"
	for _, d := range []string{a.lay.Dir, a.lay.Perm, a.lay.Temp} {
		info, err := os.Stat(d)
		switch {
		case err != nil:
			note("%s does not exist yet; kcx creates it when needed", d)
		case !info.IsDir():
			problem("%s is not a directory", d)
		case unixModes && info.Mode().Perm()&0o077 != 0:
			problem("%s is readable by others (mode %o); chmod 700 %s, or kcx doctor --fix", d, info.Mode().Perm(), d)
		default:
			ok("%s is private", d)
		}
	}
	if info, err := os.Stat(a.lay.State); err != nil {
		note("%s does not exist yet (the state file); kcx NAME creates it", a.lay.State)
	} else if unixModes && info.Mode().Perm()&0o077 != 0 {
		problem("%s is readable by others (mode %o); chmod 600 %s, or kcx doctor --fix", a.lay.State, info.Mode().Perm(), a.lay.State)
	} else {
		ok("%s is the state file, private", a.lay.State)
	}
	var loose []string
	for _, f := range append(layout.DirFiles(a.lay.Perm), layout.DirFiles(a.lay.Temp)...) {
		info, err := os.Stat(f)
		if err == nil && unixModes && info.Mode().Perm()&0o077 != 0 {
			if len(loose) < 3 {
				problem("%s is readable by others (mode %o); chmod 600 %s, or kcx doctor --fix", f, info.Mode().Perm(), f)
			}
			loose = append(loose, f)
		}
	}
	if len(loose) > 3 {
		problem("... %d files in all are readable by others; chmod 600 them, or kcx doctor --fix", len(loose))
	}

	// What kubectl would quietly get wrong.
	first := map[kubeconfig.Entry]string{}
	var own []string
	for _, f := range a.lay.Files() {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		cfg, err := kubeconfig.LoadFile(f)
		if err != nil {
			problem("%s does not load: %v", f, err)
			continue
		}
		for _, e := range kubeconfig.Entries(cfg) {
			if other, seen := first[e]; seen {
				problem("the %s %s is defined in %s and in %s; kubectl uses the first: rename one (kcx mv)", e.Kind, e.Name, other, f)
			} else {
				first[e] = f
			}
		}
		if f != a.lay.State && cfg.CurrentContext != "" {
			own = append(own, f)
		}
		if f == a.lay.State && len(cfg.Contexts) > 0 {
			note("%s holds %d context(s) another tool wrote there; kcx adopt CTX [NAME] gives each a file of its own", f, len(cfg.Contexts))
		}
	}
	switch {
	case len(own) > 3:
		problem("%d files set a current context of their own, which wins whenever the state file has none (%s); kcx doctor --fix unsets them, kubectl --kubeconfig FILE config unset current-context does one", len(own), few(own))
	case len(own) > 0:
		for _, f := range own {
			problem("%s sets a current context of its own, which wins whenever the state file has none; kubectl --kubeconfig %s config unset current-context, or kcx doctor --fix", f, f)
		}
	}
	for _, dir := range []string{a.lay.Perm, a.lay.Temp} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			n := e.Name()
			if !strings.HasPrefix(n, ".") && !strings.HasSuffix(n, ".yaml") {
				note("%s/%s is not a .yaml file, so KUBECONFIG does not list it", dir, n)
			}
		}
	}

	// The shell.
	want, auto := a.lay.Kubeconfig(), EnvAuto
	if private := a.getenv(EnvShell); private != "" {
		want, auto = private+string(os.PathListSeparator)+want, EnvShellAuto
	}
	if k := a.getenv("KUBECONFIG"); k == "" {
		note("KUBECONFIG is not set in this shell; eval \"$(kcx init zsh)\" (or bash) sets it from the layout")
	} else if k != want && a.getenv(EnvRO) == "" && a.getenv(EnvRun) == "" {
		if a.getenv(auto) == "" {
			note("KUBECONFIG was set by something other than kcx's shell integration; kcx env prints the layout's value")
		} else {
			note("KUBECONFIG is behind the layout (a file was added or removed); the next prompt, or eval \"$(kcx env)\", catches up")
		}
	} else {
		ok("KUBECONFIG matches the layout")
	}

	// Settings and profiles.
	if s, err := a.settings(); err != nil {
		problem("%v", err)
	} else {
		ok("settings: lab_node=%v secret_cache=%v probe=%v picker=%s", s.LabNode, s.SecretCache, s.Probe, s.Picker)
	}
	files, err := profile.Files(a.profileSources())
	if err != nil {
		problem("profiles: %v", err)
	} else if len(files) == 0 {
		note("no profiles in %s; kcx fetch has nothing to fetch with (that is fine for a machine without a cluster list)", strings.Join(a.profileSources(), ":"))
	} else if set, err := profile.Load(files); err != nil {
		problem("%v", err)
	} else {
		ok("profiles: %d cluster(s), %d group(s), %d provider(s) in %s", len(set.Clusters), len(set.Groups), len(set.Providers), strings.Join(files, " "))
		for _, name := range sortedKeys(set.Providers) {
			p := set.Providers[name]
			if p.Kind == "command" && len(p.Command) > 0 {
				if _, err := exec.LookPath(p.Command[0]); err != nil {
					problem("provider %s: %s is not on PATH", name, p.Command[0])
				} else {
					ok("provider %s: %s found", name, p.Command[0])
				}
			}
			if p.Secret != "" {
				if _, err := secret.Resolve(p.Secret, nil, nil); err != nil {
					problem("provider %s: %v", name, err)
				} else {
					ok("provider %s: secret provider %s found", name, p.Secret)
				}
			}
		}
	}

	// Programs.
	for _, prog := range []string{"kubectl", "fzf"} {
		if _, err := exec.LookPath(prog); err != nil {
			note("%s is not on PATH", prog)
		} else {
			ok("%s found", prog)
		}
	}
	if problems > 0 {
		return fmt.Errorf("%d problem(s) to fix", problems)
	}
	return nil
}

// doctorFix repairs what doctor can without a decision: private modes on the
// layout's directories and files, and no current context in a cluster file
// (the state file alone holds it). Each repair is printed; the report that
// follows shows what is left. A file is rewritten the way kubectl would, so
// comments and key order in it do not survive.
func (a *App) doctorFix() error {
	fixed := func(format string, v ...any) { fmt.Fprintf(a.stdout, "fixed "+format+"\n", v...) }
	unixModes := runtime.GOOS != "windows"
	for _, d := range []string{a.lay.Dir, a.lay.Perm, a.lay.Temp} {
		info, err := os.Stat(d)
		if err != nil || !info.IsDir() || !unixModes || info.Mode().Perm()&0o077 == 0 {
			continue
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
		fixed("%s: mode 700", d)
	}
	files := append([]string{a.lay.State}, layout.DirFiles(a.lay.Perm)...)
	files = append(files, layout.DirFiles(a.lay.Temp)...)
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil || !unixModes || info.Mode().Perm()&0o077 == 0 {
			continue
		}
		if err := os.Chmod(f, 0o600); err != nil {
			return err
		}
		fixed("%s: mode 600", f)
	}
	for _, f := range a.lay.Files() {
		if f == a.lay.State {
			continue
		}
		if _, err := os.Stat(f); err != nil {
			continue
		}
		cfg, err := kubeconfig.LoadFile(f)
		if err != nil || cfg.CurrentContext == "" {
			continue
		}
		cfg.CurrentContext = ""
		if err := kubeconfig.WriteFile(f, cfg); err != nil {
			return err
		}
		fixed("%s: its own current context unset", f)
	}
	return nil
}

// few names up to three of a list, and counts the rest.
func few(list []string) string {
	if len(list) <= 3 {
		return strings.Join(list, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(list[:3], ", "), len(list)-3)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}
