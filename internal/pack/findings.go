package pack

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/curiouspub/cli/internal/check"
	"github.com/curiouspub/cli/internal/units"
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
func results(files []File, symlinks []string, targets map[string]string, names NameScope) check.Results {
	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.Path)
	}

	var findings []check.Finding
	findings = append(findings, symlinkFindings(symlinks, targets, names)...)
	findings = append(findings, collisionFindings(paths)...)
	findings = append(findings, charsetFindings(publishedVerbatim(paths, names.Public))...)
	if names.Public != "." {
		// A public folder at the root already checked every name as it
		// is, the pages included, and a page reported twice is noise.
		findings = append(findings, routeFindings(paths, names.Pages)...)
	}

	// Sorted here as well as in the combiner, so this producer's own
	// output is in report order for anything that looks at it directly.
	check.SortFindings(findings)
	return check.Results{Findings: findings, Manifest: walkManifest()}
}

// symlinkFindings reports the skipped links: a WARNING for each one the
// build reads, and one NOTE for the rest.
//
// THE CLAIM IS ABOUT THE SITE, SO IT IS MADE ONLY WHERE THE SITE COMES
// FROM. A link is never followed and never packed, which is right
// everywhere. But "what it points at will not be part of your site" is
// true only of a link in the public folder, which Astro copies, or in the
// source folder, which it compiles. Every fresh official starter ships an
// agent-instructions file at its root as a link, which no build reads;
// warning about it asked every new project a question, and stopped every
// run without a terminal at it.
//
// A warned link is one finding of its own, naming where it points, since
// that is the half its owner needs in order to put the real file there.
// The rest are one note: recorded in the report, shown by no surface.
//
// REQUIRED MUTATION, run 2026-10-03: warn on every link. The fresh-starter
// rows red.
func symlinkFindings(symlinks []string, targets map[string]string, names NameScope) []check.Finding {
	var out []check.Finding
	var elsewhere []string
	for _, link := range symlinks {
		if !within(link, names.Public) && !within(link, names.Source) {
			elsewhere = append(elsewhere, link)
			continue
		}
		points := "somewhere this check couldn't read"
		if target, ok := targets[link]; ok {
			points = target
		}
		out = append(out, check.Finding{
			CheckID:  check.IDSymlinks,
			Severity: check.SeverityWarning,
			Message: fmt.Sprintf("%s is a symbolic link to %s. Links are skipped, so what it "+
				"points at will not be part of your site.", link, points),
			Paths: check.NewPaths(link),
			Next: "If your site needs that content, replace the link with the real file or " +
				"directory and deploy again.",
		})
	}
	if len(elsewhere) > 0 {
		out = append(out, check.Finding{
			CheckID:  check.IDSymlinks,
			Severity: check.SeverityNote,
			Message: fmt.Sprintf("%s outside the public and source folders skipped; no build "+
				"reads them:", countOf(len(elsewhere), "symbolic link was", "symbolic links were")),
			Paths: check.NewPaths(elsewhere...),
		})
	}
	return out
}

// collisionFindings reports names that differ only in letter case, one
// finding per GROUP.
//
// The group is the unit because that is what a reader can act on: told
// about one half of a pair, they would still have to go and find the
// other. The comparison is over the WHOLE path — two files with the same
// name in two directories are not a pair, and two files whose
// directories differ only in case are.
//
// IT IS A WARNING, AND ITS TEXT IS CONDITIONAL. Every step that stores
// and serves a site keeps a path's letter case, so each spelling stays
// its own file at its own address. What is left is that two addresses
// differing only in case are easy to mistype, and a
// case-changed address is a page that is not there. The detector also
// runs over every walked path, including a pair outside the published
// folders that never reaches the site at all, so the text says what
// happens IF the names are published and does not say that they will be.
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
			Message: fmt.Sprintf("%s differ only in capitalisation:",
				countOf(len(colliding), "file name", "file names")),
			Paths: check.NewPaths(colliding...),
			Next: "If they end up on the site, each is its own address, and a link has to " +
				"match its capitalisation exactly. " +
				"If they are meant to be one file, keep one; otherwise rename one so the " +
				"names differ by more than case.",
		})
	}
	return out
}

// hasCombiningMark reports whether any part of p carries a combining
// mark — a character that attaches to its neighbour, so a name is more
// characters than it appears to be.
//
// ALL THREE CATEGORIES UNICODE CALLS A MARK, which is a correction. It
// asked Mn — NONSPACING marks — alone, so a spacing mark (Mc, a
// Devanagari visarga) or an enclosing one (Me, a combining circle) fell
// through to the fallback and was refused by naming the character
// instead. Neither publishes either way, so nothing was let through;
// what was missed was the SENTENCE.
//
// AND THE SENTENCE IS WHY THIS FUNCTION EXISTS. The warning that used to
// call it was retired because it could never fire alone, on the argument
// that the wording was the actionable half — so a rule kept for its
// wording that covered two of its three categories was that argument
// holding for two thirds of its subject.
func hasCombiningMark(p string) bool {
	for _, r := range p {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Me, r) {
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

// NameScope is where the file-name check reads: the folders whose names
// reach the built site. Both are project-relative and slash-separated,
// as the caller resolved them from the project's config.
type NameScope struct {
	// Public is the folder Astro copies into the site unchanged. "."
	// means the project root.
	Public string
	// Source is the folder the build compiles, srcDir.
	Source string
	// Pages is the folder whose files become routes, `pages` inside
	// the source folder.
	Pages string
}

// DefaultNameScope is Astro's own layout, for a project whose config
// moves neither folder.
var DefaultNameScope = NameScope{Public: "public", Source: "src", Pages: "src/pages"}

// within reports whether p is dir or lies under it. "." holds everything.
func within(p, dir string) bool {
	return dir == "." || p == dir || strings.HasPrefix(p, dir+"/")
}

// publishedVerbatim is the part of the walk whose names survive the
// build: the files under the public folder, which Astro copies into the
// site unchanged.
//
// EVERYTHING ELSE IS COMPILED, and that is why the name check stopped
// reading it. The platform's character rule is a rule about the files it
// stores and serves, which are the build's output. A page called
// `[slug].astro`, or a folder called `[tag]`, is how Astro writes a
// dynamic route; neither reaches the output under that name, and refusing
// them refused every project with a blog. A name the build does produce
// and the platform cannot store is still refused, by the platform, after
// the build.
//
// REQUIRED MUTATION, run 2026-10-03: return paths unfiltered. The row
// over the dynamic-route fixture reds, naming its four files.
func publishedVerbatim(paths []string, publicDir string) []string {
	if publicDir == "." {
		return paths
	}
	prefix := publicDir + "/"
	var out []string
	for _, p := range paths {
		if strings.HasPrefix(p, prefix) {
			out = append(out, p)
		}
	}
	return out
}

// routeParam is one bracketed stretch of a page's name: a route
// parameter, replaced by whatever values the page produces.
var routeParam = regexp.MustCompile(`\[[^\]]*\]`)

// routeFindings checks the pages folder for the characters a page's
// name carries into its address.
//
// A PAGE'S ROUTE KEEPS EVERY CHARACTER OUTSIDE ITS BRACKETS. Astro
// replaces a `[slug]` stretch with the values the page produces, and
// leaves the rest of the name as it is: `café.astro` is served at
// `café/`, and that is a name the platform will refuse after the build.
// So the brackets are removed and what is left is checked, under the
// same rule and wording as the public folder.
//
// A segment starting `_` is skipped, with everything under it: Astro
// does not route those, so their names never reach the site.
//
// REQUIRED MUTATION, run 2026-10-03: return nil. The row over
// `café.astro` reds, and the dynamic-route rows stay green.
func routeFindings(paths []string, pagesDir string) []check.Finding {
	prefix := pagesDir + "/"
	var out []check.Finding
outer:
	for _, p := range paths {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		segments := strings.Split(strings.TrimPrefix(p, prefix), "/")
		for i, seg := range segments {
			if strings.HasPrefix(seg, "_") {
				continue outer
			}
			segments[i] = routeParam.ReplaceAllString(seg, "")
		}
		if reason := charsetProblem(strings.Join(segments, "/")); reason != "" {
			out = append(out, charsetFinding(p, reason))
		}
	}
	return out
}

// charsetFindings is the walk's HARD STOP: a name the platform will
// refuse once the upload has already started. It is handed only the
// names the build will publish as they are; see publishedVerbatim.
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
		if reason := charsetProblem(p); reason != "" {
			out = append(out, charsetFinding(p, reason))
		}
	}
	return out
}

// charsetFinding is the one finding both readers produce, naming the
// file as it is on disk.
func charsetFinding(p, reason string) check.Finding {
	return check.Finding{
		CheckID:   check.IDPathCharset,
		FailureID: string(check.FamilyPathCharset),
		Severity:  check.SeverityHardStop,
		Message:   fmt.Sprintf("%s can't be published, because %s.", p, reason),
		Paths:     check.NewPaths(p),
		Why: fmt.Sprintf("Every part of a published path may use only letters, digits, "+
			"and the characters . _ ~ and - , with at most %d bytes in any one part "+
			"and %d bytes in the whole path.", pathSegmentLimit, pathTotalLimit),
		Next: nextFor(p),
	}
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
		return "its name is written with a combining mark — the mark is a separate " +
			"character from the one it attaches to, so two names that look the same " +
			"can be different files"
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
//
// THE NUMBER IS GROUPED, and that was a correction rather than a flourish
// (2026-09-08). The counts this originally served were small — a handful
// of skipped links, a colliding pair — so the grouping never showed, and
// nothing distinguished "does not group" from "has never been given a
// number worth grouping". Then the limits arrived with counts in the
// thousands, and one program was printing "3,412 files" in one message
// and "3000 files" in the next, decided by which sentence a reader
// happened to hit.
//
// REQUIRED MUTATION, run 2026-09-08: restore the ungrouped formatting,
// written as `_ = units.Count(n)` above it — deleting the call outright
// leaves the import unused, and a mutation that will not compile proves
// nothing. Reds the grouping row in this package's suite on both of its
// halves, and nothing else.
func countOf(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return units.Count(n) + " " + many
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
		return "Rename it using plain unmarked letters — retyping the marked letter " +
			"gives you its other spelling, which this platform cannot serve either — " +
			"then update whatever links to it and run `curious deploy` again."
	}
	return "Rename it to something inside that set, update whatever links to it, and " +
		"run `curious deploy` again."
}
