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
	perplexityURL     = "https://www.perplexity.ai/"
	sessionCookieName = "__Secure-next-auth.session-token"
	csrfCookieName    = "next-auth.csrf-token"
	pollInterval      = 2 * time.Second
	loginTimeout      = 5 * time.Minute
)

// BrowserLogin opens a visible browser for the user to log in to Perplexity,
// waits for the session cookie, and returns the saved session.
// If Chrome/Chromium is not installed, it automatically downloads one via Rod.
func BrowserLogin() (*models.SavedSession, error) {
	fmt.Println("Launching browser for Perplexity login...")
	fmt.Println("Please log in to your Perplexity account in the browser window.")
	fmt.Printf("Waiting up to %s for authentication...\n\n", loginTimeout)

	launch := launcher.New().
		Headless(false).
		Set("disable-blink-features", "AutomationControlled")

	// Try to find an installed browser first, otherwise Rod will auto-download Chromium
	path, hasPath := launcher.LookPath()
	if hasPath {
		launch = launch.Bin(path)
	} else {
		fmt.Println("No Chrome/Chromium found — downloading Chromium automatically (~80MB, one-time)...")
	}

	u, err := launch.Launch()
	if err != nil {
		return nil, fmt.Errorf("could not launch browser: %w", err)
	}

	browser := rod.New().ControlURL(u).MustConnect()
	defer browser.MustClose()

	page := browser.MustPage(perplexityURL)
	defer page.MustClose()

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
		ExpiresAt:    time.Now().Add(7 * 24 * time.Hour),
	}

	return session
}