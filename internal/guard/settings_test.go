package guard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// settingNames is the environment vocabulary this program reads, ASSERTED
// AS A SET rather than matched by a pattern.
//
// A closed list built by a pattern cannot contain the members that arrive
// after the pattern was chosen, so the pattern is used to CHECK this list
// rather than to stand in for it: the row below scans the tree for
// anything shaped like one of these and fails if it finds a name this
// list does not have. Adding a setting is then one edit here and a red
// row until it is made.
var settingNames = []string{
	"CURIOUS_API_URL",
	"CURIOUS_CONFIG",
	"CURIOUS_DEBUG",
}

// settingPattern is how a setting name is SHAPED. It exists to audit the
// list above and is never used in its place.
var settingPattern = regexp.MustCompile(`CURIOUS_[A-Z][A-Z0-9_]*`)

// TestASettingNamedInAFailureIsNamedInItsWhy.
//
// # The paragraph an agent never reads is the wrong place for a fact
//
// A failure's last paragraph is an instruction written for somebody at a
// terminal, and the agent surface renders the ACTION as a value instead —
// so that paragraph's words do not travel. That is the right trade for an
// instruction. It is the wrong trade for a FACT, and three failures had
// buried one there: the name of the environment variable holding the bad
// value, mentioned only in the next-step line.
//
// Dropped, an agent is told "curious can't use that API address" and not
// which setting to look at, which is a refusal it cannot act on. The
// first instance was caught by a row asserting one variable in one
// failure. This is that row's general form.
//
// THE RULE IS PLACEMENT, NOT ABSENCE. A setting may appear in the
// next-step line as well — "Check CURIOUS_API_URL, or unset it" is good
// copy for a terminal. What it may not do is appear ONLY there, because
// the Why is the paragraph both surfaces render.
//
// A failure built with Quoted has no Why at all, so naming a setting in
// one is always a violation: the fact has nowhere to live that an agent
// will see. That is a real constraint rather than an accident of this
// guard, and it is why the configuration failure was rebuilt around a
// Why of its own.
//
// # It reads the source rather than a list of failures
//
// The set this ranges over is every ui.Failure CONSTRUCTED in the tree,
// found by parsing rather than by enumeration, because a list of
// failures is a closed list built by hand and the next failure to name a
// setting is the one nobody remembers to add to it.
//
// Test files are exempt: a fixture naming a setting is a fixture, not
// copy anybody is shown.
//
// REQUIRED MUTATION, run 2026-09-12: move any of the three setting names
// out of its Why and back into its next-step line. Reds here, naming the
// file, the construction and the setting.
func TestASettingNamedInAFailureIsNamedInItsWhy(t *testing.T) {
	root := moduleRoot(t)

	var goFiles []string
	for _, path := range publishedTextFiles(t, root) {
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			goFiles = append(goFiles, path)
		}
	}
	if len(goFiles) == 0 {
		t.Fatal("no published Go file was scanned, so this guard measured nothing")
	}

	t.Run("the list is the whole vocabulary", func(t *testing.T) {
		known := map[string]bool{}
		for _, name := range settingNames {
			known[name] = true
		}
		found := map[string]bool{}
		for _, path := range goFiles {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			for _, hit := range settingPattern.FindAllString(string(src), -1) {
				found[hit] = true
			}
		}
		if len(found) == 0 {
			t.Fatal("the scan found no setting name anywhere, so the comparison " +
				"below is between two empty sets and this row is inert")
		}
		var missing, extra []string
		for name := range found {
			if !known[name] {
				missing = append(missing, name)
			}
		}
		for _, name := range settingNames {
			if !found[name] {
				extra = append(extra, name)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			t.Errorf("the tree reads %v and settingNames does not list them.\nA rule "+
				"about where a setting is named cannot cover a setting this list has "+
				"never heard of.", missing)
		}
		if len(extra) > 0 {
			t.Errorf("settingNames lists %v and nothing in the tree reads them.\nA "+
				"list carrying a name the program stopped using reports this guard as "+
				"covering more than it does.", extra)
		}
	})

	t.Run("every failure that names a setting names it in the Why", func(t *testing.T) {
		fset := token.NewFileSet()
		// CONSTANTS ARE RESOLVED FIRST, and the reason is a green this
		// guard produced before they were. The internal failure names
		// its setting through two hops — Why: internalWhy, and
		// internalWhy concatenates debugEnvVar — so the construction's
		// SOURCE spells no setting at all, and a guard reading source
		// text checked it and found nothing to say. It was not a failure
		// that complied; it was a failure this guard could not see.
		//
		// A structural fix has a field of view, and this one's was
		// "names written as literals at the construction".
		consts := stringConstants(t, goFiles, fset)
		checked := 0
		for _, path := range goFiles {
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", path, err)
			}
			parsed, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				continue
			}
			text := func(n ast.Node) string {
				lo := fset.Position(n.Pos()).Offset
				hi := fset.Position(n.End()).Offset
				if lo < 0 || hi > len(src) || lo > hi {
					return ""
				}
				return string(src[lo:hi])
			}
			pkg := parsed.Name.Name
			ast.Inspect(parsed, func(n ast.Node) bool {
				whole, why, kind, ok := failureParts(n, text)
				if !ok {
					return true
				}
				whole = expandConstants(whole, consts[pkg])
				why = expandConstants(why, consts[pkg])
				checked++
				for _, name := range settingNames {
					if !strings.Contains(whole, name) {
						continue
					}
					if strings.Contains(why, name) {
						continue
					}
					where := fset.Position(n.Pos())
					t.Errorf("%s:%d builds a failure (%s) naming %s, and its Why does "+
						"not.\nThe next-step line is rendered for a terminal and "+
						"replaced by a value on the agent surface, so a setting named "+
						"only there reaches one reader of two — and the one it misses "+
						"is the one that cannot look the value up.\n%s",
						displayPath(root, path), where.Line, kind, name, whole)
				}
				return true
			})
		}
		if checked == 0 {
			t.Fatal("no failure construction was found in the tree, so this guard " +
				"asserted nothing about placement")
		}
		t.Logf("%d failure constructions checked", checked)
	})
}

// failureParts returns the whole source of a ui.Failure construction and
// the source of its Why, for the three shapes this program builds one in.
//
// THE WHY IS POSITIONAL IN TWO OF THEM and named in the third, which is
// why this is a function rather than a field lookup. Quoted has no Why at
// all and says so by returning an empty one — a construction that cannot
// carry the fact is reported rather than skipped.
func failureParts(n ast.Node, text func(ast.Node) string) (whole, why, kind string, ok bool) {
	switch node := n.(type) {
	case *ast.CallExpr:
		name := calleeName(node.Fun)
		switch name {
		case "NewFailure":
			// THE WHY IS ARGUMENT THREE, and it moved there when the
			// constructor gained a family id. This guard reddened the day
			// that happened, which is the behaviour to keep: a positional
			// summary that is wrong about a signature must fail rather
			// than read a neighbouring argument as the one it wanted.
			if len(node.Args) < 3 {
				return "", "", "", false
			}
			return text(node), text(node.Args[2]), "NewFailure", true
		case "Quoted":
			// NO WHY BY CONSTRUCTION. The middle paragraph is somebody
			// else's sentence, so there is nowhere in this shape for a
			// fact of ours to live where both surfaces render it.
			return text(node), "", "Quoted — which has no Why", true
		}
	case *ast.CompositeLit:
		if typeName(node.Type) != "Failure" {
			return "", "", "", false
		}
		for _, elt := range node.Elts {
			kv, isKV := elt.(*ast.KeyValueExpr)
			if !isKV {
				continue
			}
			if ident, isIdent := kv.Key.(*ast.Ident); isIdent && ident.Name == "Why" {
				why = text(kv.Value)
			}
		}
		return text(node), why, "Failure literal", true
	}
	return "", "", "", false
}

// calleeName is the function's own name, with or without a package
// qualifier — the same construction is written ui.NewFailure outside the
// ui package and NewFailure inside it.
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// typeName is the composite literal's type name, qualified or not.
func typeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// stringConstants maps each package's string constants to their values,
// so a construction naming a setting through one is not invisible to the
// row above.
//
// IT ITERATES TO A FIXED POINT because these nest: the internal failure's
// Why is a constant that concatenates another constant, and one pass
// would resolve the outer one to text still carrying the inner one's
// NAME. Three passes is well past the depth this tree uses and the loop
// stops early when nothing moved.
func stringConstants(t *testing.T, paths []string, fset *token.FileSet) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		parsed, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			continue
		}
		pkg := parsed.Name.Name
		if out[pkg] == nil {
			out[pkg] = map[string]string{}
		}
		for _, decl := range parsed.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, name := range value.Names {
					if i >= len(value.Values) {
						continue
					}
					lo := fset.Position(value.Values[i].Pos()).Offset
					hi := fset.Position(value.Values[i].End()).Offset
					if lo < 0 || hi > len(src) || lo > hi {
						continue
					}
					out[pkg][name.Name] = string(src[lo:hi])
				}
			}
		}
	}
	for pkg, names := range out {
		for pass := 0; pass < 3; pass++ {
			moved := false
			for name, value := range names {
				expanded := expandConstants(value, names)
				if expanded != value {
					out[pkg][name] = expanded
					moved = true
				}
			}
			if !moved {
				break
			}
		}
	}
	return out
}

// expandConstants appends the value of every constant the text names, so
// a search for a setting sees through one.
//
// IT APPENDS RATHER THAN REPLACES, because the question this guard asks
// is "does this text CONTAIN the name" and appending cannot lose a name
// the text already spelled.
func expandConstants(text string, names map[string]string) string {
	if text == "" || len(names) == 0 {
		return text
	}
	var b strings.Builder
	b.WriteString(text)
	for name, value := range names {
		if name == "_" || value == "" {
			continue
		}
		if strings.Contains(text, name) {
			b.WriteString(" ")
			b.WriteString(value)
		}
	}
	return b.String()
}
