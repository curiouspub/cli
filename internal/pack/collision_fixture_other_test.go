//go:build !linux

package pack

import "testing"

// addCaseCollision cannot build its fixture here, and the reason is a
// property of the filesystem rather than of this package.
//
// Measured: writing README.md and then readme.md into one directory on a
// case-insensitive volume leaves ONE file, named README.md, holding the
// second write's bytes. That is the default behaviour of the usual
// filesystem on this platform, so the state the fixture describes cannot
// exist to be walked.
//
// WHAT THE GREEN TICK ON THIS PLATFORM IS NOT CLAIMING: that the walk
// ever met two colliding files on a real disk. What it does claim, here
// as everywhere, is that the detector groups colliding names correctly —
// that row is driven from an injected path list in findings_test.go and
// runs on every platform, because the detector is a pure function of the
// list and needs no filesystem at all.
func addCaseCollision(t *testing.T, root string) bool {
	t.Helper()
	return false
}
