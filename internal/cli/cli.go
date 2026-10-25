// Package cli is kcx's command line: argument handling, the usage text and the
// glue between the commands and the packages that do the work.
package cli

import (
	"errors"
	"fmt"
	"io"

	"strings"

	"github.com/junior/kcx/internal/config"
	"github.com/junior/kcx/internal/kubeconfig"
	"github.com/junior/kcx/internal/layout"
	"github.com/junior/kcx/internal/profile"
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
		case "", "ls", "list", "env", "-c", "current", "ns", "__complete", "-h", "--help", "help", "-v", "--version":
		default:
			return fmt.Errorf("this is a READ-ONLY shell for %s: only kcx ls, env, -c and ns work here; exit it first", a.getenv(EnvRO))
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
		return a.cmdLs(nil)
	case "-c", "current":
		return a.cmdCurrent()
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
	last := ""
	if len(words) > 0 {
		last = words[len(words)-1]
	}
	switch last {
	case "--context", "--server", "--context-name", "--user-name", "-":
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
		for _, w := range []string{"ns", "fetch", "profile", "secret", "ls", "env", "add", "rm", "mv", "adopt", "-c", "-", "-h", "-v"} {
			fmt.Fprintln(a.stdout, w)
		}
	case "ns", "namespace":
		if len(words) == 1 {
			for _, w := range []string{"-c", "-", "--force"} {
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

const usageText = `kcx - one kubeconfig file per cluster under ~/.kube, and the tools to use them.

  kcx                         list the contexts (the picker comes in a later build)
  kcx NAME [-u USER]          switch to NAME: a cluster file, a context, kind-NAME, or a
                              profile cluster, fetched first when its file is missing or
                              its credentials no longer work (--refresh: fetch regardless;
                              --no-probe: do not check them)
  kcx -                       switch to the previous context
  kcx -c                      print the current context
  kcx ls [-q]                 every context: where its file lives and its server
                              (-q: names only)
  kcx ns [NAME|-|-c]          switch the current context's namespace, go back, or print
                              it; without an argument, list the cluster's namespaces
      --force                 set it without asking the cluster whether it exists
  kcx env                     print the KUBECONFIG export line, for shells other than zsh

Profiles, ~/.config/kcx/profiles/*.toml (KCX_PROFILES overrides): the clusters a
machine may fetch, in groups, each through a provider command and a secret provider.
kcx ships none; kcx profile check validates yours.
  kcx fetch NAME...           fetch these clusters' kubeconfigs (a refresh when the file exists)
  kcx fetch --all             every profile cluster that has a file, current context kept
  kcx fetch --group G         every cluster of group G
  kcx fetch --missing         every profile cluster without a file
      -u USER                 the user, instead of the profile's template
      --use                   switch afterwards (one cluster)
  kcx profile ls              the clusters the profiles know, and which are fetched
  kcx profile check           read the profiles and report mistakes
  kcx secret status|refresh|forget [PROVIDER]
                              talk to a secret provider; never prints a secret

Permanent clusters, one file each in ~/.kube/configs:
  kcx add NAME FILE           import the kubeconfig in FILE as NAME
  kcx add NAME -              ... read from standard input
  kcx add NAME -- CMD...      run CMD with KUBECONFIG set to a new, empty file and
                              import what it writes there; an argument {} is replaced
                              by that file's path, for tools with a file option
      -f                      replace an existing NAME (the old file goes to the Trash)
      --context CTX           the context to take, when the source holds several
      --server URL            rewrite the cluster's server address
      --use                   switch to NAME afterwards
      --keep-names            keep the context and user names the source gave them
      --context-name T        name the context from a template: {name}, {user}, {cluster}
      --user-name T           name the user from a template
  kcx rm NAME                 move NAME's file to the Trash
  kcx mv OLD NEW              rename a permanent cluster
  kcx adopt CTX [NAME]        move a context another tool wrote into ~/.kube/config
                              into a file of its own

  kcx -v                      version          kcx -h    this help

Examples:
  kcx add eks-lab -- aws eks update-kubeconfig --name lab --region us-west-2
  kcx add oke-lab -- oci ce cluster create-kubeconfig --cluster-id ID --file {}
  ssh pi sudo cat /etc/rancher/k3s/k3s.yaml | kcx add pi - --server https://pi.lan:6443

This is a development build of kcx 2; the picker, the read-only shell, run and temp
clusters land in the next builds. kcx 1.0, the bash script, keeps working beside it.
`
