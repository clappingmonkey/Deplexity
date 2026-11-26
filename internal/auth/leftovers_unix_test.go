//go:build darwin || linux

package auth

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// testBrowser is the stand-in browser executable: the test binary, which
// sleeps in helperModeSleep.
func testBrowser(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// startProfileSleeper starts a detached stand-in browser child whose command
// line names dir as its profile, and reaps it when it dies.
func startProfileSleeper(t *testing.T, dir string) int {
	t.Helper()
	cmd := exec.Command(testBrowser(t), "--user-data-dir="+dir)
	return startSleeper(t, cmd)
}

// startSleeper starts cmd detached in helperModeSleep and waits until the
// kernel reports its own arguments, not the ones forked from the test.
func startSleeper(t *testing.T, cmd *exec.Cmd) int {
	t.Helper()
	cmd.Env = append(cmd.Env, os.Environ()...)
	cmd.Env = append(cmd.Env, helperModeEnv+"="+helperModeSleep)
	detach(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		got := procArgs(cmd.Process.Pid)
		// Before exec the child still shows the test's arguments. Arguments
		// with an empty argv[0] read back as nil on macOS once it has exec'd.
		if slices.Equal(got, cmd.Args) || (cmd.Args[0] == "" && runtime.GOOS == "darwin" && got == nil) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sleeper %d never showed %q, has %q", cmd.Process.Pid, cmd.Args, got)
		}
	}
	return cmd.Process.Pid
}

func assertProcessAlive(t *testing.T, pid int) {
	t.Helper()
	// A killed child of the test stays a zombie until it is reaped, and
	// kill(pid, 0) still succeeds for it.
	if err := syscall.Kill(pid, 0); err != nil || isZombie(pid) {
		t.Errorf("process %d for another profile was killed: %v", pid, err)
	}
}

func TestProcArgsReadsExactArguments(t *testing.T) {
	got := procArgs(os.Getpid())
	if !slices.Equal(got, os.Args) {
		t.Errorf("procArgs(self) = %q, want %q", got, os.Args)
	}
	if procArgs(-1) != nil {
		t.Error("procArgs of a missing process should be nil")
	}
}

func TestSweepProfileProcessesKillsOnlyThatProfile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), loginProfilePrefix+"sweep")
	match := startProfileSleeper(t, dir)
	suffixed := startProfileSleeper(t, dir+"-other")
	// ps joins arguments with spaces, so "<dir> other" once looked like dir.
	spaced := startProfileSleeper(t, dir+" other")

	start := time.Now()
	sweepProfileProcesses(dir, testBrowser(t), leftoverSweepTimeout)
	if took := time.Since(start); took > leftoverSweepTimeout+time.Second {
		t.Errorf("sweep took %s, want it bounded by %s", took, leftoverSweepTimeout)
	}
	assertProcessGone(t, match)
	assertProcessAlive(t, suffixed)
	assertProcessAlive(t, spaced)
}

// Only real arguments count: not argv[0], and not the environment, which a
// parser could reach on macOS when argv[0] is empty.
func TestSweepProfileProcessesIgnoresArgv0AndEnvironment(t *testing.T) {
	dir := filepath.Join(t.TempDir(), loginProfilePrefix+"env")
	want := "--user-data-dir=" + dir

	asArgv0 := startSleeper(t, &exec.Cmd{Path: testBrowser(t), Args: []string{want}})
	inEnv := startSleeper(t, &exec.Cmd{Path: testBrowser(t), Args: []string{""}, Env: []string{want}})
	// An empty argv[0] followed by the executable's own name passes every
	// identity check, so only exact argument parsing keeps want out of argv.
	named := startSleeper(t, &exec.Cmd{Path: testBrowser(t), Args: []string{"", filepath.Base(testBrowser(t))}, Env: []string{want}})
	// Many empty arguments once made the macOS parser panic, which would end
	// the whole CLI from inside a cleanup.
	many := startSleeper(t, &exec.Cmd{Path: testBrowser(t), Args: append(make([]string, 5000), filepath.Base(testBrowser(t))), Env: []string{want}})

	sweepProfileProcesses(dir, testBrowser(t), 300*time.Millisecond)
	assertProcessAlive(t, asArgv0)
	assertProcessAlive(t, inEnv)
	assertProcessAlive(t, named)
	assertProcessAlive(t, many)
}

func TestProfileMatcherArgs(t *testing.T) {
	m := profileMatcher{wants: []string{"--user-data-dir=/tmp/p"}}
	for _, tc := range []struct {
		argv []string
		want bool
	}{
		{[]string{"chrome", "--user-data-dir=/tmp/p"}, true},
		{[]string{"chrome", "--type=gpu", "--user-data-dir=/tmp/p"}, true},
		{[]string{"--user-data-dir=/tmp/p"}, false},
		{[]string{"chrome", "--user-data-dir=/tmp/p other"}, false},
		{[]string{"chrome", "--user-data-dir=/tmp/p2"}, false},
		{nil, false},
	} {
		if got := m.matchArgs(tc.argv); got != tc.want {
			t.Errorf("matchArgs(%q) = %v, want %v", tc.argv, got, tc.want)
		}
	}
}

func TestProfileMatcherExec(t *testing.T) {
	m := profileMatcher{root: "/Applications/Brave Browser.app"}
	helper := "/Applications/Brave Browser.app/Contents/Frameworks/Helpers/Brave Browser Helper.app/Contents/MacOS/Brave Browser Helper"
	for _, tc := range []struct {
		exe  string
		argv []string
		want bool
	}{
		{helper, []string{helper, "--type=gpu"}, true},
		{helper, []string{"Brave Browser Helper"}, true},
		{helper, []string{"", "--type=gpu"}, false},
		{helper, []string{"--user-data-dir=/tmp/p"}, false},
		{"/Applications/Brave Browser.app2/x", []string{"x"}, false},
		{"/bin/sleep", []string{"sleep"}, false},
		{"Brave Browser", []string{"Brave Browser"}, false},
	} {
		if got := m.matchExec(tc.exe, tc.argv); got != tc.want {
			t.Errorf("matchExec(%q, %q) = %v, want %v", tc.exe, tc.argv, got, tc.want)
		}
	}
	if (profileMatcher{}).matchExec(helper, []string{helper}) {
		t.Error("matchExec without a browser root should match nothing")
	}
}

func TestBrowserRoot(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ browser, want string }{
		{filepath.Join(dir, "Outer.app/Contents/Frameworks/Inner.app/Contents/MacOS/Inner"), filepath.Join(resolved, "Outer.app")},
		{filepath.Join(dir, "opt/chrome/chrome"), filepath.Join(resolved, "opt/chrome")},
		{"chrome", ""},
	} {
		if strings.HasPrefix(tc.browser, dir) {
			if err := os.MkdirAll(filepath.Dir(tc.browser), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		if got := browserRoot(tc.browser); got != tc.want {
			t.Errorf("browserRoot(%q) = %q, want %q", tc.browser, got, tc.want)
		}
	}
}

// A browser started with one spelling of the temp dir must be found when the
// sweep is given the other one (macOS /var is a symlink to /private/var).
func TestSweepProfileProcessesMatchesSymlinkedSpelling(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}

	viaLink := startProfileSleeper(t, filepath.Join(link, loginProfilePrefix+"a"))
	sweepProfileProcesses(filepath.Join(link, loginProfilePrefix+"a"), testBrowser(t), leftoverSweepTimeout)
	assertProcessGone(t, viaLink)

	// The usual case: the browser got the resolved path, cleanup has the
	// resolved path too; the link spelling is matched after the profile is
	// already gone.
	resolved := startProfileSleeper(t, filepath.Join(real, loginProfilePrefix+"b"))
	sweepProfileProcesses(filepath.Join(link, loginProfilePrefix+"b"), testBrowser(t), leftoverSweepTimeout)
	assertProcessGone(t, resolved)
}

func TestCanonicalPathWorksAfterRemoval(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolvedParent, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(link, loginProfilePrefix+"removed")
	if got, want := canonicalPath(gone), filepath.Join(resolvedParent, loginProfilePrefix+"removed"); got != want {
		t.Errorf("canonicalPath(%q) = %q, want %q", gone, got, want)
	}
}

// superviseLogin hands the helper the resolved profile path, so the helper's
// check must accept the resolved spelling of the temp dir too.
func TestIsLoginProfileDirAcceptsResolvedTempDir(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "tmp-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", link)

	for dir, want := range map[string]bool{
		filepath.Join(link, loginProfilePrefix+"x"):        true,
		filepath.Join(resolved, loginProfilePrefix+"x"):    true,
		filepath.Join(resolved, "other-x"):                 false,
		filepath.Join(t.TempDir(), loginProfilePrefix+"x"): false,
	} {
		if got := isLoginProfileDir(dir); got != want {
			t.Errorf("isLoginProfileDir(%q) = %v, want %v", dir, got, want)
		}
	}
}
