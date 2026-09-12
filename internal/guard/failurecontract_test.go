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
	binding string   // reaching definitions for a scenario-covered expression
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
	files = attachFailureTypeInfo(t, root, fset, files)

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
				key := targetFuncKey(p, packageFuncKey(root, p.path, fd.Name.Name))
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
				summaries[targetFuncKey(p, declaredFuncKey(fd, p.info))] = s
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
			if vs, ok := n.(*ast.ValueSpec); ok {
				zeros := zeroFailureDeclarations(p.info, vs)
				if zeros > 0 {
					pos := fset.Position(vs.Pos())
					site := rel + ":" + itoa(pos.Line)
					for range zeros {
						sitesVisited++
						obs = append(obs,
							obligation{where: site, field: "ID", expr: "zero ui.Failure declaration"},
							obligation{where: site, field: "Next", expr: "zero ui.Failure declaration"},
							obligation{where: site, field: "NextText", expr: "zero ui.Failure declaration"})
					}
				}
				return true
			}
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
				if _, inHelper := summaries[targetFuncKey(p, enclosingFuncKey(p, lit.Pos()))]; inHelper {
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
					sel, ok := unparen(lhs).(*ast.SelectorExpr)
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
				if sel, ok := unparen(unary.X).(*ast.SelectorExpr); ok && isProtectedFailureField(p.info, sel) {
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
			key := targetFuncKey(p, calledFuncKey(call.Fun, enclosingDecl(p.file, call.Pos()), p.info))
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
			if _, isHelper := summaries[targetFuncKey(p, enclosingFuncKey(p, call.Pos()))]; isHelper {
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
	unresolvedDetails := map[string]obligation{}
	for _, o := range obs {
		switch {
		case o.problem != "":
			invalid = append(invalid, o.where+" "+o.field+" "+o.problem+": "+o.expr)
		case len(o.values) == 0:
			message := o.where + " " + o.field + " = " + o.expr + " (unsupported form)"
			unresolved = append(unresolved, message)
			unresolvedDetails[message] = o
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
		if by, ok := scenarioCoverage(m, unresolvedDetails[m].binding); ok {
			t.Logf("unresolved statically, covered by scenario: %s (%s)", m, by)
			continue
		}
		t.Errorf("UNRESOLVED: %s [binding %q]\nAn obligation this checker cannot establish is "+
			"not an obligation that is satisfied. Use a supported form, extend the "+
			"checker deliberately, or name the scenario test that covers it.", m, unresolvedDetails[m].binding)
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

func targetFuncKey(p parsedFile, key string) string {
	if key == "" || p.target == "" {
		return key
	}
	return p.target + "|" + key
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
	var candidates []summary
	mutated := false

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CompositeLit:
			if !isFailureExpr(p.info, node) {
				return true
			}
			s := summary{fn: fd.Name.Name, idArg: -1, act: -1, txt: -1}
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
					s.idArg = i
				case "Next":
					s.act = i
				case "NextText":
					s.txt = i
				}
			}
			candidates = append(candidates, s)
		case *ast.CallExpr:
			// A FORWARDING HELPER: its own parameters passed straight to
			// a constructor. uploadFailed is the one in this tree.
			c := calledFuncKey(node.Fun, fd, p.info)
			if c != modulePath+"/internal/ui.NewFailure" &&
				c != modulePath+"/internal/ui.Quoted" {
				return true
			}
			s := summary{fn: fd.Name.Name, idArg: -1, act: -1, txt: -1}
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
					s.idArg = j
				case 3:
					s.act = j
				case 4:
					s.txt = j
				}
			}
			candidates = append(candidates, s)
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					_, mutatedParam := params[id.Name]
					mutated = mutated || mutatedParam
				}
			}
		}
		return true
	})
	if mutated || len(candidates) == 0 {
		return summary{}, false
	}
	want := candidates[0]
	if want.idArg < 0 || want.act < 0 || want.txt < 0 {
		return summary{}, false
	}
	for _, got := range candidates[1:] {
		if got.idArg != want.idArg || got.act != want.act || got.txt != want.txt {
			return summary{}, false
		}
	}
	return want, true
}

type parsedFile = struct {
	path   string
	file   *ast.File
	src    []byte
	info   *types.Info
	target string
}

// attachFailureTypeInfo type-checks repository packages from their source.
// The collector then asks about the object or type bound to an AST node,
// never the unqualified spelling the author chose for it.
func attachFailureTypeInfo(t *testing.T, root string, fset *token.FileSet, files []parsedFile) []parsedFile {
	t.Helper()
	type target struct{ goos, goarch string }
	var checked []parsedFile
	for _, target := range []target{
		{"darwin", "amd64"}, {"darwin", "arm64"},
		{"linux", "amd64"}, {"linux", "arm64"},
		{"windows", "amd64"}, {"windows", "arm64"},
	} {
		ctx := build.Default
		ctx.GOOS, ctx.GOARCH = target.goos, target.goarch
		if target.goos != "windows" {
			ctx.BuildTags = append(ctx.BuildTags, "unix")
		}
		var targetFiles []parsedFile
		for _, original := range files {
			matched, err := ctx.MatchFile(filepath.Dir(original.path), filepath.Base(original.path))
			if err != nil {
				t.Fatalf("reading build constraints in %s: %v", original.path, err)
			}
			if !matched {
				continue
			}
			f, err := parser.ParseFile(fset, original.path, original.src, 0)
			if err != nil {
				t.Fatalf("parsing %s for %s/%s: %v", original.path, target.goos, target.goarch, err)
			}
			clone := parsedFile{path: original.path, file: f, src: original.src,
				target: target.goos + "/" + target.goarch}
			targetFiles = append(targetFiles, clone)
		}
		selected := map[string][]*parsedFile{}
		for i := range targetFiles {
			p := &targetFiles[i]
			rel, err := filepath.Rel(root, filepath.Dir(p.path))
			if err != nil {
				t.Fatalf("finding package for %s: %v", p.path, err)
			}
			path := modulePath
			if rel != "." {
				path += "/" + filepath.ToSlash(rel)
			}
			selected[path] = append(selected[path], p)
		}
		l := &failureSourceImporter{
			fset: fset, sources: selected, packages: map[string]*types.Package{},
			loading: map[string]bool{}, fallback: archiveImporter(fset, root, target.goos, target.goarch),
		}
		for path := range selected {
			if _, err := l.Import(path); err != nil {
				t.Fatalf("type-checking %s for %s/%s failure identities: %v", path, target.goos, target.goarch, err)
			}
		}
		checked = append(checked, targetFiles...)
	}
	return checked
}

func archiveImporter(fset *token.FileSet, root, goos, goarch string) types.Importer {
	return importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		cmd := exec.Command("go", "list", "-export", "-f", "{{.Export}}", path)
		cmd.Dir = root
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GOOS=") && !strings.HasPrefix(entry, "GOARCH=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, "GOOS="+goos, "GOARCH="+goarch)
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
	return calledFuncKeySeen(fun, within, info, map[types.Object]bool{}, 0)
}

func calledFuncKeySeen(fun ast.Expr, within *ast.FuncDecl, info *types.Info,
	seen map[types.Object]bool, depth int) string {
	if info == nil || fun == nil {
		return ""
	}
	if depth > 8 {
		return ""
	}
	switch f := fun.(type) {
	case *ast.Ident:
		if fn, ok := info.Uses[f].(*types.Func); ok {
			return objectKey(fn)
		}
		obj := info.Uses[f]
		if obj == nil {
			obj = info.Defs[f]
		}
		if obj != nil && seen[obj] {
			return ""
		}
		if obj != nil {
			seen[obj] = true
			defer delete(seen, obj)
		}
		if within != nil {
			if def, ok := soleDefinition(f.Name, within); ok {
				return calledFuncKeySeen(def, within, info, seen, depth+1)
			}
		}
	case *ast.SelectorExpr:
		if fn, ok := info.Uses[f.Sel].(*types.Func); ok {
			return objectKey(fn)
		}
	case *ast.ParenExpr:
		return calledFuncKeySeen(f.X, within, info, seen, depth+1)
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
		selection := info.Selections[sel]
		if selection == nil {
			return false
		}
		typ := selection.Recv()
		for step, index := range selection.Index() {
			if ptr, ok := types.Unalias(typ).(*types.Pointer); ok {
				typ = ptr.Elem()
			}
			named, namedOK := types.Unalias(typ).(*types.Named)
			underlying := typ
			if namedOK {
				underlying = named.Underlying()
			}
			st, ok := underlying.(*types.Struct)
			if !ok || index >= st.NumFields() {
				return false
			}
			field := st.Field(index)
			if step == len(selection.Index())-1 {
				return namedOK && isFailureValueType(named) && field.Name() == sel.Sel.Name
			}
			typ = field.Type()
		}
	}
	return false
}

func unparen(e ast.Expr) ast.Expr {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			return e
		}
		e = p.X
	}
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

func zeroFailureDeclarations(info *types.Info, vs *ast.ValueSpec) int {
	if info == nil || vs == nil || len(vs.Values) != 0 ||
		!isFailureValueType(info.TypeOf(vs.Type)) {
		return 0
	}
	return len(vs.Names)
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
		const NextWait NextAction = "Wait"
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
			var makeFailure = ui.NewFailure
			_ = makeFailure("id", "next", "text")
			var zero ui.Failure
			_ = &zero
		}`)
	literals, calls, declarations := 0, 0, 0
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
		case *ast.ValueSpec:
			declarations += zeroFailureDeclarations(p.info, n)
		}
		return true
	})
	if literals != 5 || calls != 1 || declarations != 1 {
		t.Fatalf("recognised %d failure literals, %d aliased constructor calls, and %d zero declarations; want 5, 1, 1",
			literals, calls, declarations)
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
			(f.Next) = ""
			p := &(f.ID)
			*p = ""
			w := struct{ *ui.Failure }{&f}
			w.Next = ""
			f.NextText = "Retry."
			f.NextText = "\t"
		}`)
	forbidden, checkedText := 0, 0
	blankText := false
	ast.Inspect(p.file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if sel, ok := unparen(lhs).(*ast.SelectorExpr); ok && isProtectedFailureField(p.info, sel) {
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
				if sel, ok := unparen(n.X).(*ast.SelectorExpr); ok && isProtectedFailureField(p.info, sel) {
					forbidden++
				}
			}
		}
		return true
	})
	if forbidden != 6 || checkedText != 2 || !blankText {
		t.Fatalf("found %d forbidden writes/addresses and %d checked NextText writes (blank found %t), want 6, 2, true",
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
	f, err = parser.ParseFile(fset, "deferred.go", `package fixture
		func advice() (s string) { defer func() { s = "" }(); return "Retry." }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if allReturnsNonBlankDecl(f.Decls[0].(*ast.FuncDecl)) {
		t.Fatal("a deferred mutation of a named result was certified non-blank")
	}
	nonBlankHelpers = map[string]bool{"one.advice": false, "two.advice": true}
	if allReturnsNonBlankAnywhere("one.advice") || !allReturnsNonBlankAnywhere("two.advice") {
		t.Fatal("same-named helpers in different packages contaminated one another")
	}
}

func TestSummaryRequiresEveryConstructionAndUnmutatedParameters(t *testing.T) {
	for _, body := range []string{
		`next = ""; return &ui.Failure{ID: id, Next: next, NextText: text}`,
		`if bad { return &ui.Failure{} }; return &ui.Failure{ID: id, Next: next, NextText: text}`,
	} {
		p := typedFailureFixture(t, `package fixture
			import ui "github.com/curiouspub/cli/internal/ui"
			func build(id ui.FailureID, next ui.NextAction, text string, bad bool) *ui.Failure {
				`+body+`
			}`)
		fd := p.file.Decls[1].(*ast.FuncDecl)
		if _, ok := verifySummary(fd, func(parsedFile, ast.Node) string { return "" }, p); ok {
			t.Fatalf("unsafe helper was summarised: %s", body)
		}
	}
}

func TestResolutionDoesNotInventValuePreservationOrTrustNames(t *testing.T) {
	p := typedFailureFixture(t, `package fixture
		import ui "github.com/curiouspub/cli/internal/ui"
		const NextWait = ui.NextWait
		func erase(ui.NextAction) ui.NextAction { return "" }
		func use() {
			_ = erase(ui.NextWait)
			NextWait := ui.NextAction("")
			_ = NextWait
			_ = ui.NextAction("NextWait")
		}`)
	fd := p.file.Decls[3].(*ast.FuncDecl)
	var expressions []ast.Expr
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Rhs) == 1 &&
			len(as.Lhs) == 1 && calleeName(as.Lhs[0]) == "_" {
			expressions = append(expressions, as.Rhs[0])
		}
		return true
	})
	if len(expressions) != 3 {
		t.Fatalf("found %d fixture expressions, want 3", len(expressions))
	}
	text := func(parsedFile, ast.Node) string { return "fixture" }
	for i, e := range expressions {
		o := resolveIn("fixture", "Next", e, text, p, "action", fd, 0)
		if len(o.values) > 0 && validActions[o.values[0]] {
			t.Errorf("unsafe expression %d resolved to a valid action: %v", i, o.values)
		}
	}
}

func TestFunctionAliasCyclesFailClosed(t *testing.T) {
	p := typedFailureFixture(t, `package fixture
		import ui "github.com/curiouspub/cli/internal/ui"
		func use() {
			var a, b = ui.NewFailure, ui.NewFailure
			a = b
			b = a
			_ = a("id", "next", "text")
		}`)
	fd := p.file.Decls[1].(*ast.FuncDecl)
	var call *ast.CallExpr
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && calleeName(c.Fun) == "a" {
			call = c
		}
		return true
	})
	if call == nil {
		t.Fatal("fixture call not found")
	}
	if got := calledFuncKey(call.Fun, fd, p.info); got != "" {
		t.Fatalf("cyclic alias resolved as %q, want unresolved", got)
	}
}

func TestBuildTargetsKeepTheirOwnTypeInfoAndIncludeArm64(t *testing.T) {
	root := t.TempDir()
	sources := map[string]string{
		"action_windows.go": "//go:build windows\npackage fixture\nconst action = \"Wait\"\n",
		"action_other.go":   "//go:build !windows\npackage fixture\nconst action = \"\"\n",
		"arch_arm64.go":     "//go:build arm64\npackage fixture\nconst arm64Present = true\n",
		"common.go":         "package fixture\nvar observed = action\n",
	}
	fset := token.NewFileSet()
	var files []parsedFile
	for name, source := range sources {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, path, source, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, parsedFile{path: path, file: f, src: []byte(source)})
	}
	checked := attachFailureTypeInfo(t, root, fset, files)
	seenCommon, seenArm64 := map[string]string{}, map[string]bool{}
	helperKeys := map[string]bool{}
	for _, p := range checked {
		switch filepath.Base(p.path) {
		case "common.go":
			helperKeys[targetFuncKey(p, "fixture.helper")] = true
			ast.Inspect(p.file, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok || id.Name != "action" {
					return true
				}
				if c, ok := p.info.Uses[id].(*types.Const); ok {
					seenCommon[p.target] = constant.StringVal(c.Val())
				}
				return true
			})
		case "arch_arm64.go":
			seenArm64[p.target] = true
		}
	}
	if len(seenCommon) != 6 {
		t.Fatalf("common file was checked in %d targets, want 6: %v", len(seenCommon), seenCommon)
	}
	if len(helperKeys) != 6 {
		t.Fatalf("helper summaries collapse to %d target keys, want 6: %v", len(helperKeys), helperKeys)
	}
	for target, value := range seenCommon {
		want := ""
		if strings.HasPrefix(target, "windows/") {
			want = "Wait"
		}
		if value != want {
			t.Errorf("%s reused another target's type info: action = %q, want %q", target, value, want)
		}
	}
	for _, target := range []string{"darwin/arm64", "linux/arm64", "windows/arm64"} {
		if !seenArm64[target] {
			t.Errorf("arm64-only file was not checked for %s", target)
		}
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

	if p.info != nil {
		if value, ok := typedStringConstant(p.info, e); ok {
			if kind == "text" {
				if strings.TrimSpace(value) == "" {
					o.values = []string{"blank"}
				} else {
					o.values = []string{"nonblank"}
				}
			} else {
				o.values = []string{value}
			}
			return o
		}
	}
	switch v := e.(type) {
	case *ast.SelectorExpr: // ui.NextWait, ui.IDUploadStalled
		// Only typed constants resolve above. Spelling is not evidence: a
		// field or local may have the same name as a catalog constant.
	case *ast.Ident: // NextWait inside package ui, or a name to trace
		// A LOCAL NAME resolves to its reaching definition, when there is
		// exactly one. More than one assignment and it stays unresolved:
		// this checker unions branches it can see and refuses what it
		// cannot.
		if within != nil {
			if def, ok := soleDefinitionAt(v.Name, within, v.Pos()); ok {
				return resolveIn(site, field, def, text, p, kind, within, depth+1)
			}
			o.binding = definitionFingerprint(v.Name, within, v.Pos(), text, p)
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
		if len(v.Args) == 1 && p.info != nil && p.info.Types[v.Fun].IsType() {
			if inner := resolveIn(site, field, v.Args[0], text, p, kind, within, depth+1); len(inner.values) > 0 {
				o.values = inner.values
			}
		}
		// A COPY HELPER WHOSE EVERY RETURN IS NON-BLANK establishes
		// non-blank for its caller. This is the bounded string analysis
		// the contract asks for, and it is why retryAdvice does not have
		// to be special-cased: its returns are concatenations containing
		// authored literals, so the property holds by reading them.
		if kind == "text" && o.values == nil {
			if allReturnsNonBlankAnywhere(targetFuncKey(p, calledFuncKey(v.Fun, within, p.info))) {
				o.values = []string{"nonblank"}
			}
		}
	}
	return o
}

func typedStringConstant(info *types.Info, e ast.Expr) (string, bool) {
	if info == nil || e == nil {
		return "", false
	}
	if tv, ok := info.Types[e]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return constant.StringVal(tv.Value), true
	}
	var obj types.Object
	switch v := unparen(e).(type) {
	case *ast.Ident:
		obj = info.Uses[v]
		if obj == nil {
			obj = info.Defs[v]
		}
	case *ast.SelectorExpr:
		obj = info.Uses[v.Sel]
	}
	c, ok := obj.(*types.Const)
	if !ok || c.Val().Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(c.Val()), true
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
		if _, deferred := n.(*ast.DeferStmt); deferred {
			ok = false
			return true
		}
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

// soleDefinition returns the expression a local name is assigned, but
// ONLY when it is assigned exactly once.
//
// TWO ASSIGNMENTS MEAN UNRESOLVED, not "pick one". A name written twice
// carries two possible values and this checker unions what it can see
// rather than guessing which reached the call.
func soleDefinition(name string, fd *ast.FuncDecl) (ast.Expr, bool) {
	return soleDefinitionAt(name, fd, fd.End())
}

func soleDefinitionAt(name string, fd *ast.FuncDecl, before token.Pos) (ast.Expr, bool) {
	var found ast.Expr
	count := 0
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n != nil && n.Pos() >= before {
			return false
		}
		if decl, ok := n.(*ast.DeclStmt); ok {
			gen, ok := decl.Decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				return true
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, id := range vs.Names {
					if id.Name != name {
						continue
					}
					count++
					if len(vs.Values) == len(vs.Names) {
						found = vs.Values[i]
					} else if len(vs.Values) == 1 && len(vs.Names) == 1 {
						found = vs.Values[0]
					} else {
						found = nil
					}
				}
			}
			return true
		}
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

func definitionFingerprint(name string, fd *ast.FuncDecl, before token.Pos,
	text func(parsedFile, ast.Node) string, p parsedFile) string {
	var definitions []string
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n != nil && n.Pos() >= before {
			return false
		}
		switch node := n.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && id.Name == name {
					rhs := "<multi-value>"
					if len(node.Rhs) == len(node.Lhs) {
						rhs = text(p, node.Rhs[i])
					} else if len(node.Rhs) == 1 {
						rhs = text(p, node.Rhs[0]) + "#" + itoa(i)
					}
					definitions = append(definitions, rhs)
				}
			}
		case *ast.DeclStmt:
			gen, ok := node.Decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				return true
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, id := range vs.Names {
					if id.Name == name {
						rhs := "<zero>"
						if len(vs.Values) == len(vs.Names) {
							rhs = text(p, vs.Values[i])
						}
						definitions = append(definitions, rhs)
					}
				}
			}
		}
		return true
	})
	return strings.Join(definitions, " | ")
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

var scenarioCoveredBinding = map[string]string{
	"internal/flow/preflight.go:223 ID":       "",
	"internal/flow/preflight.go:223 NextText": "f.Next | standingAction",
	"internal/flow/upload.go:340 NextText":    "expiredCopy()#1",
	"internal/flow/upload.go:372 ID":          "refusalCopy(host, deps.ExpiresAt, deps.Now())#0",
	"internal/flow/upload.go:372 Next":        "refusalCopy(host, deps.ExpiresAt, deps.Now())#1",
	"internal/flow/upload.go:372 NextText":    "expiredCopy()#1 | refusalCopy(host, deps.ExpiresAt, deps.Now())#3",
}

func scenarioCoverage(message, binding string) (string, bool) {
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
	wantBinding, bound := scenarioCoveredBinding[key]
	return by, ok && got == want && bound && binding == wantBinding
}

func TestAScenarioWaiverBelongsToItsExpression(t *testing.T) {
	key := "internal/flow/upload.go:372 Next"
	if _, ok := scenarioCoverage(key+" = changedAtRuntime() (unsupported form)",
		"refusalCopy(host, deps.ExpiresAt, deps.Now())#1"); ok {
		t.Fatal("a different expression inherited the scenario waiver at the same location")
	}
	wantBinding := "refusalCopy(host, deps.ExpiresAt, deps.Now())#1"
	if by, ok := scenarioCoverage(key+" = action (unsupported form)", wantBinding); !ok || by == "" {
		t.Fatal("the expression the scenario actually covers lost its waiver")
	}
	if _, ok := scenarioCoverage(key+" = action (unsupported form)", wantBinding+" | \"\""); ok {
		t.Fatal("an expression with an additional reaching definition inherited the waiver")
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
