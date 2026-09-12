package leakcheck_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/leakcheck"
	"github.com/curiouspub/cli/internal/rulefile"
)

// A TOY VOCABULARY, and it is toy on purpose. Every row here needs text
// that the rules catch, and this file is one of the files the real rules
// are read against — so a fixture spelling a real forbidden string would
// be the leak arriving through the package that hunts it. Nothing below
// matches anything this repository actually forbids, which also keeps
// these rows from moving when the real manifests do.
func toyRules(t *testing.T) leakcheck.Rules {
	t.Helper()
	rules, err := leakcheck.New(
		[]rulefile.Rule{
			{ID: "marker-id", Text: `\bZZQ-[0-9]+\b`},
			{ID: "marker-phrase", Text: `(?i:\bthe zzq file\b)`},
		},
		[]rulefile.Rule{{ID: "term-01", Text: "zzqcloud"}},
	)
	if err != nil {
		t.Fatalf("building the toy vocabulary: %v", err)
	}
	return rules
}

// ids renders what Check returned as "<id>:<line>" pairs, sorted, so a
// row can state the whole answer instead of picking through a slice.
func ids(matches []leakcheck.Match) []string {
	var out []string
	for _, m := range matches {
		out = append(out, m.PatternID+":"+itoa(m.Line))
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func same(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestCheckNumbersLinesFromOneAndReportsEveryMatch covers the two things
// a caller cannot recover if this gets them wrong: where to look, and how
// many there were.
//
// EVERY MATCH ON A LINE, not the first. A line naming several forbidden
// things would otherwise report one of them, and whoever is fixing them
// discovers the next only by running again.
func TestCheckNumbersLinesFromOneAndReportsEveryMatch(t *testing.T) {
	rules := toyRules(t)
	content := "a clean first line\nZZQ-1 and ZZQ-2 on one line\nnothing\nzzqcloudClient here\n"

	got := ids(rules.Check("internal/api/client.go", content))
	want := []string{"marker-id:2", "marker-id:2", "term-01:4"}
	if !same(got, want) {
		t.Errorf("Check returned %v, want %v", got, want)
	}
}

// TestAnEmptyPathIsASurfaceThatIsNotAFile is the row for the caller that
// is not reading files at all. A commit message quoting a manifest is
// still a published message, and every exemption here is a statement
// about a FILE's job in this repository.
func TestAnEmptyPathIsASurfaceThatIsNotAFile(t *testing.T) {
	rules := toyRules(t)
	const line = "zzqcloud\n"

	if got := ids(rules.Check("scripts/vendor-terms.txt", line)); len(got) != 0 {
		t.Errorf("a manifest's own data line was reported (%v); a rule cannot name what it "+
			"forbids without writing it down", got)
	}
	got := ids(rules.Check("", line))
	if !same(got, []string{"term-01:1"}) {
		t.Errorf("the same text on a surface that is not a file returned %v, want one "+
			"finding.\nA commit message is not a manifest however much it quotes one, and a "+
			"check that inherited the exemption would let the name through on the surface "+
			"nobody can edit afterwards", got)
	}
}

// TestARuleFilesExemptionIsPerLineAndPerCheck asserts the narrowness
// rather than the exemption: the waiver buys the provider vocabulary on
// data lines and nothing else, which is what keeps a private identifier
// written on one from shipping.
func TestARuleFilesExemptionIsPerLineAndPerCheck(t *testing.T) {
	rules := toyRules(t)

	for _, row := range []struct {
		name, path, content string
		want                []string
	}{
		{"a data line is exempt from the vocabulary", "scripts/vendor-terms.txt", "zzqcloud\n", nil},
		{"a data line is NOT exempt from the patterns", "scripts/vendor-terms.txt", "zzqcloud ZZQ-7\n",
			[]string{"marker-id:1"}},
		{"a comment line is read like any prose", "scripts/vendor-terms.txt", "# zzqcloud runs it\n",
			[]string{"term-01:1"}},
		{"a file that merely sits beside one is read", "scripts/cut-release.sh", "zzqcloud\n",
			[]string{"term-01:1"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := ids(rules.Check(row.path, row.content)); !same(got, row.want) {
				t.Errorf("Check(%q) returned %v, want %v", row.path, got, row.want)
			}
		})
	}
}

// TestAQuotedRuleGetsNoPathBasedWaiver is the regression for granting a
// writable test path authority to suppress an ordinary string literal.
// Historical quoted rules are recorded in the blob ledger; a path an
// outsider can write is not evidence that newly published text is safe.
func TestAQuotedRuleGetsNoPathBasedWaiver(t *testing.T) {
	rules := toyRules(t)
	const quoting = "internal/guard/guard_test.go"

	for _, row := range []struct {
		name, path, content string
		want                []string
	}{
		{"a quoted entry is read", quoting, "\t\"zzqcloud-sdk\",\n",
			[]string{"term-01:1"}},
		{"a sentence naming it is not", quoting, "// zzqcloud is where it runs\n",
			[]string{"term-01:1"}},
		{"a quoted entry carrying a citation still reds", quoting, "\t\"ZZQ-4\",\n",
			[]string{"marker-id:1"}},
		{"the shape is not the licence", "internal/api/client.go", "\t\"zzqcloud-sdk\",\n",
			[]string{"term-01:1"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := ids(rules.Check(row.path, row.content)); !same(got, row.want) {
				t.Errorf("Check(%q) over %q returned %v, want %v", row.path, row.content, got, row.want)
			}
		})
	}
}

// TestInfrastructureSeparatesTheTwoVocabularies exists because the one
// sentence of a report that differs between them is decided by it, and
// because an id is deliberately not required to spell which file it came
// from.
func TestInfrastructureSeparatesTheTwoVocabularies(t *testing.T) {
	rules := toyRules(t)
	if !rules.Infrastructure("term-01") {
		t.Error("a provider term's id is not reported as infrastructure, so a report would " +
			"describe it as a citation pattern and send its reader to the wrong manifest")
	}
	for _, id := range []string{"marker-id", "marker-phrase", "not-an-id-at-all"} {
		if rules.Infrastructure(id) {
			t.Errorf("%s is reported as infrastructure and is not", id)
		}
	}
}

// TestAVocabularyThatLooksForNothingSaysSo. A set of rules that lost its
// contents does not fail — it passes everything, quietly, which is the
// one outcome a check like this must never produce.
func TestAVocabularyThatLooksForNothingSaysSo(t *testing.T) {
	full := toyRules(t)
	if full.Empty() {
		t.Fatal("the toy vocabulary reports itself empty")
	}
	noTerms, err := leakcheck.New([]rulefile.Rule{{ID: "a", Text: "x"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	noPatterns, err := leakcheck.New(nil, []rulefile.Rule{{ID: "b", Text: "y"}})
	if err != nil {
		t.Fatal(err)
	}
	if !noTerms.Empty() || !noPatterns.Empty() {
		t.Error("half a vocabulary does not report itself empty, so a check could run with " +
			"one of its two rules silently switched off")
	}
}

// TestAPatternThatCannotCompileIsNamedByItsId. The complaint has to
// identify the line somebody edits, and the id is the handle that does
// that without writing the expression into the message.
func TestAPatternThatCannotCompileIsNamedByItsId(t *testing.T) {
	_, err := leakcheck.New([]rulefile.Rule{{ID: "broken-rule", Text: "("}}, nil)
	if err == nil {
		t.Fatal("a pattern that does not compile was accepted, so the rule it was meant to " +
			"be would be enforced nowhere")
	}
	if !strings.Contains(err.Error(), "broken-rule") {
		t.Errorf("the complaint %q does not name the rule", err)
	}
}

// TestAProviderRuleMustBeOneReachableToken closes a loader gap where an
// inline note or invisible BOM became part of the map key. The manifest
// parsed, but no content could ever produce that exact token.
func TestAProviderRuleMustBeOneReachableToken(t *testing.T) {
	patterns := []rulefile.Rule{{ID: "pattern", Text: "x"}}
	for _, row := range []struct {
		name, text string
	}{
		{"an inline note", "zzqcloud # note"},
		{"a BOM after the separator", "\ufeffzzqcloud"},
	} {
		t.Run(row.name, func(t *testing.T) {
			_, err := leakcheck.New(patterns, []rulefile.Rule{{ID: "term-01", Text: row.text}})
			if err == nil {
				t.Fatal("an unreachable provider map key was accepted")
			}
			if !strings.Contains(err.Error(), "term-01") || strings.Contains(err.Error(), row.text) {
				t.Errorf("the refusal should name the id without reproducing its text: %q", err)
			}
		})
	}
}

// TestTheGoSumExemptionIsAColumn. The module path beside a checksum is
// somebody's choice and go.sum keeps entries for modules no longer in the
// graph, so excusing the whole line hides exactly the half worth reading.
func TestTheGoSumExemptionIsAColumn(t *testing.T) {
	rules := toyRules(t)
	const hash = "h1:qqZzqcloudZz0000000000000000000000="

	if got := ids(rules.Check("go.sum", "example.com/mod v1.2.3 "+hash+"\n")); len(got) != 0 {
		t.Errorf("a checksum column was read (%v); a dependency bump nobody chose the bytes "+
			"of would red the build on a file no author can edit", got)
	}
	got := ids(rules.Check("go.sum", "example.com/zzqcloud/sdk v1.2.3 h1:0000000000000000000000000000000000000000000=\n"))
	if !same(got, []string{"term-01:1"}) {
		t.Errorf("the module path beside a checksum returned %v, want one finding — it is a "+
			"human choice, and a stale entry is invisible to the dependency graph check", got)
	}
}
