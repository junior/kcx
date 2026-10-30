//go:build windows

package tty

import "os"

// Open opens the console: CONIN$ to read (with write access too, which setting
// its mode for a password needs) and CONOUT$ to write.
func Open() (*TTY, error) {
	in, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	return &TTY{In: in, Out: out}, nil
}
