package secret

import (
	"runtime"
	"testing"
)

// needsPOSIX skips a test that runs POSIX shell scripts (a stand-in shell, a
// fake platform command or secret provider, sh -c): Windows has no sh that kcx
// relies on, and the Windows runner tests the rest.
func needsPOSIX(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("runs POSIX shell scripts")
	}
}
