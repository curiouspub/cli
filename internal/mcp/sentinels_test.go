package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
)

// sentinelSource is the file the terminal package declares its ways-a-
// run-can-end in. It is named once, here, so the walk and the failure
// message cannot disagree about which file was read.
const sentinelSource = "errors.go"

// exportedSentinels is every sentinel the terminal package exports,
// paired with the value this package renders.
//
// # The SET comes from the declaring package and the VALUES stay local
//
// A list of five names typed here would be a claim about whoever last
// remembered to edit it — and the defect this pairing exists to catch is
// precisely a member added over there and never considered here, which a
// local list cannot see by construction. So the names are read out of the
// source that declares them, and the map below supplies the values,
// checked in both directions: a sentinel declared and not mapped is the
// gap, and a name mapped and no longer declared is a rename nobody
// followed.
//
// Go's reflection cannot enumerate a package's exported variables from
// outside it, which is why this parses rather than asks.
func exportedSentinels(t *testing.T) map[string]error {
	t.Helper()

	known := map[string]error{
		"ErrNotInteractive": ui.ErrNotInteractive,
		"ErrAborted":        ui.ErrAborted,
		"ErrNoAnswer":       ui.ErrNoAnswer,
		"ErrInterrupted":    ui.ErrInterrupted,
		"ErrServerClosed":   ui.ErrServerClosed,
	}

	declared := declaredSentinels(t)
	if len(declared) == 0 {
		t.Fatalf("no exported sentinel was found in the terminal package's %s, so the "+
			"walk has stopped matching the tree rather than the tree having none",
			sentinelSource)
	}

	for _, name := range declared {
		if _, mapped := known[name]; !mapped {
			t.Errorf("the terminal package exports %s and this package has no value for "+
				"it, so nothing here has decided what a caller should be told when one "+
				"arrives. Add it to the map in %s and give it a case.", name, sentinelSource)
		}
	}
	for name := range known {
		if !contains(declared, name) {
			t.Errorf("this package maps %s and the terminal package no longer declares "+
				"it, so the mapping is about a name that is gone", name)
		}
	}
	return known
}

// declaredSentinels reads the exported error variables out of the
// terminal package's own source.
//
// IT PARSES RATHER THAN GREPS, which matters for the shape these take:
// they are declared in one grouped var block with a doc comment on every
// member and prose between them, so a line-oriented match would be
// deciding membership by how somebody formatted a comment.
//
// WHAT IT LOOKS FOR IS THE TYPE OF THE DECLARATION rather than the name:
// an exported variable initialised from errors.New. Matching a name
// prefix would admit anything somebody spelled that way and miss a
// sentinel spelled otherwise, which is the same defect as a guard that
// reads spellings.
func declaredSentinels(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "ui", sentinelSource)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	var names []string
	for _, decl := range file.Decls {
		gen, isGen := decl.(*ast.GenDecl)
		if !isGen || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, isValue := spec.(*ast.ValueSpec)
			if !isValue {
				continue
			}
			for i, name := range value.Names {
				if !name.IsExported() || i >= len(value.Values) {
					continue
				}
				if isErrorsNew(value.Values[i]) {
					names = append(names, name.Name)
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

// isErrorsNew reports whether an expression is errors.New(...).
func isErrorsNew(expr ast.Expr) bool {
	call, isCall := expr.(*ast.CallExpr)
	if !isCall {
		return false
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel || sel.Sel.Name != "New" {
		return false
	}
	pkg, isIdent := sel.X.(*ast.Ident)
	return isIdent && pkg.Name == "errors"
}

func contains(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

// TestTheSentinelWalkSeesWhatItClaimsTo is the instrument's own bench.
//
// The row that uses it runs over a tree where every sentinel is expected
// to be mapped, so on its own it can only show the walk saying yes. A
// walk that returned nothing would make that row report a clean surface
// for the wrong reason — which the emptiness check catches — and a walk
// that returned everything in the file would make it red on constants
// that are not sentinels at all.
func TestTheSentinelWalkSeesWhatItClaimsTo(t *testing.T) {
	declared := declaredSentinels(t)

	// The one name whose absence would be invisible: this file's own map
	// is keyed by hand, so if the walk stopped seeing the member most
	// recently added over there, nothing else would notice.
	if !contains(declared, "ErrServerClosed") {
		t.Errorf("the walk found %v and not ErrServerClosed, which the terminal package "+
			"declares in the same block as the rest", declared)
	}

	// And it must not sweep up everything: that file also declares an
	// exported constant and an exported function, and a walk that
	// returned those would make the row above red for reasons that have
	// nothing to do with a sentinel.
	for _, notASentinel := range []string{"ExitServerClosed", "ServerClosed"} {
		if contains(declared, notASentinel) {
			t.Errorf("the walk returned %s, which is not an error variable — it is "+
				"matching more than errors.New declarations, so %s cannot say what it "+
				"claims", notASentinel, strings.TrimSuffix(sentinelSource, ".go"))
		}
	}
}
