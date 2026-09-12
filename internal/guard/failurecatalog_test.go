package guard

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
)

type publicCatalogEntry struct {
	Action []string `json:"action"`
	Family *string  `json:"family"`
	ID     string   `json:"id"`
	Stages []string `json:"stages"`
}

// TestCatalogJSONMatchesRegeneration is both the drift row and the target of
// failureid.go's go:generate directive. The directive sets
// WRITE_FAILURE_CATALOG; an ordinary test regenerates in memory and compares
// the committed public artefact byte for byte.
func TestCatalogJSONMatchesRegeneration(t *testing.T) {
	root := moduleRoot(t)
	TestTheFailureContractHolds(t)
	if t.Failed() {
		return
	}
	want := generatedFailureCatalog(t, root)
	path := filepath.Join(root, "catalog.json")
	if os.Getenv("WRITE_FAILURE_CATALOG") == "1" {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading committed catalog.json: %v; run go generate ./internal/ui", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("catalog.json differs from the constants and production failure sites; run go generate ./internal/ui")
	}
}

// generatedFailureCatalog derives action and stages from production sites,
// rather than declaring either by hand. That assumption is explicit because a
// hand-maintained description of those sites is the drift this catalog exists
// to prevent. The input is the obligation set collected by the failure
// contract's type-aware recogniser above; this is serialization over that
// census, not a second recogniser.
func generatedFailureCatalog(t *testing.T, root string) []byte {
	t.Helper()
	constants := exportedNamedStringConstants(t,
		filepath.Join(root, "internal", "ui"), "FailureID")
	active := map[string]bool{}
	for _, id := range ui.ActiveFailureIDs {
		active[string(id)] = true
	}
	for name, value := range constants {
		if !active[value] {
			t.Errorf("ui.%s = %q is a FailureID constant absent from ActiveFailureIDs", name, value)
		}
	}
	for value := range active {
		found := false
		for _, constantValue := range constants {
			found = found || constantValue == value
		}
		if !found {
			t.Errorf("ActiveFailureIDs contains %q, which has no FailureID constant", value)
		}
	}

	familySites := productionCheckFamilySites(root)
	families := map[string]bool{}
	for family := range familySites {
		families[family] = true
	}
	type sets struct{ actions, stages map[string]bool }
	byID := map[string]*sets{}
	for _, value := range constants {
		if byID[value] != nil {
			t.Errorf("FailureID constant value %q is declared more than once", value)
			continue
		}
		byID[value] = &sets{actions: map[string]bool{}, stages: map[string]bool{}}
	}
	// Check-backed families are authored by finding producers before flow turns
	// them into ui.Failures. Both are emission stages: retaining the producer is
	// why a limit family says "pack" rather than pretending its first appearance
	// was the generic ownCopy call in flow.
	for family, stages := range familySites {
		if entry := byID[family]; entry != nil {
			for stage := range stages {
				entry.stages[stage] = true
			}
		}
	}

	groups := map[string][]obligation{}
	for _, o := range failureContractObligations {
		if o.problem != "" {
			continue
		}
		groups[o.source.target+"|"+o.where] = append(groups[o.source.target+"|"+o.where], o)
	}
	for _, group := range groups {
		var idOb, actionOb *obligation
		for i := range group {
			switch group[i].field {
			case "ID":
				idOb = &group[i]
			case "Next":
				actionOb = &group[i]
			}
		}
		if idOb == nil && actionOb == nil {
			continue // a checked post-construction NextText write
		}
		if idOb == nil || actionOb == nil {
			t.Errorf("%s has only one of the catalog's ID/action obligations", group[0].where)
			continue
		}

		pairs, correlated := correlatedReturnPairs(*idOb, *actionOb)
		if !correlated {
			ids := append([]string(nil), idOb.values...)
			if len(ids) == 0 && findingFamilyExpression(*idOb) {
				ids = mapKeys(families)
			}
			actions := append([]string(nil), actionOb.values...)
			if len(ids) == 0 || len(actions) == 0 {
				t.Errorf("%s cannot derive catalog ID/action from production site (ID %q, action %q)",
					group[0].where, idOb.expr, actionOb.expr)
				continue
			}
			for _, id := range ids {
				for _, action := range actions {
					pairs = append(pairs, [2]string{id, action})
				}
			}
		}
		stage := catalogStage(group[0].where)
		for _, pair := range pairs {
			entry := byID[pair[0]]
			if entry == nil {
				t.Errorf("%s emits undeclared failure id %q", group[0].where, pair[0])
				continue
			}
			if !validActions[pair[1]] {
				t.Errorf("%s emits invalid action %q", group[0].where, pair[1])
				continue
			}
			entry.actions[pair[1]] = true
			entry.stages[stage] = true
		}
	}

	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entries := make([]publicCatalogEntry, 0, len(ids))
	for _, id := range ids {
		set := byID[id]
		if len(set.actions) == 0 || len(set.stages) == 0 {
			t.Errorf("catalog id %q has no derived production action/stage", id)
		}
		var family *string
		if families[id] {
			value := id
			family = &value
		}
		entries = append(entries, publicCatalogEntry{
			Action: mapKeys(set.actions), Family: family, ID: id, Stages: mapKeys(set.stages),
		})
	}
	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func mapKeys[V ~bool](set map[string]V) []string {
	out := make([]string, 0, len(set))
	for value, present := range set {
		if present {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func productionCheckFamilySites(root string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, p := range failureContractFiles {
		ast.Inspect(p.file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			c, ok := p.info.Uses[id].(*types.Const)
			if ok && c.Val().Kind() == constant.String &&
				namedTypeKey(c.Type()) == modulePath+"/internal/check.FailureFamily" {
				family := constant.StringVal(c.Val())
				if out[family] == nil {
					out[family] = map[string]bool{}
				}
				out[family][catalogStage(displayPath(root, p.path))] = true
			}
			return true
		})
	}
	return out
}

func findingFamilyExpression(o obligation) bool {
	call, ok := unparen(o.sourceExpression()).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 || o.source.info == nil || !o.source.info.Types[call.Fun].IsType() {
		return false
	}
	sel, ok := unparen(call.Args[0]).(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "FailureID" &&
		namedTypeKey(o.source.info.TypeOf(sel.X)) == modulePath+"/internal/check.Finding"
}

func (o obligation) sourceExpression() ast.Expr {
	if o.within == nil || o.before == token.NoPos {
		return nil
	}
	var found ast.Expr
	ast.Inspect(o.within.Body, func(n ast.Node) bool {
		e, ok := n.(ast.Expr)
		if ok && e.Pos() == o.before {
			found = e
			return false
		}
		return found == nil
	})
	return found
}

func correlatedReturnPairs(idOb, actionOb obligation) ([][2]string, bool) {
	idKey, idIndex, idOK := multiValueOrigin(idOb)
	actionKey, actionIndex, actionOK := multiValueOrigin(actionOb)
	if !idOK || !actionOK || idKey != actionKey {
		return nil, false
	}
	for _, p := range failureContractFiles {
		if p.target != idOb.source.target {
			continue
		}
		for _, decl := range p.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || targetFuncKey(p, declaredFuncKey(fd, p.info)) != idKey {
				continue
			}
			pairs := map[[2]string]bool{}
			valid := true
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if _, nested := n.(*ast.FuncLit); nested {
					return false
				}
				ret, ok := n.(*ast.ReturnStmt)
				if !ok {
					return true
				}
				if idIndex >= len(ret.Results) || actionIndex >= len(ret.Results) {
					valid = false
					return true
				}
				id := resolveIn("catalog", "ID", ret.Results[idIndex], nodeText, p, "id", fd, 0)
				action := resolveIn("catalog", "Next", ret.Results[actionIndex], nodeText, p, "action", fd, 0)
				if len(id.values) != 1 || len(action.values) != 1 {
					valid = false
					return true
				}
				pairs[[2]string{id.values[0], action.values[0]}] = true
				return true
			})
			out := make([][2]string, 0, len(pairs))
			for pair := range pairs {
				out = append(out, pair)
			}
			sort.Slice(out, func(i, j int) bool {
				if out[i][0] == out[j][0] {
					return out[i][1] < out[j][1]
				}
				return out[i][0] < out[j][0]
			})
			return out, valid && len(out) > 0
		}
	}
	return nil, false
}

func multiValueOrigin(o obligation) (string, int, bool) {
	id, ok := unparen(o.sourceExpression()).(*ast.Ident)
	if !ok || o.within == nil {
		return "", 0, false
	}
	var call *ast.CallExpr
	index, count := 0, 0
	ast.Inspect(o.within.Body, func(n ast.Node) bool {
		if n != nil && n.Pos() >= o.before {
			return false
		}
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			return true
		}
		for i, lhs := range as.Lhs {
			name, ok := lhs.(*ast.Ident)
			if !ok || name.Name != id.Name {
				continue
			}
			count++
			call, ok = unparen(as.Rhs[0]).(*ast.CallExpr)
			if ok {
				index = i
			}
		}
		return true
	})
	if count != 1 || call == nil {
		return "", 0, false
	}
	key := calledFuncKey(call.Fun, o.within, o.source.info)
	return targetFuncKey(o.source, key), index, key != ""
}

func nodeText(p parsedFile, n ast.Node) string {
	if n == nil {
		return ""
	}
	// resolveIn only needs this callback for diagnostics and definition
	// fingerprints here; catalog return values are typed constants.
	return "catalog expression"
}

func catalogStage(where string) string {
	path := strings.SplitN(where, ":", 2)[0]
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) >= 2 && (parts[0] == "internal" || parts[0] == "pkg" || parts[0] == "cmd") {
		return parts[1]
	}
	return parts[0]
}

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
