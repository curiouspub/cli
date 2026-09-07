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
// Every clause below refuses outright, before the scheme is even looked
// at, because each one names a way a base URL can carry more than "a
// scheme and a host" while still looking superficially fine:
//
//   - An OPAQUE URL ("mailto:x", "https:opaque-thing") has no authority
//     component at all — Hostname() reports an empty string for one, and
//     letting it fall through to the empty-host case below would blame
//     the wrong thing.
//   - An EMPTY HOST ("https:///path") parses without error and without a
//     scheme problem, so nothing else here would catch it.
//   - USERINFO gets its own refusal and its own reason, because the
//     failure mode is not "a strange base URL" but a credential leak by
//     placement: net/http converts a "user:pass@" prefix into an
//     Authorization: Basic header on every request this client sends,
//     silently, whether or not the caller ever asked for one — and the
//     password sits afterwards in c.baseURL, an ordinary string field.
//     internal/guard's Secret check watches unexported fields that can
//     reach a ui.Secret; a password folded into a plain string never
//     reaches that type, so it passes the guard by never being the kind
//     of value the guard was built to find. Refusing it here is the only
//     place that failure mode can be stopped.
//   - A QUERY or FRAGMENT has no legitimate reason to be part of a base
//     URL a caller configures once at construction; either one existing
//     is a sign the value came from somewhere that concatenated more
//     than it meant to.
//
// On success it returns the URL with every trailing slash trimmed — not
// just one — and does not otherwise rewrite it: a trailing dot inside
// the host is valid DNS syntax and is left for the resolver to accept,
// since only the COMPARISON above needed it stripped.
func validateBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid API base URL %q: %w", raw, err)
	}

	if u.Opaque != "" {
		return "", fmt.Errorf(
			"refusing API base %q: not a URL with a scheme and a host — use a base "+
				"URL naming a server, with the https scheme, such as api.example.com", raw)
	}

	host := strings.ToLower(u.Hostname())
	host = strings.TrimSuffix(host, ".")

	if host == "" {
		return "", fmt.Errorf(
			"refusing API base %q: missing a host — the base URL must name the "+
				"server to talk to, such as api.example.com over https", raw)
	}

	if u.User != nil {
		return "", fmt.Errorf(
			"refusing API base %q: a base URL must not carry a username or "+
				"password — remove the \"user:pass@\" segment from the host", raw)
	}

	if u.RawQuery != "" || u.ForceQuery {
		return "", fmt.Errorf(
			"refusing API base %q: a base URL must not carry a query string", raw)
	}

	if u.Fragment != "" {
		return "", fmt.Errorf(
			"refusing API base %q: a base URL must not carry a fragment", raw)
	}

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

	return strings.TrimRight(u.String(), "/"), nil
}

// Option configures a Client built by New.
type Option func(*Client)

// WithTimeout overrides the default 30-second per-request timeout. A
// zero or negative duration is refused by New, at construction — the
// same "refuse once, up front" shape the address guard already follows
// — rather than accepted here and left to make every subsequent call
// fail immediately with a deadline-exceeded error, which is what a
// context.WithTimeout given a non-positive duration does.
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

// ResolveBaseURL answers "which API endpoint is this run talking to",
// and it is the ONE place that question is answered. An explicit argument
// wins; an empty one falls back to CURIOUS_API_URL, and then to the
// compiled-in default.
//
// It is exported because more than this package needs the answer. The
// stored credential records the endpoint it was issued against so a
// token from a local development server is never sent to production, and
// the code holding that file has to know what the effective endpoint IS
// before it can compare. Without this, that code would have to repeat the
// fallback chain — including a second copy of the default URL, which the
// compiled-in-hostname guard refuses outright, correctly: a URL a reader
// cannot find by grepping for one declaration is the shape a phone-home
// takes.
//
// It performs NO validation. The result is the base URL a caller MEANT,
// which is exactly what a comparison needs even when it is one the
// address guard would refuse to dial — a stored endpoint that a
// tightened guard now rejects must still be comparable, or the guard
// would strand a config it can no longer describe. New applies the guard
// immediately afterwards; nothing else should assume it has been applied.
func ResolveBaseURL(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if fromEnv := os.Getenv("CURIOUS_API_URL"); fromEnv != "" {
		return fromEnv
	}
	return defaultAPIBase
}

// New constructs a Client. An empty baseURL falls back to
// CURIOUS_API_URL, and then to the compiled-in default — through
// ResolveBaseURL, which is the same function anything else asking that
// question uses, so the two cannot drift. Every base URL — explicit, from
// the environment, or the default — passes through the address guard
// before this returns; see validateBaseURL.
func New(baseURL string, opts ...Option) (*Client, error) {
	normalized, err := validateBaseURL(ResolveBaseURL(baseURL))
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

	if c.timeout <= 0 {
		return nil, fmt.Errorf(
			"refusing to construct a client with a non-positive timeout (%v): every "+
				"call would fail immediately with a deadline-exceeded error instead "+
				"of ever attempting one — pass a positive duration to WithTimeout, or "+
				"omit the option entirely for the %v default", c.timeout, defaultTimeout)
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
		// CheckRedirect refuses every redirect outright, rather than
		// following it as http.Client would by default. This API never
		// legitimately redirects, and the address guard above only ever
		// looks at the URL a call STARTS with — it has no say over where
		// a redirect response points. net/http's own rule for forwarding
		// a request's Authorization header across a redirect keys on
		// host, never scheme, so a same-host https-to-http redirect
		// would carry a bearer token onto the wire in clear; a 307 or
		// 308 would also re-send the request body to whatever host the
		// redirect names. Nothing attaches a token to a request in this
		// package yet, but this is the Client every authenticated call
		// will inherit, and the refusal belongs here before the first
		// one exists rather than after.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return c, nil
}

// Transport returns the *http.Transport every request from this Client
// goes through — the same one New builds, Proxy field included. It is a
// read-only escape hatch for a caller with a genuine reason to extend
// the Transport (additional TLS trust material, a wrapped RoundTripper,
// connection-level instrumentation) without this package growing a
// bespoke setter for every such knob. It grants no more trust than any
// other exported method already does: nothing stops a caller from
// mutating the result unwisely, the same as with any pointer this
// package hands back.
func (c *Client) Transport() *http.Transport {
	return c.httpClient.Transport.(*http.Transport)
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
				if errors.Is(decErr, io.ErrUnexpectedEOF) {
					// The connection was cut mid-body — a connection-level
					// failure indistinguishable from any other dropped
					// connection, and exactly the kind of blip a retry
					// exists to smooth over. Every OTHER decode failure
					// (malformed JSON, a field of the wrong shape) is this
					// client's own problem to report, never the network's.
					return true, fmt.Errorf("reading response body from %s: %w", c.baseURL, decErr)
				}
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
	if isConnectionRefused(err) {
		return fmt.Errorf("connection refused by %s: %w", host, err)
	}
	var certErr *tls.CertificateVerificationError
	var recordErr tls.RecordHeaderError
	if errors.As(err, &certErr) || errors.As(err, &recordErr) {
		return fmt.Errorf("TLS error talking to %s: %w", host, err)
	}
	return fmt.Errorf("could not reach %s: %w", host, err)
}
