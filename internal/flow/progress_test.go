package flow

import (
	"testing"

	"github.com/curiouspub/cli/pkg/wire"
)

// seenReport is one thing a run said while it was working, kept as the
// two halves it was said in. Joining them here would make every
// assertion below a test of this file's own formatting.
type seenReport struct {
	phase wire.Phase
	line  string
}

// recordAll is the sink these rows measure with: everything it was
// given, in order, with no filtering of its own.
func recordAll(into *[]seenReport) DeployProgress {
	return func(phase wire.Phase, line string) {
		*into = append(*into, seenReport{phase: phase, line: line})
	}
}

// TestARunReportsBothHalvesOfWhereItHasGot.
//
// THE CLAIM IS NOT "SOMETHING WAS REPORTED". Either half alone is
// satisfied by a channel that names the phase and forgets the output, or
// by one that echoes the log and never says which step produced it —
// and both of those read as working, because a caller watching a build
// sees lines arriving either way. What is asserted is the PAIR, at every
// report, including the two boundary cases a caller would otherwise have
// to handle: the first phase, which arrives before any output exists,
// and the first line, which arrives under the phase already named.
//
// THE ORDER IS ASSERTED RATHER THAN THE MEMBERSHIP. Progress is the
// narration of a sequence, and a caller shown the build phase before the
// install phase has been told something false about a run that was fine.
//
// REQUIRED MUTATION, run 2026-09-11: report the phase alone on a log
// event — deps.Progress("", ev.Line) in renderEvent. Reds here naming
// report 1, "{queued a}" against a want of "{queued a}"… the phase half
// empty. The mirror mutation, dropping run.lastLine from the phase
// branch, reds report 2.
func TestARunReportsBothHalvesOfWhereItHasGot(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	// The consent question, which every first login asks and this
	// project's clean pre-flight leaves as the only one.
	run.prompt.confirms = []answer{no()}

	run.script.eventScripts = []eventScript{{frames: []string{
		phaseFrame(wire.PhaseQueued),
		logFrame("a"),
		phaseFrame(wire.PhaseBuilding),
		logFrame("b"),
		doneFrame(wire.StatusBuilt),
	}}}

	var got []seenReport
	run.deps.Progress = recordAll(&got)

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	want := []seenReport{
		// The first phase, with nothing said yet. An empty line here is
		// the honest answer rather than an omission.
		{phase: wire.PhaseQueued, line: ""},
		{phase: wire.PhaseQueued, line: "a"},
		{phase: wire.PhaseBuilding, line: "a"},
		{phase: wire.PhaseBuilding, line: "b"},
		// NOTHING FOR THE TERMINAL EVENT. The status it carries is the
		// value the call returns, which arrives before any report could
		// and says more than a phase would.
	}
	assertReports(t, got, want)
}

// TestAReplayedBuildIsNarratedOnce is the replay half, and it is the one
// that cannot be got right by accident.
//
// The stream carries no resume point, so a reconnection re-sends the
// build from its beginning. The terminal suppresses what it has already
// shown; a progress channel that did not would narrate one build twice
// to a caller with no scrollback to notice it in — which is worse than
// the terminal case, because the second telling is indistinguishable
// from a build that really did install twice.
//
// THE FIRST CONNECTION ENDS WITHOUT ITS TERMINAL EVENT, which is what
// makes the run reconnect at all: a stream that ends without `done`
// ended abnormally and is picked up again.
//
// REQUIRED MUTATION, run 2026-09-11 and RE-RUN 2026-09-12: move the
// report in the log branch of renderEvent above the replay test, beside
// the counter. Reds here with SEVEN reports against five, and on this
// row alone — the two log lines of the second connection repeating "a"
// and "b".
//
// THE COUNT WAS WRITTEN AS EIGHT and the phase was named as repeating
// with them. It does not: the replayed phase event is suppressed by the
// `ev.Phase == run.lastPhase` check in the phase branch (stream.go:589),
// which this mutation does not touch. Measured on the tip:
//
//	progress_test.go:143: the sink heard 7 reports, want 5
//
// Corrected because a required-mutation note is a claim about what a
// command PRINTS, and one written from the mutation's intent rather
// than its output is the same defect as a commit message that names
// less than its diff — read by the next person as a measurement.
func TestAReplayedBuildIsNarratedOnce(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}

	delivered := []string{
		phaseFrame(wire.PhaseInstalling),
		logFrame("a"),
		logFrame("b"),
	}
	run.script.eventScripts = []eventScript{
		// Cut off mid-build: no terminal event, so the run picks the log
		// up again.
		{frames: delivered},
		// The replay, from the beginning, plus the rest of the build.
		{frames: append(append([]string{}, delivered...),
			phaseFrame(wire.PhaseUploading),
			logFrame("c"),
			doneFrame(wire.StatusBuilt))},
	}

	var got []seenReport
	run.deps.Progress = recordAll(&got)

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	if connections := run.script.eventConnections(); connections != 2 {
		t.Fatalf("the stream was opened %d times, so this row did not measure a replay",
			connections)
	}

	want := []seenReport{
		{phase: wire.PhaseInstalling, line: ""},
		{phase: wire.PhaseInstalling, line: "a"},
		{phase: wire.PhaseInstalling, line: "b"},
		// The second connection replays the three above and says nothing
		// about them, then carries on where it left off.
		{phase: wire.PhaseUploading, line: "b"},
		{phase: wire.PhaseUploading, line: "c"},
	}
	assertReports(t, got, want)
}

// TestARunWithNoSinkStillFinishes is the positive control for the two
// rows above, and it is about the DEFAULT rather than about progress.
//
// Every report in this package is an unconditional call, which is the
// point — a channel checked for nil at each of three sites is three
// places to forget, and forgetting one is a panic inside somebody's
// deploy rather than a missing line. That is only safe while the default
// is applied where the stream is read, and nothing else in this file
// would notice it being removed: both rows above supply a sink.
//
// THE BUILD'S OUTPUT IS ASSERTED TOO, so this is not merely "it did not
// panic". A default applied by swallowing the whole rendering would also
// not panic.
//
// REQUIRED MUTATION, run 2026-09-11: delete the default in streamBuild.
// Reds here, and loudly — a call through a nil func value is a nil
// pointer dereference, nothing in this sequence contains a panic, and
// the binary dies inside renderEvent with this row's name on it. The two
// rows above stay green, which is what establishes that this one carries
// the default.
func TestARunWithNoSinkStillFinishes(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	// The consent question, which every first login asks and this
	// project's clean pre-flight leaves as the only one.
	run.prompt.confirms = []answer{no()}

	run.script.eventScripts = []eventScript{{frames: []string{
		phaseFrame(wire.PhaseBuilding),
		logFrame("only line"),
		doneFrame(wire.StatusBuilt),
	}}}
	if run.deps.Progress != nil {
		t.Fatal("this row is about a run with no sink and one was wired")
	}

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	if got := buildLogOutput(t, run); got != "only line\n" {
		t.Errorf("the build log reached stdout as %q, want the one line the stream sent", got)
	}
}

// assertReports compares what a sink heard with what it should have
// heard, position by position.
func assertReports(t *testing.T, got, want []seenReport) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("the sink heard %d reports, want %d:\ngot  %+v\nwant %+v",
			len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("report %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
