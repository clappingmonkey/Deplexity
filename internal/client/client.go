package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

const (
	baseURL   = "https://www.perplexity.ai"
	userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

	// DefaultDelay is the pause between consecutive API requests.
	DefaultDelay = 500 * time.Millisecond
)

// Client is an authenticated HTTP client for the Perplexity internal API.
type Client struct {
	http      *http.Client
	baseURL   string
	delay     time.Duration
	lastReq   time.Time
}

// New creates a new authenticated client from a saved session.
func New(session *models.SavedSession) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("could not create cookie jar: %w", err)
	}

	perplexityURL, _ := url.Parse(baseURL)
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
	jar.SetCookies(perplexityURL, cookies)

	return &Client{
		http: &http.Client{
			Jar:     jar,
			Timeout: 30 * time.Second,
		},
		baseURL: baseURL,
		delay:   DefaultDelay,
	}, nil
}

// SetDelay configures the minimum time between consecutive requests.
func (c *Client) SetDelay(d time.Duration) {
	c.delay = d
}

// Get performs an authenticated GET request to the given API path and
// decodes the JSON response into dest.
func (c *Client) Get(path string, dest interface{}) error {
	c.rateLimit()

	fullURL := c.baseURL + path

	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return fmt.Errorf("could not create request for %s: %w", path, err)
	}

	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s failed: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("authentication failed (HTTP %d) — try 'deplexity login' to refresh your session", resp.StatusCode)
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
func (c *Client) GetRaw(path string) ([]byte, error) {
	c.rateLimit()

	fullURL := c.baseURL + path

	req, err := http.NewRequest("GET", fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("could not create request for %s: %w", path, err)
	}

	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request to %s failed: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("authentication failed (HTTP %d) — try 'deplexity login' to refresh your session", resp.StatusCode)
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

// setHeaders adds the required headers to mimic a real browser request.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Referer", "https://www.perplexity.ai/")
	req.Header.Set("Origin", "https://www.perplexity.ai")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
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
