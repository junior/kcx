package cli

import (
	"os"
	"strings"

	"golang.org/x/term"
)

// usageText is kcx -h. A line without indent is a heading; a line that starts
// with two spaces and "kcx" is a command, its description after two spaces or
// more; a line that starts with six spaces and a dash is an option. The
// colouring below relies on that shape.
const usageText = `kcx - one kubeconfig file per cluster under ~/.kube, and the tools to use them.

Contexts and namespaces
  kcx                         pick a context (fzf); the top line browses the profiles
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

One cluster per command or per shell, and read-only
  kcx run NAME [-n NS] -- CMD...
                              run one command against NAME, the shared current context
                              untouched: what agents should use. A switch or a namespace
                              change inside stays inside. A cluster that is read-only by
                              default runs behind the read-only proxy unless --rw
  kcx shell [NAME] [-n NS] [--rw]
                              a shell of its own for NAME: kcx NAME, kcx -, kcx ns and
                              kubectl config changes inside it stay inside it, and the
                              other shells keep their context. A cluster that is read-only
                              by default opens a read-only shell unless --rw
  kcx -r [NAME] [-n NS] [-- CMD...]
                              a read-only shell for NAME, or one read-only command: a
                              local proxy refuses writes, exec, attach, port-forward and
                              cp before they leave the machine; auth can-i still works.
                              Inside the shell, kcx ns changes the shell's namespace only

Profiles: the clusters a machine may fetch
  ~/.config/kcx/profiles/*.toml (KCX_PROFILES overrides), in groups, each through a
  provider command and a secret provider. kcx ships none; kcx profile check validates
  yours.
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

Permanent clusters, one file each in ~/.kube/configs
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

Temp clusters, kind, one file each in ~/.kube/temp
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

Shell and setup
  kcx env                     print the KUBECONFIG export line, for shells other than zsh
  kcx init zsh|bash           the shell snippet: eval "$(kcx init zsh)" keeps KUBECONFIG
                              in step and installs completion
  kcx completion zsh|bash     the completion part alone
  kcx doctor [--fix]          check the layout, the files, the settings, the profiles
                              and the programs kcx relies on; --fix first repairs the
                              modes and a current context a cluster file sets of its own
  kcx -v                      version
  kcx -h                      this help (plain when NO_COLOR is set or output is a file)

Agents
  Every shell shares one current context, so a switch in one session points kubectl
  in all the others at another cluster. Name the cluster per command, kubectl
  --context NAME or kcx run NAME -- CMD, and use kcx -r for anything that only reads.

Examples
  kcx add eks-lab -- aws eks update-kubeconfig --name lab --region us-west-2
  kcx add oke-lab -- oci ce cluster create-kubeconfig --cluster-id ID --file {}
  ssh pi sudo cat /etc/rancher/k3s/k3s.yaml | kcx add pi - --server https://pi.lan:6443
  kcx temp create t1 --engine lab --config calico
  kcx run kind-t1 -- kubectl get nodes
`

// helpText is the usage: coloured when standard output is a terminal and
// NO_COLOR is not set, plain otherwise.
func (a *App) helpText() string {
	f, ok := a.stdout.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) || a.getenv("NO_COLOR") != "" {
		return usageText
	}
	return colourUsage(usageText)
}

const (
	sgrReset  = "\x1b[0m"
	sgrBold   = "\x1b[1m"
	sgrCyan   = "\x1b[36m"
	sgrYellow = "\x1b[33m"
)

// colourUsage paints the usage by the shape usageText describes: headings bold,
// commands cyan up to their description, options yellow, the rest as it is.
func colourUsage(text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		switch {
		case l == "":
		case i == 0:
			lines[i] = sgrBold + "kcx" + sgrReset + strings.TrimPrefix(l, "kcx")
		case !strings.HasPrefix(l, " "):
			lines[i] = sgrBold + l + sgrReset
		case strings.HasPrefix(l, "  kcx "):
			head, rest := splitColumns(l)
			lines[i] = sgrCyan + head + sgrReset + rest
		case strings.HasPrefix(l, "      -"):
			head, rest := splitColumns(l)
			lines[i] = sgrYellow + head + sgrReset + rest
		}
	}
	return strings.Join(lines, "\n")
}

// splitColumns cuts a usage line at the first run of two spaces after its
// indent: the command or option, and the description that follows.
func splitColumns(l string) (string, string) {
	indent := len(l) - len(strings.TrimLeft(l, " "))
	if i := strings.Index(l[indent:], "  "); i >= 0 {
		return l[:indent+i], l[indent+i:]
	}
	return l, ""
}
