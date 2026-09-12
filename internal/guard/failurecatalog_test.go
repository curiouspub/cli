package guard

import (
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
)

func TestActiveFailureIDsAreUniqueAndReachable(t *testing.T) {
	root := moduleRoot(t)
	active := make([]string, 0, len(ui.ActiveFailureIDs))
	for _, id := range ui.ActiveFailureIDs {
		active = append(active, string(id))
	}

	fset := token.NewFileSet()
	var files []parsedFile
	for _, path := range publishedTextFiles(t, root) {
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, parsedFile{path: path, file: f, src: src})
	}
	files = attachFailureTypeInfo(t, root, fset, files)

	used := map[string]bool{}
	var unsafeExpressions []string
	for _, p := range files {
		if p.info == nil || filepath.Clean(p.path) == filepath.Join(root, "internal", "ui", "failureid.go") {
			continue
		}
		for id := range failureIDsUsedInFile(p) {
			used[id] = true
		}
		ast.Inspect(p.file, func(n ast.Node) bool {
			if kv, ok := n.(*ast.KeyValueExpr); ok && calleeName(kv.Key) == "FailureID" {
				if !catalogFailureIDExpression(kv.Value, enclosingDecl(p.file, kv.Pos()), p.info) {
					unsafeExpressions = append(unsafeExpressions,
						displayPath(root, p.path)+":"+itoa(fset.Position(kv.Pos()).Line))
				}
			}
			return true
		})
	}
	for _, where := range unsafeExpressions {
		t.Errorf("production FailureID at %s is not a constant or an exact value-preserving parameter conversion", where)
	}
	for _, problem := range catalogProblems(active, used) {
		t.Error(problem)
	}
}

func failureIDsUsedInFile(p parsedFile) map[string]bool {
	used := map[string]bool{}
	if p.info == nil || p.file == nil {
		return used
	}
	ast.Inspect(p.file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		c, ok := p.info.Uses[id].(*types.Const)
		if !ok || c.Val().Kind() != constant.String {
			return true
		}
		typeKey := namedTypeKey(c.Type())
		if typeKey == modulePath+"/internal/ui.FailureID" ||
			typeKey == modulePath+"/internal/check.FailureFamily" {
			used[constant.StringVal(c.Val())] = true
		}
		return true
	})
	return used
}

func catalogFailureIDExpression(e ast.Expr, within *ast.FuncDecl, info *types.Info) bool {
	if e == nil || info == nil {
		return false
	}
	if tv, ok := info.Types[e]; ok && tv.Value != nil && tv.Value.Kind() == constant.String {
		return true
	}
	call, ok := unparen(e).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || !info.Types[call.Fun].IsType() || within == nil {
		return false
	}
	id, ok := unparen(call.Args[0]).(*ast.Ident)
	if !ok {
		return false
	}
	obj := info.Uses[id]
	if obj == nil {
		obj = info.Defs[id]
	}
	for _, field := range within.Type.Params.List {
		for _, name := range field.Names {
			if info.Defs[name] == obj {
				return true
			}
		}
	}
	return false
}

func namedTypeKey(typ types.Type) string {
	named, ok := types.Unalias(typ).(*types.Named)
	if !ok {
		return ""
	}
	return objectKey(named.Obj())
}

func catalogProblems(active []string, used map[string]bool) []string {
	counts := map[string]int{}
	for _, id := range active {
		counts[id]++
	}
	var problems []string
	for id, count := range counts {
		if id == "" {
			problems = append(problems, "ActiveFailureIDs contains an empty id")
		}
		if count > 1 {
			problems = append(problems, "ActiveFailureIDs contains duplicate "+id)
		}
		if !used[id] {
			problems = append(problems, "ActiveFailureIDs contains orphan "+id)
		}
	}
	for id := range used {
		if counts[id] == 0 {
			problems = append(problems, "production uses id absent from ActiveFailureIDs: "+id)
		}
	}
	return problems
}

func TestCatalogSetValidationRejectsMissingDuplicatesAndOrphans(t *testing.T) {
	if activeFailureID("not-in-the-catalog") {
		t.Fatal("a non-empty id outside ActiveFailureIDs passed membership")
	}
	if !activeFailureID(string(ui.IDInternalFault)) {
		t.Fatal("a declared active id failed membership")
	}
	problems := catalogProblems([]string{"used", "used", "orphan"}, map[string]bool{
		"used": true, "unknown": true,
	})
	joined := strings.Join(problems, "\n")
	for _, want := range []string{"duplicate used", "orphan orphan", "absent from ActiveFailureIDs: unknown"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q from catalog failures:\n%s", want, joined)
		}
	}
}

func TestCatalogUsageIsFileScopedAndRejectsComputedFamilyIDs(t *testing.T) {
	fset := token.NewFileSet()
	catalogSource := `package check
		type FailureFamily string
		const IDUnused FailureFamily = "unused"
		var Active = []FailureFamily{IDUnused}`
	productionSource := `package check
		func hardStop(family FailureFamily) struct{ FailureID string } {
			return struct{ FailureID string }{FailureID: string(family) + "-typo"}
		}`
	catalog, err := parser.ParseFile(fset, "failureid.go", catalogSource, 0)
	if err != nil {
		t.Fatal(err)
	}
	production, err := parser.ParseFile(fset, "production.go", productionSource, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	if _, err := (&types.Config{}).Check(modulePath+"/internal/check", fset, []*ast.File{catalog, production}, info); err != nil {
		t.Fatal(err)
	}
	used := failureIDsUsedInFile(parsedFile{file: production, info: info})
	if used["unused"] {
		t.Fatal("the catalog declaration certified its own production reachability")
	}
	fd := production.Decls[0].(*ast.FuncDecl)
	var value ast.Expr
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if kv, ok := n.(*ast.KeyValueExpr); ok && calleeName(kv.Key) == "FailureID" {
			value = kv.Value
		}
		return true
	})
	if catalogFailureIDExpression(value, fd, info) {
		t.Fatal("a computed family id was accepted as an exact catalog value")
	}
}
