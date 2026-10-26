package cli

import (
	"strings"
	"testing"
)

func TestColourUsage(t *testing.T) {
	sample := "kcx - the tool.\n\nHeading\n  kcx go [-x]                 does a thing\n                              and more\n      --flag                  an option\n  a plain line\n"
	got := colourUsage(sample)
	want := sgrBold + "kcx" + sgrReset + " - the tool.\n\n" +
		sgrBold + "Heading" + sgrReset + "\n" +
		sgrCyan + "  kcx go [-x]" + sgrReset + "                 does a thing\n" +
		"                              and more\n" +
		sgrYellow + "      --flag" + sgrReset + "                  an option\n" +
		"  a plain line\n"
	if got != want {
		t.Fatalf("colourUsage:\n%q\nwant\n%q", got, want)
	}
	// Every command line of the real usage splits into a command and a description.
	for _, l := range strings.Split(usageText, "\n") {
		if strings.HasPrefix(l, "  kcx ") {
			if head, _ := splitColumns(l); strings.Contains(strings.TrimSpace(head), "  ") {
				t.Errorf("command keeps a double space: %q", l)
			}
		}
	}
	// Without a terminal, kcx -h is the plain text.
	code, out, _ := runKcx(t, env{"HOME": t.TempDir()}, "-h")
	if code != 0 || out != usageText || strings.Contains(out, "\x1b[") {
		t.Fatalf("plain help: %d", code)
	}
}
