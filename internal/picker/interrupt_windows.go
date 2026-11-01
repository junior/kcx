//go:build windows

package picker

import (
	"os"
	"os/signal"
)

// catchInterrupt keeps a Ctrl-C at the console from ending kcx while the menu
// waits for an answer: the console then ends the read, which the menu takes as
// a quit. It returns what stops the catching.
func catchInterrupt() (stop func()) {
	if signal.Ignored(os.Interrupt) {
		return func() {}
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	return func() { signal.Stop(c) }
}
