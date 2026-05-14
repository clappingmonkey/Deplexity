package auth

import (
	"fmt"
	"strings"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

const (
	perplexityURL    = "https://www.perplexity.ai/"
	sessionCookieName = "__Secure-next-auth.session-token"
	csrfCookieName    = "next-auth.csrf-token"
	pollInterval      = 2 * time.Second
	loginTimeout      = 5 * time.Minute
)

// BrowserLogin opens a visible browser for the user to log in to Perplexity,
// waits for the session cookie, and returns the saved session.
func BrowserLogin() (*models.SavedSession, error) {
	fmt.Println("Launching browser for Perplexity login...")
	fmt.Println("Please log in to your Perplexity account in the browser window.")
	fmt.Printf("Waiting up to %s for authentication...\n\n", loginTimeout)

	// Find browser path
	path, hasPath := launcher.LookPath()
	if !hasPath {
		return nil, fmt.Errorf("could not find Chrome/Chromium browser — please install Chrome")
	}

	// Launch a visible browser (not headless)
	u := launcher.New().
		Bin(path).
		Headless(false).
		Set("disable-blink-features", "AutomationControlled").
		MustLaunch()

	browser := rod.New().ControlURL(u).MustConnect()
	defer browser.MustClose()

	// Navigate to Perplexity
	page := browser.MustPage(perplexityURL)
	defer page.MustClose()

	// Wait for the user to complete login by polling for the session cookie.
	deadline := time.Now().Add(loginTimeout)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("login timed out after %s — please try again", loginTimeout)
		}

		cookies, err := browser.GetCookies()
		if err != nil {
			time.Sleep(pollInterval)
			continue
		}

		session := findSessionFromCookies(cookies)
		if session != nil {
			fmt.Println("\nAuthentication successful!")
			return session, nil
		}

		time.Sleep(pollInterval)
	}
}

// findSessionFromCookies checks whether the required session cookie is present.
func findSessionFromCookies(cookies []*proto.NetworkCookie) *models.SavedSession {
	var sessionToken, csrfToken string
	var allCookies []models.Cookie

	for _, c := range cookies {
		if !strings.Contains(c.Domain, "perplexity.ai") {
			continue
		}

		allCookies = append(allCookies, models.Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Expires:  float64(c.Expires),
			Secure:   c.Secure,
			HTTPOnly: c.HTTPOnly,
		})

		switch c.Name {
		case sessionCookieName:
			sessionToken = c.Value
		case csrfCookieName:
			csrfToken = c.Value
		}
	}

	if sessionToken == "" {
		return nil
	}

	session := &models.SavedSession{
		SessionToken: sessionToken,
		CSRFToken:    csrfToken,
		Cookies:      allCookies,
		ExpiresAt:    time.Now().Add(7 * 24 * time.Hour), // ~7 day expiry
	}

	return session
}
