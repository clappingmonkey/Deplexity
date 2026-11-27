//go:build windows

package main

import "golang.org/x/sys/windows"

// hideInput turns off console echo and line input and keeps Ctrl+C, so a
// pasted token is not shown and is not cut off by the console line buffer.
// readTerminalLine does the line handling. The returned func restores the
// previous mode.
func hideInput(fd int) (func() error, error) {
	h := windows.Handle(fd)
	var old uint32
	if err := windows.GetConsoleMode(h, &old); err != nil {
		return nil, err
	}
	hidden := old&^(windows.ENABLE_ECHO_INPUT|windows.ENABLE_LINE_INPUT) | windows.ENABLE_PROCESSED_INPUT
	if err := windows.SetConsoleMode(h, hidden); err != nil {
		return nil, err
	}
	return func() error { return windows.SetConsoleMode(h, old) }, nil
}
