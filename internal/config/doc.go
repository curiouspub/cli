// Package config loads and persists the CLI's local configuration: the
// bearer token, and the API endpoint that token was issued against.
//
// # It holds the only secret this binary keeps
//
// A token stored here spends one of an account's daily deploy slots and
// publishes to the internet under its owner's identity. There is no
// second copy of it anywhere on the machine and no way to re-derive it —
// losing the file costs a login, and leaking it costs more than that. So
// every decision below is a security decision rather than a
// housekeeping one, and each is written down beside the code that makes
// it.
//
// # Where the file lives
//
// In order: the CURIOUS_CONFIG environment variable (a full path to the
// file itself), then XDG_CONFIG_HOME/curious/config.json, then the
// platform default — see Path.
//
// CURIOUS_CONFIG is an escape hatch with two jobs. It lets someone keep
// a token outside their home directory, and it is what makes this
// package's own tests hermetic: a test that cannot redirect the path has
// to either read the developer's real token or skip itself.
//
// # What the file looks like
//
// A small JSON object carrying a schema version, the token, and the API
// base URL the token was issued against. It is versioned from the very
// first write, because adding a version to a format already in the field
// means guessing what the unversioned files meant.
//
// Fields this build does not know about are READ, KEPT and WRITTEN BACK.
// A newer release adds a field, an older release runs once, and the
// field survives — the same forward-compatible posture the wire contract
// takes with unknown JSON, applied to our own file. Without it, running
// an older binary once silently destroys state a newer one is relying
// on.
//
// That promise is about a particular flow, and the flow is named rather
// than assumed: LOAD, THEN SAVE ON THE VALUE LOAD RETURNED. The fields
// being preserved are the ones the read put there, so a value built
// fresh has none and preserves none — it writes the fields this build
// knows and claims nothing about any others. Saving is a Config method
// taking the token and the endpoint it was issued against together, so
// the value carrying the file's other state is the value that writes it.
//
// # The endpoint the token was issued against
//
// A token is only valid at the server that issued it. Sending a
// development token to production is a wasted request; sending a
// production token to a development server hands a real credential to
// whatever is listening on that port. So the file records the endpoint,
// Load compares it against the endpoint in force, and a token that does
// not match is treated as absent — the run asks for a fresh login rather
// than spending a credential somewhere it does not belong.
//
// # What it will not do
//
// Load never writes, never repairs and never deletes. A file it cannot
// understand is reported with the path and an instruction, and left
// exactly as it was: it is the only copy of a credential, and an
// automatic repair that guesses wrong destroys it with no way back.
package config
