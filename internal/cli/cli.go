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
	"github.com/junior/kcx/internal/shell"
	"github.com/junior/kcx/internal/temp"
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
		return a.cmdPick()
	case "-c", "current":
		return a.cmdCurrent()
	case "run":
		return a.cmdRun(args[1:])
	case "-r":
		return a.cmdReadonly(args[1:])
	case "init":
		return a.cmdShell(shell.Init, "kcx init zsh|bash", args[1:])
	case "completion":
		return a.cmdShell(shell.Completion, "kcx completion zsh|bash", args[1:])
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

// cmdShell prints a shell snippet: `kcx init SHELL` or `kcx completion SHELL`.
func (a *App) cmdShell(f func(string) (string, error), usage string, args []string) error {
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
		for _, w := range []string{"ns", "fetch", "run", "-r", "profile", "secret", "ls", "env", "init", "completion", "doctor", "add", "rm", "mv", "adopt", "temp", "-c", "-", "-h", "-v"} {
			fmt.Fprintln(a.stdout, w)
		}
	case "run", "-r":
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

  kcx                         pick a context (fzf); the top line fetches a profile cluster
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
  kcx run NAME [-n NS] -- CMD...
                              run one command against NAME, the shared current context
                              untouched: what agents should use. A switch or a namespace
                              change inside stays inside. A cluster that is read-only by
                              default runs behind the read-only proxy unless --rw
  kcx -r [NAME] [-n NS] [-- CMD...]
                              a read-only shell for NAME, or one read-only command: a
                              local proxy refuses writes, exec, attach, port-forward and
                              cp before they leave the machine; auth can-i still works
  kcx init zsh|bash           the shell snippet: eval "$(kcx init zsh)" keeps KUBECONFIG
                              in step and installs completion
  kcx completion zsh|bash     the completion part alone
  kcx doctor                  check the layout, the files, the settings, the profiles
                              and the programs kcx relies on

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

Temp clusters, kind, one file each in ~/.kube/temp:
  kcx temp create NAME [--engine DOCKER_CONTEXT] [--config PRESET|FILE] [--use] [-- KIND_ARGS...]
                              DOCKER_CONTEXT: the engine to run on (default: docker's
                              current one); on a remote engine the API server listens
                              on the engine's network address. PRESET: a file in
                              ~/.config/kind; without --config, kind's own one-node
                              cluster. The context is kind-NAME.
  kcx temp ls                 temp clusters, and whether each still exists
  kcx temp rm NAME            delete the cluster and its kubeconfig
  kcx temp prune              delete the kubeconfigs of clusters that are gone
  kcx temp add NAME FILE|-|-- CMD...
                              keep another tool's temp cluster (k3d, minikube) here

  kcx -v                      version          kcx -h    this help

Agents: every shell shares one current context, so a switch in one session points
kubectl in all the others at another cluster. Name the cluster per command,
kubectl --context NAME or kcx run NAME -- CMD, and use kcx -r for anything that only
reads.

Examples:
  kcx add eks-lab -- aws eks update-kubeconfig --name lab --region us-west-2
  kcx add oke-lab -- oci ce cluster create-kubeconfig --cluster-id ID --file {}
  ssh pi sudo cat /etc/rancher/k3s/k3s.yaml | kcx add pi - --server https://pi.lan:6443
  kcx temp create t1 --engine lab --config calico
  kcx run kind-t1 -- kubectl get nodes
`
