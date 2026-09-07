package preflight

import (
	"sort"

	"github.com/curiouspub/cli/internal/check"
)

// Result is what one check reports back to the engine.
//
// A check emits a Finding only when it has something to say — there is
// no "pass" finding — so a check that looked and liked what it saw
// returns a zero Result. What that shape cannot express on its own is
// the difference between FOUND NOTHING and NEVER LOOKED, and NotRun is
// where that difference is paid for.
type Result struct {
	Findings []check.Finding

	// NotRun says the check did not look, and why. Empty means it ran.
	//
	// The reason is written for a person: it goes into the report
	// verbatim, beside the name of the check that was skipped, because a
	// skipped check rendered as a tick is a lie the reader will act on.
	// A check that cannot read package.json says so; it does not stay
	// quiet and let silence be read as approval.
	NotRun string
}

// Check is one pre-flight check as the engine sees it: the ids it
// reports on, and the function that does the reading.
//
// IDs IS A LIST BECAUSE ONE CHECK CAN COVER MORE THAN ONE ID. The
// astro.config check answers two questions off a single parse — where
// the pages live, and whether the build format will produce URLs this
// platform can route — and re-reading the file to answer them separately
// would buy nothing. The manifest still needs a row for each, or a
// renderer could never say whether the second one was looked at.
type Check struct {
	IDs []string
	Run func(fsys FS, root string) Result
}

// The DECLARED ORDER is not kept here. It lives in the result package,
// with the check ids themselves, and this engine reads it — because the
// engine is not the only producer of findings. The file walk reports its
// own ids from a package that must never import this one, and the row
// that asserts the two between them cover everything has to run
// somewhere both can be seen. A private copy here would be a second
// list, and two lists that are uniformly wrong pass every check that
// compares one against itself.
//
// The order is applied to whatever the caller registered, in whatever
// sequence. An order that merely mirrored the caller's slice would be
// the caller's rule, and a second caller assembling the same checks
// differently would get a different report out of the same project.

// Run executes every check and returns what they found, together with a
// manifest of what was asked.
//
// ALL CHECKS RUN. There is no short-circuit on the first hard failure,
// and that is the aggregate behaviour the whole engine exists for: a
// project missing astro AND a lockfile should learn both facts in one
// run rather than discover the second only after fixing the first.
//
// The engine does not decide what blocking MEANS. It produces findings
// and a manifest; the caller renders them, and the two surfaces this
// program has answer a warning differently — one asks a person, the
// other attaches it to a result an agent reads. Putting that decision
// inside a check would make the second surface a rewrite rather than a
// renderer.
//
// Nothing here reaches the network, writes a file, or executes any of
// the user's code.
func Run(checks []Check, fsys FS, root string) check.Results {
	ordered := make([]Check, len(checks))
	copy(ordered, checks)
	sort.SliceStable(ordered, func(i, j int) bool {
		return firstRank(ordered[i]) < firstRank(ordered[j])
	})

	var findings []check.Finding
	var manifest check.Manifest

	for _, c := range ordered {
		var result Result
		if c.Run != nil {
			result = c.Run(fsys, root)
		}

		findings = append(findings, result.Findings...)
		for _, id := range c.IDs {
			manifest = append(manifest, check.Ran{
				CheckID: id,
				Ran:     result.NotRun == "",
				Reason:  result.NotRun,
			})
		}
	}

	// Sorting the manifest as well as the findings matters for a check
	// that declares its ids out of the declared order; the rows are
	// adjacent either way, and this is what makes them adjacent in the
	// right sequence. Both sorts are the result package's, for the same
	// reason the order itself is: two sorters would disagree the first
	// time one of them was changed.
	check.SortManifest(manifest)
	check.SortFindings(findings)

	// One pair-shaped concept, one spelling. Every producer in this
	// program returns check.Results, so the place where two of them meet
	// takes them as they come rather than re-wrapping at each call site
	// — and a second producer added later cannot arrive in a shape the
	// combiner has to learn.
	return check.Results{Findings: findings, Manifest: manifest}
}

// firstRank is a check's place in the running order: the earliest
// declared position among the ids it reports on.
func firstRank(c Check) int {
	best := len(check.DeclaredOrder())
	for _, id := range c.IDs {
		if r := check.Rank(id); r < best {
			best = r
		}
	}
	return best
}
