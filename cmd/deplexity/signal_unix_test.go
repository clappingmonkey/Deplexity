//go:build unix

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"
)

// signalHelperArg must be passed after "--" to run the helper, so an inherited
// environment variable alone cannot turn the test into a blocking process.
const signalHelperArg = "deplexity-signal-helper"

// TestInterruptProcess is the child side of TestSecondInterruptForceQuits.
// It cancels on the first signal and then blocks like an operation that
// ignores its context.
func TestInterruptProcess(t *testing.T) {
	if os.Getenv("DEPLEXITY_SIGNAL_TEST_PROCESS") != "1" || !slices.Contains(flag.Args(), signalHelperArg) {
		t.Skip("helper process")
	}
	ctx, stop := interruptContext(context.Background(), io.Discard)
	defer stop()
	fmt.Println("ready")
	<-ctx.Done()
	fmt.Println("canceled")
	select {}
}

// startInterruptHelper runs TestInterruptProcess in a subprocess and waits
// until it has installed its handler.
func startInterruptHelper(t *testing.T, ctx context.Context) (*exec.Cmd, func(string)) {
	t.Helper()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestInterruptProcess$", "--", signalHelperArg)
	cmd.Env = append(os.Environ(), "DEPLEXITY_SIGNAL_TEST_PROCESS=1")
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("helper stderr:\n%s", stderr)
		}
	})
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	expect := func(want string) {
		t.Helper()
		for lines.Scan() {
			if lines.Text() == want {
				return
			}
		}
		t.Fatalf("helper exited before printing %q: %v", want, lines.Err())
	}
	expect("ready")
	return cmd, expect
}

// waitForceQuit asserts the helper was force-quit by one of the given
// signals: either killed by it, or, where its inherited handling ignores it
// (as under Bazel's test runner), exited with status 128+n.
func waitForceQuit(t *testing.T, ctx context.Context, cmd *exec.Cmd, allowed ...syscall.Signal) {
	t.Helper()
	err := cmd.Wait()
	if ctx.Err() != nil {
		t.Fatal("helper did not exit after the second signal")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Wait = %v, want the helper force-quit", err)
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("helper exit = %v, want a wait status", exitErr)
	}
	for _, sig := range allowed {
		if (status.Signaled() && status.Signal() == sig) || (status.Exited() && status.ExitStatus() == 128+int(sig)) {
			return
		}
	}
	t.Fatalf("helper exit = %v, want force-quit by one of %v", exitErr, allowed)
}

func TestSecondInterruptForceQuits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd, expect := startInterruptHelper(t, ctx)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	expect("canceled")
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	waitForceQuit(t, ctx, cmd, syscall.SIGINT)
}

func TestSignalBurstForceQuits(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd, _ := startInterruptHelper(t, ctx)
	// Back to back, so the second usually arrives before the first is handled.
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitForceQuit(t, ctx, cmd, syscall.SIGINT, syscall.SIGTERM)
}

func TestInterruptContextStopCancels(t *testing.T) {
	ctx, stop := interruptContext(context.Background(), io.Discard)
	stop()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stop did not cancel the context")
	}
}
