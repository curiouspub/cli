package guard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// serverTextExpressions are the ways this program gets hold of somebody
// else's sentence. They belong in a failure's QUOTATION, never in its
// prose: prose keeps its line breaks and a quotation does not, so a
// server sentence joined into a Why can add a paragraph in this
// program's voice.
//
// The set is small because there are only a few doors. It is asserted
// as a set below rather than trusted, since a new door added later is
// exactly the member a hand-written list would miss.
var serverTextExpressions = map[string]string{
	"Message":    "an error envelope's message, written by the server",
	"Error":      "an error's own text",
	"serverSaid": "the helper that prefers the server's message over the error's",
}

// TestNoServerSentenceIsJoinedIntoOurProse.
//
// THE INSTANCE, and it is why this is a guard rather than a convention.
// A sweep converted thirty-one call sites by searching for the two
// expressions it knew about, and a cold reviewer then found two it had
// not: one where the sentence was joined in through a HELPER CALL rather
// than a field access, and one where it was assigned to a local first.
// A hand-chosen sweep misses members; a machine reading the argument
// does not.
//
// ITS BLIND SPOT, written down rather than left to be found: this reads
// the Why ARGUMENT's own subtree, so a value laundered through a local
// or a parameter is invisible to it. That is exactly the second of the
// two sites above — `why := serverMessage` — and it is the reason the
// shapes below are also kept small enough to read.
//
// REQUIRED MUTATION, run 2026-09-09: put `serverSaid(err)` back into a
// Why. Reds naming the file, the line and the expression.
func TestNoServerSentenceIsJoinedIntoOurProse(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	checked, found := 0, map[string]bool{}

	for _, path := range goFiles(t, filepath.Join(root, "internal", "flow"), false) {
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isUICall(call, "NewFailure") || len(call.Args) < 2 {
				return true
			}
			checked++
			ast.Inspect(call.Args[1], func(inner ast.Node) bool {
				name := serverTextName(inner)
				if name == "" {
					return true
				}
				found[name] = true
				t.Errorf("%s:%d joins %s into a failure's prose (%s).\n"+
					"Prose keeps its line breaks and a quotation does not, so a "+
					"sentence from the far end can add a paragraph in this "+
					"program's voice. Pass it to Quoting instead.",
					filepath.ToSlash(rel), fset.Position(inner.Pos()).Line,
					name, serverTextExpressions[name])
				return false
			})
			return true
		})
	}

	// The control: a walk that found no failures to look at would report
	// a clean tree for the wrong reason.
	if checked < 10 {
		t.Fatalf("only %d failure constructions were examined, so this row is "+
			"looking in the wrong place", checked)
	}
}

// serverTextName reports which server-text expression a node is, if any.
func serverTextName(n ast.Node) string {
	switch e := n.(type) {
	case *ast.SelectorExpr:
		if _, named := serverTextExpressions[e.Sel.Name]; named {
			return e.Sel.Name
		}
	case *ast.Ident:
		if _, named := serverTextExpressions[e.Name]; named {
			return e.Name
		}
	}
	return ""
}

// isUICall reports whether a call is ui.<name>(…).
func isUICall(call *ast.CallExpr, name string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "ui"
}

// TestTheServerTextSetHasNotGrownQuietly reads the flow package for the
// doors this program has to somebody else's words, and requires each to
// be named above.
//
// Without it the row above is about whatever somebody listed, which is
// the defect it was written for arriving one level up.
func TestTheServerTextSetHasNotGrownQuietly(t *testing.T) {
	root := moduleRoot(t)
	for _, path := range goFiles(t, filepath.Join(root, "internal", "flow"), false) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		body := string(raw)
		for _, door := range []string{"apiErr.Message", ".Error()", "serverSaid("} {
			if !strings.Contains(body, door) {
				continue
			}
			key := strings.TrimSuffix(strings.TrimPrefix(
				strings.TrimPrefix(door, "apiErr."), "."), "()")
			key = strings.TrimSuffix(key, "(")
			if _, named := serverTextExpressions[key]; !named {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s uses %s, which is a way of getting the far end's "+
					"words and is not in serverTextExpressions — so the row above "+
					"is not looking for it", filepath.ToSlash(rel), door)
			}
		}
	}
}
