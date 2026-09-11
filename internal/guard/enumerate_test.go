package guard

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------
// One answer to "which files are ours"
// ---------------------------------------------------------------------
//
// This package used to have two, and they disagreed exactly when a
// worktree lane existed. One walked the tree honouring nothing but three
// hand-written directory names; the other asked git and therefore
// honoured every ignore rule the repository already reviews. Measured
// with two lanes open: the walk saw 188 Go files with its skip list and
// 572 without, against git's 188. They agreed only because somebody
// maintained the list — and the list had already drifted from the rule
// it duplicated, skipping all of .claude/ where .gitignore ignores
// .claude/worktrees/ narrowly and on purpose, "so anything this
// repository later decides to track under .claude/ is unaffected".
//
// The walk needs a skip for every ignored directory that ever appears.
// Git needs none, because .gitignore is already that list.
//
// # IT HONOURS .gitignore AND NOTHING ELSE
//
// --exclude-standard is three sources in one flag: the project's
// .gitignore files, .git/info/exclude, and the operator's global
// core.excludesFile. The last two are properties of a MACHINE rather
// than of this repository, so a personal rule could drop a real file out
// of a guard's scope on one laptop and nowhere else — the guard passing
// because of something nobody else can see. Two people running this
// suite over the same commit must be running it over the same files.
//
// So the flag is --exclude-per-directory=.gitignore, which reads the
// project tree and stops. The pack matcher made the same choice for the
// same reason and its brief states it: a tarball that depended on the
// developer's machine-level git config would let two people packing one
// commit produce different sites, with the difference invisible to both.
//
// Measured, git 2.50.1, in a hermetic temp repository: with a file named
// in .git/info/exclude, --exclude-standard returns two Go files and this
// flag returns three. Both drop the file named in .gitignore. On this
// repository the two forms return identical sets — 197 Go files either
// way — so the change is behaviour-preserving here and shows only where
// the trap lives. TestTheEnumerationHonoursGitignoreAndNothingElse
// asserts it rather than trusting the flag's name.
//
// # THE EXCEPTIONS ARE SEMANTIC, AND EACH ONE'S REASON IS CHECKED
//
// What remains after git has answered is not a skip list of ignored
// directories — git handled those — but a short list of things that ARE
// in the repository and are not this module's code. Every one carries a
// reason, and the row below asserts the reason is still true rather than
// asserting the name is still listed. A guard's exception outliving its
// reason is how a scope quietly stops meaning what it says.
// gitExecPath is the ONE GIT_-prefixed variable gitSafeEnv keeps, and it
// is kept because it answers "where are git's own helper binaries",
// which is a question about the installation rather than about which
// repository is being read. Portable and relocated git builds set it and
// cannot run without it.
const gitExecPath = "GIT_EXEC_PATH"

// gitSafeEnv is the process environment with every OTHER GIT_-prefixed
// variable removed, so that cmd.Dir is the only thing deciding which
// repository a git command in this file operates on.
//
// IT IS AN ALLOWLIST BY CONSTRUCTION, and that is the whole design. The
// obvious shape is a denylist — GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE,
// GIT_COMMON_DIR, GIT_OBJECT_DIRECTORY, GIT_CEILING_DIRECTORIES,
// GIT_NAMESPACE, GIT_CONFIG_COUNT and its numbered keys — and a denylist
// of environment variables is *a closed list built by a pattern*, which
// cannot contain the members that arrive after the pattern was chosen
// A closed list built by a pattern cannot contain the members that
// arrive after the pattern was chosen, and git has added variables to
// that set before and will again. The next one would arrive silently,
// because nothing in a denylist reports what it failed to think of.
//
// WHY IT EXISTS, measured 2026-09-11 during a cold read. The
// commands here previously inherited the environment verbatim:
//
//   - with GIT_INDEX_FILE pointing at a populated index, `git ls-files
//     --cached` in a freshly-initialised repository returned a file that
//     did not exist in it at all. The Lstat filter below happens to drop
//     such a path, so the rows stayed green — the leak was MASKED rather
//     than prevented, which is the worse of the two outcomes because it
//     leaves nothing to notice.
//   - with GIT_DIR set, `git init` in a new directory does not produce
//     the repository being measured, and the enumeration reports the
//     OTHER repository's contents. Measured: a fixture that should have
//     held two files reported somebody else's.
//
// Neither is hypothetical and neither needs malice: both variables are
// set by ordinary tooling — hooks, wrappers, IDE integrations, and any
// script that runs this suite from inside another git operation. The
// second one reaches the REAL guards, not only the fixture, because
// repoFiles is what every guard in this package is built on.
//
// This is the hermetic recipe's field of view, one directory over from
// where that lesson was written. The recipe inherited from the pack
// matcher neutralises git's CONFIGURATION — global, system, home,
// template — and says nothing about the variables that decide which
// repository is open. A structural fix has a field of view, and the
// field of view is itself an unguarded assumption.
func gitSafeEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if ok && strings.HasPrefix(name, "GIT_") && name != gitExecPath {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

func repoFiles(t *testing.T, root, subtree string) []string {
	t.Helper()

	// -z, so a path containing a newline cannot split into two entries.
	// --cached is the tracked set; --others adds untracked files and the
	// exclude flag subtracts what the PROJECT's own ignore rules cover.
	args := []string{"ls-files", "-z", "--cached", "--others",
		"--exclude-per-directory=" + gitignoreFile}
	if subtree != "" {
		args = append(args, "--", subtree)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Env = gitSafeEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files in %s: %v\n"+
			"This guard cannot establish which files are this repository's, so it "+
			"fails rather than report a pass over a set it never determined.",
			root, err)
	}

	var files []string
	seen := make(map[string]bool)
	for _, name := range strings.Split(string(out), "\x00") {
		// An unmerged path is listed once per stage, so dedupe rather
		// than scan the same file three times mid-conflict.
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if excludedByReason(name) {
			continue
		}

		path := filepath.Join(root, filepath.FromSlash(name))
		info, statErr := os.Lstat(path)
		if statErr != nil {
			if errors.Is(statErr, fs.ErrNotExist) {
				// Tracked in the index, deleted in the working tree.
				// There is no content to read, and that is not a
				// violation — but it IS a way the index and the disk
				// disagree, so the row below asserts this branch rather
				// than leaving it to be discovered.
				continue
			}
			t.Fatalf("stat %s: %v", path, statErr)
		}
		if !info.Mode().IsRegular() {
			// A symlink or a submodule directory: nothing to read here,
			// and following either would scan content this repository
			// does not author.
			continue
		}
		files = append(files, path)
	}

	if len(files) == 0 {
		t.Fatalf("git listed no file under %s, so every guard built on this would "+
			"silently pass", displayPath(root, filepath.Join(root, subtree)))
	}
	sort.Strings(files)
	return files
}

// gitignoreFile is the ONE ignore source this enumeration reads. Named
// rather than inlined so the mutation that restores --exclude-standard
// is a visible edit at a named constant instead of a character inside a
// string.
const gitignoreFile = ".gitignore"

// repoException is a path prefix that is in this repository and is not
// this module's code, with the reason it is excluded and a check that
// the reason still holds.
type repoException struct {
	prefix string
	why    string
	// stillTrue re-establishes the reason from the tree. It returns the
	// evidence when the reason holds and a description of what it found
	// when it does not.
	stillTrue func(t *testing.T, root string) (string, bool)
}

// repoExceptions is the whole list, and it is SHORT because git handles
// the ignored ones. bin/ and .claude/worktrees/ are not here: both are
// gitignored, so the enumeration already excludes them and a second
// mention would be a second home for one fact — which is the defect this
// file exists to remove.
var repoExceptions = []repoException{
	{
		prefix: "vendor",
		why:    "a vendored dependency is somebody else's code, carried rather than authored",
		stillTrue: func(t *testing.T, root string) (string, bool) {
			t.Helper()
			// THE SAME FLAGS repoFiles USES, not git's default set.
			// This check justifies a filter that runs over --cached AND
			// --others, and it asked git only about --cached: an
			// untracked, unignored vendor/ tree — exactly what `go mod
			// vendor` leaves behind, and nothing in .gitignore covers
			// it — was removed by excludedByReason while this reason
			// reported that nothing was excluded. An exception's
			// reason-check with a narrower field of view than the filter
			// it justifies reports "inert" about a filter that is
			// acting. Found 2026-09-11 by a cold read; the repo
			// has no vendor tree today, so the defect was reachable
			// rather than live.
			cmd := exec.Command("git", "ls-files", "-z", "--cached", "--others",
				"--exclude-per-directory="+gitignoreFile, "--", "vendor")
			cmd.Dir = root
			cmd.Env = gitSafeEnv()
			out, err := cmd.Output()
			if err != nil {
				return "git ls-files -- vendor failed: " + err.Error(), false
			}
			listed := strings.Count(strings.TrimRight(string(out), "\x00"), "\x00")
			if len(bytes.TrimRight(out, "\x00")) > 0 {
				listed++
			}
			if listed == 0 {
				return "vendor/ holds no file this enumeration would have listed, " +
					"so nothing is excluded by it", true
			}
			// A tracked vendor tree is still somebody else's code, so
			// the exception holds — but it now has an effect, and a
			// reader should know how large it is.
			return "", true
		},
	},
}

// excludedByReason reports whether a git-listed path is covered by one
// of the exceptions above.
func excludedByReason(name string) bool {
	for _, e := range repoExceptions {
		if name == e.prefix || strings.HasPrefix(name, e.prefix+"/") {
			return true
		}
	}
	return false
}

// TestEveryEnumerationExceptionStillHasItsReason checks the reasons
// rather than the names.
//
// AN EXCEPTION OUTLIVING ITS REASON IS HOW A SCOPE STOPS MEANING WHAT IT
// SAYS, and this package has the instance: a streams guard carried an
// exception whose stated reason — "renders no server-supplied string" —
// had stopped being true, and the guard went on excluding the file on
// the strength of a sentence nobody rechecked.
func TestEveryEnumerationExceptionStillHasItsReason(t *testing.T) {
	root := moduleRoot(t)
	if len(repoExceptions) == 0 {
		t.Fatal("there are no exceptions to check, so this row proves nothing about " +
			"a list it never read")
	}
	for _, e := range repoExceptions {
		t.Run(e.prefix, func(t *testing.T) {
			if e.why == "" {
				t.Fatalf("%q is excluded from every guard's scope and gives no reason",
					e.prefix)
			}
			evidence, ok := e.stillTrue(t, root)
			if !ok {
				t.Errorf("%q is excluded because %q, and that is no longer true: %s",
					e.prefix, e.why, evidence)
			}
			if evidence != "" {
				t.Logf("%s: %s", e.prefix, evidence)
			}
		})
	}
}

// ---------------------------------------------------------------------
// The rows that red when two enumerations would have disagreed
// ---------------------------------------------------------------------

// hermeticRepo builds a repository in a temp directory and returns its
// root. NOTHING IN THE CHECKOUT IS WRITTEN OR DELETED BY THESE ROWS, and
// that is a rule rather than tidiness.
//
// A row that creates and deletes files in the tree it runs in fails
// dirty: an interrupted run leaves a repository somebody cleans by hand,
// and the fixture it left behind is a .go file the NEXT run's guards
// will scan. Worse, a row that deletes a TRACKED file from the working
// checkout to see what an enumeration does is a row that, on a bad day,
// deletes somebody's uncommitted work. The fixtures are the point; the
// checkout is not a fixture.
//
// # THE ENVIRONMENT IS NEUTRALISED, AND THE RECIPE WAS PAID FOR ONCE
//
// Inherited verbatim from the pack matcher's differential test,
// INCLUDING the part that had to be corrected: GIT_CONFIG_GLOBAL and
// GIT_CONFIG_SYSTEM point at paths inside this temp directory that do
// not exist, and HOME **and USERPROFILE** point at an empty one. The
// obvious recipe is spelled in Unix — GIT_CONFIG_GLOBAL=/dev/null with
// HOME alone works on Linux and macOS and neutralises nothing on
// Windows, where git resolves home from USERPROFILE. The leg that
// neutralises nothing is then the leg that reports green, on the one
// platform nobody develops on, and this repository's gate runs there.
// A missing config file is treated by git as empty, so the
// nonexistent-path form needs no platform branch.
func hermeticRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH, so this row cannot build a repository: %v", err)
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("making an empty home: %v", err)
	}
	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("making the repository root: %v", err)
	}

	// gitSafeEnv FIRST, then the deliberate additions on top: strip every
	// GIT_ variable the operator holds, then put back only the ones this
	// fixture means to set. Built the other way round — os.Environ() with
	// additions — an inherited GIT_DIR or GIT_INDEX_FILE survives, and
	// the fixture measures a repository it did not build. Measured, both
	// of them, on 2026-09-11; see gitSafeEnv.
	env := gitSafeEnv(
		"HOME="+home,
		"USERPROFILE="+home,
		"GIT_CONFIG_GLOBAL="+filepath.Join(dir, "no-such-global"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(dir, "no-such-system"),
		"GIT_AUTHOR_NAME=guard", "GIT_AUTHOR_EMAIL=guard@example.invalid",
		"GIT_COMMITTER_NAME=guard", "GIT_COMMITTER_EMAIL=guard@example.invalid",
	)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	// --template= so no machine-level hook or exclude file is copied in.
	run("init", "-q", "--template=", ".")

	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("making %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	write(".gitignore", "ignored-by-gitignore.go\n")
	write("tracked.go", "package x\n")
	write("tracked-then-deleted.go", "package x\n")
	run("add", ".gitignore", "tracked.go", "tracked-then-deleted.go")
	run("commit", "-qm", "fixtures")

	// The two directions, made in the temp repository and nowhere else.
	write("ignored-by-gitignore.go", "package x\n")
	// UNTRACKED AND NOT IGNORED: the only kind of file an exclude rule
	// can reach. See TestTheEnumerationHonoursGitignoreAndNothingElse
	// for why the distinction is the whole row.
	write("untracked-but-ours.go", "package x\n")
	if err := os.Remove(filepath.Join(root, "tracked-then-deleted.go")); err != nil {
		t.Fatalf("deleting the tracked fixture: %v", err)
	}
	return root
}

// naiveWalk is the enumeration this package USED to have: a walk that
// honours nothing but a list of directory names. It is kept, here, as
// the thing the row measures against — because the disagreement has to
// be a measurement the suite takes rather than a fact somebody
// remembered, and a row that only checked the correct set would stay
// green the day the walk came back.
func naiveWalk(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	sort.Strings(files)
	return files
}

func relNames(root string, paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		rel, err := filepath.Rel(root, p)
		if err != nil {
			rel = p
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

// TestTheEnumerationAndAWalkDisagreeInBothDirections is the row this
// task exists for.
//
// # WHICH FILE WITNESSES WHICH DIRECTION, AND WHY IT IS NOT SYMMETRIC
//
// Corrected 2026-09-11 after a cold read, because the comment at the
// bottom of this row used to say the two directions were carried by
// "one file ignored and one deleted", and that was wrong in a way the
// row could not report. Measured, the sets are:
//
//	enumeration: .gitignore, tracked.go, untracked-but-ours.go
//	naive walk:  ignored-by-gitignore.go, tracked.go, untracked-but-ours.go
//
// So tracked-then-deleted.go is in NEITHER, and witnesses no
// disagreement at all. It is still load-bearing — the loop above asserts
// the enumeration drops it, and mutation 2 reds on exactly that — but
// what it proves is that the index and the disk disagree and the
// enumeration sides with the disk. That is a different claim from this
// row's title, and it was being counted toward the title.
//
// The two real witnesses are ignored-by-gitignore.go and .gitignore, and
// THEY ARE NOT THE SAME KIND OF FACT:
//
//   - ignored-by-gitignore.go is the git direction. The walk sees it,
//     the enumeration honours .gitignore and does not. This is the
//     disagreement the task was minted for.
//   - .gitignore is in the enumeration and not in the walk ONLY because
//     naiveWalk filters to ".go". It is an artefact of the walk's own
//     suffix filter, not a fact about git.
//
// And the asymmetry is STRUCTURAL rather than an accident of these
// fixtures: repoFiles returns (git's set) ∩ (what is on disk), because
// of the Lstat filter. A same-filter walk returns what is on disk. So
// the enumeration can never exceed such a walk on git grounds — only on
// files the walk's own filter drops. There is no fixture that would fix
// this, which is why the answer is to say it here rather than to add one.
//
// REQUIRED MUTATIONS, RUN 2026-09-11 AGAINST THE TEMP REPOSITORY:
//
//  1. Drop the exclude flag from repoFiles. ignored-by-gitignore.go
//     enters the set and the first assertion reds.
//  2. Drop the fs.ErrNotExist branch. tracked-then-deleted.go enters the
//     set and the second reds.
//  3. Point the helpers back at a walk. The third assertion reds, which
//     is the one that makes this a guard against the change coming back
//     rather than a one-time correction.
func TestTheEnumerationAndAWalkDisagreeInBothDirections(t *testing.T) {
	root := hermeticRepo(t)
	got := relNames(root, repoFiles(t, root, ""))

	// IN THE WORKING TREE, NOT IN GIT. The walk sees it; the
	// enumeration must not.
	for _, name := range got {
		if name == "ignored-by-gitignore.go" {
			t.Errorf("the enumeration returned %s, which .gitignore covers.\n"+
				"A guard scoped by this set would then scan a file the repository "+
				"does not publish — which is what a walk does, and why this "+
				"enumeration asks git.", name)
		}
	}

	// TRACKED IN GIT, DELETED ON DISK. git lists it from the index; the
	// enumeration must drop it, because there is no content to read.
	for _, name := range got {
		if name == "tracked-then-deleted.go" {
			t.Errorf("the enumeration returned %s, which is in the index and not on "+
				"disk. Reading it is an error, and reporting it as scanned is a "+
				"claim about a file that was never opened.", name)
		}
	}

	// The set that SHOULD come back, stated in full so an addition is as
	// visible as a removal.
	want := []string{".gitignore", "tracked.go", "untracked-but-ours.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the enumeration returned %v, want %v", got, want)
	}

	// AND THE WALK DISAGREES — measured here rather than remembered.
	walked := relNames(root, naiveWalk(t, root))
	wantWalk := []string{"ignored-by-gitignore.go", "tracked.go", "untracked-but-ours.go"}
	if strings.Join(walked, ",") != strings.Join(wantWalk, ",") {
		t.Fatalf("the naive walk returned %v, want %v — this row's whole subject is "+
			"the difference between the two answers, so a walk that no longer "+
			"differs means the fixtures stopped exercising it", walked, wantWalk)
	}
	// EACH DIRECTION IS ASSERTED BY ITS OWN WITNESS, BY NAME.
	//
	// The set comparison above is not enough on its own: two sets can
	// differ while one is a subset of the other, so a row that only
	// checked `got != walked` would stay green the day a direction
	// disappeared. Naming the witness makes the loss of either one a
	// failure that says which.
	holds := func(set []string, name string) bool {
		for _, s := range set {
			if s == name {
				return true
			}
		}
		return false
	}
	if !(holds(walked, "ignored-by-gitignore.go") && !holds(got, "ignored-by-gitignore.go")) {
		t.Error("ignored-by-gitignore.go no longer witnesses the walk-sees-more " +
			"direction. This is the direction that is ABOUT GIT: .gitignore covers " +
			"the file, the enumeration honours that and a walk cannot.")
	}
	if !(holds(got, ".gitignore") && !holds(walked, ".gitignore")) {
		t.Error("`.gitignore` no longer witnesses the enumeration-sees-more " +
			"direction. Note what this direction is NOT about: see the comment " +
			"above this row.")
	}
}

// TestTheEnumerationHonoursGitignoreAndNothingElse asserts the flag
// rather than trusting its name.
//
// .git/info/exclude and the operator's global core.excludesFile are
// properties of a MACHINE. A guard whose scope they can change is a
// guard that passes on one laptop because of something nobody else can
// see — and the pack matcher's brief already established that
// configuring the neutralisation is not enough, it has to be asserted.
//
// REQUIRED MUTATION, RUN 2026-09-11: restore --exclude-standard. This
// row reds, naming the file that vanished from the set; nothing else in
// the package moves, because on this repository the two flag forms
// return identical sets — which is exactly why the row cannot be left to
// the real tree.
func TestTheEnumerationHonoursGitignoreAndNothingElse(t *testing.T) {
	root := hermeticRepo(t)

	info := filepath.Join(root, ".git", "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatalf("making .git/info: %v", err)
	}
	// THE FIXTURE MUST BE UNTRACKED, and this row asserted a TRACKED one
	// for its first draft — a thing no exclude rule can remove, because
	// --exclude-standard filters --others and a tracked path arrives
	// through --cached. The assertion could not have failed under any
	// flag. The mutation below is what found it: restoring
	// --exclude-standard left the row green, which is a row measuring
	// nothing rather than a rule holding.
	if err := os.WriteFile(filepath.Join(info, "exclude"),
		[]byte("untracked-but-ours.go\n"), 0o644); err != nil {
		t.Fatalf("writing .git/info/exclude: %v", err)
	}

	got := relNames(root, repoFiles(t, root, ""))
	var found bool
	for _, name := range got {
		if name == "untracked-but-ours.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("untracked-but-ours.go left the set once .git/info/exclude named "+
			"it, so this enumeration reads an ignore source that belongs to a "+
			"MACHINE rather "+
			"than to the repository.\nSet: %v\nA guard whose scope a personal "+
			"exclude file can change passes on one laptop for a reason nobody "+
			"else can see. The flag is --exclude-per-directory=%s precisely so "+
			"that cannot happen.", got, gitignoreFile)
	}
}

// TestTheEnumerationIgnoresTheOperatorsGitEnvironment asserts that
// cmd.Dir is the ONLY thing deciding which repository gets read.
//
// Added 2026-09-11, after a cold read asked whether the fixture
// was hermetic and the answer, measured, was no. Two variables were
// enough, and neither requires anything unusual on the machine:
//
//   - GIT_INDEX_FILE pointing at a populated index put a path into
//     `git ls-files --cached` that did not exist in the repository being
//     measured. The Lstat filter in repoFiles dropped it, so every row
//     stayed green: the leak was MASKED, which is worse than a leak that
//     shows, because a masked one leaves nothing to find.
//   - GIT_DIR made `git init` in a fresh directory produce something
//     other than the repository under test, and the enumeration then
//     reported the other repository's contents.
//
// The second reaches the REAL guards and not only this fixture, because
// repoFiles is what every guard in this package is built on: an operator
// with GIT_DIR exported — a hook, a wrapper, a script running this suite
// from inside another git operation — would have had the whole package
// asserting invariants about somebody else's tree.
//
// REQUIRED MUTATION: replace gitSafeEnv() with os.Environ() in either
// repoFiles or hermeticRepo. This row reds; the others do not, which is
// the point of it existing separately.
func TestTheEnumerationIgnoresTheOperatorsGitEnvironment(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is not on PATH: %v", err)
	}

	// A repository that is not the one under test, holding a file whose
	// name could not come from the fixture. It is committed AND left on
	// disk, so that a leak cannot be quietly absorbed by the Lstat
	// filter the way GIT_INDEX_FILE's was.
	foreign := t.TempDir()
	foreignHome := filepath.Join(foreign, "home")
	if err := os.MkdirAll(foreignHome, 0o755); err != nil {
		t.Fatalf("making the foreign home: %v", err)
	}
	foreignEnv := gitSafeEnv(
		"HOME="+foreignHome,
		"USERPROFILE="+foreignHome,
		"GIT_CONFIG_GLOBAL="+filepath.Join(foreign, "no-such-global"),
		"GIT_CONFIG_SYSTEM="+filepath.Join(foreign, "no-such-system"),
		"GIT_AUTHOR_NAME=other", "GIT_AUTHOR_EMAIL=other@example.invalid",
		"GIT_COMMITTER_NAME=other", "GIT_COMMITTER_EMAIL=other@example.invalid",
	)
	runForeign := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = foreign
		cmd.Env = foreignEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s in the foreign repo: %v\n%s",
				strings.Join(args, " "), err, out)
		}
	}
	runForeign("init", "-q", "--template=", ".")
	if err := os.WriteFile(filepath.Join(foreign, "somebody-elses.go"),
		[]byte("package other\n"), 0o644); err != nil {
		t.Fatalf("writing the foreign fixture: %v", err)
	}
	runForeign("add", "somebody-elses.go")
	runForeign("commit", "-qm", "not ours")

	// Now hand the suite the environment an unlucky operator would have.
	t.Setenv("GIT_DIR", filepath.Join(foreign, ".git"))
	t.Setenv("GIT_WORK_TREE", foreign)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(foreign, ".git", "index"))

	root := hermeticRepo(t)
	got := relNames(root, repoFiles(t, root, ""))

	want := []string{".gitignore", "tracked.go", "untracked-but-ours.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("with GIT_DIR, GIT_WORK_TREE and GIT_INDEX_FILE exported, the "+
			"enumeration returned %v, want %v.\nThe repository a guard reads must "+
			"be decided by cmd.Dir and by nothing an operator carries in their "+
			"environment — otherwise every invariant in this package is an "+
			"invariant about whichever tree git happened to open.", got, want)
	}
	for _, name := range got {
		if name == "somebody-elses.go" {
			t.Errorf("%s reached the enumeration, so it read the foreign "+
				"repository rather than the one under test", name)
		}
	}
}

// TestTheVendorExceptionsReasonSeesWhatTheFilterRemoves asserts that the
// exception's reason-check and the filter it justifies ask git the SAME
// question.
//
// Added 2026-09-11 with the fix it proves. The reason-check ran a bare
// `git ls-files -- vendor`, which is the TRACKED set, while
// excludedByReason removes paths from an enumeration built with
// --cached AND --others. So an untracked, unignored vendor/ tree — what
// `go mod vendor` leaves behind, and nothing in this repository's
// .gitignore covers it — was being removed from every guard's scope
// while the check written to keep the exception honest reported that the
// exception excluded nothing at all.
//
// That is this file's own subject turned on itself. The exception list
// exists because "a guard's exception outliving its reason is how a
// scope quietly stops meaning what it says", and the mechanism here is
// one step subtler: the reason had not stopped being true, it had
// stopped being CHECKED over the whole set it governs. A reason-check
// with a narrower field of view than the filter it justifies will report
// "inert" about a filter that is acting — and it will do it in the
// reassuring direction, which is why nothing was going to notice.
//
// REQUIRED MUTATION: put the reason-check's flags back to a bare
// `git ls-files -z -- vendor`. This row reds; nothing else does.
func TestTheVendorExceptionsReasonSeesWhatTheFilterRemoves(t *testing.T) {
	root := hermeticRepo(t)

	dir := filepath.Join(root, "vendor", "example.com", "dep")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("making the vendor fixture: %v", err)
	}
	// UNTRACKED AND UNIGNORED, which is the whole case: git's default
	// ls-files cannot see it and --others can.
	if err := os.WriteFile(filepath.Join(dir, "dep.go"),
		[]byte("package dep\n"), 0o644); err != nil {
		t.Fatalf("writing the vendor fixture: %v", err)
	}

	// First half: the filter really does remove it.
	for _, name := range relNames(root, repoFiles(t, root, "")) {
		if strings.HasPrefix(name, "vendor/") {
			t.Fatalf("the enumeration returned %s, so this row's premise is gone: "+
				"the vendor exception is no longer removing anything and there is "+
				"nothing for its reason to be checked against", name)
		}
	}

	// Second half: the reason-check SAW that removal.
	var vendorException *repoException
	for i := range repoExceptions {
		if repoExceptions[i].prefix == "vendor" {
			vendorException = &repoExceptions[i]
		}
	}
	if vendorException == nil {
		t.Fatal("there is no vendor exception, so this row is asserting nothing")
	}
	evidence, ok := vendorException.stillTrue(t, root)
	if !ok {
		t.Fatalf("the vendor exception reported its reason broken: %s", evidence)
	}
	if evidence != "" {
		t.Errorf("with an untracked vendor/ tree present and REMOVED by the "+
			"exception, its reason-check reported %q.\nThe check and the filter "+
			"are looking at different sets: the filter runs over --cached and "+
			"--others, the check asked about only one of them. An exception that "+
			"reports itself inert while it is removing files is the failure this "+
			"list was built to prevent, wearing the reassuring face.", evidence)
	}
}
