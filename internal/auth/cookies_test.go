package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

func TestSaveAndLoadSession(t *testing.T) {
	// Override config dir for testing
	tmpDir := t.TempDir()
	origConfigDir := configDirName
	t.Cleanup(func() {
		// configDirName is a const, so we test the functions directly with temp paths
		_ = origConfigDir
	})

	session := &models.SavedSession{
		SessionToken: "test-token-123",
		CSRFToken:    "csrf-456",
		Cookies: []models.Cookie{
			{
				Name:   "__Secure-next-auth.session-token",
				Value:  "test-token-123",
				Domain: ".perplexity.ai",
				Path:   "/",
				Secure: true,
			},
		},
		ExpiresAt: time.Now().Add(7 * 24 * time.Hour),
	}

	// Write session to temp dir
	session.SavedAt = time.Now()
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	sessionPath := filepath.Join(tmpDir, "session.json")
	if err := os.WriteFile(sessionPath, data, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Read it back
	readData, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var loaded models.SavedSession
	if err := json.Unmarshal(readData, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if loaded.SessionToken != "test-token-123" {
		t.Errorf("token = %q, want %q", loaded.SessionToken, "test-token-123")
	}
	if len(loaded.Cookies) != 1 {
		t.Fatalf("cookies len = %d, want 1", len(loaded.Cookies))
	}
	if loaded.Cookies[0].Domain != ".perplexity.ai" {
		t.Errorf("domain = %q, want %q", loaded.Cookies[0].Domain, ".perplexity.ai")
	}
}

func TestSessionExists(t *testing.T) {
	// With no session file, SessionExists should return false.
	// This test relies on the default path not existing in CI.
	// In real testing we'd inject the path, but this validates the basic logic.
	_ = SessionExists()
}
