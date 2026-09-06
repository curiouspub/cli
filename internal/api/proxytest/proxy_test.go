package proxytest

import (
	"context"
	"crypto/tls"
	"net"
	"sync/atomic"
	"testing"

	"github.com/curiouspub/cli/internal/api"
)

// wireProxyTestTransport reaches into a Client built by the real
// api.New — so a mutation that removes Proxy: http.ProxyFromEnvironment
// from New's own Transport is actually caught by these tests — through
// Client's own Transport accessor rather than an unexported field, since
// this package is deliberately not package api. It adds only what a
// throwaway certificate and a redirect-to-backend dialer need, and never
// touches the Proxy field New already set.
func wireProxyTestTransport(t *testing.T, c *api.Client) {
	t.Helper()
	tr := c.Transport()
	tr.TLSClientConfig = &tls.Config{RootCAs: proxyBackendPool}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == proxyShouldBypassHost+":443" {
			// The whole point of this hostname is that it must not need
			// to resolve anywhere real. Redirecting the ACTUAL dial to
			// the same local backend the proxy tunnels to proves the
			// request reached a real server without this suite ever
			// touching a real network.
			addr = proxyBackend.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
}

// TestProxyHonoured is this suite's "proxy honoured" row: with
// HTTPS_PROXY pointed at a local test proxy, a Capacity call is observed
// AT THE PROXY. Asserted by the proxy's own CONNECT count, never by the
// call merely succeeding — a client that silently bypassed the proxy and
// reached the backend directly would also succeed, and that distinction
// is the entire test. The backend's own hit count is asserted too: the
// proxy's CONNECT count alone proves the tunnel was opened, not that a
// real request travelled through it end to end.
func TestProxyHonoured(t *testing.T) {
	beforeConnects := atomic.LoadInt32(&proxyConnectCount)
	beforeBackendHits := atomic.LoadInt32(&proxyBackendHits)

	c, err := api.New("https://" + proxyShouldUseHost)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	wireProxyTestTransport(t, c)

	resp, err := c.Capacity(context.Background())
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if !resp.Open {
		t.Errorf("Open = false, want true")
	}

	if got := atomic.LoadInt32(&proxyConnectCount) - beforeConnects; got != 1 {
		t.Fatalf("proxy saw %d new CONNECT requests, want exactly 1 — the call "+
			"must be observed AT THE PROXY, not merely succeed", got)
	}
	if got := atomic.LoadInt32(&proxyBackendHits) - beforeBackendHits; got != 1 {
		t.Fatalf("backend saw %d new requests, want exactly 1 — the tunnelled "+
			"request must actually reach the backend, not merely open the tunnel", got)
	}
}

// TestProxyNoProxyHonoured is this suite's next row: NO_PROXY covering
// the target host makes the client bypass a configured proxy entirely —
// the proxy sees zero requests, and the call still succeeds because it
// went straight to the real backend. The backend's own hit count proves
// "still succeeds" means "actually reached the backend", not merely
// "returned no error".
//
// proxyShouldBypassHost is deliberately NOT a loopback spelling:
// net/http's own proxy resolution exempts loopback addresses
// unconditionally, which would make this row pass for a reason that has
// nothing to do with NO_PROXY. A non-loopback host that NO_PROXY
// actually names is what makes this test exercise the variable it
// claims to.
func TestProxyNoProxyHonoured(t *testing.T) {
	beforeConnects := atomic.LoadInt32(&proxyConnectCount)
	beforeBackendHits := atomic.LoadInt32(&proxyBackendHits)

	c, err := api.New("https://" + proxyShouldBypassHost)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	wireProxyTestTransport(t, c)

	resp, err := c.Capacity(context.Background())
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if !resp.Open {
		t.Errorf("Open = false, want true")
	}

	if got := atomic.LoadInt32(&proxyConnectCount) - beforeConnects; got != 0 {
		t.Fatalf("proxy saw %d new CONNECT requests, want 0 — NO_PROXY should "+
			"have sent this call directly to the backend", got)
	}
	if got := atomic.LoadInt32(&proxyBackendHits) - beforeBackendHits; got != 1 {
		t.Fatalf("backend saw %d new requests, want exactly 1 — bypassing the "+
			"proxy must still reach the real backend directly", got)
	}
}
