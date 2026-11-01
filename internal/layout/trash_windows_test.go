//go:build windows

package layout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// Only a fixed drive with its Recycle Bin on takes a file; anything else is a
// reason to refuse.
func TestBinFactsWhy(t *testing.T) {
	for _, c := range []struct {
		f    binFacts
		want string
	}{
		{binFacts{driveType: windows.DRIVE_FIXED}, ""},
		{binFacts{driveType: windows.DRIVE_REMOTE}, "it is on a network drive"},
		{binFacts{driveType: windows.DRIVE_REMOVABLE}, "it is on a removable drive"},
		{binFacts{driveType: windows.DRIVE_CDROM}, "it is on an optical drive"},
		{binFacts{driveType: windows.DRIVE_RAMDISK}, "it is on a RAM disk"},
		{binFacts{driveType: windows.DRIVE_UNKNOWN}, "kcx cannot tell what kind of drive it is on"},
		{binFacts{driveType: windows.DRIVE_NO_ROOT_DIR}, "kcx cannot tell what kind of drive it is on"},
		{binFacts{driveType: windows.DRIVE_FIXED, subst: true}, "it is on a subst drive"},
		{binFacts{driveType: windows.DRIVE_FIXED, off: true}, "the Recycle Bin of its drive is turned off"},
		{binFacts{driveType: windows.DRIVE_FIXED, policy: true}, "a policy turns the Recycle Bin off"},
	} {
		if got := c.f.why(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.f, got, c.want)
		}
	}
}

// A file on this machine's temporary drive goes to the Recycle Bin (on CI's
// runners, a fixed drive with the bin on); a file on a share refuses, and
// nothing moves.
func TestTrashRefusesWhereNoRecycleBin(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.yaml")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := binFactsOf(file)
	if err != nil {
		t.Fatal(err)
	}
	if why := f.why(); why != "" {
		if os.Getenv("CI") == "true" {
			t.Fatalf("the runner's temporary drive (%+v): %s", f, why)
		}
		t.Skipf("this machine's temporary drive has no Recycle Bin: %s", why)
	}
	share := `\\localhost\C$\kcx-no-such-dir\b.yaml`
	err = Trash(func(string) string { return "" }, file, share)
	if err == nil || !strings.Contains(err.Error(), share+" cannot go to the Recycle Bin") || !strings.Contains(err.Error(), "move or delete it yourself") {
		t.Fatalf("a share: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("a refusal moved the other file too: %v", err)
	}
	if err := Trash(func(string) string { return "" }, file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("still there after the Recycle Bin: %v", err)
	}
}
