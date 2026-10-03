package pack

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/check"
)

// TestWalkEmitsItsFindingsFromOneTree is the end-to-end row: one walk,
// several kinds of trouble, and every one of them reported in the one
// answer the surfaces will render.
//
// TWO FINDINGS, AND THE COUNT IS A RULING RATHER THAN A FIXTURE. A
// combining mark is by definition outside ASCII, so a name carrying one
// is always also outside the key charset — there was no tree in which
// the mark warning fired and the charset hard stop did not, which is why
// the warning was retired and its detection folded into the hard stop's
// wording. The fixture holds a skipped link and one decomposed name, and
// expects the link's warning and the charset stop.
//
// THE COLLISION IS ABSENT ON TWO OF THE THREE PLATFORMS, and the helper
// that would add it says so. Writing two names that differ only in case
// into one directory leaves ONE file on a case-insensitive volume, so
// the fixture cannot exist there — the detector itself is driven from an
// injected list in findings_test.go, on every platform.
func TestWalkEmitsItsFindingsFromOneTree(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "index.html", body: "<html>"},
		{path: "public/cafe\u0301.png", body: "img"},
	})
	if !symlinkSupported(t) {
		t.Skip("this account cannot create symbolic links, and the row needs a real one " +
			"alongside the other findings to show they arrive together")
	}
	if err := os.Symlink(filepath.Join(root, "index.html"), filepath.Join(root, "content")); err != nil {
		t.Fatalf("creating the symlink fixture: %v", err)
	}

	want := []string{check.IDPathCharset, check.IDSymlinks}
	if addCaseCollision(t, root) {
		want = []string{check.IDPathCharset, check.IDSymlinks, check.IDCaseCollision}
	}

	res := mustWalk(t, OSFileSystem{}, root).Results
	if got := idsOf(res); !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v — hard stop first, then the declared order", got, want)
	}
	if len(res.Manifest) != len(walkIDs) {
		t.Errorf("manifest = %v, want a row for each of %v", res.Manifest, walkIDs)
	}
}

// TestWalkEmitsEveryFindingFromOneListing is the row above WITHOUT a
// filesystem, and it runs everywhere.
//
// IT EXISTS BECAUSE THE ROW ABOVE SKIPS. That one needs a real symbolic
// link, and a Windows account without the privilege to create one skips
// it — which would leave the property that every finding arrives
// together, in report order, from a single walk untested on exactly the
// platform whose file names are least like everybody else's. A synthetic
// directory listing holds every state at once: a link, two names
// colliding when lowercased, and one carrying a combining mark. No
// filesystem has an opinion about any of them.
//
// MUTATION: sort the findings by the order the detectors run rather than
// by severity and rank. Reds here — the hard stop leaves the front.
func TestWalkEmitsEveryFindingFromOneListing(t *testing.T) {
	fsys := fakeFS{dirs: map[string][]fakeEntry{
		"": {
			{name: "index.html", size: 6},
			{name: "content", mode: fs.ModeSymlink},
			{name: "README.md", size: 1},
			{name: "readme.md", size: 1},
			{name: "public", mode: fs.ModeDir},
		},
		"public": {{name: "cafe\u0301.png", size: 1}},
	}}

	res := mustWalk(t, fsys, "root").Results
	want := []string{
		check.IDPathCharset,
		check.IDSymlinks,
		check.IDCaseCollision,
	}
	if got := idsOf(res); !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v — the hard stop first, then the declared order", got, want)
	}
	if len(res.Manifest) != len(walkIDs) {
		t.Errorf("manifest = %v, want a row for each of %v", res.Manifest, walkIDs)
	}
}

// TestWalkStopsOnAnAssetNameTheServerWouldRefuse is the charset check
// reached through a real tree rather than through an injected list, and
// the legal sibling is the half that makes it an allowlist rather than a
// mood.
//
// The offending name is a SPACE rather than an accented character on
// purpose: a space is a legal file name on all three platforms in the
// matrix, so the fixture builds everywhere. The non-ASCII case is
// asserted from an injected list, where no filesystem has an opinion.
func TestWalkStopsOnAnAssetNameTheServerWouldRefuse(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "public/my photo.png", body: "img"},
		{path: "public/a-b_c.1~2.png", body: "img"},
		{path: "index.html", body: "<html>"},
	})

	res := mustWalk(t, OSFileSystem{}, root).Results
	stops := findingsFor(res, check.IDPathCharset)
	if len(stops) != 1 {
		t.Fatalf("charset findings = %v, want exactly one — the sibling name is legal", stops)
	}
	if stops[0].Severity != check.SeverityHardStop {
		t.Errorf("Severity = %q, want a hard stop", stops[0].Severity)
	}
	if !reflect.DeepEqual(stops[0].Paths, []string{"public/my photo.png"}) {
		t.Errorf("Paths = %v, want the offending file", stops[0].Paths)
	}
	if !strings.Contains(headline(stops[0]), "public/my photo.png") {
		t.Errorf("the message %q does not name the file", headline(stops[0]))
	}
	if strings.Contains(headline(stops[0])+stops[0].Why+stops[0].Next, "a-b_c.1~2.png") {
		t.Errorf("the finding names the legal sibling")
	}
}

// TestWalkLetsAstrosDynamicRoutesThrough is the fixture that found the
// defect: a public Astro starter with a blog, refused before it was
// built because four of its pages are named the way Astro names a
// dynamic route. One of them sits in a bracketed FOLDER, which is the
// case a fix written against file names alone would miss.
//
// The legal sibling under public/ and the space beside it are the other
// half: the check still runs where the names are published as they are.
//
// REQUIRED MUTATION, run 2026-10-03: publishedVerbatim returns every
// path. Reds here, naming the four route files.
func TestWalkLetsAstrosDynamicRoutesThrough(t *testing.T) {
	routes := []string{
		"src/pages/[...slug].astro",
		"src/pages/blog/[...page].astro",
		"src/pages/blog/[slug].astro",
		"src/pages/tags/[tag]/[...page].astro",
	}
	entries := []entry{
		{path: "package.json", body: "{}"},
		{path: "public/favicon.svg", body: "<svg/>"},
		{path: "public/my photo.png", body: "img"},
	}
	for _, r := range routes {
		entries = append(entries, entry{path: r, body: "---\n---\n"})
	}

	res := mustWalk(t, OSFileSystem{}, writeTree(t, entries)).Results
	stops := findingsFor(res, check.IDPathCharset)
	var named []string
	for _, f := range stops {
		named = append(named, f.Paths...)
	}
	if !reflect.DeepEqual(named, []string{"public/my photo.png"}) {
		t.Errorf("path-charset named %v, want only the public/ asset with a space — "+
			"a route's source name never reaches the built site", named)
	}
}

// TestAnAccentedNameStopsOnlyWhereItIsPublished is the non-ASCII half,
// from a synthetic listing so no filesystem's opinion of the name
// matters: an accented letter under public/ is still a hard stop, and
// the same letter in a component's name, which the build compiles away,
// is not this check's business.
func TestAnAccentedNameStopsOnlyWhereItIsPublished(t *testing.T) {
	fsys := fakeFS{dirs: map[string][]fakeEntry{
		"":               {{name: "public", mode: fs.ModeDir}, {name: "src", mode: fs.ModeDir}},
		"public":         {{name: "caf\u00e9.png", size: 1}},
		"src":            {{name: "components", mode: fs.ModeDir}},
		"src/components": {{name: "caf\u00e9.astro", size: 1}},
	}}

	stops := findingsFor(mustWalk(t, fsys, "root").Results, check.IDPathCharset)
	if len(stops) != 1 || stops[0].Severity != check.SeverityHardStop ||
		!reflect.DeepEqual(stops[0].Paths, []string{"public/caf\u00e9.png"}) {
		t.Errorf("path-charset = %+v, want one hard stop naming the public/ asset", stops)
	}
}

// TestAPageKeepsTheCharactersOutsideItsBrackets is the pages half: a
// page's route keeps every character of its name except the bracketed
// parameters, so those characters are checked, and the bracketed
// stretches are not.
//
// From a synthetic listing, so the accented name is the same bytes on
// every platform.
func TestAPageKeepsTheCharactersOutsideItsBrackets(t *testing.T) {
	fsys := fakeFS{dirs: map[string][]fakeEntry{
		"":    {{name: "src", mode: fs.ModeDir}, {name: "source", mode: fs.ModeDir}},
		"src": {{name: "pages", mode: fs.ModeDir}},
		"src/pages": {
			{name: "caf\u00e9.astro", size: 1},
			{name: "about me.astro", size: 1},
			{name: "[...slug].astro", size: 1},
			{name: "[lang]-caf\u00e9.astro", size: 1},
			{name: "_drafts", mode: fs.ModeDir},
			{name: "tags", mode: fs.ModeDir},
		},
		"src/pages/_drafts":    {{name: "caf\u00e9.astro", size: 1}},
		"src/pages/tags":       {{name: "[tag]", mode: fs.ModeDir}},
		"src/pages/tags/[tag]": {{name: "[...page].astro", size: 1}},
		"source":               {{name: "pages", mode: fs.ModeDir}},
		"source/pages":         {{name: "caf\u00e9.astro", size: 1}},
	}}

	for _, tc := range []struct {
		pages string
		want  []string
	}{
		{"src/pages", []string{
			"src/pages/[lang]-caf\u00e9.astro",
			"src/pages/about me.astro",
			"src/pages/caf\u00e9.astro",
		}},
		// A moved source folder is followed, and the old one is then
		// not a pages folder at all.
		{"source/pages", []string{"source/pages/caf\u00e9.astro"}},
	} {
		t.Run(tc.pages, func(t *testing.T) {
			tree, err := Walk(fsys, "root", NameScope{Public: "public", Pages: tc.pages})
			if err != nil {
				t.Fatal(err)
			}
			var named []string
			for _, f := range findingsFor(tree.Results, check.IDPathCharset) {
				if f.Severity != check.SeverityHardStop {
					t.Errorf("%s: Severity = %q, want a hard stop", f.Paths, f.Severity)
				}
				if !strings.Contains(f.Message, f.Paths[0]) {
					t.Errorf("the message %q does not name the file", f.Message)
				}
				named = append(named, f.Paths...)
			}
			sort.Strings(named)
			if !reflect.DeepEqual(named, tc.want) {
				t.Errorf("path-charset named %v, want %v — brackets stripped, `_` skipped", named, tc.want)
			}
		})
	}
}

// TestTheNameCheckReadsTheConfiguredPublicFolder is the same rule with
// the public folder moved, which Astro allows: the check follows it, in
// both directions.
func TestTheNameCheckReadsTheConfiguredPublicFolder(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "static/my photo.png", body: "img"},
		{path: "public/my photo.png", body: "img"},
		{path: "src/my notes.md", body: "text"},
	})

	for _, tc := range []struct {
		publicDir string
		want      []string
	}{
		{"static", []string{"static/my photo.png"}},
		{"public", []string{"public/my photo.png"}},
		// The project root itself: everything is copied as it is.
		{".", []string{"public/my photo.png", "src/my notes.md", "static/my photo.png"}},
	} {
		t.Run(tc.publicDir, func(t *testing.T) {
			tree, err := Walk(OSFileSystem{}, root, NameScope{Public: tc.publicDir, Pages: "src/pages"})
			if err != nil {
				t.Fatal(err)
			}
			var named []string
			for _, f := range findingsFor(tree.Results, check.IDPathCharset) {
				if f.Severity != check.SeverityHardStop {
					t.Errorf("%s: Severity = %q, want a hard stop", f.Paths, f.Severity)
				}
				named = append(named, f.Paths...)
			}
			if !reflect.DeepEqual(named, tc.want) {
				t.Errorf("path-charset named %v, want %v", named, tc.want)
			}
		})
	}
}

// TestEveryManifestRowIsBuiltInOnePlace reads this package's own source
// and asserts that a manifest row is constructed exactly once, inside
// the helper named for the job.
//
// IT IS A GUARD RATHER THAN A CONVENTION because the row's shape is
// expected to change, and a change that has one site is mechanical while
// the same change spread over three is an invitation to update two of
// them. Nothing in the compiler notices a second literal appearing, and
// nothing in the behaviour differs until the shape moves.
//
// MUTATION: build a second row inline anywhere in this package's
// non-test source. Reds here, naming the file and the function.
func TestEveryManifestRowIsBuiltInOnePlace(t *testing.T) {
	const helper = "answered"

	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing this package's sources: %v", err)
	}

	var sites []string
	scanned := 0
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		scanned++

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Status" {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "check" {
					return true
				}
				sites = append(sites, fn.Name.Name+" in "+name)
				return true
			})
		}
	}

	if scanned == 0 {
		t.Fatal("parsed ZERO production files — this guard would pass by looking at nothing")
	}
	if len(sites) != 1 || !strings.HasPrefix(sites[0], helper+" ") {
		t.Errorf("manifest rows are built at %v, want exactly one site, in %s", sites, helper)
	}
}
