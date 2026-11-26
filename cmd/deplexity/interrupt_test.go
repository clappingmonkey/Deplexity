package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// blockingWriter never returns from Write, like a full stderr pipe.
type blockingWriter struct{}

func (blockingWriter) Write([]byte) (int, error) { select {} }

// chanWriter forwards each write to a channel.
type chanWriter chan string

func (c chanWriter) Write(p []byte) (int, error) {
	c <- string(p)
	return len(p), nil
}

func TestHandleInterruptsForcesOnQueuedSecondSignal(t *testing.T) {
	sigCh := make(chan os.Signal, 2)
	sigCh <- os.Interrupt
	sigCh <- syscall.SIGTERM
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var forced os.Signal
	handleInterrupts(sigCh, make(chan struct{}), cancel, blockingWriter{}, func(s os.Signal) { forced = s })

	if ctx.Err() == nil {
		t.Error("context not canceled by the first signal")
	}
	if forced != syscall.SIGTERM {
		t.Errorf("forced = %v, want SIGTERM", forced)
	}
}

func TestHandleInterruptsBlockedWriterDelaysNothing(t *testing.T) {
	sigCh := make(chan os.Signal, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	forced := make(chan os.Signal, 1)
	go handleInterrupts(sigCh, make(chan struct{}), cancel, blockingWriter{}, func(s os.Signal) { forced <- s })

	sigCh <- os.Interrupt
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("cancel waited on the blocked writer")
	}
	sigCh <- os.Interrupt
	select {
	case <-forced:
	case <-time.After(5 * time.Second):
		t.Fatal("second signal did not force quit while the writer was blocked")
	}
}

func TestHandleInterruptsSingleSignalPrintsMessage(t *testing.T) {
	sigCh := make(chan os.Signal, 2)
	sigCh <- os.Interrupt
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	out := make(chanWriter, 1)

	returned := make(chan struct{})
	go func() {
		handleInterrupts(sigCh, done, cancel, out, func(os.Signal) { t.Error("unexpected force") })
		close(returned)
	}()

	select {
	case msg := <-out:
		if !strings.Contains(msg, "finishing current operation") {
			t.Errorf("output = %q, want the interrupt message", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no interrupt message")
	}
	if ctx.Err() == nil {
		t.Error("context not canceled")
	}
	close(done)
	<-returned
}

func TestHandleInterruptsReturnsOnNormalExit(t *testing.T) {
	done := make(chan struct{})
	close(done)
	handleInterrupts(make(chan os.Signal), done, func() { t.Error("unexpected cancel") }, blockingWriter{}, func(os.Signal) { t.Error("unexpected force") })
}

func TestHandleInterruptsStoppedWinsOverQueuedSignals(t *testing.T) {
	for range 200 {
		sigCh := make(chan os.Signal, 2)
		sigCh <- os.Interrupt
		sigCh <- syscall.SIGTERM
		done := make(chan struct{})
		close(done)
		handleInterrupts(sigCh, done, func() {}, blockingWriter{}, func(os.Signal) { t.Fatal("forced after stop") })
	}
}

func TestInterrupterConcurrentStop(t *testing.T) {
	for range 100 {
		canceled := 0
		var mu sync.Mutex
		in := newInterrupter(func() { mu.Lock(); canceled++; mu.Unlock() }, func(os.Signal) { t.Error("unexpected quit") })
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(in.stop) // a double close of done would panic here
		}
		wg.Wait()
		if !isClosed(in.done) || canceled != 8 {
			t.Fatalf("done closed=%t cancels=%d, want closed and 8", isClosed(in.done), canceled)
		}
	}
}

func TestInterrupterStopAndForceAreExclusive(t *testing.T) {
	for range 200 {
		var quits atomic.Int32
		in := newInterrupter(func() {}, func(os.Signal) { quits.Add(1) })
		var wg sync.WaitGroup
		wg.Go(in.stop)
		wg.Go(func() { in.force(os.Interrupt) })
		wg.Go(func() { in.force(syscall.SIGTERM) })
		wg.Wait()
		if q := quits.Load(); q > 1 {
			t.Fatalf("quit called %d times, want at most once", q)
		}
	}

	quit := false
	in := newInterrupter(func() {}, func(os.Signal) { quit = true })
	in.stop()
	in.force(os.Interrupt)
	if quit {
		t.Error("force quit after a normal stop")
	}
}

func TestInterruptContextStopIsIdempotent(t *testing.T) {
	ctx, stop := interruptContext(context.Background(), blockingWriter{})
	stop()
	stop()
	if ctx.Err() == nil {
		t.Error("stop did not cancel the context")
	}
}

func TestWriteBoundedGivesUpOnBlockedWriter(t *testing.T) {
	start := time.Now()
	writeBounded(blockingWriter{}, "warning\n", 50*time.Millisecond)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("writeBounded blocked for %s", elapsed)
	}
}

func TestWriteBoundedWrites(t *testing.T) {
	var buf bytes.Buffer
	writeBounded(&buf, "warning\n", time.Second)
	if buf.String() != "warning\n" {
		t.Errorf("wrote %q", buf.String())
	}
}
