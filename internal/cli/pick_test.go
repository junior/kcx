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
[cluster.prod1]
group = "production"
site  = "austin"
aliases = ["main"]
[cluster.prod2]
group = "production"
site  = "austin"
[cluster.prod3]
group = "production"
site  = "atlanta"
[cluster.office2-lab]
group = "lab"
[cluster.office7-lab]
group = "lab"
`
	if err := os.WriteFile(filepath.Join(dir, "acme.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := profile.Load([]string{filepath.Join(dir, "acme.toml")})
	if err != nil {
		t.Fatal(err)
	}
	fetched := func(n string) bool { return n == "prod1" || n == "office7-lab" }
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
	want = []string{"prod1\tfetched  (main)", "prod2\t"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("clusterLines austin: %q", got)
	}
	// Without a site filter the site shows in the description.
	got = clusterLines(set, "production", "", fetched)
	want = []string{"prod1\taustin  fetched  (main)", "prod2\taustin", "prod3\tatlanta"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("clusterLines all: %q", got)
	}
	got = clusterLines(set, "lab", "", fetched)
	want = []string{"office2-lab\t", "office7-lab\tfetched"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("clusterLines lab: %q", got)
	}
}
