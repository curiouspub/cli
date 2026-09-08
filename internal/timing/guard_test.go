package timing_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/timing"
)

// The guard: every stall window a test uses is one of the registry's.
// ---------------------------------------------------------------------
//
// A rule with no mechanism holds until its author is busy. Four of this
// repository's five stall windows were numbers somebody chose, and one
// of those went in the day after the entry saying not to do that was
// written, by the person who wrote it. So the rule is a test rather than
// a paragraph: a stall window that is not in the registry cannot reach
// the tree.
//
// WHAT IT SCANS, and what it therefore cannot see:
//
//   - _test.go files only. A stall timeout set in SHIPPED code is the
//     product's own default — it is behaviour rather than a test margin,
//     it has no fixture and no leg to be measured on, and reding on it
//     would be this guard demanding evidence for a thing it does not
//     describe.
//   - Assignments and composite-literal fields whose field name is one
//     of stallFields. A window reaching the same field through a helper
//     function's parameter is outside the resolver's view; it is said
//     here rather than left to be found, because a guard with an
//     undocumented blind spot reads as total coverage and ends the
//     search.
//   - Directories named .git, vendor and .claude are skipped. The last
//     is not housekeeping: a worktree lane is a second checkout of this
//     repository INSIDE it, and walking one counts another branch's
//     files as though they were this one's.
//   - testdata is skipped. Nothing under it is compiled, so nothing
//     under it can set a stall timeout on a running client.
//
// A NOTE ON RUNNING IT. This row reads state the toolchain does not
// track as an input to this package — the whole module's source tree —
// so a cached PASS stays valid while the tree underneath it starts
// violating the rule. The Makefile runs the suite with -count=1 for
// exactly that reason, and anyone invoking this by hand should too.

// stallFields are the struct fields whose value IS a stall window. They
// are written out rather than matched by a suffix, because a suffix rule
// silently adopts the next field somebody names that way and silently
// misses the next one they do not.
var stallFields = map[string]bool{
	"StallTimeout":       true,
	"UploadStallTimeout": true,
	"StreamStallTimeout": true,
}

// timingImportPath is the package a stall window must come from.
const timingImportPath = "github.com/curiouspub/cli/internal/timing"

// stallSite is one place a test chooses a stall window.
type stallSite struct {
	file  string
	line  int
	field string
	// entry is the registry entry the value resolved to, empty when it
	// resolved to nothing.
	entry string
	// text is the expression as written, for a message a reader can act
	// on without opening the file.
	text string
}

// moduleRoot walks up from this package's own directory until it finds
// the module's go.mod.
//
// It is a duplicate of a helper the guard package next door keeps, and
// deliberately so: that one is an unexported helper of a _test package,
// and exporting it to share fifteen lines would make the guard package's
// test surface into API. The duplicated thing is a walk, not a fact —
// two homes for one FACT diverge silently; two homes for one loop do
// not.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("walked up to the filesystem root without finding a go.mod")
		}
		dir = parent
	}
}

// testFiles is every _test.go file in the module.
func testFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", ".claude", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	sort.Strings(files)
	return files
}

// timingAlias is the name this file refers to the registry package by,
// or "" when it does not import it at all.
func timingAlias(file *ast.File) string {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != timingImportPath {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return "timing"
	}
	return ""
}

// entryOf reads `<alias>.<Name>.Window` and returns Name.
//
// The whole selector chain has to match. `<alias>.<Name>` on its own is
// an entry and not a window; a field other than Window is some other
// value of the entry's, and neither is a stall window.
func entryOf(alias string, expr ast.Expr) (string, bool) {
	if alias == "" {
		return "", false
	}
	outer, ok := expr.(*ast.SelectorExpr)
	if !ok || outer.Sel.Name != "Window" {
		return "", false
	}
	inner, ok := outer.X.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := inner.X.(*ast.Ident)
	if !ok || pkg.Name != alias {
		return "", false
	}
	return inner.Sel.Name, true
}

// resolve turns the expression a stall field was given into the registry
// entry behind it.
//
// It accepts the selector written out, and ONE level of local name: an
// identifier the enclosing function assigns exactly once, from that
// selector. One level is what keeps a row readable —
//
//	stall := timing.SomeEntry.Window
//	deps.StallTimeout = stall
//
// — and "exactly once" is what stops the indirection becoming a hole: an
// identifier that starts at a registry window and is then reassigned to a
// number of somebody's own resolves to nothing here, and reds.
func resolve(alias string, locals map[string][]ast.Expr, expr ast.Expr) (string, bool) {
	if name, ok := entryOf(alias, expr); ok {
		return name, true
	}
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return "", false
	}
	assigned := locals[ident.Name]
	if len(assigned) != 1 {
		return "", false
	}
	return entryOf(alias, assigned[0])
}

// scanFunc collects, for one function body, every identifier assignment
// and every place a stall field is given a value.
func scanFunc(fset *token.FileSet, root, path, alias string, body *ast.BlockStmt) []stallSite {
	locals := map[string][]ast.Expr{}
	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range s.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && i < len(s.Rhs) {
					locals[id.Name] = append(locals[id.Name], s.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, name := range s.Names {
				if i < len(s.Values) {
					locals[name.Name] = append(locals[name.Name], s.Values[i])
				}
			}
		}
		return true
	})

	var sites []stallSite
	record := func(field string, value ast.Expr) {
		name, _ := resolve(alias, locals, value)
		pos := fset.Position(value.Pos())
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		sites = append(sites, stallSite{
			file:  filepath.ToSlash(rel),
			line:  pos.Line,
			field: field,
			entry: name,
			text:  exprText(fset, value),
		})
	}

	ast.Inspect(body, func(n ast.Node) bool {
		switch s := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range s.Lhs {
				sel, ok := lhs.(*ast.SelectorExpr)
				if ok && stallFields[sel.Sel.Name] && i < len(s.Rhs) {
					record(sel.Sel.Name, s.Rhs[i])
				}
			}
		case *ast.KeyValueExpr:
			if key, ok := s.Key.(*ast.Ident); ok && stallFields[key.Name] {
				record(key.Name, s.Value)
			}
		}
		return true
	})
	return sites
}

// exprText renders an expression the way it was written, so a refusal
// can quote it.
func exprText(fset *token.FileSet, expr ast.Expr) string {
	start := fset.Position(expr.Pos())
	end := fset.Position(expr.End())
	if start.Filename != end.Filename || start.Line != end.Line {
		return "the expression at " + start.String()
	}
	data, err := os.ReadFile(start.Filename)
	if err != nil {
		return "the expression at " + start.String()
	}
	if end.Offset > len(data) || start.Offset < 0 || start.Offset > end.Offset {
		return "the expression at " + start.String()
	}
	return string(data[start.Offset:end.Offset])
}

// scanModule is every stall-window site in the module's test files.
func scanModule(t *testing.T) []stallSite {
	t.Helper()
	root := moduleRoot(t)
	fset := token.NewFileSet()

	var sites []stallSite
	for _, path := range testFiles(t, root) {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		alias := timingAlias(file)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			sites = append(sites, scanFunc(fset, root, path, alias, fn.Body)...)
		}
	}
	return sites
}

// TestEveryStallWindowATestUsesIsInTheRegistry is the absence half of
// the guard, and it is a row of its own rather than a clause of the
// measurement row below. They are two failures — a window nobody
// registered, and a registered window nobody measured — and one combined
// row would be satisfied by an implementation that caught either.
//
// REQUIRED MUTATION, RUN 2026-09-09: delete one entry's line from the
// Registry map, leaving the entry itself declared so the tree still
// compiles — a mutation that does not build is not evidence. Reds:
//
//	internal/flow/stream_test.go:1324 sets StreamStallTimeout from
//	timing.StreamPartialLineIsNotAStall, which is not in the registry.
//
// The positive control at the foot stays green, because the other four
// sites still resolve.
//
// IT ALSO RED A SECOND ROW, which the prediction did not say and the
// note records rather than tidies away: TestEveryDeclaredEntryIsInTheRegistry
// caught the same mutation from the other side — "declared and is not in
// the Registry map". The two see different halves. That row would stay
// green if the entry were deleted outright, and this one would then not
// compile; between them the entry cannot leave the map by either door.
func TestEveryStallWindowATestUsesIsInTheRegistry(t *testing.T) {
	sites := scanModule(t)

	// A GUARD THAT SCANNED NOTHING REPORTS THE SAME CLEAN RESULT AS A
	// GUARD OVER A CLEAN TREE. This repository has stall rows; a scan
	// finding no site at all means the walk, the parse or the field
	// names have stopped matching the code, not that the rule is kept.
	if len(sites) == 0 {
		t.Fatal("the scan found no stall window anywhere in this module's tests, " +
			"which cannot be true while the stall rows exist — the walk or the " +
			"field names have stopped matching the tree")
	}

	resolved := 0
	for _, site := range sites {
		switch {
		case site.entry == "":
			t.Errorf("%s:%d sets %s to %s, which is not a window from the registry.\n"+
				"A stall window is a margin, and a margin written inline carries its "+
				"number and not the reason the number was chosen. Add an entry to "+
				"internal/timing with the quantity it bounds and the evidence behind "+
				"it, and take the window from there.",
				site.file, site.line, site.field, site.text)
		case timing.Registry[site.entry] == nil:
			t.Errorf("%s:%d sets %s from timing.%s, which is not in the registry.",
				site.file, site.line, site.field, site.entry)
		default:
			resolved++
		}
	}

	// THE POSITIVE CONTROL. Without it a resolver that refused
	// everything — a broken alias, a selector shape that no longer
	// matches — would fail this row loudly for the wrong reason, and a
	// resolver that resolved nothing while the registry was empty would
	// pass it silently.
	if resolved == 0 {
		t.Errorf("not one of the %d stall windows found resolved to a registry "+
			"entry, so this row cannot distinguish a tree that keeps the rule from "+
			"a resolver that has stopped working", len(sites))
	}
}

// TestTheResolverAcceptsARegistryWindowAndRefusesEverythingElse is the
// resolver's own bench. The row above runs it over the real tree, where
// every site is expected to pass — so on its own it can only show that
// the resolver says yes, never that it can say no.
func TestTheResolverAcceptsARegistryWindowAndRefusesEverythingElse(t *testing.T) {
	// A real entry name, read from the registry rather than typed, so a
	// rename cannot leave this bench asserting about a name that is gone.
	var known string
	for name := range timing.Registry {
		if known == "" || name < known {
			known = name
		}
	}

	cases := []struct {
		name string
		// src is a function body the resolver is run over.
		src  string
		want string
	}{
		{
			name: "the selector written out",
			src:  "d.StallTimeout = timing." + known + ".Window",
			want: known,
		},
		{
			name: "a composite literal field",
			src:  "_ = deps{StreamStallTimeout: timing." + known + ".Window}",
			want: known,
		},
		{
			name: "one local name assigned once from the registry",
			src:  "stall := timing." + known + ".Window\nd.UploadStallTimeout = stall",
			want: known,
		},
		{
			name: "a duration written inline",
			src:  "d.StallTimeout = 600 * time.Millisecond",
			want: "",
		},
		{
			name: "a local name that starts at the registry and is then replaced",
			src: "stall := timing." + known + ".Window\n" +
				"stall = 60 * time.Millisecond\n" +
				"d.StreamStallTimeout = stall",
			want: "",
		},
		{
			name: "the entry without the window",
			src:  "d.StallTimeout = timing." + known,
			want: "",
		},
		{
			name: "a window from something that is not the registry package",
			src:  "d.StallTimeout = elsewhere." + known + ".Window",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			src := "package p\nfunc f() {\n" + tc.src + "\n}\n"
			file, err := parser.ParseFile(fset, "bench.go", src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parsing the bench source: %v", err)
			}
			fn := file.Decls[0].(*ast.FuncDecl)
			sites := scanFunc(fset, ".", "bench.go", "timing", fn.Body)
			if len(sites) != 1 {
				t.Fatalf("the scan found %d stall sites in this bench, want 1", len(sites))
			}
			if got := sites[0].entry; got != tc.want {
				t.Errorf("resolved to %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTheRegistryIsImportedByTestsOnly keeps a registry of test margins
// out of the shipped binary. It is not a size argument: the entries name
// test rows and carry measurement provenance, and none of that is
// something a person who downloads this client should find inside it.
func TestTheRegistryIsImportedByTestsOnly(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()

	var offenders []string
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", ".claude", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		// The registry's own source is the one non-test file allowed to
		// be in this package.
		if filepath.Dir(path) == filepath.Join(root, "internal", "timing") {
			return nil
		}
		scanned++
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err == nil && p == timingImportPath {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				offenders = append(offenders, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if scanned == 0 {
		t.Fatal("this row scanned no shipped source at all, so its silence is about " +
			"an empty walk rather than about a clean tree")
	}
	for _, path := range offenders {
		t.Errorf("%s is shipped code and imports the stall-window registry, which "+
			"exists for test margins and names test rows", path)
	}
}
