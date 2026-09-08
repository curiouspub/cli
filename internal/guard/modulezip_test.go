package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryPathAModuleZipWouldRefuseSitsInsideANestedModule.
//
// A published Go module's zip may only carry paths made of ordinary
// printable ASCII. One byte outside that and the whole module becomes
// UNFETCHABLE — not the file, the module, at every commit that contains
// it:
//
//	create zip: …/<astral character>/pages/.gitkeep:
//	malformed file path: invalid char
//
// This repository met that for three days. Two pre-flight fixtures are
// named for what they test — a directory whose name is a single astral
// character, and one carrying a Latin-1 accent — because a row proving
// that an escape in a config file resolves to a real directory needs a
// real directory with that real name. They are correct fixtures and the
// rows need them.
//
// The fix is not to rename them. It is a go.mod in their subtree, which
// makes it a module of its own and leaves it out of the parent's zip.
// This row is what keeps that true: it refuses any tracked path a zip
// would reject unless the path sits inside a nested module.
//
// WHY A ROW AND NOT A NOTE. Nothing about adding such a fixture fails
// locally. `go build`, `go test` and every other guard stay green; the
// damage appears only when somebody outside this repository runs
// `go get`, and the first symptom is a version pin that will not move.
// Three days passed before anyone did.
func TestEveryPathAModuleZipWouldRefuseSitsInsideANestedModule(t *testing.T) {
	root := moduleRoot(t)

	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("listing tracked files: %v", err)
	}
	paths := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(paths) < 50 {
		t.Fatalf("git listed %d tracked files, which is too few to be this "+
			"repository — a scan that found nothing would report clean", len(paths))
	}

	var seen int
	for _, path := range paths {
		if path == "" || asciiPrintable(path) {
			continue
		}
		seen++
		if inNestedModule(root, path) {
			continue
		}
		t.Errorf("%s carries a byte a module zip refuses, and is not inside a "+
			"nested module.\n"+
			"    A published module's paths are printable ASCII, and one that is "+
			"not makes the WHOLE module unfetchable at every commit containing "+
			"it — `go get` fails for everybody, and the symptom is a pin that "+
			"will not move rather than an error anybody here sees.\n"+
			"    If the name is what the fixture is FOR, put a go.mod in its "+
			"subtree so the zip omits it. If it is incidental, rename it.", path)
	}

	// THE POSITIVE CONTROL, and it is not decoration: this row's whole
	// body is skipped for an all-ASCII tree, so without a path that
	// actually exercises the check, deleting the check would leave it
	// green. The two pre-flight fixtures are that path, and if they ever
	// go away this row must be re-pointed rather than quietly retired.
	if seen == 0 {
		t.Fatal("no tracked path carries a non-ASCII byte, so nothing exercised " +
			"this check — the fixtures it was written for are gone, and it is " +
			"now a row that cannot fail")
	}
}

// asciiPrintable reports whether every byte of s is one a module zip
// accepts. Byte-wise on purpose: the rule is about bytes in a path, not
// about what they decode to.
func asciiPrintable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// inNestedModule reports whether path lies under a directory carrying its
// own go.mod, below the repository root. Walking upward rather than
// reading the root's own file, because the question is "is this excluded
// from the parent module", and a go.mod anywhere between here and the
// root is what excludes it.
func inNestedModule(root, path string) bool {
	dir := filepath.Dir(path)
	for dir != "." && dir != string(filepath.Separator) {
		if _, err := os.Stat(filepath.Join(root, dir, "go.mod")); err == nil {
			return true
		}
		dir = filepath.Dir(dir)
	}
	return false
}
