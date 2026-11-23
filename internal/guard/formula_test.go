package guard

import (
	"strings"
	"testing"
)

// TestTheFormulaJobWritesFromThisRunsChecksums keeps the tap formula
// tied to this run's checksums. Stage renders it before sealing the
// candidate. The tap job promotes those exact bytes and reads them back.
func TestTheFormulaJobWritesFromThisRunsChecksums(t *testing.T) {
	root := moduleRoot(t)
	lines := readYAMLLines(readRepoFile(t, root, releaseWorkflow))
	var stage, formula job
	for _, j := range jobs(lines) {
		switch j.Name {
		case "stage":
			stage = j
		case "formula":
			formula = j
		}
	}
	if stage.Body == nil || formula.Body == nil {
		t.Fatalf("%s must have both stage and formula jobs", releaseWorkflow)
	}
	for _, l := range formula.Body {
		for _, read := range []string{"gh release", "gh api", "releases/download/${GITHUB_REF_NAME}\" -", "curl ", "wget "} {
			if strings.Contains(l.Text, read) && !strings.Contains(l.Text, "-download-base") {
				t.Errorf("%s:%d: the formula job reads from the network (%q)", releaseWorkflow, l.N, read)
			}
		}
	}
	render, ok := runBlock(stage.Body, "go run ./tools/formula")
	if !ok {
		t.Fatal("stage does not run the repository's formula templater")
	}
	if _, ok := lineWith(render, `go run ./tools/formula -version "$want" -checksums sealed/checksums.txt`); !ok {
		t.Error("stage does not render the formula from this candidate's checksums")
	}
	out, writes := lineWith(render, "-out sealed/curiouspub.rb")
	seal, seals := lineWith(render, "shasum -a 256 -- * > MANIFEST.sha256")
	if !writes || !seals || out.N >= seal.N {
		t.Error("stage must write the formula into the candidate before sealing its manifest")
	}
	up := uploadNamed(stage.Body, "candidate")
	if !hasEntry(up, "id", "upload") || !hasEntry(up, "path", "sealed/") ||
		!hasEntry(stage.Body, "artifact-id", "${{ steps.upload.outputs.artifact-id }}") {
		t.Error("stage must expose the identity of its sealed candidate upload")
	}
	down := stepUsing(formula.Body, "actions/download-artifact@")
	if !hasEntry(down, "artifact-ids", "${{ needs.stage.outputs.artifact-id }}") ||
		!hasEntry(down, "digest-mismatch", "error") || !hasEntry(down, "path", "staged") {
		t.Error("the formula job must download the exact stage artifact and reject a digest mismatch")
	}
	if _, ok := lineWith(formula.Body, "go run ./tools/formula"); ok {
		t.Error("the formula job must not regenerate the reviewed formula")
	}
	if !hasEntry(formula.Body, "MANIFEST_SHA", "${{ needs.stage.outputs.manifest-sha }}") {
		t.Error("the formula job must verify against the manifest digest from stage")
	}
	promote, ok := runBlock(formula.Body, `cp "$expected" "$target"`)
	if !ok {
		t.Fatal("the formula job does not copy the verified candidate formula")
	}
	verify, verifies := lineWith(promote, `(cd staged && printf '%s  MANIFEST.sha256\n' "$MANIFEST_SHA" | sha256sum -c - && sha256sum -c MANIFEST.sha256)`)
	copyLine, _ := lineWith(promote, `cp "$expected" "$target"`)
	if !verifies || verify.N >= copyLine.N {
		t.Error("the formula job must verify the approved manifest digest and its members before copying the formula")
	}
	for _, want := range []string{
		`expected="$PWD/staged/curiouspub.rb"`,
		"target=Formula/curiouspub.rb",
	} {
		if _, ok := lineWith(promote, want); !ok {
			t.Errorf("the formula job does not preserve exact tap bytes: missing %q", want)
		}
	}
	fetch, fetches := lineWith(promote, "git fetch origin refs/heads/main")
	readback, reads := lineWith(promote, `git show "FETCH_HEAD:$target" | cmp - "$expected"`)
	if !fetches || !reads || fetch.N >= readback.N {
		t.Error("the formula job must fetch the tap and compare its exact published bytes")
	}
	if _, ok := lineWith(formula.Body, "repository: curiouspub/homebrew-tap"); !ok {
		t.Error("the formula job does not write to the tap")
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
