//go:build unix

package auth

import (
	"os"
	"os/exec"
	"syscall"
)

// prepareTree makes cmd start in its own process group. The browser it
// launches inherits the group, so one kill reaches both. A terminal Ctrl+C
// goes to the CLI's foreground group, so the helper does not see it; the CLI
// stops the helper itself. Browser children that call setsid leave the
// group; they lose their debugging pipe when the helper dies and exit.
func prepareTree(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// procTree is a started helper and the process group it leads.
type procTree struct {
	p *os.Process
}

func newProcTree(p *os.Process) (*procTree, error) {
	return &procTree{p: p}, nil
}

// kill kills the helper's process group. Callers must not use it after the
// helper was reaped: only then could the group ID belong to someone else.
func (t *procTree) kill() {
	_ = syscall.Kill(-t.p.Pid, syscall.SIGKILL)
	_ = t.p.Kill()
}

func (t *procTree) release() {}
