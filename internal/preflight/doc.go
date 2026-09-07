// Package preflight holds the CLI's local, before-any-network-write
// checks and the engine that runs them.
//
// The ordering principle the whole package exists to serve: LOCAL TRUTHS
// BEFORE GLOBAL STATE. A project that cannot deploy makes zero network
// calls, so nothing here reaches the network, writes a file, or executes
// any of the user's code — it reads files under the project directory
// and nothing else.
//
// The RESULT MODEL these checks emit lives in its own leaf package, not
// here, because the file walk and the check that reads the walk's output
// would otherwise import each other through it. The filesystem seam
// below stays here: only the result model moved.
package preflight
