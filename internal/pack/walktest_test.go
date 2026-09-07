package pack

import (
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/check"
)

// Shared fixture machinery for this package's suite. It lives in its own
// file because several suites use it, and a helper that grows in
// whichever file happened to need it first is how two of them end up
// with near-identical copies.

// entry is one thing to create in a fixture tree. A trailing slash on
// path means "make this directory and put nothing in it".
//
// FIXTURES ARE BUILT FROM A SLICE, NEVER A MAP, and that is the point of
// the type. Go randomises map iteration, so a map-built fixture is
// created in a different order on every run — which sounds like a free
// determinism test and is the opposite of one: it makes a failure
// unreproducible. Creation order is chosen deliberately here, and the
// determinism rows vary it deliberately.
type entry struct {
	path string
	body string
}

func writeTree(t *testing.T, entries []entry) string {
	t.Helper()
	return writeTreeUnder(t, t.TempDir(), entries)
}

func writeTreeUnder(t *testing.T, root string, entries []entry) string {
	t.Helper()
	for _, e := range entries {
		full := filepath.Join(root, filepath.FromSlash(e.path))
		if strings.HasSuffix(e.path, "/") {
			if err := os.MkdirAll(full, 0o755); err != nil {
				t.Fatalf("creating fixture directory %s: %v", e.path, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("creating fixture directory for %s: %v", e.path, err)
		}
		if err := os.WriteFile(full, []byte(e.body), 0o644); err != nil {
			t.Fatalf("creating fixture file %s: %v", e.path, err)
		}
	}
	return root
}

// pathsOf is the file list as a comparable slice of names. Size and mode
// are asserted where they are the subject; everywhere else the question
// is which files came back.
func pathsOf(files []File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// findingsFor selects one check's findings out of a walk's answer.
func findingsFor(res check.Results, id string) []check.Finding {
	var out []check.Finding
	for _, f := range res.Findings {
		if f.CheckID == id {
			out = append(out, f)
		}
	}
	return out
}

// idsOf is the ordered list of check ids that produced findings, WITH
// REPEATS, so a row can assert how many findings each check contributed
// as well as which checks fired.
func idsOf(res check.Results) []string {
	out := make([]string, 0, len(res.Findings))
	for _, f := range res.Findings {
		out = append(out, f.CheckID)
	}
	return out
}

func mustWalk(t *testing.T, fsys FS, root string) Tree {
	t.Helper()
	tree, err := Walk(fsys, root)
	if err != nil {
		t.Fatalf("Walk(%s): %v", root, err)
	}
	return tree
}

// countingFS counts CONTENT OPENS and remembers what was opened.
//
// It is the instrument behind two claims that cannot be asserted on
// trust: that the walk reads names rather than bytes, and that the only
// bytes it does read belong to an ignore file. A wrapper that counted
// nothing would pass both, which is why one row makes it count something
// on purpose before any zero from it is believed.
type countingFS struct {
	FS
	opened []string
}

func counted() *countingFS { return &countingFS{FS: OSFileSystem{}} }

func (c *countingFS) Open(name string) (io.ReadCloser, error) {
	c.opened = append(c.opened, filepath.ToSlash(name))
	return c.FS.Open(name)
}

// relativeOpens reports what was opened as paths relative to root and
// slash-separated, so a row can name the file it expects rather than a
// temporary directory nobody can predict.
func (c *countingFS) relativeOpens(root string) []string {
	prefix := filepath.ToSlash(root) + "/"
	out := make([]string, 0, len(c.opened))
	for _, p := range c.opened {
		out = append(out, strings.TrimPrefix(p, prefix))
	}
	sort.Strings(out)
	return out
}

// fakeFS serves a tree the test writes out by hand, so that entry KINDS
// the host filesystem cannot portably hold — a symlink on a Windows
// account without the privilege to make one, a named pipe on Windows at
// all — can still be put in front of the walk on every platform.
//
// THE POINT IS TO SEPARATE THE DETECTOR FROM THE FIXTURE. The walk's
// classification of an entry is a pure function of what the directory
// listing said it was, and testing it only where the real filesystem can
// hold the entry means two of the three platforms never test it — and
// then red for a reason that has nothing to do with the classification
// when the fixture fails to build. The rows that use a real symlink or a
// real named pipe are still here; they confirm that the real platform
// produces the entry kind this one asserts on.
type fakeEntry struct {
	name string
	mode fs.FileMode // fs.ModeDir, fs.ModeSymlink, fs.ModeNamedPipe, … or 0
	size int64
	body string
}

type fakeFS struct {
	// dirs is keyed by slash-separated path relative to the walk's root,
	// with "" for the root itself.
	dirs map[string][]fakeEntry
}

func (f fakeFS) key(name string) string {
	return strings.Trim(strings.TrimPrefix(filepath.ToSlash(name), "root"), "/")
}

func (f fakeFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, ok := f.dirs[f.key(name)]
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	out := make([]fs.DirEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, fakeDirEntry{e})
	}
	return out, nil
}

func (f fakeFS) Open(name string) (io.ReadCloser, error) {
	dir, base := path.Split(strings.Trim(filepath.ToSlash(name), "/"))
	for _, e := range f.dirs[f.key(dir)] {
		if e.name == base {
			return io.NopCloser(strings.NewReader(e.body)), nil
		}
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

type fakeDirEntry struct{ e fakeEntry }

func (d fakeDirEntry) Name() string               { return d.e.name }
func (d fakeDirEntry) IsDir() bool                { return d.e.mode.IsDir() }
func (d fakeDirEntry) Type() fs.FileMode          { return d.e.mode.Type() }
func (d fakeDirEntry) Info() (fs.FileInfo, error) { return fakeFileInfo{d.e}, nil }

type fakeFileInfo struct{ e fakeEntry }

func (i fakeFileInfo) Name() string       { return i.e.name }
func (i fakeFileInfo) Size() int64        { return i.e.size }
func (i fakeFileInfo) Mode() fs.FileMode  { return i.e.mode }
func (i fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (i fakeFileInfo) IsDir() bool        { return i.e.mode.IsDir() }
func (i fakeFileInfo) Sys() any           { return nil }

// symlinkSupported reports whether this machine will let the test make a
// symbolic link, by making one rather than by asking about the platform.
//
// A Windows account without the create-symlink privilege refuses, and
// the CI image's answer is not something this repository controls. The
// rows that need a REAL link say so and skip with a reason; the walk's
// handling of a symlink entry is asserted on every platform through
// fakeFS above, so a skip here costs the confirmation and not the
// property.
func symlinkSupported(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, nil, 0o644); err != nil {
		t.Fatalf("writing symlink probe target: %v", err)
	}
	return os.Symlink(target, filepath.Join(dir, "link")) == nil
}

// reversedFS hands back every directory listing backwards.
//
// It is how the determinism claim is tested WITHOUT depending on the
// order a filesystem happens to return entries in. Asserting that two
// walks over two identically-built trees agree proves nothing if the
// platform sorts its listings — which the standard library's reader
// does. This wrapper makes the input order genuinely different on every
// platform, which is the condition the claim is about.
type reversedFS struct{ FS }

func (r reversedFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := r.FS.ReadDir(name)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries, nil
}
