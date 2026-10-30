// Package secret speaks to secret providers: the programs that hold the password
// or token a fetch provider needs. The interface is four verbs, which is what the
// work broker kcx replaces already has:
//
//	get      writes the secret to standard output, which must be a pipe; exit 0;
//	         non-zero and nothing on stdout when there is no usable secret
//	refresh  obtains a new secret from the human and stores it; exit 1 on nothing
//	forget   drops whatever is cached; always exits 0
//	status   prints cached, expired or empty (with detail after a space); exit 0
//	         when cached, 1 otherwise
//
// Exit 3 from any verb says the provider cannot run on this machine at all (a
// script that a locked-down PowerShell refuses, say): get and refresh report
// ErrUnavailable, and a fetch then lets its command ask the person itself.
//
// kcx connects get's standard output to the fetch command's standard input and
// never holds the secret in a string, an argument or an environment variable of
// its own. Two providers are built in: prompt (no cache, asks at every fetch) and
// env:VAR (the variable's value; for CI and tests). A name ending in .ps1 is a
// PowerShell script (script.go).
package secret

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/junior/kcx/v2/internal/tty"
)

// Provider is a source of one secret.
type Provider interface {
	// Name is how the profile named it.
	Name() string
	// Get starts producing the secret. The reader yields it; wait reports whether
	// the provider succeeded and must be called after the reader is drained or
	// closed. A failed Get means there is no usable secret right now.
	Get(ctx context.Context) (r io.ReadCloser, wait func() error, err error)
	// Refresh asks the human for a new secret and stores it.
	Refresh(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error
	// Forget drops the cached secret.
	Forget(ctx context.Context) error
	// Status reports cached, expired or empty.
	Status(ctx context.Context) (string, error)
	// Caches says whether the provider keeps a secret between calls.
	Caches() bool
}

// LookPath is the shape of exec.LookPath, passed in so tests can supply one.
type LookPath func(string) (string, error)

// ErrUnavailable is a provider's exit 3: it cannot run on this machine at all.
var ErrUnavailable = errors.New("the provider cannot run here")

// exitUnavailable is the exit status that says so.
const exitUnavailable = 3

// Resolve is ResolveWith for a provider that is no script.
func Resolve(name string, lookPath LookPath, tty io.Reader) (Provider, error) {
	return ResolveWith(name, Options{LookPath: lookPath, TTY: tty})
}

// ResolveWith finds the provider a profile names: "" or "none" is no provider;
// "prompt" and "env:VAR" are built in; a name ending in .ps1 is a PowerShell
// script (script.go); an absolute path is run as is; any other name is looked up
// as kcx-secret-NAME on PATH, then as NAME itself.
func ResolveWith(name string, o Options) (Provider, error) {
	lookPath, tty := o.LookPath, o.TTY
	switch {
	case name == "" || name == "none":
		return nil, nil
	case name == "prompt":
		return &Prompt{tty: tty}, nil
	case isScript(name):
		return resolveScript(name, o)
	case strings.HasPrefix(name, "env:"):
		v := strings.TrimPrefix(name, "env:")
		if v == "" {
			return nil, errors.New("secret env: needs a variable name")
		}
		return &Env{Var: v}, nil
	case filepath.IsAbs(name):
		if _, err := os.Stat(name); err != nil {
			return nil, fmt.Errorf("secret provider %s: %w", name, err)
		}
		return &Exec{name: name, path: name}, nil
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	for _, candidate := range []string{"kcx-secret-" + name, name} {
		if p, err := lookPath(candidate); err == nil {
			return &Exec{name: name, path: p}, nil
		}
	}
	return nil, fmt.Errorf("secret provider %s: neither kcx-secret-%s nor %s is on PATH", name, name, name)
}

// Exec is a provider that is a program with the four verbs, or a PowerShell
// script that shell runs.
type Exec struct {
	name, path string
	shell      string // for a script: the PowerShell, by absolute path
	// Stderr receives the provider's own messages (prompts included); os.Stderr
	// when nil.
	Stderr io.Writer
}

// NewExec wraps a program at path.
func NewExec(name, path string) *Exec { return &Exec{name: name, path: path} }

func (e *Exec) Name() string { return e.name }
func (e *Exec) Caches() bool { return true }

func (e *Exec) stderr() io.Writer {
	if e.Stderr != nil {
		return e.Stderr
	}
	return os.Stderr
}

// Get runs `PROVIDER get` with its standard output on a pipe, so the secret
// travels from the provider to the fetch command through the kernel.
func (e *Exec) Get(ctx context.Context) (io.ReadCloser, func() error, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd := e.command(ctx, "get")
	cmd.Stdout = w
	cmd.Stderr = e.stderr()
	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, nil, fmt.Errorf("secret provider %s: %w", e.name, err)
	}
	_ = w.Close() // the child holds its own copy
	wait := func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("secret provider %s: get: %w", e.name, exitError(err))
		}
		return nil
	}
	return r, wait, nil
}

func (e *Exec) Refresh(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := e.command(ctx, "refresh")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("secret provider %s: refresh: %w", e.name, exitError(err))
	}
	return nil
}

func (e *Exec) Forget(ctx context.Context) error {
	cmd := e.command(ctx, "forget")
	cmd.Stderr = e.stderr()
	if err := cmd.Run(); err != nil {
		if err := exitError(err); !errors.Is(err, ErrUnavailable) { // one that cannot run here holds nothing
			return fmt.Errorf("secret provider %s: forget: %w", e.name, err)
		}
	}
	return nil
}

func (e *Exec) Status(ctx context.Context) (string, error) {
	cmd := e.command(ctx, "status")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = e.stderr()
	err := cmd.Run()
	status := strings.TrimSpace(out.String())
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			switch ee.ExitCode() {
			case 1:
				if status == "" {
					status = "empty"
				}
				return status, nil
			case exitUnavailable:
				if status == "" {
					status = "cannot run here"
				}
				return status + " (a fetch lets its command ask for the secret itself)", nil
			}
		}
		return status, fmt.Errorf("secret provider %s: status: %w", e.name, exitError(err))
	}
	if status == "" {
		status = "cached"
	}
	return status, nil
}

func exitError(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ee.ExitCode() == exitUnavailable {
			return fmt.Errorf("exit %d: %w", exitUnavailable, ErrUnavailable)
		}
		return fmt.Errorf("exit %d", ee.ExitCode())
	}
	return err
}

// Prompt asks the human at every fetch and caches nothing: for shared hosts
// where a cache would outlive the session.
type Prompt struct {
	tty io.Reader // the terminal to read from; /dev/tty when nil
	// Stderr is where the prompt is written; os.Stderr when nil.
	Stderr io.Writer
}

func (p *Prompt) Name() string { return "prompt" }
func (p *Prompt) Caches() bool { return false }

func (p *Prompt) Get(ctx context.Context) (io.ReadCloser, func() error, error) {
	value, err := p.ask("Password: ")
	if err != nil {
		return nil, nil, err
	}
	return io.NopCloser(bytes.NewReader(value)), func() error { return nil }, nil
}

func (p *Prompt) Refresh(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	return nil // nothing is stored; the next Get asks
}
func (p *Prompt) Forget(ctx context.Context) error { return nil }
func (p *Prompt) Status(ctx context.Context) (string, error) {
	return "empty (prompt: asks at every fetch, caches nothing)", nil
}

// ask reads a line without echo from the terminal, stripping the bracketed-paste
// markers some terminals wrap a paste in.
func (p *Prompt) ask(label string) ([]byte, error) {
	errw := p.Stderr
	if errw == nil {
		errw = os.Stderr
	}
	if p.tty != nil {
		fmt.Fprint(errw, label)
		line, err := readLine(p.tty)
		fmt.Fprintln(errw)
		if err != nil {
			return nil, err
		}
		return stripPaste(line), nil
	}
	t, err := tty.Open()
	if err != nil {
		return nil, errors.New("no terminal to ask for the secret on; run this from a terminal, or configure a secret provider that caches")
	}
	defer t.Close()
	fmt.Fprint(errw, label)
	value, err := term.ReadPassword(int(t.In.Fd()))
	fmt.Fprintln(errw)
	if err != nil {
		return nil, err
	}
	value = stripPaste(value)
	if len(value) == 0 {
		return nil, errors.New("no secret entered")
	}
	return value, nil
}

func readLine(r io.Reader) ([]byte, error) {
	var line []byte
	buf := make([]byte, 1)
	for {
		n, err := r.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
		}
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				break
			}
			return nil, errors.New("no secret entered")
		}
	}
	if len(line) == 0 {
		return nil, errors.New("no secret entered")
	}
	return line, nil
}

func stripPaste(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte("\x1b[200~"))
	b = bytes.TrimSuffix(b, []byte("\x1b[201~"))
	return bytes.TrimRight(b, "\r\n")
}

// Env takes the secret from an environment variable, for CI and tests.
type Env struct {
	Var    string
	Getenv func(string) string
}

func (e *Env) Name() string { return "env:" + e.Var }
func (e *Env) Caches() bool { return false }

func (e *Env) Get(ctx context.Context) (io.ReadCloser, func() error, error) {
	getenv := e.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	v := getenv(e.Var)
	if v == "" {
		return nil, nil, fmt.Errorf("secret provider env:%s: the variable is empty", e.Var)
	}
	return io.NopCloser(strings.NewReader(v)), func() error { return nil }, nil
}

func (e *Env) Refresh(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	return fmt.Errorf("secret provider env:%s cannot be refreshed; set the variable", e.Var)
}
func (e *Env) Forget(ctx context.Context) error { return nil }
func (e *Env) Status(ctx context.Context) (string, error) {
	getenv := e.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	if getenv(e.Var) == "" {
		return "empty", nil
	}
	return "cached (from the environment)", nil
}
