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
// A FRESH SLICE each call. A caller that sorted or reordered what it was
// handed would otherwise change what the next reader of the same Report
// sees, and a validated article that changes under its readers is not
// one. The Paths inside are already this Report's own — Combine copied
// them at construction — so nothing here reaches back into a producer.
func (r Report) Findings() []Finding {
	if len(r.findings) == 0 {
		return nil
	}
	out := make([]Finding, len(r.findings))
	copy(out, r.findings)
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
