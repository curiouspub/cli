package pack

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/curiouspub/cli/pkg/wire"
)

// THE README, READ AS DATA. Two of its claims are generated from a table
// and four constants this package already holds, and these rows compare
// the README's own text against that data rather than against a second,
// hand-typed copy of it — the same reason forcedExcludeNames renders a
// message from forcedExcludes instead of a literal list.

// moduleRoot walks up from the test binary's working directory until it
// finds the module's go.mod. Duplicated from internal/guard's helper of
// the same name: it is fifteen lines, and importing a sibling package
// into every package that wants its own README row would be a stranger
// dependency than repeating them.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("walked up to the filesystem root without finding a go.mod")
		}
		dir = parent
	}
}

func readReadme(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	return string(data)
}

// excludeTableRow is one row of the README's exclusion table, in the shape
// the row below parses: a backticked name and a plain-text description of
// where it matches.
var excludeTableRowPattern = regexp.MustCompile("(?m)^\\| `([^`]+)` \\| ([^|]+) \\|\\s*$")

// readmeExcludeRows is every row the README's exclusion table carries, in
// the order they appear.
func readmeExcludeRows(readme string) [][2]string {
	var out [][2]string
	for _, m := range excludeTableRowPattern.FindAllStringSubmatch(readme, -1) {
		out = append(out, [2]string{m[1], strings.TrimSpace(m[2])})
	}
	return out
}

// excludeMatchText renders what a rule's SCOPE is, in the words the
// README's second column uses. It is derived from the rule's own fields
// rather than typed once per name, so a rule that changes shape changes
// this text without anybody editing a sentence to match — which is the
// property the mutation rows below exercise.
func excludeMatchText(r forcedRule) string {
	switch {
	case r.prefix && r.rootOnly:
		return "any name beginning with it, project root only"
	case r.prefix:
		return "any name beginning with it, any depth"
	case r.rootOnly:
		return "project root only"
	default:
		return "any depth"
	}
}

// TestReadmeExclusionTableMatchesForcedExcludes holds the README's
// exclusion table to forcedExcludes AS DATA, both directions: a rule with
// no row is missing, a row with no rule is an orphan, and a row that
// disagrees about scope is wrong regardless of which side changed.
func TestReadmeExclusionTableMatchesForcedExcludes(t *testing.T) {
	root := moduleRoot(t)
	readme := readReadme(t, root)
	rows := readmeExcludeRows(readme)
	if len(rows) == 0 {
		t.Fatal("README.md carries no exclusion-table row in the `| `name`  | matches |` shape, " +
			"so this row compared nothing")
	}

	want := make([][2]string, 0, len(forcedExcludes))
	for _, r := range forcedExcludes {
		want = append(want, [2]string{r.name, excludeMatchText(r)})
	}

	if len(rows) != len(want) {
		t.Fatalf("README's exclusion table carries %d row(s), forcedExcludes carries %d:\n"+
			"README: %v\ncode:   %v", len(rows), len(want), rows, want)
	}
	for i, w := range want {
		if rows[i] != w {
			t.Errorf("exclusion table row %d = %v, want %v (from forcedExcludes[%d])", i, rows[i], w, i)
		}
	}
}

// humanCount renders n with thousands separators, the way a person reads
// a file count rather than the way a computer prints one.
func humanCount(n int) string {
	s := strconv.Itoa(n)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// humanMB renders n bytes as whole megabytes, MB MEANING 1,000,000 BYTES —
// cli/CLAUDE.md's own stated convention, so the README and this row read
// the same number the same way. Every constant this row checks is an
// exact multiple of a million; a limit that stopped being one would need
// this test rewritten, not silently rounded.
func humanMB(n int) string {
	if n%1_000_000 != 0 {
		panic(fmt.Sprintf("%d is not a whole number of 1,000,000-byte megabytes", n))
	}
	return strconv.Itoa(n / 1_000_000)
}

// TestReadmeLimitsMatchWireConstants holds the four local limits the
// README states to the four wire.Max* constants they are read from,
// rather than to a second, hand-typed copy of them.
//
// EACH CONSTANT IS BOUND TO ITS OWN FIGURE ON ITS OWN LINE, and the
// figure is matched as a whole token. The looser shape this replaced —
// the constant's name within a couple of hundred characters of the figure
// — is satisfied by a neighbour in any list short enough to read, and by
// any longer number that happens to end in the same digits.
func TestReadmeLimitsMatchWireConstants(t *testing.T) {
	root := moduleRoot(t)
	readme := readReadme(t, root)

	cases := []struct {
		constant string
		human    string
	}{
		{"MaxSourceFiles", humanCount(wire.MaxSourceFiles) + " files"},
		{"MaxSourceFileBytes", humanMB(wire.MaxSourceFileBytes) + " MB"},
		{"MaxSourceTotalBytes", humanMB(wire.MaxSourceTotalBytes) + " MB"},
		{"MaxPackedBytes", humanMB(wire.MaxPackedBytes) + " MB"},
	}
	lines := strings.Split(readme, "\n")
	for _, c := range cases {
		bound := false
		for _, line := range lines {
			if limitBinding(line, c.constant, c.human) {
				bound = true
				break
			}
		}
		if !bound {
			t.Errorf("no line of the README names `%s` and its figure %q together.\n"+
				"The binding is per LINE and the figure is matched as a whole token, so a "+
				"neighbouring limit's number cannot stand in for this one and a longer number "+
				"cannot contain it.", c.constant, c.human)
		}
	}
}

// limitBinding reports whether ONE line names a constant and states its
// figure.
//
// THE BINDING IS PER LINE, and that is the correction. It was a window of
// two hundred characters in either direction, which in a bulleted list of
// four limits reaches comfortably into the neighbours — so a constant
// whose own figure was wrong stayed green on the strength of the line
// below it. A list where every entry can satisfy every other entry's
// assertion is not four checks; it is one check, repeated.
func limitBinding(line, constant, human string) bool {
	return strings.Contains(line, "`"+constant+"`") && anchoredFigure(line, human)
}

// anchoredFigure matches a figure as a WHOLE token.
//
// A SUBSTRING MATCH IS SATISFIED BY A LARGER NUMBER CONTAINING IT: five
// megabytes is inside fifty-five, thirty inside a hundred and thirty. The
// figure carries its unit, so what has to be excluded is a digit or a
// grouping separator immediately before it and a digit immediately after
// — which is what separates a number from a longer one that merely ends
// the same way.
func anchoredFigure(text, human string) bool {
	return regexp.MustCompile(
		`(?:^|[^0-9.,])` + regexp.QuoteMeta(human) + `(?:$|[^0-9])`).MatchString(text)
}

// TestTheLimitBindingIsAnchoredAndPerLine is the permanent fixture for
// the two ways the previous shape could be satisfied without being true.
func TestTheLimitBindingIsAnchoredAndPerLine(t *testing.T) {
	const real = "- `MaxSourceFileBytes` — 5 MB for any single file."
	if !limitBinding(real, "MaxSourceFileBytes", "5 MB") {
		t.Errorf("the real line does not bind its own constant to its own figure: %q", real)
	}
	// A LONGER NUMBER CONTAINING THE FIGURE.
	if limitBinding("- `MaxSourceFileBytes` — 55 MB for any single file.",
		"MaxSourceFileBytes", "5 MB") {
		t.Error("5 MB matched inside 55 MB, so a figure ten times the real one would pass")
	}
	if limitBinding("- `MaxPackedBytes` — 130 MB once packed.", "MaxPackedBytes", "30 MB") {
		t.Error("30 MB matched inside 130 MB")
	}
	// A WRONG FIGURE ON THE CONSTANT'S OWN LINE.
	if limitBinding("- `MaxPackedBytes` — 90 MB once packed.", "MaxPackedBytes", "30 MB") {
		t.Error("a line stating the wrong figure for its own constant was accepted")
	}
	// A NEIGHBOUR'S FIGURE CANNOT REACH ACROSS.
	if limitBinding("- `MaxPackedBytes` — the same as above.", "MaxPackedBytes", "30 MB") {
		t.Error("a constant with no figure of its own was bound to one from somewhere else")
	}
}
