package rulefile_test

import (
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/rulefile"
)

// TestParseKeepsTheRuleVerBatimAfterTheId is the row the whole format
// rests on: several citation patterns match PHRASES, so a parser that
// split a data line into fields would hand back the first word and drop
// the rest — a pattern that then matches nothing, with every check still
// green.
//
// MUTATION RUN: replacing the cut with strings.Fields reds here naming
// the truncated rule, and reds nowhere else in this package.
func TestParseKeepsTheRuleVerbatimAfterTheId(t *testing.T) {
	rules, err := rulefile.Parse("m.txt", "a-phrase   two words  and a tab\there\n")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("got %d rule(s), want 1", len(rules))
	}
	if rules[0].ID != "a-phrase" {
		t.Errorf("id is %q", rules[0].ID)
	}
	if want := "two words  and a tab\there"; rules[0].Text != want {
		t.Errorf("the rule came back as %q, want %q — internal whitespace is part of a "+
			"pattern and a parser that normalises it has changed what the rule matches",
			rules[0].Text, want)
	}
	if rules[0].Line != 1 {
		t.Errorf("line is %d, want 1", rules[0].Line)
	}
}

// TestParseIgnoresBlanksAndComments pins the handling every one of these
// manifests already had, because the id column must not change what
// counts as a rule. A comment promoted to a rule is a rule nobody wrote.
func TestParseIgnoresBlanksAndComments(t *testing.T) {
	rules, err := rulefile.Parse("m.txt", "# a comment with words\n\n   \nid-one  x\n\t# indented comment\nid-two  y\n")
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("got %d rule(s), want 2 — blanks and comments declare nothing", len(rules))
	}
	if rules[0].ID != "id-one" || rules[1].ID != "id-two" {
		t.Errorf("got %q and %q", rules[0].ID, rules[1].ID)
	}
	if rules[1].Line != 6 {
		t.Errorf("the second rule reports line %d, want 6 — a complaint that names the "+
			"wrong line sends its reader to edit somebody else's rule", rules[1].Line)
	}
}

// TestParseRefusesTheThreeWaysAnIdCanBeUseless covers what a validator is
// for. Each row is a real way to write one of these files wrong, and a
// parser that accepted any of them would produce a manifest whose records
// cannot be trusted to point at anything.
func TestParseRefusesTheThreeWaysAnIdCanBeUseless(t *testing.T) {
	for _, row := range []struct {
		name, text, wants string
	}{
		{"an id with no rule after it", "lonely\n", "no rule after it"},
		{"an id with only whitespace after it", "lonely   \t\n", "no rule after it"},
		{"an id that is not an id", "Not_An_Id  x\n", "is not an id"},
		{"an id used twice", "dup  x\ndup  y\n", "already used"},
	} {
		t.Run(row.name, func(t *testing.T) {
			_, err := rulefile.Parse("m.txt", row.text)
			if err == nil {
				t.Fatalf("parsing %q was accepted; a manifest that parses to rules nobody "+
					"can refer to is worse than one that fails to load, because it loads",
					row.text)
			}
			if !strings.Contains(err.Error(), row.wants) {
				t.Errorf("the complaint is %q and does not say %q", err, row.wants)
			}
		})
	}
}

// TestParseNamesTheFileAndTheLine is small and is here because a
// validator that refuses without saying where is a validator somebody
// fixes by guessing.
func TestParseNamesTheFileAndTheLine(t *testing.T) {
	_, err := rulefile.Parse("scripts/whatever.txt", "ok  x\nNOPE  y\n")
	if err == nil {
		t.Fatal("a bad id was accepted")
	}
	for _, want := range []string{"scripts/whatever.txt", ":2:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the complaint %q does not carry %q", err, want)
		}
	}
}
