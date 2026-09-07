//go:build !unix

package pack

import "testing"

// namedPipeFixture has no portable spelling on this platform — there is
// no named pipe this package's dependency policy can reach for. The real
// one is in walk_pipe_unix_test.go, and the property it confirms is
// asserted on every platform through the synthetic listing in
// TestWalkSkipsIrregularEntriesSilently. Returning ok=false lets the row
// skip with a reason rather than fail to compile.
func namedPipeFixture(t *testing.T) (string, bool) {
	t.Helper()
	return "", false
}

// ignoreFilePipeFixture has the same no-portable-pipe limitation as
// namedPipeFixture above.
func ignoreFilePipeFixture(t *testing.T) (string, bool) {
	t.Helper()
	return "", false
}
