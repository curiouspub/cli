//go:build !windows

package check

import (
	"reflect"
	"testing"
)

// TestNewPathsKeepsABackslashInAFilename is a build-tagged row and the
// tag is the assertion's subject rather than a convenience.
//
// ON UNIX A BACKSLASH IS AN ORDINARY FILENAME CHARACTER. A project may
// legitimately contain a file called `weird\name.astro`, and a
// normaliser written as a blind replacement of backslash with slash
// would rename it in the report — pointing the user at a directory that
// does not exist while claiming to have found their file.
//
// It cannot run on Windows, where that byte is a separator and the file
// cannot exist, so the row would be asserting the opposite of the truth
// there. That is why it is tagged rather than skipped at runtime: a
// wholly skipped file is one nobody notices has stopped compiling.
func TestNewPathsKeepsABackslashInAFilename(t *testing.T) {
	got := NewPaths(`weird\name.astro`)
	want := []string{`weird\name.astro`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewPaths = %v, want %v — a backslash here is part of the name", got, want)
	}
}
