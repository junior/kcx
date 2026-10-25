package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/junior/kcx/internal/kubeconfig"
	"github.com/junior/kcx/internal/profile"
	"github.com/junior/kcx/internal/proxy"
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
	env := withoutEnv(os.Environ(), "KUBECONFIG", EnvAuto, EnvRun, EnvRO, EnvROConfig)
	env = append(env, "KUBECONFIG="+strings.Join(files, string(os.PathListSeparator)), EnvRun+"="+ctx)
	return a.exec(cmd, env)
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
		picked, err := a.pickContext("read-only> ", "a READ-ONLY shell for which context? ESC cancels")
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
	if err := proxy.WriteKubeconfig(ro, ctx, srv.Addr()); err != nil {
		return err
	}
	roctx := proxy.ReadonlyContext(ctx)
	if namespace != "" {
		_ = os.Chmod(ro, 0o600)
		cfg, err := kubeconfig.LoadFile(ro)
		if err != nil {
			return err
		}
		cfg.Contexts[roctx].Namespace = namespace
		if err := kubeconfig.WriteFile(ro, cfg); err != nil {
			return err
		}
		_ = os.Chmod(ro, 0o400)
	}
	// KCX_RO keeps it read-only through the shell's own startup files: the init
	// snippet and `kcx env` leave this KUBECONFIG alone, and kcx itself refuses
	// everything but queries inside.
	env := withoutEnv(os.Environ(), "KUBECONFIG", EnvAuto, EnvRun, EnvRO, EnvROConfig)
	env = append(env, "KUBECONFIG="+ro, EnvRO+"="+roctx, EnvROConfig+"="+ro)
	if len(cmd) > 0 {
		a.warn("read-only run on %s", roctx)
		return a.exec(cmd, env)
	}
	shell := a.getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	a.say("READ-ONLY shell for %s; exit to leave", ctx)
	a.say("reads work; writes, exec, attach, port-forward and cp are refused by a local proxy")
	err = a.exec([]string{shell}, env)
	a.say("read-only shell closed")
	return err
}

// exec runs a command with the given environment, the user's terminal as its
// stdio, and reports its exit status through exitError.
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
	if err := c.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return exitError{ee.ExitCode()}
		}
		return fmt.Errorf("cannot run %s: %w", cmd[0], err)
	}
	return nil
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
