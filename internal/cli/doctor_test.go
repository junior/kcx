package cli

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// --fix repairs the modes and a cluster file's own current context and says so;
// findings of one kind across many files fold into one line.
func TestDoctorFixAndFold(t *testing.T) {
	e, l := fixture(t)
	for i := 1; i <= 5; i++ {
		n := fmt.Sprintf("c%d", i)
		writeKubeconfig(t, l.PermFile(n), n, n, n, "https://"+n+".example.test", n)
	}
	if err := os.Chmod(l.PermFile("c1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(l.Perm, 0o755); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runKcx(t, e, "doctor")
	if code != 1 ||
		!strings.Contains(out, "FIX   "+l.Perm+" is readable by others (mode 755)") ||
		!strings.Contains(out, "FIX   "+l.PermFile("c1")+" is readable by others (mode 644)") ||
		!strings.Contains(out, "FIX   5 files set a current context of their own, which wins whenever the state file has none ("+l.PermFile("c1")+", "+l.PermFile("c2")+", "+l.PermFile("c3")+", and 2 more); kcx doctor --fix unsets them") ||
		!strings.Contains(errs, "3 problem(s) to fix") {
		t.Fatalf("doctor before the fix: %d %q %q", code, out, errs)
	}
	code, out, errs = runKcx(t, e, "doctor", "--fix")
	if code != 0 ||
		!strings.Contains(out, "fixed "+l.Perm+": mode 700\n") ||
		!strings.Contains(out, "fixed "+l.PermFile("c1")+": mode 600\n") ||
		!strings.Contains(out, "fixed "+l.PermFile("c5")+": its own current context unset\n") ||
		strings.Contains(out, "FIX   ") {
		t.Fatalf("doctor --fix: %d %q %q", code, out, errs)
	}
	for i := 1; i <= 5; i++ {
		f := l.PermFile(fmt.Sprintf("c%d", i))
		info, err := os.Stat(f)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s after the fix: %v %v", f, info, err)
		}
		data, _ := os.ReadFile(f)
		if strings.Contains(string(data), "current-context: c") || !strings.Contains(string(data), "server: https://") {
			t.Fatalf("%s after the fix:\n%s", f, data)
		}
	}
	if code, out, errs := runKcx(t, e, "doctor"); code != 0 {
		t.Fatalf("doctor after the fix: %d %q %q", code, out, errs)
	}
	if code, _, errs := runKcx(t, e, "doctor", "--nope"); code != 2 || !strings.Contains(errs, "kcx doctor [--fix]") {
		t.Fatalf("doctor --nope: %d %q", code, errs)
	}
}
