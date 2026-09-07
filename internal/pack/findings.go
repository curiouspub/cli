package pack

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/curiouspub/cli/internal/check"
)

// What the walk has to say about a project, and the row that says it
// looked.
//
// ALL THREE ARE COMPUTED FROM THE PATH LIST ALONE — no file is opened, no
// byte of anybody's content is read — which is why they can be produced
// by the scan rather than by the packer. That timing is the whole point
// of putting them here: a warning discovered while writing the archive
// arrives after the user has already agreed to publish, and the first
// thing they would learn about the content directory that got dropped is
// a build failure they cannot explain.

// walkIDs are the check ids this package claims, in report order.
//
// They are DECLARED IN THE RESULT PACKAGE and only referenced here. One
// list, in one place, is what lets a single row assert that the
// producers between them answer every question the program knows about —
// two lists checked against themselves would agree with each other while
// both were wrong.
var walkIDs = []string{
	check.IDSymlinks,
	check.IDCaseCollision,
	check.IDPathCharset,
}

// answered builds a manifest row, and it is THE ONLY PLACE IN THIS
// PACKAGE ONE IS BUILT.
//
// The row's shape DID change — from a flag and a string to a per-id
// status carrying a decline's kind and reason — and having one site made
// that mechanical where the same change spread over three would have
// been an invitation to update two of them. Nothing in the compiler
// notices a second literal appearing, so a guard in this package's own
// suite reads the source and asserts there is one. It earned its keep on
// the first change it met.
//
// Every row this walk produces reports as having run, because the walk
// always runs: it is not a check the engine registers and there is no
// path on which it declines. The rows exist anyway. A row is the only
// thing that separates FOUND NOTHING from NEVER LOOKED, and a report
// that simply omitted a check nobody could see would say the same
// nothing as a report of a clean project.
func answered(id string) check.Status {
	return check.Status{CheckID: id}
}

func walkManifest() check.Manifest {
	m := make(check.Manifest, 0, len(walkIDs))
	for _, id := range walkIDs {
		m = append(m, answered(id))
	}
	return m
}

// results is the walk's whole answer in the shape every producer here
// returns.
func results(files []File, symlinks []string) check.Results {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}

	var findings []check.Finding
	findings = append(findings, symlinkFindings(symlinks)...)
	findings = append(findings, collisionFindings(paths)...)
	findings = append(findings, charsetFindings(paths)...)

	// Sorted here as well as in the combiner, so this producer's own
	// output is in report order for anything that looks at it directly.
	check.SortFindings(findings)
	return check.Results{Findings: findings, Manifest: walkManifest()}
}

// symlinkFindings reports every skipped link in ONE finding.
//
// The unit is the walk's decision rather than the individual link: "these
// were not included" is one sentence, and the renderer prints the list
// under it. Splitting it per link would print that sentence once per
// link, which is how a useful warning becomes noise on a project that
// happens to use several.
func symlinkFindings(symlinks []string) []check.Finding {
	if len(symlinks) == 0 {
		return nil
	}
	return []check.Finding{{
		CheckID:  check.IDSymlinks,
		Severity: check.SeverityWarning,
		Message: fmt.Sprintf(
			"%s skipped, so whatever they point at will not be part of your site:",
			countOf(len(symlinks), "symbolic link was", "symbolic links were")),
		Paths: check.NewPaths(symlinks...),
		Next: "If your site needs that content, replace each link with the real file " +
			"or directory and deploy again.",
	}}
}

// collisionFindings reports names that would become one file once the
// site is served, one finding per colliding GROUP.
//
// The group is the unit because that is what a reader can act on: told
// about one half of a pair, they would still have to go and find the
// other. The comparison is over the WHOLE path — two files with the same
// name in two directories do not collide, and two files whose
// directories differ only in case do.
func collisionFindings(paths []string) []check.Finding {
	groups := make(map[string][]string, len(paths))
	var order []string
	for _, p := range paths {
		key := strings.ToLower(p)
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], p)
	}

	var out []check.Finding
	for _, key := range order {
		group := groups[key]
		if len(group) < 2 {
			continue
		}
		colliding := append([]string(nil), group...)
		sort.Strings(colliding)
		out = append(out, check.Finding{
			CheckID:  check.IDCaseCollision,
			Severity: check.SeverityWarning,
			Message: fmt.Sprintf(
				"%s differ only in capitalisation and would collide once the site is served:",
				countOf(len(colliding), "file name", "file names")),
			Paths: check.NewPaths(colliding...),
			Next: "Rename one of them so the names differ by more than case, or the site " +
				"will serve whichever the platform kept.",
		})
	}
	return out
}

// hasCombiningMark reports whether any part of p carries a combining
// mark — a letter and its accent written as two characters rather than
// one.
//
// IT SURVIVED THE WARNING THAT USED TO CALL IT. That warning was retired
// because it could never fire alone; the detection is still worth having,
// because it decides which sentence the hard stop uses, and a name whose
// accent is a separate character is the refusal a person is least able
// to see for themselves in a file listing.
func hasCombiningMark(p string) bool {
	for _, r := range p {
		if unicode.Is(unicode.Mn, r) {
			return true
		}
	}
	return false
}

// The limits a published path has to fit inside. They belong to the
// address a file is served at rather than to the local filesystem, which
// is why they are checked against the relative path and in BYTES.
const (
	pathSegmentLimit = 255
	pathTotalLimit   = 1024
)

// charsetFindings is the walk's HARD STOP: a name the platform will
// refuse once the upload has already started.
//
// It qualifies as a hard stop under the standing rule that those are for
// facts a scanner can prove. This one is a character test over a name
// already in hand, with no inference in it and nothing it can be wrong
// about — and the alternative is letting somebody archive and upload a
// project that cannot land, then telling them so afterwards.
//
// ONE FINDING PER FILE, because the REASON differs per file and one
// message cannot carry two of them. A space and a character outside
// ASCII are different things to tell somebody about, and the message has
// to name the file rather than the rule: the reader is usually somebody
// whose photo happens to have a space in its name, not somebody who
// wants to learn about character sets.
func charsetFindings(paths []string) []check.Finding {
	var out []check.Finding
	for _, p := range paths {
		reason := charsetProblem(p)
		if reason == "" {
			continue
		}
		out = append(out, check.Finding{
			CheckID:  check.IDPathCharset,
			Severity: check.SeverityHardStop,
			Message:  fmt.Sprintf("%s can't be published, because %s.", p, reason),
			Paths:    check.NewPaths(p),
			Why: fmt.Sprintf("Every part of a published path may use only letters, digits, "+
				"and the characters . _ ~ and - , with at most %d bytes in any one part "+
				"and %d bytes in the whole path.", pathSegmentLimit, pathTotalLimit),
			Next: nextFor(p),
		})
	}
	return out
}

// charsetProblem says why a path cannot be published, or returns empty
// when it can.
//
// The character test comes first because it is the common one, and
// because naming the offending character is the most useful thing this
// message can do.
func charsetProblem(p string) string {
	// THE MARK IS ASKED ABOUT FIRST, and over the whole path rather than
	// up to the first other offence. It is the refusal a reader cannot
	// see: two spellings of one visible name look identical in a file
	// listing, where a space does not. A name breaking both rules is
	// better described by the one its owner would never have found.
	if hasCombiningMark(p) {
		return "its name is written with a combining accent mark — the accent is a " +
			"separate character from the letter it sits on, so the name is two " +
			"characters where it looks like one"
	}
	for _, r := range p {
		if r == '/' || allowedInPath(r) {
			continue
		}
		if r == ' ' {
			return "its name contains a space"
		}
		return fmt.Sprintf("its name contains %q (U+%04X)", string(r), r)
	}
	for _, segment := range strings.Split(p, "/") {
		if len(segment) > pathSegmentLimit {
			return fmt.Sprintf("one part of it is %d bytes long and the limit is %d",
				len(segment), pathSegmentLimit)
		}
	}
	if len(p) > pathTotalLimit {
		return fmt.Sprintf("it is %d bytes long and the limit is %d", len(p), pathTotalLimit)
	}
	return ""
}

// allowedInPath is the allowlist, written out rather than expressed as
// "not one of these": a denylist of troublesome characters is a list
// somebody will find the edge of, and this set is small enough to state.
func allowedInPath(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	switch r {
	case '.', '_', '~', '-':
		return true
	}
	return false
}

// countOf renders "1 thing" and "3 things", so a message about one file
// does not read as though it were written for a list.
func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// nextFor is the action, and a name carrying a combining mark gets a
// different one.
//
// RETYPING IS NOT THE FIX THERE, which is the trap this sentence exists
// to keep somebody out of. Typing the accented letter again usually
// produces the OTHER spelling — one character instead of two — and that
// spelling is outside the allowed set as well, so the rename looks like
// it worked and the deploy fails the same way. The accent has to leave
// the name.
func nextFor(p string) string {
	if hasCombiningMark(p) {
		return "Rename it using plain unaccented letters — retyping the accented letter " +
			"gives you its other spelling, which this platform cannot serve either — " +
			"then update whatever links to it and run `curious deploy` again."
	}
	return "Rename it to something inside that set, update whatever links to it, and " +
		"run `curious deploy` again."
}
