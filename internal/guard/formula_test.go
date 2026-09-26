package guard

import (
	"strings"
	"testing"
)

// TestTheFormulaJobWritesFromThisRunsChecksums pins where the Homebrew
// formula comes from. Every platform's sha256 in it is what the package
// manager checks a download against, so its source is this run's own
// checksum file, handed over as an artefact, and never a read of the
// release. And it is written by this repository's program, not by the
// release tool's deprecated formula block, which the tool's own validator
// refuses and TestReleaseConfigIsAcceptedByTheReleaseTool keeps out.
func TestTheFormulaJobWritesFromThisRunsChecksums(t *testing.T) {
	root := moduleRoot(t)
	lines := readYAMLLines(readRepoFile(t, root, releaseWorkflow))
	var formula, publish job
	for _, j := range jobs(lines) {
		switch j.Name {
		case "formula":
			formula = j
		case "publish":
			publish = j
		}
	}
	if formula.Body == nil {
		t.Fatalf("%s has no formula job, so nothing writes the Homebrew formula", releaseWorkflow)
	}
	for _, l := range formula.Body {
		for _, read := range []string{"gh release", "gh api", "releases/download/${GITHUB_REF_NAME}\" -", "curl ", "wget "} {
			if strings.Contains(l.Text, read) && !strings.Contains(l.Text, "-download-base") {
				t.Errorf("%s:%d: the formula job reads from the network (%q)", releaseWorkflow, l.N, read)
			}
		}
	}
	down := stepUsing(formula.Body, "actions/download-artifact@")
	up := stepUsing(publish.Body, "actions/upload-artifact@")
	if down == nil || up == nil {
		t.Fatal("the checksum file is not handed from the publish job to the formula job as an artefact")
	}
	if a, b := valueOf(up, "name"); !b {
		t.Fatal("the publish job's artefact has no name")
	} else if c, _ := valueOf(down, "name"); c != a {
		t.Errorf("the publish job uploads %q and the formula job downloads %q", a, c)
	}
	if _, ok := lineWith(formula.Body, "go run ./tools/formula"); !ok {
		t.Error("the formula job does not run this repository's templater")
	}
	if _, ok := lineWith(formula.Body, "-checksums dist/checksums.txt"); !ok {
		t.Error("the formula job does not hand the templater the artefact's checksum file")
	}
	if _, ok := lineWith(formula.Body, "repository: curiouspub/homebrew-tap"); !ok {
		t.Error("the formula job does not write to the tap")
	}
	if _, ok := lineWith(formula.Body, "cp curiouspub.rb tap/Formula/curiouspub.rb"); !ok {
		t.Error("the formula job does not place the formula at Formula/curiouspub.rb in the tap")
	}
	if !hasEntry(formula.Body, "environment", "release") {
		t.Error("the formula job writes to the tap outside the reviewed release environment")
	}
}
