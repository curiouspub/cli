//go:build unix

package pack

import (
	"io/fs"
	"os"
	"testing"
)

// THIS FILE IS TAGGED FOR THE PLATFORMS WHERE A PERMISSION BIT MEANS
// WHAT THESE ROWS READ IT TO MEAN, and the tag rather than a runtime
// skip is deliberate: on Windows the mode a file reports is derived from
// a read-only attribute rather than from nine bits, so the rows here do
// not have a weaker version there — they have no subject at all. A
// tagged row leaves this repository's skip gate's view entirely, which
// is why the platform it belongs to is written down here.
//
// The partner file supplies the same helper for every other platform and
// says no, so the row that needs a real chmod skips with a reason
// instead of failing to compile.

// chmodFixture sets a file's permission bits and reports whether they
// took. A filesystem mounted without permission support answers the
// request and changes nothing, so the result is READ BACK rather than
// inferred from the absence of an error — a fixture that silently did
// not happen is a row asserting over a state that never arrived.
func chmodFixture(t *testing.T, path string, mode fs.FileMode) bool {
	t.Helper()

	if err := os.Chmod(path, mode); err != nil {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("reading %s back after chmod: %v", path, err)
	}
	return info.Mode().Perm() == mode
}

// TestTheTemporaryArchiveIsPrivateToWhoeverPackedIt.
//
// The archive holds the user's own source, which may be an unpublished
// site, a draft, or a private repository's contents. It is written into
// a directory every account on the machine can list, so the bits are the
// only thing between it and anybody else logged in.
//
// REQUIRED MUTATION, RUN: chmod the file to 0644 immediately after
// CreateTemp in Pack.
func TestTheTemporaryArchiveIsPrivateToWhoeverPackedIt(t *testing.T) {
	root := writeTree(t, determinismFixture())
	_, archive := packTree(t, root)

	info, err := os.Stat(archive.Path)
	if err != nil {
		t.Fatalf("reading the archive back: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("archive mode = %o, want 600 — it holds the user's own source", got)
	}
}
