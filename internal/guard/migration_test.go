package guard

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The two ways off the older cask. Which one applies depends on whether
// the formula is already installed, because while the cask is installed
// it owns the command and the package manager installs the formula
// without linking it.
const (
	migrateThenInstall = "brew uninstall --cask curious && brew install curiouspub/tap/curiouspub"
	migrateThenLink    = "brew uninstall --cask curious && brew link curiouspub/tap/curiouspub"
)

// TestEveryCaskMigrationNamesBothCommands holds the places a cask user
// can meet the migration — the formula's caveats and the README's install
// section — to naming both commands.
//
// THERE WERE THREE PLACES, AND THE THIRD LEFT WITH THE CASK. The cask
// carried the same caveat while it was still released. It is gone from
// the tap now, but installed copies are not, so the two places that
// remain are the ones a person with an old cask still reads.
//
// IT WAS ONE COMMAND, AND ONE WAS WRONG FOR HALF THE READERS. The cask's
// caveat said to uninstall it and install the formula. On a machine that
// had already installed the formula over the cask, that leaves a formula
// that was never linked — the cask owned the command at the time — and a
// link to nothing once the cask is gone. The repair for that reader is to
// link, not to install, and nothing told them so. It was found on a real
// machine, by hand.
//
// THE FORMULA IS RENDERED, NOT READ. Its caveats are written by the tool
// the release runs, so this row runs that tool against a fixture checksum
// file and reads what it wrote. A row that searched the tool's source for
// the two strings would pass for a string that is declared and never
// printed.
//
// AND NONE OF THEM SAYS THE FORMULA IS LEFT UNLINKED. That was the first
// wording, from one machine whose package manager skipped the link. A
// current package manager takes the command over from the cask instead,
// measured on another, so the sentence was true of one version and false
// of the next. The commands are right for both; the claim about linking
// is held out of both places.
//
// MUTATIONS RUN, performed and observed: dropping the link command from
// each place reds this row naming that place, and no other
// row; so does removing the formula's caveats from the rendered output.
func TestEveryCaskMigrationNamesBothCommands(t *testing.T) {
	root := moduleRoot(t)
	places := map[string]string{
		"the formula's caveats":           renderedFormulaCaveats(t, root),
		"the README's installing section": readmeSection(t, readReadmeFile(t, root), "## Installing it"),
	}
	for place, text := range places {
		if strings.TrimSpace(text) == "" {
			t.Errorf("%s could not be found, so this row read nothing there", place)
			continue
		}
		if strings.Contains(strings.Join(strings.Fields(text), " "), "not linked") {
			t.Errorf("%s says the formula is not linked while the cask is installed. A "+
				"current package manager links it and takes the command over, so the "+
				"sentence is false for the version most readers have.", place)
		}
		for _, command := range []string{migrateThenInstall, migrateThenLink} {
			if !strings.Contains(text, command) {
				t.Errorf("%s does not name\n  %s\nA cask user meets the migration here, and "+
					"which command they need depends on whether the formula is already "+
					"installed — so both have to be written out.", place, command)
			}
		}
	}
}

// renderedFormulaCaveats runs the release's formula tool against a
// fixture and returns the caveats block of what it wrote.
func renderedFormulaCaveats(t *testing.T, root string) string {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the go tool is not on PATH, and this row renders the formula with it: %v", err)
	}
	dir := t.TempDir()
	const version = "9.9.9"
	var sums strings.Builder
	for i, p := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
		fmt.Fprintf(&sums, "%064x  curious_%s_%s.tar.gz\n", i+1, version, p)
	}
	checksums := filepath.Join(dir, "checksums.txt")
	if err := os.WriteFile(checksums, []byte(sums.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "curiouspub.rb")
	cmd := exec.Command(gobin, "run", "./tools/formula",
		"-version", version, "-checksums", checksums,
		"-download-base", "https://example.test/releases/download/v"+version,
		"-homepage", "https://example.test", "-desc", "A fixture.", "-out", out)
	cmd.Dir = root
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rendering the formula: %v\n%s", err, msg)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	formula := string(data)
	start := strings.Index(formula, "  def caveats\n")
	if start < 0 {
		return ""
	}
	end := strings.Index(formula[start:], "\n  end\n")
	if end < 0 {
		return ""
	}
	return formula[start : start+end]
}

// readmeSection returns the README from a heading to the next heading of
// the same level.
func readmeSection(t *testing.T, readme, heading string) string {
	t.Helper()
	start := strings.Index(readme, "\n"+heading+"\n")
	if start < 0 {
		return ""
	}
	rest := readme[start+1+len(heading):]
	if next := strings.Index(rest, "\n## "); next >= 0 {
		rest = rest[:next]
	}
	return rest
}
