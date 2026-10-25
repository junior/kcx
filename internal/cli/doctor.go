package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/junior/kcx/internal/kubeconfig"
	"github.com/junior/kcx/internal/layout"
	"github.com/junior/kcx/internal/profile"
	"github.com/junior/kcx/internal/secret"
)

// cmdDoctor checks the layout, the files, the settings, the profiles and the
// programs kcx relies on, and says what to do about each finding. Problems exit
// 1; notes do not.
func (a *App) cmdDoctor(args []string) error {
	if len(args) != 0 {
		return usagef("kcx doctor takes no arguments")
	}
	problems := 0
	ok := func(format string, v ...any) { fmt.Fprintf(a.stdout, "ok    "+format+"\n", v...) }
	note := func(format string, v ...any) { fmt.Fprintf(a.stdout, "note  "+format+"\n", v...) }
	problem := func(format string, v ...any) {
		problems++
		fmt.Fprintf(a.stdout, "FIX   "+format+"\n", v...)
	}

	// The layout.
	for _, d := range []string{a.lay.Dir, a.lay.Perm, a.lay.Temp} {
		info, err := os.Stat(d)
		switch {
		case err != nil:
			note("%s does not exist yet; kcx creates it when needed", d)
		case !info.IsDir():
			problem("%s is not a directory", d)
		case info.Mode().Perm()&0o077 != 0:
			problem("%s is readable by others (mode %o); chmod 700 %s", d, info.Mode().Perm(), d)
		default:
			ok("%s is private", d)
		}
	}
	if info, err := os.Stat(a.lay.State); err != nil {
		note("%s does not exist yet (the state file); kcx NAME creates it", a.lay.State)
	} else if info.Mode().Perm()&0o077 != 0 {
		problem("%s is readable by others (mode %o); chmod 600 %s", a.lay.State, info.Mode().Perm(), a.lay.State)
	} else {
		ok("%s is the state file, private", a.lay.State)
	}
	for _, f := range append(layout.DirFiles(a.lay.Perm), layout.DirFiles(a.lay.Temp)...) {
		info, err := os.Stat(f)
		if err == nil && info.Mode().Perm()&0o077 != 0 {
			problem("%s is readable by others (mode %o); chmod 600 %s", f, info.Mode().Perm(), f)
		}
	}

	// What kubectl would quietly get wrong.
	first := map[kubeconfig.Entry]string{}
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
			problem("%s sets a current context of its own, which wins whenever the state file has none; kubectl --kubeconfig %s config unset current-context", f, f)
		}
		if f == a.lay.State && len(cfg.Contexts) > 0 {
			note("%s holds %d context(s) another tool wrote there; kcx adopt CTX [NAME] gives each a file of its own", f, len(cfg.Contexts))
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
	if k := a.getenv("KUBECONFIG"); k == "" {
		note("KUBECONFIG is not set in this shell; eval \"$(kcx init zsh)\" (or bash) sets it from the layout")
	} else if k != a.lay.Kubeconfig() && a.getenv(EnvRO) == "" && a.getenv(EnvRun) == "" {
		if a.getenv(EnvAuto) == "" {
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

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}
