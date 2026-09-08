package pack

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/check"
)

// What the archive does with a symbolic link, over REAL links.
//
// The walk is what finds them and what refuses to follow them; these
// rows are about the other half — that nothing the walk skipped can turn
// up in the archive, and that what the user is told about it names no
// path outside their project.
//
// BOTH ROWS NEED A LINK THE PLATFORM WILL ACTUALLY CREATE, which an
// account without the privilege will not, so both probe first and skip
// with a reason. The walk's own handling of a link entry is asserted on
// every platform from a synthetic listing; what a skip here costs is the
// confirmation that a real filesystem produces the entry kind those rows
// describe.

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("creating the symbolic link %s: %v", link, err)
	}
}

// TestNoSkippedLinkReachesTheArchive.
//
// Three kinds in one fixture, because they fail differently: a link
// inside the project, which following would pack the same bytes twice; a
// link to a system file, which following would put a file the author
// never chose into their site; and a loop, which following would not
// finish at all.
//
// NO LINK ENTRY IS EMITTED EITHER, which is the half that is about
// somebody else's security rather than this user's. A link entry
// pointing outside the extraction root is exactly the payload a hardened
// extractor exists to refuse, and a client that routinely produced them
// would make every real attack look like ordinary traffic.
//
// THE ROW FINISHING IS ITSELF AN ASSERTION. A packer that resolved what
// it was handed would not fail on the loop below; it would stop
// returning, and the suite's own timeout would be the only thing that
// noticed.
//
// REQUIRED MUTATION, RUN: in the walk's descend, treat a symlink entry
// as a regular file — append it to w.files instead of w.symlinks.
func TestNoSkippedLinkReachesTheArchive(t *testing.T) {
	if !symlinkSupported(t) {
		t.Skip("this machine will not let the test create a symbolic link, and the row " +
			"needs four real ones")
	}

	root := writeTree(t, []entry{
		{path: "index.html", body: "<html>"},
		{path: "src/pages/about.astro", body: "---"},
	})
	mustSymlink(t, "index.html", filepath.Join(root, "inside-link"))
	mustSymlink(t, "/etc/passwd", filepath.Join(root, "outside-link"))
	mustSymlink(t, "loop-b", filepath.Join(root, "loop-a"))
	mustSymlink(t, "loop-a", filepath.Join(root, "loop-b"))

	links := []string{"inside-link", "loop-a", "loop-b", "outside-link"}

	tree, archive := packTree(t, root)

	if strings.Join(tree.Symlinks, ",") != strings.Join(links, ",") {
		t.Fatalf("the walk skipped %v, want %v — nothing below is measuring what it "+
			"claims otherwise", tree.Symlinks, links)
	}

	found := findingsFor(tree.Results, check.IDSymlinks)
	if len(found) != 1 {
		t.Fatalf("symlink findings = %v, want the one naming every link", found)
	}
	if strings.Join(found[0].Paths, ",") != strings.Join(links, ",") {
		t.Errorf("Paths = %v, want every link by its path inside the project: %v",
			found[0].Paths, links)
	}

	entries := readArchive(t, archive.Path)

	// The positive control. Every assertion below is about something
	// being ABSENT, and an empty archive would satisfy all of them.
	if got := namesOf(entries); strings.Join(got, ",") != "index.html,src/pages/about.astro" {
		t.Fatalf("archive holds %v, want exactly the two regular files", got)
	}

	for _, e := range entries {
		if e.hdr.Typeflag == tar.TypeSymlink || e.hdr.Typeflag == tar.TypeLink {
			t.Errorf("%s is a link entry (%q) with target %q",
				e.hdr.Name, e.hdr.Typeflag, e.hdr.Linkname)
		}
		if e.hdr.Linkname != "" {
			t.Errorf("%s carries a link target, %q", e.hdr.Name, e.hdr.Linkname)
		}
	}
}

// TestTheSkippedLinkWarningNamesNoTarget.
//
// A link is named by its path INSIDE the project and by nothing else.
// What it points at is not printed, in any form: a link to a key under
// somebody's home directory would otherwise put that path into
// scrollback, into a screenshot, and into the bug report they paste into
// a public issue tracker — and the user learns nothing from it that the
// link's own name does not already tell them.
//
// The detection and the wording both belong to the walk. What this row
// pins is the property, from outside, as a set of substrings that must
// not appear — because this is exactly the kind of detail that comes
// back the first time somebody makes the message more helpful.
//
// REQUIRED MUTATION, RUN: have the walk record each link as
// "path -> target" and the finding will render both.
func TestTheSkippedLinkWarningNamesNoTarget(t *testing.T) {
	if !symlinkSupported(t) {
		t.Skip("this machine will not let the test create a symbolic link, and the row " +
			"needs two real ones")
	}

	// A stand-in home directory, outside the project, holding something
	// nobody would want printed.
	home := t.TempDir()
	key := filepath.Join(home, ".ssh", "id_rsa")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatalf("building the stand-in home directory: %v", err)
	}
	if err := os.WriteFile(key, []byte("not a key"), 0o600); err != nil {
		t.Fatalf("writing the stand-in key: %v", err)
	}

	root := writeTree(t, []entry{{path: "index.html", body: "<html>"}})
	mustSymlink(t, "/etc/passwd", filepath.Join(root, "system-link"))
	mustSymlink(t, key, filepath.Join(root, "home-link"))

	tree := mustWalk(t, OSFileSystem{}, root)
	found := findingsFor(tree.Results, check.IDSymlinks)
	if len(found) != 1 {
		t.Fatalf("symlink findings = %v, want the one this row is about", found)
	}

	// Everything a surface has to render, in one string: the headline,
	// the three optional parts, and the structured list an agent reads.
	f := found[0]
	rendered := strings.Join(append([]string{f.Message, f.What, f.Why, f.Next}, f.Paths...), "\n")

	// The positive control: the links ARE named, so the absences below
	// are not the absence of a warning.
	for _, want := range []string{"home-link", "system-link"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the warning does not name %s, so it is not the output this row is "+
				"about:\n%s", want, rendered)
		}
	}

	for _, forbidden := range []struct {
		what  string
		value string
	}{
		{"a system path", "/etc/passwd"},
		{"a system path's basename", "passwd"},
		{"the absolute path of a private key", key},
		{"the directory holding it", home},
		{"a private key's basename", "id_rsa"},
		{"the directory it sits in", ".ssh"},
	} {
		if strings.Contains(rendered, forbidden.value) {
			t.Errorf("the warning renders %s (%q):\n%s", forbidden.what, forbidden.value, rendered)
		}
	}
}
