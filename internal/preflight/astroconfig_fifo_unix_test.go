//go:build unix

package preflight

import (
	"path/filepath"
	"syscall"
	"testing"
)

// newFIFOFixture creates a real named pipe called astro.config.mjs in a
// fresh temp directory and returns that directory. Nothing ever opens
// the write end, so a read against it blocks forever unless the caller
// (isFile, by way of CheckAstroConfig) refuses to Open it in the first
// place — which is the exact property TestCheckAstroConfig_FIFODoesNotHang
// exists to check.
func newFIFOFixture(t *testing.T) (string, bool) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "astro.config.mjs")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("creating FIFO fixture: %v", err)
	}
	return dir, true
}
