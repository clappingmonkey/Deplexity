//go:build !unix && !windows

package auth

import (
	"os"
	"os/exec"
)

func prepareTree(*exec.Cmd) {}

// procTree is a started helper. Without process groups or jobs only the
// helper itself is killed; its browser exits when the debugging pipe closes.
type procTree struct {
	p *os.Process
}

func newProcTree(p *os.Process) (*procTree, error) {
	return &procTree{p: p}, nil
}

func (t *procTree) kill() { _ = t.p.Kill() }

func (t *procTree) release() {}
