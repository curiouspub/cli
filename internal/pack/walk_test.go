package pack

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------
// Forced excludes
// ---------------------------------------------------------------------

// forcedExcludeTree is the fixture the forced-exclude rows share: one of
// every shape the rule has to tell apart, including the two that look
// alike and are not — a build directory at the root, which goes, and a
// source directory of the same name three levels down, which stays.
func forcedExcludeTree() []entry {
	return []entry{
		{path: "package.json", body: "{}"},
		{path: "node_modules/left-pad/index.js", body: "x"},
		{path: "packages/a/node_modules/left-pad/index.js", body: "x"},
		{path: "dist/index.html", body: "<html>"},
		{path: "src/components/dist/Button.astro", body: "---"},
		{path: "src/pages/index.astro", body: "---"},
		{path: ".git/HEAD", body: "ref: refs/heads/main"},
		{path: ".astro/types.d.ts", body: "// generated"},
		{path: ".env", body: "SECRET=1"},
		{path: ".env.local", body: "SECRET=2"},
		{path: ".env.example", body: "SECRET="},
		{path: ".DS_Store", body: "\x00\x00"},
		{path: "src/.DS_Store", body: "\x00\x00"},
		{path: "Thumbs.db", body: "\x00"},
	}
}

// TestWalkExcludesTheForcedSet is the security row of this package. A
// pattern the walk does not understand is a file the author deliberately
// kept off the internet arriving on a public website, so the list is
// asserted whole rather than sampled.
//
// THE DEPTH RULES ARE THE HALF WORTH READING. node_modules and .git go
// at any depth, because a monorepo has several of the first and a
// submodule or a linked worktree puts the second anywhere. The build and
// cache directories go at the ROOT ONLY, because src/components/dist is
// somebody's real source directory and deleting it from their site would
// be a bug they could not diagnose from the outside.
//
// MUTATION: make the build-output exclude apply at any depth —
// src/components/dist/Button.astro disappears and this row reds. MUST
// NOT MOVE: every other row in this file.
func TestWalkExcludesTheForcedSet(t *testing.T) {
	root := writeTree(t, forcedExcludeTree())

	got := pathsOf(mustWalk(t, OSFileSystem{}, root).Files)
	want := []string{
		"package.json",
		"src/components/dist/Button.astro",
		"src/pages/index.astro",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

// TestWalkExcludesEveryEnvSpelling. The rule is deliberately the whole
// prefix, example files included: a rule with an exception is a rule
// somebody finds the wrong edge of, and the cost of the exception (a
// sample file the build never reads is absent) is nothing beside the
// cost of the mistake (a real secret uploaded and served).
func TestWalkExcludesEveryEnvSpelling(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "keep.txt", body: "keep"},
		{path: ".env", body: "1"},
		{path: ".env.local", body: "2"},
		{path: ".env.example", body: "3"},
		{path: ".env.production.local", body: "4"},
		{path: "src/.env", body: "5"},
		{path: "deep/nested/.env.test", body: "6"},
	})

	got := pathsOf(mustWalk(t, OSFileSystem{}, root).Files)
	if !reflect.DeepEqual(got, []string{"keep.txt"}) {
		t.Errorf("files = %v, want only keep.txt — every .env spelling is excluded at any depth", got)
	}
}

// TestWalkExcludesAGitDirectoryFile. A linked worktree and a submodule
// both spell .git as a FILE holding a pointer, not as a directory, so a
// rule that only excluded directories would upload one. The forced set
// therefore matches on the NAME and does not care what kind of thing is
// wearing it.
//
// MUTATION: make the forced excludes apply to directories only. Reds
// here and nowhere else in this file.
func TestWalkExcludesAGitDirectoryFile(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "keep.txt", body: "keep"},
		{path: ".git", body: "gitdir: ../.git/worktrees/x"},
	})

	got := pathsOf(mustWalk(t, OSFileSystem{}, root).Files)
	if !reflect.DeepEqual(got, []string{"keep.txt"}) {
		t.Errorf("files = %v, want only keep.txt", got)
	}
}

// TestWalkForcedExcludesBeatAGitignoreNegation is the ordering rule
// stated as a test. The forced set exists for what the builder needs and
// for what must never leave the machine; letting a file inside the
// project switch either off defeats both, and a negation is exactly the
// switch somebody would reach for.
//
// MUTATION: evaluate the ignore rules before the forced set — both
// negated paths come back and this row reds.
func TestWalkForcedExcludesBeatAGitignoreNegation(t *testing.T) {
	root := writeTree(t, []entry{
		{path: ".gitignore", body: "!node_modules/keep.js\n!.env\n!.git\n"},
		{path: "node_modules/keep.js", body: "x"},
		{path: ".env", body: "SECRET=1"},
		{path: "index.html", body: "<html>"},
	})

	got := pathsOf(mustWalk(t, OSFileSystem{}, root).Files)
	want := []string{".gitignore", "index.html"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v — a negation cannot re-include a forced exclude", got, want)
	}
}

// ---------------------------------------------------------------------
// Ignore rules through the walk
// ---------------------------------------------------------------------

// TestWalkDeeperIgnoreFileWinsBothWays. A nested ignore file overrides a
// shallower one, and the row runs it in both directions on purpose: a
// matcher that always preferred the ignoring verdict would pass the
// first half and fail the second, and one that always preferred the
// including verdict would do the reverse.
//
// MUTATION: scan the ignore files outermost-first instead of
// innermost-first. Reds on both halves.
func TestWalkDeeperIgnoreFileWinsBothWays(t *testing.T) {
	root := writeTree(t, []entry{
		{path: ".gitignore", body: "*.log\n!keep/*.tmp\n"},
		{path: "keep/.gitignore", body: "!important.log\n"},
		{path: "keep/important.log", body: "x"},
		{path: "keep/other.log", body: "x"},
		{path: "keep/scratch.tmp", body: "x"},
		{path: "drop/.gitignore", body: "*.tmp\n"},
		{path: "drop/scratch.tmp", body: "x"},
		{path: "drop/kept.txt", body: "x"},
	})

	got := pathsOf(mustWalk(t, OSFileSystem{}, root).Files)
	want := []string{
		".gitignore",
		"drop/.gitignore",
		"drop/kept.txt",
		"keep/.gitignore",
		"keep/important.log",
		"keep/scratch.tmp",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

// TestWalkDoesNotDescendIntoAnIgnoredDirectory, and the negation inside
// it stays dead. That is git's own documented behaviour rather than an
// optimisation this walk invented: a file inside an excluded directory
// cannot be re-included, so declining to descend is behaviour-preserving
// — and it is also what makes a symlink loop unreachable.
func TestWalkDoesNotDescendIntoAnIgnoredDirectory(t *testing.T) {
	root := writeTree(t, []entry{
		{path: ".gitignore", body: "build/\n!build/keep.txt\n"},
		{path: "build/keep.txt", body: "x"},
		{path: "build/nested/deep.txt", body: "x"},
		{path: "index.html", body: "<html>"},
	})

	got := pathsOf(mustWalk(t, OSFileSystem{}, root).Files)
	want := []string{".gitignore", "index.html"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

// TestWalkIgnoresTheRepositoryLocalExcludeFile. The tarball is a
// function of the project directory and nothing else. If it also
// depended on a file inside the version-control directory, or on the
// developer's machine-wide configuration, two people packing the same
// commit would produce different sites and neither could see the
// difference.
//
// The row asserts it rather than leaving it as a comment, which is the
// difference between a rule and an intention.
func TestWalkIgnoresTheRepositoryLocalExcludeFile(t *testing.T) {
	root := writeTree(t, []entry{
		{path: ".git/info/exclude", body: "secret.txt\n"},
		{path: "secret.txt", body: "still packed"},
		{path: "index.html", body: "<html>"},
	})

	got := pathsOf(mustWalk(t, OSFileSystem{}, root).Files)
	want := []string{"index.html", "secret.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v — nothing outside the project tree decides what is packed", got, want)
	}
}

// ---------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------

// TestWalkIsDeterministic covers the three ways the answer could drift:
// between two runs over one tree, between two trees built in different
// orders with different timestamps, and — the one a filesystem hides —
// between two directory-listing orders.
//
// THE REVERSED-LISTING LEG IS THE LOAD-BEARING ONE. The standard
// library's directory reader sorts, so two runs over two copies agree
// even from a walk that simply emitted whatever it was handed. Reversing
// the listing makes the input order genuinely different, which is the
// condition the claim is actually about.
//
// MUTATION: drop the final sort and emit files in traversal order. The
// reversed-listing leg reds; the other two pass, which is the whole
// reason the reversed one is here.
func TestWalkIsDeterministic(t *testing.T) {
	forward := []entry{
		{path: "a/one.txt", body: "1"},
		{path: "a/two.txt", body: "2"},
		{path: "b/three.txt", body: "3"},
		{path: "a-sibling.txt", body: "4"},
		{path: "z.txt", body: "5"},
	}
	backward := make([]entry, len(forward))
	for i, e := range forward {
		backward[len(forward)-1-i] = e
	}

	first := writeTree(t, forward)
	second := writeTree(t, backward)
	for _, e := range backward {
		full := filepath.Join(second, filepath.FromSlash(e.path))
		stamp := time.Unix(1_000_000_000, 0)
		if err := os.Chtimes(full, stamp, stamp); err != nil {
			t.Fatalf("restamping %s: %v", e.path, err)
		}
	}

	want := pathsOf(mustWalk(t, OSFileSystem{}, first).Files)

	if again := pathsOf(mustWalk(t, OSFileSystem{}, first).Files); !reflect.DeepEqual(again, want) {
		t.Errorf("two walks over one tree disagreed: %v then %v", want, again)
	}
	if other := pathsOf(mustWalk(t, OSFileSystem{}, second).Files); !reflect.DeepEqual(other, want) {
		t.Errorf("a copy built in a different order and restamped walked differently: %v, want %v",
			other, want)
	}
	if rev := pathsOf(mustWalk(t, reversedFS{OSFileSystem{}}, first).Files); !reflect.DeepEqual(rev, want) {
		t.Errorf("reversing every directory listing changed the answer: %v, want %v", rev, want)
	}
}

// TestWalkSortsBytewiseOnTheSlashSeparatedPath pins WHICH order, not
// merely that there is one. Byte-wise on the slash-separated relative
// path puts "a-sibling.txt" before "a/one.txt", because the hyphen sorts
// below the separator — which is the opposite of what a
// directory-at-a-time traversal produces, and therefore the discriminating
// case.
//
// MUTATION: sort on the host-separated path instead. Passes on this
// platform and reds on the one where the separator is a backslash, which
// is why the pair below is asserted as bytes rather than as a shape.
func TestWalkSortsBytewiseOnTheSlashSeparatedPath(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "a/one.txt", body: "1"},
		{path: "a-sibling.txt", body: "2"},
		{path: "a.txt", body: "3"},
	})

	got := pathsOf(mustWalk(t, OSFileSystem{}, root).Files)
	want := []string{"a-sibling.txt", "a.txt", "a/one.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i] < got[j] }) {
		t.Errorf("files are not in byte order: %v", got)
	}
}

// TestWalkPathsAreSlashSeparatedOnEveryPlatform. The relative path is
// built by joining components with a slash rather than by asking the
// standard library for a relative path, which would hand back the host's
// separator — and this value is compared, sorted and rendered into a
// result an agent may act on.
func TestWalkPathsAreSlashSeparatedOnEveryPlatform(t *testing.T) {
	root := writeTree(t, []entry{{path: "src/pages/blog/post.astro", body: "---"}})

	tree := mustWalk(t, OSFileSystem{}, root)
	if got := pathsOf(tree.Files); !reflect.DeepEqual(got, []string{"src/pages/blog/post.astro"}) {
		t.Errorf("files = %v, want a slash-separated path", got)
	}
	for _, f := range tree.Files {
		if strings.ContainsRune(f.Path, '\\') {
			t.Errorf("path %q carries a backslash", f.Path)
		}
	}
}

// ---------------------------------------------------------------------
// What the walk refuses to carry
// ---------------------------------------------------------------------

// TestWalkListsASymlinkInsteadOfPackingIt runs on every platform,
// because the entry kind comes from the listing rather than from the
// filesystem. The walk records the link, never follows it, and never
// descends through it — the last of which is also what makes a link
// pointing at one of its own ancestors harmless.
func TestWalkListsASymlinkInsteadOfPackingIt(t *testing.T) {
	fsys := fakeFS{dirs: map[string][]fakeEntry{
		"": {
			{name: "index.html", size: 6},
			{name: "content", mode: fs.ModeSymlink},
			{name: "src", mode: fs.ModeDir},
		},
		"src": {{name: "page.astro", size: 3}},
	}}

	tree := mustWalk(t, fsys, "root")
	if got := pathsOf(tree.Files); !reflect.DeepEqual(got, []string{"index.html", "src/page.astro"}) {
		t.Errorf("files = %v, want the two regular files", got)
	}
	if !reflect.DeepEqual(tree.Symlinks, []string{"content"}) {
		t.Errorf("symlinks = %v, want [content]", tree.Symlinks)
	}
}

// TestWalkSkipsIrregularEntriesSilently. A socket, a named pipe and a
// device node cannot be meaningfully packed and never belong to an Astro
// project, so they are dropped without a word — no file, no link, and no
// finding.
//
// MUTATION: treat an unrecognised entry kind as a regular file. Reds
// here.
func TestWalkSkipsIrregularEntriesSilently(t *testing.T) {
	fsys := fakeFS{dirs: map[string][]fakeEntry{
		"": {
			{name: "index.html", size: 6},
			{name: "pipe", mode: fs.ModeNamedPipe},
			{name: "sock", mode: fs.ModeSocket},
			{name: "dev", mode: fs.ModeDevice},
			{name: "tty", mode: fs.ModeDevice | fs.ModeCharDevice},
		},
	}}

	tree := mustWalk(t, fsys, "root")
	if got := pathsOf(tree.Files); !reflect.DeepEqual(got, []string{"index.html"}) {
		t.Errorf("files = %v, want only index.html", got)
	}
	if len(tree.Symlinks) != 0 {
		t.Errorf("symlinks = %v, want none", tree.Symlinks)
	}
	if len(tree.Results.Findings) != 0 {
		t.Errorf("findings = %v, want none — an irregular entry is skipped silently",
			tree.Results.Findings)
	}
}

// TestWalkListsARealSymlink is the confirmation half: the row above
// asserts what the walk does with a symlink ENTRY, and this one asserts
// that a real filesystem produces that entry for a real link.
//
// It skips where the account cannot create one — a Windows user without
// the privilege — and says so, because the property is already covered
// on every platform by the row above. What the skip costs is the
// confirmation, not the coverage.
func TestWalkListsARealSymlink(t *testing.T) {
	if !symlinkSupported(t) {
		t.Skip("this account cannot create symbolic links; the walk's handling of a " +
			"symlink entry is asserted on every platform by TestWalkListsASymlinkInsteadOfPackingIt")
	}

	root := writeTree(t, []entry{
		{path: "index.html", body: "<html>"},
		{path: "real/page.astro", body: "---"},
	})
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "content")); err != nil {
		t.Fatalf("creating the symlink fixture: %v", err)
	}

	tree := mustWalk(t, OSFileSystem{}, root)
	if got := pathsOf(tree.Files); !reflect.DeepEqual(got, []string{"index.html", "real/page.astro"}) {
		t.Errorf("files = %v — the link's target must not be walked through the link", got)
	}
	if !reflect.DeepEqual(tree.Symlinks, []string{"content"}) {
		t.Errorf("symlinks = %v, want [content]", tree.Symlinks)
	}
}

// TestWalkSkipsARealNamedPipe is the confirmation half for the irregular
// entries, and it also proves the walk never opens one: a read against a
// pipe nothing is writing to blocks forever, so a walk that opened what
// it found would hang here rather than fail.
func TestWalkSkipsARealNamedPipe(t *testing.T) {
	root, ok := namedPipeFixture(t)
	if !ok {
		t.Skip("named pipes have no portable spelling on this platform; the walk's " +
			"handling of the entry kind is asserted everywhere by TestWalkSkipsIrregularEntriesSilently")
	}

	tree := mustWalk(t, OSFileSystem{}, root)
	if got := pathsOf(tree.Files); !reflect.DeepEqual(got, []string{"index.html"}) {
		t.Errorf("files = %v, want only index.html", got)
	}
}

// ---------------------------------------------------------------------
// What the walk reads
// ---------------------------------------------------------------------

// TestWalkReadsNamesNotBytes, with its own positive control.
//
// The findings are computed from the path list alone, so a tree
// with no ignore file is walked without a single content read. The
// control immediately below is what makes that zero mean anything: the
// same wrapper is asked to open a file the walk found, and the count has
// to move. A wrapper that counted nothing would pass the first assertion
// and every other one built on it.
//
// MUTATION: have the walk open each regular file. The zero assertion
// reds. MUTATION: make the wrapper's Open forget to count. The control
// reds.
func TestWalkReadsNamesNotBytes(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "index.html", body: "<html>"},
		{path: "src/pages/index.astro", body: "---"},
		{path: "public/logo.svg", body: "<svg/>"},
	})

	fsys := counted()
	tree := mustWalk(t, fsys, root)
	if len(tree.Results.Findings) != 0 {
		t.Fatalf("findings = %v, want none from a clean tree", tree.Results.Findings)
	}
	if got := fsys.relativeOpens(root); len(got) != 0 {
		t.Errorf("the walk opened %v, want nothing — it reads names, not bytes", got)
	}

	before := len(fsys.opened)
	rc, err := fsys.Open(filepath.Join(root, filepath.FromSlash(tree.Files[0].Path)))
	if err != nil {
		t.Fatalf("opening a walked file through the same wrapper: %v", err)
	}
	if _, err := io.ReadAll(rc); err != nil {
		t.Fatalf("reading it: %v", err)
	}
	rc.Close()
	if len(fsys.opened) <= before {
		t.Errorf("the wrapper counted %d opens after a real read of %s, want more than %d — "+
			"an instrument that counts nothing cannot witness a zero",
			len(fsys.opened), tree.Files[0].Path, before)
	}
}

// TestWalkOpensIgnoreFilesAndNothingElse is the honest form of the claim
// above on a tree that has ignore rules in it.
//
// AN IGNORE FILE IS THE ONE FILE THE WALK MUST READ. Its rules are bytes
// and there is no way to obtain them from a name, so "the walk opens
// nothing" cannot be true of a project that has one — what is true, and
// what is worth asserting, is that the ignore files are the ONLY thing
// it opens, and that it does not open one inside a directory the rules
// have already excluded.
//
// MUTATION: read the ignore file inside the excluded directory before
// deciding to prune. Reds here.
func TestWalkOpensIgnoreFilesAndNothingElse(t *testing.T) {
	root := writeTree(t, []entry{
		{path: ".gitignore", body: "build/\n"},
		{path: "index.html", body: "<html>"},
		{path: "src/.gitignore", body: "*.tmp\n"},
		{path: "src/page.astro", body: "---"},
		{path: "src/scratch.tmp", body: "x"},
		{path: "build/.gitignore", body: "!everything\n"},
		{path: "build/out.html", body: "x"},
	})

	fsys := counted()
	mustWalk(t, fsys, root)

	want := []string{".gitignore", "src/.gitignore"}
	if got := fsys.relativeOpens(root); !reflect.DeepEqual(got, want) {
		t.Errorf("opened %v, want %v", got, want)
	}
}

// TestWalkWillNotReadRulesFromSomethingThatIsNotAFile.
//
// ADDED AFTER A MUTATION FAILED TO RED. The walk only loads an ignore
// file when the directory listing says it is a regular file, and
// removing that condition changed nothing in this suite: every fixture
// had a real one, so the guard was live, correct and unwitnessed. The
// state it guards against is a link or a pipe wearing the name — the
// second of which is not merely wrong to read but impossible to finish
// reading, since a pipe nothing is writing to never ends.
//
// MUTATION: load the ignore file whatever kind of entry wears the name.
// Reds here, and on the real-pipe row below.
func TestWalkWillNotReadRulesFromSomethingThatIsNotAFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode fs.FileMode
	}{
		{"a named pipe", fs.ModeNamedPipe},
		{"a symbolic link", fs.ModeSymlink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := &countingFS{FS: fakeFS{dirs: map[string][]fakeEntry{
				"": {
					{name: gitignoreName, mode: tc.mode, body: "*.astro\n"},
					{name: "page.astro", size: 3},
				},
			}}}

			tree := mustWalk(t, fsys, "root")
			if !slices.Contains(pathsOf(tree.Files), "page.astro") {
				t.Errorf("files = %v — rules were taken from something that is not a file",
					pathsOf(tree.Files))
			}
			if len(fsys.opened) != 0 {
				t.Errorf("opened %v, want nothing", fsys.opened)
			}
		})
	}
}

// TestWalkDoesNotHangOnAPipeNamedLikeAnIgnoreFile is the confirmation
// half, and the failure it prevents is a HANG rather than a wrong
// answer: nothing ever opens the write end of this pipe, so a walk that
// read what it found would wait here forever and the row would time out
// instead of reporting.
func TestWalkDoesNotHangOnAPipeNamedLikeAnIgnoreFile(t *testing.T) {
	root, ok := ignoreFilePipeFixture(t)
	if !ok {
		t.Skip("named pipes have no portable spelling on this platform; that the walk " +
			"refuses to read rules from one is asserted everywhere by " +
			"TestWalkWillNotReadRulesFromSomethingThatIsNotAFile")
	}

	tree := mustWalk(t, OSFileSystem{}, root)
	if got := pathsOf(tree.Files); !reflect.DeepEqual(got, []string{"page.astro"}) {
		t.Errorf("files = %v, want only page.astro", got)
	}
}

// ---------------------------------------------------------------------
// Sizes, modes and failure
// ---------------------------------------------------------------------

// TestWalkReportsSizeAndMode. The file list feeds a limits check and a
// tarball writer, and neither can do its job from a name.
func TestWalkReportsSizeAndMode(t *testing.T) {
	root := writeTree(t, []entry{{path: "index.html", body: "0123456789"}})

	tree := mustWalk(t, OSFileSystem{}, root)
	if len(tree.Files) != 1 {
		t.Fatalf("files = %v, want one", pathsOf(tree.Files))
	}
	if tree.Files[0].Size != 10 {
		t.Errorf("Size = %d, want 10", tree.Files[0].Size)
	}
	if !tree.Files[0].Mode.IsRegular() {
		t.Errorf("Mode = %v, want a regular file", tree.Files[0].Mode)
	}
}

// TestWalkRefusesARootItCannotRead. Pointed at a directory that is not
// there, the walk says so rather than reporting an empty project — which
// would be indistinguishable from a project with no files in it, and
// would reach the packer as a successful scan.
func TestWalkRefusesARootItCannotRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-here")

	_, err := Walk(OSFileSystem{}, missing)
	if err == nil {
		t.Fatal("walking a directory that does not exist succeeded")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("error = %v, want it to wrap the not-exist error", err)
	}
}
