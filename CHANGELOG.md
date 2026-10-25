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
