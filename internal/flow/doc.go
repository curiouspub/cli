// Package flow holds the interactive, CLI-mode renderer and the deploy
// sequence that ties pre-flight, packing, upload and log streaming
// together into what a person running `curious deploy` sees.
//
// The pre-flight renderer is here today; the sequence around it lands in
// a later change. This is where the DECISIONS about a result are made
// rather than where the result is produced — what a warning costs, what
// a hard stop costs, and what to do when there is nobody to ask — which
// is the split that lets the agent-facing surface be a second renderer
// instead of a second set of checks.
package flow
