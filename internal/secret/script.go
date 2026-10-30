package secret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// A secret provider named by a path ending in .ps1 is a PowerShell script, which
// Go cannot start and a PATH lookup cannot find. kcx runs it with a PowerShell
// named by absolute path, never one found on PATH: Windows PowerShell
// (%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe) unless the
// powershell setting says pwsh (PowerShell 7: %ProgramFiles%\PowerShell\7\
// pwsh.exe on Windows, pwsh on PATH elsewhere) or gives an absolute path. Each
// verb runs as -NoLogo -NoProfile [-NonInteractive] -File SCRIPT VERB: get,
// status and forget with -NonInteractive, so a script that would prompt fails
// rather than wait; refresh without it, on the console. kcx passes no
// -ExecutionPolicy: the machine's policy applies to this script as to any other.

// Options are what resolving a provider may consult besides its name.
type Options struct {
	LookPath LookPath  // exec.LookPath when nil
	TTY      io.Reader // where the prompt provider reads (tests); the terminal when nil
	// For a script provider: the environment (os.Getenv when nil), what ~
	// stands for, the powershell setting, and the platform (runtime.GOOS when
	// empty).
	Getenv     func(string) string
	Home       string
	PowerShell string
	GOOS       string
}

func (o Options) getenv(k string) string {
	if o.Getenv != nil {
		return o.Getenv(k)
	}
	return os.Getenv(k)
}

func (o Options) goos() string {
	if o.GOOS != "" {
		return o.GOOS
	}
	return runtime.GOOS
}

func (o Options) lookPath() LookPath {
	if o.LookPath != nil {
		return o.LookPath
	}
	return exec.LookPath
}

func isScript(name string) bool { return strings.HasSuffix(strings.ToLower(name), ".ps1") }

func resolveScript(name string, o Options) (Provider, error) {
	path, err := findScript(name, o)
	if err != nil {
		return nil, err
	}
	shell, err := powerShell(o)
	if err != nil {
		return nil, fmt.Errorf("secret provider %s: %w", name, err)
	}
	return &Exec{name: name, path: path, shell: shell}, nil
}

// findScript locates a script provider: an absolute path, a path under ~, or a
// bare file name, looked for in the PATH directories. A relative path is
// refused: what it is relative to would depend on where kcx runs.
func findScript(name string, o Options) (string, error) {
	p := name
	switch {
	case strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`):
		if o.Home == "" {
			return "", fmt.Errorf("secret provider %s: no home directory for ~", name)
		}
		p = filepath.Join(o.Home, p[2:])
	case filepath.IsAbs(p):
	case !strings.ContainsAny(p, `/\`):
		for _, dir := range filepath.SplitList(o.getenv("PATH")) {
			if dir == "" {
				continue
			}
			candidate := filepath.Join(dir, p)
			if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
				return candidate, nil
			}
		}
		return "", fmt.Errorf("secret provider %s: not in any PATH directory", name)
	default:
		return "", fmt.Errorf(`secret provider %s: a script is named by an absolute path, by ~\..., or by a file name in a PATH directory`, name)
	}
	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("secret provider %s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("secret provider %s: %s is not a file", name, p)
	}
	return p, nil
}

// powerShell is the PowerShell that runs script providers, by absolute path.
func powerShell(o Options) (string, error) {
	var p string
	switch s := o.PowerShell; {
	case s == "" || s == "windows":
		if o.goos() != "windows" {
			return "", errors.New(`only Windows has Windows PowerShell; powershell = "pwsh" in config.toml runs scripts with PowerShell 7`)
		}
		root := o.getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		p = filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	case s == "pwsh" && o.goos() != "windows":
		found, err := o.lookPath()("pwsh")
		if err != nil {
			return "", errors.New("pwsh is not on PATH")
		}
		return found, nil
	case s == "pwsh":
		pf := o.getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		p = filepath.Join(pf, "PowerShell", "7", "pwsh.exe")
	case filepath.IsAbs(s):
		p = s
	default:
		return "", fmt.Errorf(`the powershell setting is "windows", "pwsh" or an absolute path, not %q`, s)
	}
	if info, err := os.Stat(p); err != nil || info.IsDir() {
		return "", fmt.Errorf("no PowerShell at %s", p)
	}
	return p, nil
}

// command is the process for one verb: the program, or for a script the
// PowerShell that runs it.
func (e *Exec) command(ctx context.Context, verb string) *exec.Cmd {
	if e.shell == "" {
		return exec.CommandContext(ctx, e.path, verb)
	}
	args := []string{"-NoLogo", "-NoProfile"}
	if verb != "refresh" {
		args = append(args, "-NonInteractive")
	}
	return exec.CommandContext(ctx, e.shell, append(args, "-File", e.path, verb)...)
}
