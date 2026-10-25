// Package picker drives fzf, when it is there and a human is at the terminal, and
// says so when it is not: kcx then prints a list instead. Rows are shown as
// aligned columns, which may carry colour; the value a row stands for travels
// in a hidden first field, so fzf matches and shows the columns only and the
// value comes back untouched.
package picker

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// ErrUnavailable means no picker can run: stdout is not a terminal, fzf is not
// on PATH, or the settings asked for plain lists.
var ErrUnavailable = errors.New("no picker available")

// ErrQuit means the user pressed q in a picker that offers it: the whole
// interaction is over, not only this step (ESC steps back, with "" and no error).
var ErrQuit = errors.New("quit")

// Line is one row: Value is what a pick returns when the row is chosen; Cols are
// what the row shows, padded into columns, and may hold ANSI colour codes.
type Line struct {
	Value string
	Cols  []string
}

// Picker finds and runs fzf.
type Picker struct {
	// Plain disables fzf even when it is there (settings: picker = "plain").
	Plain bool
	// Quit offers the q key: a pick returns ErrQuit when it is pressed. While it
	// is on, q cannot be typed into the filter; the other letters still find a
	// name that holds a q.
	Quit     bool
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

// Pick shows lines, each "value<TAB>column<TAB>column...", with the value as the
// first column shown, and returns the chosen value, or "" when the user
// cancelled.
func (p Picker) Pick(lines []string, prompt, header string) (string, error) {
	rows := make([]Line, 0, len(lines))
	for _, l := range lines {
		f := strings.Split(l, "\t")
		rows = append(rows, Line{Value: f[0], Cols: f})
	}
	return p.PickLines(rows, prompt, header)
}

// PickLines shows rows in fzf and returns the chosen row's Value: "" when the
// user cancelled (ESC, ctrl-c, nothing matched), ErrQuit when q was pressed and
// Quit is on.
func (p Picker) PickLines(rows []Line, prompt, header string) (string, error) {
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
	args := []string{"--prompt=" + prompt, "--header=" + header, "--height=40%", "--reverse", "--delimiter=\t", "--with-nth=2..", "--ansi"}
	if p.Quit {
		args = append(args, "--expect=q")
	}
	cmd := exec.Command(fzf, args...)
	cmd.Stdin = strings.NewReader(strings.Join(Render(rows), "\n") + "\n")
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
	value, quit := Parse(out.String(), p.Quit)
	if quit {
		return "", ErrQuit
	}
	return value, nil
}

// Render turns rows into the lines fzf reads: the value, a tab, then the
// columns padded to a common width and joined by two spaces. A row's trailing
// empty columns are left out, and its last column is not padded.
func Render(rows []Line) []string {
	var widths []int
	for _, r := range rows {
		for i, c := range r.Cols {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			if w := Width(c); w > widths[i] {
				widths[i] = w
			}
		}
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		cols := r.Cols
		for len(cols) > 0 && cols[len(cols)-1] == "" {
			cols = cols[:len(cols)-1]
		}
		var b strings.Builder
		b.WriteString(r.Value)
		b.WriteByte('\t')
		for i, c := range cols {
			if i > 0 {
				b.WriteString("  ")
			}
			b.WriteString(c)
			if i < len(cols)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-Width(c)))
			}
		}
		out = append(out, b.String())
	}
	return out
}

var ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")

// Width is the number of cells a column takes: its runes, colour codes aside.
func Width(s string) int {
	return utf8.RuneCountInString(ansi.ReplaceAllString(s, ""))
}

// Parse reads what fzf printed. With expect, the first line is the key that
// ended the pick (empty for Enter) and the chosen line follows; the value is
// the hidden first field of that line.
func Parse(out string, expect bool) (value string, quit bool) {
	out = strings.TrimRight(out, "\n")
	if expect {
		key, rest, _ := strings.Cut(out, "\n")
		if key == "q" {
			return "", true
		}
		out = rest
	}
	if i := strings.IndexByte(out, '\t'); i >= 0 {
		out = out[:i]
	}
	return out, false
}

var colors = map[string]string{
	"red": "31", "green": "32", "yellow": "33", "blue": "34", "magenta": "35", "cyan": "36", "white": "37",
}

// Paint wraps s in the ANSI colour named (red, green, yellow, blue, magenta,
// cyan, white), or returns it as it is for an empty or unknown name.
func Paint(color, s string) string {
	code, ok := colors[color]
	if !ok || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}
