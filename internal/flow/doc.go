// Package flow holds the interactive, CLI-mode renderer and the deploy
// sequence that ties pre-flight, packing, upload and log streaming
// together into what a person running `curious deploy` sees.
//
// The pre-flight renderer and the login machine are here today; the
// sequence around them lands in a later change, and nothing in this
// package is wired into `curious deploy` yet. This is where the
// DECISIONS about a result are made rather than where the result is
// produced — what a warning costs, what a hard stop costs, and what to
// do when there is nobody to ask — which is the split that lets the
// agent-facing surface be a second renderer instead of a second set of
// checks.
//
// # Why the login is a state machine and not a function
//
// One rule shapes it: a wrong or expired code never restarts the flow.
// Every obvious implementation breaks that rule the same way — the
// verify call fails, the function returns the error, and the caller
// begins again at "what is your email?" — so somebody who mistyped one
// digit is asked to retype their address while the code they were sent
// is still valid and now out of reach. Writing it with the states named
// is what makes the rule survive the error paths, because every recovery
// has to name the state it goes to, and no failure edge names the first
// one.
//
// The second thing shaping it is what the server will not say. The step
// that sends a code answers identically whether it sent one, silently
// declined, was over a send budget or was in a cooldown, so this client
// is blind by design and its copy has to be honest about a state it
// cannot observe: where a code would go, and what to do when nothing
// arrives — never that one is on its way.
package flow
