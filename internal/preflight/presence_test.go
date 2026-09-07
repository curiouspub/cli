package preflight

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// syscallNotDir stands for the error a Unix host returns for a path
// whose parent is a regular file. Only its SHAPE matters to the
// classifier — that it is not a not-exist error — so a plain sentinel
// says what the row means without pinning an errno, which would make the
// row an assertion about the operating system rather than about this
// code.
var syscallNotDir = errors.New("not a directory")

// stubFS answers from a table instead of from a disk, which is what lets
// the rows below assert a platform's error shape on every platform.
//
// THAT SEAM IS THE POINT. The defect being fixed is that one operating
// system reports a path whose parent is a FILE as "not a directory" and
// another reports it as "does not exist" — so the interesting input is
// an error shape, and an error shape is exactly the thing a test cannot
// portably arrange by writing files. Driving the classifier from a table
// means the Windows-shaped case is asserted on the Linux and macOS legs
// too, rather than being a claim only one runner could ever check.
type stubFS struct {
	dirs map[string]bool  // path -> is a directory
	errs map[string]error // path -> the error a stat returns
}

type stubInfo struct {
	name  string
	isDir bool
}

func (s stubInfo) Name() string { return s.name }
func (s stubInfo) Size() int64  { return 0 }
func (s stubInfo) Mode() fs.FileMode {
	return map[bool]fs.FileMode{true: fs.ModeDir, false: 0}[s.isDir]
}
func (s stubInfo) ModTime() time.Time { return time.Time{} }
func (s stubInfo) IsDir() bool        { return s.isDir }
func (s stubInfo) Sys() any           { return nil }

func (s stubFS) Stat(name string) (fs.FileInfo, error) {
	if err, ok := s.errs[name]; ok {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	if isDir, ok := s.dirs[name]; ok {
		return stubInfo{name: filepath.Base(name), isDir: isDir}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

func (s stubFS) Lstat(name string) (fs.FileInfo, error) { return s.Stat(name) }

func (s stubFS) Open(string) (io.ReadCloser, error) {
	return nil, &fs.PathError{Op: "open", Path: "", Err: fs.ErrInvalid}
}

// TestPresenceOfAPathUnderAFileIsUndeterminedOnEveryPlatform is the row
// the platform split was hiding. One project tree got two different
// sentences depending on the operating system that read it: where a path
// component is a regular file, Unix reports "not a directory" and the
// classifier answered UNDETERMINED, while Windows reports the same
// situation as "does not exist" and it answered ABSENT — a confident
// claim that nothing is there, about a question that cannot be asked.
//
// Both error shapes are driven here, so both are asserted on all three
// legs.
//
// MUTATION: map a not-exist error straight to absent again. The
// windows-shaped row reds; the unix-shaped one does not move, which is
// what makes the pair worth having.
func TestPresenceOfAPathUnderAFileIsUndeterminedOnEveryPlatform(t *testing.T) {
	root := filepath.Join("proj")
	src := filepath.Join(root, "src")
	pages := filepath.Join(src, "pages")

	cases := []struct {
		name string
		fsys stubFS
	}{
		{
			// What Unix reports: the stat fails with something that is
			// not a not-exist error.
			name: "the host says not a directory",
			fsys: stubFS{
				dirs: map[string]bool{root: true, src: false},
				errs: map[string]error{pages: syscallNotDir},
			},
		},
		{
			// What Windows reports: the very same tree, described as a
			// path that does not exist.
			name: "the host says the path does not exist",
			fsys: stubFS{
				dirs: map[string]bool{root: true, src: false},
				errs: map[string]error{pages: fs.ErrNotExist},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDir(tc.fsys, pages); got != undetermined {
				t.Errorf("isDir = %v, want undetermined — src is a file, so whether "+
					"src/pages exists is not a question with an answer", got)
			}
			if got := isFile(tc.fsys, filepath.Join(src, "astro.config.mjs")); got != undetermined {
				t.Errorf("isFile = %v, want undetermined", got)
			}
		})
	}
}

// TestPresenceStillCallsAGenuinelyMissingPathAbsent is the floor beside
// the row above, and without it the fix could be "answer undetermined to
// everything" — which would turn every ordinary project with no pages
// directory into a message about being unable to look.
func TestPresenceStillCallsAGenuinelyMissingPathAbsent(t *testing.T) {
	root := filepath.Join("proj")

	cases := []struct {
		name string
		path string
		fsys stubFS
	}{
		{
			name: "missing from a directory that exists",
			path: filepath.Join(root, "src"),
			fsys: stubFS{dirs: map[string]bool{root: true}},
		},
		{
			// Nothing on the way down exists, which still settles the
			// question: if proj/src is not there, proj/src/pages is not
			// there either.
			name: "missing several levels down",
			path: filepath.Join(root, "src", "pages"),
			fsys: stubFS{dirs: map[string]bool{root: true}},
		},
		{
			name: "nothing exists at all, up to the top",
			path: filepath.Join("nowhere", "src", "pages"),
			fsys: stubFS{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDir(tc.fsys, tc.path); got != absent {
				t.Errorf("isDir(%s) = %v, want absent", tc.path, got)
			}
		})
	}
}

// TestPresenceOnTheRealFilesystemAgreesWithTheTable runs the same tree
// through the host rather than through a table.
//
// WHAT EACH LEG OBSERVES, because a green tick is not evidence about a
// branch the leg never reached: on Linux and macOS the host reports "not
// a directory" and the pre-existing branch produces the answer; on
// Windows the host reports the path as not existing and the new
// parent-confirming branch produces it. The assertion is the same on all
// three, which is the property being bought — one tree, one answer.
func TestPresenceOnTheRealFilesystemAgreesWithTheTable(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	if got := isDir(OSFileSystem{}, filepath.Join(root, "src", "pages")); got != undetermined {
		t.Errorf("isDir = %v, want undetermined", got)
	}
	if got := isDir(OSFileSystem{}, filepath.Join(root, "nothing-here")); got != absent {
		t.Errorf("isDir on a genuinely missing directory = %v, want absent", got)
	}
}

// TestCheckAstroConfigNamesTheFileItFound. "Couldn't check whether
// src/pages exists" is a claim about this program's inability, and it
// was made about a tree this program read perfectly well: src is a file.
// The reader can act on that sentence and cannot act on the other one.
//
// MUTATION: fall back to the generic wording. Reds here.
// MUST NOT MOVE: the ordinary no-pages-directory message.
func TestCheckAstroConfigNamesTheFileItFound(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	findings := CheckAstroConfig(OSFileSystem{}, root)
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want one", findings)
	}
	message := findings[0].Message

	if strings.Contains(message, "Couldn't check whether") {
		t.Errorf("the message claims this program could not look, at a tree it read:\n%s", message)
	}
	if !strings.Contains(message, "src") || !strings.Contains(message, "not a directory") {
		t.Errorf("the message does not say what was actually found:\n%s", message)
	}
}
