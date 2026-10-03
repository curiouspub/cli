package pack

import (
	"io"
	"io/fs"
	"os"
)

// FS is the narrow filesystem surface the walk needs.
//
// IT DECLARES ITS OWN RATHER THAN BORROWING THE PRE-FLIGHT ENGINE'S, and
// the reason is a shape mismatch rather than a preference: the engine
// hands a check a seam of stat-and-open, which cannot express directory
// enumeration, and widening it would add a method for one caller that no
// check on the other side has any use for. This package must not import
// that one in any case — the result model is the leaf both of them share,
// and it is a leaf precisely so the walk and the checks that read the
// walk's output do not import each other.
//
// OPEN IS HERE, AND THE WALK NEVER CALLS IT ON A PROJECT FILE. That is
// deliberate: a seam with no content-reading operation would make "the
// walk reads no file contents" trivially true and unmeasurable, and the
// claim is worth measuring. A wrapper that counts Open sees the walk
// reading ignore files and nothing else, and the same wrapper can be
// asked to read a walked file so that its zero is known to be a real
// zero rather than a broken instrument.
type FS interface {
	// ReadDir lists a directory's entries. The entries describe
	// themselves — a symlink reports as a symlink rather than as
	// whatever it points at — which is what lets the walk decline to
	// follow one without resolving it first.
	ReadDir(name string) ([]fs.DirEntry, error)

	// Open opens a file's contents for reading. This is the operation a
	// read-counting wrapper counts.
	Open(name string) (io.ReadCloser, error)
}

// OSFileSystem is the production FS: the real filesystem, through the
// standard library.
type OSFileSystem struct{}

// ReadDir implements FS. The standard library returns entries sorted by
// name; the walk does not rely on that, and one of its rows reverses the
// order on purpose to prove it.
func (OSFileSystem) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }

// Open implements FS.
func (OSFileSystem) Open(name string) (io.ReadCloser, error) { return os.Open(name) }

// linkReader is the one thing a warning about a link wants that the walk
// does not otherwise need: where the link points. It is a separate,
// optional interface so a filesystem that cannot answer — every test
// double this package already has — still walks, and the warning then
// names the link without its target.
type linkReader interface {
	Readlink(name string) (string, error)
}

// The real filesystem must answer it, or no warning in production names
// a target and only the test doubles would notice.
var _ linkReader = OSFileSystem{}

// Readlink reads where a link points, without following it.
func (OSFileSystem) Readlink(name string) (string, error) { return os.Readlink(name) }

// readLink asks fsys where name points, when fsys can say.
func readLink(fsys FS, name string) (string, bool) {
	lr, ok := fsys.(linkReader)
	if !ok {
		return "", false
	}
	target, err := lr.Readlink(name)
	if err != nil {
		return "", false
	}
	return target, true
}
