//go:build darwin

package shell

import (
	"bytes"
	"errors"
	"os"
	"unsafe"
)

// Darwin pty ioctls. They are spelled out here rather than pulled from a
// library because the pty in this file exists only to give one test a terminal
// with a controllable round trip, and that is not worth a dependency.
const (
	tiocPtyGrant = 0x20007454
	tiocPtyUnlk  = 0x20007452
	tiocPtyGname = 0x40807453
	tiocSWinsz   = 0x80087467
)

// openPTY returns the master side of a new pseudo-terminal and the path of its
// slave.
func openPTY() (*os.File, string, error) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, "", err
	}
	fail := func(err error) (*os.File, string, error) {
		_ = master.Close()
		return nil, "", err
	}
	if err := ioctl(master.Fd(), tiocPtyGrant, 0); err != nil {
		return fail(err)
	}
	if err := ioctl(master.Fd(), tiocPtyUnlk, 0); err != nil {
		return fail(err)
	}
	name := make([]byte, 128)
	if err := ioctl(master.Fd(), tiocPtyGname, uintptr(unsafe.Pointer(&name[0]))); err != nil {
		return fail(err)
	}
	end := bytes.IndexByte(name, 0)
	if end < 0 {
		return fail(errors.New("pty: unterminated slave name"))
	}
	return master, string(name[:end]), nil
}
