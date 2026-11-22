package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	utls "github.com/refraction-networking/utls"
)

// errInsecureScheme marks a request refused because it was not https. It is
// never retried.
var errInsecureScheme = errors.New("refusing non-https request")

// tlsHandshakeTimeout bounds the utls handshake on a freshly dialed conn.
// Tests shorten it.
var tlsHandshakeTimeout = 15 * time.Second

// chromeTransport is an http.RoundTripper that uses utls to impersonate
// Chrome's TLS fingerprint (JA3/JA4), bypassing Cloudflare's fingerprint-based
// challenges. Connections are pooled by a single long-lived http.Transport,
// which negotiates HTTP/2 or HTTP/1.1 from the utls ALPN result.
type chromeTransport struct {
	base *http.Transport
}

// newChromeTransport creates a transport that impersonates Chrome's TLS
// handshake and verifies certificates against the system roots.
func newChromeTransport() *chromeTransport {
	return newChromeTransportWithConfig(nil)
}

// newChromeTransportWithConfig is newChromeTransport with an optional utls
// config template (tests use it to trust a local test CA).
func newChromeTransportWithConfig(tmpl *utls.Config) *chromeTransport {
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)

	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}

	return &chromeTransport{base: &http.Transport{
		// Proxies are deliberately unsupported: net/http performs the TLS
		// handshake itself for proxied requests, which would bypass utls and
		// lose the Chrome fingerprint.
		Proxy: nil,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialChromeTLS(ctx, dialer, tmpl, network, addr)
		},
		Protocols:             &protocols,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: 30 * time.Second,
			PingTimeout:     15 * time.Second,
		},
	}}
}

// RoundTrip refuses anything but https so requests never fall back to a
// plaintext or non-fingerprinted transport.
func (t *chromeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("%w to %s://%s", errInsecureScheme, req.URL.Scheme, req.URL.Host)
	}
	return t.base.RoundTrip(req)
}

// CloseIdleConnections closes pooled connections that are not in use.
func (t *chromeTransport) CloseIdleConnections() {
	t.base.CloseIdleConnections()
}

func dialChromeTLS(ctx context.Context, dialer *net.Dialer, tmpl *utls.Config, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid address %q: %w", addr, err)
	}

	rawConn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	var cfg *utls.Config
	if tmpl != nil {
		cfg = tmpl.Clone()
	} else {
		cfg = &utls.Config{}
	}
	cfg.ServerName = host

	// net/http detaches dials from request cancellation and the dialer timeout
	// only covers TCP, so the handshake needs its own deadline.
	hsCtx, cancel := context.WithTimeout(ctx, tlsHandshakeTimeout)
	defer cancel()

	uConn := utls.UClient(rawConn, cfg, utls.HelloChrome_Auto)
	if err := uConn.HandshakeContext(hsCtx); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("utls handshake with %s: %w", host, err)
	}
	return &utlsConn{UConn: uConn}, nil
}

// utlsConn exposes the utls connection state as a crypto/tls
// ConnectionState, which net/http uses to select HTTP/2 via ALPN.
type utlsConn struct {
	*utls.UConn
}

func (c *utlsConn) ConnectionState() tls.ConnectionState {
	s := c.UConn.ConnectionState()
	return tls.ConnectionState{
		Version:                     s.Version,
		HandshakeComplete:           s.HandshakeComplete,
		DidResume:                   s.DidResume,
		CipherSuite:                 s.CipherSuite,
		NegotiatedProtocol:          s.NegotiatedProtocol,
		NegotiatedProtocolIsMutual:  s.NegotiatedProtocolIsMutual,
		ServerName:                  s.ServerName,
		PeerCertificates:            s.PeerCertificates,
		VerifiedChains:              s.VerifiedChains,
		SignedCertificateTimestamps: s.SignedCertificateTimestamps,
		OCSPResponse:                s.OCSPResponse,
		ECHAccepted:                 s.ECHAccepted,
	}
}
