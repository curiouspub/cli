package citations_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/curiouspub/cli/internal/citations"
)

// TestSplitSubwordsSplitsTheWayAnIdentifierIsBuilt is the first test this
// tokeniser has ever had. It moved here from a package that holds nothing
// but tests, where it was exercised only THROUGH the rule that uses it —
// so a change to the splitter could only be caught by a row about
// something else, and only if that rule happened to range over the
// spelling that broke.
func TestSplitSubwordsSplitsTheWayAnIdentifierIsBuilt(t *testing.T) {
	cases := []struct {
		name string
		run  string
		want []string
	}{
		{"a lowercase word is one subword", "handler", []string{"handler"}},
		{"camel case splits at every capital", "readTheHeader", []string{"read", "The", "Header"}},
		{"a leading capital is not a split", "Header", []string{"Header"}},

		// THE ACRONYM BOUNDARY, and it is the reason this is written by
		// hand rather than as a pattern: the natural expression needs a
		// negative lookahead, which Go's engine does not have.
		{"an acronym run keeps its letters together", "HTMLTemplate", []string{"HTML", "Template"}},
		{"a trailing acronym is one subword", "readHTML", []string{"read", "HTML"}},
		{"an acronym alone is one subword", "HTML", []string{"HTML"}},
		{"two capitals then lowercase split between them", "IOReader", []string{"IO", "Reader"}},

		// Digits stay attached to the letters they follow, so a name
		// ending in a digit survives as one subword rather than becoming
		// a word and a number that join back into something else.
		{"a digit stays with the word it follows", "point2D", []string{"point2", "D"}},
		{"a bare digit run is its own subword", "9lives", []string{"9", "lives"}},
		{"digits only", "42", []string{"42"}},

		{"an empty run yields nothing", "", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := citations.SplitSubwords(tc.run); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("SplitSubwords(%q) = %v, want %v", tc.run, got, tc.want)
			}
		})
	}
}

// TestIdentifierTokensSplitsAndJoins asserts BOTH halves of the
// tokeniser's contract, because a rule built on it fails differently when
// each half is missing: without splitting, every camelCase spelling of a
// forbidden name walks past; without joining, a two-part name the
// convention itself splits matches neither of its halves.
//
// Each row therefore asserts a presence AND an absence. A row that only
// listed what it wanted is satisfied by a tokeniser returning every
// substring, which would match ordinary English on every line.
func TestIdentifierTokensSplitsAndJoins(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		present []string
		absent  []string
	}{
		{
			name:    "a camel case identifier yields its parts",
			line:    "widgetStoreClient",
			present: []string{"widget", "store", "client"},
			absent:  []string{"idget", "toreclient"},
		},
		{
			name:    "adjacent parts are joined, so a split name still matches",
			line:    "widgetStoreClient",
			present: []string{"widgetstore", "storeclient", "widgetstoreclient"},
			absent:  []string{"widgetclient"},
		},
		{
			name: "a whole word is not its inner letters",
			line: "drawing",
			// THE PROPERTY THAT KEEPS THE RULE QUIET. An ordinary English
			// word containing a forbidden name's letters must not match:
			// substring matching reds on prose, and this is what
			// distinguishes an identifier that NAMES something from a
			// word that merely contains those letters.
			present: []string{"drawing"},
			absent:  []string{"draw", "raw", "wing"},
		},
		{
			name:    "runs are separated by punctuation",
			line:    "widget_store.client",
			present: []string{"widget", "store", "client"},
			absent:  []string{"widgetstore", "storeclient"},
		},
		{
			name:    "everything is lowered, so spelling does not decide",
			line:    "WIDGET",
			present: []string{"widget"},
			absent:  []string{"WIDGET"},
		},
		{
			name:    "a line with no alphanumeric run yields nothing",
			line:    "-- // ---",
			present: nil,
			absent:  []string{""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := citations.IdentifierTokens(tc.line)
			for _, want := range tc.present {
				if !got[want] {
					t.Errorf("IdentifierTokens(%q) does not contain %q; it has %v",
						tc.line, want, sortedKeys(got))
				}
			}
			for _, unwanted := range tc.absent {
				if got[unwanted] {
					t.Errorf("IdentifierTokens(%q) contains %q, which it must not: a token that is "+
						"not a whole subword or a contiguous join of subwords makes every rule built "+
						"on this red on ordinary English", tc.line, unwanted)
				}
			}
			if len(tc.present) == 0 && len(got) != 0 {
				t.Errorf("IdentifierTokens(%q) = %v, want no tokens at all", tc.line, sortedKeys(got))
			}
		})
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
