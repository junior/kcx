package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/junior/kcx/v2/internal/config"
	"github.com/junior/kcx/v2/internal/fetch"
	"github.com/junior/kcx/v2/internal/kubeconfig"
	"github.com/junior/kcx/v2/internal/layout"
	"github.com/junior/kcx/v2/internal/ns"
	"github.com/junior/kcx/v2/internal/profile"
	"github.com/junior/kcx/v2/internal/secret"
)

// probeTimeout bounds the authenticated probe before a switch.
const probeTimeout = 2 * time.Second

// apiTimeout bounds a namespace list or check.
const apiTimeout = 10 * time.Second

// settings loads ~/.config/kcx/config.toml once.
func (a *App) settings() (config.Settings, error) {
	if a.cfg != nil {
		return *a.cfg, nil
	}
	s, err := config.Load(a.lay.Config)
	if err != nil {
		return s, err
	}
	a.cfg = &s
	return s, nil
}

// labNode says whether this machine holds no permanent credentials: the settings
// file, or KCX_LAB_NODE=1.
func (a *App) labNode() bool {
	if a.getenv("KCX_LAB_NODE") == "1" {
		return true
	}
	s, err := a.settings()
	return err == nil && s.LabNode
}

// profileSources are the files and directories profiles come from: KCX_PROFILES
// (a colon-separated list) or ~/.config/kcx/profiles.
func (a *App) profileSources() []string {
	if v := a.getenv("KCX_PROFILES"); v != "" {
		return strings.Split(v, string(os.PathListSeparator))
	}
	return []string{a.lay.Profiles}
}

// profiles loads and merges the profiles once.
func (a *App) profiles() (*profile.Set, error) {
	if a.set != nil {
		return a.set, nil
	}
	files, err := profile.Files(a.profileSources())
	if err != nil {
		return nil, err
	}
	set, err := profile.Load(files)
	if err != nil {
		return nil, err
	}
	a.set = set
	return set, nil
}

// interactive says a human is at the keyboard: standard input is a terminal.
func (a *App) interactive() bool {
	if f, ok := a.stdin.(*os.File); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	return false
}

func (a *App) runner(secretCache bool) *fetch.Runner {
	return &fetch.Runner{
		Stdin: a.stdin, Stdout: a.stdout, Stderr: a.stderr,
		Interactive: a.interactive(), SecretCache: secretCache,
		Getenv: a.getenv,
	}
}

// fetchCluster obtains c's kubeconfig through its provider and imports it as
// c.Name, replacing an existing file (a refresh). It returns the context name.
func (a *App) fetchCluster(set *profile.Set, c *profile.Cluster, user string) (string, error) {
	if err := a.labGuard(); err != nil {
		return "", err
	}
	s, err := a.settings()
	if err != nil {
		return "", err
	}
	work, err := os.MkdirTemp("", "kcx.")
	if err != nil {
		return "", fmt.Errorf("mktemp failed: %w", err)
	}
	defer os.RemoveAll(work)
	out := filepath.Join(work, "kubeconfig")
	if err := a.runner(s.SecretCache).Fetch(context.Background(), fetch.Request{Set: set, Cluster: c, User: user, Out: out}); err != nil {
		return "", err
	}
	return a.importFetched(set, c, out)
}

// importFetched imports the kubeconfig a provider wrote into the cluster's file.
func (a *App) importFetched(set *profile.Set, c *profile.Cluster, out string) (string, error) {
	p := set.ProviderOf(c)
	opts := kubeconfig.ImportOptions{
		Rename: true, ContextName: p.ContextName, UserName: p.UserName,
		Replace: true, TrashOld: false,
	}
	cfg, err := kubeconfig.Prepare(out, c.Name, opts)
	if err != nil {
		return "", err
	}
	if err := a.lay.Ensure(); err != nil {
		return "", err
	}
	dest := a.lay.PermFile(c.Name)
	ctx, err := kubeconfig.Import(cfg, dest, a.lay.Files(), a.lay.State, a.getenv, opts)
	if err != nil {
		return "", err
	}
	a.say("fetched %s (%s)", c.Name, dest)
	return ctx, nil
}

// readonlyWarning tells the user that a cluster defaults to read-only for run
// and shell, in red when standard error is a terminal.
func (a *App) readonlyWarning(set *profile.Set, c *profile.Cluster) {
	if !set.Readonly(c) {
		return
	}
	msg := fmt.Sprintf("kcx: %s is read-only by default (group %s): kcx run and kcx shell use the read-only proxy unless --rw", c.Name, c.Group)
	if f, ok := a.stderr.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		msg = "\x1b[31m" + msg + "\x1b[0m"
	}
	fmt.Fprintln(a.stderr, msg)
}

// cmdSwitchArgs parses `kcx NAME [-u USER] [--refresh] [--no-probe]`.
func (a *App) cmdSwitchArgs(args []string) error {
	name, user, refresh, noProbe := "", "", false, false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-u":
			if i+1 >= len(args) {
				return usagef("-u needs a value")
			}
			i++
			user = args[i]
		case strings.HasPrefix(arg, "-u") && len(arg) > 2:
			user = arg[2:]
		case arg == "--refresh":
			refresh = true
		case arg == "--no-probe":
			noProbe = true
		case strings.HasPrefix(arg, "-"):
			return usagef("unknown option %s", arg)
		default:
			if name != "" {
				return usagef("kcx NAME switches; other commands: kcx -h")
			}
			name = arg
		}
	}
	if name == "" {
		return usagef("kcx NAME switches; other commands: kcx -h")
	}
	return a.switchTo(name, user, refresh, noProbe)
}

// switchTo is `kcx NAME`: switch; fetch first when NAME is only in a profile or
// its credentials no longer work; --refresh fetches regardless. Inside a kcx
// shell the switch is the shell's own (see use).
func (a *App) switchTo(name, user string, refresh, noProbe bool) error {
	if err := a.runGuard(); err != nil {
		return err
	}
	ctx, c, set, err := a.prepare(name, user, refresh, noProbe)
	if err != nil {
		return err
	}
	if err := a.use(ctx); err != nil {
		return err
	}
	a.switched(ctx)
	if c != nil {
		a.readonlyWarning(set, c)
	}
	return nil
}

// prepare resolves NAME for a switch or a kcx shell: a context, or a profile
// cluster, fetched first when its file is missing or its credentials no longer
// work (a 401, or a failure in the credential layer); refresh fetches
// regardless, noProbe skips the check. c is NAME's profile cluster, nil for a
// context no profile knows.
func (a *App) prepare(name, user string, refresh, noProbe bool) (string, *profile.Cluster, *profile.Set, error) {
	ctx, resolveErr := a.resolve(name)
	var set *profile.Set
	var c *profile.Cluster
	if s, err := a.profiles(); err == nil {
		set = s
		c, _ = set.Lookup(name)
		if c == nil && ctx != "" {
			// The context exists; the profile may know the file's cluster by its name.
			c, _ = set.Lookup(fileNameOf(a.lay, ctx, name))
		}
	} else if resolveErr != nil {
		return "", nil, nil, fmt.Errorf("%v; and the profiles cannot be read: %v", resolveErr, err)
	}
	switch {
	case c == nil && resolveErr != nil:
		return "", nil, nil, resolveErr
	case c == nil:
		return ctx, nil, set, nil // not a profile cluster: nothing to fetch or probe
	}
	needFetch := refresh || resolveErr != nil
	var probeErr error
	if !needFetch && !noProbe {
		if s, err := a.settings(); err == nil && s.Probe {
			switch r, err := ns.Probe(context.Background(), a.lay.Files(), ctx, probeTimeout); {
			case r == ns.ProbeUnauthorized:
				a.say("the credentials of %s no longer work; fetching", c.Name)
				needFetch = true
			case r == ns.ProbeCredentials:
				// A refresh token the platform rotated or revoked, or a credential
				// plugin that failed: what a fresh fetch repairs, as a 401 is. A
				// refresh that works never gets here.
				a.say("the credentials of %s failed (%v); fetching", c.Name, err)
				needFetch, probeErr = true, err
			case err != nil || r == ns.ProbeUnreachable:
				a.warn("%s did not answer the probe (%v); switching anyway", c.Name, err)
			}
		}
	}
	if needFetch {
		var err error
		ctx, err = a.fetchCluster(set, c, user)
		if err != nil {
			if probeErr != nil {
				return "", nil, nil, fmt.Errorf("%s: the credentials failed (%v) and fetching again failed: %w", c.Name, probeErr, err)
			}
			return "", nil, nil, err
		}
	}
	return ctx, c, set, nil
}

// fileNameOf returns the short name of the file that defines ctx under configs/,
// or name when ctx is not a managed file's context.
func fileNameOf(lay layout.Layout, ctx, name string) string {
	for _, f := range layout.DirFiles(lay.Perm) {
		ctxs, err := kubeconfig.ContextsIn(f)
		if err == nil && len(ctxs) == 1 && ctxs[0] == ctx {
			return strings.TrimSuffix(filepath.Base(f), ".yaml")
		}
	}
	return name
}

// cmdFetch is `kcx fetch NAME... | --all | --group G | --missing [-u USER] [--use]`.
func (a *App) cmdFetch(args []string) error {
	var names []string
	user, all, missing, use := "", false, false, false
	var groups []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-u":
			if i+1 >= len(args) {
				return usagef("-u needs a value")
			}
			i++
			user = args[i]
		case arg == "--all":
			all = true
		case arg == "--missing":
			missing = true
		case arg == "--use":
			use = true
		case arg == "--group":
			if i+1 >= len(args) {
				return usagef("--group needs a value")
			}
			i++
			groups = append(groups, args[i])
		case strings.HasPrefix(arg, "--group="):
			groups = append(groups, strings.TrimPrefix(arg, "--group="))
		case strings.HasPrefix(arg, "-"):
			return usagef("unknown option %s", arg)
		default:
			names = append(names, arg)
		}
	}
	if len(names) == 0 && !all && !missing && len(groups) == 0 {
		return usagef("kcx fetch NAME... | --all | --group G | --missing")
	}
	set, err := a.profiles()
	if err != nil {
		return err
	}
	if len(set.Names()) == 0 {
		return fmt.Errorf("no profiles in %s: nothing to fetch with (kcx profile ls)", strings.Join(a.profileSources(), ":"))
	}
	var clusters []*profile.Cluster
	seen := map[string]bool{}
	add := func(c *profile.Cluster) {
		if !seen[c.Name] {
			seen[c.Name] = true
			clusters = append(clusters, c)
		}
	}
	for _, n := range names {
		c, ok := set.Lookup(n)
		if !ok {
			return fmt.Errorf("no cluster named %s in the profiles (kcx profile ls)", n)
		}
		add(c)
	}
	for _, g := range groups {
		if _, ok := set.Groups[g]; !ok {
			return fmt.Errorf("no group named %s in the profiles", g)
		}
		for _, n := range set.InGroup(g) {
			add(set.Clusters[n])
		}
	}
	if all || missing {
		for _, n := range set.Names() {
			_, err := os.Stat(a.lay.PermFile(n))
			exists := err == nil
			if (all && exists) || (missing && !exists) {
				add(set.Clusters[n])
			}
		}
	}
	if len(clusters) == 0 {
		a.say("nothing to fetch")
		return nil
	}
	if use && len(clusters) != 1 {
		return usagef("--use goes with one cluster")
	}
	if err := a.labGuard(); err != nil {
		return err
	}
	s, err := a.settings()
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "kcx.")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	outFor := func(c *profile.Cluster) string { return filepath.Join(work, c.Name+".kubeconfig") }
	var lastCtx string
	done, failed, aborted := a.runner(s.SecretCache).Many(context.Background(), set, clusters, user, outFor, func(c *profile.Cluster) error {
		ctx, err := a.importFetched(set, c, outFor(c))
		if err != nil {
			return err
		}
		lastCtx = ctx
		return nil
	})
	names = nil
	for n := range failed {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		a.warn("%s: %v", n, failed[n])
	}
	if len(clusters) > 1 || len(failed) > 0 {
		a.say("fetched %d of %d", len(done), len(clusters))
	}
	if use && len(done) == 1 {
		if err := a.runGuard(); err != nil {
			return err
		}
		if err := a.use(lastCtx); err != nil {
			return err
		}
		a.switched(lastCtx)
		a.readonlyWarning(set, clusters[0])
	}
	if aborted || len(failed) > 0 {
		return fmt.Errorf("%d fetch(es) failed", len(failed))
	}
	return nil
}

// cmdProfile is `kcx profile ls|check`.
func (a *App) cmdProfile(args []string) error {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "ls", "list", "":
		if len(args) > 1 {
			return usagef("kcx profile ls takes no arguments")
		}
		set, err := a.profiles()
		if err != nil {
			return err
		}
		if len(set.Names()) == 0 {
			a.warn("no profiles in %s", strings.Join(a.profileSources(), ":"))
			return nil
		}
		paint := func(_, s string) string { return s }
		if a.colourful() {
			paint = a.paint
		}
		writeColumns(a.stdout, profileRows(set, a.fetched, paint))
		return nil
	case "check":
		files, err := profile.Files(a.profileSources())
		if err != nil {
			return err
		}
		set, err := profile.Load(files)
		if err != nil {
			return err
		}
		a.say("ok: %d cluster(s), %d group(s), %d provider(s) in %d file(s)", len(set.Clusters), len(set.Groups), len(set.Providers), len(files))
		for _, f := range files {
			fmt.Fprintln(a.stdout, f)
		}
		return nil
	}
	return usagef("kcx profile ls|check")
}

// profileRows is the table kcx profile ls prints: a header, then each cluster in
// profile order with its group, site, provider and whether it is fetched and
// read-only; the name and the group in the group's colour, as the picker shows
// them.
func profileRows(set *profile.Set, fetched func(string) bool, paint func(color, s string) string) [][]string {
	rows := [][]string{{"NAME", "GROUP", "SITE", "PROVIDER", "FETCHED", "READ-ONLY"}}
	for _, n := range set.Names() {
		c := set.Clusters[n]
		done, ro := "-", "-"
		if fetched(n) {
			done = "yes"
		}
		if set.Readonly(c) {
			ro = "yes"
		}
		color := set.GroupOf(c).Color
		rows = append(rows, []string{paint(color, n), paint(color, c.Group), siteOf(c), set.ProviderOf(c).Name, done, ro})
	}
	return rows
}

// cmdSecret is `kcx secret status|refresh|forget [PROVIDER]`: a pass-through to
// the secret provider, never `get`.
func (a *App) cmdSecret(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return usagef("kcx secret status|refresh|forget [PROVIDER]")
	}
	verb := args[0]
	switch verb {
	case "status", "refresh", "forget":
	default:
		return usagef("kcx secret status|refresh|forget [PROVIDER]")
	}
	name := ""
	if len(args) == 2 {
		name = args[1]
	} else {
		set, err := a.profiles()
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, p := range set.Providers {
			if p.Secret != "" {
				seen[p.Secret] = true
			}
		}
		if len(seen) != 1 {
			return usagef("kcx secret %s PROVIDER: the profiles name %d secret provider(s)", verb, len(seen))
		}
		for n := range seen {
			name = n
		}
	}
	prov, err := secret.Resolve(name, nil, nil)
	if err != nil {
		return err
	}
	if prov == nil {
		return fmt.Errorf("%s is no secret provider", name)
	}
	if e, ok := prov.(*secret.Exec); ok {
		e.Stderr = a.stderr
	}
	ctx := context.Background()
	switch verb {
	case "status":
		s, err := prov.Status(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "%s: %s\n", prov.Name(), s)
	case "refresh":
		if err := prov.Refresh(ctx, a.stdin, a.stdout, a.stderr); err != nil {
			return err
		}
	case "forget":
		if err := prov.Forget(ctx); err != nil {
			return err
		}
		a.say("%s: forgotten", prov.Name())
	}
	return nil
}

// cmdNs is `kcx ns [NAME|-|-c] [--force]`.
func (a *App) cmdNs(args []string) error {
	force, unset := false, false
	target := ""
	for _, arg := range args {
		if arg == "--current" { // kubens's spelling
			arg = "-c"
		}
		switch {
		case arg == "--force" || arg == "-f":
			force = true
		case arg == "--unset":
			unset = true
		case arg == "-c" || arg == "-" || !strings.HasPrefix(arg, "-"):
			if target != "" {
				return usagef("kcx ns [NAME|-|-c|--unset] [--force]")
			}
			target = arg
		default:
			return usagef("unknown option %s", arg)
		}
	}
	files := a.store().Files
	if unset {
		if target != "" {
			return usagef("kcx ns --unset takes no namespace")
		}
		return a.unsetNamespace(files)
	}
	switch target {
	case "-c":
		_, namespace, err := ns.Current(files)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.stdout, namespace)
		return nil
	case "":
		ctx, _, err := ns.Current(files)
		if err != nil {
			return err
		}
		return a.pickNamespace(files, ctx)
	}
	ctx, current, err := ns.Current(files)
	if err != nil {
		return err
	}
	namespace := target
	if target == "-" {
		namespace = ns.ReadPrevious(a.lay.NSPrev, ctx)
		if namespace == "" {
			return fmt.Errorf("no previous namespace recorded for %s", ctx)
		}
	} else if !force && a.getenv(EnvRO) == "" {
		ok, err := ns.Exists(context.Background(), files, ctx, namespace, apiTimeout)
		if err != nil {
			return fmt.Errorf("%w; --force sets it anyway", err)
		}
		if !ok {
			return fmt.Errorf("no namespace %s in %s; --force sets it anyway", namespace, ctx)
		}
	}
	if err := a.setNamespace(files, ctx, namespace); err != nil {
		return err
	}
	if current != namespace && a.getenv(EnvRO) == "" {
		if err := ns.WritePrevious(a.lay.NSPrev, ctx, current); err != nil {
			return err
		}
	}
	a.say("namespace of %s is %s", ctx, namespace)
	return nil
}
