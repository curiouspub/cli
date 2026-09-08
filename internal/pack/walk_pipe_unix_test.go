//go:build unix

package pack

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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

// TestPackRefusesANamedPipeBeforeOpeningIt is the third hostile input,
// and the only one whose failure mode is a HANG rather than a wrong
// archive.
//
// Opening a FIFO blocks until somebody writes to it. A packer that
// checked the entry's kind after opening the file — or not at all —
// would stop a deploy on a file the user forgot was in their project,
// with no output and nothing to read. So the refusal is measured with a
// DEADLINE OF ITS OWN: this row cannot express "did not hang" by
// returning, only by returning in time.
//
// REQUIRED MUTATION, RUN: delete the IsRegular check from writeEntry.
// This row does not fail — it HANGS, and the deadline below is what
// turns that into a red rather than a suite that never finishes.
func TestPackRefusesANamedPipeBeforeOpeningIt(t *testing.T) {
	root, _ := namedPipeFixture(t)

	done := make(chan error, 1)
	go func() {
		_, err := Pack(OSFileSystem{}, root, t.TempDir(), []File{
			{Path: "pipe", Size: 0, Mode: os.ModeNamedPipe | 0o644},
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Pack accepted a named pipe")
		}
		if !strings.Contains(err.Error(), "pipe") {
			t.Errorf("error = %q, want it to name the entry", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Pack did not return within five seconds — it opened the pipe and " +
			"is waiting for a writer, which is a deploy stopped with nothing to read")
	}
}
