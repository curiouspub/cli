package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/curiouspub/cli/pkg/wire"
)

// TestNetworkFailureCopy_ConnRefusedAndTLSDistinctMessages proves two of
// networkError's three branches, which shipped with no test naming
// either. Both scenarios are hermetic and instant: connection-refused
// only needs a listener bound, learned, and closed before it is dialled;
// TLS only needs a server whose certificate this client has no reason to
// trust. Neither needs a fixture or the network.
//
// Ported from an independent reconstruction of this client's documented
// behaviour, built from this client's public surface and the wire
// contract alone, with no access to networkError's own source. Its
// independence bought exactly this: proof that both branches are
// reachable and distinguishable from spec-level knowledge alone, not
// only from having read the classifier that produces them — the
// classifier itself shipped with zero coverage for either.
func TestNetworkFailureCopy_ConnRefusedAndTLSDistinctMessages(t *testing.T) {
	ctx := t.Context()

	// Connection refused: bind, learn the address, close, dial the now-dead
	// address.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not bind an ephemeral port to manufacture connection-refused: %v", err)
	}
	refusedAddr := ln.Addr().String()
	_ = ln.Close()

	cRefused, err := New("http://" + refusedAddr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, refusedErr := cRefused.AuthStart(ctx, wire.AuthStartRequest{Email: "a@example.com"})
	if refusedErr == nil {
		t.Fatal("expected an error dialling a closed loopback port")
	}
	if _, ok := refusedErr.(*APIError); ok {
		t.Errorf("connection-refused surfaced as *APIError; want a plain network "+
			"error — no HTTP response was ever received: %v", refusedErr)
	}
	if !strings.Contains(refusedErr.Error(), "connection refused") {
		t.Errorf("connection-refused message %q does not say so", refusedErr.Error())
	}
	if host, _, _ := net.SplitHostPort(refusedAddr); !strings.Contains(refusedErr.Error(), host) {
		t.Errorf("connection-refused message %q does not name the host it tried (%s)",
			refusedErr.Error(), host)
	}

	// TLS: an HTTPS server whose certificate this client has no way to
	// trust (New exposes no TLS-config option), so the handshake fails.
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"open":true,"accounts_left":1,"resets_at":"2026-01-01T00:00:00Z"}`))
	}))
	defer tlsServer.Close()

	cTLS, err := New(tlsServer.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, tlsErr := cTLS.AuthStart(ctx, wire.AuthStartRequest{Email: "a@example.com"})
	if tlsErr == nil {
		t.Fatal("expected a certificate-trust error against a self-signed test server")
	}
	if _, ok := tlsErr.(*APIError); ok {
		t.Errorf("TLS failure surfaced as *APIError; want a plain network error — "+
			"no HTTP response was ever received: %v", tlsErr)
	}
	if !strings.Contains(tlsErr.Error(), "TLS error") {
		t.Errorf("TLS error message %q does not say so", tlsErr.Error())
	}
	tlsHost, _, _ := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(tlsServer.URL, "https://"), "http://"))
	if tlsHost != "" && !strings.Contains(tlsErr.Error(), tlsHost) {
		t.Errorf("TLS error message %q does not name the host it tried (%s)", tlsErr.Error(), tlsHost)
	}

	if refusedErr.Error() == tlsErr.Error() {
		t.Errorf("connection-refused and TLS produced the identical message %q; "+
			"each must get its own", refusedErr.Error())
	}
}
