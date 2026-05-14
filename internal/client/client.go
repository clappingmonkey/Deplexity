package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

const (
	baseURL = "https://www.perplexity.ai"

	// UserAgent is the Chrome user-agent string sent with all requests.
	UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

	// DefaultDelay is the pause between consecutive API requests.
	DefaultDelay = 500 * time.Millisecond
)

// ErrNotAuthenticated indicates the session is invalid or expired.
var ErrNotAuthenticated = errors.New("authentication failed — run 'deplexity login' to refresh your session")

// Client is an authenticated HTTP client for the Perplexity internal API.
type Client struct {
	http    *http.Client
	baseURL string
	delay   time.Duration
	lastReq time.Time
	cookies []*http.Cookie
	verbose bool
}

// New creates a new authenticated client from a saved session.
func New(session *models.SavedSession) (*Client, error) {
	var cookies []*http.Cookie
	for _, c := range session.Cookies {
		cookies = append(cookies, &http.Cookie{
			Name:     c.Name,
			Value:    c.Value,
			Domain:   c.Domain,
			Path:     c.Path,
			Secure:   c.Secure,
			HttpOnly: c.HTTPOnly,
		})
	}

	return &Client{
		http: &http.Client{
			Timeout:   30 * time.Second,
			Transport: newChromeTransport(),
		},
		baseURL: baseURL,
		delay:   DefaultDelay,
		cookies: cookies,
	}, nil
}

// SetVerbose enables debug output.
func (c *Client) SetVerbose(v bool) {
	c.verbose = v
}

// SetDelay configures the minimum time between consecutive requests.
func (c *Client) SetDelay(d time.Duration) {
	c.delay = d
}

// Get performs an authenticated GET request to the given API path and
// decodes the JSON response into dest.
func (c *Client) Get(ctx context.Context, path string, dest interface{}) error {
	c.rateLimit()

	fullURL := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return fmt.Errorf("could not create request for %s: %w", path, err)
	}

	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", path, err)
	}
	defer resp.Body.Close()

	if c.verbose {
		log.Printf("[DEBUG] %s %s → %d", req.Method, req.URL.String(), resp.StatusCode)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w (HTTP %d)", ErrNotAuthenticated, resp.StatusCode)
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected response from %s: HTTP %d — %s", path, resp.StatusCode, string(body))
	}

	if dest != nil {
		if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
			return fmt.Errorf("could not decode response from %s: %w", path, err)
		}
	}

	return nil
}

// GetRaw performs an authenticated GET request and returns the raw response body.
func (c *Client) GetRaw(ctx context.Context, path string) ([]byte, error) {
	c.rateLimit()

	fullURL := c.baseURL + path

	req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("could not create request for %s: %w", path, err)
	}

	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", path, err)
	}
	defer resp.Body.Close()

	if c.verbose {
		log.Printf("[DEBUG] %s %s → %d", req.Method, req.URL.String(), resp.StatusCode)
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w (HTTP %d)", ErrNotAuthenticated, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read response from %s: %w", path, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected response from %s: HTTP %d — %s", path, resp.StatusCode, string(body))
	}

	return body, nil
}

// setHeaders adds the required headers and cookies to mimic a real browser request.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Referer", "https://www.perplexity.ai/")
	req.Header.Set("Origin", "https://www.perplexity.ai")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("x-app-apiclient", "default")
	req.Header.Set("x-app-apiversion", "2.18")

	// Add cookies directly to avoid net/http cookie value validation warnings.
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}

	if c.verbose {
		log.Printf("[DEBUG] %s %s", req.Method, req.URL.String())
	}
}

// rateLimit enforces the configured delay between requests.
func (c *Client) rateLimit() {
	if c.delay > 0 && !c.lastReq.IsZero() {
		elapsed := time.Since(c.lastReq)
		if elapsed < c.delay {
			time.Sleep(c.delay - elapsed)
		}
	}
	c.lastReq = time.Now()
}
