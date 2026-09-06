package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/curiouspub/cli/internal/ui"
)

// defaultAPIBase is the compiled-in default control-plane API base — one
// of the at most two named URL constants internal/guard's second guard
// allows in this whole module. CURIOUS_API_URL overrides it; nothing
// else in this module may name a compiled-in host, named or bare.
const defaultAPIBase = "https://api.curious.pub"

// defaultTimeout bounds one request end to end — connect through
// reading the body — via context.WithTimeout in attempt below. A client
// with no deadline turns a dead connection into a hang nothing but
// Ctrl-C can end.
const defaultTimeout = 30 * time.Second

// Version is this binary's release version, folded into every request's
// User-Agent. cmd/curious's own `version` variable belongs to package
// main and is set at release time via -ldflags; it cannot be imported
// here without inverting the module's dependency direction, so this
// package carries its own copy instead — left at "dev" by a plain build,
// exactly like main's, and wired to the real value by whatever
// constructs the Client the shipped binary actually uses.
var Version = "dev"

// insecureLoopbackHosts is the address guard's accepted set for a
// non-https base URL — the dev-environment escape hatch, and nothing
// wider. Every other host must arrive over https, or a bearer token
// this client attaches would go out in clear text on a typo'd
// environment variable.
//
// [::1] sits beside localhost and 127.0.0.1 deliberately: excluding it
// would fail closed but break an IPv6-only dev loop, and it is exactly
// as much a loopback address as the other two. Nothing else — including
// every OTHER 127.0.0.0/8 address — is in this set, because the set is
// exact, not a range.
var insecureLoopbackHosts = map[string]bool{
	"localhost": true,
	"127.0.0.1": true,
	"::1":       true,
}

// validateBaseURL is the address guard. It parses raw exactly once and
// decides allow or refuse from what url.Parse already extracted — never
// from a second read of the raw string, which is where a lexical
// shortcut (a prefix or substring check) hides a spelling nobody
// thought to probe.
//
// The normalisation is host := lowercase(u.Hostname()), trailing dot
// stripped — in that order, and read from Hostname() rather than
// u.Host, because Hostname() already strips the port AND any
// "user:pass@" userinfo. A comparison against the raw string, or against
// u.Host, would let a userinfo segment or a path/fragment substring
// impersonate a host that was never actually being dialled.
//
// On success it returns raw with one trailing slash trimmed; it does not
// otherwise rewrite the URL — a trailing dot inside the host is valid
// DNS syntax and is left for the resolver to accept, since only the
// COMPARISON needs it stripped.
func validateBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid API base URL %q: %w", raw, err)
	}

	host := strings.ToLower(u.Hostname())
	host = strings.TrimSuffix(host, ".")

	switch strings.ToLower(u.Scheme) {
	case "https":
		// Any host is fine over https: the transport encrypts whatever a
		// bearer token would otherwise expose in clear.
	case "http":
		if !insecureLoopbackHosts[host] {
			return "", fmt.Errorf(
				"refusing plaintext API base %q: http is only allowed to a loopback "+
					"host (localhost, 127.0.0.1, or [::1]) for local development — "+
					"use a base URL with the https scheme instead, or point "+
					"CURIOUS_API_URL at one of those hosts", raw)
		}
	default:
		return "", fmt.Errorf(
			"refusing API base %q: the scheme must be https, or http to a loopback "+
				"host for local development", raw)
	}

	return strings.TrimSuffix(u.String(), "/"), nil
}

// Option configures a Client built by New.
type Option func(*Client)

// WithTimeout overrides the default 30-second per-request timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithToken sets the bearer token this Client attaches to a call that
// takes one. None of this epic's four calls do — auth/verify RETURNS a
// token, it does not spend one — so this has no visible effect against
// them; it exists for the calls a later change adds to this Client.
func WithToken(t ui.Secret) Option {
	return func(c *Client) { c.Token = t }
}

// Client is this CLI's handle onto the public /v1 API.
type Client struct {
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
	userAgent  string

	// Token is exported on purpose. internal/guard's fourth guard
	// forbids an UNEXPORTED struct field that can reach a ui.Secret,
	// because fmt cannot call a method on a value it reaches by
	// reflecting an unexported field — an unexported Secret field here
	// would print in full through %v on a Client handed to a log line by
	// accident, and ui.Secret's own methods could not stop it. Exporting
	// the field costs nothing beyond that: ui.Secret already redacts
	// itself through every formatting and marshalling path on its own.
	Token ui.Secret
}

// New constructs a Client. An empty baseURL falls back to
// CURIOUS_API_URL, and then to the compiled-in default. Every base URL —
// explicit, from the environment, or the default — passes through the
// address guard before this returns; see validateBaseURL.
func New(baseURL string, opts ...Option) (*Client, error) {
	if baseURL == "" {
		baseURL = os.Getenv("CURIOUS_API_URL")
	}
	if baseURL == "" {
		baseURL = defaultAPIBase
	}

	normalized, err := validateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	c := &Client{
		baseURL:   normalized,
		timeout:   defaultTimeout,
		userAgent: fmt.Sprintf("curious/%s (%s/%s)", Version, runtime.GOOS, runtime.GOARCH),
	}
	for _, opt := range opts {
		opt(c)
	}

	c.httpClient = &http.Client{
		Transport: &http.Transport{
			// http.DefaultTransport sets this for free, which is exactly
			// what makes leaving it off a custom Transport dangerous: a
			// bare &http.Transport{} silently ignores HTTPS_PROXY,
			// HTTP_PROXY and NO_PROXY altogether, and does so quietly —
			// the direct connection still works in every environment
			// that has no proxy to bypass, which is every dev machine
			// and every CI runner, and never the one desk behind a
			// corporate proxy where it matters.
			Proxy: http.ProxyFromEnvironment,
		},
	}
	return c, nil
}

// retryBackoff is deliberately small: a retry exists to smooth over a
// blip, not to make a person wait, and the two REQUIRED calls it applies
// to (Capacity, Waitlist) are calls a user is actively waiting on.
func retryBackoff(attempt int) time.Duration {
	return time.Duration(attempt) * 25 * time.Millisecond
}

// do performs a JSON request against path, retrying on connection-level
// failures and 5xx responses when idempotent is true, and decodes a 2xx
// body into out (when out is non-nil and the body is non-empty).
//
// idempotent gates retrying at all: auth/start and auth/verify pass
// false, because retrying either spends something a retry cannot safely
// spend twice — see the package doc.
func (c *Client) do(ctx context.Context, method, path string, reqBody, out any, idempotent bool) error {
	var payload []byte
	if reqBody != nil {
		var err error
		payload, err = json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
	}

	attempts := 1
	if idempotent {
		attempts = 3
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryBackoff(attempt)):
			case <-ctx.Done():
				return lastErr
			}
		}

		retryable, err := c.attempt(ctx, method, path, payload, out)
		if err == nil {
			return nil
		}
		lastErr = err
		if !idempotent || !retryable {
			return err
		}
	}
	return lastErr
}

// attempt performs exactly one HTTP round trip. retryable reports
// whether the caller's retry loop should try again: true for a
// connection-level failure or a 5xx status, false for everything else
// (a successful decode, or a non-2xx response this client can already
// fully explain).
func (c *Client) attempt(ctx context.Context, method, path string, payload []byte, out any) (retryable bool, err error) {
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(reqCtx, method, c.baseURL+path, body)
	if err != nil {
		return false, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return true, networkError(err, c.baseURL)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil {
			// Decoded but never branched on: pkg/wire's own doc comment
			// is explicit that success is the HTTP status, and several
			// of these bodies are empty objects by design. "The response
			// is empty, let me check a field" is the exact mistake that
			// shape exists to prevent.
			if decErr := json.NewDecoder(resp.Body).Decode(out); decErr != nil && !errors.Is(decErr, io.EOF) {
				return false, fmt.Errorf("decoding response body: %w", decErr)
			}
		}
		return false, nil
	}

	apiErr := decodeAPIError(resp)
	return resp.StatusCode >= 500, apiErr
}

// networkError turns a transport-level failure into a message that
// names the host it was talking to and, where the underlying error
// makes it possible to tell, what kind of failure it was — "connection
// refused" against api.curious.pub means something different from the
// same error against localhost:8080 in dev.
func networkError(err error, baseURL string) error {
	host := baseURL
	if u, parseErr := url.Parse(baseURL); parseErr == nil && u.Host != "" {
		host = u.Host
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("could not resolve %s: %w", host, err)
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("connection refused by %s: %w", host, err)
	}
	var certErr *tls.CertificateVerificationError
	var recordErr tls.RecordHeaderError
	if errors.As(err, &certErr) || errors.As(err, &recordErr) {
		return fmt.Errorf("TLS error talking to %s: %w", host, err)
	}
	return fmt.Errorf("could not reach %s: %w", host, err)
}
