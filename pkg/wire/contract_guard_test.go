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

// This file closes the gaps that the fixture-driven golden tests in
// wire_test.go cannot see, each demonstrated by mutation before being
// written:
//
//  1. Adding `omitempty` to any field passed the whole golden suite,
//     because every golden value is non-zero. Dropping a field at its zero
//     value is a breaking change for clients that rely on its presence —
//     and `accounts_left: 0` is exactly the closed-capacity case the
//     endpoint exists to signal.
//  2. A new exported type could be added to the package and never
//     golden-tested, because nothing tied the package's surface to the
//     goldenCases table.
//  3. A new ErrorCode constant could be added and never pinned, because
//     the count check in TestErrorCodeConstants compares a literal table
//     to a literal number in the same file and never reads wire.go.
//  4. A new ErrorCode constant could be added and left out of
//     AllErrorCodes, which every consumer that ranges the set — the
//     server's status table and Retry-After pairing guards among them —
//     would then be structurally blind to. Pinning the constants (gap 3)
//     does not pin the enumeration of them.
//  5. A code's Retry-After obligation could be changed, or a new code
//     could join with no obligation decided for it at all, because
//     CarriesRetryAfter answers for any input and nothing forced its
//     answers to be stated anywhere.
//  6. A second exported package-level var could join the contract with no
//     guard at all. The AST guards above enumerate exported constants,
//     structs and non-struct types; none of them looks at var decls, and
//     AllErrorCodes — the package's first exported var — happens to be
//     pinned only because gap 4's guard reads it by name.
//  7. Two more string enums (DeployStatus, Phase) and five
//     integer constants (the Max* limits) to a constant guard that had,
//     until then, only ever needed to read strings. declaredErrorCodeValues
//     was deliberately type-blind for a reason gap 4's mutation proved:
//     `const CodeSneaky = ErrorCode("sneaky")` — the same constant written
//     as a conversion instead of a typed literal — walked past a
//     type-filtered predecessor. Five vocabularies now share one constant
//     block, so blindness cannot survive; every exported constant must be
//     partitioned into ErrorCode, DeployStatus, Phase or the limits. The
//     property gap 4's mutation actually established is narrower than
//     "never filter by type" and it is what survives the change: an
//     unclassifiable constant must fail LOUDLY, naming itself, never be
//     silently skipped or defaulted into a partition it does not belong
//     to. classifyExportedConstants below is the single partitioner all
//     four declaredXValues helpers read from, so the "loud failure, never
//     a skip" behaviour lives in one place rather than four copies that
//     could drift apart.
//
//  8. A NEW field carrying `omitempty` from birth bypassed gap 1
//     entirely — see omitempty_guard_test.go, which bans the tag rather
//     than enumerating its victims.
//  9. Exported FUNCS and METHODS had no guard at all, and gap 6's own
//     comment wrongly called vars "the one shape" that was unguarded —
//     see func_guard_test.go. A custom UnmarshalJSON was the sharp case:
//     it can revoke this package's unknown-field tolerance for one type
//     without touching a field or a tag, where no fixture can see it.
//
// These guards are enforced by parsing the AST of EVERY non-test .go file
// in this package — not only wire.go, despite what several comments below
// still say in passing. parseSources reads the directory, which is what
// makes a constant declared in a second file catchable; that is load-
// bearing, not incidental.

package wire

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestZeroValueMarshalsEveryField marshals the ZERO value of each wire type
// and asserts the emitted JSON carries exactly the same key set as the
// type's fixture. This is what makes an `omitempty` tag — added years from
// now, in good faith, by someone tidying up — fail CI instead of silently
// dropping a field from responses to already-released clients.
func TestZeroValueMarshalsEveryField(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.newEmpty())
			if err != nil {
				t.Fatalf("marshal zero value: %v", err)
			}
			assertSameKeySet(t, got, readFixture(t, tc.fixture))
		})
	}
}

// assertSameKeySet compares the key structure of two JSON documents,
// recursively, ignoring values entirely.
func assertSameKeySet(t *testing.T, gotJSON, wantJSON []byte) {
	t.Helper()
	var got, want any
	if err := json.Unmarshal(gotJSON, &got); err != nil {
		t.Fatalf("unmarshaling got: %v\n%s", err, gotJSON)
	}
	if err := json.Unmarshal(wantJSON, &want); err != nil {
		t.Fatalf("unmarshaling want: %v\n%s", err, wantJSON)
	}
	compareKeys(t, "", got, want)
}

func compareKeys(t *testing.T, path string, got, want any) {
	t.Helper()
	wantObj, ok := want.(map[string]any)
	if !ok {
		return // scalar or array — nothing to compare structurally
	}
	gotObj, ok := got.(map[string]any)
	if !ok {
		if got == nil {
			t.Fatalf("at %q: marshals to null at the zero value where the fixture has an "+
				"object. A pointer-to-struct field does this; decide deliberately whether "+
				"the contract wants a nullable field there.", path)
		}
		t.Fatalf("at %q: expected a JSON object, got %T", path, got)
	}
	for k, wv := range wantObj {
		gv, present := gotObj[k]
		if !present {
			t.Fatalf("at %q: field %q vanishes when the zero value is marshalled. "+
				"An omitempty tag does this, and dropping a field at its zero value is a "+
				"breaking change for any client that reads it (accounts_left:0 is the "+
				"closed-capacity signal). The /v1 contract is additive-only.", path, k)
		}
		compareKeys(t, path+"/"+k, gv, wv)
	}
	for k := range gotObj {
		if _, expected := wantObj[k]; !expected {
			t.Fatalf("at %q: field %q is emitted but absent from the fixture — "+
				"add it to the fixture deliberately, so the contract change is visible "+
				"in the diff", path, k)
		}
	}
}

// TestEveryExportedStructIsGoldenTested parses wire.go and asserts that
// every exported struct type appears in the goldenCases table. Without
// this, a type added to the public contract could ship with no fixture
// pinning its field names at all.
func TestEveryExportedStructIsGoldenTested(t *testing.T) {
	declared := declaredStructTypes(t)

	// Keyed on the Go type of the case's value, not the case name: a type
	// may legitimately have several cases (CapacityResponse has an open and
	// a closed one), and the case name is free text.
	covered := map[string]bool{}
	for _, tc := range goldenCases() {
		rt := reflect.TypeOf(tc.value)
		for rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		covered[rt.Name()] = true
	}

	// Types that never appear at the top level of a response body, and are
	// therefore pinned through their container's fixture instead. Keep this
	// list short and justified: every entry is a type whose field names are
	// NOT directly asserted by a fixture of its own.
	nestedOnly := map[string]bool{
		"Error": true, // always wrapped in ErrorResponse; its keys are pinned by error_response.json
	}

	for name := range declared {
		if covered[name] || nestedOnly[name] {
			continue
		}
		t.Errorf("exported type %s has no entry in goldenCases() — every type in the "+
			"public contract needs a fixture pinning its field names (or an explicit "+
			"nestedOnly justification)", name)
	}
	for name := range covered {
		if !declared[name] {
			t.Errorf("goldenCases() covers type %s, which is not an exported struct in wire.go", name)
		}
	}
	for name := range nestedOnly {
		if !declared[name] {
			t.Errorf("nestedOnly names %s, which is no longer declared in wire.go — "+
				"remove the exemption", name)
		}
	}

	// Exported named types that are not structs escape both guards above:
	// the struct guard skips them, and the constant guard only reads
	// strings. Each entry below must name the guard pair that actually
	// pins its constants — the same "name your guard" discipline as
	// TestEveryExportedVarIsGuarded's covered list, so this is an index of
	// coverage rather than a longer literal comparison a new type could
	// slip past unnoticed.
	nonStructGuarded := map[string]string{
		"ErrorCode":    "TestEveryErrorCodeConstantIsPinned + TestAllErrorCodesEnumeratesEveryConstant",
		"DeployStatus": "TestEveryDeployStatusConstantIsPinned + TestAllDeployStatusesEnumeratesEveryConstant",
		"Phase":        "TestEveryPhaseConstantIsPinned + TestAllPhasesEnumeratesEveryConstant",
		"EventType":    "TestEveryEventTypeConstantIsPinned + TestAllEventTypesEnumeratesEveryConstant",
	}
	nonStruct := declaredNonStructTypes(t)
	for name := range nonStruct {
		if _, ok := nonStructGuarded[name]; !ok {
			t.Errorf("exported non-struct type %s is unguarded — the struct and constant "+
				"guards cannot see it. Write a guard for it before it enters the frozen "+
				"contract, then admit it to nonStructGuarded in this test.", name)
		}
	}
	for name := range nonStructGuarded {
		if !nonStruct[name] {
			t.Errorf("expected %s to be declared as an exported non-struct type", name)
		}
	}
}

// TestEveryErrorCodeConstantIsPinned parses wire.go and asserts the set of
// declared ErrorCode constants is exactly the set pinned below. The count
// check in TestErrorCodeConstants cannot do this: it compares a literal
// table against a literal number in the same file, so a ninth constant
// added to wire.go would go unnoticed.
func TestEveryErrorCodeConstantIsPinned(t *testing.T) {
	// Deliberately duplicated from TestErrorCodeConstants. The duplication is
	// the point: this list is derived from the spec, the other from the Go
	// constants, and the AST comparison below is what forces them to agree.
	pinned := map[string]string{
		"CodeBadRequest":     "bad_request",
		"CodeUnauthorized":   "unauthorized",
		"CodeForbidden":      "forbidden",
		"CodeNotFound":       "not_found",
		"CodeRateLimited":    "rate_limited",
		"CodeCapacityClosed": "capacity_closed",
		"CodeMaintenance":    "maintenance",
		"CodeInternal":       "internal",
		"CodeDeployFailed":   "deploy_failed",
		"CodeDeployNotReady": "deploy_not_ready",
	}

	declared := declaredErrorCodeValues(t)

	for name, value := range declared {
		want, ok := pinned[name]
		if !ok {
			t.Errorf("ErrorCode constant %s (= %q) is declared in wire.go but not pinned by a "+
				"test — the exported IDENTIFIER is public API of this module just as the "+
				"string value is", name, value)
			continue
		}
		if value != want {
			t.Errorf("ErrorCode constant %s = %q, want %q — changing a shipped value breaks "+
				"every client already switching on it", name, value, want)
		}
	}
	for name := range pinned {
		if _, ok := declared[name]; !ok {
			t.Errorf("ErrorCode constant %s is pinned by tests but no longer declared in "+
				"wire.go — removing or renaming it is a breaking change within v1", name)
		}
	}
}

// TestAllErrorCodesEnumeratesEveryConstant parses wire.go and asserts
// AllErrorCodes lists exactly the declared ErrorCode constants, once each.
//
// TestEveryErrorCodeConstantIsPinned above proves a new constant cannot be
// added without a test noticing; it says nothing about the enumeration.
// A constant declared in wire.go but absent from AllErrorCodes is worse
// than an unpinned one: every consumer that ranges the set believes it has
// seen the whole contract, so the omission reads as "this code does not
// exist" rather than as a missing entry. The server's status table and its
// Retry-After pairing guard both range this slice, and would pass with a
// code they have no mapping for.
func TestAllErrorCodesEnumeratesEveryConstant(t *testing.T) {
	declared := declaredErrorCodeValues(t)

	listed := map[string]bool{}
	for _, code := range AllErrorCodes {
		if listed[string(code)] {
			t.Errorf("AllErrorCodes lists %q more than once — a consumer ranging the "+
				"set would handle it twice, and a duplicate can hide a missing entry "+
				"from a length check", code)
		}
		listed[string(code)] = true
	}

	declaredValues := valueSet(declared)
	for value := range declaredValues {
		if !listed[value] {
			t.Errorf("ErrorCode %q is declared in wire.go but missing from AllErrorCodes — "+
				"add it in the same commit as the constant, or every consumer that ranges "+
				"the set is blind to it", value)
		}
	}
	for value := range listed {
		if !declaredValues[value] {
			t.Errorf("AllErrorCodes contains %q, which is not declared as an exported "+
				"constant in wire.go — the enumeration may only name codes the contract "+
				"actually defines", value)
		}
	}
}

// TestEveryDeployStatusConstantIsPinned mirrors
// TestEveryErrorCodeConstantIsPinned for DeployStatus: it parses wire.go and
// asserts the set of declared DeployStatus constants is exactly the set
// pinned below.
func TestEveryDeployStatusConstantIsPinned(t *testing.T) {
	// Deliberately duplicated from TestDeployStatusConstants, for the same
	// reason as the ErrorCode pair: this list is derived from the spec, the
	// other from the Go constants, and the AST comparison below is what
	// forces them to agree.
	pinned := map[string]string{
		"StatusQueued":   "queued",
		"StatusBuilding": "building",
		"StatusBuilt":    "built",
		"StatusLive":     "live",
		"StatusFailed":   "failed",
	}

	declared := declaredDeployStatusValues(t)

	for name, value := range declared {
		want, ok := pinned[name]
		if !ok {
			t.Errorf("DeployStatus constant %s (= %q) is declared in wire.go but not pinned by a "+
				"test — the exported IDENTIFIER is public API of this module just as the "+
				"string value is", name, value)
			continue
		}
		if value != want {
			t.Errorf("DeployStatus constant %s = %q, want %q — changing a shipped value breaks "+
				"every client already switching on it", name, value, want)
		}
	}
	for name := range pinned {
		if _, ok := declared[name]; !ok {
			t.Errorf("DeployStatus constant %s is pinned by tests but no longer declared in "+
				"wire.go — removing or renaming it is a breaking change within v1", name)
		}
	}
}

// TestAllDeployStatusesEnumeratesEveryConstant mirrors
// TestAllErrorCodesEnumeratesEveryConstant for DeployStatus: it proves that
// AllDeployStatuses lists exactly the declared DeployStatus constants, once
// each. This is a set check only — TestAllDeployStatusesOrder in
// wire_test.go is what pins the order, and the two are deliberately
// separate: a status present but out of order would pass this test and
// fail that one, which is the point of keeping them apart.
func TestAllDeployStatusesEnumeratesEveryConstant(t *testing.T) {
	declared := declaredDeployStatusValues(t)

	listed := map[string]bool{}
	for _, status := range AllDeployStatuses {
		if listed[string(status)] {
			t.Errorf("AllDeployStatuses lists %q more than once — a consumer ranging the "+
				"set would handle it twice, and a duplicate can hide a missing entry "+
				"from a length check", status)
		}
		listed[string(status)] = true
	}

	declaredValues := valueSet(declared)
	for value := range declaredValues {
		if !listed[value] {
			t.Errorf("DeployStatus %q is declared in wire.go but missing from "+
				"AllDeployStatuses — add it in the same commit as the constant, or every "+
				"consumer that ranges the set is blind to it (the server's transition "+
				"table among them)", value)
		}
	}
	for value := range listed {
		if !declaredValues[value] {
			t.Errorf("AllDeployStatuses contains %q, which is not declared as an exported "+
				"constant in wire.go — the enumeration may only name statuses the contract "+
				"actually defines", value)
		}
	}
}

// TestEveryPhaseConstantIsPinned mirrors TestEveryErrorCodeConstantIsPinned
// for Phase: it parses wire.go and asserts the set of declared Phase
// constants is exactly the set pinned below.
func TestEveryPhaseConstantIsPinned(t *testing.T) {
	// Deliberately duplicated from TestPhaseConstants, for the same reason
	// as the ErrorCode and DeployStatus pairs.
	pinned := map[string]string{
		"PhaseQueued":     "queued",
		"PhaseStarting":   "starting",
		"PhaseExtracting": "extracting",
		"PhaseInstalling": "installing",
		"PhaseBuilding":   "building",
		"PhaseUploading":  "uploading",
		"PhasePublishing": "publishing",
	}

	declared := declaredPhaseValues(t)

	for name, value := range declared {
		want, ok := pinned[name]
		if !ok {
			t.Errorf("Phase constant %s (= %q) is declared in wire.go but not pinned by a "+
				"test — the exported IDENTIFIER is public API of this module just as the "+
				"string value is", name, value)
			continue
		}
		if value != want {
			t.Errorf("Phase constant %s = %q, want %q — changing a shipped value breaks "+
				"every client already switching on it", name, value, want)
		}
	}
	for name := range pinned {
		if _, ok := declared[name]; !ok {
			t.Errorf("Phase constant %s is pinned by tests but no longer declared in "+
				"wire.go — removing or renaming it is a breaking change within v1", name)
		}
	}
}

// TestAllPhasesEnumeratesEveryConstant mirrors
// TestAllErrorCodesEnumeratesEveryConstant for Phase: it proves that
// AllPhases lists exactly the declared Phase constants, once each. As with
// DeployStatus, this is a set check only — TestAllPhasesOrder in
// wire_test.go pins the lifecycle order the exit test cites, kept separate so a
// phase present but out of order fails that test rather than this one.
func TestAllPhasesEnumeratesEveryConstant(t *testing.T) {
	declared := declaredPhaseValues(t)

	listed := map[string]bool{}
	for _, phase := range AllPhases {
		if listed[string(phase)] {
			t.Errorf("AllPhases lists %q more than once — a consumer ranging the set "+
				"would handle it twice, and a duplicate can hide a missing entry from a "+
				"length check", phase)
		}
		listed[string(phase)] = true
	}

	declaredValues := valueSet(declared)
	for value := range declaredValues {
		if !listed[value] {
			t.Errorf("Phase %q is declared in wire.go but missing from AllPhases — add it "+
				"in the same commit as the constant, or every consumer that ranges the set "+
				"is blind to it", value)
		}
	}
	for value := range listed {
		if !declaredValues[value] {
			t.Errorf("AllPhases contains %q, which is not declared as an exported constant "+
				"in wire.go — the enumeration may only name phases the contract actually "+
				"defines", value)
		}
	}
}

// TestEveryLimitConstantIsPinned parses wire.go and asserts the set of
// declared limit constants is exactly the set pinned
// below, by both name and value. It exists for the same reason
// AllErrorCodes has TestEveryErrorCodeConstantIsPinned: a sixth limit added
// later must not be able to enter the contract unpinned. Five integers
// entering a constant guard that had only ever read strings is exactly
// where a sixth would slip in later, unnoticed, if this guard did not
// exist.
func TestEveryLimitConstantIsPinned(t *testing.T) {
	// Deliberately duplicated from TestLimitConstants, for the same reason
	// as the string-enum pairs above: this table is the independent
	// restatement the AST comparison below forces to agree with wire.go.
	pinned := map[string]int64{
		"MaxPackedBytes":      30_000_000,
		"MaxSourceFiles":      3_000,
		"MaxSourceFileBytes":  5_000_000,
		"MaxSourceTotalBytes": 30_000_000,
		"MaxOutputFiles":      1_000,
		"MaxOutputTotalBytes": 30_000_000,
	}

	declared := declaredLimitValues(t)

	for name, value := range declared {
		want, ok := pinned[name]
		if !ok {
			t.Errorf("limit constant %s is declared in wire.go but not pinned by a test — "+
				"a sixth limit must not be able to enter the contract unpinned", name)
			continue
		}
		if value != want {
			t.Errorf("limit constant %s = %d, want %d", name, value, want)
		}
	}
	for name := range pinned {
		if _, ok := declared[name]; !ok {
			t.Errorf("limit constant %s is pinned by tests but no longer declared in "+
				"wire.go — removing or changing a limit's name is a breaking change "+
				"within v1", name)
		}
	}
}

// TestEveryEventTypeConstantIsPinned mirrors its siblings for
// EventType: the event NAMES are contract strings both repos write onto
// and read off the wire, so a typo in either one is a stream nobody
// handles.
func TestEveryEventTypeConstantIsPinned(t *testing.T) {
	pinned := map[string]string{
		"EventLog":   "log",
		"EventPhase": "phase",
		"EventDone":  "done",
		"EventError": "error",
	}

	declared := declaredEventTypeValues(t)

	for name, value := range declared {
		want, ok := pinned[name]
		if !ok {
			t.Errorf("EventType constant %s (= %q) is declared in wire.go but not pinned "+
				"by a test", name, value)
			continue
		}
		if value != want {
			t.Errorf("EventType constant %s = %q, want %q — the event name is what a "+
				"client matches on; changing it breaks every released binary", name, value, want)
		}
	}
	for name := range pinned {
		if _, ok := declared[name]; !ok {
			t.Errorf("EventType constant %s is pinned by tests but no longer declared "+
				"in wire.go — removing it is a breaking change within v1", name)
		}
	}
}

// TestAllEventTypesEnumeratesEveryConstant proves AllEventTypes lists
// exactly the declared EventType constants, once each. A type declared
// and left out of the slice reads to every consumer that ranges it as
// "this event does not exist" — and for the event vocabulary that means
// a client silently skipping a stream it was meant to handle, which the
// skip-unknown rule makes indistinguishable from correct behaviour.
func TestAllEventTypesEnumeratesEveryConstant(t *testing.T) {
	declared := declaredEventTypeValues(t)
	declaredValues := valueSet(declared)

	listed := map[string]bool{}
	for _, e := range AllEventTypes {
		if listed[string(e)] {
			t.Errorf("AllEventTypes lists %q more than once", e)
		}
		listed[string(e)] = true
	}
	for value := range declaredValues {
		if !listed[value] {
			t.Errorf("EventType %q is declared in wire.go but missing from AllEventTypes", value)
		}
	}
	for value := range listed {
		if !declaredValues[value] {
			t.Errorf("AllEventTypes contains %q, which is not declared as an exported "+
				"constant in wire.go", value)
		}
	}
}

// TestCarriesRetryAfterIsPinned pins, per code, whether /v1 requires the
// response to carry Retry-After. The table below is a literal restatement
// of the spec, deliberately NOT derived from the retryAfterCodes map it
// checks: a guard that reads its expected value from the thing it guards
// passes forever and protects nothing.
//
// The obligation is protocol semantics, not a server implementation
// detail — a client reads Retry-After, and the server has no freedom to
// omit it — so a code joining the contract without a stated answer here
// is a decision that was never made, and fails rather than defaulting to
// false.
func TestCarriesRetryAfterIsPinned(t *testing.T) {
	pinned := map[ErrorCode]bool{
		CodeBadRequest:     false,
		CodeUnauthorized:   false,
		CodeForbidden:      false,
		CodeNotFound:       false,
		CodeRateLimited:    true, // token-bucket reset
		CodeCapacityClosed: true, // resets_at, always known
		CodeMaintenance:    false,
		CodeInternal:       false,
		// Neither carries one, and neither is an oversight. A failed
		// build has nothing to wait for. A build still running has no
		// honest figure — the server cannot say how long one has left,
		// and a made-up number is worse than none, because a client
		// would sleep on it and report a failure at the wrong moment.
		CodeDeployFailed:   false,
		CodeDeployNotReady: false,
	}

	listed := map[ErrorCode]bool{}
	for _, code := range AllErrorCodes {
		listed[code] = true

		want, stated := pinned[code]
		if !stated {
			t.Errorf("ErrorCode %q has no entry in this table — whether a code carries "+
				"Retry-After is a deliberate contract decision, not a default. State it "+
				"here and in retryAfterCodes.", code)
			continue
		}
		if got := CarriesRetryAfter(code); got != want {
			t.Errorf("CarriesRetryAfter(%q) = %v, want %v — changing a code's Retry-After "+
				"obligation changes what an already-released client is told to do", code, got, want)
		}
	}
	for code := range pinned {
		if !listed[code] {
			t.Errorf("ErrorCode %q is pinned here but absent from AllErrorCodes — removing "+
				"a code is a breaking change within v1", code)
		}
	}
}

// TestEveryExportedVarIsGuarded parses wire.go and asserts that every
// exported package-level var is named in the covered list below.
//
// A var is the one shape of exported surface the other AST guards are blind
// to: declaredStructTypes and declaredNonStructTypes read TYPE decls,
// declaredErrorCodeValues reads CONST decls. AllErrorCodes is guarded only
// because TestAllErrorCodesEnumeratesEveryConstant reaches for it by name —
// nothing generalises that to a second var. And a var is mutable state in a
// public package: a consumer can append to a slice or reassign it, so
// "which vars exist" is contract in a way that needs deciding, not
// inheriting.
//
// This guard does not say what a var must contain — it says a var may not
// enter the frozen contract until someone has written a guard for it and
// admitted it here. Failing is the correct behaviour for a var nobody has
// thought about yet.
func TestEveryExportedVarIsGuarded(t *testing.T) {
	// Every entry must name the guard that actually pins the var's contents;
	// this list is an index of coverage, not a mute allowlist.
	covered := map[string]string{
		"AllErrorCodes":     "TestAllErrorCodesEnumeratesEveryConstant pins it against the declared ErrorCode constants",
		"AllDeployStatuses": "TestAllDeployStatusesEnumeratesEveryConstant pins it against the declared DeployStatus constants, and TestAllDeployStatusesOrder pins its order",
		"AllPhases":         "TestAllPhasesEnumeratesEveryConstant pins it against the declared Phase constants, and TestAllPhasesOrder pins its order",
		"AllEventTypes":     "TestAllEventTypesEnumeratesEveryConstant pins it against the declared EventType constants, and TestAllEventTypesOrder pins its order",
	}

	declared := declaredExportedVars(t)

	for name := range declared {
		if _, ok := covered[name]; !ok {
			t.Errorf("exported var %s is unguarded — the struct, non-struct-type and "+
				"constant guards cannot see var decls. Write a guard pinning its contents, "+
				"then admit it to the covered list in this test. A var in a public package "+
				"is mutable contract: consumers can read it, range it, and rely on what is "+
				"in it.", name)
		}
	}
	for name := range covered {
		if !declared[name] {
			t.Errorf("the covered list names %s, which is no longer an exported var in "+
				"wire.go — removing an exported var is a breaking change within v1; if it "+
				"was deliberate, drop the entry in the same commit", name)
		}
	}
}

// parseSources parses every non-test .go file in this directory.
func parseSources(t *testing.T) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package directory: %v", err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("no non-test .go files found — the AST guards would silently pass")
	}
	return files
}

func declaredStructTypes(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, f := range parseSources(t) {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				if _, isStruct := ts.Type.(*ast.StructType); !isStruct {
					continue
				}
				out[ts.Name.Name] = true
			}
		}
	}
	return out
}

// valueSet turns a name->value partition into the set of its values, for
// the enumeration guards, which are about which VALUES appear in an
// All… slice. The pinned guards are about NAMES; both properties matter
// and they are checked separately.
func valueSet(m map[string]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for _, v := range m {
		out[v] = true
	}
	return out
}

// classifiedConstants is the result of partitioning every exported constant
// in the package into the vocabulary it belongs to. It is the single
// mechanism behind declaredErrorCodeValues, declaredDeployStatusValues,
// declaredPhaseValues and declaredLimitValues below: each of those calls
// classifyExportedConstants once and reads its own field back out, so the
// partitioning logic — and its "anything unclassifiable fails loudly" rule
// — lives in exactly one place rather than four copies that could drift
// apart from each other.
type classifiedConstants struct {
	// Keyed by CONSTANT NAME, not by value. Value-keyed maps let a
	// duplicate-NAME constant collapse into an already-pinned entry and
	// enter the frozen contract unguarded — a line review demonstrated
	// `StatusSneaky DeployStatus = "queued"` and
	// `CodeBoth ErrorCode = ErrorCode("bad_request")` both leaving the
	// suite green (2026-08-17), and a mutation in this repo confirmed it.
	// The exported IDENTIFIER is public API of a released module just as
	// much as the string value is: once a CLI ships importing it, removing
	// it is the breaking change this package forbids. The limits partition
	// was already name-keyed and would have caught its analogue.
	errorCodes     map[string]string
	deployStatuses map[string]string
	phases         map[string]string
	eventTypes     map[string]string
	limits         map[string]int64
}

// classifyExportedConstants collects every exported constant declared in
// the package and sorts it into ErrorCode, DeployStatus, Phase or the limit
// constants, by its declared type or its conversion callee.
//
// This package used to hold only ErrorCode constants, so
// declaredErrorCodeValues was deliberately type-BLIND: an earlier,
// type-filtered version matched only `Name ErrorCode = "value"` and was
// shown by mutation to miss `const CodeSneaky = ErrorCode("sneaky")` — the
// same constant written as a conversion. This package later added two more string enums
// and five integer constants, so blindness cannot survive unchanged — but
// the property the mutation actually proved is narrower than "never filter
// by type", and it is what this function preserves: an unclassifiable
// constant must fail LOUDLY, naming itself, and never be silently skipped
// or defaulted into whichever partition happens to be checked first. A
// partitioner is fine; a partitioner with a silent default is the original
// defect wearing new clothes.
//
// Concretely:
//   - A string literal typed ErrorCode/DeployStatus/Phase (either via the
//     ValueSpec's own Type, e.g. `X ErrorCode = "y"`, or via a matching
//     single-argument conversion, e.g. `X = ErrorCode("y")`) joins that
//     vocabulary.
//   - An untyped integer literal with no declared type and no conversion
//     (the shape every limit constant is declared in) joins the limits.
//   - Anything else — an untyped string literal belonging to no named
//     vocabulary, a conversion to an unrecognised type, a constant with no
//     explicit value, a non-literal expression — is unclassifiable and
//     fails the test by name. This is what an untyped `const Sneaky = "x"`
//     must do: it belongs to no vocabulary, and the correct behaviour is a
//     named failure, not a pass and not a silent assignment.
func classifyExportedConstants(t *testing.T) classifiedConstants {
	t.Helper()
	out := classifiedConstants{
		errorCodes:     map[string]string{},
		deployStatuses: map[string]string{},
		phases:         map[string]string{},
		eventTypes:     map[string]string{},
		limits:         map[string]int64{},
	}
	for _, f := range parseSources(t) {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range vs.Names {
					if !name.IsExported() {
						continue
					}
					if i >= len(vs.Values) {
						t.Errorf("exported constant %s has no explicit value; the contract "+
							"guard cannot read it, so it cannot be pinned", name.Name)
						continue
					}
					classifyOneConstant(t, name.Name, vs.Type, vs.Values[i], &out)
				}
			}
		}
	}
	return out
}

// classifyOneConstant sorts a single exported constant into one field of
// out, or fails the test naming the constant if it cannot. See
// classifyExportedConstants for the invariant this preserves.
func classifyOneConstant(t *testing.T, name string, declType ast.Expr, value ast.Expr, out *classifiedConstants) {
	t.Helper()

	// The declared type, read from the ValueSpec itself (e.g. the
	// "ErrorCode" in `X ErrorCode = "y"`), if the spec states one.
	typeName := ""
	if ident, ok := declType.(*ast.Ident); ok {
		typeName = ident.Name
	}

	// A single-argument type conversion — e.g. ErrorCode("y") — names its
	// target type as the callee, which overrides any type on the spec
	// itself.
	//
	// An earlier version of this comment claimed Go forbids a ValueSpec
	// carrying BOTH an explicit type and a conversion. That is false —
	// `const CodeBoth ErrorCode = ErrorCode("bad_request")` compiles, as a
	// line review demonstrated (2026-08-17). The override is still
	// unambiguous, but for a different and better reason: where both are
	// present the compiler requires them to AGREE, so for any code that
	// compiles, callee and spec type name the same type and it cannot
	// matter which one is read. Recorded because this is the file whose
	// whole doctrine is that a claim and its code must not drift.
	if call, ok := value.(*ast.CallExpr); ok {
		if len(call.Args) != 1 {
			t.Errorf("exported constant %s is a call with %d arguments; the contract guard "+
				"only reads single-argument type conversions", name, len(call.Args))
			return
		}
		callee, ok := call.Fun.(*ast.Ident)
		if !ok {
			t.Errorf("exported constant %s is not a recognisable literal or type "+
				"conversion; the contract guard cannot classify it", name)
			return
		}
		typeName = callee.Name
		value = call.Args[0]
	}

	lit, ok := value.(*ast.BasicLit)
	if !ok {
		t.Errorf("exported constant %s is not a plain literal or a single-argument type "+
			"conversion of one; the contract guard cannot classify it", name)
		return
	}

	switch typeName {
	case "ErrorCode":
		if s, ok := stringLitValue(t, name, lit); ok {
			out.errorCodes[name] = s
		}
	case "DeployStatus":
		if s, ok := stringLitValue(t, name, lit); ok {
			out.deployStatuses[name] = s
		}
	case "Phase":
		if s, ok := stringLitValue(t, name, lit); ok {
			out.phases[name] = s
		}
	case "EventType":
		if s, ok := stringLitValue(t, name, lit); ok {
			out.eventTypes[name] = s
		}
	case "":
		// No declared type and no conversion. The only classifiable shape
		// left is an untyped INTEGER literal — the shape every limit
		// constant is declared in (`MaxSourceFiles = 3_000`). An untyped
		// STRING literal here is exactly `const Sneaky = "x"`: it names no
		// vocabulary, however plausible its value looks, and must fail
		// rather than be guessed into a partition.
		if lit.Kind == token.INT {
			// Base 0 (rather than 10) so Go's underscore-grouped integer
			// literals — `3_000` — parse without stripping the separators
			// by hand first.
			n, err := strconv.ParseInt(lit.Value, 0, 64)
			if err != nil {
				t.Errorf("exported constant %s is not a parsable integer literal: %v", name, err)
				return
			}
			out.limits[name] = n
			return
		}
		t.Errorf("exported constant %s has no declared type and is not a plain limit "+
			"integer — it belongs to no known vocabulary (ErrorCode, DeployStatus, Phase, "+
			"EventType, or a limit constant) and must not sit in the contract unowned", name)
	default:
		t.Errorf("exported constant %s is declared as %s, which is none of ErrorCode, "+
			"DeployStatus, Phase or EventType — add a partition for it in "+
			"classifyOneConstant before it enters the frozen contract", name, typeName)
	}
}

// stringLitValue reads a *ast.BasicLit as an unquoted Go string, or fails
// the test naming the constant if the literal is not a string.
func stringLitValue(t *testing.T, name string, lit *ast.BasicLit) (string, bool) {
	t.Helper()
	if lit.Kind != token.STRING {
		t.Errorf("exported constant %s does not carry a string literal value; the "+
			"contract guard cannot read it", name)
		return "", false
	}
	unquoted, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Fatalf("unquoting %s for constant %s: %v", lit.Value, name, err)
	}
	return unquoted, true
}

// declaredErrorCodeValues returns the ErrorCode partition of
// classifyExportedConstants.
func declaredErrorCodeValues(t *testing.T) map[string]string {
	t.Helper()
	return classifyExportedConstants(t).errorCodes
}

// declaredDeployStatusValues returns the DeployStatus partition of
// classifyExportedConstants.
func declaredDeployStatusValues(t *testing.T) map[string]string {
	t.Helper()
	return classifyExportedConstants(t).deployStatuses
}

// declaredPhaseValues returns the Phase partition of
// classifyExportedConstants.
func declaredPhaseValues(t *testing.T) map[string]string {
	t.Helper()
	return classifyExportedConstants(t).phases
}

// declaredEventTypeValues returns the EventType partition of
// classifyExportedConstants.
func declaredEventTypeValues(t *testing.T) map[string]string {
	t.Helper()
	return classifyExportedConstants(t).eventTypes
}

// declaredLimitValues returns the limit-constant partition of
// classifyExportedConstants, keyed by constant name (there is no shared
// named type to key these by, unlike the three string enums — the name
// itself, e.g. "MaxSourceFiles", is what a sixth unpinned limit would be
// caught by).
func declaredLimitValues(t *testing.T) map[string]int64 {
	t.Helper()
	return classifyExportedConstants(t).limits
}

// declaredExportedVars collects the name of every exported package-level
// var in the package.
//
// f.Decls holds only top-level declarations, so function-local vars are out
// of scope by construction. Names are read per-spec rather than per-decl, so
// grouped `var ( … )` blocks and multi-name specs (`var A, B = 1, 2`) are
// each seen as the several vars they are.
func declaredExportedVars(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, f := range parseSources(t) {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range vs.Names {
					if !name.IsExported() {
						continue
					}
					out[name.Name] = true
				}
			}
		}
	}
	return out
}

// declaredNonStructTypes collects exported named types that are not structs.
// The struct guard cannot see them, and the constant guard only understands
// strings, so a future `type DeployState string` with its own constants
// would otherwise enter the public contract entirely unguarded.
func declaredNonStructTypes(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, f := range parseSources(t) {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ts.Name.IsExported() {
					continue
				}
				if _, isStruct := ts.Type.(*ast.StructType); isStruct {
					continue
				}
				out[ts.Name.Name] = true
			}
		}
	}
	return out
}
