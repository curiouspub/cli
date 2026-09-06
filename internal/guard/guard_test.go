// Package guard holds this repository's mechanically-enforced versions of
// the hard rules this public repo states in its own CLAUDE.md: no
// infrastructure-provider SDK and no telemetry dependency, no
// compiled-in hostname beyond a small and
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
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
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
// Guard 3 no longer uses this helper at all — it scans every file the
// repository publishes, not only Go sources, via publishedTextFiles
// below. Scoping it to ".go" left a citation in a markdown document, a
// workflow YAML, a Makefile or a shell script shipping past it, and
// every one of those is as world-readable as a source file.
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

// publishedTextFiles returns every text file this repository PUBLISHES:
// every file git tracks, plus every untracked file no ignore rule
// covers. Guard 3's threat is a world-readable file disclosing private
// paper, and "world-readable" has nothing to do with a file's extension
// — so the extension is not a filter here. A markdown document, the
// Makefile, a workflow YAML and a shell script are all exactly as public
// as a .go file, and every one of them is in scope.
//
// ASKING GIT rather than walking the filesystem is the load-bearing
// half, and it is here because of a measurement rather than an argument.
// A filesystem walk cannot tell a file that ships from one that provably
// never will — and this repository now grows the second kind ON PURPOSE.
// CLAUDE.md is split in two: a public half, which is product surface for
// anyone reading the repo, and a gitignored CLAUDE.local.md holding the
// orchestration half, which exists precisely to carry the internal
// identifiers and document references this guard hunts. Under a
// filesystem walk that file reds `make ci` on the day it lands and every
// day after, and the quickest way out of a permanently red gate is to
// loosen the pattern that catches real leaks. Measured before this
// change was written: a gitignored CLAUDE.local.md holding two
// identifiers failed this guard twice, naming a file it has no business
// reading.
//
// So the universe is now the right one on its own terms — not "files on
// this disk" but "files this repository publishes":
//
//   - TRACKED files are in scope because they are already public.
//   - UNTRACKED, UNIGNORED files are in scope because they are one
//     `git add` away from being public, and a guard that waited for the
//     add would find the violation after its author stopped looking.
//   - IGNORED files are out of scope, because git will not publish them
//     — which is the same property that makes it safe to keep private
//     paper in one.
//
// This also retires two skips that used to be name-based: `.git` is
// never listed by git at all, and `bin/` is ignored, so both now leave
// scope by the rule rather than by a list somebody has to maintain.
//
// What is still skipped, and why neither is a hole:
//
//   - vendor/: third-party source, not authored here. Nothing in it can
//     cite this project's private paper.
//   - Binary files, detected by a NUL byte rather than by extension, so
//     an unfamiliar binary format is skipped on evidence instead of on a
//     list. STATED LIMIT: a UTF-16 text file is full of NUL bytes and is
//     therefore skipped as binary. Nothing here is UTF-16 today; Windows
//     tooling writes it, so if one ever arrives that way it is outside
//     this guard and inside the leak scan's.
//
// Two further stated limits. File CONTENTS are matched, never file
// NAMES: a branch name, a commit message, a tag or a pull-request title
// is a published surface no pattern here can see, and those are
// hand-checked at the publish point. And `--exclude-standard` honours
// the operator's GLOBAL ignore file as well as this repository's, so a
// personal global rule could in principle drop a real file out of scope
// on one machine — CI checks out clean with no global excludes, which is
// where the answer that counts is produced.
func publishedTextFiles(t *testing.T, root string) []string {
	t.Helper()

	// -z, so a path containing a newline cannot split into two entries.
	// --cached is the tracked set; --others adds untracked files and
	// --exclude-standard subtracts everything the ignore rules cover.
	cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files in %s: %v\n"+
			"This guard cannot establish what the repository publishes, so it fails "+
			"rather than report a pass over a set it never determined.", root, err)
	}

	var files []string
	seen := make(map[string]bool)
	for _, name := range strings.Split(string(out), "\x00") {
		// An unmerged path is listed once per stage, so dedupe rather
		// than scan the same file three times mid-conflict.
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if name == "vendor" || strings.HasPrefix(name, "vendor/") {
			continue
		}

		path := filepath.Join(root, filepath.FromSlash(name))
		info, statErr := os.Lstat(path)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				// Tracked in the index, deleted in the working tree.
				// There is no content to scan, and that is not a
				// violation.
				continue
			}
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if !info.Mode().IsRegular() {
			// A symlink or a submodule directory: nothing to read here,
			// and following either would scan content this repository
			// does not author.
			continue
		}

		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("reading %s: %v", path, readErr)
		}
		probe := data
		if len(probe) > 8192 {
			probe = probe[:8192]
		}
		if bytes.IndexByte(probe, 0) >= 0 {
			continue
		}
		files = append(files, path)
	}

	if len(files) == 0 {
		t.Fatal("git listed no publishable text file under the module root — this guard would silently pass")
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
// Guard 1: no provider SDK, no telemetry/analytics dependency.
// ---------------------------------------------------------------------

// loadBannedDependencies reads the committed denylist of import-path
// substrings this module refuses to carry. Like the citation manifest, it
// is READ on every run and never cached in this package: the proof that a
// rule file is load bearing is that deleting a line measurably changes
// what the guard can see, and a guard consulting its own hardcoded copy
// would pass that check while failing to do it.
//
// It moved out of this file 2026-09-06 for a second reason, and the
// second one is why it is a file rather than a var. The denylist has to
// SPELL the vendor names it forbids — you cannot match a module path
// without writing it — and the citation guard now forbids those same
// names in authored text. Two rules in one repository, each correct,
// pointing opposite ways at the same string. A rule file resolves it the
// way the citation manifest already did: its DATA lines are exempt from
// the citation scan because a rule cannot name what it forbids without
// writing it down, and its COMMENT lines are scanned like any other
// prose, so the exemption buys exactly the lines that need it and not one
// more.
func loadBannedDependencies(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "scripts", "banned-dependencies.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var fragments []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// A data line is a bare import-path substring. Neither whitespace
		// nor "#" is legal in an import path, so either one means somebody
		// wrote a trailing comment — and a trailing comment does not
		// terminate a line here, it becomes PART of the fragment. The
		// fragment then matches nothing and the ban is silently off, with
		// the guard still green. Found by a reviewer within hours of this
		// file being created, against a real banned import: baseline red,
		// one inline comment later, green.
		if strings.ContainsAny(line, " \t#") {
			t.Fatalf("scripts/banned-dependencies.txt: %q is not a bare import-path fragment "+
				"(whitespace or # present). A trailing comment silently disables the ban it "+
				"is attached to; put the comment on its own line.", line)
		}
		fragments = append(fragments, strings.ToLower(line))
	}
	if len(fragments) == 0 {
		t.Fatal("scripts/banned-dependencies.txt lists no fragments — this guard would silently pass")
	}
	return fragments
}

// Matching is case-INSENSITIVE, and two entries above earn it. The list
// once carried "datadoghq" and "honeycomb.io", which match no real
// import path: the modules are github.com/DataDog/... and
// github.com/honeycombio/..., so both clients compiled in with the guard
// green. A fragment nobody has checked against a real path is a line of
// reassurance, not a check.

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
	bannedDependencyFragments := loadBannedDependencies(t, root)

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
		// GOWORK=off, and it is load bearing. `go list -m all` inherits
		// the environment, so an operator standing in a Go workspace that
		// also includes the private server repository got THAT module's
		// requirements reported as this one's — twenty-three
		// provider-SDK lines belonging to a module that is entitled to
		// them, a confident failure, and nothing wrong with this repo.
		// A false red on the exact cross-repo session the workspace rules
		// prescribe is how a guard gets switched off. The subject here is
		// this module's own go.mod, never whatever workspace it is being
		// read from.
		cmd.Env = append(os.Environ(), "GOWORK=off")
		out, err := cmd.CombinedOutput()
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			lower := strings.ToLower(line)
			for _, banned := range bannedDependencyFragments {
				if strings.Contains(lower, strings.ToLower(banned)) {
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
			"This repo never imports an infrastructure-provider SDK or a "+
			"telemetry/analytics client — it speaks this project's public HTTP "+
			"API and phones home to nobody.",
			strings.Join(offenders, "; "))
		return
	}

	if hardErr != nil {
		t.Fatalf("a `go list` invocation failed with no banned dependency in its output "+
			"(a genuine build problem, not this guard): %v\n%s", hardErr, hardOut)
	}
}

// ---------------------------------------------------------------------
// Guard 2: no compiled-in host beyond a small, explicitly named
// allowance — as a URL literal, as a bare hostname, or embedded.
// ---------------------------------------------------------------------
//
// WHAT THIS GUARD DOES NOT SEE, stated because the heading used to
// promise more than the body measured:
//
//   - A URL built at RUN TIME from parts that are individually innocent:
//     (&url.URL{Scheme: "https", Host: h}).String(), strings.Join of
//     scheme and host, fmt.Sprintf with the separator in the format.
//     The bare-hostname check below is what catches the HOST in those,
//     which is the part that matters; the scheme is not the secret.
//   - Anything reached over the network or read from a file at run time.
//   - Test files, deliberately: fixtures legitimately contain URLs, and
//     a test binary is not shipped.
//   - A bare hostname whose final label is outside the short TLD list
//     below. That list is short on purpose — this repository is full of
//     dotted FILENAMES, and matching "any dotted word" would red on
//     go.mod and package.json. A reserved documentation TLD is also
//     outside it, which is harmless: such a name does not resolve.

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

// allowedHosts is the same list the leak scan enforces, and it exists
// because a hostname needs no scheme to be a compiled-in host: a bare
// `const tracker = "collect.example"` passed every URL-shaped check here
// while being exactly the thing they are for.
//
// Kept as ONE list with the leak scan rather than two that drift.
var allowedHosts = map[string]bool{
	"api.curious.pub": true,
	"curious.pub":     true,
	"curiously.dev":   true,
	"github.com":      true,
}

// hostLiteralPattern matches a bare dotted hostname. The final label
// must be one of a short list of real TLDs rather than "any letters",
// because this repository is full of dotted FILENAMES — go.mod,
// package.json, astro.config.mjs — and a guard that reds on those is one
// somebody deletes on its first afternoon.
var hostLiteralPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\.(com|dev|pub|io|net|org)$`)

// embedDirectivePattern finds a //go:embed directive's patterns, so the
// files a binary carries are scanned for hosts too. Without this, a URL
// in an embedded text file shipped inside the binary with nothing in
// `make ci` seeing a scheme anywhere.
var embedDirectivePattern = regexp.MustCompile(`^\s*//go:embed\s+(.+)$`)

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
	var hosts []string
	var embedded []string

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
			if err != nil {
				return true
			}
			if !urlBearingPattern.MatchString(unquoted) {
				if hostLiteralPattern.MatchString(unquoted) && !allowedHosts[unquoted] {
					pos := fset.Position(lit.Pos())
					hosts = append(hosts, fmt.Sprintf("%q (%s:%d)",
						unquoted, displayPath(root, path), pos.Line))
				}
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

	// Embedded files ship inside the binary; scan what they carry.
	for _, path := range goFiles(t, root, false) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			m := embedDirectivePattern.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			for _, pat := range strings.Fields(m[1]) {
				pat = strings.Trim(pat, `"`)
				matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), pat))
				for _, target := range matches {
					body, err := os.ReadFile(target)
					if err != nil {
						continue
					}
					if urlBearingPattern.Match(body) {
						embedded = append(embedded, fmt.Sprintf("%s (embedded by %s)",
							displayPath(root, target), displayPath(root, path)))
					}
				}
			}
		}
	}

	if len(embedded) > 0 {
		sort.Strings(embedded)
		t.Errorf("found a URL inside an EMBEDDED file: %s\n"+
			"go:embed puts the file's bytes in the shipped binary, so a host in "+
			"there is as compiled-in as one in a source literal — and no literal "+
			"scan sees it.", strings.Join(embedded, "; "))
	}

	if len(hosts) > 0 {
		sort.Strings(hosts)
		t.Errorf("found a compiled-in HOSTNAME that is not on the allowed list: %s\n"+
			"A hostname needs no scheme to be a host. Allowed: api.curious.pub, "+
			"curious.pub, curiously.dev, github.com — the same list the leak scan "+
			"enforces.", strings.Join(hosts, "; "))
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

// loadVendorTerms reads the provider and service vocabulary this
// repository may not name in authored text. Read on every run, no cached
// copy, for the reason every rule file here is: deleting a line has to
// measurably change what the guard can see.
func loadVendorTerms(t *testing.T, root string) map[string]bool {
	t.Helper()
	path := filepath.Join(root, "scripts", "vendor-terms.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	terms := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		terms[strings.ToLower(line)] = true
	}
	if len(terms) == 0 {
		t.Fatal("scripts/vendor-terms.txt lists no terms — this guard would silently pass")
	}
	return terms
}

// splitSubwords splits one alphanumeric run the way an identifier is
// actually built: an acronym run, a capitalised word, a lowercase word,
// or a bare digit run. Digits stay attached to the letters they follow,
// so a name ending in a digit survives as one subword.
//
// Written by hand rather than as a pattern because the natural
// expression for the acronym boundary needs a negative lookahead, and
// RE2 — Go's engine, chosen for its linear-time guarantee — does not
// have one. The first version of this used one and panicked at init.
func splitSubwords(run string) []string {
	isUpper := func(b byte) bool { return b >= 'A' && b <= 'Z' }
	isLower := func(b byte) bool { return b >= 'a' && b <= 'z' }
	isDigit := func(b byte) bool { return b >= '0' && b <= '9' }

	var out []string
	for i := 0; i < len(run); {
		start := i
		switch {
		case isUpper(run[i]):
			for i < len(run) && isUpper(run[i]) {
				i++
			}
			// An uppercase run followed by lowercase is an acronym whose
			// last letter opens the next word: a run then a capitalised
			// word splits between them, not after them.
			if i-start > 1 && i < len(run) && isLower(run[i]) {
				i--
			}
			for i < len(run) && (isLower(run[i]) || isDigit(run[i])) {
				i++
			}
		case isLower(run[i]):
			for i < len(run) && (isLower(run[i]) || isDigit(run[i])) {
				i++
			}
		default:
			for i < len(run) && isDigit(run[i]) {
				i++
			}
		}
		out = append(out, run[start:i])
	}
	return out
}

// alphanumericRun finds the maximal runs a line is tokenised from.
var alphanumericRun = regexp.MustCompile(`[A-Za-z0-9]+`)

// identifierTokens returns every whole subword of a line, plus every
// CONTIGUOUS JOIN of adjacent subwords.
//
// This is the whole of why the vendor rule is not a regular expression,
// and both halves are load bearing.
//
// SPLITTING is what catches the real spellings. A word-boundary pattern
// sees no boundary inside an identifier, so every camelCase and
// snake_case spelling of a forbidden name walked straight past the
// pattern that replaced it — which is how the rule shipped evadable in
// the first place.
//
// JOINING is what catches a name that is itself split by the convention:
// a two-part product name written in camelCase arrives as two subwords
// and matches neither, until the adjacent pair is rejoined.
//
// And matching a whole subword rather than a SUBSTRING is what keeps the
// guard quiet: an ordinary English word for a defect contains one of
// these terms outright, and a substring match reds on it. Splitting
// distinguishes an identifier that NAMES a provider from a word that
// merely contains those letters.
func identifierTokens(line string) map[string]bool {
	out := map[string]bool{}
	for _, run := range alphanumericRun.FindAllString(line, -1) {
		subs := splitSubwords(run)
		for i := range subs {
			joined := ""
			for j := i; j < len(subs); j++ {
				joined += strings.ToLower(subs[j])
				out[joined] = true
			}
		}
	}
	return out
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
	vendorTerms := loadVendorTerms(t, root)

	// RULE FILES: files whose job is to name what the repository forbids.
	// Their DATA lines are exempt from this scan and their COMMENT lines
	// are not, which is the narrowest exemption that works — a rule cannot
	// name what it forbids without writing it down, but the prose
	// explaining a rule has no such need and is scanned like any other.
	//
	// There are two, and the second one is here because two correct rules
	// pointed opposite ways at the same string: the dependency denylist
	// must spell vendor module paths to match them, and the citation
	// manifest now forbids those vendor names in authored text. Without
	// this, the repository reds against itself and the fastest way out is
	// to delete one of the two rules.
	ruleFiles := map[string]bool{
		filepath.Join(root, "scripts", "citation-patterns.txt"):   true,
		filepath.Join(root, "scripts", "banned-dependencies.txt"): true,
		filepath.Join(root, "scripts", "vendor-terms.txt"):        true,
	}
	for _, path := range publishedTextFiles(t, root) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		isRuleFile := ruleFiles[path]
		for lineNum, line := range strings.Split(string(data), "\n") {
			// A rule file's DATA line is exempt from the VENDOR check
			// only — never from the patterns. That narrowness is the
			// point, and it was the reviewer's remedy rather than the
			// author's: the dependency denylist has to spell provider
			// module paths to match them, so the vendor vocabulary is a
			// genuine collision. No other pattern has any legitimate
			// reason to match a denylist entry, so no other pattern is
			// waived. An exemption drawn file-wide would have let a
			// private identifier ship on a data line, which was measured
			// rather than argued.
			isRuleData := isRuleFile && !strings.HasPrefix(strings.TrimSpace(line), "#")
			if !isRuleData {
				for token := range identifierTokens(line) {
					if vendorTerms[token] {
						t.Errorf("%s:%d names infrastructure (%q) in authored text\n"+
							"What serves the API is not a fact this repository carries. "+
							"State the conclusion without the vendor; see CLAUDE.md.",
							displayPath(root, path), lineNum+1, token)
					}
				}
			}
			for _, re := range patterns {
				// EVERY match on the line, not the first. Found while
				// landing the vendor rule: a line naming several
				// forbidden things reported one of them, so an author
				// fixing violations discovers the next only by running
				// again. A guard that reveals its findings one per run
				// is a guard that gets a reputation for moving goalposts.
				for _, m := range re.FindAllString(line, -1) {
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
// Guard 4: no unexported struct field can reach a Secret.
// ---------------------------------------------------------------------

// secretTypeString is how go/types spells this repo's Secret. Comparing
// the fully-qualified string rather than an object identity keeps the
// check independent of which importer instance produced the package.
const (
	modulePath       = "github.com/curiouspub/cli"
	secretTypeString = modulePath + "/internal/ui.Secret"
)

// TestNoUnexportedSecretFields fails on any struct field that is
// unexported and whose type can REACH a Secret, and on any defined type
// declared from Secret.
//
// The mechanism, measured rather than assumed: fmt cannot call a method
// on a value it reached by reflecting an UNEXPORTED field, so String,
// GoString and Format are all skipped and the underlying string is
// printed. Making Secret an opaque struct does not fix it either — the
// same reflection prints the inner field. So the type provably cannot
// defend itself here, and the only place the rule can live is a guard.
//
// TRANSITIVE, and that is the whole point of the rewrite. The first
// version of this guard matched the SPELLING of the immediate field
// type, which answered "is this field written `ui.Secret`" while the
// rule is "can this field reach a Secret". Those coincide only in the
// shape its author had in mind. fmt's read-only flag propagates through
// every field reached via an unexported one, so
//
//	type inner struct{ Token ui.Secret }   // exported!
//	type outer struct{ in inner }          // one unexported field
//
// prints the secret in full through %v, and the spelling check was
// green. That is not an exotic shape, it is `struct{ creds Credentials }`
// — the ordinary way to hold a credential. Aliases, defined types,
// dot-imports, maps, slices and generic instantiations were all green
// too, for the same reason.
//
// The residue, named rather than left to be found:
//
//   - An `any` or interface-typed field can hold a Secret at runtime and
//     no static check can see it.
//   - A generic container is only inspected where it is INSTANTIATED in
//     a package this check type-checks.
//   - Non-test sources only. The harm is a secret reaching a terminal or
//     a log from shipped code, and internal/ui's own suite must build
//     this exact shape to pin the limits it documents.
func TestNoUnexportedSecretFields(t *testing.T) {
	root := moduleRoot(t)

	var offenders []string
	var derived []string
	structsChecked := 0

	for _, dir := range packageDirs(t, root) {
		fset := token.NewFileSet()
		var files []*ast.File
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", filepath.Join(dir, name), err)
			}
			files = append(files, f)
		}
		if len(files) == 0 {
			continue
		}

		info := &types.Info{Types: make(map[ast.Expr]types.TypeAndValue)}
		conf := types.Config{
			Importer: importer.ForCompiler(fset, "source", nil),
			// A package that will not type-check must not silently
			// disappear from this guard's view: the error is collected
			// and the walk continues over whatever did resolve, and the
			// zero-structs check below is what catches a total failure.
			Error: func(error) {},
		}
		// The package PATH given here shows up inside every type string
		// this guard prints. Passing the absolute directory put the
		// checkout's full filesystem path — and the operator's username —
		// into failure output that CI logs verbatim, so it is the
		// module-relative import path instead.
		pkgPath := modulePath
		if rel, err := filepath.Rel(root, dir); err == nil && rel != "." {
			pkgPath = modulePath + "/" + filepath.ToSlash(rel)
		}
		_, _ = conf.Check(pkgPath, fset, files, info)

		for _, f := range files {
			ast.Inspect(f, func(n ast.Node) bool {
				// A defined type built FROM Secret strips its methods and
				// therefore leaks even in an exported field.
				if ts, ok := n.(*ast.TypeSpec); ok && !ts.Assign.IsValid() {
					if tv, ok := info.Types[ts.Type]; ok && isSecretType(tv.Type) {
						pos := fset.Position(ts.Pos())
						derived = append(derived, fmt.Sprintf("%s (%s:%d)",
							ts.Name.Name, displayPath(root, pos.Filename), pos.Line))
					}
				}

				st, ok := n.(*ast.StructType)
				if !ok {
					return true
				}
				tv, ok := info.Types[st]
				if !ok {
					return true
				}
				strct, ok := tv.Type.(*types.Struct)
				if !ok {
					return true
				}
				structsChecked++
				for i := 0; i < strct.NumFields(); i++ {
					field := strct.Field(i)
					if field.Exported() || field.Name() == "_" {
						continue
					}
					if !reachesSecret(field.Type(), map[types.Type]bool{}) {
						continue
					}
					pos := fset.Position(field.Pos())
					offenders = append(offenders, fmt.Sprintf("%s %s (%s:%d)",
						field.Name(), field.Type(), displayPath(root, pos.Filename), pos.Line))
				}
				return true
			})
		}
	}

	if structsChecked == 0 {
		t.Fatal("type-checked ZERO struct types — this guard would silently pass")
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("found an UNEXPORTED struct field that can reach a Secret: %s\n"+
			"fmt cannot call a method on a value reached by reflecting an unexported "+
			"field, and that applies at every depth below it — so %%v on the "+
			"containing struct prints the real secret even when the Secret itself "+
			"sits in an exported field further down. Export the field, or do not "+
			"let this struct reach a Secret.",
			strings.Join(offenders, "; "))
	}

	if len(derived) > 0 {
		sort.Strings(derived)
		t.Errorf("found a defined type declared FROM Secret: %s\n"+
			"A defined type does not inherit its source type's methods, so this one "+
			"has no String, no Format and no MarshalJSON — it prints in full "+
			"everywhere, including from an EXPORTED field. Use ui.Secret itself, or "+
			"an alias (=) if a local name is wanted.",
			strings.Join(derived, "; "))
	}
}

// packageDirs returns every directory under root holding non-test Go
// source. testdata trees are skipped: Go itself ignores them, and a
// fixture is not shipped code.
func packageDirs(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "bin", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			seen[filepath.Dir(path)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(seen) == 0 {
		t.Fatal("found no package directories — this guard would silently pass")
	}
	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}

// isSecretType reports whether t is exactly ui.Secret.
func isSecretType(t types.Type) bool {
	named, ok := types.Unalias(t).(*types.Named)
	return ok && types.TypeString(named, nil) == secretTypeString
}

// reachesSecret reports whether a value of type t can contain a Secret,
// following named types, aliases, pointers, slices, arrays, maps,
// channels and struct fields — at any depth, and regardless of whether
// the fields below are exported, because fmt's read-only flag propagates
// downward from the first unexported field.
//
// Interfaces return false: what they hold is a runtime fact. That is the
// guard's honest limit, stated in the test's doc comment too.
func reachesSecret(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true

	t = types.Unalias(t)
	if isSecretType(t) {
		return true
	}

	switch u := t.(type) {
	case *types.Named:
		for i := 0; i < u.TypeArgs().Len(); i++ {
			if reachesSecret(u.TypeArgs().At(i), seen) {
				return true
			}
		}
		return reachesSecret(u.Underlying(), seen)
	case *types.Pointer:
		return reachesSecret(u.Elem(), seen)
	case *types.Slice:
		return reachesSecret(u.Elem(), seen)
	case *types.Array:
		return reachesSecret(u.Elem(), seen)
	case *types.Chan:
		return reachesSecret(u.Elem(), seen)
	case *types.Map:
		return reachesSecret(u.Key(), seen) || reachesSecret(u.Elem(), seen)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if reachesSecret(u.Field(i).Type(), seen) {
				return true
			}
		}
	}
	return false
}
