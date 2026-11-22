package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	utls "github.com/refraction-networking/utls"

	"github.com/clappingmonkey/deplexity/internal/models"
)

const (
	baseURL = "https://www.perplexity.ai"

	// UserAgent is the Chrome user-agent string sent with all requests.
	UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

	// DefaultDelay is the pause between consecutive API requests.
	DefaultDelay = 1 * time.Second

	// maxDelay caps the adaptive delay increase after 429 responses, unless
	// the configured minimum delay is already higher.
	maxDelay = 10 * time.Second

	// adaptiveDecayRequests is how many consecutive successes before
	// the delay is reduced by half (back toward the configured minimum).
	adaptiveDecayRequests = 20

	// maxNetworkRetries is the retry limit for transient network errors
	// (DNS, dial, TLS). Higher than HTTP retries since outages can last minutes.
	maxNetworkRetries = 15

	// networkRetryBase is the initial backoff for network errors.
	networkRetryBase = 5 * time.Second

	// networkRetryMax caps backoff between network retries.
	networkRetryMax = 2 * time.Minute

	// maxRedirects bounds redirect chains; every hop must stay on https.
	maxRedirects = 10

	// maxAPIResponseBody caps a single Perplexity API response. Thread detail
	// pages are the largest responses and are far below this.
	maxAPIResponseBody = 128 << 20 // 128 MB

	// maxRawURLBody caps how much data GetRawURL will read into memory. Skill
	// SKILL.md bodies are small (KBs); this guards against a misdirected or
	// erroring pre-signed URL returning an unexpectedly large payload.
	maxRawURLBody = 10 << 20 // 10 MB

	// maxErrorBody caps how much of a non-200 response is read for the error
	// message.
	maxErrorBody = 4 << 10 // 4 KB

	// maxDrainBody caps how much of an unwanted body is discarded to keep an
	// HTTP/1.1 connection reusable.
	maxDrainBody = 64 << 10 // 64 KB
)

// ErrNotAuthenticated indicates the session is invalid or expired.
var ErrNotAuthenticated = errors.New("authentication failed — run 'deplexity login' to refresh your session")

// errRedirectRejected marks a redirect that left https or exceeded
// maxRedirects. It is never retried.
var errRedirectRejected = errors.New("redirect rejected")

// Client is an authenticated HTTP client for the Perplexity internal API.
//
// A Client is not safe for concurrent use: request pacing and the adaptive
// delay are tracked without locking.
type Client struct {
	http          *http.Client
	baseURL       string
	delay         time.Duration // current inter-request delay (adaptive)
	minDelay      time.Duration // configured floor the adaptive delay decays to
	lastReq       time.Time
	waitDelay     func(context.Context, time.Duration) error
	retryWait     func(context.Context, time.Duration) error
	consecutiveOK int // consecutive 200s since last 429
	cookies       []*http.Cookie
	verbose       bool
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
			Timeout:       30 * time.Second,
			Transport:     newChromeTransport(),
			CheckRedirect: checkRedirect,
		},
		baseURL:  baseURL,
		delay:    DefaultDelay,
		minDelay: DefaultDelay,
		cookies:  cookies,
	}, nil
}

// Close releases idle pooled connections.
func (c *Client) Close() {
	c.http.CloseIdleConnections()
}

// SetVerbose enables debug output.
func (c *Client) SetVerbose(v bool) {
	c.verbose = v
}

// SetDelay configures the minimum time between consecutive requests. The
// adaptive delay may rise above it after 429 responses and decays back to it.
func (c *Client) SetDelay(d time.Duration) {
	c.delay = d
	c.minDelay = d
}

// Get performs an authenticated GET request to the given API path and
// decodes the JSON response into dest.
func (c *Client) Get(ctx context.Context, path string, dest any) error {
	resp, err := c.do(ctx, requestSpec{
		label:    path,
		adaptive: true,
		newRequest: func(ctx context.Context) (*http.Request, error) {
			return c.newAPIRequest(ctx, http.MethodGet, path, nil)
		},
	})
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)

	if err := checkAPIStatus(path, resp); err != nil {
		return err
	}
	c.onSuccess()
	return decodeJSON(path, resp.Body, dest, maxAPIResponseBody)
}

// Post performs an authenticated POST request with a JSON body and decodes the
// response into dest.
func (c *Client) Post(ctx context.Context, path string, body, dest any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("could not marshal request body for %s: %w", path, err)
		}
	}

	resp, err := c.do(ctx, requestSpec{
		label:    path,
		adaptive: true,
		newRequest: func(ctx context.Context) (*http.Request, error) {
			var r io.Reader
			if payload != nil {
				r = bytes.NewReader(payload)
			}
			req, err := c.newAPIRequest(ctx, http.MethodPost, path, r)
			if err == nil && payload != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			return req, err
		},
	})
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)

	if err := checkAPIStatus(path, resp); err != nil {
		return err
	}
	c.onSuccess()
	return decodeJSON(path, resp.Body, dest, maxAPIResponseBody)
}

// GetRawURL fetches an absolute URL and returns the raw response body.
//
// Unlike Get/Post, it does not prepend baseURL and does not attach the
// Perplexity session cookie, Origin, or API headers. It is intended for
// fetching pre-signed third-party URLs (e.g. S3-hosted skill bodies) that
// require no authentication and must not receive our session cookie. It reuses
// the same uTLS transport and the standard HTTP/network retry loops.
//
// A 401/403 is not treated as a Perplexity auth failure (ErrNotAuthenticated):
// the target is a third-party URL, so such a status typically means the
// pre-signed URL has expired. Errors and debug logs never include the query
// string, which carries the pre-signed credentials, and never echo the
// third-party response body.
func (c *Client) GetRawURL(ctx context.Context, rawURL string) ([]byte, error) {
	// The URL originates from the (authenticated) Perplexity API, but anything
	// that is not an absolute https URL with a host is refused: skill bodies
	// are always served from https pre-signed storage URLs. Redirects are held
	// to the same rule by checkRedirect. This is not full SSRF hardening (no
	// private-IP filtering), which is out of scope for a personal export CLI.
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("invalid skill body URL")
	}
	label := redactURL(u)
	if u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("refusing to fetch non-https or hostless URL %s", label)
	}

	resp, err := c.do(ctx, requestSpec{
		label: label,
		// S3 rate limits are independent of Perplexity's and must not perturb
		// its adaptive delay; pacing still applies so one client stays polite
		// across mixed traffic.
		adaptive: false,
		newRequest: func(ctx context.Context) (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("User-Agent", UserAgent)
			req.Header.Set("Accept", "*/*")
			return req, nil
		},
	})
	if err != nil {
		return nil, err
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected response from %s: HTTP %d", label, resp.StatusCode)
	}
	body, err := io.ReadAll(http.MaxBytesReader(nil, io.NopCloser(resp.Body), maxRawURLBody))
	if err != nil {
		return nil, bodyReadError(label, maxRawURLBody, err)
	}
	return body, nil
}

// requestSpec describes one logical request for do.
type requestSpec struct {
	// label identifies the request in errors and logs. It must not contain
	// credentials.
	label string
	// adaptive updates the shared adaptive delay on 429 responses.
	adaptive bool
	// newRequest builds a fresh request for each attempt.
	newRequest func(context.Context) (*http.Request, error)
}

// do sends a request with pacing, network-error retries and retries of
// retryable HTTP statuses (429/502/503/504). It returns the first response
// with any other status; the caller must close its body.
func (c *Client) do(ctx context.Context, spec requestSpec) (*http.Response, error) {
	httpAttempt := 0
	netAttempt := 0

	for {
		if err := c.rateLimit(ctx); err != nil {
			return nil, err
		}

		req, err := spec.newRequest(ctx)
		if err != nil {
			return nil, fmt.Errorf("could not create request for %s: %w", spec.label, err)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			err = redactURLError(err)
			if isPermanentTransportError(err) {
				return nil, fmt.Errorf("request to %s failed: %w", spec.label, err)
			}
			netAttempt++
			if netAttempt > maxNetworkRetries {
				return nil, fmt.Errorf("request to %s failed after %d network retries: %w", spec.label, maxNetworkRetries, err)
			}
			backoff := computeBackoff(netAttempt-1, networkRetryBase, networkRetryMax)
			c.debugf("network error on %s, backing off %s (attempt %d/%d): %v", spec.label, backoff, netAttempt, maxNetworkRetries, err)
			if err := c.waitForRetry(ctx, backoff); err != nil {
				return nil, err
			}
			continue
		}

		// Reset the network attempt counter on any successful connection.
		netAttempt = 0
		c.debugf("%s %s → %d", req.Method, spec.label, resp.StatusCode)

		if !isRetryableStatus(resp.StatusCode) {
			return resp, nil
		}

		drainAndClose(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests && spec.adaptive {
			c.onRateLimited()
		}
		httpAttempt++
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if httpAttempt > maxRetries {
			return nil, fmt.Errorf("request to %s failed after %d retries (last status HTTP %d)", spec.label, maxRetries, resp.StatusCode)
		}
		backoff := computeBackoff(httpAttempt-1, baseBackoff, maxBackoff)
		c.debugf("retryable HTTP %d on %s, backing off %s (attempt %d/%d)", resp.StatusCode, spec.label, backoff, httpAttempt, maxRetries)
		if err := c.waitForRetry(ctx, backoff); err != nil {
			return nil, err
		}
	}
}

// newAPIRequest builds a Perplexity API request with browser-like headers and
// the session cookie.
func (c *Client) newAPIRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	return req, nil
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
}

func (c *Client) debugf(format string, args ...any) {
	if c.verbose {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// checkAPIStatus maps a non-200 Perplexity API response to an error.
func checkAPIStatus(label string, resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrNotAuthenticated, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		if snippet := readErrorSnippet(resp.Body); snippet != "" {
			return fmt.Errorf("unexpected response from %s: HTTP %d — %s", label, resp.StatusCode, snippet)
		}
		return fmt.Errorf("unexpected response from %s: HTTP %d", label, resp.StatusCode)
	}
	return nil
}

// decodeJSON decodes exactly one JSON value from body into dest, rejecting
// trailing data and bodies above limit bytes. A nil dest discards the body.
func decodeJSON(label string, body io.Reader, dest any, limit int64) error {
	limited := http.MaxBytesReader(nil, io.NopCloser(body), limit)
	if dest == nil {
		if _, err := io.Copy(io.Discard, limited); err != nil {
			return bodyReadError(label, limit, err)
		}
		return nil
	}

	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(dest); err != nil {
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
			return fmt.Errorf("could not decode response from %s: %w", label, err)
		}
		return bodyReadError(label, limit, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return fmt.Errorf("could not decode response from %s: trailing data: %w", label, err)
	}
	return nil
}

// bodyReadError classifies a failure while reading a response body.
func bodyReadError(label string, limit int64, err error) error {
	if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
		return fmt.Errorf("response from %s exceeds %d byte cap", label, limit)
	}
	return fmt.Errorf("could not read response from %s: %w", label, redactURLError(err))
}

// readErrorSnippet returns up to maxErrorBody bytes of an error response as
// trimmed, valid UTF-8.
func readErrorSnippet(body io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(body, maxErrorBody))
	return strings.TrimSpace(strings.ToValidUTF8(string(data), string(utf8.RuneError)))
}

// drainAndClose discards up to maxDrainBody of a body, then closes it. An
// HTTP/1.1 connection is reused only if the body was fully drained, so reuse
// is best-effort for large error bodies. It is safe to call on an
// already-consumed body.
func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxDrainBody))
	_ = body.Close()
}

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// isPermanentTransportError reports errors that retrying cannot fix:
// rejected redirects, refused schemes and certificate verification failures.
func isPermanentTransportError(err error) bool {
	return errors.Is(err, errRedirectRejected) || errors.Is(err, errInsecureScheme) || isCertificateError(err)
}

// isCertificateError reports TLS certificate verification failures.
func isCertificateError(err error) bool {
	var verification *utls.CertificateVerificationError
	var stdVerification *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var systemRoots x509.SystemRootsError
	var constraint x509.ConstraintViolationError
	return errors.As(err, &constraint) ||
		errors.As(err, &verification) ||
		errors.As(err, &stdVerification) ||
		errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostname) ||
		errors.As(err, &invalid) ||
		errors.As(err, &systemRoots)
}

// checkRedirect keeps every redirect hop on https with a host.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return &redirectError{msg: fmt.Sprintf("redirect rejected: stopped after %d redirects", maxRedirects)}
	}
	if req.URL.Scheme != "https" || req.URL.Host == "" {
		return &redirectError{msg: "redirect rejected: refusing non-https or hostless URL " + redactURL(req.URL)}
	}
	// net/http sets Referer to the previous URL, query included, which would
	// hand a pre-signed signature to the next host.
	if ref := req.Header.Get("Referer"); ref != "" {
		if u, err := url.Parse(ref); err != nil {
			req.Header.Del("Referer")
		} else if u.RawQuery != "" || u.User != nil || u.Fragment != "" {
			req.Header.Set("Referer", redactURL(u))
		}
	}
	return nil
}

// redactURL drops userinfo, query and fragment, which may carry credentials
// such as pre-signed URL signatures.
func redactURL(u *url.URL) string {
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// safeError shows a message built only from known-safe parts while keeping the
// original error reachable through Unwrap for errors.Is/As.
type safeError struct {
	msg string
	err error
}

func (e *safeError) Error() string { return e.msg }
func (e *safeError) Unwrap() error { return e.err }

// redirectError is returned by checkRedirect; its message is built from a
// redacted URL and is safe to display.
type redirectError struct{ msg string }

func (e *redirectError) Error() string { return e.msg }
func (e *redirectError) Unwrap() error { return errRedirectRejected }

// redactURLError replaces a transport error's message with a description
// chosen from known error types. net/http embeds raw URLs (query, userinfo,
// fragment) in *url.Error and in nested errors such as a malformed redirect
// Location, and no text scrubbing reliably removes them, so the original text
// is never displayed.
func redactURLError(err error) error {
	if err == nil {
		return nil
	}
	return &safeError{msg: describeTransportError(err), err: err}
}

func describeTransportError(err error) string {
	var redirect *redirectError
	var dnsErr *net.DNSError
	var opErr *net.OpError
	var errno syscall.Errno
	var netErr net.Error
	switch {
	case errors.As(err, &redirect):
		return redirect.msg
	case errors.Is(err, errInsecureScheme):
		return errInsecureScheme.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "request timed out"
	case errors.Is(err, context.Canceled):
		return "request canceled"
	case isCertificateError(err):
		return "TLS certificate verification failed"
	case errors.As(err, &dnsErr):
		return fmt.Sprintf("DNS lookup failed for host %q", dnsErr.Name)
	case errors.As(err, &opErr) && errors.As(err, &errno):
		return fmt.Sprintf("%s %s: %s", opErr.Op, opErr.Net, errno.Error())
	case errors.As(err, &netErr) && netErr.Timeout():
		return "network timeout"
	case errors.As(err, &opErr):
		return fmt.Sprintf("%s %s failed", opErr.Op, opErr.Net)
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "connection closed unexpectedly"
	}
	return "transport error"
}

// rateLimit enforces the configured delay between requests without delaying
// cancellation. lastReq advances only when the caller may issue a request.
func (c *Client) rateLimit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.delay > 0 && !c.lastReq.IsZero() {
		elapsed := time.Since(c.lastReq)
		if elapsed < c.delay {
			wait := c.waitDelay
			if wait == nil {
				wait = waitForDelay
			}
			if err := wait(ctx, c.delay-elapsed); err != nil {
				return err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.lastReq = time.Now()
	return nil
}

func waitForDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) waitForRetry(ctx context.Context, delay time.Duration) error {
	if c.retryWait != nil {
		return c.retryWait(ctx, delay)
	}
	return waitForDelay(ctx, delay)
}

// onSuccess records a successful request and gradually lowers the adaptive
// delay back toward the configured minimum after sustained success.
func (c *Client) onSuccess() {
	c.consecutiveOK++
	if c.consecutiveOK >= adaptiveDecayRequests && c.delay > c.minDelay {
		next := c.delay / 2
		// Halving never reaches a zero minimum; snap once the delay is small.
		if next < DefaultDelay/4 {
			next = c.minDelay
		}
		c.delay = max(next, c.minDelay)
		c.consecutiveOK = 0
		c.debugf("adaptive delay decreased to %s", c.delay)
	}
}

// onRateLimited doubles the inter-request delay (a zero delay jumps to
// DefaultDelay so it still backs off) and caps it at maxDelay, or at the
// configured minimum if that is higher.
func (c *Client) onRateLimited() {
	c.consecutiveOK = 0
	ceiling := max(maxDelay, c.minDelay)
	next := c.delay * 2
	if c.delay == 0 {
		next = min(DefaultDelay, ceiling)
	}
	newDelay := min(max(next, c.minDelay), ceiling)
	if newDelay != c.delay {
		c.delay = newDelay
		c.debugf("adaptive delay increased to %s", c.delay)
	}
}
