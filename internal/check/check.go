// Package check holds the result model the CLI's local, before-any-
// network-write checks emit, and nothing else.
//
// IT IMPORTS NOTHING ELSE IN THIS TREE, and that is the whole reason it
// is a package rather than a file. The checks and the engine that runs
// them do not all live together: one of them walks the project's files
// and emits findings of its own, and another reads that walk's file list
// to decide what to say. With the result model sitting inside the
// package that owns the engine, those two import each other and Go
// refuses to build it. A leaf both can import is the fix, and keeping it
// a leaf is what stops the cycle coming back.
package check

// Severity is how serious a Finding is. A hard stop blocks the deploy
// outright; a warning is shown and may be proceeded past — the CLI and
// MCP surfaces each decide separately what "past" means for their own
// flow, but the record of having found it is the same either way.
//
// A NOTE is neither. It is a fact a check established and deliberately
// does not put in front of anyone: kept in the structured result for a
// caller that asks for it, suppressed by every surface by default. It
// exists because of a criterion ruled 2026-09-07:
//
//	AN ADVISORY NAMES SOMETHING THE USER CAN ACT ON OR OBSERVE, OR IT
//	DOES NOT FIRE.
//
// The instance that minted it: a config-scanning check gained a gate
// that refuses a file it cannot read, and one of the two keys it reads
// then reported "I could not confirm this" on every project whose config
// contained a template literal — measured at two of the three real Astro
// configs available, on projects where nothing was wrong and nothing was
// observable. Deleting the fact would have been the other error, since
// "the scan gave up" and "the key is absent" are genuinely different
// states and a later reader may need to tell them apart. A note is the
// third option: recorded, not raised.
type Severity string

const (
	SeverityHardStop Severity = "hard_stop"
	SeverityWarning  Severity = "warning"
	SeverityNote     Severity = "note"
)

// Declared reports whether s is one of the three constants above.
//
// THE ZERO VALUE IS WHY THIS EXISTS. A producer that forgets to set a
// severity leaves the empty string, which is not a level anybody chose
// — and left unchecked it counted as advisory, prompted a person to
// decide about it, and sorted after the notes no surface shows.
// Prompting on a field nobody set is the never-false-positive criterion
// broken by an omission rather than by a judgement, so the gate refuses
// it and this is where the question is asked.

// The check ids, which are STABLE STRINGS rather than internal labels.
// A machine-readable result keys on them and a support answer names one,
// so the spelling is contract: changing one is changing something
// outside this program is keying on.
//
// They live here, in the leaf, because they are not all emitted from one
// place. The file walk emits four of its own from a package that must
// not import the engine, and a renderer that has to name a check it did
// not run needs the id without depending on whoever produces it.
const (
	IDAstroDep    = "astro-dep"
	IDLockfile    = "lockfile"
	IDPagesDir    = "pages-dir"
	IDBuildFormat = "build-format"
	IDLocalhost   = "localhost"

	// The four the file walk owns. They are stated here with the rest
	// rather than in the package that produces them for the same reason
	// the others are: the set has to be enumerable from ONE place, and a
	// renderer naming a check it did not run needs the id without
	// depending on whoever produced it.
	IDSymlinks      = "symlinks"
	IDCaseCollision = "case-collision"
	IDUnicodeMarks  = "unicode-marks"
	IDPathCharset   = "path-charset"
)

// Finding is one pre-flight check's result: which check produced it, how
// serious it is, and what to tell the person or agent running the
// deploy.
type Finding struct {
	CheckID  string
	Severity Severity
	Message  string

	// Paths names the files the finding is about, project-relative and
	// slash-separated, and is empty when the finding is about the
	// project as a whole.
	//
	// It is a field rather than something folded into Message because
	// more than one check has files to name and one of the surfaces
	// consuming these is a machine. With Message alone, every path goes
	// into prose and an agent has to parse English back out of it to
	// learn which file to open — and the prose is the part most likely
	// to be reworded later.
	//
	// SLASH-SEPARATED ON EVERY PLATFORM, deliberately: this value is
	// rendered into a result an agent may act on, and a separator that
	// changes with the machine that produced it is a difference nobody
	// asked for in a field meant to be compared.
	//
	// BUILD IT WITH NewPaths. A producer working from the standard
	// library's relative-path helper gets the host's separator, and this
	// struct accepts backslashes in silence — a documented contract with
	// nothing enforcing it is a contract that holds until the first
	// Windows user.
	Paths []string

	// What, Why and Next are OPTIONAL product copy: what happened, why
	// it happened, and what to do about it. A check that has worked its
	// words out carries them here; one that has not leaves them empty
	// and Message is the whole message.
	//
	// THE THIRD PART IS THE PRODUCT. A hard stop that names no action
	// leaves the reader to guess, and the reader is usually somebody
	// deploying their first site who has no model of what this program
	// does. "No lockfile found" is a fact; "run npm install, commit the
	// lockfile, try again" is a fix.
	//
	// They sit BESIDE Message rather than replacing it, because the two
	// serve different readers. A machine-readable result wants one line
	// it can put in a list; a person stopped mid-deploy wants three
	// paragraphs. Folding them into one field would make every surface
	// choose the wrong length for one of the two.
	//
	// Stored ALREADY WRAPPED and printed exactly as written, matching
	// how the terminal renders every other failure: re-wrapping to the
	// terminal's width would mean measuring the terminal, and it would
	// make a golden file a test of the terminal rather than of the copy.
	What string
	Why  string
	Next string
}

// HasCopy reports whether this Finding carries product copy of its own.
//
// ANY PART COUNTS, not all three. A check that wrote only the action —
// the part a reader can act on, and the one most often missing — has
// worked its copy out as far as it needed to, and answering "no" there
// would throw that sentence away.
//
// It is asked here rather than at each surface so that the terminal and
// the machine-readable result cannot disagree about whether a finding
// has copy — the sort of divergence nobody notices until the two render
// the same finding differently.
func (f Finding) HasCopy() bool {
	return f.What != "" || f.Why != "" || f.Next != ""
}

// Advisory reports whether this Finding is one a surface shows by
// default. It is the single place the note/not-note distinction is
// spelled, so a surface filters by asking rather than by comparing
// against a severity constant it might get wrong or forget to update
// when a fourth severity appears.
func (f Finding) Advisory() bool { return f.Severity != SeverityNote }

// Advisories returns only the findings a surface shows by default,
// preserving order. A surface that renders the raw slice instead is not
// wrong so much as it is choosing to show notes, and it should say so.
func Advisories(findings []Finding) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.Advisory() {
			out = append(out, f)
		}
	}
	return out
}

// Ran is one row per check the engine was ASKED to run, whether or not
// it produced a finding.
//
// It exists to pay for a findings-only stream. A check emits a Finding
// only when it has something to say, which means "found nothing" and
// "never looked" arrive at a surface as the same silence — and a skipped
// check rendered as a tick is a lie the reader will act on. The
// distinction is not recoverable from the findings, so it is recorded
// separately and exactly once.
type Ran struct {
	CheckID string
	Ran     bool
	Reason  string // why not, when Ran is false; empty otherwise
}

// Manifest is ordered and complete: one row per check id the engine was
// asked to cover, in the order results are reported.
//
// A SLICE RATHER THAN A MAP, deliberately. A map answers every lookup a
// caller of this type wants and renders in a different order on every
// run, and the report has to be byte-identical across two runs over one
// project.
type Manifest []Ran

// NotRun returns the rows for checks that did not run. A renderer reads
// not-run from HERE and never from the absence of a finding, which is
// the one mistake this type exists to make impossible: under a
// findings-only stream the two look identical from the outside.
func (m Manifest) NotRun() []Ran {
	var out []Ran
	for _, row := range m {
		if !row.Ran {
			out = append(out, row)
		}
	}
	return out
}

// Declared reports whether s is one of the three declared severities.
func (s Severity) Declared() bool {
	switch s {
	case SeverityHardStop, SeverityWarning, SeverityNote:
		return true
	}
	return false
}
