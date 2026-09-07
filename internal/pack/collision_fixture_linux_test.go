//go:build linux

package pack

import (
	"os"
	"path/filepath"
	"testing"
)

// THIS FILE IS TAGGED FOR ONE PLATFORM AND THE TAG IS THE POINT.
//
// The state it builds — two files in one directory whose names differ
// only in case — is a state the other two filesystems in the matrix
// refuse to hold. Measured: writing README.md and then readme.md into
// one directory on a case-insensitive volume leaves ONE file, named
// README.md, holding the second write's bytes. The default filesystem on
// two of the three platforms this ships to behaves that way.
//
// So a row that built this fixture untagged would red on those legs for
// a reason that has nothing to do with the code it is aimed at, and a
// reader meeting that red would conclude the detector was broken. The
// DETECTOR is a pure function of a path list and is driven from an
// injected one on every platform, in findings_test.go. What is tagged
// here is only the part that needs a filesystem willing to hold the
// state.
//
// A runtime check follows the writes anyway. The tag says which platform
// usually behaves this way; it cannot say anything about the particular
// volume the test happens to be running on, and a case-folding mount is
// available on this platform too.

// addCaseCollision writes two files whose names differ only in case and
// reports whether the volume actually kept both.
func addCaseCollision(t *testing.T, root string) bool {
	t.Helper()
	upper := filepath.Join(root, "README.md")
	lower := filepath.Join(root, "readme.md")
	if err := os.WriteFile(upper, []byte("upper"), 0o644); err != nil {
		t.Fatalf("writing the collision fixture: %v", err)
	}
	if err := os.WriteFile(lower, []byte("lower"), 0o644); err != nil {
		t.Fatalf("writing the collision fixture: %v", err)
	}

	first, err := os.ReadFile(upper)
	if err != nil {
		t.Fatalf("reading back the collision fixture: %v", err)
	}
	if string(first) != "upper" {
		// The volume folded the two names together. Undo the fixture so
		// the tree is what the caller asked for, and say the state could
		// not be built.
		if err := os.Remove(upper); err != nil {
			t.Fatalf("removing the folded collision fixture: %v", err)
		}
		return false
	}
	return true
}

// TestWalkDetectsARealOnDiskCollision is the confirmation half: the
// injected-list rows assert what the detector does with two colliding
// names, and this one asserts that a real filesystem can hand it two.
func TestWalkDetectsARealOnDiskCollision(t *testing.T) {
	root := writeTree(t, []entry{{path: "index.html", body: "<html>"}})
	if !addCaseCollision(t, root) {
		t.Skip("this volume folds names that differ only in case, so the fixture the row " +
			"needs cannot exist on it")
	}

	tree := mustWalk(t, OSFileSystem{}, root)
	if got := len(tree.Files); got != 3 {
		t.Fatalf("files = %v, want three — both spellings survived the write", pathsOf(tree.Files))
	}

	found := findingsFor(tree.Results, "case-collision")
	if len(found) != 1 {
		t.Fatalf("collision findings = %v, want one", found)
	}
	want := []string{"README.md", "readme.md"}
	if len(found[0].Paths) != 2 || found[0].Paths[0] != want[0] || found[0].Paths[1] != want[1] {
		t.Errorf("Paths = %v, want %v", found[0].Paths, want)
	}
}
