package ui

// FailureID is a failure's stable public identity.
//
// # Why a failure needs one
//
// A failure's identity used to be its headline text, which made every
// consumer of it fragile: a troubleshooting entry keyed to wording rots
// the first time the wording improves, a support answer cannot cite
// anything, and an agent reading a refusal has no token to match on.
//
// # What an id is, and what it is not
//
// An id names THE CAUSE AS THE USER MEETS IT — not the stage that
// noticed, and not the code path that raised it. `upload-link-expired`,
// never `publish-403`. It is short, lowercase, hyphenated and
// vendor-neutral, it carries no task number and no package name, and it
// is a PUBLIC ANCHOR: renaming one is a migration with a stated reason,
// never a tidy-up.
//
// # One family, one id
//
// A family is the same diagnosed condition and the same recovery
// contract. Wording, paths, hosts, timestamps and the stage that
// observed it do not create families; materially different diagnoses or
// remedies normally do. So the kill switch met at five stages is one id,
// and four ways of failing to resolve a project directory are four.
//
// Where a family corresponds to one of internal/check's checks the
// relationship stays visible in the name: a check with exactly one
// failure uses the check's own id, and a check with several uses
// `<check>-<cause>`. **The check's own id is never reused as a family id
// where the check has more than one failure** — `astro-dep` is a check,
// and `astro-dep-absent` is one of the five failures under it.
type FailureID string

// The catalog. Grouped by where a reader meets the cause rather than by
// package, because that grouping is the one the troubleshooting section
// is written from.
const (
	// Reaching the service at all.
	IDServerUnanswered         FailureID = "server-unanswered"
	IDServiceUnavailable       FailureID = "service-unavailable"
	IDCapacityCheckUnreachable FailureID = "capacity-check-unreachable"
	IDCapacityCheckFailed      FailureID = "capacity-check-failed"
	IDDailyCapacityClosed      FailureID = "daily-capacity-closed"
	IDRateLimited              FailureID = "rate-limited"
	IDClientRequestRejected    FailureID = "client-request-rejected"
	IDServerAnswerUnrecognised FailureID = "server-answer-unrecognised"

	// This machine, before anything is sent.
	IDProjectDirUnknown        FailureID = "project-dir-unknown"
	IDProjectDirMissing        FailureID = "project-dir-missing"
	IDProjectDirUnreadable     FailureID = "project-dir-unreadable"
	IDProjectPathNotADirectory FailureID = "project-path-not-a-directory"
	IDProjectUnreadable        FailureID = "project-unreadable"
	IDConfigLocationUnusable   FailureID = "config-location-unusable"
	IDAPIAddressUnusable       FailureID = "api-address-unusable"
	IDTempDirUnusable          FailureID = "temp-dir-unusable"

	// Logging in.
	IDLoginNotSaved        FailureID = "login-not-saved"
	IDLoginRefused         FailureID = "login-refused"
	IDLoginEndpointMissing FailureID = "login-endpoint-missing"

	// Sending the archive.
	IDArchiveUnreadable        FailureID = "archive-unreadable"
	IDUploadAddressUnusable    FailureID = "upload-address-unusable"
	IDUploadStalled            FailureID = "upload-stalled"
	IDUploadLinkExpired        FailureID = "upload-link-expired"
	IDUploadHostUnreachable    FailureID = "upload-host-unreachable"
	IDUploadConnectionLost     FailureID = "upload-connection-lost"
	IDUploadRedirected         FailureID = "upload-redirected"
	IDUploadRefusedUnexplained FailureID = "upload-refused-unexplained"
	IDUploadSignatureMismatch  FailureID = "upload-signature-mismatch"
	IDUploadAnswerUnrecognised FailureID = "upload-answer-unrecognised"

	// Building and publishing.
	IDBuildFailed                FailureID = "build-failed"
	IDBuildLogLost               FailureID = "build-log-lost"
	IDBuildOutputRefused         FailureID = "build-output-refused"
	IDPublishNotConfirmed        FailureID = "publish-not-confirmed"
	IDPublishedAddressInvalid    FailureID = "published-address-invalid"
	IDDeployUnknownToServer      FailureID = "deploy-unknown-to-server"
	IDDeployNotCompletedByServer FailureID = "deploy-not-completed-by-server"

	// The waitlist, when the door is closed.
	IDWaitlistDeclined      FailureID = "waitlist-declined"
	IDWaitlistNeedsTerminal FailureID = "waitlist-needs-terminal"
	IDWaitlistSignupFailed  FailureID = "waitlist-signup-failed"

	// The pre-flight, which refuses before anything leaves the machine.
	// THE CHECK'S OWN ID IS THE FAMILY ID where the check has exactly one
	// failure, and `<check>-<cause>` where it has several.
	IDAstroDepMissing     FailureID = "astro-dep-missing"
	IDAstroDepUnreadable  FailureID = "astro-dep-unreadable"
	IDAstroDepInvalidJSON FailureID = "astro-dep-invalid-json"
	IDAstroDepNotObject   FailureID = "astro-dep-not-object"
	IDAstroDepAbsent      FailureID = "astro-dep-absent"
	IDLockfileUnsupported FailureID = "lockfile-unsupported"
	IDLockfileWorkspace   FailureID = "lockfile-workspace"
	IDLockfileMissing     FailureID = "lockfile-missing"
	IDLimitFiles          FailureID = "limit-files"
	IDLimitFileSize       FailureID = "limit-file-size"
	IDLimitTotal          FailureID = "limit-total"
	IDLimitPacked         FailureID = "limit-packed"
	IDPathCharset         FailureID = "path-charset"
	IDProjectNotReady     FailureID = "project-not-ready"

	// This program's own faults, and the two questions it cannot ask.
	IDNeedsATerminal      FailureID = "needs-a-terminal"
	IDAnswerNotUnderstood FailureID = "answer-not-understood"
	IDInternalFault       FailureID = "internal-fault"
)

// ActiveFailureIDs is the catalog as a set, and it is what makes the
// catalog CONSUMABLE rather than merely declared.
//
// A guard asserts that every failure the tree constructs carries an id
// from this list, and that every entry here is reachable. The README's
// troubleshooting section is generated against it, so an id added here
// without an entry there fails, and an entry there naming an id absent
// here fails too.
//
// RETIRED IDS STAY RESERVED. An id that stops being reachable is removed
// from this list and recorded as retired; it is never reissued to a
// different cause, because a public anchor pointing at two different
// things over time is worse than one pointing at nothing.
var ActiveFailureIDs = []FailureID{
	IDServerUnanswered, IDServiceUnavailable, IDCapacityCheckUnreachable,
	IDCapacityCheckFailed, IDDailyCapacityClosed, IDRateLimited,
	IDClientRequestRejected, IDServerAnswerUnrecognised,

	IDProjectDirUnknown, IDProjectDirMissing, IDProjectDirUnreadable,
	IDProjectPathNotADirectory, IDProjectUnreadable, IDConfigLocationUnusable,
	IDAPIAddressUnusable, IDTempDirUnusable,

	IDLoginNotSaved, IDLoginRefused, IDLoginEndpointMissing,

	IDArchiveUnreadable, IDUploadAddressUnusable, IDUploadStalled,
	IDUploadLinkExpired, IDUploadHostUnreachable, IDUploadConnectionLost,
	IDUploadRedirected, IDUploadRefusedUnexplained, IDUploadSignatureMismatch,
	IDUploadAnswerUnrecognised,

	IDBuildFailed, IDBuildLogLost, IDBuildOutputRefused, IDPublishNotConfirmed,
	IDPublishedAddressInvalid, IDDeployUnknownToServer,
	IDDeployNotCompletedByServer,

	IDWaitlistDeclined, IDWaitlistNeedsTerminal, IDWaitlistSignupFailed,

	IDAstroDepMissing, IDAstroDepUnreadable, IDAstroDepInvalidJSON,
	IDAstroDepNotObject, IDAstroDepAbsent, IDLockfileUnsupported,
	IDLockfileWorkspace, IDLockfileMissing, IDLimitFiles, IDLimitFileSize,
	IDLimitTotal, IDLimitPacked, IDPathCharset, IDProjectNotReady,

	IDNeedsATerminal, IDAnswerNotUnderstood, IDInternalFault,
}
