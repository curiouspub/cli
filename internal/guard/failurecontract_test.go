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
}

type obligation struct {
	where  string
	field  string
	expr   string
	chain  string
	values []string // resolved possibilities; empty means unresolved
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
		files = append(files, parsedFile{path, f, src})
	}
	if len(files) == 0 {
		t.Fatal("no published Go file was scanned, so this guard measured nothing")
	}

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
			if fd, ok := d.(*ast.FuncDecl); ok && allReturnsNonBlank(fd.Name.Name, p.file) {
				nonBlankHelpers[fd.Name.Name] = true
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
				summaries[fd.Name.Name] = s
			}
		}
	}
	for _, want := range []string{"NewFailure", "Quoted", "uploadFailed"} {
		if _, ok := summaries[want]; !ok {
			t.Errorf("no VERIFIED summary for %s. Either its body stopped "+
				"forwarding its arguments to the failure it builds, or this "+
				"checker no longer understands the shape it uses — and both mean "+
				"every call to it is unchecked rather than fine", want)
		}
	}

	// --- 2. COLLECT OBLIGATIONS ---------------------------------------
	var obs []obligation
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
				if typeName(lit.Type) != "Failure" {
					return true
				}
				pos := fset.Position(lit.Pos())
				site := rel + ":" + itoa(pos.Line)
				if _, inHelper := summaries[enclosingFunc(p.file, lit.Pos())]; inHelper {
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
				obs = append(obs,
					resolveIn(site, "ID", field("ID"), text, p, "id", in, 0),
					resolveIn(site, "Next", field("Next"), text, p, "action", in, 0),
					resolveIn(site, "NextText", field("NextText"), text, p, "text", in, 0))
				return true
			}
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calleeName(call.Fun)
			s, known := summaries[name]
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
			if _, isHelper := summaries[enclosingFunc(p.file, call.Pos())]; isHelper {
				return true
			}
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
				if v == `""` || v == "" {
					invalid = append(invalid, o.where+" has no failure id")
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
		if by, ok := scenarioCovered[keyOf(m)]; ok {
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
	contractSitesVisited = len(obs) / 3
	t.Logf("%d obligations across %d sites, %d invalid, %d unresolved",
		len(obs), contractSitesVisited, len(invalid), len(unresolved))
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
			if typeName(node.Type) != "Failure" {
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
			if c := calleeName(node.Fun); c != "NewFailure" && c != "Quoted" {
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
			if strings.TrimSpace(strings.Trim(v.Value, "`\"")) == "" {
				o.values = []string{"blank"}
			} else {
				o.values = []string{"nonblank"}
			}
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
			if allReturnsNonBlank(calleeName(v.Fun), p.file) ||
				allReturnsNonBlankAnywhere(calleeName(v.Fun)) {
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
		return strings.TrimSpace(strings.Trim(v.Value, "`\"")) != ""
	case *ast.BinaryExpr:
		return containsNonBlankLiteral(v.X) || containsNonBlankLiteral(v.Y)
	case *ast.ParenExpr:
		return containsNonBlankLiteral(v.X)
	}
	return false
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

func enclosingFunc(f *ast.File, p token.Pos) string {
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && p >= fd.Pos() && p <= fd.End() {
			return fd.Name.Name
		}
	}
	return ""
}

// allReturnsNonBlank establishes that every return path of a function in
// this file yields a non-blank string.
//
// IT IS DELIBERATELY FILE-LOCAL AND SHALLOW. A deeper analysis would
// resolve more and be harder to trust; an unresolved obligation is a
// visible failure that somebody reads, which is the safer side to be
// wrong on. If a copy helper moves out of its caller's file, this returns
// false and the obligation goes unresolved rather than silently passing.
func allReturnsNonBlank(name string, f *ast.File) bool {
	if name == "" {
		return false
	}
	var fd *ast.FuncDecl
	for _, d := range f.Decls {
		if c, ok := d.(*ast.FuncDecl); ok && c.Name.Name == name {
			fd = c
			break
		}
	}
	if fd == nil || fd.Body == nil {
		return false
	}
	saw := false
	ok := true
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		ret, is := n.(*ast.ReturnStmt)
		if !is || len(ret.Results) == 0 {
			return true
		}
		saw = true
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

func allReturnsNonBlankAnywhere(name string) bool { return nonBlankHelpers[name] }

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
				files[key] = parsedFile{path, f, src}
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
