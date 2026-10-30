package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/junior/kcx/v2/internal/kubeconfig"
	"github.com/junior/kcx/v2/internal/ns"
	"github.com/junior/kcx/v2/internal/profile"
	"github.com/junior/kcx/v2/internal/proxy"
)

// exitError carries a child process's exit status to Main.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

// resolveForRun finds the context for run or -r: a context, a file, or a profile
// cluster, fetched when its file is missing. No probe: a run with a dead login
// fails on its own command, and a fetch at every run would be slow.
func (a *App) resolveForRun(name string) (ctx string, c *profile.Cluster, set *profile.Set, err error) {
	ctx, resolveErr := a.resolve(name)
	if s, perr := a.profiles(); perr == nil {
		set = s
		c, _ = set.Lookup(name)
		if c == nil && ctx != "" {
			c, _ = set.Lookup(fileNameOf(a.lay, ctx, name))
		}
	} else if resolveErr != nil {
		return "", nil, nil, fmt.Errorf("%v; and the profiles cannot be read: %v", resolveErr, perr)
	}
	if resolveErr != nil {
		if c == nil {
			return "", nil, nil, resolveErr
		}
		ctx, err = a.fetchCluster(set, c, "")
		if err != nil {
			return "", nil, nil, err
		}
	}
	return ctx, c, set, nil
}

// cmdRun is `kcx run NAME [-n NS] [--rw] -- CMD...`: one command against one
// cluster, the shared current context untouched. A private state file comes
// first in KUBECONFIG and holds a shadow of the context (the real cluster and
// user, the run's own namespace), so a switch or a namespace change inside the
// command lands there and nowhere else. A cluster that is read-only by default
// runs behind the read-only proxy unless --rw.
func (a *App) cmdRun(args []string) error {
	name, namespace, rw := "", "", false
	var cmd []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--":
			cmd = append([]string{}, args[i+1:]...)
			i = len(args)
		case arg == "-n" || arg == "--namespace":
			if i+1 >= len(args) {
				return usagef("-n needs a namespace")
			}
			i++
			namespace = args[i]
		case strings.HasPrefix(arg, "-n") && len(arg) > 2:
			namespace = arg[2:]
		case arg == "--rw":
			rw = true
		case strings.HasPrefix(arg, "-"):
			return usagef("unknown option %s", arg)
		default:
			if name != "" {
				return usagef("kcx run NAME [-n NS] [--rw] -- COMMAND...")
			}
			name = arg
		}
	}
	if name == "" || len(cmd) == 0 {
		return usagef("kcx run NAME [-n NS] [--rw] -- COMMAND...")
	}
	ctx, c, set, err := a.resolveForRun(name)
	if err != nil {
		return err
	}
	if c != nil && set.Readonly(c) && !rw {
		a.warn("%s is read-only by default (group %s): running behind the read-only proxy; --rw runs with the real credentials", c.Name, c.Group)
		return a.readonly(ctx, namespace, cmd)
	}
	work, err := os.MkdirTemp("", "kcx.")
	if err != nil {
		return fmt.Errorf("mktemp failed: %w", err)
	}
	defer os.RemoveAll(work)
	shadow := filepath.Join(work, "current.yaml")
	if err := a.writeShadow(shadow, ctx, namespace); err != nil {
		return err
	}
	files := append([]string{shadow}, a.lay.Files()...)
	env := withoutEnv(os.Environ(), scoped...)
	env = append(env, "KUBECONFIG="+strings.Join(files, string(os.PathListSeparator)), EnvRun+"="+ctx, EnvRunConfig+"="+shadow)
	return a.exec(cmd, env)
}

// cmdShell is `kcx shell [NAME] [-n NS] [--rw]`: a shell of its own for NAME.
// It resolves NAME as `kcx NAME` does (fetching when the file is missing or the
// login is dead), then starts $SHELL with a private kubeconfig first in
// KUBECONFIG: copies of every context (names only, never credentials) and the
// shell's current context. A switch (kcx NAME, kcx -, the picker), a namespace
// change (kcx ns, kubectl config set-context) or a kubectl config use-context
// inside lands in that file, so the other shells keep theirs. A cluster that is
// read-only by default opens a read-only shell instead, unless --rw.
func (a *App) cmdShell(args []string) error {
	if err := a.runGuard(); err != nil {
		return err
	}
	if a.getenv(EnvShell) != "" {
		return fmt.Errorf("this is already a kcx shell: kcx NAME switches it, exit leaves it")
	}
	name, namespace, rw := "", "", false
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "-n" || arg == "--namespace":
			if i+1 >= len(args) {
				return usagef("-n needs a namespace")
			}
			i++
			namespace = args[i]
		case strings.HasPrefix(arg, "-n") && len(arg) > 2:
			namespace = arg[2:]
		case arg == "--rw":
			rw = true
		case strings.HasPrefix(arg, "-"):
			return usagef("unknown option %s", arg)
		default:
			if name != "" {
				return usagef("kcx shell [NAME] [-n NS] [--rw]")
			}
			name = arg
		}
	}
	if name == "" {
		picked, err := a.pickContext("shell> ", "a kcx shell for which context?")
		if err != nil {
			return err
		}
		if picked == "" {
			return nil
		}
		name = picked
	}
	ctx, c, set, err := a.prepare(name, "", false, false)
	if err != nil {
		return err
	}
	if c != nil && set.Readonly(c) && !rw {
		a.warn("%s is read-only by default (group %s): a read-only shell; --rw opens a kcx shell with the real credentials", c.Name, c.Group)
		return a.readonly(ctx, namespace, nil)
	}
	work, err := os.MkdirTemp("", "kcx-shell.")
	if err != nil {
		return fmt.Errorf("mktemp failed: %w", err)
	}
	defer os.RemoveAll(work)
	private := filepath.Join(work, "config.yaml")
	if err := a.writeShellConfig(private, ctx, namespace); err != nil {
		return err
	}
	list := private + string(os.PathListSeparator) + a.lay.Kubeconfig()
	// KCX_SHELL_AUTO, not KCX_AUTO, tracks the value: a startup file that knows
	// only KCX_AUTO (an older ~/.zshenv) then leaves this KUBECONFIG alone.
	env := withoutEnv(os.Environ(), scoped...)
	env = append(env, "KUBECONFIG="+list, EnvShell+"="+private, EnvShellAuto+"="+list)
	sh := a.userShell()
	a.say("kcx shell for %s: kcx NAME, kcx ns and kubectl config changes stay in this shell; exit to leave", ctx)
	err = a.exec([]string{sh}, env)
	a.say("kcx shell closed")
	return err
}

// writeShellConfig writes a kcx shell's private kubeconfig: a copy of every
// context the layout defines (the context entries only; the clusters and users
// stay in their files, which KUBECONFIG lists after this one), the namespace of
// the starting context replaced when one is given, and that context current.
// With every context copied, any change kubectl makes to a context lands here.
func (a *App) writeShellConfig(path, ctx, namespace string) error {
	merged, err := kubeconfig.Store{Files: a.lay.Files()}.Load()
	if err != nil {
		return err
	}
	if _, ok := merged.Contexts[ctx]; !ok {
		return fmt.Errorf("no context named %s", ctx)
	}
	cfg := clientcmdapi.NewConfig()
	for name, c := range merged.Contexts {
		cp := *c
		cp.LocationOfOrigin = ""
		cfg.Contexts[name] = &cp
	}
	if namespace != "" {
		cfg.Contexts[ctx].Namespace = namespace
	}
	cfg.CurrentContext = ctx
	return kubeconfig.WriteFile(path, cfg)
}

// shellCopy makes sure a kcx shell's private file holds a copy of ctx (a
// cluster fetched or added during the shell has none yet) and, when current
// is set, makes it the shell's current context.
func (a *App) shellCopy(private, ctx string, current bool) error {
	cfg, err := kubeconfig.LoadFile(private)
	if err != nil {
		return err
	}
	changed := false
	if _, ok := cfg.Contexts[ctx]; !ok {
		merged, err := kubeconfig.Store{Files: a.lay.Files()}.Load()
		if err != nil {
			return err
		}
		real, ok := merged.Contexts[ctx]
		if !ok {
			return fmt.Errorf("no context named %s", ctx)
		}
		cp := *real
		cp.LocationOfOrigin = ""
		cfg.Contexts[ctx] = &cp
		changed = true
	}
	if current && cfg.CurrentContext != ctx {
		cfg.CurrentContext = ctx
		changed = true
	}
	if !changed {
		return nil
	}
	return kubeconfig.WriteFile(private, cfg)
}

// setNamespace sets ctx's namespace the way kubectl does, in the first file
// that defines ctx; inside a kcx shell that is made to be the shell's own file.
func (a *App) setNamespace(files []string, ctx, namespace string) error {
	if private := a.getenv(EnvShell); private != "" {
		if err := a.shellCopy(private, ctx, false); err != nil {
			return err
		}
	}
	return ns.Set(files, ctx, namespace)
}

// unsetNamespace is `kcx ns --unset`: the current context sets no namespace any
// more, so kubectl uses default; the one it had becomes the previous namespace,
// as with any change (not inside a read-only shell, as there).
func (a *App) unsetNamespace(files []string) error {
	ctx, current, err := ns.Current(files)
	if err != nil {
		return err
	}
	merged, err := kubeconfig.Store{Files: files}.Load()
	if err != nil {
		return err
	}
	if c, ok := merged.Contexts[ctx]; !ok || c.Namespace == "" {
		a.say("%s sets no namespace; kubectl uses default", ctx)
		return nil
	}
	if err := a.setNamespace(files, ctx, ""); err != nil {
		return err
	}
	if a.getenv(EnvRO) == "" {
		if err := ns.WritePrevious(a.lay.NSPrev, ctx, current); err != nil {
			return err
		}
	}
	a.say("namespace of %s unset; kubectl uses default", ctx)
	return nil
}

// writeShadow writes a one-context kubeconfig: the context under its own name,
// pointing at the real cluster and user by name (kubectl resolves them across
// files), with the run's namespace, and set as current.
func (a *App) writeShadow(path, ctx, namespace string) error {
	merged, err := a.store().Load()
	if err != nil {
		return err
	}
	real, ok := merged.Contexts[ctx]
	if !ok {
		return fmt.Errorf("no context named %s", ctx)
	}
	cfg := clientcmdapi.NewConfig()
	shadow := *real
	shadow.LocationOfOrigin = ""
	if namespace != "" {
		shadow.Namespace = namespace
	}
	cfg.Contexts[ctx] = &shadow
	cfg.CurrentContext = ctx
	return kubeconfig.WriteFile(path, cfg)
}

// cmdReadonly is `kcx -r [NAME] [-n NS] [-- CMD...]`: a read-only shell for NAME,
// or one read-only command.
func (a *App) cmdReadonly(args []string) error {
	name, namespace := "", ""
	var cmd []string
	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--":
			cmd = append([]string{}, args[i+1:]...)
			i = len(args)
		case arg == "-n" || arg == "--namespace":
			if i+1 >= len(args) {
				return usagef("-n needs a namespace")
			}
			i++
			namespace = args[i]
		case strings.HasPrefix(arg, "-"):
			return usagef("unknown option %s", arg)
		default:
			if name != "" {
				return usagef("kcx -r [NAME] [-- COMMAND...]")
			}
			name = arg
		}
	}
	if name == "" {
		picked, err := a.pickContext("read-only> ", "a READ-ONLY shell for which context?")
		if err != nil {
			return err
		}
		if picked == "" {
			return nil
		}
		name = picked
	}
	ctx, _, _, err := a.resolveForRun(name)
	if err != nil {
		return err
	}
	return a.readonly(ctx, namespace, cmd)
}

// readonly runs cmd (or the user's shell when cmd is empty) behind the read-only
// proxy for ctx.
func (a *App) readonly(ctx, namespace string, cmd []string) error {
	srv, err := proxy.Start(a.lay.Files(), ctx)
	if err != nil {
		return err
	}
	defer srv.Close()
	work, err := os.MkdirTemp("", "kcx.")
	if err != nil {
		return fmt.Errorf("mktemp failed: %w", err)
	}
	defer os.RemoveAll(work)
	ro := filepath.Join(work, "readonly.yaml")
	if err := srv.WriteKubeconfig(ro); err != nil {
		return err
	}
	roctx := proxy.ReadonlyContext(ctx)
	if namespace == "" {
		// The namespace the context has (a kcx shell's own, from inside one),
		// as kubectl would use it, rather than the server's default.
		if merged, err := a.store().Load(); err == nil {
			if c, ok := merged.Contexts[ctx]; ok {
				namespace = c.Namespace
			}
		}
	}
	if namespace != "" {
		cfg, err := kubeconfig.LoadFile(ro)
		if err != nil {
			return err
		}
		cfg.Contexts[roctx].Namespace = namespace
		if err := kubeconfig.WriteFile(ro, cfg); err != nil {
			return err
		}
	}
	// KCX_RO keeps it read-only through the shell's own startup files: the init
	// snippet and `kcx env` leave this KUBECONFIG alone, and kcx itself refuses
	// everything but queries inside.
	env := withoutEnv(os.Environ(), scoped...)
	env = append(env, "KUBECONFIG="+ro, EnvRO+"="+roctx, EnvROConfig+"="+ro)
	if len(cmd) > 0 {
		a.warn("read-only run on %s", roctx)
		return a.exec(cmd, env)
	}
	shell := a.userShell()
	a.say("READ-ONLY shell for %s; exit to leave", ctx)
	a.say("reads work; writes, exec, attach, port-forward and cp are refused by a local proxy")
	err = a.exec([]string{shell}, env)
	a.say("read-only shell closed")
	return err
}

// exec runs a command with the given environment, the user's terminal as its
// stdio, and reports its exit status through exitError.
//
// While the child runs, kcx outlives the signals meant for it, so that it can
// remove its private files and return the child's status: Ctrl-C reaches the
// child from the terminal itself (they share a process group, or an
// interactive shell holds the terminal), so kcx only stays alive; a terminate
// or a hang-up (a closed terminal window) is passed on to the child. A signal
// ignored when kcx started (nohup, or & in a script) stays ignored, for the
// child as well.
func (a *App) exec(cmd []string, env []string) error {
	path, err := exec.LookPath(cmd[0])
	if err != nil {
		return fmt.Errorf("cannot run %s: %w", cmd[0], err)
	}
	c := exec.Command(path, cmd[1:]...)
	c.Env = env
	// The user's own stdio in production (os.Stdin and friends are files, so a
	// shell gets its terminal); buffers in tests.
	c.Stdin, c.Stdout, c.Stderr = a.stdin, a.stdout, a.stderr
	var watch []os.Signal
	for _, s := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !signal.Ignored(s) {
			watch = append(watch, s)
		}
	}
	sigs := make(chan os.Signal, 4)
	if len(watch) > 0 {
		signal.Notify(sigs, watch...)
		defer signal.Stop(sigs)
	}
	if err := c.Start(); err != nil {
		return fmt.Errorf("cannot run %s: %w", cmd[0], err)
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sigs:
				if s != os.Interrupt {
					_ = c.Process.Signal(s)
				}
			case <-done:
				return
			}
		}
	}()
	err = c.Wait()
	close(done)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return exitError{exitCode(ee)}
		}
		return fmt.Errorf("cannot run %s: %w", cmd[0], err)
	}
	return nil
}

// userShell is the shell a kcx shell or a read-only shell starts.
func (a *App) userShell() string {
	return pickShell(runtime.GOOS, a.getenv, exec.LookPath)
}

// pickShell is SHELL, or /bin/sh when it is unset. On Windows, where SHELL is
// rarely set and Git Bash sets it to a path such as /usr/bin/bash that a
// Windows program cannot start, it is the first that can be found of SHELL, its
// base name, PowerShell 7, Windows PowerShell and COMSPEC (cmd.exe).
func pickShell(goos string, getenv func(string) string, lookPath func(string) (string, error)) string {
	sh := getenv("SHELL")
	if goos != "windows" {
		if sh == "" {
			return "/bin/sh"
		}
		return sh
	}
	var candidates []string
	if sh != "" {
		candidates = append(candidates, sh, filepath.Base(sh))
	}
	candidates = append(candidates, "pwsh.exe", "powershell.exe")
	if c := getenv("COMSPEC"); c != "" {
		candidates = append(candidates, c)
	}
	for _, c := range candidates {
		if p, err := lookPath(c); err == nil {
			return p
		}
	}
	return "cmd.exe"
}

// exitCode is the child's exit status or, when a signal ended it, 128 plus the
// signal's number, as a shell reports it.
func exitCode(ee *exec.ExitError) int {
	if code := ee.ExitCode(); code >= 0 {
		return code
	}
	type signaled interface {
		Signaled() bool
		Signal() syscall.Signal
	}
	if ws, ok := ee.Sys().(signaled); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}

// withoutEnv returns env minus the named variables.
func withoutEnv(env []string, drop ...string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
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
	return out
}
