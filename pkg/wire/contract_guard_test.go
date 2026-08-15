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

// This file closes five gaps that the fixture-driven golden tests in
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
//
// Gaps 2, 3 and 4 are enforced by parsing wire.go's AST, which is the only
// way to enumerate a Go package's declared types and constants at test
// time.

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
	// strings. ErrorCode is the only one, and a new one must not slip in
	// unguarded.
	nonStruct := declaredNonStructTypes(t)
	for name := range nonStruct {
		if name != "ErrorCode" {
			t.Errorf("exported non-struct type %s is unguarded — the struct and constant "+
				"guards cannot see it. Write a guard for it before it enters the frozen "+
				"contract.", name)
		}
	}
	if !nonStruct["ErrorCode"] {
		t.Error("expected ErrorCode to be declared as an exported non-struct type")
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
	pinned := map[string]bool{
		"bad_request":     true,
		"unauthorized":    true,
		"forbidden":       true,
		"not_found":       true,
		"rate_limited":    true,
		"capacity_closed": true,
		"maintenance":     true,
		"internal":        true,
	}

	declared := declaredErrorCodeValues(t)

	for value := range declared {
		if !pinned[value] {
			t.Errorf("ErrorCode %q is declared in wire.go but not pinned by a test — "+
				"error codes are public contract and a consumer switches on them", value)
		}
	}
	for value := range pinned {
		if !declared[value] {
			t.Errorf("ErrorCode %q is pinned by tests but no longer declared in wire.go — "+
				"removing or renaming a code is a breaking change within v1", value)
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

	for value := range declared {
		if !listed[value] {
			t.Errorf("ErrorCode %q is declared in wire.go but missing from AllErrorCodes — "+
				"add it in the same commit as the constant, or every consumer that ranges "+
				"the set is blind to it", value)
		}
	}
	for value := range listed {
		if !declared[value] {
			t.Errorf("AllErrorCodes contains %q, which is not declared as an exported "+
				"constant in wire.go — the enumeration may only name codes the contract "+
				"actually defines", value)
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

// declaredErrorCodeValues collects the value of every exported constant in
// the package.
//
// It deliberately does NOT filter on the declared type. An earlier version
// matched only `Name ErrorCode = "value"` and was shown by mutation to miss
// `const CodeSneaky = ErrorCode("sneaky")` — the same constant written as a
// conversion — and would equally miss an untyped `CodeB = "b"`. Since this
// package should only ever declare ErrorCode constants, the robust rule is
// simpler: every exported constant must be a pinned string, whatever form
// it is written in. Anything else fails loudly rather than slipping through.
func declaredErrorCodeValues(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
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
					value, ok := stringConstValue(vs.Values[i])
					if !ok {
						t.Errorf("exported constant %s is not a plain string literal; the "+
							"contract guard cannot read it. Keep this package to string "+
							"constants, or extend the guard deliberately.", name.Name)
						continue
					}
					unquoted, err := strconv.Unquote(value)
					if err != nil {
						t.Fatalf("unquoting %s for constant %s: %v", value, name.Name, err)
					}
					out[unquoted] = true
				}
			}
		}
	}
	return out
}

// stringConstValue reads a string literal written either directly ("x") or
// wrapped in a single-argument conversion such as ErrorCode("x").
func stringConstValue(expr ast.Expr) (string, bool) {
	switch v := expr.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		return v.Value, true
	case *ast.CallExpr:
		if len(v.Args) != 1 {
			return "", false
		}
		return stringConstValue(v.Args[0])
	}
	return "", false
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
