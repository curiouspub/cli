package preflight

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/check"
)

// standaloneProject builds a project with a package.json declaring astro
// and whatever else the row needs, IN A TEMPORARY DIRECTORY.
//
// THE TEMPORARY DIRECTORY IS THE ROW'S ONLY GUARANTEE, not hygiene. The
// workspace sniff walks every ancestor of the project to the filesystem
// root, so a fixture committed inside this repository would be answered
// by whatever sits above the checkout — and a row asserting the
// standalone message would then be testing the machine it ran on. It is
// about to matter more: a wrapper package is coming to this repository,
// so a package.json may yet land above this package, and a committed
// fixture would be one unrelated change away from silently testing the
// opposite thing.
func standaloneProject(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if _, ok := files[packageJSONName]; !ok {
		files[packageJSONName] = `{"name":"standalone","dependencies":{"astro":"^5.0.0"}}` + "\n"
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("building the fixture: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("building the fixture: %v", err)
		}
	}

	// THE PRECONDITION IS ASSERTED, NOT ASSUMED. A control that has
	// quietly become a failure case turns a row green for the wrong
	// reason, and this one has a plausible way to rot: somebody's home
	// directory, or a checkout above the temporary directory, carrying a
	// workspace marker.
	if marker, found := workspaceAbove(OSFileSystem{}, root); found {
		t.Fatalf("a workspace marker (%s) exists above %s, so this row cannot "+
			"observe the standalone message at all", marker, root)
	}
	return root
}

// TestLockfileAcceptsEveryInstallableLockfile is the acceptance half of
// every refusal below, and the pnpm row carries an extra assertion of
// its own: NO finding at all, not merely no hard stop. An earlier draft
// warned on a pnpm lockfile, and a lingering warning would fire on every
// pnpm project in the world while the build agent installs from it
// perfectly well.
//
// REQUIRED MUTATION: remove any one entry from installableLockfiles in
// lockfile.go. That entry's row reds and the other two do not.
func TestLockfileAcceptsEveryInstallableLockfile(t *testing.T) {
	for _, tc := range []struct{ name, fixture string }{
		{"package-lock.json", "valid"},
		{"npm-shrinkwrap.json", "lockfile-shrinkwrap"},
		{"pnpm-lock.yaml", "lockfile-pnpm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := CheckLockfile(OSFileSystem{}, projectFixture(t, tc.fixture))
			if len(res.Findings) != 0 {
				t.Errorf("findings = %+v, want none — this lockfile is installed from "+
					"with no warning attached", res.Findings)
			}
			if len(res.Declined) != 0 {
				t.Errorf("declined = %+v, want nothing", res.Declined)
			}
		})
	}
}

// TestLockfileHardStopsOnALockfileItCannotInstallFrom covers the second
// of the three messages. The distinction is the point: telling somebody
// with a lockfile in front of them that they have none is how a message
// loses its reader, so this one names what they have.
//
// REQUIRED MUTATION: append the entries of otherLockfiles to
// installableLockfiles in lockfile.go. Every row here reds on the
// missing hard stop.
func TestLockfileHardStopsOnALockfileItCannotInstallFrom(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fixture string
		names   []string
	}{
		{"yarn only", "lockfile-yarn-only", []string{"yarn.lock"}},
		{"bun only", "lockfile-bun-only", []string{"bun.lockb"}},
		{"both", "lockfile-two-others", []string{"yarn.lock", "bun.lockb"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			finding := soleFinding(t, CheckLockfile(OSFileSystem{}, projectFixture(t, tc.fixture)),
				check.IDLockfile)
			if finding.Severity != check.SeverityHardStop {
				t.Errorf("severity = %q, want a hard stop — the build installs from "+
					"neither of these", finding.Severity)
			}
			text := wholeText(finding)
			for _, name := range tc.names {
				if !strings.Contains(text, name) {
					t.Errorf("the message does not name %s, so the reader is told they "+
						"have no lockfile while looking at one:\n%s", name, text)
				}
			}
			if !strings.Contains(text, "npm install") {
				t.Errorf("the message names no install command:\n%s", text)
			}
		})
	}
}

// TestLockfileHardStopsWithNoLockfileAtAll is the standing message, and
// the three spellings are asserted individually.
//
// THE THIRD SPELLING IS WHY THIS ROW IS NOT A CONTAINS-ONE-WORD CHECK.
// The build agent reaches this same condition from the other side and
// emits its own message; the two must tell the same story, and its
// wording names three lockfiles it will install from. A project carrying
// only an npm-shrinkwrap.json builds perfectly well, so a message naming
// two of the three would tell its owner they have no lockfile.
//
// REQUIRED MUTATION: drop "npm-shrinkwrap.json or " from the Why in
// CheckLockfile. Reds naming that spelling.
func TestLockfileHardStopsWithNoLockfileAtAll(t *testing.T) {
	root := standaloneProject(t, map[string]string{})

	finding := soleFinding(t, CheckLockfile(OSFileSystem{}, root), check.IDLockfile)
	if finding.Severity != check.SeverityHardStop {
		t.Errorf("severity = %q, want a hard stop", finding.Severity)
	}
	for _, spelling := range installableLockfiles {
		if !strings.Contains(finding.Why, spelling) {
			t.Errorf("Why does not name %s:\n%s", spelling, finding.Why)
		}
	}
	if !strings.Contains(finding.Next, "commit the lockfile") {
		t.Errorf("Next = %q, want the instruction that ends this for a standalone "+
			"project", finding.Next)
	}
}

// TestLockfileMessageDiffersInsideAWorkspace covers the third message,
// one row per detection signal.
//
// THE VERDICT DOES NOT MOVE, only the words. A package inside a
// workspace genuinely cannot be built on its own — its dependencies
// resolve through the workspace root — so a deploy that got past here
// would die in the sandbox with a worse message. What was wrong was the
// INSTRUCTION: telling somebody to create a file they already have,
// three directories up.
//
// ASSERTING THE ABSENCE OF THE OLD ADVICE IS HALF THE ROW. Asserting
// only the new wording would let both strings ship together, which is
// the shape this failure actually takes.
//
// The astro-dep assertion beside it is the control: it proves the inner
// package really is a complete Astro project, so the lockfile hard stop
// below it is about the lockfile and not about a fixture that would fail
// anything.
//
// REQUIRED MUTATION: delete the workspaceAbove branch from
// CheckLockfile. Every row falls through to the standing message, which
// reds on both the "workspace" assertion and the absence one.
func TestLockfileMessageDiffersInsideAWorkspace(t *testing.T) {
	for _, tc := range []struct{ name, fixture string }{
		{"a pnpm workspace file", "workspace-pnpm"},
		{"a package.json with a workspaces field", "workspace-field"},
		{"a monorepo build config", "workspace-turbo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(projectFixture(t, tc.fixture), "apps", "site")

			if res := CheckAstroDep(OSFileSystem{}, root); len(res.Findings) != 0 {
				t.Fatalf("the inner package did not pass astro-dep (%+v), so the "+
					"lockfile row below it is not about the lockfile", res.Findings)
			}

			finding := soleFinding(t, CheckLockfile(OSFileSystem{}, root), check.IDLockfile)
			if finding.Severity != check.SeverityHardStop {
				t.Errorf("severity = %q, want a hard stop — this package cannot be "+
					"built standalone", finding.Severity)
			}
			text := wholeText(finding)
			if !strings.Contains(strings.ToLower(text), "workspace") {
				t.Errorf("the message does not say this is a workspace:\n%s", text)
			}
			if strings.Contains(text, "commit the lockfile") {
				t.Errorf("the message still tells them to commit a lockfile they "+
					"already have:\n%s", text)
			}
		})
	}
}

// TestLockfileSniffIgnoresAMarkerInTheProjectItself pins the decision
// that only ANCESTORS are searched. A marker in the directory being
// deployed says that THIS project is the workspace root — a different
// thing to tell somebody, and one for which the standing advice, create
// a lockfile, is correct.
//
// REQUIRED MUTATION: in workspaceAbove (lockfile.go), check the markers
// before climbing — move the marker loop above the `dir = parent`
// assignment. Reds: the standing message becomes the workspace one.
func TestLockfileSniffIgnoresAMarkerInTheProjectItself(t *testing.T) {
	root := standaloneProject(t, map[string]string{
		"pnpm-workspace.yaml": "packages:\n  - 'apps/*'\n",
	})

	finding := soleFinding(t, CheckLockfile(OSFileSystem{}, root), check.IDLockfile)
	if strings.Contains(strings.ToLower(wholeText(finding)), "workspace") {
		t.Errorf("a marker in the project's own directory was read as a workspace "+
			"ABOVE it:\n%s", wholeText(finding))
	}
}

// TestLockfileDeclinesWhenThereIsNoPackageJSON is the check saying it
// cannot run rather than saying something wrong. "Commit a lockfile" is
// bad advice for a directory that is not a Node project at all: the
// astro-dep check has already said the useful thing, and a second,
// wronger instruction underneath it only muddies the fix.
//
// THE KIND IS ASSERTED BECAUSE IT IS A PRICE. Environmental joins the
// question a surface asks and refuses when there is nobody to ask; the
// other kind renders below advisory and asks nothing. The two behave
// identically on this condition TODAY, because astro-dep hard-stops in
// the same run and a hard stop never prompts — which is exactly why the
// choice is pinned here rather than left to be discovered later.
//
// REQUIRED MUTATION: in CheckLockfile, return the standing no-lockfile
// hard stop instead of the decline. Reds on the finding count.
//
// SECOND REQUIRED MUTATION: give the decline check.ByDesign. Reds on the
// kind alone, which is what makes that assertion load-bearing.
func TestLockfileDeclinesWhenThereIsNoPackageJSON(t *testing.T) {
	res := CheckLockfile(OSFileSystem{}, projectFixture(t, "no-package-json"))

	if len(res.Findings) != 0 {
		t.Errorf("findings = %+v, want none — a check that cannot run says so rather "+
			"than guessing", res.Findings)
	}
	declined, ok := res.Declined[check.IDLockfile]
	if !ok {
		t.Fatalf("declined = %+v, want a row for %s", res.Declined, check.IDLockfile)
	}
	if declined.Kind != check.Environmental {
		t.Errorf("kind = %v, want Environmental — something outside the check stopped "+
			"it looking, and the reader can see it", declined.Kind)
	}
	if !strings.Contains(declined.Reason, packageJSONName) {
		t.Errorf("reason = %q, want it to name the file that was missing", declined.Reason)
	}
}

// TestLockfileDeclinesWhenItCannotTellWhetherALockfileIsThere is the
// difference between "there is none" and "I could not look", on the
// question this check exists to answer. Every sentence this check can
// produce asserts absence, and a permission error must not become one of
// them.
//
// The control below is the other half: the same fixture, nothing
// refused, is accepted — so the decline is about the refusal and not
// about a check that declines everything.
//
// REQUIRED MUTATION: in CheckLockfile, delete the `unanswerable` branch.
// The check then reports the standing no-lockfile hard stop about a
// project whose lockfile it simply could not stat, and this row reds.
func TestLockfileDeclinesWhenItCannotTellWhetherALockfileIsThere(t *testing.T) {
	root := projectFixture(t, "valid")
	fsys := refusingFS{refuse: map[string]bool{
		filepath.Join(root, "package-lock.json"): true,
	}}

	res := CheckLockfile(fsys, root)
	if len(res.Findings) != 0 {
		t.Errorf("findings = %+v, want none — a confident claim of absence built out "+
			"of a permission error is the thing this branch prevents", res.Findings)
	}
	declined, ok := res.Declined[check.IDLockfile]
	if !ok {
		t.Fatalf("declined = %+v, want a row for %s", res.Declined, check.IDLockfile)
	}
	if !strings.Contains(declined.Reason, "package-lock.json") {
		t.Errorf("reason = %q, want it to name the file it could not check",
			declined.Reason)
	}

	if res := CheckLockfile(OSFileSystem{}, root); len(res.Declined) != 0 {
		t.Errorf("the same fixture read normally declined %+v, so the row above is "+
			"not about the refusal", res.Declined)
	}
}

// TestWorkspaceSniffChangesNothingOnDisk is the read-only claim, checked
// against the tree rather than trusted.
//
// REQUIRED MUTATION: in workspaceAbove (lockfile.go), write a file into
// each directory it visits before stat-ing it. Reds on the comparison.
func TestWorkspaceSniffChangesNothingOnDisk(t *testing.T) {
	fixture := projectFixture(t, "workspace-pnpm")
	before := treeSnapshot(t, fixture)

	CheckLockfile(OSFileSystem{}, filepath.Join(fixture, "apps", "site"))

	if after := treeSnapshot(t, fixture); !reflect.DeepEqual(before, after) {
		t.Errorf("the fixture tree changed:\nbefore %v\nafter  %v", before, after)
	}
}

// recordingFS is the real filesystem with every stat-ed path recorded.
// It is what turns "walks up to the filesystem root" from a claim into a
// measurement: the set of directories asked about is observable, and the
// two ways this loop can be wrong — starting in the wrong place, and
// stopping too early — change that set in opposite directions.
type recordingFS struct {
	visited []string
}

func (r *recordingFS) record(name string) { r.visited = append(r.visited, filepath.Dir(name)) }

func (r *recordingFS) Stat(name string) (fs.FileInfo, error) {
	r.record(name)
	return os.Stat(name)
}

func (r *recordingFS) Lstat(name string) (fs.FileInfo, error) {
	r.record(name)
	return os.Lstat(name)
}

func (r *recordingFS) Open(name string) (io.ReadCloser, error) { return os.Open(name) }

// TestWorkspaceSniffClimbsEveryAncestorAndStops measures the climb.
//
// It runs from a temporary directory with no marker anywhere above it,
// so the sniff cannot stop early for a legitimate reason and the visited
// set is the whole climb.
//
// REQUIRED MUTATION: in workspaceAbove, return "" ,false after the first
// ancestor. Reds on the missing directories.
//
// SECOND REQUIRED MUTATION: move the marker loop above `dir = parent`.
// The project's own directory joins the visited set and this reds too,
// which is the same defect the ignores-its-own-marker row catches from
// the other side.
func TestWorkspaceSniffClimbsEveryAncestorAndStops(t *testing.T) {
	root := standaloneProject(t, map[string]string{})

	var want []string
	for dir := filepath.Dir(root); ; {
		want = append(want, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	fsys := &recordingFS{}
	if marker, found := workspaceAbove(fsys, root); found {
		t.Fatalf("found a workspace marker (%s) above a temporary directory", marker)
	}

	seen := map[string]bool{}
	for _, dir := range fsys.visited {
		seen[dir] = true
	}
	for _, dir := range want {
		if !seen[dir] {
			t.Errorf("the climb never asked about %s, so it stopped short of the "+
				"filesystem root", dir)
		}
	}
	if seen[filepath.Clean(root)] {
		t.Errorf("the climb asked about the project directory itself, which is not " +
			"an ancestor of it")
	}
}
