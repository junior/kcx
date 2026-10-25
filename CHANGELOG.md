# Changelog

All notable changes to kcx. The format follows Keep a Changelog; versions
follow Semantic Versioning. kcx 1.0.0 was a bash script shipped in the
author's dotfiles; 2.0.0 is its rewrite in Go, in this repository.

## Unreleased: 2.0.0

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
  `{env.VAR}`), groups (provider, colour, read-only default) and clusters
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
