package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

// testHome points the config directory at a fresh temporary home.
func testHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func testSession(token string) *models.SavedSession {
	return &models.SavedSession{
		SessionToken: token,
		Cookies:      []models.Cookie{{Name: sessionCookieName, Value: token, Domain: ".perplexity.ai", Path: "/", Secure: true}},
		ExpiresAt:    time.Now().Add(time.Hour),
	}
}

func TestSaveAndLoadSession(t *testing.T) {
	testHome(t)
	if SessionExists() {
		t.Fatal("session exists before saving")
	}
	if err := SaveSession(testSession("tok-1")); err != nil {
		t.Fatal(err)
	}
	if !SessionExists() {
		t.Fatal("session missing after saving")
	}
	loaded, err := LoadSession()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SessionToken != "tok-1" || len(loaded.Cookies) != 1 || loaded.SavedAt.IsZero() {
		t.Fatalf("loaded = %+v", loaded)
	}

	if err := SaveSession(testSession("tok-2")); err != nil {
		t.Fatal(err)
	}
	if loaded, _ := LoadSession(); loaded.SessionToken != "tok-2" {
		t.Fatalf("token after resave = %q", loaded.SessionToken)
	}
	dir, _ := ConfigDir()
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".session.json.tmp-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files left: %v", leftovers)
	}

	if err := DeleteSession(); err != nil || SessionExists() {
		t.Fatalf("DeleteSession: %v, exists %t", err, SessionExists())
	}
	if err := DeleteSession(); err != nil {
		t.Fatalf("second DeleteSession: %v", err)
	}
	if _, err := LoadSession(); err == nil || !strings.Contains(err.Error(), "deplexity login") {
		t.Fatalf("LoadSession after delete: %v", err)
	}
}

func TestPermissionWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions")
	}
	setup := func(t *testing.T, dirMode, fileMode os.FileMode, symlink bool) string {
		dir := filepath.Join(t.TempDir(), "deplexity")
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "session.json")
		target := path
		if symlink {
			target = filepath.Join(t.TempDir(), "elsewhere.json")
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(target, []byte("{}"), fileMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, fileMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, dirMode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	t.Run("symlinked loose dir", func(t *testing.T) {
		target := t.TempDir()
		if err := os.WriteFile(filepath.Join(target, "session.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0777); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(t.TempDir(), "deplexity")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if got := permissionWarning(filepath.Join(link, "session.json")); !strings.Contains(got, "deplexity is mode 0777") {
			t.Fatalf("permissionWarning = %q", got)
		}
	})
	for _, tt := range []struct {
		name              string
		dirMode, fileMode os.FileMode
		symlink           bool
		want              string
	}{
		{name: "private", dirMode: 0700, fileMode: 0600},
		{name: "loose file", dirMode: 0700, fileMode: 0644, want: "session.json is mode 0644"},
		{name: "loose dir", dirMode: 0755, fileMode: 0600, want: "deplexity is mode 0755"},
		{name: "symlinked file", dirMode: 0700, fileMode: 0600, symlink: true, want: "session.json is a symlink"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := permissionWarning(setup(t, tt.dirMode, tt.fileMode, tt.symlink))
			if tt.want == "" && got != "" || !strings.Contains(got, tt.want) {
				t.Fatalf("permissionWarning = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSaveSessionTightensPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	testHome(t)
	dir, _ := ConfigDir()
	path, _ := SessionPath()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if w := permissionWarning(path); !strings.Contains(w, "0644") {
		t.Fatalf("warning = %q", w)
	}

	if err := SaveSession(testSession("tok")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %04o, want 0700", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %04o, want 0600", fi.Mode().Perm())
	}
	if w := permissionWarning(path); w != "" {
		t.Errorf("warning after save = %q", w)
	}
}

func TestSaveSessionReplacesSymlinkInsteadOfFollowing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	testHome(t)
	dir, _ := ConfigDir()
	path, _ := SessionPath()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := SaveSession(testSession("tok")); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "keep" {
		t.Fatalf("symlink target was written: %q", data)
	}
	if fi, _ := os.Lstat(path); fi.Mode()&os.ModeSymlink != 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("session file mode = %v", fi.Mode())
	}
}
