package flow

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"testing"

	"github.com/curiouspub/cli/internal/timing"
)

// The pin: holding the write side's governing quantity still.
// ---------------------------------------------------------------------
//
// The write-side rows bound the time for the client's send buffer to
// free space. That quantity is not a property of this repository at all:
// it is set by how large the kernels at the two ends decided the buffers
// should be, and both of them GROW a connection's buffers as it carries
// traffic. Round 1 measured the consequence rather than theorised it —
// the same probe over a connection kept warm across twenty runs reported
// 594 ms where a fresh connection reported 434 ms, because the kernel
// had given the busy one a larger buffer and the gap being measured is
// that buffer's window-update step divided by the drain rate.
//
// A margin over an autotuned quantity is a margin over a number nobody
// chose and nobody can reproduce. So the buffers are PINNED, and the
// window is a margin over the gap under the pin.
//
// # THE PIN IS A PAIR, AND THAT IS NOT SYMMETRY FOR ITS OWN SAKE
//
// An upload has the client SENDING and the fixture RECEIVING. Pin the
// client's SO_SNDBUF alone and the gap is still set by how often the far
// end's autotuned receive buffer advertises more window; pin the
// fixture's SO_RCVBUF alone and it is set by the sender's. A socket
// option has an END, and "the connection" names two sockets.
//
// MEASURED, not argued, on darwin 2026-09-10. With the send end pinned
// and the receive end left to autotune, the worst gap over sixty runs
// was 204.132 ms; with both ends pinned it was 154.359 ms over ONE
// HUNDRED runs — a third worse on two thirds of the evidence. And the
// buffers this machine hands out when nobody asks, read off a live
// socket while the mutation below was running, are 146,988 bytes
// sending and 408,300 receiving: three times the pinned size on the end
// that governs, on the leg with the NARROWEST autotuning range of the
// three. A Linux receive buffer autotunes into the megabytes, so the
// half-pinned measurement there would be further out still.
//
// # A PIN IS A CLAIM, SO IT IS READ BACK
//
// setsockopt may clamp, round, double or silently ignore a request and
// reports none of it. Every pin here is read back with getsockopt
// afterwards, and only a read-back size is ever called a pin. Linux
// hands back TWICE what was asked for — its own bookkeeping overhead is
// counted in — and that is the kernel agreeing, not disagreeing, which
// is why the read-back is RECORDED PER LEG in internal/timing rather
// than compared against the request.

// pinnedBuffer is the size both ends of a write-side row's connection
// ask for, in bytes.
//
// SMALL ON PURPOSE, and the size is a consequence of what the gap is
// made of. The store fixture drains at one chunk per pause, so the
// sender is released in steps of however much window the receiver
// advertises at a time — and a receiver advertises a fraction of its
// buffer. A large buffer therefore does not merely make the gap
// variable, it makes it LONG: the sender waits for a fraction of
// megabytes to drain at a few megabytes a second. Pinned small, the gap
// collapses to the fixture's own pacing quantum, which is a quantity
// this repository chose and can reproduce.
//
// IT IS 128 KiB RATHER THAN 64 BECAUSE THE DIFFERENCE WAS MEASURED AND
// THERE WAS NONE. Twenty runs at each on darwin, 2026-09-10: 110.590 ms
// worst at 128 KiB and 107.821 ms at 64 KiB. Under two and a half per
// cent apart across a doubling, which says the buffer has stopped being
// what sets the tail — what is left is the fixture's own pacing quantum
// and the scheduler. So the larger of the two is taken, because it sits
// further from every platform's own minimum: a request that lands under
// a kernel's clamp reads back as a number nobody asked for, and a leg
// that quietly clamped would be measuring a condition the record does
// not describe.
const pinnedBuffer = 128 << 10

// socketPin is one END's record across a run: what was asked for, what
// the kernel gave back, what went wrong, and how many connections it was
// applied to.
//
// THE CONNECTION COUNT IS THE POSITIVE CONTROL. A pin that was never
// applied to anything reports no error and no disagreement — it reports
// nothing at all, which is indistinguishable from a pin that worked
// perfectly unless somebody counts. Every row using one asserts the
// count is not zero.
type socketPin struct {
	mu   sync.Mutex
	pin  timing.Pin
	uses int
}

// applied records one connection's outcome.
//
// THE FIRST FAILURE STICKS. A run whose second connection pinned
// cleanly has still measured a gap over a connection that did not, and
// letting the later success overwrite the earlier failure would turn the
// record into "the last thing that happened" rather than "what this run
// was measured under".
//
// TWO CONNECTIONS DISAGREEING IS ALSO A FAILURE. A pin that means one
// thing on one socket and another on the next is not a condition, and a
// gap measured across both was measured under neither.
func (p *socketPin) applied(requested, readBack int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.uses++
	if p.pin.Err != "" {
		return
	}
	switch {
	case err != nil:
		p.pin = timing.Pin{Requested: requested, Err: err.Error()}
	case p.uses > 1 && p.pin.ReadBack != readBack:
		p.pin = timing.Pin{Requested: requested, Err: fmt.Sprintf(
			"two connections in one run read back different buffer sizes, %d then %d, "+
				"so there is no single condition this run's gap was measured under",
			p.pin.ReadBack, readBack)}
	default:
		p.pin = timing.Pin{Requested: requested, ReadBack: readBack}
	}
}

// record is this end's claim and the number of connections it covers.
func (p *socketPin) record() (timing.Pin, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pin, p.uses
}

// pinBuffer sets one socket buffer and reads it straight back, folding
// every way that can go wrong into the record rather than into a fatal.
//
// IT NEVER FAILS THE RUN. A leg that cannot pin is a leg that pays a
// wider window and a longer row, which is a mode to be recorded and run
// in — not a reason for the suite to stop. The refusal, where one
// belongs, is at the row: a row whose recorded gap was measured under a
// pin this run did not hold says so and reds there.
func pinBuffer(conn net.Conn, size, option int, set func(*net.TCPConn, int) error, into *socketPin) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		into.applied(size, 0, fmt.Errorf(
			"the connection is a %T rather than a TCP connection, so it has no socket "+
				"buffer to pin", conn))
		return
	}
	if err := set(tcp, size); err != nil {
		into.applied(size, 0, fmt.Errorf("asking for a %d-byte buffer: %w", size, err))
		return
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		into.applied(size, 0, fmt.Errorf("reaching the socket to read the buffer back: %w", err))
		return
	}
	var readBack int
	var readErr error
	if err := raw.Control(func(fd uintptr) { readBack, readErr = socketBuffer(fd, option) }); err != nil {
		into.applied(size, 0, fmt.Errorf("controlling the socket to read the buffer back: %w", err))
		return
	}
	if readErr != nil {
		into.applied(size, 0, fmt.Errorf("reading the buffer back: %w", readErr))
		return
	}
	into.applied(size, readBack, nil)
}

// pinnedTransport is a transport whose every connection has its SEND
// buffer pinned, and read back, before a byte of a body reaches it.
//
// NO PROXY, and that is deliberate rather than an omission — it is the
// one way this differs from the transport the shipped client builds, and
// the row beside the seam asserts what production gets. A proxy would
// put the pinned socket between this client and the proxy, and the gap
// would then be governed by a buffer at the far end of somebody else's
// connection. The rows using this dial a fixture on loopback; a runner
// with a proxy variable set must not quietly turn them into a
// measurement of it.
func pinnedTransport(size int) (*http.Transport, *socketPin) {
	pin := &socketPin{}
	dialer := &net.Dialer{}
	return &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			pinBuffer(conn, size, soSendBuffer, (*net.TCPConn).SetWriteBuffer, pin)
			return conn, nil
		},
	}, pin
}

// pinnedListener sets the RECEIVE buffer on every connection the object
// store accepts, and reads it back.
//
// IT WRAPS THE LISTENER rather than reaching into the handler, because
// the handler runs after the request headers have arrived — by which
// time the connection has already been established and the client may
// already be pushing a body into whatever buffer the kernel chose. The
// only moment before that this test can reach is the accept.
//
// The size is read from the store at accept time rather than captured at
// construction, because a row configures its store between building it
// and running anything through it, which is the same order every other
// knob on that fixture is set in.
type pinnedListener struct {
	net.Listener
	store *objectStore
}

func (l *pinnedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.store.mu.Lock()
	size := l.store.pinReceiveBuffer
	l.store.mu.Unlock()
	if size > 0 {
		pinBuffer(conn, size, soReceiveBuffer, (*net.TCPConn).SetReadBuffer, l.store.pin)
	}
	return conn, nil
}

// observedPin folds the two ends into the shape the registry records.
func observedPin(client, fixture *socketPin) *timing.PinnedPair {
	send, _ := client.record()
	receive, _ := fixture.record()
	return &timing.PinnedPair{Send: send, Receive: receive}
}

// pinsWereApplied is the positive control both write-side rows and their
// probes run: a pin nothing was ever applied to is silent, and silence
// here is indistinguishable from success.
//
// REQUIRED MUTATION, RUN 2026-09-10: set the store's pinReceiveBuffer
// back to zero in the slow-upload row, leaving only the client end
// pinned. Reds here, naming the end that was not pinned —
//
//	the store fixture never pinned a receive buffer, so the far end of
//	this connection autotuned freely and the gap measured is that
//	autotuning — the listener wrapper is not the one being accepted on
//
// — and nothing else moves, because a half-pinned run is a run whose gap
// is under a condition nobody recorded rather than a run that fails.
// That is the whole reason this control exists: without it the row goes
// on passing, and the number it is a margin over quietly becomes the far
// end's autotuning.
func pinsWereApplied(t *testing.T, client, fixture *socketPin) {
	t.Helper()
	if _, uses := client.record(); uses == 0 {
		t.Fatal("the client's send buffer was never pinned on any connection, so " +
			"whatever this run measured, it did not measure a pinned socket — the " +
			"transport carrying the body is not the one that was pinned")
	}
	if _, uses := fixture.record(); uses == 0 {
		t.Fatal("the store fixture never pinned a receive buffer, so the far end of " +
			"this connection autotuned freely and the gap measured is that " +
			"autotuning — the listener wrapper is not the one being accepted on")
	}
}

// confirmPin refuses when the pin this run actually held is not the one
// the recorded gap was measured under.
//
// A MEASUREMENT'S CONDITION IS PART OF THE NUMBER, and this is that rule
// with a mechanism. A window is five times a gap; the gap is only that
// number under a stated buffer size, and a run that got a different
// buffer size is running a margin over a quantity nobody measured. It
// compares the READ-BACK and never the request, because the request is
// what was typed and the read-back is what the kernel did with it.
//
// A leg whose record carries no pin at all is running in the other mode
// and there is nothing here to confirm; the sizing and the fixture pay
// for that elsewhere.
//
// REQUIRED MUTATION, RUN 2026-09-10, and it is the one that proves the
// read-back is a read of the SOCKET and not an echo of the request: make
// pinBuffer skip its setsockopt and go straight to the getsockopt. The
// slow-upload row reds with both ends named, carrying this machine's own
// defaults —
//
//	records a gap on darwin measured with the client's SO_SNDBUF reading
//	back 131072 bytes, and this run read back 146988
//	… the store fixture's SO_RCVBUF … this run read back 408300
//
// — which is a row saying, in numbers, that it is running a margin over
// a quantity it does not have. A read-back that echoed the request would
// have reported 131072 twice and passed.
func confirmPin(t *testing.T, entry *timing.Entry, leg timing.Leg, got *timing.PinnedPair) {
	t.Helper()
	recorded := entry.Measurements[leg]
	if !recorded.Measured() || recorded.Pin == nil {
		return
	}
	for _, end := range []struct {
		name  string
		want  timing.Pin
		have  timing.Pin
		which string
	}{
		{"send", recorded.Pin.Send, got.Send, "the client's SO_SNDBUF"},
		{"receive", recorded.Pin.Receive, got.Receive, "the store fixture's SO_RCVBUF"},
	} {
		if end.want.Err != "" {
			// The record says this leg could not pin. This run holding
			// one is a better environment than the record describes,
			// which is a reason to retake the record and not a reason to
			// red — the window is wider than it needs to be, never
			// narrower.
			continue
		}
		if end.have.Err != "" {
			t.Errorf("timing.%s records a gap on %s measured with %s pinned at %d bytes, "+
				"and this run could not pin it at all: %s.\nThe window here is a margin "+
				"over a gap taken under a condition this machine does not have, so it is "+
				"a margin over nothing in particular.",
				entry.Name, leg, end.which, end.want.ReadBack, end.have.Err)
			continue
		}
		if end.have.ReadBack != end.want.ReadBack {
			t.Errorf("timing.%s records a gap on %s measured with %s reading back %d "+
				"bytes, and this run read back %d.\nA measurement's condition is part of "+
				"the number: retake the gap under this kernel's answer rather than "+
				"keeping a margin over a buffer this machine does not give out.",
				entry.Name, leg, end.which, end.want.ReadBack, end.have.ReadBack)
		}
	}
}

// pinnedUpload arms both ends of the connection this run's archive will
// travel over, and hands back the two records.
//
// BOTH ENDS IN ONE CALL, because arming one is worse than arming
// neither: a row that pinned only the client would report a pin, hold a
// read-back, satisfy every assertion about it, and still be measuring
// the far end's autotuning. Two calls is one call somebody forgets.
func (r *deployRun) pinnedUpload(size int) (client, fixture *socketPin) {
	r.t.Helper()
	transport, client := pinnedTransport(size)
	r.t.Cleanup(transport.CloseIdleConnections)
	r.deps.UploadTransport = transport

	r.store.mu.Lock()
	r.store.pinReceiveBuffer = size
	r.store.mu.Unlock()

	return client, r.store.pin
}
