# kcx

[![CI](https://github.com/junior/kcx/actions/workflows/ci.yml/badge.svg)](https://github.com/junior/kcx/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/junior/kcx)](https://github.com/junior/kcx/releases/latest)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/junior/kcx/badge)](https://scorecard.dev/viewer/?uri=github.com/junior/kcx)

One kubeconfig file per cluster, and the tools to use them: a context and
namespace switcher, cluster profiles that fetch a kubeconfig by short name
through your platform's own command with a brokered secret, a read-only shell
for AI tools, and temp kind clusters on any Docker engine.

![Terminal demo](.github/demo.gif)

The recording runs the example profile against a kind cluster, with two small
scripts standing in for the platform's command and its secret provider; every
name in it is made up. The prompt is Powerlevel10k in its pure style, with the
current cluster added in its group's colour. [.github/demo.tape](.github/demo.tape)
and [.github/demo-setup.sh](.github/demo-setup.sh) regenerate it.

**Status: released.** 2.0.0 is the Go rewrite of a bash tool that lived in the
author's dotfiles. Each release carries binaries for macOS, Linux and Windows on
amd64 and arm64, with an SBOM per archive, cosign-signed checksums and GitHub
build provenance. Install it with Homebrew, built from source
(`brew install junior/tap/kcx`), with mise from the release
(`mise use -g github:junior/kcx@latest`, which verifies the attestation), with
Go (`go install github.com/junior/kcx/v2/cmd/kcx@latest`, from 2.2.0 on), or
take an archive from the release page. Notarised macOS binaries come later.

## Why

When every tool merges into `~/.kube/config`, importing one cluster can
overwrite another one's credentials, two clusters can share a user entry, and
a context switch rewrites the file that holds every token. kcx keeps one file
per cluster, writes a switch into a state file that holds nothing else, and
never rewrites a credential file except when its own content changes.

## The layout

| path | holds |
| --- | --- |
| `~/.kube/config` | the current context, and nothing else of kcx's. kubectl writes `current-context` into the first file of `KUBECONFIG`, so this one comes first and a switch never rewrites a file that holds credentials. A tool that writes "the default kubeconfig" lands here too; `kcx adopt` moves it into a file of its own |
| `~/.kube/configs/NAME.yaml` | one permanent cluster: its cluster, user and context, named after the file by default |
| `~/.kube/temp/NAME@ENGINE.yaml` | one temp kind cluster, `ENGINE` being the Docker context it runs on; deleted with the cluster |

`KUBECONFIG` lists them in that order. `eval "$(kcx init zsh)"` (or `bash`)
keeps it current in every shell without running kcx, installs completion, and
leaves a `KUBECONFIG` you set on purpose alone. `kcx env` prints the export
line for anything else.

In PowerShell, on Windows or anywhere, `kcx init pwsh | Out-String |
Invoke-Expression` in your profile does the same before each prompt. It wraps
the prompt function in place, so load it after a prompt theme's own init
(oh-my-posh, starship); the theme still sees the last command's status.
`kcx env pwsh` prints `$env:` assignments.

## Commands

```
kcx                         pick a context (fzf); the top line browses your profiles
kcx NAME                    switch; fetch first when NAME is only in a profile or its
                            credentials no longer work (--refresh forces)
kcx -  |  kcx -c            previous context  |  current context
kcx --unset                 no current context; kcx - brings it back
kcx ns [NAME|-|-c|--unset]  namespace: switch, go back, print, unset; a picker without arguments
kcx ls [-q] [--group G] [PATTERN]
                            every context, where its file lives, its server, and what
                            kubectl would quietly get wrong; filtered by group or name
kcx run NAME [-n NS] -- CMD one command against one cluster, the shared context untouched
kcx shell [NAME] [-n NS]    a shell of its own: switches and namespaces inside stay inside
kcx -r [NAME] [-- CMD]      a read-only shell, or one read-only command
kcx fetch NAME... | --all | --group G | --missing
kcx add | rm | mv | adopt   permanent clusters: import, Trash, rename, adopt
kcx temp create|ls|rm|prune|add      temp kind clusters
kcx profile ls|check        the clusters your profiles know
kcx secret status|refresh|forget     talk to a secret provider; never prints a secret
kcx init | completion zsh|bash       shell integration
kcx doctor [--fix]          check the layout, the files, the settings and the profiles;
                            --fix repairs modes and stray current contexts
```

`kcx -h` has the details. kubectx's and kubens's long spellings work as well:
`--current`, `--readonly`, `--shell` (`-s`), `-V`.

## Profiles: bring your own cluster list

A profile is yours: which clusters exist, how each is fetched, which group and
site it belongs to, its short names. kcx ships none;
[examples/profiles/acme.toml](examples/profiles/acme.toml) shows the shape.
Profiles live in `~/.config/kcx/profiles/*.toml` (or wherever `KCX_PROFILES`
points), are merged in order, and a name defined twice is an error.

```toml
[provider.acme]
command  = ["acme", "get-kubeconfig", "{host}", "-u", "{user}", "-c", "{out}"]
secret   = "acme-secret"
host     = "{name}.k8s.acme.example"
user     = ["{env.ACME_USER}", "{env.USER}"]
rejected = "401|access_denied"

[group.production]
label    = "Production"
provider = "acme"
color    = "red"
readonly = true

[cluster.prod1]
group = "production"
site  = "Austin"
```

`kcx prod1` then fetches `prod1` through the command, imports the result as
`~/.kube/configs/prod1.yaml` with its entries named `prod1`, and switches to it. A
provider's `context_name = "{user}@{name}"` keeps the `user@cluster` shape
some platforms use; `kcx add --keep-names` imports a cloud CLI's kubeconfig
with the names the cloud gave it. The file is always `NAME.yaml`, and `kcx
NAME` resolves by file first.

`kcx` alone opens the picker over your contexts, and its top line browses the
profiles the way they are organised: group, then site, then cluster, in the
order the files list them (file by file, as their names sort), with a check
mark on the clusters that have a file and a star on the current one. A group's
`label` is what the picker shows for it, its `color` paints its rows (red,
green, yellow, blue, magenta, cyan or white; `NO_COLOR` turns that off), and a
site is any text. In the browse, ESC steps back and q quits. `kcx profile ls`
lists the same clusters as a table, in the same colours when its output is a
terminal.

### Secret providers

A provider command that needs a password gets it on standard input from a
secret provider: any program with four verbs, `get` (writes the secret to
standard output, which must be a pipe), `refresh` (asks you and stores it),
`forget`, `status`. The secret travels from `get` to the command through a pipe
and is never an argument, an environment variable or a string kcx keeps. Two
providers are built in: `prompt` (asks at every fetch, caches nothing) and
`env:VAR`.

On Windows a provider can be a PowerShell script: a `secret` ending in `.ps1`,
named by an absolute path, by `~\...`, or by a file name in a `PATH`
directory. kcx runs it with Windows PowerShell by its absolute path, never one
found on `PATH` (`powershell = "pwsh"` in the settings for PowerShell 7), as
`-NoLogo -NoProfile -NonInteractive -File SCRIPT VERB`; `refresh` runs without
`-NonInteractive`, on the console. kcx passes no execution policy: the
machine's applies.

A provider that cannot run on a machine at all, a script that a locked-down
PowerShell refuses for one, says so by exiting 3. kcx then lets the fetch
command ask for the secret itself: the command gets the terminal as its
standard input, nothing piped and no timeout, and after a refusal it may ask
once more. With no one at a terminal, nothing runs.

The lockout policy is an invariant: a refused secret is forgotten; if you are
there, kcx asks once and retries once; a second refusal, or no one to ask,
stops it; the first refusal during `kcx fetch --all` aborts the rest. Two bad
logins at most, so an automated run can never lock an account. A run asks for
a provider's secret once: after a refresh that failed, the rest of the run's
clusters for that provider fail at once instead of asking again.

## Read-only, for AI tools and for yourself

`kcx -r NAME -- CMD` runs a command, and `kcx -r NAME` opens a shell, whose
`KUBECONFIG` holds none of the cluster's credentials and points at a local
proxy. The proxy refuses, before anything leaves the machine: protocol
upgrades (exec, attach, port-forward, cp), the `proxy` subresource of pods,
services and nodes, impersonation (`--as`), and every method but GET, HEAD
and OPTIONS. It allows the authorization and authentication reviews, so
`kubectl auth can-i` works. kubectl shows a refusal as an error naming kcx.
The proxy speaks TLS and answers only requests that carry a token made for the
session, which only that `KUBECONFIG`, a file only you can read, holds: on a
shared machine, another user cannot borrow it. A group with `readonly = true`
makes `kcx run` use the proxy unless `--rw`. Accident-proof, not
adversary-proof: anything running as you can read the real files and bypass it
with `--kubeconfig`.

## One shell, one cluster

Every shell shares one current context, so a switch made in one terminal points
kubectl in all the others at another cluster. `kcx shell NAME` opens a shell of
its own instead: a private kubeconfig comes first in its `KUBECONFIG`, holding
a copy of each context (names only, never credentials) and the shell's current
one. `kcx NAME`, `kcx -`, `kcx ns`, the picker and kubectl's own `config
use-context` or `set-context` inside it change that file and nothing else; the
prompt and `kcx -c` show the shell's context; `exit` drops it all. A cluster
that is read-only by default opens a read-only shell unless `--rw`. A shell
another tool's startup file has pointed elsewhere gets its own `KUBECONFIG`
back at the first prompt.

## Agents

Agents name the cluster per command, `kubectl --context NAME` or `kcx run NAME
-- CMD`, and use `kcx -r NAME -- CMD` for anything that only reads. A switch or
a namespace change inside `kcx run` stays inside it.

## Windows

kcx runs on Windows, amd64 and arm64, as it does on macOS and Linux, and CI
runs its tests and a PowerShell end to end there. Its home is kubectl's:
`%USERPROFILE%` unless `HOME` is set (client-go's rule), so `~/.kube` above is
`%USERPROFILE%\.kube`, and `KUBECONFIG` is separated by `;`. `kcx rm` moves a
file to the Recycle Bin. `kcx shell` and the read-only shell start `SHELL`
when it is set, else PowerShell 7, else Windows PowerShell, else `cmd.exe`.
Prompts for a secret or a name read the console. A fetch command is a program,
as elsewhere (a PowerShell script runs as `powershell.exe -File SCRIPT`, by
full command line); a secret provider may be a PowerShell script itself (see
Secret providers). Windows has no file modes for kcx to set: your profile
directory's permissions keep `~/.kube` private, and `kcx doctor` does not
check modes there.

## Settings

`~/.config/kcx/config.toml`, all optional:

```toml
[settings]
lab_node     = false   # true: refuse permanent credentials on this machine
secret_cache = true    # false: forget a cached secret after every fetch (shared hosts)
probe        = true    # false: no authenticated check before switching to a profile cluster
picker       = "fzf"   # or "plain"
trash        = "auto"  # or "none"
powershell   = "windows" # or "pwsh", or an absolute path: what runs a .ps1 secret provider
```

## Building and testing

Go 1.26 or later. `go build ./cmd/kcx`; `go test ./...`. The tests run in
sandbox home directories and never touch yours. The shell snippets are tested
in bash 3.2, bash 5 and zsh, and the PowerShell one in pwsh, when those shells
are present. CI runs the suite on Linux, macOS and Windows; the fuzz tests run
on their seeds there, and `go test -fuzz FuzzCheck ./internal/proxy` explores
further. [CONTRIBUTING.md](CONTRIBUTING.md) has the rest.

## Security

See [SECURITY.md](SECURITY.md) for what kcx promises and how to report a
problem.

## License

MIT.
