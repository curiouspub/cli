package check

import (
	"fmt"
	"strings"
)

// Report is a combined, VALIDATED result — the only shape a surface is
// allowed to render.
//
// IT CANNOT BE BUILT OUTSIDE THIS PACKAGE. Its fields are unexported, so
// the zero value is the only one a caller can name, and the zero value
// is not valid. Combine is the single thing that returns a usable one.
//
// THAT UNFORGEABILITY IS THE WHOLE DESIGN, and it is the answer to a
// class rather than to an instance. Every promise this package made was
// previously a promise about a CALL SITE: duplicates are refused if
// somebody calls Combine, coverage is checked if somebody calls
// CoverageGaps, and a caller holding a producer's raw output could
// render it having asked neither. A renderer that requires a Report
// cannot be handed something nobody validated, and there is no second
// door to keep in step with the first.
type Report struct {
	findings []Finding
	manifest Manifest

	// validated is the marker only Combine sets. It is what makes the
	// zero value distinguishable from a real report whose producers
	// happened to find nothing — which is the same "found nothing versus
	// never looked" distinction the manifest exists for, one level up.
	validated bool
}

// Valid reports whether this Report came from Combine. A surface handed
// the zero value has been handed something that never passed the gate,
// and should say so as a programming error rather than render it.
func (r Report) Valid() bool { return r.validated }

// Findings returns the validated findings, in report order.
//
// A FRESH SLICE each call, AND FRESH SLICES INSIDE IT. A caller that
// sorted or reordered what it was handed would otherwise change what the
// next reader of the same Report sees, and a validated article that
// changes under its readers is not one.
//
// The outer copy alone was not enough, and the gap was the same one
// Combine closes at the other end. Combine deep-copies on the way IN so a
// producer cannot reach into a finished report; this copies on the way
// OUT so a reader cannot either — including into the length disagreement
// between Paths and Sizes that the gate refuses at construction. A
// validated article its readers can invalidate is not validated, it is
// merely validated once.
//
// It reuses Combine's own copy rather than repeating the field list,
// because the day a third slice joins Finding is the day two hand-written
// copies stop agreeing.
func (r Report) Findings() []Finding {
	if len(r.findings) == 0 {
		return nil
	}
	out := make([]Finding, len(r.findings))
	for i, f := range r.findings {
		out[i] = copyFinding(f)
	}
	return out
}

// Manifest returns the validated manifest, in report order, as its own
// slice for the same reason Findings does.
func (r Report) Manifest() Manifest {
	if len(r.manifest) == 0 {
		return nil
	}
	out := make(Manifest, len(r.manifest))
	copy(out, r.manifest)
	return out
}

// CoverageError is what Combine returns when the producers between them
// do not answer exactly the declared set of questions.
//
// Both directions are carried because they are two different failures. A
// declared check with no row is a silent hole: nobody looked, and
// nothing in the report says so. A row for an id nobody declared is a
// producer answering a question that is not on the list — in practice a
// mistyped id, which reaches the user as the name of a check they have
// never heard of.
type CoverageError struct {
	Missing    []string
	Unexpected []string
}

func (e *CoverageError) Error() string {
	var parts []string
	if len(e.Missing) > 0 {
		parts = append(parts, "no producer covered "+strings.Join(e.Missing, ", "))
	}
	if len(e.Unexpected) > 0 {
		parts = append(parts, "a producer reported on "+strings.Join(e.Unexpected, ", ")+
			", which nothing declared")
	}
	return "the report does not cover the declared checks: " + strings.Join(parts, "; ")
}

// UnclaimedFindingError is what Combine returns when a finding reports
// on a check no manifest row claims.
//
// Manifest rows were refused and findings were not, so a producer could
// answer a question it never claimed — and where that finding then
// sorted came down to argument order, which is the caller's rule. The
// engine refuses to let a caller decide that for checks; the combiner
// should not hand it back at the other end.
type UnclaimedFindingError struct {
	CheckIDs []string
}

func (e *UnclaimedFindingError) Error() string {
	return fmt.Sprintf("a finding reports on %s, which no producer claimed to have run",
		strings.Join(e.CheckIDs, ", "))
}

// UndeclaredSeverityError is what Combine returns when a finding carries
// a severity that is not one of the three constants.
//
// THE ZERO VALUE IS THE CASE THIS EXISTS FOR. A producer that forgot to
// set one produced a finding that counted as advisory, that PROMPTED a
// person to decide about it, and that sorted after the notes no surface
// shows. Prompting on a field nobody set is the never-false-positive
// criterion broken by an omission rather than by a judgement.
type UndeclaredSeverityError struct {
	CheckIDs []string
}

func (e *UndeclaredSeverityError) Error() string {
	return fmt.Sprintf("a finding from %s carries no declared severity",
		strings.Join(e.CheckIDs, ", "))
}

// ContradictedFindingError is what Combine returns when a finding
// reports on a check the manifest says declined.
//
//	Skipped lockfile: couldn't read package.json
//	lockfile is stale
//
// Two adjacent lines about one id, disagreeing about whether anybody
// looked. The claimed-ids rule permits it by construction, because the
// id IS claimed — the row is there, it simply says the check declined.
//
// A check that looked enough to find something ANSWERED. A check that
// declined has nothing to report. There is no third case.
type ContradictedFindingError struct {
	CheckIDs []string
}

func (e *ContradictedFindingError) Error() string {
	return fmt.Sprintf("a finding reports on %s, which the manifest says was declined; "+
		"a check that found something answered", strings.Join(e.CheckIDs, ", "))
}

// EmptyMessageError is what Combine returns when a finding carries no
// message a person could read.
//
// THE OTHER FIVE ENFORCEMENTS ARE ALL ABOUT A FINDING'S ADDRESS — who
// claimed the id, whether the universe covers it, whether the severity
// exists, whether the id was answered. None is about its CONTENT, so a
// warning saying nothing at all passed the gate, rendered as a blank
// line, and still asked the user whether to continue. They decided about
// nothing, and a "no" stopped their deploy.
//
// This is the criterion this package's own Severity documents, finally
// asked where it can be enforced: AN ADVISORY NAMES SOMETHING THE USER
// CAN ACT ON OR OBSERVE, OR IT DOES NOT FIRE. A finding with no message
// names nothing by construction, whatever its severity.
type EmptyMessageError struct {
	CheckIDs []string
}

func (e *EmptyMessageError) Error() string {
	return fmt.Sprintf("a finding from %s carries no message; a finding that says nothing "+
		"renders as a blank line and still asks the reader to decide about it",
		strings.Join(e.CheckIDs, ", "))
}

// SizeMismatchError is what Combine returns when a finding's Sizes and
// Paths disagree in length.
//
// They are two parallel slices read by index, so a disagreement is not a
// cosmetic problem: a renderer pairing them prints one file's path
// against another file's measurement, or stops short of the list. Both
// are wrong quietly, and both get worse the longer the list, which is
// exactly where nobody is counting.
type SizeMismatchError struct {
	CheckIDs []string
}

func (e *SizeMismatchError) Error() string {
	return fmt.Sprintf("a finding from %s carries sizes that do not match its paths one "+
		"for one; they are read by index, so a mismatch renders one file's measurement "+
		"against another file's name", strings.Join(e.CheckIDs, ", "))
}

// Supersede returns a Report in which rows the earlier producers DECLINED
// are replaced by a later producer's real answers.
//
// WHY A REPORT NEEDS THIS AT ALL. Some checks cannot answer when the
// report a person consents to is built, because the thing they measure
// does not exist yet. The packed-size limit is the whole example: before
// anything is packed there is no archive, so the honest row says the
// check declined rather than answering with a zero. Then the archive is
// written, the check runs for real — and the validated report still
// carries the decline.
//
// That is TWO HOMES FOR ONE ID, and the machine-readable one is the
// false one: a person sees the refusal with its real numbers while an
// agent reading the manifest is told nothing was ever packed. The human
// surface being right is what makes it easy to miss.
//
// WHAT IT WILL AND WILL NOT DO, because a primitive that rewrites a
// validated article has to be narrow:
//
//   - only a DECLINED row may be replaced. An answered row is a producer
//     that looked and reported; overwriting it would let a later part of
//     a run quietly change what an earlier one found.
//   - only by a row of the SAME ID. There is no cross-id merging here.
//   - and therefore only by the producer that declined it: duplicate
//     refusal means exactly one producer claims any id in a report, so
//     replacing by id IS replacing by that producer. The rule needs no
//     producer field because the gate already made ids unique.
//
// Findings from the later results join the report, and the whole thing
// goes back through the same enforcements Combine applies — a superseded
// report is validated by the same rules or it is not validated at all.
func (r Report) Supersede(later Results) (Report, error) {
	claimed := make(map[string]Status, len(r.manifest))
	for _, row := range r.manifest {
		claimed[row.CheckID] = row
	}

	var unknown, notDeclined []string
	for _, row := range later.Manifest {
		existing, ok := claimed[row.CheckID]
		if !ok {
			unknown = append(unknown, row.CheckID)
			continue
		}
		if existing.Outcome != Declined {
			notDeclined = append(notDeclined, row.CheckID)
		}
	}
	sortIDs(unknown)
	sortIDs(notDeclined)
	if len(unknown) > 0 || len(notDeclined) > 0 {
		return Report{}, &SupersedeError{Unknown: unknown, NotDeclined: notDeclined}
	}

	replaced := make(map[string]Status, len(later.Manifest))
	for _, row := range later.Manifest {
		replaced[row.CheckID] = row
	}

	merged := Results{Findings: append([]Finding(nil), r.findings...)}
	merged.Findings = append(merged.Findings, later.Findings...)
	for _, row := range r.manifest {
		if answer, ok := replaced[row.CheckID]; ok {
			merged.Manifest = append(merged.Manifest, answer)
			continue
		}
		merged.Manifest = append(merged.Manifest, row)
	}
	return Combine(merged)
}

// SupersedeError is what Supersede returns when a later producer tried to
// replace a row it may not.
type SupersedeError struct {
	Unknown     []string
	NotDeclined []string
}

func (e *SupersedeError) Error() string {
	var parts []string
	if len(e.Unknown) > 0 {
		parts = append(parts, fmt.Sprintf("no row in the report claims %s",
			strings.Join(e.Unknown, ", ")))
	}
	if len(e.NotDeclined) > 0 {
		parts = append(parts, fmt.Sprintf("%s already answered, and an answer is not "+
			"a later producer's to overwrite", strings.Join(e.NotDeclined, ", ")))
	}
	return "refusing to supersede: " + strings.Join(parts, "; ")
}
