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
// rather than to a second, hand-typed copy of them. Each check requires
// the constant's own name, backticked, within a short distance of the
// human figure derived from its value — close enough to catch the number
// drifting from the constant, loose enough to survive a rewritten
// sentence around it.
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
	for _, c := range cases {
		pattern := regexp.MustCompile(
			"`" + regexp.QuoteMeta(c.constant) + "`[\\s\\S]{0,200}?" + regexp.QuoteMeta(c.human))
		reverse := regexp.MustCompile(
			regexp.QuoteMeta(c.human) + "[\\s\\S]{0,200}?`" + regexp.QuoteMeta(c.constant) + "`")
		if !pattern.MatchString(readme) && !reverse.MatchString(readme) {
			t.Errorf("README does not show %q within 200 characters of `%s` (in either order); "+
				"the README's limits section should read this figure off the constant", c.human, c.constant)
		}
	}
}
