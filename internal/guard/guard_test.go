// Package guard holds this repository's mechanically-enforced versions of
// the hard rules this public repo states in its own CLAUDE.md: no AWS SDK
// or telemetry dependency, no compiled-in hostname beyond a small and
// explicitly named allowance, no private citation in any file, and no
// unexported struct field holding a Secret. Each is a test, so a
// violation fails at the moment it is introduced rather than at review.
//
// A NOTE ON RUNNING THEM. These guards read state Go does not track as an
// input to this package — the whole module's source tree, its dependency
// graph, a manifest file. Go's test cache therefore does not know when
// their answer has changed, and a cached PASS stays valid while the tree
// underneath it starts violating the rule. That is measured, not
// theorised: a banned import added elsewhere went undetected on a cached
// run and failed instantly without the cache. The Makefile runs
// `go test -count=1` for exactly this reason, and anyone invoking these
// by hand should too. **A guard that can report a stale pass is worse
// than no guard, because it is trusted.**
//
// Every guard here reads real state — the module's own dependency graph,
// its own source files, and its own citation-pattern manifest —
// rather than a hand-copied approximation of any of them, so a change to
// any of those three things is what the guard actually reacts to.
package guard

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// moduleRoot walks up from the test binary's working directory (which
// `go test` sets to this package's own source directory) until it finds
// the module's go.mod. Every guard below needs the whole module's source
// tree, not just this one package's.
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

// goFiles returns every ".go" file under root, skipping version control
// and vendor directories. When includeTests is false it also skips files
// ending in "_test.go".
//
// The two guards below want DIFFERENT universes, and the difference is
// the point rather than an inconsistency:
//
//   - Guard 2 (hostnames) scans non-test sources only. Test fixtures
//     legitimately contain URLs — a localhost fixture is the very string
//     a check searches for — so including them would red the guard on
//     its first honest day, and a guard that cries wolf on day one is a
//     guard somebody disables.
//   - Guard 3 (citations) scans EVERYTHING, tests included. A _test.go
//     file in this repository is exactly as world-readable as a shipped
//     one, and there is no legitimate reason for a test to contain a
//     private section number or task id. Scoping it to non-test sources
//     was this guard's original blind spot, proved by putting a citation
//     in a _test.go file and watching the guard stay green.
//
// The general form is on the record three times already: a structural
// fix has a field of view, and the field of view is itself an unguarded
// assumption.
//
// Guard 3 no longer uses this helper at all — it walks EVERY file, not
// only Go sources, via allTextFiles below. Scoping it to ".go" left a
// citation in a workflow YAML, a Makefile or a shell script shipping
// past it, and every one of those is as world-readable as a source file.
func goFiles(t *testing.T, root string, includeTests bool) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if !includeTests && strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(files) == 0 {
		t.Fatal("no .go files found under the module root — this guard would silently pass")
	}
	sort.Strings(files)
	return files
}

// allTextFiles returns every text file under root that ships in this
// repository. Guard 3's threat is a world-readable FILE disclosing
// private paper, and "world-readable" has nothing to do with the
// extension — so the extension is not a filter here.
//
// What is skipped, and why each is not a hole:
//
//   - .git, vendor and bin: not authored content. bin holds build output
//     that is gitignored and never published.
//   - The citation manifest itself. It necessarily contains text shaped
//     like the citations it hunts. Today's patterns are written so they
//     do not match themselves, but that is a property of how they happen
//     to be spelled, and a future pattern carrying a literal example
//     would red the guard against its own rule file. Excluded
//     deliberately, and named here so the exclusion is a decision rather
//     than a silent gap.
//   - Binary files, detected by a NUL byte rather than by extension, so
//     an unfamiliar binary format is skipped on evidence instead of on a
//     list somebody has to maintain.
func allTextFiles(t *testing.T, root string) []string {
	t.Helper()
	manifest := filepath.Join(root, "scripts", "citation-patterns.txt")
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if path == manifest {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		probe := data
		if len(probe) > 8192 {
			probe = probe[:8192]
		}
		if bytes.IndexByte(probe, 0) >= 0 {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(files) == 0 {
		t.Fatal("no text files found under the module root — this guard would silently pass")
	}
	sort.Strings(files)
	return files
}

// displayPath renders path relative to root for a readable failure
// message, falling back to the absolute path if that fails for some
// reason (it never should, since every path here came from walking root).
func displayPath(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return rel
	}
	return path
}

// ---------------------------------------------------------------------
// Guard 1: no AWS SDK, no telemetry/analytics dependency.
// ---------------------------------------------------------------------

// bannedDependencyFragments are import-path substrings this guard refuses
// to find anywhere in the module's dependency graph: an AWS SDK (the
// client speaks only the public HTTP API, never AWS directly) or a
// telemetry/analytics vendor (no phone-home, ever — see CLAUDE.md's hard
// don'ts).
var bannedDependencyFragments = []string{
	"aws-sdk",
	"aws/smithy",
	"segment.com",
	"segmentio",
	"mixpanel",
	"amplitude",
	"google-analytics",
	"getsentry",
	"sentry-go",
	"posthog",
	"datadoghq",
	"honeycomb.io",
	"newrelic",
	// Telemetry that arrives as a "standard" rather than as a vendor,
	// which is how it gets waved through. OpenTelemetry is a
	// phone-home whatever the governance model, and it was the gap a
	// review found in the vendor-name-only list above.
	"opentelemetry.io",
	"opencensus.io",
	"go.elastic.co/apm",
	"bugsnag",
	"rollbar",
	"logrocket",
}

// TestNoBannedDependencies walks the module's full dependency graph and
// fails naming any import path that matches a banned fragment.
//
// It uses `go list -e -deps`, not a plain `go list -deps`: -e reports the
// import path of a package it cannot resolve instead of aborting the
// whole command, which matters for the mutation this guard is proved
// against — a scratch source importing a dependency this module has
// never fetched fails to resolve, and without -e that failure would hide
// the very import path this test needs to see.
func TestNoBannedDependencies(t *testing.T) {
	root := moduleRoot(t)

	// Two sources, because neither sees what the other does.
	//
	//   - `go list -deps -test ./...` is the package graph INCLUDING test
	//     imports. Without -test, a banned client imported only from a
	//     _test.go file is invisible — and a dependency added "just for a
	//     test" is in go.sum, in the module cache, and one careless import
	//     away from the shipped binary.
	//   - `go list -m all` is the MODULE graph, which still names a
	//     requirement nothing imports yet. A go.mod line is the commitment;
	//     the import is a formality that follows.
	sources := []struct {
		label string
		args  []string
	}{
		{"package graph (tests included)", []string{"list", "-e", "-deps", "-test", "./..."}},
		{"module requirements", []string{"list", "-m", "all"}},
	}

	var offenders []string
	var hardErr error
	var hardOut []byte

	for _, src := range sources {
		cmd := exec.Command("go", src.args...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			for _, banned := range bannedDependencyFragments {
				if strings.Contains(line, banned) {
					offenders = append(offenders, fmt.Sprintf("%s: %s", src.label, line))
					break
				}
			}
		}
		if err != nil && hardErr == nil {
			hardErr, hardOut = err, out
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("dependency graph names a banned import path: %s\n"+
			"This repo never imports an AWS SDK or a telemetry/analytics client — "+
			"the client speaks only the public HTTP API and phones home to nobody.",
			strings.Join(offenders, "; "))
		return
	}

	if hardErr != nil {
		t.Fatalf("a `go list` invocation failed with no banned dependency in its output "+
			"(a genuine build problem, not this guard): %v\n%s", hardErr, hardOut)
	}
}

// ---------------------------------------------------------------------
// Guard 2: no compiled-in hostname beyond a small, counted allowance.
// ---------------------------------------------------------------------

// urlBearingPattern matches a literal that carries a scheme separator
// ANYWHERE, not only at the start. Anchoring it to the start was a real
// gap twice over: a struct tag renders as `endpoint:"https://host"`, so
// the literal begins with the tag key, and a URL split across a
// concatenation puts the separator mid-literal.
var urlBearingPattern = regexp.MustCompile(`://`)

// schemeFragmentPattern matches a literal that is nothing but the front
// of a URL. On its own it is harmless; as an operand of a `+` it is a
// host being assembled out of pieces small enough that no single one
// looks like a URL — `"http" + "s://" + host`.
var schemeFragmentPattern = regexp.MustCompile(`^https?:?/{0,2}$`)

// maxNamedURLConstants is the guard's own stated ceiling, not a count
// read from anywhere else — a guard whose allowance can be widened by
// editing the same file that trips it is not a guard.
const maxNamedURLConstants = 2

// allowedURLConstants pins the VALUE of every permitted named URL
// constant, because a ceiling of two says nothing about WHICH two.
// Counting alone, swapping the API base for an attacker's host and
// leaving the count at two passed — the guard's name promised a
// protection its body did not provide.
//
// A constant not listed here fails even if the count is within budget.
// Adding a line is a deliberate, reviewable act in a public repo, which
// is the whole difference between an allowance and a hole.
//
// The site base domain has no entry on purpose: it is a bare domain with
// no scheme, so it is not a URL literal and never reaches this map.
var allowedURLConstants = map[string]string{
	"https://api.curious.pub": "the default control-plane API base",
}

// TestAtMostTwoNamedURLConstants parses every non-test source file's AST
// and classifies each http(s) string literal it finds into one of two
// buckets:
//
//   - a BARE literal — a URL written inline as a function argument, a
//     struct field, a local variable, anything other than the sole value
//     of a single-name, top-level const or var — fails immediately,
//     regardless of how many exist. A URL a reader cannot find by
//     grepping for one declaration is the shape a phone-home takes.
//   - a NAMED constant — the sole value of a single-name, top-level const
//     or var — is allowed, but at most two of them total across the
//     whole module: the default API base and the site base domain the
//     client assembles a published URL from.
//
// Scanning the AST rather than the raw text is what keeps a URL inside a
// comment or a doc string out of scope, without needing to say so
// anywhere: go/ast represents a comment as its own node kind, never as a
// *ast.BasicLit, so nothing here ever visits one.
func TestAtMostTwoNamedURLConstants(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()

	var bare []string
	var named []string
	var unapproved []string
	var split []string

	for _, path := range goFiles(t, root, false) {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}

		// Every BasicLit that is the sole value of a single-name,
		// top-level const or var declaration — the shape a URL must have
		// to be a "named constant" rather than a bare literal.
		namedLits := map[*ast.BasicLit]string{}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != 1 || len(vs.Values) != 1 {
					continue
				}
				if lit, ok := vs.Values[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					namedLits[lit] = vs.Names[0].Name
				}
			}
		}

		ast.Inspect(f, func(n ast.Node) bool {
			// A URL assembled from pieces: flag the concatenation itself,
			// since no single operand looks like a URL.
			if be, ok := n.(*ast.BinaryExpr); ok && be.Op == token.ADD {
				for _, operand := range []ast.Expr{be.X, be.Y} {
					l, ok := operand.(*ast.BasicLit)
					if !ok || l.Kind != token.STRING {
						continue
					}
					uq, err := strconv.Unquote(l.Value)
					if err != nil || !schemeFragmentPattern.MatchString(uq) {
						continue
					}
					pos := fset.Position(l.Pos())
					split = append(split, fmt.Sprintf("%q (%s:%d)",
						uq, displayPath(root, path), pos.Line))
				}
				return true
			}

			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			unquoted, err := strconv.Unquote(lit.Value)
			if err != nil || !urlBearingPattern.MatchString(unquoted) {
				return true
			}
			pos := fset.Position(lit.Pos())
			loc := fmt.Sprintf("%s:%d", displayPath(root, path), pos.Line)
			if name, isNamed := namedLits[lit]; isNamed {
				named = append(named, fmt.Sprintf("%s = %q (%s)", name, unquoted, loc))
				if _, allowed := allowedURLConstants[unquoted]; !allowed {
					unapproved = append(unapproved,
						fmt.Sprintf("%s = %q (%s)", name, unquoted, loc))
				}
			} else {
				bare = append(bare, fmt.Sprintf("%q (%s)", unquoted, loc))
			}
			return true
		})
	}

	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("found a compiled-in URL that is not a single named top-level "+
			"const or var: %s\n"+
			"Every http(s) URL in a non-test source must be declared as one named "+
			"constant, not written inline — see CLAUDE.md's hard don'ts.",
			strings.Join(bare, "; "))
	}

	if len(split) > 0 {
		sort.Strings(split)
		t.Errorf("found a URL being assembled from scheme fragments: %s\n"+
			"A host split across a concatenation is still a compiled-in host, "+
			"and splitting it is how one gets past a guard that only reads whole "+
			"literals. Declare the URL as one named constant.",
			strings.Join(split, "; "))
	}

	if len(unapproved) > 0 {
		sort.Strings(unapproved)
		t.Errorf("found a named URL constant whose VALUE is not approved: %s\n"+
			"The ceiling of %d says how many, not which. Every named URL constant "+
			"must appear in allowedURLConstants with its exact value — otherwise "+
			"replacing the API base with another host passes while the count is "+
			"unchanged.",
			strings.Join(unapproved, "; "), maxNamedURLConstants)
	}

	if len(named) > maxNamedURLConstants {
		sort.Strings(named)
		t.Errorf("found %d named URL constants, want at most %d: %s\n"+
			"This repo allows exactly two: the default API base and the site base "+
			"domain the client assembles a published URL from. A third is a widened "+
			"guard, and a guard loosened by the thing that trips it is not a guard.",
			len(named), maxNamedURLConstants, strings.Join(named, "; "))
	}
}

// ---------------------------------------------------------------------
// Guard 3: no private citation in a source comment.
// ---------------------------------------------------------------------

// loadCitationPatterns reads and compiles every pattern in the committed
// manifest. It genuinely reads the file on every run rather than caching
// a copy of its contents in this package: the required proof for this
// guard is that deleting a line from the manifest measurably changes what
// the guard can see, and a guard that read its own hardcoded copy of the
// patterns would fail that proof while still going green on the day
// nobody expects it to.
func loadCitationPatterns(t *testing.T, root string) []*regexp.Regexp {
	t.Helper()
	path := filepath.Join(root, "scripts", "citation-patterns.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var patterns []*regexp.Regexp
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		re, err := regexp.Compile(line)
		if err != nil {
			t.Fatalf("scripts/citation-patterns.txt:%d: invalid pattern %q: %v", i+1, line, err)
		}
		patterns = append(patterns, re)
	}
	if len(patterns) == 0 {
		t.Fatal("scripts/citation-patterns.txt contains no patterns — this guard would silently pass")
	}
	return patterns
}

// TestNoPrivateCitations scans EVERY text file this repository ships —
// Go sources, tests, the Makefile, the CI workflow, shell scripts,
// markdown — against every pattern the manifest declares, and fails
// naming the file, line and pattern that matched. Not only string
// literals: a citation is at least as likely to live in a comment.
//
// The scope is the whole tree because the threat is that a
// world-readable file discloses private paper, and every file here is
// world-readable. A guard's scope follows its threat, not the scope of
// whatever guard sits next to it in the file.
func TestNoPrivateCitations(t *testing.T) {
	root := moduleRoot(t)
	patterns := loadCitationPatterns(t, root)

	for _, path := range allTextFiles(t, root) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for lineNum, line := range strings.Split(string(data), "\n") {
			for _, re := range patterns {
				if m := re.FindString(line); m != "" {
					t.Errorf("%s:%d matches citation pattern %q (matched %q): %q\n"+
						"This repo states a conclusion and its reasoning, never the private "+
						"document either came from — rewrite the line instead of citing it; "+
						"see CLAUDE.md's public-conclusions rule.",
						displayPath(root, path), lineNum+1, re.String(), m, strings.TrimSpace(line))
				}
			}
		}
	}
}

// ---------------------------------------------------------------------
// Guard 4: no unexported struct field holds a Secret.
// ---------------------------------------------------------------------

// TestNoUnexportedSecretFields fails on any struct field that is
// unexported AND typed Secret, because that combination defeats the
// redaction and no method on the type can prevent it.
//
// The mechanism, measured rather than assumed: fmt cannot call a method
// on a value it reached by reflecting an UNEXPORTED field, so String,
// GoString and Format are all skipped and the underlying string is
// printed. `%v`, `%+v`, `%#v` and Sprint on the containing struct each
// print the real secret. Making Secret an opaque struct does not fix it
// either — the same reflection prints the inner field.
//
// So the type provably cannot defend itself here, and the only place the
// rule can live is a guard. That is the whole reason this one exists:
// every other guard in this file enforces a rule the code could in
// principle follow by accident, and this one enforces a rule the
// language gives no way to express.
//
// SCOPE, and its threat: non-test sources only. The harm is a secret
// reaching a user's terminal or a log line from shipped code. A test's
// own output is not shipped — and internal/ui's redaction suite must
// construct exactly this shape to pin the limit it documents, so
// including tests would red the guard against the test that proves the
// thing the guard is for. The remaining exposure is a test printing a
// secret into CI output, which is a smaller and differently-shaped risk.
func TestNoUnexportedSecretFields(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()

	var offenders []string
	for _, path := range goFiles(t, root, false) {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		inUIPackage := f.Name.Name == "ui"

		ast.Inspect(f, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			for _, field := range st.Fields.List {
				if !isSecretType(field.Type, inUIPackage) {
					continue
				}
				for _, name := range field.Names {
					if name.IsExported() || name.Name == "_" {
						continue
					}
					pos := fset.Position(name.Pos())
					offenders = append(offenders, fmt.Sprintf("%s (%s:%d)",
						name.Name, displayPath(root, path), pos.Line))
				}
			}
			return true
		})
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("found an UNEXPORTED struct field holding a Secret: %s\n"+
			"fmt cannot call a method on a value reached by reflecting an "+
			"unexported field, so the redaction is skipped and %%v on the "+
			"containing struct prints the real secret. Export the field, or "+
			"do not store a Secret in this struct.",
			strings.Join(offenders, "; "))
	}
}

// isSecretType reports whether expr names ui.Secret — written as
// `ui.Secret` from outside the package, or as a bare `Secret` from
// inside it. Pointers and slices of it count: the field still holds the
// value and reflection still reaches through.
func isSecretType(expr ast.Expr, inUIPackage bool) bool {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return isSecretType(e.X, inUIPackage)
	case *ast.ArrayType:
		return isSecretType(e.Elt, inUIPackage)
	case *ast.Ident:
		return inUIPackage && e.Name == "Secret"
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		return ok && pkg.Name == "ui" && e.Sel.Name == "Secret"
	}
	return false
}
