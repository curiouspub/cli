package pack

import (
	"reflect"
	"testing"
)

// verdict is the matcher's answer for one path, asked of a stack holding
// a single ignore file at the project root — the shape almost every row
// below wants.
func verdict(body, rel string, isDir bool) bool {
	return ignoredBy([]*ignoreFile{parseIgnoreFile("", []byte(body))}, rel, isDir)
}

// TestIgnoreSyntax walks the syntax this matcher promises to support.
//
// PARTIAL SUPPORT IS THE FAILURE MODE THAT MATTERS. A pattern the
// matcher does not understand is a file its author deliberately excluded
// being uploaded and published, so every construct gets a positive row
// AND a negative one — a matcher that ignored everything would satisfy
// half of this table on its own.
//
// The table is checked against the real thing separately, in the
// differential row, because our reading of the documentation is exactly
// the thing that cannot be trusted here.
func TestIgnoreSyntax(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		path  string
		isDir bool
		want  bool
	}{
		{"a bare name matches at any depth", "*.log", "deep/nested/x.log", false, true},
		{"a star does not cross a separator", "*.log", "x.log", false, true},
		{"a star matches an empty run", "build*", "build", true, true},

		{"a later negation wins inside one file", "*.log\n!important.log", "important.log", false, false},
		{"an earlier negation loses to a later ignore", "!important.log\n*.log", "important.log", false, true},

		{"a leading slash anchors to the file's own directory", "/root-only.txt", "root-only.txt", false, true},
		{"an anchored pattern does not match deeper", "/root-only.txt", "sub/root-only.txt", false, false},
		{"an internal slash anchors too", "src/pages", "src/pages", true, true},
		{"an internal slash does not float", "src/pages", "app/src/pages", true, false},

		{"a trailing slash matches a directory", "build/", "build", true, true},
		{"a trailing slash does not match a file", "build/", "build", false, false},

		{"a leading double star floats", "**/temp", "a/b/temp", true, true},
		{"a middle double star spans directories", "docs/**/*.pdf", "docs/a/b/x.pdf", false, true},
		{"a middle double star also spans none", "docs/**/*.pdf", "docs/x.pdf", false, true},
		{"a trailing double star needs something inside", "star/**", "star", true, false},
		{"a trailing double star matches what is inside", "star/**", "star/a/f.txt", false, true},

		{"a question mark matches one character", "a?c.txt", "abc.txt", false, true},
		{"a question mark does not match none", "a?c.txt", "ac.txt", false, false},
		{"a question mark does not cross a separator", "a?c.txt", "a/c.txt", false, false},

		{"a character class matches a member", "[abc].txt", "b.txt", false, true},
		{"a character class refuses a non-member", "[abc].txt", "d.txt", false, false},
		{"a range matches", "[a-c].txt", "b.txt", false, true},
		{"a negated class refuses a member", "[!abc].txt", "b.txt", false, false},
		{"a negated class matches a non-member", "[!abc].txt", "d.txt", false, true},
		{"a caret negates too", "[^abc].txt", "d.txt", false, true},
		{"a named class matches", "[[:digit:]]-file.txt", "7-file.txt", false, true},
		{"a named class refuses", "[[:digit:]]-file.txt", "x-file.txt", false, false},

		{"a comment is not a pattern", "# comment", "comment", false, false},
		{"an escaped hash is a literal", `\#literal`, "#literal", false, true},
		{"an escaped bang is a literal, not a negation", "*.txt\n" + `\!bang.txt`, "!bang.txt", false, true},
		{"an escaped bang does not negate", "*.txt\n" + `\!bang.txt`, "other.txt", false, true},

		{"trailing spaces are dropped", "trailing   ", "trailing", false, true},
		{"trailing spaces are not part of the name", "trailing   ", "trailing   ", false, false},
		{"an escaped trailing space is kept", `space\ kept`, "space kept", false, true},
		{"an escaped trailing space is required", `space\ kept`, "space kept ", false, false},

		{"a blank line matches nothing", "\n\n", "anything.txt", false, false},
		{"a carriage return is not part of the name", "windows.txt\r\nother.txt\r\n", "windows.txt", false, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := verdict(tc.body, tc.path, tc.isDir); got != tc.want {
				t.Errorf("ignored(%q, %q, dir=%v) = %v, want %v",
					tc.body, tc.path, tc.isDir, got, tc.want)
			}
		})
	}
}

// TestIgnoreStackPrefersTheDeeperFile. Two files can both have an
// opinion about one path, and the deeper one wins — that is what makes a
// nested ignore file useful at all.
//
// MUTATION: scan the stack outermost-first. Both halves red.
func TestIgnoreStackPrefersTheDeeperFile(t *testing.T) {
	root := parseIgnoreFile("", []byte("*.log\n"))
	nested := parseIgnoreFile("keep", []byte("!*.log\n"))
	stack := []*ignoreFile{root, nested}

	if ignoredBy(stack, "keep/a.log", false) {
		t.Error("the nested negation lost to the root's ignore")
	}
	if !ignoredBy(stack, "other/a.log", false) {
		t.Error("the root's ignore stopped applying outside the nested file's directory")
	}
}

// TestIgnoreFileOnlyGovernsItsOwnSubtree. A pattern in a nested file is
// relative to that file's directory, so an anchored one must not escape
// upwards and a bare one must not reach sideways.
func TestIgnoreFileOnlyGovernsItsOwnSubtree(t *testing.T) {
	nested := []*ignoreFile{parseIgnoreFile("src", []byte("/local.txt\n*.tmp\n"))}

	if !ignoredBy(nested, "src/local.txt", false) {
		t.Error("an anchored pattern did not match inside its own directory")
	}
	if ignoredBy(nested, "local.txt", false) {
		t.Error("an anchored pattern in a nested file reached the project root")
	}
	if ignoredBy(nested, "other/x.tmp", false) {
		t.Error("a nested file's pattern reached a sibling directory")
	}
	if !ignoredBy(nested, "src/deep/x.tmp", false) {
		t.Error("a nested file's bare pattern did not reach below its own directory")
	}
}

// TestParseIgnoreFileDropsWhatIsNotAPattern. Blank lines and comments
// produce no pattern at all, rather than a pattern that never matches:
// the difference is invisible in a verdict and obvious in a report of
// which line matched, which is what the differential row compares.
func TestParseIgnoreFileDropsWhatIsNotAPattern(t *testing.T) {
	f := parseIgnoreFile("", []byte("# a comment\n\n   \n*.log\n"))
	if len(f.patterns) != 1 {
		t.Fatalf("patterns = %d, want one: %+v", len(f.patterns), f.patterns)
	}
	if f.patterns[0].line != 4 {
		t.Errorf("line = %d, want 4 — the line number is what a reader is pointed at",
			f.patterns[0].line)
	}
}

// TestIgnoreFileKeepsItsDirectory. The stack is scanned by depth, and a
// file that did not know where it lived could not tell which paths it
// governs.
func TestIgnoreFileKeepsItsDirectory(t *testing.T) {
	got := []string{
		parseIgnoreFile("", nil).dir,
		parseIgnoreFile("src/pages", nil).dir,
	}
	if !reflect.DeepEqual(got, []string{"", "src/pages"}) {
		t.Errorf("dirs = %v", got)
	}
}
