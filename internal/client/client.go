package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/clappingmonkey/deplexity/internal/models"
)

const (
	baseURL = "https://www.perplexity.ai"

	// UserAgent is the Chrome user-agent string sent with all requests.
	UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

	// DefaultDelay is the pause between consecutive API requests.
	DefaultDelay = 1 * time.Second

	// maxDelay caps the adaptive delay increase after 429 responses.
	maxDelay = 10 * time.Second

	// adaptiveDecayRequests is how many consecutive successes before
	// the delay is reduced by half (back toward DefaultDelay).
	adaptiveDecayRequests = 20

	// maxNetworkRetries is the retry limit for transient network errors
	// (DNS, dial, TLS). Higher than HTTP retries since outages can last minutes.
	maxNetworkRetries = 15

	// networkRetryBase is the initial backoff for network errors.
	networkRetryBase = 5 * time.Second

	// networkRetryMax caps backoff between network retries.
	networkRetryMax = 2 * time.Minute
)

// ErrNotAuthenticated indicates the session is invalid or expired.
var ErrNotAuthenticated = errors.New("authentication failed — run 'deplexity login' to refresh your session")

// Client is an authenticated HTTP client for the Perplexity internal API.
type Client struct {
	http    *http.Client
	baseURL string
	delay            time.Duration
	lastReq          time.Time
	consecutiveOK    int // consecutive 200s since last 429
	cookies          []*http.Cookie
	verbose          bool
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
	fullURL := c.baseURL + path

	httpAttempt := 0
	netAttempt := 0

	for {
		if httpAttempt > maxRetries {
			return fmt.Errorf("request to %s failed after %d retries", path, maxRetries)
		}
		if netAttempt > maxNetworkRetries {
			return fmt.Errorf("request to %s failed after %d network retries", path, maxNetworkRetries)
		}

		c.rateLimit()

		req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
		if err != nil {
			return fmt.Errorf("could not create request for %s: %w", path, err)
		}

		c.setHeaders(req)

		resp, err := c.http.Do(req)
		if err != nil {
			// Retry transient network errors (DNS, dial, TLS handshake).
			if ctx.Err() != nil {
				return ctx.Err()
			}
			netAttempt++
			backoff := computeBackoff(netAttempt-1, networkRetryBase, networkRetryMax)
			if c.verbose {
				log.Printf("[DEBUG] network error, backing off %s (attempt %d/%d): %v", backoff, netAttempt, maxNetworkRetries, err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}

		// Reset network attempt counter on any successful connection.
		netAttempt = 0

		if c.verbose {
			log.Printf("[DEBUG] %s %s → %d", req.Method, req.URL.String(), resp.StatusCode)
		}

		if resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout {
			resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests {
				c.onRateLimited()
			}
			httpAttempt++
			backoff := computeBackoff(httpAttempt-1, baseBackoff, maxBackoff)
			if c.verbose {
				log.Printf("[DEBUG] retryable error %d, backing off %s (attempt %d/%d)", resp.StatusCode, backoff, httpAttempt, maxRetries)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}

		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w (HTTP %d)", ErrNotAuthenticated, resp.StatusCode)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("unexpected response from %s: HTTP %d — %s", path, resp.StatusCode, string(body))
		}

		c.onSuccess()
		// Reset HTTP attempt counter on success so the next call starts fresh.
		httpAttempt = 0

		if dest != nil {
			if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
				return fmt.Errorf("could not decode response from %s: %w", path, err)
			}
		}

		return nil
	}
}

// GetRaw performs an authenticated GET request and returns the raw response body.
func (c *Client) GetRaw(ctx context.Context, path string) ([]byte, error) {
	fullURL := c.baseURL + path

	httpAttempt := 0
	netAttempt := 0

	for {
		if httpAttempt > maxRetries {
			return nil, fmt.Errorf("request to %s failed after %d retries", path, maxRetries)
		}
		if netAttempt > maxNetworkRetries {
			return nil, fmt.Errorf("request to %s failed after %d network retries", path, maxNetworkRetries)
		}

		c.rateLimit()

		req, err := http.NewRequestWithContext(ctx, "GET", fullURL, nil)
		if err != nil {
			return nil, fmt.Errorf("could not create request for %s: %w", path, err)
		}

		c.setHeaders(req)

		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			netAttempt++
			backoff := computeBackoff(netAttempt-1, networkRetryBase, networkRetryMax)
			if c.verbose {
				log.Printf("[DEBUG] network error, backing off %s (attempt %d/%d): %v", backoff, netAttempt, maxNetworkRetries, err)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}

		netAttempt = 0

		if c.verbose {
			log.Printf("[DEBUG] %s %s → %d", req.Method, req.URL.String(), resp.StatusCode)
		}

		if resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout {
			resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests {
				c.onRateLimited()
			}
			httpAttempt++
			backoff := computeBackoff(httpAttempt-1, baseBackoff, maxBackoff)
			if c.verbose {
				log.Printf("[DEBUG] retryable error %d, backing off %s (attempt %d/%d)", resp.StatusCode, backoff, httpAttempt, maxRetries)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}

		defer resp.Body.Close()

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

		c.onSuccess()
		httpAttempt = 0

		return body, nil
	}
}

// Post performs an authenticated POST request with a JSON body and decodes the response into dest.
func (c *Client) Post(ctx context.Context, path string, body interface{}, dest interface{}) error {
	fullURL := c.baseURL + path

	httpAttempt := 0
	netAttempt := 0

	for {
		if httpAttempt > maxRetries {
			return fmt.Errorf("request to %s failed after %d retries", path, maxRetries)
		}
		if netAttempt > maxNetworkRetries {
			return fmt.Errorf("request to %s failed after %d network retries", path, maxNetworkRetries)
		}

		c.rateLimit()

		var bodyReader io.Reader
		if body != nil {
			bodyBytes, err := json.Marshal(body)
			if err != nil {
				return fmt.Errorf("could not marshal request body for %s: %w", path, err)
			}
			bodyReader = strings.NewReader(string(bodyBytes))
		}

		req, err := http.NewRequestWithContext(ctx, "POST", fullURL, bodyReader)
		if err != nil {
			return fmt.Errorf("could not create request for %s: %w", path, err)
		}

		c.setHeaders(req)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			netAttempt++
			backoff := computeBackoff(netAttempt-1, networkRetryBase, networkRetryMax)
			if c.verbose {
				log.Printf("[DEBUG] network error, backing off %s (attempt %d/%d): %v", backoff, netAttempt, maxNetworkRetries, err)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}

		netAttempt = 0

		if c.verbose {
			log.Printf("[DEBUG] %s %s → %d", req.Method, req.URL.String(), resp.StatusCode)
		}

		if resp.StatusCode == http.StatusTooManyRequests ||
			resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable ||
			resp.StatusCode == http.StatusGatewayTimeout {
			resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests {
				c.onRateLimited()
			}
			httpAttempt++
			backoff := computeBackoff(httpAttempt-1, baseBackoff, maxBackoff)
			if c.verbose {
				log.Printf("[DEBUG] retryable error %d, backing off %s (attempt %d/%d)", resp.StatusCode, backoff, httpAttempt, maxRetries)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				continue
			}
		}

		defer resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w (HTTP %d)", ErrNotAuthenticated, resp.StatusCode)
		}

		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("unexpected response from %s: HTTP %d — %s", path, resp.StatusCode, string(respBody))
		}

		c.onSuccess()
		httpAttempt = 0

		if dest != nil {
			if err := json.NewDecoder(resp.Body).Decode(dest); err != nil {
				return fmt.Errorf("could not decode response from %s: %w", path, err)
			}
		}

		return nil
	}
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

	// Add cookies directly as raw header to avoid net/http cookie value validation warnings.
	var cookieParts []string
	for _, cookie := range c.cookies {
		cookieParts = append(cookieParts, cookie.Name+"="+cookie.Value)
	}
	if len(cookieParts) > 0 {
		req.Header.Set("Cookie", strings.Join(cookieParts, "; "))
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

// onSuccess records a successful request and gradually lowers the
// adaptive delay back toward DefaultDelay after sustained success.
func (c *Client) onSuccess() {
	c.consecutiveOK++
	if c.consecutiveOK >= adaptiveDecayRequests && c.delay > DefaultDelay {
		c.delay = c.delay / 2
		if c.delay < DefaultDelay {
			c.delay = DefaultDelay
		}
		c.consecutiveOK = 0
		if c.verbose {
			log.Printf("[DEBUG] adaptive delay decreased to %s", c.delay)
		}
	}
}

// onRateLimited doubles the inter-request delay (up to maxDelay).
func (c *Client) onRateLimited() {
	c.consecutiveOK = 0
	newDelay := c.delay * 2
	if newDelay > maxDelay {
		newDelay = maxDelay
	}
	if newDelay != c.delay {
		c.delay = newDelay
		if c.verbose {
			log.Printf("[DEBUG] adaptive delay increased to %s", c.delay)
		}
	}
}
