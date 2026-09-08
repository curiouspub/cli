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

	"github.com/curiouspub/cli/internal/citations"
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
			case ".claude":
				// WORKTREE LANES LIVE HERE, and a lane is a second
				// checkout of this repository inside it. Walking one
				// makes every guard that uses this list count another
				// branch's files as though they were ours: the URL
				// ceiling saw four constants where the tree has two,
				// and would have red for anybody with a lane open.
				//
				// .gitignore cannot fix it, and that is the part worth
				// knowing. This walk never consults git — the citation
				// guard one file over enumerates through
				// `git ls-files --cached --others --exclude-standard`
				// and therefore honours ignores, while this one honours
				// nothing but the two names above. TWO ANSWERS TO
				// "which files are ours", in one package, disagreeing
				// exactly when a lane exists.
				//
				// This skip is the narrow fix. The real one is a single
				// enumeration both use, which is a change with its own
				// reasons and its own task.
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
//
// THAT SENTENCE AND THE ALLOWANCE BELOW RECONCILE, they do not trade, and
// the axis is WHO rather than WHEN. **Self-service widening is
// forbidden**: an author raising this number, or adding a line below, as
// part of getting their own change green is the guard loosening itself
// on behalf of the thing that tripped it, and no interval makes that
// legitimate. **Desk-ruled widening with a recorded reason is the
// allowance working** — it is precisely the deliberate, reviewable act
// the map below describes, and refusing it would leave the only exit a
// spelling that hides the value from the check.
//
// Different actors, not different days. Raised to 3 on 2026-09-08 by a
// ruling, for the needle the pre-flight development-URL scan greps for.
const maxNamedURLConstants = 3

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

	// Ruled 2026-09-08: a NEEDLE, never a destination. The pre-flight
	// development-URL check greps source files for this string and does
	// nothing else with it; its package imports no network package. It
	// arrived first as two constants joined at package level — a spelling
	// that passed only because the split detector read literal operands
	// and not folded values, which is the hole this entry exists instead
	// of. The entry is the door; the folding is the wall.
	"http://localhost": "the string the development-URL pre-flight check searches for",
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

// constEnv is every package-level string constant and variable in the
// module whose value is a plain literal, keyed twice: by directory for
// same-package references, and by package name for qualified ones.
//
// It exists so a URL assembled out of NAMED pieces can be folded back to
// the string it actually is. The scheme-fragment net below reads literal
// operands only, which is one indirection short: moving each half behind
// a constant produced a compiled-in `http://localhost` that no grep for
// `://` could find, and the guard passed. Judging the FOLDED VALUE closes
// that, because the value is what ends up in the binary regardless of how
// many names it was spelled with.
type constEnv struct {
	byDir map[string]string // dir + "\x00" + name
	byPkg map[string]string // package name + "." + name
}

func newConstEnv() *constEnv {
	return &constEnv{byDir: map[string]string{}, byPkg: map[string]string{}}
}

func (e *constEnv) add(dir, pkg, name, value string) {
	e.byDir[dir+"\x00"+name] = value
	e.byPkg[pkg+"."+name] = value
}

// foldString evaluates a constant string expression to its value.
//
// WHAT IT DOES NOT SEE, said here rather than left to be found, because a
// guard with an undocumented blind spot reads as total coverage and ends
// the search. The environment holds literal values only, so a constant
// defined as another concatenation (`const a = b + "x"`) does not resolve
// and its users fold to nothing — one level, not a fixpoint. Anything
// involving a variable, a function call or a cross-module import folds to
// nothing as well. Those all fall through to the fragment net, which is
// why both are kept rather than one replacing the other.
func foldString(e *constEnv, dir string, expr ast.Expr) (string, bool) {
	switch n := expr.(type) {
	case *ast.ParenExpr:
		return foldString(e, dir, n.X)
	case *ast.BasicLit:
		if n.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(n.Value)
		return v, err == nil
	case *ast.Ident:
		v, ok := e.byDir[dir+"\x00"+n.Name]
		return v, ok
	case *ast.SelectorExpr:
		pkg, ok := n.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		v, ok := e.byPkg[pkg.Name+"."+n.Sel.Name]
		return v, ok
	case *ast.BinaryExpr:
		if n.Op != token.ADD {
			return "", false
		}
		left, ok := foldString(e, dir, n.X)
		if !ok {
			return "", false
		}
		right, ok := foldString(e, dir, n.Y)
		if !ok {
			return "", false
		}
		return left + right, true
	}
	return "", false
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
//
// CONSTANT FOLDING, added 2026-09-08, and the hole it closes was live.
// The fragment net below reads LITERAL operands of a `+`. Moving each
// half of a URL behind a named constant leaves no literal for it to see,
// so `scheme + "//" + host` passed while the same value written as one
// literal failed — a compiled-in URL that no grep for `://` could find,
// which is the exact property this guard exists to keep. Pass two now
// folds constant expressions to their value and judges the VALUE, the
// same way a literal's is judged.
//
// REQUIRED MUTATION, RUN IN THIS ORDER on 2026-09-08 and both halves
// observed. Restore the pre-ruling world: put the assembly back in
// internal/preflight/localhost.go —
//
//	const devURLScheme = "http:"
//	const devURLHost   = "localhost"
//	var devURLNeedle = devURLScheme + "//" + devURLHost
//
// and delete "http://localhost" from allowedURLConstants. With folding,
// that reds here naming the folded value; without folding, it passed.
// The order matters and is the ruling's: the class was killed first —
// the shipped spelling made to fail — and only then was the instance
// admitted through the allow-list. A door opened before the wall exists
// is not a door.
func TestNamedURLConstantsStayUnderTheCeiling(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()

	var bare []string
	var named []string
	var unapproved []string
	var split []string
	var assembled []string
	var hosts []string
	var embedded []string

	// PASS ONE: parse everything and collect the module's string
	// constants. It is two passes rather than one because a name folds
	// against a declaration that may live in a file this walk has not
	// reached yet, or in another package altogether.
	type parsedFile struct {
		path string
		dir  string
		f    *ast.File
	}
	var files []parsedFile
	env := newConstEnv()
	for _, path := range goFiles(t, root, false) {
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		dir := filepath.Dir(path)
		files = append(files, parsedFile{path: path, dir: dir, f: f})

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
				lit, ok := vs.Values[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				env.add(dir, f.Name.Name, vs.Names[0].Name, v)
			}
		}
	}

	// PASS TWO: classify.
	for _, pf := range files {
		f, path, dir := pf.f, pf.path, pf.dir

		// Every expression that is the sole value of a single-name,
		// top-level const or var declaration — the shape a URL must have
		// to be a "named constant" rather than a bare literal. It is
		// keyed by EXPRESSION rather than by literal because a folded
		// concatenation is named in exactly the same way, and a value
		// spelled with three names is no less compiled in than one.
		namedExprs := map[ast.Expr]string{}
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
				namedExprs[vs.Values[0]] = vs.Names[0].Name
			}
		}

		ast.Inspect(f, func(n ast.Node) bool {
			// A URL assembled from pieces: fold it FIRST, because a
			// concatenation of named constants has no literal operand
			// for the fragment net below to see, and folding is what
			// makes the value judged the same way a literal's is.
			if be, ok := n.(*ast.BinaryExpr); ok && be.Op == token.ADD {
				if value, folded := foldString(env, dir, be); folded &&
					urlBearingPattern.MatchString(value) {
					pos := fset.Position(be.Pos())
					loc := fmt.Sprintf("%s:%d", displayPath(root, path), pos.Line)
					if name, isNamed := namedExprs[be]; isNamed {
						named = append(named, fmt.Sprintf("%s = %q (%s)", name, value, loc))
						if _, allowed := allowedURLConstants[value]; !allowed {
							unapproved = append(unapproved,
								fmt.Sprintf("%s = %q (%s)", name, value, loc))
						}
					} else {
						assembled = append(assembled, fmt.Sprintf("%q (%s)", value, loc))
					}
					// The operands are pieces of a value already judged;
					// descending would report the same URL twice.
					return false
				}
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
			if name, isNamed := namedExprs[lit]; isNamed {
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

	if len(assembled) > 0 {
		sort.Strings(assembled)
		t.Errorf("found a URL assembled from named constants: %s\n"+
			"Folded to its value, that is a compiled-in URL that no grep for a "+
			"scheme can find — which is the property this guard exists to keep. "+
			"Declare it as one named constant and have it approved.",
			strings.Join(assembled, "; "))
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
			"The budget is the control-plane API base and the development-URL "+
			"needle, with one space spare. Widening it to green your own change "+
			"is the guard loosening itself on behalf of the thing that tripped "+
			"it; a widening ruled at the desk, with its reason written into "+
			"allowedURLConstants, is the allowance working as designed.",
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

// THE TOKENISER USED TO SIT HERE and now lives in internal/citations,
// unchanged. It moved because a second reader of these same rule files
// arrived — the surface check under tools/, which reads a commit message,
// a branch name and a tag rather than a file — and a package holding
// nothing but tests cannot be imported by anything. Keeping this package
// tests-only is worth more than the proximity: it is where rules ABOUT
// this repository live, and giving it production code to export would
// make it importable.
//
// The two readers must tokenise identically or the vocabulary is enforced
// in a comment and evadable in the message of the commit that adds it.

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
// TestGeneratedManifestExemptionIsRealAndNarrow proves the go.sum
// carve-out both ways, because an exemption is the one kind of change
// that makes a guard quieter and therefore the one kind that must be
// shown to have a floor.
//
// The hazard is measured, not supposed: the vendor check tokenises
// identifiers, so a base64 module hash can spell a banned subword. Over
// 200,000 random hashes against the real term list, 0.72% of lines trip.
// A dependency bump would then red a file no author can edit, and the
// only fast way out would be deleting a term from the vendor list — a
// rule weakened by the thing that trips it.
//
// Both halves are asserted here. The exemption must APPLY to go.sum, and
// it must NOT apply to the file sitting next to it: go.mod is authored,
// every module path in it is somebody's choice, and a provider SDK
// appearing there is the whole point of the dependency rule.
//
// REQUIRED MUTATION: make stripModuleHashes return the empty string. The
// two path-column rows red — stripping the hash must not take the module
// path with it.
//
// SECOND: restore the original carve-out by returning ("", false) for a
// generated manifest. The same two rows red, which is the whole reason
// this test was rewritten: that shape passed every row of the previous
// table, and a reviewer found the hole by asking what the exemption
// covered BEYOND what the rows asserted.
//
// THIRD: make moduleHashField match nothing, so the hash column is
// scanned. The hash row reds — the original hazard is still live and the
// exemption still has work to do.
//
// FOURTH: match on filepath.Base(relPath). The nested-go.sum row reds.
//
// All four run and observed red before this comment was committed; see
// the report for the reds and the checksum-verified reverts.
//
// A FIFTH WAS TRIED AND DOES NOT RED, and saying so is worth more than
// dropping it: adding go.mod to generatedManifestNames changes nothing.
// Under the previous file-wide carve-out that mutation was the whole
// defence against an over-broad exemption, and it reddened. Under this
// one the "exemption" is only "strip checksum columns", and go.mod has
// none — so the line comes through untouched and is scanned in full.
// The failure mode is now unreachable by construction rather than
// guarded against, which is the better outcome and the reason the row
// below stays anyway: it is the floor that reds if this ever goes back to
// excusing a file.
//
// Note what these are aimed at, because it is why this test has the shape
// it does: NONE of them can be caught by the scan itself. The
// repository's own go.sum contains no banned token today, so an exemption
// deleted, widened, or drawn around the wrong column would leave the whole
// suite green. An exemption is the one kind of change that makes a guard
// quieter, and it has to be falsifiable somewhere other than in the guard
// it quietens.
func TestGeneratedManifestExemptionIsRealAndNarrow(t *testing.T) {
	root := moduleRoot(t)
	vendorTerms := loadVendorTerms(t, root)

	// The fixtures are DERIVED from the real term list at run time rather
	// than written down, and the reason is this guard itself: this file
	// is authored text, the vendor check reads it, and a literal banned
	// term here would red the scan. Carving this file out instead would
	// be the guard loosened by the thing that trips it — on the file
	// whose whole subject is a carve-out. Deriving them also keeps them
	// true as the list changes, and sampling the tree's own go.sum would
	// not work at all: that file is clean by luck, and a test resting on
	// luck stops testing anything when the luck turns.
	//
	// THREE fixtures, because a go.sum line has two columns with opposite
	// standing and the first version of this test only ever built one of
	// them. A term inside a HASH is an accident of base64 and must not
	// red; the same term inside a MODULE PATH is a person's choice and
	// must.
	var sample string
	for _, term := range sortedKeys(vendorTerms) {
		if len(term) >= 2 && len(term) <= 4 {
			sample = term
			break
		}
	}
	if sample == "" {
		t.Fatal("no short vendor term to splice into a fixture — this test cannot " +
			"demonstrate the hazard it exists for, and skipping would say so to nobody " +
			"because the suite runs without -v")
	}
	capitalised := strings.ToUpper(sample[:1]) + sample[1:]

	// A checksum column carrying the term as a base64 accident.
	hashField := "h1:qq" + capitalised + "Zz0000000000000000000000="
	// A checksum column carrying nothing, for the rows where the term
	// must come from somewhere else.
	const cleanHash = "h1:0000000000000000000000000000000000000000000="
	// A module path carrying the term deliberately, the way a provider's
	// own module is spelled.
	pathField := "example.com/" + sample + "/" + sample + "-sdk-go"

	for name, fixture := range map[string]string{"hash": hashField, "path": pathField} {
		var tripped []string
		for token := range citations.IdentifierTokens(fixture) {
			if vendorTerms[token] {
				tripped = append(tripped, token)
			}
		}
		if len(tripped) == 0 {
			t.Fatalf("the %s fixture %q no longer tokenises to any banned term, so the "+
				"rows below cannot observe what they claim to", name, fixture)
		}
		t.Logf("%s fixture tokenises to banned terms: %v", name, tripped)
	}
	for token := range citations.IdentifierTokens(cleanHash) {
		if vendorTerms[token] {
			t.Fatalf("the clean-hash fixture unexpectedly tokenises to %q, so the "+
				"path-column rows would pass for the wrong reason", token)
		}
	}

	cases := []struct {
		name     string
		relPath  string
		line     string
		ruleFile bool
		wantRead bool // is the banned term in this line's scanned text?
		why      string
	}{
		// The hazard the carve-out answers: a term inside the HASH.
		{"a hash column is not read", "go.sum", "example.com/mod v1.2.3 " + hashField, false, false,
			"a dependency bump would red the build on bytes nobody authored, and the " +
				"only quick way out would be deleting a term from the vendor list"},

		// THE FLOOR, and the reason this table was rewritten. The module
		// PATH on the same line is somebody's choice, and go.sum keeps
		// entries for modules no longer in the graph — which the
		// dependency check cannot see either, since `go list -m all`
		// omits a module nothing imports.
		{"a path column IS read", "go.sum", pathField + " v1.2.3 " + cleanHash, false, true,
			"a provider named in a module path is a human choice, and a stale go.sum " +
				"entry is invisible to the dependency graph check"},
		{"a path column is read even beside a hash", "go.sum", pathField + " v1.2.3 " + hashField, false, true,
			"stripping the hash must not take the path with it"},

		{"go.mod is read in full", "go.mod", pathField + " v1.2.3", false, true,
			"go.mod is AUTHORED — a provider SDK named there is exactly what the " +
				"dependency rule exists to catch"},
		{"a nested go.sum is read in full", "internal/x/go.sum", "example.com/mod v1.2.3 " + hashField, false, true,
			"the exemption is for this module's own manifest, not for every file that " +
				"shares its name somewhere in the tree"},
		{"an ordinary source file is read", "internal/api/client.go", hashField, false, true,
			"the exemption must not leak to authored code"},
		{"a rule file's data line stays exempt", "scripts/banned-dependencies.txt", pathField, true, false,
			"a denylist cannot match a module path without spelling one"},
		{"a rule file's comment is still read", "scripts/banned-dependencies.txt", "# " + pathField, true, true,
			"the prose explaining a rule has no need to name what the rule forbids"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scanText, ok := vendorScanLine(tc.relPath, tc.line, tc.ruleFile)
			var found []string
			if ok {
				for token := range citations.IdentifierTokens(scanText) {
					if vendorTerms[token] {
						found = append(found, token)
					}
				}
			}
			if got := len(found) > 0; got != tc.wantRead {
				t.Errorf("line %q under %q: banned term seen = %v, want %v (scanned %q) — %s",
					tc.line, tc.relPath, got, tc.wantRead, scanText, tc.why)
			}
		})
	}
}

// mustRel is filepath.Rel with the error turned into a test failure: a
// path that came out of the tree walk is always under the root, so a
// failure here means the walk changed rather than that a caller passed
// something odd.
func mustRel(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("relativising %s against %s: %v", path, root, err)
	}
	return rel
}

// sortedKeys gives a map's keys in a fixed order, so a test that picks
// "the first" of something picks the same one on every run rather than
// whichever the map iteration happened to yield.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// vendorScanLine returns the text of a line the vendor check should
// read, and whether it should read anything at all. It is the whole of
// that scoping decision, in one function, so the scan and its test
// exercise the same code rather than two statements of one intention.
//
// It is a function rather than an inline condition because the
// exemptions have NO observable effect on a clean tree: this
// repository's own go.sum contains no banned token today, so a scan that
// had lost its scoping entirely would still be green, and a mutation
// aimed at the scan would prove nothing. Testing the decision directly is
// what makes a carve-out falsifiable instead of merely present.
//
// relPath is slash-separated and relative to the module root, so a file
// called go.sum nested somewhere inside the tree is not the module's own
// manifest and gets no exemption.
//
// THE EXEMPTION IS A COLUMN, NOT A FILE, and that correction is the
// point of this function's current shape. The first version excused the
// whole of go.sum on the grounds that nobody chooses the bytes of a
// hash. True — and the same line also carries a MODULE PATH, which is
// somebody's choice, and go.sum keeps entries for modules no longer in
// the graph until someone runs `go mod tidy`. So a provider SDK named in
// a stale entry went unseen here, and the dependency graph check cannot
// see it either, because `go list -m all` does not list a module nothing
// imports. Two rules, one blind by construction and one blinded by a
// carve-out drawn wider than its own argument. Found by a reviewer
// probing what the exemption covered BEYOND what its tests asserted;
// every one of those tests passed.
func vendorScanLine(relPath, line string, isRuleFile bool) (string, bool) {
	if isRuleFile && !strings.HasPrefix(strings.TrimSpace(line), "#") {
		return "", false
	}
	if generatedManifestNames()[relPath] {
		return stripModuleHashes(line), true
	}
	return line, true
}

// moduleHashField matches a go.sum checksum column: an algorithm name, a
// colon, and base64. A module path cannot match it — a path has no colon
// — and neither can a version, which is why dropping fields by this
// shape leaves exactly the human-chosen part of the line behind.
var moduleHashField = regexp.MustCompile(`^[A-Za-z0-9]+:[A-Za-z0-9+/]*={0,2}$`)

// stripModuleHashes removes the checksum columns from a go.sum line and
// returns what a person actually wrote: the module path and the version.
//
// This is the narrow form of the go.sum carve-out. The hazard it answers
// is real and measured — the vendor check reads subwords inside
// identifiers, base64 produces capitalised fragments freely, and 0.72% of
// random module hashes tokenise to a banned term, so a dependency bump
// nobody chose the bytes of could red the build on a file no author can
// edit. The hazard is entirely in the hash. Excusing the rest of the line
// bought nothing and cost the only part of the file worth reading.
func stripModuleHashes(line string) string {
	fields := strings.Fields(line)
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if moduleHashField.MatchString(f) {
			continue
		}
		kept = append(kept, f)
	}
	return strings.Join(kept, " ")
}

// generatedManifestNames is the exemption set, in one place so the scan
// and the test above cannot disagree about what it contains. A test that
// restated the list would pass while the scan used a different one.
func generatedManifestNames() map[string]bool {
	return map[string]bool{"go.sum": true}
}

// ruleFileNames is the set of files whose job is to name what this
// repository forbids, keyed by their slash-separated path from the
// module root. Their DATA lines are exempt from the vendor vocabulary
// check and from nothing else.
//
// It is a function for the same reason generatedManifestNames is one:
// the scan and anything asserting about the scan read the same value, so
// a row cannot pass against a list the scan does not use.
func ruleFileNames() map[string]bool {
	return map[string]bool{
		"scripts/citation-patterns.txt":     true,
		"scripts/banned-dependencies.txt":   true,
		"scripts/vendor-terms.txt":          true,
		"scripts/provider-auth-actions.txt": true,
	}
}

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
	// They exist because two correct rules point opposite ways at the
	// same string: a denylist must spell vendor module paths and vendor
	// action paths in order to match them, and the citation manifest
	// forbids those vendor names in authored text. Without this, the
	// repository reds against itself and the fastest way out is to delete
	// one of the two rules.
	//
	// ADDING A MEMBER IS WIDENING AN EXEMPTION, so it is worth saying
	// what this one does and does not buy. The waiver is per-LINE and per-
	// CHECK: a data line in one of these files is exempt from the VENDOR
	// vocabulary and from nothing else, so a private identifier written
	// on one still reds, and every comment line in them is scanned like
	// any other prose. The set is a function rather than a literal here
	// so that a row can assert what is in it without restating the list.
	ruleFiles := map[string]bool{}
	for name := range ruleFileNames() {
		ruleFiles[filepath.Join(root, filepath.FromSlash(name))] = true
	}

	// GENERATED MANIFESTS: files no human wrote, exempt from the VENDOR
	// check by NAME and from nothing else. There is one, and the reason
	// it exists is a measurement rather than a worry.
	//
	// The vendor check tokenises identifiers rather than matching words,
	// which is what lets it see a provider name inside camelCase. A
	// go.sum line is a module path followed by a base64 hash, and base64
	// produces capitalised fragments freely — so a hash can contain a
	// subword this check bans. Measured against the real term list over
	// 200,000 random hashes: 0.72% of lines trip, most often on the
	// shortest terms. Four lines is a 2.8% chance; twenty lines is 13.5%;
	// fifty is nearly a third.
	//
	// The failure that matters is not the red itself but what a red would
	// force. A dependency bump nobody chose the bytes of would break the
	// build, on a file no author can edit, and the only quick way out
	// would be deleting a term from the vendor list — which is the rule
	// weakened by the thing that trips it, the exact dynamic the rule
	// file carve-out above exists to prevent.
	//
	// go.mod is NOT here and that is deliberate. It is authored: a human
	// chooses every module path in it, and a provider SDK appearing there
	// is precisely what the dependency denylist is for. The line is
	// GENERATED versus AUTHORED, not "manifest" — the two files sit
	// beside each other and only one of them is written by a person.
	//
	// The exemption is by filename rather than by content shape because a
	// content heuristic ("looks like base64") would also excuse an
	// authored line that happened to look generated, and there would be
	// no way to tell from the outside which had happened.
	for _, path := range publishedTextFiles(t, root) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		isRuleFile := ruleFiles[path]
		rel := filepath.ToSlash(mustRel(t, root, path))
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
			if scanText, ok := vendorScanLine(rel, line, isRuleFile); ok {
				for token := range citations.IdentifierTokens(scanText) {
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
