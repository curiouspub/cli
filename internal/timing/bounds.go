package timing

import (
	"time"

	"github.com/curiouspub/cli/internal/api"
)

// Bound is a limit the shipped client enforces on one phase of opening a
// connection: how long it waits to connect, to finish the TLS handshake,
// or for the server's response headers.
//
// # Why these live beside the stall windows
//
// The build log is read in phases, and each phase has exactly one named
// bound. A stall window bounds a silence once the response has arrived;
// these three bound everything before it. They are recorded here, with
// the reason each number was chosen, for the reason the windows are: a
// constant carried to the place that uses it carries its number and not
// the reason, and a number nobody can trace is a number nobody can move.
//
// # They are not windows, and the registry treats them apart
//
// An Entry is a margin over a measured gap, and it is refused without a
// measurement on every leg. None of these has been measured: they were
// ruled as starting points, and each says so. They become measurements
// the way the windows did, and until then Provisional stays true and the
// guard beside this file requires the reason to say what they rest on.
//
// # The numbers are the client's, and this is their record
//
// Unlike the windows, these values are SHIPPED: the client's transport
// enforces them. This registry is kept out of the shipped binary, because
// it names test rows and carries measurement provenance nobody who
// downloads the client should find inside it. So the numbers live as the
// client's own constants, and each Bound here READS its constant rather
// than restating it: one home per number, and the reason beside it here.
type Bound struct {
	// Name must equal the key the bound is registered under and the
	// identifier it is declared as.
	Name string

	// Value is the bound itself.
	Value time.Duration

	// Governs names the interval the bound ends, stated as the phase of a
	// request rather than as the knob that sets it.
	Governs string

	// Reason is why this number and not another. For a provisional bound
	// it says so and says what it rests on.
	Reason string

	// Provisional is true until the bound is re-ruled from a measurement.
	Provisional bool
}

// Connect bounds establishing the TCP connection.
var Connect = Bound{
	Name:    "Connect",
	Value:   api.DefaultConnectTimeout,
	Governs: "dialling the server, from the request leaving to the TCP connection being established",
	Reason: "provisional, ruled 2026-09-29 as a starting point: long enough for a slow or " +
		"distant network to connect, short enough that a host that will never answer is " +
		"reported as unreachable rather than left hanging. Not yet measured; re-ruled from " +
		"measurement.",
	Provisional: true,
}

// TLSHandshake bounds the TLS handshake once the connection is up.
var TLSHandshake = Bound{
	Name:    "TLSHandshake",
	Value:   api.DefaultTLSHandshakeTimeout,
	Governs: "the TLS handshake, from the connection being established to the secure channel being ready",
	Reason: "provisional, ruled 2026-09-29 as a starting point, and the figure the standard " +
		"library's own default transport uses. A custom transport does not inherit it, so " +
		"until it was set this phase had no bound at all. Not yet measured; re-ruled from " +
		"measurement.",
	Provisional: true,
}

// ResponseHeaders bounds the wait for the server's response headers once
// the request has been written.
var ResponseHeaders = Bound{
	Name:    "ResponseHeaders",
	Value:   api.DefaultResponseHeaderTimeout,
	Governs: "the server answering, from the request being written to its response headers arriving",
	Reason: "provisional, ruled 2026-09-29 as a starting point, equal to the client's existing " +
		"per-request total so no ordinary call is bounded more tightly than before. For the " +
		"build log it is the bound on a server that accepts and never answers, which the " +
		"stall window covered only while it was armed before the connection opened. Not yet " +
		"measured; re-ruled from measurement.",
	Provisional: true,
}

// Bounds is every connection bound, by name.
var Bounds = map[string]*Bound{
	"Connect":         &Connect,
	"TLSHandshake":    &TLSHandshake,
	"ResponseHeaders": &ResponseHeaders,
}
