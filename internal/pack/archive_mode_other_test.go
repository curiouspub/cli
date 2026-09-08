//go:build !unix

package pack

import (
	"io/fs"
	"testing"
)

// chmodFixture cannot build its fixture here, and the reason is a
// property of the platform rather than of this package: the mode a file
// reports on Windows is derived from a read-only attribute rather than
// from nine permission bits, so a request for 0777 or 0600 has nowhere
// to land and reading it back would report something neither asked for.
//
// WHAT THE SKIP COSTS, because the answer is a claim and not a note: the
// row that uses this helper is the confirmation that a real on-disk mode
// does not reach the archive. The property itself — every entry packs as
// 0644 whatever the file claimed — is asserted on every platform from a
// synthetic listing, in archive_test.go, because the header is a pure
// function of the list and needs no filesystem at all. What is lost here
// is the confirmation that a real volume can hand the packer a file
// wearing some other mode.
func chmodFixture(t *testing.T, path string, mode fs.FileMode) bool {
	t.Helper()
	return false
}
