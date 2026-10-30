package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/junior/kcx/v2/internal/kubeconfig"
	"github.com/junior/kcx/v2/internal/layout"
)

// addArgs is what `kcx add` and `kcx temp add` accept:
// [-f] [--context CTX] [--server URL] [--use] [--keep-names] [--context-name T] [--user-name T] NAME (FILE | - | -- CMD...)
type addArgs struct {
	name, src       string
	cmd             []string
	force, use      bool
	keepNames       bool
	ctx, server     string
	ctxName, usrNme string
}

func parseAdd(args []string) (addArgs, error) {
	var p addArgs
	haveCmd := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func(flag string) (string, error) {
			if i+1 >= len(args) {
				return "", usagef("%s needs a value", flag)
			}
			i++
			return args[i], nil
		}
		var err error
		switch {
		case arg == "-f" || arg == "--force":
			p.force = true
		case arg == "--use":
			p.use = true
		case arg == "--keep-names":
			p.keepNames = true
		case arg == "--context":
			p.ctx, err = value(arg)
		case strings.HasPrefix(arg, "--context="):
			p.ctx = strings.TrimPrefix(arg, "--context=")
		case arg == "--server":
			p.server, err = value(arg)
		case strings.HasPrefix(arg, "--server="):
			p.server = strings.TrimPrefix(arg, "--server=")
		case arg == "--context-name":
			p.ctxName, err = value(arg)
		case strings.HasPrefix(arg, "--context-name="):
			p.ctxName = strings.TrimPrefix(arg, "--context-name=")
		case arg == "--user-name":
			p.usrNme, err = value(arg)
		case strings.HasPrefix(arg, "--user-name="):
			p.usrNme = strings.TrimPrefix(arg, "--user-name=")
		case arg == "--":
			haveCmd = true
			p.cmd = append([]string{}, args[i+1:]...)
			i = len(args)
		case arg == "-":
			if p.name == "" || p.src != "" {
				return p, usagef("kcx add NAME - reads standard input")
			}
			p.src = "-"
		case strings.HasPrefix(arg, "-"):
			return p, usagef("unknown option %s", arg)
		default:
			switch {
			case p.name == "":
				p.name = arg
			case p.src == "":
				p.src = arg
			default:
				return p, usagef("unexpected argument %s", arg)
			}
		}
		if err != nil {
			return p, err
		}
	}
	if p.name == "" {
		return p, usagef("kcx add needs a NAME")
	}
	if haveCmd {
		if p.src != "" {
			return p, usagef("give a FILE, - or -- CMD..., not two of them")
		}
		if len(p.cmd) == 0 {
			return p, usagef("nothing to run after --")
		}
	} else if p.src == "" {
		return p, usagef("kcx add %s needs FILE, - or -- CMD...", p.name)
	}
	return p, nil
}

// cmdAdd imports a cluster into a file of its own. where is "perm" or "temp".
func (a *App) cmdAdd(where string, args []string) error {
	p, err := parseAdd(args)
	if err != nil {
		return err
	}
	if !layout.ValidName(p.name) {
		return fmt.Errorf("%s: a name takes letters, digits, . _ and -, up to 63", p.name)
	}
	var dest string
	if where == "perm" {
		if err := a.labGuard(); err != nil {
			return err
		}
		dest = a.lay.PermFile(p.name)
	} else {
		dest = a.lay.TempFile(p.name, "")
		for _, f := range layout.DirFiles(a.lay.Temp) {
			if n, _ := layout.SplitTemp(f); n == p.name && f != dest {
				return fmt.Errorf("a kind temp cluster named %s exists already (%s)", p.name, f)
			}
		}
	}
	if _, err := os.Stat(dest); err == nil && !p.force {
		return fmt.Errorf("%s exists already (%s); -f replaces it and moves the old file to the Trash", p.name, dest)
	}
	work, err := os.MkdirTemp("", "kcx.")
	if err != nil {
		return fmt.Errorf("mktemp failed: %w", err)
	}
	defer os.RemoveAll(work)
	input := filepath.Join(work, "kubeconfig")
	switch {
	case len(p.cmd) > 0:
		if err := a.runProvider(p.cmd, input); err != nil {
			return err
		}
	case p.src == "-":
		data, err := io.ReadAll(a.stdin)
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			return fmt.Errorf("nothing on standard input")
		}
		if err := os.WriteFile(input, data, 0o600); err != nil {
			return err
		}
	default:
		input = p.src
	}
	opts := kubeconfig.ImportOptions{
		Context: p.ctx, Server: p.server, Rename: where == "perm", KeepNames: p.keepNames,
		ContextName: p.ctxName, UserName: p.usrNme, Replace: p.force, TrashOld: true,
	}
	cfg, err := kubeconfig.Prepare(input, p.name, opts)
	if err != nil {
		return err
	}
	if err := a.lay.Ensure(); err != nil {
		return err
	}
	ctx, err := kubeconfig.Import(cfg, dest, a.lay.Files(), a.lay.State, a.getenv, opts)
	if err != nil {
		return err
	}
	if where == "perm" {
		a.say("added %s (%s)", p.name, dest)
	} else {
		a.say("kept temp cluster %s as context %s (%s)", p.name, ctx, dest)
	}
	a.hintNew(ctx, dest)
	if p.use {
		if err := a.runGuard(); err != nil {
			return err
		}
		if err := a.use(ctx); err != nil {
			return err
		}
		a.switched(ctx)
	}
	return nil
}

// runProvider runs a command that writes a kubeconfig: KUBECONFIG and KCX_OUT
// point at out, and an argument {} is replaced by out's path, for tools with a
// file option. The command's output goes to the user; nothing is parsed.
func (a *App) runProvider(cmd []string, out string) error {
	args := make([]string, 0, len(cmd))
	for _, c := range cmd {
		if c == "{}" {
			c = out
		}
		args = append(args, c)
	}
	c := exec.Command(args[0], args[1:]...)
	c.Stdin = a.stdin
	c.Stdout = a.stdout
	c.Stderr = a.stderr
	c.Env = append(filteredEnv(a.getenv, "KUBECONFIG", "KCX_OUT", EnvAuto), "KUBECONFIG="+out, "KCX_OUT="+out)
	if err := c.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("the command failed (exit %d); nothing was imported", ee.ExitCode())
		}
		return fmt.Errorf("cannot run %s: %w", args[0], err)
	}
	info, err := os.Stat(out)
	if err != nil || info.Size() == 0 {
		return fmt.Errorf("the command wrote no kubeconfig: tools that ignore KUBECONFIG need {} where their file option goes")
	}
	_ = os.Chmod(out, 0o600)
	return nil
}

// filteredEnv is the process environment without the named variables.
func filteredEnv(getenv layout.Getenv, drop ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		k := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			k = kv[:i]
		}
		skip := false
		for _, d := range drop {
			if k == d {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, kv)
		}
	}
	_ = getenv
	return out
}

// labGuard refuses permanent credentials on a lab node (a docker-engine role):
// they do not belong on one. KCX_ALLOW_ON_LAB_NODE=1 overrides, on the user's word.
func (a *App) labGuard() error {
	if !a.labNode() {
		return nil
	}
	if a.getenv("KCX_ALLOW_ON_LAB_NODE") == "1" {
		return nil
	}
	return fmt.Errorf("this machine is a lab node, and a permanent cluster's credentials do not belong on one: add it on a desktop instead; temp clusters are fine here (kcx temp create)")
}

func (a *App) cmdRm(args []string) error {
	if len(args) != 1 || args[0] == "" {
		return usagef("kcx rm NAME")
	}
	name := args[0]
	f := a.lay.PermFile(name)
	if _, err := os.Stat(f); err != nil {
		for _, t := range layout.DirFiles(a.lay.Temp) {
			if n, _ := layout.SplitTemp(t); n == name {
				return fmt.Errorf("%s is a temp cluster: kcx temp rm %s", name, name)
			}
		}
		return fmt.Errorf("no permanent cluster named %s (kcx ls lists them)", name)
	}
	cur, err := a.store().Current()
	if err != nil {
		return err
	}
	ctxs := managedContexts(f)
	if err := layout.Trash(a.getenv, f); err != nil {
		return fmt.Errorf("could not move %s to the Trash: %w", f, err)
	}
	a.say("moved %s to the Trash", f)
	if cur != "" && containsString(ctxs, cur) {
		if err := kubeconfig.ClearCurrent(a.lay.State); err != nil {
			return err
		}
		a.say("there is no current context now; kcx NAME picks one")
	}
	return nil
}

func (a *App) cmdMv(args []string) error {
	if len(args) != 2 {
		return usagef("kcx mv OLD NEW")
	}
	oldName, newName := args[0], args[1]
	src := a.lay.PermFile(oldName)
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("no permanent cluster named %s", oldName)
	}
	if !layout.ValidName(newName) {
		return fmt.Errorf("%s: a name takes letters, digits, . _ and -, up to 63", newName)
	}
	dest := a.lay.PermFile(newName)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s exists already", newName)
	}
	oldCtxs := managedContexts(src)
	cur, err := a.store().Current()
	if err != nil {
		return err
	}
	opts := kubeconfig.ImportOptions{Rename: true}
	cfg, err := kubeconfig.Prepare(src, newName, opts)
	if err != nil {
		return err
	}
	// The source's own entries are about to go, so they may not count as taken.
	files := make([]string, 0, len(a.lay.Files()))
	for _, f := range a.lay.Files() {
		if f != src {
			files = append(files, f)
		}
	}
	ctx, err := kubeconfig.Import(cfg, dest, files, a.lay.State, a.getenv, opts)
	if err != nil {
		return err
	}
	// The same credentials now live under the new name: this is a rename, not a removal.
	if err := os.Remove(src); err != nil {
		return err
	}
	if cur != "" && containsString(oldCtxs, cur) {
		if err := kubeconfig.SetCurrent(a.lay.State, ctx); err != nil {
			return err
		}
	}
	if prev := kubeconfig.ReadPrevious(a.lay.Prev); prev != "" && containsString(oldCtxs, prev) {
		if err := kubeconfig.WritePrevious(a.lay.Prev, ctx); err != nil {
			return err
		}
	}
	a.say("renamed %s to %s", oldName, newName)
	a.hintNew(ctx, dest)
	return nil
}

// cmdAdopt moves a context another tool wrote into ~/.kube/config into a file of
// its own, with the cluster and the user, unless another context there still
// uses them.
func (a *App) cmdAdopt(args []string) error {
	if len(args) < 1 || len(args) > 2 || args[0] == "" {
		return usagef("kcx adopt CTX [NAME]")
	}
	if err := a.runGuard(); err != nil {
		return err
	}
	if err := a.labGuard(); err != nil {
		return err
	}
	ctx := args[0]
	name := ctx
	if len(args) == 2 {
		name = args[1]
	}
	state, err := kubeconfig.LoadFile(a.lay.State)
	if err != nil {
		return err
	}
	c, ok := state.Contexts[ctx]
	if !ok {
		return fmt.Errorf("%s is not in ~/.kube/config; adopt moves what other tools wrote there", ctx)
	}
	if !layout.ValidName(name) {
		return fmt.Errorf("%s cannot be a file name: kcx adopt '%s' NAME", name, ctx)
	}
	dest := a.lay.PermFile(name)
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%s exists already (%s)", name, dest)
	}
	except := []kubeconfig.Entry{{Kind: "context", Name: ctx}}
	clusterUsed, userUsed := false, false
	for n, other := range state.Contexts {
		if n == ctx {
			continue
		}
		if other.Cluster == c.Cluster {
			clusterUsed = true
		}
		if other.AuthInfo == c.AuthInfo {
			userUsed = true
		}
	}
	if !clusterUsed && c.Cluster != "" {
		except = append(except, kubeconfig.Entry{Kind: "cluster", Name: c.Cluster})
	}
	if !userUsed && c.AuthInfo != "" {
		except = append(except, kubeconfig.Entry{Kind: "user", Name: c.AuthInfo})
	}
	opts := kubeconfig.ImportOptions{Context: ctx, Rename: true, Except: except}
	cfg, err := kubeconfig.Prepare(a.lay.State, name, opts)
	if err != nil {
		return err
	}
	cur := state.CurrentContext
	newCtx, err := kubeconfig.Import(cfg, dest, a.lay.Files(), a.lay.State, a.getenv, opts)
	if err != nil {
		return err
	}
	if err := kubeconfig.RemoveFromFile(a.lay.State, ctx); err != nil {
		return err
	}
	if cur == ctx {
		if err := kubeconfig.SetCurrent(a.lay.State, newCtx); err != nil {
			return err
		}
	}
	a.say("%s now lives in %s as %s", ctx, dest, newCtx)
	a.hintNew(newCtx, dest)
	return nil
}
