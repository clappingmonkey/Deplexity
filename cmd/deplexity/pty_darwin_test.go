package main

import (
	"bytes"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY returns the controller and terminal ends of a new pseudo-terminal.
func openPTY(t *testing.T) (controller, tty *os.File) {
	t.Helper()
	controller, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	fd := controller.Fd()
	for _, req := range []uint{unix.TIOCPTYGRANT, unix.TIOCPTYUNLK} {
		if err := unix.IoctlSetInt(int(fd), req, 0); err != nil {
			controller.Close()
			t.Fatalf("pty ioctl: %v", err)
		}
	}
	var name [128]byte
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, unix.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		controller.Close()
		t.Fatalf("TIOCPTYGNAME: %v", errno)
	}
	tty, err = os.OpenFile(string(name[:bytes.IndexByte(name[:], 0)]), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		controller.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { tty.Close(); controller.Close() })
	return controller, tty
}
