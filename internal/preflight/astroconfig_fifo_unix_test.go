//go:build unix

package preflight

import (
	"os"
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

// newSymlinkToFIFOFixture creates a real named pipe (named differently
// from astro.config.mjs, so the FIFO itself is never a candidate by
// direct name) and a symlink called astro.config.mjs pointing at it.
// isFile must resolve the link, see the FIFO at the far end, and refuse
// it — the shape a fix that follows every symlink unconditionally would
// miss.
func newSymlinkToFIFOFixture(t *testing.T) (string, bool) {
	t.Helper()
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "real-pipe")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatalf("creating FIFO fixture: %v", err)
	}
	linkPath := filepath.Join(dir, "astro.config.mjs")
	if err := os.Symlink(fifoPath, linkPath); err != nil {
		t.Fatalf("creating symlink-to-FIFO fixture: %v", err)
	}
	return dir, true
}
