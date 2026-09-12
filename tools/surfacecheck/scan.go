package main

import (
	"regexp"
	"sort"
	"strings"

	"github.com/curiouspub/cli/internal/citations"
)

// Finding is one thing this repository forbids, found on a surface it
// publishes. It carries where it was found and what matched, because a
// report an author cannot act on is a report that gets ignored.
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
	// surface it was read from. And every vendor finding used to carry a
	// single generic label, so no two entries in that vocabulary could be
	// told apart at all — which is fatal to anything that has to RECORD a
	// finding, because the record has to name the rule.
	PatternID string

	// Infrastructure distinguishes a provider or service name from a
	// citation pattern, for the one sentence of the report that differs
	// between them. It is carried rather than derived from the id,
	// because deriving it would tie the renderer to how a data file
	// happens to spell its handles.
	Infrastructure bool

	// Match is the text that tripped the rule.
	Match string
}

// pattern is one compiled citation rule with the handle the manifest
// gives it.
type pattern struct {
	id string
	re *regexp.Regexp
}

// Rules is one compiled vocabulary: the citation patterns and the vendor
// terms, together, because a surface is scanned against both or it is
// scanned against neither.
type Rules struct {
	patterns []pattern
	vendor   map[string]string // term -> the id the manifest gives it
}

// Scan returns every finding on one surface.
//
// EVERY MATCH ON A LINE, not the first, and the same reasoning holds here
// as it does for the file scan: a message naming several forbidden things
// would otherwise report one of them, and an author fixing them would
// discover the next only by pushing again. A check that reveals its
// findings one per run is a check that gets a reputation for moving
// goalposts.
//
// The vendor terms are reported in sorted order so that two runs over one
// message produce the same report. Go's map iteration is deliberately
// unordered, and a report whose lines shuffle between runs is one nobody
// can diff.
func (r Rules) Scan(subject, text string) []Finding {
	var out []Finding
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lineNumber := i + 1
		if len(lines) == 1 {
			// A subject with one line has no line to number: a branch
			// name is not "line 1 of the branch name".
			lineNumber = 0
		}

		var terms []string
		for token := range citations.IdentifierTokens(line) {
			if _, ok := r.vendor[token]; ok {
				terms = append(terms, token)
			}
		}
		sort.Strings(terms)
		for _, term := range terms {
			out = append(out, Finding{
				Subject:        subject,
				Line:           lineNumber,
				PatternID:      r.vendor[term],
				Infrastructure: true,
				Match:          term,
			})
		}

		for _, p := range r.patterns {
			for _, m := range p.re.FindAllString(line, -1) {
				out = append(out, Finding{
					Subject:   subject,
					Line:      lineNumber,
					PatternID: p.id,
					Match:     m,
				})
			}
		}
	}
	return out
}

// Empty reports whether these rules would look for nothing. A vocabulary
// that lost its contents does not fail — it passes everything, quietly,
// which is the one outcome a check like this must never produce.
func (r Rules) Empty() bool { return len(r.patterns) == 0 || len(r.vendor) == 0 }
