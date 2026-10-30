package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaultsAndFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "config.toml")
	s, err := Load(missing)
	if err != nil {
		t.Fatal(err)
	}
	if s != Default() || !s.SecretCache || !s.Probe || s.LabNode || s.Picker != "fzf" || s.Trash != "auto" {
		t.Fatalf("defaults: %+v", s)
	}
	if err := os.WriteFile(missing, []byte("[settings]\nlab_node = true\nsecret_cache = false\npicker = \"plain\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err = Load(missing)
	if err != nil {
		t.Fatal(err)
	}
	if !s.LabNode || s.SecretCache || !s.Probe || s.Picker != "plain" || s.Trash != "auto" {
		t.Fatalf("file: %+v", s)
	}
}

func TestLoadRefusesMistakes(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"typo":   "[settings]\nlab_nod = true\n",
		"picker": "[settings]\npicker = \"gui\"\n",
		"trash":  "[settings]\ntrash = \"delete\"\n",
		"syntax": "[settings\n",
		"table":  "[setting]\nlab_node = true\n",
		"ps":     "[settings]\npowershell = \"powershell7\"\n",
		"psrel":  "[settings]\npowershell = \"bin/pwsh\"\n",
	}
	for name, content := range cases {
		p := filepath.Join(dir, name+".toml")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), p) {
			t.Errorf("%s: expected an error naming the file, got %v", name, err)
		}
	}
}

func TestPowerShellSetting(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(dir, "pwsh.exe")
	for _, v := range []string{"windows", "pwsh", abs} {
		p := filepath.Join(dir, "s.toml")
		if err := os.WriteFile(p, []byte("[settings]\npowershell = '"+v+"'\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		s, err := Load(p)
		if err != nil || s.PowerShell != v {
			t.Errorf("powershell = %q: %+v %v", v, s, err)
		}
	}
	if Default().PowerShell != "" {
		t.Fatal("the default is Windows PowerShell, by no setting")
	}
}
