//go:build linux

package shell

import (
	"os"
	"strconv"
	"unsafe"
)

// Linux pty ioctls; see the note in pty_darwin_test.go on why they are inline.
const (
	tiocSPtlck = 0x40045431
	tiocGPtn   = 0x80045430
	tiocSWinsz = 0x5414
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
	var unlock int32
	if err := ioctl(master.Fd(), tiocSPtlck, uintptr(unsafe.Pointer(&unlock))); err != nil {
		return fail(err)
	}
	var n uint32
	if err := ioctl(master.Fd(), tiocGPtn, uintptr(unsafe.Pointer(&n))); err != nil {
		return fail(err)
	}
	return master, "/dev/pts/" + strconv.Itoa(int(n)), nil
}
