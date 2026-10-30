package layout

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHomeDir(t *testing.T) {
	env := func(m map[string]string) Getenv { return func(k string) string { return m[k] } }
	if got := HomeDir("linux", env(map[string]string{"HOME": "/home/jane", "USERPROFILE": "/x"})); got != "/home/jane" {
		t.Fatalf("unix: %q", got)
	}
	if got := HomeDir("darwin", env(nil)); got != "" {
		t.Fatalf("unix without HOME: %q", got)
	}

	// Windows, client-go's order: the first holding .kube/config wins, HOMEDRIVE
	// and HOMEPATH before USERPROFILE.
	root := t.TempDir()
	profile := filepath.Join(root, "profile")
	drive, path := root, string(filepath.Separator)+"drivehome"
	drivePath := drive + path
	home := filepath.Join(root, "home")
	for _, d := range []string{profile, drivePath, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	vars := map[string]string{"USERPROFILE": profile, "HOMEDRIVE": drive, "HOMEPATH": path}
	// No .kube/config anywhere, no HOME: USERPROFILE, the first writable one.
	if got := HomeDir("windows", env(vars)); got != profile {
		t.Fatalf("windows, nothing there yet: %q, want USERPROFILE %q", got, profile)
	}
	// A .kube/config under HOMEDRIVE+HOMEPATH: that one.
	if err := os.MkdirAll(filepath.Join(drivePath, ".kube"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(drivePath, ".kube", "config"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := HomeDir("windows", env(vars)); got != drivePath {
		t.Fatalf("windows, config under HOMEDRIVE+HOMEPATH: %q", got)
	}
	// HOME set and holding one too: HOME comes first.
	if err := os.MkdirAll(filepath.Join(home, ".kube"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".kube", "config"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	vars["HOME"] = home
	if got := HomeDir("windows", env(vars)); got != home {
		t.Fatalf("windows, HOME with a config: %q", got)
	}
	// Nothing exists: the first that is set.
	missing := map[string]string{"USERPROFILE": filepath.Join(root, "nope1"), "HOMEDRIVE": "Q:", "HOMEPATH": `\nope2`}
	if got := HomeDir("windows", env(missing)); got != missing["USERPROFILE"] {
		t.Fatalf("windows, nothing exists: %q", got)
	}
	if got := HomeDir("windows", env(nil)); got != "" {
		t.Fatalf("windows, nothing set: %q", got)
	}
}
