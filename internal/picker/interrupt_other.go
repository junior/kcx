//go:build !windows

package picker

// catchInterrupt does nothing outside Windows: Ctrl-C at the menu ends kcx, as
// at any prompt, where a caught one would leave the read waiting for Enter.
func catchInterrupt() (stop func()) { return func() {} }
