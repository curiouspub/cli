package flow

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

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
// sending and 408,300 receiving: three times the pinned size, on the leg
// with the NARROWEST autotuning range of the three. A Linux receive
// buffer autotunes into the megabytes, so the half-pinned measurement
// there would be further out still.
//
// # WHAT EACH END IS WORTH, MEASURED PER LEG
//
// A standing story about this connection said the pair rule was really a
// SEND-end rule, and it mattered because one of the three legs cannot
// hold a receive pin at all — the story is what would have let that
// leg's numbers stand on a general claim. The answer is measured per leg
// instead, because a claim about three legs made from one leg's
// arithmetic is the thing this whole exercise exists to stop.
//
// THE CONTROL VARIES ONE END. The send buffer is held at the shipped
// size on every arm, read back and confirmed held across every sample
// taken while bodies moved, and only the receive end moves: larger than
// the send buffer, absent, and smaller than it. Twenty runs an arm, both
// of the conditions the gate runs, each arm bounded so that one that
// will not finish is a ROW rather than a dead test binary.
//
// LINUX, 2026-09-10, send read back 262,144 and held:
//
//	receive pinned 256 KiB   178.092 ms (-race)   178.277 ms (plain)
//	receive unpinned          77.047 ms (-race)    76.541 ms (plain)
//	receive pinned 16 KiB    DID NOT COMPLETE: 2 of 20 runs in 4m0s,
//	                         worst so far 880.727 ms (-race),
//	                         880.185 ms (plain)
//
// That is one leg's table and it is written as one leg's. What it says
// about THIS leg is that the receive end is not a detail here: two arms
// whose receive buffers were both larger than the send buffer differ by
// 101.0 ms under the detector and 101.7 ms without it, and the arm at
// the small end does not finish at all. So linux is recorded with the
// PAIR pinned, and its window comes from the gap measured under that
// pair.
//
// WINDOWS is the leg where all three arms complete, and its table is
// below at windowsReceiveControl.
//
// DARWIN is recorded differently and the difference is the honest one:
// its send end is pinned and its receive end is UNPINNABLE, with the
// autotuned range recorded rather than a size claimed. See the section
// below and Pin.Sustained in internal/timing.

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
// # THE CURVE SAYS SMALLER, AND ONE LEG SAYS OTHERWISE
//
// Darwin, one probe, one pass of twenty runs at each size — the worst
// gap between two progress events at the client:
//
//	16 KiB    37.7 ms
//	64 KiB   107.8 ms
//	128 KiB  110-122 ms
//	512 KiB  139.5 ms
//	none     417.9-434.1 ms
//
// The curve SATURATES above about 64 KiB: a doubling from 64 to 128
// moves the tail by under three per cent, because past that point what
// is left is the fixture's own pacing quantum and the scheduler rather
// than the buffer. Below it the buffer is the whole quantity, and on
// that table alone the right answer is the small end — the row measures
// a stall DETECTOR rather than throughput, the window is five times this
// gap by rule, and every upload row spends three windows.
//
// THE TABLE IS ONE LEG'S, AND THE OTHER TWO GET A VOTE. Taken to 16 KiB,
// the hosted linux runner stopped finishing: the upload probe alone ran
// for twenty-five minutes there without completing, in the plain pass as
// well as under the detector, where the same probe takes about a minute
// on this machine and sixty-five seconds in a two-core linux container.
// The previous size — 128 KiB — completed that probe on the same runner
// in about thirty seconds. Two hypotheses were tested and killed: the
// pin size makes no difference to the probe's cost on two cores here
// (59s at 16 KiB, 59s at 128 KiB), and a two-core linux container
// reproduces neither the hang nor anything close to it (the whole
// package in 130s). The MECHANISM on that runner is not identified, and
// an unexplained collapse is not a thing to ship a leg on.
//
// # AND IT IS THE RECEIVE END, WHICH IS THE ONE NEW FACT ABOUT IT
//
// The control above pinned only the RECEIVE end at 16 KiB and left the
// send end at the shipped size, and it reproduced: two runs in four
// minutes against twenty in fifty-seven seconds at a quarter megabyte,
// with a worst gap past 880 ms where the sibling arms sit at 77 and
// 178. Whatever this is, it is not a property of the pair or of the send
// end — it is the far end's receive buffer at the small end of the
// range, on this one runner. That narrows the search for whoever picks
// it up; it does not identify the mechanism, and it is not chased here.
//
// So the size is the one with evidence on all three legs rather than the
// one the curve prefers on one of them. What it costs is real and
// measured — the gap is about three times what 16 KiB gives, and the
// window with it — and the reason to pay it is that a leg which cannot
// finish reports nothing at all, which is worse than a leg that reports
// a wider margin.
//
// # THIS IS RECORDED AS AN OPEN OBSERVATION AND IS DELIBERATELY NOT CHASED
//
// Ruled rather than forgotten, which is the difference between an open
// question and a loose end. Everything known about the collapse is in
// the paragraph above, both hypotheses that were killed included, and
// the honest state is that nobody can name the mechanism. What that
// buys, and the only thing it buys, is the right size: an unexplained
// collapse is not a thing to ship a leg on, so the leg is shipped on the
// size that finishes.
//
// The reason not to chase it further is a budget rather than a lack of
// interest. Each attempt is a run on a hosted runner in both of the
// conditions the gate uses, spent on a question that changes no number
// this repository records — 64 KiB on that runner is untested, and
// testing it would at best move a gap the window already clears by more
// than the rule asks. A round has one question it exists to answer, and
// a run spent here is a run not spent there.
//
// WHOEVER PICKS THIS UP: the measurement that would settle it is 64 KiB
// on that same hosted runner, plain and under the detector, with the
// probe timed rather than merely observed to finish. That is a run
// somebody has to spend rather than a number to pick.
//
// A REQUEST UNDER A KERNEL'S CLAMP READS BACK AS A NUMBER NOBODY ASKED
// FOR, and that danger is handled by a guard rather than by guessing
// high: the coherence rule beside the registry refuses to claim a pin
// whose ReadBack falls outside [Requested, 2*Requested]. Linux doubles,
// and reads back 262144 for this request.
//
// # AND ONLY ONE OF THE TWO ENDS ACTUALLY HOLDS THIS SIZE ON DARWIN
//
// Measured, not assumed, and it is the round's most uncomfortable
// number: the client's SO_SNDBUF reads back what it asked for and is
// still that when the last byte goes out, while the accepting end's
// SO_RCVBUF reads back the same figure and is then moved by the kernel
// up to about 646,000 for the rest of the body. macOS ships
// net.inet.tcp.doautorcvbuf=1 and setting SO_RCVBUF does not turn it
// off. Re-setting the option on every drain step was tried: it held the
// floor and not the ceiling.
//
// THAT USED TO BE FOLLOWED BY A REASON IT DID NOT MATTER — a general
// claim that the send end governs everywhere — and a general claim is
// exactly what this record does not make. What is recorded for this leg
// is the CONDITION: the send end pinned and confirmed, the receive end
// unpinnable, and the range the kernel actually ran it over while the
// body moved. A window sized from a gap taken under that is a window
// sized under a condition that is disclosed rather than claimed.
//
// The record's own per-leg numbers behave like a quantity measured under
// a buffer that moves: this same probe reported 108, 113, 128, 135, 195,
// 208 and 421 ms across seven passes on one machine in one day. What
// follows from that is a ruling rather than a number — see the Pin field
// in internal/timing — and the honest state is that this leg's
// write-side figures are taken under a condition it does not hold still.
// The record says so per leg through Pin.Sustained rather than leaving
// the read-back to be read as a duration; see socketPin.sample.
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

	// option and conns are what a sampler needs to ask this end, later
	// and repeatedly, what its buffer actually is. Every socket this
	// record covers is kept, because "the connection" for a probe is
	// twenty of them.
	option int
	conns  []*net.TCPConn

	// samples, low and high are the buffer's range WHILE BODIES WERE
	// MOVING, which is a different fact from the read-back beside it and
	// on one leg a wildly different one. See sample.
	samples int
	low     int
	high    int
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
// IT NEVER FAILS THE RUN ITSELF, and that is about WHERE the refusal
// belongs rather than about whether there is one. This function is
// reached from a dialler and from an accept loop, neither of which is a
// place a test can usefully fail from: a t.Fatal inside an Accept
// reports on the wrong goroutine and takes the fixture down with it. So
// every way a pin can go wrong is folded into the record, and the row
// reads the record and reds — see pinsWereApplied, and see the Pin field
// in internal/timing for why a leg that cannot pin stops rather than
// paying.
func pinBuffer(conn net.Conn, size, option int, into *socketPin) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		into.applied(size, 0, fmt.Errorf(
			"the connection is a %T rather than a TCP connection, so it has no socket "+
				"buffer to pin", conn))
		return
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		into.applied(size, 0, fmt.Errorf("reaching the socket to pin its buffer: %w", err))
		return
	}
	// THE REQUEST AND THE READ-BACK HAPPEN IN ONE Control CALL, and that
	// is a measurement rather than tidiness. Through the standard
	// library's SetReadBuffer they are several operations apart, and on
	// darwin the kernel's receive autosizing can move the buffer inside
	// that gap: the row asserting that two connections in one run agree
	// saw 16,384 on the first and 277,696 on the second, both having had
	// the same request honoured. Closed to two adjacent syscalls, the
	// read-back is an answer to the request rather than a race with the
	// kernel. It is still only an INSTANT — see socketPin.sample for what
	// happens to that buffer afterwards.
	var readBack int
	var opErr error
	if err := raw.Control(func(fd uintptr) {
		if setErr := setSocketBuffer(fd, option, size); setErr != nil {
			opErr = fmt.Errorf("asking for a %d-byte buffer: %w", size, setErr)
			return
		}
		var readErr error
		readBack, readErr = socketBuffer(fd, option)
		if readErr != nil {
			opErr = fmt.Errorf("reading the buffer back: %w", readErr)
		}
	}); err != nil {
		into.applied(size, 0, fmt.Errorf("controlling the socket to pin its buffer: %w", err))
		return
	}
	// WATCHED EVEN WHEN THE REQUEST FAILED, and that is the refined stop
	// rule's other half rather than tidiness. A receive pin that cannot
	// be applied is RECORDED AS THE CONDITION and the measurement
	// proceeds under it — so the thing that has to be recorded is what
	// the buffer actually was over the body, which is precisely the
	// number a socket nobody sampled cannot supply. Registering only on
	// success meant a leg running in that mode reported an error and no
	// range: the condition named and not measured.
	//
	// MEASURED, not reasoned: with the receive setsockopt made to fail,
	// the row reddened on the sampler's own positive control — "was
	// never sampled while a body was moving" — which is that control
	// correctly reporting that the record would have been half a fact.
	into.watching(tcp, option)
	if opErr != nil {
		into.applied(size, 0, opErr)
		return
	}
	into.applied(size, readBack, nil)
}

// watching remembers a socket so it can be asked again later.
func (p *socketPin) watching(conn *net.TCPConn, option int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.option = option
	p.conns = append(p.conns, conn)
}

// sample asks every socket this record covers what its buffer is NOW.
//
// # THE READ-BACK IS AN INSTANT AND THE CONDITION IS A DURATION
//
// A pin is set at accept or at dial and read back one syscall later, and
// that read-back is the whole evidence round 2 recorded. It proves the
// kernel agreed at that instant. It says nothing about the seconds
// afterwards, during which the body actually moves and the gap being
// measured actually happens — and on one of this project's three legs
// the two answers are not close.
//
// MEASURED on darwin, 2026-09-10, against a 16,384-byte request at both
// ends: the client's SO_SNDBUF read back 16,384 and was still 16,384
// when the last byte left, while the accepting end's SO_RCVBUF read back
// 16,384 and then sat between 331,996 and 539,008 for every sample after
// the first two hundred milliseconds. macOS ships
// net.inet.tcp.doautorcvbuf=1, and setting SO_RCVBUF does not clear it:
// re-setting the option on every drain step held the floor at the
// requested size and moved the ceiling not at all.
//
// So this exists to turn "measured under a 16 KiB receive buffer" from a
// sentence into a range a reader can check. A socket that has been
// closed answers with an error and is skipped rather than counted, since
// a shut connection's buffer is not a condition anything was measured
// under.
// A SOCKET THAT STOPS ANSWERING IS DROPPED, and that is a cost bound
// rather than tidiness. A probe opens a connection per run and this
// record covers every one of them, so a list that only ever grows makes
// each tick cost more than the last: twenty runs leave twenty sockets,
// nineteen of them closed, and the sampler pays a descriptor lock and a
// failed syscall for each, forty times a second, for the whole probe.
//
// MEASURED, on the machine least able to absorb it. internal/flow
// completed on a two-core hosted linux runner in about ninety seconds
// before this sampler existed and TIMED OUT at six hundred there with
// it, having produced no measurement at all — the run spent, and nothing
// brought back. Dropping the dead sockets keeps the list at the one or
// two that are live and the tick at constant cost.
func (p *socketPin) sample() {
	p.mu.Lock()
	conns := append([]*net.TCPConn(nil), p.conns...)
	option := p.option
	p.mu.Unlock()

	var sizes []int
	var dead []*net.TCPConn
	for _, conn := range conns {
		size, ok := currentBuffer(conn, option)
		if !ok {
			dead = append(dead, conn)
			continue
		}
		sizes = append(sizes, size)
	}

	p.mu.Lock()
	for _, size := range sizes {
		if p.samples == 0 || size < p.low {
			p.low = size
		}
		if size > p.high {
			p.high = size
		}
		p.samples++
	}
	if len(dead) > 0 {
		gone := make(map[*net.TCPConn]bool, len(dead))
		for _, conn := range dead {
			gone[conn] = true
		}
		// Rebuilt rather than truncated, because watching may have
		// appended a fresh socket while this tick was reading the old
		// ones and a live connection dropped here would be a hole in
		// exactly the record this exists to keep.
		kept := p.conns[:0]
		for _, conn := range p.conns {
			if !gone[conn] {
				kept = append(kept, conn)
			}
		}
		p.conns = kept
	}
	p.mu.Unlock()
}

// currentBuffer reads one socket's buffer now, and reports false when
// the socket can no longer answer — which is what a closed connection
// does, and which is not a measurement.
func currentBuffer(conn *net.TCPConn, option int) (int, bool) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, false
	}
	var size int
	var readErr error
	if err := raw.Control(func(fd uintptr) { size, readErr = socketBuffer(fd, option) }); err != nil {
		return 0, false
	}
	if readErr != nil || size <= 0 {
		return 0, false
	}
	return size, true
}

// watch samples both this end's sockets every step until the row ends.
//
// A GOROUTINE RATHER THAN A HOOK IN THE READER, because the quantity is
// a property of the socket over time and not of any byte passing
// through it — and because the two ends have to be sampled the same way
// for their answers to be comparable. The step is the store fixture's
// own pacing quantum: sampling faster would say nothing more, and
// sampling slower could step over the very excursion being looked for.
func (p *socketPin) watch(t *testing.T, step time.Duration) {
	t.Helper()
	done := make(chan struct{})
	var stopped sync.WaitGroup
	stopped.Add(1)
	go func() {
		defer stopped.Done()
		ticker := time.NewTicker(step)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				p.sample()
			}
		}
	}()
	t.Cleanup(func() {
		close(done)
		stopped.Wait()
	})
}

// sustained is the range this end's buffer was observed at, or nil when
// nobody sampled it.
func (p *socketPin) sustained() *timing.Sustained {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.samples == 0 {
		return nil
	}
	return &timing.Sustained{Samples: p.samples, Low: p.low, High: p.high}
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
			pinBuffer(conn, size, soSendBuffer, pin)
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
// THE SIZE IS THE WRAPPER'S OWN, FIXED BEFORE IT IS INSTALLED, and that
// is round 2's defect removed. It used to be read off the store at
// accept time, on the argument that a row configures its fixture between
// building it and running anything through it — which is true of every
// other knob on that fixture and false of this one. The other knobs are
// read by a HANDLER, which cannot run until a connection exists; this
// one is read by the ACCEPT, which is the moment a connection begins. So
// the store was already listening while the size was still zero, and a
// connection arriving in that gap autotuned and was counted into the
// same record as the pinned ones. Written once, before the wrapper goes
// into the server, there is nothing to read at accept time and nothing
// for a mutex to protect: the field is immutable for the listener's
// whole life.
type pinnedListener struct {
	net.Listener
	// size is the SO_RCVBUF every accepted connection is pinned to, and
	// zero pins nothing at all.
	size int
	// pin is the store's record, shared so that every connection this
	// listener accepts accumulates into one claim about the run.
	pin *socketPin
}

func (l *pinnedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if l.size > 0 {
		pinBuffer(conn, l.size, soReceiveBuffer, l.pin)
	}
	return conn, nil
}

// observedPin folds the two ends into the shape the registry records,
// each with the range its buffer was actually observed at.
func observedPin(client, fixture *socketPin) *timing.PinnedPair {
	send, _ := client.record()
	receive, _ := fixture.record()
	send.Sustained = client.sustained()
	receive.Sustained = fixture.sustained()
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
//
// REQUIRED MUTATION FOR THE STOP, RUN 2026-09-10: make pinBuffer's
// setsockopt fail with "operation not permitted". The slow-upload row
// reds here rather than running on:
//
//	send NOT PINNED (asking for a 131072-byte buffer: operation not
//	permitted). This row's window is a margin over a gap measured with
//	this client's own send buffer held at a stated size …
//
// REQUIRED MUTATION FOR THE OTHER HALF OF THAT RULE, RUN 2026-09-10 IN
// TWO PARTS, because the rule turns on what the RECORD says and one part
// cannot show that.
//
//  1. Make the setsockopt fail on the RECEIVE end alone, on a leg whose
//     record already says it cannot pin that end. The row does NOT stop:
//     it logs the condition, measures under it, and PASSES. That is the
//     refined rule working — the gate is green on that leg because its
//     row is true, not because the rule bent.
//  2. The same mutation with that leg's record edited to CLAIM a held
//     receive pin. Now it reds, one function down in confirmPin:
//     "records a gap on darwin measured with the store fixture's
//     SO_RCVBUF pinned at 131072 bytes, and this run could not pin it at
//     all". A leg that was pinning and has stopped is a different event
//     from a leg that never could, and the record is the only thing that
//     can tell them apart.
//
// AND THE FIRST PART REDDENED SOMEWHERE THE PREDICTION DID NOT NAME, on
// the sampler's own positive control: "was never sampled while a body
// was moving". pinBuffer registered a socket with the sampler only after
// a successful request, so a leg running in the cannot-pin mode reported
// an error and NO RANGE — the condition named and not measured, which is
// exactly the half this record needs most. Fixed there rather than
// waived here.
//
// REQUIRED MUTATION FOR THE SAMPLER'S CONTROL, RUN 2026-09-10: drop the
// two watch calls from pinnedUploadRun. Both ends red, each saying it
// "was never sampled while a body was moving" — which is the difference
// between a run that watched the buffer hold and one that never looked.
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

	// THE STOP RULE, AND IT IS ABOUT THE SEND END. A run stops when the
	// SEND pin fails, because that is the end this repository sets on
	// the socket it owns and a failure there means the condition the
	// whole measurement is named for was never established.
	//
	// A RECEIVE PIN THAT CANNOT BE APPLIED IS THE CONDITION, NOT A STOP,
	// and that is a refinement rather than a loosening. One of the three
	// legs has a kernel that moves an accepted socket's receive buffer
	// whatever SO_RCVBUF asked for; there is nothing this suite can do
	// about that, and there is nothing for that leg to pay with either —
	// pacingFor refuses above a window of about 2.47 seconds, because
	// the body it would need exceeds this product's own 30 MB input
	// limit, and that limit is the product's rather than the test's. So
	// the failure is RECORDED as the condition the gap was taken under,
	// the range the buffer actually ran over is sampled and written down
	// beside it, and the measurement proceeds. The gate is green on that
	// leg because its row is TRUE as written, not because the rule bent
	// for it.
	//
	// WHAT STILL REDS ON THE RECEIVE END is a pin the RECORD CLAIMS is
	// held and this run could not hold — a leg that was pinning and has
	// stopped, or two connections in one run disagreeing about a size the
	// record says is one condition. That comparison needs the record, so
	// it lives in confirmPin, one function down, where the record is in
	// hand.
	if got, _ := client.record(); got.Err != "" {
		t.Fatalf("send NOT PINNED (%s).\nThis row's window is a margin over a gap "+
			"measured with this client's own send buffer held at a stated size, and "+
			"this run does not have it. That is the end this repository sets on a "+
			"socket it owns, so a failure here is not a kernel's answer — it is the "+
			"condition never having been established at all.", got.Err)
	}
	if got, _ := fixture.record(); got.Err != "" {
		// LOGGED RATHER THAN SWALLOWED. A condition that is disclosed
		// only in a package-level record is a condition nobody reading a
		// run's output can see, and this is the line that says which of
		// the two modes this particular run was in.
		t.Logf("receive NOT PINNED (%s) — recorded as this leg's condition, and the "+
			"gap below is measured under it rather than under a size anybody chose",
			got.Err)
	}

	// THE SAMPLER'S OWN POSITIVE CONTROL. Everything the record now says
	// about a buffer HOLDING is a claim about samples taken while a body
	// moved, and a sampler that never fired reports the same silence as
	// one that fired and found nothing to report.
	for _, end := range []struct {
		which string
		pin   *socketPin
	}{{"the client's SO_SNDBUF", client}, {"the store fixture's SO_RCVBUF", fixture}} {
		if s := end.pin.sustained(); s == nil || s.Samples == 0 {
			t.Errorf("%s was never sampled while a body was moving, so this run can "+
				"say what the kernel answered at the instant it was asked and nothing "+
				"about the seconds the gap was measured over", end.which)
		}
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

// pinnedUploadRun builds a deploy run whose archive travels over a
// connection pinned at BOTH ends, and hands back the two records.
//
// BOTH ENDS IN ONE CALL, because arming one is worse than arming
// neither: a row that pinned only the client would report a pin, hold a
// read-back, satisfy every assertion about it, and still be measuring
// the far end's autotuning. Two calls is one call somebody forgets.
//
// IT IS A CONSTRUCTOR AND NOT A METHOD. As a method it could only run
// on a store that was already
// listening, so the receive pin was necessarily a thing set after the
// fact; as a constructor it hands the size to the store's own
// constructor and the listener is wrapped with it in place. The client
// end has no such constraint — a transport pins at dial, and nothing has
// dialled yet — but it is armed here anyway so that the pair is one act.
func pinnedUploadRun(t *testing.T, root string, size int) (run *deployRun, client, fixture *socketPin) {
	t.Helper()
	run = newDeployRunPinnedAt(t, root, size)
	transport, client := pinnedTransport(size)
	t.Cleanup(transport.CloseIdleConnections)
	run.deps.UploadTransport = transport
	// BOTH ENDS ARE WATCHED, not only set. A read-back proves the kernel
	// agreed at the instant it was asked; the gap this row is about
	// happens over the seconds afterwards, and on darwin one of the two
	// ends does not stay where it was put. See socketPin.sample.
	client.watch(t, pacedPause)
	run.store.pin.watch(t, pacedPause)
	return run, client, run.store.pin
}

// TestEveryConnectionOneRunAcceptsReadsBackTheSameBuffer protects a
// measurement's CONDITION rather than a measurement.
//
// A window is five times a gap, and a gap is that number only under the
// buffers it was taken over. A run whose connections were not all pinned
// the same way has no single condition at all: its worst gap might have
// come from the pinned connection or from the one that autotuned, and
// nothing in the number says which. That is not a theoretical hole. It
// is what round 2 shipped, and what round 2's own runtime refusal
// reported when the suite was finally run under -race —
//
//	receive NOT PINNED (two connections in one run read back different
//	buffer sizes, 392384 then 131072, so there is no single condition
//	this run's gap was measured under)
//
// — with a worst gap of 267.2 ms against a record of 154.4 ms, because
// the connection that autotuned was three times the pinned size and the
// gap is a fraction of the buffer divided by the drain rate.
//
// THE RUNTIME REFUSAL STAYS, and this row is not a replacement for it.
// They answer different questions. The refusal in socketPin.applied is a
// property of every run of every write-side row, on every leg, including
// the ones nobody is looking at; this row is a property of the FIXTURE,
// checked once, cheaply, where a reader can see what is being claimed.
// A kernel that clamps one connection and not the next would be caught
// by the first and not by the second; a constructor that lets a
// connection in before the pin exists is caught by both, which is how
// the defect was found.
//
// REQUIRED MUTATIONS, RUN 2026-09-10 — three, and the third is the one
// worth reading, because it did NOT do what the round predicted.
//
//  1. Make the listener skip the pin on its FIRST accepted connection.
//     Reds on the count, which is the positive control doing its job:
//
//     the store accepted 1 connection(s) and this row made 2, so either
//     a connection was never accepted or two requests shared one — and
//     a row that pins nothing reports exactly what a row that pins
//     everything does
//
//  2. Pin the first connection to 392,384 — round 2's own reported
//     number — and the rest to the pinned size. Reds through the
//     runtime refusal, naming both: "two connections in one run read
//     back different buffer sizes, 392384 then …".
//
//  3. PUT THE SIZE BACK WHERE ROUND 2 HAD IT: a field on the store,
//     written after the server has started, read under the store's
//     mutex at accept. This row STAYED GREEN, and so did the upload row
//     beside it, and the race detector said nothing. That is not a hole
//     in this row — it is a correction to the diagnosis. Round 2's write
//     through pinnedUpload took the store's mutex and the accept-side
//     read took it too, so that path was synchronised; and in every call
//     site the assignment happens before anything dials the store, so no
//     connection was ever accepted in the gap. The race the detector
//     really reported was one line further out — the PROBE assigned the
//     same field with no lock at all (stall_probe_test.go, round 2) —
//     and that one reproduces on the round-2 tree every time.
//
// So the parameterisation is right and it removes a real race, and the
// symptom quoted above it has a SECOND cause that the parameterisation
// does not touch: darwin moves an accepted socket's receive buffer on
// its own, so a read-back taken two syscalls after the request can
// already be the kernel's number rather than ours. That is why this
// round also samples the buffer while the body moves rather than trusting
// the read-back. See socketPin.sample.
func TestEveryConnectionOneRunAcceptsReadsBackTheSameBuffer(t *testing.T) {
	store := newObjectStore(t, &deployJournal{}, pinnedBuffer)
	store.acceptAnyLength = true

	transport, client := pinnedTransport(pinnedBuffer)
	t.Cleanup(transport.CloseIdleConnections)
	httpClient := &http.Client{Transport: transport}

	// TWO, because one connection cannot disagree with anything. The
	// idle connection is closed between them so the second request has
	// to dial again: a keep-alive reuse would make this row a test of
	// one socket wearing the name of two.
	const connections = 2
	for i := 0; i < connections; i++ {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, store.url,
			strings.NewReader("a body small enough to be about the socket and not the transfer"))
		if err != nil {
			t.Fatalf("building request %d: %v", i+1, err)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		transport.CloseIdleConnections()
	}

	// THE COUNT FIRST, and it is this row's positive control. A pin
	// applied to nothing reports no error and no disagreement — it
	// reports nothing at all, which reads exactly like a pin that worked
	// on every connection there was.
	receive, receiveUses := store.pin.record()
	if receiveUses != connections {
		t.Fatalf("the store accepted %d connection(s) and this row made %d, so either "+
			"a connection was never accepted or two requests shared one — and a row "+
			"that pins nothing reports exactly what a row that pins everything does",
			receiveUses, connections)
	}
	send, sendUses := client.record()
	if sendUses != connections {
		t.Fatalf("the client dialled %d time(s) and this row made %d requests, so the "+
			"send end was not pinned once per connection", sendUses, connections)
	}

	for _, end := range []struct {
		which string
		pin   timing.Pin
	}{{"the store fixture's SO_RCVBUF", receive}, {"the client's SO_SNDBUF", send}} {
		if end.pin.Err != "" {
			t.Errorf("%s: %s.\nThe two connections this row opened were not one "+
				"condition, so a gap measured across them would be a margin over "+
				"whichever of the two happened to be slower.", end.which, end.pin.Err)
			continue
		}
		if end.pin.ReadBack <= 0 {
			t.Errorf("%s asked for %d bytes and read nothing back, so this row proved "+
				"the two connections agreed about a size neither of them has",
				end.which, end.pin.Requested)
		}
	}
	t.Logf("two connections, one condition: send asked %d read back %d; receive asked "+
		"%d read back %d", send.Requested, send.ReadBack, receive.Requested, receive.ReadBack)

	// THE DETECTOR'S OWN BENCH. Run only over a fixture that agrees, the
	// checks above can be seen to say yes and never to say no — and a
	// socketPin that recorded whatever it was told last would pass every
	// one of them while being exactly the instrument this round exists
	// to replace.
	agree := &socketPin{}
	agree.applied(pinnedBuffer, pinnedBuffer, nil)
	agree.applied(pinnedBuffer, pinnedBuffer, nil)
	if p, n := agree.record(); p.Err != "" || n != 2 {
		t.Errorf("two connections reading back the same size were recorded as a "+
			"failure (%q over %d uses), so this row refuses everything", p.Err, n)
	}
	disagree := &socketPin{}
	disagree.applied(pinnedBuffer, 392384, nil)
	disagree.applied(pinnedBuffer, pinnedBuffer, nil)
	if p, _ := disagree.record(); p.Err == "" {
		t.Errorf("two connections reading back 392384 and %d were recorded as one "+
			"condition, which is the round-2 defect passing its own guard", pinnedBuffer)
	}
}
