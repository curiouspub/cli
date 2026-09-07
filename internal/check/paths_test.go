package check

import (
	"path/filepath"
	"reflect"
	"testing"
)

// TestNewPathsNormalisesTheHostSeparator builds its input with the
// host's own joiner, so the row does different work on each leg and both
// halves are worth having.
//
// ON WINDOWS it observes a real conversion: filepath.Join produces
// backslashes there, and the assertion is that they come back as
// slashes. ON UNIX filepath.Join already produces slashes, so what it
// observes is that the constructor leaves a correct path alone. Neither
// leg alone is the whole claim, and saying which is which matters —
// a green tick on a leg that never reached the conversion is not
// evidence about the conversion.
func TestNewPathsNormalisesTheHostSeparator(t *testing.T) {
	got := NewPaths(
		filepath.Join("src", "pages", "index.astro"),
		filepath.Join("src", "lib", "api.ts"),
	)
	want := []string{"src/pages/index.astro", "src/lib/api.ts"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewPaths = %v, want %v", got, want)
	}
}

// TestNewPathsOfNothingIsNil. A finding about the project as a whole
// carries no paths, and an empty non-nil slice would render as a
// difference nobody meant in a machine-readable result.
func TestNewPathsOfNothingIsNil(t *testing.T) {
	if got := NewPaths(); got != nil {
		t.Errorf("NewPaths() = %#v, want nil", got)
	}
}

// TestNewPathsLeavesSlashedPathsAlone — the walk will hand it paths that
// are already correct on two of the three legs, and a constructor that
// disturbed those would be worse than none.
func TestNewPathsLeavesSlashedPathsAlone(t *testing.T) {
	want := []string{"src/pages/index.astro", "public/hero image.png", "a.ts"}
	got := NewPaths(want...)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("NewPaths = %v, want %v unchanged", got, want)
	}
}
