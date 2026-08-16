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
// # Enum vocabularies evolve additively too, and unknown members are RENDERED
//
// Some exported types in this package (DeployStatus, Phase, and others this
// package will grow later) are closed string enums, not free-form data —
// but the additive-only rule applies to them exactly as it applies to
// fields: a new member may be added to a vocabulary at any time, and a
// client built before that member existed must keep working. The
// obligation this places on every consumer is stated once, here, because
// it is the same obligation for every such type: an unknown value is
// RENDERED, never switched on exhaustively. A switch that lists every
// currently-known value and panics or errors on the rest looks correct
// right up until the server ships a value the switch predates — and for
// an enum carried on a streamed event (DeployStatus's and Phase's home),
// that failure mode is the most tempting to write by accident, because an
// unhandled case in a progress display looks like nothing at all rather
// than like an error. DeployStatus and Phase each restate this in their
// own terms, next to their constants, because that is the one place a
// client author is certain to look; it is stated here as the general rule
// so a future enum in this package inherits it without having to say so
// again.
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

// DeployCreateRequest is the body of POST /v1/deploys.
type DeployCreateRequest struct {
	// Bytes is the size, in bytes, of the PACKED tarball — a stat of the
	// file just written to disk, not the uncompressed pre-flight total the
	// client-side limits measure (see MaxSourceTotalBytes below).
	// The two numbers are easy to confuse and this is deliberately the
	// compressed one: the server signs the presigned PUT with this value
	// as an exact Content-Length, so a client that sends the wrong number
	// gets every upload refused by the object store, not a soft warning.
	Bytes int64 `json:"bytes"`
}

// DeployCreateResponse is the success body of POST /v1/deploys.
//
// There is deliberately no site_id field, in either direction. The data
// model already lets a site point at its current deploy, so redeploying an
// existing site is clearly intended eventually — but nothing yet knows
// whether the trial CLI can even name a site to redeploy, and there is no
// mechanism by which it would remember one. A field nothing populates is
// the worst kind to freeze into an additive-only contract: adding one later
// is always allowed, removing one never is.
//
// UploadURL also carries no expiry field: the client learns the URL has
// expired by the PUT failing, which it must handle regardless, and a
// second representation of the same fact is a second thing to keep true.
type DeployCreateResponse struct {
	DeployID  string `json:"deploy_id"`
	UploadURL string `json:"upload_url"`
}

// DeployStartResponse is the success body of POST /v1/deploys/{id}/start:
// an empty JSON object, for the same reason as WaitlistResponse. Reserved
// for future additive fields.
type DeployStartResponse struct{}

// DeployStatus is the lifecycle state of a deploy, as recorded in the
// deploy record's status attribute. It is what the record says the deploy IS —
// contrast Phase, which is what the build pipeline is DOING. The two
// vocabularies share two spellings ("queued", "building") for related but
// distinct moments, and are declared as distinct Go types for exactly that
// reason: the compiler refuses a comparison between a DeployStatus and a
// Phase, even where nothing stops a human eye sliding from one to the other
// while reading a log.
//
// An unknown status is rendered, never switched on exhaustively — see the
// package doc's enum-evolution rule. A CLI binary already in someone's
// hands must survive a status added to this vocabulary after it was built.
type DeployStatus string

const (
	StatusQueued   DeployStatus = "queued"
	StatusBuilding DeployStatus = "building"
	// StatusBuilt means output validated and stored, with nothing serving
	// it STEADILY — not simply "nothing serving it". The publish step
	// writes the edge routing entry *before* flipping the status to live,
	// deliberately, so a crash between those two writes leaves a deploy
	// that is still `built` while its site has already started serving.
	// Read this before assuming `built` means "not yet reachable": for one
	// crash-shaped window, it does not. The exception is bounded and
	// self-healing, not a standing risk: the site's expiry is written
	// earlier still, so the residue cannot outlive it, and publish is
	// idempotent, so a retry always completes the transition.
	StatusBuilt DeployStatus = "built"
	// StatusLive ships with no producer yet, deliberately: nothing emits it
	// at the time this constant is declared, but the vocabulary ships
	// complete anyway, because the cost of a client never having to think
	// about a status lands in installed binaries, not compile errors — see
	// the package doc.
	StatusLive   DeployStatus = "live"
	StatusFailed DeployStatus = "failed"
)

// AllDeployStatuses is every DeployStatus this contract defines, in
// declaration order — the lifecycle order, `built` between `building` and
// `live`. As with AllErrorCodes, this exists so neither side of the wire
// keeps a hand-written copy that can silently fall behind: the server
// ranges it to prove every status has a transition rule, the client ranges
// it to prove it renders every one. A new status is added to
// this slice in the same commit that declares the constant; the contract
// guards in this package fail a constant that is declared and not listed.
var AllDeployStatuses = []DeployStatus{
	StatusQueued,
	StatusBuilding,
	StatusBuilt,
	StatusLive,
	StatusFailed,
}

// Phase is a label for what the build pipeline is DOING right now, emitted
// on the `phase` SSE event — contrast DeployStatus, which is
// what the deploy record IS. "queued" and "building" appear in both
// vocabularies with related but distinct meanings, and Phase is a distinct
// Go type for exactly the reason DeployStatus is: the compiler refuses a
// comparison between them, which the reader has no compiler to check while
// staring at a log line.
//
// The control plane emits queued, starting and publishing; the build
// agent's own four internal phase names — extracting, installing,
// building, uploading — are translated onto the middle four constants
// below by a single server-side function, so an internal rename never
// reaches this public contract.
//
// An unknown phase is rendered as generic progress, never switched on
// exhaustively — see the package doc's enum-evolution rule, restated here
// in Phase's own terms because a stream is where a client is most tempted
// to switch exhaustively.
type Phase string

const (
	PhaseQueued     Phase = "queued"
	PhaseStarting   Phase = "starting"
	PhaseExtracting Phase = "extracting"
	PhaseInstalling Phase = "installing"
	PhaseBuilding   Phase = "building"
	PhaseUploading  Phase = "uploading"
	// PhasePublishing ships with no producer yet, deliberately, for the
	// same reason StatusLive does: the publish step is what will emit it,
	// and shipping the constant now costs one line while shipping it late
	// would cost every binary already in a user's hands.
	PhasePublishing Phase = "publishing"
)

// AllPhases is every Phase this contract defines, in declaration order —
// and that declaration order IS the documented lifecycle order. The
// server's exit test asserts that `phase` events arrive in this order and
// cites this slice directly; nothing else states the order, so the order
// and the statement of the order cannot drift apart. A new phase is added to
// this slice in the same commit that declares the constant, for the same
// reason as AllDeployStatuses and AllErrorCodes.
var AllPhases = []Phase{
	PhaseQueued,
	PhaseStarting,
	PhaseExtracting,
	PhaseInstalling,
	PhaseBuilding,
	PhaseUploading,
	PhasePublishing,
}

// LogEvent is the data: payload of the SSE `log` event on
// GET /v1/deploys/{id}/events. It carries the line and nothing else: the
// stream is combined stdout/stderr by design, so there is no separate
// stream to name, and a timestamp belongs to the
// persisted log object, which is server-side plain text. Both are addable
// later if a client ever needs them.
type LogEvent struct {
	Line string `json:"line"`
}

// PhaseEvent is the data: payload of the SSE `phase` event. Phases are
// progress labels, not control flow — see Phase's doc comment for the
// render-unknown obligation this event carries.
type PhaseEvent struct {
	Phase Phase `json:"phase"`
}

// DoneEvent is the data: payload of the SSE `done` event, marking the end
// of the stream. It carries only the final Status: url and expires_at are
// to be added later, additively, once the site is actually being served
// and there is something to make them true.
//
// The SSE `error` event reuses the existing wire.Error rather than a
// dedicated type — one error shape on the wire, not two, so a client that
// already switches on ErrorCode for HTTP responses switches on the same
// values here.
type DoneEvent struct {
	Status DeployStatus `json:"status"`
}

// The limit constants below are contract, not local policy: the MCP tool
// descriptions must state them, and the
// client and server must agree on the exact byte, because a client
// blocking at 30 MiB against a server enforcing 30 MB (decimal SI) is a
// class of bug nobody would find quickly. They are untyped so they compare
// and assign against either int or int64 call sites without a conversion.
//
// The allowed-extension list deliberately stays out of this package: it is
// an enforcement policy the trial tier may tighten, and freezing it in an
// additive-only contract would convert a policy into a promise. It lives
// server-side.
const (
	MaxSourceFiles      = 3_000
	MaxSourceFileBytes  = 5_000_000
	MaxSourceTotalBytes = 30_000_000
	MaxOutputFiles      = 1_000
	MaxOutputTotalBytes = 30_000_000
)
