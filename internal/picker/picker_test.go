package picker

import (
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
	if _, err := p.PickLines([]Line{{Value: "a", Cols: []string{"a"}}}, "> ", ""); err != ErrUnavailable {
		t.Fatalf("PickLines without a terminal: %v", err)
	}
}
