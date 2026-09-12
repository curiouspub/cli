package guard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestTheCheckerVisitsEveryConstruction.
//
// # The arithmetic that should have been a row
//
// The contract checker reported "56 sites" and a separate count of the
// tree's ui.Failure constructions came to 60. Nobody subtracted them.
// The four in the gap were composite literals the checker never visited
// — three of them the standing failures a person meets when the program
// cannot ask them anything — and every one carried no id while the
// checker reported a clean census.
//
// A checker's claim about its own COVERAGE is a claim about a
// repository, and a claim about a repository has a command that answers
// it. This is that command.
//
// # Why two counts written differently
//
// The census below is deliberately NOT the checker's enumeration
// refactored into a function: that would compare a number with itself.
// It walks for the TYPE — anything that brings a ui.Failure into
// existence — and it fails on any construction shape it does not
// recognise, so a form neither this row nor the checker knows about
// announces itself here rather than being silently absent from both.
//
// REQUIRED MUTATIONS, run 2026-09-12:
//  1. Remove the composite-literal branch from the checker. Reds here
//     with 56 against 60, which is the arithmetic that went unnoticed.
//  2. Add `x := new(ui.Failure)` to a non-test file. Reds here naming an
//     unrecognised construction shape, because neither side can resolve
//     its fields.
func TestTheCheckerVisitsEveryConstruction(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()

	var census []string    // every construction, by site
	var unknown []string   // shapes neither side can read
	var visitable []string // the subset the checker's rules admit

	var files []parsedFile
	for _, path := range publishedTextFiles(t, root) {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		f, perr := parser.ParseFile(fset, path, src, 0)
		if perr != nil {
			t.Fatalf("%s did not parse: %v", displayPath(root, path), perr)
		}
		files = append(files, parsedFile{path: path, file: f, src: src})
	}
	attachFailureTypeInfo(t, root, fset, files)

	helperKeys := map[string]bool{
		modulePath + "/internal/ui.NewFailure":     true,
		modulePath + "/internal/ui.Quoted":         true,
		modulePath + "/internal/flow.uploadFailed": true,
	}
	for _, p := range files {
		if p.info == nil {
			continue
		}
		rel := displayPath(root, p.path)
		at := func(n ast.Node) string {
			return rel + ":" + itoa(fset.Position(n.Pos()).Line)
		}

		ast.Inspect(p.file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CompositeLit:
				isFailure := isFailureExpr(p.info, node)
				embeddedZeros := embeddedZeroFailures(p.info, node)
				if !isFailure && embeddedZeros == 0 {
					return true
				}
				for range embeddedZeros + btoi(isFailure) {
					site := at(node)
					census = append(census, site)
					if !helperKeys[enclosingFuncKey(p, node.Pos())] {
						visitable = append(visitable, site)
					}
				}
			case *ast.CallExpr:
				key := calledFuncKey(node.Fun, enclosingDecl(p.file, node.Pos()), p.info)
				if helperKeys[key] {
					site := at(node)
					census = append(census, site)
					if !helperKeys[enclosingFuncKey(p, node.Pos())] {
						visitable = append(visitable, site)
					}
					return true
				}
				// AN UNRECOGNISED WAY TO BUILD ONE. new(Failure) gives a
				// zero value with no fields to read, so neither this row
				// nor the checker can say anything about its id or its
				// action — which makes it exactly the hole this row
				// exists to surface.
				if calleeName(node.Fun) == "new" && len(node.Args) == 1 &&
					isFailureExpr(p.info, node.Args[0]) {
					unknown = append(unknown, at(node)+" new(Failure)")
				}
			}
			return true
		})
	}

	sort.Strings(unknown)
	for _, u := range unknown {
		t.Errorf("UNRECOGNISED CONSTRUCTION: %s\nNeither this census nor the "+
			"contract checker can read the fields of a failure built this way, so "+
			"it would carry no obligations and pass by being invisible.", u)
	}
	if len(census) == 0 {
		t.Fatal("the census found no ui.Failure construction at all, so it would " +
			"agree with a checker that visited nothing")
	}

	// --- and now the comparison the arithmetic never got -------------
	visited := contractSiteCount(t)
	if visited != len(visitable) {
		t.Errorf("the contract checker visited %d sites and there are %d "+
			"constructions it should visit (%d found in total, less the ones "+
			"inside the helpers' own bodies).\nA checker that claims every "+
			"construction and reaches fewer reports a clean census over the part "+
			"it can see.", visited, len(visitable), len(census))
	}
	t.Logf("%d constructions, %d visitable, checker visited %d",
		len(census), len(visitable), visited)
}

func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}
