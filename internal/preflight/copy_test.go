package preflight

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/check"
)

// hardStopCase is one condition this package refuses a project for, and
// the words it refuses with.
type hardStopCase struct {
	name    string
	finding check.Finding
}

// everyHardStop drives both refusing checks over every condition each of
// them can stop a deploy for, and returns the findings.
//
// IT IS ENUMERATED RATHER THAN SAMPLED. "Every hard stop names an
// action" is one fact with several parts, and checking one representative
// is not sampling the set — it is checking the part somebody happened to
// think of. The list is written out so that a NEW refusal added without
// a row here is a refusal nothing in this file has ever looked at, which
// is at least a visible omission rather than an invisible one.
func everyHardStop(t *testing.T) []hardStopCase {
	t.Helper()

	unreadableRoot := projectFixture(t, "valid")
	unreadable := refusingFS{refuse: map[string]bool{
		filepath.Join(unreadableRoot, packageJSONName): true,
	}}

	cases := []struct {
		name string
		res  Result
	}{
		{"astro is not declared", CheckAstroDep(OSFileSystem{}, projectFixture(t, "astro-absent"))},
		{"there is no package.json", CheckAstroDep(OSFileSystem{}, projectFixture(t, "no-package-json"))},
		{"package.json will not parse", CheckAstroDep(OSFileSystem{}, projectFixture(t, "malformed-package-json"))},
		{"package.json is not an object", CheckAstroDep(OSFileSystem{}, projectFixture(t, "array-package-json"))},
		{"package.json cannot be read", CheckAstroDep(unreadable, unreadableRoot)},
		{"the lockfile is one we cannot install from", CheckLockfile(OSFileSystem{}, projectFixture(t, "lockfile-yarn-only"))},
		{"there is no lockfile", CheckLockfile(OSFileSystem{}, standaloneProject(t, map[string]string{}))},
		{"this is a package inside a workspace", CheckLockfile(OSFileSystem{},
			filepath.Join(projectFixture(t, "workspace-pnpm"), "apps", "site"))},
	}

	var out []hardStopCase
	for _, c := range cases {
		var found bool
		for _, f := range c.res.Findings {
			if f.Severity != check.SeverityHardStop {
				continue
			}
			found = true
			out = append(out, hardStopCase{name: c.name, finding: f})
		}
		if !found {
			t.Errorf("%q produced no hard stop, so this file has nothing to check "+
				"about it: %+v", c.name, c.res)
		}
	}
	return out
}

// TestEveryHardStopNamesAnAction holds the rule that makes the
// three-part shape worth having. A hard stop that names no action leaves
// the reader to guess, and the reader is usually somebody deploying
// their first site who has no model of what this program does. "No
// lockfile found" is a fact; "run an install, commit the lockfile, try
// again" is a fix.
//
// REQUIRED MUTATION: blank the Next of any one copy in astrodep.go or
// lockfile.go. Reds naming that condition.
func TestEveryHardStopNamesAnAction(t *testing.T) {
	for _, tc := range everyHardStop(t) {
		t.Run(tc.name, func(t *testing.T) {
			if strings.TrimSpace(tc.finding.Message) == "" {
				t.Error("no Message — the machine-readable result has nothing to list")
			}
			if strings.TrimSpace(tc.finding.What) == "" {
				t.Error("no What — the reader is not told what happened")
			}
			if strings.TrimSpace(tc.finding.Why) == "" {
				t.Error("no Why — the reader is not told why")
			}
			if strings.TrimSpace(tc.finding.Next) == "" {
				t.Error("no Next — it stops the run without naming an action, which " +
					"is the one part of this shape that is the product")
			}
		})
	}
}

// developerText matches the shapes this package's copy must never
// contain: a Go type or package-qualified name, a source position, a
// pointer address, or a goroutine dump.
//
// IT IS A DELIBERATE SECOND COPY OF A RULE THE TERMINAL PACKAGE ALSO
// HOLDS, and the duplication is the point rather than an oversight. That
// package's version guards the copy that package authors; this copy
// moved here, to the checks that own its conditions, and a guard that
// stayed behind would have been a rule about an empty set. The two are
// independent because their subjects are.
var developerText = regexp.MustCompile(
	`\*?\b[a-z][a-z0-9]*\.[A-Z][A-Za-z0-9]*|\.go:[0-9]+|0x[0-9a-f]{4,}|goroutine [0-9]+`)

// TestHardStopCopyIsWrittenForAPerson keeps a hard stop reading as a
// message rather than as a diagnostic: no Go type names, no source
// positions, and no sentence beginning with a report about what this
// program tried and could not do.
//
// The instrument is checked in the same function, immediately below the
// assertion it serves. A scan for absence that has never been shown to
// find anything is indistinguishable from a scan that matches nothing at
// all, and the second one passes forever.
//
// REQUIRED MUTATION: put the decoder's error into the invalid-JSON copy
// with %v — `fmt.Sprintf("%s (%v)", base, err)` in invalidJSONWhy, with
// the error threaded through. Reds on the type name in it.
func TestHardStopCopyIsWrittenForAPerson(t *testing.T) {
	for _, tc := range everyHardStop(t) {
		t.Run(tc.name, func(t *testing.T) {
			shipped := wholeText(tc.finding)
			if m := developerText.FindString(shipped); m != "" {
				t.Errorf("the copy contains developer text (%q):\n%s", m, shipped)
			}
			for _, line := range strings.Split(shipped, "\n") {
				lower := strings.ToLower(strings.TrimSpace(line))
				if strings.HasPrefix(lower, "failed to") || strings.HasPrefix(lower, "error:") {
					t.Errorf("a line reads as a report about the program rather than a "+
						"message to the person reading it: %q", line)
				}
			}
		})
	}

	// The controls. Both scans are run against fabricated text that does
	// contain what they look for, so their silence above is evidence.
	sample := "json.SyntaxError at parse.go:41 (0xc000102030), goroutine 17"
	if developerText.FindString(sample) == "" {
		t.Errorf("the developer-text scan matches nothing even in %q, so its silence "+
			"above proves nothing", sample)
	}
	for _, line := range []string{"failed to open the file", "Error: something"} {
		lower := strings.ToLower(strings.TrimSpace(line))
		if !strings.HasPrefix(lower, "failed to") && !strings.HasPrefix(lower, "error:") {
			t.Errorf("the prefix scan does not match %q", line)
		}
	}
}
