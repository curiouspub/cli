//go:build !unix

package preflight

import "testing"

// newFIFOFixture has no portable implementation on this platform (no
// named pipes on Windows, at least not through anything this package's
// dependency policy allows it to reach) — see
// astroconfig_fifo_unix_test.go for the real one. Returning ok=false
// lets TestCheckAstroConfig_FIFODoesNotHang skip cleanly rather than
// failing to compile.
func newFIFOFixture(t *testing.T) (string, bool) {
	t.Helper()
	return "", false
}

// newSymlinkToFIFOFixture has the same no-portable-FIFO limitation as
// newFIFOFixture above — see astroconfig_fifo_unix_test.go for the real
// one.
func newSymlinkToFIFOFixture(t *testing.T) (string, bool) {
	t.Helper()
	return "", false
}
