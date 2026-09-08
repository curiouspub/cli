// Package flow holds the interactive, CLI-mode renderer and the deploy
// sequence that ties pre-flight, packing, upload and log streaming
// together into what a person running `curious deploy` sees.
//
// The sequence is here now and `curious deploy` runs it. It stops with a
// built deploy in hand: the archive is packed, uploaded and built, and
// the log of that build has been rendered — but publishing the result and
// printing its address arrive in a later change, so Deploy hands its
// caller a Handoff rather than a site anybody can visit.
//
// # Why the stream is a narrator and not an authority
//
// The event stream's terminating event carries the BUILDER's own claim.
// Output validation runs after it, and can still refuse what the build
// produced — so a client that treated that event as the settled truth
// would tell somebody their build succeeded and then report a failure at
// the next step, which is a description of the wrong event. This package
// renders what the stream said, stops only on a build the server says
// failed, and leaves the rest of the question to the call that can
// actually answer it.
//
// This is where the DECISIONS about a result are made rather than where
// the result is produced — what a warning costs, what a hard stop costs,
// and what to do when there is nobody to ask — which is the split that
// lets the agent-facing surface be a second renderer instead of a second
// set of checks.
//
// # Why the sequence lives here and not in the command
//
// The order the steps run in is a product decision with reasons — local
// truths before global state, the capacity check beside the login it
// gates, one walk feeding three readers — and every one of those reasons
// is testable without a terminal, a real endpoint or a subprocess. In
// the command it would be reachable only by running the binary, which is
// how an ordering rule comes to have no row that can see it.
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
