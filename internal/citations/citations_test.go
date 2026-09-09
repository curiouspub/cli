package citations_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

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

// ---------------------------------------------------------------------
// The join is bounded, and the bound is what keeps a stranger's text
// from holding the machine reading it.
// ---------------------------------------------------------------------

// TestEveryTokenFitsTheBound asserts the length rule in both directions.
//
// The absence half on its own would be satisfied by a tokeniser that
// returned nothing, so the presence half names the exact boundary: a
// join of the full bound is emitted and the next one along is not. A
// bound tested only from above is a bound nobody can tell from a
// tokeniser that has quietly stopped joining at all.
func TestEveryTokenFitsTheBound(t *testing.T) {
	// Eight-byte subwords, so the joins land exactly on multiples of
	// eight and the bound falls between two of them rather than inside
	// one. Four of them are the bound; five are past it.
	const part = "Abcdefgh"
	line := strings.Repeat(part, 5)

	got := citations.IdentifierTokens(line)

	for n := 1; n*len(part) <= citations.MaxTokenLength; n++ {
		want := strings.ToLower(strings.Repeat(part, n))
		if !got[want] {
			t.Errorf("a join of %d bytes is missing, and the bound is %d: %v",
				len(want), citations.MaxTokenLength, sortedKeys(got))
		}
	}
	tooLong := strings.ToLower(strings.Repeat(part, 5))
	if got[tooLong] {
		t.Errorf("a join of %d bytes was emitted past a bound of %d",
			len(tooLong), citations.MaxTokenLength)
	}
	for token := range got {
		if len(token) > citations.MaxTokenLength {
			t.Errorf("the tokeniser emitted a %d-byte token past a bound of %d",
				len(token), citations.MaxTokenLength)
		}
	}
}

// TestTheCostOfTokenisingIsBoundedByTheBound measures what the bound is
// actually for.
//
// A ROW ASSERTING "IT FINISHES" PASSES AT ANY SPEED, which is why this
// one measures two sizes and asks what the second cost relative to the
// first. Unbounded, the joins are every contiguous run of adjacent
// subwords and the work grows as the cube of the input: on the machine
// this was written on, a four-fold step from 4 KiB to 16 KiB took the
// cost from 339 ms to 19 s — a ratio of 56 — and one 64 KiB line, which
// is a size a pull request's body may legitimately be, extrapolates to
// about twenty minutes of processor time. Bounded, the same step measures
// between 4.0 and 4.2 across ten trials, and 64 KiB takes 25 ms.
//
// THE CEILING IS SIZED OFF BOTH NUMBERS rather than off one of them.
// Sixteen sits a factor of 3.8 above the worst ratio measured with the
// bound in place and a factor of 3.5 below the ratio measured without it,
// so an ordinary bad minute on a shared runner cannot reach it and the
// defect cannot hide under it. Sizing a window against only the value it
// must not exceed leaves it unknown whether the row can still fail.
//
// The two sizes are measured INTERLEAVED and each is the best of several
// runs: a slow window on a shared machine then lands on both, where it
// cancels, rather than on the larger alone, where it would read as
// growth.
func TestTheCostOfTokenisingIsBoundedByTheBound(t *testing.T) {
	const (
		small   = 4 << 10
		large   = 16 << 10
		rounds  = 7
		ceiling = 16.0
	)
	// A line of alternating case, which is the worst input this
	// tokeniser has: every two bytes are a subword, so the number of
	// joins is as large as the length allows.
	smallLine := strings.Repeat("AbCd", small/4)
	largeLine := strings.Repeat("AbCd", large/4)

	// Warm up both, so the first measurement of either is not paying for
	// a cold allocator. Without this the SMALL side reads high and the
	// ratio reads low, which is the direction that hides a failure.
	citations.IdentifierTokens(smallLine)
	citations.IdentifierTokens(largeLine)

	smallCost, largeCost := time.Hour, time.Hour
	for i := 0; i < rounds; i++ {
		start := time.Now()
		citations.IdentifierTokens(smallLine)
		if d := time.Since(start); d < smallCost {
			smallCost = d
		}
		start = time.Now()
		citations.IdentifierTokens(largeLine)
		if d := time.Since(start); d < largeCost {
			largeCost = d
		}
	}

	if smallCost <= 0 {
		t.Fatalf("the smaller input measured %v, so the ratio below divides by noise", smallCost)
	}
	if ratio := float64(largeCost) / float64(smallCost); ratio > ceiling {
		t.Errorf("a four-fold input took %.1f times as long (%v against %v), and the ceiling "+
			"is %.0f.\nWithout a bound on the join this grows as the cube of the input, and the "+
			"input is a stranger's pull request. Nothing runs out of memory — the map "+
			"deduplicates — it simply holds the machine for as long as whoever wrote the text "+
			"likes.", ratio, largeCost, smallCost, ceiling)
	}
}
