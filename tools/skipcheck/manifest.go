package main

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"strings"
)

// rule is one declared skip: which platforms it is legitimate on, and
// which row it covers.
type rule struct {
	Platforms []string
	Package   string
	Test      string
}

// knownPlatforms is the set the platform column may name, plus the
// wildcard.
//
// IT IS CHECKED RATHER THAN ACCEPTED, because a typo in this column is
// invisible in exactly the way that matters: the rule silently matches
// nothing, the skip it was written for fails the run, and the line
// sitting in the file says it should not have. A misspelling that
// declared MORE than intended would be worse still.
var knownPlatforms = []string{"linux", "darwin", "windows", "*"}

// parseManifest reads the declared-skip rules.
//
// The file is read on every run and no copy is kept anywhere, which is
// what makes deleting a line from it measurably change what the tool
// allows — the same property the other rule files in this repository
// have, and the reason anyone can check they are really being read.
func parseManifest(r io.Reader) ([]rule, error) {
	var rules []rule
	scanner := bufio.NewScanner(r)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("line %d: %q has %d fields, want three: "+
				"platforms, package, test", n, line, len(fields))
		}

		platforms := strings.Split(fields[0], ",")
		for _, p := range platforms {
			if !slices.Contains(knownPlatforms, p) {
				return nil, fmt.Errorf("line %d: %q is not a platform this repository "+
					"builds for; use one of %s", n, p, strings.Join(knownPlatforms, ", "))
			}
		}
		rules = append(rules, rule{
			Platforms: platforms,
			Package:   fields[1],
			Test:      fields[2],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("the manifest declares no skips at all, which is not a " +
			"state this repository is in — a file that could not be read looks exactly " +
			"like one that allows nothing")
	}
	return rules, nil
}

// declared reports whether this skip is one somebody wrote down for this
// platform.
func declared(rules []rule, goos string, s skip) bool {
	for _, r := range rules {
		if r.Package != s.Package || r.Test != s.Test {
			continue
		}
		if slices.Contains(r.Platforms, "*") || slices.Contains(r.Platforms, goos) {
			return true
		}
	}
	return false
}

// manifestLine renders the line that would declare this skip.
//
// It is printed rather than described because the answer to "what do I
// do about this" is a line in a file, and a reader who has to compose
// one from a prose description will get the column order wrong at least
// once.
func manifestLine(goos string, s skip) string {
	return fmt.Sprintf("%s %s %s", goos, s.Package, s.Test)
}

// report prints every skip and returns how many nobody declared.
//
// A DECLARED SKIP IS STILL PRINTED. The reason its author wrote is the
// whole product here, and a reason nobody ever reads is indistinguishable
// from a reason nobody wrote. Printing only the surprises would make the
// legitimate ones invisible again, which is the state this tool exists
// to leave.
//
// A DECLARED SKIP THAT DID NOT HAPPEN IS NOT A FAILURE, deliberately.
// Every one of these is conditional on the machine — an account that CAN
// create symbolic links does not skip the rows needing them — so failing
// on absence would turn the manifest into a prediction of the runner
// rather than a record of what is allowed.
func report(w io.Writer, rules []rule, goos string, skips []skip) int {
	if len(skips) == 0 {
		fmt.Fprintf(w, "\nNo rows were skipped on %s.\n", goos)
		return 0
	}

	fmt.Fprintf(w, "\n%d row(s) skipped on %s:\n", len(skips), goos)
	undeclared := 0
	for _, s := range skips {
		mark := "declared"
		if !declared(rules, goos, s) {
			mark = "NOT DECLARED"
			undeclared++
		}
		fmt.Fprintf(w, "  [%s] %s %s\n      %s\n", mark, s.Package, s.Test, s.Reason)
	}

	if undeclared > 0 {
		fmt.Fprintf(w, "\n%d skip(s) nobody declared. A row that stops running for a "+
			"reason nobody wrote down is a row nobody knows stopped running.\n"+
			"Either fix the condition, or add the line to the manifest with a comment "+
			"saying why it is legitimate here:\n\n", undeclared)
		for _, s := range skips {
			if !declared(rules, goos, s) {
				fmt.Fprintf(w, "  %s\n", manifestLine(goos, s))
			}
		}
		fmt.Fprintln(w)
	}
	return undeclared
}
