// Package preflight holds the CLI's local, before-any-network-write
// checks and the engine that runs them.
//
// The ordering principle the whole package exists to serve: LOCAL TRUTHS
// BEFORE GLOBAL STATE. A project that cannot deploy makes zero network
// calls, so nothing here reaches the network, writes a file, or executes
// any of the user's code.
//
// IT READS FILES UNDER THE PROJECT DIRECTORY, WITH ONE NAMED EXCEPTION,
// and the exception is written down here because the sentence above used
// to end "and nothing else". The lockfile check stats a handful of names
// in each ANCESTOR of the project directory, up to the filesystem root,
// to tell a package inside a monorepo from a standalone project with no
// lockfile at all. It only reads, it follows no link out of the tree, it
// stops at the root, and no verdict depends on what it finds — it
// chooses which of two messages a person is shown, because the standing
// advice, create a lockfile, is actively wrong for somebody whose
// lockfile exists three directories up.
//
// The RESULT MODEL these checks emit lives in its own leaf package, not
// here, because the file walk and the check that reads the walk's output
// would otherwise import each other through it. The filesystem seam
// below stays here: only the result model moved.
package preflight
