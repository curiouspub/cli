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

// declaredOrder is the fixed order pre-flight results are reported in,
// and it is the ENGINE's order rather than the caller's: cheapest and
// most fundamental first, so somebody standing in the wrong directory
// reads "this isn't an Astro project" as the first line and not as the
// fourth.
//
// build-format sits beside pages-dir because the same check produces
// both, off the same parse of the same file, and splitting them in the
// report would put two facts about one file in two places.
//
// The order is applied to whatever the caller registered, in whatever
// sequence. An order that merely mirrored the caller's slice would be
// the caller's rule, and a second caller assembling the same checks
// differently would get a different report out of the same project.
var declaredOrder = []string{
	check.IDAstroDep,
	check.IDLockfile,
	check.IDPagesDir,
	check.IDBuildFormat,
	check.IDLocalhost,
}

// rank places a check id in the declared order. An id nobody declared
// sorts after every id that was, rather than being dropped or panicking:
// a check that reports something unexpected is still reporting
// something, and the failure mode with no symptom is the one to avoid.
func rank(id string) int {
	for i, known := range declaredOrder {
		if known == id {
			return i
		}
	}
	return len(declaredOrder)
}

// severityRank orders the report's sections: every hard stop, then every
// warning, then the notes no surface shows by default.
func severityRank(s check.Severity) int {
	switch s {
	case check.SeverityHardStop:
		return 0
	case check.SeverityWarning:
		return 1
	case check.SeverityNote:
		return 2
	default:
		return 3
	}
}

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
func Run(checks []Check, fsys FS, root string) ([]check.Finding, check.Manifest) {
	ordered := make([]Check, len(checks))
	copy(ordered, checks)
	sort.SliceStable(ordered, func(i, j int) bool {
		return firstRank(ordered[i]) < firstRank(ordered[j])
	})

	type placed struct {
		finding check.Finding
		arrival int
	}
	var found []placed
	var manifest check.Manifest

	for _, c := range ordered {
		var result Result
		if c.Run != nil {
			result = c.Run(fsys, root)
		}

		for _, f := range result.Findings {
			found = append(found, placed{finding: f, arrival: len(found)})
		}
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
	// right sequence.
	sort.SliceStable(manifest, func(i, j int) bool {
		return rank(manifest[i].CheckID) < rank(manifest[j].CheckID)
	})

	// THE RANKING IS TOTAL, not merely sorted: severity, then declared
	// check order, then arrival. Two runs over one project have to
	// produce byte-identical output, and a comparison that leaves any
	// pair unordered gives that away to whatever the sort happens to do
	// with them.
	sort.SliceStable(found, func(i, j int) bool {
		a, b := found[i], found[j]
		if sa, sb := severityRank(a.finding.Severity), severityRank(b.finding.Severity); sa != sb {
			return sa < sb
		}
		if ra, rb := rank(a.finding.CheckID), rank(b.finding.CheckID); ra != rb {
			return ra < rb
		}
		return a.arrival < b.arrival
	})

	var findings []check.Finding
	for _, p := range found {
		findings = append(findings, p.finding)
	}
	return findings, manifest
}

// firstRank is a check's place in the running order: the earliest
// declared position among the ids it reports on.
func firstRank(c Check) int {
	best := len(declaredOrder)
	for _, id := range c.IDs {
		if r := rank(id); r < best {
			best = r
		}
	}
	return best
}
