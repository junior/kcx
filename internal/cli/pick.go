package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/junior/kcx/internal/kubeconfig"
	"github.com/junior/kcx/internal/layout"
	"github.com/junior/kcx/internal/ns"
	"github.com/junior/kcx/internal/picker"
	"github.com/junior/kcx/internal/profile"
)

// sortStrings is sort.Strings, kept here so doctor.go needs no extra import.
func sortStrings(s []string) { sort.Strings(s) }

const (
	pickFetch   = "+ fetch / browse the profile clusters"
	markFetched = "\u2713" // a check mark: the cluster has its file
	markCurrent = "*"      // the current context
	sep         = "  |  "  // between the parts of a picker's header
)

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

// paint colours s the way a profile group asks, unless NO_COLOR is set.
func (a *App) paint(color, s string) string {
	if a.getenv("NO_COLOR") != "" {
		return s
	}
	return picker.Paint(color, s)
}

// fetched says whether a profile cluster has its file.
func (a *App) fetched(name string) bool {
	_, err := os.Stat(a.lay.PermFile(name))
	return err == nil
}

// pickContext offers the contexts in fzf and returns the one chosen, or "" on a
// cancel. Without a picker it returns an error that says to name the context.
func (a *App) pickContext(prompt, header string) (string, error) {
	p := a.picker()
	if !p.Available() {
		return "", usagef("name the context (no picker: fzf on PATH and a terminal are needed)")
	}
	rows, err := a.contextRows()
	if err != nil {
		return "", err
	}
	return p.PickLines(rows, prompt, header+sep+markCurrent+" current"+sep+"ESC cancels")
}

// contextRows are the picker's rows, one per context: its name, its group (or
// where its file lives), its site, and the current marker. A context that is a
// profile cluster's takes its group's colour and label.
func (a *App) contextRows() ([]picker.Line, error) {
	s := a.store()
	cur, err := s.Current()
	if err != nil {
		return nil, err
	}
	var set *profile.Set
	if ps, err := a.profiles(); err == nil {
		set = ps
	}
	var rows []picker.Line
	for _, f := range s.Files {
		if f == s.Shadow {
			continue // a kcx shell's or a run's copies of the contexts below
		}
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
			shown, place, site := n, "("+a.lay.Where(f)+")", ""
			if set != nil {
				if c, ok := set.Lookup(clusterNameOf(a.lay, f, n, len(names))); ok {
					g := set.Groups[c.Group]
					shown = a.paint(g.Color, n)
					place = a.paint(g.Color, g.Display())
					if set.Readonly(c) {
						place += ", read-only"
					}
					site = c.Site
				}
			}
			mark := ""
			if n == cur {
				mark = markCurrent
			}
			rows = append(rows, picker.Line{Value: n, Cols: []string{shown, place, site, mark}})
		}
	}
	return rows, nil
}

// clusterNameOf is the profile name a context may stand for: the file's name
// when the file is a permanent cluster's holding that one context (the file is
// the short name, whatever the context is called), else the context's own.
func clusterNameOf(lay layout.Layout, file, ctx string, contexts int) string {
	if contexts == 1 && filepath.Dir(file) == lay.Perm {
		return strings.TrimSuffix(filepath.Base(file), ".yaml")
	}
	return ctx
}

// cmdPick is `kcx` with no arguments: the picker when there is one, the list
// otherwise. The top line browses the profiles (group, site, cluster, the way
// the clusters are organised); the next makes a temp cluster.
func (a *App) cmdPick() error {
	p := a.picker()
	if !p.Available() {
		return a.cmdLs(nil)
	}
	if err := a.runGuard(); err != nil {
		return err
	}
	rows, err := a.contextRows()
	if err != nil {
		return err
	}
	header := "switch context"
	var top []picker.Line
	var set *profile.Set
	if s, err := a.profiles(); err == nil && len(s.Names()) > 0 {
		set = s
		total, done := fetchedCount(set, a.fetched)
		top = append(top, picker.Line{Value: pickFetch, Cols: []string{pickFetch, counts(total, done)}})
		header = "switch, or browse the profiles from the top line"
	}
	if _, err := lookPath("kind"); err == nil {
		top = append(top, picker.Line{Value: pickTemp, Cols: []string{pickTemp, "on any Docker engine"}})
	}
	rows = append(top, rows...)
	pick, err := p.PickLines(rows, "context> ", header+sep+markCurrent+" current"+sep+"ESC cancels")
	if err != nil {
		return err
	}
	switch pick {
	case "":
		return nil
	case pickTemp:
		return a.tempWizard()
	case pickFetch:
		name, err := a.browse(p, set)
		if err != nil || name == "" {
			return err
		}
		return a.switchTo(name, "", false, false)
	}
	return a.switchTo(pick, "", false, false)
}

// browse is the profile drill-down: group, then site (when the group spans more
// than one), then cluster, with the fetched ones and the current one marked.
// ESC steps back, and at the first step cancels; q quits from any step. It
// returns the chosen cluster's name, "" on a cancel or a quit.
func (a *App) browse(p picker.Picker, set *profile.Set) (string, error) {
	p.Quit = true
	current := ""
	if cur, err := a.store().Current(); err == nil && cur != "" {
		current = fileNameOf(a.lay, cur, cur)
	}
	done := func(err error) (string, error) {
		if errors.Is(err, picker.ErrQuit) {
			return "", nil
		}
		return "", err
	}
	step, group, site := "group", "", ""
	for {
		switch step {
		case "group":
			total, fetched := fetchedCount(set, a.fetched)
			pick, err := p.PickLines(groupRows(set, a.fetched, a.paint), "Group> ", counts(total, fetched)+sep+"ESC cancels  q quits")
			if err != nil || pick == "" {
				return done(err)
			}
			group = pick
			if len(sitesOf(set, group)) > 1 {
				step = "site"
			} else {
				site = ""
				step = "cluster"
			}
		case "site":
			pick, err := p.PickLines(siteRows(set, group, a.fetched), "Site> ", set.Groups[group].Display()+sep+"ESC back  q quits")
			if err != nil {
				return done(err)
			}
			if pick == "" {
				step = "group"
				continue
			}
			site = pick
			step = "cluster"
		case "cluster":
			crumbs := set.Groups[group].Display()
			shown := site
			if sites := sitesOf(set, group); shown == "" && len(sites) == 1 {
				shown = sites[0]
			}
			if shown != "" && shown != "-" {
				crumbs += " > " + shown
			}
			pick, err := p.PickLines(clusterRows(set, group, site, a.fetched, current, a.paint), "Cluster> ", crumbs+sep+markFetched+" fetched  "+markCurrent+" current"+sep+"ESC back  q quits")
			if err != nil {
				return done(err)
			}
			if pick == "" {
				if len(sitesOf(set, group)) > 1 {
					step = "site"
				} else {
					step = "group"
				}
				continue
			}
			return pick, nil
		}
	}
}

// fetchedCount counts the profile clusters and those that have their file.
func fetchedCount(set *profile.Set, fetched func(string) bool) (total, done int) {
	for _, n := range set.Names() {
		total++
		if fetched(n) {
			done++
		}
	}
	return total, done
}

// counts is "N clusters, F fetched".
func counts(total, done int) string {
	return fmt.Sprintf("%s, %d fetched", plural(total, "cluster"), done)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// groupRows lists the groups that have clusters, in profile order: the label
// in the group's colour, the counts, and the read-only flag.
func groupRows(set *profile.Set, fetched func(string) bool, paint func(color, s string) string) []picker.Line {
	var rows []picker.Line
	for _, g := range set.GroupNames() {
		names := set.InGroup(g)
		if len(names) == 0 {
			continue
		}
		done := 0
		for _, n := range names {
			if fetched(n) {
				done++
			}
		}
		grp := set.Groups[g]
		cols := []string{paint(grp.Color, grp.Display()), plural(len(names), "cluster"), fmt.Sprintf("%d fetched", done)}
		if grp.Readonly {
			cols = append(cols, "read-only")
		}
		rows = append(rows, picker.Line{Value: g, Cols: cols})
	}
	return rows
}

// sitesOf lists the sites of a group's clusters in the order they first appear;
// a cluster without a site counts under "-".
func sitesOf(set *profile.Set, group string) []string {
	var sites []string
	seen := map[string]bool{}
	for _, n := range set.InGroup(group) {
		s := siteOf(set.Clusters[n])
		if !seen[s] {
			seen[s] = true
			sites = append(sites, s)
		}
	}
	return sites
}

func siteOf(c *profile.Cluster) string {
	if c.Site == "" {
		return "-"
	}
	return c.Site
}

// siteRows lists a group's sites with their counts.
func siteRows(set *profile.Set, group string, fetched func(string) bool) []picker.Line {
	var rows []picker.Line
	for _, s := range sitesOf(set, group) {
		total, done := 0, 0
		for _, n := range set.InGroup(group) {
			if siteOf(set.Clusters[n]) != s {
				continue
			}
			total++
			if fetched(n) {
				done++
			}
		}
		rows = append(rows, picker.Line{Value: s, Cols: []string{s, plural(total, "cluster"), fmt.Sprintf("%d fetched", done)}})
	}
	return rows
}

// clusterRows lists the clusters of a group (of one site, when given) in
// profile order: the name in the group's colour, the fetched and current
// marks, and the aliases.
func clusterRows(set *profile.Set, group, site string, fetched func(string) bool, current string, paint func(color, s string) string) []picker.Line {
	color := set.Groups[group].Color
	var rows []picker.Line
	for _, n := range set.InGroup(group) {
		c := set.Clusters[n]
		if site != "" && siteOf(c) != site {
			continue
		}
		var marks []string
		if fetched(n) {
			marks = append(marks, markFetched)
		}
		if n == current {
			marks = append(marks, markCurrent)
		}
		aliases := ""
		if len(c.Aliases) > 0 {
			aliases = "(" + strings.Join(c.Aliases, ", ") + ")"
		}
		rows = append(rows, picker.Line{Value: n, Cols: []string{paint(color, n), strings.Join(marks, " "), aliases}})
	}
	return rows
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
	var rows []picker.Line
	for _, n := range list {
		mark := ""
		if n == current {
			mark = markCurrent
		}
		rows = append(rows, picker.Line{Value: n, Cols: []string{n, mark}})
	}
	choice, err := p.PickLines(rows, "namespace> ", "namespace of "+ctx+sep+markCurrent+" current"+sep+"ESC cancels")
	if errors.Is(err, picker.ErrUnavailable) || choice == "" {
		return err
	}
	if err != nil {
		return err
	}
	if err := a.setNamespace(files, ctx, choice); err != nil {
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
