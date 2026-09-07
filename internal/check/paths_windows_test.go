//go:build windows

package check

import (
	"reflect"
	"testing"
)

// TestNewPathsConvertsBackslashSeparators is the Windows half, and it is
// the only row in this package that observes the conversion on a literal
// input rather than on one the host happened to build.
//
// It is tagged rather than skipped so that it compiles on every push —
// `go vet` with the Windows target reaches it even where the test does
// not run — and so that whoever breaks it is told which platform it
// belongs to instead of finding an empty file.
//
// WHAT THE OTHER LEGS DO NOT ASSERT: nothing here runs on Linux or
// macOS, so a green tick on those legs says nothing about separator
// conversion. The untagged row beside this one covers them by building
// its input with the host's own joiner, which is a no-op there.
func TestNewPathsConvertsBackslashSeparators(t *testing.T) {
	got := NewPaths(`src\pages\index.astro`, `src\lib\api.ts`)
	want := []string{"src/pages/index.astro", "src/lib/api.ts"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewPaths = %v, want %v", got, want)
	}
}
