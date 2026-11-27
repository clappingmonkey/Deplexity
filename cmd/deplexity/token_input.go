package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"golang.org/x/term"

	"github.com/clappingmonkey/deplexity/internal/auth"
)

// maxTerminalLine bounds how much of a typed line is kept. The rest of a
// longer line is still read, so it does not end up in the shell afterwards,
// and the token is rejected as too long.
const maxTerminalLine = 64 << 10

var errTerminalClosing = errors.New("terminal is being restored for exit")

func (cmd *LoginCmd) readStdinToken(ctx context.Context) (string, error) {
	if cmd.stdin != nil {
		return readContext(ctx, func() (string, error) { return auth.ReadToken(cmd.stdin) })
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return readContext(ctx, func() (string, error) { return auth.ReadToken(os.Stdin) })
	}
	// Typed or pasted at a terminal: keep the token off the screen and out of
	// scrollback.
	return readHiddenToken(ctx, os.Stdin, os.Stderr, func() (func() error, error) { return hideInput(fd) })
}

// hiddenTerminal is the terminal whose echo is off, so forceQuit can turn it
// back on before os.Exit. The mutex makes hiding, restoring and shutdown one
// step each: a restore waits for a hide in progress, and returns only once
// the terminal is back.
var hiddenTerminal struct {
	mu      sync.Mutex
	restore func() error
	closed  bool
}

func hideTerminal(hide func() (func() error, error)) error {
	hiddenTerminal.mu.Lock()
	defer hiddenTerminal.mu.Unlock()
	if hiddenTerminal.closed {
		return errTerminalClosing
	}
	restore, err := hide()
	if err != nil {
		return err
	}
	hiddenTerminal.restore = restore
	return nil
}

func restoreTerminal() {
	hiddenTerminal.mu.Lock()
	defer hiddenTerminal.mu.Unlock()
	restoreTerminalLocked()
}

// shutdownTerminal restores the terminal and stops any later hide. forceQuit
// calls it before os.Exit.
func shutdownTerminal() {
	hiddenTerminal.mu.Lock()
	defer hiddenTerminal.mu.Unlock()
	hiddenTerminal.closed = true
	restoreTerminalLocked()
}

func restoreTerminalLocked() {
	if restore := hiddenTerminal.restore; restore != nil {
		hiddenTerminal.restore = nil
		_ = restore()
	}
}

// readHiddenToken reads a token typed at the terminal r with echo off. The
// prompt goes to w only once echo is off, so nothing typed in answer to it
// can be shown. It returns as soon as ctx ends, restoring the terminal first.
func readHiddenToken(ctx context.Context, r io.Reader, w io.Writer, hide func() (func() error, error)) (string, error) {
	if err := hideTerminal(hide); err != nil {
		return "", fmt.Errorf("could not hide terminal input (%v); pipe the token in instead", err)
	}
	defer restoreTerminal()
	fmt.Fprint(w, "Paste the session token and press Enter (input is hidden): ")
	defer fmt.Fprintln(w)
	return readContext(ctx, func() (string, error) {
		line, err := readTerminalLine(r)
		if err != nil {
			return "", err
		}
		return auth.ReadToken(strings.NewReader(line + "\n"))
	})
}

// readContext runs a blocking read and returns when it finishes or ctx ends.
// A read still blocked at that point is left behind; the command is ending.
func readContext(ctx context.Context, read func() (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	type result struct {
		token string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		token, err := read()
		done <- result{token, err}
	}()
	select {
	case res := <-done:
		return res.token, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// readTerminalLine reads one line from a terminal in non-canonical mode,
// where the OS line limit (1024 bytes on macOS) does not apply. It handles
// what canonical mode did: Enter ends the line, Ctrl+D (Ctrl+Z on Windows)
// ends it early, Backspace deletes and Ctrl+U clears.
func readTerminalLine(r io.Reader) (string, error) {
	br := bufio.NewReader(r)
	var line []byte
	overflow := false
	for {
		b, err := br.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return string(line), nil
			}
			return "", fmt.Errorf("could not read session token: %w", err)
		}
		switch b {
		case '\r', '\n', 0x04, 0x1a:
			if overflow {
				return "", fmt.Errorf("session token is longer than %d bytes", maxTerminalLine)
			}
			return string(line), nil
		case 0x7f, 0x08:
			if len(line) > 0 {
				line = line[:len(line)-1]
			}
		case 0x15:
			line, overflow = line[:0], false
		default:
			if len(line) < maxTerminalLine {
				line = append(line, b)
			} else {
				overflow = true
			}
		}
	}
}
