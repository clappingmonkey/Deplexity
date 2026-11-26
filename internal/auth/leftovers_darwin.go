package auth

import (
	"bytes"
	"encoding/binary"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// killProfileProcesses kills this user's processes that m matches, and
// returns how many it found. It reads processes and their arguments from
// sysctl, so no subprocess such as ps can block it.
//
// kern.procargs2 has no explicit boundary between the executable path's
// padding and the arguments (see parseProcArgs), so a process is only killed
// when its executable also lies in the browser's bundle and argv[0] names
// it, which Chrome's children do. Safety relies on the 8-byte padding of the
// 64-bit Darwin ABI. Before any sweep it parses this process's own buffer as
// a smoke test and kills nothing unless that gives exactly os.Args; that
// catches a different layout on this system, but cannot prove every other
// process's buffer has the expected one.
//
// macOS has no pidfd. Right before the kill it checks again that the process
// with that ID started at the same time and still matches, so a process that
// reused the ID is not killed. Only the few microseconds between that check
// and the kill remain, and the ID would have to be freed and reused within
// them.
func killProfileProcesses(m profileMatcher, deadline time.Time) int {
	if m.root == "" || !procArgsLayoutOK() {
		return 0
	}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return 0
	}
	uid := uint32(os.Geteuid())
	var n int
	for i := range procs {
		if time.Now().After(deadline) {
			break
		}
		pid := int(procs[i].Proc.P_pid)
		start := procs[i].Proc.P_starttime
		if skipPID(pid) || procs[i].Eproc.Ucred.Uid != uid || !m.matchPID(pid) {
			continue
		}
		now, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err != nil || now.Proc.P_starttime != start || !m.matchPID(pid) {
			continue
		}
		if syscall.Kill(pid, syscall.SIGKILL) == nil {
			n++
		}
	}
	return n
}

func (m profileMatcher) matchPID(pid int) bool {
	exe, argv := procExecArgs(pid)
	return m.matchArgs(argv) && m.matchExec(exe, argv)
}

// procArgs returns the arguments of pid, or nil when it is gone or not
// readable.
func procArgs(pid int) []string {
	_, argv := procExecArgs(pid)
	return argv
}

// procExecArgs returns the executable path and arguments of pid from
// kern.procargs2, or nil arguments when they cannot be read unambiguously.
func procExecArgs(pid int) (string, []string) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return "", nil
	}
	return parseProcArgs(buf)
}

// procArgsLayoutOK is a smoke test that kern.procargs2 has the layout
// parseProcArgs expects: it parses this process's own buffer once. A failed
// read also counts as a mismatch, which disables sweeping for this process.
var procArgsLayoutOK = sync.OnceValue(func() bool {
	return slices.Equal(procArgs(os.Getpid()), os.Args)
})

// parseProcArgs parses a kern.procargs2 buffer: argc, the executable path
// NUL-padded to the pointer size (8 bytes on every macOS Go supports), then
// argc NUL-terminated arguments and the environment.
//
// An empty argument is indistinguishable from more padding, so leading empty
// arguments could shift the environment into the argument list. A buffer is
// therefore only accepted when the NULs after the path are exactly the
// padding, which also rules out any process with an empty argv[0] (never a
// browser process), and when it holds exactly argc complete arguments.
// Anything else returns nil arguments, so the process is left alone.
func parseProcArgs(buf []byte) (string, []string) {
	const align = 8
	if len(buf) < 4 {
		return "", nil
	}
	argc := int(binary.LittleEndian.Uint32(buf))
	rest := buf[4:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return "", nil
	}
	exe := string(rest[:end])
	padded := rest[end:]
	rest = bytes.TrimLeft(padded, "\x00")
	if argc < 1 || len(padded)-len(rest) != align-end%align {
		return exe, nil
	}
	argv := make([]string, 0, min(argc, 4096))
	for len(argv) < argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return exe, nil
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	return exe, argv
}
