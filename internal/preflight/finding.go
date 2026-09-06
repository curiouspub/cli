// Package preflight holds the shared result type the CLI's local,
// before-any-network-write checks emit. The type is scaffolded here,
// ahead of the engine that runs the checks and the checks themselves,
// because more than one of those checks needs to construct one.
package preflight

// Severity is how serious a Finding is. A hard stop blocks the deploy
// outright; a warning is shown and may be proceeded past — the CLI and
// MCP surfaces each decide separately what "past" means for their own
// flow, but the record of having found it is the same either way.
type Severity string

const (
	SeverityHardStop Severity = "hard_stop"
	SeverityWarning  Severity = "warning"
)

// Finding is one pre-flight check's result: which check produced it, how
// serious it is, and what to tell the person or agent running the
// deploy.
type Finding struct {
	CheckID  string
	Severity Severity
	Message  string
}
