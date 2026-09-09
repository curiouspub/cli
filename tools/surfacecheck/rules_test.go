package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------
// A repository to check, built here rather than borrowed.
// ---------------------------------------------------------------------

// fixture is a small real repository. The rows below drive the same git
// commands the checker runs in a workflow, because the seam this check
// fails at is precisely the one between what git was asked and what it
// answered — and a fake answering plausibly cannot fail that way.
type fixture struct {
	t    *testing.T
	dir  string
	repo repo
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, dir: dir, repo: repo{dir: dir}}
	f.git("init", "--quiet")
	// Named rather than inherited: the initial branch git chooses depends
	// on its version and on the operator's own configuration, and a row
	// about a default branch cannot be written against a name that
	// changes per machine.
	f.git("symbolic-ref", "HEAD", "refs/heads/main")
	f.git("config", "user.name", "surface check fixture")
	f.git("config", "user.email", "fixture@example.invalid")
	f.git("config", "commit.gpgsign", "false")
	return f
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	out, err := f.repo.run(args...)
	if err != nil {
		f.t.Fatalf("fixture: %v", err)
	}
	return out
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

// commit stages the named paths and nothing else, then writes the
// message through a file so a multi-line body survives intact.
func (f *fixture) commit(message string, paths ...string) string {
	f.t.Helper()
	f.git(append([]string{"add", "--"}, paths...)...)
	messageFile := filepath.Join(f.t.TempDir(), "message")
	if err := os.WriteFile(messageFile, []byte(message), 0o644); err != nil {
		f.t.Fatalf("fixture: %v", err)
	}
	f.git("commit", "--quiet", "--allow-empty", "--file", messageFile)
	return strings.TrimSpace(f.git("rev-parse", "HEAD"))
}

// realRuleFile reads one of this repository's own rule files.
func realRuleFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot(t), filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// withoutLine returns a rule file with exactly one data line removed, and
// fails if that line was not there — an edit that changed nothing is not
// a narrowing, and a row resting on one proves nothing.
func withoutLine(t *testing.T, text, line string) string {
	t.Helper()
	var kept []string
	removed := 0
	for _, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == line {
			removed++
			continue
		}
		kept = append(kept, l)
	}
	if removed != 1 {
		t.Fatalf("removing %q from the rule file took out %d line(s), want exactly 1", line, removed)
	}
	return strings.Join(kept, "\n")
}

// narrowedRepo builds a repository whose BASE declares this project's
// real rule files and whose HEAD has one pattern line taken out, with a
// commit in between whose message only that line would have caught.
//
// That is the shape of the attack the union exists for: one push deletes
// the line that would catch it and adds the message, together.
func narrowedRepo(t *testing.T) (f *fixture, base, head, phrase, removed string) {
	t.Helper()
	rules := realRules(t)
	phrase, removed = aCitedPhrase(t, rules)

	patterns := realRuleFile(t, citationPatternsPath)
	vendor := realRuleFile(t, vendorTermsPath)

	f = newFixture(t)
	f.write(citationPatternsPath, patterns)
	f.write(vendorTermsPath, vendor)
	base = f.commit("the rules as they stand", citationPatternsPath, vendorTermsPath)

	f.write(citationPatternsPath, withoutLine(t, patterns, removed))
	head = f.commit("tidy the walk\n\nas "+phrase+" says\n", citationPatternsPath)
	return f, base, head, phrase, removed
}

// widenedRepo is narrowedRepo's mirror: a repository whose BASE lacks one
// pattern line and whose HEAD declares it, with a message only that line
// would catch.
//
// A range that ADDS a rule is the ordinary case — it is what tightening
// the vocabulary looks like — and the rows built on narrowedRepo cannot
// see it at all.
func widenedRepo(t *testing.T) (f *fixture, base, phrase, added string) {
	t.Helper()
	rules := realRules(t)
	phrase, added = aCitedPhrase(t, rules)

	patterns := realRuleFile(t, citationPatternsPath)
	vendor := realRuleFile(t, vendorTermsPath)

	f = newFixture(t)
	f.write(citationPatternsPath, withoutLine(t, patterns, added))
	f.write(vendorTermsPath, vendor)
	base = f.commit("the rules before the line was added", citationPatternsPath, vendorTermsPath)

	// HEAD IS THE WORKING TREE, which is where this loader reads it, and
	// the line is back.
	f.write(citationPatternsPath, patterns)
	return f, base, phrase, added
}

// TestALineADDEDInTheRangeIsInForceForIt is the direction the union's
// other rows cannot see.
//
// They cover a line DELETED in the range, which the base's copy keeps
// alive, and a range that narrows nothing. Every one of them is satisfied
// by a loader that reads the BASE alone and consults head only to work
// out what went missing — measured, not supposed: with the union replaced
// by the base's lines, the whole package stayed green. Such a loader
// would miss every message caught by a rule added in the range that
// contains it, which is what tightening the vocabulary looks like.
//
// BOTH DIRECTIONS. The base alone must not catch the phrase, or the
// presence below says nothing about where the rule came from.
func TestALineADDEDInTheRangeIsInForceForIt(t *testing.T) {
	f, base, phrase, added := widenedRepo(t)

	// THE ABSENCE. The base predates the line, so on its own it is blind
	// to the phrase.
	before, _, err := LoadRules(f.repo.atRevision(base), f.repo.atRevision(base))
	if err != nil {
		t.Fatalf("loading the rules at the base: %v", err)
	}
	if got := before.Scan("commit", phrase); len(got) != 0 {
		t.Fatalf("the base already catches the phrase (%v), so the row below cannot attribute "+
			"anything to the line that was added", got)
	}

	// THE PRESENCE. The union takes head's copy, so the new line is in
	// force for the range that introduces it.
	union, narrowings, err := LoadRules(f.repo.atRevision(base), f.repo.workingTree())
	if err != nil {
		t.Fatalf("loading the union: %v", err)
	}
	if got := union.Scan("commit", phrase); len(got) == 0 {
		t.Errorf("a message caught only by %q — a line this range ADDS — was not reported.\n"+
			"A loader reading the base alone would pass every other row here and miss every "+
			"message a newly added rule is meant to catch.", added)
	}

	// AND NOTHING WAS GIVEN UP. A range that only adds must report no
	// narrowing, or "a narrowing is a finding" reds on every tightening.
	if len(narrowings) != 0 {
		t.Errorf("a range that adds a rule reported %d narrowing(s): %v", len(narrowings), narrowings)
	}
}

// ---------------------------------------------------------------------
// The rule files are READ, at both ends.
// ---------------------------------------------------------------------

// TestTheRuleFilesAreReadAtBothEnds is the pair of properties this whole
// loader exists for, and each is asserted in both directions.
//
// MUTATIONS RUN AGAINST THE REAL LOADER, with what each one ACTUALLY
// reddened rather than what it was expected to. The files were preserved
// by copy and the restores verified by checksum.
//
//   - unionOf given its own inlined pattern list for the citation file,
//     reading neither end -> "deleting a line measurably changes what is
//     caught" reds on its ABSENCE half, saying the phrase is still
//     reported with the line removed. It was predicted to go GREEN, on
//     the reasoning that an inlined copy makes the deletion invisible.
//     It cannot: the row asserts both directions, and an inlined copy
//     satisfies the presence half while destroying the absence half. A
//     row that only asked for "different output" would indeed have gone
//     green, which is why it does not ask that. Four further rows red
//     with it — both narrowing rows, the empty-file refusal, and the
//     self-test's own historical half, that last because a vocabulary
//     cut down to one pattern no longer catches the message it must.
//
//   - unionOf returning head's lines alone -> exactly two rows here red:
//     "a line deleted inside the range still catches" and "a narrowing is
//     reported with its line named". End to end, the same mutation makes
//     the whole command print "clean." and exit 0 on a push that deletes
//     the catching line and adds the message together. The self-test
//     stays green through it, which is the reason these are separate
//     controls: the historical message is caught by a pattern head still
//     declares, so nothing about the union is visible from there.
func TestTheRuleFilesAreReadAtBothEnds(t *testing.T) {
	f, base, head, phrase, removed := narrowedRepo(t)

	t.Run("deleting a line measurably changes what is caught", func(t *testing.T) {
		// THE PRESENCE. The file as the base declares it catches the
		// phrase, so the absence below is about the deleted line rather
		// than about a checker that finds nothing.
		full, _, err := LoadRules(f.repo.atRevision(base), f.repo.atRevision(base))
		if err != nil {
			t.Fatalf("loading the rules at the base: %v", err)
		}
		if got := full.Scan("commit", phrase); len(got) == 0 {
			t.Fatal("the rule file as committed catches nothing, so the row below cannot " +
				"attribute an absence to the deleted line")
		}

		// THE ABSENCE. One line out, and the same phrase is invisible.
		// "Different output" alone would be satisfied by a checker that
		// reported the manifest's length, so both halves are named.
		narrowed, _, err := LoadRules(f.repo.atRevision(head), f.repo.atRevision(head))
		if err != nil {
			t.Fatalf("loading the rules at head: %v", err)
		}
		if got := narrowed.Scan("commit", phrase); len(got) != 0 {
			t.Errorf("with %q removed from %s the phrase is still reported (%v)\n"+
				"Deleting a line has to change what this catches, or nobody can check the "+
				"file is really being read", removed, citationPatternsPath, got)
		}
	})

	t.Run("a line deleted inside the range still catches, because the union has the base's copy",
		func(t *testing.T) {
			// THE ROW THE HEAD-ONLY READING WOULD PASS. The working tree
			// is head, where the line is gone; the base still has it.
			union, _, err := LoadRules(f.repo.atRevision(base), f.repo.workingTree())
			if err != nil {
				t.Fatalf("loading the union: %v", err)
			}
			if got := union.Scan("commit", phrase); len(got) == 0 {
				t.Error("a message caught only by a pattern deleted in the same range was " +
					"not reported. One push can otherwise delete the line that would catch " +
					"it and add the message, together, and this check loads the weakened " +
					"file and passes.")
			}

			// And its control: read head alone — which is what the
			// content rule does, correctly, on a surface with no range —
			// and the same phrase goes through. The two ideas are each
			// right; their combination is not.
			headOnly, _, err := LoadRules(f.repo.workingTree(), f.repo.workingTree())
			if err != nil {
				t.Fatalf("loading head alone: %v", err)
			}
			if got := headOnly.Scan("commit", phrase); len(got) != 0 {
				t.Errorf("head alone still reports the phrase (%v), so the row above passes "+
					"whether or not the union is doing anything", got)
			}
		})

	t.Run("a narrowing is reported with its line named", func(t *testing.T) {
		_, narrowings, err := LoadRules(f.repo.atRevision(base), f.repo.workingTree())
		if err != nil {
			t.Fatalf("loading the union: %v", err)
		}
		if len(narrowings) != 1 {
			t.Fatalf("the range reports %d narrowing(s), want exactly 1: %v", len(narrowings), narrowings)
		}
		if narrowings[0].Path != citationPatternsPath || narrowings[0].Line != removed {
			t.Errorf("narrowing reported as %+v, want %s / %q — a narrowing nobody can read "+
				"is a red nobody can act on", narrowings[0], citationPatternsPath, removed)
		}

		// THE CONTROL. A range that narrows nothing reports nothing, so
		// the row above is not satisfied by a loader that always reports.
		_, none, err := LoadRules(f.repo.atRevision(base), f.repo.atRevision(base))
		if err != nil {
			t.Fatalf("loading the base against itself: %v", err)
		}
		if len(none) != 0 {
			t.Errorf("a range that narrows nothing reports %d narrowing(s): %v", len(none), none)
		}
	})
}

// TestTheLoaderRefusesWhatItCannotCheckWith covers the two ways the
// vocabulary can go quiet. Neither is a failure git reports, and both
// leave a check that passes everything.
func TestTheLoaderRefusesWhatItCannotCheckWith(t *testing.T) {
	t.Run("an empty pattern file is refused rather than obeyed", func(t *testing.T) {
		f := newFixture(t)
		f.write(citationPatternsPath, "# every line here is prose\n")
		f.write(vendorTermsPath, realRuleFile(t, vendorTermsPath))
		base := f.commit("rules with nothing in one of them", citationPatternsPath, vendorTermsPath)

		if _, _, err := LoadRules(f.repo.atRevision(base), f.repo.workingTree()); err == nil {
			t.Error("a pattern file declaring nothing was accepted; this check would then " +
				"pass every message it was ever given, and say so in the same words a clean " +
				"range does")
		}

		// THE PRESENCE. The same fixture with real patterns loads, so the
		// refusal above is about emptiness rather than about the fixture.
		f.write(citationPatternsPath, realRuleFile(t, citationPatternsPath))
		head := f.commit("rules restored", citationPatternsPath)
		if _, _, err := LoadRules(f.repo.atRevision(head), f.repo.workingTree()); err != nil {
			t.Errorf("a fixture with real rule files was refused: %v", err)
		}
	})

	t.Run("a rule file neither end declares is refused", func(t *testing.T) {
		f := newFixture(t)
		f.write("README.md", "a repository with no rule files at all\n")
		base := f.commit("no rules anywhere", "README.md")

		_, _, err := LoadRules(f.repo.atRevision(base), f.repo.workingTree())
		if err == nil {
			t.Fatal("a repository declaring no vocabulary at all was accepted")
		}
		if !strings.Contains(err.Error(), citationPatternsPath) {
			t.Errorf("the refusal is %q and does not name the file that is missing; an "+
				"operator mid-procedure is the least able to guess", err)
		}
	})
}

// TestSamenessIsTheFILESOwnQuestion covers an edit that gives nothing up
// being reported as though it did.
//
// A term is looked up after lowering, so two spellings of one term are
// one rule: changing the case of a letter in the vocabulary retires
// nothing. A pattern is compiled exactly as written, so two spellings are
// two different patterns and swapping one for the other really does
// retire the first. Compared verbatim, both read as a narrowing — and a
// narrowing is a finding, so an edit that changed nothing fails a run and
// teaches its reader that this report cries wolf.
//
// THE SAME EDIT IN BOTH FILES, which is what makes this a row about the
// files rather than about lowercasing: one marker line, re-cased in each,
// and exactly one narrowing comes back.
func TestSamenessIsTheFILESOwnQuestion(t *testing.T) {
	const asWritten, recased = "ZZMARKERONLYFORTHISROW", "zzmarkeronlyforthisrow"

	f := newFixture(t)
	patterns := realRuleFile(t, citationPatternsPath)
	vendor := realRuleFile(t, vendorTermsPath)
	f.write(citationPatternsPath, patterns+"\n"+asWritten+"\n")
	f.write(vendorTermsPath, vendor+"\n"+asWritten+"\n")
	base := f.commit("a marker line in both rule files", citationPatternsPath, vendorTermsPath)

	// HEAD IS THE WORKING TREE, and the only edit is the case of that one
	// line in each file.
	f.write(citationPatternsPath, patterns+"\n"+recased+"\n")
	f.write(vendorTermsPath, vendor+"\n"+recased+"\n")

	rules, narrowings, err := LoadRules(f.repo.atRevision(base), f.repo.workingTree())
	if err != nil {
		t.Fatalf("loading the union: %v", err)
	}

	// THE ABSENCE. Nothing was given up in the vocabulary.
	for _, n := range narrowings {
		if n.Path == vendorTermsPath {
			t.Errorf("re-casing a term reported a narrowing (%+v).\nA term is looked up after "+
				"lowering, so both spellings are the same rule and nothing was retired — and a "+
				"narrowing is a finding, so this fails a run over an edit that changed nothing.",
				n)
		}
	}
	// THE PRESENCE, in the same breath: the term still matches, so the
	// absence above is about sameness rather than about a term that fell
	// out of the union altogether.
	if got := rules.Scan("commit", "a line naming "+asWritten+" outright"); len(got) == 0 {
		t.Error("the re-cased term matches nothing at either end, so the row above is passing " +
			"because the vocabulary lost it rather than because it kept it")
	}

	// AND THE CONTROL, which is the same edit in the other file. A pattern
	// is compiled as written; the two spellings are two patterns, and
	// swapping them retires one.
	saw := 0
	for _, n := range narrowings {
		if n.Path == citationPatternsPath && n.Line == asWritten {
			saw++
		}
	}
	if saw != 1 {
		t.Errorf("re-casing a pattern reported %d narrowing(s) for %s, want exactly 1: %v\n"+
			"Without this the row above is satisfied by a loader that has stopped reporting "+
			"narrowings at all.", saw, citationPatternsPath, narrowings)
	}
}
