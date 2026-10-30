package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrintable(t *testing.T) {
	for in, want := range map[string]string{
		"prod1":                      "prod1",
		"caf\u00e9-\u65e5\u672c":     "caf\u00e9-\u65e5\u672c", // letters of any script stay
		"evil\x1b]0;pwned\x07":       `evil\x1b]0;pwned\x07`,
		"two\nlines\r":               `two\x0alines\x0d`,
		"del\x7f c1\u0085":           `del\x7f c1\x85`,
		"admin\u202egnp.exe":         `admin\u202egnp.exe`, // a direction override
		"https://k8s.example:6443/x": "https://k8s.example:6443/x",
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %q, want %q", in, got, want)
		}
	}
}

// kcx add says what an imported file does that its owner should know; doctor
// lists the same as notes; kcx ls escapes what a terminal would act on.
func TestRisksAndEscapes(t *testing.T) {
	e, l := fixture(t)
	src := filepath.Join(t.TempDir(), "vendor.yaml")
	content := `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://vendor.example.test:6443
    insecure-skip-tls-verify: true
users:
- name: u
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: vendor-login
      args: [--token, not-a-secret-arg]
contexts:
- name: x
  context: {cluster: c, user: u}
`
	if err := os.WriteFile(src, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, errs := runKcx(t, e, "add", "vendor", src)
	if code != 0 || !strings.Contains(errs, "note: cluster vendor skips TLS verification") ||
		!strings.Contains(errs, "note: user vendor logs in by running vendor-login") || strings.Contains(errs, "not-a-secret-arg") {
		t.Fatalf("add: %d %q", code, errs)
	}
	code, out, _ := runKcx(t, e, "doctor")
	if !strings.Contains(out, "note  "+l.PermFile("vendor")+": cluster vendor skips TLS verification") ||
		!strings.Contains(out, "note  "+l.PermFile("vendor")+": user vendor logs in by running vendor-login") {
		t.Fatalf("doctor: %d %q", code, out)
	}

	odd := "apiVersion: v1\nkind: Config\nclusters:\n- name: odd\n  cluster: {server: 'http://odd.example.test'}\n" +
		"users:\n- name: odd\n  user: {token: not-a-secret}\ncontexts:\n- name: \"odd\\e]0;pwned\\a\"\n  context: {cluster: odd, user: odd}\n"
	if err := os.WriteFile(l.PermFile("odd"), []byte(odd), 0o600); err != nil {
		t.Fatal(err)
	}
	_, out, _ = runKcx(t, e, "ls")
	if strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, `odd\x1b]0;pwned\x07`) {
		t.Fatalf("ls let a control character through: %q", out)
	}
	_, out, _ = runKcx(t, e, "doctor")
	if !strings.Contains(out, "cluster odd is reached over plain http") {
		t.Fatalf("doctor on plain http: %q", out)
	}
}
