// Package tty opens the person's terminal, for a prompt that must reach them
// even when standard input and output are pipes: /dev/tty on Unix, the console
// on Windows (CONIN$ to read, CONOUT$ to write).
package tty

import "os"

// TTY is the terminal: In to read from (its descriptor is what
// term.ReadPassword takes), Out to write a prompt to.
type TTY struct {
	In, Out *os.File
}

// Close closes what Open opened.
func (t *TTY) Close() error {
	err := t.In.Close()
	if t.Out != t.In {
		if e := t.Out.Close(); err == nil {
			err = e
		}
	}
	return err
}
