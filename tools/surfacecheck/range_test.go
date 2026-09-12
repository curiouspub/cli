package main

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// clonedRepo copies this repository into a temporary directory so a row
// can commit into it without touching the checkout it is running in.
//
// A CLONE rather than a fresh repository, because the self-test reads two
// commits out of this project's own history before any range is examined.
// A repository without them is one this checker refuses, correctly: a
// checkout that cannot answer whether the checker works is not one to
// trust the range to either. Sharing the object store makes it cheap.
func clonedRepo(t *testing.T) *fixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	out, err := exec.Command("git", "clone", "--quiet", "--shared", moduleRoot(t), dir).CombinedOutput()
	if err != nil {
		t.Fatalf("cloning this repository: %v: %s", err, out)
	}
	f := &fixture{t: t, dir: dir, repo: repo{dir: dir}}
	f.git("config", "user.name", "surface check fixture")
	f.git("config", "user.email", "fixture@example.invalid")
	f.git("config", "commit.gpgsign", "false")

	// THE RULE FILES ARE TAKEN FROM THIS WORKING TREE, not from whatever
	// the clone's tip carries, and they are left UNCOMMITTED because that
	// is exactly where the head end of a range reads them from.
	//
	// A clone's tip is history. It can predate any change to these files
	// — the id column, a pattern, a term — so a row that did not do this
	// would be measuring the checker against a vocabulary somebody
	// retired months ago, and would go green or red for reasons nothing
	// in the row can see. The subject of every row below is the checker
	// against the vocabulary this repository declares NOW.
	for _, path := range []string{citationPatternsPath, vendorTermsPath} {
		f.write(path, realRuleFile(t, path))
	}
	return f
}

// checked runs the command the way a workflow does and returns everything
// a workflow would see.
func checked(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	code = run(args, func(string) string { return "" }, &out, &errs)
	return code, out.String(), errs.String()
}

// TestTheRangeCheckAnswersTheThreeWaysItCan drives the whole command.
//
// THE THREE ANSWERS ARE THE POINT. Clean, found something, and could not
// look are different findings, and a check that spells the third like the
// first reports a shallow checkout as a clean range.
func TestTheRangeCheckAnswersTheThreeWaysItCan(t *testing.T) {
	rules := realRules(t)
	phrase, removed := aCitedPhrase(t, rules)

	t.Run("a non-empty clean range succeeds", func(t *testing.T) {
		// THE POSITIVE CONTROL, and it is the only row here that can tell
		// "this check is working" from "this check fails on everything".
		// Every row below asserts a failure, and a checker that always
		// failed would satisfy the lot of them.
		f := clonedRepo(t)
		base := strings.TrimSpace(f.git("rev-parse", "HEAD"))
		f.write("notes.txt", "an ordinary change\n")
		head := f.commit("pack: the walk reads the tree once\n\nNothing private here.\n", "notes.txt")

		code, stdout, stderr := checked(t, "-repo", f.dir, "-base", base, "-head", head,
			"-branch", "pack/read-the-tree-once")
		if code != exitClean {
			t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitClean, stdout, stderr)
		}
		if !strings.Contains(stdout, "examined 2 published surface(s)") {
			t.Errorf("stdout does not say how much it examined:\n%s", stdout)
		}
	})

	t.Run("the self-test runs before the range is examined", func(t *testing.T) {
		f := clonedRepo(t)
		base := strings.TrimSpace(f.git("rev-parse", "HEAD"))
		f.write("notes.txt", "an ordinary change\n")
		head := f.commit("pack: another ordinary change\n", "notes.txt")

		_, stdout, _ := checked(t, "-repo", f.dir, "-base", base, "-head", head)
		selfTested := strings.Index(stdout, "self-test:")
		examined := strings.Index(stdout, "examined ")
		if selfTested < 0 || examined < 0 {
			t.Fatalf("stdout is missing the self-test or the range:\n%s", stdout)
		}
		if selfTested > examined {
			t.Error("the range was examined before the checker proved it can answer both " +
				"ways. An empty range failing is satisfied by a check that always fails, and " +
				"a clean message passing by one that always passes; only the order makes " +
				"both impossible.")
		}
	})

	t.Run("a citation anywhere in the range fails, naming the commit", func(t *testing.T) {
		// DEEP IN THE RANGE, not at the tip: the candidate set is what a
		// merge publishes, and a message four commits back is published
		// exactly as loudly as the last one.
		f := clonedRepo(t)
		base := strings.TrimSpace(f.git("rev-parse", "HEAD"))
		f.write("notes.txt", "first\n")
		cited := f.commit("pack: tidy the walk\n\nas "+phrase+" says\n", "notes.txt")
		f.write("notes.txt", "second\n")
		head := f.commit("pack: an ordinary follow-up\n", "notes.txt")

		code, stdout, stderr := checked(t, "-repo", f.dir, "-base", base, "-head", head)
		if code != exitFindings {
			t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitFindings, stdout, stderr)
		}
		if !strings.Contains(stdout, short(cited)) {
			t.Errorf("the report does not name the commit that carries it:\n%s", stdout)
		}
		if !strings.Contains(stdout, removed) {
			t.Errorf("the report does not name the pattern that matched:\n%s", stdout)
		}
	})

	t.Run("a branch name carrying a citation fails", func(t *testing.T) {
		f := clonedRepo(t)
		base := strings.TrimSpace(f.git("rev-parse", "HEAD"))
		f.write("notes.txt", "an ordinary change\n")
		head := f.commit("pack: an ordinary message\n", "notes.txt")

		code, stdout, _ := checked(t, "-repo", f.dir, "-base", base, "-head", head,
			"-branch", "fix/"+phrase)
		if code != exitFindings {
			t.Fatalf("exit %d, want %d\nstdout:\n%s", code, exitFindings, stdout)
		}
		if !strings.Contains(stdout, "branch name") {
			t.Errorf("the report does not say the branch name was the surface:\n%s", stdout)
		}
	})

	t.Run("a message caught only by a line deleted in the same range fails", func(t *testing.T) {
		// THE ATTACK THE UNION EXISTS FOR, end to end: one push deletes
		// the line that would catch it and adds the message, together.
		f := clonedRepo(t)
		// THE BASE IS PINNED TO THE FILE THIS ROW IS ABOUT, rather than
		// to whatever the clone's tip happens to carry. The row asks what
		// happens when a range RETIRES a named rule, so the base has to
		// be a revision that declares that rule under that name — and the
		// tip of a clone is history, which may predate the id column
		// entirely and would have the narrowing reported under a
		// synthesised handle instead.
		f.write(citationPatternsPath, realRuleFile(t, citationPatternsPath))
		base := f.commit("the rule file as this working tree declares it", citationPatternsPath)
		f.write(citationPatternsPath, withoutRule(t, realRuleFile(t, citationPatternsPath), removed))
		head := f.commit("guard: retire a pattern\n\nand "+phrase+" while we are here\n",
			citationPatternsPath)

		code, stdout, stderr := checked(t, "-repo", f.dir, "-base", base, "-head", head)
		if code != exitFindings {
			t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitFindings, stdout, stderr)
		}
		if !strings.Contains(stdout, "pattern set narrowed") {
			t.Errorf("the narrowing was not reported:\n%s", stdout)
		}
		if !strings.Contains(stdout, short(head)) {
			t.Errorf("the message was not reported, so reading head alone would have "+
				"passed this push:\n%s", stdout)
		}
	})

	t.Run("a narrowing with nothing else in the range is the only finding", func(t *testing.T) {
		// So an intentional narrowing lands on its own and the diff shows
		// exactly what is being given up, rather than as a red nobody can
		// separate from the rest of a change.
		f := clonedRepo(t)
		// THE BASE IS PINNED TO THE FILE THIS ROW IS ABOUT, rather than
		// to whatever the clone's tip happens to carry. The row asks what
		// happens when a range RETIRES a named rule, so the base has to
		// be a revision that declares that rule under that name — and the
		// tip of a clone is history, which may predate the id column
		// entirely and would have the narrowing reported under a
		// synthesised handle instead.
		f.write(citationPatternsPath, realRuleFile(t, citationPatternsPath))
		base := f.commit("the rule file as this working tree declares it", citationPatternsPath)
		f.write(citationPatternsPath, withoutRule(t, realRuleFile(t, citationPatternsPath), removed))
		head := f.commit("guard: retire a pattern\n", citationPatternsPath)

		code, stdout, _ := checked(t, "-repo", f.dir, "-base", base, "-head", head)
		if code != exitFindings {
			t.Fatalf("exit %d, want %d\nstdout:\n%s", code, exitFindings, stdout)
		}
		narrowings := strings.Count(stdout, "pattern set narrowed")
		if narrowings != 1 {
			t.Errorf("%d narrowing lines, want exactly 1:\n%s", narrowings, stdout)
		}
		if strings.Contains(stdout, "matches ") || strings.Contains(stdout, "names infrastructure") {
			t.Errorf("something other than the narrowing was reported:\n%s", stdout)
		}
		if !strings.Contains(stdout, removed) {
			t.Errorf("the narrowing does not name the line that went:\n%s", stdout)
		}
	})

	t.Run("an empty range passes on the strength of the name it checked", func(t *testing.T) {
		// A branch created at the default tip: there are no commits
		// because there is nothing new, and its NAME was checked.
		f := clonedRepo(t)
		head := strings.TrimSpace(f.git("rev-parse", "HEAD"))

		code, stdout, stderr := checked(t, "-repo", f.dir, "-base", head, "-head", head,
			"-branch", "pack/nothing-new-yet")
		if code != exitClean {
			t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitClean, stdout, stderr)
		}
		if !strings.Contains(stdout, "examined 1 published surface(s)") {
			t.Errorf("stdout does not show that the name alone was examined:\n%s", stdout)
		}
	})

	t.Run("a range that examined nothing at all is not a pass", func(t *testing.T) {
		// The same empty range with no name: nothing was read, so this run
		// is evidence about nothing and must not be spelled like a clean
		// one.
		f := clonedRepo(t)
		head := strings.TrimSpace(f.git("rev-parse", "HEAD"))

		code, stdout, stderr := checked(t, "-repo", f.dir, "-base", head, "-head", head)
		if code != exitUndetermined {
			t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitUndetermined, stdout, stderr)
		}
		if !strings.Contains(stderr, "no published surface was examined") {
			t.Errorf("stderr does not say what happened:\n%s", stderr)
		}
	})

	t.Run("an unresolvable range says so, and says it differently", func(t *testing.T) {
		f := clonedRepo(t)
		head := strings.TrimSpace(f.git("rev-parse", "HEAD"))
		const absent = "0123456789012345678901234567890123456789"

		code, stdout, stderr := checked(t, "-repo", f.dir, "-base", absent, "-head", head,
			"-branch", "pack/some-branch")
		if code != exitUndetermined {
			t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitUndetermined, stdout, stderr)
		}
		if !strings.Contains(stderr, "could not be resolved") {
			t.Errorf("stderr does not say the range could not be resolved:\n%s", stderr)
		}
		// AND IT IS A DIFFERENT ANSWER FROM THE EMPTY RANGE ABOVE, which
		// passes. Collapsing the two is how a shallow checkout reports as
		// a clean one.
		if code == exitClean {
			t.Error("an unresolvable range is spelled the same way as a clean one")
		}
	})
}

// ---------------------------------------------------------------------
// Turning a workflow event into a range.
// ---------------------------------------------------------------------

func eventJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("building the event payload: %v", err)
	}
	return raw
}

// TestTheEventDecidesWhatIsPublished covers each trigger this check is
// wired to, plus the one it is not.
func TestTheEventDecidesWhatIsPublished(t *testing.T) {
	t.Run("a push takes the range the push itself declares", func(t *testing.T) {
		f := newFixture(t)
		f.write("notes.txt", "one\n")
		base := f.commit("first", "notes.txt")
		f.write("notes.txt", "two\n")
		head := f.commit("second", "notes.txt")

		req, err := requestFromEvent(f.repo, "push", "pack/some-branch", eventJSON(t, map[string]any{
			"before": base, "after": head, "ref": "refs/heads/pack/some-branch",
		}))
		if err != nil {
			t.Fatalf("resolving the push: %v", err)
		}
		if req.Base != base || req.Head != head {
			t.Errorf("range %s..%s, want %s..%s", req.Base, req.Head, base, head)
		}
		if len(req.Named) != 1 || req.Named[0].Text != "pack/some-branch" {
			t.Errorf("named surfaces %v, want the branch name alone", req.Named)
		}
	})

	t.Run("a new branch is measured against the default branch", func(t *testing.T) {
		f := newFixture(t)
		f.write("notes.txt", "one\n")
		first := f.commit("first", "notes.txt")
		f.git("checkout", "--quiet", "-b", "pack/new-branch")
		f.write("notes.txt", "two\n")
		head := f.commit("second", "notes.txt")

		req, err := requestFromEvent(f.repo, "push", "pack/new-branch", eventJSON(t, map[string]any{
			"before": zeroSHA, "after": head, "ref": "refs/heads/pack/new-branch",
			"repository": map[string]any{"default_branch": "main"},
		}))
		if err != nil {
			t.Fatalf("resolving the push: %v", err)
		}
		if req.Base != first {
			t.Errorf("base %s, want the merge base %s — a new branch with no before-sha has "+
				"to be measured against something, and the default branch is what a merge "+
				"would publish it into", req.Base, first)
		}
	})

	t.Run("a force-pushed branch is measured against the default branch", func(t *testing.T) {
		// THE BEFORE-SHA IS PRESENT AND GONE, which is not the same as
		// absent. Every rebase of a branch under review is a force push,
		// and the commit it names was discarded by the push itself — so
		// the checkout holds a range whose base is not in it.
		//
		// This used to refuse, correctly and PERMANENTLY: the same head
		// failed on every re-run, and since this check is required, the
		// merge queue could never take the branch. Measured on a real
		// one: "the base of the range could not be resolved".
		//
		// REQUIRED MUTATION, run 2026-09-09: trust the before-sha
		// without resolving it. Reds here, with the range unresolvable.
		f := newFixture(t)
		f.write("notes.txt", "one\n")
		first := f.commit("first", "notes.txt")
		f.git("checkout", "--quiet", "-b", "pack/rebased")
		f.write("notes.txt", "two\n")
		head := f.commit("second", "notes.txt")

		req, err := requestFromEvent(f.repo, "push", "pack/rebased", eventJSON(t, map[string]any{
			// A commit this checkout has never had, which is what a
			// discarded base looks like from here.
			"before": strings.Repeat("b", 40), "after": head,
			"ref":        "refs/heads/pack/rebased",
			"repository": map[string]any{"default_branch": "main"},
		}))
		if err != nil {
			t.Fatalf("resolving a force-pushed branch: %v", err)
		}
		if req.Base != first {
			t.Errorf("base %s, want the merge base %s — a base the push discarded "+
				"has to be measured against something, and a check that can never "+
				"go green blocks the queue rather than reporting anything",
				req.Base, first)
		}
		if req.Skip != "" {
			t.Errorf("a force-pushed branch was skipped (%q); its commits are as "+
				"published as any others", req.Skip)
		}
	})

	t.Run("a deleted branch is skipped, and says why", func(t *testing.T) {
		f := newFixture(t)
		req, err := requestFromEvent(f.repo, "push", "pack/gone", eventJSON(t, map[string]any{
			"before": strings.Repeat("a", 40), "after": zeroSHA, "deleted": true,
			"ref": "refs/heads/pack/gone",
		}))
		if err != nil {
			t.Fatalf("resolving the deletion: %v", err)
		}
		if req.Skip == "" {
			t.Fatal("a branch deletion was not skipped; there is no message, no tree and no " +
				"new name, so there is nothing being published to check")
		}
		if !strings.Contains(req.Skip, "pack/gone") {
			t.Errorf("the skip reason %q does not say what was deleted", req.Skip)
		}
	})

	t.Run("a tag push checks the tag's name and its message", func(t *testing.T) {
		f := newFixture(t)
		f.write("notes.txt", "one\n")
		base := f.commit("first", "notes.txt")
		f.write("notes.txt", "two\n")
		head := f.commit("second", "notes.txt")
		f.git("tag", "--annotate", "--message", "the release note lives elsewhere", "v9.9.9")

		req, err := requestFromEvent(f.repo, "push", "v9.9.9", eventJSON(t, map[string]any{
			"before": base, "after": head, "ref": "refs/tags/v9.9.9",
		}))
		if err != nil {
			t.Fatalf("resolving the tag push: %v", err)
		}
		var subjects []string
		for _, n := range req.Named {
			subjects = append(subjects, n.Subject)
		}
		if len(req.Named) != 2 || subjects[0] != "tag name" || subjects[1] != "tag message" {
			t.Fatalf("named surfaces %v, want the tag's name and its message", subjects)
		}
		if req.Named[0].Text != "v9.9.9" {
			t.Errorf("the tag name was read as %q", req.Named[0].Text)
		}
		if !strings.Contains(req.Named[1].Text, "the release note lives elsewhere") {
			t.Errorf("the tag message was read as %q", req.Named[1].Text)
		}
	})

	t.Run("a lightweight tag has a name and no message", func(t *testing.T) {
		// THE CONTROL for the row above: it must be the tag's own message
		// being read, not the commit's arriving under another name.
		f := newFixture(t)
		f.write("notes.txt", "one\n")
		base := f.commit("first", "notes.txt")
		f.write("notes.txt", "two\n")
		head := f.commit("second", "notes.txt")
		f.git("tag", "v9.9.8")

		req, err := requestFromEvent(f.repo, "push", "v9.9.8", eventJSON(t, map[string]any{
			"before": base, "after": head, "ref": "refs/tags/v9.9.8",
		}))
		if err != nil {
			t.Fatalf("resolving the tag push: %v", err)
		}
		if len(req.Named) != 1 || req.Named[0].Subject != "tag name" {
			t.Errorf("named surfaces %v, want the tag name alone", req.Named)
		}
	})

	t.Run("a pull request checks its title, its body and its branch", func(t *testing.T) {
		f := newFixture(t)
		f.write("notes.txt", "one\n")
		first := f.commit("first", "notes.txt")
		f.git("checkout", "--quiet", "-b", "pack/proposal")
		f.write("notes.txt", "two\n")
		head := f.commit("second", "notes.txt")
		f.git("checkout", "--quiet", "main")
		f.write("other.txt", "moved on\n")
		baseTip := f.commit("the base branch moved on", "other.txt")

		req, err := requestFromEvent(f.repo, "pull_request", "", eventJSON(t, map[string]any{
			"pull_request": map[string]any{
				"title": "pack: read the tree once",
				"body":  "the walk used to read it twice",
				"base":  map[string]any{"sha": baseTip},
				"head":  map[string]any{"sha": head, "ref": "pack/proposal"},
			},
		}))
		if err != nil {
			t.Fatalf("resolving the pull request: %v", err)
		}
		// THE MERGE BASE, not the base branch's tip: the range is what
		// this pull request adds, and the base branch having moved on is
		// not part of it.
		if req.Base != first {
			t.Errorf("base %s, want the merge base %s", req.Base, first)
		}
		var subjects []string
		for _, n := range req.Named {
			subjects = append(subjects, n.Subject)
		}
		want := []string{"branch name", "pull request title", "pull request body"}
		if strings.Join(subjects, ",") != strings.Join(want, ",") {
			t.Errorf("named surfaces %v, want %v — a squash merge composes its message out "+
				"of the title and the body, so they are published rather than unreachable",
				subjects, want)
		}
	})

	t.Run("a merge queue entry checks the commit it will land", func(t *testing.T) {
		f := newFixture(t)
		req, err := requestFromEvent(f.repo, "merge_group", "", eventJSON(t, map[string]any{
			"merge_group": map[string]any{
				"base_sha": strings.Repeat("a", 40),
				"head_sha": strings.Repeat("b", 40),
				"head_ref": "refs/heads/gh-readonly-queue/main/pr-1",
			},
		}))
		if err != nil {
			t.Fatalf("resolving the merge group: %v", err)
		}
		if req.Base != strings.Repeat("a", 40) || req.Head != strings.Repeat("b", 40) {
			t.Errorf("range %s..%s, want the queue's own ends", req.Base, req.Head)
		}
		if len(req.Named) != 1 {
			t.Errorf("named surfaces %v, want the queue ref", req.Named)
		}
	})

	t.Run("an event this check does not know is refused, not shrugged at", func(t *testing.T) {
		f := newFixture(t)
		_, err := requestFromEvent(f.repo, "workflow_dispatch", "", []byte(`{}`))
		if err == nil {
			t.Fatal("an unknown trigger was accepted; a check that shrugs at one is a check " +
				"somebody widens the triggers past")
		}
		if !strings.Contains(err.Error(), "workflow_dispatch") {
			t.Errorf("the refusal %q does not name the trigger it did not understand", err)
		}
	})
}
