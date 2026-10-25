// Package cli is kcx's command line: argument handling, the usage text and the
// glue between the commands and the packages that do the work.
package cli

import (
	"errors"
	"fmt"
	"io"

	"strings"

	"github.com/junior/kcx/internal/layout"
)

// version is stamped by the release build (-X github.com/junior/kcx/internal/cli.version=...).
var version = "2.0.0-dev"

// Environment variable names the shell integration relies on. ~/.zshenv and the
// `kcx init` snippets read them, so they are part of the contract.
const (
	EnvRO       = "KCX_RO"        // set inside a read-only shell: the context, with " (READ-ONLY)"
	EnvROConfig = "KCX_RO_CONFIG" // the read-only kubeconfig, which `kcx env` hands back
	EnvRun      = "KCX_RUN"       // set inside `kcx run`: the context the run is for
	EnvAuto     = "KCX_AUTO"      // the KUBECONFIG value the shell integration set itself
)

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
}

// Main runs kcx with args and returns the exit status.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
	app := &App{stdin: stdin, stdout: stdout, stderr: stderr, getenv: getenv}
	lay, err := layout.FromEnv(getenv)
	if err != nil {
		fmt.Fprintf(stderr, "kcx: %s\n", err)
		return 1
	}
	app.lay = lay
	if err := app.run(args); err != nil {
		var ue usageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "kcx: %s\nRun kcx -h for the commands.\n", ue.msg)
			return 2
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
		case "", "ls", "list", "env", "-c", "current", "__complete", "-h", "--help", "help", "-v", "--version":
		default:
			return fmt.Errorf("this is a READ-ONLY shell for %s: only kcx ls, env and -c work here; exit it first", a.getenv(EnvRO))
		}
	}
	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, usageText)
		return nil
	case "-v", "--version":
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
		return usagef("no command yet in this build: kcx -h")
	}
	if strings.HasPrefix(cmd, "-") {
		return usagef("unknown option %s", cmd)
	}
	return usagef("unknown command %s", cmd)
}

// cmdEnv prints the export line for shells other than zsh. A read-only shell's
// (or a run's) KUBECONFIG is the point of it: a startup file that evals this must
// get that one back, never the real credentials.
func (a *App) cmdEnv() error {
	switch {
	case a.getenv(EnvRO) != "":
		v := a.getenv(EnvROConfig)
		if v == "" {
			v = a.getenv("KUBECONFIG")
		}
		fmt.Fprintf(a.stdout, "export KUBECONFIG=%s\n", shellQuote(v))
	case a.getenv(EnvRun) != "":
		fmt.Fprintf(a.stdout, "export KUBECONFIG=%s\n", shellQuote(a.getenv("KUBECONFIG")))
	default:
		k := a.lay.Kubeconfig()
		fmt.Fprintf(a.stdout, "export KUBECONFIG=%s %s=%s\n", shellQuote(k), EnvAuto, shellQuote(k))
	}
	return nil
}

// cmdComplete prints the words tab completion may offer, given the words already
// typed after `kcx`. The shell functions call it; it is not for people.
func (a *App) cmdComplete(words []string) error {
	first := ""
	if len(words) > 0 {
		first = words[0]
	}
	switch first {
	case "":
		for _, w := range []string{"env", "-h", "-v"} {
			fmt.Fprintln(a.stdout, w)
		}
	}
	return nil
}

// shellQuote single-quotes a value for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

const usageText = `kcx - one kubeconfig file per cluster under ~/.kube, and the tools to use them.

  kcx env                     print the KUBECONFIG export line, for shells other than zsh
  kcx -v                      version
  kcx -h                      this help

This is a development build of kcx 2; the commands of the design land slice by
slice. kcx 1.0, the bash script, keeps working beside it.
`
