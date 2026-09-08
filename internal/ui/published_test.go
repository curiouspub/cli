package ui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestPublishedFailureSetIsComplete is what makes publishedFailures a SET
// rather than a list somebody remembered to update.
//
// It reads this package's own source and asserts, BOTH DIRECTIONS, that
// the registry holds exactly the package-level Failure values declared
// here: nothing declared is missing from it, and nothing in it has been
// deleted from the source and left behind as a dangling entry.
//
// WHY A SOURCE READ RATHER THAN A BELIEF. The row this guards used to
// carry its own literal of three names under a comment claiming it
// checked the published examples "as a SET". The claim was true when
// written and false the day a fourth failure was added, because nobody
// edits a copy test in another file while writing copy — and nothing in
// the tree noticed for two days. A list checked against a second list
// typed by the same hand proves the hands agreed, not that either is
// right; the source is the only thing that cannot be forgotten to update,
// because it IS the change.
//
// This is the guard-4 precedent — an AST-backed set equivalence, mutated
// in both directions — applied inside one package rather than across two.
//
// WHAT IT DOES NOT SEE: a Failure built at run time and shown by a
// caller, since only a package-level declaration is a "standing" one; and
// a Failure declared in another package, which this package could not
// speak for anyway.
//
// REQUIRED MUTATION, both directions, run 2026-09-08:
//   - delete any line from publishedFailures — reds, naming the value
//     that is declared and unregistered;
//   - add an entry for a name no longer declared — reds the other way.
func TestPublishedFailureSetIsComplete(t *testing.T) {
	declared := declaredFailureVars(t)

	if len(declared) == 0 {
		t.Fatal("found no package-level Failure declarations at all — the source " +
			"scan below is looking for the wrong shape, and an empty expectation " +
			"agrees with an empty registry for the wrong reason")
	}

	registered := make(map[string]bool, len(publishedFailures))
	for name := range publishedFailures {
		registered[name] = true
	}

	var missing, dangling []string
	for _, name := range declared {
		if !registered[name] {
			missing = append(missing, name)
		}
	}
	for name := range registered {
		if !containsString(declared, name) {
			dangling = append(dangling, name)
		}
	}

	sort.Strings(missing)
	sort.Strings(dangling)

	if len(missing) > 0 {
		t.Errorf("declared but not in publishedFailures: %s\n"+
			"Every standing failure this package can show a person belongs in the "+
			"registry, or the rows that range it are checking a subset while "+
			"claiming to check the set.", strings.Join(missing, ", "))
	}
	if len(dangling) > 0 {
		t.Errorf("in publishedFailures but not declared here: %s\n"+
			"An entry naming a value the source no longer has is a registry "+
			"describing a package that no longer exists.", strings.Join(dangling, ", "))
	}
}

// declaredFailureVars returns the name of every package-level variable in
// this package whose value is a &Failure{...} composite literal.
func declaredFailureVars(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	fset := token.NewFileSet()
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != len(vs.Values) {
					continue
				}
				for i, value := range vs.Values {
					if isFailureLiteral(value) {
						names = append(names, vs.Names[i].Name)
					}
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// isFailureLiteral reports whether e is `&Failure{...}` — the shape every
// standing failure in this package is declared with.
func isFailureLiteral(e ast.Expr) bool {
	unary, ok := e.(*ast.UnaryExpr)
	if !ok || unary.Op != token.AND {
		return false
	}
	lit, ok := unary.X.(*ast.CompositeLit)
	if !ok {
		return false
	}
	ident, ok := lit.Type.(*ast.Ident)
	return ok && ident.Name == "Failure"
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
