package guard

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/constant"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
)

// The failure contract, checked by a BOUNDED INTERPROCEDURAL CHECKER.
//
// # Why not an AST walk
//
// An AST walk over constructions cannot see a forwarded argument. This
// tree has three generic constructors whose action is a PARAMETER —
// ui.NewFailure, ui.Quoted, and flow.uploadFailed, which wraps the first
// — and nine production calls to the last of them. A checker that
// visited constructions and stopped would report nine obligations as
// satisfied by reading a parameter name, and a mutation at any of those
// nine callers would pass in silence. That is not a hypothetical: it is
// what the first draft of this guard's brief specified.
//
// # The three states, and the one that must never be silent
//
// Every obligation resolves to exactly one of:
//
//   - RESOLVED: a finite set of possible values, and it passes only when
//     every member is valid.
//   - INVALID: a resolved set containing something outside the contract.
//   - UNRESOLVED: a form this checker cannot establish.
//
// CI requires the invalid set AND the unresolved set to be empty, and no
// discovered site to be unclassified. Recursion, indirect calls and
// unsupported expressions FAIL CLOSED — they do not become verified
// because somebody attached a comment or a test name. The checker may
// conservatively reject a safe future implementation; that
// implementation uses a supported form or extends this file deliberately.
//
// # Summaries are verified, never declared
//
// A helper's summary — "this function's Nth parameter becomes the
// failure's action" — is established by reading the body. A registry
// entry saying "trust parameter 5" is not a summary, it is a wish. When
// a signature changes, the verification fails and the summary is
// rebuilt; it does not silently start describing a different argument.
// The setting-name guard next door reddened exactly this way when the
// constructors gained an id, which is the behaviour to keep.

// validActions is the contract's whole vocabulary for an action.
//
// MEMBERSHIP, NOT INEQUALITY. The previous rule was `Next != NextNone`,
// which admits the ZERO VALUE: NextNone is the string "None", so an
// unset field passed a test written to catch exactly that. The comment
// three lines above the constant says the fourth value exists so a
// surface can tell "no next step" from "nobody filled it in" — and that
// assertion could not tell them apart.
var validActions = map[string]bool{
	"NextNone": true, "NextFreshDeploy": true, "NextWait": true, "NextGiveUp": true,
	string(ui.NextNone): true, string(ui.NextFreshDeploy): true,
	string(ui.NextWait): true, string(ui.NextGiveUp): true,
}

func activeFailureID(value string) bool {
	for _, id := range ui.ActiveFailureIDs {
		if string(id) == value {
			return true
		}
	}
	return false
}

type obligation struct {
	where   string
	field   string
	expr    string
	chain   string
	values  []string // resolved possibilities; empty means unresolved
	problem string   // a structurally forbidden write or address-taking
}

// summary is one verified helper relationship: the parameter index whose
// value becomes the named field of the failure the helper builds.
type summary struct {
	fn    string
	idArg int
	act   int
	txt   int
}

func TestTheFailureContractHolds(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()

	var files []parsedFile
	for _, path := range publishedTextFiles(t, root) {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			// A FILE THAT WILL NOT PARSE IS NOT A FILE WITH NO
			// OBLIGATIONS. The setting-name guard continues past this and
			// should not; here it stops the run.
			t.Fatalf("%s did not parse, so this guard cannot say what is in it: %v",
				displayPath(root, path), err)
		}
		files = append(files, parsedFile{path: path, file: f, src: src})
	}
	if len(files) == 0 {
		t.Fatal("no published Go file was scanned, so this guard measured nothing")
	}
	attachFailureTypeInfo(t, root, fset, files)

	text := func(p parsedFile, n ast.Node) string {
		lo := fset.Position(n.Pos()).Offset
		hi := fset.Position(n.End()).Offset
		if lo < 0 || hi > len(p.src) || lo > hi {
			return ""
		}
		return strings.Join(strings.Fields(string(p.src[lo:hi])), " ")
	}

	// A TREE-WIDE INDEX of functions whose every return is a non-blank
	// string, so a copy helper in another file of the same package is
	// resolvable. Built once, before anything is classified.
	nonBlankHelpers = map[string]bool{}
	for _, p := range files {
		for _, d := range p.file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				key := packageFuncKey(root, p.path, fd.Name.Name)
				good := allReturnsNonBlankDecl(fd)
				if old, seen := nonBlankHelpers[key]; seen {
					nonBlankHelpers[key] = old && good
				} else {
					nonBlankHelpers[key] = good
				}
			}
		}
	}

	// --- 1. VERIFY THE SUMMARIES AGAINST THE BODIES -------------------
	summaries := map[string]summary{}
	for _, p := range files {
		for _, d := range p.file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			s, ok := verifySummary(fd, text, p)
			if ok {
				summaries[declaredFuncKey(fd, p.info)] = s
			}
		}
	}
	for _, want := range []string{"NewFailure", "Quoted", "uploadFailed"} {
		found := false
		for key := range summaries {
			found = found || strings.HasSuffix(key, "."+want)
		}
		if !found {
			t.Errorf("no VERIFIED summary for %s. Either its body stopped "+
				"forwarding its arguments to the failure it builds, or this "+
				"checker no longer understands the shape it uses — and both mean "+
				"every call to it is unchecked rather than fine", want)
		}
	}

	// --- 2. COLLECT OBLIGATIONS ---------------------------------------
	var obs []obligation
	sitesVisited := 0
	for _, p := range files {
		rel := displayPath(root, p.path)
		ast.Inspect(p.file, func(n ast.Node) bool {
			// A COMPOSITE LITERAL IS A CONSTRUCTION TOO, and leaving it
			// out is how four failures came to carry no id while this
			// checker reported a clean census. Three of them were the
			// standing failures a person meets when the program cannot
			// ask them anything — no terminal, no usable answer, a closed
			// door — and they rendered no id line at all.
			//
			// The checker's claim was "every production ui.Failure
			// construction" and it visited only the ones built through a
			// summarised helper. The claim was the thing that was wrong.
			if lit, isLit := n.(*ast.CompositeLit); isLit {
				isFailure := isFailureExpr(p.info, lit)
				embeddedZeros := embeddedZeroFailures(p.info, lit)
				if !isFailure && embeddedZeros == 0 {
					return true
				}
				pos := fset.Position(lit.Pos())
				site := rel + ":" + itoa(pos.Line)
				if _, inHelper := summaries[enclosingFuncKey(p, lit.Pos())]; inHelper {
					return true
				}
				in := enclosingDecl(p.file, lit.Pos())
				field := func(name string) ast.Expr {
					for _, elt := range lit.Elts {
						if kv, ok := elt.(*ast.KeyValueExpr); ok &&
							calleeName(kv.Key) == name {
							return kv.Value
						}
					}
					return nil
				}
				if isFailure {
					sitesVisited++
					obs = append(obs,
						resolveIn(site, "ID", field("ID"), text, p, "id", in, 0),
						resolveIn(site, "Next", field("Next"), text, p, "action", in, 0),
						resolveIn(site, "NextText", field("NextText"), text, p, "text", in, 0))
				}
				for range embeddedZeros {
					sitesVisited++
					obs = append(obs,
						obligation{where: site, field: "ID", expr: "embedded zero ui.Failure"},
						obligation{where: site, field: "Next", expr: "embedded zero ui.Failure"},
						obligation{where: site, field: "NextText", expr: "embedded zero ui.Failure"})
				}
				return true
			}
			if as, ok := n.(*ast.AssignStmt); ok {
				for i, lhs := range as.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok || !isProtectedFailureField(p.info, sel) {
						continue
					}
					pos := fset.Position(sel.Pos())
					site := rel + ":" + itoa(pos.Line)
					if sel.Sel.Name == "NextText" && i < len(as.Rhs) && len(as.Lhs) == len(as.Rhs) {
						obs = append(obs, resolveIn(site, "NextText", as.Rhs[i], text, p,
							"text", enclosingDecl(p.file, as.Pos()), 0))
					} else {
						obs = append(obs, obligation{where: site, field: sel.Sel.Name,
							expr: text(p, lhs), problem: "protected field written after construction"})
					}
				}
				return true
			}
			if unary, ok := n.(*ast.UnaryExpr); ok && unary.Op == token.AND {
				if sel, ok := unary.X.(*ast.SelectorExpr); ok && isProtectedFailureField(p.info, sel) {
					pos := fset.Position(unary.Pos())
					obs = append(obs, obligation{where: rel + ":" + itoa(pos.Line),
						field: sel.Sel.Name, expr: text(p, unary),
						problem: "address of protected field taken"})
				}
				return true
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			key := calledFuncKey(call.Fun, enclosingDecl(p.file, call.Pos()), p.info)
			s, known := summaries[key]
			if !known {
				return true
			}
			at := func(i int) ast.Expr {
				if i < 0 || i >= len(call.Args) {
					return nil
				}
				return call.Args[i]
			}
			pos := fset.Position(call.Pos())
			site := rel + ":" + itoa(pos.Line)
			// INSIDE A SUMMARISED HELPER the arguments are its own
			// parameters, which is a DEFINITION rather than an
			// obligation — it is what the summary was verified from.
			// Callers are what carry obligations, and that is the whole
			// point: uploadFailed's body is checked once, its nine
			// callers are checked nine times.
			if _, isHelper := summaries[enclosingFuncKey(p, call.Pos())]; isHelper {
				return true
			}
			sitesVisited++
			in := enclosingDecl(p.file, call.Pos())
			obs = append(obs,
				resolveIn(site, "ID", at(s.idArg), text, p, "id", in, 0),
				resolveIn(site, "Next", at(s.act), text, p, "action", in, 0),
				resolveIn(site, "NextText", at(s.txt), text, p, "text", in, 0))
			return true
		})
	}
	if len(obs) == 0 {
		t.Fatal("no obligation was discovered, so this guard asserted nothing")
	}

	// --- 3. CLASSIFY ---------------------------------------------------
	var invalid, unresolved []string
	for _, o := range obs {
		switch {
		case o.problem != "":
			invalid = append(invalid, o.where+" "+o.field+" "+o.problem+": "+o.expr)
		case len(o.values) == 0:
			unresolved = append(unresolved,
				o.where+" "+o.field+" = "+o.expr+" (unsupported form)")
		case o.field == "Next":
			for _, v := range o.values {
				if !validActions[v] {
					invalid = append(invalid,
						o.where+" Next = "+v+", which is not one of the four")
				}
			}
		case o.field == "ID":
			for _, v := range o.values {
				if !activeFailureID(v) {
					invalid = append(invalid, o.where+" has failure id "+v+
						", which is not in ActiveFailureIDs")
				}
			}
		case o.field == "NextText":
			for _, v := range o.values {
				if v == "blank" {
					invalid = append(invalid, o.where+" NextText is blank")
				}
			}
		}
	}
	sort.Strings(invalid)
	sort.Strings(unresolved)

	for _, m := range invalid {
		t.Errorf("INVALID: %s", m)
	}
	// AN UNRESOLVED OBLIGATION IS EITHER COVERED BY A NAMED SCENARIO OR
	// IT FAILS. There is no third option and no baseline file.
	//
	// Six obligations in this tree carry values that arrive at RUN TIME —
	// a family id read off a finding, and three values taken from a
	// helper's multiple returns. No static analysis of the call site can
	// establish those, and pretending otherwise would mean either a
	// checker that guesses or a rule that quietly skips what it cannot
	// see. Each is listed here against the test that exercises it, and
	// that test's existence is asserted below: a covered entry naming a
	// test nobody wrote is the mute button this mechanism exists to
	// avoid.
	for _, m := range unresolved {
		if by, ok := scenarioCoverage(m); ok {
			t.Logf("unresolved statically, covered by scenario: %s (%s)", m, by)
			continue
		}
		t.Errorf("UNRESOLVED: %s\nAn obligation this checker cannot establish is "+
			"not an obligation that is satisfied. Use a supported form, extend the "+
			"checker deliberately, or name the scenario test that covers it.", m)
	}
	// RECORDED FOR THE CENSUS ROW, which compares this against an
	// independently written count of the tree's constructions. Two
	// numbers from one enumeration would agree with themselves.
	contractSitesVisited = sitesVisited
	t.Logf("%d obligations across %d sites, %d invalid, %d unresolved",
		len(obs), contractSitesVisited, len(invalid), len(unresolved))
}

func packageFuncKey(root, path, name string) string {
	rel, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil {
		return ""
	}
	pkg := modulePath
	if rel != "." {
		pkg += "/" + filepath.ToSlash(rel)
	}
	return pkg + "." + name
}

// verifySummary establishes a helper's relationship by READING ITS BODY.
//
// Two shapes are supported, because two are what this tree uses: a
// function whose body returns a &Failure{...} literal wiring its own
// parameters to fields, and a function that forwards its parameters to
// another summarised helper. Anything else is not summarised, and every
// call to it becomes unresolved rather than assumed.
func verifySummary(fd *ast.FuncDecl, text func(parsedFile, ast.Node) string,
	p parsedFile) (summary, bool) {

	params := paramNames(fd)
	s := summary{fn: fd.Name.Name, idArg: -1, act: -1, txt: -1}
	found := false

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CompositeLit:
			if !isFailureExpr(p.info, node) {
				return true
			}
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				id, ok := kv.Value.(*ast.Ident)
				if !ok {
					continue
				}
				i, isParam := params[id.Name]
				if !isParam {
					continue
				}
				switch calleeName(kv.Key) {
				case "ID":
					s.idArg, found = i, true
				case "Next":
					s.act, found = i, true
				case "NextText":
					s.txt, found = i, true
				}
			}
		case *ast.CallExpr:
			// A FORWARDING HELPER: its own parameters passed straight to
			// a constructor. uploadFailed is the one in this tree.
			c := calledFuncKey(node.Fun, fd, p.info)
			if c != modulePath+"/internal/ui.NewFailure" &&
				c != modulePath+"/internal/ui.Quoted" {
				return true
			}
			for i, arg := range node.Args {
				id, ok := arg.(*ast.Ident)
				if !ok {
					continue
				}
				j, isParam := params[id.Name]
				if !isParam {
					continue
				}
				// Constructor positions: (id, what, why|detail, next, nextText)
				switch i {
				case 0:
					s.idArg, found = j, true
				case 3:
					s.act, found = j, true
				case 4:
					s.txt, found = j, true
				}
			}
		}
		return true
	})
	if !found || s.idArg < 0 || s.act < 0 || s.txt < 0 {
		return summary{}, false
	}
	return s, true
}

type parsedFile = struct {
	path string
	file *ast.File
	src  []byte
	info *types.Info
}

// attachFailureTypeInfo type-checks repository packages from their source.
// The collector then asks about the object or type bound to an AST node,
// never the unqualified spelling the author chose for it.
func attachFailureTypeInfo(t *testing.T, root string, fset *token.FileSet, files []parsedFile) {
	t.Helper()
	allByImport := map[string][]*parsedFile{}
	for i := range files {
		rel, err := filepath.Rel(root, filepath.Dir(files[i].path))
		if err != nil {
			t.Fatalf("finding package for %s: %v", files[i].path, err)
		}
		path := modulePath
		if rel != "." {
			path += "/" + filepath.ToSlash(rel)
		}
		allByImport[path] = append(allByImport[path], &files[i])
	}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		ctx := build.Default
		ctx.GOOS = goos
		if goos != "windows" {
			ctx.BuildTags = append(ctx.BuildTags, "unix")
		}
		selected := map[string][]*parsedFile{}
		for path, group := range allByImport {
			for _, p := range group {
				matched, err := ctx.MatchFile(filepath.Dir(p.path), filepath.Base(p.path))
				if err != nil {
					t.Fatalf("reading build constraints in %s: %v", p.path, err)
				}
				if matched {
					selected[path] = append(selected[path], p)
				}
			}
		}
		l := &failureSourceImporter{
			fset: fset, sources: selected, packages: map[string]*types.Package{},
			loading: map[string]bool{}, fallback: archiveImporter(fset, root, goos),
		}
		for path := range selected {
			if _, err := l.Import(path); err != nil {
				t.Fatalf("type-checking %s for %s failure identities: %v", path, goos, err)
			}
		}
	}
}

func archiveImporter(fset *token.FileSet, root, goos string) types.Importer {
	return importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		cmd := exec.Command("go", "list", "-export", "-f", "{{.Export}}", path)
		cmd.Dir = root
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GOOS=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "GOOS="+goos)
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("locating export data for %s: %w", path, err)
		}
		archive := strings.TrimSpace(string(out))
		if archive == "" {
			return nil, fmt.Errorf("go list returned no export data for %s", path)
		}
		return os.Open(archive)
	})
}

type failureSourceImporter struct {
	fset     *token.FileSet
	sources  map[string][]*parsedFile
	packages map[string]*types.Package
	loading  map[string]bool
	fallback types.Importer
}

func (l *failureSourceImporter) Import(path string) (*types.Package, error) {
	if pkg := l.packages[path]; pkg != nil {
		return pkg, nil
	}
	group, local := l.sources[path]
	if !local {
		return l.fallback.Import(path)
	}
	if l.loading[path] {
		return nil, types.Error{Msg: "import cycle involving " + path}
	}
	l.loading[path] = true
	defer delete(l.loading, path)
	asts := make([]*ast.File, 0, len(group))
	for _, p := range group {
		asts = append(asts, p.file)
	}
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: l, GoVersion: "go1.26"}
	pkg, err := conf.Check(path, l.fset, asts, info)
	if err != nil {
		return nil, err
	}
	l.packages[path] = pkg
	for _, p := range group {
		p.info = info
	}
	return pkg, nil
}

func objectKey(obj types.Object) string {
	if obj == nil || obj.Pkg() == nil {
		return ""
	}
	return obj.Pkg().Path() + "." + obj.Name()
}

func declaredFuncKey(fd *ast.FuncDecl, info *types.Info) string {
	if fd == nil || info == nil {
		return ""
	}
	return objectKey(info.Defs[fd.Name])
}

func enclosingFuncKey(p parsedFile, pos token.Pos) string {
	for _, d := range p.file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && pos >= fd.Pos() && pos <= fd.End() {
			return declaredFuncKey(fd, p.info)
		}
	}
	return ""
}

func calledFuncKey(fun ast.Expr, within *ast.FuncDecl, info *types.Info) string {
	if info == nil || fun == nil {
		return ""
	}
	switch f := fun.(type) {
	case *ast.Ident:
		if fn, ok := info.Uses[f].(*types.Func); ok {
			return objectKey(fn)
		}
		if within != nil {
			if def, ok := soleDefinition(f.Name, within); ok {
				return calledFuncKey(def, within, info)
			}
		}
	case *ast.SelectorExpr:
		if fn, ok := info.Uses[f.Sel].(*types.Func); ok {
			return objectKey(fn)
		}
	case *ast.ParenExpr:
		return calledFuncKey(f.X, within, info)
	}
	return ""
}

func isFailureExpr(info *types.Info, expr ast.Expr) bool {
	if info == nil || expr == nil {
		return false
	}
	return isFailureType(info.TypeOf(expr))
}

func isProtectedFailureField(info *types.Info, sel *ast.SelectorExpr) bool {
	if info == nil || sel == nil {
		return false
	}
	switch sel.Sel.Name {
	case "ID", "Next", "NextText":
		return isFailureExpr(info, sel.X)
	}
	return false
}

func embeddedZeroFailures(info *types.Info, lit *ast.CompositeLit) int {
	if info == nil || lit == nil || isFailureExpr(info, lit) {
		return 0
	}
	typ := types.Unalias(info.TypeOf(lit))
	if named, ok := typ.(*types.Named); ok {
		typ = named.Underlying()
	}
	st, ok := typ.(*types.Struct)
	if !ok {
		return 0
	}
	count := 0
	for i := 0; i < st.NumFields(); i++ {
		field := st.Field(i)
		if !field.Anonymous() || !isFailureValueType(field.Type()) {
			continue
		}
		provided := false
		if len(lit.Elts) > 0 {
			if _, keyed := lit.Elts[0].(*ast.KeyValueExpr); keyed {
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					id, named := kv.Key.(*ast.Ident)
					if ok && named && id.Name == field.Name() {
						provided = true
					}
				}
			} else if i < len(lit.Elts) {
				provided = true
			}
		}
		if !provided {
			count++
		}
	}
	return count
}

func isFailureType(typ types.Type) bool {
	if typ == nil {
		return false
	}
	if ptr, ok := types.Unalias(typ).(*types.Pointer); ok {
		typ = ptr.Elem()
	}
	return isFailureValueType(typ)
}

func isFailureValueType(typ types.Type) bool {
	if typ == nil {
		return false
	}
	named, ok := types.Unalias(typ).(*types.Named)
	return ok && objectKey(named.Obj()) == modulePath+"/internal/ui.Failure"
}

type fixedImporter map[string]*types.Package

func (i fixedImporter) Import(path string) (*types.Package, error) {
	if pkg := i[path]; pkg != nil {
		return pkg, nil
	}
	return nil, fmt.Errorf("fixture import %q not provided", path)
}

func typedFailureFixture(t *testing.T, source string) parsedFile {
	t.Helper()
	fset := token.NewFileSet()
	uiSource := `package ui
		type FailureID string
		type NextAction string
		type Failure struct { ID FailureID; Next NextAction; NextText string }
		func NewFailure(id FailureID, next NextAction, text string) *Failure {
			return &Failure{ID: id, Next: next, NextText: text}
		}`
	uiFile, err := parser.ParseFile(fset, "ui.go", uiSource, 0)
	if err != nil {
		t.Fatal(err)
	}
	uiPkg, err := (&types.Config{}).Check(modulePath+"/internal/ui", fset, []*ast.File{uiFile}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(fset, "fixture.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{},
		Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: fixedImporter{modulePath + "/internal/ui": uiPkg}}
	if _, err := conf.Check("fixture", fset, []*ast.File{f}, info); err != nil {
		t.Fatal(err)
	}
	return parsedFile{path: "fixture.go", file: f, src: []byte(source), info: info}
}

func TestFailureRecognitionUsesTypesAcrossAliasesAndElision(t *testing.T) {
	p := typedFailureFixture(t, `package fixture
		import ui "github.com/curiouspub/cli/internal/ui"
		type F = ui.Failure
		type Failure struct{}
		func use() {
			_ = F{}
			_ = []ui.Failure{{}}
			_ = map[string]ui.Failure{"x": {}}
			_ = [1]ui.Failure{{}}
			_ = struct{ ui.Failure }{}
			_ = Failure{}
			makeFailure := ui.NewFailure
			_ = makeFailure("id", "next", "text")
		}`)
	literals, calls := 0, 0
	ast.Inspect(p.file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if isFailureExpr(p.info, n) {
				literals++
			}
			literals += embeddedZeroFailures(p.info, n)
		case *ast.CallExpr:
			if calledFuncKey(n.Fun, enclosingDecl(p.file, n.Pos()), p.info) ==
				modulePath+"/internal/ui.NewFailure" {
				calls++
			}
		}
		return true
	})
	if literals != 5 || calls != 1 {
		t.Fatalf("recognised %d failure literals and %d aliased constructor calls, want 5 and 1", literals, calls)
	}
}

func TestProtectedWritesAreFoundThroughCopiesAndAddresses(t *testing.T) {
	p := typedFailureFixture(t, `package fixture
		import ui "github.com/curiouspub/cli/internal/ui"
		func use(f ui.Failure) {
			f.Next = ""
			f.ID = ""
			g := f
			g.Next = ""
			p := &f.ID
			*p = ""
			f.NextText = "Retry."
			f.NextText = "\t"
		}`)
	forbidden, checkedText := 0, 0
	blankText := false
	ast.Inspect(p.file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok && isProtectedFailureField(p.info, sel) {
					if sel.Sel.Name == "NextText" {
						checkedText++
						if i < len(n.Rhs) {
							if lit, ok := n.Rhs[i].(*ast.BasicLit); ok {
								classification, decoded := classifyTextLiteral(lit)
								blankText = blankText || decoded && classification == "blank"
							}
						}
					} else {
						forbidden++
					}
				}
			}
		case *ast.UnaryExpr:
			if n.Op == token.AND {
				if sel, ok := n.X.(*ast.SelectorExpr); ok && isProtectedFailureField(p.info, sel) {
					forbidden++
				}
			}
		}
		return true
	})
	if forbidden != 4 || checkedText != 2 || !blankText {
		t.Fatalf("found %d forbidden writes/addresses and %d checked NextText writes (blank found %t), want 4, 2, true",
			forbidden, checkedText, blankText)
	}
}

func TestNonBlankAnalysisDecodesLiteralsAndRejectsNakedReturns(t *testing.T) {
	for _, literal := range []string{`"\t"`, `"\n"`, `"\u0020"`, "`  `"} {
		f, err := parser.ParseExpr(literal)
		if err != nil {
			t.Fatal(err)
		}
		if containsNonBlankLiteral(f) {
			t.Errorf("%s was treated as non-blank without decoding it", literal)
		}
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", `package fixture
		func advice(b bool) (s string) { if b { return }; return "Retry." }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	fd := f.Decls[0].(*ast.FuncDecl)
	if allReturnsNonBlankDecl(fd) {
		t.Fatal("a helper with a naked blank return was certified non-blank")
	}
	nonBlankHelpers = map[string]bool{"one.advice": false, "two.advice": true}
	if allReturnsNonBlankAnywhere("one.advice") || !allReturnsNonBlankAnywhere("two.advice") {
		t.Fatal("same-named helpers in different packages contaminated one another")
	}
}

// resolve reduces one argument expression to the finite set of values it
// can carry, or to nothing at all — which is UNRESOLVED and fails.
func resolve(site, field string, e ast.Expr, text func(parsedFile, ast.Node) string,
	p parsedFile, kind string) obligation {
	return resolveIn(site, field, e, text, p, kind, nil, 0)
}

// resolveIn carries the enclosing function, so a local name can be traced
// to the statement that defined it, and a depth, so a cycle stops rather
// than recursing forever. RECURSION FAILS CLOSED: past the bound the
// obligation is unresolved, which is a visible failure.
func resolveIn(site, field string, e ast.Expr, text func(parsedFile, ast.Node) string,
	p parsedFile, kind string, within *ast.FuncDecl, depth int) obligation {

	o := obligation{where: site, field: field}
	if depth > 4 {
		return o
	}
	if e == nil {
		return o
	}
	o.expr = text(p, e)

	// A NAME IS NOT A VALUE UNTIL IT IS ONE. An identifier this checker
	// cannot tie to a declared constant is UNRESOLVED, never invalid: a
	// local variable holding a perfectly good action would otherwise be
	// reported as an illegal action, which sends a reader to fix code
	// that is right. The first run of this checker did exactly that to
	// the upload refusal's `action` variable.
	known := func(name string) bool {
		return validActions[name] || strings.HasPrefix(name, "ID")
	}
	if kind != "text" && p.info != nil {
		if tv, ok := p.info.Types[e]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
			o.values = []string{constant.StringVal(tv.Value)}
			return o
		}
	}
	switch v := e.(type) {
	case *ast.SelectorExpr: // ui.NextWait, ui.IDUploadStalled
		if known(v.Sel.Name) {
			o.values = []string{v.Sel.Name}
		}
	case *ast.Ident: // NextWait inside package ui, or a name to trace
		if known(v.Name) {
			o.values = []string{v.Name}
			break
		}
		// A PACKAGE-LEVEL CONSTANT resolves to its own value.
		if def := packageConst(v.Name, p.file); def != nil {
			return resolveIn(site, field, def, text, p, kind, within, depth+1)
		}
		// A LOCAL NAME resolves to its reaching definition, when there is
		// exactly one. More than one assignment and it stays unresolved:
		// this checker unions branches it can see and refuses what it
		// cannot.
		if within != nil {
			if def, ok := soleDefinition(v.Name, within); ok {
				return resolveIn(site, field, def, text, p, kind, within, depth+1)
			}
		}
	case *ast.BasicLit:
		if kind == "text" {
			classification, ok := classifyTextLiteral(v)
			if !ok {
				return o
			}
			o.values = []string{classification}
		} else {
			o.values = []string{v.Value}
		}
	case *ast.BinaryExpr: // concatenation
		if kind == "text" && containsNonBlankLiteral(v) {
			o.values = []string{"nonblank"}
		}
	case *ast.CallExpr:
		// A CONVERSION PRESERVES ITS VALUE: ui.FailureID(x) where x is
		// itself resolvable.
		if len(v.Args) == 1 {
			if inner := resolve(site, field, v.Args[0], text, p, kind); len(inner.values) > 0 {
				o.values = inner.values
			}
		}
		// A COPY HELPER WHOSE EVERY RETURN IS NON-BLANK establishes
		// non-blank for its caller. This is the bounded string analysis
		// the contract asks for, and it is why retryAdvice does not have
		// to be special-cased: its returns are concatenations containing
		// authored literals, so the property holds by reading them.
		if kind == "text" && o.values == nil {
			if allReturnsNonBlankAnywhere(calledFuncKey(v.Fun, within, p.info)) {
				o.values = []string{"nonblank"}
			}
		}
	}
	return o
}

// containsNonBlankLiteral is the bounded string analysis: a concatenation
// is non-blank when any component is a non-blank literal, because adding
// anything to a non-blank string leaves it non-blank.
func containsNonBlankLiteral(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		classification, ok := classifyTextLiteral(v)
		return ok && classification == "nonblank"
	case *ast.BinaryExpr:
		return containsNonBlankLiteral(v.X) || containsNonBlankLiteral(v.Y)
	case *ast.ParenExpr:
		return containsNonBlankLiteral(v.X)
	}
	return false
}

func classifyTextLiteral(lit *ast.BasicLit) (string, bool) {
	if lit == nil || lit.Kind != token.STRING {
		return "", false
	}
	decoded, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	if strings.TrimSpace(decoded) == "" {
		return "blank", true
	}
	return "nonblank", true
}

func paramNames(fd *ast.FuncDecl) map[string]int {
	out := map[string]int{}
	i := 0
	if fd.Type.Params == nil {
		return out
	}
	for _, f := range fd.Type.Params.List {
		for _, n := range f.Names {
			out[n.Name] = i
			i++
		}
	}
	return out
}

// allReturnsNonBlank establishes that every return path of a function in
// one declaration yields a non-blank string.
//
// IT IS DELIBERATELY SHALLOW. The tree-wide index qualifies declarations
// by package and ANDs same-symbol build variants, so a blank Windows return
// cannot borrow a non-blank Unix implementation's result. A deeper analysis
// would resolve more and be harder to trust; unresolved is the safer side.
func allReturnsNonBlankDecl(fd *ast.FuncDecl) bool {
	if fd == nil || fd.Body == nil {
		return false
	}
	saw := false
	ok := true
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		ret, is := n.(*ast.ReturnStmt)
		if !is {
			return true
		}
		saw = true
		if len(ret.Results) == 0 {
			ok = false
			return true
		}
		// The LAST result is the next-step string in every copy helper
		// this tree has; a helper shaped otherwise stays unresolved.
		if !containsNonBlankLiteral(ret.Results[len(ret.Results)-1]) {
			ok = false
		}
		return true
	})
	return saw && ok
}

// packageConst finds a package-level constant's value in this file.
func packageConst(name string, f *ast.File) ast.Expr {
	for _, d := range f.Decls {
		gen, ok := d.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, n := range vs.Names {
				if n.Name == name && i < len(vs.Values) {
					return vs.Values[i]
				}
			}
		}
	}
	return nil
}

// soleDefinition returns the expression a local name is assigned, but
// ONLY when it is assigned exactly once.
//
// TWO ASSIGNMENTS MEAN UNRESOLVED, not "pick one". A name written twice
// carries two possible values and this checker unions what it can see
// rather than guessing which reached the call.
func soleDefinition(name string, fd *ast.FuncDecl) (ast.Expr, bool) {
	var found ast.Expr
	count := 0
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range as.Lhs {
			id, ok := lhs.(*ast.Ident)
			if !ok || id.Name != name {
				continue
			}
			count++
			// A ONE-TO-ONE ASSIGNMENT carries an expression this checker
			// can follow. A multi-value call — id, action := f() — does
			// not: the value is the function's Nth result, which needs
			// the callee's return analysis, so it stays unresolved.
			if len(as.Rhs) == len(as.Lhs) {
				found = as.Rhs[i]
			} else {
				found = nil
			}
		}
		return true
	})
	return found, count == 1 && found != nil
}

func enclosingDecl(f *ast.File, p token.Pos) *ast.FuncDecl {
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && p >= fd.Pos() && p <= fd.End() {
			return fd
		}
	}
	return nil
}

// nonBlankHelpers indexes, across the whole tree, the functions whose
// every return yields a non-blank string. A copy helper called from
// another file of its package is resolvable through it.
var nonBlankHelpers map[string]bool

func allReturnsNonBlankAnywhere(key string) bool { return key != "" && nonBlankHelpers[key] }

// scenarioCovered names, for each obligation no static analysis of the
// call site can establish, the test that does establish it.
//
// EVERY ENTRY IS A VALUE THAT ARRIVES AT RUN TIME. ownCopy reads its
// family from a check.Finding; the upload refusal takes its id, action
// and copy from a helper's multiple returns. The checker refuses to
// invent a value for either, and these tests supply the coverage the
// checker cannot.
var scenarioCovered = map[string]string{
	"internal/flow/preflight.go:223 ID":       "flow.TestAHardStopCarriesItsCheckFamilyIntoTheFailure",
	"internal/flow/preflight.go:223 NextText": "flow.TestAHardStopCarriesItsCheckFamilyIntoTheFailure",
	"internal/flow/upload.go:340 NextText":    "flow.TestTheOtherTwoRefusalBranchesKeepTheirOwnFamilies",
	"internal/flow/upload.go:372 ID":          "flow.TestARefusalInsideTheWindowSaysGiveUp",
	"internal/flow/upload.go:372 Next":        "flow.TestARefusalInsideTheWindowSaysGiveUp",
	"internal/flow/upload.go:372 NextText":    "flow.TestARefusalInsideTheWindowSaysGiveUp",
}

// scenarioCoveredExpression binds each unchanged waiver above to the exact
// expression its scenario exercises. A line number says where an expression
// used to be; it does not prove that replacement code has the same behaviour.
var scenarioCoveredExpression = map[string]string{
	"internal/flow/preflight.go:223 ID":       "ui.FailureID(f.FailureID)",
	"internal/flow/preflight.go:223 NextText": "next",
	"internal/flow/upload.go:340 NextText":    "next",
	"internal/flow/upload.go:372 ID":          "id",
	"internal/flow/upload.go:372 Next":        "action",
	"internal/flow/upload.go:372 NextText":    "next",
}

func scenarioCoverage(message string) (string, bool) {
	key := keyOf(message)
	by, ok := scenarioCovered[key]
	if !ok {
		return "", false
	}
	want, fingerprinted := scenarioCoveredExpression[key]
	if !fingerprinted {
		return "", false
	}
	prefix := key + " = "
	if !strings.HasPrefix(message, prefix) {
		return "", false
	}
	got, _, ok := strings.Cut(strings.TrimPrefix(message, prefix), " (unsupported form)")
	return by, ok && got == want
}

func TestAScenarioWaiverBelongsToItsExpression(t *testing.T) {
	key := "internal/flow/upload.go:372 Next"
	if _, ok := scenarioCoverage(key + " = changedAtRuntime() (unsupported form)"); ok {
		t.Fatal("a different expression inherited the scenario waiver at the same location")
	}
	if by, ok := scenarioCoverage(key + " = action (unsupported form)"); !ok || by == "" {
		t.Fatal("the expression the scenario actually covers lost its waiver")
	}
}

// keyOf is the site and field of an unresolved message, which is what
// scenarioCovered is keyed by.
func keyOf(message string) string {
	parts := strings.Fields(message)
	if len(parts) < 2 {
		return message
	}
	return parts[0] + " " + parts[1]
}

// TestEveryScenarioCoverEntryAssertsTheNamedField.
//
// # The control that was one level too weak
//
// Its first form asserted only that the named test EXISTED. That is not
// coverage, and the gap was found the day after it was written: the
// entry for `upload.go:340 NextText` named a test that calls
// `refusalCopy` as `id, action, _, _ :=` — discarding the copy entirely —
// and then asserts on the id and the action and nothing else. A test
// that exists, runs, passes, and never reads the field it is recorded as
// covering.
//
// An entry naming a test that does not touch its field is the
// baseline-shaped mute button with one extra step of indirection: the
// list looks checked because something on the other end has the right
// name.
//
// So the control reads the named test's BODY and requires an assertion
// that mentions the field. That is deliberately a low bar — it does not
// judge whether the assertion is a good one — but it is a bar the
// existing miss fails, and a bar that cannot be met by naming.
//
// REQUIRED MUTATION, run 2026-09-12: point an entry at a test that does
// not read its field. Reds here, naming both.
func TestEveryScenarioCoverEntryAssertsTheNamedField(t *testing.T) {
	root := moduleRoot(t)
	if len(scenarioCovered) == 0 {
		t.Skip("nothing is scenario-covered, so there is nothing to check")
	}
	fset := token.NewFileSet()

	// Index every test function body in the tree, by package.TestName.
	bodies := map[string]*ast.FuncDecl{}
	files := map[string]parsedFile{}
	for _, path := range publishedTextFiles(t, root) {
		if !strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		f, perr := parser.ParseFile(fset, path, src, 0)
		if perr != nil {
			t.Fatalf("%s did not parse: %v", displayPath(root, path), perr)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && strings.HasPrefix(fd.Name.Name, "Test") {
				key := f.Name.Name + "." + fd.Name.Name
				bodies[key] = fd
				files[key] = parsedFile{path: path, file: f, src: src}
			}
		}
	}

	for obligation, named := range scenarioCovered {
		fd, ok := bodies[named]
		if !ok {
			t.Errorf("%s is recorded as covered by %s, and no such test exists. "+
				"An entry naming a test nobody wrote reads exactly like coverage "+
				"and is none", obligation, named)
			continue
		}
		parts := strings.Fields(obligation)
		if len(parts) < 2 {
			t.Errorf("%q is not a <site> <field> key", obligation)
			continue
		}
		field := parts[1]

		// THE FIELD MUST APPEAR IN AN ASSERTION, not merely in the file.
		// A mention in a comment, or in a struct being built as a
		// fixture, is not the test reading the value under test.
		p := files[named]
		touched := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			cond, ok := n.(*ast.IfStmt)
			if !ok || cond.Cond == nil {
				return true
			}
			lo := fset.Position(cond.Cond.Pos()).Offset
			hi := fset.Position(cond.Cond.End()).Offset
			if lo < 0 || hi > len(p.src) || lo > hi {
				return true
			}
			// THE FIELD NAME, OR THE LOCAL A GO AUTHOR WOULD HOLD IT IN.
			// A test reads the value out of a helper's returns into a
			// lower-camel local — `nextText` for NextText — and that is
			// the same field under the convention every Go file here
			// follows. Matching only the exported spelling reported two
			// tests as not asserting a field they assert on the next
			// line; matching case-insensitively would let `valid` count
			// as a mention of ID.
			condText := string(p.src[lo:hi])
			if strings.Contains(condText, field) ||
				strings.Contains(condText, lowerFirst(field)) {
				touched = true
			}
			return true
		})
		if !touched {
			t.Errorf("%s is recorded as covered by %s, and that test never asserts "+
				"on %s.\nThe checker cannot establish this obligation and the test "+
				"named as covering it does not either, so nothing does — which is "+
				"worse than an unresolved entry, because this one looks answered.",
				obligation, named, field)
		}
	}
}

// lowerFirst is the field name as a Go local would spell it.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// contractSitesVisited is how many construction sites the contract
// checker last visited, and contractSiteCount is how the census row
// obtains it without depending on test order.
var contractSitesVisited int

func contractSiteCount(t *testing.T) int {
	t.Helper()
	if contractSitesVisited > 0 {
		return contractSitesVisited
	}
	// THE CENSUS ROW MUST NOT PASS BY RUNNING FIRST. Go orders tests
	// within a package by declaration, and a row that silently read a
	// zero would compare nothing against nothing and report clean.
	//
	// IT RUNS THE COLLECTION, NOT THE TEST. Calling the contract test
	// under this row's own *testing.T attributed every contract failure
	// to the census as well, so one defect printed two reds and the
	// census's was a passing count sitting under a failure. Measured
	// while mutating an id away: two rows red, one of them saying
	// nothing true about itself.
	contractSitesVisited = collectedSiteCount(t)
	return contractSitesVisited
}

// collectedSiteCount runs the contract checker's own collection and
// returns how many construction sites it visits, WITHOUT asserting
// anything about them.
//
// It is the checker's enumeration and deliberately so: the census row
// compares it against a census written separately, and the thing being
// compared has to be the number the checker actually uses. What must not
// be shared is the CENSUS, and that is written from the type rather than
// from the checker's rules.
func collectedSiteCount(t *testing.T) int {
	t.Helper()
	// The contract test records the count as a side effect; running it
	// under a throwaway T keeps its assertions off this row's ledger
	// while still producing the number.
	before := contractSitesVisited
	contractSitesVisited = 0
	TestTheFailureContractHolds(&testing.T{})
	got := contractSitesVisited
	if got == 0 {
		contractSitesVisited = before
		t.Fatal("the contract checker visited no site, so the census below would " +
			"compare against nothing")
	}
	return got
}
