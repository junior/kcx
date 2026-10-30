# Contributing

Thanks for looking. kcx is small on purpose, and a change that keeps it so is
the easiest to take.

- Open an issue first for anything beyond a small fix, so that we agree on its
  shape before you write it.
- A security problem goes through private vulnerability reporting, never an
  issue: see [SECURITY.md](SECURITY.md).

## Building and testing

Go 1.26 or later; `go.mod`'s toolchain line picks the exact release.

```
go build ./cmd/kcx
go vet ./...
go test ./...
```

The tests run in sandbox home directories. Keep it that way: no test may read
or write the real `~/.kube` or `~/.config`. A test that runs a POSIX shell
script calls `needsPOSIX(t)`, which skips it on Windows. CI also runs
staticcheck, govulncheck, the race detector (Linux and macOS) and a check that
text files are plain ASCII, on Linux, macOS and Windows, plus a PowerShell end
to end on Windows (`.github/windows-e2e.ps1`).

## What a change should keep

- Few dependencies: the client-go family, one TOML parser, x/term and x/sys.
  Ask before adding one.
- The shell snippets run in bash 3.2, bash 5 and zsh, and the PowerShell one
  in PowerShell 7 and Windows PowerShell 5.1, under Set-StrictMode.
- A secret is never an argument, an environment variable or a line of output.
- A message says what happened and what to do next.
- Plain ASCII in code, comments and documents.
- One change per commit: a subject that says what it does, a body that says
  why. A CHANGELOG entry for anything a user would notice.
