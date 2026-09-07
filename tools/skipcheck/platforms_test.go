package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestKnownPlatformsMatchTheMatrixTheWorkflowRuns asserts that the
// platforms the skip manifest may name and the legs CI actually runs are
// the same set, in both directions.
//
// THE TWO FAILURES ARE DIFFERENT AND BOTH ARE SILENT. A platform this
// tool accepts that no leg runs makes a manifest line that can never
// match: it looks like coverage and is decoration, and the skip it was
// written for goes on failing the run somewhere else — or worse, the
// line is the reason nobody notices the leg is gone. A leg that runs and
// this tool would reject is the opposite: a legitimate skip on that
// platform cannot be declared at all, so the run fails and the only way
// out is to widen a rule that has nothing to do with the problem.
//
// IT LIVES IN THIS PACKAGE ON PURPOSE. Placed with the repo-wide guards
// it would have to recover knownPlatforms from source, which means
// scraping the elements of a slice literal out of the syntax tree — a
// second transcription of the value, agreeing with the first for as long
// as nobody writes it a different way. Here it reads the variable
// itself, and the only thing left to parse is the workflow, which is not
// this repository's to restate.
//
// The residue, named rather than left to be found:
//
//   - The workflow is read as text. A matrix expressed some other way —
//     an include list, a variable — would be invisible, and this row
//     fails loudly rather than passing if it finds no matrix at all.
//   - It asserts the SET, not that every leg runs the same command. A
//     leg that runs something else entirely is a different question.
func TestKnownPlatformsMatchTheMatrixTheWorkflowRuns(t *testing.T) {
	legs := matrixPlatforms(t)

	// The wildcard is a rule-file spelling, not a platform. Counting it
	// as one would let a matrix leg go missing and be paid for by an
	// asterisk that means "all of them" and would then mean fewer.
	accepted := map[string]bool{}
	for _, p := range knownPlatforms {
		if p == "*" {
			continue
		}
		accepted[p] = true
	}
	if len(accepted) == len(knownPlatforms) {
		t.Error("the wildcard is missing from knownPlatforms; a manifest line covering " +
			"every platform has no spelling")
	}

	running := map[string]bool{}
	for _, p := range legs {
		running[p] = true
	}

	var acceptedNotRun, runNotAccepted []string
	for p := range accepted {
		if !running[p] {
			acceptedNotRun = append(acceptedNotRun, p)
		}
	}
	for p := range running {
		if !accepted[p] {
			runNotAccepted = append(runNotAccepted, p)
		}
	}
	sort.Strings(acceptedNotRun)
	sort.Strings(runNotAccepted)

	if len(acceptedNotRun) > 0 {
		t.Errorf("accepted in a manifest line but run by no matrix leg: %s\n"+
			"a rule naming it can never match, so it reads as coverage and is decoration",
			strings.Join(acceptedNotRun, ", "))
	}
	if len(runNotAccepted) > 0 {
		t.Errorf("run by the matrix but rejected in a manifest line: %s\n"+
			"a legitimate skip there cannot be declared at all", strings.Join(runNotAccepted, ", "))
	}
}

// matrixPlatforms reads the workflow's matrix and returns the GOOS names
// of the legs it runs.
func matrixPlatforms(t *testing.T) []string {
	t.Helper()

	// runnerGOOS maps a runner image label to the operating system Go
	// names it. The labels carry a channel suffix — "-latest", a version
	// — which is why the prefix is what is matched.
	runnerGOOS := map[string]string{
		"ubuntu":  "linux",
		"macos":   "darwin",
		"windows": "windows",
	}

	// NAMED RELATIVE TO THE REPOSITORY in every message below. An
	// absolute path here puts the checkout's location — and the
	// operator's username — into output that CI logs verbatim, which is
	// a lesson one guard over already paid for.
	const workflow = ".github/workflows/ci.yml"
	data, err := os.ReadFile(filepath.Join(repoRoot(t), filepath.FromSlash(workflow)))
	if err != nil {
		t.Fatalf("reading %s: %v", workflow, err)
	}

	var found []string
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "os:") {
			continue
		}
		open := strings.Index(trimmed, "[")
		closeAt := strings.LastIndex(trimmed, "]")
		if open < 0 || closeAt < open {
			continue
		}
		for _, label := range strings.Split(trimmed[open+1:closeAt], ",") {
			label = strings.Trim(strings.TrimSpace(label), `"'`)
			name, known := runnerGOOS[strings.SplitN(label, "-", 2)[0]]
			if !known {
				t.Fatalf("the workflow runs %q and this row does not know what operating "+
					"system that is; a leg it cannot name is a leg it cannot check", label)
			}
			found = append(found, name)
		}
	}

	if len(found) == 0 {
		t.Fatalf("no matrix was found in %s — this row scanned nothing, and a row that "+
			"passes over an empty set is worse than none", workflow)
	}
	return found
}

// repoRoot walks up from this package to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("locating the working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}
