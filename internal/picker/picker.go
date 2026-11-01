// Package picker lets the user pick a row: in fzf when it is on PATH, else in
// a numbered menu on the terminal, and says so when no one is at the terminal:
// kcx then prints a list instead. Rows are shown as aligned columns, which may
// carry colour; the value a row stands for travels in a hidden first field, so
// fzf matches and shows the columns only and the value comes back untouched.
package picker

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/junior/kcx/v2/internal/tty"
)

// ErrUnavailable means no pick can happen: standard output is not a terminal,
// or the terminal cannot be opened.
var ErrUnavailable = errors.New("no picker available")

// ErrQuit means the user pressed q in a picker that offers it: the whole
// interaction is over, not only this step (ESC steps back, with "" and no error).
var ErrQuit = errors.New("quit")

// Sep goes between the parts of a picker's header.
const Sep = "  |  "

// statusControlCExit is the exit status Windows gives a program that Ctrl-C
// ended.
const statusControlCExit = 0xC000013A

// Line is one row: Value is what a pick returns when the row is chosen; Cols are
// what the row shows, padded into columns, and may hold ANSI colour codes.
type Line struct {
	Value string
	Cols  []string
}

// Picker runs fzf, or the numbered menu.
type Picker struct {
	// Plain shows the numbered menu even where fzf is installed (settings:
	// picker = "plain").
	Plain bool
	// Quit offers q: a pick returns ErrQuit when it is pressed. In fzf, while it
	// is on, q cannot be typed into the filter; the other letters still find a
	// name that holds a q.
	Quit     bool
	Stdout   *os.File
	LookPath func(string) (string, error)
	// Term opens the terminal the numbered menu talks on: tty.Open when nil. A
	// test gives it a script, and then Stdout need not be a terminal.
	Term func() (io.ReadWriteCloser, error)
}

// Available says whether a pick can happen: someone is at the terminal, which
// standard output being one stands for.
func (p Picker) Available() bool {
	if p.Term != nil {
		return true
	}
	return p.Stdout != nil && term.IsTerminal(int(p.Stdout.Fd()))
}

// fzf is fzf's path, or "" when the numbered menu is to be shown.
func (p Picker) fzf() string {
	if p.Plain {
		return ""
	}
	lookPath := p.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	path, err := lookPath("fzf")
	if err != nil {
		return ""
	}
	return path
}

// Pick shows lines, each "value<TAB>column<TAB>column...", with the value as the
// first column shown, and returns the chosen value, or "" when the user
// cancelled. back says that a cancel here steps back to the step before.
func (p Picker) Pick(lines []string, prompt, header string, back bool) (string, error) {
	rows := make([]Line, 0, len(lines))
	for _, l := range lines {
		f := strings.Split(l, "\t")
		rows = append(rows, Line{Value: f[0], Cols: f})
	}
	return p.PickLines(rows, prompt, header, back)
}

// PickLines shows rows, in fzf or the numbered menu, and returns the chosen
// row's Value: "" when the user cancelled (ESC, ctrl-c, nothing matched; Enter
// in the menu), ErrQuit when q was pressed and Quit is on. back says that a
// cancel here steps back to the step before, and the hint shown with the rows
// says so; the caller does the stepping.
func (p Picker) PickLines(rows []Line, prompt, header string, back bool) (string, error) {
	if !p.Available() {
		return "", ErrUnavailable
	}
	if fzf := p.fzf(); fzf != "" {
		return p.runFzf(fzf, rows, prompt, join(header, keys(false, p.Quit, back)))
	}
	open := p.Term
	if open == nil {
		open = func() (io.ReadWriteCloser, error) {
			t, err := tty.Open()
			if err != nil {
				return nil, err
			}
			return t, nil
		}
	}
	t, err := open()
	if err != nil {
		return "", ErrUnavailable
	}
	defer t.Close()
	defer catchInterrupt()()
	return Menu(t, t, rows, header, p.Quit, back)
}

func (p Picker) runFzf(fzf string, rows []Line, prompt, header string) (string, error) {
	args := []string{"--prompt=" + prompt, "--header=" + header, "--height=40%", "--reverse", "--delimiter=\t", "--with-nth=2..", "--ansi"}
	if p.Quit {
		args = append(args, "--expect=q")
	}
	cmd := exec.Command(fzf, args...)
	cmd.Stdin = strings.NewReader(strings.Join(Render(rows), "\n") + "\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr // fzf draws its UI on the terminal itself
	// A Windows console sends Ctrl-C to every program on it: without a handler
	// kcx would end there, before fzf's own exit says the pick was cancelled.
	if !signal.Ignored(os.Interrupt) {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, os.Interrupt)
		defer signal.Stop(sigs)
	}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// No match (1), ESC or ctrl-c (130, or Windows' status for a program
			// Ctrl-C ended), or a signal (-1): a cancel, not an error.
			if code := ee.ExitCode(); code == 1 || code == 130 || code == -1 || uint32(code) == statusControlCExit {
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

// keys is the hint a step shows: what ESC and q do in fzf, or what the menu
// takes besides a number.
func keys(menu, quit, back bool) string {
	if menu {
		s := "Enter = cancel"
		if back {
			s = "Enter = back"
		}
		if quit {
			s += ", q = quit"
		}
		return s
	}
	s := "ESC cancels"
	if back {
		s = "ESC back"
	}
	if quit {
		s += "  q quits"
	}
	return s
}

func join(header, hint string) string {
	if header == "" {
		return hint
	}
	return header + Sep + hint
}

// Menu is the picker without fzf: the header, the rows numbered from 1, then a
// question, its answer read from in a line at a time. A number picks its row
// and returns the row's Value. Enter (an empty line), or q where quit is off,
// returns "": a cancel, or a step back where back is set. q returns ErrQuit
// where quit is on. The end of the input, or a read that fails (Ctrl-C on a
// Windows console), ends it as q does. Anything else asks again.
func Menu(in io.Reader, out io.Writer, rows []Line, header string, quit, back bool) (string, error) {
	if header != "" {
		fmt.Fprintln(out, header)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "  (nothing to pick)")
		return "", nil
	}
	width := len(strconv.Itoa(len(rows)))
	for i, l := range Render(rows) {
		_, shown, _ := strings.Cut(l, "\t")
		fmt.Fprintf(out, "  %*d) %s\n", width, i+1, shown)
	}
	question := fmt.Sprintf("Select 1-%d (%s): ", len(rows), keys(true, quit, back))
	for {
		fmt.Fprint(out, question)
		line, err := readLine(in)
		answer := strings.TrimSpace(line)
		if err != nil && answer == "" {
			fmt.Fprintln(out)
			if quit {
				return "", ErrQuit
			}
			return "", nil
		}
		if answer == "" || strings.EqualFold(answer, "q") {
			if answer != "" && quit {
				return "", ErrQuit
			}
			return "", nil
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(rows) {
			return rows[n-1].Value, nil
		}
		fmt.Fprintf(out, "not a choice: %s\n", answer)
	}
}

// readLine reads one line, without its newline, a byte at a time: nothing typed
// after it is taken from a terminal that the next step reads again.
func readLine(r io.Reader) (string, error) {
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := r.Read(b)
		if n > 0 {
			if b[0] == '\n' {
				return string(line), nil
			}
			line = append(line, b[0])
		}
		if err != nil {
			return string(line), err
		}
	}
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
