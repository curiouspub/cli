package guard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestTheGateRunsEveryPreflightRow, and more generally: the gate's test
// selectors NAME what they exclude and never describe a name's shape.
//
// # The instance
//
// A long round ran its suite with `-run '^Test[^P]|^TestP[^r]'` to skip
// three slow stall probes. That pattern also excludes every test whose
// name begins TestPr — which was the whole of TestPreflight*, a
// package's rendering rows. Two of them were red across three commits
// and only `make ci` said so, because no local run could: the filter
// removed them before the runner saw them.
//
// The gate itself never had that problem — it runs whole packages — and
// that is exactly why this row exists. The defect was in a command
// somebody typed, and the only durable defence is that the gate's own
// selectors cannot acquire the same shape without a test saying so.
//
// # Why a negative character class is the thing forbidden
//
// `-skip '^TestProbe'` names three tests and will name exactly those
// three forever. `-run '^Test[^P]…'` names a SHAPE, and every test
// written afterwards falls inside or outside it by how it was spelled.
// The second is an exclusion that grows on its own, which is the sibling
// of an exclusion written for one line eventually matching another.
//
// # The count comes from the package, not from a grep
//
// A grep for TestPreflight is how this was missed in the first place, so
// the row enumerates the package's test functions from its own source
// and fails if it finds none — a scan that matches nothing reports a
// clean result identical to a scan that found everything admitted.
//
// REQUIRED MUTATION, run 2026-09-12: add `-run '^Test[^P]'` to a test
// target. Reds here, naming the target and the rows it would drop.
func TestTheGateRunsEveryPreflightRow(t *testing.T) {
	root := moduleRoot(t)

	// --- the package's own list, by AST ------------------------------
	pkgDir := filepath.Join(root, "internal", "flow")
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("reading %s: %v", pkgDir, err)
	}
	fset := token.NewFileSet()
	var preflightRows []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, filepath.Join(pkgDir, e.Name()), nil, 0)
		if perr != nil {
			t.Fatalf("%s did not parse: %v", e.Name(), perr)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil {
				continue
			}
			if strings.HasPrefix(fd.Name.Name, "TestPreflight") {
				preflightRows = append(preflightRows, fd.Name.Name)
			}
		}
	}
	if len(preflightRows) == 0 {
		t.Fatal("no TestPreflight row was found in internal/flow, so this guard " +
			"is comparing the gate's selectors against an empty list and would " +
			"report clean whatever they said")
	}
	t.Logf("%d TestPreflight rows in internal/flow", len(preflightRows))

	// --- the gate's selectors ----------------------------------------
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("reading the Makefile: %v", err)
	}
	runFlag := regexp.MustCompile(`-run[= ]'([^']*)'|-run[= ]"([^"]*)"`)
	skipFlag := regexp.MustCompile(`-skip[= ]'([^']*)'|-skip[= ]"([^"]*)"`)

	for _, line := range strings.Split(string(makefile), "\n") {
		if !strings.Contains(line, "go test") && !strings.Contains(line, "skipcheck") {
			continue
		}
		if m := runFlag.FindStringSubmatch(line); m != nil {
			pattern := m[1] + m[2]
			// A -run SELECTOR ON THE GATE is how a test stops being run
			// without anybody deciding it should. If one is ever needed,
			// every row below has to be checked against it here.
			var dropped []string
			re, cerr := regexp.Compile(pattern)
			if cerr != nil {
				t.Errorf("the gate carries -run %q, which does not compile: %v",
					pattern, cerr)
				continue
			}
			for _, name := range preflightRows {
				if !re.MatchString(name) {
					dropped = append(dropped, name)
				}
			}
			if len(dropped) > 0 {
				t.Errorf("the gate's -run %q excludes %d of %d TestPreflight rows, "+
					"starting with %s.\nA selector that drops a row drops it "+
					"silently: the run is green and the row did not execute.",
					pattern, len(dropped), len(preflightRows), dropped[0])
			}
		}
		if m := skipFlag.FindStringSubmatch(line); m != nil {
			pattern := m[1] + m[2]
			// NAMED, NOT SHAPE-DESCRIBED. A skip is allowed — the stall
			// probes are genuinely slow — but it names them.
			if strings.ContainsAny(pattern, "[]") {
				t.Errorf("the gate carries -skip %q, which describes the SHAPE of a "+
					"name rather than naming what it skips.\nA shape grows on its "+
					"own: every test written afterwards falls inside or outside it "+
					"by how it happens to be spelled, and the ones that fall in are "+
					"never run again.", pattern)
			}
		}
	}
}
