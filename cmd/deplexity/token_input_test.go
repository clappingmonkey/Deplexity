package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetHiddenTerminal(t *testing.T) {
	t.Helper()
	reset := func() {
		hiddenTerminal.mu.Lock()
		hiddenTerminal.restore, hiddenTerminal.closed = nil, false
		hiddenTerminal.mu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func TestReadTerminalLine(t *testing.T) {
	tests := []struct {
		name, in, want string
		wantErr        bool
	}{
		{name: "enter", in: "tok\rrest", want: "tok"},
		{name: "newline", in: "tok\n", want: "tok"},
		{name: "ctrl-d ends the line", in: "tok\x04", want: "tok"},
		{name: "ctrl-z ends the line", in: "tok\x1a", want: "tok"},
		{name: "eof", in: "tok", want: "tok"},
		{name: "backspace", in: "tox\x7fk\x08\x08ok\n", want: "tok"},
		{name: "backspace on empty", in: "\x7ftok\n", want: "tok"},
		{name: "ctrl-u clears", in: "junk\x15tok\n", want: "tok"},
		{name: "long token", in: strings.Repeat("x", 4096) + "\r", want: strings.Repeat("x", 4096)},
		{name: "overflow", in: strings.Repeat("x", maxTerminalLine+1) + "\r", wantErr: true},
		{name: "ctrl-u clears overflow", in: strings.Repeat("x", maxTerminalLine+1) + "\x15tok\r", want: "tok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readTerminalLine(strings.NewReader(tt.in))
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("readTerminalLine = %q, %v", got, err)
			}
		})
	}
}

func TestReadTerminalLineDrainsOverflowToEndOfLine(t *testing.T) {
	r := strings.NewReader(strings.Repeat("x", maxTerminalLine+10) + "\nnext")
	if _, err := readTerminalLine(r); err == nil {
		t.Fatal("overflow accepted")
	}
	// Everything up to the newline was consumed, so none of the token is
	// left for the shell; the bufio.Reader may also hold what came after it.
	if r.Len() > len("next") {
		t.Fatalf("%d bytes of the long line were left unread", r.Len()-len("next"))
	}
}

func TestReadStdinTokenPipeStopsOnCancel(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close(); w.Close() })
	cmd := &LoginCmd{stdin: r}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	done := make(chan error, 1)
	go func() {
		_, err := cmd.readStdinToken(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read from an open, silent pipe ignored cancellation")
	}
}

func TestShutdownTerminalWaitsForHideInProgress(t *testing.T) {
	resetHiddenTerminal(t)
	var hidden atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	go func() {
		_ = hideTerminal(func() (func() error, error) {
			close(entered)
			<-release
			hidden.Store(true)
			return func() error { hidden.Store(false); return nil }, nil
		})
	}()
	<-entered
	done := make(chan struct{})
	go func() { shutdownTerminal(); close(done) }()
	select {
	case <-done:
		t.Fatal("shutdownTerminal returned while echo was being turned off")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-done
	if hidden.Load() {
		t.Fatal("shutdownTerminal returned with echo still off")
	}
	if err := hideTerminal(func() (func() error, error) {
		t.Fatal("hide ran after shutdown")
		return nil, nil
	}); !errors.Is(err, errTerminalClosing) {
		t.Fatalf("hide after shutdown = %v", err)
	}
}

func TestRestoreTerminalWaitsForRestoreInProgress(t *testing.T) {
	resetHiddenTerminal(t)
	var hidden atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	if err := hideTerminal(func() (func() error, error) {
		hidden.Store(true)
		return func() error {
			calls.Add(1)
			close(entered)
			<-release
			hidden.Store(false)
			return nil
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	go restoreTerminal()
	<-entered
	var wg sync.WaitGroup
	wg.Add(1)
	returned := make(chan struct{})
	go func() { defer wg.Done(); shutdownTerminal(); close(returned) }()
	select {
	case <-returned:
		t.Fatal("second restore returned before the first finished")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if hidden.Load() || calls.Load() != 1 {
		t.Fatalf("echo off = %v, restore calls = %d", hidden.Load(), calls.Load())
	}
}
