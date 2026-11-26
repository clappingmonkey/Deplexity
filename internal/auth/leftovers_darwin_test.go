package auth

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// On macOS a match must also be an executable of the browser that was
// started, because kern.procargs2 cannot always tell arguments from the
// environment.
func TestSweepProfileProcessesIgnoresOtherExecutables(t *testing.T) {
	dir := filepath.Join(t.TempDir(), loginProfilePrefix+"exe")
	// "; :" keeps sh from exec-ing sleep, so its arguments stay.
	other := exec.Command("/bin/sh", "-c", "sleep 60; :", "--user-data-dir="+dir)
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	time.Sleep(100 * time.Millisecond)

	sweepProfileProcesses(dir, testBrowser(t), 300*time.Millisecond)
	assertProcessAlive(t, other.Process.Pid)
}
