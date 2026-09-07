package guard

import (
	"go/ast"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/check"
)

// TestCheckIDConstantsMatchTheDeclaredUniverse asserts that the check
// ids the result package EXPORTS and the universe it DECLARES are the
// same set, in both directions.
//
// WHY IT REPLACES A ROW THAT LOOKED LIKE IT DID THIS. The row it stands
// in for compared a hand-written map of constants against a hand-written
// map of their values — two transcriptions of one author's belief. It
// could see a value change, and nothing else: a constant added and left
// out of both maps was invisible, and neither map was ever compared
// against the declared order, which is the list the program actually
// uses. When a proposed check was retired this afternoon it caught
// nothing; the declared-order rows did. The row named for asserting a
// set was the one row that never looked at one.
//
// SO THE TWO SIDES COME FROM DIFFERENT MECHANISMS, which is the whole
// point. One is read out of the SOURCE by a type checker; the other is
// the compiled program's own answer at run time. A single edit cannot
// satisfy both by agreeing with itself.
//
// IT ASKS A TYPE QUESTION, NOT A SPELLING ONE, and that is guard 4's
// lesson applied rather than admired. A guard that matched names
// beginning with "ID" would answer "is this constant SPELLED like a
// check id" while the rule is "is this constant one". A check id added
// as `LocalhostCheck` would walk straight past it. The criterion here is
// instead: an exported constant whose type is a bare string — untyped,
// or `string` — because that is what a check id is in this package and
// what nothing else in it is. Severity, Status and DeclineKind
// constants all have defined types and are correctly invisible.
//
// The consequence, stated so it is a decision rather than a surprise: an
// exported string constant in that package which is NOT a check id will
// red this guard. The fix is to give it a defined type, which is what
// every other family of constants there already has.
//
// The residue, named rather than left to be found:
//
//   - A check id built at run time rather than declared as a constant
//     would be invisible. Nothing does that, and the ids are contract —
//     a machine surface keys on them — so a computed one would be a
//     different defect.
//   - Test files are not read. A test-only id is not part of what the
//     package exports to the program.
func TestCheckIDConstantsMatchTheDeclaredUniverse(t *testing.T) {
	exported := exportedStringConstants(t, filepath.Join(moduleRoot(t), "internal", "check"))

	if len(exported) == 0 {
		t.Fatal("no exported string constants were found at all — this guard scanned " +
			"nothing, and a guard that passes over an empty set is worse than none")
	}

	// One direction: every exported id is in the universe.
	declared := map[string]bool{}
	for _, id := range check.DeclaredOrder() {
		declared[id] = true
	}
	var undeclared []string
	byValue := map[string][]string{}
	for name, value := range exported {
		byValue[value] = append(byValue[value], name)
		if !declared[value] {
			undeclared = append(undeclared, name+" = "+value)
		}
	}
	sort.Strings(undeclared)
	if len(undeclared) > 0 {
		t.Errorf("exported but not in the declared universe: %s\n"+
			"a check id nothing declares is a question no producer is obliged to answer, "+
			"and the coverage rule cannot see it is missing", strings.Join(undeclared, ", "))
	}

	// The other direction: every declared id is an exported constant.
	values := map[string]bool{}
	for _, value := range exported {
		values[value] = true
	}
	var unexported []string
	for _, id := range check.DeclaredOrder() {
		if !values[id] {
			unexported = append(unexported, id)
		}
	}
	if len(unexported) > 0 {
		t.Errorf("declared but exported by no constant: %s\n"+
			"a bare string in the universe is one nobody can refer to without spelling "+
			"it again, which is how two spellings of one id arrive", strings.Join(unexported, ", "))
	}

	// Distinctness, which the set comparison alone cannot see: two
	// constants sharing a value both match a one-entry universe.
	for value, names := range byValue {
		if len(names) > 1 {
			sort.Strings(names)
			t.Errorf("%s share the id %q", strings.Join(names, " and "), value)
		}
	}

	// The naming convention, asserted as a convention and not as the
	// membership rule. It is what a reader greps for.
	for name := range exported {
		if !strings.HasPrefix(name, "ID") {
			t.Errorf("%s is a check id constant whose name does not begin with ID; the "+
				"guard does not depend on that, but every reader does", name)
		}
	}
}

// exportedStringConstants type-checks a package's non-test sources and
// returns its exported constants of bare string type, by name and value.
//
// The VALUE comes from the type checker's own constant folding rather
// than from the syntax of the declaration, so a concatenation or a
// reference to another constant is read as what it evaluates to. A guard
// that scraped string literals out of the AST would be a third
// transcription of the same belief, which is the defect it replaces.
func exportedStringConstants(t *testing.T, dir string) map[string]string {
	t.Helper()

	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, parseErr := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", name, parseErr)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatalf("no non-test sources in %s", dir)
	}

	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check(modulePath+"/internal/check", fset, files, nil)
	if err != nil {
		t.Fatalf("type-checking the result package: %v", err)
	}

	out := map[string]string{}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj, ok := scope.Lookup(name).(*types.Const)
		if !ok || !obj.Exported() {
			continue
		}
		basic, ok := types.Unalias(obj.Type()).(*types.Basic)
		if !ok || basic.Info()&types.IsString == 0 {
			continue
		}
		out[name] = constant.StringVal(obj.Val())
	}
	return out
}
