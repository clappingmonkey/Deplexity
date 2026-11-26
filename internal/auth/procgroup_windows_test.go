package auth

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// assertProcessGone waits for pid to exit. On Windows FindProcess opens a
// handle, and Wait works for any process, not only children.
func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	p, err := os.FindProcess(pid)
	if err != nil {
		return // already gone
	}
	defer p.Release()
	exited := make(chan struct{})
	go func() {
		_, _ = p.Wait()
		close(exited)
	}()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		_ = p.Kill()
		t.Fatalf("fake browser %d is still running", pid)
	}
}

// detach does nothing: Windows has no sessions to escape the job with.
func detach(*exec.Cmd) {}
