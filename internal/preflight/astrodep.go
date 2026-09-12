package preflight

import (
	"fmt"

	"github.com/curiouspub/cli/internal/check"
)

// AstroDepCheck is the astro-dep row as the engine registers it.
//
// It is a constructor rather than an exported variable so that the
// registration cannot be reordered or mutated by a caller holding it —
// every caller gets its own value, and the ids it claims are decided
// here rather than at the call site.
func AstroDepCheck() Check {
	return Check{IDs: []string{check.IDAstroDep}, Run: CheckAstroDep}
}

// CheckAstroDep answers the cheapest and most fundamental question this
// program asks: is this an Astro project at all?
//
// EVERY OUTCOME IS A HARD STOP OR SILENCE. There is no middle answer,
// because there is no half-deployable state here: either the project
// declares astro or the build has nothing to build. That makes the words
// matter more than usual — a hard stop is a dead end, and the reader is
// often somebody deploying their first site — so all four failures carry
// their own What, Why and Next rather than a one-line summary the
// renderer has to dress up.
//
// The copy lives HERE, beside the conditions that produce it, and that
// is a correction to where it used to live. Three of these sentences sat
// in the terminal package with nothing but their own tests reading them,
// because a finding could not carry copy and there was no way to deliver
// them. Words and condition are one thing; keeping them in two packages
// is how they come to disagree.
func CheckAstroDep(fsys FS, root string) Result {
	pkg := readPackageJSON(fsys, root)

	switch pkg.fault {
	case packageJSONMissing:
		return hardStop(check.IDAstroDep, check.FamilyAstroDepMissing, "There's no package.json in this directory.",
			noPackageJSONWhat, noPackageJSONWhy, wrongFolderNext)

	case packageJSONUnreadable:
		return hardStopAbout(check.IDAstroDep, check.FamilyAstroDepUnreadable, "package.json couldn't be read.",
			"curious couldn't read package.json.",
			"It's there, but this run couldn't open it — usually a permissions\n"+
				"problem on the file itself or on the directory holding it.",
			"Make package.json readable and run `curious deploy` again.")

	case packageJSONInvalid:
		return hardStopAbout(check.IDAstroDep, check.FamilyAstroDepInvalidJSON, "package.json isn't valid JSON.",
			"package.json isn't valid JSON.",
			invalidJSONWhy(pkg.position),
			invalidJSONNext(pkg.position))

	case packageJSONNotAnObject:
		return hardStopAbout(check.IDAstroDep, check.FamilyAstroDepNotObject, "package.json isn't a JSON object.",
			"package.json isn't a JSON object.",
			"curious reads package.json to check this is an Astro project, and this\n"+
				"one holds something else — an array, a string, or a bare null.",
			"package.json should start with `{` and hold the fields your package\n"+
				"manager writes. Fix it and try again.")
	}

	if declaresAstro(pkg.fields) {
		return Result{}
	}
	return hardStopAbout(check.IDAstroDep, check.FamilyAstroDepAbsent, "package.json doesn't list astro as a dependency.",
		notAnAstroProjectWhat, notAnAstroProjectWhy, wrongFolderNext)
}

// The copy for the two conditions this check shares a shape with. They
// are named constants rather than inline strings because two of the four
// failures end with the same instruction, and an instruction that is
// meant to be identical should be identical by construction.
const (
	notAnAstroProjectWhat = "This doesn't look like an Astro project."

	notAnAstroProjectWhy = "curious deploys Astro sites, and package.json here doesn't list\n" +
		"astro as a dependency."

	noPackageJSONWhat = "This doesn't look like an Astro project."

	noPackageJSONWhy = "curious deploys Astro sites, and every Astro project has a\n" +
		"package.json at its root. This directory doesn't have one."

	wrongFolderNext = "If this is the wrong folder, pass the right one:\n" +
		"`curious deploy ./my-site`."
)

// invalidJSONWhy names WHERE the file stopped parsing when the decoder
// gave a usable offset, and says only that it did not parse when it did
// not. A sentence promising a position and then printing none reads as
// broken output; a shorter sentence reads as a shorter sentence.
func invalidJSONWhy(position string) string {
	base := "curious reads package.json to check this is an Astro project, and this\n" +
		"one couldn't be parsed."
	if position == "" {
		return base
	}
	return fmt.Sprintf("%s The problem starts at %s.", base, position)
}

// invalidJSONNext points at the position when there is one, and gives
// the same instruction without it when there is not.
func invalidJSONNext(position string) string {
	if position == "" {
		return "Open package.json, fix the JSON, and try again."
	}
	return "Open package.json, fix the JSON at that point, and try again."
}

// hardStop builds a hard-stop result for a finding about the project as
// a whole — one that names no file to open, because there is none.
func hardStop(id string, family check.FailureFamily, message, what, why, next string) Result {
	return Result{Findings: []check.Finding{{
		CheckID:   id,
		FailureID: string(family),
		Severity:  check.SeverityHardStop,
		Message:   message,
		What:      what,
		Why:       why,
		Next:      next,
	}}}
}

// hardStopAbout is the same, for a finding about package.json — the file
// the reader has to open.
//
// The path is carried in the FIELD rather than folded into the prose,
// which is what the field is for: one of the surfaces reading these is a
// machine, and an agent should not have to parse English back out of a
// sentence to learn which file to open.
func hardStopAbout(id string, family check.FailureFamily, message, what, why, next string) Result {
	res := hardStop(id, family, message, what, why, next)
	res.Findings[0].Paths = check.NewPaths(packageJSONName)
	return res
}
