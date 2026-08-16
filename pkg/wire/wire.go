// Copyright (c) 2026 Curious Pub
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to
// deal in the Software without restriction, including without limitation the
// rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
// sell copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
// FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER
// DEALINGS IN THE SOFTWARE.

// Package wire is the public, versioned contract for the curious.pub /v1
// HTTP API. It contains ONLY request/response types, JSON tags, and error
// codes — no HTTP client, no CLI logic, no config loading. Both the
// `curious` CLI and the private `platform` control plane import this
// package, and platform's server implementation must conform to it.
//
// # The contract is additive-only within v1
//
// Once a field, type, or error code in this package has shipped in a
// released CLI build, it may never be renamed, removed, retyped, or
// repurposed. New fields and new error codes may be added freely; nothing
// existing may change meaning underneath a client that is already in
// someone's hands.
//
// This matters because the CLI is distributed as a compiled binary that
// users update on their own schedule (or not at all). A server-side change
// that quietly renames a JSON field or reinterprets an error code breaks
// every copy of the CLI still running the old contract, with no way for
// the server to know which clients are affected. Treating v1 as
// additive-only — and encoding that discipline via the golden tests in
// wire_test.go — is what lets the server evolve without a coordinated
// client release.
//
// Unknown fields are IGNORED ON BOTH SIDES: clients tolerate newer
// servers, and servers tolerate newer clients. This is what makes
// additive evolution and rollback safe. Tolerance in one direction only
// is not a weaker version of the same property — it is a different and
// much worse one, because it turns every additive request field into a
// deployment-order obligation and makes rolling a server back a
// client-breaking act. TestForwardCompatibleDecoding pins the client
// half; the server half is the platform's own decoding discipline, and
// is stated here because this package is where both halves are agreed.
//
// Success is signalled by the HTTP status alone; error semantics live in
// the error envelope. No client behaviour may depend on the contents of a
// success body — several are empty objects, reserved for fields that may
// be added additively later.
//
// If a breaking change is ever truly necessary, it belongs in a new
// version (v2), not a mutation of v1.
package wire

import "time"

// ErrorCode identifies the kind of error returned by the /v1 API. This is
// the codes' only registry: consumers switch on these values, so they are
// contract in the same sense the JSON field names are. The server owns the
// mapping from code to HTTP status and must not define a second registry
// of its own.
type ErrorCode string

const (
	CodeBadRequest     ErrorCode = "bad_request"
	CodeUnauthorized   ErrorCode = "unauthorized"
	CodeForbidden      ErrorCode = "forbidden"
	CodeNotFound       ErrorCode = "not_found"
	CodeRateLimited    ErrorCode = "rate_limited"
	CodeCapacityClosed ErrorCode = "capacity_closed"
	CodeMaintenance    ErrorCode = "maintenance"
	CodeInternal       ErrorCode = "internal"
)

// AllErrorCodes is every ErrorCode this contract defines, in declaration
// order. It exists so that both sides of the wire can range the full set
// instead of each keeping a hand-written copy that silently falls behind:
// a client can prove it handles every code it may receive, and the server
// can prove — in a test rather than a comment — that every code it may be
// asked to write has an HTTP status and a Retry-After decision. Without
// it, a code added here is invisible to the other side until it appears
// in a response nobody wrote a branch for.
//
// A new code is added to this slice in the same commit that declares the
// constant; the contract guards in this package fail a constant that is
// declared and not listed. Callers must treat the slice as read-only —
// it is package-level state shared by every importer.
var AllErrorCodes = []ErrorCode{
	CodeBadRequest,
	CodeUnauthorized,
	CodeForbidden,
	CodeNotFound,
	CodeRateLimited,
	CodeCapacityClosed,
	CodeMaintenance,
	CodeInternal,
}

// retryAfterCodes is the set behind CarriesRetryAfter. It is unexported
// deliberately: an exported map is mutable by any importer, and this one
// states an obligation the server must not be able to edit at runtime.
var retryAfterCodes = map[ErrorCode]bool{
	CodeRateLimited:    true, // token bucket: retry after the bucket refills
	CodeCapacityClosed: true, // daily account cap: retry after resets_at
}

// CarriesRetryAfter reports whether a /v1 response using code always
// includes a Retry-After header. This is a property of the protocol, not
// of any one implementation: the server has no freedom to omit the header
// for these codes, and a client may rely on it being present.
//
// The name says what the contract guarantees rather than what a caller
// should do about it. It is NOT a general "should I retry?" — maintenance
// and internal are both worth retrying later, and neither carries the
// header, because neither has a reset time the server can honestly name.
//
// An unknown code reports false: a code this contract does not define
// carries no obligation, and inventing one for it would be a guess.
func CarriesRetryAfter(code ErrorCode) bool {
	return retryAfterCodes[code]
}

// Error is the machine-readable error carried in every non-2xx /v1
// response, wrapped in ErrorResponse.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
}

// ErrorResponse is the JSON body of every non-2xx /v1 response.
type ErrorResponse struct {
	Error Error `json:"error"`
}

// CapacityResponse is the body of GET /v1/capacity. Clients call this
// before any other work, and stop if Open is false, so that a user is
// never asked to do work that cannot land.
type CapacityResponse struct {
	Open         bool      `json:"open"`
	AccountsLeft int       `json:"accounts_left"`
	ResetsAt     time.Time `json:"resets_at"`
}

// WaitlistRequest is the body of POST /v1/waitlist.
type WaitlistRequest struct {
	Email string `json:"email"`
}

// WaitlistResponse is the success body of POST /v1/waitlist: an empty
// JSON object. It is deliberately empty — success is the HTTP status, and
// a per-endpoint constant string would carry no information a caller
// doesn't already have. Reserved for future additive fields.
type WaitlistResponse struct{}

// AuthStartRequest is the body of POST /v1/auth/start: the first step of
// the email + 6-digit-code login flow.
type AuthStartRequest struct {
	Email string `json:"email"`
}

// AuthStartResponse is the success body of POST /v1/auth/start: an empty
// JSON object, for the same reason as WaitlistResponse. Note that this
// response is deliberately identical whether or not a code was actually
// sent — the endpoint must not reveal whether an address is registered,
// rate-limited, or in cooldown — so there is nothing here for a client to
// branch on by design. Reserved for future additive fields.
type AuthStartResponse struct{}

// AuthVerifyRequest is the body of POST /v1/auth/verify: the second step
// of the login flow, submitting the code sent to Email.
type AuthVerifyRequest struct {
	Email          string `json:"email"`
	Code           string `json:"code"`
	MarketingOptIn bool   `json:"marketing_opt_in"`
}

// AuthVerifyResponse is the response body of POST /v1/auth/verify,
// carrying the bearer token for subsequent requests.
type AuthVerifyResponse struct {
	Token string `json:"token"`
}
