package timing_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/timing"
)

// TestEveryConnectionBoundCarriesItsReason holds the connection bounds to
// what the registry promises about every number in it: the number, the
// interval it ends, and why it is that number. A provisional bound must
// say it is provisional, so nobody reads a starting point as a
// measurement.
func TestEveryConnectionBoundCarriesItsReason(t *testing.T) {
	if len(timing.Bounds) == 0 {
		t.Fatal("the registry holds no connection bounds, so this row observed nothing")
	}
	for key, b := range timing.Bounds {
		if b.Name != key {
			t.Errorf("the bound registered as %q is named %q", key, b.Name)
		}
		if b.Value <= 0 {
			t.Errorf("%s is %v; a bound of zero or less is no bound at all, and the "+
				"transport would read it as none", key, b.Value)
		}
		if strings.TrimSpace(b.Governs) == "" {
			t.Errorf("%s does not say which interval it ends", key)
		}
		if strings.TrimSpace(b.Reason) == "" {
			t.Errorf("%s does not say why it is %v", key, b.Value)
		}
		if b.Provisional && !strings.Contains(b.Reason, "provisional") {
			t.Errorf("%s is provisional and its reason does not say so:\n%s", key, b.Reason)
		}
		if !b.Provisional {
			t.Errorf("%s is marked measured, and no bound has a measurement field to carry "+
				"one yet: re-ruling a bound from measurement is a change to this type, "+
				"not to a flag", key)
		}
	}
}

// TestEveryBoundReadsTheClientsConstant holds the one-home rule for the
// connection bounds. The numbers ship as the client's own constants, and
// this registry records them with their reasons; if an entry wrote its
// number out instead of reading the constant, the record and the client
// could disagree and nothing would notice. So the source of this file is
// parsed and every Bound's Value must be a selector naming the client's
// constant, never a literal.
func TestEveryBoundReadsTheClientsConstant(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, "internal", "timing", "bounds.go"), nil, 0)
	if err != nil {
		t.Fatalf("parsing bounds.go: %v", err)
	}
	seen := 0
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if id, ok := lit.Type.(*ast.Ident); !ok || id.Name != "Bound" {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); !ok || key.Name != "Value" {
				continue
			}
			seen++
			sel, ok := kv.Value.(*ast.SelectorExpr)
			pkg, pkgOK := func() (*ast.Ident, bool) {
				if !ok {
					return nil, false
				}
				id, ok := sel.X.(*ast.Ident)
				return id, ok
			}()
			if !ok || !pkgOK || pkg.Name != "api" || !strings.HasPrefix(sel.Sel.Name, "Default") {
				t.Errorf("bounds.go:%d sets a Bound's Value to something other than the client's "+
					"Default constant. The number ships in the client; this record reads it, "+
					"and a restated number is a second home that can drift.",
					fset.Position(kv.Pos()).Line)
			}
		}
		return true
	})
	if seen != len(timing.Bounds) {
		t.Fatalf("bounds.go sets %d Bound Values and the registry holds %d bounds; every one "+
			"must be read, and this row must have seen each", seen, len(timing.Bounds))
	}
}
