//go:build unix

package auth

import (
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// assertProcessGone waits for pid to die. A dead process that was not reaped
// yet (a zombie, for example under a container init that reaps slowly)
// counts as gone.
func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) || isZombie(pid) {
			return
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("fake browser %d is still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// detach starts cmd in a new session, the way a browser child that calls
// setsid leaves the helper's process group.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
