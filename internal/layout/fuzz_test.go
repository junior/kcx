package layout

import (
	"path/filepath"
	"strings"
	"testing"
)

// FuzzValidName: a name kcx accepts is a plain file name that stays in its
// directory on every platform: letters, digits, . _ and -, not starting with
// a dot, no separator, no @ (which splits a temp file's engine), at most 63.
func FuzzValidName(f *testing.F) {
	for _, s := range []string{"prod1", "office2-lab", "a.b_c", "../x", ".hidden", "x@docker", `a\b`, "", strings.Repeat("a", 64)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !ValidName(s) {
			return
		}
		if len(s) == 0 || len(s) > 63 || strings.HasPrefix(s, ".") || strings.ContainsAny(s, `/\@: `) ||
			strings.Contains(s, "..") && strings.Trim(s, ".") == "" || filepath.Base(s) != s {
			t.Fatalf("ValidName accepted %q", s)
		}
		for _, r := range s {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
				t.Fatalf("ValidName accepted %q, with %q", s, r)
			}
		}
	})
}
