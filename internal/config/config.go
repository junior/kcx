// Package config reads the machine settings, ~/.config/kcx/config.toml: what is
// true of this machine, as opposed to the profiles, which describe clusters.
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Settings is the [settings] table. Every field has a default, so a missing file
// is a valid configuration.
type Settings struct {
	// LabNode refuses permanent credentials on this machine (a lab node holds none).
	LabNode bool `toml:"lab_node"`
	// SecretCache false asks a caching secret provider to forget after every fetch
	// (a shared host where a cache outlives the session).
	SecretCache bool `toml:"secret_cache"`
	// Probe false skips the authenticated probe before a switch (5.5 of the design).
	Probe bool `toml:"probe"`
	// Picker is "fzf" (when on PATH and on a terminal) or "plain" (a numbered list).
	Picker string `toml:"picker"`
	// Trash is "auto" (the system Trash, refusing when there is none) or "none"
	// (a refusal that names the file, never a deletion).
	Trash string `toml:"trash"`
}

// Default is the configuration of a machine without a settings file.
func Default() Settings {
	return Settings{SecretCache: true, Probe: true, Picker: "fzf", Trash: "auto"}
}

type file struct {
	Settings Settings `toml:"settings"`
}

// Load reads path, or returns the defaults when it does not exist. Unknown keys
// are an error: a typo in a settings file must not pass as a default.
func Load(path string) (Settings, error) {
	s := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return s, fmt.Errorf("%s: %w", path, err)
	}
	f := file{Settings: s}
	meta, err := toml.Decode(string(data), &f)
	if err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return s, fmt.Errorf("%s: unknown key(s): %s", path, strings.Join(keys, ", "))
	}
	s = f.Settings
	switch s.Picker {
	case "fzf", "plain":
	default:
		return s, fmt.Errorf("%s: picker must be fzf or plain, not %q", path, s.Picker)
	}
	switch s.Trash {
	case "auto", "none":
	default:
		return s, fmt.Errorf("%s: trash must be auto or none, not %q", path, s.Trash)
	}
	return s, nil
}
