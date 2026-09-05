// Package guard holds this repository's mechanically-enforced versions of
// three of the hard rules this public repo states in its own CLAUDE.md:
// no AWS SDK or telemetry dependency, no compiled-in hostname beyond a
// small and explicitly counted allowance, and no private citation in a
// source comment. Each is a test, run by `go test ./...`, so a violation
// fails at the moment it is introduced rather than at review.
//
// Every guard here reads real state — the module's own dependency graph,
// its own source files, and its own citation-pattern manifest —
// rather than a hand-copied approximation of any of them, so a change to
// any of those three things is what the guard actually reacts to.
package guard

import (
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
// The stated LIMIT of guard 3's view, so the next reader does not have
// to rediscover it: it sees ".go" files only. A citation in a workflow
// YAML, a Makefile or a shell script ships past it. That gap is real and
// is covered by the leak scan, which reads the same manifest over the
// whole tree and the git history.
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
	cmd := exec.Command("go", "list", "-e", "-deps", "./...")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()

	var offenders []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for _, banned := range bannedDependencyFragments {
			if strings.Contains(line, banned) {
				offenders = append(offenders, line)
				break
			}
		}
	}

	if len(offenders) > 0 {
		t.Errorf("dependency graph names a banned import path: %s\n"+
			"This repo never imports an AWS SDK or a telemetry/analytics client — "+
			"the client speaks only the public HTTP API and phones home to nobody.",
			strings.Join(offenders, "; "))
		return
	}

	if err != nil {
		t.Fatalf("go list -e -deps ./... failed with no banned dependency in its output "+
			"(a genuine build problem, not this guard): %v\n%s", err, out)
	}
}

// ---------------------------------------------------------------------
// Guard 2: no compiled-in hostname beyond a small, counted allowance.
// ---------------------------------------------------------------------

var urlLiteralPattern = regexp.MustCompile(`^https?://`)

// maxNamedURLConstants is the guard's own stated ceiling, not a count
// read from anywhere else — a guard whose allowance can be widened by
// editing the same file that trips it is not a guard.
const maxNamedURLConstants = 2

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
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			unquoted, err := strconv.Unquote(lit.Value)
			if err != nil || !urlLiteralPattern.MatchString(unquoted) {
				return true
			}
			pos := fset.Position(lit.Pos())
			loc := fmt.Sprintf("%s:%d", displayPath(root, path), pos.Line)
			if name, isNamed := namedLits[lit]; isNamed {
				named = append(named, fmt.Sprintf("%s = %q (%s)", name, unquoted, loc))
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

// TestNoPrivateCitations scans every Go source file's raw text — TESTS
// INCLUDED, and not only string literals, since a citation is at least as
// likely to live in a comment as in a value — against every pattern the
// manifest declares, and fails naming the file, line and pattern that
// matched.
func TestNoPrivateCitations(t *testing.T) {
	root := moduleRoot(t)
	patterns := loadCitationPatterns(t, root)

	for _, path := range goFiles(t, root, true) {
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
