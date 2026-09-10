// Package timing holds every stall window this repository's tests turn
// on, and the evidence behind each one.
//
// # Why the windows live here rather than beside the rows they govern
//
// A stall window is a MARGIN, and a margin is only as good as the
// measurement it was sized from. Written inline, a window is a number in
// a test file: the next reader sees the value and not the quantity it
// bounds, the machine it was measured on, or whether it was measured at
// all. Five of these existed in two files, and exactly one of them had a
// measurement behind it — the other four were numbers somebody chose,
// and one of those four was sized against the fixture's own pacing knob,
// which is the quantity the row does NOT depend on.
//
// A constant carried between two places carries its number, not the
// reason the number was chosen. So the number and the reason are stored
// together, and a guard beside this package refuses a stall window that
// is not here.
//
// # Not internal/testing
//
// A package of that name sitting beside the standard library's `testing`
// is a confusion nobody needs, and this one is about time rather than
// about tests.
//
// # How the numbers in here were taken
//
// Each leg's number is the worst gap seen over ONE HUNDRED consecutive
// runs of that entry's probe — five passes of twenty, the last of which
// ran with the whole suite in parallel, because a margin measured on an
// idle machine is not the margin a gate has. The probes live beside the rows
// they measure, in internal/flow, and reproduce each row's own fixture
// rather than a cheaper approximation of it: a shorter transfer over a
// connection kept warm answered a different question by 160 ms.
//
// A block point is likewise the LARGEST seen, because the danger it
// guards against is a fixture too small to make the client block, and
// the largest block point is the one a fixture has to clear.
//
// # A WRITE-SIDE NUMBER IS ALSO TAKEN UNDER A PIN
//
// The gap a write-side window bounds is set by socket buffers at BOTH
// ends of the connection, and both kernels grow those as a connection
// carries traffic. So the write-side probes pin them — the client's
// SO_SNDBUF through a dialler, the fixture's SO_RCVBUF on every
// connection it accepts — and read each one back with getsockopt,
// because setsockopt may clamp, round, double or ignore a request and
// says so nowhere. The pin is recorded beside the gap, per leg, and it
// is part of the number: a run whose sockets read back a different size
// is not a run that beat the record, it is a run under a different
// condition, and the rows say so rather than comparing the two figures.
//
// What the pin bought here was measured rather than assumed: on darwin
// it took the worst gap from 434.081 ms to 154.359 ms, and halving the
// pinned size again moved it by under three per cent. The buffer was
// most of the quantity and never all of it.
//
// # This package is imported by test files only
//
// It compiles as ordinary code so that a test in any package can import
// it, and nothing outside a _test.go file does — which is asserted
// rather than hoped for, so no registry of test margins reaches a
// shipped binary.
package timing

import "time"

// Leg is one of the three operating systems this project's gate runs.
// Socket buffer sizes, their autotuning and scheduling granularity all
// belong to the kernel and the runner, so a number measured on one leg
// says nothing about the other two.
type Leg string

const (
	Linux   Leg = "linux"
	Darwin  Leg = "darwin"
	Windows Leg = "windows"
)

// Legs is every leg an entry must carry a measurement for. It is the set
// the gate runs, and a window measured on fewer of them is a window
// whose margin is unknown where it is not measured.
var Legs = []Leg{Linux, Darwin, Windows}

// Side is which end of the connection the client is on, and it decides
// what the gap between two progress events is made of. The two are
// separate quantities measured by separate methods, and one measurement
// can never describe both.
type Side string

const (
	// Write: the client is pushing a body. The gap between two progress
	// events is the time for the kernel's send buffer to free space,
	// which is governed by how fast the far end reads. A write side has
	// a BLOCK POINT — the bytes handed over before the client stops
	// making progress at all.
	Write Side = "write"

	// Read: the client is consuming a stream. The gap between two
	// progress events is how long the far end waits before writing more,
	// plus whatever the scheduler adds. Nothing is buffering on this
	// client's behalf, so a read side has NO block point and asking for
	// one would be meaningless.
	Read Side = "read"
)

// Pin is ONE END's claim about a socket buffer: the size that was asked
// for, the size the kernel reported back when asked, and what went wrong
// if anything did.
//
// # A PIN IS A CLAIM, SO IT IS READ BACK
//
// setsockopt is free to clamp a request, to round it, to double it, or —
// under a sandbox or a hardened kernel — to accept it and do nothing. It
// reports none of that: the call returns success and the socket keeps
// whatever buffer the kernel decided it should have. So a window sized
// from a gap "measured under a 128 KiB buffer" can perfectly well have
// been measured under a buffer nobody chose, and nothing anywhere would
// say so. Setting the option proves only that the request was made;
// getsockopt afterwards is what proves the kernel agreed.
//
// # LINUX REPORTS THE DOUBLED VALUE, AND THAT IS NOT DISAGREEMENT
//
// A Linux kernel stores twice what SO_SNDBUF/SO_RCVBUF asked for — the
// second half is its own bookkeeping overhead — and getsockopt hands
// that doubled number straight back. So on Linux ReadBack == 2*Requested
// is the kernel HONOURING the request, and treating it as a refusal
// would throw away the only leg where the pin is most certainly applied.
// Darwin and Windows report back what was asked for, give or take their
// own clamping. That is why ReadBack is RECORDED PER LEG rather than
// compared against Requested: the relation between the two is the
// kernel's business, and the only thing that has to hold is that the run
// which took the gap and the run reading this record saw the SAME
// ReadBack.
//
// # AND THE WINDOW IS COMPUTED FROM THE GAP UNDER ReadBack
//
// Never from Requested. Requested is what was typed; ReadBack is the
// condition the number was measured in, and a measurement's condition is
// part of the number.
type Pin struct {
	// Requested is the buffer size handed to setsockopt, in bytes.
	Requested int

	// ReadBack is what getsockopt reported afterwards, in bytes. Zero
	// means nothing was read back, which is the same as no pin at all —
	// see Held.
	ReadBack int

	// Err is the failure, rendered, or "" when there was none. It is a
	// STRING rather than an error because this is a transcription of
	// what a run observed, written out by hand into a record — an error
	// value in a package-level literal would be a live object standing
	// in for a thing that happened once, on a machine, in the past.
	Err string
}

// Held reports whether this end really was pinned: the kernel answered,
// and it answered with a size. A Pin carrying an Err is not a weaker
// pin, it is the record of a leg that could not pin — which is a
// legitimate thing to write down and a different mode to run in.
func (p Pin) Held() bool { return p.Err == "" && p.ReadBack > 0 }

// PinnedPair is BOTH ENDS of the connection a write-side gap was
// measured over, and the pair is the unit because a socket option has an
// END and "the test connection" names two sockets.
//
// An upload has the client SENDING and the fixture RECEIVING. The gap
// being bounded is the time for the client's send buffer to free space,
// and that is governed by how fast the far end drains — which is set by
// the RECEIVE buffer at the other end and by how often that end can
// advertise a larger window. Pin only the sender and the number measured
// is the receiver's autotuning; pin only the receiver and it is the
// sender's. Neither half is the measurement.
//
// The two Pins carry the same Requested by construction here, and are
// still recorded separately, because each end's kernel answers for
// itself.
type PinnedPair struct {
	// Send is the CLIENT's SO_SNDBUF — the end this repository ships.
	Send Pin

	// Receive is the FIXTURE's SO_RCVBUF, set on every connection the
	// object-store double accepts.
	Receive Pin
}

// Held reports whether both ends were really pinned. A pair with one end
// held is not half a pin; it is a gap measured against the other end's
// autotuning, which is the thing the pair exists to remove.
func (p *PinnedPair) Held() bool {
	return p != nil && p.Send.Held() && p.Receive.Held()
}

// Measurement is one leg's evidence for one window: the worst gap
// observed, over how many runs, on what date, and — on the write side —
// the socket buffers it was observed under.
//
// A ZERO VALUE IS "NOT MEASURED", never "measured at zero". That
// distinction is the whole point of the type: an absent number and a
// number somebody chose look identical once they are both durations, and
// the guard beside this package can only refuse the first if the two are
// distinguishable.
type Measurement struct {
	// WorstGap is the longest interval observed between two consecutive
	// progress events at the client, over every run.
	WorstGap time.Duration

	// Runs is how many runs stand behind WorstGap.
	Runs int

	// Date is when they were taken, as YYYY-MM-DD. A measurement with no
	// date cannot be judged stale.
	Date string

	// BlockPoint is the bytes the client handed over before it stopped
	// making progress at all. WRITE SIDE ONLY: on the read side nothing
	// is buffering on this client's behalf, so there is no such number
	// and a value here would be an invention.
	BlockPoint int64

	// Pin is the socket-buffer pair WorstGap was measured under, and it
	// says which MODE this leg is in. WRITE SIDE ONLY, for the same
	// reason BlockPoint is: on the read side the gap is the far end's
	// pacing plus the scheduler, and no buffer this client can set
	// governs it.
	//
	// A nil Pin on a write-side measurement is not "unpinned", it is
	// NOBODY SAID — and the guard reds on it, because the cost of the
	// row on that leg depends on the answer. A Pin present with an Err
	// is the honest record of a leg that tried and could not, and that
	// leg pays: its window is a margin over an autotuned buffer, which
	// is large, and its fixture has to be large enough to spend three of
	// them.
	Pin *PinnedPair
}

// Measured reports whether this leg has evidence behind it. All three
// fields are required: a duration with no run count is one sample, and a
// number with no date cannot be judged stale.
func (m Measurement) Measured() bool {
	return m.WorstGap > 0 && m.Runs > 0 && m.Date != ""
}

// Pinned reports whether this leg's gap was measured over a connection
// whose buffers were pinned at BOTH ends and confirmed by reading them
// back. It is the question "which mode is this leg in", and everything
// about the cost of the row it stands behind follows from the answer.
func (m Measurement) Pinned() bool { return m.Pin.Held() }

// Carried records that an entry's measurement came from another entry
// rather than from a run of its own.
//
// CARRYING IS ALLOWED WITH ITS REASON, and only with it. Rows sharing a
// mechanism may share a measurement — the argument for why it transfers
// is how a measurement is meant to be used. Carrying the number alone is
// the defect this whole package exists against, so an entry that names a
// source and gives no reason is refused.
type Carried struct {
	// From is the Name of the entry the numbers came from.
	From string

	// Reason is why the measurement transfers: the same side, the same
	// reader, and what makes the two rows one mechanism. "The same as X"
	// is a citation of a reason that was about something else, so a
	// reason that says only that is no reason.
	Reason string
}

// Entry is one stall window and everything known about it.
type Entry struct {
	// Name is this entry's own name, and it must equal the key it is
	// registered under and the identifier it is declared as. The guard
	// resolves a test's `timing.Name.Window` to this.
	Name string

	// Row is the test this window governs, named so a reader of the
	// registry can go and read the row, and a reader of the row can find
	// the evidence.
	Row string

	// Window is the stall window itself.
	Window time.Duration

	// Side is which mechanism this window bounds.
	Side Side

	// Governs names the quantity the gap is actually set by — not the
	// knob the fixture turns. Sizing a window against the visible
	// quantity rather than the governing one is how a timing row becomes
	// a flake with a schedule.
	Governs string

	// Instrument is the progress reader the measurement is taken
	// through. A measurement may only be carried between rows on the
	// same side, through the same reader.
	Instrument string

	// Measurements is the per-leg evidence. A leg absent from this map,
	// or present with a zero Measurement, is UNMEASURED and the guard
	// reds on it.
	Measurements map[Leg]Measurement

	// SetBy is the leg whose number sized this window: the slowest of
	// the measured legs. It is checked rather than decorative — an entry
	// naming a leg that is not its slowest is refused.
	SetBy Leg

	// Carried is non-nil when this entry's numbers came from another
	// entry rather than from its own run.
	Carried *Carried
}

// MinimumMargin is the sizing rule, and it lives at the registry because
// this is where the numbers are.
//
// A window is at least FIVE TIMES the measured worst gap on the SLOWEST
// leg — not the average, and not the machine the author happens to be
// sitting at. The margin that failed one run in six was twelve times the
// fixture's own pacing knob and about twice the quantity the row
// actually depended on; a multiple of the governing quantity is the only
// multiple that means anything.
const MinimumMargin = 5

// UploadSlowIsNotStalled bounds the row that proves a slow upload is not
// a stalled one: the store consumes the body at a pace that makes the
// whole upload span several windows while never letting the gap between
// two bytes reach one.
//
// # THE ROUND BEFORE THIS ONE LEFT IT UNDER THE RULE, AND THE PIN IS WHAT SETTLED IT
//
// The state this replaced: 434.081 ms measured on darwin against a
// 600 ms window — a margin of 1.4 where the rule asks for 5 — and a
// refusal to raise the window, because the row ALSO asserts it spent at
// least three windows uploading, and at 5x that meant a 6.5 s transfer
// of about 17 MB. The two constraints were set against each other by one
// kernel buffer, and which of them gave was recorded as a ruling nobody
// had made.
//
// NEITHER GAVE, BECAUSE THE QUANTITY WAS WRONG. The gap being bounded is
// the time for the client's send buffer to free space, and neither
// kernel on a connection holds that still: both grow a connection's
// buffers as it carries traffic, which is why the same probe over a warm
// connection reported 594 ms where a fresh one reported 434 ms. A margin
// over an autotuned quantity is a margin over a number nobody chose.
// Pinned at both ends — the client's SO_SNDBUF through a dialler, the
// store fixture's SO_RCVBUF on every connection it accepts, each read
// back with getsockopt — the worst gap over one hundred runs on darwin
// is 154.359 ms, and a 1 s window is 6.48 times it.
//
// # WHAT THE PIN DID AND DID NOT BUY, BECAUSE BOTH WERE MEASURED
//
// It removed the buffer's contribution and NOT the machine's. At 128 KiB
// the worst over twenty runs was 110.590 ms and at 64 KiB it was
// 107.821 ms — under two and a half per cent across a doubling — so what
// is left is the fixture's own pacing quantum plus whatever the
// scheduler adds, and the spread from pass to pass (about 105 ms to
// about 220 ms across nine passes of twenty) is a heavy tail rather than
// a buffer. That is the honest account of why this window is 1 s and not
// 200 ms: the pin bought a factor of about three on the worst gap, not a
// factor of thirty.
//
// # AND THE FIXTURE FOLLOWS THE WINDOW RATHER THAN SITTING BESIDE IT
//
// The three-window assertion costs paced bytes in proportion to the
// window, so the row derives its fixture from this number rather than
// carrying constants that agree with it only on the leg they were typed
// on. See pacingFor in internal/flow. Measured at this window on darwin:
// the two upload rows cost 3.91 s and 1.97 s.
var UploadSlowIsNotStalled = Entry{
	Name:   "UploadSlowIsNotStalled",
	Row:    "TestASlowUploadIsNotAStalledOne",
	Window: 1000 * time.Millisecond,
	Side:   Write,
	Governs: "the time for the kernel's send buffer to free space, which is set by how " +
		"fast the far end reads and by how much window it advertises at a time — not " +
		"by the pause the store fixture asks for, and a fixed quantity at all only " +
		"while both ends' buffers are pinned",
	Instrument: "progressReader, internal/flow/upload.go",
	Measurements: map[Leg]Measurement{
		// 154.358875 ms as measured, rounded up to the microsecond the
		// way the probe's own paste hint rounds it. Five passes of
		// twenty on 2026-09-10, the last with the whole suite running in
		// parallel: 106.639, 133.892, 154.359, 117.084, 113.427 ms.
		Darwin: {
			WorstGap: 154359 * time.Microsecond, Runs: 100, Date: "2026-09-10",
			BlockPoint: 819200,
			Pin: &PinnedPair{
				Send:    Pin{Requested: 131072, ReadBack: 131072},
				Receive: Pin{Requested: 131072, ReadBack: 131072},
			},
		},
	},
	SetBy: Darwin,
}

// UploadWedgedStops bounds the other half of that pair: a store that
// reads a little and then stops reading at all, holding the request
// open. One window has to let the slow upload through and stop this one,
// so the two numbers are the same number by construction.
//
// THE PIN IS CARRIED WITH THE NUMBER, and it has to be. A window is five
// times a gap only under the condition the gap was measured in, so a
// wedged row running over an autotuned socket while its sibling ran over
// a pinned one would be two rows depending on one constant while
// standing in two different environments. Its row pins both ends the
// same way; see internal/flow/socketpin_test.go.
var UploadWedgedStops = Entry{
	Name:   "UploadWedgedStops",
	Row:    "TestAWedgedUploadStopsAndSaysSo",
	Window: 1000 * time.Millisecond,
	Side:   Write,
	Governs: "the time for the kernel's send buffer to free space, which is set by how " +
		"fast the far end reads — the same quantity its sibling row measures, under " +
		"the same pin",
	Instrument: "progressReader, internal/flow/upload.go",
	Measurements: map[Leg]Measurement{
		Darwin: {
			WorstGap: 154359 * time.Microsecond, Runs: 100, Date: "2026-09-10",
			BlockPoint: 819200,
			Pin: &PinnedPair{
				Send:    Pin{Requested: 131072, ReadBack: 131072},
				Receive: Pin{Requested: 131072, ReadBack: 131072},
			},
		},
	},
	SetBy: Darwin,
	Carried: &Carried{
		From: "UploadSlowIsNotStalled",
		Reason: "the two rows are one ruling and share one window by construction: a " +
			"single number has to let a slow upload through and stop a wedged one, " +
			"so a window that differed between them would prove neither half. Same " +
			"side (write), same reader (progressReader), same store fixture, and the " +
			"gap being bounded is the same send-buffer drain — the wedged row simply " +
			"stops the drain rather than slowing it. Both rows also run under the " +
			"same pinned pair of socket buffers, which is what makes the shared " +
			"number a shared CONDITION rather than a shared digit.",
	},
}

// StreamGoesQuiet bounds the row that proves a stream which stops
// talking is picked up again. The fixture sends one frame and then holds
// the connection open, sending nothing.
//
// ITS GOVERNING QUANTITY IS NOT THE OTHER TWO STREAM ROWS'. The window
// has to outlast the gap from the watchdog being armed — which happens
// BEFORE the connection is opened — to the first byte arriving, so it
// covers connection establishment as well as delivery. Nothing paces
// this fixture, so there is no inter-frame gap to measure.
var StreamGoesQuiet = Entry{
	Name:   "StreamGoesQuiet",
	Row:    "TestAStreamThatStopsTalkingIsReconnected",
	Window: 100 * time.Millisecond,
	Side:   Read,
	Governs: "the interval from the watchdog being armed — before the connection is " +
		"opened — to the first byte of the first frame arriving: connection " +
		"establishment plus delivery plus whatever the scheduler adds",
	Instrument: "streamProgress, internal/flow/stream.go",
	Measurements: map[Leg]Measurement{
		Darwin: {WorstGap: 1246 * time.Microsecond, Runs: 100, Date: "2026-09-09"},
	},
	SetBy: Darwin,
}

// StreamKeepAlivesAreProofOfLife bounds the row that proves a comment
// frame counts as traffic. The fixture sends nothing but keep-alives,
// paced, for longer than the window.
var StreamKeepAlivesAreProofOfLife = Entry{
	Name:   "StreamKeepAlivesAreProofOfLife",
	Row:    "TestKeepAliveFramesAreProofOfLifeAndAreNeverRendered",
	Window: 300 * time.Millisecond,
	Side:   Read,
	Governs: "the interval between two flushes ARRIVING at this client at the " +
		"fixture's keep-alive pace — the pace plus delivery plus scheduling, not " +
		"the pace on its own; the first such interval runs from the watchdog " +
		"being armed, which is before the connection is opened",
	Instrument: "streamProgress, internal/flow/stream.go",
	Measurements: map[Leg]Measurement{
		Darwin: {WorstGap: 18892 * time.Microsecond, Runs: 100, Date: "2026-09-09"},
	},
	SetBy: Darwin,
}

// StreamPartialLineIsNotAStall bounds the row that proves bytes arriving
// without a newline are progress. The fixture delivers one frame in ten
// paced pieces.
//
// IT IS NOT CARRIED FROM THE KEEP-ALIVE ROW even though both are read
// side through the same reader, because the two fixtures pace
// differently and the gap being bounded includes that pace. A
// measurement taken at one pace does not bound a row running at a
// slower one, and carrying it would be the defect this package exists
// against wearing a permitted name.
//
// THE WINDOW WAS 60 ms AND IS RAISED BY DARWIN'S MEASUREMENT. Sixty was
// three times the fixture's 20 ms pacing knob — the visible quantity,
// chosen the day after the rule against doing that was written. The
// governing quantity measured 24.96 ms: the pace plus delivery plus
// scheduling, which is what the client's watchdog actually sees. Three
// times the knob was 2.4 times the real gap. The fixture also grew, from
// ten pieces to twenty-eight, because the row asserts it spends at least
// three windows on one line and a bigger window needs a longer line to
// spend it on — the row's meaning is unchanged, only its arithmetic.
var StreamPartialLineIsNotAStall = Entry{
	Name:   "StreamPartialLineIsNotAStall",
	Row:    "TestBytesArrivingWithoutANewlineAreNotAStall",
	Window: 150 * time.Millisecond,
	Side:   Read,
	Governs: "the interval between two partial writes of one frame ARRIVING at this " +
		"client at the fixture's own pace — the pace plus delivery plus scheduling; " +
		"the first such interval runs from the watchdog being armed, which is " +
		"before the connection is opened",
	Instrument: "streamProgress, internal/flow/stream.go",
	Measurements: map[Leg]Measurement{
		Darwin: {WorstGap: 23374 * time.Microsecond, Runs: 100, Date: "2026-09-09"},
	},
	SetBy: Darwin,
}

// Registry is every stall window in this repository, keyed by name.
//
// It is written out rather than assembled by reflection, so that adding
// an entry is a deliberate, readable act — and the row beside it walks
// this package's own source to prove no declared entry is missing from
// here, because a registry that can be silently under-populated is not
// one.
var Registry = map[string]*Entry{
	"UploadSlowIsNotStalled":         &UploadSlowIsNotStalled,
	"UploadWedgedStops":              &UploadWedgedStops,
	"StreamGoesQuiet":                &StreamGoesQuiet,
	"StreamKeepAlivesAreProofOfLife": &StreamKeepAlivesAreProofOfLife,
	"StreamPartialLineIsNotAStall":   &StreamPartialLineIsNotAStall,
}

// Lookup returns the entry registered under name.
func Lookup(name string) (*Entry, bool) {
	e, ok := Registry[name]
	return e, ok
}

// UnmeasuredLegs is every leg this entry has no evidence for, in the
// order Legs declares, so a message about them reads the same way twice.
func (e *Entry) UnmeasuredLegs() []Leg {
	var missing []Leg
	for _, leg := range Legs {
		if !e.Measurements[leg].Measured() {
			missing = append(missing, leg)
		}
	}
	return missing
}

// SlowestMeasuredLeg is the measured leg with the widest worst gap — the
// one the sizing rule is applied against — and false when nothing has
// been measured at all.
func (e *Entry) SlowestMeasuredLeg() (Leg, bool) {
	var slowest Leg
	var worst time.Duration
	for _, leg := range Legs {
		m := e.Measurements[leg]
		if !m.Measured() {
			continue
		}
		if m.WorstGap > worst {
			slowest, worst = leg, m.WorstGap
		}
	}
	return slowest, worst > 0
}
