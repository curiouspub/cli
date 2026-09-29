package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// The build log is opened in three phases before any response exists —
// dialling, the TLS handshake, the server answering — and each has its own
// bound on the transport. A failure in any of them is reported with the
// phase it happened in, taken from the client's own record of how far the
// request got. These rows put a request into each phase and hold it there.

const (
	rowConnect = 2 * time.Second
	rowTLS     = 200 * time.Millisecond
	rowHeaders = 200 * time.Millisecond
)

// openStage opens the build log against base with the row bounds and
// returns the stage the failure reports. It waits at most ten times the
// widest bound, and a request still open after that is a phase with no
// bound at all.
func openStage(t *testing.T, base string) (StreamStage, time.Duration) {
	t.Helper()
	c, err := New(base, WithConnectionBounds(rowConnect, rowTLS, rowHeaders))
	if err != nil {
		t.Fatalf("New(%q): %v", base, err)
	}
	type result struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan result, 1)
	go func() {
		started := time.Now()
		body, err := c.DeployEvents(context.Background(), "dep_row")
		if body != nil {
			_ = body.Close()
		}
		done <- result{err, time.Since(started)}
	}()
	var res result
	select {
	case res = <-done:
	case <-time.After(10 * rowConnect):
		t.Fatalf("opening the build log against %s had not come back after %v: a phase with "+
			"nothing bounding it", base, 10*rowConnect)
	}
	var open *StreamOpenError
	if !errors.As(res.err, &open) {
		t.Fatalf("opening the build log against %s returned %v (%T), want a *StreamOpenError "+
			"naming the phase", base, res.err, res.err)
	}
	return open.Stage, res.elapsed
}

// TestARefusedConnectionIsNamedAsTheConnectPhase: nothing listens on the
// port, so the request never gets past dialling.
func TestARefusedConnectionIsNamedAsTheConnectPhase(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	if stage, _ := openStage(t, "http://"+addr); stage != StageConnect {
		t.Errorf("a refused connection is reported as the %q phase, want %q", stage, StageConnect)
	}
}

// TestAHandshakeThatNeverFinishesIsBoundedAndNamed: the server accepts the
// connection and never speaks TLS. The handshake bound ends it, and the
// failure names the TLS phase.
func TestAHandshakeThatNeverFinishesIsBoundedAndNamed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var mu sync.Mutex
	var held []net.Conn
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn) // accepted, and never answered
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		for _, c := range held {
			_ = c.Close()
		}
	}()

	stage, elapsed := openStage(t, "https://"+ln.Addr().String())
	if stage != StageTLS {
		t.Errorf("a handshake that never finished is reported as the %q phase, want %q", stage, StageTLS)
	}
	if elapsed >= rowConnect {
		t.Errorf("the handshake failure took %v, past the %v connect bound, so something "+
			"other than the %v handshake bound ended it", elapsed, rowConnect, rowTLS)
	}
}

// TestAServerThatNeverAnswersIsBoundedAndNamed: the server takes the
// request and writes no response. The header bound ends it, and the
// failure names the response phase.
func TestAServerThatNeverAnswersIsBoundedAndNamed(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	stage, elapsed := openStage(t, srv.URL)
	if stage != StageResponse {
		t.Errorf("a server that never answered is reported as the %q phase, want %q", stage, StageResponse)
	}
	if elapsed >= rowConnect {
		t.Errorf("the failure took %v, past the %v connect bound, so something other than "+
			"the %v header bound ended it", elapsed, rowConnect, rowHeaders)
	}
}

// TestTheDefaultBoundsReachTheTransport: a client built with no options
// carries the three Default constants on its transport, so the numbers
// the registry records are the numbers that ship. The dial bound lives
// inside a function value and cannot be read back, so it is proved by
// behaviour instead, in the connect row above; the other two are fields.
func TestTheDefaultBoundsReachTheTransport(t *testing.T) {
	c, err := New("https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	tr := c.Transport()
	if tr.TLSHandshakeTimeout != DefaultTLSHandshakeTimeout {
		t.Errorf("the transport's TLS bound is %v, want %v", tr.TLSHandshakeTimeout, DefaultTLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != DefaultResponseHeaderTimeout {
		t.Errorf("the transport's header bound is %v, want %v", tr.ResponseHeaderTimeout, DefaultResponseHeaderTimeout)
	}
	if tr.DialContext == nil {
		t.Error("the transport has no dialler of its own, so the connect bound is not on it")
	}
	for _, bad := range [][3]time.Duration{{0, 1, 1}, {1, 0, 1}, {1, 1, 0}, {-1, 1, 1}} {
		if _, err := New("https://example.test", WithConnectionBounds(bad[0], bad[1], bad[2])); err == nil {
			t.Errorf("New accepted connection bounds %v; zero or less means no bound at all", bad)
		}
	}
}
