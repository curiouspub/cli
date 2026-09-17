package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// THE PRIVATE VOCABULARY, PROVED ON THE THING IT WAS WRITTEN FOR.
//
// Every other rule in the citation manifest matches a SHAPE — a sign and
// a number, a letter and digits — and can be demonstrated against a
// string written right here, because writing one down discloses nothing.
// These three match WORDS in a citing context, and a fixture spelling one
// out would be the citation shipping inside the file that hunts it. That
// is the same objection the ledger of already-published matches answers
// by recording a blob and a rule id and never the text that matched.
//
// So the evidence is a GIT OBJECT, named by its hash and read at run
// time. It is a revision of the file next door, from before the citations
// in it were rewritten. It is already published — which is exactly why
// naming it costs nothing and proves everything: the shape is reachable
// to any stranger who clones this repository, so a rule claiming to catch
// it can be held to that claim rather than to a description of it.
//
// THE OBJECT DOES NOT GO AWAY, and that was worth establishing rather
// than assuming. A branch merged here keeps its own commits — the merge
// records two parents rather than replaying the work as one new commit,
// and the branch is not deleted afterwards — so every object on it stays
// reachable from the trunk once it lands. The ledger entries beside these
// rules are therefore permanent records of something permanently
// published, which is what a ledger is for; had the history been replayed
// instead, both they and this row would have been describing objects
// nobody could reach.
//
// IF THE OBJECT IS EVER GONE THIS ROW FAILS RATHER THAN SKIPPING, and the
// message says what to do about it. A skip would leave a rule that reads
// as proved and is not, which is the one outcome every guard in this
// package is arranged to prevent.

// privateVocabularyRules are the three rules this row is about, named by
// the handles the manifest gives them.
//
// NAMED RATHER THAN COUNTED. A row asserting only that "the vocabulary
// fired" passes with two of the three deleted, which is the shape of
// every check that measures a total instead of a set.
var privateVocabularyRules = []string{
	"card-section-cited",
	"private-ruling-cited",
	"private-instructions-file",
}

// citationProofBlob is the published object these three rules were
// written against, and citationProofPath is the name it wore. The path is
// passed to the engine because a verdict depends on it — a manifest's
// data line is exempt from the provider vocabulary and a source file's is
// not — and this object is neither, so naming it honestly is the point.
const (
	citationProofBlob = "73d011b9517fb05b29373c50d56f075e2e7c83c8"
	citationProofPath = "internal/guard/readmecontract_test.go"
)

// publishedBlob reads one object out of this repository by its hash.
//
// It goes through the same environment scrubbing every other git command
// in this package uses, so that the checkout under test is the only thing
// deciding which repository answers.
func publishedBlob(t *testing.T, root, sha string) string {
	t.Helper()
	cmd := exec.Command("git", "cat-file", "blob", sha)
	cmd.Dir = root
	cmd.Env = gitSafeEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the object %s could not be read from this checkout: %v\n"+
			"This row proves the private-vocabulary rules against a revision that is "+
			"already published, rather than against a fixture — a fixture spelling one of "+
			"these out would be the citation shipping inside the file that forbids it. An "+
			"object that is no longer reachable means every ref that reached it is gone. "+
			"Re-point this row and the ledger entries beside it at an object that still "+
			"carries the shape, or retire the rules and both together.", sha, err)
	}
	return string(out)
}

// firedRuleIDs is the set of rule handles the engine reports on content.
func firedRuleIDs(t *testing.T, root, path, content string) map[string]bool {
	t.Helper()
	engine := realEngine(t, root)
	fired := map[string]bool{}
	for _, m := range engine.Check(path, content) {
		fired[m.PatternID] = true
	}
	return fired
}

// TestThePrivateVocabularyFiresOnThePublishedCitationsAndNotOnWhatReplacedThem
// proves both halves, in the order that makes each mean something.
//
// A rule that fires on nothing is indistinguishable from a rule that is
// switched off, so the first half is the one that has to run first: it
// establishes that these three can see anything at all. The second half
// is what stops the answer being "it sees everything" — the same file, at
// the revision that rewrote those comments, must come back clean.
//
// Between them they say the rules catch a CITATION and not the honest
// prose around it, which is the whole of what narrowing them was for.
func TestThePrivateVocabularyFiresOnThePublishedCitationsAndNotOnWhatReplacedThem(t *testing.T) {
	root := moduleRoot(t)

	// HALF ONE: the published object. Each rule must fire on it.
	published := publishedBlob(t, root, citationProofBlob)
	if strings.TrimSpace(published) == "" {
		t.Fatalf("the object %s read back empty, so nothing below is measuring anything",
			citationProofBlob)
	}
	firedThen := firedRuleIDs(t, root, citationProofPath, published)
	for _, id := range privateVocabularyRules {
		if !firedThen[id] {
			t.Errorf("the rule %s does not fire on the published revision it was written "+
				"for.\nA rule that matches nothing is indistinguishable from one that was "+
				"never added: it passes every scan, for ever, and the next occurrence of the "+
				"shape it describes ships unnoticed.", id)
		}
	}

	// HALF TWO: the same path as the tree carries it now. None may fire.
	current, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(citationProofPath)))
	if err != nil {
		t.Fatalf("reading %s: %v", citationProofPath, err)
	}
	firedNow := firedRuleIDs(t, root, citationProofPath, string(current))
	for _, id := range privateVocabularyRules {
		if firedNow[id] {
			t.Errorf("the rule %s fires on %s as the tree carries it today.\n"+
				"These three match ordinary words in a citing context, so the danger they "+
				"carry is the opposite of the one above: a rule that reds on honest prose is "+
				"a rule somebody switches off, and a switched-off rule catches nothing.",
				id, citationProofPath)
		}
	}
}

// TestEveryPrivateVocabularyRuleIsDeclaredInTheManifest ties the list
// above to the file it names, in both directions.
//
// Without it this file could go on naming a rule the manifest no longer
// declares — the row above would red, but with a message about a rule
// that fires on nothing, which points a reader at the object rather than
// at the deletion that actually happened.
func TestEveryPrivateVocabularyRuleIsDeclaredInTheManifest(t *testing.T) {
	root := moduleRoot(t)
	declared := map[string]bool{}
	for _, rule := range manifestRules(t, root, "scripts/citation-patterns.txt") {
		declared[rule.ID] = true
	}
	for _, id := range privateVocabularyRules {
		if !declared[id] {
			t.Errorf("this file proves the rule %s and scripts/citation-patterns.txt does "+
				"not declare it", id)
		}
	}
}
