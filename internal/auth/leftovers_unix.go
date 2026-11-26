//go:build darwin || linux

package auth

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// sweepProfileProcesses kills the processes that still have the login
// profile dir as an argument, and stops once it has found none for a few
// passes in a row or limit has passed. browser is the executable the login
// started.
//
// Chrome passes --user-data-dir to its renderer, GPU and utility children.
// Children that call setsid leave the helper's process group, can outlive the
// browser, and write into the profile again after it was removed. The
// crashpad handler gets no --user-data-dir, so this does not find it; it
// exits on its own once the browser it watches is gone. chromedp only sweeps for them when it created the profile itself;
// Deplexity owns its profile, so it does the same here. The profile path is a
// fresh MkdirTemp name, so it identifies only processes of this login.
//
// Arguments are compared exactly, from the kernel's argument list, never
// from space-joined text, and argv[0] never counts. Both the given and the
// symlink-resolved spelling of dir match. No subprocess is run. A process
// that cannot be identified safely is left alone.
func sweepProfileProcesses(dir, browser string, limit time.Duration) {
	const (
		quietPasses = 3
		pause       = 10 * time.Millisecond
	)
	m := profileMatcher{wants: profileArgs(dir), root: browserRoot(browser)}
	deadline := time.Now().Add(limit)
	for quiet := 0; quiet < quietPasses && time.Now().Before(deadline); time.Sleep(pause) {
		if killProfileProcesses(m, deadline) == 0 {
			quiet++
		} else {
			quiet = 0
		}
	}
}

// profileMatcher decides whether a process belongs to the login profile.
type profileMatcher struct {
	// wants are the --user-data-dir arguments that name the profile.
	wants []string
	// root is the browser's install directory: its .app bundle on macOS,
	// otherwise the executable's directory.
	root string
}

// matchArgs reports whether argv, after argv[0], has one of the wanted
// arguments as a whole argument.
func (m profileMatcher) matchArgs(argv []string) bool {
	if len(argv) < 2 {
		return false
	}
	return slices.ContainsFunc(argv[1:], func(a string) bool { return slices.Contains(m.wants, a) })
}

// matchExec reports whether exe, the absolute executable path, is inside the
// browser's install directory and argv[0] names it.
func (m profileMatcher) matchExec(exe string, argv []string) bool {
	if m.root == "" || !filepath.IsAbs(exe) || len(argv) == 0 || filepath.Base(argv[0]) != filepath.Base(exe) {
		return false
	}
	return strings.HasPrefix(resolvePath(exe), m.root+string(filepath.Separator))
}

// resolvePath resolves the symlinks in path, or in its parent when path
// itself does not exist.
func resolvePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return canonicalPath(path)
}

// profileArgs returns the --user-data-dir arguments that name dir.
func profileArgs(dir string) []string {
	wants := []string{"--user-data-dir=" + dir}
	if c := canonicalPath(dir); c != dir {
		wants = append(wants, "--user-data-dir="+c)
	}
	return wants
}

// browserRoot returns the directory a browser and all its helper
// executables live in: the outermost .app bundle around browser on macOS,
// otherwise its directory. It returns "" when browser is not absolute.
func browserRoot(browser string) string {
	if !filepath.IsAbs(browser) {
		return ""
	}
	browser = resolvePath(browser)
	root := filepath.Dir(browser)
	for dir := root; ; {
		if strings.HasSuffix(dir, ".app") {
			root = dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return root
		}
		dir = parent
	}
}

// skipPID reports pids the sweep must never signal.
func skipPID(pid int) bool {
	return pid <= 1 || pid == os.Getpid()
}
