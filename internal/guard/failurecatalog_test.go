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
	"strconv"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
)

// publicCatalog is the published artefact: a note saying what its fields
// mean, and one entry per failure id.
type publicCatalog struct {
	Note     []string             `json:"note"`
	Failures []publicCatalogEntry `json:"failures"`
}

type publicCatalogEntry struct {
	Action         []string           `json:"action"`
	Family         *string            `json:"family"`
	Headline       map[string]*string `json:"headline"`
	HeadlineReason map[string]string  `json:"headline_reason"`
	ID             string             `json:"id"`
	Stages         []string           `json:"stages"`
}

// publicCatalogNote opens the published note. It travels in the file because
// the meaning of stages changed, and a consumer reading the file is the one
// who needs to know. The lines after it are generated from each stage's
// documented sentence, by catalogNote.
const publicCatalogNote = "stages are the stages each failure's construction sites DECLARE: " +
	"what a person was doing when the failure met them, never the package or file that raised " +
	"it. headline maps each of those stages to the failure's What exactly as written in code, " +
	"Go verbs kept. A null headline is a What composed at run time, and headline_reason says so. " +
	"Each line after this one names a stage and what a person is doing when a failure carries it."

// catalogNote is the published note: the opening line, then one line per
// declared stage, in declared order, carrying the one sentence that stage's
// constant is documented with in internal/ui.
func catalogNote(t *testing.T, root string) []string {
	t.Helper()
	sentences := stageSentences(t, root)
	note := []string{publicCatalogNote}
	for _, stage := range ui.Stages {
		sentence := sentences[string(stage)]
		if sentence == "" {
			t.Errorf("stage %q has no documented sentence, so the catalog note cannot say what it means", stage)
		}
		note = append(note, stageNoteLine(string(stage), sentence))
	}
	return note
}

// stageNoteLine is how the note carries one stage.
func stageNoteLine(stage, sentence string) string { return stage + ": " + sentence }

// stageSentences reads, from internal/ui's own source, the sentence each
// Stage constant is documented with, keyed by the stage's value. The doc
// comment is the one home: the note is generated from it, not copied.
func stageSentences(t *testing.T, root string) map[string]string {
	t.Helper()
	dir := filepath.Join(root, "internal", "ui")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Values) != 1 {
					continue
				}
				typ, ok := vs.Type.(*ast.Ident)
				lit, isLit := vs.Values[0].(*ast.BasicLit)
				if !ok || typ.Name != "Stage" || !isLit || lit.Kind != token.STRING {
					continue
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("stage constant %s does not decode: %v", vs.Names[0].Name, err)
				}
				out[value] = strings.Join(strings.Fields(vs.Doc.Text()), " ")
			}
		}
	}
	return out
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

// generatedFailureCatalog derives every field from production sites rather
// than declaring any by hand: actions from the actions sites pass, stages
// from the stages sites DECLARE, and headlines from the What each site
// writes. The input is the obligation set collected by the failure
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

	seen := map[string]bool{}
	for _, value := range constants {
		if seen[value] {
			t.Errorf("FailureID constant value %q is declared more than once", value)
		}
		seen[value] = true
	}
	families := productionCheckFamilies()
	census := catalogCensusFrom(t, failureContractObligations, families, constants)

	entries := make([]publicCatalogEntry, 0, len(census.actions))
	for _, id := range headlineKeys(census.actions) {
		if len(census.actions[id]) == 0 || len(census.stages[id]) == 0 {
			t.Errorf("catalog id %q has no derived production action/stage", id)
		}
		var family *string
		if families[id] {
			value := id
			family = &value
		}
		headline := map[string]*string{}
		reason := map[string]string{}
		for _, stage := range mapKeys(census.stages[id]) {
			values := census.whats[id][stage]
			if len(values) != 1 {
				// THE ROW NAMES THE SITES; this refuses to emit a headline it
				// would have to choose. See TestOneWhatPerIdAndStage.
				t.Errorf("catalog id %q at stage %q carries %d What values, and a headline is one What "+
					"per (id, Stage); TestOneWhatPerIdAndStage names the sites", id, stage, len(values))
				continue
			}
			for value := range values {
				kind, text, _ := strings.Cut(value, ":")
				if kind == "literal" || kind == "format" {
					quoted := text
					headline[stage] = &quoted
				} else {
					headline[stage] = nil
					reason[stage] = "composed"
				}
			}
		}
		entries = append(entries, publicCatalogEntry{
			Action: mapKeys(census.actions[id]), Family: family, Headline: headline, HeadlineReason: reason,
			ID: id, Stages: mapKeys(census.stages[id]),
		})
	}
	out, err := json.MarshalIndent(publicCatalog{Note: catalogNote(t, root), Failures: entries}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

// catalogCensus is what production sites say about each failure id: the
// actions they pass, the stages they declare, the What each stage's sites
// write, and how each composed What is built. The generator serialises it
// and the headline rows read it, so the artefact and the rows cannot
// disagree about what the sites say.
type catalogCensus struct {
	actions map[string]map[string]bool
	stages  map[string]map[string]bool
	// id -> stage -> What value -> the sites writing it. A composed What is
	// filed as "composed": its words are not the catalog's to quote, and how
	// it is built is filed under builders instead.
	whats map[string]map[string]map[string]map[string]bool
	// id -> stage -> "constructor · shape" -> the sites building a composed What
	builders map[string]map[string]map[string]map[string]bool
}

func catalogCensusFrom(t *testing.T, obs []obligation, families map[string]bool,
	constants map[string]string) catalogCensus {
	t.Helper()
	c := catalogCensus{actions: map[string]map[string]bool{}, stages: map[string]map[string]bool{},
		whats:    map[string]map[string]map[string]map[string]bool{},
		builders: map[string]map[string]map[string]map[string]bool{}}
	for _, value := range constants {
		c.actions[value] = map[string]bool{}
		c.stages[value] = map[string]bool{}
		c.whats[value] = map[string]map[string]map[string]bool{}
		c.builders[value] = map[string]map[string]map[string]bool{}
	}
	file := func(m map[string]map[string]map[string]map[string]bool, id, stage, key, site string) {
		if m[id][stage] == nil {
			m[id][stage] = map[string]map[string]bool{}
		}
		if m[id][stage][key] == nil {
			m[id][stage][key] = map[string]bool{}
		}
		m[id][stage][key][site] = true
	}
	addWhat := func(id, stage, value, site, constructor string) {
		kind, shape, _ := strings.Cut(value, ":")
		if kind == "composed" {
			file(c.whats, id, stage, "composed", site)
			file(c.builders, id, stage, constructor+" · "+shape, site)
			return
		}
		file(c.whats, id, stage, value, site)
	}

	// family -> What value -> site -> constructor, read where each finding is
	// built rather than at the one generic site that turns it into a failure.
	familyWhats := map[string]map[string]map[string]string{}
	groups := map[string][]obligation{}
	for _, o := range obs {
		if o.problem != "" {
			continue
		}
		if o.field == "FamilyWhat" {
			if o.family == "" || len(o.values) != 1 {
				t.Errorf("%s: a check family's What was not read (%q)", o.where, o.expr)
				continue
			}
			if familyWhats[o.family] == nil {
				familyWhats[o.family] = map[string]map[string]string{}
			}
			if familyWhats[o.family][o.values[0]] == nil {
				familyWhats[o.family][o.values[0]] = map[string]string{}
			}
			familyWhats[o.family][o.values[0]][o.where] = o.constructor
			continue
		}
		groups[o.source.target+"|"+o.where] = append(groups[o.source.target+"|"+o.where], o)
	}
	for _, group := range groups {
		var idOb, actionOb, stageOb, whatOb *obligation
		for i := range group {
			switch group[i].field {
			case "ID":
				idOb = &group[i]
			case "Next":
				actionOb = &group[i]
			case "Stage":
				stageOb = &group[i]
			case "What":
				whatOb = &group[i]
			}
		}
		if idOb == nil && actionOb == nil {
			continue // a checked post-construction NextText write
		}
		if idOb == nil || actionOb == nil {
			t.Errorf("%s has only one of the catalog's ID/action obligations", group[0].where)
			continue
		}
		if stageOb == nil || len(stageOb.values) != 1 {
			expr := ""
			if stageOb != nil {
				expr = stageOb.expr
			}
			t.Errorf("%s declares no single Stage the catalog can record (%q)", group[0].where, expr)
			continue
		}
		stage := stageOb.values[0]

		familySite := false
		pairs, correlated := correlatedReturnPairs(*idOb, *actionOb)
		if !correlated {
			ids := append([]string(nil), idOb.values...)
			if len(ids) == 0 && findingFamilyExpression(*idOb) {
				ids = mapKeys(families)
				familySite = true
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
		for _, pair := range pairs {
			if c.actions[pair[0]] == nil {
				t.Errorf("%s emits undeclared failure id %q", group[0].where, pair[0])
				continue
			}
			if !validActions[pair[1]] {
				t.Errorf("%s emits invalid action %q", group[0].where, pair[1])
				continue
			}
			c.actions[pair[0]][pair[1]] = true
			c.stages[pair[0]][stage] = true
			if familySite {
				// The family's What is read where its finding is written.
				for value, sites := range familyWhats[pair[0]] {
					for site, constructor := range sites {
						addWhat(pair[0], stage, value, site, constructor)
					}
				}
				continue
			}
			if whatOb == nil || len(whatOb.values) != 1 {
				t.Errorf("%s: the What of %q was not read", group[0].where, pair[0])
				continue
			}
			addWhat(pair[0], stage, whatOb.values[0], group[0].where, whatOb.constructor)
		}
	}
	return c
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

// productionCheckFamilies is every check family a production file names.
func productionCheckFamilies() map[string]bool {
	out := map[string]bool{}
	for _, p := range failureContractFiles {
		ast.Inspect(p.file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			c, ok := p.info.Uses[id].(*types.Const)
			if ok && c.Val().Kind() == constant.String &&
				namedTypeKey(c.Type()) == modulePath+"/internal/check.FailureFamily" {
				out[constant.StringVal(c.Val())] = true
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
