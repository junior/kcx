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
- The lab-node guard (`KCX_LAB_NODE=1` until the settings file exists).
