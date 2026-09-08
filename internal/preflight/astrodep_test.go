package preflight

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/check"
)

// TestAstroDepAcceptsAstroInDependencies is the acceptance half of every
// refusal row below it. A check that refused everything would satisfy
// each of those on its own, so a row proving a valid project is ACCEPTED
// is what makes them assertions about discrimination rather than about a
// check that always says no.
//
// REQUIRED MUTATION: remove "dependencies" from dependencySections in
// packagejson.go. This row reds; the devDependencies row beside it does
// not move, which is what makes the pair worth having.
func TestAstroDepAcceptsAstroInDependencies(t *testing.T) {
	res := CheckAstroDep(OSFileSystem{}, projectFixture(t, "valid"))

	if len(res.Findings) != 0 {
		t.Errorf("findings on a project declaring astro = %+v, want none", res.Findings)
	}
	if len(res.Declined) != 0 {
		t.Errorf("declined = %+v, want nothing — the check could look and did", res.Declined)
	}
}

// TestAstroDepAcceptsAstroInDevDependencies is the widening, and it has
// its own name because it is a DECISION rather than an implementation
// detail. The pre-flight table says "dependencies"; a large minority of
// real projects keep build tooling in devDependencies and build
// perfectly well, so refusing them would be a hard stop on a working
// project — the exact failure this whole set of checks is shaped around.
//
// REQUIRED MUTATION: remove "devDependencies" from dependencySections in
// packagejson.go. This row reds and the one above it does not.
func TestAstroDepAcceptsAstroInDevDependencies(t *testing.T) {
	res := CheckAstroDep(OSFileSystem{}, projectFixture(t, "astro-in-dev-dependencies"))

	if len(res.Findings) != 0 {
		t.Errorf("findings on a project declaring astro in devDependencies = %+v, "+
			"want none — this is the widening and it is deliberate", res.Findings)
	}
}

// TestAstroDepHardStopsWhenAstroIsAbsent asserts the SPECIFIC refusal
// rather than that some refusal happened. A row satisfied by "a finding
// appeared" is one exit from passing because the fixture was missing,
// the id was mistyped, or the check refused everything it was handed.
//
// REQUIRED MUTATION: return Result{} instead of the final hard stop in
// CheckAstroDep. Reds on the count.
func TestAstroDepHardStopsWhenAstroIsAbsent(t *testing.T) {
	res := CheckAstroDep(OSFileSystem{}, projectFixture(t, "astro-absent"))

	finding := soleFinding(t, res, check.IDAstroDep)
	if finding.Severity != check.SeverityHardStop {
		t.Errorf("severity = %q, want a hard stop — there is no half-deployable "+
			"state here", finding.Severity)
	}
	if !strings.Contains(finding.What, "Astro") {
		t.Errorf("What = %q, want it to name Astro", finding.What)
	}
	if !strings.Contains(finding.Next, "curious deploy ./my-site") {
		t.Errorf("Next = %q, want the hint that names a different folder", finding.Next)
	}
	if len(finding.Paths) != 1 || finding.Paths[0] != packageJSONName {
		t.Errorf("Paths = %v, want the one file the reader has to open", finding.Paths)
	}
}

// TestAstroDepHardStopsWithNoPackageJSON covers the reader standing in
// the wrong directory, which is the commonest way to meet this check.
//
// IT NAMES NO PATH, deliberately: the path list is what a reader opens,
// and there is nothing to open. The assertion is here so that a later
// change adding one has to argue for it.
//
// REQUIRED MUTATION: in readPackageJSON (packagejson.go), return
// packageJSONFound for the absent case. The check then reports that
// package.json does not list astro, and both assertions red.
func TestAstroDepHardStopsWithNoPackageJSON(t *testing.T) {
	res := CheckAstroDep(OSFileSystem{}, projectFixture(t, "no-package-json"))

	finding := soleFinding(t, res, check.IDAstroDep)
	if finding.Severity != check.SeverityHardStop {
		t.Errorf("severity = %q, want a hard stop", finding.Severity)
	}
	if !strings.Contains(finding.Why, "package.json") {
		t.Errorf("Why = %q, want it to name the missing file", finding.Why)
	}
	if !strings.Contains(finding.Next, "wrong folder") {
		t.Errorf("Next = %q, want it to ask whether this is the right folder",
			finding.Next)
	}
	if len(finding.Paths) != 0 {
		t.Errorf("Paths = %v, want none — there is no file here to open", finding.Paths)
	}
}

// developerJSONText is the vocabulary the decoder's own error string
// uses. None of it may reach a person: a Go type name and a byte offset
// are a report about this program, not a message about their project.
//
// The fragments are written lower-case and both sides of every
// comparison are folded, so a message that shouts one of them is caught
// exactly as a message that whispers it.
var developerJSONText = []string{
	"invalid character",
	"looking for beginning",
	"unmarshal",
	"json:",
	"syntaxerror",
	"offset",
}

// TestAstroDepNamesTheParsePositionAndNotTheGoError is the row the
// malformed case exists for: a position a person can find in an editor,
// and none of the decoder's own words.
//
// The absence half is paired with a control below it, because a scan for
// absence that has never been shown to find anything is the same thing
// as no scan at all.
//
// REQUIRED MUTATION: in decodeFault (packagejson.go), drop the position
// from the syntax-error branch — return packageJSONFile{fault:
// packageJSONInvalid}. Reds on the position.
//
// SECOND REQUIRED MUTATION, for the other half: put the decoder's error
// into the Why with %v. Reds on the developer-text scan.
func TestAstroDepNamesTheParsePositionAndNotTheGoError(t *testing.T) {
	res := CheckAstroDep(OSFileSystem{}, projectFixture(t, "malformed-package-json"))

	finding := soleFinding(t, res, check.IDAstroDep)
	if finding.Severity != check.SeverityHardStop {
		t.Errorf("severity = %q, want a hard stop", finding.Severity)
	}

	// The fixture's trailing comma leaves the decoder complaining about
	// the closing brace on line 6, column 3.
	if !strings.Contains(finding.Why, "line 6, column 3") {
		t.Errorf("Why = %q, want the position of the parse failure", finding.Why)
	}

	shipped := strings.ToLower(wholeText(finding))
	for _, fragment := range developerJSONText {
		if strings.Contains(shipped, fragment) {
			t.Errorf("the message carries the decoder's own words (%q):\n%s",
				fragment, wholeText(finding))
		}
	}

	// The control. Every fragment above is checked against text that
	// really does contain it, so the silence up there is evidence.
	sample := "json: cannot unmarshal array into Go value of type map[string]json.RawMessage; " +
		"invalid character '}' looking for beginning of object key string at offset 92 (SyntaxError)"
	for _, fragment := range developerJSONText {
		if !strings.Contains(strings.ToLower(sample), fragment) {
			t.Errorf("the scan does not match %q even in a real decoder error, so its "+
				"silence above proves nothing", fragment)
		}
	}
}

// TestAstroDepHardStopsOnAPackageJSONThatIsNotAnObject covers the two
// ways a file can be valid JSON and still not be a manifest. They are
// one row because they must produce one message: "this isn't valid JSON"
// would be false for both, and the fix — replace the contents — is the
// same.
//
// REQUIRED MUTATION: in decodeFault (packagejson.go), delete the
// UnmarshalTypeError branch. The array case falls through to "isn't
// valid JSON" and reds.
//
// SECOND REQUIRED MUTATION: in readPackageJSON, delete the `fields ==
// nil` guard. The null case is then reported as a manifest listing no
// dependencies, and its row reds.
func TestAstroDepHardStopsOnAPackageJSONThatIsNotAnObject(t *testing.T) {
	nullProject := t.TempDir()
	if err := os.WriteFile(filepath.Join(nullProject, packageJSONName), []byte("null\n"), 0o644); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	for _, tc := range []struct {
		name string
		root string
	}{
		{"an array", projectFixture(t, "array-package-json")},
		{"a bare null", nullProject},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finding := soleFinding(t, CheckAstroDep(OSFileSystem{}, tc.root), check.IDAstroDep)
			if finding.Severity != check.SeverityHardStop {
				t.Errorf("severity = %q, want a hard stop", finding.Severity)
			}
			if !strings.Contains(finding.What, "isn't a JSON object") {
				t.Errorf("What = %q, want it to say the file is not an object — "+
					"\"isn't valid JSON\" is false here and points at the wrong fix",
					finding.What)
			}
		})
	}
}

// TestAstroDepReportsAPackageJSONItCannotRead keeps "I looked and it is
// not there" apart from "I could not look". They lead to different
// sentences because they have different fixes: one asks whether this is
// the right folder, the other asks about permissions.
//
// REQUIRED MUTATION: in readPackageJSON (packagejson.go), map the
// undetermined case to packageJSONMissing. The wrong-folder copy appears
// and both assertions red.
func TestAstroDepReportsAPackageJSONItCannotRead(t *testing.T) {
	root := projectFixture(t, "valid")
	fsys := refusingFS{refuse: map[string]bool{
		filepath.Join(root, packageJSONName): true,
	}}

	finding := soleFinding(t, CheckAstroDep(fsys, root), check.IDAstroDep)
	if !strings.Contains(finding.What, "couldn't read") {
		t.Errorf("What = %q, want it to say the file could not be read", finding.What)
	}
	if strings.Contains(wholeText(finding), "wrong folder") {
		t.Errorf("an unreadable package.json produced the wrong-folder copy:\n%s",
			wholeText(finding))
	}

	// The control: the same fixture, nothing refused, is accepted. Without
	// it this row would also pass against a check that refused every
	// project it was ever handed.
	if res := CheckAstroDep(OSFileSystem{}, root); len(res.Findings) != 0 {
		t.Errorf("the same fixture read normally produced %+v, so the row above is "+
			"not about the refusal", res.Findings)
	}
}

// TestJSONPositionCountsLinesAndColumns exercises the instrument the
// malformed row depends on, including the offsets that name nowhere.
//
// The out-of-range rows are the control: they prove this function can
// answer "I don't know", so a row asserting a real position is asserting
// something the function had to compute rather than something it always
// says.
//
// REQUIRED MUTATION: in jsonPosition (packagejson.go), stop resetting
// column to 1 at a newline. Every row past the first line reds.
func TestJSONPositionCountsLinesAndColumns(t *testing.T) {
	data := []byte("{\n  \"a\": 1,\n}\n")

	for _, tc := range []struct {
		name   string
		offset int64
		want   string
	}{
		{"the first byte", 1, "line 1, column 1"},
		{"the newline ending line one", 2, "line 1, column 2"},
		{"the start of line two", 3, "line 2, column 1"},
		{"the closing brace on line three", 13, "line 3, column 1"},
		{"one past the data", int64(len(data)) + 1, "line 4, column 1"},
		{"an offset of zero names nowhere", 0, ""},
		{"a negative offset names nowhere", -1, ""},
		{"an offset past the data names nowhere", int64(len(data)) + 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonPosition(data, tc.offset); got != tc.want {
				t.Errorf("jsonPosition(offset %d) = %q, want %q", tc.offset, got, tc.want)
			}
		})
	}
}
