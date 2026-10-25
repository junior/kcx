# kcx

One kubeconfig file per cluster, and the tools to use them: a context picker, a
namespace switcher, cluster profiles that fetch a kubeconfig by short name
through your platform's own command, a read-only shell for AI tools, temp kind
clusters on any Docker engine.

**Status: in development.** This is the Go rewrite (2.0) of a bash tool; the
commands land slice by slice and nothing is released yet.

## The layout

Every cluster gets a kubeconfig file of its own, so importing one cluster can
never overwrite another one's credentials, which is what happens when every
tool merges into `~/.kube/config`.

| path | holds |
| --- | --- |
| `~/.kube/config` | the current context, and nothing else of kcx's. kubectl writes `current-context` into the first file of `KUBECONFIG`, so this one comes first and a switch never rewrites a file that holds credentials. A tool that writes "the default kubeconfig" lands here too; `kcx adopt` moves it into a file of its own |
| `~/.kube/configs/NAME.yaml` | one permanent cluster |
| `~/.kube/temp/NAME@ENGINE.yaml` | one temp kind cluster, `ENGINE` being the docker context it runs on; deleted with the cluster |

`KUBECONFIG` lists them in that order. `kcx env` prints the export line;
`kcx init zsh|bash` (coming) prints a snippet that keeps it current in every
shell without running kcx.

## Agents

Every shell shares one current context, so a switch made in one session points
kubectl in all the others at another cluster. Agents name the cluster per
command, `kubectl --context NAME` or `kcx run NAME -- CMD`, and use
`kcx -r NAME -- CMD` for anything that only reads.

## Building

Go 1.26 or later: `go build ./cmd/kcx`. Tests: `go test ./...`.

## License

MIT.
