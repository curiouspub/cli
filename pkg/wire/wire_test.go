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

package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fixedResetsAt is the one fixed timestamp used across the golden fixtures
// below. It is always constructed with time.UTC so that RFC 3339 rendering
// is deterministic and ends in "Z" rather than a numeric offset. Tests must
// never use time.Now() — a moving target makes a golden fixture pointless.
var fixedResetsAt = time.Date(2026, 3, 1, 12, 30, 0, 0, time.UTC)

// readFixture loads a testdata fixture and returns its raw bytes.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

// assertSemanticJSONEqual compares two JSON byte slices structurally
// (field names, nesting, values) rather than byte-for-byte, so differences
// in indentation or key order don't fail the test — but a renamed,
// removed, or retyped field will.
func assertSemanticJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotVal, wantVal any
	if err := json.Unmarshal(got, &gotVal); err != nil {
		t.Fatalf("unmarshaling got JSON: %v\n%s", err, got)
	}
	if err := json.Unmarshal(want, &wantVal); err != nil {
		t.Fatalf("unmarshaling want JSON: %v\n%s", err, want)
	}
	if !reflect.DeepEqual(gotVal, wantVal) {
		t.Fatalf("JSON mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// goldenCase bundles everything needed to golden-test a single wire type:
// a fully-populated value, its checked-in fixture, and a fresh zero value
// to unmarshal into for the round-trip assertion.
type goldenCase struct {
	name     string
	fixture  string
	value    any        // fully-populated value, e.g. &CapacityResponse{...}
	newEmpty func() any // returns a fresh pointer to unmarshal into
}

func goldenCases() []goldenCase {
	return []goldenCase{
		{
			name:    "ErrorResponse",
			fixture: "error_response.json",
			value: &ErrorResponse{
				Error: Error{
					Code:    CodeNotFound,
					Message: "site not found",
				},
			},
			newEmpty: func() any { return &ErrorResponse{} },
		},
		{
			name:    "CapacityResponse",
			fixture: "capacity_response.json",
			value: &CapacityResponse{
				Open:         true,
				AccountsLeft: 42,
				ResetsAt:     fixedResetsAt,
			},
			newEmpty: func() any { return &CapacityResponse{} },
		},
		{
			// The closed state is the whole point of the endpoint — the
			// step-zero gate — and the only fixture where the meaningful
			// values are zero values.
			name:    "CapacityResponseClosed",
			fixture: "capacity_response_closed.json",
			value: &CapacityResponse{
				Open:         false,
				AccountsLeft: 0,
				ResetsAt:     fixedResetsAt,
			},
			newEmpty: func() any { return &CapacityResponse{} },
		},
		{
			name:    "WaitlistRequest",
			fixture: "waitlist_request.json",
			value: &WaitlistRequest{
				Email: "user@example.com",
			},
			newEmpty: func() any { return &WaitlistRequest{} },
		},
		{
			// Deliberately empty: success is the HTTP status. The fixture
			// pins the empty object so that adding a field later shows up
			// as a visible contract change.
			name:     "WaitlistResponse",
			fixture:  "waitlist_response.json",
			value:    &WaitlistResponse{},
			newEmpty: func() any { return &WaitlistResponse{} },
		},
		{
			name:    "AuthStartRequest",
			fixture: "auth_start_request.json",
			value: &AuthStartRequest{
				Email: "user@example.com",
			},
			newEmpty: func() any { return &AuthStartRequest{} },
		},
		{
			// Deliberately empty, and deliberately identical for every
			// outcome — the endpoint must not reveal whether an address is
			// registered, rate-limited, or in cooldown.
			name:     "AuthStartResponse",
			fixture:  "auth_start_response.json",
			value:    &AuthStartResponse{},
			newEmpty: func() any { return &AuthStartResponse{} },
		},
		{
			name:    "AuthVerifyRequest",
			fixture: "auth_verify_request.json",
			value: &AuthVerifyRequest{
				Email:          "user@example.com",
				Code:           "123456",
				MarketingOptIn: true,
			},
			newEmpty: func() any { return &AuthVerifyRequest{} },
		},
		{
			name:    "AuthVerifyResponse",
			fixture: "auth_verify_response.json",
			value: &AuthVerifyResponse{
				Token: "example-opaque-token-value",
			},
			newEmpty: func() any { return &AuthVerifyResponse{} },
		},
	}
}

// TestGolden_MarshalMatchesFixture marshals a fully-populated value of each
// wire type and compares it against the checked-in fixture. This pins the
// exact field names and structure of the /v1 contract: a rename, removal,
// or retype shows up here as a test failure before it ever reaches a
// released client.
func TestGolden_MarshalMatchesFixture(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			want := readFixture(t, tc.fixture)
			assertSemanticJSONEqual(t, got, want)
		})
	}
}

// TestGolden_UnmarshalRoundTrips unmarshals each fixture and asserts it
// produces the same value that was used to generate it, proving the
// fixture round-trips through the Go type without loss.
func TestGolden_UnmarshalRoundTrips(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			raw := readFixture(t, tc.fixture)
			got := tc.newEmpty()
			if err := json.Unmarshal(raw, got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, tc.value) {
				t.Fatalf("round-trip mismatch:\n got: %#v\nwant: %#v", got, tc.value)
			}
		})
	}
}

// TestForwardCompatibleDecoding proves that a fixture containing fields the
// current client doesn't know about (i.e. a future additive server change)
// still decodes successfully into the known fields. This is what lets the
// server add fields without breaking an already-released CLI.
func TestForwardCompatibleDecoding(t *testing.T) {
	raw := readFixture(t, "capacity_response_extra_fields.json")

	var got CapacityResponse
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal with unknown extra fields must succeed, got error: %v", err)
	}

	want := CapacityResponse{
		Open:         true,
		AccountsLeft: 42,
		ResetsAt:     fixedResetsAt,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("known fields decoded incorrectly:\n got: %#v\nwant: %#v", got, want)
	}
}

// TestCapacityResponse_ResetsAtIsUTCRFC3339 asserts that ResetsAt marshals
// as RFC 3339 rendered in UTC (ending in "Z"), not a local/numeric offset.
// A fixed timestamp is used deliberately — never time.Now() — so the
// assertion is reproducible.
func TestCapacityResponse_ResetsAtIsUTCRFC3339(t *testing.T) {
	cr := CapacityResponse{
		Open:         true,
		AccountsLeft: 7,
		ResetsAt:     fixedResetsAt,
	}

	b, err := json.Marshal(cr)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal into map: %v", err)
	}

	resetsAt, ok := decoded["resets_at"].(string)
	if !ok {
		t.Fatalf("resets_at missing or not a string in %s", b)
	}

	if !strings.HasSuffix(resetsAt, "Z") {
		t.Fatalf("resets_at %q must render in UTC (suffix Z), not a local offset", resetsAt)
	}

	parsed, err := time.Parse(time.RFC3339, resetsAt)
	if err != nil {
		t.Fatalf("resets_at %q is not valid RFC 3339: %v", resetsAt, err)
	}
	if !parsed.Equal(fixedResetsAt) {
		t.Fatalf("resets_at %q does not represent the same instant as %v", resetsAt, fixedResetsAt)
	}
}

// TestErrorCodeConstants pins the exact string value of every ErrorCode
// constant. The codes are contract — a consumer switches on them — so a
// typo or accidental rename here must fail loudly rather than silently
// shipping a different wire value.
func TestErrorCodeConstants(t *testing.T) {
	cases := []struct {
		code ErrorCode
		want string
	}{
		{CodeBadRequest, "bad_request"},
		{CodeUnauthorized, "unauthorized"},
		{CodeForbidden, "forbidden"},
		{CodeNotFound, "not_found"},
		{CodeRateLimited, "rate_limited"},
		{CodeCapacityClosed, "capacity_closed"},
		{CodeMaintenance, "maintenance"},
		{CodeInternal, "internal"},
	}

	if len(cases) != 8 {
		t.Fatalf("expected 8 error code constants to be tested, got %d — update this test if the set changed", len(cases))
	}

	for _, tc := range cases {
		if string(tc.code) != tc.want {
			t.Errorf("ErrorCode constant %v = %q, want %q", tc.code, string(tc.code), tc.want)
		}
	}
}

// TestErrorResponse_JSONShape is a focused check (independent of the
// fixture-driven table above) that ErrorResponse wraps Error under an
// "error" key with "code" and "message" beneath it, since this envelope is
// the shape every non-2xx /v1 response uses.
func TestErrorResponse_JSONShape(t *testing.T) {
	er := ErrorResponse{Error: Error{Code: CodeMaintenance, Message: "we'll be back shortly"}}

	b, err := json.Marshal(er)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	errObj, ok := decoded["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level \"error\" object, got %s", b)
	}
	if errObj["code"] != "maintenance" {
		t.Fatalf("expected error.code = maintenance, got %v", errObj["code"])
	}
	if errObj["message"] != "we'll be back shortly" {
		t.Fatalf("expected error.message to round-trip, got %v", errObj["message"])
	}
}
