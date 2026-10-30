package cli

import (
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"unicode"

	"github.com/junior/kcx/v2/internal/kubeconfig"
	"github.com/junior/kcx/v2/internal/layout"
	"github.com/junior/kcx/v2/internal/picker"
	"github.com/junior/kcx/v2/internal/profile"
)

// store is what kcx itself reads: the layout, except inside a read-only shell, a
// kcx shell or a run, whose own kubeconfig is the point of them. A kcx shell's
// and a run's private file comes first and is marked as the store's Shadow.
func (a *App) store() kubeconfig.Store {
	if a.getenv(EnvRO) != "" {
		if ro := a.getenv(EnvROConfig); ro != "" {
			return kubeconfig.Store{Files: []string{ro}}
		}
	}
	for _, v := range []string{EnvShell, EnvRunConfig} {
		if p := a.getenv(v); p != "" {
			return kubeconfig.Store{Files: append([]string{p}, a.lay.Files()...), Shadow: p}
		}
	}
	if a.getenv(EnvRO) != "" || a.getenv(EnvRun) != "" {
		if k := a.getenv("KUBECONFIG"); k != "" {
			return kubeconfig.Store{Files: strings.Split(k, string(os.PathListSeparator))}
		}
	}
	return kubeconfig.Store{Files: a.lay.Files()}
}

// say prints one of kcx's own lines on standard output.
func (a *App) say(format string, args ...any) {
	fmt.Fprintf(a.stdout, "kcx: "+format+"\n", args...)
}

// cmdUnset is `kcx --unset`: no current context, as kubectx -u does. The one
// that was current becomes the previous one, so `kcx -` brings it back. Not
// inside a kcx shell, which would then follow the shared context.
func (a *App) cmdUnset() error {
	if err := a.runGuard(); err != nil {
		return err
	}
	if a.getenv(EnvShell) != "" {
		return fmt.Errorf("a kcx shell always has a context of its own: kcx NAME switches it, exit leaves it")
	}
	cur, err := a.store().Current()
	if err != nil {
		return err
	}
	if cur == "" {
		a.say("there is no current context to unset")
		return nil
	}
	if err := kubeconfig.ClearCurrent(a.lay.State); err != nil {
		return err
	}
	if err := kubeconfig.WritePrevious(a.lay.Prev, cur); err != nil {
		return err
	}
	if still, err := a.store().Current(); err == nil && still != "" {
		a.warn("%s is still current: a cluster file sets it; kcx doctor --fix unsets it there", still)
		return nil
	}
	a.say("no current context now (it was %s; kcx - brings it back)", cur)
	return nil
}

// lsFilter builds the filter of `kcx ls --group G PATTERN`, nil when there is
// none. A context passes --group when its file is a profile cluster of group G
// (the file's short name, whatever the context is called), and PATTERN when its
// name contains it, or, with * ? or [ in it, matches it as a glob.
func (a *App) lsFilter(group, pattern string) (func(ctx, file string, contexts int) bool, error) {
	if group == "" && pattern == "" {
		return nil, nil
	}
	var set *profile.Set
	if group != "" {
		s, err := a.profiles()
		if err != nil {
			return nil, err
		}
		if _, ok := s.Groups[group]; !ok {
			return nil, fmt.Errorf("no group named %s in the profiles (kcx profile ls)", group)
		}
		set = s
	}
	glob := strings.ContainsAny(pattern, "*?[")
	if glob {
		if _, err := path.Match(pattern, ""); err != nil {
			return nil, usagef("%s is not a valid pattern: %v", pattern, err)
		}
	}
	return func(ctx, file string, contexts int) bool {
		if set != nil {
			c, ok := set.Lookup(clusterNameOf(a.lay, file, ctx, contexts))
			if !ok || c.Group != group {
				return false
			}
		}
		switch {
		case pattern == "":
			return true
		case glob:
			ok, _ := path.Match(pattern, ctx)
			return ok
		default:
			return strings.Contains(ctx, pattern)
		}
	}, nil
}

// lsNames prints the names of the contexts that pass keep, sorted, each once:
// `kcx ls -q` with a filter.
func (a *App) lsNames(s kubeconfig.Store, keep func(ctx, file string, contexts int) bool) error {
	seen := map[string]bool{}
	var out []string
	for _, f := range s.Files {
		if f == s.Shadow {
			continue
		}
		if _, err := os.Stat(f); err != nil {
			continue
		}
		cfg, err := kubeconfig.LoadFile(f)
		if err != nil {
			continue
		}
		for n := range cfg.Contexts {
			if !seen[n] && keep(n, f, len(cfg.Contexts)) {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	for _, n := range out {
		fmt.Fprintln(a.stdout, n)
	}
	return nil
}

// switched confirms a switch, saying when it was a kcx shell's own.
func (a *App) switched(ctx string) {
	if a.getenv(EnvShell) != "" {
		a.say("switched to %s in this kcx shell", ctx)
		return
	}
	a.say("switched to %s", ctx)
}

// warn prints one of kcx's own lines on standard error without failing.
func (a *App) warn(format string, args ...any) {
	fmt.Fprintf(a.stderr, "kcx: "+format+"\n", args...)
}

// printable is s with control and format characters escaped (\x1b, \u202e),
// for names and addresses read from kubeconfig files on their way to a
// terminal: a context named with an escape sequence must not drive the
// terminal, nor one with a direction override show as another name.
func printable(s string) string {
	if strings.IndexFunc(s, unprintable) < 0 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case !unprintable(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

func unprintable(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }

// runGuard refuses a switch inside `kcx run`: the context there is fixed for the
// whole command.
func (a *App) runGuard() error {
	if r := a.getenv(EnvRun); r != "" {
		return fmt.Errorf("inside kcx run the context is %s for the whole command; switch outside it", r)
	}
	return nil
}

// resolve turns what the user typed into a context name: the file
// configs/NAME.yaml (whatever its context is called), a temp file NAME, a
// context NAME, or kind-NAME.
func (a *App) resolve(name string) (string, error) {
	if ctx, ok := a.fileContext(a.lay.PermFile(name)); ok {
		return ctx, nil
	}
	for _, f := range layout.DirFiles(a.lay.Temp) {
		if n, _ := layout.SplitTemp(f); n == name {
			if ctx, ok := a.fileContext(f); ok {
				return ctx, nil
			}
		}
	}
	ctxs, err := a.store().Contexts()
	if err != nil {
		return "", err
	}
	for _, candidate := range []string{name, "kind-" + name} {
		for _, c := range ctxs {
			if c == candidate {
				return c, nil
			}
		}
	}
	return "", fmt.Errorf("no context named %s (kcx ls lists them)", name)
}

// fileContext returns the one context a managed file defines.
func (a *App) fileContext(path string) (string, bool) {
	if _, err := os.Stat(path); err != nil {
		return "", false
	}
	ctxs, err := kubeconfig.ContextsIn(path)
	if err != nil || len(ctxs) != 1 {
		return "", false
	}
	return ctxs[0], true
}

// use switches to ctx, remembering the context we came from for `kcx -`.
func (a *App) use(ctx string) error {
	cur, err := a.store().Current()
	if err != nil {
		return err
	}
	if private := a.getenv(EnvShell); private != "" {
		// Inside a kcx shell a switch is the shell's: its private file gets the
		// context (a copy, if it has none yet) and the current context; the
		// state file the other shells share is left alone.
		if err := a.shellCopy(private, ctx, true); err != nil {
			return err
		}
		if cur != "" && cur != ctx {
			return kubeconfig.WritePrevious(a.lay.Prev, cur)
		}
		return nil
	}
	if err := a.lay.Ensure(); err != nil {
		return err
	}
	if err := kubeconfig.SetCurrent(a.lay.State, ctx); err != nil {
		return err
	}
	if cur != "" && cur != ctx {
		if err := kubeconfig.WritePrevious(a.lay.Prev, cur); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) cmdPrevious() error {
	if err := a.runGuard(); err != nil {
		return err
	}
	prev := kubeconfig.ReadPrevious(a.lay.Prev)
	if prev == "" {
		return fmt.Errorf("no previous context recorded yet")
	}
	ctxs, err := a.store().Contexts()
	if err != nil {
		return err
	}
	if !containsString(ctxs, prev) {
		return fmt.Errorf("the previous context, %s, no longer exists", prev)
	}
	if err := a.use(prev); err != nil {
		return err
	}
	a.switched(prev)
	return nil
}

func (a *App) cmdCurrent() error {
	cur, err := a.store().Current()
	if err != nil {
		return err
	}
	if cur == "" {
		return fmt.Errorf("no current context (kcx NAME picks one)")
	}
	fmt.Fprintln(a.stdout, cur)
	return nil
}

// cmdLs lists every context: where its file lives and its server, and on
// standard error what kubectl would quietly get wrong.
func (a *App) cmdLs(args []string) error {
	quiet, group, pattern := false, "", ""
	const usage = "kcx ls [-q] [--group G] [PATTERN]"
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-q":
			quiet = true
		case arg == "--group":
			if i+1 >= len(args) {
				return usagef("--group needs a group")
			}
			i++
			group = args[i]
		case strings.HasPrefix(arg, "--group="):
			group = strings.TrimPrefix(arg, "--group=")
		case strings.HasPrefix(arg, "-"):
			return usagef(usage)
		default:
			if pattern != "" {
				return usagef(usage)
			}
			pattern = arg
		}
	}
	if ro := a.getenv(EnvRO); ro != "" {
		fmt.Fprintln(a.stdout, ro)
		return nil
	}
	keep, err := a.lsFilter(group, pattern)
	if err != nil {
		return err
	}
	s := a.store()
	if quiet && keep != nil {
		return a.lsNames(s, keep)
	}
	if quiet {
		ctxs, err := s.Contexts()
		if err != nil {
			return err
		}
		for _, c := range ctxs {
			fmt.Fprintln(a.stdout, c)
		}
		return nil
	}
	cur, err := s.Current()
	if err != nil {
		return err
	}
	type row struct{ mark, ctx, where, server string }
	var rows []row
	first := map[kubeconfig.Entry]string{}
	told := map[kubeconfig.Entry]bool{}
	inbox := 0
	var stray []string
	for _, f := range s.Files {
		if f == s.Shadow {
			continue // copies of contexts listed below, and the shell's current one
		}
		if _, err := os.Stat(f); err != nil {
			continue
		}
		cfg, err := kubeconfig.LoadFile(f)
		if err != nil {
			a.warn("%s", err)
			continue
		}
		where := a.lay.Where(f)
		names := make([]string, 0, len(cfg.Contexts))
		for n := range cfg.Contexts {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if f == a.lay.State {
				inbox++
			}
			if keep != nil && !keep(n, f, len(names)) {
				continue
			}
			mark := ""
			if n == cur {
				mark = "*"
			}
			rows = append(rows, row{mark, n, where, kubeconfig.ServerOf(cfg, n)})
		}
		for _, e := range kubeconfig.Entries(cfg) {
			if other, seen := first[e]; seen {
				if !told[e] {
					told[e] = true
					a.warn("the %s %s is defined in %s and in %s; kubectl uses the first", e.Kind, printable(e.Name), other, f)
				}
			} else {
				first[e] = f
			}
		}
		if f != a.lay.State && cfg.CurrentContext != "" {
			stray = append(stray, f)
		}
	}
	if len(rows) == 0 {
		if keep != nil {
			a.warn("no context matches")
		} else {
			a.warn("no contexts yet: kcx add NAME FILE|-|-- CMD..., or kcx temp create NAME")
		}
	} else {
		var lines [][]string
		for _, r := range rows {
			lines = append(lines, []string{r.mark, printable(r.ctx), r.where, printable(r.server)})
		}
		writeColumns(a.stdout, lines)
	}
	if inbox > 0 {
		a.warn("~/.kube/config holds %d context(s) another tool wrote there: kcx adopt CTX [NAME] gives one a file of its own", inbox)
	}
	if len(stray) > 0 {
		a.warn("these files set a current context of their own, which wins whenever the state file has none; clear it with kubectl --kubeconfig FILE config unset current-context:\n  %s", strings.Join(stray, "\n  "))
	}
	for _, dir := range []string{a.lay.Perm, a.lay.Temp} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var other []string
		for _, e := range entries {
			n := e.Name()
			if strings.HasPrefix(n, ".") || strings.HasSuffix(n, ".yaml") {
				continue
			}
			other = append(other, n)
		}
		if len(other) > 0 {
			a.warn("not in KUBECONFIG (kcx lists *.yaml files only): %s: %s", dir, strings.Join(other, " "))
		}
	}
	return nil
}

// writeColumns aligns rows of cells, two spaces between columns; a cell's
// colour codes take no room.
func writeColumns(w io.Writer, rows [][]string) {
	var width []int
	for _, r := range rows {
		for i, c := range r {
			for len(width) <= i {
				width = append(width, 0)
			}
			if n := picker.Width(c); n > width[i] {
				width[i] = n
			}
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			if i == len(r)-1 {
				b.WriteString(c)
				break
			}
			b.WriteString(c)
			b.WriteString(strings.Repeat(" ", width[i]-picker.Width(c)+2))
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

// hintNew says what works at once after a file appeared: a shell started before
// it does not list it, so kubectl --context fails there until KUBECONFIG is
// recomputed; `kcx run` works regardless.
func (a *App) hintNew(ctx, file string) {
	inherited := a.getenv("KUBECONFIG")
	for _, f := range strings.Split(inherited, string(os.PathListSeparator)) {
		if f == file {
			return
		}
	}
	a.say("kcx run %s -- CMD works now; kubectl --context %s once KUBECONFIG lists the file: in a new shell, at the next zsh prompt, or after eval \"$(kcx env)\"", ctx, ctx)
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// managedContexts returns the contexts a file defines, for checks against the
// current context.
func managedContexts(path string) []string {
	ctxs, _ := kubeconfig.ContextsIn(path)
	return ctxs
}
