//go:build unix

package pack

import (
	"os"
	"path/filepath"
	"reflect"
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

// TestTheCompressionDiagnosticDoesNotBlockOnAPipe is the half of the
// same refusal whose failure is a HANG rather than a wrong answer.
//
// Opening a FIFO blocks until somebody writes to it. The diagnostic's own
// error guard cannot help: a blocked open never returns an error to skip
// on. So a pipe left in a project would stop a deploy on the one path
// that had already failed, with no output and nothing to read.
//
// REQUIRED MUTATION: drop the packable guard from compressedSize. This
// row does not fail, it HANGS, and the deadline below is what turns that
// into a red rather than a suite that never finishes.
func TestTheCompressionDiagnosticDoesNotBlockOnAPipe(t *testing.T) {
	root, _ := namedPipeFixture(t)

	type outcome struct {
		measured []compressed
		left     []skipped
	}
	run := func(t *testing.T, pipeSize int64) outcome {
		t.Helper()
		done := make(chan outcome, 1)
		go func() {
			m, l := leastCompressible(OSFileSystem{}, root, []File{
				{Path: "index.html", Size: 6, Mode: 0o644},
				{Path: "pipe", Size: pipeSize, Mode: os.ModeNamedPipe | 0o644},
			})
			done <- outcome{measured: m, left: l}
		}()
		select {
		case got := <-done:
			return got
		case <-time.After(5 * time.Second):
			t.Fatal("the diagnostic did not return within five seconds — it opened " +
				"the pipe and is waiting for a writer, which is a deploy stopped " +
				"with nothing to read")
			return outcome{}
		}
	}

	// SIZE ZERO IS THE FIXTURE THAT ONCE HID THE DEFECT. The diagnostic
	// skipped empty files first, so a pipe recorded as zero bytes never
	// reached the open and this row was satisfied by the empty-file rule
	// while the mode refusal was absent — a negative row passing on an
	// earlier, unrelated guard. The refusal now comes first and the row
	// asserts WHICH one fired, so the fixture that hid the defect is the
	// one that proves the fix.
	//
	// REQUIRED MUTATIONS, BOTH RUN. Delete the packable branch from
	// leastCompressible: reds here, naming the empty-file skip. Move it
	// back BELOW the size check — the exact original ordering — and it
	// reds here too, which is the mutation the old row could not have.
	t.Run("a pipe is refused for being a pipe, not passed over for being empty", func(t *testing.T) {
		got := run(t, 0)
		if len(got.left) != 1 || got.left[0].file.Path != "pipe" {
			t.Fatalf("skipped = %v, want the pipe alone", got.left)
		}
		if got.left[0].reason != skippedUnpackable {
			t.Errorf("the pipe was skipped for reason %d, want the packer's own "+
				"refusal — skipped as an empty file is the right answer for the "+
				"wrong reason, and it stops being right the moment somebody "+
				"records a size", got.left[0].reason)
		}
	})

	// AND THE HARM ITSELF, which the row above no longer demonstrates:
	// with a size recorded, nothing but the refusal stands between the
	// diagnostic and an open that blocks until somebody writes. This one
	// fails by TIMING OUT rather than by asserting, which is why it
	// carries its own deadline — a suite that never finishes is not a
	// red.
	//
	// REQUIRED MUTATION, RUN: delete the packable branch. This subtest
	// hangs and the deadline reports it at five seconds.
	t.Run("a pipe with a recorded size does not block the run", func(t *testing.T) {
		got := run(t, 64)
		var names []string
		for _, m := range got.measured {
			names = append(names, m.file.Path)
		}
		if !reflect.DeepEqual(names, []string{"index.html"}) {
			t.Errorf("measured %v, want the regular file alone", names)
		}
	})
}
