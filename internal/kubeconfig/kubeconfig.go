// Package kubeconfig reads and writes kubeconfig files with the rules kubectl
// uses, through client-go's clientcmd: the merge of a KUBECONFIG list, the file a
// change belongs to, and self-contained imports.
//
// kcx writes three kinds of file and no other: the state file (the current
// context, and nothing else of kcx's), a managed file per cluster (import,
// rename, a namespace set on its context), and the small state files it shares
// with kubectx and kubens (the previous context, the previous namespace).
package kubeconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/junior/kcx/v2/internal/layout"
)

// Store is the list of files kcx reads, in KUBECONFIG order. Inside a kcx shell
// or a run, Shadow names the private file that comes first: it holds copies of
// contexts and the shell's current context, so listings skip it, while the
// merged view (Load, Current) reads it first.
type Store struct {
	Files  []string
	Shadow string
}

// Load merges the files the way kubectl does: the first file to set a key wins,
// missing files are skipped, a malformed file is an error.
func (s Store) Load() (*clientcmdapi.Config, error) {
	rules := &clientcmd.ClientConfigLoadingRules{Precedence: s.Files}
	cfg, err := rules.Load()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// Contexts lists the merged context names, sorted.
func (s Store) Contexts() ([]string, error) {
	cfg, err := s.Load()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// Current is the merged current context, "" when none is set.
func (s Store) Current() (string, error) {
	cfg, err := s.Load()
	if err != nil {
		return "", err
	}
	return cfg.CurrentContext, nil
}

// LoadFile reads one kubeconfig file. A missing file reads as an empty config.
func LoadFile(path string) (*clientcmdapi.Config, error) {
	cfg, err := clientcmd.LoadFromFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return clientcmdapi.NewConfig(), nil
		}
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// WriteFile writes cfg to path through a dotfile beside it, mode 0600.
func WriteFile(path string, cfg *clientcmdapi.Config) error {
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return err
	}
	return layout.WriteFileAtomic(path, data, 0o600)
}

// ContextsIn lists the context names one file defines, sorted.
func ContextsIn(path string) ([]string, error) {
	cfg, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cfg.Contexts))
	for name := range cfg.Contexts {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// Entry is one named thing in a kubeconfig: a cluster, a user or a context.
type Entry struct {
	Kind string // "cluster", "user" or "context"
	Name string
}

// Risks names what a config does that its owner should know before trusting
// it, in a stable order: a cluster reached over plain http or without TLS
// verification, where whoever sits between can read the credentials sent; and
// a user that logs in by running a program, which kubectl runs whenever the
// context is used. It names the program, never its arguments or environment,
// which may hold a secret.
func Risks(cfg *clientcmdapi.Config) []string {
	var out []string
	for _, name := range sortedNames(cfg.Clusters) {
		c := cfg.Clusters[name]
		switch {
		case strings.HasPrefix(strings.ToLower(c.Server), "http://"):
			out = append(out, fmt.Sprintf("cluster %s is reached over plain http: what kubectl sends, credentials included, travels unencrypted", name))
		case c.InsecureSkipTLSVerify:
			out = append(out, fmt.Sprintf("cluster %s skips TLS verification: whoever sits between you and it can read what kubectl sends, credentials included", name))
		}
	}
	for _, name := range sortedNames(cfg.AuthInfos) {
		if u := cfg.AuthInfos[name]; u.Exec != nil && u.Exec.Command != "" {
			out = append(out, fmt.Sprintf("user %s logs in by running %s, which kubectl runs whenever a context of it is used", name, u.Exec.Command))
		}
	}
	return out
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Entries lists every cluster, user and context one config defines.
func Entries(cfg *clientcmdapi.Config) []Entry {
	var out []Entry
	for name := range cfg.Clusters {
		out = append(out, Entry{"cluster", name})
	}
	for name := range cfg.AuthInfos {
		out = append(out, Entry{"user", name})
	}
	for name := range cfg.Contexts {
		out = append(out, Entry{"context", name})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ---- the state file ------------------------------------------------------------

// SetCurrent writes ctx as the current context into the state file, and nothing
// else: never into a file that holds credentials.
func SetCurrent(state, ctx string) error {
	cfg, err := LoadFile(state)
	if err != nil {
		return err
	}
	cfg.CurrentContext = ctx
	if err := WriteFile(state, cfg); err != nil {
		return fmt.Errorf("could not write the current context into %s: %w", state, err)
	}
	return nil
}

// ClearCurrent removes the current context from the state file, if it has one.
func ClearCurrent(state string) error {
	if _, err := os.Stat(state); err != nil {
		return nil
	}
	cfg, err := LoadFile(state)
	if err != nil {
		return err
	}
	if cfg.CurrentContext == "" {
		return nil
	}
	cfg.CurrentContext = ""
	return WriteFile(state, cfg)
}

// ReadPrevious returns the context stored in the previous-context file ("" when
// none).
func ReadPrevious(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WritePrevious records ctx as the previous context, mode 0600.
func WritePrevious(path, ctx string) error {
	return layout.WriteFileAtomic(path, []byte(ctx), 0o600)
}

// ---- importing -----------------------------------------------------------------

// ImportOptions shape an import.
type ImportOptions struct {
	Context   string // the context to take when the source holds several
	Server    string // rewrite the cluster's server address
	Rename    bool   // name the cluster, user and context after Name (false keeps the source's names: temp add)
	KeepNames bool   // with Rename: keep the context and user names the source gave them; the cluster still becomes Name
	// Templates for the context and user names when Rename is set and KeepNames is
	// not: {name} is Name, {user} and {cluster} are the source's entry names.
	// Empty means "{name}".
	ContextName string
	UserName    string
	Replace     bool // an existing destination is replaced
	TrashOld    bool // with Replace: the old file goes to the Trash first (else it is overwritten)
	// Except lists entries of the state file that the import may collide with
	// because they are about to move out of it (adopt).
	Except []Entry
}

// Prepare reads one context out of src, self-contained, renamed as opts say, with
// no current context, ready to be written. It does not touch the destination.
func Prepare(src, name string, opts ImportOptions) (*clientcmdapi.Config, error) {
	in, err := clientcmd.LoadFromFile(src)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", src, err)
	}
	if err := clientcmd.ResolveLocalPaths(in); err != nil {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	names := make([]string, 0, len(in.Contexts))
	for n := range in.Contexts {
		names = append(names, n)
	}
	sort.Strings(names)
	ctx := opts.Context
	switch {
	case ctx != "":
		if _, ok := in.Contexts[ctx]; !ok {
			return nil, fmt.Errorf("no context %s in %s; it has: %s", ctx, src, strings.Join(names, " "))
		}
	case len(names) == 1:
		ctx = names[0]
	case len(names) == 0:
		return nil, fmt.Errorf("%s holds no context", src)
	default:
		return nil, fmt.Errorf("%s holds %d contexts; take one with --context: %s", src, len(names), strings.Join(names, " "))
	}
	in.CurrentContext = ctx
	if err := clientcmdapi.MinifyConfig(in); err != nil {
		return nil, fmt.Errorf("cannot import %s: %w", src, err)
	}
	if err := clientcmdapi.FlattenConfig(in); err != nil {
		return nil, fmt.Errorf("cannot embed the certificate files of %s: %w", src, err)
	}
	if len(in.Clusters) != 1 {
		return nil, fmt.Errorf("cannot import %s: its context names no cluster that exists", src)
	}
	if len(in.AuthInfos) > 1 {
		return nil, fmt.Errorf("cannot import %s: more than one user", src)
	}
	out := clientcmdapi.NewConfig()
	context := in.Contexts[ctx]
	var srcCluster, srcUser string
	for n := range in.Clusters {
		srcCluster = n
	}
	for n := range in.AuthInfos {
		srcUser = n
	}
	clusterName, contextName, userName := srcCluster, ctx, srcUser
	if opts.Rename {
		clusterName = name
		if opts.KeepNames {
			contextName, userName = ctx, srcUser
		} else {
			contextName = expand(opts.ContextName, name, srcUser, srcCluster)
			userName = expand(opts.UserName, name, srcUser, srcCluster)
		}
	}
	cluster := in.Clusters[srcCluster]
	if opts.Server != "" {
		cluster.Server = opts.Server
	}
	cluster.LocationOfOrigin = ""
	out.Clusters[clusterName] = cluster
	context.Cluster = clusterName
	if srcUser != "" {
		user := in.AuthInfos[srcUser]
		user.LocationOfOrigin = ""
		out.AuthInfos[userName] = user
		context.AuthInfo = userName
	}
	context.LocationOfOrigin = ""
	out.Contexts[contextName] = context
	out.CurrentContext = ""
	return out, nil
}

func expand(tmpl, name, user, cluster string) string {
	if tmpl == "" {
		return name
	}
	r := strings.NewReplacer("{name}", name, "{user}", user, "{cluster}", cluster)
	return r.Replace(tmpl)
}

// CheckFree refuses when an entry of cfg is already defined in one of files,
// other than skip. Entries listed in except are allowed in the state file state
// (they are about to move out of it).
func CheckFree(cfg *clientcmdapi.Config, files []string, skip, state string, except []Entry) error {
	wanted := Entries(cfg)
	for _, f := range files {
		if f == skip {
			continue
		}
		if _, err := os.Stat(f); err != nil {
			continue
		}
		have, err := LoadFile(f)
		if err != nil {
			return err
		}
		for _, e := range Entries(have) {
			if f == state && contains(except, e) {
				continue
			}
			if contains(wanted, e) {
				return fmt.Errorf("a %s named %s already exists in %s; choose another name", e.Kind, e.Name, f)
			}
		}
	}
	return nil
}

func contains(list []Entry, e Entry) bool {
	for _, x := range list {
		if x == e {
			return true
		}
	}
	return false
}

// Import writes the prepared config to dest, after checking that none of its
// entries exists elsewhere in files. It returns the name of the written context.
func Import(cfg *clientcmdapi.Config, dest string, files []string, state string, getenv layout.Getenv, opts ImportOptions) (string, error) {
	if err := CheckFree(cfg, files, dest, state, opts.Except); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return "", err
	}
	if _, err := os.Stat(dest); err == nil {
		if !opts.Replace {
			return "", fmt.Errorf("%s exists", dest)
		}
		if opts.TrashOld {
			if err := layout.Trash(getenv, dest); err != nil {
				return "", fmt.Errorf("could not move the old %s to the Trash; nothing was replaced: %w", dest, err)
			}
		}
	}
	if err := WriteFile(dest, cfg); err != nil {
		return "", fmt.Errorf("could not write %s: %w", dest, err)
	}
	// Read back: a file kubectl cannot load is worse than no file.
	back, err := clientcmd.LoadFromFile(dest)
	if err != nil || len(back.Contexts) == 0 {
		_ = os.Remove(dest)
		return "", fmt.Errorf("the converted kubeconfig did not read back; nothing was written")
	}
	var ctx string
	for n := range cfg.Contexts {
		ctx = n
	}
	return ctx, nil
}

// RemoveFromFile deletes context ctx from the file at path, and its cluster and
// user when no other context there still uses them. Used by adopt on the state
// file.
func RemoveFromFile(path, ctx string) error {
	cfg, err := LoadFile(path)
	if err != nil {
		return err
	}
	c, ok := cfg.Contexts[ctx]
	if !ok {
		return nil
	}
	delete(cfg.Contexts, ctx)
	clusterUsed, userUsed := false, false
	for _, other := range cfg.Contexts {
		if other.Cluster == c.Cluster {
			clusterUsed = true
		}
		if other.AuthInfo == c.AuthInfo {
			userUsed = true
		}
	}
	if !clusterUsed {
		delete(cfg.Clusters, c.Cluster)
	}
	if !userUsed && c.AuthInfo != "" {
		delete(cfg.AuthInfos, c.AuthInfo)
	}
	return WriteFile(path, cfg)
}

// ServerOf returns the server of the cluster that context ctx uses, "-" when
// unknown.
func ServerOf(cfg *clientcmdapi.Config, ctx string) string {
	c, ok := cfg.Contexts[ctx]
	if !ok {
		return "-"
	}
	cl, ok := cfg.Clusters[c.Cluster]
	if !ok || cl.Server == "" {
		return "-"
	}
	return cl.Server
}
