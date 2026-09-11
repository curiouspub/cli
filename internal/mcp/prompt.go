package mcp

import (
	"fmt"

	"github.com/curiouspub/cli/internal/flow"
	"github.com/curiouspub/cli/internal/ui"
)

// The sequence's own seams, satisfied here. Without these lines either
// could drift into something only this package implements, and the drift
// would not show up until a step this surface does not exercise asked
// for a method nothing supplies.
var (
	_ flow.DeployPrompter   = (*agentPrompter)(nil)
	_ flow.CapacityPrompter = quietPrompter{}
)

// recentBuildLines is how many lines of the build's own output a failed
// deploy hands back.
//
// WHAT IT BOUNDS IS A MESSAGE an agent reads in one piece, and the tail
// is the useful end: a build log's beginning is a package manager's
// inventory and whatever went wrong is at the bottom. It is deliberately
// chosen here rather than borrowed from the step that bounds a status
// report — that one bounds an answer to "how is it going", this one
// bounds the explanation attached to a refusal, and a constant carries
// its number rather than the reason the number was picked.
const recentBuildLines = 60

// agentPrompter is the terminal the deploy sequence is handed when there
// is no terminal.
//
// # It answers the one question it can and refuses the rest
//
// Confirm says YES. The only question the sequence asks that reaches
// here is the pre-flight one — "continue anyway?" over warnings — and
// warnings are non-blocking on this surface by design: a prompt reached
// over a pipe is a hang, and the findings travel back in the result
// instead, where an agent can act on them. Answering it is therefore the
// behaviour, not a workaround for the absence of a person.
//
// THAT IS A BLUNT INSTRUMENT AND ITS ONE SHARP EDGE IS STATED. There is a
// second question behind a shut account cap — whether to join the
// waitlist — and this would answer that yes too. It cannot do any harm:
// the very next thing that path does is ask for an email address, which
// this refuses, and the sequence then ends with the copy written for a
// closed door met with nobody to ask. So the offer is never made rather
// than made on somebody's behalf, and the ending is the right one.
//
// Line and Email REFUSE, which is what makes the paragraph above true
// and is also how a deploy with no stored login ends: the sequence would
// log in, the login asks for an address, and the refusal becomes a
// refusal naming the two tools that do it instead.
//
// # What it collects, and why each half is kept
//
// The build's own output is kept because a failed build's explanation IS
// the log — the copy the sequence produces says so in as many words —
// and an agent that was not watching progress has seen none of it. A
// bounded tail of it is attached to the refusal, which is the difference
// between a message that names where to look and one that points at
// something the reader never received.
//
// The narration is kept for the same reason one level down: it is the
// only place some facts are said at all — why a stored login was not
// usable, that capacity is running low, what the archive weighed.
type agentPrompter struct {
	build []string
	notes []string
}

func (p *agentPrompter) Step(format string, args ...any) {
	p.notes = append(p.notes, fmt.Sprintf(format, args...))
}

func (p *agentPrompter) Result(format string, args ...any) {
	p.build = append(p.build, fmt.Sprintf(format, args...))
	if len(p.build) > recentBuildLines {
		p.build = p.build[len(p.build)-recentBuildLines:]
	}
}

func (p *agentPrompter) Line(string) (string, error)  { return "", ui.ErrNotInteractive }
func (p *agentPrompter) Email(string) (string, error) { return "", ui.ErrNotInteractive }

func (p *agentPrompter) Confirm(string, bool) (bool, error) { return true, nil }

// quietPrompter is the terminal the capacity gate is handed, and it is a
// SECOND TYPE rather than the one above with its answers changed.
//
// The gate's question is the waitlist offer, and the honest answer to it
// here is that there is nobody to offer it to — so this refuses, and the
// gate ends with the copy written for a closed door met over a pipe,
// which names the terminal a person would need to join the list. That is
// the opposite of the answer the deploy prompter gives, for the opposite
// reason: there, saying yes is the spec'd behaviour for warnings, and
// the offer is reached only as a neighbour of it.
//
// Its narration goes nowhere. The gate's one line is a heads-up that
// capacity is running low, and the tool it sits inside is about to
// succeed or refuse with a sentence of its own; a line about how many
// slots are left, attached to a login that worked, is noise an agent
// would have to decide what to do with.
type quietPrompter struct{}

func (quietPrompter) Step(string, ...any)                {}
func (quietPrompter) Email(string) (string, error)       { return "", ui.ErrNotInteractive }
func (quietPrompter) Confirm(string, bool) (bool, error) { return false, ui.ErrNotInteractive }
