// Package leakcheck is the one engine that decides whether a piece of
// this repository's text says something the repository does not publish.
//
// WHY IT IS A PACKAGE AND NOT A FUNCTION IN ITS FIRST CALLER. There are
// two enumerations of things to read and there is one question to ask
// about each of them. The surface check walks a range of published
// surfaces — a commit message, a branch name, a tag, a pull request's
// text; a history scan walks every blob the repository has ever
// published. Those are different universes and neither is more real than
// the other, but a repository with two implementations of "is this a
// leak" has two definitions of one, and the two drift in the direction
// nobody is watching.
//
// THE PATH IS AN ARGUMENT BECAUSE THE ANSWER DEPENDS ON IT. The same
// bytes are a violation in a source file and are the rule itself in a
// manifest whose job is to spell what it forbids. Until this package
// existed that knowledge lived in a _test.go file, where nothing outside
// that package could reach it — so the second reader of these rules would
// have had to restate it, which is the drift above with a head start.
//
// AN EMPTY PATH IS A SURFACE THAT IS NOT A FILE, and it gets no
// exemptions. A commit message is not a manifest however much it quotes
// one.
package leakcheck

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/curiouspub/cli/internal/citations"
	"github.com/curiouspub/cli/internal/rulefile"
)

// Match is one rule firing on one line.
//
// IT CARRIES NO TEXT, and that absence is the design rather than an
// economy. The text a rule matches IS the private citation or the
// provider name, and every caller here writes somewhere public: a run's
// log, which on a fork's pull request is more public than the surface it
// read; a committed ledger of matches already published, which would
// become a directory of exactly what is being defended. A struct that
// cannot hold the string cannot leak it through a caller nobody has
// written yet — which is a stronger property than every caller
// remembering not to print it.
type Match struct {
	// PatternID is the handle the manifest gives the rule that fired.
	PatternID string

	// Line is the 1-based line of the content the rule fired on.
	Line int
}

// pattern is one compiled citation rule with its handle.
type pattern struct {
	id string
	re *regexp.Regexp
}

// Rules is one compiled vocabulary: the citation patterns and the
// provider vocabulary together, because text is read against both or it
// is read against neither.
type Rules struct {
	patterns []pattern

	// vendor maps a lowercased term to the id its manifest gives it.
	vendor map[string]string
}

// New compiles one vocabulary out of the rules two manifests declare.
func New(patterns, vendorTerms []rulefile.Rule) (Rules, error) {
	r := Rules{vendor: map[string]string{}}
	for _, rule := range patterns {
		re, err := regexp.Compile(rule.Text)
		if err != nil {
			return Rules{}, fmt.Errorf("the rule %s is not a pattern this check can "+
				"compile: %w", rule.ID, err)
		}
		r.patterns = append(r.patterns, pattern{id: rule.ID, re: re})
	}
	for _, rule := range vendorTerms {
		r.vendor[strings.ToLower(rule.Text)] = rule.ID
	}
	return r, nil
}

// Empty reports whether these rules would look for nothing. A vocabulary
// that lost its contents does not fail — it passes everything, quietly,
// which is the one outcome a check like this must never produce.
func (r Rules) Empty() bool { return len(r.patterns) == 0 || len(r.vendor) == 0 }

// Infrastructure reports whether an id belongs to the provider
// vocabulary rather than to the citation patterns.
//
// It exists so a report can say which of the two fired without the id
// having to spell it. Deriving that from how a data file happens to
// prefix its handles would tie a renderer to a naming convention that
// nothing enforces.
func (r Rules) Infrastructure(id string) bool {
	for _, ruleID := range r.vendor {
		if ruleID == id {
			return true
		}
	}
	return false
}

// Check returns every rule that fires on content, read as though it sat
// at path.
//
// path is slash-separated and relative to the module root, or empty for a
// surface that is not a file at all.
//
// EVERY MATCH ON A LINE, not the first. A line naming several forbidden
// things would otherwise report one of them, and whoever is fixing them
// would discover the next only by running again — a check that reveals
// its findings one per run gets a reputation for moving goalposts.
//
// The provider terms on a line are reported in sorted order so that two
// runs over one input produce the same list. Go's map iteration is
// deliberately unordered, and a report whose lines shuffle between runs
// is one nobody can diff.
func (r Rules) Check(path, content string) []Match {
	var out []Match
	for i, line := range strings.Split(content, "\n") {
		lineNumber := i + 1

		if scanText, read := VendorScanLine(path, line); read {
			var terms []string
			for token := range citations.IdentifierTokens(scanText) {
				if _, ok := r.vendor[token]; ok {
					terms = append(terms, token)
				}
			}
			sort.Strings(terms)
			for _, term := range terms {
				out = append(out, Match{PatternID: r.vendor[term], Line: lineNumber})
			}
		}

		// THE CITATION PATTERNS READ THE WHOLE LINE, ALWAYS. Every
		// exemption below is per-line AND per-check: a manifest has to
		// spell the provider names it forbids, and nothing else about a
		// data line deserves a waiver, so a private identifier written on
		// one still reds.
		for _, p := range r.patterns {
			for range p.re.FindAllString(line, -1) {
				out = append(out, Match{PatternID: p.id, Line: lineNumber})
			}
		}
	}
	return out
}

// RuleFileNames is the set of files whose job is to name what this
// repository forbids, keyed by their slash-separated path from the module
// root. Their DATA lines are exempt from the provider vocabulary and from
// nothing else.
//
// It is a function so that the scan and anything asserting about the scan
// read the same value: a row that restated the list would pass against a
// list the scan does not use.
func RuleFileNames() map[string]bool {
	return map[string]bool{
		"scripts/citation-patterns.txt":     true,
		"scripts/vendor-terms.txt":          true,
		"scripts/banned-dependencies.txt":   true,
		"scripts/provider-auth-actions.txt": true,
	}
}

// GeneratedManifestNames is the exemption set for files no human wrote.
// There is one, and what it buys is a COLUMN rather than a file; see
// stripModuleHashes.
func GeneratedManifestNames() map[string]bool {
	return map[string]bool{"go.sum": true}
}

// moduleHashField matches a go.sum checksum column: an algorithm name, a
// colon, and base64. A module path cannot match it — a path has no colon
// — and neither can a version, which is why dropping fields by this shape
// leaves exactly the human-chosen part of the line behind.
var moduleHashField = regexp.MustCompile(`^[A-Za-z0-9]+:[A-Za-z0-9+/]*={0,2}$`)

// VendorScanLine returns the part of one line the provider vocabulary is
// read against, and whether it is read at all.
//
// relPath is slash-separated and relative to the module root, so a file
// called go.sum nested somewhere inside the tree is not the module's own
// manifest and gets no exemption. An empty relPath is a surface that is
// not a file and gets none either.
//
// THE go.sum EXEMPTION IS A COLUMN, NOT A FILE, and that correction is
// the point of this function's current shape. The first version excused
// the whole of go.sum on the grounds that nobody chooses the bytes of a
// hash. True — and the same line also carries a MODULE PATH, which is
// somebody's choice, and go.sum keeps entries for modules no longer in
// the graph until someone runs `go mod tidy`. So a provider SDK named in
// a stale entry went unseen here, and the dependency graph check cannot
// see it either, because `go list -m all` does not list a module nothing
// imports. Two rules, one blind by construction and one blinded by a
// carve-out drawn wider than its own argument. Found by a reviewer
// probing what the exemption covered BEYOND what its tests asserted;
// every one of those tests passed.
func VendorScanLine(relPath, line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	isComment := strings.HasPrefix(trimmed, "#")

	if RuleFileNames()[relPath] && !isComment {
		return "", false
	}
	if GeneratedManifestNames()[relPath] {
		return stripModuleHashes(line), true
	}
	return line, true
}

// stripModuleHashes removes the checksum columns from a go.sum line and
// returns what a person actually wrote: the module path and the version.
//
// This is the narrow form of the go.sum carve-out. The hazard it answers
// is real and measured — the provider check reads subwords inside
// identifiers, base64 produces capitalised fragments freely, and 0.72% of
// random module hashes tokenise to a banned term, so a dependency bump
// nobody chose the bytes of could red the build on a file no author can
// edit. The hazard is entirely in the hash. Excusing the rest of the line
// bought nothing and cost the only part of the file worth reading.
func stripModuleHashes(line string) string {
	fields := strings.Fields(line)
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if moduleHashField.MatchString(f) {
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " ")
}

// VendorTerms returns the provider vocabulary as a copy: each lowercased
// term against the id its manifest gives it.
//
// A COPY, because the map is the vocabulary this whole package is built
// on and handing out the original lets a caller edit what the rules
// forbid. The one caller is a test that has to derive a fixture from the
// real list rather than writing a forbidden name down, which is the same
// discipline the manifests themselves keep.
func (r Rules) VendorTerms() map[string]string {
	out := make(map[string]string, len(r.vendor))
	for term, id := range r.vendor {
		out[term] = id
	}
	return out
}
