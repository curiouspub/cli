package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// outsideAnyRepository returns a directory git will refuse to answer
// questions about, and PROVES it is one rather than assuming it.
//
// A temporary directory is normally nowhere near a checkout, but where a
// machine puts its temporary files is a property of the machine — and a
// temporary directory that happened to sit inside a repository would make
// every row below ask its question of a working git and pass on the
// wrong evidence. So the assumption is checked, and a failure here says
// what went wrong instead of quietly weakening the rows.
func outsideAnyRepository(t *testing.T) repo {
	t.Helper()
	dir := t.TempDir()
	r := repo{dir: dir}
	if out, err := r.run("rev-parse", "--git-dir"); err == nil {
		t.Fatalf("%s is inside a git repository (%s), so the rows below cannot ask what "+
			"happens outside one", dir, strings.TrimSpace(out))
	}
	return r
}

// undetermined asserts that an error is git declining to answer, with
// git's own words kept, rather than a statement about the repository.
func undetermined(t *testing.T, what string, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: git answered where it cannot have", what)
	}
	if errors.Is(err, errNotInHistory) {
		t.Errorf("%s: git not answering was reported as the checkout not carrying the "+
			"revision (%v).\nThose are opposite findings that look identical from a "+
			"distance, and the second one comes with advice that fixes nothing.", what, err)
	}
	var failure *gitError
	if !errors.As(err, &failure) {
		t.Fatalf("%s: the failure came back as %v, which carries nothing of git's own", what, err)
	}
	if failure.Stderr == "" && failure.Code >= 0 {
		t.Errorf("%s: git exited %d and its words were dropped, so whoever hits this is "+
			"told a diagnosis with no evidence under it", what, failure.Code)
	}
}

// TestTheThreeAnswersGitCanGive is the seam this repository cares about
// most, asked of the three commands that reach git.
//
// "I could not look" is not "I looked and found nothing", and the two
// arrive at the same call site: a revision that is absent and a directory
// with no repository in it both come back as an error from the same line.
// Collapsing them answers a missing repository with "fetch the full
// history and run again", which fixes nothing and is said with authority.
//
// EVERY ROW HAS BOTH DIRECTIONS. A row that only asserts the undetermined
// case is satisfied by a checker that calls everything undetermined,
// which would refuse every range it was ever given.
//
// MUTATIONS RUN, and what actually reddened is recorded at each.
func TestTheThreeAnswersGitCanGive(t *testing.T) {
	t.Run("a revision that is absent is not the same as a git that would not answer",
		func(t *testing.T) {
			// MUTATION: map every failure of rev-parse to errNotInHistory,
			// as it was before. The undetermined half reds, saying git not
			// answering was reported as the checkout not carrying the
			// revision; the two halves below stay green, which is the
			// measurement that says nothing else could see this.
			f := newFixture(t)
			f.write("notes.txt", "one\n")
			head := f.commit("first", "notes.txt")

			// THE ANSWER. A real repository, asked about a commit it does
			// not have, says so.
			const absent = "0123456789012345678901234567890123456789"
			if _, err := f.repo.resolve(absent); !errors.Is(err, errNotInHistory) {
				t.Errorf("a revision this checkout does not carry came back as %v, want the "+
					"not-in-history answer", err)
			}
			// AND THE CONTROL: the same call on a commit it does have.
			if got, err := f.repo.resolve(head); err != nil || got != head {
				t.Errorf("resolving a commit this checkout carries gave %q, %v", got, err)
			}

			// THE REFUSAL.
			_, err := outsideAnyRepository(t).resolve(head)
			undetermined(t, "resolving a revision with no repository to resolve it in", err)
		})

	t.Run("two commits with nothing in common is not the same as nowhere to ask",
		func(t *testing.T) {
			// MUTATION: map every failure of merge-base to errNotInHistory.
			// The undetermined half reds exactly as above and the other two
			// stay green.
			f := newFixture(t)
			f.write("notes.txt", "one\n")
			first := f.commit("first", "notes.txt")
			f.git("checkout", "--quiet", "--orphan", "unrelated")
			second := f.commit("a history with nothing in common")

			// THE ANSWER, and it is a real one: these two share nothing.
			if _, err := f.repo.mergeBase(first, second); !errors.Is(err, errNotInHistory) {
				t.Errorf("two unrelated histories came back as %v, want the not-in-history "+
					"answer", err)
			}
			// THE CONTROL. A pair that does share a commit answers with it.
			if got, err := f.repo.mergeBase(first, first); err != nil || got != first {
				t.Errorf("the merge base of a commit with itself gave %q, %v", got, err)
			}

			// THE REFUSAL.
			_, err := outsideAnyRepository(t).mergeBase(first, second)
			undetermined(t, "asking for a merge base with no repository to ask in", err)
		})

	t.Run("a tag is read by its ref, so its NAME cannot decide what is read", func(t *testing.T) {
		// THE ROW FOR A NAME IN OPTION POSITION. A tag name is chosen by
		// whoever pushed it, and asking git to list tags by pattern puts
		// that name where an option goes. Observed against real tags: a
		// name shaped like an option is eaten as one and the pattern slot
		// is left empty, so every tag in the repository comes back as this
		// one's message.
		//
		// MUTATION: ask `git tag --list --format=%(contents) <name>` again.
		// This row reds on the absence half, reporting that the message of
		// the OTHER tag arrived as this one's — which is the shape the
		// defect actually has, and is worse than the missing message it
		// was described as.
		f := newFixture(t)
		f.write("notes.txt", "one\n")
		f.commit("first", "notes.txt")

		const otherMessage = "the message of a tag nobody asked about"
		const ownMessage = "the message of the tag with the awkward name"
		f.git("tag", "--annotate", "--message", otherMessage, "an-ordinary-tag")
		f.git("tag", "--annotate", "--message", ownMessage, "a-name-to-move")
		object := strings.TrimSpace(f.git("rev-parse", "refs/tags/a-name-to-move"))
		// git will not CREATE a tag whose name begins with a dash, and a
		// remote can carry one all the same, so the ref is written
		// directly. That is the whole point: this name is not one this
		// repository chose.
		f.git("update-ref", "refs/tags/-n1", object)
		f.git("tag", "--delete", "a-name-to-move")

		message, annotated, err := f.repo.tagMessage("-n1")
		if err != nil {
			t.Fatalf("reading the message of a tag with an option-shaped name: %v", err)
		}
		if !annotated || !strings.Contains(message, ownMessage) {
			t.Errorf("the tag's own message did not come back (annotated=%v): %q",
				annotated, message)
		}
		if strings.Contains(message, otherMessage) {
			t.Errorf("another tag's message arrived as this one's: %q\n"+
				"The name was read as an option and the pattern slot left empty, so what came "+
				"back is every tag in the repository — scanned, and reported against a surface "+
				"none of it came from.", message)
		}
	})

	t.Run("a tag that is not there, a tag with no message, and nowhere to look",
		func(t *testing.T) {
			f := newFixture(t)
			f.write("notes.txt", "one\n")
			f.commit("first", "notes.txt")
			f.git("tag", "lightweight")
			f.git("tag", "--annotate", "--message", "an ordinary message", "annotated")

			// PRESENT, WITH A MESSAGE.
			if _, annotated, err := f.repo.tagMessage("annotated"); err != nil || !annotated {
				t.Errorf("an annotated tag came back as annotated=%v, %v", annotated, err)
			}
			// PRESENT, WITH NO MESSAGE. An absence, and a real one.
			if _, annotated, err := f.repo.tagMessage("lightweight"); err != nil || annotated {
				t.Errorf("a lightweight tag came back as annotated=%v, %v", annotated, err)
			}
			// NOT PRESENT AT ALL. Also an absence, and a different one.
			if _, _, err := f.repo.tagMessage("never-existed"); !errors.Is(err, errNotInHistory) {
				t.Errorf("a tag this checkout does not carry came back as %v, want the "+
					"not-in-history answer", err)
			}
			// AND NOWHERE TO LOOK, which is none of the three above.
			_, _, err := outsideAnyRepository(t).tagMessage("annotated")
			undetermined(t, "reading a tag with no repository to read it from", err)
		})
}

// TestWhatWasReadIsKeptWhenTheRestCannotBe covers a run that fails part
// way through.
//
// The named surfaces — a branch name, a tag's name and message, a pull
// request's title — are read BEFORE the range is walked. If the walk then
// fails, a citation already found in one of them is a fact this program
// is holding, and dropping it on the way out leaves the author with an
// undetermined run and no idea that anything was found. The run stays
// undetermined either way, because what could not be read may carry more;
// that decision does not need the evidence destroyed to make it.
//
// MUTATION RUN: return nil findings from examine on the failing paths, as
// it did before. Both rows here red — the unit one saying nothing was
// kept, the end-to-end one saying the branch name was not reported — and
// nothing else in the package moves, because every other row reaches a
// range that can be read.
func TestWhatWasReadIsKeptWhenTheRestCannotBe(t *testing.T) {
	rules := realRules(t)
	phrase, _ := aCitedPhrase(t, rules)

	t.Run("examine returns what it read before the range failed", func(t *testing.T) {
		f := newFixture(t)
		f.write("notes.txt", "one\n")
		head := f.commit("first", "notes.txt")
		req := request{Named: []named{{Subject: "branch name", Text: "fix/" + phrase}}}

		const absent = "0123456789012345678901234567890123456789"
		findings, examined, err := examine(f.repo, rules, req, func() ([]string, error) {
			return f.repo.commits(absent, head)
		})
		if err == nil {
			t.Fatal("a range whose base is not in the repository was walked without error")
		}
		if len(findings) == 0 {
			t.Error("the branch name was read, carried a citation, and was thrown away when " +
				"the commits behind it could not be walked")
		}
		if examined == 0 {
			t.Error("a surface was read and the count came back at zero, so the floor above " +
				"cannot tell this from a run that read nothing")
		}
	})

	t.Run("and the command reports it, while still refusing to call the run clean",
		func(t *testing.T) {
			// END TO END, over a range whose middle commit is gone from the
			// object store: both ends resolve, and the walk between them
			// cannot be done. Observed rather than supposed — git answers
			// rev-parse for both endpoints and fails the revision walk.
			f := clonedRepo(t)
			base := strings.TrimSpace(f.git("rev-parse", "HEAD"))
			f.write("notes.txt", "one\n")
			middle := f.commit("pack: a commit whose object is about to go", "notes.txt")
			f.write("notes.txt", "two\n")
			head := f.commit("pack: an ordinary follow-up\n", "notes.txt")

			loose := filepath.Join(f.dir, ".git", "objects", middle[:2], middle[2:])
			if err := os.Remove(loose); err != nil {
				t.Fatalf("removing the middle commit's object: %v", err)
			}

			code, stdout, stderr := checked(t, "-repo", f.dir, "-base", base, "-head", head,
				"-branch", "fix/"+phrase)
			if code != exitUndetermined {
				t.Fatalf("exit %d, want %d — a range that could not be walked is not a clean "+
					"range\nstdout:\n%s\nstderr:\n%s", code, exitUndetermined, stdout, stderr)
			}
			if !strings.Contains(stdout, "branch name") {
				t.Errorf("the branch name carried a citation and the run says nothing about "+
					"it:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
			}
			// AND THE CONTROL. The same repository with its objects intact
			// reaches the range, so the row above is about the failure
			// rather than about the fixture.
			f2 := clonedRepo(t)
			base2 := strings.TrimSpace(f2.git("rev-parse", "HEAD"))
			f2.write("notes.txt", "one\n")
			head2 := f2.commit("pack: an ordinary message\n", "notes.txt")
			if code, stdout, stderr := checked(t, "-repo", f2.dir, "-base", base2, "-head", head2,
				"-branch", "pack/an-ordinary-name"); code != exitClean {
				t.Fatalf("the intact control exited %d, want %d\nstdout:\n%s\nstderr:\n%s",
					code, exitClean, stdout, stderr)
			}
		})
}
