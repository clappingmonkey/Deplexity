//go:build windows

package auth

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x2000
	processSetQuota                   = 0x0100
	processTerminate                  = 0x0001
)

// jobObjectExtendedLimitInformation mirrors JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type jobObjectExtendedLimit struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
	IoCounters              [6]uint64
	ProcessMemoryLimit      uintptr
	JobMemoryLimit          uintptr
	PeakProcessMemoryUsed   uintptr
	PeakJobMemoryUsed       uintptr
}

func prepareTree(*exec.Cmd) {}

// procTree is a started helper inside a job object. Every process the
// helper starts, including the browser, joins the job; terminating or
// closing the job kills them all, even if Deplexity itself dies.
type procTree struct {
	p   *os.Process
	job syscall.Handle
}

// newProcTree puts p in a new kill-on-close job. It runs before the helper
// receives its request, so the helper cannot have started a browser yet.
func newProcTree(p *os.Process) (*procTree, error) {
	job, _, err := procCreateJobObjectW.Call(0, 0)
	if job == 0 {
		return nil, fmt.Errorf("creating a job object: %w", err)
	}
	t := &procTree{p: p, job: syscall.Handle(job)}
	info := jobObjectExtendedLimit{LimitFlags: jobObjectLimitKillOnJobClose}
	if ok, _, err := procSetInformationJobObject.Call(job, jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); ok == 0 {
		t.release()
		return nil, fmt.Errorf("configuring the job object: %w", err)
	}
	h, err := syscall.OpenProcess(processSetQuota|processTerminate, false, uint32(p.Pid))
	if err != nil {
		t.release()
		return nil, fmt.Errorf("opening the helper process: %w", err)
	}
	defer syscall.CloseHandle(h)
	if ok, _, err := procAssignProcessToJobObject.Call(job, uintptr(h)); ok == 0 {
		t.release()
		return nil, fmt.Errorf("assigning the helper to the job object: %w", err)
	}
	return t, nil
}

func (t *procTree) kill() {
	_, _, _ = procTerminateJobObject.Call(uintptr(t.job), 1)
	_ = t.p.Kill()
}

// release closes the job, which kills anything still in it.
func (t *procTree) release() {
	if t.job != 0 {
		_ = syscall.CloseHandle(t.job)
		t.job = 0
	}
}
