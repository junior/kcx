package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/junior/kcx/v2/internal/picker"
	"github.com/junior/kcx/v2/internal/profile"
)

// acmeProfile has two groups of clusters, one spread over two sites, and an
// empty group.
const acmeProfile = `
[provider.acme]
command = ["acme", "{host}", "{out}"]
[group.production]
label    = "Production"
provider = "acme"
color    = "red"
readonly = true
[group.lab]
provider = "acme"
[group.empty]
provider = "acme"
[cluster.prod1]
group = "production"
site  = "Austin"
aliases = ["main"]
[cluster.prod2]
group = "production"
site  = "Austin"
[cluster.prod3]
group = "production"
site  = "Atlanta"
[cluster.office2-lab]
group = "lab"
[cluster.office7-lab]
group = "lab"
`

// acmeSet writes acmeProfile into dir and loads it.
func acmeSet(t *testing.T, dir string) *profile.Set {
	t.Helper()
	path := filepath.Join(dir, "acme.toml")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(acmeProfile), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := profile.Load([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// The drill-down's rows are pure functions over the profile; fzf itself is only
// exercised by hand.
func TestBrowseRows(t *testing.T) {
	set := acmeSet(t, t.TempDir())
	fetched := func(n string) bool { return n == "prod1" || n == "office7-lab" }
	plain := func(_, s string) string { return s }
	join := func(rows []picker.Line) string {
		var parts []string
		for _, r := range rows {
			parts = append(parts, r.Value+"="+strings.Join(r.Cols, "|"))
		}
		return strings.Join(parts, "; ")
	}

	// Groups come in profile order, labelled, counted, flagged; an empty one is left out.
	if got := join(groupRows(set, fetched, plain)); got != "production=Production|3 clusters|1 fetched|read-only; lab=lab|2 clusters|1 fetched" {
		t.Fatalf("groupRows: %s", got)
	}
	// The colour paints what is shown, never the value that comes back.
	if row := groupRows(set, fetched, picker.Paint)[0]; row.Value != "production" || row.Cols[0] != picker.Paint("red", "Production") {
		t.Fatalf("groupRows painted: %+v", row)
	}
	// Sites in the order they first appear; no site counts under "-".
	if s := sitesOf(set, "production"); strings.Join(s, ",") != "Austin,Atlanta" {
		t.Fatalf("sitesOf production: %v", s)
	}
	if s := sitesOf(set, "lab"); strings.Join(s, ",") != "-" {
		t.Fatalf("sitesOf lab: %v", s)
	}
	if got := join(siteRows(set, "production", fetched)); got != "Austin=Austin|2 clusters|1 fetched; Atlanta=Atlanta|1 cluster|0 fetched" {
		t.Fatalf("siteRows: %s", got)
	}
	// Clusters of one site: the marks for fetched and current, then the aliases.
	if got := join(clusterRows(set, "production", "Austin", fetched, "prod1", plain)); got != "prod1=prod1|\u2713 *|(main); prod2=prod2||" {
		t.Fatalf("clusterRows Austin: %s", got)
	}
	if got := join(clusterRows(set, "production", "", fetched, "", plain)); got != "prod1=prod1|\u2713|(main); prod2=prod2||; prod3=prod3||" {
		t.Fatalf("clusterRows production: %s", got)
	}
	if got := join(clusterRows(set, "lab", "", fetched, "prod1", plain)); got != "office2-lab=office2-lab||; office7-lab=office7-lab|\u2713|" {
		t.Fatalf("clusterRows lab: %s", got)
	}
	if total, done := fetchedCount(set, fetched); total != 5 || done != 2 {
		t.Fatalf("fetchedCount: %d %d", total, done)
	}
	if counts(1, 0) != "1 cluster, 0 fetched" || counts(72, 3) != "72 clusters, 3 fetched" {
		t.Fatal("counts")
	}
}

// typed is a terminal that types in and records what the menu shows.
type typed struct {
	io.Reader
	out *bytes.Buffer
}

func (t typed) Write(p []byte) (int, error) { return t.out.Write(p) }
func (t typed) Close() error                { return nil }

// The browse in the numbered menu: Enter steps back, q quits from any step, the
// end of the input quits, and the chosen cluster comes back.
func TestBrowseMenu(t *testing.T) {
	e, l := fixture(t)
	set := acmeSet(t, t.TempDir())
	writeKubeconfig(t, l.PermFile("prod1"), "prod1", "prod1", "prod1", "https://prod1.example.test:6443", "")
	a := &App{getenv: env{"HOME": e["HOME"], "NO_COLOR": "1"}.get, lay: l}
	for _, c := range []struct {
		name, in, want string
		shows          []string
	}{
		{"a site, back, another site, a cluster", "1\n2\n\n1\n2\n", "prod2", []string{
			"5 clusters, 1 fetched\n  1) Production  3 clusters  1 fetched  read-only\n  2) lab         2 clusters  0 fetched\nSelect 1-2 (Enter = cancel, q = quit): ",
			"Production\n  1) Austin   2 clusters  1 fetched\n  2) Atlanta  1 cluster   0 fetched\nSelect 1-2 (Enter = back, q = quit): ",
			"Production > Atlanta  |  \u2713 fetched  * current\n  1) prod3\n",
			"Production > Austin  |  \u2713 fetched  * current\n  1) prod1  \u2713  (main)\n  2) prod2\n",
		}},
		{"one site: straight to the clusters, and back to the groups", "2\n\nq\n", "", []string{
			"lab  |  \u2713 fetched  * current\n  1) office2-lab\n  2) office7-lab\nSelect 1-2 (Enter = back, q = quit): ",
		}},
		{"q at a later step", "1\nq\n", "", nil},
		{"Enter at the first step", "\n", "", nil},
		{"the end of the input", "1\n1\n", "", nil},
		{"asks again", "1\n1\nx\n1\n", "prod1", []string{"not a choice: x\nSelect 1-2 (Enter = back, q = quit): "}},
	} {
		var out bytes.Buffer
		term := typed{strings.NewReader(c.in), &out} // one terminal for every step
		p := picker.Picker{Plain: true, Term: func() (io.ReadWriteCloser, error) { return term, nil }}
		got, err := a.browse(p, set)
		if got != c.want || err != nil {
			t.Errorf("%s: %q, %v; want %q", c.name, got, err, c.want)
		}
		for _, w := range c.shows {
			if !strings.Contains(out.String(), w) {
				t.Errorf("%s: shows\n%s\nwant it to hold\n%s", c.name, out.String(), w)
			}
		}
	}
}

// Without fzf, kcx alone is the numbered menu: its top line browses the
// profiles, and a pick switches.
func TestPickMenu(t *testing.T) {
	e, l := fixture(t)
	oldLook := lookPath
	lookPath = func(string) (string, error) { return "", os.ErrNotExist } // no kind, no temp line
	t.Cleanup(func() { lookPath = oldLook })
	if err := os.MkdirAll(filepath.Dir(l.Config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(l.Config, []byte("[settings]\npicker = \"plain\"\nprobe = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pick := func(in string) string {
		t.Helper()
		var out, errb, shown bytes.Buffer
		term := typed{strings.NewReader(in), &shown}
		a := &App{stdin: strings.NewReader(""), stdout: &out, stderr: &errb, getenv: e.get, lay: l,
			term: func() (io.ReadWriteCloser, error) { return term, nil }}
		if err := a.run(nil); err != nil {
			t.Fatalf("%q: %v\n%s", in, err, errb.String())
		}
		cur, err := a.store().Current()
		if err != nil {
			t.Fatal(err)
		}
		return cur + "\n" + shown.String()
	}
	if got := pick("2\n"); !strings.HasPrefix(got, "beta\n") || !strings.Contains(got, "  1) alpha  (configs)\n  2) beta   (configs)\nSelect 1-2 (Enter = cancel): ") {
		t.Fatalf("pick beta:\n%s", got)
	}
	acmeSet(t, l.Profiles)
	writeKubeconfig(t, l.PermFile("office7-lab"), "office7-lab", "office7-lab", "office7-lab", "https://office7-lab.example.test:6443", "")
	got := pick("1\n2\n2\n")
	if !strings.HasPrefix(got, "office7-lab\n") || !strings.Contains(got, "  1) + fetch / browse the profile clusters  5 clusters, 1 fetched\n") {
		t.Fatalf("browse to office7-lab:\n%s", got)
	}
	if got := pick("\n"); !strings.HasPrefix(got, "office7-lab\n") {
		t.Fatalf("Enter cancels, nothing switched:\n%s", got)
	}
}
