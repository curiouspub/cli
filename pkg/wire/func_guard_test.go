package wire

// A ninth gap: exported FUNCTIONS AND METHODS.
//
// TestEveryExportedVarIsGuarded's own comment says "a var is the one
// shape of exported surface the other AST guards are blind to". That was
// false. No guard enumerated exported FuncDecls, and a line review
// demonstrated two consequences (2026-08-17), both leaving the suite
// green:
//
//   - `func IsTerminal(s DeployStatus) bool` enters the frozen contract
//     with nothing pinning its behaviour — contrast CarriesRetryAfter,
//     whose answer is pinned per code.
//   - `func (DoneEvent) UnmarshalJSON(...)` using DisallowUnknownFields
//     silently revokes, for that one type, the package doc's load-bearing
//     promise that unknown fields are IGNORED ON BOTH SIDES.
//
// The second is the serious one, and it is why this guard covers methods
// and not only functions: a custom marshaller or unmarshaller is the one
// kind of method that can rewrite this package's meaning without touching
// a field or a tag, which is all the golden fixtures can see.
//
// Same discipline as the var guard: this test does not say what a
// function must DO. It says a function may not enter the frozen contract
// until someone has written a guard for it and admitted it here.

import (
	"encoding/json"
	"go/ast"
	"strings"
	"testing"
)

// TestEveryExportedFuncIsGuarded fails on any exported function or method
// not named below. Each entry must name the guard that actually pins its
// behaviour; this is an index of coverage, not a mute allowlist.
func TestEveryExportedFuncIsGuarded(t *testing.T) {
	covered := map[string]string{
		"CarriesRetryAfter": "TestCarriesRetryAfterIsPinned pins its answer for every code in AllErrorCodes",
	}

	declared := map[string]bool{}
	for _, f := range parseSources(t) {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			name := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				// A method: record it as Type.Method so a marshaller on one
				// type is distinguishable from the same name on another.
				recv := "?"
				switch rt := fn.Recv.List[0].Type.(type) {
				case *ast.Ident:
					recv = rt.Name
				case *ast.StarExpr:
					if id, ok := rt.X.(*ast.Ident); ok {
						recv = id.Name
					}
				}
				name = recv + "." + name
			}
			declared[name] = true
		}
	}

	for name := range declared {
		if _, ok := covered[name]; !ok {
			t.Errorf("exported func/method %s is unguarded — the struct, type, constant "+
				"and var guards cannot see func decls. A method in particular can rewrite "+
				"this package's meaning without touching a field or a tag: a custom "+
				"UnmarshalJSON revokes the unknown-field tolerance the package doc "+
				"promises, and no fixture would notice. Write a guard, then admit it "+
				"here.", name)
		}
	}
	for name := range covered {
		if !declared[name] {
			t.Errorf("the covered list names %s, which is no longer an exported func in "+
				"this package — removing exported API is a breaking change within v1; if "+
				"it was deliberate, drop the entry in the same commit", name)
		}
	}
}

// TestForwardCompatibleDecoding_EveryType extends the unknown-field
// tolerance proof from one type to ALL of them.
//
// The package doc calls that tolerance load-bearing in both directions
// and cites TestForwardCompatibleDecoding as pinning the client half.
// That test covers exactly one type, CapacityResponse — so the promise
// was pinned for 1 of 14, and a per-type custom unmarshaller could revoke
// it for any of the rest undetected. Here every golden fixture is decoded
// with an extra unknown key injected; each must succeed.
func TestForwardCompatibleDecoding_EveryType(t *testing.T) {
	for _, tc := range goldenCases() {
		t.Run(tc.name, func(t *testing.T) {
			raw := readFixture(t, tc.fixture)

			var asMap map[string]any
			if err := json.Unmarshal(raw, &asMap); err != nil {
				t.Fatalf("fixture is not a JSON object: %v", err)
			}
			asMap["a_field_from_a_newer_server"] = "ignored"
			withUnknown, err := json.Marshal(asMap)
			if err != nil {
				t.Fatalf("re-marshalling fixture: %v", err)
			}

			if err := json.Unmarshal(withUnknown, tc.newEmpty()); err != nil {
				t.Fatalf("decoding %s with an unknown field failed: %v\n"+
					"Unknown fields must be IGNORED on both sides — that is what lets the "+
					"server add fields without a coordinated client release, and lets a "+
					"server be rolled back without breaking clients. A custom "+
					"UnmarshalJSON with DisallowUnknownFields is the usual cause.",
					tc.name, err)
			}
		})
	}
}

// TestNoTypeDefinesCustomJSONMethods is the structural half of the guard
// above: rather than only proving tolerance holds today, it refuses the
// mechanism that would remove it. Every type here is a plain struct of
// plain fields, and encoding/json's default behaviour IS the contract.
func TestNoTypeDefinesCustomJSONMethods(t *testing.T) {
	banned := map[string]bool{
		"UnmarshalJSON": true,
		"MarshalJSON":   true,
		"UnmarshalText": true,
		"MarshalText":   true,
	}
	for _, f := range parseSources(t) {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !banned[fn.Name.Name] {
				continue
			}
			t.Errorf("%s is defined in this package — a custom JSON method changes what "+
				"the wire format means without changing any field or tag, so no golden "+
				"fixture can see it. If one is genuinely needed, it needs its own guard "+
				"and a deliberate decision recorded here first.",
				strings.TrimSpace(fn.Name.Name))
		}
	}
}
