package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// SaveSession persists the session to disk with restricted permissions.
func SaveSession(session *models.SavedSession) error {
	dir, err := ConfigDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(dir, dirPermission); err != nil {
		return fmt.Errorf("could not create config directory: %w", err)
	}

	session.SavedAt = time.Now()

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal session: %w", err)
	}

	path := filepath.Join(dir, sessionFile)
	if err := os.WriteFile(path, data, filePermission); err != nil {
		return fmt.Errorf("could not write session file: %w", err)
	}

	return nil
}

// LoadSession reads the saved session from disk.
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

	return &session, nil
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
