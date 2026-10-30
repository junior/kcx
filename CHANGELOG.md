# Changelog

All notable changes to kcx. The format follows Keep a Changelog; versions
follow Semantic Versioning. kcx 1.0.0 was a bash script shipped in the
author's dotfiles; 2.0.0 is its rewrite in Go, in this repository.

## 2.1.2 (2026-10-29)

### Security

- The read-only proxy (`kcx -r`, and `kcx run` or `kcx shell` on a read-only
  group) answered every request that reached its port on 127.0.0.1. While one
  ran, another user of the same machine could read through it whatever your
  credentials can read, Secrets included, and a web page could try the same by
  rebinding a name of its own to 127.0.0.1. It now speaks TLS, with a
  certificate made for the session by a CA made for the session, and answers
  only requests addressed to 127.0.0.1 or localhost that carry a token made
  for the session, which only the shell's kubeconfig (mode 0600) holds. The
  token stops at the proxy: the API server sees the same credentials as
  before. Every release from 2.0.0 to 2.1.1 is affected.
- The read-only proxy refuses impersonation (`kubectl --as`, `--as-group`,
  `--as-uid`), which would act as someone else with your credentials. A
  request that brings credentials of its own (`kubectl --token`) is refused
  too, where it used to reach the cluster with them.
- golang.org/x/net 0.62.0, for GO-2026-6866 and GO-2026-6860; govulncheck
  finds no call from kcx into either.

## 2.1.1 (2026-10-27)

### Changed

- `kcx profile ls` paints each cluster's name and group in the group's
  `color`, as the picker already does, when its output is a terminal and
  `NO_COLOR` is not set; into a file or a pipe the table stays plain.

## 2.1.0 (2026-10-26)

### Added

- `kcx shell [NAME] [-n NS] [--rw]`: a shell of its own for one cluster. A
  private kubeconfig comes first in its `KUBECONFIG`, holding a copy of every
  context (names only, never credentials) and the shell's current context, so
  `kcx NAME`, `kcx -`, `kcx ns`, the picker, every `--use` and kubectl's own
  `config use-context` and `set-context` inside it change that file and
  nothing else; the previous context and namespaces are the shell's own too.
  NAME resolves as `kcx NAME` does, fetching when the file is missing or the
  login is dead; without NAME, a picker. A cluster that is read-only by
  default opens a read-only shell unless `--rw`.
- The shell integration knows a kcx shell (`KCX_SHELL`, its value tracked by
  `KCX_SHELL_AUTO`, so an older inline copy of the function that knows only
  `KCX_AUTO` leaves it alone), and at the first prompt of a kcx shell, a
  read-only shell or a run puts back that shell's own `KUBECONFIG` when a
  startup file read after the snippet set another. Its prompt hooks now come
  first, so the prompt shows what is current.
- `kcx --unset`: no current context, as kubectx's `-u` does; the context that
  was current becomes the previous one, so `kcx -` brings it back. Long form
  only: `-u` stays the user of a fetch.
- `kcx ns --unset`: the current context sets no namespace any more, so
  kubectl uses default; the old one becomes the previous namespace.
- `kcx ls [--group G] [PATTERN]`: the contexts of a profile group, and names
  that contain PATTERN or, with `*`, `?` or `[`, match it as a glob; with `-q`
  too. `--group` completes the profile's group names, for `fetch` as well.
- kubectx's and kubens's long spellings: `--current` (also for `kcx ns`),
  `--readonly`, `--shell` and `-s`, `-V`.
- `kcx run`, `kcx shell` and `kcx -r` outlive the signals meant for their
  child: Ctrl-C reaches the child from the terminal, a terminate or a hang-up
  (a closed terminal window) is passed on to it, and kcx then removes its
  private files and returns the child's status, 128 plus the signal's number
  when a signal ended it. A signal ignored when kcx started stays ignored.
- `kcx run` exports `KCX_RUN_CONFIG`, its private file: `kcx env` and the
  first-prompt restore give an interactive shell inside a run its kubeconfig
  back, and `ls` there no longer reports the run's copy as a duplicate.

### Fixed

- A read-only shell starts in the context's own namespace, as kubectl would,
  rather than in `default`; `-n` still overrides it.
- `kcx init zsh` sorts the cluster files bytewise, as the Go side does, so
  `kcx doctor` no longer calls `KUBECONFIG` behind the layout under a locale
  that sorts mixed-case names otherwise.

## 2.0.1 (2026-10-26)

### Fixed

- `kcx NAME` fetches again when the probe fails in the credential layer
  itself, not only on a 401: a refresh token the platform has rotated or
  revoked ("failed to refresh token: oauth2: ..."), or a credential plugin that
  failed. Before, such a failure counted as "did not answer the probe" and kcx
  switched anyway, to a context kubectl could not use. When the fetch fails
  too, the error reports both failures. A refresh that works still needs no
  fetch.

## 2.0.0 (2026-10-25)

The first release of the rewrite, after five candidates tested on the author's
Mac, work laptop and lab node; their sections below say what each brought.
Install: `brew install junior/tap/kcx` (built from source), `mise use -g
github:junior/kcx@latest` (the release binary, its attestation verified), or an
archive from the release page.

### Added

- The layout of 1.0: `~/.kube/config` as the state file, one permanent
  cluster per file under `~/.kube/configs`, one temp cluster per file under
  `~/.kube/temp`, `KUBECONFIG` listing them in that order.
- `kcx env`, `kcx -v`, `kcx -h`, and the hidden `__complete` word list the
  shell completions call.
- Contexts: `kcx NAME` (a cluster file, a context, or `kind-NAME`), `kcx -`,
  `kcx -c`, `kcx ls [-q]` with its warnings about what kubectl would quietly
  get wrong (an entry defined twice, a stray current context, files outside
  `KUBECONFIG`). A switch writes the state file and nothing else.
- Permanent clusters: `kcx add NAME FILE|-|-- CMD...` (self-contained import,
  entries renamed to NAME, `--context`, `--server`, `--use`, `-f` with the old
  file going to the Trash), `kcx rm`, `kcx mv`, `kcx adopt`.
- Context and user names from a template (`--context-name '{user}@{name}'`,
  `--user-name`) and `--keep-names` for kubeconfigs a cloud CLI wrote; the
  file name stays the short name and `kcx NAME` resolves by file first.
- The lab-node guard (`lab_node = true` in the settings file, or `KCX_LAB_NODE=1`).
- Machine settings in `~/.config/kcx/config.toml`: `lab_node`, `secret_cache`,
  `probe`, `picker`, `trash`; an unknown key is an error, not a default.
- Profiles in `~/.config/kcx/profiles/*.toml` (`KCX_PROFILES` overrides):
  providers (a command template with `{name}`, `{host}`, `{user}`, `{out}`,
  `{env.VAR}`), groups (provider, label, colour, read-only default) and clusters
  (group, site, aliases, overrides); merged in order, a name defined twice is
  an error; `kcx profile ls|check`.
- `kcx fetch NAME... | --all | --group G | --missing [-u USER] [--use]`: the
  provider command runs with `KUBECONFIG` and `KCX_OUT` pointing at a private
  file and the secret on its standard input, newline-terminated, straight from
  the secret provider's `get` through a pipe; the result is imported as the
  short name; `--all` keeps the current context.
- Secret providers behind one interface, the four verbs `get` (pipe only),
  `refresh`, `forget`, `status`: any program named in a profile (looked up as
  `kcx-secret-NAME`, then `NAME`), plus the built-in `prompt` (asks at every
  fetch, caches nothing) and `env:VAR`. `kcx secret status|refresh|forget`.
- The lockout policy: a refused secret is forgotten; with a human present kcx
  asks once and retries once; a second refusal, or no human, stops; the first
  refusal during `fetch --all` aborts the rest; a network failure keeps the
  cache. Two bad logins at most.
- `kcx NAME` fetches first when NAME is only in a profile, or when a quick
  authenticated probe (GET /version, two seconds) answers 401; `--refresh`
  forces, `--no-probe` or `probe = false` skips the probe; a network failure
  switches anyway and says so. Switching to a read-only cluster warns.
- `kcx ns [NAME|-|-c] [--force]`: the namespace is written into the context's
  own file, as kubectl does; the previous namespace lives in
  `~/.kube/kubens/<context>`, kubens's own file; NAME is checked against the
  cluster unless `--force`; inside a read-only shell `ns` writes the read-only
  kubeconfig.
- `kcx run NAME [-n NS] [--rw] -- CMD...`: a private state file first in
  `KUBECONFIG` holds a shadow of the context (the real cluster and user by
  name, the run's own namespace), so a switch or a namespace change inside the
  command lands there and nowhere else; the child's exit status is kcx's. A
  cluster that is read-only by default runs behind the proxy unless `--rw`.
- `kcx -r [NAME] [-n NS] [-- CMD...]`: the read-only shell and one-shot, now
  an in-process reverse proxy on client-go's transport (exec plugins and OIDC
  included), with no credential copy on disk. Refused before leaving the
  machine: protocol upgrades (exec, attach, port-forward, cp), the `proxy`
  subresource of pods, services and nodes, every method but GET, HEAD and
  OPTIONS; allowed: the authorization and authentication reviews, so
  `kubectl auth can-i` works. Refusals are a Kubernetes Status that kubectl
  renders as an error naming kcx.
- The picker: a bare `kcx` runs fzf over the contexts when fzf is on PATH and
  stdout is a terminal (`picker = "plain"` turns it off); the top line browses
  the profiles the way the clusters are organised, group, then site, then
  cluster, with the fetched ones marked, and fetches or switches; `kcx ns`
  without an argument picks a namespace; `kcx -r` without a name picks a
  context.
- `kcx init zsh|bash` and `kcx completion zsh|bash`: the `KUBECONFIG` rule in
  pure shell (no kcx run per shell start), the guard for `KCX_RO` and
  `KCX_RUN`, a prompt hook that recomputes the variable, and completion over
  kcx's own word list; tested in bash 3.2, bash 5 and zsh.
- `kcx doctor`: permissions, duplicate entries across files, a managed file
  with a current context, files `KUBECONFIG` does not list, the shell's
  `KUBECONFIG`, the settings, the profiles and whether their commands and
  secret providers are on PATH.
- Temp kind clusters, as in 1.0: `kcx temp create NAME [--engine CTX]
  [--config PRESET|FILE] [--use] [-- KIND_ARGS...]` (on a remote engine the
  API server listens on the engine's address on a port kcx picks, with a
  retry on a collision), `kcx temp ls|rm|prune|add`, and the picker's "+ new
  temp cluster" wizard.
- Releases: GoReleaser builds darwin, linux and windows on amd64 and arm64
  plus a macOS universal binary, with checksums, SBOMs, keyless cosign
  signatures, GitHub artifact attestations, and Apple signing and
  notarisation when the release has the certificates.
- Since the first candidate: profiles keep groups and clusters in the order
  written, and a group takes a `label` (what the picker shows) and one of seven
  colour names; the picker shows aligned columns in the groups' colours, the
  browse line first, and the drill-down marks fetched and current clusters,
  ESC stepping back and q quitting; `kcx doctor --fix` repairs modes and a
  cluster file's own current context, and same-cause findings fold into one
  line; `kcx -h` is grouped under headings and coloured on a terminal; `kcx
  ns`, `kcx init` and `kcx completion` work inside a read-only shell.

## 2.0.0-rc.5 (2026-10-25)

### Fixed

- `kcx init` and `kcx completion` work inside a read-only shell. A startup
  file that evaluates `kcx init zsh` runs in that shell too, and the refusal
  it got made Powerlevel10k's instant prompt warn about console output during
  initialisation. The snippet keeps the read-only `KUBECONFIG` on its own.

## 2.0.0-rc.4 (2026-10-25)

Two things the second day of use asked for.

### Changed

- `kcx ns` works inside a read-only shell: the shell's own kubeconfig, which
  holds no credentials, is now writable by its owner, and the namespace change
  stays in the shell. Before, the file's mode refused the write.
- `kcx -h` is grouped under headings and, on a terminal, coloured: headings in
  bold, commands in cyan, options in yellow; plain when `NO_COLOR` is set or
  the output is not a terminal.

## 2.0.0-rc.3 (2026-10-25)

The picker after a day of use beside the tool it replaces, and a doctor that
repairs what it finds. Everything listed under 2.0.0 above is in it.

### Added

- `kcx doctor --fix` repairs the modes of the layout's directories and files
  and unsets a current context a cluster file sets of its own; findings of one
  kind across many files fold into one line.
- A profile group takes a `label`, what the picker shows for it.

### Changed

- Profiles keep groups and clusters in the order written, file by file; the
  picker, `kcx fetch --all`, `--group` and `--missing`, and `kcx profile ls`
  walk them that way.
- The picker shows aligned columns: the browse line first, then each context
  with its group (the label, in the group's colour), its site and a star on
  the current one. The drill-down marks fetched clusters with a check mark,
  names its steps in short headers, and q quits from any step while ESC steps
  back. A group's `color` must be one of red, green, yellow, blue, magenta,
  cyan and white; `NO_COLOR` turns colours off.
- The example profile's clusters are prod1, prod2, prod3, office2-lab and
  office7-lab, at the sites austin, atlanta and bastrop.

## 2.0.0-rc.2 (2026-10-25)

A pre-release for the author's own tests on every kind of machine, through the
channels 2.0.0 will use. Everything listed under 2.0.0 above is in it. (rc.1
was never published: its release job stopped at the cosign step, which now
writes a Sigstore bundle.)
