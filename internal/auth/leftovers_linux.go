package auth

import (
	"bytes"
	"os"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// killProfileProcesses kills the processes that m matches, and returns how
// many it found. /proc/<pid>/cmdline separates arguments exactly, so the
// arguments alone identify the profile's processes.
//
// It opens each match as a pidfd, reads its arguments again and signals
// through the pidfd, so the kill cannot reach a process that reused the ID
// after the check. Without a pidfd (Linux before 5.3, or a seccomp policy
// that denies it) it kills nothing rather than signal a bare ID.
func killProfileProcesses(m profileMatcher, deadline time.Time) int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	var n int
	for _, entry := range entries {
		if time.Now().After(deadline) {
			break
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || skipPID(pid) || !m.matchArgs(procArgs(pid)) {
			continue
		}
		fd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			continue
		}
		if m.matchArgs(procArgs(pid)) && unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0) == nil {
			n++
		}
		_ = unix.Close(fd)
	}
	return n
}

// procArgs returns the arguments of pid, or nil when it is gone.
func procArgs(pid int) []string {
	cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(cmdline) == 0 {
		return nil
	}
	var argv []string
	for arg := range bytes.SplitSeq(bytes.TrimSuffix(cmdline, []byte{0}), []byte{0}) {
		argv = append(argv, string(arg))
	}
	return argv
}
