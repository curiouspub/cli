package flow

import (
	"fmt"
	"time"

	"github.com/curiouspub/cli/internal/ui"
)

// The copy for a shut account cap, and the renderer that says when it
// opens again. BOTH HAVE ONE HOME, and this file is it.
//
// The same event reaches a person by two routes: the gate can meet the
// cap before a login is attempted, or the login can meet it at the
// moment an account would actually be spent. Left as two wordings and
// two renderings, one of those readers is told an hour the other is not,
// about the same reset — so the words and the condition stop being two
// things that can drift.
const (
	// closedHeadline is how a shut cap is announced, wherever it is met.
	closedHeadline = "curious.pub is full for today."

	// capacityUsedUp is the standing explanation, used wherever the
	// server did not send one of its own.
	capacityUsedUp = "Today's trial capacity is used up, so there is no account " +
		"for this run to make."

	// uploadedNothing is the reassurance every stop on this path
	// carries. A person whose deploy stopped has no way of knowing, from
	// out here, whether their files went anywhere.
	uploadedNothing = "Nothing has been uploaded and nothing has been deployed."
)

// resetsLayout renders a time of day WITH ITS NUMERIC OFFSET, and it is
// built from the login flow's own wall-clock layout rather than beside
// it so the two cannot drift on the time-of-day half.
//
// The offset is the addition, and it is not decoration. A zone
// abbreviation alone is ambiguous across the world and absent entirely
// from a zone that has no name, so a reader in the wrong place reads a
// correct time and plans around the wrong hour.
const resetsLayout = wallClockLayout + " (-07:00)"

// announceClosed is what a person reads before they are asked anything.
//
// It is narration rather than the returned failure because a question
// needs its reason ABOVE it — a prompt whose justification is printed
// after the run ends is a prompt nobody could answer.
func announceClosed(resetsAt, now time.Time) string {
	return closedHeadline + " " + capacityUsedUp + "\n" + reopensLine(resetsAt, now)
}

// reopensLine names when the door opens again, or says plainly that
// nobody said.
func reopensLine(resetsAt, now time.Time) string {
	if phrase, known := reopensPhrase(resetsAt, now); known {
		return "It reopens at " + phrase + "."
	}
	return "curious wasn't told when it reopens."
}

// reopensPhrase is THE reset renderer, and it has one home for the same
// reason the headline above does.
//
// THE ZONE COMES FROM THE CLOCK, deliberately, rather than from
// time.Local read in here. Two things fall out of that. Production
// renders in the machine's own zone, because time.Now already returns a
// value in it — so this package names no zone anywhere. And a test can
// state what it means with time.FixedZone, which is the only portable
// way to say it: nothing in this module imports a zone database, and
// embedding one to make a named lookup work would add most of a megabyte
// to a binary that ships through three channels for the sake of a
// rendering test.
//
// It reports whether there was anything to render. A reset that is not
// in the future is the server having sent nothing usable, and adding
// nothing to the current time would tell the reader to come back
// immediately — the one answer that is certainly wrong.
func reopensPhrase(resetsAt, now time.Time) (string, bool) {
	if !resetsAt.After(now) {
		return "", false
	}
	return fmt.Sprintf("%s — %s",
		resetsAt.In(now.Location()).Format(resetsLayout),
		relativePhrase(resetsAt.Sub(now))), true
}

// afterTheReset renders the "when to come back" half of an instruction,
// falling back to a phrase that promises nothing when there is no reset
// time to name.
func afterTheReset(resetsAt, now time.Time) string {
	if phrase, known := reopensPhrase(resetsAt, now); known {
		return "after " + phrase
	}
	return "a little later"
}

// relativePhrase turns a duration into the sentence half that saves the
// reader doing arithmetic. A wall-clock time alone answers "when"; it
// does not answer "is that worth waiting for", which is the question
// somebody standing at a stopped deploy is actually asking.
//
// The bands are coarse on purpose — "in about 4 hours" is honest about a
// reset the client learned from a header, where "in 3 hours 58 minutes"
// claims a precision nothing here has.
func relativePhrase(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "in under a minute"
	case d < 45*time.Minute:
		return "in about " + countOf(int(d.Round(time.Minute).Minutes()), "minute")
	case d < 90*time.Minute:
		return "in about an hour"
	case d < 22*time.Hour:
		return "in about " + countOf(int(d.Round(time.Hour).Hours()), "hour")
	default:
		return "in about " + countOf(int(d.Round(24*time.Hour).Hours()/24), "day")
	}
}

// countOf renders a count and its unit, pluralised. It exists so that
// "1 slots left" cannot happen — a message that cannot manage grammar
// reads as a message nobody checked.
func countOf(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// closedCapacityFailure is the standing copy for a shut account cap: the
// shared headline, whatever the server said about it, and when to come
// back. The login flow's own stop calls this rather than carrying a
// second wording of the same event.
func closedCapacityFailure(serverMessage string, resetsAt, now time.Time) *ui.Failure {
	why := serverMessage
	if why == "" {
		why = capacityUsedUp
	}
	return ui.NewFailure(
		closedHeadline,
		why,
		"Try again "+afterTheReset(resetsAt, now)+". "+uploadedNothing)
}
