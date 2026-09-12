package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

	// And a pull-request ref, which is checked before it can be merged.
	f.write("proposed.txt", toyMatch+" in a proposed commit\n")
	proposed := f.commitAt("2026-01-04T00:00:00Z", "proposed", "proposed.txt")
	f.publish("refs/remotes/origin/pull/1/head", proposed)

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

// TestANULSkipIsSaidInWords keeps the deliberate binary policy visible
// in a clean report. A count alone does not tell a reader that those
// blobs were not examined or that UTF-16 text falls on the skipped side.
func TestANULSkipIsSaidInWords(t *testing.T) {
	f := newFixture(t)
	f.write("plain.txt", "an ordinary line\n")
	f.write("wide.txt", "w\x00i\x00d\x00e\x00\n\x00")
	head := f.commitAt("2026-01-01T00:00:00Z", "text and nul", "plain.txt", "wide.txt")
	f.publish("refs/remotes/origin/main", head)

	code, stdout, stderr := scanned(t, "-repo", f.dir, "-public")
	if code != exitClean {
		t.Fatalf("exit %d, want %d\n%s\n%s", code, exitClean, stdout, stderr)
	}
	for _, want := range []string{"1 NUL-containing blob(s) were not examined", "UTF-16 text"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the report does not say %q:\n%s", want, stdout)
		}
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

// TestAnEntryThatMatchesNothingIsAFailure, in both of its forms: a blob
// that is no longer reachable, and one that no longer matches the rule
// its entry names. A ledger that accumulates lines nobody can check is a
// ledger nobody reads — and an exception that has outlived its reason is
// exactly the shape this whole mechanism exists to avoid becoming.
func TestAnEntryThatMatchesNothingIsAFailure(t *testing.T) {
	for _, row := range []struct {
		name, swap string
	}{
		{"a blob that is not reachable", "0000000000000000000000000000000000000000 marker-id rewrite-ineligible leak.txt"},
		{"a rule that does not fire on it", "%s term-01 rewrite-ineligible leak.txt"},
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
	if len(entries) != 1 || entries[0].PatternID != "estate-box" {
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
