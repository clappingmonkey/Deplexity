package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// macOSBrowsers are application bundles checked under /Applications and
// ~/Applications.
var macOSBrowsers = []string{
	"Google Chrome.app/Contents/MacOS/Google Chrome",
	"Chromium.app/Contents/MacOS/Chromium",
	"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	"Brave Browser.app/Contents/MacOS/Brave Browser",
}

// windowsBrowsers are paths relative to %ProgramFiles%, %ProgramFiles(x86)%
// and %LocalAppData%.
var windowsBrowsers = []string{
	`Google\Chrome\Application\chrome.exe`,
	`Chromium\Application\chrome.exe`,
	`Microsoft\Edge\Application\msedge.exe`,
	`BraveSoftware\Brave-Browser\Application\brave.exe`,
}

// pathBrowsers are executable names looked up on $PATH, after the install
// locations. exec.LookPath adds .exe on Windows.
var pathBrowsers = []string{
	"chrome",
	"msedge",
	"google-chrome",
	"google-chrome-stable",
	"chromium",
	"chromium-browser",
	"microsoft-edge",
	"microsoft-edge-stable",
	"brave-browser",
	"brave",
}

// findBrowser returns the browser executable for BrowserLogin: the explicit
// path, then the environment override, then the first well-known browser
// installed for goos. exists and lookPath are injected for tests.
func findBrowser(explicit, env, goos string, exists func(string) bool, lookPath func(string) (string, error)) (string, error) {
	for _, override := range []string{explicit, env} {
		if override == "" {
			continue
		}
		// A bare name is only looked up on $PATH, never in the current
		// directory.
		if strings.ContainsAny(override, `/\`) {
			if exists(override) {
				return override, nil
			}
		} else if p, err := lookPath(override); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("browser %q not found; pass the full path with --browser (or set %s), or use `deplexity login --cookie <TOKEN>`", override, BrowserEnv)
	}

	var candidates []string
	switch goos {
	case "darwin":
		home, _ := os.UserHomeDir()
		for _, app := range macOSBrowsers {
			candidates = append(candidates, filepath.Join("/Applications", app))
			if home != "" {
				candidates = append(candidates, filepath.Join(home, "Applications", app))
			}
		}
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if dir := os.Getenv(env); dir != "" {
				for _, rel := range windowsBrowsers {
					candidates = append(candidates, filepath.Join(dir, rel))
				}
			}
		}
	}
	for _, c := range candidates {
		if exists(c) {
			return c, nil
		}
	}
	for _, name := range pathBrowsers {
		if p, err := lookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w; install one, pass --browser <path> (or set %s), or use `deplexity login --cookie <TOKEN>`", errNoBrowser, BrowserEnv)
}

func browserFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
