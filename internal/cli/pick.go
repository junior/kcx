package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

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

const pickFetch = "+ fetch / browse the profile clusters"

// cmdPick is `kcx` with no arguments: the picker when there is one, the list
// otherwise. The top lines browse the profiles (group, site, cluster, the way
// the clusters are organised) and make a temp cluster.
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
	var set *profile.Set
	if s, err := a.profiles(); err == nil && len(s.Names()) > 0 {
		set = s
		fetched := 0
		for _, n := range set.Names() {
			if _, err := os.Stat(a.lay.PermFile(n)); err == nil {
				fetched++
			}
		}
		lines = append([]string{fmt.Sprintf("%s\t%d clusters, %d fetched", pickFetch, len(set.Names()), fetched)}, lines...)
	}
	if _, err := lookPath("kind"); err == nil {
		lines = append([]string{pickTemp + "\ton any Docker engine"}, lines...)
	}
	pick, err := p.Pick(lines, "context> ", "switch context; the top lines browse the profiles or make a temp cluster; ESC cancels")
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
// than one), then cluster, with the fetched ones marked. ESC steps back, and at
// the first step cancels. It returns the chosen cluster's name, "" on a cancel.
func (a *App) browse(p picker.Picker, set *profile.Set) (string, error) {
	fetched := func(name string) bool {
		_, err := os.Stat(a.lay.PermFile(name))
		return err == nil
	}
	step, group, site := "group", "", ""
	for {
		switch step {
		case "group":
			pick, err := p.Pick(groupLines(set, fetched), "Group> ", "which group of clusters; ESC cancels")
			if err != nil || pick == "" {
				return "", err
			}
			group = pick
			if len(sitesOf(set, group)) > 1 {
				step = "site"
			} else {
				site = ""
				step = "cluster"
			}
		case "site":
			pick, err := p.Pick(siteLines(set, group, fetched), "Site> ", group+"; ESC goes back")
			if err != nil {
				return "", err
			}
			if pick == "" {
				step = "group"
				continue
			}
			site = pick
			step = "cluster"
		case "cluster":
			header := group
			if site != "" {
				header += " > " + site
			}
			pick, err := p.Pick(clusterLines(set, group, site, fetched), "Cluster> ", header+"; fetched ones switch, the others are fetched first; ESC goes back")
			if err != nil {
				return "", err
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

// groupLines lists the groups that have clusters, with counts, in a stable order:
// the order the profiles name them is not kept by TOML, so sorted by name.
func groupLines(set *profile.Set, fetched func(string) bool) []string {
	counts := map[string][2]int{}
	for _, n := range set.Names() {
		c := set.Clusters[n]
		v := counts[c.Group]
		v[0]++
		if fetched(n) {
			v[1]++
		}
		counts[c.Group] = v
	}
	groups := make([]string, 0, len(counts))
	for g := range counts {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	var lines []string
	for _, g := range groups {
		desc := fmt.Sprintf("%d clusters, %d fetched", counts[g][0], counts[g][1])
		if set.Groups[g].Readonly {
			desc += ", read-only"
		}
		lines = append(lines, g+"\t"+desc)
	}
	return lines
}

// sitesOf lists the sites of a group's clusters, sorted; a cluster without a site
// counts under "-".
func sitesOf(set *profile.Set, group string) []string {
	seen := map[string]bool{}
	for _, n := range set.InGroup(group) {
		s := set.Clusters[n].Site
		if s == "" {
			s = "-"
		}
		seen[s] = true
	}
	sites := make([]string, 0, len(seen))
	for s := range seen {
		sites = append(sites, s)
	}
	sort.Strings(sites)
	return sites
}

func siteLines(set *profile.Set, group string, fetched func(string) bool) []string {
	var lines []string
	for _, s := range sitesOf(set, group) {
		total, done := 0, 0
		for _, n := range set.InGroup(group) {
			cs := set.Clusters[n].Site
			if cs == "" {
				cs = "-"
			}
			if cs != s {
				continue
			}
			total++
			if fetched(n) {
				done++
			}
		}
		lines = append(lines, fmt.Sprintf("%s\t%d clusters, %d fetched", s, total, done))
	}
	return lines
}

// clusterLines lists the clusters of a group (and site, when given) with their
// state: "fetched" for the ones that have a file.
func clusterLines(set *profile.Set, group, site string, fetched func(string) bool) []string {
	var lines []string
	for _, n := range set.InGroup(group) {
		c := set.Clusters[n]
		cs := c.Site
		if cs == "" {
			cs = "-"
		}
		if site != "" && cs != site {
			continue
		}
		desc := ""
		if site == "" && c.Site != "" {
			desc = c.Site
		}
		if fetched(n) {
			if desc != "" {
				desc += "  "
			}
			desc += "fetched"
		}
		if len(c.Aliases) > 0 {
			if desc != "" {
				desc += "  "
			}
			desc += "(" + strings.Join(c.Aliases, ", ") + ")"
		}
		lines = append(lines, n+"\t"+desc)
	}
	return lines
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
