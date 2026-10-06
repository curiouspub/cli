package guard

// Guards over WHICH GO RELEASE THIS SUITE RUNS UNDER.
//
// go.mod's toolchain line is a floor under Go's default behaviour and not
// a pin: a machine whose own Go is newer runs every command in the module
// with that newer Go, as-is, while CI installs exactly the release the
// line names. A local green is therefore evidence about the toolchain it
// ran on and about no other, and that is not hypothetical: on a newer Go
// three rows that pass on the named release fail, over nothing in this
// module: the same source, under two releases. The two runs simply were
// not the same experiment.
//
// So the Makefile reads the toolchain line and exports it, which makes
// every target run the module's release, and these rows hold the three
// places that has to stay true: the suite itself says so when it is run
// under another Go, the Makefile derives the version from go.mod and
// states it nowhere, and every workflow installs Go from go.mod rather
// than from a number of its own.
//
// REQUIRED MUTATION, run 2026-10-06, on a machine whose own Go is newer
// than the release go.mod names and with GOTOOLCHAIN unset:
//
//   - The derive and export lines removed from the Makefile: the three
//     rows that depend on the release (a successful run's exact bytes, a
//     secret map key's uncovered path, the rendering of an unknown config
//     field) went red, the first row here went red naming
//     both releases, the second went red once for each missing line, and
//     make's own environment chose the newer Go.
//   - The export restated as a literal release, derive line kept: the
//     second row went red twice, naming the restating line and the missing
//     export, and the first stayed
//     green under the named release.
//   - go.mod's toolchain line deleted: make refused at parse time with
//     the message above, exit status 2, before running any recipe.
//   - A workflow's setup-go step given a version of its own, first beside
//     the go.mod line and then in a step spelled with its name before its
//     uses line and no go.mod line at all: the third row went red both
//     times, the second time naming the version and the missing go.mod
//     line.

import (
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// goModToolchain returns the value of go.mod's toolchain line, failing if
// there is none: the Makefile has no other source for the version.
func goModToolchain(t *testing.T, root string) string {
	t.Helper()
	for _, line := range strings.Split(readRepoFile(t, root, "go.mod"), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "toolchain "); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatal("go.mod has no toolchain line, and the Makefile needs one: it is the only place the Go release every target runs under is written down")
	return ""
}

// TestTheSuiteRunsUnderGoModsToolchain fails a run made under any other
// Go release than the one go.mod names.
//
// A direct `go test` on a machine whose Go is newer reds here ON PURPOSE.
// The other failures that run produces are real but unexplained: a row
// that renders differently on a newer release looks like a bug in this
// repository, and this row is what says it is the toolchain instead,
// naming both releases and the two ways to run under the right one.
func TestTheSuiteRunsUnderGoModsToolchain(t *testing.T) {
	want := goModToolchain(t, moduleRoot(t))
	if got := runtime.Version(); got != want {
		t.Fatalf("this suite is running under %s, and go.mod's toolchain line names %s. Run it through make, which runs go.mod's toolchain for every target, or set GOTOOLCHAIN=%s.", got, want, want)
	}
}

// TestTheMakefileTakesTheToolchainFromGoModAndNowhereElse holds the
// Makefile to deriving the release from go.mod, exporting it, and never
// spelling a release of its own. A restated version is a second copy
// that moves on a different day from the first, and the day it does the
// local runs and CI's quietly disagree again.
func TestTheMakefileTakesTheToolchainFromGoModAndNowhereElse(t *testing.T) {
	root := moduleRoot(t)
	lines := strings.Split(readRepoFile(t, root, "Makefile"), "\n")

	derive := regexp.MustCompile(`^GO_TOOLCHAIN\s*:=\s*\$\(shell .*go\.mod.*\)\s*$`)
	export := regexp.MustCompile(`^export GOTOOLCHAIN\s*:=\s*\$\(GO_TOOLCHAIN\)\s*$`)
	restated := regexp.MustCompile(`go1\.[0-9]+`)

	var sawDerive, sawExport bool
	for i, line := range lines {
		line = strings.TrimRight(line, "\r")
		if derive.MatchString(line) {
			sawDerive = true
		}
		if export.MatchString(line) {
			sawExport = true
		}
		if restated.MatchString(line) {
			t.Errorf("Makefile line %d names a Go release (%q). The release is read from go.mod's toolchain line and written nowhere else, so that there is one copy to move.", i+1, strings.TrimSpace(line))
		}
	}
	if !sawDerive {
		t.Error("the Makefile has no line deriving GO_TOOLCHAIN from go.mod, so no target knows which Go release the module names")
	}
	if !sawExport {
		t.Error("the Makefile does not `export GOTOOLCHAIN := $(GO_TOOLCHAIN)`, so its targets run whatever Go the machine has, which on a newer one is not the release CI uses")
	}
}

// TestEverySetupGoReadsTheVersionFromGoMod holds each workflow's Go
// install to the same single source the Makefile uses. A step naming a
// version of its own would put CI on a release the module does not name.
func TestEverySetupGoReadsTheVersionFromGoMod(t *testing.T) {
	root := moduleRoot(t)
	found := 0
	for _, path := range workflowFiles(t, root) {
		lines := readYAMLLines(readWorkflow(t, path))
		for i, l := range lines {
			if !strings.HasPrefix(strings.TrimPrefix(l.Text, "- "), "uses: actions/setup-go@") {
				continue
			}
			found++
			// THE STEP, however it is spelled. The uses line either opens
			// the list item itself or sits inside one that opens on an
			// earlier, shallower line; every line deeper than the item's
			// own belongs to the step, in whichever order its keys come.
			start := i
			if !strings.HasPrefix(l.Text, "- ") {
				for start > 0 && lines[start].Indent >= l.Indent {
					start--
				}
				if !strings.HasPrefix(lines[start].Text, "- ") {
					t.Errorf("%s line %d: a setup-go uses line belongs to no step this guard can read", path, l.N)
					continue
				}
			}
			step := []yamlLine{lines[start]}
			for _, n := range lines[start+1:] {
				if n.Indent <= lines[start].Indent {
					break
				}
				step = append(step, n)
			}
			var haveFile bool
			for _, n := range step {
				text := strings.TrimPrefix(n.Text, "- ")
				if text == "go-version-file: go.mod" {
					haveFile = true
				}
				if strings.HasPrefix(text, "go-version:") {
					t.Errorf("%s line %d: a setup-go step names a Go version of its own (%q); it must read go.mod's instead", path, n.N, n.Text)
				}
			}
			if !haveFile {
				t.Errorf("%s line %d: a setup-go step lacks `go-version-file: go.mod`, so it does not install the release go.mod names", path, l.N)
			}
		}
	}
	if found == 0 {
		t.Fatal("found no setup-go step in any workflow — this guard would silently pass")
	}
}
