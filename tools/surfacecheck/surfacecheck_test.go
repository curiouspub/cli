package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------
// Fixtures, and why none of them is written down.
// ---------------------------------------------------------------------

// moduleRoot walks up from this package to the directory holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("locating the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// realRules compiles this repository's own rule files as they stand.
func realRules(t *testing.T) Rules {
	t.Helper()
	tree := repo{dir: moduleRoot(t)}.workingTree()
	rules, narrowings, err := LoadRules(tree, tree)
	if err != nil {
		t.Fatalf("loading this repository's own rule files: %v", err)
	}
	if len(narrowings) != 0 {
		t.Fatalf("one end compared against itself reported %d narrowing(s); a set cannot be "+
			"narrower than itself, so the comparison is wrong", len(narrowings))
	}
	return rules
}

// aCitedPhrase returns a phrase the real pattern file catches, together
// with the single pattern that catches it.
//
// IT IS DERIVED AT RUN TIME AND NEVER WRITTEN DOWN, which is the same
// discipline the content rule's own fixtures follow and for the same
// reason: this file is authored text, the check reads it, and a literal
// example of what it forbids would be the leak arriving through the test
// that hunts it. Every candidate below is assembled from pieces that
// match nothing on their own.
//
// EXACTLY ONE pattern must match, because the rows using this ask what a
// SINGLE line in the manifest is worth. A phrase two patterns catch would
// still be caught with either one deleted, and the deletion row would
// report a property nobody has.
func aCitedPhrase(t *testing.T, rules Rules) (phrase, rule string) {
	t.Helper()
	candidates := []string{
		strings.ToUpper("learnings"),
		"acceptance " + "check",
	}
	for _, candidate := range candidates {
		var matched []string
		for _, f := range rules.Scan("candidate", candidate) {
			matched = append(matched, f.PatternID)
		}
		if len(matched) == 1 {
			return candidate, matched[0]
		}
	}
	t.Fatal("no candidate phrase is caught by exactly one pattern in the real manifest, so " +
		"the rows below cannot ask what one line is worth. Either the manifest changed under " +
		"them or the candidates have stopped matching; both are findings rather than reasons " +
		"to skip.")
	return "", ""
}

// aForbiddenName returns a provider term from the real vocabulary and an
// ordinary English word that CONTAINS it as a substring.
//
// The word is written down and the term is not, which is the whole shape
// of the control: "draws" carries a forbidden name inside it and is not
// one, so it must pass — and the row is worthless unless the containment
// is real, which is asserted here rather than assumed.
func aForbiddenName(t *testing.T, rules Rules) (term, id, ordinary string) {
	t.Helper()
	const word = "draws"
	vocabulary := rules.VendorTerms()
	var terms []string
	for candidate := range vocabulary {
		if strings.Contains(word, candidate) {
			terms = append(terms, candidate)
		}
	}
	sort.Strings(terms)
	if len(terms) == 0 {
		t.Fatalf("%q no longer contains any term from the real vocabulary, so the "+
			"tokenisation control below proves nothing about substring matching — a word "+
			"merely adjacent in meaning is not the shape this row needs", word)
	}
	return terms[0], vocabulary[terms[0]], word
}

// ---------------------------------------------------------------------
// What the checker finds on one surface.
// ---------------------------------------------------------------------

// TestScanReadsThePublishedSurfaces drives the scanner against this
// repository's real vocabulary.
//
// EVERY ROW ASSERTING AN ABSENCE ALSO ASSERTS A PRESENCE, because an
// absence on its own is satisfied by a scanner that finds nothing ever —
// which is exactly the failure the rows about empty ranges cannot see.
func TestScanReadsThePublishedSurfaces(t *testing.T) {
	rules := realRules(t)
	phrase, rule := aCitedPhrase(t, rules)
	term, termID, ordinary := aForbiddenName(t, rules)

	t.Run("a message carrying a private citation is reported with the pattern that caught it",
		func(t *testing.T) {
			found := rules.Scan("commit abcdef0", "tidy up the walk\n\nas "+phrase+" says\n")
			if len(found) == 0 {
				t.Fatal("a message carrying a citation produced no finding")
			}
			if found[0].Subject != "commit abcdef0" {
				t.Errorf("finding names the subject %q, want the commit it came from",
					found[0].Subject)
			}
			if found[0].PatternID != rule {
				t.Errorf("finding names the rule %q, want %q — an author who is not told "+
					"which line caught them cannot tell a real citation from a false one",
					found[0].PatternID, rule)
			}
			if found[0].Line != 3 {
				t.Errorf("finding is on line %d, want 3: a body line is not the subject line",
					found[0].Line)
			}
			// THE PRESENCE'S OWN CONTROL. Without this, the row above is
			// satisfied by a scanner that reports every message.
			if clean := rules.Scan("commit abcdef0", "tidy up the walk\n\nno citation here\n"); len(clean) != 0 {
				t.Errorf("an ordinary message produced %d finding(s): %v", len(clean), clean)
			}
		})

	t.Run("a forbidden name inside an ordinary word passes, and as its own subword fails",
		func(t *testing.T) {
			// THE ABSENCE. The word really contains the term — asserted in
			// aForbiddenName, not assumed — so this is a substring that
			// must not match, rather than a word that happens to be safe.
			if found := rules.Scan("commit abcdef0", "the walk "+ordinary+" its file list once"); len(found) != 0 {
				t.Errorf("%q was reported: %v\nan ordinary English word containing a "+
					"forbidden name is not a naming of it, and a check that reds on prose "+
					"is a check somebody switches off", ordinary, found)
			}
			// THE PRESENCE. Spelled as its own subword inside an
			// identifier, the same term must be seen — otherwise the row
			// above is passing because the vocabulary is off.
			spelled := "handoff to " + strings.ToUpper(term[:1]) + term[1:] + "Runtime"
			found := rules.Scan("commit abcdef0", spelled)
			// NAMED BY ITS ID, which is the whole of what a finding now
			// carries about which rule fired. The term itself is not on
			// the struct: the text that matched IS the thing this check
			// exists to keep out of published places, and a report is a
			// published place.
			if len(found) != 1 || !found[0].Infrastructure || found[0].PatternID != termID {
				t.Errorf("the same term spelled as a subword produced %v, want one "+
					"infrastructure finding under %s", found, termID)
			}
		})

	t.Run("a branch name carrying a private citation is reported", func(t *testing.T) {
		found := rules.Scan("branch name", "fix/"+phrase)
		if len(found) != 1 {
			t.Fatalf("a branch name carrying a citation produced %d finding(s), want 1", len(found))
		}
		if found[0].Line != 0 {
			t.Errorf("the finding is on line %d; a branch name has no lines to number, and "+
				"printing one invites somebody to go looking for it", found[0].Line)
		}
		if clean := rules.Scan("branch name", "fix/the-walk-reads-once"); len(clean) != 0 {
			t.Errorf("an ordinary branch name produced %d finding(s): %v", len(clean), clean)
		}
	})

	t.Run("every match on a line is reported, not the first", func(t *testing.T) {
		found := rules.Scan("commit abcdef0", phrase+" and "+phrase)
		if len(found) != 2 {
			t.Errorf("a line naming two forbidden things produced %d finding(s), want 2\n"+
				"a check that reveals its findings one per run gets a reputation for moving "+
				"goalposts", len(found))
		}
	})

	t.Run("an empty vocabulary is not a clean answer", func(t *testing.T) {
		if !(Rules{}).Empty() {
			t.Error("a rule set with nothing in it does not report itself as empty, so a " +
				"vocabulary that lost its contents would pass everything silently")
		}
		if rules.Empty() {
			t.Error("the real vocabulary reports itself as empty, so every row above " +
				"scanned against nothing")
		}
	})
}
