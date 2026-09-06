// Package api is the HTTP client this CLI uses to talk to the public
// /v1 API: capacity checks and the email + code login flow today, and —
// in a later change — deploy creation, upload, and the build-log event
// stream. It knows only pkg/wire's types; it never reshapes them into a
// second, "nicer" shape, and it never invents an endpoint the contract
// does not define.
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
// could report failure for a call that had already succeeded.
package api
