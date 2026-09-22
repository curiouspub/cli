package flow

import (
	"errors"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// The sentence that made this file necessary. It was the whole of what a
// person was told after a build the service never ran, and every clause of
// it was false for that failure.
//
// IT IS QUOTED IN FULL rather than matched by a fragment, because the
// fragment that matters is the accusation, and a rewording that kept the
// accusation while breaking a shorter match would pass a weaker row.
const theProjectBlamingSentence = "it is a problem in the project\n" +
	"rather than in curious or the service"

// theServiceBlamingClaim is the opposite error and is checked for too. A
// client that met an origin it did not recognise and announced an outage
// would be the same defect running the other way: a broken build reported
// as somebody else's fault, and a person who stops looking at their own
// code because of it.
const theServiceBlamingClaim = "This one is ours"

// TestTheCopyBranchesOnWhoseFaultItWas is this half's central row, and it
// exists because NOTHING COVERED THIS ENDING AT ALL. Measured
// while writing it: no test in this repository named IDBuildFailed, called
// buildFailedFailure, or asserted a word of what it said. The copy that
// sent a person to fix a project that was fine was, until this file, the
// only untested terminal ending in the deploy flow.
//
// That is worth stating precisely because the fix is a branch: a branch is
// exactly the shape that rots quietly when a later origin is added and
// nobody re-reads the default.
func TestTheCopyBranchesOnWhoseFaultItWas(t *testing.T) {
	cases := []struct {
		name   string
		origin wire.FailureOrigin

		wantID ui.FailureID
		// Exactly one of these three is true per case, and the table
		// states all three every time rather than listing only what is
		// expected — a row that says what must be ABSENT is the half
		// that catches a copy edit widening an accusation.
		blamesProject bool
		blamesService bool
		wantNext      ui.NextAction
	}{
		{
			name:          "the project's build failed, and saying so is correct",
			origin:        wire.OriginProject,
			wantID:        ui.IDBuildFailed,
			blamesProject: true,
			wantNext:      ui.NextFreshDeploy,
		},
		{
			name:          "the service failed, and the project must not be blamed",
			origin:        wire.OriginService,
			wantID:        ui.IDBuildServiceFault,
			blamesService: true,
			wantNext:      ui.NextWait,
		},
		{
			name:     "the server said nothing, so neither side may be named",
			origin:   wire.OriginUnstated,
			wantID:   ui.IDBuildFailedUnexplained,
			wantNext: ui.NextFreshDeploy,
		},
		{
			// THE CASE A RELEASED CLIENT WILL ACTUALLY MEET. This
			// contract is additive, so a server may ship an origin
			// after this binary is in somebody's hands, and the value
			// below is deliberately not one of the three declared: it
			// stands for whatever gets added next.
			name:     "an origin this build predates is not an accusation either",
			origin:   wire.FailureOrigin("some-origin-added-later"),
			wantID:   ui.IDBuildFailedUnexplained,
			wantNext: ui.NextFreshDeploy,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := buildFailedFailure(c.origin)

			var failure *ui.Failure
			if !errors.As(err, &failure) {
				t.Fatalf("buildFailedFailure(%q) returned %T, which is not a *ui.Failure — "+
					"every ending of this flow is one, and the MCP half reads it by that type",
					c.origin, err)
			}

			if failure.ID != c.wantID {
				t.Errorf("origin %q got failure id %q, want %q — the id is what somebody "+
					"quotes when they report this, so it has to tell the three endings apart",
					c.origin, failure.ID, c.wantID)
			}
			if failure.Next != c.wantNext {
				t.Errorf("origin %q got next action %q, want %q — this is the field the "+
					"agent-facing half acts on, and it must not tell anyone to wait for a "+
					"broken build to fix itself, or to edit a project that is fine",
					c.origin, failure.Next, c.wantNext)
			}

			said := failure.What + "\n" + failure.Why + "\n" + failure.NextText

			if got := strings.Contains(said, theProjectBlamingSentence); got != c.blamesProject {
				t.Errorf("origin %q: copy blames the project = %v, want %v.\nThe copy was:\n%s",
					c.origin, got, c.blamesProject, said)
			}
			if got := strings.Contains(said, theServiceBlamingClaim); got != c.blamesService {
				t.Errorf("origin %q: copy blames the service = %v, want %v.\nThe copy was:\n%s",
					c.origin, got, c.blamesService, said)
			}
		})
	}
}

// TestTheServiceFaultCopySendsNobodyToALogThatMayNotExist is separate from
// the table above because it is a different claim, and collapsing it into
// a "does not blame the project" assertion would lose it.
//
// THE FAILURE THAT PRODUCED THIS BRANCH HAD NO LOG. The build session was
// refused, so nothing ran and nothing was written. Copy telling that person
// to read the log above, or to fix what it reports, is a second false
// sentence standing where the first one was — and it is the exact sentence
// a well-meaning edit would reach for, because it reads as helpful.
func TestTheServiceFaultCopySendsNobodyToALogThatMayNotExist(t *testing.T) {
	var failure *ui.Failure
	if !errors.As(buildFailedFailure(wire.OriginService), &failure) {
		t.Fatal("the service-origin ending is not a *ui.Failure")
	}
	said := failure.What + "\n" + failure.Why + "\n" + failure.NextText

	for _, sending := range []string{
		"Fix what the log reports",
		"in the log above rather than here",
	} {
		if strings.Contains(said, sending) {
			t.Errorf("the service-fault copy contains %q.\n"+
				"There may be no log at all when the service is at fault — that is what "+
				"happened the day this branch was written — so this sends a person to read "+
				"something that does not exist.\nThe copy was:\n%s", sending, said)
		}
	}
}

// TestEveryDeclaredOriginRendersSomething closes the gap the table above
// cannot: the table names the origins it knows, and a fourth constant added
// to the contract would simply not appear in it.
//
// It ranges wire.AllFailureOrigins, which the contract guards pin to the
// declared constants, so a new origin arrives here automatically and has to
// render a failure carrying an id, a stage and an action — the three fields
// every other ending in this package declares at its construction site.
func TestEveryDeclaredOriginRendersSomething(t *testing.T) {
	if len(wire.AllFailureOrigins) == 0 {
		t.Fatal("wire.AllFailureOrigins is empty — this row would assert nothing")
	}
	for _, origin := range wire.AllFailureOrigins {
		var failure *ui.Failure
		if !errors.As(buildFailedFailure(origin), &failure) {
			t.Errorf("origin %q does not render a *ui.Failure", origin)
			continue
		}
		if failure.ID == "" || failure.Stage == "" || failure.Next == "" {
			t.Errorf("origin %q renders id=%q stage=%q next=%q — every ending declares "+
				"all three", origin, failure.ID, failure.Stage, failure.Next)
		}
		if strings.TrimSpace(failure.Why) == "" {
			t.Errorf("origin %q renders an empty explanation", origin)
		}
	}
}

// TestTheOriginSurvivesTheStream is the end-to-end half, and it is here
// because the table above proves the BRANCH while proving nothing about
// the wire reaching it.
//
// The threading it guards is easy to break silently: the stream used to
// return only a DeployStatus, and the origin arrives on the same event. A
// refactor that went back to returning the status alone would leave every
// row above green — each calls buildFailedFailure directly — and would
// quietly render the unstated copy for every failure the service caused.
func TestTheOriginSurvivesTheStream(t *testing.T) {
	for _, c := range []struct {
		name   string
		frame  string
		wantID ui.FailureID
	}{
		{
			name:   "a service fault arrives as one",
			frame:  doneFrameWithOrigin(wire.StatusFailed, string(wire.OriginService)),
			wantID: ui.IDBuildServiceFault,
		},
		{
			name:   "a project failure arrives as one",
			frame:  doneFrameWithOrigin(wire.StatusFailed, string(wire.OriginProject)),
			wantID: ui.IDBuildFailed,
		},
		{
			// A SERVER OLDER THAN THE FIELD, which is every server
			// until the server half of this change lands. It sends no
			// origin key at all — not an empty one — and this is the
			// path the client is on today.
			name:   "a server that predates the field accuses nobody",
			frame:  doneFrame(wire.StatusFailed),
			wantID: ui.IDBuildFailedUnexplained,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
			run.prompt.confirms = []answer{no()}
			run.script.eventScripts = []eventScript{{frames: []string{
				phaseFrame(wire.PhaseBuilding),
				logFrame("a line the build printed"),
				c.frame,
			}}}

			handoff, err := run.run()
			if handoff != nil {
				defer handoff.Release()
			}
			if err == nil {
				t.Fatal("a failed build returned no error")
			}

			var failure *ui.Failure
			if !errors.As(err, &failure) {
				t.Fatalf("the failed build ended as %T, not a *ui.Failure: %v", err, err)
			}
			if failure.ID != c.wantID {
				t.Errorf("the stream produced failure id %q, want %q — the origin on the "+
					"done event did not reach the copy that branches on it.\n%s",
					failure.ID, c.wantID, rendered(err))
			}
			if failure.DeployID == "" {
				t.Error("the failure carries no deploy id — it is the one thing a person " +
					"can quote afterwards, and a service fault is exactly when they need to")
			}
		})
	}
}
