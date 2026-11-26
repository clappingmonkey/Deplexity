//go:build !unix && !windows

package auth

import (
	"os/exec"
	"testing"
)

// assertProcessGone is not checked on these platforms; unix and Windows
// check it.
func assertProcessGone(*testing.T, int) {}

// detach does nothing without sessions.
func detach(*exec.Cmd) {}
