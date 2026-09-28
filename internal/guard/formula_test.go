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
	if down == nil {
		t.Fatal("the checksum file is not handed from the publish job to the formula job as an artefact")
	}
	name, _ := valueOf(down, "name")
	up := uploadNamed(publish.Body, name)
	if name == "" || up == nil {
		t.Fatalf("the formula job downloads the artefact %q, and the publish job uploads no artefact by that name", name)
	}
	if path, _ := valueOf(up, "path"); path != "dist/checksums.txt" {
		t.Errorf("the artefact the formula job downloads is %q, not the checksum file the release tool writes", path)
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
	if _, ok := lineWith(formula.Body, "git rm --quiet --ignore-unmatch Casks/curious.rb"); !ok {
		t.Error("the formula job does not delete the tap's old cask. Nothing writes it any more, " +
			"so a copy left in the tap goes on offering the last version it was given.")
	}
}

// TestTheTapIsWrittenByTheFormulaJobAlone holds the release to one writer
// of the tap, and the credential to that writer.
//
// THE CASK WAS THE SECOND WRITER, AND IT IS GONE. The release tool wrote
// the cask into the tap from the publishing job, which is why that job
// held the tap token. With the cask removed, nothing in the release
// configuration may write to the tap again: neither a cask block nor the
// tool's own formula block, whose deprecation is the reason the formula
// is written outside it. And the token goes where the writing is, which
// is the formula job and nowhere else.
func TestTheTapIsWrittenByTheFormulaJobAlone(t *testing.T) {
	root := moduleRoot(t)
	config := readYAMLLines(readRepoFile(t, root, releaseConfig))
	for _, key := range []string{"homebrew_casks", "brews"} {
		if _, ok := topLevelBlock(config, key); ok {
			t.Errorf("%s has a %s block, so the release tool writes to the tap as well as the "+
				"formula job. The tap has one writer.", releaseConfig, key)
		}
	}
	all := jobs(readYAMLLines(readRepoFile(t, root, releaseWorkflow)))
	seen := 0
	for _, j := range all {
		for _, l := range j.Body {
			if !strings.Contains(l.Text, "secrets.HOMEBREW_TAP_TOKEN") {
				continue
			}
			seen++
			if j.Name != "formula" {
				t.Errorf("%s:%d: the %q job names the tap token. Only the formula job writes to "+
					"the tap, so only it holds the credential.", releaseWorkflow, l.N, j.Name)
			}
		}
	}
	if seen == 0 {
		t.Error("nothing in the release workflow names the tap token, so the formula job cannot write the tap")
	}
}
