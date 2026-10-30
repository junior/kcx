// Package cli is kcx's command line: argument handling, the usage text and the
// glue between the commands and the packages that do the work.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"

	"github.com/junior/kcx/v2/internal/config"
	"github.com/junior/kcx/v2/internal/kubeconfig"
	"github.com/junior/kcx/v2/internal/layout"
	"github.com/junior/kcx/v2/internal/profile"
	"github.com/junior/kcx/v2/internal/shell"
	"github.com/junior/kcx/v2/internal/temp"
)

// version is what kcx -v prints; main sets it with SetVersion.
var version = "dev"

// SetVersion sets what kcx -v prints: stamp when the build has one (the release
// build passes -X main.version=X.Y.Z, a path that no module rename moves), else
// the module version a go install records (v2.2.0 prints as 2.2.0), else "dev".
func SetVersion(stamp string) {
	version = resolveVersion(stamp, debug.ReadBuildInfo)
}

func resolveVersion(stamp string, buildInfo func() (*debug.BuildInfo, bool)) string {
	if stamp != "" {
		return stamp
	}
	if bi, ok := buildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return "dev"
}

// Environment variable names the shell integration relies on. ~/.zshenv and the
// `kcx init` snippets read them, so they are part of the contract.
const (
	EnvRO        = "KCX_RO"         // set inside a read-only shell: the context, with " (READ-ONLY)"
	EnvROConfig  = "KCX_RO_CONFIG"  // the read-only kubeconfig, which `kcx env` hands back
	EnvRun       = "KCX_RUN"        // set inside `kcx run`: the context the run is for
	EnvRunConfig = "KCX_RUN_CONFIG" // the run's private file, first in its KUBECONFIG
	EnvShell     = "KCX_SHELL"      // set inside `kcx shell`: the shell's private file, first in its KUBECONFIG
	EnvShellAuto = "KCX_SHELL_AUTO" // the KUBECONFIG value a kcx shell's integration set itself
	EnvAuto      = "KCX_AUTO"       // the KUBECONFIG value the shell integration set itself
)

// scoped are the variables a kcx shell, a run or a read-only shell sets; a new
// one drops them all before setting its own, so one never inherits another's.
var scoped = []string{"KUBECONFIG", EnvAuto, EnvRun, EnvRunConfig, EnvRO, EnvROConfig, EnvShell, EnvShellAuto}

// usageError is a mistake in the arguments: exit 2, and a pointer to -h.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// App holds what every command needs.
type App struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv layout.Getenv
	lay    layout.Layout
	cfg    *config.Settings // loaded on first use
	set    *profile.Set     // loaded on first use
}

// Main runs kcx with args and returns the exit status.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	app := &App{stdin: stdin, stdout: stdout, stderr: stderr, getenv: getenv}
	lay, err := layout.FromEnv(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "kcx: %s\n", err)
		return 1
	}
	// Inside a kcx shell or a run, the previous context and the previous
	// namespaces are the private file's neighbours, so `kcx -` and `kcx ns -`
	// there never touch what the other shells share.
	for _, v := range []string{EnvShell, EnvRunConfig} {
		if p := getenv(v); p != "" {
			lay.Prev = filepath.Join(filepath.Dir(p), "kubectx")
			lay.NSPrev = filepath.Join(filepath.Dir(p), "kubens")
			break
		}
	}
	app.lay = lay
	if err := app.run(args); err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "kcx: %s\nRun kcx -h for the commands.\n", ue.msg)
			return 2
		}
		var ee exitError
		if errors.As(err, &ee) {
			return ee.code // a command kcx ran for the user: its status, no message
		}
		fmt.Fprintf(stderr, "kcx: %s\n", err)
		return 1
	}
	return 0
}

func (a *App) run(args []string) error {
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}
	if a.getenv(EnvRO) != "" {
		switch cmd {
		// init and completion only print shell snippets, and a startup file that
		// evaluates them runs inside this shell too: the snippet's own guard keeps
		// the read-only KUBECONFIG.
		case "", "ls", "list", "env", "init", "completion", "-c", "--current", "current", "ns", "__complete", "-h", "--help", "help", "-v", "-V", "--version":
		default:
			return fmt.Errorf("this is a READ-ONLY shell for %s: only kcx ls, env, init, completion, -c and ns work here; exit it first", a.getenv(EnvRO))
		}
	}
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, a.helpText())
		return nil
	case "-v", "-V", "--version":
		fmt.Fprintf(a.stdout, "kcx %s\n", version)
		return nil
	case "env":
		if len(args) != 1 {
			return usagef("kcx env takes no arguments")
		}
		return a.cmdEnv()
	case "__complete":
		return a.cmdComplete(args[1:])
	case "":
		return a.cmdPick()
	case "-c", "--current", "current":
		return a.cmdCurrent()
	case "--unset":
		if len(args) != 1 {
			return usagef("kcx --unset takes no arguments")
		}
		return a.cmdUnset()
	case "run":
		return a.cmdRun(args[1:])
	case "shell", "-s", "--shell":
		return a.cmdShell(args[1:])
	case "-r", "--readonly":
		return a.cmdReadonly(args[1:])
	case "init":
		return a.cmdSnippet(shell.Init, "kcx init zsh|bash", args[1:])
	case "completion":
		return a.cmdSnippet(shell.Completion, "kcx completion zsh|bash", args[1:])
	case "doctor":
		return a.cmdDoctor(args[1:])
	case "temp":
		return a.cmdTemp(args[1:])
	case "-":
		if len(args) != 1 {
			return usagef("kcx - takes no arguments")
		}
		return a.cmdPrevious()
	case "ls", "list":
		return a.cmdLs(args[1:])
	case "add":
		return a.cmdAdd("perm", args[1:])
	case "rm", "remove":
		return a.cmdRm(args[1:])
	case "mv", "rename":
		return a.cmdMv(args[1:])
	case "adopt":
		return a.cmdAdopt(args[1:])
	case "ns", "namespace":
		return a.cmdNs(args[1:])
	case "fetch":
		return a.cmdFetch(args[1:])
	case "profile", "profiles":
		return a.cmdProfile(args[1:])
	case "secret":
		return a.cmdSecret(args[1:])
	}
	if strings.HasPrefix(cmd, "-") {
		return usagef("unknown option %s", cmd)
	}
	return a.cmdSwitchArgs(args)
}

// cmdEnv prints the export line for shells other than zsh. A read-only shell's,
// a kcx shell's or a run's KUBECONFIG is the point of it: a startup file that
// evals this must get that one back, never the shared or the real one.
func (a *App) cmdEnv() error {
	switch {
	case a.getenv(EnvRO) != "":
		v := a.getenv(EnvROConfig)
		if v == "" {
			v = a.getenv("KUBECONFIG")
		}
		fmt.Fprintf(a.stdout, "export KUBECONFIG=%s\n", shellQuote(v))
	case a.getenv(EnvShell) != "":
		k := a.getenv(EnvShell) + string(os.PathListSeparator) + a.lay.Kubeconfig()
		fmt.Fprintf(a.stdout, "export KUBECONFIG=%s %s=%s\n", shellQuote(k), EnvShellAuto, shellQuote(k))
	case a.getenv(EnvRunConfig) != "":
		k := a.getenv(EnvRunConfig) + string(os.PathListSeparator) + a.lay.Kubeconfig()
		fmt.Fprintf(a.stdout, "export KUBECONFIG=%s\n", shellQuote(k))
	case a.getenv(EnvRun) != "":
		fmt.Fprintf(a.stdout, "export KUBECONFIG=%s\n", shellQuote(a.getenv("KUBECONFIG")))
	default:
		k := a.lay.Kubeconfig()
		fmt.Fprintf(a.stdout, "export KUBECONFIG=%s %s=%s\n", shellQuote(k), EnvAuto, shellQuote(k))
	}
	return nil
}

// cmdSnippet prints a shell snippet: `kcx init SHELL` or `kcx completion SHELL`.
func (a *App) cmdSnippet(f func(string) (string, error), usage string, args []string) error {
	if len(args) != 1 {
		return usagef("%s", usage)
	}
	s, err := f(args[0])
	if err != nil {
		return usagef("%s", err)
	}
	fmt.Fprint(a.stdout, s)
	return nil
}

// cmdComplete prints the words tab completion may offer, given the words already
// typed after `kcx`. The shell functions call it; it is not for people.
func (a *App) cmdComplete(words []string) error {
	first := ""
	if len(words) > 0 {
		first = words[0]
	}
	last := ""
	if len(words) > 0 {
		last = words[len(words)-1]
	}
	switch last {
	case "--context", "--server", "--context-name", "--user-name", "-":
		return nil
	case "--engine":
		if ctxs, err := newEngine().Contexts(); err == nil {
			for _, c := range ctxs {
				fmt.Fprintln(a.stdout, c.Name)
			}
		}
		return nil
	case "--config":
		for _, p := range temp.Presets(a.lay.Presets) {
			fmt.Fprintln(a.stdout, p)
		}
		return nil
	case "--group":
		if set, err := a.profiles(); err == nil {
			for _, g := range set.GroupNames() {
				fmt.Fprintln(a.stdout, g)
			}
		}
		return nil
	}
	names := func(dir string) {
		for _, f := range layout.DirFiles(dir) {
			n, _ := layout.SplitTemp(f)
			fmt.Fprintln(a.stdout, n)
		}
	}
	switch first {
	case "":
		ctxs, _ := a.store().Contexts()
		for _, c := range ctxs {
			fmt.Fprintln(a.stdout, c)
		}
		if set, err := a.profiles(); err == nil {
			for _, n := range set.Names() {
				if !containsString(ctxs, n) {
					fmt.Fprintln(a.stdout, n)
				}
			}
		}
		for _, w := range []string{"ns", "fetch", "run", "shell", "-r", "profile", "secret", "ls", "env", "init", "completion", "doctor", "add", "rm", "mv", "adopt", "temp", "-c", "--unset", "-", "-h", "-v"} {
			fmt.Fprintln(a.stdout, w)
		}
	case "run", "shell", "-r":
		if len(words) == 1 {
			ctxs, _ := a.store().Contexts()
			for _, c := range ctxs {
				fmt.Fprintln(a.stdout, c)
			}
			if set, err := a.profiles(); err == nil {
				for _, n := range set.Names() {
					if !containsString(ctxs, n) {
						fmt.Fprintln(a.stdout, n)
					}
				}
			}
		}
	case "ls", "list":
		for _, w := range []string{"-q", "--group"} {
			fmt.Fprintln(a.stdout, w)
		}
	case "init", "completion":
		if len(words) == 1 {
			fmt.Fprintln(a.stdout, "zsh")
			fmt.Fprintln(a.stdout, "bash")
		}
	case "temp":
		sub := ""
		if len(words) > 1 {
			sub = words[1]
		}
		switch sub {
		case "":
			for _, w := range []string{"create", "ls", "rm", "prune", "add"} {
				fmt.Fprintln(a.stdout, w)
			}
		case "rm", "remove", "delete":
			if len(words) == 2 {
				names(a.lay.Temp)
			}
		case "create":
			for _, w := range []string{"--engine", "--config", "--use"} {
				fmt.Fprintln(a.stdout, w)
			}
		}
	case "ns", "namespace":
		if len(words) == 1 {
			for _, w := range []string{"-c", "-", "--force", "--unset"} {
				fmt.Fprintln(a.stdout, w)
			}
		}
	case "fetch":
		if set, err := a.profiles(); err == nil {
			for _, n := range set.Names() {
				fmt.Fprintln(a.stdout, n)
			}
		}
		for _, w := range []string{"--all", "--missing", "--group", "--use", "-u"} {
			fmt.Fprintln(a.stdout, w)
		}
	case "profile", "profiles":
		if len(words) == 1 {
			fmt.Fprintln(a.stdout, "ls")
			fmt.Fprintln(a.stdout, "check")
		}
	case "secret":
		if len(words) == 1 {
			for _, w := range []string{"status", "refresh", "forget"} {
				fmt.Fprintln(a.stdout, w)
			}
		}
	case "rm", "remove", "mv", "rename":
		if len(words) == 1 {
			names(a.lay.Perm)
		}
	case "adopt":
		if len(words) == 1 {
			ctxs, _ := kubeconfig.ContextsIn(a.lay.State)
			for _, c := range ctxs {
				fmt.Fprintln(a.stdout, c)
			}
		}
	case "add":
		for _, w := range []string{"-f", "--context", "--server", "--use", "--keep-names", "--context-name", "--user-name"} {
			fmt.Fprintln(a.stdout, w)
		}
	}
	return nil
}

// shellQuote single-quotes a value for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
