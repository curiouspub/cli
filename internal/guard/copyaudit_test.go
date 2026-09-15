package guard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// THE COPY AUDIT. The corpus is named once: every string that
// reaches ui — What, Why, NextText — plus check.Finding's authored copy,
// the MCP tool descriptions and refusal text, the npm postinstall's
// messages, help text, the README, and any user-facing document in
// cli/. Detail is in the corpus and is deliberately never linted here —
// it is somebody else's sentence (ui/failure.go's own words), and
// editing the rule to reach it would mean asserting a property of a
// quotation rather than of this program's own writing.
//
// WHAT THIS SCANNER RESOLVES, STATED AS A LIMIT RATHER THAN IMPLIED AS
// COMPLETE. It follows five shapes: a call to ui.NewFailure or ui.Quoted,
// a composite literal of type Failure or Finding, a call to a method
// named Step, Result or Help (the terminal's own narration surface,
// which is how copy reaches a person without ever passing through a
// Failure — the ui.Failure census this project's own CLAUDE.md warns
// against treating as complete), a call to ErrorResult or TextResult
// (the MCP surface's equivalent), and a top-level const or a zero-
// argument function whose single return statement is one of the above
// shapes (which is how the four ui.Prose usage constants and the two
// wire-driven MCP descriptions are reached without naming each one by
// hand). Within each, only a STRING LITERAL, a concatenation of string
// literals, an identifier resolving to another such constant, or an
// fmt.Sprintf call whose format argument is one of those is resolved —
// a value built any other way (a helper call, a variable computed at
// run time) contributes nothing, which is the same fail-closed posture
// the failure catalog's own recogniser takes for a What it cannot read.
//
// THE NAMED GAP THIS LEAVES: internal/config builds several of its own
// diagnostic sentences (a corrupt config file, an ambiguous field, a
// permissions warning) as plain `error` values returned through helper
// functions, and at least one of them — NoTokenReason — reaches a
// person verbatim through deploy's own `deps.Prompt.Step("%s",
// cfg.NoTokenReason.Error())`. That call site's own format string ("%s")
// is scanned; the dynamic value behind it is not, because tracing an
// arbitrary error value back to the function that built it is a
// data-flow analysis this pass does not attempt. Read by hand for this
// round (internal/config/config.go's fmt.Errorf and errors.New call
// sites): none contains a forbidden token or a command-position mention
// outside the registered set. Named here because a hand check is not a
// mechanical one, and this file's own rule is to say so rather than let
// the gap pass as coverage.
//
// README.md, npm/README.md and npm/install.js are read as prose and
// scanned whole rather than field by field, because neither markdown nor
// the install script has a "field" for this scanner to key on the way a
// struct literal does.

// ---------------------------------------------------------------------
// Resolving an expression to the text it prints, when it can be known
// without running the program.
// ---------------------------------------------------------------------

// packageConsts is every top-level const this package's own files
// declare that resolves to a plain string, keyed by name. It is built in
// two passes so a const referring to one declared later in the same
// package (or in a different file of it) still resolves — the ordinary
// case for Go source, which does not require declaration order.
func packageConsts(files []*ast.File) map[string]string {
	raw := map[string]ast.Expr{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				raw[vs.Names[0].Name] = vs.Values[0]
			}
		}
	}
	resolved := map[string]string{}
	for pass := 0; pass < 3; pass++ {
		for name, expr := range raw {
			if _, done := resolved[name]; done {
				continue
			}
			if v, ok := literalString(expr, resolved); ok {
				resolved[name] = v
			}
		}
	}
	return resolved
}

// literalString resolves e to a compile-time string when e is built
// entirely from string literals, concatenation, a reference to a name in
// consts, or fmt.Sprintf applied to such a format string. See the file
// doc comment for what this deliberately does not follow.
func literalString(e ast.Expr, consts map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.ParenExpr:
		return literalString(v.X, consts)
	case *ast.Ident:
		s, ok := consts[v.Name]
		return s, ok
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		x, ok := literalString(v.X, consts)
		if !ok {
			return "", false
		}
		y, ok := literalString(v.Y, consts)
		if !ok {
			return "", false
		}
		return x + y, true
	case *ast.CallExpr:
		switch calleeName(v.Fun) {
		case "Sprintf":
			if len(v.Args) == 0 {
				return "", false
			}
			return literalString(v.Args[0], consts)
		case "Prose", "FailureID", "Stage", "Secret":
			// A defined-type conversion — ui.Prose(x) and the like — carries
			// the same text as its argument; only the type changes.
			if len(v.Args) == 1 {
				return literalString(v.Args[0], consts)
			}
		}
		return "", false
	default:
		return "", false
	}
}

// calleeName and typeName — the bare, unqualified name a call or a
// composite literal's type is written with, "Sprintf" for both
// fmt.Sprintf and a dot-imported Sprintf, "Failure" for both Failure{}
// and ui.Failure{} — are settings_test.go's own helpers, reused rather
// than redeclared: this package already has one definition of "what is
// this expression's bare name", and a second one is exactly the drift
// this project's own CLAUDE.md warns a reshaped copy invites.

// ---------------------------------------------------------------------
// The corpus: every copy site this scanner can read, across every
// published non-test Go file, plus README.md, npm/README.md and
// npm/install.js.
// ---------------------------------------------------------------------

// copyItem is one resolved piece of authored copy, with enough origin to
// name in a finding and to check against the one recorded exception.
type copyItem struct {
	text   string
	origin string // "path:line" for Go sites, "path" for whole-document ones

	// isDoc marks a whole-document item — README.md, npm/README.md,
	// npm/install.js — rather than one Go copy field. The package-path
	// and Go-type-name rules below are scoped OFF this half of the
	// corpus: those two rules exist to catch an internal implementation
	// detail leaking into a RUNTIME message, and a document whose whole
	// job is describing this repository's own layout and its own public
	// import path (`import "github.com/curiouspub/cli/pkg/wire"`, the
	// README's own words about `pkg/wire`) legitimately names both. The
	// other four forbidden tokens — "failed to", "unable to", "error:",
	// "nil", "0x" — still apply everywhere, doc or not.
	isDoc bool
}

// goCorpus walks every published non-test .go file and returns every
// copy site the five shapes above resolve.
func goCorpus(t *testing.T, root string) []copyItem {
	t.Helper()
	fset := token.NewFileSet()
	byDir := map[string][]*ast.File{}
	type fileInfo struct {
		file *ast.File
		path string
	}
	var all []fileInfo
	for _, path := range goFiles(t, root, false) {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		dir := filepath.Dir(path)
		byDir[dir] = append(byDir[dir], f)
		all = append(all, fileInfo{f, path})
	}
	if len(all) == 0 {
		t.Fatal("no published non-test Go file found, so the copy-audit corpus would be empty")
	}
	constsByDir := map[string]map[string]string{}
	for dir, files := range byDir {
		constsByDir[dir] = packageConsts(files)
	}

	var items []copyItem
	add := func(fset *token.FileSet, path string, e ast.Expr, consts map[string]string) {
		if e == nil {
			return
		}
		s, ok := literalString(e, consts)
		if !ok || strings.TrimSpace(s) == "" {
			return
		}
		items = append(items, copyItem{text: s, origin: displayPath(root, path) + ":" +
			itoa(fset.Position(e.Pos()).Line)})
	}

	for _, fi := range all {
		consts := constsByDir[filepath.Dir(fi.path)]

		// Every resolvable top-level const, directly — this is how the
		// four ui.Prose usage texts, the login flow's prompts and hints,
		// and every *Description const reach the corpus without this
		// scanner having to know each one's name.
		for _, decl := range fi.file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 {
					continue
				}
				add(fset, fi.path, vs.Names[0], consts)
			}
		}

		// Every zero-argument, no-receiver function whose body is exactly
		// one return statement — deploySiteDescription and
		// deployStatusDescription's own shape.
		for _, decl := range fi.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Type.Params == nil || len(fd.Type.Params.List) != 0 ||
				fd.Body == nil || len(fd.Body.List) != 1 {
				continue
			}
			ret, ok := fd.Body.List[0].(*ast.ReturnStmt)
			if !ok || len(ret.Results) != 1 {
				continue
			}
			add(fset, fi.path, ret.Results[0], consts)
		}

		ast.Inspect(fi.file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				switch calleeName(node.Fun) {
				case "Step", "Result", "Help":
					for _, arg := range node.Args {
						add(fset, fi.path, arg, consts)
					}
				case "NewFailure":
					if len(node.Args) == 6 {
						add(fset, fi.path, node.Args[2], consts) // What
						add(fset, fi.path, node.Args[3], consts) // Why
						add(fset, fi.path, node.Args[5], consts) // NextText
					}
				case "Quoted":
					if len(node.Args) == 6 {
						add(fset, fi.path, node.Args[2], consts) // What
						// Args[3] is Detail — somebody else's sentence,
						// deliberately excluded by name below.
						add(fset, fi.path, node.Args[5], consts) // NextText
					}
				case "ErrorResult", "TextResult":
					if len(node.Args) > 0 {
						add(fset, fi.path, node.Args[0], consts)
					}
				}
			case *ast.CompositeLit:
				switch typeName(node.Type) {
				case "Failure":
					for _, elt := range node.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := kv.Key.(*ast.Ident)
						if !ok {
							continue
						}
						switch key.Name {
						case "What", "Why", "NextText":
							add(fset, fi.path, kv.Value, consts)
						}
					}
				case "Finding":
					for _, elt := range node.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						key, ok := kv.Key.(*ast.Ident)
						if !ok {
							continue
						}
						switch key.Name {
						case "Message", "What", "Why", "Next":
							add(fset, fi.path, kv.Value, consts)
						}
					}
				}
			}
			return true
		})
	}
	return items
}

// itoa is streams_test.go's own helper, reused here too.

// jsStringLiteralPattern is a deliberately narrow reader of npm/install.js:
// a single- or double-quoted JavaScript string with no embedded quote of
// its own kind, or a template literal with no ${...} interpolation. It
// is not a JavaScript parser, and it does not need to be one — install.js
// has no dependency and is meant to be read in one sitting by a person,
// which is exactly the property that makes a small regex adequate for a
// mechanical second reader too. A literal outside this shape (an escaped
// quote, an interpolated template) contributes nothing, the same
// fail-closed posture the Go side takes for an expression it cannot
// resolve.
var jsStringLiteralPattern = regexp.MustCompile(
	"'([^'\\\\]*)'" + `|"([^"\\]*)"` + "|`([^`$]*)`")

func jsCorpus(t *testing.T, root string) []copyItem {
	t.Helper()
	path := filepath.Join(root, "npm", "install.js")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var items []copyItem
	for i, line := range strings.Split(string(data), "\n") {
		for _, m := range jsStringLiteralPattern.FindAllStringSubmatch(line, -1) {
			text := m[1] + m[2] + m[3]
			if strings.TrimSpace(text) == "" {
				continue
			}
			items = append(items, copyItem{text: text,
				origin: "npm/install.js:" + itoa(i+1), isDoc: true})
		}
	}
	return items
}

func docCorpus(t *testing.T, root string, relPath string) copyItem {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, relPath))
	if err != nil {
		t.Fatalf("reading %s: %v", relPath, err)
	}
	return copyItem{text: string(data), origin: relPath, isDoc: true}
}

// copyAuditCorpus is the whole corpus this file's two rows share, built
// once per test binary run so the two checks read exactly the same
// evidence rather than two passes that could disagree about what exists.
func copyAuditCorpus(t *testing.T, root string) []copyItem {
	t.Helper()
	items := goCorpus(t, root)
	items = append(items, jsCorpus(t, root)...)
	items = append(items, docCorpus(t, root, "README.md"))
	items = append(items, docCorpus(t, root, filepath.Join("npm", "README.md")))
	return items
}

// ---------------------------------------------------------------------
// The forbidden-token rule.
// ---------------------------------------------------------------------

// forbiddenTokenExceptions is the one origin allowed to carry "error:" —
// the client's own framing of server content on the build-log stream,
// which internal/flow/stream.go's own comment names as SOMEBODY ELSE'S
// CONTENT rather than this program's sentence. Asserted by
// TestForbiddenTokenExceptionSetHasExactlyOneMember below, so a second
// entry added here without a matching change to that row is a red rather
// than a silent widening.
var forbiddenTokenExceptions = map[string]bool{
	"errorNarration": true,
}

// originIsExempt reports whether origin is the one recorded exception's
// own site.
func originIsExempt(origin string) bool {
	// The exception is scoped to the one declaration site recorded
	// above: internal/flow/stream.go's errorNarration
	// constant. A go/ast const declaration's own position is its name,
	// so the corpus item for it carries that file with the line the
	// name sits on — checked here by file rather than by line, because a
	// file this narrow (one named exception) is what makes "checked by
	// file" safe: nothing else in stream.go is allowed to say "error:".
	return strings.HasSuffix(origin, "internal/flow/stream.go") ||
		strings.Contains(origin, "internal/flow/stream.go:")
}

func TestForbiddenTokenExceptionSetHasExactlyOneMember(t *testing.T) {
	if len(forbiddenTokenExceptions) != 1 {
		t.Fatalf("forbiddenTokenExceptions has %d member(s), want exactly 1 — a second exception "+
			"granted here needs a ruling recorded beside it, not a silent widening of the set",
			len(forbiddenTokenExceptions))
	}
}

var (
	failedToPattern    = regexp.MustCompile(`(?i)failed to`)
	unableToPattern    = regexp.MustCompile(`(?i)unable to`)
	errorColonPatter   = regexp.MustCompile(`(?i)error:`)
	nilPattern         = regexp.MustCompile(`\bnil\b`)
	hexPrefixPattern   = regexp.MustCompile(`0x[0-9a-fA-F]`)
	packagePathPattern = regexp.MustCompile(
		`github\.com/curiouspub/cli|\b(?:internal|cmd|pkg)/[a-zA-Z0-9_.]+(?:/[a-zA-Z0-9_.]+)*`)
)

// forbiddenTokenHits reports every forbidden pattern text contains,
// named the way this project's three-part copy shape names its own
// checks: what happened, why, and never a bare implementation leak.
//
// isDoc SCOPES OFF THE PACKAGE-PATH AND GO-TYPE-NAME RULES, and that is a
// finding this row's own first run produced rather than a design
// decided in advance: applied to the whole of README.md and
// npm/README.md, "a package path" fired on `pkg/wire` and on
// `github.com/curiouspub/cli` — this repository's own public import
// path, named in the sentence that exists to tell a reader they can
// import it. Those two rules exist to catch an internal implementation
// detail leaking into a RUNTIME message; a document whose job is
// describing this repository's own layout is not that, and a rule that
// cannot tell the two apart would forbid the README from naming the
// package it is the README for. The other four tokens are unaffected —
// "failed to", "unable to", "error:", "nil" and "0x" have no legitimate
// use in prose describing this project either.
func forbiddenTokenHits(text string, typeNames map[string]bool, isDoc bool) []string {
	var hits []string
	if failedToPattern.MatchString(text) {
		hits = append(hits, `"failed to"`)
	}
	if unableToPattern.MatchString(text) {
		hits = append(hits, `"unable to"`)
	}
	if errorColonPatter.MatchString(text) {
		hits = append(hits, `"error:"`)
	}
	if nilPattern.MatchString(text) {
		hits = append(hits, `"nil"`)
	}
	if hexPrefixPattern.MatchString(text) {
		hits = append(hits, `"0x"`)
	}
	if isDoc {
		return hits
	}
	if packagePathPattern.MatchString(text) {
		hits = append(hits, "a package path ("+packagePathPattern.FindString(text)+")")
	}
	for _, word := range strings.FieldsFunc(text, func(r rune) bool {
		return !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9')
	}) {
		if typeNames[word] {
			hits = append(hits, "a Go type name ("+word+")")
		}
	}
	return hits
}

// exportedMultiHumpTypeNames is this module's own definition of "looks
// like a Go type name" for this row: an exported type whose name carries
// at least two uppercase letters — APIError, LoginRefusal, FailureID,
// NextAction, ExitServerClosed. A single-hump exported name (Result,
// Config, Client, Prose, Stage, Secret, Finding, Failure) is also a
// perfectly ordinary capitalised English word, and checking for it would
// red on legitimate prose the moment a sentence used one at the start —
// this project's own copy does, routinely. Stated as the definition
// rather than left implicit, because a guard's own CLAUDE.md entry says
// exactly this must be quoted rather than assumed.
func exportedMultiHumpTypeNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	fset := token.NewFileSet()
	for _, path := range goFiles(t, root, false) {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				name := ts.Name.Name
				if name == "" || !('A' <= name[0] && name[0] <= 'Z') {
					continue
				}
				uppers := 0
				for _, r := range name {
					if 'A' <= r && r <= 'Z' {
						uppers++
					}
				}
				if uppers >= 2 {
					names[name] = true
				}
			}
		}
	}
	return names
}

// TestNoAuthoredCopyContainsAForbiddenToken is the mechanical half of the
// copy audit's forbidden-token rule.
func TestNoAuthoredCopyContainsAForbiddenToken(t *testing.T) {
	root := moduleRoot(t)
	typeNames := exportedMultiHumpTypeNames(t, root)
	items := copyAuditCorpus(t, root)
	if len(items) == 0 {
		t.Fatal("the copy-audit corpus is empty, so this row compared nothing")
	}
	for _, item := range items {
		if originIsExempt(item.origin) {
			continue
		}
		for _, hit := range forbiddenTokenHits(item.text, typeNames, item.isDoc) {
			t.Errorf("%s contains %s: %q", item.origin, hit, item.text)
		}
	}
}

// TestForbiddenCopyRedsOnAKnownBadPhraseAndTheExceptionHolds is the
// row's own fixture: a message containing "failed to open config" reds,
// the exempted origin does not, and a plain sentence does not.
func TestForbiddenCopyRedsOnAKnownBadPhraseAndTheExceptionHolds(t *testing.T) {
	typeNames := map[string]bool{"APIError": true}
	if hits := forbiddenTokenHits("curious failed to open config.", typeNames, false); len(hits) == 0 {
		t.Error(`"curious failed to open config." was not flagged`)
	}
	if hits := forbiddenTokenHits("curious.pub is not taking deploys right now.", typeNames, false); len(hits) != 0 {
		t.Errorf("ordinary copy was flagged: %v", hits)
	}
	if hits := forbiddenTokenHits("the *APIError type carries the code.", typeNames, false); len(hits) == 0 {
		t.Error("a Go type name leaking into copy was not flagged")
	}
	if hits := forbiddenTokenHits(
		"import \"github.com/curiouspub/cli/pkg/wire\"", typeNames, true); len(hits) != 0 {
		t.Errorf("a doc-origin item naming this repository's own public import path was flagged: %v", hits)
	}
	if hits := forbiddenTokenHits(
		"import \"github.com/curiouspub/cli/pkg/wire\"", typeNames, false); len(hits) == 0 {
		t.Error("the same text from a non-doc origin was not flagged, so isDoc is not actually scoping anything")
	}
	if !originIsExempt("internal/flow/stream.go:127") {
		t.Error("the recorded exception's own origin was not treated as exempt")
	}
	if originIsExempt("internal/flow/upload.go:127") {
		t.Error("an unrelated file was treated as exempt")
	}
}

// ---------------------------------------------------------------------
// The command-token-position rule.
// ---------------------------------------------------------------------

// dispatchTokens is read from cmd/curious/main.go's own switch — never
// typed beside it — so a command added there is registered here the
// moment it is, and a rename is caught the moment it stops matching
// whatever a reader is still writing about the old name.
func dispatchTokens(t *testing.T, root string) map[string]bool {
	t.Helper()
	path := filepath.Join(root, "cmd", "curious", "main.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	tokens := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok || cc.List == nil {
				continue // the default clause
			}
			for _, expr := range cc.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				tokens[v] = true
			}
		}
		return true
	})
	if len(tokens) == 0 {
		t.Fatal("no case clause found in cmd/curious/main.go's dispatch switch, so no token is registered")
	}
	return tokens
}

// commandPositionPattern finds every backtick-delimited mention of this
// binary followed by one word: `curious <token>` or `npx curiouspub
// <token>`. A trailing "<...>" placeholder is captured as-is and
// recognised as a declared placeholder rather than a token to check —
// main.go's own usage text writes `curious <command> -h` the same way.
// Prose that merely follows the word "curious" with no leading backtick
// — "curious couldn't finish logging you in." — never matches this
// pattern at all, which is what keeps ordinary sentences out of scope by
// construction rather than by an exception list.
var commandPositionPattern = regexp.MustCompile(
	"`(?:curious|npx curiouspub) (<[a-zA-Z]+>|[a-z][a-z0-9_-]*)")

// fencedCommandLinePattern is the third position: a literal invocation at
// the start of a line inside a fenced code block, with no backticks of
// its own because the fence already delimits it.
var fencedCommandLinePattern = regexp.MustCompile(
	`(?m)^\s*(?:curious|npx curiouspub) (<[a-zA-Z]+>|[a-z][a-z0-9_-]*)`)

// fencedBlockPattern finds every ``` ... ``` block in a markdown corpus
// item, so the third command position is read only inside one.
var fencedBlockPattern = regexp.MustCompile("(?s)```.*?```")

func unregisteredCommandMentions(text string, registered map[string]bool) []string {
	var bad []string
	check := func(token string) {
		if strings.HasPrefix(token, "<") {
			return // a declared placeholder, not a token to look up
		}
		if !registered[token] {
			bad = append(bad, token)
		}
	}
	for _, m := range commandPositionPattern.FindAllStringSubmatch(text, -1) {
		check(m[1])
	}
	for _, block := range fencedBlockPattern.FindAllString(text, -1) {
		for _, m := range fencedCommandLinePattern.FindAllStringSubmatch(block, -1) {
			check(m[1])
		}
	}
	return bad
}

// TestNoAuthoredCopyNamesAnUnregisteredCommand is the mechanical half of
// the command-token-position rule.
func TestNoAuthoredCopyNamesAnUnregisteredCommand(t *testing.T) {
	root := moduleRoot(t)
	registered := dispatchTokens(t, root)
	items := copyAuditCorpus(t, root)
	for _, item := range items {
		for _, bad := range unregisteredCommandMentions(item.text, registered) {
			t.Errorf("%s names `curious %s` in command position, and %q is not a token "+
				"cmd/curious/main.go's dispatch switch cases", item.origin, bad, bad)
		}
	}
}

// TestCommandTokenRuleFixture is the row's own fixture: an unregistered
// token in command position reds, a registered one does not, prose after
// "curious" with no backtick does not, and the declared placeholder does
// not.
func TestCommandTokenRuleFixture(t *testing.T) {
	registered := map[string]bool{"deploy": true, "version": true, "mcp": true, "help": true}

	if bad := unregisteredCommandMentions("Run `curious frobnicate` first.", registered); len(bad) != 1 {
		t.Errorf("an unregistered token in command position was not caught: %v", bad)
	}
	if bad := unregisteredCommandMentions("Run `curious deploy` again.", registered); len(bad) != 0 {
		t.Errorf("a registered token was flagged: %v", bad)
	}
	if bad := unregisteredCommandMentions(
		"curious couldn't finish logging you in.", registered); len(bad) != 0 {
		t.Errorf("prose with no leading backtick was flagged: %v", bad)
	}
	if bad := unregisteredCommandMentions(
		"Run 'curious <command> -h' for a command's own flags.", registered); len(bad) != 0 {
		t.Errorf("the declared <command> placeholder was flagged: %v", bad)
	}
	if bad := unregisteredCommandMentions(
		"```\ncurious frobnicate\n```", registered); len(bad) != 1 {
		t.Errorf("an unregistered token inside a fenced block was not caught: %v", bad)
	}
	if bad := unregisteredCommandMentions(
		"```\nnpx curiouspub deploy\n```", registered); len(bad) != 0 {
		t.Errorf("a registered token inside a fenced block was flagged: %v", bad)
	}

	// A newly registered command is picked up with no change to this
	// file: registering it in the map above is the only edit a real
	// dispatch addition would ever need here.
	registered["whoami"] = true
	if bad := unregisteredCommandMentions("Run `curious whoami`.", registered); len(bad) != 0 {
		t.Errorf("a freshly registered token was flagged: %v", bad)
	}
}
