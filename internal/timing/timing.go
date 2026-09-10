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
// Each leg's number is the worst gap seen across CONSECUTIVE runs of
// that entry's probe, and the count is a FLOOR of twenty with the actual
// figure written into the entry. Twenty is the least that can be called
// a distribution; more is better, and where more were done the record
// says how many, because a reader weighing a margin needs to know
// whether it stands on the floor or well above it. See MinimumRuns —
// this paragraph used to say a hundred, flatly, while the task that
// wrote it asked for twenty, and the two disagreed for a day.
//
// A pass with the whole suite running in parallel is worth more than one
// on an idle machine, because a margin measured on an idle machine is
// not the margin a gate has. The probes live beside the rows they
// measure, in internal/flow, and reproduce each row's own fixture rather
// than a cheaper approximation of it: a shorter transfer over a
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
// What the pin buys was measured rather than assumed. On darwin, one
// probe, one pass of twenty runs at each size: 37.7 ms at 16 KiB,
// 107.8 ms at 64 KiB, 110-122 ms at 128 KiB, 139.5 ms at 512 KiB, and
// 417.9-434.1 ms with no pin at all. The curve saturates above about
// 64 KiB, where what is left is the fixture's own pacing quantum and the
// scheduler; below it the buffer is the whole quantity. 16 KiB is what
// this repository asks for, and the reasoning is at pinnedBuffer in
// internal/flow.
//
// # AND ON DARWIN ONLY ONE OF THE TWO ENDS STAYS WHERE IT IS PUT
//
// A read-back is an INSTANT. Sampled repeatedly while a body was
// actually moving — which is what Pin.Sustained records — the client's
// SO_SNDBUF held at the requested 16,384 bytes across every one of
// 16,562 samples, and the accepting end's SO_RCVBUF read back 16,384 and
// was then run by the kernel between 16,384 and 539,008 across 16,560.
// macOS ships net.inet.tcp.doautorcvbuf=1 and setting SO_RCVBUF does not
// clear it; re-setting the option on every drain step held the floor and
// not the ceiling.
//
// So on this leg the pair is one pinned end and one that is merely
// asked. It is still a condition — the gap tracks the requested size
// across a factor of thirty-two, which it could not do if the pin
// governed nothing — because the SEND buffer is the binding one: a
// client can never have more outstanding than its own send buffer holds,
// so it is released about once per that many bytes drained, and the
// receive buffer matters only when it is the SMALLER of the two. Here it
// never is. Whether a leg whose receive end cannot be held should be
// recorded as unpinnable under the rule at Measurement.Pin is a ruling
// nobody has made; what this package now does is measure it rather than
// assert the opposite.
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
	//
	// IT IS AN INSTANT AND NOT A DURATION, which is the whole reason the
	// field below exists. Read one syscall after the request, it says
	// the kernel agreed at that moment; it says nothing whatever about
	// the seconds afterwards during which the body actually moves.
	ReadBack int

	// Sustained is what the kernel held this buffer at WHILE a body was
	// moving over the connection, sampled repeatedly rather than once.
	// Nil means nobody sampled it.
	//
	// # A READ-BACK PROVES THE REQUEST WAS HONOURED, NOT THAT IT STUCK
	//
	// This field was added in round 3 because the difference turned out
	// to be the whole measurement on one of the three legs. Measured on
	// darwin, 2026-09-10, with a 16,384-byte request at both ends: the
	// client's SO_SNDBUF read back 16,384 and was still 16,384 when the
	// last byte went out, and the accepting end's SO_RCVBUF read back
	// 16,384 and was between 331,996 and 539,008 for every sample taken
	// after the first two hundred milliseconds. macOS ships
	// net.inet.tcp.doautorcvbuf=1 and setting SO_RCVBUF does not turn it
	// off: re-setting the option on every drain step held the FLOOR at
	// the requested size and moved the ceiling not at all.
	//
	// So a record carrying only ReadBack can say "measured under a
	// 16 KiB receive buffer" about a connection that spent its whole
	// life at four hundred kilobytes, and nothing in the record would
	// disagree. Sampling is what turns that from a claim into a number,
	// and the number is per leg because it is the kernel's behaviour and
	// not this repository's.
	Sustained *Sustained

	// Err is the failure, rendered, or "" when there was none. It is a
	// STRING rather than an error because this is a transcription of
	// what a run observed, written out by hand into a record — an error
	// value in a package-level literal would be a live object standing
	// in for a thing that happened once, on a machine, in the past.
	Err string
}

// Sustained is a buffer's range over a body, rather than its value at an
// instant.
//
// LOW AND HIGH RATHER THAN A MEAN, because the question is not "what was
// it usually" but "was it ONE size". A mean of a buffer that doubled
// halfway through is a number that describes no moment of the transfer,
// and a condition is either held or it is not.
type Sustained struct {
	// Samples is how many times the buffer was read while a body was
	// moving. Zero is "nobody looked", which the rows keep separate from
	// "it did not move".
	Samples int

	// Low and High are the smallest and largest sizes seen across those
	// samples, in bytes.
	Low, High int
}

// Steady reports whether every sample agreed: the buffer was one size
// for the whole body rather than a size the kernel revised.
func (s *Sustained) Steady() bool { return s != nil && s.Samples > 0 && s.Low == s.High }

// Held reports whether this end really was pinned: the kernel answered,
// and it answered with a size that is an ANSWER TO THE REQUEST. A Pin
// carrying an Err is not a weaker pin, it is the record of a leg that
// could not pin — which is a legitimate thing to write down and a
// different mode to run in.
//
// # THE COHERENCE BAND, AND WHY A BARE "NOT ZERO" WAS NOT ENOUGH
//
// A read-back is only evidence if it stands in a known relation to what
// was asked for. Linux stores twice the request and hands the doubled
// number straight back, so the honest band is [Requested, 2*Requested]
// and nothing else in it is honest: a kernel that reported half the
// request clamped it, and one that reported thirty times it was
// answering about a buffer nobody chose. Outside the band the pin is not
// CLAIMED — the record still says what happened, and nothing reads it as
// a condition.
//
// This was measured rather than reasoned about: before the band existed,
// a Pin of {Requested: 131072, ReadBack: 1} passed every guard in this
// package. One byte is not a socket buffer on any operating system, and
// the check that let it through was asking whether a number was
// positive.
func (p Pin) Held() bool {
	if p.Err != "" || p.Requested <= 0 {
		return false
	}
	return p.ReadBack >= p.Requested && p.ReadBack <= 2*p.Requested
}

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
	// NOBODY SAID — and the guard reds on it, because what happens on
	// that leg depends on the answer.
	//
	// # A LEG THAT CANNOT PIN IS A STOP, NOT A PAYER
	//
	// This paragraph used to promise the opposite, and the promise was
	// arithmetically impossible. It said a leg that tried to pin and
	// could not simply pays: a wider window, a margin over an autotuned
	// buffer, and a fixture large enough to spend three of them. There
	// is nothing to pay WITH. The upload row's fixture is derived from
	// its window by pacingFor in internal/flow, and pacingFor REFUSES
	// above a window of about 2.47 seconds, because past that the body
	// it would have to pace exceeds this client's own 30 MB input limit
	// — which corresponds to a worst gap over about 494 ms. Darwin's own
	// autotuned gap was 434 ms. The leg the promise was written for was
	// already inside a rounding error of the refusal, and the two legs
	// nobody had measured were the ones the promise was about.
	//
	// THE LIMIT IS THE PRODUCT'S AND DOES NOT MOVE FOR A TEST. It is
	// what this client tells a person their project may be, re-validated
	// server-side; a suite that widened it to make one of its own rows
	// affordable would be measuring a client nobody ships.
	//
	// So the behaviour is: a run whose pin does not hold REDS, at the
	// row, naming the end that failed — "receive NOT PINNED" — and the
	// entry for that leg records the failure in this field. Whether the
	// leg is then written off as unpinnable, and what the row becomes
	// there, is a ruling a person makes at a desk with the evidence in
	// front of them. It is not a skip, and it is not a fixture that
	// quietly grows until it trips a product limit somewhere else.
	Pin *PinnedPair
}

// MinimumRuns is the FLOOR on the run count behind a leg's number, and
// it is a floor rather than a target.
//
// One run is an outcome; a margin is a distribution. The window that
// preceded the first measured one here failed about one run in six and
// passed the single run that chose it, which is the whole argument for a
// number rather than a habit. Twenty is the least that can be called a
// distribution at this scale.
//
// THE RECORD STATES THE ACTUAL COUNT, not this constant. A hundred runs
// are recorded as a hundred, because a reader weighing a margin needs to
// know whether it stands on the floor or well above it — and because a
// record that rounded every count down to the minimum would make five
// passes and one pass indistinguishable. This package's own doc comment
// used to say a hundred, flatly, while the task that wrote it asked for
// twenty; the two disagreed for a day and a reader had no way to tell
// which described the entry in front of them.
const MinimumRuns = 20

// measurementDateLayout is the shape a measurement's date is written in.
const measurementDateLayout = "2006-01-02"

// Measured reports whether this leg has evidence behind it.
//
// All three fields are required and each is checked for what it is
// rather than for being non-zero: a duration with no run count is one
// sample; a run count under the floor is not a distribution; and a date
// that does not PARSE cannot be compared with anything, which is the
// only thing a date is for here. A string that merely is not empty
// passes an emptiness test and tells a reader nothing — "soon", "last
// week" and "2026-13-45" are all non-empty.
func (m Measurement) Measured() bool {
	if m.WorstGap <= 0 || m.Runs < MinimumRuns {
		return false
	}
	_, err := time.Parse(measurementDateLayout, m.Date)
	return err == nil
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
// # WHAT THE NUMBER IS, AND WHAT IT IS A NUMBER ABOUT
//
// 126.162 ms, the worst gap over 140 consecutive runs on darwin —
// seven passes of twenty on 2026-09-10, four of them with the whole
// module's suite running in parallel, all of them under the race
// detector. An 800 ms window is 6.34 times it.
//
// The condition is named because a gap is only that number under one:
// both ends asked for a 16,384-byte socket buffer, the client's held
// there for every sample taken while a body was moving, and the store
// fixture's was moved by the kernel up to 539,008 — see the package
// comment, and Pin.Sustained. Every connection is fresh: the row opens
// one, and a connection kept warm across runs answered a different
// question by 160 ms when round 1 tried it.
//
// # THE ROUND BEFORE THIS ONE MEASURED THROUGH A HOLE IN ITS OWN INSTRUMENT
//
// Round 2 recorded 154.359 ms here under a 128 KiB pin, and struck it.
// The receive-buffer size was a field written onto the store fixture
// AFTER its listener had started accepting, so a connection could be
// accepted before the size existed; under the race detector the write
// and the accept-path read were reported as the data race they were, and
// the round's own "two connections in one run disagree" refusal reported
// the consequence — 392,384 on one connection and 131,072 on the next,
// with a worst gap of 267.2 ms against a recorded 154.4 ms. Nothing from
// that round carried across the fix. The pin is now a PARAMETER of the
// fixture's constructor and the listener is wrapped with it already in
// place, which is why there is no longer anything for a mutex to guard.
//
// # THE NUMBER IS TAKEN UNDER THE RACE DETECTOR BECAUSE THE ROW RUNS THERE
//
// make ci runs this package twice, once plainly and once under -race, so
// the detector is one of the conditions this margin has to hold in. It
// is the worse one: the same probe reported 42.1 ms without it. A margin
// sized against the friendlier of two conditions the gate actually uses
// is a margin that is right in the runs nobody worries about.
//
// # AND THE FIXTURE FOLLOWS THE WINDOW RATHER THAN SITTING BESIDE IT
//
// The three-window assertion costs paced bytes in proportion to the
// window, so the row derives its fixture from this number rather than
// carrying constants that agree with it only on the leg they were typed
// on. See pacingFor in internal/flow. Measured at this window on darwin:
// the two upload rows cost 7.80 s and 6.53 s under the race detector —
// 14.33 s combined, against a ceiling of 15 s that was set before the
// detector was in the gate — and 3.17 s and 1.89 s without it.
var UploadSlowIsNotStalled = Entry{
	Name:   "UploadSlowIsNotStalled",
	Row:    "TestASlowUploadIsNotAStalledOne",
	Window: 800 * time.Millisecond,
	Side:   Write,
	Governs: "the time for the kernel's send buffer to free space, which is set by how " +
		"fast the far end reads and by how much window it advertises at a time — not " +
		"by the pause the store fixture asks for, and a fixed quantity at all only " +
		"while both ends' buffers are pinned",
	Instrument: "progressReader, internal/flow/upload.go",
	Measurements: map[Leg]Measurement{
		// SEVEN PASSES OF TWENTY on 2026-09-10, all under the race
		// detector because that is one of the conditions the gate runs
		// this row in, and four of them with the whole module's suite
		// running in parallel: 36.581, 38.512, 42.478, 44.418, 51.126,
		// 54.917, 126.162 ms. The worst is recorded, rounded up to the
		// microsecond the way the probe's own paste hint rounds it.
		//
		// The tail is the machine and not the buffer: six passes sit
		// between 36 and 55 ms and the seventh is more than twice the
		// sixth. One earlier pass, taken against a narrower fixture
		// while the window was still being chosen, reached 150.616 ms —
		// which this window clears at 5.3 times, so it is recorded here
		// in words rather than thrown away.
		Darwin: {
			WorstGap: 126162 * time.Microsecond, Runs: 140, Date: "2026-09-10",
			BlockPoint: 622592,
			Pin: &PinnedPair{
				Send: Pin{Requested: 16384, ReadBack: 16384,
					Sustained: &Sustained{Samples: 16562, Low: 16384, High: 16384}},
				Receive: Pin{Requested: 16384, ReadBack: 16384,
					Sustained: &Sustained{Samples: 16560, Low: 16384, High: 539008}},
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
	Window: 800 * time.Millisecond,
	Side:   Write,
	Governs: "the time for the kernel's send buffer to free space, which is set by how " +
		"fast the far end reads — the same quantity its sibling row measures, under " +
		"the same pin",
	Instrument: "progressReader, internal/flow/upload.go",
	Measurements: map[Leg]Measurement{
		// SEVEN PASSES OF TWENTY on 2026-09-10, all under the race
		// detector because that is one of the conditions the gate runs
		// this row in, and four of them with the whole module's suite
		// running in parallel: 36.581, 38.512, 42.478, 44.418, 51.126,
		// 54.917, 126.162 ms. The worst is recorded, rounded up to the
		// microsecond the way the probe's own paste hint rounds it.
		//
		// The tail is the machine and not the buffer: six passes sit
		// between 36 and 55 ms and the seventh is more than twice the
		// sixth. One earlier pass, taken against a narrower fixture
		// while the window was still being chosen, reached 150.616 ms —
		// which this window clears at 5.3 times, so it is recorded here
		// in words rather than thrown away.
		Darwin: {
			WorstGap: 126162 * time.Microsecond, Runs: 140, Date: "2026-09-10",
			BlockPoint: 622592,
			Pin: &PinnedPair{
				Send: Pin{Requested: 16384, ReadBack: 16384,
					Sustained: &Sustained{Samples: 16562, Low: 16384, High: 16384}},
				Receive: Pin{Requested: 16384, ReadBack: 16384,
					Sustained: &Sustained{Samples: 16560, Low: 16384, High: 539008}},
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
		// FOURTEEN passes of twenty on 2026-09-10, all under the race
		// detector and four of them with the whole module's suite in
		// parallel. Fourteen rather than seven because this row's
		// fixture did not change when the partial-line row's did, so
		// both sets of passes are evidence about the same thing: 0.666,
		// 0.726, 0.768, 0.813, 1.020, 1.188, 1.288, 1.420, 1.574, 1.843,
		// 1.884, 2.478, 2.519, 2.597 ms.
		//
		// Nothing paces this fixture, so what is measured is
		// establishment plus delivery plus the scheduler, and the
		// numbers sit where a loopback connection sits.
		Darwin: {WorstGap: 2598 * time.Microsecond, Runs: 280, Date: "2026-09-10"},
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
		// Fourteen passes of twenty on 2026-09-10, all under the race
		// detector and four with the whole suite in parallel — this
		// row's fixture was untouched by the partial-line change, so
		// both sets count: 17.220, 17.396, 17.459, 17.493, 17.510,
		// 17.904, 18.079, 18.204, 18.294, 18.603, 22.830, 25.151,
		// 32.318, 32.449 ms.
		//
		// The fixture's keep-alive pace is 15 ms and the client sees 17
		// to 32, which is the pace plus delivery plus scheduling — the
		// governing quantity rather than the visible one, and the whole
		// reason this window is not three times fifteen.
		Darwin: {WorstGap: 32450 * time.Microsecond, Runs: 280, Date: "2026-09-10"},
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
// THE WINDOW WAS 60 ms, THEN 150, AND IS NOW 250, EACH TIME BY
// MEASUREMENT. Sixty was three times the fixture's 20 ms pacing knob —
// the visible quantity, chosen the day after the rule against doing that
// was written. A hundred and fifty came from a 24.96 ms measurement of
// the governing one. This round retook it under the conditions the gate
// actually runs — the race detector among them — and the worst over 140
// runs is past 33 ms, which the 150 ms window cleared by 4.5 and the
// rule asks 5 of.
//
// THE FIXTURE IS NOW DERIVED rather than written down, and that is the
// more useful half of the change. It has been a constant twice, ten
// pieces and then twenty-eight, and both times somebody had to notice
// that a wider window needed a longer line to spend three of itself on.
// See partialLineFrames in internal/flow: it grows the line until
// delivering it outlasts three and a half windows, and CHECKS what it
// got rather than trusting a piece count — splitEvenly rounds the piece
// size up and then runs out of string, so a computed count of
// forty-three silently produced thirty.
var StreamPartialLineIsNotAStall = Entry{
	Name:   "StreamPartialLineIsNotAStall",
	Row:    "TestBytesArrivingWithoutANewlineAreNotAStall",
	Window: 250 * time.Millisecond,
	Side:   Read,
	Governs: "the interval between two partial writes of one frame ARRIVING at this " +
		"client at the fixture's own pace — the pace plus delivery plus scheduling; " +
		"the first such interval runs from the watchdog being armed, which is " +
		"before the connection is opened",
	Instrument: "streamProgress, internal/flow/stream.go",
	Measurements: map[Leg]Measurement{
		// Seven passes of twenty on 2026-09-10, under the race detector:
		// 22.871, 23.151, 23.586, 25.080, 27.849, 31.138, 32.702 ms.
		//
		// SEVEN AND NOT FOURTEEN, because this row's fixture changed
		// inside the round — it is derived from the window now rather
		// than written down — and an earlier set of seven was taken
		// against the shorter line. Those passes measured a different
		// delivery and are not folded in, which is the same rule that
		// keeps a read-side number from being carried across two paces.
		Darwin: {WorstGap: 32702 * time.Microsecond, Runs: 140, Date: "2026-09-10"},
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
