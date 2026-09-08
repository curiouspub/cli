package preflight

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/check"
)

// projectFixture names a committed project tree at the REPOSITORY root
// rather than in this package's own testdata directory.
//
// THE PATH IS DELIBERATE AND IT COSTS ONE LINE. A project fixture is not
// this package's private data: the manual legs of other work run the
// built binary against the same trees from the repository root, and a Go
// test's working directory is its own package directory — so the same
// written path would otherwise name two different places and neither
// would exist. One root directory, reached from here by climbing out.
//
// It fails rather than skips when the tree is not there. A fixture the
// suite cannot find is a broken checkout, and a row that quietly stopped
// running over a tree that vanished is the failure this repository's
// skip gate exists to make visible.
func projectFixture(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "projects", name)
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("project fixture %s: %v", name, err)
	}
	if !info.IsDir() {
		t.Fatalf("project fixture %s is not a directory", name)
	}
	return root
}

// findingsFor returns the findings one check id produced, in the order
// they arrived.
func findingsFor(res Result, id string) []check.Finding {
	var out []check.Finding
	for _, f := range res.Findings {
		if f.CheckID == id {
			out = append(out, f)
		}
	}
	return out
}

// soleFinding returns the one finding a check produced, failing loudly
// when there is not exactly one.
//
// It reports the whole slice on failure rather than only its length,
// because the interesting failures here are a check emitting a SECOND
// finding nobody expected — and a count alone leaves the reader running
// the row again with a print statement in it.
func soleFinding(t *testing.T, res Result, id string) check.Finding {
	t.Helper()
	found := findingsFor(res, id)
	if len(found) != 1 {
		t.Fatalf("%s produced %d findings, want exactly 1: %+v", id, len(found), found)
	}
	return found[0]
}

// wholeText is everything a finding puts in front of a person: its
// summary and all three parts of its copy.
//
// ASSERTIONS ABOUT ABSENCE USE THIS RATHER THAN ONE FIELD, and that is
// the point of it existing. A row proving that some wrong advice is gone
// has to look everywhere the advice could be, or the words simply move
// one field over and the row keeps passing.
func wholeText(f check.Finding) string {
	return strings.Join([]string{f.Message, f.What, f.Why, f.Next}, "\n")
}

// refusingFS is the real filesystem with named paths made unreadable.
//
// IT EXISTS BECAUSE THE INTERESTING INPUT IS AN ERROR SHAPE. A row that
// arranged a real unreadable file would take the read permission away,
// which two supported environments legitimately refuse to honour — a
// process running as the superuser ignores the bits, and Windows does
// not gate reads by them at all — so the row would skip on exactly the
// machines where it is most likely to matter. Injecting the refusal
// instead asserts the behaviour on all three legs.
type refusingFS struct {
	refuse map[string]bool
}

func (r refusingFS) refused(name string) error {
	if r.refuse[filepath.Clean(name)] {
		return &fs.PathError{Op: "stat", Path: name, Err: fs.ErrPermission}
	}
	return nil
}

func (r refusingFS) Stat(name string) (fs.FileInfo, error) {
	if err := r.refused(name); err != nil {
		return nil, err
	}
	return os.Stat(name)
}

func (r refusingFS) Lstat(name string) (fs.FileInfo, error) {
	if err := r.refused(name); err != nil {
		return nil, err
	}
	return os.Lstat(name)
}

func (r refusingFS) Open(name string) (io.ReadCloser, error) {
	if err := r.refused(name); err != nil {
		return nil, err
	}
	return os.Open(name)
}

// treeSnapshot records every path under root with its size and
// modification time, which is what a read-only claim is checked against.
func treeSnapshot(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, strings.Join([]string{
			path,
			info.Mode().String(),
			info.ModTime().UTC().Format("2006-01-02T15:04:05.000000000"),
			fs.FormatFileInfo(info),
		}, "|"))
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotting %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}
