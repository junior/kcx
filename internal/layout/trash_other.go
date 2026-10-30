//go:build !windows

package layout

// platformTrash is the Trash the platform offers through its own API; on Unix
// kcx uses the trash commands instead.
func platformTrash(paths []string) (handled bool, err error) { return false, nil }
