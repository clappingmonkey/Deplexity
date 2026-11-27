//go:build darwin || linux

package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func echoOn(t *testing.T, fd int) bool {
	t.Helper()
	tio, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		t.Fatal(err)
	}
	return tio.Lflag&unix.ECHO != 0
}

// promptSignal closes its channel on the first write, which readHiddenToken
// does once echo is off: the reader is ready.
type promptSignal struct {
	once sync.Once
	ch   chan struct{}
}

func newPromptSignal() *promptSignal { return &promptSignal{ch: make(chan struct{})} }

func (p *promptSignal) Write(b []byte) (int, error) {
	p.once.Do(func() { close(p.ch) })
	return len(b), nil
}

func waitPrompt(t *testing.T, p *promptSignal) {
	t.Helper()
	select {
	case <-p.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("hidden read never prompted")
	}
}

// readHiddenFromPTY runs readHiddenToken on a new pty and, once it has
// prompted, either cancels (when cancel is set) or types input.
func readHiddenFromPTY(t *testing.T, ctx context.Context, cancel context.CancelFunc, input string) (string, error, bool) {
	t.Helper()
	controller, tty := openPTY(t)
	fd := int(tty.Fd())
	if !echoOn(t, fd) {
		t.Fatal("new pty has echo off")
	}
	type result struct {
		token string
		err   error
	}
	prompted := newPromptSignal()
	done := make(chan result, 1)
	go func() {
		token, err := readHiddenToken(ctx, tty, prompted, func() (func() error, error) { return hideInput(fd) })
		done <- result{token, err}
	}()
	waitPrompt(t, prompted)
	if echoOn(t, fd) {
		t.Fatal("echo still on after the prompt")
	}
	if cancel != nil {
		cancel()
	}
	if input != "" {
		if _, err := controller.WriteString(input); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case res := <-done:
		return res.token, res.err, echoOn(t, fd)
	case <-time.After(5 * time.Second):
		t.Fatal("hidden read did not return")
		return "", nil, false
	}
}

func TestReadHiddenTokenOnTerminal(t *testing.T) {
	t.Run("token", func(t *testing.T) {
		got, err, echo := readHiddenFromPTY(t, context.Background(), nil, "tok-1\n")
		if err != nil || got != "tok-1" || !echo {
			t.Fatalf("token = %q, err = %v, echo restored = %v", got, err, echo)
		}
	})
	t.Run("ctrl-d on an empty line", func(t *testing.T) {
		_, err, echo := readHiddenFromPTY(t, context.Background(), nil, "\x04")
		if err == nil || !strings.Contains(err.Error(), "empty") || !echo {
			t.Fatalf("err = %v, echo restored = %v", err, echo)
		}
	})
	t.Run("cancel while waiting", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err, echo := readHiddenFromPTY(t, ctx, cancel, "")
		if !errors.Is(err, context.Canceled) || !echo {
			t.Fatalf("err = %v, echo restored = %v", err, echo)
		}
	})
}

func TestRestoreTerminalDuringHiddenRead(t *testing.T) {
	resetHiddenTerminal(t)
	_, tty := openPTY(t)
	fd := int(tty.Fd())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	prompted := newPromptSignal()
	go func() {
		_, _ = readHiddenToken(ctx, tty, prompted, func() (func() error, error) { return hideInput(fd) })
	}()
	waitPrompt(t, prompted)
	if echoOn(t, fd) {
		t.Fatal("echo still on after the prompt")
	}
	// What forceQuit does before os.Exit.
	shutdownTerminal()
	if !echoOn(t, fd) {
		t.Fatal("restoreTerminal left echo off")
	}
}

func TestDisableEchoRejectsNonTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, err := hideInput(int(r.Fd())); err == nil {
		t.Fatal("hideInput accepted a pipe")
	}
}

func TestReadHiddenTokenAcceptsLongTokenOnTerminal(t *testing.T) {
	// Longer than the 1024-byte canonical line limit on macOS.
	for _, n := range []int{1024, 2048, 4096} {
		want := strings.Repeat("x", n)
		got, err, echo := readHiddenFromPTY(t, context.Background(), nil, want+"\r")
		if err != nil || got != want || !echo {
			t.Fatalf("%d bytes: len = %d, err = %v, echo restored = %v", n, len(got), err, echo)
		}
	}
}

// promptProbe records whether echo was already off when the prompt was
// written.
type promptProbe struct {
	t        *testing.T
	fd       int
	wrote    bool
	echoSeen bool
}

func (p *promptProbe) Write(b []byte) (int, error) {
	if !p.wrote {
		p.wrote = true
		p.echoSeen = echoOn(p.t, p.fd)
	}
	return len(b), nil
}

func TestHiddenPromptAppearsOnlyAfterEchoIsOff(t *testing.T) {
	resetHiddenTerminal(t)
	controller, tty := openPTY(t)
	fd := int(tty.Fd())
	probe := &promptProbe{t: t, fd: fd}
	go func() { _, _ = controller.WriteString("tok\r") }()
	if _, err := readHiddenToken(context.Background(), tty, probe, func() (func() error, error) { return hideInput(fd) }); err != nil {
		t.Fatal(err)
	}
	if !probe.wrote || probe.echoSeen {
		t.Fatalf("prompt written = %v, echo on at prompt = %v", probe.wrote, probe.echoSeen)
	}
}
