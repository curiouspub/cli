// Package api is the HTTP client this CLI uses to talk to the public
// /v1 API: capacity checks, the email + code login flow, deploy
// creation, the call that starts a build, the build-log event stream,
// and the call that gives a built deploy an address. It knows only
// pkg/wire's types; it never reshapes them into a second, "nicer"
// shape, and it never invents an endpoint the contract does not define.
//
// The upload is deliberately NOT here. It goes to a link the server
// signed, at an origin this client holds no credential for and answers in
// a vocabulary the /v1 contract does not define, so it belongs to the
// sequence that owns the archive rather than to the client that speaks
// this API.
//
// # Construction
//
// New resolves a base URL — an explicit argument, else CURIOUS_API_URL,
// else the compiled-in default — through an address guard before
// building anything: a base URL that is not https, and not http to a
// loopback host, is refused outright, because a typo'd environment
// variable must never send a bearer token over the wire in clear. See
// validateBaseURL's own doc comment for the guard itself.
//
// # Error mapping
//
// Any non-2xx response decodes into an *APIError carrying the HTTP
// status, the wire.ErrorCode, the server's own message, and any
// Retry-After the response carried. A code this binary has never seen is
// not a parse failure — pkg/wire's contract is additive-only, so an
// unrecognised code is carried through with its message intact rather
// than replaced or swallowed. Only a body that is not the error envelope
// at all (an HTML page from a proxy, an empty or truncated body) falls
// back to a generic message; the raw body never reaches the terminal.
//
// # Retries
//
// A request is retried, at most twice with a small backoff, only for
// connection-level failures and 5xx responses, and only for the two
// calls the wire contract documents as idempotent (Capacity, Waitlist).
// auth/start and auth/verify are never retried: the first spends part of
// a per-identity hourly send budget on every attempt, and the second
// consumes its code atomically, so a retry after an ambiguous timeout
// could report failure for a call that had already succeeded. The create
// is never retried because a repeat makes a second deploy record, the
// start is never retried because everything a caller is waiting for
// afterwards arrives on the event stream — see DeployStart — and the
// publish is never retried because whether it landed is the very
// question a retry would be asking, and asking twice cannot answer it.
//
// # The event stream is not a request in that sense
//
// DeployEvents is the one call that does not go through the shared path,
// and it takes NO deadline of any kind. Every other call here is bounded
// end to end by the per-request timeout, which is right for a small
// request and a small answer and fatal for a connection held open for the
// whole of a build: a total deadline cannot express "is this making
// progress", and a server's keep-alive frames cannot extend one. What
// ends that request is the caller's context.
package api
