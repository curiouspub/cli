package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// events builds a test2json stream out of lines, so a row can state the
// shape it is feeding in rather than escaping JSON by hand.
func events(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func out(pkg, test, text string) string {
	return `{"Action":"output","Package":"` + pkg + `","Test":"` + test +
		`","Output":` + quote(text) + `}`
}

func pkgOut(pkg, text string) string {
	return `{"Action":"output","Package":"` + pkg + `","Output":` + quote(text) + `}`
}

func act(action, pkg, test string) string {
	if test == "" {
		return `{"Action":"` + action + `","Package":"` + pkg + `"}`
	}
	return `{"Action":"` + action + `","Package":"` + pkg + `","Test":"` + test + `"}`
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `"`, `\"`), "\n", `\n`) + `"`
}

// ---------------------------------------------------------------------
// Reading the stream
// ---------------------------------------------------------------------

// TestCollectFindsEverySkipWithItsReason. The reason is the whole point:
// a list of test names that stopped running tells a reader nothing they
// can act on, and the reason is the sentence the author wrote for
// exactly this moment.
func TestCollectFindsEverySkipWithItsReason(t *testing.T) {
	stream := events(
		act("run", "example.com/a", "TestOne"),
		out("example.com/a", "TestOne", "=== RUN   TestOne\n"),
		out("example.com/a", "TestOne", "    a_test.go:9: no pipes on this platform\n"),
		out("example.com/a", "TestOne", "--- SKIP: TestOne (0.00s)\n"),
		act("skip", "example.com/a", "TestOne"),
		act("run", "example.com/a", "TestTwo"),
		out("example.com/a", "TestTwo", "--- PASS: TestTwo (0.00s)\n"),
		act("pass", "example.com/a", "TestTwo"),
		pkgOut("example.com/a", "ok  \texample.com/a\t0.1s\n"),
		act("pass", "example.com/a", ""),
	)

	var buf bytes.Buffer
	got, seen, err := collect(strings.NewReader(stream), &buf)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if seen == 0 {
		t.Error("seen = 0, and two tests were in the stream")
	}
	want := []skip{{
		Package: "example.com/a",
		Test:    "TestOne",
		Reason:  "a_test.go:9: no pipes on this platform",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("skips = %#v, want %#v", got, want)
	}
}

// TestCollectPrintsWhatANonVerboseRunWOULDHavePrinted, plus the skips.
//
// A passing test's chatter is dropped and a failing test's is kept,
// which is what makes the log readable enough that anybody keeps
// running it. A tool that made every run verbose to surface three lines
// would be turned off inside a week.
func TestCollectPrintsWhatANonVerboseRunWOULDHavePrinted(t *testing.T) {
	stream := events(
		act("run", "example.com/a", "TestQuiet"),
		out("example.com/a", "TestQuiet", "=== RUN   TestQuiet\n"),
		out("example.com/a", "TestQuiet", "    nobody needs to read this\n"),
		act("pass", "example.com/a", "TestQuiet"),
		act("run", "example.com/a", "TestLoud"),
		out("example.com/a", "TestLoud", "    a_test.go:4: everything is on fire\n"),
		act("fail", "example.com/a", "TestLoud"),
		pkgOut("example.com/a", "FAIL\texample.com/a\t0.1s\n"),
		act("fail", "example.com/a", ""),
	)

	var buf bytes.Buffer
	if _, _, err := collect(strings.NewReader(stream), &buf); err != nil {
		t.Fatalf("collect: %v", err)
	}

	printed := buf.String()
	if strings.Contains(printed, "nobody needs to read this") {
		t.Error("a passing test's output was printed")
	}
	if !strings.Contains(printed, "everything is on fire") {
		t.Error("a failing test's output was dropped")
	}
	if !strings.Contains(printed, "FAIL\texample.com/a") {
		t.Error("the package summary line was dropped")
	}
}

// TestCollectDropsTheVerboseModeMarkers. The stream is produced in
// verbose mode whether anybody asked or not, so every package emits a
// bare PASS or FAIL line above its summary. An ordinary run prints one
// line per package and this wrapper has to as well — a reader who
// notices it printing something the real command does not stops
// believing it is showing them the real command.
//
// MUTATION: pass those markers through. Reds here.
func TestCollectDropsTheVerboseModeMarkers(t *testing.T) {
	stream := events(
		pkgOut("example.com/a", "PASS\n"),
		pkgOut("example.com/a", "ok  \texample.com/a\t0.1s\n"),
		act("pass", "example.com/a", ""),
		act("run", "example.com/a", "TestOne"),
		out("example.com/a", "TestOne", "    a_test.go:3: PASS is a fine thing to say\n"),
		act("fail", "example.com/a", "TestOne"),
	)

	var buf bytes.Buffer
	if _, _, err := collect(strings.NewReader(stream), &buf); err != nil {
		t.Fatalf("collect: %v", err)
	}
	printed := buf.String()
	for _, line := range strings.Split(printed, "\n") {
		if strings.TrimSpace(line) == "PASS" {
			t.Errorf("a bare verbose-mode marker was printed:\n%s", printed)
		}
	}
	if !strings.Contains(printed, "ok  \texample.com/a") {
		t.Error("the package summary line went with it")
	}
	if !strings.Contains(printed, "PASS is a fine thing to say") {
		t.Error("a failing test's own output was dropped because it mentioned the marker")
	}
}

// TestCollectDoesNotCountAPackageWithNoTestsAsASkippedROW. The stream
// spells both with the same word, and treating them alike would fail
// every run over a package that has no tests yet — this repository has
// one on purpose.
func TestCollectDoesNotCountAPackageWithNoTestsAsASkippedRow(t *testing.T) {
	stream := events(
		pkgOut("example.com/empty", "?   \texample.com/empty\t[no test files]\n"),
		act("skip", "example.com/empty", ""),
	)

	var buf bytes.Buffer
	got, _, err := collect(strings.NewReader(stream), &buf)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("skips = %#v, want none — a package with no tests is not a skipped row", got)
	}
	if !strings.Contains(buf.String(), "[no test files]") {
		t.Error("the package line was dropped")
	}
}

// TestCollectRefusesAStreamItLearnedNothingFrom. A tool that reports
// "no undeclared skips" after reading zero events has told you nothing
// and looks exactly like a clean run.
func TestCollectRefusesAStreamItLearnedNothingFrom(t *testing.T) {
	var buf bytes.Buffer
	_, seen, err := collect(strings.NewReader(""), &buf)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if seen != 0 {
		t.Errorf("seen = %d over an empty stream, want 0", seen)
	}
}

// ---------------------------------------------------------------------
// The manifest
// ---------------------------------------------------------------------

func TestParseManifestReadsRulesAndIgnoresProse(t *testing.T) {
	got, err := parseManifest(strings.NewReader(`
# a comment, which is not a rule

windows       example.com/a   TestNoPipesHere
linux,darwin  example.com/b   TestNeedsRoot
*             example.com/c   TestEverywhere
`))
	if err != nil {
		t.Fatalf("parseManifest: %v", err)
	}
	want := []rule{
		{Platforms: []string{"windows"}, Package: "example.com/a", Test: "TestNoPipesHere"},
		{Platforms: []string{"linux", "darwin"}, Package: "example.com/b", Test: "TestNeedsRoot"},
		{Platforms: []string{"*"}, Package: "example.com/c", Test: "TestEverywhere"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rules = %#v, want %#v", got, want)
	}
}

// TestParseManifestRefusesALineItCannotRead. A malformed line that was
// silently dropped would read as a declaration and behave as an
// absence — the skip it was meant to cover would fail the run, and the
// line sitting right there in the file would say it should not.
func TestParseManifestRefusesALineItCannotRead(t *testing.T) {
	if _, err := parseManifest(strings.NewReader("windows example.com/a\n")); err == nil {
		t.Error("a two-field line was accepted")
	}
	if _, err := parseManifest(strings.NewReader("amiga example.com/a TestX\n")); err == nil {
		t.Error("a platform nothing builds for was accepted")
	}
}

// TestParseManifestRefusesAnEmptyFile, because a manifest that declares
// nothing and a manifest nobody could read look identical from here, and
// the first is a state this repository is not in.
func TestParseManifestRefusesAnEmptyFile(t *testing.T) {
	if _, err := parseManifest(strings.NewReader("# only a comment\n")); err == nil {
		t.Error("a manifest with no rules was accepted")
	}
}

// TestDeclaredMatchesOnThePlatformItIsRunningOn. The platform column is
// the reason the manifest is worth having: a skip that is legitimate on
// one leg and a bug on another is exactly the case a single global list
// cannot express.
//
// MUTATION: ignore the platform column and match on package and test
// alone. The wrong-platform row reds.
func TestDeclaredMatchesOnThePlatformItIsRunningOn(t *testing.T) {
	rules := []rule{
		{Platforms: []string{"windows"}, Package: "example.com/a", Test: "TestNoPipesHere"},
		{Platforms: []string{"*"}, Package: "example.com/c", Test: "TestEverywhere"},
	}
	pipes := skip{Package: "example.com/a", Test: "TestNoPipesHere"}
	always := skip{Package: "example.com/c", Test: "TestEverywhere"}
	other := skip{Package: "example.com/a", Test: "TestSomethingElse"}

	if !declared(rules, "windows", pipes) {
		t.Error("a skip declared for this platform was reported as undeclared")
	}
	if declared(rules, "linux", pipes) {
		t.Error("a skip declared only for another platform was accepted here")
	}
	if !declared(rules, "linux", always) || !declared(rules, "windows", always) {
		t.Error("a rule for every platform did not match one of them")
	}
	if declared(rules, "windows", other) {
		t.Error("a skip nobody declared was accepted")
	}
}

// TestManifestLineShowsWhatToAdd. The tool's whole job at the moment of
// failure is to tell somebody what to do about it, and the answer is a
// line in a file — so it prints that line rather than describing it.
func TestManifestLineShowsWhatToAdd(t *testing.T) {
	got := manifestLine("windows", skip{Package: "example.com/a", Test: "TestX"})
	for _, want := range []string{"windows", "example.com/a", "TestX"} {
		if !strings.Contains(got, want) {
			t.Errorf("manifestLine = %q, want it to contain %q", got, want)
		}
	}
	rules, err := parseManifest(strings.NewReader(got + "\n"))
	if err != nil {
		t.Fatalf("the line it printed does not parse: %v", err)
	}
	if len(rules) != 1 || rules[0].Test != "TestX" {
		t.Errorf("the line it printed parses to %#v", rules)
	}
}

// TestReportIsLoudAboutAnUndeclaredSkipAndQuietAboutADeclaredOne, and
// the asymmetry is the ruling: a declared skip is information and an
// undeclared one is a row that stopped running with nobody's agreement.
//
// A DECLARED SKIP THAT DID NOT HAPPEN IS NOT A FAILURE, deliberately.
// Every one of these is conditional on the machine — an account that CAN
// make symbolic links does not skip the rows that need them — so failing
// on absence would make the manifest a prediction of the runner rather
// than a record of what is allowed.
func TestReportIsLoudAboutAnUndeclaredSkipAndQuietAboutADeclaredOne(t *testing.T) {
	rules := []rule{{Platforms: []string{"*"}, Package: "example.com/a", Test: "TestFine"}}

	var quiet bytes.Buffer
	if bad := report(&quiet, rules, "linux", []skip{
		{Package: "example.com/a", Test: "TestFine", Reason: "declared"},
	}); bad != 0 {
		t.Errorf("undeclared = %d over a declared skip, want 0", bad)
	}
	if !strings.Contains(quiet.String(), "TestFine") {
		t.Error("a declared skip was not printed at all; the reason nobody reads is the " +
			"reason nobody wrote down")
	}

	var loud bytes.Buffer
	if bad := report(&loud, rules, "linux", []skip{
		{Package: "example.com/a", Test: "TestSurprise", Reason: "nobody agreed to this"},
	}); bad != 1 {
		t.Errorf("undeclared = %d over an undeclared skip, want 1", bad)
	}
	if !strings.Contains(loud.String(), "TestSurprise") {
		t.Error("the undeclared skip was not named")
	}
	if !strings.Contains(loud.String(), "example.com/a") {
		t.Error("the report does not carry the line to add")
	}
}

// ---------------------------------------------------------------------
// The whole tool, over a real run
// ---------------------------------------------------------------------

// TestRunRefusesARunThatReachedNoTests.
//
// A tool that read zero outcomes reports the same empty skip list as one
// that read a clean suite, and "no undeclared skips" is then a sentence
// about nothing. This is the branch that tells those apart, and it has
// to be exercised against a real invocation because that is the only
// place the count comes from.
//
// The paired half matters as much: a run that DID reach a test succeeds.
// A guard that failed on both would be a guard nobody could satisfy.
//
// MUTATION: drop the outcome count and trust the exit code. The first
// half reds.
func TestRunRefusesARunThatReachedNoTests(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "expected-skips.txt")
	if err := os.WriteFile(manifest,
		[]byte("* example.com/nothing TestNothing\n"), 0o644); err != nil {
		t.Fatalf("writing the manifest fixture: %v", err)
	}

	const self = "github.com/curiouspub/cli/internal/skipcheck"

	var nothing bytes.Buffer
	err := run(manifest, []string{"-count=1", "-run", "NoTestIsCalledThis", self},
		&nothing, &nothing)
	if err == nil {
		t.Errorf("a run that reached no tests was accepted:\n%s", nothing.String())
	}

	var something bytes.Buffer
	if err := run(manifest, []string{"-count=1", "-run", "TestParseManifestRefusesAnEmptyFile", self},
		&something, &something); err != nil {
		t.Errorf("a run that did reach a test was refused: %v\n%s", err, something.String())
	}
}

// TestRunSaysWhichFileItCouldNotRead. The manifest is the whole basis
// for the verdict, so a missing one is not a reason to allow everything
// — it is a reason to stop, naming the path, because that is the one
// thing the reader has to fix.
func TestRunSaysWhichFileItCouldNotRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-here.txt")

	var buf bytes.Buffer
	err := run(missing, []string{"-count=1", "./..."}, &buf, &buf)
	if err == nil {
		t.Fatal("a missing manifest was treated as one allowing nothing, and the run passed")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error = %q, want it to name the file it could not read", err)
	}
}
