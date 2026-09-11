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
// scheduler; below it the buffer is the whole quantity.
//
// 128 KiB IS WHAT THIS REPOSITORY ASKS FOR, AND IT IS NOT WHAT THE
// CURVE PREFERS. The table above is one leg's, and a second leg
// overruled it: at 16 KiB the hosted linux runner stopped finishing the
// upload probe at all, where 128 KiB completes it in about thirty
// seconds there. A leg that reports nothing is worse than a leg that
// reports a wider margin, so the size is the one with evidence on all
// three legs rather than the one with the best number on one of them.
// The reasoning, and the two hypotheses that were killed before it was
// accepted, are at pinnedBuffer in internal/flow.
//
// # AND ON DARWIN ONLY ONE OF THE TWO ENDS STAYS WHERE IT IS PUT
//
// A read-back is an INSTANT. Sampled repeatedly while a body was
// actually moving — which is what Pin.Sustained records — the client's
// SO_SNDBUF held at the requested 131,072 bytes across every one of
// 16,653 samples, and the accepting end's SO_RCVBUF read back 131,072
// and was then run by the kernel up to 646,336 across 16,649.
// macOS ships net.inet.tcp.doautorcvbuf=1 and setting SO_RCVBUF does not
// clear it; re-setting the option on every drain step held the floor and
// not the ceiling.
//
// So on this leg the pair is one pinned end and one that is merely
// asked, and that is what the entry records: send pinned and confirmed,
// receive UNPINNABLE, with the range the kernel ran it over while the
// body moved written down beside the gap. It is a disclosed condition
// rather than a claimed one.
//
// # WHAT EACH END IS WORTH IS A PER-LEG QUESTION AND IS MEASURED AS ONE
//
// There is no general sentence here about which end governs. A control
// in internal/flow holds the send end still and varies only the receive
// end, and its tables are recorded beside the pin, per leg, because a
// claim about three legs made from one leg's arithmetic is precisely
// what this package exists to stop. Each leg's window comes from that
// leg's own measurement, under that leg's own recorded condition.
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
	// darwin, 2026-09-10: the client's SO_SNDBUF read back what it asked
	// for and was still that when the last byte went out, and the
	// accepting end's SO_RCVBUF read back the same figure and was
	// several times it for every sample taken after the first two
	// hundred milliseconds — up to 646,336 against a 131,072 request,
	// and up to 539,008 against a 16,384 one. macOS ships
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

// FixturePace is the READ side's condition, and it is to a read-side
// number what a pinned pair of socket buffers is to a write-side one:
// the thing the gap was measured UNDER, stated where the gap is
// recorded so that neither can be read without the other.
//
// # WHY IT HAD TO BE WRITTEN DOWN, AND WHY IT HAD TO BE A CONSTANT
//
// Measured across twenty-one passes in one round: the worst gap a
// read-side probe reports IS the fixture's own widest pause between two
// flushes, to within a millisecond, every time. That is the honest
// answer to what a read-side window is a margin over — how long the
// machine can starve the server goroutine — and it has a consequence
// that took a round to see.
//
// While anything about the fixture is DERIVED FROM THE WINDOW, a margin
// of five times a measured maximum cannot converge. The maximum of a
// heavy-tailed sample grows with how long you look; a wider window made
// the fixture deliver for longer; the longer delivery produced a larger
// maximum; five times that asked for a wider window. It went from 150
// to 250 to 350 to 400 milliseconds inside a single round on exactly
// that treadmill, and every step of it was the rule being applied
// correctly.
//
// AN INSTRUMENT THAT FOLLOWS ITS OWN READING CANNOT CONVERGE. So the
// fixture's pace and the length of what it delivers are STATED
// CONSTANTS in internal/flow, chosen once by a person, and this field is
// where the registry records which constants a leg's number was taken
// under. The window is then five times the measured maximum and stays
// there, because nothing downstream of it moves the fixture.
//
// IT IS ON THE ENTRY AND NOT ON THE MEASUREMENT, which is the opposite
// of where the write side's condition lives, and the difference is real
// rather than tidy. A socket buffer is the KERNEL's answer to a request,
// so it differs per leg and is recorded per leg. A fixture's pace is
// this repository's own constant: it is the same number on all three
// legs by construction, and three copies of one constant would be three
// chances to disagree.
type FixturePace struct {
	// Interval is the pause the fixture leaves between two flushes.
	//
	// ZERO IS A STATEMENT AND NOT A BLANK. One of these fixtures writes
	// its frames and then goes silent for the rest of the row, so there
	// is no interval to state, and what its gap is made of is
	// connection establishment plus delivery plus the scheduler. Which
	// of the two a zero means is answered by the field below and by the
	// entry's own Governs line; "nobody said" is a nil FixturePace, and
	// the guard beside this package refuses that.
	Interval time.Duration

	// Flushes is how many times the fixture writes and flushes on the
	// connection the gap is measured over.
	//
	// IT IS CHECKED AGAINST THE FIXTURE rather than transcribed beside
	// it. The probe in internal/flow asserts that the script it is about
	// to run has exactly this many frames at exactly the interval above,
	// so a constant moved in one place and not the other reds at the
	// measurement rather than sitting here describing a fixture that
	// stopped existing. A record is pasted or it is retyped, and a
	// retyped number is a number with a transcription error waiting in
	// it.
	Flushes int
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
	//
	// TWENTY MEANS ONE PASS AND FORTY MEANS TWO, and which conditions
	// those passes were is a live question rather than a convention.
	// The gate runs these packages twice, plainly and under the race
	// detector, and the rule is that a leg keeps the WORSE of the two —
	// but for one round only the raced pass printed its figures, so the
	// hosted legs here carry a single condition's number. Both passes
	// publish now; the next retake of a hosted leg records the worse of
	// two rather than the one that happened to be readable.
	Runs int

	// Date is when they were taken, as YYYY-MM-DD. A measurement with no
	// date cannot be judged stale.
	Date string

	// Integrity is what the passes behind WorstGap say about
	// THEMSELVES: whether the fixture held the pace this measurement
	// assumes it held. A nil Integrity on a measured leg is not "the
	// fixture was fine", it is nobody having looked, and the guard reds
	// on it.
	Integrity *PaceIntegrity

	// FlushBudget is what a WRITE-THEN-SILENT fixture's own widest gap
	// between flushes is allowed to be on this leg before the pass that
	// produced it is called starved. It is the threshold the 3× rule is
	// applied to for a fixture that states no interval, and it is what
	// makes "no unpaced fixtures" true: every row now declares the
	// quantity its starvation is measured against, and none is exempt.
	//
	// IT IS ON THE MEASUREMENT AND NOT ON THE FixturePace, which is the
	// opposite of where the interval lives, and the reason is the reason
	// given there in the other direction. An interval is this
	// repository's own constant — the same number on all three legs by
	// construction, so three copies would be three chances to disagree.
	// A flush budget is not a constant anybody chose: it is how long
	// THIS KIND OF MACHINE takes to get a goroutine back on a core, which
	// differs per leg by an order of magnitude and is therefore measured
	// per leg, dated, and retaken like any other reading here. It sits
	// beside WorstGap because it comes off the same passes.
	//
	// ZERO MEANS THIS FIXTURE STATES AN INTERVAL and the interval is the
	// threshold. Zero on a fixture whose Pace.Interval is ALSO zero is
	// nobody having looked, and the guard beside this package refuses it
	// rather than letting it through as a threshold of nought — which
	// would not be a lenient rule but the strictest possible one, marking
	// every pass starved and stopping the leg at its cap.
	FlushBudget time.Duration

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

// PaceIntegrity is a measurement's report on its own instrument.
//
// # AN INSTRUMENT THAT RECORDS ITS OWN STARVATION AS THE SUBJECT'S MARGIN
//
// Both timing ROWS already tell these apart. Each one reads the
// fixture's widest gap between its own flushes and refuses when the
// fixture itself paused past the window, saying in terms that the row
// "measured the machine rather than the client". The PROBE beside them
// did not. It recorded the starved gap as the leg's worst, the registry
// sized a window at five times it, and the row's fixture then grew to
// span three and a half of that — which is raising the number until the
// failures stop, performed by the instrument instead of by a person.
//
// The instance, on the gate's own macOS runner, 2026-09-10: the client's
// worst gap was 315.903625 ms and the fixture's own widest gap between
// two flushes was 315.881875 ms, twenty-two MICROSECONDS apart, at a
// stated 20 ms pace. The client did not wait. The runner stopped the
// server goroutine for a third of a second and the client reported it
// faithfully.
//
// So a pass now declares whether it measured anything. A fixture that
// missed its own stated pace by more than the threshold below did not,
// and its number is excluded from the maximum rather than becoming it.
//
// # A READING DISCARDED FOR CARRYING NO INFORMATION CANNOT ALSO BE INFORMATION
//
// That rule used to have a second half, and the second half was
// calibrated wrong. A leg in which more than one pass in five starved
// was a STOP — "this runner cannot hold the fixture's pace" — and it
// fired three times in one working day with no client-margin breach
// underneath any of them. Every window held with room: 32 ms against
// 255 ms, 1.2 ms against 55 ms. Five of twenty starved on one branch and
// six of twenty on another probe in the same command; one of twenty in
// the same command on the main line; and the branch that produced the
// worst readings produced zero of twenty on all six probes when the
// identical command was run again. That is a distribution, and the old
// rule drew a line across the middle of it and called everything past
// the line a broken runner.
//
// It was also not measuring what its name said. "One pass in five
// starved" is a statement about a sample the probe CHOSE TO KEEP, and it
// had no reason to keep one: a starved pass is excluded from the maximum
// precisely because it measured nothing. Having discarded a reading as
// uninformative, the same rule then counted it as evidence against the
// runner — and the cost was paid in the wrong direction, since a starved
// pass makes the sample SMALLER and the answer to a short sample is
// another reading.
//
// So a starved pass is DISCARDED AND RETAKEN. It does not count toward
// the passes a leg asked for; the probe takes another one, the way every
// other instrument does when a reading is spoiled, and it stops only
// after spending a bounded number of attempts without reaching the
// count. Nothing about WHAT COUNTS as starved moved with this: the
// threshold below is the same three, and the window is the same five
// times the valid maximum. What changed is only what the probe does with
// a pass that starved.
type PaceIntegrity struct {
	// Attempts is every pass this leg ran, the thrown-away ones
	// included. Valid and Starved divide it in two: the passes whose
	// fixture held its stated pace within the threshold, and the passes
	// whose did not and were retaken. WorstGap is a maximum over the
	// Valid ones only, and on a leg that finished, Valid is the count
	// the probe asked for rather than whatever survived.
	//
	// # STARVED OVER ATTEMPTS IS A RATE, AND THE RATE IS THE POINT
	//
	// A runner's noise is now MEASURED rather than reddened. The rule
	// that preceded this one produced, on a noisy leg, a red and no
	// number — so the quantity everybody was arguing about existed
	// nowhere except in the memory of whoever was interrupted. Recorded
	// per leg and per retake, it is something a later reader can plot:
	// how often this machine, on this leg, under this condition, was not
	// an instrument.
	//
	// The three numbers are ONE ARITHMETIC FACT and the guard beside
	// this package says so — an attempt either measured the client or
	// was discarded and retaken, so Valid plus Starved is Attempts.
	// Recording Valid and Starved alone would leave the rate's
	// denominator to be inferred, and the inference is only right while
	// nothing retakes.
	Attempts int
	Valid    int
	Starved  int

	// ThresholdNum over ThresholdDen is the multiple of the stated pace
	// a fixture may miss by and still be counted. It is recorded here
	// rather than only in the probe because a number and the rule that
	// admitted it are one fact, and the probe checks this field against
	// the rule it is about to apply.
	ThresholdNum int
	ThresholdDen int

	// StatedPace and Flushes are the FIXTURE THIS LEG'S NUMBER WAS TAKEN
	// THROUGH, recorded per leg rather than only per entry.
	//
	// # AN ENTRY-LEVEL PACE CANNOT SAY WHICH LEGS PREDATE IT
	//
	// The entry carries one Pace and the probe checks it against the
	// fixture it is about to run, so a constant moved in one place and
	// not the other reds at the measurement. What that cannot see is a
	// fixture change made on ONE machine: the leg being remeasured
	// agrees with the new constant, the other two legs' numbers were
	// taken through the old one, and nothing anywhere says so. It
	// happened on 2026-09-11 — the keep-alive fixture went 76 flushes to
	// 36 and the partial line's 116 to 61 while linux and windows still
	// carried figures from the longer ones, all of it green.
	//
	// So each leg's record says what it ran through, and the guard reds
	// when that has drifted from the entry's own. A number and the
	// fixture it was measured through are one fact, and the fact is per
	// leg because the measurement is.
	//
	// Zero pace means the fixture STATES no interval — it writes and goes
	// silent — and the threshold its passes were judged against is the
	// FlushBudget below rather than this field. It no longer means
	// "there is no integrity test to apply": that exemption was the hole
	// this pairing closed.
	StatedPace time.Duration

	// FlushBudget is the threshold a write-then-silent fixture's passes
	// were judged against, recorded beside the pace so a reader of this
	// record can tell WHICH of the two the 3× was applied to without
	// going back to the entry. Exactly one of StatedPace and FlushBudget
	// is non-zero on a well-formed record, and the guard says so.
	FlushBudget time.Duration
	Flushes     int

	// WorstFixtureGap is the widest gap the fixture left, across ALL
	// ATTEMPTS including the ones that were thrown away. It is the
	// number that says how far from a measuring instrument this leg's
	// runner was, and retaking the starved attempts without recording it
	// would hide exactly that — a probe that quietly retook six passes
	// and reported a clean twenty would be describing a machine nobody
	// ran on.
	WorstFixtureGap time.Duration

	// ValidGapMin, ValidGapMedian and ValidGapMax are the fixture's own
	// gap across the passes that were ADMITTED — the shape of the
	// population the threshold let in, rather than the one number at the
	// top of it.
	//
	// # A THRESHOLD IS A LINE THROUGH A DISTRIBUTION NOBODY HAD LOOKED AT
	//
	// Three times the stated pace was chosen between two readings: the
	// ordinary passes near 1.25× and the starved one at 15.8×. That is a
	// defensible line and it says nothing about what lies between. The
	// first retake under the rule produced a darwin partial-line maximum
	// of 47.297583 ms whose own fixture gap was 47.14675 ms — 2.36× the
	// stated pace, admitted, and the reading that set that window.
	//
	// So the record now carries the distribution rather than its
	// endpoint. Divided by StatedPace these three are the ratio spread,
	// per leg, per retake: if the admitted passes cluster near 1.2× with
	// one at 2.4×, the line is in open space; if they run continuously
	// from 1× to 3×, there is no gap for a line to sit in and the
	// threshold is dividing one population rather than separating two.
	// That is the evidence the threshold moves on — not one entry's
	// unlucky pass.
	// A RECORD IS ONE PASS'S, not an aggregate of several. A leg is
	// measured over a series and keeps the pass whose CLIENT gap was
	// worst, so every field here came from one run of the probe and the
	// attempts that run happened to spend. The alternative — a min over
	// one pass, a median over another, a maximum over a third — puts a
	// median in the record that no sample produced.
	ValidGapMin    time.Duration
	ValidGapMedian time.Duration
	ValidGapMax    time.Duration
}

// Ratios renders the admitted passes' fixture gaps as multiples of the
// stated pace, which is the form the threshold is written in.
//
// IT RETURNS ZEROS FOR AN UNPACED FIXTURE rather than dividing by one.
// A fixture with no pace has no ratio to a pace, and a 1.0 there would
// read as "held it exactly" — a claim about a test that was never run.
func (p *PaceIntegrity) Ratios() (min, median, max float64) {
	if p == nil || p.StatedPace <= 0 {
		return 0, 0, 0
	}
	pace := float64(p.StatedPace)
	return float64(p.ValidGapMin) / pace,
		float64(p.ValidGapMedian) / pace,
		float64(p.ValidGapMax) / pace
}

// StarvationRate is the share of this leg's attempts that measured the
// machine rather than the client and were thrown away: Starved over
// Attempts.
//
// IT IS A COLUMN AND NOT A GATE, which is the whole of the change it
// arrived with. Nothing refuses on this number. What refuses is a probe
// that spent its attempts without reaching the passes it asked for —
// which is a statement about whether there is a measurement here at all,
// rather than about how much work it took to get one.
//
// THE DENOMINATOR IS ATTEMPTS, and that is the field this method exists
// to pin down. Under a probe that never retook a pass, attempts and
// passes were the same twenty and either would have read correctly; the
// two only separate once a spoiled reading is replaced, and a rate whose
// denominator was a fixed twenty would then quietly report a fraction of
// the wrong thing. The record carries the number rather than leaving it
// to be derived.
//
// IT RETURNS ZERO FOR A LEG THAT ATTEMPTED NOTHING rather than dividing
// by one. No attempts is nobody having run the probe, and a clean 0.0
// there would read as a runner that never starved — a claim about a
// measurement that does not exist.
func (p *PaceIntegrity) StarvationRate() float64 {
	if p == nil || p.Attempts <= 0 {
		return 0
	}
	return float64(p.Starved) / float64(p.Attempts)
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
	// AN UNCLASSIFIED NUMBER IS NOT A MEASUREMENT, and that is the
	// definition changing rather than a stricter test of the old one.
	// A worst gap is now the maximum over the passes in which the
	// fixture held its stated pace — so a number with no record of
	// which passes those were is a maximum over an unknown population,
	// and there is no way to reclassify it afterwards because the
	// passes behind it kept no fixture record.
	//
	// The consequence is deliberate and it is large: every figure taken
	// before this rule stopped being a measurement the day it landed,
	// and the probe prints the record to paste for each one, because a
	// leg this returns false for is a leg the probe treats as unmeasured.
	if m.Integrity == nil {
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

	// Pace is the fixture's stated pacing, and it is READ SIDE ONLY for
	// the same reason Pin and BlockPoint are write side only: it is the
	// condition that side's gap is measured under, and the other side
	// has a different one. A read-side entry with no Pace is nobody
	// saying, and the guard reds on it; a write-side entry carrying one
	// is a condition with no bearing on the number beside it, and the
	// guard reds on that too.
	Pace *FixturePace

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
// 226.326875 ms, the worst gap over 240 consecutive runs on darwin —
// twelve passes of twenty on 2026-09-11, all of them with the whole
// package running, all of them under the race detector. A 1150 ms
// window is 5.08 times it.
//
// IT WAS 126.162 ms OVER 140 RUNS AND THAT NUMBER WAS NOT WRONG, it was
// short. Seven passes agreed with each other to within six per cent and
// twelve found a range of 109 to 226. See the darwin measurement below
// for the readings, for the idle-versus-busy control that failed to
// find an axis for the spread, and for what is still open about it.
//
// The condition is named because a gap is only that number under one:
// both ends asked for a 131,072-byte socket buffer, the client's held
// there for every sample taken while a body was moving, and the store
// fixture's was moved by the kernel up to 646,336 — see the package
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
// on. See pacingFor in internal/flow.
//
// # THE CEILING IS TWENTY SECONDS, AND IT INCLUDES THE RACE PASS
//
// It was fifteen, and fifteen was chosen while the gate ran these rows
// once. The gate now runs them TWICE — plainly and under the race
// detector — and the detector is not a rounding error here.
//
// RETAKEN 2026-09-11, on the tip this ceiling describes, after the
// window went 800 ms to 1150: the two rows cost 10.02 s and 7.23 s
// under the detector against 4.55 s and 1.91 s without. SEVENTEEN POINT
// TWO FIVE SECONDS COMBINED under the detector, six point four six
// without.
//
// THAT IS 2.75 s OF HEADROOM AND IT IS THE THINNEST THIS CEILING HAS
// BEEN. At the 800 ms window the same pair cost 13.24 s under the
// detector (7.46 and 5.78, against 3.16 and 1.51 without), and a second
// measurement that day gave 13.75 s. The fixture spans three and a half
// windows by construction, so this cost rises with the window roughly
// in proportion: another move of the size the last one was would put
// the pair through the ceiling. Whether the ceiling then moves or the
// rows change shape is a ruling, and it is one worth having before the
// number forces it rather than after.
//
// So the ceiling restates rather than moves: it is the same intent —
// the live suite's timeout has to exceed its row budgets, and that is a
// number to see rather than to discover — applied to the condition the
// gate actually runs. A ceiling that excluded the detector pass would
// be a budget for a run nobody makes, and the number it reported would
// be the friendlier of two figures with nothing beside it saying which.
var UploadSlowIsNotStalled = Entry{
	Name: "UploadSlowIsNotStalled",
	Row:  "TestASlowUploadIsNotAStalledOne",
	// 900 ms: five times darwin's 179.409167 ms is 897.05 ms.
	//
	// IT WENT 800 TO 1150 AND BACK DOWN, and the round trip is the
	// record of what the integrity test was worth. 1150 came from a
	// 226.326875 ms maximum over twelve unclassified passes; 850 comes
	// from 166.233959 ms over 140 passes in which the store fixture was
	// confirmed to have held its own 25 ms drain pace. The difference is
	// not a better sample, it is passes in which the thing being
	// measured was not what moved.
	//
	// A WINDOW IS THE SHIPPED CLIENT'S STALL THRESHOLD, which is why
	// coming back down matters rather than being tidy. An inflated
	// window is a client that waits longer than it needs to before
	// telling a person their upload has stopped.
	Window: 900 * time.Millisecond,
	Side:   Write,
	Governs: "the time for the kernel's send buffer to free space, which is set by how " +
		"fast the far end reads and by how much window it advertises at a time — not " +
		"by the pause the store fixture asks for, and a fixed quantity at all only " +
		"while both ends' buffers are pinned",
	Instrument: "progressReader, internal/flow/upload.go",
	Measurements: map[Leg]Measurement{
		// TWELVE PASSES OF TWENTY on 2026-09-11, every one under the
		// race detector because that is one of the two conditions the
		// gate runs this row in, and every one with the whole package
		// running:
		//
		//	109.329  110.174  111.530  112.024  149.724  181.424  221.993
		//	110.316  153.796  162.177  203.950  226.327
		//
		// 109.329 to 226.327 ms, median 153.796 — a factor of 2.07
		// between the smallest and the largest, on one machine, on one
		// tip, under one recorded condition.
		//
		// # THE SEVEN PASSES THIS REPLACES SAID SIX PER CENT
		//
		// The record here read 127.606 ms over seven passes and said
		// they sat "inside six per cent of each other, which is what a
		// quantity that is mostly a fixture's own pacing quantum looks
		// like." Every one of those seven landed in the low half of the
		// range above. They were not wrong and they were not enough:
		// seven readings that agree are evidence about seven readings,
		// and this leg needed twelve before it showed the top of its
		// own range.
		//
		// # AND CONTENTION IS NOT THE AXIS — MEASURED, NOT ASSUMED
		//
		// The obvious reading of a spread like this is that the machine
		// was busy, and the obvious reading was checked rather than
		// believed. The first seven passes ran while this record's
		// author was editing, greping and vetting between them; the
		// last five ran with nothing else asked of the machine at all.
		// The IDLE series holds the maximum — 226.327 ms — and its own
		// spread, 110.316 to 226.327, is wider than the busy series'.
		// One thing varied, and the answer did not move.
		//
		// # WHAT IS NOT ESTABLISHED, AND IS NOT WRITTEN HERE AS THOUGH
		// # IT WERE
		//
		// That the gap follows the receive end. It is the natural story
		// — this leg's receive buffer autotunes over a factor of five
		// while the body moves, and a quantity that will not hold still
		// is the obvious suspect for a gap that will not either — and
		// the twelve passes do not carry it. Grouped by the ceiling the
		// kernel ran the receive buffer up to: the four passes at
		// 646,336 gave 153.796 to 203.950, and the six passes at about
		// 604,100 gave 109.329 to 226.327. The largest gap of all came
		// with an ORDINARY ceiling. A weak tendency and a
		// counterexample is not a mechanism.
		//
		// The control that would ask properly cannot run on this leg:
		// varying one end needs the other to stay where it is put, and
		// this is the leg where one of them will not. So what is
		// recorded is the CONDITION and the RANGE, and the window covers
		// the observed maximum rather than the median.
		//
		// # THE MAXIMUM HAS NOT CONVERGED, AND THAT IS THE OPEN ITEM
		//
		// Each series found a new high — 221.993 in the first seven,
		// 226.327 in the next five. Five times a running maximum is a
		// rule that does not settle while the maximum keeps moving, and
		// whether the basis should change (a stated quantile with its
		// sampling written down, rather than a maximum) is a ruling this
		// record is waiting on rather than a number to keep raising.
		//
		// # THE RECEIVE END CARRIES AN ERROR, AND IT IS NOT A FAILURE OF THIS CODE
		//
		// It is the honest record of a leg that cannot hold a receive
		// pin. macOS ships net.inet.tcp.doautorcvbuf=1 and setting
		// SO_RCVBUF does not clear it, so the kernel moves an accepted
		// socket's buffer on its own — between the setsockopt and the
		// getsockopt even when they are adjacent syscalls, and
		// continuously afterwards. Measured three ways: two connections
		// in one run read back different sizes; sampling the buffer while
		// a body moved found it between 131,072 and 646,336 against a
		// request of 131,072; and re-setting the option on every drain
		// step held the floor and not the ceiling. Windows, on the same
		// code, reads back what it was asked for and HOLDS there across
		// every sample.
		//
		// THE SEND END DOES HOLD, and this leg is recorded as exactly
		// that: send pinned and confirmed across every sample, receive
		// UNPINNABLE with the range written down. No sentence here says
		// which end governs in general — that is measured per leg by the
		// control whose tables sit beside pinnedBuffer in internal/flow,
		// and this leg is not one the control can ask, because varying
		// one end needs the other to stay where it is put.
		//
		// THE STOP RULE IS ABOUT THE SEND END AND ABOUT A PIN THAT WAS
		// CLAIMED. A run stops when the send pin fails, or when two
		// connections in one run read back different sizes for a pin the
		// record claims is held. A receive pin that cannot be applied is
		// the CONDITION on this leg and the measurement proceeds under
		// it — which is why the gate is green here without the rule
		// having bent: this leg's row is true as written.
		//
		// WHAT REMAINS A RULING RATHER THAN A NUMBER is what this leg's
		// write-side figures are worth, given that the condition behind
		// them moves across a factor of five while the body goes out.
		// That is a decision for a person with the evidence in front of
		// them, and the evidence is all here.
		Darwin: {
			WorstGap: 179409167 * time.Nanosecond, Runs: 20, Date: "2026-09-11",
			Integrity: &PaceIntegrity{Attempts: 20, Valid: 20, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 25 * time.Millisecond, WorstFixtureGap: 57369375,
				ValidGapMin: 26486875, ValidGapMedian: 26716750,
				ValidGapMax: 57369375},
			BlockPoint: 819200,
			Pin: &PinnedPair{
				Send: Pin{Requested: 131072, ReadBack: 131072,
					Sustained: &Sustained{Samples: 16653, Low: 131072, High: 131072}},
				Receive: Pin{Requested: 131072,
					Err: "this kernel's own receive autosizing moves an accepted socket's " +
						"buffer whatever SO_RCVBUF asked for: sampling during a body found " +
						"it between 131072 and 646336, and two connections in one run can " +
						"read back different sizes",
					Sustained: &Sustained{Samples: 16649, Low: 131072, High: 646336}},
			},
		},
		// Two passes of twenty on the gate's own linux runner,
		// 2026-09-10 — one under the race detector at 102.239ms and one
		// without at 104.523ms, which is the pair of conditions make ci
		// runs this row in and a leg where the detector costs almost
		// nothing.
		//
		// LINUX HOLDS ITS PIN AT BOTH ENDS. It reports the DOUBLED value
		// — 262,144 for a 131,072 request, its own bookkeeping counted in
		// — and holds there for every sample taken while a body moved,
		// which is the kernel honouring the request rather than refusing
		// it. It is the leg where the pair rule is least in doubt.
		Linux: {
			WorstGap: 102256000 * time.Nanosecond, Runs: 20, Date: "2026-09-11",
			Integrity: &PaceIntegrity{Attempts: 20, Valid: 20, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 25 * time.Millisecond, WorstFixtureGap: 26301870,
				ValidGapMin: 25833365, ValidGapMedian: 25947440,
				ValidGapMax: 26301870},
			BlockPoint: 589824,
			Pin: &PinnedPair{
				Send: Pin{Requested: 131072, ReadBack: 262144,
					Sustained: &Sustained{Samples: 2299, Low: 262144, High: 262144}},
				Receive: Pin{Requested: 131072, ReadBack: 262144,
					Sustained: &Sustained{Samples: 2299, Low: 262144, High: 262144}},
			},
		},
		// Two passes of twenty on the gate's own windows runner,
		// 2026-09-10: 51.780ms under the race detector and 114.215ms
		// without it. The detector is FASTER here, which is the opposite
		// of darwin and is left as an observation rather than explained.
		//
		// WINDOWS HOLDS ITS PIN AT BOTH ENDS TOO, and reads back exactly
		// what it asked for. Between this leg and linux, darwin is the
		// only one of the three whose receive buffer will not stay where
		// it is put — which is what makes that a fact about one kernel
		// rather than about this fixture.
		Windows: {
			WorstGap: 53542000 * time.Nanosecond, Runs: 20, Date: "2026-09-11",
			Integrity: &PaceIntegrity{Attempts: 20, Valid: 20, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 25 * time.Millisecond, WorstFixtureGap: 27003200,
				ValidGapMin: 25819200, ValidGapMedian: 26065400,
				ValidGapMax: 27003200},
			BlockPoint: 229376,
			Pin: &PinnedPair{
				Send: Pin{Requested: 131072, ReadBack: 131072,
					Sustained: &Sustained{Samples: 2327, Low: 131072, High: 131072}},
				Receive: Pin{Requested: 131072, ReadBack: 131072,
					Sustained: &Sustained{Samples: 2327, Low: 131072, High: 131072}},
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
	Window: 900 * time.Millisecond,
	Side:   Write,
	Governs: "the time for the kernel's send buffer to free space, which is set by how " +
		"fast the far end reads — the same quantity its sibling row measures, under " +
		"the same pin",
	Instrument: "progressReader, internal/flow/upload.go",
	Measurements: map[Leg]Measurement{
		// THE SAME TWELVE PASSES OF TWENTY, 2026-09-11, because this is
		// a CARRIED measurement and carrying is sharing one run's
		// evidence rather than agreeing to have some of one's own. The
		// range, the idle-versus-busy control that failed to find an
		// axis, and what is not established about the receive end are
		// all written once, beside UploadSlowIsNotStalled. The guard
		// below this file requires the two to agree field for field, so
		// a reader who finds a difference here has found a bug rather
		// than a nuance.
		//
		// # THE RECEIVE END CARRIES AN ERROR, AND IT IS NOT A FAILURE OF THIS CODE
		//
		// It is the honest record of a leg that cannot hold a receive
		// pin. macOS ships net.inet.tcp.doautorcvbuf=1 and setting
		// SO_RCVBUF does not clear it, so the kernel moves an accepted
		// socket's buffer on its own — between the setsockopt and the
		// getsockopt even when they are adjacent syscalls, and
		// continuously afterwards. Measured three ways: two connections
		// in one run read back different sizes; sampling the buffer while
		// a body moved found it between 131,072 and 646,336 against a
		// request of 131,072; and re-setting the option on every drain
		// step held the floor and not the ceiling. Windows, on the same
		// code, reads back what it was asked for and HOLDS there across
		// every sample.
		//
		// THE SEND END DOES HOLD, and this leg is recorded as exactly
		// that: send pinned and confirmed across every sample, receive
		// UNPINNABLE with the range written down. No sentence here says
		// which end governs in general — that is measured per leg by the
		// control whose tables sit beside pinnedBuffer in internal/flow,
		// and this leg is not one the control can ask, because varying
		// one end needs the other to stay where it is put.
		//
		// THE STOP RULE IS ABOUT THE SEND END AND ABOUT A PIN THAT WAS
		// CLAIMED. A run stops when the send pin fails, or when two
		// connections in one run read back different sizes for a pin the
		// record claims is held. A receive pin that cannot be applied is
		// the CONDITION on this leg and the measurement proceeds under
		// it — which is why the gate is green here without the rule
		// having bent: this leg's row is true as written.
		//
		// WHAT REMAINS A RULING RATHER THAN A NUMBER is what this leg's
		// write-side figures are worth, given that the condition behind
		// them moves across a factor of five while the body goes out.
		// That is a decision for a person with the evidence in front of
		// them, and the evidence is all here.
		Darwin: {
			WorstGap: 179409167 * time.Nanosecond, Runs: 20, Date: "2026-09-11",
			Integrity: &PaceIntegrity{Attempts: 20, Valid: 20, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 25 * time.Millisecond, WorstFixtureGap: 57369375,
				ValidGapMin: 26486875, ValidGapMedian: 26716750,
				ValidGapMax: 57369375},
			BlockPoint: 819200,
			Pin: &PinnedPair{
				Send: Pin{Requested: 131072, ReadBack: 131072,
					Sustained: &Sustained{Samples: 16653, Low: 131072, High: 131072}},
				Receive: Pin{Requested: 131072,
					Err: "this kernel's own receive autosizing moves an accepted socket's " +
						"buffer whatever SO_RCVBUF asked for: sampling during a body found " +
						"it between 131072 and 646336, and two connections in one run can " +
						"read back different sizes",
					Sustained: &Sustained{Samples: 16649, Low: 131072, High: 646336}},
			},
		},
		// Two passes of twenty on the gate's own linux runner,
		// 2026-09-10 — one under the race detector at 102.239ms and one
		// without at 104.523ms, which is the pair of conditions make ci
		// runs this row in and a leg where the detector costs almost
		// nothing.
		//
		// LINUX HOLDS ITS PIN AT BOTH ENDS. It reports the DOUBLED value
		// — 262,144 for a 131,072 request, its own bookkeeping counted in
		// — and holds there for every sample taken while a body moved,
		// which is the kernel honouring the request rather than refusing
		// it. It is the leg where the pair rule is least in doubt.
		Linux: {
			WorstGap: 102256000 * time.Nanosecond, Runs: 20, Date: "2026-09-11",
			Integrity: &PaceIntegrity{Attempts: 20, Valid: 20, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 25 * time.Millisecond, WorstFixtureGap: 26301870,
				ValidGapMin: 25833365, ValidGapMedian: 25947440,
				ValidGapMax: 26301870},
			BlockPoint: 589824,
			Pin: &PinnedPair{
				Send: Pin{Requested: 131072, ReadBack: 262144,
					Sustained: &Sustained{Samples: 2299, Low: 262144, High: 262144}},
				Receive: Pin{Requested: 131072, ReadBack: 262144,
					Sustained: &Sustained{Samples: 2299, Low: 262144, High: 262144}},
			},
		},
		// Two passes of twenty on the gate's own windows runner,
		// 2026-09-10: 51.780ms under the race detector and 114.215ms
		// without it. The detector is FASTER here, which is the opposite
		// of darwin and is left as an observation rather than explained.
		//
		// WINDOWS HOLDS ITS PIN AT BOTH ENDS TOO, and reads back exactly
		// what it asked for. Between this leg and linux, darwin is the
		// only one of the three whose receive buffer will not stay where
		// it is put — which is what makes that a fact about one kernel
		// rather than about this fixture.
		Windows: {
			WorstGap: 53542000 * time.Nanosecond, Runs: 20, Date: "2026-09-11",
			Integrity: &PaceIntegrity{Attempts: 20, Valid: 20, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 25 * time.Millisecond, WorstFixtureGap: 27003200,
				ValidGapMin: 25819200, ValidGapMedian: 26065400,
				ValidGapMax: 27003200},
			BlockPoint: 229376,
			Pin: &PinnedPair{
				Send: Pin{Requested: 131072, ReadBack: 131072,
					Sustained: &Sustained{Samples: 2327, Low: 131072, High: 131072}},
				Receive: Pin{Requested: 131072, ReadBack: 131072,
					Sustained: &Sustained{Samples: 2327, Low: 131072, High: 131072}},
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
// A SECOND ROW TAKES THIS WINDOW, and it is recorded here rather than
// only at the row, because a reader of this entry would otherwise think
// it bounds one thing. TestASilentStreamEndsTheReadingRatherThanWaiting
// covers the status read of a stream that has gone silent: a different
// caller, the same quantity — the watchdog is armed before the
// connection is opened, the fixture delivers and then holds, and the
// instrument named below is the one both readings are wrapped in. The
// measurement is carried with that reason rather than re-taken, which is
// what this registry permits and what it forbids doing silently.
var StreamGoesQuiet = Entry{
	Name: "StreamGoesQuiet",
	Row:  "TestAStreamThatStopsTalkingIsReconnected",
	// 55 ms: five times darwin's 10.2835 ms is 51.42 ms, and this is the
	// next round number above it. It was 100 ms, chosen from nothing,
	// and the measurement has brought it DOWN — which is worth saying,
	// because every other move this task has made to a window has been
	// upward and a reader could be forgiven for thinking that is what
	// measuring a margin does.
	Window: 55 * time.Millisecond,
	Side:   Read,
	Governs: "the interval from the watchdog being armed — before the connection is " +
		"opened — to the first byte of the first frame arriving: connection " +
		"establishment plus delivery plus whatever the scheduler adds",
	Instrument: "streamProgress, internal/flow/stream.go",
	// NOTHING PACES THIS ONE, and the zero says so rather than leaving
	// it out. The fixture writes its frames and then holds the
	// connection open in silence, so the only interval there is to
	// measure is the one from the watchdog being armed to the first byte
	// arriving.
	Pace: &FixturePace{Interval: 0, Flushes: 2},
	// TAKEN OFF THE GATE'S OWN RUNNERS, 2026-09-10: two passes on each
	// leg — one under the race detector and one without, which is the
	// pair of conditions make ci runs this row in — and a THOUSAND runs
	// per pass rather than twenty. The run count is this entry's alone
	// and it is the round's other correction: this fixture writes its
	// frames and goes silent, so it yields about one arrival per run,
	// and twenty runs was a twenty-sample maximum sitting beside two
	// fifteen-hundred-sample ones and being compared with them by the
	// same five-times rule. A thousand costs eighty milliseconds.
	//
	// IT CHANGED THE ANSWER BY A FACTOR OF FIFTEEN, and only on the leg
	// that decides. On this machine the larger sample found nothing new
	// — 2,000 runs reported 1.605 ms against 1.879 ms over twenty — and
	// that killed the hypothesis it was run on. On the gate's macOS
	// runner the same change took the figure from 0.685 ms to 10.284 ms.
	// The tail is real, it lives on a leg this machine is not, and
	// twenty runs could not see it.
	Measurements: map[Leg]Measurement{
		// 746.761µs under the detector, 468.614µs without it.
		//
		// BUDGET: 203.517µs, the widest this leg's fixture was seen to
		// go between its two flushes across fifteen CI readings on
		// 2026-09-11 (both conditions). The spread is 140µs to 204µs
		// with nothing outside it — this leg has no tail.
		Linux: {WorstGap: 807557 * time.Nanosecond, Runs: 1000, Date: "2026-09-11",
			FlushBudget: 203517 * time.Nanosecond,
			Integrity: &PaceIntegrity{Attempts: 1000, Valid: 1000, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 0, FlushBudget: 203517 * time.Nanosecond,
				Flushes: 2, WorstFixtureGap: 53951}},
		// 10.2835ms under the detector, 1.033458ms without it — a factor
		// of ten between the two conditions, and the detector is the one
		// this window is sized against because it is the worse of two
		// the gate actually runs.
		//
		// THE STORED NUMBER SAID 4.121458 ms OVER 7,000 RUNS AND BOTH
		// HALVES OF THAT WERE WRONG. The window three fields up is
		// derived from 10.2835 ms — "five times darwin's 10.2835 ms is
		// 51.42 ms, and this is the next round number above it" — and the
		// comment immediately above this line states the same figure as
		// what was measured, twice. A 7,000-run pass reporting a maximum
		// two and a half times SMALLER than the 1,000-run pass beside it
		// is not a reading, it is a transcription: more runs cannot find
		// a smaller maximum. Fifteen CI readings taken 2026-09-11 settle
		// it — nine of them sit between 10.34 ms and 10.95 ms, which is
		// the figure the comment always carried.
		//
		// It is corrected here to the number its own derivation uses, and
		// the cost of the error was not theoretical: at 4.121458 ms the
		// margin floor reported every ordinary run as 2.5× a stale
		// record, and shouted "the RECORD is what is stale" on a row that
		// was behaving exactly as measured. A RECORD IS PASTED OR IT IS
		// RETYPED — this file's own rule, and the two numbers that
		// diverged were four lines apart inside one entry.
		//
		// RETAKEN ON THE GATE'S OWN DARWIN RUNNER, 2026-09-11, after the
		// transcription above was corrected: four passes, two runs by
		// two conditions, a thousand runs each. 754.083 µs and
		// 10.645375 ms under the detector, 614.541 µs and 2.195708 ms
		// without it. The leg keeps the worse, which is 10.645375 ms —
		// a shade past the 10.2835 ms the correction restored, and the
		// third independent confirmation that this leg's raced figure
		// lives at about ten and a half milliseconds rather than at four.
		// Five times it is 53.23 ms against a 55 ms window, so the
		// window still clears the rule and does not move.
		Darwin: {WorstGap: 10645375 * time.Nanosecond, Runs: 1000, Date: "2026-09-11",
			// BUDGET: 11.010375 ms, the widest this leg's fixture was
			// seen to go between its two flushes on a pass that was not
			// itself starved, across fifteen CI readings on 2026-09-11.
			// Nine of the fifteen cluster at 10.04–10.07 ms, which is a
			// scheduling quantum rather than noise; one sat at 11.01 ms
			// and one — excluded, and the reason this budget exists — at
			// 35.113375 ms.
			FlushBudget: 11010375 * time.Nanosecond,
			Integrity: &PaceIntegrity{Attempts: 1000, Valid: 1000, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 0, FlushBudget: 11010375 * time.Nanosecond,
				Flushes: 2, WorstFixtureGap: 574667 * time.Nanosecond}},
		// 2.2355ms under the detector, 2.0315ms without it.
		//
		// BUDGET: 2.1332ms, the widest this leg's fixture was seen to go
		// between its two flushes across fifteen CI readings on
		// 2026-09-11 and the four-pass retake that followed them. The
		// spread is 2.02 ms to 2.13 ms — tighter than either other leg,
		// and with no tail.
		//
		// THE RETAKE MOVED IT, BY TWO PER CENT. Fifteen readings put the
		// widest at 2.0896 ms and the retake's raced pass went 2.1332 ms
		// — inside three times either figure, so nothing starved, but a
		// budget defined as the widest gap on valid passes is wrong the
		// moment a valid pass goes wider. It is raised to what was
		// observed rather than left at what was observed first.
		Windows: {WorstGap: 4012500 * time.Nanosecond, Runs: 1000, Date: "2026-09-11",
			FlushBudget: 2133200 * time.Nanosecond,
			Integrity: &PaceIntegrity{Attempts: 1000, Valid: 1000, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 0, FlushBudget: 2133200 * time.Nanosecond,
				Flushes: 2, WorstFixtureGap: 1645200}},
	},
	SetBy: Darwin,
}

// StreamKeepAlivesAreProofOfLife bounds the row that proves a comment
// frame counts as traffic. The fixture sends nothing but keep-alives,
// paced, for longer than the window.
var StreamKeepAlivesAreProofOfLife = Entry{
	Name: "StreamKeepAlivesAreProofOfLife",
	Row:  "TestKeepAliveFramesAreProofOfLifeAndAreNeverRendered",
	// 230 ms: five times darwin's 44.825959 ms is 224.13 ms. It was 525,
	// and 525 was five times a 104.351792 ms reading that the integrity
	// test now names for what it was — the runner starving the server
	// goroutine, recorded as the client's margin. Before that it was
	// 300 ms, chosen from nothing.
	//
	// ITS OWN ROW AFFORDS TWO OF IT, which is the ruled ratio and not a
	// coincidence of the numbers. The row buys its quiet with
	// seventy-five beats at fifteen milliseconds — 1.125 s — and asserts
	// that the quiet covers this window twice: once for what the row
	// proves, once so the proof is not sitting on its own boundary.
	//
	// IT AFFORDED 600 ms AGAINST THIS 525 ms WINDOW FOR ONE ROUND, and
	// that is recorded because the row was green throughout. Seventy-five
	// milliseconds of headroom is a flake with a schedule: a leg
	// reporting past about 120 ms would have carried the window past
	// what its own fixture covers, and the symptom would have been a red
	// row on a runner rather than a number somebody had to rule on.
	//
	// The fixture still does not grow to meet the window on its own. A
	// window that outgrows it stops the row and asks a person to
	// lengthen it deliberately, because a fixture that follows the
	// window is what stopped the read side converging in the first
	// place.
	Window: 230 * time.Millisecond,
	Side:   Read,
	Governs: "the interval between two flushes ARRIVING at this client at the " +
		"fixture's keep-alive pace — the pace plus delivery plus scheduling, not " +
		"the pace on its own; the first such interval runs from the watchdog " +
		"being armed, which is before the connection is opened",
	Instrument: "streamProgress, internal/flow/stream.go",
	// Seventy-five comment frames and a terminating one, fifteen
	// milliseconds apart — 1.125 s of nothing but keep-alives, which is
	// what makes the row able to see a client that stopped counting them
	// as proof of life. Both numbers were already constants; what is new
	// is that the row and the probe beside it now read the SAME two, so
	// the thing being measured is the thing that ships.
	//
	// IT WAS FORTY, AND FORTY WAS 600 ms AGAINST A 525 ms WINDOW. The
	// row passed, its assertion held, and seventy-five milliseconds
	// separated the fixture from the window it is a margin over — which
	// is a flake with a schedule rather than a margin, because the
	// window is a measurement and the next leg to report past about
	// 120 ms would have carried it past what its own fixture affords.
	// The fixture now affords the window TWICE, the ratio is asserted in
	// the row rather than assumed, and a window that outgrows it stops
	// the row and asks a person rather than resizing itself. See
	// keepAliveBeats and keepAliveHeadroom in internal/flow.
	Pace: &FixturePace{Interval: 15 * time.Millisecond, Flushes: 36},
	// TAKEN OFF THE GATE'S OWN RUNNERS, 2026-09-10: two passes of twenty
	// on each leg, one under the race detector and one without. The
	// probe now runs the row's own forty beats rather than twenty of its
	// own, so nothing from before that carries.
	//
	// # THE WORSE CONDITION IS NOT THE SAME ONE ON EVERY LEG
	//
	// Recorded per leg with both figures, because on this entry the
	// detector is the FRIENDLIER condition on two of the three legs and
	// a record naming only one of them would understate the margin the
	// gate has to hold. Every number here is the worse of the two, which
	// is what WorstGap means and what the probe's own paste hint asks
	// for.
	Measurements: map[Leg]Measurement{
		// 15.692101ms under the detector, 15.475903ms without it.
		Linux: {WorstGap: 15785000 * time.Nanosecond, Runs: 20, Date: "2026-09-11",
			Integrity: &PaceIntegrity{Attempts: 20, Valid: 20, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 15 * time.Millisecond, Flushes: 36,
				WorstFixtureGap: 15444588, ValidGapMin: 15332180,
				ValidGapMedian: 15392740, ValidGapMax: 15444588}},
		// 24.413541ms under the detector and 104.351792ms WITHOUT it, on
		// the gate's macOS runner — and the fixture's own widest pause
		// between two flushes in that pass was 104.142542ms, which says
		// what the number is: the runner starved the server goroutine
		// for a tenth of a second. This machine produced 92.959375ms in
		// a pass of its own while a second suite ran beside it. A
		// read-side window is a margin over how long the machine can
		// stop the far end from writing, and this is the largest such
		// pause anybody has measured here.
		Darwin: {WorstGap: 38055000 * time.Nanosecond, Runs: 20, Date: "2026-09-11",
			Integrity: &PaceIntegrity{Attempts: 20, Valid: 20, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 15 * time.Millisecond, Flushes: 36,
				WorstFixtureGap: 38010583, ValidGapMin: 16393708,
				ValidGapMedian: 16641500, ValidGapMax: 38010583}},
		// 16.6084ms under the detector, 28.8472ms without it.
		Windows: {WorstGap: 16656000 * time.Nanosecond, Runs: 20, Date: "2026-09-11",
			Integrity: &PaceIntegrity{Attempts: 20, Valid: 20, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 15 * time.Millisecond, Flushes: 36,
				WorstFixtureGap: 16287100, ValidGapMin: 15723400,
				ValidGapMedian: 15844500, ValidGapMax: 16287100}},
	},
	SetBy: Darwin,
}

// StreamPartialLineIsNotAStall bounds the row that proves bytes arriving
// without a newline are progress. The fixture delivers one frame in a
// long sequence of paced one-byte pieces.
//
// IT IS NOT CARRIED FROM THE KEEP-ALIVE ROW even though both are read
// side through the same reader, because the two fixtures pace
// differently and the gap being bounded includes that pace. A
// measurement taken at one pace does not bound a row running at a
// slower one, and carrying it would be the defect this package exists
// against wearing a permitted name.
//
// # THIS WINDOW IS THE INSTANCE BEHIND THE RULE AT FixturePace
//
// It has been 60 ms, then 150, then 250, then 350, then 400 — the last
// three inside a single round, each one arrived at by applying the
// five-times rule correctly to a fresh measurement. Sixty was three
// times the fixture's own pacing knob, the visible quantity rather than
// the governing one, and that was the defect everyone knew about. The
// three that followed were something else: the fixture's LENGTH was
// computed from the window, so a wider window delivered for longer, a
// longer delivery sampled more of a heavy tail, and the larger maximum
// asked for a wider window again.
//
// AN INSTRUMENT THAT FOLLOWS ITS OWN READING CANNOT CONVERGE, and no
// amount of care at any one step of that loop would have shown it. What
// shows it is the sequence.
//
// So the fixture is a STATED CONSTANT — see partialLineDelivery and
// partialLinePace in internal/flow — and the window is five times the
// maximum measured under it. The row still asserts it spent three
// windows on one line, and it now CHECKS that the stated fixture is
// long enough to do so rather than growing one that is: a window past
// what the constant can cover is a red that asks a person to raise the
// constant deliberately, which is the same decision as before with the
// feedback loop taken out of it.
//
// The helper still grows the line and CHECKS what it got rather than
// computing a count and trusting it — splitEvenly rounds the piece size
// up and then runs out of string, so a computed count of forty-three
// silently produced thirty.
var StreamPartialLineIsNotAStall = Entry{
	Name: "StreamPartialLineIsNotAStall",
	Row:  "TestBytesArrivingWithoutANewlineAreNotAStall",
	// 255 ms: five times darwin's 50.792875 ms is 253.96 ms.
	//
	// THE SEQUENCE IS 60, 150, 250, 350, 400, 230, 650, 255, and every
	// one of those moves was the sizing rule applied correctly to a
	// number. The first four were a window chasing a fixture that was
	// chasing it back, cut by stating the fixture. 650 was not that —
	// the fixture did not move under it — it was a single starved pass
	// reading 128.757833 ms while the fixture itself had stopped for
	// 139.277875 ms at a stated 20 ms pace. 255 is what is left when
	// passes like that are classified instead of recorded.
	Window: 255 * time.Millisecond,
	Side:   Read,
	Governs: "the interval between two partial writes of one frame ARRIVING at this " +
		"client at the fixture's own pace — the pace plus delivery plus scheduling; " +
		"the first such interval runs from the watchdog being armed, which is " +
		"before the connection is opened",
	Instrument: "streamProgress, internal/flow/stream.go",
	// A hundred and fifteen one-byte pieces of a single log frame and a
	// terminating frame, twenty milliseconds apart: 2.3 s of delivery,
	// STATED, so that it no longer follows the window it is a margin
	// over. See partialLineDelivery in internal/flow for why the
	// constant is generously above what the row's own assertion needs —
	// a fixture sitting at the boundary is one that reds the first time
	// a leg is slower — and for the one deliberate raise it has had,
	// what moved it, and what that raise does and does not close.
	Pace: &FixturePace{Interval: 20 * time.Millisecond, Flushes: 61},
	// TAKEN OFF THE GATE'S OWN RUNNERS, 2026-09-10: two passes of twenty
	// on each leg, one under the race detector and one without. Every
	// figure that stood here before was taken through a fixture whose
	// length was computed from the window it was being measured against,
	// so each one described a delivery that no longer exists.
	//
	// THE MARGIN CONVERGED IN ONE PASS UNDER THE STATED FIXTURE, AND
	// THEN MOVED ONCE MORE — and the difference between that move and
	// the four before it is the whole test of whether stating the
	// fixture worked. The window had gone 150 to 250 to 350 to 400
	// inside a single round while the fixture followed it. With the
	// fixture stated it settled, three legs measured once produced one
	// number, and it stood until a twelfth pass under the SAME stated
	// 1.5 s reported 128.757833 ms.
	//
	// That is not the loop restarting. The old moves were the fixture
	// growing and handing back a larger maximum it had manufactured;
	// this one is a fixture that did not change reporting something it
	// had always been able to report and had not yet seen. The window
	// followed the reading, and the fixture then followed the window
	// once, by hand, with the row refusing until a person did it.
	Measurements: map[Leg]Measurement{
		// 20.831652ms under the detector, 20.591115ms without it.
		Linux: {WorstGap: 21612000 * time.Nanosecond, Runs: 20, Date: "2026-09-11",
			Integrity: &PaceIntegrity{Attempts: 20, Valid: 20, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 20 * time.Millisecond, Flushes: 61,
				WorstFixtureGap: 20591111, ValidGapMin: 20398193,
				ValidGapMedian: 20421878, ValidGapMax: 20591111}},
		// # DARWIN'S MAXIMUM CAME OFF A DEVELOPER MAC, NOT OFF THE RUNNER
		//
		// 128.757833 ms, one pass of twenty on 2026-09-11, out of eleven
		// such passes whose other ten ran 24.283 to 34.717 ms. The
		// gate's own macOS runner gave 45.707917 ms under the detector
		// and 43.846625 ms without, over two passes of twenty on
		// 2026-09-10, and that figure held this record until the larger
		// one turned up.
		//
		// PER LEG MEANS PER OS. A darwin reading is a darwin reading
		// whichever darwin took it, and a record that kept the runner's
		// number because the runner is the machine the gate watches
		// would be recording where it looked rather than what is there.
		// The other two read-side entries were checked the same way and
		// both keep the runner's figure: StreamGoesQuiet 10.284 ms
		// against this machine's 6.785, StreamKeepAlives 104.352 ms
		// against this machine's 45.453.
		//
		// WHAT THE OUTLIER IS. In that pass the FIXTURE's own widest gap
		// between flushes was 139.277875 ms, at a stated 20 ms pace —
		// larger than the client's worst arrival gap, which is what it
		// looks like when the machine starves the server goroutine and
		// the client then receives two writes together. So the quantity
		// is not delivery and it is not this client: it is how long this
		// kind of machine can stop the far end from writing. That is the
		// same quantity StreamKeepAlives measures with a different pace,
		// and its darwin record — 104.352 ms, also a starvation event,
		// also one pass — is the same phenomenon at a comparable size.
		// Two entries sampling one distribution and carrying windows a
		// factor of two apart is an open item rather than a finding.
		Darwin: {WorstGap: 48391958 * time.Nanosecond, Runs: 20, Date: "2026-09-11",
			Integrity: &PaceIntegrity{Attempts: 20, Valid: 20, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 20 * time.Millisecond, Flushes: 61,
				WorstFixtureGap: 48492625, ValidGapMin: 21446750,
				ValidGapMedian: 22331708, ValidGapMax: 48492625}},
		// 21.4369ms under the detector, 26.8971ms without it.
		Windows: {WorstGap: 21346000 * time.Nanosecond, Runs: 20, Date: "2026-09-11",
			Integrity: &PaceIntegrity{Attempts: 20, Valid: 20, Starved: 0,
				ThresholdNum: 3, ThresholdDen: 1,
				StatedPace: 20 * time.Millisecond, Flushes: 61,
				WorstFixtureGap: 21103800, ValidGapMin: 20786900,
				ValidGapMedian: 20883600, ValidGapMax: 21103800}},
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
