// Package picker drives fzf, when it is there and a human is at the terminal, and
// says so when it is not: kcx then prints a list instead.
package picker

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// ErrUnavailable means no picker can run: stdout is not a terminal, fzf is not
// on PATH, or the settings asked for plain lists.
var ErrUnavailable = errors.New("no picker available")

// Picker finds and runs fzf.
type Picker struct {
	// Plain disables fzf even when it is there (settings: picker = "plain").
	Plain    bool
	Stdout   *os.File
	LookPath func(string) (string, error)
}

// Available says whether a pick can happen.
func (p Picker) Available() bool {
	if p.Plain || p.Stdout == nil || !term.IsTerminal(int(p.Stdout.Fd())) {
		return false
	}
	lookPath := p.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	_, err := lookPath("fzf")
	return err == nil
}

// Pick shows lines in fzf and returns the chosen line's first tab-separated
// field, or "" when the user cancelled. Each line is "value<TAB>description".
func (p Picker) Pick(lines []string, prompt, header string) (string, error) {
	if !p.Available() {
		return "", ErrUnavailable
	}
	lookPath := p.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	fzf, err := lookPath("fzf")
	if err != nil {
		return "", ErrUnavailable
	}
	cmd := exec.Command(fzf, "--prompt="+prompt, "--height=40%", "--reverse", "--delimiter=\t", "--tabstop=4", "--header="+header)
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr // fzf draws its UI on the terminal itself
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			switch ee.ExitCode() {
			case 1, 130: // no match, or ESC / ctrl-c: a cancel, not an error
				return "", nil
			}
		}
		return "", err
	}
	line := strings.TrimRight(out.String(), "\n")
	if i := strings.IndexByte(line, '\t'); i >= 0 {
		line = line[:i]
	}
	return line, nil
}
