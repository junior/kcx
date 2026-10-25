package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/junior/kcx/internal/kubeconfig"
	"github.com/junior/kcx/internal/ns"
	"github.com/junior/kcx/internal/picker"
	"github.com/junior/kcx/internal/profile"
)

// sortStrings is sort.Strings, kept here so doctor.go needs no extra import.
func sortStrings(s []string) { sort.Strings(s) }

func (a *App) picker() picker.Picker {
	p := picker.Picker{}
	if s, err := a.settings(); err == nil && s.Picker == "plain" {
		p.Plain = true
	}
	if f, ok := a.stdout.(*os.File); ok {
		p.Stdout = f
	}
	return p
}

// pickContext offers the contexts in fzf and returns the one chosen, or "" on a
// cancel. Without a picker it returns an error that says to name the context.
func (a *App) pickContext(prompt, header string) (string, error) {
	p := a.picker()
	if !p.Available() {
		return "", usagef("name the context (no picker: fzf on PATH and a terminal are needed)")
	}
	lines, err := a.contextLines()
	if err != nil {
		return "", err
	}
	return p.Pick(lines, prompt, header)
}

// contextLines are the picker's rows: "context<TAB>(where) [group]  current".
func (a *App) contextLines() ([]string, error) {
	s := a.store()
	cur, err := s.Current()
	if err != nil {
		return nil, err
	}
	var set *profile.Set
	if ps, err := a.profiles(); err == nil {
		set = ps
	}
	var lines []string
	for _, f := range s.Files {
		if _, err := os.Stat(f); err != nil {
			continue
		}
		cfg, err := kubeconfig.LoadFile(f)
		if err != nil {
			continue
		}
		names := make([]string, 0, len(cfg.Contexts))
		for n := range cfg.Contexts {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			desc := "(" + a.lay.Where(f) + ")"
			if set != nil {
				if c, ok := set.Lookup(fileNameOf(a.lay, n, n)); ok {
					desc += " " + c.Group
					if set.Readonly(c) {
						desc += ", read-only"
					}
				}
			}
			if n == cur {
				desc += "  current"
			}
			lines = append(lines, n+"\t"+desc)
		}
	}
	return lines, nil
}

const pickFetch = "+ fetch a cluster from a profile"

// cmdPick is `kcx` with no arguments: the picker when there is one, the list
// otherwise. The top line fetches a profile cluster that has no file yet.
func (a *App) cmdPick() error {
	p := a.picker()
	if !p.Available() {
		return a.cmdLs(nil)
	}
	if err := a.runGuard(); err != nil {
		return err
	}
	lines, err := a.contextLines()
	if err != nil {
		return err
	}
	var unfetched []*profile.Cluster
	if set, err := a.profiles(); err == nil {
		for _, n := range set.Names() {
			if _, err := os.Stat(a.lay.PermFile(n)); err != nil {
				unfetched = append(unfetched, set.Clusters[n])
			}
		}
	}
	if len(unfetched) > 0 {
		lines = append([]string{pickFetch + "\t" + fmt.Sprintf("%d not fetched yet", len(unfetched))}, lines...)
	}
	pick, err := p.Pick(lines, "context> ", "switch context; ESC cancels")
	if err != nil {
		return err
	}
	switch pick {
	case "":
		return nil
	case pickFetch:
		set, _ := a.profiles()
		var rows []string
		for _, c := range unfetched {
			desc := c.Group
			if c.Site != "" {
				desc += ", " + c.Site
			}
			if set.Readonly(c) {
				desc += ", read-only"
			}
			rows = append(rows, c.Name+"\t"+desc)
		}
		choice, err := p.Pick(rows, "fetch> ", "fetch which cluster? ESC goes back")
		if err != nil || choice == "" {
			return err
		}
		return a.switchTo(choice, "", false, true)
	}
	return a.switchTo(pick, "", false, false)
}

// pickNamespace is `kcx ns` with no argument on a terminal: the cluster's
// namespaces in fzf, the choice set.
func (a *App) pickNamespace(files []string, ctx string) error {
	p := a.picker()
	list, err := ns.List(context.Background(), files, ctx, apiTimeout)
	if err != nil {
		return err
	}
	if !p.Available() {
		for _, n := range list {
			fmt.Fprintln(a.stdout, n)
		}
		return nil
	}
	current, _ := ns.Of(files, ctx)
	var lines []string
	for _, n := range list {
		if n == current {
			lines = append(lines, n+"\tcurrent")
		} else {
			lines = append(lines, n)
		}
	}
	choice, err := p.Pick(lines, "namespace> ", "namespace of "+ctx+"; ESC cancels")
	if errors.Is(err, picker.ErrUnavailable) || choice == "" {
		return err
	}
	if err != nil {
		return err
	}
	if err := ns.Set(files, ctx, choice); err != nil {
		return err
	}
	if current != choice && a.getenv(EnvRO) == "" {
		if err := ns.WritePrevious(a.lay.NSPrev, ctx, current); err != nil {
			return err
		}
	}
	a.say("namespace of %s is %s", ctx, choice)
	return nil
}
