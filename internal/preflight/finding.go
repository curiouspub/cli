// Package preflight holds the shared result type the CLI's local,
// before-any-network-write checks emit. The type is scaffolded here,
// ahead of the engine that runs the checks and the checks themselves,
// because more than one of those checks needs to construct one.
package preflight

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

// Finding is one pre-flight check's result: which check produced it, how
// serious it is, and what to tell the person or agent running the
// deploy.
type Finding struct {
	CheckID  string
	Severity Severity
	Message  string
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
