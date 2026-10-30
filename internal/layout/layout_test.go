package layout

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func envOf(m map[string]string) Getenv {
	return func(k string) string { return m[k] }
}

func testLayout(t *testing.T) (Layout, string) {
	t.Helper()
	home := t.TempDir()
	l, err := FromEnv(envOf(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatal(err)
	}
	return l, home
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFromEnv(t *testing.T) {
	if _, err := FromEnv(envOf(nil)); err == nil {
		t.Fatal("no HOME: expected an error")
	}
	l, home := testLayout(t)
	if l.State != filepath.Join(home, ".kube", "config") || l.Perm != filepath.Join(home, ".kube", "configs") ||
		l.Temp != filepath.Join(home, ".kube", "temp") || l.Prev != filepath.Join(home, ".kube", "kubectx") ||
		l.NSPrev != filepath.Join(home, ".kube", "kubens") {
		t.Fatalf("unexpected paths: %+v", l)
	}
	if l.Config != filepath.Join(home, ".config", "kcx", "config.toml") || l.Profiles != filepath.Join(home, ".config", "kcx", "profiles") {
		t.Fatalf("unexpected config paths: %+v", l)
	}
	xdg, err := FromEnv(envOf(map[string]string{"HOME": home, "XDG_CONFIG_HOME": "/x/conf"}))
	if err != nil {
		t.Fatal(err)
	}
	if xdg.Profiles != filepath.Join("/x/conf", "kcx", "profiles") || xdg.Presets != filepath.Join("/x/conf", "kind") {
		t.Fatalf("XDG_CONFIG_HOME not honoured: %+v", xdg)
	}
}

func TestFilesOrderAndFilter(t *testing.T) {
	l, _ := testLayout(t)
	// The state file is listed first even before it exists.
	if got := l.Files(); len(got) != 1 || got[0] != l.State {
		t.Fatalf("empty layout: got %v", got)
	}
	touch(t, filepath.Join(l.Perm, "beta.yaml"))
	touch(t, filepath.Join(l.Perm, "Zeta.yaml")) // bytewise: uppercase sorts before lowercase
	touch(t, filepath.Join(l.Perm, "alpha.yaml"))
	touch(t, filepath.Join(l.Perm, ".kcx.123"))         // a write in progress: ignored
	touch(t, filepath.Join(l.Perm, "notes.txt"))        // not a kubeconfig: ignored
	touch(t, filepath.Join(l.Perm, "sub", "deep.yaml")) // one level only
	touch(t, filepath.Join(l.Temp, "t1@imac.yaml"))
	touch(t, filepath.Join(l.Temp, "a@desktop-linux.yaml"))
	want := []string{
		l.State,
		filepath.Join(l.Perm, "Zeta.yaml"),
		filepath.Join(l.Perm, "alpha.yaml"),
		filepath.Join(l.Perm, "beta.yaml"),
		filepath.Join(l.Temp, "a@desktop-linux.yaml"),
		filepath.Join(l.Temp, "t1@imac.yaml"),
	}
	got := l.Files()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Files():\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if k := l.Kubeconfig(); k != strings.Join(want, string(os.PathListSeparator)) {
		t.Fatalf("Kubeconfig(): %s", k)
	}
}

func TestEnsure(t *testing.T) {
	l, _ := testLayout(t)
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{l.Dir, l.Perm, l.Temp} {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode %o, want 700", d, info.Mode().Perm())
		}
	}
	info, err := os.Stat(l.State)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode %o, want 600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(l.State)
	if string(data) != EmptyKubeconfig {
		t.Fatalf("state file: %q", data)
	}
	// A second Ensure leaves an existing state file alone and tightens a loose directory.
	if err := os.WriteFile(l.State, []byte("apiVersion: v1\nkind: Config\ncurrent-context: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(l.Perm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(l.State)
	if !strings.Contains(string(data), "current-context: x") {
		t.Fatal("Ensure overwrote the state file")
	}
	info, _ = os.Stat(l.Perm)
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("configs mode %o still open to others", info.Mode().Perm())
	}
}

func TestWhereAndSplitTemp(t *testing.T) {
	l, _ := testLayout(t)
	cases := map[string]string{
		l.State:                               "~/.kube/config (written by another tool)",
		filepath.Join(l.Perm, "prod.yaml"):    "configs",
		filepath.Join(l.Temp, "t1@imac.yaml"): "temp, on imac",
		filepath.Join(l.Temp, "k3d-x.yaml"):   "temp",
		"/elsewhere/config":                   "/elsewhere/config",
	}
	for file, want := range cases {
		if got := l.Where(file); got != want {
			t.Errorf("Where(%s) = %q, want %q", file, got, want)
		}
	}
	if n, e := SplitTemp(filepath.Join(l.Temp, "t1@imac.yaml")); n != "t1" || e != "imac" {
		t.Errorf("SplitTemp: %q %q", n, e)
	}
	if n, e := SplitTemp(filepath.Join(l.Temp, "k3d-x.yaml")); n != "k3d-x" || e != "" {
		t.Errorf("SplitTemp without engine: %q %q", n, e)
	}
	if l.PermFile("prod1") != filepath.Join(l.Perm, "prod1.yaml") || l.TempFile("t1", "imac") != filepath.Join(l.Temp, "t1@imac.yaml") || l.TempFile("x", "") != filepath.Join(l.Temp, "x.yaml") {
		t.Error("PermFile/TempFile paths")
	}
}

func TestValidNames(t *testing.T) {
	good := []string{"prod1", "office2-lab", "eks.lab_2", "A", "0", strings.Repeat("a", 63)}
	bad := []string{"", "-x", ".x", "_x", "a/b", "a@b", "a b", "a:b", strings.Repeat("a", 64), "caf\u00e9"}
	for _, n := range good {
		if !ValidName(n) {
			t.Errorf("ValidName(%q) = false", n)
		}
	}
	for _, n := range bad {
		if ValidName(n) {
			t.Errorf("ValidName(%q) = true", n)
		}
	}
	for _, n := range []string{"t1", "lab.2", "a-b"} {
		if !ValidKindName(n) {
			t.Errorf("ValidKindName(%q) = false", n)
		}
	}
	for _, n := range []string{"", "T1", "a_b", "-a", ".a", strings.Repeat("a", 41)} {
		if ValidKindName(n) {
			t.Errorf("ValidKindName(%q) = true", n)
		}
	}
}

func TestWriteFileAtomicAndTrash(t *testing.T) {
	l, _ := testLayout(t)
	if err := l.Ensure(); err != nil {
		t.Fatal(err)
	}
	p := l.PermFile("x")
	if err := WriteFileAtomic(p, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	if left := DirFiles(l.Perm); len(left) != 1 {
		t.Fatalf("temp dotfile left behind or file missing: %v", left)
	}
	trash := filepath.Join(t.TempDir(), "trash")
	env := envOf(map[string]string{"KCX_TRASH_DIR": trash})
	if err := Trash(env, p); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("file still in place after Trash")
	}
	entries, _ := os.ReadDir(trash)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "x.yaml.") {
		t.Fatalf("trash holds %v", entries)
	}
	if err := Trash(env); err != nil {
		t.Fatal("Trash of nothing should succeed")
	}
}
