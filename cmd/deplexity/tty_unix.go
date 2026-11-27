//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package main

import "golang.org/x/sys/unix"

// hideInput turns off echo and canonical mode on the terminal fd and keeps
// Ctrl+C, so a pasted token is not shown and is not cut off by the kernel's
// line limit (1024 bytes on macOS). readTerminalLine does the line handling.
// The returned func restores the previous settings.
func hideInput(fd int) (func() error, error) {
	old, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	hidden := *old
	hidden.Lflag &^= unix.ECHO | unix.ICANON
	hidden.Lflag |= unix.ISIG
	hidden.Cc[unix.VMIN] = 1
	hidden.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &hidden); err != nil {
		return nil, err
	}
	return func() error { return unix.IoctlSetTermios(fd, ioctlSetTermios, old) }, nil
}
