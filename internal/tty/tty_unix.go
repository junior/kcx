//go:build !windows

package tty

import "os"

// Open opens /dev/tty, for reading and writing.
func Open() (*TTY, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return &TTY{In: f, Out: f}, nil
}
