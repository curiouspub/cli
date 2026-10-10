package guard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"sort"
	"strings"
	"testing"
)

// A CODE THIS BUILD HAS NO COPY FOR GETS ONE ANSWER, AND IT IS NEVER "TRY
// AGAIN".
//
// The wire contract is additive, so the server is entitled to send a code
// this binary predates, and every handler of a code meets one sooner or
// later. The client already treats it as a stop: it does not retry. Copy
// that then tells the reader to try again in a moment contradicts the
// program's own behaviour, and it said so at six handlers, each written
// separately, one of them in a shape a search for the wording could not
// find.
//
// So the answer has one home, unrecognisedAnswer in internal/flow, and
// these two rules are what keep every handler pointed at it:
//
//   - EVERY SWITCH OVER wire.ErrorCode HAS A default: CLAUSE WHOSE WHOLE
//     BODY RETURNS THE RENDERER'S RESULT. Whatever the enclosing function
//     returns: a handler that builds its message as a string is still a
//     handler. The one exception is keyed below, by file and function, with
//     its reason, and it still needs a default: clause.
//   - EVERY == OR != WITH A wire.ErrorCode OPERAND IS RED, outside the keyed
//     exceptions. An if-chain is a switch without the default this rule can
//     check, so dispatch on a code is written as a switch. Each exception is
//     keyed on the file, the enclosing function and the other operand, so a
//     later comparison in the same function does not inherit approval.
//
// An exception that matches nothing is red, because it is approval nobody
// is using, waiting for something to use it.
//
// THE UNIVERSE IS EVERY PUBLISHED NON-TEST GO FILE, type-checked for every
// platform the release builds, the same set the failure census reads. Test
// files are left out on purpose: a test compares a code to measure what a
// handler did, and renders nothing to anyone. A rule that reddened on
// `apiErr.Code != wire.CodeBadRequest` in a test would forbid the
// measurement.
//
// WHAT THIS CANNOT SEE, stated so nobody reads more into a green run than it
// says. It matches the type of an operand, so a code first converted to a
// plain string, looked up in a map, passed to slices.Contains, or inspected
// by a type switch over some wider value is not seen. The routing tables
// that index by code are exactly such lookups. That is why each handler's
// own rows, beside the handler, drive an unknown code all the way through.
//
// REQUIRED MUTATIONS, each made, run and reverted:
//  1. A new switch over wire.ErrorCode, in a scratch non-test file, whose
//     default: prints its own retry advice. Reds: its default does not
//     return the renderer.
//  2. Delete one handler switch's default: clause. Reds: no default.
//  3. A new function that compares a code with == and falls through to
//     retry copy. Reds: a comparison with no exception.
//  4. Put back the capacity check's old `== wire.CodeMaintenance`. Reds: a
//     comparison with no exception.
//  5. A new switch over wire.ErrorCode in a function returning a string,
//     whose default builds a retry phrase. Reds: the exception is keyed to
//     one function, not to every function that returns a string.
//  6. A new `==` in an excepted function against a different constant from
//     its key. Reds: that comparison has no exception.
//  7. Delete the login's refusal count comparison. Reds: its exception
//     matches nothing.

const (
	wireErrorCodeKey   = modulePath + "/pkg/wire.ErrorCode"
	unrecognisedAnswer = modulePath + "/internal/flow.unrecognisedAnswer"
	errorUnexplained   = modulePath + "/internal/flow.errorUnexplained"
	requestUnserved    = modulePath + "/internal/flow.requestUnserved"
)

// codeSwitchExceptions are the switches over a code whose default: clause
// may return something other than the renderer. Each still needs a default:
// clause, so a code it does not name is answered by a decision rather than
// by whatever follows the switch.
var codeSwitchExceptions = []struct{ file, fn, reason string }{
	{"internal/flow/stream.go", "errorSide",
		"maps a code to the phrase naming which side a stopped build was on; it renders no " +
			"failure, and returns a string, so its default cannot return the renderer. Its " +
			"default is held to a neutral phrase by its own row"},
}

// codeComparisonExceptions are the comparisons with a code that are not
// dispatch on it.
var codeComparisonExceptions = []struct{ file, fn, against, reason string }{
	{"internal/api/errors.go", "(*APIError).Error", `""`,
		"formats a code into the error string only when there is one; renders no failure and dispatches nothing"},
	{"internal/api/errors.go", "decodeAPIError", `""`,
		"detects a body that is not the wire envelope, which carries no code; not dispatch on a code"},
	{"internal/flow/login.go", "trackRefusal", "wire.CodeUnauthorized",
		"counts consecutive refusals of one kind; returns a count and renders nothing"},
	{"internal/flow/stream.go", "errorLine", `""`,
		"an error event with no code renders its message alone; not dispatch on a code"},
}

type codeSite struct {
	where   string // file:line:column
	file    string
	fn      string
	against string // comparisons only
	problem string // switches only; empty when the switch is sound
	// emptyProblem is what is wrong with a switch's answer to the empty
	// code, or nothing. Switches only.
	emptyProblem string
}

func TestEveryCodeDispatchEndsInTheSharedUnknownAnswer(t *testing.T) {
	switches, comparisons := codeDispatchSites(t)

	if len(switches) == 0 {
		t.Fatal("no switch over wire.ErrorCode was found, so the switch rule checked nothing. " +
			"Either the handlers stopped switching on the code, or this scan stopped seeing the type")
	}
	if len(comparisons) == 0 {
		t.Fatal("no comparison with a wire.ErrorCode was found, so the comparison rule checked " +
			"nothing. The excepted comparisons alone should be seen")
	}

	switchMatched := make([]int, len(codeSwitchExceptions))
	for _, s := range switches {
		excepted := -1
		for i, e := range codeSwitchExceptions {
			if s.file == e.file && s.fn == e.fn {
				excepted = i
			}
		}
		if excepted >= 0 {
			switchMatched[excepted]++
			if s.problem == "has no default: clause" {
				t.Errorf("%s: the switch over wire.ErrorCode in %s has no default: clause. It is "+
					"excepted from returning the shared answer, not from having a default: a code "+
					"it does not name must be answered by a decision, not by whatever follows the switch",
					s.where, s.fn)
			}
			continue
		}
		if s.problem != "" {
			t.Errorf("%s: the switch over wire.ErrorCode in %s %s.\n"+
				"Every switch over the code ends in `default: return unrecognisedAnswer(...)`, so a "+
				"code this build has no copy for gets the one answer that never suggests trying again.",
				s.where, s.fn, s.problem)
		}
	}
	for i, e := range codeSwitchExceptions {
		if switchMatched[i] == 0 {
			t.Errorf("the switch exception for %s in %s matches no switch over wire.ErrorCode. "+
				"Approval nobody uses is approval waiting for something to use it: delete the "+
				"entry, or find where the switch went", e.fn, e.file)
		}
	}

	compareMatched := make([]int, len(codeComparisonExceptions))
	for _, c := range comparisons {
		excepted := -1
		for i, e := range codeComparisonExceptions {
			if c.file == e.file && c.fn == e.fn && c.against == e.against {
				excepted = i
			}
		}
		if excepted >= 0 {
			compareMatched[excepted]++
			continue
		}
		t.Errorf("%s: %s compares a wire.ErrorCode with %s outside a switch.\n"+
			"Dispatch on a code is written as a switch, whose default: returns the shared answer; "+
			"an if-chain falls through to whatever copy follows it. A comparison that is not "+
			"dispatch is keyed in codeComparisonExceptions with its reason.",
			c.where, c.fn, c.against)
	}
	for i, e := range codeComparisonExceptions {
		if compareMatched[i] == 0 {
			t.Errorf("the comparison exception for %s in %s against %s matches nothing. "+
				"Delete the entry, or find where the comparison went", e.fn, e.file, e.against)
		}
	}

	if !t.Failed() {
		t.Logf("%d switches and %d comparisons over wire.ErrorCode, every one accounted for",
			len(switches), len(comparisons))
	}
}

// A REFUSAL WITH NO CODE GETS ITS OWN ANSWER AT EVERY HANDLER, AND THE
// STATUS DECIDES WHICH.
//
// A body that is not the wire envelope carries no code. Answered by the
// default: clause, it would be described as a code the server sent and
// would quote the client's own sentence as the server's. So every handler
// switch over wire.ErrorCode, outside the keyed switch exceptions, has a
// `case "":` clause whose every return is one of the two code-less
// answers in internal/flow, and which reaches both: errorUnexplained for a
// status that passes on its own, requestUnserved for one that does not.
//
// WHAT THIS CANNOT SEE: whether the condition choosing between the two is
// the status, and whether the situation each site passes is true. The
// rows beside the handlers drive a code-less answer through each one.
//
// REQUIRED MUTATION, made, run and reverted: delete one handler's
// `case "":` clause. Reds: that switch has no case "": clause.
func TestEveryCodeSwitchAnswersAnAnswerWithNoCode(t *testing.T) {
	switches, _ := codeDispatchSites(t)
	checked := 0
	for _, s := range switches {
		excepted := false
		for _, e := range codeSwitchExceptions {
			excepted = excepted || (s.file == e.file && s.fn == e.fn)
		}
		if excepted {
			continue
		}
		checked++
		if s.emptyProblem != "" {
			t.Errorf("%s: the switch over wire.ErrorCode in %s %s.\n"+
				"A refusal with no code is not a code this build has no copy for: it gets "+
				"`case \"\": if clearsOnItsOwn(apiErr) { return errorUnexplained(...) }; "+
				"return requestUnserved(...)`.", s.where, s.fn, s.emptyProblem)
		}
	}
	if checked == 0 {
		t.Fatal("no handler switch over wire.ErrorCode was checked, so this rule checked nothing")
	}
	if !t.Failed() {
		t.Logf("%d handler switches answer an answer with no code", checked)
	}
}

// codeDispatchSites is every switch whose tag is a wire.ErrorCode and every
// comparison with one, across every platform the release builds. A file
// that builds on several platforms is reported once.
func codeDispatchSites(t *testing.T) (switches, comparisons []codeSite) {
	t.Helper()
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
			t.Fatalf("%s did not parse, so this guard cannot say what is in it: %v",
				displayPath(root, path), err)
		}
		files = append(files, parsedFile{path: path, file: f, src: src})
	}
	if len(files) == 0 {
		t.Fatal("no published Go file was scanned, so this guard measured nothing")
	}
	files = attachFailureTypeInfo(t, root, fset, files)

	seen := map[string]bool{}
	for _, p := range files {
		rel := displayPath(root, p.path)
		ast.Inspect(p.file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SwitchStmt:
				if node.Tag == nil || !isWireErrorCode(p.info.TypeOf(node.Tag)) {
					return true
				}
				pos := fset.Position(node.Pos())
				site := codeSite{
					where: fmt.Sprintf("%s:%d:%d", rel, pos.Line, pos.Column),
					file:  rel, fn: enclosingFuncName(p.file, node.Pos()),
					problem:      defaultProblem(p.info, node),
					emptyProblem: emptyCodeProblem(p.info, node),
				}
				if !seen["switch "+site.where] {
					seen["switch "+site.where] = true
					switches = append(switches, site)
				}
			case *ast.BinaryExpr:
				if node.Op != token.EQL && node.Op != token.NEQ {
					return true
				}
				other := node.Y
				switch {
				case isWireErrorCode(p.info.TypeOf(node.X)):
				case isWireErrorCode(p.info.TypeOf(node.Y)):
					other = node.X
				default:
					return true
				}
				pos := fset.Position(node.Pos())
				site := codeSite{
					where: fmt.Sprintf("%s:%d:%d", rel, pos.Line, pos.Column),
					file:  rel, fn: enclosingFuncName(p.file, node.Pos()),
					against: comparedWith(p.info, other),
				}
				if !seen["compare "+site.where] {
					seen["compare "+site.where] = true
					comparisons = append(comparisons, site)
				}
			}
			return true
		})
	}
	sort.Slice(switches, func(i, j int) bool { return switches[i].where < switches[j].where })
	sort.Slice(comparisons, func(i, j int) bool { return comparisons[i].where < comparisons[j].where })
	return switches, comparisons
}

func isWireErrorCode(typ types.Type) bool {
	if typ == nil {
		return false
	}
	named, ok := types.Unalias(typ).(*types.Named)
	return ok && objectKey(named.Obj()) == wireErrorCodeKey
}

// defaultProblem says what is wrong with a switch's default: clause, or
// nothing. The clause's whole body must be one return whose last result is
// a direct call to the shared answer: a clause that prints its own advice
// first and then returns the answer has still printed its own advice.
func defaultProblem(info *types.Info, sw *ast.SwitchStmt) string {
	var def *ast.CaseClause
	for _, stmt := range sw.Body.List {
		if cc, ok := stmt.(*ast.CaseClause); ok && cc.List == nil {
			def = cc
		}
	}
	if def == nil {
		return "has no default: clause"
	}
	if len(def.Body) != 1 {
		return fmt.Sprintf("has a default: clause of %d statements, not one return", len(def.Body))
	}
	ret, ok := def.Body[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) == 0 {
		return "has a default: clause that does not return"
	}
	call, ok := unparen(ret.Results[len(ret.Results)-1]).(*ast.CallExpr)
	if !ok || calledObject(info, call.Fun) != unrecognisedAnswer {
		return "has a default: clause that does not return unrecognisedAnswer's result"
	}
	return ""
}

// emptyCodeProblem says what is wrong with a switch's answer to the empty
// code, or nothing. The clause names the empty code alone, and every return
// in it is a direct call to one of the two code-less answers, both of which
// it reaches: the status decides between them, so a clause that can reach
// only one has dropped the decision.
func emptyCodeProblem(info *types.Info, sw *ast.SwitchStmt) string {
	var clause *ast.CaseClause
	for _, stmt := range sw.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, e := range cc.List {
			if lit, ok := unparen(e).(*ast.BasicLit); ok && lit.Kind == token.STRING &&
				(lit.Value == `""` || lit.Value == "``") {
				clause = cc
			}
		}
	}
	if clause == nil {
		return `has no case "": clause`
	}
	if len(clause.List) != 1 {
		return `answers the empty code in a clause shared with other codes`
	}
	called := map[string]bool{}
	problem := ""
	ast.Inspect(&ast.BlockStmt{List: clause.Body}, func(n ast.Node) bool {
		if _, nested := n.(*ast.FuncLit); nested {
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		callee := ""
		if len(ret.Results) > 0 {
			if call, ok := unparen(ret.Results[len(ret.Results)-1]).(*ast.CallExpr); ok {
				callee = calledObject(info, call.Fun)
			}
		}
		if callee != errorUnexplained && callee != requestUnserved {
			problem = `has a case "": clause with a return that is neither code-less answer`
		}
		called[callee] = true
		return true
	})
	if problem != "" {
		return problem
	}
	if !called[errorUnexplained] || !called[requestUnserved] {
		return `has a case "": clause that does not reach both code-less answers`
	}
	return ""
}

func calledObject(info *types.Info, fun ast.Expr) string {
	var obj types.Object
	switch f := unparen(fun).(type) {
	case *ast.Ident:
		obj = info.Uses[f]
	case *ast.SelectorExpr:
		obj = info.Uses[f.Sel]
	}
	if fn, ok := obj.(*types.Func); ok {
		return objectKey(fn)
	}
	return ""
}

// comparedWith names the other operand of a comparison: a named constant by
// its package and name, a literal by its spelling, anything else by its
// expression.
func comparedWith(info *types.Info, e ast.Expr) string {
	e = unparen(e)
	var ident *ast.Ident
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Value
	case *ast.Ident:
		ident = v
	case *ast.SelectorExpr:
		ident = v.Sel
	}
	if ident != nil {
		if c, ok := info.Uses[ident].(*types.Const); ok && c.Pkg() != nil {
			return c.Pkg().Name() + "." + c.Name()
		}
	}
	return types.ExprString(e)
}

// enclosingFuncName is the declared function a position is inside, with a
// method's receiver written the way a reader would write it.
func enclosingFuncName(f *ast.File, pos token.Pos) string {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || pos < fd.Pos() || pos > fd.End() {
			continue
		}
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			return "(" + types.ExprString(fd.Recv.List[0].Type) + ")." + fd.Name.Name
		}
		return fd.Name.Name
	}
	return "<file scope>"
}
