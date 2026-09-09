package guard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// THE NAME OF A REQUIRED CHECK IS A STRING IN THREE PLACES and only two
// of them are in this repository: the workflow that produces it, the
// table in CLAUDE.md, and the branch ruleset, which is a setting.
//
// A required check is matched by NAME. Rename a job — or let the Node
// floor rise, since one of the names has the version in it — and the
// check the ruleset is waiting for is simply never produced. GitHub does
// not fail a merge for a required check that never arrives; it has one
// fewer gate, quietly, until somebody edits the setting to match.
//
// This row compares the two copies that are here, so a rename reds in
// CI before it goes dark in the ruleset. It cannot read the ruleset, and
// its message says so rather than implying the third copy is covered.

var (
	// jobLine is a top-level job key: two spaces, a name, a colon.
	jobLine = regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)
	// nameLine is the job's display name.
	nameLine = regexp.MustCompile(`^    name:\s*(.+?)\s*$`)
	// matrixValues is a one-dimensional matrix axis written inline.
	matrixValues = regexp.MustCompile(`^        [A-Za-z0-9_-]+:\s*\[(.+)\]\s*$`)
)

// checkNamesFrom derives the check names one workflow produces: the
// job's display name, or its key, with each matrix value in brackets
// after it — which is how GitHub composes them.
func checkNamesFrom(t *testing.T, path string) []string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var names []string
	inJobs := false
	var key, display string
	var axis []string

	flush := func() {
		if key == "" {
			return
		}
		label := display
		if label == "" {
			label = key
		}
		if len(axis) == 0 {
			names = append(names, label)
		}
		for _, value := range axis {
			names = append(names, label+" ("+value+")")
		}
		key, display, axis = "", "", nil
	}

	for _, line := range strings.Split(string(body), "\n") {
		if line == "jobs:" {
			inJobs = true
			continue
		}
		if !inJobs {
			continue
		}
		if m := jobLine.FindStringSubmatch(line); m != nil {
			flush()
			key = m[1]
			continue
		}
		if m := nameLine.FindStringSubmatch(line); m != nil && key != "" {
			display = strings.Trim(m[1], `"'`)
			continue
		}
		if m := matrixValues.FindStringSubmatch(line); m != nil && key != "" {
			for _, value := range strings.Split(m[1], ",") {
				axis = append(axis, strings.Trim(strings.TrimSpace(value), `"'`))
			}
		}
	}
	flush()
	return names
}

// requiredChecks reads the table in CLAUDE.md — the copy a person
// maintains — as the thing to compare against.
func requiredChecks(t *testing.T, root string) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	row := regexp.MustCompile("(?m)^  \\| `([^`]+)` \\|$")
	var names []string
	for _, m := range row.FindAllStringSubmatch(string(body), -1) {
		names = append(names, m[1])
	}
	return names
}

// TestTheRequiredCheckNamesMatchTheWorkflows.
//
// REQUIRED MUTATION, run 2026-09-09: move the Node floor in the matrix
// from "22" to "24". Reds naming the check the ruleset would go on
// waiting for.
func TestTheRequiredCheckNamesMatchTheWorkflows(t *testing.T) {
	root := moduleRoot(t)

	produced := map[string]bool{}
	for _, workflow := range []string{"ci.yml", "snapshot.yml"} {
		for _, name := range checkNamesFrom(t,
			filepath.Join(root, ".github", "workflows", workflow)) {
			produced[name] = true
		}
	}
	// The control for the derivation: a parser that stopped working
	// would report a clean tree by producing nothing.
	if len(produced) < 6 {
		t.Fatalf("only %d check names were derived from the workflows, so this "+
			"row is not reading them: %v", len(produced), produced)
	}

	listed := requiredChecks(t, root)
	if len(listed) != 7 {
		t.Fatalf("CLAUDE.md lists %d required checks, want 7 — the table is the "+
			"copy this row compares against and it is not being read", len(listed))
	}

	for _, name := range listed {
		if !produced[name] {
			t.Errorf("CLAUDE.md requires the check %q and no workflow job produces "+
				"it any more.\nA required check that never arrives does not fail a "+
				"merge — it simply stops being a gate, and main is un-gated on that "+
				"leg until somebody edits the branch ruleset by hand. This row "+
				"cannot read the ruleset; fix the name here AND there.", name)
		}
	}
}
