package pack

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

// THE MATCHER IS CHECKED AGAINST GIT, NOT AGAINST OUR READING OF GIT.
//
// Partial pattern support is precisely how a file its author excluded
// gets uploaded and published, and the failure is silent: every row we
// thought to write passes, and the one construct nobody thought of is
// the one in somebody's real project. So the verdicts come from the real
// program, over every path in a fixture built to exercise the whole
// documented syntax.
//
// The comparison is over FILES. A directory's verdict is observable
// through the files under it — a directory rule we fail to apply leaves
// its files in the walk's output, and one we apply too eagerly takes
// them out — so every directory in the fixture holds at least one file,
// and a directory-level disagreement surfaces as a file-level one.

// gitCommand finds git, or explains why this row cannot run.
//
// CI has git — the checkout that puts this repository on the runner is
// git — so the skip cannot quietly become the normal outcome there.
func gitCommand(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git is not on PATH, so there is nothing to compare against: %v", err)
	}
	return path
}

// hermeticGit is the environment a differential run needs, and every
// entry in it is load-bearing.
//
// The environment being neutralised is the DEVELOPER'S. Left alone, git
// consults a machine-wide ignore file and a user-level configuration —
// exactly the two sources this matcher deliberately does not read — so
// on a machine that has either, the comparison either fails or passes
// for the wrong reason, and what it is measuring is the laptop rather
// than the matcher.
//
// THE RECIPE IS SPELLED PORTABLY, and the obvious spelling is not. A
// null device is not a path on Windows, and git there resolves the
// user's home from the profile variables rather than from HOME — so the
// Unix spelling neutralises nothing on the one platform nobody develops
// on, and the leg that neutralises nothing is then the leg that reports
// green. A missing configuration file is treated by git as an empty one,
// so a path that does not exist needs no platform branch, and all four
// home variables are set rather than the one this platform happens to
// use.
//
// The environment is BUILT rather than inherited. Anything not named
// here — a per-user configuration directory variable among them — is
// absent from the child, which is the point.
func hermeticGit(home, missingConfig string) []string {
	env := []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"HOMEDRIVE=" + filepath.VolumeName(home),
		"HOMEPATH=" + strings.TrimPrefix(home, filepath.VolumeName(home)),
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	}
	if missingConfig != "" {
		env = append(env,
			"GIT_CONFIG_GLOBAL="+missingConfig,
			"GIT_CONFIG_SYSTEM="+missingConfig,
		)
	}
	// Enough of the host environment for git to start at all: where its
	// own executable lives, and on Windows where the system does.
	for _, name := range []string{
		"PATH", "PATHEXT", "SystemRoot", "SYSTEMROOT", "ComSpec", "COMSPEC",
		"TEMP", "TMP", "TMPDIR", "WINDIR",
	} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// initRepo makes the fixture a git repository with NO template, so
// nothing arrives in it that a template would have brought — the
// repository-local exclude file included.
func initRepo(t *testing.T, git, repo string, env []string) {
	t.Helper()
	template := filepath.Join(t.TempDir(), "empty-template")
	if err := os.MkdirAll(template, 0o755); err != nil {
		t.Fatalf("creating the empty template directory: %v", err)
	}

	cmd := exec.Command(git, "init", "--quiet", "--template="+template, repo)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(repo, ".git", "info", "exclude")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the empty template still produced a repository-local exclude file (%v), "+
			"so this comparison would be reading a source the matcher deliberately ignores", err)
	}
}

// checkIgnore asks git for a verdict on every path, and returns one.
//
// --non-matching is what makes "every path" true: without it a path that
// matches nothing produces no output at all, and a missing line is
// indistinguishable from a path the caller forgot to send. The NUL
// framing is what makes a fixture containing a space in a filename safe
// to send.
func checkIgnore(t *testing.T, git, repo string, env []string, paths []string) map[string]bool {
	t.Helper()

	cmd := exec.Command(git, "-C", repo, "check-ignore",
		"-z", "--verbose", "--non-matching", "--no-index", "--stdin")
	cmd.Env = env
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Exit 1 means "none of these paths is ignored", which is an answer
	// rather than a failure. Anything above that is git refusing.
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("git check-ignore: %v\n%s", err, stderr.String())
		}
	}

	fields := strings.Split(stdout.String(), "\x00")
	if len(fields) > 0 && fields[len(fields)-1] == "" {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%4 != 0 {
		t.Fatalf("git produced %d NUL-separated fields, which is not a whole number of "+
			"source/line/pattern/path records: %q", len(fields), stdout.String())
	}

	out := make(map[string]bool, len(paths))
	for i := 0; i < len(fields); i += 4 {
		pattern, path := fields[i+2], fields[i+3]
		// An empty pattern means nothing matched. A pattern that begins
		// with the negation mark matched and un-ignored the path, which
		// is the case a verdict read as "did anything match?" gets
		// exactly backwards.
		out[filepath.ToSlash(path)] = pattern != "" && !strings.HasPrefix(pattern, "!")
	}

	for _, p := range paths {
		if _, ok := out[p]; !ok {
			t.Fatalf("git returned no verdict for %q", p)
		}
	}
	return out
}

// differentialFixture exercises every construct the matcher claims to
// support, and every directory in it holds at least one file so that a
// directory rule has something observable under it.
//
// A file whose name carries trailing spaces is deliberately absent:
// Windows strips them, so the fixture would differ between platforms.
// That construct is asserted from a string in the syntax table instead,
// where no filesystem has an opinion.
func differentialFixture() []entry {
	const rootIgnore = "# a comment, which is not a pattern\n" +
		"\\#literal\n" +
		"*.log\n" +
		"!important.log\n" +
		"/root-only.txt\n" +
		"build/\n" +
		"**/temp\n" +
		"docs/**/*.pdf\n" +
		"a?c.txt\n" +
		"[abc].txt\n" +
		"[!xyz]-class.txt\n" +
		"[[:digit:]]-digit.txt\n" +
		"trailing   \n" +
		"space\\ kept\n" +
		"star/**\n" +
		"src/anchored.txt\n"

	const nestedIgnore = "!*.log\n" +
		"/localonly.txt\n"

	return []entry{
		{path: ".gitignore", body: rootIgnore},
		{path: "index.html", body: "<html>"},
		{path: "#literal", body: "x"},
		{path: "x.log", body: "x"},
		{path: "important.log", body: "x"},
		{path: "root-only.txt", body: "x"},
		{path: "sub/root-only.txt", body: "x"},
		{path: "sub/x.log", body: "x"},
		{path: "build/out.html", body: "x"},
		{path: "build/nested/deep.html", body: "x"},
		{path: "a/temp/file.txt", body: "x"},
		{path: "b/c/temp/file.txt", body: "x"},
		{path: "b/c/kept.txt", body: "x"},
		{path: "docs/a/b/paper.pdf", body: "x"},
		{path: "docs/paper.pdf", body: "x"},
		{path: "docs/notes.txt", body: "x"},
		{path: "abc.txt", body: "x"},
		{path: "a.txt", body: "x"},
		{path: "d.txt", body: "x"},
		{path: "q-class.txt", body: "x"},
		{path: "x-class.txt", body: "x"},
		{path: "7-digit.txt", body: "x"},
		{path: "z-digit.txt", body: "x"},
		{path: "trailing", body: "x"},
		{path: "space kept", body: "x"},
		{path: "star/inside/f.txt", body: "x"},
		{path: "src/anchored.txt", body: "x"},
		{path: "src/nested/anchored.txt", body: "x"},
		{path: "keep/.gitignore", body: nestedIgnore},
		{path: "keep/a.log", body: "x"},
		{path: "keep/localonly.txt", body: "x"},
		{path: "keep/deeper/localonly.txt", body: "x"},
	}
}

// treeFiles is every file in the fixture, as slash-separated relative
// paths, with the repository's own directory left out.
//
// THE REPOSITORY DIRECTORY IS EXCLUDED BY NECESSITY, not by convenience:
// git does not consider its own directory ignored, and this walk
// excludes it unconditionally, so the two disagree there by design and
// the disagreement says nothing about the matcher.
func treeFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("enumerating the fixture: %v", err)
	}
	sort.Strings(out)
	return out
}

// TestGitDifferential is the row this package's correctness argument
// rests on.
func TestGitDifferential(t *testing.T) {
	git := gitCommand(t)

	tmp := t.TempDir()
	home := filepath.Join(tmp, "empty-home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("creating the empty home directory: %v", err)
	}
	missingConfig := filepath.Join(tmp, "no-such-git-config")
	if _, err := os.Stat(missingConfig); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the configuration path this row points git at exists (%v); it has to "+
			"be absent, because absent is what git treats as empty", err)
	}
	env := hermeticGit(home, missingConfig)

	root := writeTree(t, differentialFixture())
	initRepo(t, git, root, env)

	paths := treeFiles(t, root)
	if len(paths) < 25 {
		t.Fatalf("the fixture offers %d paths to compare, which is too few to be "+
			"exercising the syntax it claims to", len(paths))
	}

	packed := map[string]bool{}
	for _, f := range mustWalk(t, OSFileSystem{}, root).Files {
		packed[f.Path] = true
	}

	theirs := checkIgnore(t, git, root, env, paths)

	var ignored, kept int
	for _, p := range paths {
		ours := !packed[p]
		if theirs[p] {
			ignored++
		} else {
			kept++
		}
		if ours != theirs[p] {
			verb := map[bool]string{true: "ignores", false: "keeps"}
			t.Errorf("%s: this matcher %s it, git %s it", p, verb[ours], verb[theirs[p]])
		}
	}

	// A comparison that only ever saw one verdict would agree with a
	// constant function, which is what a badly broken matcher looks
	// like from the outside.
	if ignored == 0 || kept == 0 {
		t.Errorf("the comparison saw %d ignored and %d kept paths; it needs both, or it "+
			"agrees with a matcher that answers the same thing every time", ignored, kept)
	}
}

// TestGitDifferentialNeutralisesTheDevelopersEnvironment asserts the
// recipe rather than merely applying it.
//
// A hermetic setup that silently stopped being hermetic would not fail —
// it would keep passing, on a comparison that had quietly become a
// measurement of whoever's machine it ran on. So the hazard is PLANTED
// and shown to bite, and then shown not to bite once neutralised.
//
// THE TWO ROUTES ARE NEUTRALISED BY DIFFERENT HALVES OF THE RECIPE, and
// that is the reason both halves are in it. Measured with git 2.50.1:
// a machine-wide ignore file named by the user's configuration is
// silenced by pointing the configuration variables at a path that does
// not exist — and the DEFAULT one, which git looks for under the user's
// own home directory without any configuration naming it, survives that
// completely and is silenced only by moving home. Either half alone
// leaves one route open.
func TestGitDifferentialNeutralisesTheDevelopersEnvironment(t *testing.T) {
	git := gitCommand(t)

	tmp := t.TempDir()
	missingConfig := filepath.Join(tmp, "no-such-git-config")
	emptyHome := filepath.Join(tmp, "empty-home")
	if err := os.MkdirAll(emptyHome, 0o755); err != nil {
		t.Fatalf("creating the empty home: %v", err)
	}

	// Route one: a home directory carrying git's DEFAULT ignore file,
	// which no configuration has to name.
	defaultHome := filepath.Join(tmp, "home-with-default-ignore")
	if err := os.MkdirAll(filepath.Join(defaultHome, ".config", "git"), 0o755); err != nil {
		t.Fatalf("creating the hazard home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(defaultHome, ".config", "git", "ignore"),
		[]byte("secret.txt\n"), 0o644); err != nil {
		t.Fatalf("planting the default ignore file: %v", err)
	}

	// Route two: a home directory whose configuration NAMES an ignore
	// file somewhere else.
	namedHome := filepath.Join(tmp, "home-with-named-ignore")
	namedIgnore := filepath.Join(tmp, "named-excludes")
	if err := os.MkdirAll(namedHome, 0o755); err != nil {
		t.Fatalf("creating the hazard home: %v", err)
	}
	if err := os.WriteFile(namedIgnore, []byte("other-secret.txt\n"), 0o644); err != nil {
		t.Fatalf("planting the named ignore file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(namedHome, ".gitconfig"),
		[]byte("[core]\n\texcludesFile = "+filepath.ToSlash(namedIgnore)+"\n"), 0o644); err != nil {
		t.Fatalf("planting the configuration that names it: %v", err)
	}

	root := writeTree(t, []entry{
		{path: "secret.txt", body: "x"},
		{path: "other-secret.txt", body: "x"},
	})
	initRepo(t, git, root, hermeticGit(emptyHome, missingConfig))
	paths := []string{"secret.txt", "other-secret.txt"}

	tests := []struct {
		name       string
		env        []string
		wantSecret bool
		wantOther  bool
	}{
		{
			name: "the default ignore file under the user's home bites",
			env:  hermeticGit(defaultHome, ""),
			// It is found without any configuration naming it, so the
			// configuration variables cannot be what silences it.
			wantSecret: true,
		},
		{
			name:       "and pointing the configuration elsewhere does NOT silence it",
			env:        hermeticGit(defaultHome, missingConfig),
			wantSecret: true,
		},
		{
			name:       "moving home silences it",
			env:        hermeticGit(emptyHome, missingConfig),
			wantSecret: false,
		},
		{
			name:      "an ignore file the user's configuration names bites",
			env:       hermeticGit(namedHome, ""),
			wantOther: true,
		},
		{
			name:      "and the configuration variables silence that one",
			env:       hermeticGit(namedHome, missingConfig),
			wantOther: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := checkIgnore(t, git, root, tc.env, paths)
			if got["secret.txt"] != tc.wantSecret {
				t.Errorf("secret.txt ignored = %v, want %v", got["secret.txt"], tc.wantSecret)
			}
			if got["other-secret.txt"] != tc.wantOther {
				t.Errorf("other-secret.txt ignored = %v, want %v",
					got["other-secret.txt"], tc.wantOther)
			}
		})
	}
}
