package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/junior/kcx/internal/profile"
)

// The drill-down's lists are pure functions over the profile; fzf itself is only
// exercised by hand.
func TestBrowseLines(t *testing.T) {
	dir := t.TempDir()
	content := `
[provider.acme]
command = ["acme", "{host}", "{out}"]
[group.production]
provider = "acme"
readonly = true
[group.lab]
provider = "acme"
[cluster.p1]
group = "production"
site  = "austin"
aliases = ["prod1"]
[cluster.p2]
group = "production"
site  = "austin"
[cluster.a1]
group = "production"
site  = "atlanta"
[cluster.r4-lab]
group = "lab"
[cluster.r7-lab]
group = "lab"
`
	if err := os.WriteFile(filepath.Join(dir, "acme.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := profile.Load([]string{filepath.Join(dir, "acme.toml")})
	if err != nil {
		t.Fatal(err)
	}
	fetched := func(n string) bool { return n == "p1" || n == "r7-lab" }
	got := groupLines(set, fetched)
	want := []string{"lab\t2 clusters, 1 fetched", "production\t3 clusters, 1 fetched, read-only"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("groupLines: %q", got)
	}
	if s := sitesOf(set, "production"); strings.Join(s, ",") != "atlanta,austin" {
		t.Fatalf("sitesOf production: %v", s)
	}
	if s := sitesOf(set, "lab"); strings.Join(s, ",") != "-" {
		t.Fatalf("sitesOf lab (no sites): %v", s)
	}
	got = siteLines(set, "production", fetched)
	want = []string{"atlanta\t1 clusters, 0 fetched", "austin\t2 clusters, 1 fetched"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("siteLines: %q", got)
	}
	got = clusterLines(set, "production", "austin", fetched)
	want = []string{"p1\tfetched  (prod1)", "p2\t"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("clusterLines austin: %q", got)
	}
	// Without a site filter the site shows in the description.
	got = clusterLines(set, "production", "", fetched)
	want = []string{"a1\tatlanta", "p1\taustin  fetched  (prod1)", "p2\taustin"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("clusterLines all: %q", got)
	}
	got = clusterLines(set, "lab", "", fetched)
	want = []string{"r4-lab\t", "r7-lab\tfetched"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("clusterLines lab: %q", got)
	}
}
