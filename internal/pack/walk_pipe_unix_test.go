//go:build unix

package pack

import (
	"path/filepath"
	"syscall"
	"testing"
)

// namedPipeFixture builds a tree holding one regular file and one real
// named pipe, and returns the root.
//
// NOTHING EVER OPENS THE WRITE END, which is what makes the fixture do
// two jobs at once. A walk that classified the pipe as a regular file
// would list it; a walk that tried to read what it found would block
// forever rather than fail, and the row would time out instead of
// reporting.
func namedPipeFixture(t *testing.T) (string, bool) {
	t.Helper()
	root := writeTree(t, []entry{{path: "index.html", body: "<html>"}})
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatalf("creating the named-pipe fixture: %v", err)
	}
	return root, true
}

// ignoreFilePipeFixture builds a tree whose ignore file is a real named
// pipe with nobody writing to it. A walk that read what it found would
// block here rather than fail, so the row that uses it is testing for a
// hang as much as for a verdict.
func ignoreFilePipeFixture(t *testing.T) (string, bool) {
	t.Helper()
	root := writeTree(t, []entry{{path: "page.astro", body: "---"}})
	if err := syscall.Mkfifo(filepath.Join(root, gitignoreName), 0o600); err != nil {
		t.Fatalf("creating the named-pipe fixture: %v", err)
	}
	return root, true
}
