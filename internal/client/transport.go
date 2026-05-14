package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// chromeTransport is an http.RoundTripper that uses utls to impersonate
// Chrome's TLS fingerprint (JA3/JA4), bypassing Cloudflare's fingerprint-based
// challenges.
type chromeTransport struct {
	mu        sync.Mutex
	h2Transport *http2.Transport
}

// newChromeTransport creates a transport that impersonates Chrome's TLS handshake.
func newChromeTransport() *chromeTransport {
	return &chromeTransport{}
}

func (t *chromeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// For non-HTTPS, fall back to default transport.
	if req.URL.Scheme != "https" {
		return http.DefaultTransport.RoundTrip(req)
	}

	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)

	// Dial TCP connection.
	dialer := &net.Dialer{}
	tcpConn, err := dialer.DialContext(req.Context(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	// Wrap with utls using Chrome fingerprint.
	config := &utls.Config{
		ServerName:         host,
		InsecureSkipVerify: false,
	}
	uConn := utls.UClient(tcpConn, config, utls.HelloChrome_Auto)

	if err := uConn.HandshakeContext(req.Context()); err != nil {
		tcpConn.Close()
		return nil, fmt.Errorf("utls handshake with %s: %w", host, err)
	}

	// Use HTTP/2 if negotiated, otherwise HTTP/1.1.
	alpn := uConn.ConnectionState().NegotiatedProtocol
	if alpn == "h2" {
		return t.roundTripH2(req, uConn)
	}
	return t.roundTripH1(req, uConn)
}

func (t *chromeTransport) roundTripH1(req *http.Request, conn net.Conn) (*http.Response, error) {
	// Create a one-shot HTTP/1.1 transport with the pre-established TLS conn.
	transport := &http.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return conn, nil
		},
		// Disable TLS since we already did it.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	defer transport.CloseIdleConnections()
	return transport.RoundTrip(req)
}

func (t *chromeTransport) roundTripH2(req *http.Request, conn net.Conn) (*http.Response, error) {
	// Use HTTP/2 transport over the established TLS connection.
	h2Transport := &http2.Transport{
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return conn, nil
		},
	}
	defer h2Transport.CloseIdleConnections()
	return h2Transport.RoundTrip(req)
}
