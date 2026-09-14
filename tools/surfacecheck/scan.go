package main

import (
	"strings"

	"github.com/curiouspub/cli/internal/leakcheck"
)

// Finding is one thing this repository forbids, found on a surface it
// publishes. It carries where it was found and which rule fired, because
// a report an author cannot act on is a report that gets ignored.
type Finding struct {
	// Subject is the surface: a commit named by its identifier, a branch
	// name, a tag message, a pull request's title.
	Subject string

	// Line is the 1-based line within the subject, or 0 for a subject
	// that has no lines to speak of, such as a branch name.
	Line int

	// PatternID is the id the manifest gives the rule that matched.
	//
	// IT IS AN ID RATHER THAN THE RULE'S OWN TEXT, and both halves of
	// that are deliberate. A citation pattern quoted into a report is a
	// regular expression written into a run's log, which is readable but
	// says nothing an id does not; a provider term quoted into one is the
	// forbidden name itself, arriving in a log more public than the
	// surface it was read from. And every provider finding used to carry
	// a single generic label, so no two entries in that vocabulary could
	// be told apart at all.
	PatternID string

	// Infrastructure distinguishes a provider or service name from a
	// citation pattern, for the one sentence of the report that differs
	// between them. It is carried rather than derived from the id,
	// because deriving it would tie the renderer to how a data file
	// happens to spell its handles.
	Infrastructure bool
}

// THERE IS NO FIELD HOLDING THE TEXT THAT MATCHED, and the absence is the
// control rather than an omission. That text IS the private citation or
// the provider name, and this report goes into a run's log — on a pull
// request from a fork, a log more public than the message being read. The
// renderer used to be trusted not to print a field it was holding; now it
// is not holding one. A struct that cannot carry the string cannot leak
// it through a caller nobody has written yet.

// Rules is the vocabulary a range is read against.
//
// It is the shared engine with one thing added: the engine answers about
// a FILE at a path, and the surfaces here are not files. Everything about
// what counts as a leak lives in the engine, so that a commit message and
// a blob in this repository's history cannot be judged by two different
// definitions of one rule.
type Rules struct {
	leakcheck.Rules
}

// Scan returns every finding on one surface.
func (r Rules) Scan(subject, text string) []Finding {
	// A SUBJECT WITH ONE LINE HAS NO LINE TO NUMBER: a branch name is not
	// "line 1 of the branch name". The engine numbers from one always,
	// because a file's first line IS line 1; the surfaces here are the
	// exception and it is handled here.
	oneLine := !strings.Contains(text, "\n")

	var out []Finding
	for _, m := range r.Check("", text) {
		line := m.Line
		if oneLine {
			line = 0
		}
		out = append(out, Finding{
			Subject:        subject,
			Line:           line,
			PatternID:      m.PatternID,
			Infrastructure: r.Infrastructure(m.PatternID),
		})
	}
	return out
}
