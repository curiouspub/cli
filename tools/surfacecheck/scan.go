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

	// Rule is the pattern that matched, written exactly as the manifest
	// declares it, or vendorVocabulary for a forbidden name. Both are
	// public text from this repository's own files.
	Rule string

	// Match is the text that tripped the rule.
	Match string
}

// vendorVocabulary is the Rule value for a provider or service name. It
// is not a pattern — those are matched by tokenising rather than by a
// regular expression — so it names the check instead of a line.
const vendorVocabulary = "vendor vocabulary"

// Rules is one compiled vocabulary: the citation patterns and the vendor
// terms, together, because a surface is scanned against both or it is
// scanned against neither.
type Rules struct {
	patterns []*regexp.Regexp
	vendor   map[string]bool
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
			if r.vendor[token] {
				terms = append(terms, token)
			}
		}
		sort.Strings(terms)
		for _, term := range terms {
			out = append(out, Finding{
				Subject: subject,
				Line:    lineNumber,
				Rule:    vendorVocabulary,
				Match:   term,
			})
		}

		for _, re := range r.patterns {
			for _, m := range re.FindAllString(line, -1) {
				out = append(out, Finding{
					Subject: subject,
					Line:    lineNumber,
					Rule:    re.String(),
					Match:   m,
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
