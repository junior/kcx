package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/junior/kcx/internal/kubeconfig"
	"github.com/junior/kcx/internal/layout"
)

// store is what kcx itself reads: the layout, except inside a read-only shell or
// a run, whose KUBECONFIG is the point of them.
func (a *App) store() kubeconfig.Store {
	if a.getenv(EnvRO) != "" {
		if ro := a.getenv(EnvROConfig); ro != "" {
			return kubeconfig.Store{Files: []string{ro}}
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

// warn prints one of kcx's own lines on standard error without failing.
func (a *App) warn(format string, args ...any) {
	fmt.Fprintf(a.stderr, "kcx: "+format+"\n", args...)
}

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
	a.say("switched to %s", prev)
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
	quiet := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "-q":
		quiet = true
	default:
		return usagef("kcx ls takes only -q")
	}
	if ro := a.getenv(EnvRO); ro != "" {
		fmt.Fprintln(a.stdout, ro)
		return nil
	}
	s := a.store()
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
			mark := ""
			if n == cur {
				mark = "*"
			}
			rows = append(rows, row{mark, n, where, kubeconfig.ServerOf(cfg, n)})
			if f == a.lay.State {
				inbox++
			}
		}
		for _, e := range kubeconfig.Entries(cfg) {
			if other, seen := first[e]; seen {
				if !told[e] {
					told[e] = true
					a.warn("the %s %s is defined in %s and in %s; kubectl uses the first", e.Kind, e.Name, other, f)
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
		a.warn("no contexts yet: kcx add NAME FILE|-|-- CMD..., or kcx temp create NAME")
	} else {
		var lines [][]string
		for _, r := range rows {
			lines = append(lines, []string{r.mark, r.ctx, r.where, r.server})
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

// writeColumns aligns rows of cells, two spaces between columns.
func writeColumns(w io.Writer, rows [][]string) {
	var width []int
	for _, r := range rows {
		for i, c := range r {
			for len(width) <= i {
				width = append(width, 0)
			}
			if len(c) > width[i] {
				width[i] = len(c)
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
			b.WriteString(strings.Repeat(" ", width[i]-len(c)+2))
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
