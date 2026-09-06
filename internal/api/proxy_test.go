package api

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
)

// wireProxyTestTransport reaches into a Client built by the real New —
// so the REQUIRED MUTATION below (removing Proxy: http.ProxyFromEnvironment
// from New's own Transport) is actually exercised by these tests — and
// adds only what a throwaway certificate and a redirect-to-backend
// dialer need. It never touches the Proxy field New already set.
func wireProxyTestTransport(t *testing.T, c *Client) {
	t.Helper()
	tr, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Client's Transport is a %T, not *http.Transport", c.httpClient.Transport)
	}
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

// TestProxyHonoured is the acceptance table's "Proxy honoured" row: with
// HTTPS_PROXY pointed at a local test proxy, a Capacity call is observed
// AT THE PROXY. Asserted by the proxy's own CONNECT count, never by the
// call merely succeeding — a client that silently bypassed the proxy and
// reached the backend directly would also succeed, and that distinction
// is the entire test.
func TestProxyHonoured(t *testing.T) {
	before := atomic.LoadInt32(&proxyConnectCount)

	c, err := New("https://" + proxyShouldUseHost)
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

	if got := atomic.LoadInt32(&proxyConnectCount) - before; got != 1 {
		t.Fatalf("proxy saw %d new CONNECT requests, want exactly 1 — the call "+
			"must be observed AT THE PROXY, not merely succeed", got)
	}
}

// TestProxyNoProxyHonoured is the table's next row: NO_PROXY covering
// the target host makes the client bypass a configured proxy entirely —
// the proxy sees zero requests, and the call still succeeds because it
// went straight to the real backend.
//
// proxyShouldBypassHost is deliberately NOT a loopback spelling:
// net/http's own proxy resolution exempts loopback addresses
// unconditionally, which would make this row pass for a reason that has
// nothing to do with NO_PROXY. A non-loopback host that NO_PROXY
// actually names is what makes this test exercise the variable it
// claims to.
func TestProxyNoProxyHonoured(t *testing.T) {
	before := atomic.LoadInt32(&proxyConnectCount)

	c, err := New("https://" + proxyShouldBypassHost)
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

	if got := atomic.LoadInt32(&proxyConnectCount) - before; got != 0 {
		t.Fatalf("proxy saw %d new CONNECT requests, want 0 — NO_PROXY should "+
			"have sent this call directly to the backend", got)
	}
}
