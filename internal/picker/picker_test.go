package picker

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// fzf itself is only exercised by hand; what kcx feeds it and reads back is
// pure and tested here.
func TestRenderAlignsColumns(t *testing.T) {
	rows := []Line{
		{Value: "a", Cols: []string{"a", "x", ""}},
		{Value: "bb", Cols: []string{Paint("red", "bb"), "yy", "z"}},
		{Value: "ccc", Cols: []string{"ccc"}},
	}
	got := Render(rows)
	want := []string{
		"a\ta    x",
		"bb\t" + Paint("red", "bb") + "   yy  z",
		"ccc\tccc",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("render:\n%q\nwant\n%q", got, want)
	}
	if Width(Paint("red", "\u2713")) != 1 || Width("abc") != 3 || Width("") != 0 {
		t.Fatal("width counts runes, colour codes aside")
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		out    string
		expect bool
		value  string
		quit   bool
	}{
		{"prod1\tprod1  x\n", false, "prod1", false},
		{"\nprod1\tprod1  x\n", true, "prod1", false},
		{"q\nprod1\tprod1  x\n", true, "", true},
		{"prod1\n", false, "prod1", false},
		{"", false, "", false},
	}
	for _, c := range cases {
		v, q := Parse(c.out, c.expect)
		if v != c.value || q != c.quit {
			t.Errorf("Parse(%q, %v) = %q, %v; want %q, %v", c.out, c.expect, v, q, c.value, c.quit)
		}
	}
}

func TestPaint(t *testing.T) {
	if Paint("red", "x") != "\x1b[31mx\x1b[0m" || Paint("", "x") != "x" || Paint("plaid", "x") != "x" || Paint("red", "") != "" {
		t.Fatal("paint")
	}
	for _, c := range []string{"red", "green", "yellow", "blue", "magenta", "cyan", "white"} {
		if Paint(c, "x") == "x" {
			t.Errorf("%s does not paint", c)
		}
	}
}

func TestPickWithoutTerminal(t *testing.T) {
	p := Picker{}
	if p.Available() {
		t.Fatal("no stdout, no picker")
	}
	if _, err := p.PickLines([]Line{{Value: "a", Cols: []string{"a"}}}, "> ", "", false); err != ErrUnavailable {
		t.Fatalf("PickLines without a terminal: %v", err)
	}
}

// script is a terminal that types in and records what the menu shows.
type script struct {
	io.Reader
	out *bytes.Buffer
}

func (s script) Write(p []byte) (int, error) { return s.out.Write(p) }
func (s script) Close() error                { return nil }

func TestMenu(t *testing.T) {
	rows := []Line{
		{Value: "alpha", Cols: []string{"alpha", "Lab"}},
		{Value: "beta", Cols: []string{Paint("red", "beta"), "Production", "*"}},
	}
	cases := []struct {
		name, in   string
		quit, back bool
		value      string
		err        error
		shows      string
	}{
		{"a number picks", "2\n", false, false, "beta", nil, "Select 1-2 (Enter = cancel): "},
		{"Enter cancels", "\n", false, false, "", nil, ""},
		{"Enter steps back", "\n", true, true, "", nil, "Select 1-2 (Enter = back, q = quit): "},
		{"q quits", "q\n", true, false, "", ErrQuit, "(Enter = cancel, q = quit)"},
		{"Q quits", "Q\n", true, true, "", ErrQuit, ""},
		{"q cancels without quit", "q\n", false, true, "", nil, "(Enter = back)"},
		{"the end of the input quits", "", true, true, "", ErrQuit, ""},
		{"the end of the input cancels without quit", "", false, false, "", nil, ""},
		{"anything else asks again", "x\n0\n3\n-1\n1\n", false, false, "alpha", nil, "not a choice: x\n"},
		{"a console's line", " 2 \r\n", false, false, "beta", nil, ""},
		{"a last line without its newline", "1", false, false, "alpha", nil, ""},
	}
	for _, c := range cases {
		var out bytes.Buffer
		v, err := Menu(strings.NewReader(c.in), &out, rows, "switch context", c.quit, c.back)
		if v != c.value || !errors.Is(err, c.err) {
			t.Errorf("%s: %q, %v; want %q, %v", c.name, v, err, c.value, c.err)
		}
		if !strings.Contains(out.String(), c.shows) {
			t.Errorf("%s: shows\n%s\nwant it to hold %q", c.name, out.String(), c.shows)
		}
	}

	// The header, then the rows numbered and aligned as fzf shows them, colour
	// kept, the value hidden.
	var out bytes.Buffer
	if _, err := Menu(strings.NewReader("\n"), &out, rows, "switch context", false, false); err != nil {
		t.Fatal(err)
	}
	want := "switch context\n  1) alpha  Lab\n  2) " + Paint("red", "beta") + "   Production  *\nSelect 1-2 (Enter = cancel): "
	if out.String() != want {
		t.Fatalf("menu:\n%q\nwant\n%q", out.String(), want)
	}
	for _, typed := range []string{"0", "3", "x"} {
		var out bytes.Buffer
		if _, err := Menu(strings.NewReader(typed+"\n\n"), &out, rows, "", false, false); err != nil {
			t.Fatal(err)
		}
		if strings.Count(out.String(), "Select 1-2") != 2 || !strings.Contains(out.String(), "not a choice: "+typed+"\n") {
			t.Errorf("%s asks again:\n%s", typed, out.String())
		}
	}

	// Ten rows or more: the numbers are right-aligned.
	var many []Line
	for i := 0; i < 10; i++ {
		many = append(many, Line{Value: fmt.Sprint(i), Cols: []string{fmt.Sprint("row", i)}})
	}
	out.Reset()
	if v, err := Menu(strings.NewReader("10\n"), &out, many, "", false, false); v != "9" || err != nil {
		t.Fatalf("10th of ten: %q, %v", v, err)
	}
	if !strings.Contains(out.String(), "   1) row0\n") || !strings.Contains(out.String(), "  10) row9\n") {
		t.Fatalf("ten rows:\n%s", out.String())
	}

	// No rows: nothing to ask, and a cancel.
	out.Reset()
	in := strings.NewReader("1\n")
	if v, err := Menu(in, &out, nil, "namespace of x", true, false); v != "" || err != nil || in.Len() != 2 {
		t.Fatalf("no rows: %q, %v, %d unread", v, err, in.Len())
	}
	if out.String() != "namespace of x\n  (nothing to pick)\n" {
		t.Fatalf("no rows: %q", out.String())
	}
}

// Each step of a drill-down reads one line and leaves the rest of what was
// typed for the next.
func TestMenuReadsOneLineAtATime(t *testing.T) {
	rows := []Line{{Value: "a", Cols: []string{"a"}}, {Value: "b", Cols: []string{"b"}}}
	in := strings.NewReader("2\n1\n")
	var out bytes.Buffer
	first, _ := Menu(in, &out, rows, "", false, false)
	second, _ := Menu(in, &out, rows, "", false, false)
	if first != "b" || second != "a" {
		t.Fatalf("got %q then %q", first, second)
	}
}

func TestPickShowsTheMenu(t *testing.T) {
	rows := []Line{{Value: "a", Cols: []string{"a"}}, {Value: "b", Cols: []string{"b"}}}
	noFzf := func(string) (string, error) { return "", errors.New("not found") }
	hasFzf := func(string) (string, error) { return "/no/such/fzf", nil }
	for _, p := range []Picker{
		{LookPath: noFzf},               // fzf is not on PATH
		{Plain: true, LookPath: hasFzf}, // picker = "plain": fzf is never run
	} {
		var out bytes.Buffer
		p.Term = func() (io.ReadWriteCloser, error) { return script{strings.NewReader("2\n"), &out}, nil }
		if !p.Available() {
			t.Fatal("a terminal, so a picker")
		}
		v, err := p.PickLines(rows, "context> ", "switch context", false)
		if v != "b" || err != nil || !strings.Contains(out.String(), "  2) b\n") {
			t.Fatalf("plain=%v: %q, %v\n%s", p.Plain, v, err, out.String())
		}
	}
	// A terminal that does not open: no picker.
	p := Picker{LookPath: noFzf, Term: func() (io.ReadWriteCloser, error) { return nil, errors.New("no console") }}
	if _, err := p.PickLines(rows, "> ", "", false); err != ErrUnavailable {
		t.Fatalf("no console: %v", err)
	}
}

// fzf's header ends with its keys, as before the menu.
func TestKeys(t *testing.T) {
	for _, c := range []struct {
		menu, quit, back bool
		want             string
	}{
		{false, false, false, "ESC cancels"},
		{false, false, true, "ESC back"},
		{false, true, false, "ESC cancels  q quits"},
		{false, true, true, "ESC back  q quits"},
		{true, false, false, "Enter = cancel"},
		{true, true, true, "Enter = back, q = quit"},
	} {
		if got := keys(c.menu, c.quit, c.back); got != c.want {
			t.Errorf("keys(%v, %v, %v) = %q, want %q", c.menu, c.quit, c.back, got, c.want)
		}
	}
	if join("on kind", "ESC back") != "on kind  |  ESC back" || join("", "ESC cancels") != "ESC cancels" {
		t.Fatal("join")
	}
}
