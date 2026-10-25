package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/junior/kcx/internal/picker"
	"github.com/junior/kcx/internal/profile"
)

// The drill-down's rows are pure functions over the profile; fzf itself is only
// exercised by hand.
func TestBrowseRows(t *testing.T) {
	dir := t.TempDir()
	content := `
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
	if err := os.WriteFile(filepath.Join(dir, "acme.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := profile.Load([]string{filepath.Join(dir, "acme.toml")})
	if err != nil {
		t.Fatal(err)
	}
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
