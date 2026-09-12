package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/rulefile"
)

// A TOY VOCABULARY IN A TOY REPOSITORY. Every row here needs content the
// rules catch, and this file is one of the files the real rules are read
// against — so a fixture spelling a real forbidden string would be the
// leak arriving through the test that hunts it. Nothing below matches
// anything this repository actually forbids, and the rows do not move
// when the real manifests do.
const (
	toyPatterns = "# a toy manifest\nmarker-id  \\bZZQ-[0-9]+\\b\n"
	toyTerms    = "# a toy vocabulary\nterm-01  zzqcloud\n"
	toyMatch    = "ZZQ-9"
)

type fixture struct {
	t   *testing.T
	dir string
}

// newFixture builds a repository with the two manifests in its working
// tree and NOTHING committed yet.
//
// THE MANIFESTS ARE LEFT UNTRACKED on purpose. The scan reads them from
// the checkout, the way the real one does; leaving them out of the
// history keeps the universe of every row below exactly the content that
// row put there, so a count is a statement about the fixture rather than
// about the fixture plus its own plumbing.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, dir: t.TempDir()}
	f.git("init", "--quiet")
	f.git("symbolic-ref", "HEAD", "refs/heads/main")
	f.git("config", "user.name", "leak scan fixture")
	f.git("config", "user.email", "fixture@example.invalid")
	f.git("config", "commit.gpgsign", "false")
	// A BACKGROUND REPACK CAN RACE THE TEMPORARY DIRECTORY'S OWN
	// CLEANUP, which shows up as a cleanup failure naming a directory
	// that is not empty and has nothing to do with the row that failed.
	f.git("config", "gc.auto", "0")
	f.write(citationPatternsPath, toyPatterns)
	f.write(vendorTermsPath, toyTerms)
	return f
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("fixture: git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (f *fixture) write(path, content string) {
	f.t.Helper()
	full := filepath.Join(f.dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatalf("fixture: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		f.t.Fatalf("fixture: %v", err)
	}
}

// commitAt stages the named paths and commits them with both dates
// pinned, so a row about ordering can state the ordering it means.
func (f *fixture) commitAt(date, message string, paths ...string) string {
	f.t.Helper()
	if len(paths) > 0 {
		f.git(append([]string{"add", "--"}, paths...)...)
	}
	cmd := exec.Command("git", "commit", "--quiet", "--allow-empty", "-m", message)
	cmd.Dir = f.dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("fixture: committing: %v: %s", err, out)
	}
	return strings.TrimSpace(f.git("rev-parse", "HEAD"))
}

func (f *fixture) remove(path string) {
	f.t.Helper()
	f.git("rm", "--quiet", "--", path)
}

// publish puts a commit where this scan's universe actually looks, which
// is the ref namespace its own refspec lands in.
func (f *fixture) publish(ref, sha string) {
	f.t.Helper()
	f.git("update-ref", ref, sha)
}

// scanned runs the command the way a Makefile target does and returns
// everything a run would see.
func scanned(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	code = run(args, &out, &errs)
	return code, out.String(), errs.String()
}

// ---------------------------------------------------------------------
// There is no default mode.
// ---------------------------------------------------------------------

// TestTheModeIsNamedOrTheCommandRefuses is the row for the shape of the
// command rather than for what it finds.
//
// The two halves answer different questions and only one can run without
// something an operator holds. A bare invocation that picked one would be
// a scan that silently ran the half nobody asked for — and the half it
// would pick is the one that can pass.
func TestTheModeIsNamedOrTheCommandRefuses(t *testing.T) {
	f := newFixture(t)

	t.Run("no mode at all", func(t *testing.T) {
		code, _, stderr := scanned(t, "-repo", f.dir)
		if code != exitUndetermined {
			t.Fatalf("exit %d, want %d — a bare invocation ran a scan", code, exitUndetermined)
		}
		if !strings.Contains(stderr, "no default") {
			t.Errorf("the refusal does not say there is no default: %s", stderr)
		}
	})

	t.Run("both modes at once", func(t *testing.T) {
		code, _, stderr := scanned(t, "-repo", f.dir, "-public", "-inventory", "/nowhere")
		if code != exitUndetermined {
			t.Fatalf("exit %d, want %d", code, exitUndetermined)
		}
		if !strings.Contains(stderr, "two different") {
			t.Errorf("the refusal does not say the two are different scans: %s", stderr)
		}
	})

	t.Run("the private half refuses when it cannot read its inventory", func(t *testing.T) {
		code, _, stderr := scanned(t, "-repo", f.dir, "-inventory",
			filepath.Join(t.TempDir(), "absent.txt"))
		if code != exitUndetermined {
			t.Fatalf("exit %d, want %d — an unread inventory was reported as a clean estate",
				code, exitUndetermined)
		}
		if !strings.Contains(stderr, "unread one") {
			t.Errorf("the refusal does not distinguish a clean estate from an unread one: %s", stderr)
		}
	})
}

// ---------------------------------------------------------------------
// The universe.
// ---------------------------------------------------------------------

// TestTheUniverseIsOriginsRefsAndNotThisClones is the row the ruling
// about `--all` exists for, and it asserts all four of its halves at
// once: a branch on origin is in, a tag is in, a ref that exists only in
// this clone is out, and a pull-request ref is out.
//
// A LOCAL-ONLY REF IS NOT HYPOTHETICAL. A history rewrite leaves one
// behind, and a scan whose universe depends on which clone it runs in is
// not reproducible even on the day the answers happen to agree — the day
// they stop agreeing is the day a rewrite orphans content, which is
// exactly when somebody needs this to be trustworthy.
//
// MUTATION RUN: enumerating from `--all` instead reds this row naming the
// two findings it then reports, and nothing else in the package moves.
func TestTheUniverseIsOriginsRefsAndNotThisClones(t *testing.T) {
	f := newFixture(t)

	f.write("published.txt", "a clean line\n")
	main := f.commitAt("2026-01-01T00:00:00Z", "published", "published.txt")
	f.publish("refs/remotes/origin/main", main)

	f.write("tagged.txt", toyMatch+" in a tagged commit\n")
	tagged := f.commitAt("2026-01-02T00:00:00Z", "tagged", "tagged.txt")
	f.publish("refs/tags/v1", tagged)

	// A ref only this clone has, of exactly the shape a rewrite leaves.
	f.git("checkout", "--quiet", "-b", "side", main)
	f.write("rewritten.txt", toyMatch+" in a rewritten commit\n")
	side := f.commitAt("2026-01-03T00:00:00Z", "rewritten", "rewritten.txt")
	f.publish("refs/original/refs/heads/side", side)

	// And a pull-request ref in the exact namespace excluded by ruling.
	f.write("proposed.txt", toyMatch+" in a proposed commit\n")
	proposed := f.commitAt("2026-01-04T00:00:00Z", "proposed", "proposed.txt")
	f.publish("refs/pull/1/head", proposed)

	f.git("checkout", "--quiet", "main")
	f.git("branch", "--quiet", "-D", "side")

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitFindings {
		t.Fatalf("exit %d, want %d\n%s\n%s", code, exitFindings, stdout, stderr)
	}
	if !strings.Contains(stdout, "tagged.txt") {
		t.Errorf("a tag's content was not read; a tag is a published ref:\n%s", stdout)
	}
	for _, out := range []string{"rewritten.txt", "proposed.txt"} {
		if strings.Contains(stdout, out) {
			t.Errorf("%s is in the universe and must not be:\n%s", out, stdout)
		}
	}
}

// TestObjectMembershipIncludesATagPointingDirectlyAtATree is the
// permanent regression row for using a commit walk as the universe. The
// matching blob has no commit at all: only a published tree tag reaches
// it, so rev-list without --objects reports clean.
func TestObjectMembershipIncludesATagPointingDirectlyAtATree(t *testing.T) {
	f := newFixture(t)
	f.write("clean.txt", "ordinary\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "clean", "clean.txt")
	f.publish("refs/remotes/origin/main", head)

	f.write("tree-only.txt", toyMatch+"\n")
	f.git("add", "--", "tree-only.txt")
	tree := strings.TrimSpace(f.git("write-tree"))
	f.publish("refs/tags/tree-release", tree)

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitFindings {
		t.Fatalf("exit %d, want %d — a tree tag's blob was omitted\n%s\n%s",
			code, exitFindings, stdout, stderr)
	}
	for _, want := range []string{"public:marker-id", "tree-only.txt"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the tree-tag finding does not name %q:\n%s", want, stdout)
		}
	}
}

// A direct blob tag has no tree path, but it is still a published object
// and must still be handed to the engine. Empty path is the established
// representation of a surface that is not a file.
func TestObjectMembershipIncludesATagPointingDirectlyAtABlob(t *testing.T) {
	f := newFixture(t)
	f.write("uncommitted.txt", toyMatch+"\n")
	blob := strings.TrimSpace(f.git("hash-object", "-w", "uncommitted.txt"))
	f.publish("refs/tags/blob-release", blob)

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitFindings {
		t.Fatalf("exit %d, want %d — a direct blob tag was not examined\n%s\n%s",
			code, exitFindings, stdout, stderr)
	}
	if !strings.Contains(stdout, "public:marker-id") {
		t.Errorf("the direct blob finding is absent:\n%s", stdout)
	}
}

// TestPullInTheMiddleOfALegalTagNameIsNotARefNamespace is the exact-
// prefix row. `pull` is ordinary text after refs/tags/releases/ and does
// not turn the tag into refs/pull/*.
func TestPullInTheMiddleOfALegalTagNameIsNotARefNamespace(t *testing.T) {
	f := newFixture(t)
	f.write("clean.txt", "ordinary\n")
	clean := f.commitAt("2026-01-01T00:00:00Z", "clean", "clean.txt")
	f.publish("refs/remotes/origin/main", clean)
	f.write("tag-only.txt", toyMatch+"\n")
	head := f.commitAt("2026-01-02T00:00:00Z", "tagged", "tag-only.txt")
	f.publish("refs/tags/releases/pull/v1", head)

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitFindings || !strings.Contains(stdout, "tag-only.txt") {
		t.Fatalf("a legal tag containing /pull/ was excluded: exit %d\n%s\n%s",
			code, stdout, stderr)
	}
}

// TestThePullRefRulingNamesItsGap guards the truth of the rationale, not
// merely the ref filter. The filter is an intentional ruling; claiming
// another check covers intermediate file content would make the stated
// coverage larger than the code that exists.
func TestThePullRefRulingNamesItsGap(t *testing.T) {
	source, err := os.ReadFile("git.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(source, []byte("KNOWN, ACCEPTED GAP")) {
		t.Error("the pull-ref exclusion does not state that intermediate file content is an " +
			"accepted gap")
	}
	if bytes.Contains(source, []byte("covered rather than that it is harmless")) {
		t.Error("the pull-ref rationale still claims coverage no reader provides")
	}
}

// TestABlobIsCheckedAtEveryPublishedPath is the regression for a blob
// being assigned the single name rev-list happened to print for it.
//
// The later copy is deliberately put in a manifest path, where the
// provider vocabulary must excuse it. The earlier name is ordinary prose
// and must still red. With rev-list --objects supplying paths, git names
// this blob only at the newer, exempt path and the scan reports clean:
// publishing the second copy suppresses the first.
func TestABlobIsCheckedAtEveryPublishedPath(t *testing.T) {
	f := newFixture(t)
	content := "handoff to zzqcloud runtime\n"
	f.write("notes.txt", content)
	first := f.commitAt("2026-01-01T00:00:00Z", "ordinary copy", "notes.txt")
	f.publish("refs/remotes/origin/main", first)

	f.write("scripts/banned-dependencies.txt", content)
	second := f.commitAt("2026-01-02T00:00:00Z", "manifest copy",
		"scripts/banned-dependencies.txt")
	f.publish("refs/remotes/origin/main", second)

	refs, err := (repo{dir: f.dir}).Refs()
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := (repo{dir: f.dir}).Blobs(refs)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, b := range blobs {
		if len(b.Paths) > 1 {
			paths = b.Paths
			break
		}
	}
	wantPaths := []string{"notes.txt", "scripts/banned-dependencies.txt"}
	if strings.Join(paths, "\n") != strings.Join(wantPaths, "\n") {
		t.Fatalf("the shared blob was seen at %v, want every published path %v", paths, wantPaths)
	}

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitFindings {
		t.Fatalf("exit %d, want %d — the exempt second copy suppressed the ordinary first one\n%s\n%s",
			code, exitFindings, stdout, stderr)
	}
	if !strings.Contains(stdout, "notes.txt") {
		t.Errorf("the finding does not name the non-exempt path:\n%s", stdout)
	}
}

// TestAUniverseWithNothingInItIsNotAPass. A scan over nothing is green
// for the same reason a clean one is, and an unfetched checkout is the
// ordinary way to arrive at one.
func TestAUniverseWithNothingInItIsNotAPass(t *testing.T) {
	f := newFixture(t)
	f.write("published.txt", "a clean line\n")
	f.commitAt("2026-01-01T00:00:00Z", "published", "published.txt")
	// Committed, and published to no ref this scan reads.

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitUndetermined {
		t.Fatalf("exit %d, want %d\n%s", code, exitUndetermined, stdout)
	}
	if !strings.Contains(stderr, "not a pass") {
		t.Errorf("the refusal reads like a clean run: %s", stderr)
	}
}

// TestANULDoesNotMakeTheRestOfABlobDisappear inverts the old binary-skip
// row. The ordinary literal comes before a NUL, and remains a finding.
func TestANULDoesNotMakeTheRestOfABlobDisappear(t *testing.T) {
	f := newFixture(t)
	f.write("clean.txt", "ordinary content\n")
	f.write("mixed.bin", toyMatch+"\x00ordinary bytes after it\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "text and nul", "clean.txt", "mixed.bin")
	f.publish("refs/remotes/origin/main", head)

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitFindings {
		t.Fatalf("exit %d, want %d — NUL suppressed an ordinary match\n%s\n%s",
			code, exitFindings, stdout, stderr)
	}
	if !strings.Contains(stdout, "public:marker-id") || !strings.Contains(stdout, "2 read") {
		t.Errorf("the NUL-containing blob was not fully counted and reported:\n%s", stdout)
	}
}

// TestTheFetchFlagReachesGitAndItsFailureIsNotAPass. The refspec this
// scan's universe is defined by is a thing you can run rather than a
// sentence in a comment, and a fetch that could not happen is another
// "I could not look".
func TestTheFetchFlagReachesGitAndItsFailureIsNotAPass(t *testing.T) {
	f := newFixture(t)
	f.write("published.txt", "a clean line\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "published", "published.txt")
	f.publish("refs/remotes/origin/main", head)
	// The fixture has no remote, so the fetch cannot succeed — which is
	// the point: without the flag this same repository scans clean.

	if code, stdout, _ := scanned(t, "-repo", f.dir, "-public"); code != exitClean {
		t.Fatalf("the fixture does not scan clean without the flag: exit %d\n%s", code, stdout)
	}
	code, _, stderr := scanned(t, "-repo", f.dir, "-public", "-fetch")
	if code != exitUndetermined {
		t.Fatalf("exit %d, want %d — a fetch that did not happen was passed over",
			code, exitUndetermined)
	}
	if !strings.Contains(stderr, "could not be fetched") {
		t.Errorf("the refusal does not say what failed: %s", stderr)
	}
}

func TestAShallowBoundaryIsNotTheEndOfHistory(t *testing.T) {
	f := newFixture(t)
	f.write("plain.txt", "ordinary\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "plain", "plain.txt")
	f.publish("refs/remotes/origin/main", head)
	f.write(filepath.Join(".git", "shallow"), head+"\n")

	code, _, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitUndetermined || !strings.Contains(stderr, "shallow repository") {
		t.Fatalf("a shallow boundary was treated as complete history: exit %d\n%s", code, stderr)
	}
}

func TestTheParallelTreeWalkReturnsEveryJobAndAnyError(t *testing.T) {
	f := newFixture(t)
	f.write("plain.txt", "ordinary\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "plain", "plain.txt")
	r := repo{dir: f.dir}

	roots := make([]string, 25)
	for i := range roots {
		roots[i] = head
	}
	trees, err := r.walkTrees(roots)
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) != len(roots) {
		t.Fatalf("the worker pool returned %d jobs, want %d", len(trees), len(roots))
	}
	for i, tree := range trees {
		if tree == "" {
			t.Errorf("job %d was dropped", i)
		}
	}
	if _, err := r.walkTrees(append(roots, strings.Repeat("0", 40))); err == nil {
		t.Error("a worker error was discarded and enumeration continued")
	}
}

func TestEveryParentAndTheMergeTreeAreWalked(t *testing.T) {
	f := newFixture(t)
	f.write("root.txt", "ordinary\n")
	root := f.commitAt("2026-01-01T00:00:00Z", "root", "root.txt")

	f.git("checkout", "--quiet", "-b", "left", root)
	f.write("left.txt", "ZZQ-1\n")
	f.commitAt("2026-01-02T00:00:00Z", "left", "left.txt")
	f.git("checkout", "--quiet", "-b", "right", root)
	f.write("right.txt", "ZZQ-2\n")
	right := f.commitAt("2026-01-03T00:00:00Z", "right", "right.txt")
	f.git("checkout", "--quiet", "left")
	f.git("merge", "--quiet", "--no-commit", right)
	f.write("merge-only.txt", "ZZQ-3\n")
	merged := f.commitAt("2026-01-04T00:00:00Z", "merge", "merge-only.txt")
	f.publish("refs/remotes/origin/main", merged)

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitFindings {
		t.Fatalf("exit %d, want %d\n%s\n%s", code, exitFindings, stdout, stderr)
	}
	for _, path := range []string{"left.txt", "right.txt", "merge-only.txt"} {
		if !strings.Contains(stdout, path) {
			t.Errorf("%s was not reached through the merge graph:\n%s", path, stdout)
		}
	}
}

func TestSymlinkBytesAreBlobsAndGitlinksAreNot(t *testing.T) {
	f := newFixture(t)
	if err := os.Symlink(toyMatch, filepath.Join(f.dir, "pointer")); err != nil {
		t.Fatal(err)
	}
	head := f.commitAt("2026-01-01T00:00:00Z", "symlink", "pointer")
	f.git("update-index", "--add", "--cacheinfo", "160000,"+head+",nested")
	head = f.commitAt("2026-01-02T00:00:00Z", "gitlink")
	f.publish("refs/remotes/origin/main", head)

	refs, err := (repo{dir: f.dir}).Refs()
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := (repo{dir: f.dir}).Blobs(refs)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blobs {
		for _, path := range b.Paths {
			if path == "nested" {
				t.Fatal("a gitlink commit entry was classified as a blob")
			}
		}
	}
	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitFindings || !strings.Contains(stdout, "pointer") {
		t.Fatalf("symlink target bytes were not scanned: exit %d\n%s\n%s", code, stdout, stderr)
	}
}

func TestTreePathsPreserveCaseWithoutConsultingTheCheckout(t *testing.T) {
	f := newFixture(t)
	f.write("source.txt", toyMatch+"\n")
	blob := strings.TrimSpace(f.git("hash-object", "-w", "source.txt"))
	f.git("update-index", "--add", "--cacheinfo", "100644,"+blob+",Case.txt")
	f.git("update-index", "--add", "--cacheinfo", "100644,"+blob+",case.txt")
	head := f.commitAt("2026-01-01T00:00:00Z", "two cases")
	f.publish("refs/remotes/origin/main", head)

	refs, _ := (repo{dir: f.dir}).Refs()
	blobs, err := (repo{dir: f.dir}).Blobs(refs)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range blobs {
		if b.SHA == blob {
			want := []string{"Case.txt", "case.txt"}
			if !reflect.DeepEqual(b.Paths, want) {
				t.Fatalf("case-distinct paths became %q, want %q", b.Paths, want)
			}
			return
		}
	}
	t.Fatal("the shared case-distinct blob was not enumerated")
}

func TestMissingGraphObjectsAndBlobContentAreErrors(t *testing.T) {
	for _, object := range []string{"commit", "tree"} {
		t.Run(object, func(t *testing.T) {
			f := newFixture(t)
			f.write("plain.txt", "ordinary\n")
			head := f.commitAt("2026-01-01T00:00:00Z", "plain", "plain.txt")
			f.publish("refs/remotes/origin/main", head)
			sha := head
			if object == "tree" {
				sha = strings.TrimSpace(f.git("rev-parse", "HEAD^{tree}"))
			}
			if err := os.Remove(filepath.Join(f.dir, ".git", "objects", sha[:2], sha[2:])); err != nil {
				t.Fatal(err)
			}
			refs, _ := (repo{dir: f.dir}).Refs()
			if _, err := (repo{dir: f.dir}).Blobs(refs); err == nil {
				t.Fatalf("a missing %s was accepted as complete traversal", object)
			}
		})
	}

	f := newFixture(t)
	missing := strings.Repeat("0", 40)
	if err := (repo{dir: f.dir}).contents([]string{missing}, func(string, []byte) error {
		return nil
	}); err == nil {
		t.Fatal("missing blob content was treated as an examined blob")
	}
}

// ---------------------------------------------------------------------
// Attribution.
// ---------------------------------------------------------------------

// TestAttributionIsTreeMembershipAndNotOccurrenceChange is the row for a
// correction that was made twice, the second time inside the sentence
// correcting the first.
//
// Asking git which commits an object's occurrence COUNT changed at
// selects additions and REMOVALS alike, so it reports the commit that
// took a citation out as readily as the one that put it in. The fixture
// here is exactly that shape: one commit adds the content and a later one
// deletes it, and the answer has to be the first.
//
// MUTATION RUN, and what actually reddened: attributing from the first
// line of an occurrence-change log instead —
//
//	the finding is attributed to the commit that REMOVED the content
//	(d79e498), whose tree does not contain it.
//
// and nothing else in the package moved.
func TestAttributionIsTreeMembershipAndNotOccurrenceChange(t *testing.T) {
	f := newFixture(t)

	f.write("leak.txt", toyMatch+"\n")
	added := f.commitAt("2026-01-01T00:00:00Z", "add", "leak.txt")
	f.remove("leak.txt")
	removed := f.commitAt("2026-02-01T00:00:00Z", "remove")
	f.publish("refs/remotes/origin/main", removed)

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitFindings {
		t.Fatalf("exit %d, want %d\n%s\n%s", code, exitFindings, stdout, stderr)
	}
	if !strings.Contains(stdout, short(added)) {
		t.Errorf("the finding is not attributed to the commit whose tree contains it (%s):\n%s",
			short(added), stdout)
	}
	if strings.Contains(stdout, short(removed)) {
		t.Errorf("the finding is attributed to the commit that REMOVED the content (%s), whose "+
			"tree does not contain it. That is the whole defect this row exists for: a check "+
			"that reports removals sends its reader to the commit that fixed the problem.\n%s",
			short(removed), stdout)
	}
	if strings.Contains(stdout, "introduc") {
		t.Errorf("the report calls a commit the introducing one. Content can enter by copy, "+
			"by rewrite, by merge resolution, or from a branch since deleted; \"earliest "+
			"reachable\" is checkable and \"introduced\" is a story about intent.\n%s", stdout)
	}
}

// TestAttributionIsTheSAMEAnswerEveryTimeOnATie. Ties on author date are
// ordinary — three commits in this repository's own history share one
// date for a single blob — and an attribution that varies between runs on
// one repository is the one thing a ledger cannot have.
//
// MUTATION RUN: dropping the topological tiebreak leaves the two commits
// ordered by object id, which is stable but arbitrary; the row below
// reds when that arbitrary order puts the later commit first, which it
// does for this fixture.
func TestAttributionIsTheSameAnswerEveryTimeOnATie(t *testing.T) {
	f := newFixture(t)

	const sameInstant = "2026-01-01T00:00:00Z"
	f.write("leak.txt", toyMatch+"\n")
	first := f.commitAt(sameInstant, "first", "leak.txt")
	f.write("other.txt", "an unrelated line\n")
	second := f.commitAt(sameInstant, "second", "other.txt")
	f.publish("refs/remotes/origin/main", second)

	var answers []string
	for i := 0; i < 3; i++ {
		code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
		if code != exitFindings {
			t.Fatalf("exit %d, want %d\n%s\n%s", code, exitFindings, stdout, stderr)
		}
		answers = append(answers, attributions(stdout))
	}
	for _, answer := range answers[1:] {
		if answer != answers[0] {
			t.Fatalf("two runs over one repository disagree:\n%s\n---\n%s", answers[0], answer)
		}
	}
	if !strings.Contains(answers[0], short(first)) {
		t.Errorf("a tie on both dates did not resolve to the earlier commit in the graph "+
			"(%s); both trees contain the content, and the ordering has to come from "+
			"somewhere that cannot tie.\n%s", short(first), answers[0])
	}
}

// attributions keeps the lines of a report that name a finding and its
// earliest commit, and drops the ones carrying durations. A row about
// whether two runs AGREE cannot be asked of text that includes how long
// each took.
func attributions(stdout string) string {
	var kept []string
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "earliest commit containing it") {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// ---------------------------------------------------------------------
// The ledger.
// ---------------------------------------------------------------------

// baselined is a fixture whose one finding is recorded, so the rows below
// can take a line out and see what happens.
func baselined(t *testing.T) (*fixture, string) {
	t.Helper()
	f := newFixture(t)
	f.write("leak.txt", toyMatch+"\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "add", "leak.txt")
	f.publish("refs/remotes/origin/main", head)

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public", "-write-baseline")
	if code != exitUndetermined {
		t.Fatalf("recording exited %d, want %d — a run that RECORDED has not checked "+
			"anything, and spelling that as a pass makes writing the ledger a way to turn "+
			"the gate green\n%s\n%s", code, exitUndetermined, stdout, stderr)
	}
	ledger := filepath.Join(f.dir, filepath.FromSlash(publicBaselinePath))

	code, stdout, stderr = scanned(t, "-repo", f.dir, "-public")
	if code != exitClean {
		t.Fatalf("the recorded finding still reds: exit %d\n%s\n%s", code, stdout, stderr)
	}
	return f, ledger
}

// TestTheLedgerIsExercisedPerEntry. A list that reds only when it is
// emptied is a list nobody is watching, so the row takes out the one
// entry there is and requires the scan to name exactly it.
func TestTheLedgerIsExercisedPerEntry(t *testing.T) {
	f, ledger := baselined(t)

	recorded, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := loadBaseline(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the fixture recorded %d entries, want 1", len(entries))
	}

	var kept []string
	for _, line := range strings.Split(string(recorded), "\n") {
		if strings.HasPrefix(line, entries[0].Blob) {
			continue
		}
		kept = append(kept, line)
	}
	f.write(publicBaselinePath, strings.Join(kept, "\n"))

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitFindings {
		t.Fatalf("exit %d, want %d — removing an entry changed nothing\n%s\n%s",
			code, exitFindings, stdout, stderr)
	}
	if !strings.Contains(stdout, short(entries[0].Blob)) || !strings.Contains(stdout, "marker-id") {
		t.Errorf("the scan does not name the finding whose entry went:\n%s", stdout)
	}

	// AND BACK. Without this the row above is satisfied by a scan that
	// reds on everything.
	f.write(publicBaselinePath, string(recorded))
	if code, stdout, _ := scanned(t, "-repo", f.dir, "-public"); code != exitClean {
		t.Errorf("restoring the entry did not restore the pass: exit %d\n%s", code, stdout)
	}
}

func TestLedgerPathsRoundTripEveryGitPathByteTheFormatCanCarry(t *testing.T) {
	want := []entry{{
		finding: finding{Blob: strings.Repeat("a", 40), PatternID: "public:marker-id"},
		Reason:  "rewrite-ineligible",
		Paths:   []string{"two words.txt", "a,b.txt", "a\ttab.txt", "a\nline.txt"},
	}}
	path := filepath.Join(t.TempDir(), "ledger.txt")
	if err := os.WriteFile(path, []byte(renderBaseline("# ledger\n", want)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadBaseline(path)
	if err != nil {
		t.Fatalf("the ledger rejected its own output: %v", err)
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0].Paths, want[0].Paths) {
		t.Errorf("paths round-tripped as %q, want %q", got[0].Paths, want[0].Paths)
	}
}

// TestAnEntryThatMatchesNothingIsAFailure, in both of its forms: a blob
// that is no longer reachable, and one that no longer matches the rule
// its entry names. A ledger that accumulates lines nobody can check is a
// ledger nobody reads — and an exception that has outlived its reason is
// exactly the shape this whole mechanism exists to avoid becoming.
func TestAnEntryThatMatchesNothingIsAFailure(t *testing.T) {
	for _, row := range []struct {
		name, swap string
	}{
		{"a blob that is not reachable", "0000000000000000000000000000000000000000 public:marker-id rewrite-ineligible leak.txt"},
		{"a rule that does not fire on it", "%s public:term-01 rewrite-ineligible leak.txt"},
	} {
		t.Run(row.name, func(t *testing.T) {
			f, ledger := baselined(t)
			entries, err := loadBaseline(ledger)
			if err != nil {
				t.Fatal(err)
			}
			line := row.swap
			if strings.Contains(line, "%s") {
				line = strings.Replace(line, "%s", entries[0].Blob, 1)
			}
			f.write(publicBaselinePath, "# a ledger\n"+line+"\n")

			code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
			if code != exitFindings {
				t.Fatalf("exit %d, want %d\n%s\n%s", code, exitFindings, stdout, stderr)
			}
			if !strings.Contains(stdout, "nothing matches it now") {
				t.Errorf("the stale entry was not reported:\n%s", stdout)
			}
		})
	}
}

// TestTheManifestIsREADAndNotCopied. The proof that a rule file is load
// bearing is that deleting a line from it measurably changes what a check
// can see — and a scan consulting its own hardcoded list would pass that
// claim while failing to do it.
func TestTheManifestIsReadAndNotCopied(t *testing.T) {
	f := newFixture(t)
	f.write("leak.txt", toyMatch+"\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "add", "leak.txt")
	f.publish("refs/remotes/origin/main", head)

	if code, stdout, _ := scanned(t, "-repo", f.dir, "-public"); code != exitFindings {
		t.Fatalf("the fixture does not red before the line is removed: exit %d\n%s", code, stdout)
	}

	f.write(citationPatternsPath, "# a toy manifest with nothing in it but a different rule\n"+
		"other-id  zzq-nothing-matches-this\n")
	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitClean {
		t.Fatalf("exit %d, want %d — the rule survived being deleted from the file that "+
			"declares it, so this scan is reading a copy\n%s\n%s", code, exitClean, stdout, stderr)
	}
}

// TestTheReportNamesThePlaceAndNeverTheTextThatTrippedIt. This report
// lands in a run's log, and on a pull request from a fork that log is
// more public than the history it read — so a report carrying what it
// caught publishes it to a wider audience than the thing it was
// defending. The engine's Match cannot hold the text; this is the row
// that says nothing reconstructed it on the way out.
func TestTheReportNamesThePlaceAndNeverTheTextThatTrippedIt(t *testing.T) {
	f := newFixture(t)
	f.write("leak.txt", "a line carrying "+toyMatch+" in it\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "add", "leak.txt")
	f.publish("refs/remotes/origin/main", head)

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitFindings {
		t.Fatalf("exit %d, want %d", code, exitFindings)
	}
	if strings.Contains(stdout, toyMatch) || strings.Contains(stderr, toyMatch) {
		t.Errorf("the report reproduces the text that tripped the rule:\n%s", stdout)
	}

	// THE PRESENCE, in the same breath: an absence assertion on its own
	// is satisfied by a report that prints nothing, which would be a leak
	// closed by making the check useless.
	for _, want := range []string{"marker-id", "leak.txt", short(head)} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not name %q, so nobody can act on it:\n%s", want, stdout)
		}
	}

	// AND THE LEDGER KEEPS THE SAME PROMISE, which matters more: a log
	// scrolls away and a committed file does not.
	if code, _, _ := scanned(t, "-repo", f.dir, "-public", "-write-baseline"); code != exitUndetermined {
		t.Fatalf("recording exited %d", code)
	}
	recorded, err := os.ReadFile(filepath.Join(f.dir, filepath.FromSlash(publicBaselinePath)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recorded), toyMatch) {
		t.Errorf("the ledger records the text that matched, which publishes the thing being "+
			"recorded, in a file, for ever:\n%s", recorded)
	}
}

// TestThePrivateHalfReadsTheInventoryAndRecordsBesideIt covers the two
// properties that make it a different scan rather than the same one with
// a flag: it finds what the public half cannot, and its findings are
// recorded OUTSIDE this repository, because a public file cannot hold an
// exception to a private rule without becoming the leak.
func TestThePrivateHalfReadsTheInventoryAndRecordsBesideIt(t *testing.T) {
	f := newFixture(t)
	f.write("estate.txt", "the box is called zzq-box-7 and nothing here says so\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "add", "estate.txt")
	f.publish("refs/remotes/origin/main", head)

	// THE PUBLIC HALF CANNOT SEE IT, which is the reason the private half
	// exists at all.
	if code, stdout, _ := scanned(t, "-repo", f.dir, "-public"); code != exitClean {
		t.Fatalf("the public half reported something: exit %d\n%s", code, stdout)
	}

	outside := t.TempDir()
	inventory := filepath.Join(outside, "inventory.txt")
	if err := os.WriteFile(inventory, []byte("estate-box  zzq-box-[0-9]+\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-inventory", inventory)
	if code != exitFindings {
		t.Fatalf("exit %d, want %d\n%s\n%s", code, exitFindings, stdout, stderr)
	}
	if !strings.Contains(stdout, "estate-box") {
		t.Errorf("the inventory's rule did not fire:\n%s", stdout)
	}

	if code, _, _ := scanned(t, "-repo", f.dir, "-inventory", inventory, "-write-baseline"); code != exitUndetermined {
		t.Fatalf("recording exited %d", code)
	}
	if _, err := os.Stat(filepath.Join(outside, privateBaselineName)); err != nil {
		t.Errorf("the private ledger was not written beside its inventory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, filepath.FromSlash(publicBaselinePath))); err == nil {
		t.Error("the private half wrote into this repository. A public file cannot hold an " +
			"exception to a private rule without naming the thing it excuses")
	}
}

// TestThePrivateLedgerContainsOnlyTheDeltaFromThePublicLedger keeps one
// published fact in one home. The private vocabulary includes the public
// rules, but findings already accepted by the public ledger must neither
// be reported nor copied into the ledger beside the inventory.
func TestThePrivateLedgerContainsOnlyTheDeltaFromThePublicLedger(t *testing.T) {
	f := newFixture(t)
	f.write("public.txt", toyMatch+"\n")
	f.write("estate.txt", "the box is called zzq-box-7\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "two kinds", "public.txt", "estate.txt")
	f.publish("refs/remotes/origin/main", head)

	if code, stdout, stderr := scanned(t, "-repo", f.dir, "-public", "-write-baseline"); code != exitUndetermined {
		t.Fatalf("recording the public control exited %d\n%s\n%s", code, stdout, stderr)
	}

	outside := t.TempDir()
	inventory := filepath.Join(outside, "inventory.txt")
	if err := os.WriteFile(inventory, []byte("estate-box  zzq-box-[0-9]+\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-inventory", inventory)
	if code != exitFindings {
		t.Fatalf("exit %d, want %d\n%s\n%s", code, exitFindings, stdout, stderr)
	}
	if !strings.Contains(stdout, "estate-box") {
		t.Errorf("the private finding was not reported:\n%s", stdout)
	}
	if strings.Contains(stdout, "marker-id") {
		t.Errorf("a finding already recorded publicly was re-reported privately:\n%s", stdout)
	}

	if code, stdout, stderr = scanned(t, "-repo", f.dir, "-inventory", inventory,
		"-write-baseline"); code != exitUndetermined {
		t.Fatalf("recording the private delta exited %d\n%s\n%s", code, stdout, stderr)
	}
	privateLedger := filepath.Join(outside, privateBaselineName)
	entries, err := loadBaseline(privateLedger)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].PatternID != "private:estate-box" {
		t.Errorf("the private ledger contains %+v, want only the inventory finding", entries)
	}
	data, err := os.ReadFile(privateLedger)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "-public -write-baseline") ||
		!strings.Contains(string(data), "-inventory <path> -write-baseline") {
		t.Errorf("the private ledger's producer header names the wrong mode:\n%s", data)
	}
}

// TestARetiredPublicIDCannotEraseAPrivateFinding is the namespace attack
// in its smallest form. The public ledger remembers an id no active
// public rule owns; an inventory later reuses the raw handle. Its
// private: identity is different and must remain a finding.
func TestARetiredPublicIDCannotEraseAPrivateFinding(t *testing.T) {
	setup := func(t *testing.T, ledgerID string) (*fixture, string) {
		t.Helper()
		f := newFixture(t)
		f.write("estate.txt", "zzq-retired-value\n")
		head := f.commitAt("2026-01-01T00:00:00Z", "published", "estate.txt")
		f.publish("refs/remotes/origin/main", head)
		blob := strings.TrimSpace(f.git("rev-parse", "HEAD:estate.txt"))
		f.write(publicBaselinePath, blob+" "+ledgerID+" rewrite-ineligible estate.txt\n")
		inventory := filepath.Join(t.TempDir(), "inventory.txt")
		if err := os.WriteFile(inventory, []byte("retired-rule  zzq-retired-value\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return f, inventory
	}

	t.Run("the old unnamespaced attack is refused", func(t *testing.T) {
		f, inventory := setup(t, "retired-rule")
		code, _, stderr := scanned(t, "-repo", f.dir, "-inventory", inventory)
		if code != exitUndetermined || !strings.Contains(stderr, "outside the public namespace") {
			t.Fatalf("an old public identity was allowed to subtract: exit %d\n%s", code, stderr)
		}
	})

	t.Run("the same raw handle has a different origin", func(t *testing.T) {
		f, inventory := setup(t, "public:retired-rule")
		code, stdout, stderr := scanned(t, "-repo", f.dir, "-inventory", inventory)
		if code != exitFindings {
			t.Fatalf("exit %d, want %d — a retired public id erased a private finding\n%s\n%s",
				code, exitFindings, stdout, stderr)
		}
		if !strings.Contains(stdout, "private:retired-rule") {
			t.Errorf("the private identity was not reported:\n%s", stdout)
		}
	})
}

// TestThePublicLedgerOwnsOnlyPublicIdentities covers both entrances: a
// hand-edited ledger is refused when loaded, and the generator refuses
// before it writes a private identity into the public file.
func TestThePublicLedgerOwnsOnlyPublicIdentities(t *testing.T) {
	f := newFixture(t)
	f.write("plain.txt", "ordinary\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "plain", "plain.txt")
	f.publish("refs/remotes/origin/main", head)
	blob := strings.TrimSpace(f.git("rev-parse", "HEAD:plain.txt"))
	f.write(publicBaselinePath, blob+" private:wrong-home rewrite-ineligible plain.txt\n")

	code, _, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitUndetermined || !strings.Contains(stderr, "outside the public namespace") {
		t.Fatalf("a private identity in the public ledger was accepted: exit %d\n%s", code, stderr)
	}

	ledger := filepath.Join(t.TempDir(), "public-ledger.txt")
	found := map[finding]map[string]bool{
		{Blob: blob, PatternID: "private:wrong-home"}: {"plain.txt": true},
	}
	var stdout, errs bytes.Buffer
	if got := writeLedger(&stdout, &errs, ledger, found, "fixture", true); got != exitUndetermined {
		t.Fatalf("generator exit %d, want %d", got, exitUndetermined)
	}
	if _, err := os.Stat(ledger); !os.IsNotExist(err) {
		t.Errorf("the generator created the public ledger before refusing: %v", err)
	}

	inventory := filepath.Join(t.TempDir(), "inventory.txt")
	if err := os.WriteFile(inventory, []byte("estate-box  zzq-estate-box\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(f.dir, filepath.FromSlash(publicBaselinePath))
	code, _, stderr = scanned(t, "-repo", f.dir, "-inventory", inventory,
		"-baseline", canonical, "-write-baseline")
	if code != exitUndetermined || !strings.Contains(stderr, "cannot use the public ledger") {
		t.Errorf("an inventory run could target the public ledger: exit %d\n%s", code, stderr)
	}
}

// TestAPrivateLiteralInAPublishedPathLivesOnlyInThePrivateLedger is the
// precise property-4 row. Paths remain useful public context, but only
// the private scan has the inventory that recognizes this path as a
// finding, and only its outside ledger may record that identity.
func TestAPrivateLiteralInAPublishedPathLivesOnlyInThePrivateLedger(t *testing.T) {
	f := newFixture(t)
	const publishedPath = "zzq-private-box.txt"
	f.write(publishedPath, "ordinary content\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "named", publishedPath)
	f.publish("refs/remotes/origin/main", head)

	if code, stdout, stderr := scanned(t, "-repo", f.dir, "-public", "-write-baseline"); code != exitUndetermined {
		t.Fatalf("public recording exited %d\n%s\n%s", code, stdout, stderr)
	}
	publicLedger := filepath.Join(f.dir, filepath.FromSlash(publicBaselinePath))
	publicData, err := os.ReadFile(publicLedger)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicData), publishedPath) {
		t.Fatalf("the public ledger copied a private-path finding:\n%s", publicData)
	}

	outside := t.TempDir()
	inventory := filepath.Join(outside, "inventory.txt")
	if err := os.WriteFile(inventory, []byte("estate-box  zzq-private-box\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := scanned(t, "-repo", f.dir, "-inventory", inventory)
	if code != exitFindings || !strings.Contains(stdout, "private:estate-box") {
		t.Fatalf("the private scan missed the published path: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if code, stdout, stderr = scanned(t, "-repo", f.dir, "-inventory", inventory,
		"-write-baseline"); code != exitUndetermined {
		t.Fatalf("private recording exited %d\n%s\n%s", code, stdout, stderr)
	}
	privateData, err := os.ReadFile(filepath.Join(outside, privateBaselineName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(privateData), "private:estate-box") ||
		!strings.Contains(string(privateData), publishedPath) {
		t.Errorf("the private ledger did not record the path finding:\n%s", privateData)
	}
}

// TestAnInventoryIDCannotCollideWithAPublicID protects finding identity.
// Adding the rule text to identity is forbidden, so the only honest
// answer to two vocabularies assigning one id is to refuse them.
func TestAnInventoryIDCannotCollideWithAPublicID(t *testing.T) {
	f := newFixture(t)
	f.write("plain.txt", "an ordinary line\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "plain", "plain.txt")
	f.publish("refs/remotes/origin/main", head)

	inventory := filepath.Join(t.TempDir(), "inventory.txt")
	if err := os.WriteFile(inventory, []byte("marker-id  another-pattern\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := scanned(t, "-repo", f.dir, "-inventory", inventory)
	if code != exitUndetermined {
		t.Fatalf("exit %d, want %d — two rules were given one finding identity", code,
			exitUndetermined)
	}
	if !strings.Contains(stderr, "could not be distinguished") {
		t.Errorf("the refusal does not explain the identity collision: %s", stderr)
	}
}

func TestFindingIdentityIsExactlyBlobAndNamespacedPatternID(t *testing.T) {
	f := newFixture(t)
	f.write(citationPatternsPath, "first-id  \\bZZQ-[0-9]+\\b\nsecond-id  \\bZZQ-[0-9]+\\b\n")
	f.write("one.txt", toyMatch+"\n")
	f.write("two.txt", toyMatch+"\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "copies", "one.txt", "two.txt")
	f.publish("refs/remotes/origin/main", head)

	rules, _, err := vocabulary(repo{dir: f.dir}, true, "", "")
	if err != nil {
		t.Fatal(err)
	}
	refs, _ := (repo{dir: f.dir}).Refs()
	blobs, err := (repo{dir: f.dir}).Blobs(refs)
	if err != nil {
		t.Fatal(err)
	}
	found, _, _, err := scan(repo{dir: f.dir}, rules, blobs, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("one blob under two rules produced %d identities, want 2: %+v", len(found), found)
	}
	for _, id := range []string{"public:first-id", "public:second-id"} {
		var seen bool
		for finding, paths := range found {
			if finding.PatternID == id {
				seen = true
				if len(paths) != 2 {
					t.Errorf("%s became %d path identities, want one finding with two paths", id, len(paths))
				}
			}
		}
		if !seen {
			t.Errorf("missing identity for %s", id)
		}
	}
}

func TestBothPublicContentManifestsDriveMatching(t *testing.T) {
	f := newFixture(t)
	f.write("pattern.txt", toyMatch+"\n")
	f.write("term.txt", "zzqcloud\n")
	head := f.commitAt("2026-01-01T00:00:00Z", "both", "pattern.txt", "term.txt")
	f.publish("refs/remotes/origin/main", head)

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitFindings {
		t.Fatalf("exit %d, want %d\n%s\n%s", code, exitFindings, stdout, stderr)
	}
	for _, id := range []string{"public:marker-id", "public:term-01"} {
		if !strings.Contains(stdout, id) {
			t.Errorf("the finding from %s is absent:\n%s", id, stdout)
		}
	}
}

func TestTheIDMigrationPreservedEveryRuleText(t *testing.T) {
	r := repo{dir: filepath.Clean("../..")}
	for _, path := range []string{citationPatternsPath, vendorTermsPath} {
		before, err := r.run("show", "97cdb9d^:"+path)
		if err != nil {
			t.Fatal(err)
		}
		afterData, err := os.ReadFile(filepath.Join(r.dir, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		beforeRules := rulefile.ParseHistorical(path+" before ids", before)
		afterRules, err := rulefile.Parse(path, string(afterData))
		if err != nil {
			t.Fatal(err)
		}
		var beforeText, afterText []string
		for _, rule := range beforeRules {
			beforeText = append(beforeText, rule.Text)
		}
		for _, rule := range afterRules {
			afterText = append(afterText, rule.Text)
		}
		if !reflect.DeepEqual(beforeText, afterText) {
			t.Errorf("%s changed rule text during the id migration", path)
		}
	}
}

func TestCIIncludesOnlyThePublicHistoryScan(t *testing.T) {
	root := filepath.Clean("../..")
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(makefile), "ci: guard-a-branch-to-work-on fmt vet build leak-scan test lint") {
		t.Error("ci no longer includes the public leak scan")
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(workflow), "leak-scan-private") {
		t.Error("the private target is reachable from the public CI workflow")
	}
}

func TestNoBaselinedBlobOccursInHEAD(t *testing.T) {
	r := repo{dir: filepath.Clean("../..")}
	entries, err := loadBaseline(filepath.Join(r.dir, filepath.FromSlash(publicBaselinePath)))
	if err != nil {
		t.Fatal(err)
	}
	listing, err := r.run("ls-tree", "-r", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	headBlobs := map[string]bool{}
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] == "blob" {
			headBlobs[fields[2]] = true
		}
	}
	for _, entry := range entries {
		if headBlobs[entry.Blob] {
			t.Errorf("baselined blob %s still occurs in HEAD", short(entry.Blob))
		}
	}
}
