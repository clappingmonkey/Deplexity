package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

const (
	configDirName  = "deplexity"
	sessionFile    = "session.json"
	filePermission = 0600
	dirPermission  = 0700
)

// ConfigDir returns the path to the deplexity config directory.
func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", configDirName), nil
}

// SessionPath returns the full path to the session file.
func SessionPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionFile), nil
}

// SaveSession persists the session to disk, readable only by the current
// user. The file is replaced atomically, so an interrupted save never leaves
// a truncated session, and a session file with broader permissions (or a
// symlink in its place) is replaced rather than written through.
func SaveSession(session *models.SavedSession) error {
	dir, err := ConfigDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, dirPermission); err != nil {
		return fmt.Errorf("could not create config directory: %w", err)
	}
	if err := restrictDir(dir); err != nil {
		return err
	}

	session.SavedAt = time.Now()

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal session: %w", err)
	}

	if err := writePrivateFile(filepath.Join(dir, sessionFile), data); err != nil {
		return fmt.Errorf("could not write session file: %w", err)
	}
	return nil
}

// restrictDir removes group and other access from the config directory. A
// symlinked directory is left alone, since its target is the user's choice.
func restrictDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("could not inspect config directory: %w", err)
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(dir, dirPermission); err != nil {
		return fmt.Errorf("could not restrict config directory permissions: %w", err)
	}
	return nil
}

// writePrivateFile writes data to a new 0600 temporary file next to path,
// syncs it and renames it over path.
func writePrivateFile(path string, data []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if err := tmp.Chmod(filePermission); err != nil && runtime.GOOS != "windows" {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// LoadSession reads the saved session from disk. It warns on stderr when the
// file can be read by other users.
func LoadSession() (*models.SavedSession, error) {
	path, err := SessionPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no saved session found — run 'deplexity login' first")
		}
		return nil, fmt.Errorf("could not read session file: %w", err)
	}

	var session models.SavedSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("could not parse session file: %w", err)
	}

	if warning := permissionWarning(path); warning != "" {
		fmt.Fprintln(os.Stderr, warning)
	}
	return &session, nil
}

// permissionWarning describes a session file or config directory other users
// can access or swap out, or returns "". A writable directory lets another user
// replace session.json, and a symlinked file is read through to its target.
func permissionWarning(path string) string {
	if runtime.GOOS == "windows" {
		return ""
	}
	var notes []string
	if info, err := os.Lstat(path); err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			notes = append(notes, path+" is a symlink")
		case info.Mode().Perm()&0o077 != 0:
			notes = append(notes, fmt.Sprintf("%s is mode %04o", path, info.Mode().Perm()))
		}
	}
	// The directory is checked through a symlink: SaveSession leaves a
	// symlinked directory's mode alone, so only a warning can flag it.
	dir := filepath.Dir(path)
	if info, err := os.Stat(dir); err == nil && info.IsDir() && info.Mode().Perm()&0o077 != 0 {
		notes = append(notes, fmt.Sprintf("%s is mode %04o", dir, info.Mode().Perm()))
	}
	if len(notes) == 0 {
		return ""
	}
	return fmt.Sprintf("Warning: other users may read or replace your session (%s). Run 'chmod 700 %s' and 'deplexity login' again.",
		strings.Join(notes, "; "), dir)
}

// DeleteSession removes the saved session file.
func DeleteSession() error {
	path, err := SessionPath()
	if err != nil {
		return err
	}

	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("could not delete session file: %w", err)
	}
	return nil
}

// SessionExists checks whether a session file exists on disk.
func SessionExists() bool {
	path, err := SessionPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}
