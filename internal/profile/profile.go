// Package profile reads the cluster profiles, ~/.config/kcx/profiles/*.toml: the
// users' own data about which clusters exist, how each is fetched and which
// group and site it belongs to. kcx ships none.
//
// A profile has three kinds of table:
//
//	[provider.NAME]   how a kubeconfig is obtained: kind, command, secret, templates
//	[group.NAME]      defaults for a set of clusters: provider, label, color, readonly
//	[cluster.NAME]    one cluster: group, site, aliases, overrides
//
// Several files are read in sorted order and merged; a name defined twice is an
// error, so a machine's lists cannot shadow one another.
package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/junior/kcx/v2/internal/layout"
)

// Template is a string, or a list of strings of which the first that renders to
// something non-empty is used (`user = ["{env.ACME_USER}", "{env.USER}"]`).
type Template []string

// UnmarshalTOML accepts a string or an array of strings.
func (t *Template) UnmarshalTOML(v any) error {
	switch x := v.(type) {
	case string:
		*t = Template{x}
	case []any:
		out := make(Template, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return fmt.Errorf("a template list holds strings, not %T", e)
			}
			out = append(out, s)
		}
		*t = out
	default:
		return fmt.Errorf("a template is a string or a list of strings, not %T", v)
	}
	return nil
}

// Provider is how a kubeconfig is obtained.
type Provider struct {
	Name        string   `toml:"-"`
	Kind        string   `toml:"kind"`
	Command     []string `toml:"command"`
	Secret      string   `toml:"secret"`
	Host        Template `toml:"host"`
	User        Template `toml:"user"`
	ContextName string   `toml:"context_name"`
	UserName    string   `toml:"user_name"`
	Rejected    string   `toml:"rejected"`
	Timeout     string   `toml:"timeout"`
	// OIDC kind, reserved for the next build.
	Issuer   string `toml:"issuer"`
	ClientID string `toml:"client_id"`
	Source   string `toml:"-"`

	rejected *regexp.Regexp
	timeout  time.Duration
}

// RejectedRE is the compiled `rejected` pattern, nil when none.
func (p *Provider) RejectedRE() *regexp.Regexp { return p.rejected }

// TimeoutDuration is the parsed `timeout`, 0 for none.
func (p *Provider) TimeoutDuration() time.Duration { return p.timeout }

// Group is a set of clusters with shared defaults.
type Group struct {
	Name     string `toml:"-"`
	Label    string `toml:"label"` // what the picker shows for the group; the name when empty
	Provider string `toml:"provider"`
	Color    string `toml:"color"` // one of Colors, or empty
	Readonly bool   `toml:"readonly"`
	Source   string `toml:"-"`
}

// Display is the group's label, or its name when it has none.
func (g *Group) Display() string {
	if g.Label != "" {
		return g.Label
	}
	return g.Name
}

// Colors are the colour names a group may carry; the picker and kcx profile ls
// paint its rows with them.
var Colors = []string{"red", "green", "yellow", "blue", "magenta", "cyan", "white"}

// Cluster is one cluster a profile knows.
type Cluster struct {
	Name     string   `toml:"-"`
	Group    string   `toml:"group"`
	Site     string   `toml:"site"`
	Aliases  []string `toml:"aliases"`
	Host     Template `toml:"host"`
	User     Template `toml:"user"`
	Provider string   `toml:"provider"`
	Readonly *bool    `toml:"readonly"`
	Source   string   `toml:"-"`
}

// Set is the merged content of every profile file. Groups and clusters keep the
// order the files list them in (file by file, as Files sorts them): a picker
// shows them that way, and a fetch of many walks them that way.
type Set struct {
	Providers map[string]*Provider
	Groups    map[string]*Group
	Clusters  map[string]*Cluster
	Files     []string
	aliases   map[string]string // alias -> cluster name
	groups    []string          // group names in file order
	clusters  []string          // cluster names in file order
}

type fileContent struct {
	Provider map[string]*Provider `toml:"provider"`
	Group    map[string]*Group    `toml:"group"`
	Cluster  map[string]*Cluster  `toml:"cluster"`
}

// Files lists the profile files to read: the *.toml files of each directory in
// sources, sorted, and each plain file named. A missing directory is skipped.
func Files(sources []string) ([]string, error) {
	var out []string
	for _, s := range sources {
		info, err := os.Stat(s)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		if !info.IsDir() {
			out = append(out, s)
			continue
		}
		entries, err := os.ReadDir(s)
		if err != nil {
			return nil, err
		}
		var files []string
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") || !strings.HasSuffix(e.Name(), ".toml") {
				continue
			}
			files = append(files, filepath.Join(s, e.Name()))
		}
		sort.Strings(files)
		out = append(out, files...)
	}
	return out, nil
}

// Load reads and merges the profile files. The first error stops it: a profile
// with a mistake is not half-used.
func Load(files []string) (*Set, error) {
	set := &Set{
		Providers: map[string]*Provider{},
		Groups:    map[string]*Group{},
		Clusters:  map[string]*Cluster{},
		aliases:   map[string]string{},
		Files:     files,
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		var c fileContent
		meta, err := toml.Decode(string(data), &c)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		if undecoded := meta.Undecoded(); len(undecoded) > 0 {
			keys := make([]string, 0, len(undecoded))
			for _, k := range undecoded {
				keys = append(keys, k.String())
			}
			return nil, fmt.Errorf("%s: unknown key(s): %s", f, strings.Join(keys, ", "))
		}
		for name, p := range c.Provider {
			if prev, dup := set.Providers[name]; dup {
				return nil, fmt.Errorf("provider %s is defined in %s and in %s", name, prev.Source, f)
			}
			p.Name, p.Source = name, f
			set.Providers[name] = p
		}
		for name, g := range c.Group {
			if prev, dup := set.Groups[name]; dup {
				return nil, fmt.Errorf("group %s is defined in %s and in %s", name, prev.Source, f)
			}
			g.Name, g.Source = name, f
			set.Groups[name] = g
		}
		for name, cl := range c.Cluster {
			if prev, dup := set.Clusters[name]; dup {
				return nil, fmt.Errorf("cluster %s is defined in %s and in %s", name, prev.Source, f)
			}
			cl.Name, cl.Source = name, f
			set.Clusters[name] = cl
		}
		set.groups = append(set.groups, ordered(meta, "group", c.Group)...)
		set.clusters = append(set.clusters, ordered(meta, "cluster", c.Cluster)...)
	}
	if err := set.validate(); err != nil {
		return nil, err
	}
	return set, nil
}

func (s *Set) validate() error {
	for name, p := range s.Providers {
		switch p.Kind {
		case "":
			p.Kind = "command"
		case "command", "oidc":
		default:
			return fmt.Errorf("%s: provider %s: kind must be command or oidc, not %q", p.Source, name, p.Kind)
		}
		if p.Kind == "command" && len(p.Command) == 0 {
			return fmt.Errorf("%s: provider %s: a command provider needs a command", p.Source, name)
		}
		if p.Rejected != "" {
			re, err := regexp.Compile(p.Rejected)
			if err != nil {
				return fmt.Errorf("%s: provider %s: rejected: %w", p.Source, name, err)
			}
			p.rejected = re
		}
		if p.Timeout != "" {
			d, err := time.ParseDuration(p.Timeout)
			if err != nil || d <= 0 {
				return fmt.Errorf("%s: provider %s: timeout %q is not a duration like 90s", p.Source, name, p.Timeout)
			}
			p.timeout = d
		}
	}
	for name, g := range s.Groups {
		if g.Provider != "" {
			if _, ok := s.Providers[g.Provider]; !ok {
				return fmt.Errorf("%s: group %s names provider %s, which no profile defines", g.Source, name, g.Provider)
			}
		}
		if g.Color != "" && !containsName(Colors, g.Color) {
			return fmt.Errorf("%s: group %s: color %q is not one of %s", g.Source, name, g.Color, strings.Join(Colors, ", "))
		}
	}
	names := make([]string, 0, len(s.Clusters))
	for n := range s.Clusters {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		c := s.Clusters[name]
		if !layout.ValidName(name) {
			return fmt.Errorf("%s: cluster %s: a name takes letters, digits, . _ and -, up to 63", c.Source, name)
		}
		if c.Group == "" {
			return fmt.Errorf("%s: cluster %s has no group", c.Source, name)
		}
		g, ok := s.Groups[c.Group]
		if !ok {
			return fmt.Errorf("%s: cluster %s names group %s, which no profile defines", c.Source, name, c.Group)
		}
		provider := c.Provider
		if provider == "" {
			provider = g.Provider
		}
		if provider == "" {
			return fmt.Errorf("%s: cluster %s has no provider: set one on it or on group %s", c.Source, name, c.Group)
		}
		if _, ok := s.Providers[provider]; !ok {
			return fmt.Errorf("%s: cluster %s names provider %s, which no profile defines", c.Source, name, provider)
		}
		for _, alias := range c.Aliases {
			if !layout.ValidName(alias) {
				return fmt.Errorf("%s: cluster %s: alias %q is not a valid name", c.Source, name, alias)
			}
			if _, clash := s.Clusters[alias]; clash {
				return fmt.Errorf("%s: cluster %s: alias %s is also a cluster", c.Source, name, alias)
			}
			if other, dup := s.aliases[alias]; dup && other != name {
				return fmt.Errorf("%s: alias %s points at both %s and %s", c.Source, alias, other, name)
			}
			s.aliases[alias] = name
		}
	}
	return nil
}

// Lookup finds a cluster by name or alias.
func (s *Set) Lookup(name string) (*Cluster, bool) {
	if c, ok := s.Clusters[name]; ok {
		return c, true
	}
	if target, ok := s.aliases[name]; ok {
		return s.Clusters[target], true
	}
	return nil, false
}

// Names lists the cluster names in the order the profiles list them.
func (s *Set) Names() []string {
	return append([]string(nil), s.clusters...)
}

// GroupNames lists the group names in the order the profiles list them.
func (s *Set) GroupNames() []string {
	return append([]string(nil), s.groups...)
}

// InGroup lists the clusters of a group, in the order the profiles list them.
func (s *Set) InGroup(group string) []string {
	var out []string
	for _, n := range s.clusters {
		if s.Clusters[n].Group == group {
			out = append(out, n)
		}
	}
	return out
}

// ordered lists the names of a file's tables under one head ("group" or
// "cluster") in the order written, as the decoder saw them, with any the file
// declared another way appended in name order.
func ordered[V any](meta toml.MetaData, head string, tables map[string]V) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range meta.Keys() {
		if len(k) == 2 && k[0] == head {
			if _, ok := tables[k[1]]; ok && !seen[k[1]] {
				seen[k[1]] = true
				out = append(out, k[1])
			}
		}
	}
	var rest []string
	for n := range tables {
		if !seen[n] {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}

func containsName(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// GroupOf returns the cluster's group.
func (s *Set) GroupOf(c *Cluster) *Group { return s.Groups[c.Group] }

// ProviderOf returns the provider that fetches c: its own, else its group's.
func (s *Set) ProviderOf(c *Cluster) *Provider {
	if c.Provider != "" {
		return s.Providers[c.Provider]
	}
	return s.Providers[s.Groups[c.Group].Provider]
}

// Readonly says whether the cluster defaults to read-only: its own flag, else
// its group's.
func (s *Set) Readonly(c *Cluster) bool {
	if c.Readonly != nil {
		return *c.Readonly
	}
	return s.Groups[c.Group].Readonly
}

// Vars are the values a template may name.
type Vars struct {
	Name, Host, User, Out string
	Getenv                layout.Getenv
}

var placeholder = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_.]*)\}`)

// Render expands {name}, {host}, {user}, {out} and {env.VAR} in tmpl. An unknown
// placeholder is an error: a typo must not become part of a command.
func Render(tmpl string, v Vars) (string, error) {
	var bad string
	out := placeholder.ReplaceAllStringFunc(tmpl, func(m string) string {
		key := m[1 : len(m)-1]
		switch {
		case key == "name":
			return v.Name
		case key == "host":
			return v.Host
		case key == "user":
			return v.User
		case key == "out":
			return v.Out
		case strings.HasPrefix(key, "env."):
			if v.Getenv == nil {
				return ""
			}
			return v.Getenv(strings.TrimPrefix(key, "env."))
		}
		if bad == "" {
			bad = m
		}
		return m
	})
	if bad != "" {
		return "", fmt.Errorf("unknown placeholder %s in %q", bad, tmpl)
	}
	return out, nil
}

// RenderFirst renders the list's templates in order and returns the first
// non-empty result, or "" when none renders to anything.
func RenderFirst(t Template, v Vars) (string, error) {
	for _, tmpl := range t {
		s, err := Render(tmpl, v)
		if err != nil {
			return "", err
		}
		if s != "" {
			return s, nil
		}
	}
	return "", nil
}

// Resolve computes the host and user of cluster c for a fetch: the cluster's own
// templates win over the provider's; userOverride (-u) wins over both.
func (s *Set) Resolve(c *Cluster, userOverride string, getenv layout.Getenv) (host, user string, err error) {
	p := s.ProviderOf(c)
	v := Vars{Name: c.Name, Getenv: getenv}
	hostT := c.Host
	if len(hostT) == 0 {
		hostT = p.Host
	}
	if host, err = RenderFirst(hostT, v); err != nil {
		return "", "", fmt.Errorf("cluster %s: host: %w", c.Name, err)
	}
	v.Host = host
	switch {
	case userOverride != "":
		user = userOverride
	default:
		userT := c.User
		if len(userT) == 0 {
			userT = p.User
		}
		if user, err = RenderFirst(userT, v); err != nil {
			return "", "", fmt.Errorf("cluster %s: user: %w", c.Name, err)
		}
	}
	return host, user, nil
}

// Command renders the provider's command for a fetch of c into out.
func (s *Set) Command(c *Cluster, host, user, out string, getenv layout.Getenv) ([]string, error) {
	p := s.ProviderOf(c)
	v := Vars{Name: c.Name, Host: host, User: user, Out: out, Getenv: getenv}
	args := make([]string, 0, len(p.Command))
	for _, a := range p.Command {
		r, err := Render(a, v)
		if err != nil {
			return nil, fmt.Errorf("provider %s: %w", p.Name, err)
		}
		args = append(args, r)
	}
	return args, nil
}
