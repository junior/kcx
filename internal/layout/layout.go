// Package layout knows where kcx keeps things and what KUBECONFIG must say.
//
// Under ~/.kube (directories 0700, files 0600):
//
//	config                 the state file: the current context, and nothing of kcx's
//	                       besides. kubectl writes current-context into the FIRST
//	                       existing file in KUBECONFIG, so this one comes first and a
//	                       switch never rewrites a file that holds credentials.
//	configs/NAME.yaml      one permanent cluster.
//	temp/NAME@ENGINE.yaml  one temp kind cluster; ENGINE is the docker context it
//	                       runs on.
//	kubectx                the previous context, the file kubectx keeps too.
//	kubens/CONTEXT         the previous namespace of CONTEXT, the file kubens keeps.
//
// KUBECONFIG = config:configs/*.yaml:temp/*.yaml, each directory sorted. The shell
// integration (`kcx init`) carries the same rule, so a shell computes it without
// running kcx.
package layout

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Layout is the set of paths kcx uses on one machine.
type Layout struct {
	Home     string // the home directory
	Dir      string // ~/.kube
	State    string // ~/.kube/config, the state file
	Perm     string // ~/.kube/configs, permanent clusters
	Temp     string // ~/.kube/temp, temp kind clusters
	Prev     string // ~/.kube/kubectx, the previous context
	NSPrev   string // ~/.kube/kubens, a directory: the previous namespace per context
	Config   string // ~/.config/kcx/config.toml, machine settings
	Profiles string // ~/.config/kcx/profiles, the cluster profiles
	Presets  string // ~/.config/kind, kind cluster presets
}

// Getenv is the shape of os.Getenv, passed in so tests can supply an environment.
type Getenv func(string) string

// FromEnv builds the layout from HOME and XDG_CONFIG_HOME.
func FromEnv(getenv Getenv) (Layout, error) {
	home := getenv("HOME")
	if home == "" {
		return Layout{}, errors.New("HOME is not set")
	}
	conf := getenv("XDG_CONFIG_HOME")
	if conf == "" {
		conf = filepath.Join(home, ".config")
	}
	kube := filepath.Join(home, ".kube")
	return Layout{
		Home:     home,
		Dir:      kube,
		State:    filepath.Join(kube, "config"),
		Perm:     filepath.Join(kube, "configs"),
		Temp:     filepath.Join(kube, "temp"),
		Prev:     filepath.Join(kube, "kubectx"),
		NSPrev:   filepath.Join(kube, "kubens"),
		Config:   filepath.Join(conf, "kcx", "config.toml"),
		Profiles: filepath.Join(conf, "kcx", "profiles"),
		Presets:  filepath.Join(conf, "kind"),
	}, nil
}

// Files lists what KUBECONFIG must name, in order: the state file first, even
// before it exists, then the permanent and the temp clusters, each directory
// sorted bytewise.
func (l Layout) Files() []string {
	out := []string{l.State}
	out = append(out, DirFiles(l.Perm)...)
	out = append(out, DirFiles(l.Temp)...)
	return out
}

// DirFiles returns the regular *.yaml files directly under dir, sorted bytewise,
// without dotfiles (kcx writes through dotfiles and moves them into place). A
// missing directory yields nothing.
func DirFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".yaml") {
			continue
		}
		p := filepath.Join(dir, name)
		info, err := os.Stat(p) // follows a symlink, as `find -H` did
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Kubeconfig is the KUBECONFIG value for this layout.
func (l Layout) Kubeconfig() string {
	return strings.Join(l.Files(), string(os.PathListSeparator))
}

// Ensure creates the directories (0700) and an empty state file (0600) when
// they are missing, and takes group and other permissions off the directories.
func (l Layout) Ensure() error {
	for _, d := range []string{l.Dir, l.Perm, l.Temp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("cannot create %s: %w", d, err)
		}
		if info, err := os.Stat(d); err == nil && info.Mode().Perm()&0o077 != 0 {
			_ = os.Chmod(d, info.Mode().Perm()&^0o077)
		}
	}
	if _, err := os.Stat(l.State); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("cannot read %s: %w", l.State, err)
		}
		if err := WriteFileAtomic(l.State, []byte(EmptyKubeconfig), 0o600); err != nil {
			return fmt.Errorf("cannot create %s: %w", l.State, err)
		}
	}
	return nil
}

// EmptyKubeconfig is what a fresh state file holds.
const EmptyKubeconfig = "apiVersion: v1\nkind: Config\npreferences: {}\n"

// PermFile is the file of permanent cluster name.
func (l Layout) PermFile(name string) string {
	return filepath.Join(l.Perm, name+".yaml")
}

// TempFile is the file of temp cluster name on engine; an empty engine gives
// NAME.yaml, a cluster another tool made.
func (l Layout) TempFile(name, engine string) string {
	if engine == "" {
		return filepath.Join(l.Temp, name+".yaml")
	}
	return filepath.Join(l.Temp, name+"@"+engine+".yaml")
}

// Where says, for `kcx ls`, the place a file lives: the state file (written by
// another tool), configs, temp on an engine, or the path itself.
func (l Layout) Where(file string) string {
	switch {
	case file == l.State:
		return "~/.kube/config (written by another tool)"
	case strings.HasPrefix(file, l.Perm+string(os.PathSeparator)):
		return "configs"
	case strings.HasPrefix(file, l.Temp+string(os.PathSeparator)):
		base := strings.TrimSuffix(filepath.Base(file), ".yaml")
		if i := strings.IndexByte(base, '@'); i >= 0 {
			return "temp, on " + base[i+1:]
		}
		return "temp"
	}
	return file
}

// SplitTemp returns the cluster name and the engine of a temp file's base name
// (NAME@ENGINE.yaml or NAME.yaml).
func SplitTemp(file string) (name, engine string) {
	base := strings.TrimSuffix(filepath.Base(file), ".yaml")
	if i := strings.IndexByte(base, '@'); i >= 0 {
		return base[:i], base[i+1:]
	}
	return base, ""
}

// ValidName says whether s can be a cluster name: letters, digits, . _ and -,
// starting with a letter or a digit, up to 63 characters. No @, which separates a
// temp file's engine; no /, which would leave the directory.
func ValidName(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// ValidKindName says whether s is a name kind accepts: lowercase letters,
// digits, . and -, starting with a letter or a digit, up to 40 characters.
func ValidKindName(s string) bool {
	if s == "" || len(s) > 40 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '.' || r == '-':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// WriteFileAtomic writes data to a dotfile beside path, with mode, and renames it
// into place, so a reader never sees a half-written file and KUBECONFIG never
// lists one (the layout ignores dotfiles).
func WriteFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".kcx.*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := func() { _ = os.Remove(tmp) }
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// Trash moves files to the Trash, never deletes them: macOS's /usr/bin/trash,
// else gio trash, else a refusal that names the files. KCX_TRASH_DIR (tests) is a
// directory that stands in for the Trash.
func Trash(getenv Getenv, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	if dir := getenv("KCX_TRASH_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		stamp := time.Now().Format("20060102150405")
		for _, p := range paths {
			dest := filepath.Join(dir, fmt.Sprintf("%s.%s.%d", filepath.Base(p), stamp, os.Getpid()))
			if err := os.Rename(p, dest); err != nil {
				return err
			}
		}
		return nil
	}
	var cmd *exec.Cmd
	switch {
	case isExecutable("/usr/bin/trash"):
		cmd = exec.Command("/usr/bin/trash", paths...)
	case lookPath("gio"):
		cmd = exec.Command("gio", append([]string{"trash", "--"}, paths...)...)
	default:
		return fmt.Errorf("no Trash on this machine; move it yourself: %s", strings.Join(paths, " "))
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", cmd.Path, err, strings.TrimSpace(string(out)))
	}
	for _, p := range paths {
		if _, err := os.Lstat(p); err == nil {
			return fmt.Errorf("%s is still there after the move to the Trash", p)
		}
	}
	return nil
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func lookPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
