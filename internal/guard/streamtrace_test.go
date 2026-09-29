package guard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// streamTraceField is the deploy dependency that hands the build-log
// reader's own timeline to whoever set it.
const streamTraceField = "StreamTrace"

// TestTheStreamTraceIsNeverSetOutsideATest holds the build-log reader's
// trace hook to what it is for: a stall row that needs the client's own
// timeline to say whether a cut connection was the machine or the client.
//
// NOTHING THAT SHIPS MAY SET IT. The hook is told about every read the
// reader makes and the moment its watchdog fires; wired in production it
// would be an observation channel inside the binary that nobody asked
// for, and in a tool whose whole argument is that it reports nothing to
// anyone, that is the wrong kind of seam to leave open. So every Go file
// this module ships is parsed, and the field may not be set in any of
// them — neither as a key in a composite literal nor by assignment.
//
// IT IS A QUESTION ABOUT THE SYNTAX TREE, NOT THE SPELLING. A grep for
// the name would also match the field's declaration and the one place the
// package passes it along, and would miss a set spelled across lines.
// What is refused is a WRITE: a key naming it, or an assignment whose
// left side selects it.
func TestTheStreamTraceIsNeverSetOutsideATest(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	parsed, declared := 0, false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "dist" || name == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		parsed++
		rel := filepath.ToSlash(mustRel(t, root, path))
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.Field:
				for _, name := range x.Names {
					if name.Name == streamTraceField {
						declared = true
					}
				}
			case *ast.KeyValueExpr:
				if key, ok := x.Key.(*ast.Ident); ok && key.Name == streamTraceField {
					t.Errorf("%s:%d sets %s in a composite literal. It is a row's instrument "+
						"and nothing that ships may set it.", rel, fset.Position(x.Pos()).Line, streamTraceField)
				}
			case *ast.AssignStmt:
				for _, lhs := range x.Lhs {
					if sel, ok := lhs.(*ast.SelectorExpr); ok && sel.Sel.Name == streamTraceField {
						t.Errorf("%s:%d assigns %s. It is a row's instrument and nothing that "+
							"ships may set it.", rel, fset.Position(x.Pos()).Line, streamTraceField)
					}
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
	if parsed == 0 {
		t.Fatal("no Go file was parsed, so this row observed nothing")
	}
	if !declared {
		t.Fatalf("no non-test file declares a field named %s, so this row is guarding a "+
			"name that no longer exists — rename it here with the field", streamTraceField)
	}
}
