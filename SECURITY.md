# Security

kcx handles the files that hold your cluster credentials. Please report
anything that could expose them, or let a read-only shell change a cluster,
privately: open a report through GitHub's private vulnerability reporting on
this repository ("Report a vulnerability" under the Security tab). Do not open
a public issue for it. You should hear back within a week.

## What kcx promises

- A context switch writes only the state file (`~/.kube/config`); a file that
  holds credentials is rewritten only when its own content changes (import,
  rename, a namespace set on its context).
- Directories are created 0700 and files 0600.
- A secret handed to a fetch provider travels through a pipe from the secret
  provider's standard output to the fetch command's standard input: never a
  command-line argument, never an environment variable, never printed.
- The read-only shell (`kcx -r`, and `kcx run` or `kcx shell` on a read-only
  group) reaches the cluster through a local proxy that refuses, before
  anything leaves the machine, every request other than GET, HEAD and OPTIONS,
  the protocol upgrades that start exec, attach, port-forward and cp, the
  `proxy` subresource of pods, services and nodes, server-side dry runs and
  impersonation; it allows the authorization and authentication review
  requests.
- That proxy listens on 127.0.0.1, speaks TLS with a certificate made for the
  session, and answers only requests addressed to it that carry the session's
  token, which only the shell's kubeconfig holds, in a file only you can read:
  another user of the machine, or a web page that rebinds a name to 127.0.0.1,
  cannot use it. It is accident-proof, not adversary-proof: anything running as
  you can still read your kubeconfig files and bypass it with `--kubeconfig`.
- Release binaries are built from a signed tag by GitHub Actions, carry GitHub
  artifact attestations and an SBOM, and the macOS binaries are signed and
  notarised when the release has the certificates.

## Supported versions

The current minor release line receives fixes. Older releases do not.
