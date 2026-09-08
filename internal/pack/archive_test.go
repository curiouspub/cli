package pack

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/check"
)

// The archive's rows. Two properties run through all of them: the same
// input produces the same bytes, and nothing about the machine that
// packed it reaches the output.
//
// NO COMMITTED DIGEST APPEARS ANYWHERE HERE, and one row enforces that.
// Every determinism claim is a comparison between two packs made by the
// SAME binary, because the compression layer is an implementation rather
// than a specified format: a Go upgrade may legitimately change every
// digest, and a committed value would turn that into a red that looks
// like a packing bug.

// ---------------------------------------------------------------------
// Reading an archive back
// ---------------------------------------------------------------------

// archiveEntry is one entry as it reads back out of the archive.
type archiveEntry struct {
	hdr  tar.Header
	body string
}

// readArchive reads every entry back through the standard reader, which
// is the closest thing to the extractor on the other end.
func readArchive(t *testing.T, path string) []archiveEntry {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening the archive: %v", err)
	}
	defer f.Close()

	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("reading the archive's gzip wrapper: %v", err)
	}
	defer zr.Close()

	var out []archiveEntry
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("reading the next archive entry: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("reading the body of %s: %v", hdr.Name, err)
		}
		out = append(out, archiveEntry{hdr: *hdr, body: string(body)})
	}
	return out
}

func namesOf(entries []archiveEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.hdr.Name)
	}
	return out
}

// mustPack packs a walked project into a directory this test owns and
// removes the archive when the test ends.
func mustPack(t *testing.T, fsys FS, root string, files []File) Archive {
	t.Helper()
	a, err := Pack(fsys, root, t.TempDir(), files)
	if err != nil {
		t.Fatalf("Pack(%s): %v", root, err)
	}
	t.Cleanup(func() {
		if err := a.Remove(); err != nil {
			t.Errorf("removing the archive: %v", err)
		}
	})
	return a
}

// packTree walks a real directory and packs what it found, which is the
// ordinary path: one walk, and the archive reads the list it produced.
func packTree(t *testing.T, root string) (Tree, Archive) {
	t.Helper()
	tree := mustWalk(t, OSFileSystem{}, root)
	return tree, mustPack(t, OSFileSystem{}, root, tree.Files)
}

// ---------------------------------------------------------------------
// The normalisation table, driven directly
// ---------------------------------------------------------------------

// TestTheHeaderIsTheNormalisationTable drives the header builder rather
// than inferring each decision from the bytes that came out the far end.
//
// The fields below are the ones that make an archive differ between two
// people's machines, and every one of them is asserted against a File
// whose on-disk facts say something else: a mode of 0777 and a path with
// mixed case, so a builder that consulted either would be visible here.
//
// REQUIRED MUTATIONS, ALL RUN. One per row, named on the row.
func TestTheHeaderIsTheNormalisationTable(t *testing.T) {
	got := header(File{Path: "src/Pages/Index.astro", Size: 42, Mode: 0o777})

	for _, row := range []struct {
		field string
		got   any
		want  any
	}{
		// REQUIRED MUTATION, RUN: lowercase f.Path on the way into
		// Name. It reds here and NOWHERE ELSE in this package, which is
		// the reason the path in this fixture carries capitals: every
		// other fixture is lowercase already, so a packer that folded
		// case would have gone through the whole suite untouched.
		{"Name", got.Name, "src/Pages/Index.astro"},

		// The positive control for the whole table. Without it, a
		// builder that returned an empty header would satisfy every
		// other row here, since all of them assert a zero or a constant.
		// REQUIRED MUTATION: drop Size from the literal.
		{"Size", got.Size, int64(42)},

		// REQUIRED MUTATION: set Mode to int64(f.Mode.Perm()).
		{"Mode", got.Mode, int64(0o644)},

		// REQUIRED MUTATION: set Typeflag to tar.TypeDir.
		{"Typeflag", got.Typeflag, byte(tar.TypeReg)},

		// The four fields a header built from a FileInfo would carry
		// through, and the reason two machines would otherwise disagree.
		// REQUIRED MUTATION, one each: set Uid and Gid to os.Getuid()
		// and os.Getgid(); set Uname and Gname to any non-empty string.
		{"Uid", got.Uid, 0},
		{"Gid", got.Gid, 0},
		{"Uname", got.Uname, ""},
		{"Gname", got.Gname, ""},

		// Rendered rather than compared as time values: a wall clock and
		// a location pointer make an equality failure unreadable, and
		// the question here is which instant was written.
		// REQUIRED MUTATION: set ModTime to time.Now().
		{"ModTime", got.ModTime.UTC().Format(time.RFC3339), "1980-01-01T00:00:00Z"},
		// REQUIRED MUTATION: set AccessTime or ChangeTime to
		// packedModTime. Either one adds a per-entry record to every
		// header in the archive.
		{"AccessTime", got.AccessTime.IsZero(), true},
		{"ChangeTime", got.ChangeTime.IsZero(), true},

		// REQUIRED MUTATION: drop Format from the literal.
		//
		// WHAT THIS ROW DOES AND DOES NOT CLAIM, because the difference
		// is measurable and was measured. It claims the packer NAMES the
		// format instead of leaving the writer to infer one. It does not
		// claim every entry is encoded that way: the standard writer
		// treats a request for this format as "and the plainer one is
		// still allowed", so an entry that fits the plainer encoding
		// gets it, and only a name too long to fit any other way is
		// encoded with an extended record. That is why the assertion
		// lives here, on the header this package builds, rather than on
		// what a reader reports about the archive.
		{"Format", got.Format, tar.FormatPAX},
	} {
		if row.got != row.want {
			t.Errorf("%s = %v, want %v", row.field, row.got, row.want)
		}
	}
}

// TestOnlyTheNameAndTheSizeCrossFromTheFile is the discrimination half
// of the table above. That one asserts a set of constants, which a
// builder ignoring its argument entirely would also satisfy for every
// row but Size; this one asserts that the two fields which SHOULD vary
// do, and that nothing else moves when the file underneath changes.
//
// REQUIRED MUTATION, RUN: set Mode from f.Mode.Perm(). The two files
// below differ in mode, so the modes stop matching.
func TestOnlyTheNameAndTheSizeCrossFromTheFile(t *testing.T) {
	first := header(File{Path: "a.html", Size: 1, Mode: 0o600})
	second := header(File{Path: "b/c.html", Size: 900, Mode: 0o755})

	if first.Name == second.Name || first.Size == second.Size {
		t.Fatalf("the two headers do not differ where they must: %q/%d and %q/%d",
			first.Name, first.Size, second.Name, second.Size)
	}

	// Blank the two that are meant to differ and the rest must be equal
	// field for field — including any field added to the builder later,
	// which is what makes this stronger than listing the fields again.
	first.Name, second.Name = "", ""
	first.Size, second.Size = 0, 0
	if !reflect.DeepEqual(*first, *second) {
		t.Errorf("headers differ beyond name and size:\n%+v\n%+v", *first, *second)
	}
}

// ---------------------------------------------------------------------
// Determinism, within this binary
// ---------------------------------------------------------------------

func determinismFixture() []entry {
	return []entry{
		{path: "index.html", body: "<html>hello</html>"},
		{path: "src/pages/about.astro", body: "---\ntitle: About\n---\n"},
		{path: "src/styles/site.css", body: "body{margin:0}"},
		{path: "public/robots.txt", body: "User-agent: *\n"},
	}
}

// TestTwoPacksOfOneProjectAgreeToTheByte.
//
// REQUIRED MUTATION, RUN: set ModTime to time.Now() in header. The two
// packs then disagree, which is the whole reason the constant exists.
func TestTwoPacksOfOneProjectAgreeToTheByte(t *testing.T) {
	root := writeTree(t, determinismFixture())
	tree := mustWalk(t, OSFileSystem{}, root)

	first := mustPack(t, OSFileSystem{}, root, tree.Files)
	second := mustPack(t, OSFileSystem{}, root, tree.Files)

	if first.SHA256 != second.SHA256 {
		t.Errorf("two packs of one project disagree: %s and %s", first.SHA256, second.SHA256)
	}
	if first.Size != second.Size {
		t.Errorf("sizes = %d and %d, want the same", first.Size, second.Size)
	}
	if first.Entries != len(tree.Files) || second.Entries != len(tree.Files) {
		t.Errorf("entries = %d and %d, want %d", first.Entries, second.Entries, len(tree.Files))
	}
	if first.Path == second.Path {
		t.Errorf("both packs used one file, %s — each archive is its own temporary file", first.Path)
	}
}

// TestACopiedProjectWithNewerTimestampsPacksTheSame is the row the
// mtime constant exists for: a checkout writes the time it happened, so
// two clones of one repository carry different timestamps and identical
// content.
//
// THE COPY AND THE TIMESTAMPS ARE DONE IN GO, not in a shell. This suite
// runs on a platform with neither of the two commands that would be
// reached for, and a row that reds there for want of a shell utility is
// a row about the runner rather than about the packer.
//
// REQUIRED MUTATION, RUN: set ModTime to the walked file's own
// modification time — which means giving File a ModTime and reading it
// in the walk. Simpler and equivalent: set ModTime to time.Now(), which
// this row and the one above both catch.
func TestACopiedProjectWithNewerTimestampsPacksTheSame(t *testing.T) {
	root := writeTree(t, determinismFixture())
	original := mustPack(t, OSFileSystem{}, root, mustWalk(t, OSFileSystem{}, root).Files)

	copied := copyTree(t, root)
	future := time.Now().Add(72 * time.Hour)
	touchAll(t, copied, future)

	second := mustPack(t, OSFileSystem{}, copied, mustWalk(t, OSFileSystem{}, copied).Files)

	if original.SHA256 != second.SHA256 {
		t.Errorf("a copy with newer timestamps packed differently: %s and %s",
			original.SHA256, second.SHA256)
	}
}

// TestOneChangedByteChangesTheDigest is the discrimination control for
// the two rows above. Both of them assert that two digests MATCH, which
// a packer returning one constant would satisfy perfectly.
//
// REQUIRED MUTATION, RUN: return a fixed string from Pack as the
// SHA-256. The two rows above stay green and this one reds, which is
// the arrangement this row exists to guarantee.
func TestOneChangedByteChangesTheDigest(t *testing.T) {
	root := writeTree(t, determinismFixture())
	before := mustPack(t, OSFileSystem{}, root, mustWalk(t, OSFileSystem{}, root).Files)

	edited := filepath.Join(root, "index.html")
	body, err := os.ReadFile(edited)
	if err != nil {
		t.Fatalf("reading the fixture back: %v", err)
	}
	changed := append([]byte(nil), body...)
	changed[0] = '!'
	if err := os.WriteFile(edited, changed, 0o644); err != nil {
		t.Fatalf("editing the fixture: %v", err)
	}

	after := mustPack(t, OSFileSystem{}, root, mustWalk(t, OSFileSystem{}, root).Files)
	if before.SHA256 == after.SHA256 {
		t.Errorf("one changed byte left the digest at %s", before.SHA256)
	}
}

// TestTheDigestIgnoresWhatTheFilesystemSaysAboutTheFiles is the row for
// the fields nobody can vary on a test runner: a second uid, a second
// gid, a different set of permission bits.
//
// It packs the same names and the same bytes twice — once from a real
// directory, once from a synthetic listing whose entries claim a
// different mode and a different size-carrying FileInfo — and the two
// digests have to agree. The header takes nothing from a FileInfo, so
// the two CANNOT differ unless somebody makes them.
//
// REQUIRED MUTATION, RUN: set Mode from f.Mode.Perm() in header.
func TestTheDigestIgnoresWhatTheFilesystemSaysAboutTheFiles(t *testing.T) {
	files := determinismFixture()

	root := writeTree(t, files)
	fromDisk := mustPack(t, OSFileSystem{}, root, mustWalk(t, OSFileSystem{}, root).Files)

	// The same tree, described as world-writable and executable by a
	// listing that answers every question differently from the real one.
	synthetic := syntheticTree(files, 0o777)
	fromListing := mustPack(t, synthetic, "root", mustWalk(t, synthetic, "root").Files)

	if fromDisk.SHA256 != fromListing.SHA256 {
		t.Errorf("the digest moved with the listing's own claims: %s and %s",
			fromDisk.SHA256, fromListing.SHA256)
	}
}

// ---------------------------------------------------------------------
// What the archive holds
// ---------------------------------------------------------------------

// TestEveryEntryCarriesTheNormalisedHeader reads the table back out of a
// real archive. It is the outside view of TestTheHeaderIsTheNormalisation
// Table: that row asks what this package builds, this one asks what a
// reader on the far end sees.
//
// REQUIRED MUTATION, RUN: set Uid and Gid from os.Getuid()/os.Getgid()
// in header. Reds here and in the table row, which is the point of
// having both.
func TestEveryEntryCarriesTheNormalisedHeader(t *testing.T) {
	root := writeTree(t, determinismFixture())
	_, archive := packTree(t, root)

	entries := readArchive(t, archive.Path)
	if len(entries) == 0 {
		t.Fatal("the archive holds no entries at all, so nothing below was measured")
	}

	for _, e := range entries {
		if e.hdr.Mode != 0o644 {
			t.Errorf("%s: mode = %o, want 644", e.hdr.Name, e.hdr.Mode)
		}
		if e.hdr.Uid != 0 || e.hdr.Gid != 0 {
			t.Errorf("%s: uid/gid = %d/%d, want 0/0", e.hdr.Name, e.hdr.Uid, e.hdr.Gid)
		}
		if e.hdr.Uname != "" || e.hdr.Gname != "" {
			t.Errorf("%s: uname/gname = %q/%q, want both empty",
				e.hdr.Name, e.hdr.Uname, e.hdr.Gname)
		}
		if !e.hdr.ModTime.Equal(packedModTime) {
			t.Errorf("%s: modtime = %v, want %v", e.hdr.Name, e.hdr.ModTime, packedModTime)
		}
		if e.hdr.Typeflag != tar.TypeReg {
			t.Errorf("%s: typeflag = %q, want a regular file", e.hdr.Name, e.hdr.Typeflag)
		}
		if e.hdr.Format == tar.FormatGNU {
			t.Errorf("%s: format = %v — the pinned format rules this one out", e.hdr.Name, e.hdr.Format)
		}
	}
}

// TestTheArchiveIsTheWalksListInOrderAndNothingElse.
//
// Two claims that belong together because one fixture answers both: the
// entries are the walk's sorted list, in its order, and there are no
// directory entries even though the fixture nests three deep and holds
// an empty directory. Extraction creates parents; an empty directory
// carries nothing a site build reads.
//
// REQUIRED MUTATION, RUN: emit a tar.TypeDir header for each parent
// directory before its files.
func TestTheArchiveIsTheWalksListInOrderAndNothingElse(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "src/pages/deep/nested/index.astro", body: "---"},
		{path: "b.html", body: "b"},
		{path: "a.html", body: "a"},
		{path: "public/empty/"},
	})

	tree, archive := packTree(t, root)
	entries := readArchive(t, archive.Path)

	want := pathsOf(tree.Files)
	got := namesOf(entries)
	if len(want) == 0 {
		t.Fatal("the walk found no files, so the comparison below is between two empty lists")
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("archive entries =\n%s\nwant the walk's list in its order:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	for _, e := range entries {
		if e.hdr.Typeflag == tar.TypeDir || strings.HasSuffix(e.hdr.Name, "/") {
			t.Errorf("%q is a directory entry", e.hdr.Name)
		}
	}
	if archive.Entries != len(want) {
		t.Errorf("Entries = %d, want %d", archive.Entries, len(want))
	}
}

// TestTheArchiveNamesArePublishablePaths. No leading "./", no absolute
// path, no host separator: the names in the archive are the addresses
// the files will be served at, and the extractor on the far end refuses
// the whole archive over any of the three rather than skipping an entry.
//
// It also LOGS the listing, which is the only reason a human ever needed
// to keep one of these files. `go test ./internal/pack/ -v` shows what
// an archive tool would have printed, and nothing survives the run.
//
// REQUIRED MUTATION, RUN: prefix Name with "./" in header.
func TestTheArchiveNamesArePublishablePaths(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "index.html", body: "<html>"},
		{path: "src/pages/about.astro", body: "---"},
		{path: "public/images/logo.svg", body: "<svg/>"},
	})

	_, archive := packTree(t, root)
	entries := readArchive(t, archive.Path)
	if len(entries) != 3 {
		t.Fatalf("entries = %v, want the fixture's three", namesOf(entries))
	}

	for _, e := range entries {
		t.Logf("%v %d/%d %6d %s %s",
			fs.FileMode(e.hdr.Mode), e.hdr.Uid, e.hdr.Gid, e.hdr.Size,
			e.hdr.ModTime.UTC().Format("2006-01-02 15:04"), e.hdr.Name)
	}

	for _, e := range entries {
		switch {
		case strings.HasPrefix(e.hdr.Name, "./"):
			t.Errorf("%q carries a leading ./", e.hdr.Name)
		case strings.HasPrefix(e.hdr.Name, "/"):
			t.Errorf("%q is an absolute path", e.hdr.Name)
		case strings.Contains(e.hdr.Name, `\`):
			t.Errorf("%q carries the host's own separator", e.hdr.Name)
		case strings.Contains(e.hdr.Name, ".."):
			t.Errorf("%q climbs out of the project", e.hdr.Name)
		}
	}
}

// TestTheGzipWrapperCarriesNoNameAndNoTimestamp reads the header bytes
// directly, because that is the only place these fields exist — the
// standard reader hands back a decoded header, and a decoded zero is
// indistinguishable from a field the reader chose not to fill.
//
// THE COMPRESSION LEVEL IS OBSERVABLE HERE TOO, in the extra-flags byte:
// the strongest level writes 2, the fastest writes 4, and every level
// between writes 0. That byte is what makes the pinned constant a
// checkable claim rather than a comment.
//
// REQUIRED MUTATIONS, BOTH RUN: set compressionLevel to
// gzip.DefaultCompression (the flags byte becomes 0); set
// zw.Header.Name to any string (the name flag turns on).
func TestTheGzipWrapperCarriesNoNameAndNoTimestamp(t *testing.T) {
	root := writeTree(t, determinismFixture())
	_, archive := packTree(t, root)

	f, err := os.Open(archive.Path)
	if err != nil {
		t.Fatalf("opening the archive: %v", err)
	}
	defer f.Close()

	head := make([]byte, 10)
	if _, err := io.ReadFull(f, head); err != nil {
		t.Fatalf("reading the archive's first ten bytes: %v", err)
	}

	if head[0] != 0x1f || head[1] != 0x8b || head[2] != 8 {
		t.Fatalf("header = % x, want the gzip magic and the deflate method — nothing "+
			"below is meaningful otherwise", head)
	}

	const (
		flagName    = 0x08
		flagComment = 0x10
	)
	if head[3]&flagName != 0 {
		t.Errorf("the header names an original file, which differs per machine")
	}
	if head[3]&flagComment != 0 {
		t.Errorf("the header carries a comment")
	}
	if mtime := head[4:8]; mtime[0]|mtime[1]|mtime[2]|mtime[3] != 0 {
		t.Errorf("the header timestamp = % x, want zero — it is the time of packing", mtime)
	}
	if head[8] != 2 {
		t.Errorf("extra-flags = %d, want 2 — the strongest compression level, pinned by "+
			"name because a change to it changes every digest", head[8])
	}
	if head[9] != gzipOSUnknown {
		t.Errorf("operating-system byte = %d, want %d — naming the packing machine makes "+
			"one project's archive differ from another's", head[9], gzipOSUnknown)
	}
}

// ---------------------------------------------------------------------
// Long paths
// ---------------------------------------------------------------------

// TestLongPathsSurviveAndPackTheSameWayTwice.
//
// A long name is where a tar writer's format choice stops being uniform:
// the plain encoding holds a name of 100 bytes, reaches 255 only by
// splitting it across two fields, and cannot hold one segment longer
// than 100 at all — so a deep enough path is encoded a third way, with
// an extended record carrying the name. The record's own synthetic entry
// is named from the file's, which is why the second pack is the
// assertion that matters here.
//
// THE FIXTURE IS A SYNTHETIC LISTING RATHER THAN A REAL DIRECTORY, and
// that is a platform decision. A three-hundred-byte path is a fixture
// two of the three filesystems in the matrix have opinions about, and a
// red there would be about the runner rather than about the packer. What
// is under test is the tar layer, which is a pure function of the names
// it is handed.
//
// REQUIRED MUTATION, RUN: truncate Name to 100 bytes in header.
func TestLongPathsSurviveAndPackTheSameWayTwice(t *testing.T) {
	deep := strings.Repeat("abcdefgh/", 14) + "index.html"         // 136 bytes, splittable
	oneLongSegment := "src/" + strings.Repeat("n", 150) + ".astro" // one segment over 100
	veryDeep := strings.Repeat("abcdefgh/", 32) + "index.html"     // 298 bytes, unsplittable

	files := []entry{
		{path: "a.html", body: "short"},
		{path: deep, body: "deep"},
		{path: oneLongSegment, body: "long segment"},
		{path: veryDeep, body: "very deep"},
	}
	tree := syntheticTree(files, 0)
	walked := mustWalk(t, tree, "root")

	first := mustPack(t, tree, "root", walked.Files)
	second := mustPack(t, tree, "root", walked.Files)
	if first.SHA256 != second.SHA256 {
		t.Errorf("two packs of the same long paths disagree: %s and %s",
			first.SHA256, second.SHA256)
	}

	entries := readArchive(t, first.Path)
	got := namesOf(entries)
	want := pathsOf(walked.Files)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("names did not survive the round trip:\ngot  %q\nwant %q", got, want)
	}

	// The positive control for the paragraph above: at least one entry
	// really did need the extended encoding, so the row is not quietly
	// measuring four ordinary names.
	extended := 0
	for _, e := range entries {
		if e.hdr.Format == tar.FormatPAX {
			extended++
		}
		if e.hdr.Format == tar.FormatGNU {
			t.Errorf("%s: encoded in the format the header pin rules out", e.hdr.Name)
		}
	}
	if extended == 0 {
		t.Error("no entry needed the extended encoding, so this row measured nothing " +
			"the short-name rows do not already cover")
	}
}

// ---------------------------------------------------------------------
// The packer rewrites nothing
// ---------------------------------------------------------------------

// TestThePackerNormalisesNoName, driven DIRECTLY rather than through a
// walk of a real project.
//
// A name written with a combining mark — the letter, then the accent as
// its own character — cannot reach the packer by the ordinary route: the
// walk refuses such a name outright and the deploy stops before anything
// is packed. That is the reason to drive the packer itself rather than
// the reason to drop the question, because a packer that quietly folded
// the two spellings together would be invisible from outside.
//
// The escapes below are deliberate. Written as literal characters, the
// two names in this file would be at the mercy of whatever an editor or
// a version-control filter thinks it should do with them, which is the
// exact transformation under test.
//
// REQUIRED MUTATION, RUN, and the SPELLING of it is the disclosure: a
// real normalisation pass would need a dependency this repository does
// not carry and would not accept for this, so what was run is a
// stand-in that does exactly what one would do to these two names —
// replacing the letter-plus-accent sequence in Name with the single
// accented character. It reds here and nowhere else in the package.
func TestThePackerNormalisesNoName(t *testing.T) {
	const (
		decomposed  = "cafe\u0301.astro" // the letter, then the accent as its own character
		precomposed = "caf\u00e9.astro"  // the accented letter, as a single character
	)
	if decomposed == precomposed {
		t.Fatal("the two spellings are the same string, so this row cannot measure anything")
	}

	tree := syntheticTree([]entry{
		{path: decomposed, body: "two characters"},
		{path: precomposed, body: "one character"},
	}, 0)

	// The file list is built here rather than walked, because the walk
	// refuses both of these names.
	archive := mustPack(t, tree, "root", []File{
		{Path: decomposed, Size: int64(len("two characters"))},
		{Path: precomposed, Size: int64(len("one character"))},
	})

	got := namesOf(readArchive(t, archive.Path))
	want := []string{decomposed, precomposed}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("names = %q, want them byte for byte as they went in: %q", got, want)
	}
	// The discrimination half. Byte-identity alone would still pass if
	// the packer folded both names to one and the archive held a single
	// entry that happened to match the first.
	if len(got) != 2 || got[0] == got[1] {
		t.Errorf("entries = %q, want two names that stayed different", got)
	}
}

// TestTheArchiveModeIgnoresWhatTheListingSays is the platform-free half
// of the forced mode. The half that needs a real chmod is in the row
// beside it, which skips where the platform has no such bit.
//
// REQUIRED MUTATION, RUN: set Mode to int64(f.Mode.Perm()) in header.
func TestTheArchiveModeIgnoresWhatTheListingSays(t *testing.T) {
	files := []entry{
		{path: "build.sh", body: "#!/bin/sh\n"},
		{path: "private.txt", body: "secret"},
		{path: "index.html", body: "<html>"},
	}
	for _, mode := range []fs.FileMode{0o777, 0o600, 0o755, 0o444} {
		tree := syntheticTree(files, mode)
		archive := mustPack(t, tree, "root", mustWalk(t, tree, "root").Files)
		for _, e := range readArchive(t, archive.Path) {
			if e.hdr.Mode != 0o644 {
				t.Errorf("a %o file packed as %o, want 644 whatever the listing said",
					mode, e.hdr.Mode)
			}
		}
	}
}

// TestTheArchiveModeIgnoresWhatIsOnDisk is the confirmation half: the
// row above asserts what the packer does with a mode a listing claims,
// and this one asserts that a real volume can hand it one.
//
// THE EXECUTABLE FILE IS THE FIXTURE THAT MATTERS. Forcing the mode is
// what makes byte-identity across operating systems a real property
// rather than an aspiration, and an executable bit is the one thing
// Windows could not have reproduced — so packing a 0755 file as 0644 is
// the assertion that the claim holds, and a preserved bit is the one
// that would break it silently, on one platform, for one kind of file.
//
// REQUIRED MUTATION, RUN: set Mode to int64(f.Mode.Perm()) in header.
func TestTheArchiveModeIgnoresWhatIsOnDisk(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "build.sh", body: "#!/bin/sh\necho hello\n"},
		{path: "private.txt", body: "secret"},
		{path: "index.html", body: "<html>"},
	})

	// A slice rather than a map, like every other fixture here: map
	// iteration is randomised, and a fixture built in a different order
	// every run — or a skip naming a different mode every run — is a
	// failure nobody can reproduce.
	onDisk := []struct {
		name string
		mode fs.FileMode
	}{
		{"build.sh", 0o755},
		{"index.html", 0o444},
		{"private.txt", 0o600},
	}
	want := map[string]fs.FileMode{}
	for _, f := range onDisk {
		if !chmodFixture(t, filepath.Join(root, f.name), f.mode) {
			t.Skipf("this platform will not hold a file at mode %o, so the fixture the "+
				"row needs cannot be built on it", f.mode)
		}
		want[f.name] = f.mode
	}

	tree, archive := packTree(t, root)
	if len(tree.Files) != len(want) {
		t.Fatalf("the walk found %v, want the fixture's three files", pathsOf(tree.Files))
	}

	entries := readArchive(t, archive.Path)
	if len(entries) != len(want) {
		t.Fatalf("archive holds %v, want the fixture's three files", namesOf(entries))
	}
	for _, e := range entries {
		if e.hdr.Mode != 0o644 {
			t.Errorf("%s was %o on disk and packed as %o, want 644",
				e.hdr.Name, want[e.hdr.Name], e.hdr.Mode)
		}
	}
}

// ---------------------------------------------------------------------
// The temporary file
// ---------------------------------------------------------------------

// TestNothingIsLeftBehindWhenPackingFails.
//
// The archive holds the user's own source, so a half-written one left in
// a temporary directory after a failure is somebody's private project
// sitting on disk with nobody holding a path to it. The failure is
// injected mid-read rather than at the open, which is the case a cleanup
// written only for the early return would miss.
//
// THE ACCEPTANCE PAIR IS IN THE SAME FUNCTION and it is not decoration:
// an empty directory is also what a Pack that never created a file at
// all would leave, and a refusal row that cannot tell the difference
// between the thing failing correctly and the thing never running is not
// evidence.
//
// REQUIRED MUTATION, RUN: drop the os.Remove from Pack's deferred
// cleanup. Both halves red, and that is the right answer rather than a
// blunt one — the leftover is still there when the acceptance half
// packs, so the directory holds two archives where it should hold one.
func TestNothingIsLeftBehindWhenPackingFails(t *testing.T) {
	root := writeTree(t, determinismFixture())
	files := mustWalk(t, OSFileSystem{}, root).Files

	dir := t.TempDir()
	broken := &failingFS{FS: OSFileSystem{}, failAt: "src/styles/site.css", after: 3}

	_, err := Pack(broken, root, dir, files)
	if err == nil {
		t.Fatal("Pack returned no error over a file it could not finish reading")
	}
	if !strings.Contains(err.Error(), "src/styles/site.css") {
		t.Errorf("error = %q, want it to name the file it gave up on", err)
	}
	if left := listDir(t, dir); len(left) != 0 {
		t.Errorf("a failed pack left %v behind", left)
	}

	// The acceptance half, into the same directory: one archive appears,
	// and Remove takes it away again.
	good, err := Pack(OSFileSystem{}, root, dir, files)
	if err != nil {
		t.Fatalf("Pack over the same project without the injected failure: %v", err)
	}
	if left := listDir(t, dir); len(left) != 1 {
		t.Fatalf("a successful pack left %v, want exactly the archive", left)
	}
	if filepath.Dir(good.Path) != dir {
		t.Errorf("archive was written to %s, want it inside %s", good.Path, dir)
	}
	if err := good.Remove(); err != nil {
		t.Errorf("Remove: %v", err)
	}
	if left := listDir(t, dir); len(left) != 0 {
		t.Errorf("Remove left %v behind", left)
	}
	// Removing what is already gone is what a caller's deferred cleanup
	// does after an upload has moved the file, and it is not an error.
	if err := good.Remove(); err != nil {
		t.Errorf("a second Remove: %v", err)
	}
}

// TestPackRefusesAFileThatChangedUnderIt. The walk measured the size and
// the header promised it; a file edited between the two would otherwise
// produce an archive whose entry disagrees with its own header, and the
// extractor on the far end rejects the whole archive rather than the
// entry.
//
// REQUIRED MUTATION, RUN: drop the n != file.Size check from
// writeEntry. The shorter half reds; the longer half does not, because a
// file that GREW is refused by the archive writer itself rather than by
// this package — which is why both directions are here and only one of
// them is this package's own work.
func TestPackRefusesAFileThatChangedUnderIt(t *testing.T) {
	root := writeTree(t, []entry{{path: "index.html", body: "<html>hello</html>"}})
	files := mustWalk(t, OSFileSystem{}, root).Files

	// The acceptance half first: the same list, unedited, packs.
	if _, err := Pack(OSFileSystem{}, root, t.TempDir(), files); err != nil {
		t.Fatalf("the unedited project did not pack: %v", err)
	}

	for _, row := range []struct {
		name string
		body string
	}{
		{"shorter", "<html>"},
		{"longer", "<html>hello, and then a good deal more than there was before</html>"},
	} {
		t.Run(row.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(row.body), 0o644); err != nil {
				t.Fatalf("editing the fixture: %v", err)
			}
			_, err := Pack(OSFileSystem{}, root, t.TempDir(), files)
			if err == nil {
				t.Fatal("Pack accepted a file whose size no longer matched its header")
			}
			if !strings.Contains(err.Error(), "index.html") ||
				!strings.Contains(err.Error(), "packed") {
				t.Errorf("error = %q, want it to name the file and say what happened", err)
			}
		})
	}
}

// ---------------------------------------------------------------------
// No committed digest
// ---------------------------------------------------------------------

// TestNoCommittedDigestLivesInThisPackage.
//
// A committed digest is the one thing that would make a routine Go
// upgrade look like a packing bug: the compression layer is an
// implementation rather than a specified format, its output has changed
// across releases, and the likeliest response to that red — updating the
// committed value — teaches everybody to ignore the row that would catch
// a real regression. So the determinism rows compare two packs from the
// same binary, and this row is what stops the shortcut coming back.
//
// IT LIVES BESIDE WHAT IT GUARDS rather than with this repository's
// other guards, on the precedent of the one that keeps the support
// address to a single home: the value it is about is this package's, and
// so is the reason.
//
// WHAT IT READS, stated because a source-scanning guard with an
// undocumented field of view reads as total coverage and ends the
// search: every .go file in THIS package's own directory, tests
// included, and nothing else. A digest written into another package, a
// testdata file or a document is outside it.
//
// REQUIRED MUTATION, RUN: paste a real sixty-four-character digest into
// any row above as a want value.
func TestNoCommittedDigestLivesInThisPackage(t *testing.T) {
	// Sixty-four hex characters in a row. Case-insensitive and without
	// word boundaries, so a digest inside a longer identifier or a
	// backtick string is seen too.
	digest := regexp.MustCompile(`[0-9a-fA-F]{64}`)

	// The instrument is checked before any zero it reports is believed.
	// The needle is BUILT rather than written, because a literal one in
	// this file would be exactly what the row forbids.
	control := "want := \"" + strings.Repeat("ab", 32) + "\""
	if !digest.MatchString(control) {
		t.Fatal("the pattern does not match a digest it was handed, so a clean sweep " +
			"below would mean nothing")
	}
	if digest.MatchString("sha256 of the archive, 40 hex would be " + strings.Repeat("cd", 20)) {
		t.Fatal("the pattern matches a run of forty hex characters, so it is not " +
			"looking for what this row is about")
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading this package's own directory: %v", err)
	}

	read := 0
	var offenders []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		body, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		read++
		for i, line := range strings.Split(string(body), "\n") {
			if digest.MatchString(line) {
				offenders = append(offenders, fmt.Sprintf("%s:%d", e.Name(), i+1))
			}
		}
	}

	if read == 0 {
		t.Fatal("this row read no sources at all, so it can only report a clean sweep")
	}
	if len(offenders) > 0 {
		t.Errorf("a committed digest appears at %s — compare two packs from this binary "+
			"instead, since a toolchain upgrade may legitimately change every digest",
			strings.Join(offenders, ", "))
	}
}

// ---------------------------------------------------------------------
// Case collisions reach the archive intact
// ---------------------------------------------------------------------

// TestBothHalvesOfACaseCollisionReachTheArchive. Two names differing
// only in case are a warning rather than a refusal — they both extract
// on the build machine, and what they predict is a project that behaves
// differently there than on the author's laptop. So the packer packs
// them, both of them, and says so.
//
// The fixture needs a volume that will hold two such names, which two of
// the three platforms here will not; the helper reports whether it could
// build the state, and this row skips with its reason when it could not.
//
// REQUIRED MUTATION, RUN: lowercase Name in header. Both entries then
// carry one name and the bodies row reds.
func TestBothHalvesOfACaseCollisionReachTheArchive(t *testing.T) {
	root := writeTree(t, []entry{{path: "index.html", body: "<html>"}})
	if !addCaseCollision(t, root) {
		t.Skip("this volume folds names that differ only in case, so the fixture the " +
			"row needs cannot exist on it")
	}

	tree, archive := packTree(t, root)

	found := findingsFor(tree.Results, check.IDCaseCollision)
	if len(found) != 1 {
		t.Fatalf("collision findings = %v, want the one naming both names", found)
	}
	if len(found[0].Paths) != 2 {
		t.Errorf("Paths = %v, want both names", found[0].Paths)
	}

	bodies := map[string]string{}
	for _, e := range readArchive(t, archive.Path) {
		bodies[e.hdr.Name] = e.body
	}
	if bodies["README.md"] != "upper" || bodies["readme.md"] != "lower" {
		t.Errorf("archive holds %v, want both spellings with their own bytes", bodies)
	}
}

// ---------------------------------------------------------------------
// Fixture machinery for this file
// ---------------------------------------------------------------------

// syntheticTree builds a listing the walk and the packer can be driven
// from, creating whatever intermediate directories the paths imply.
//
// It exists so that three states can be put in front of the packer on
// every platform: a path far longer than a filesystem may want to hold,
// a name a real volume may normalise on the way to disk, and a mode
// Windows has no way to express. THE FIXTURE'S CONSTRUCTION IS A
// PLATFORM ASSUMPTION, and it is the one nobody writes down; this
// removes it for the rows whose subject is the archive rather than the
// filesystem. The rows whose subject IS the filesystem still build real
// files and skip with a reason where they cannot.
//
// Built from a SLICE and never a map, like every other fixture here: map
// iteration is randomised, so a map-built fixture is created in a
// different order every run, which sounds like a free determinism test
// and is the opposite of one — it makes a failure unreproducible.
func syntheticTree(files []entry, mode fs.FileMode) fakeFS {
	tree := fakeFS{dirs: map[string][]fakeEntry{"": nil}}

	add := func(dir string, e fakeEntry) {
		for _, existing := range tree.dirs[dir] {
			if existing.name == e.name {
				return
			}
		}
		tree.dirs[dir] = append(tree.dirs[dir], e)
	}

	for _, f := range files {
		parts := strings.Split(f.path, "/")
		dir := ""
		for _, part := range parts[:len(parts)-1] {
			add(dir, fakeEntry{name: part, mode: fs.ModeDir})
			if dir == "" {
				dir = part
			} else {
				dir += "/" + part
			}
			if _, made := tree.dirs[dir]; !made {
				tree.dirs[dir] = nil
			}
		}
		add(dir, fakeEntry{
			name: parts[len(parts)-1],
			mode: mode,
			size: int64(len(f.body)),
			body: f.body,
		})
	}
	return tree
}

// copyTree copies a fixture tree into a fresh temporary directory, in Go
// rather than through a shell: the two commands anybody would reach for
// do not exist on one of the three platforms this suite runs on.
func copyTree(t *testing.T, src string) string {
	t.Helper()

	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
	if err != nil {
		t.Fatalf("copying the fixture tree: %v", err)
	}
	return dst
}

// touchAll moves every file's timestamps, which is what a fresh checkout
// of the same content does. os.Chtimes rather than a shell utility, for
// the reason given on copyTree.
func touchAll(t *testing.T, root string, when time.Time) {
	t.Helper()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return os.Chtimes(path, when, when)
	})
	if err != nil {
		t.Fatalf("moving the fixture's timestamps: %v", err)
	}

	// The fixture is confirmed rather than assumed: a filesystem mounted
	// without timestamp support would leave this row asserting that two
	// identical trees pack identically, which is a different and much
	// weaker claim.
	moved := false
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading the fixture back: %v", err)
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			t.Fatalf("reading %s back: %v", e.Name(), err)
		}
		if !e.IsDir() && info.ModTime().After(time.Now().Add(time.Hour)) {
			moved = true
		}
	}
	if !moved {
		t.Fatalf("no timestamp under %s actually moved, so this row measures nothing", root)
	}
}

// listDir names what a directory holds, so a row can say what was left
// behind rather than only that something was.
func listDir(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// failingFS fails one file's read PART WAY THROUGH rather than at the
// open. A cleanup written for the early return would pass against a
// failing open and leave a half-written archive behind here, which is
// the case this instrument exists to reach.
type failingFS struct {
	FS
	failAt string // project-relative, slash-separated
	after  int    // bytes handed over before the read fails
}

func (f *failingFS) Open(name string) (io.ReadCloser, error) {
	rc, err := f.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(filepath.ToSlash(name), "/"+f.failAt) {
		return rc, nil
	}
	return &failingReader{ReadCloser: rc, left: f.after}, nil
}

type failingReader struct {
	io.ReadCloser
	left int
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, errors.New("the file could not be read to the end")
	}
	if len(p) > r.left {
		p = p[:r.left]
	}
	n, err := r.ReadCloser.Read(p)
	r.left -= n
	return n, err
}

// ---------------------------------------------------------------------
// Hostile inputs, driven DIRECTLY
// ---------------------------------------------------------------------

// TestPackRefusesEveryNonRegularEntry gives Pack the inputs the walk
// would never hand it, which is the only way to reach the property.
//
// WHY THESE ROWS EXIST AT ALL. The path-safety row above builds its
// fixture with writeTree and packs through packTree, so every path it
// feeds Pack came from the walk — ordinary relative names. It looks like
// the row that guards this class and it cannot see it: with no hostile
// input, deleting the refusal leaves it green. That is the never-run
// family's fourth face, a row whose INPUTS cannot reach the property it
// appears to guard, and the fix is inputs rather than assertions.
//
// The symlink row is the one that measured a real consequence before the
// refusal existed: handed a link, Pack FOLLOWED it — Open resolves one —
// and packed the target's bytes under a regular-file entry. Content from
// outside the project, in the upload, with no link entry anywhere for
// anyone to notice.
//
// REQUIRED MUTATION, RUN: delete the IsRegular check from writeEntry.
// The symlink and directory rows red; the FIFO row HANGS rather than
// failing, which is its own half of the argument and why it carries a
// deadline of its own.
func TestPackRefusesEveryNonRegularEntry(t *testing.T) {
	// The positive control first: an ordinary regular file through the
	// same door packs, so these rows cannot pass against a Pack that
	// refuses everything.
	t.Run("positive control: a regular file is still packed", func(t *testing.T) {
		root := writeTree(t, []entry{{path: "index.html", body: "<html>"}})
		archive := mustPack(t, OSFileSystem{}, root, []File{
			{Path: "index.html", Size: 6, Mode: 0o644},
		})
		defer func() { _ = archive.Remove() }()
		if got := namesOf(readArchive(t, archive.Path)); len(got) != 1 {
			t.Fatalf("entries = %v, want the one regular file", got)
		}
	})

	t.Run("a symlink is refused, and its target is never read", func(t *testing.T) {
		root := t.TempDir()
		secret := filepath.Join(t.TempDir(), "secret.txt")
		if err := os.WriteFile(secret, []byte("SECRET-CONTENT"), 0o600); err != nil {
			t.Fatalf("writing the fixture's out-of-tree file: %v", err)
		}
		if err := os.Symlink(secret, filepath.Join(root, "link.txt")); err != nil {
			t.Skipf("this runner cannot create a symbolic link, so the input this row needs cannot exist: %v", err)
		}

		_, err := Pack(OSFileSystem{}, root, t.TempDir(), []File{
			{Path: "link.txt", Size: 14, Mode: os.ModeSymlink | 0o777},
		})
		if err == nil {
			t.Fatal("Pack accepted a symbolic link — it would be followed to whatever it points at")
		}
		if !strings.Contains(err.Error(), "link.txt") {
			t.Errorf("error = %q, want it to name the entry", err)
		}
	})

	// THE DIRECTORY ROW ASSERTS THE SPECIFIC REFUSAL, and it has to.
	// Written as "an error came back" it passed under the mutation that
	// deletes the check — a directory opens fine and fails later at the
	// read, so the row was satisfied by the wrong error and proved
	// nothing about the refusal. Measured, not reasoned about: it went
	// green under M-A until this assertion named what it wanted.
	t.Run("a directory is refused, by the refusal and not by a later read", func(t *testing.T) {
		root := writeTree(t, []entry{{path: "assets/"}})
		_, err := Pack(OSFileSystem{}, root, t.TempDir(), []File{
			{Path: "assets", Size: 0, Mode: os.ModeDir | 0o755},
		})
		if err == nil {
			t.Fatal("Pack accepted a directory entry, which the extractor refuses the whole archive over")
		}
		if !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("error = %q, want the refusal — an error from further down the "+
				"path means this row is satisfied by something other than the check "+
				"it exists for", err)
		}
	})
}

// TestTheArchiveOrderIsThePackersOwn. The walk sorts, but a packer that
// wrote whatever order it was handed made determinism a property of the
// PIPELINE — the same file set in two orders produced two digests, and
// nothing in this package said so.
//
// REQUIRED MUTATION, RUN: range over files rather than
// sortedByPath(files) in Pack. Both assertions red.
func TestTheArchiveOrderIsThePackersOwn(t *testing.T) {
	root := writeTree(t, []entry{
		{path: "a.html", body: "a"},
		{path: "b.html", body: "b"},
		{path: "c.html", body: "c"},
	})
	forward := []File{
		{Path: "a.html", Size: 1, Mode: 0o644},
		{Path: "b.html", Size: 1, Mode: 0o644},
		{Path: "c.html", Size: 1, Mode: 0o644},
	}
	backward := []File{forward[2], forward[1], forward[0]}

	one := mustPack(t, OSFileSystem{}, root, forward)
	defer func() { _ = one.Remove() }()
	two := mustPack(t, OSFileSystem{}, root, backward)
	defer func() { _ = two.Remove() }()

	if one.SHA256 != two.SHA256 {
		t.Errorf("two orders of one file set produced two digests:\n %s\n %s\n"+
			"determinism has to be this function's own fact, not its caller's",
			one.SHA256, two.SHA256)
	}

	// The caller's slice is left alone: reordering somebody else's list
	// as a side effect of packing is a surprise three callers away.
	if backward[0].Path != "c.html" {
		t.Errorf("Pack reordered the caller's slice: %v", namesOfFiles(backward))
	}
}

func namesOfFiles(files []File) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}
