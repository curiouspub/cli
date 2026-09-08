package pack

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// The deterministic archive: the same project produces the same bytes.
//
// THAT IS NOT AN AESTHETIC GOAL. The same directory has to pack to the
// same bytes on two machines so that "it worked on mine and not yours"
// has one fewer cause, and so a digest can be compared across runs.
// Left to its defaults a Go tar writer records mtimes, uids, usernames
// and a gzip header timestamp — five sources of variation, none of which
// the build service reads.
//
// WHAT DETERMINISM PROMISES, AND WHERE IT STOPS. Everything below pins
// what THIS code controls: header fields, entry order, gzip metadata.
// That is enough for one build of this binary to produce identical bytes
// from identical input on any machine or operating system. It is not
// enough to promise the same bytes from a DIFFERENT build of it —
// compress/flate's encoder is an implementation rather than a specified
// format, and its output has changed across Go releases, so a toolchain
// upgrade can change every digest without a line of this package
// changing.
//
// Two consequences, both load-bearing:
//
//   - This package's suite compares two packs made by the SAME binary
//     and never a fixture against a committed digest. A committed digest
//     would turn a routine Go upgrade into a failure that looks like a
//     packing bug, and the likeliest response — updating the committed
//     value — teaches everybody to ignore the one row that would catch a
//     real regression. A guard in the suite refuses one.
//   - The SHA-256 below is an identity for THIS upload: good for
//     integrity, for matching what was sent against what arrived, and for
//     logging. It is not a cross-version content address and must not key
//     a "this content is already built" cache across releases — the same
//     project would miss the cache after an upgrade, and the reasoning
//     would look sound right up to the day it silently stopped being an
//     optimisation. If a stable content address is ever wanted, take the
//     digest over the UNCOMPRESSED tar stream: every byte of that layer
//     is fixed by the normalisation here, and only the flate encoding is
//     not.

// packedModTime is the single timestamp every entry carries.
//
// A REAL MTIME WOULD MAKE THE ARCHIVE DIFFER ON EVERY CHECKOUT, since a
// version-control checkout writes the time it happened rather than the
// time anybody edited the file. Unix epoch zero is the obvious constant
// and upsets some archive tooling, which reads it as "unset" and prints
// 1970 dates that look like corruption; 1980 is the earliest timestamp
// the other common archive format can hold, so it travels everywhere.
//
// EVERY FILE SHARING ONE TIMESTAMP IS SAFE FOR THE BUILD, and that is
// discharged rather than assumed: the service's extractor does not
// honour an entry's modification time at all, so extracted files carry
// the extraction's own wall clock and no dependency install or site
// build ever sees this value.
var packedModTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// packedMode is the mode every entry carries, and the on-disk mode is
// not consulted at all.
//
// WHY IT IS FORCED RATHER THAN PRESERVED. Preserving an executable bit
// contradicts the cross-platform claim one paragraph up: Windows has no
// execute bit to read, so the same tree packed there and on a Unix
// machine would produce different bytes for any executable file. One of
// the two had to go, and preserving a bit nothing downstream reads is
// the weaker of the pair — so byte-identity across operating systems is
// a real property here rather than an aspiration.
//
// THE CONSEQUENCE, STATED RATHER THAN BURIED: a file that was executable
// in the project arrives non-executable. A build that invokes a
// checked-in script directly would fail with a permission error; the
// workaround is the ordinary one, invoking through an interpreter, and
// the case is rare because the build runs the site generator through the
// package manager rather than executing project files itself. If it ever
// bites a real project the fix is a warning naming the file, not a
// return to mode-preservation, which would bring the divergence back.
const packedMode = 0o644

// compressionLevel is pinned BY NAME rather than left to the package
// default, because an unstated constant is one nobody reviews and this
// one is inside the digest.
//
// The archive is written once and read once by the build service, so the
// trade is bytes on the wire against a compression pass measured in
// milliseconds — which is why it is the strongest level rather than the
// fastest. CHANGING THIS SILENTLY CHANGES EVERY DIGEST: it is not a
// tuning knob, and the level is observable in the archive's second
// header byte, where the suite pins it.
const compressionLevel = gzip.BestCompression

// archivePattern names the temporary file. The suffix is last so the
// file is recognisable to anybody who finds one, and CreateTemp
// substitutes the random part for the asterisk.
const archivePattern = "curious-*.tar.gz"

// Archive is the packed project: where the bytes are, how many there
// are, what they hash to, and how many entries went in.
//
// THE CALLER OWNS THE FILE from the moment this is returned, and Remove
// is how it gives it back. A failed pack leaves nothing behind and
// returns the zero value.
type Archive struct {
	// Path is the temporary file holding the archive, created with mode
	// 0600 because it contains the user's own source, which may be
	// private.
	Path string

	// Size is the archive's length in bytes, read back from the file
	// rather than counted on the way past — the question a caller is
	// about to ask an upload about is what is on disk.
	Size int64

	// SHA256 is the hex digest of the archive's bytes, computed DURING
	// the write rather than by reading the file back. A second pass
	// would be a second answer to the same question, and the two could
	// disagree if anything touched the file in between.
	SHA256 string

	// Entries is how many files the archive holds. Directories are not
	// emitted, so this is exactly the length of the list that went in.
	Entries int
}

// Remove deletes the temporary archive. It is safe to call on a
// zero-valued Archive, so a caller can defer it beside the error check
// rather than after it.
func (a Archive) Remove() error {
	if a.Path == "" {
		return nil
	}
	if err := os.Remove(a.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing the temporary archive: %w", err)
	}
	return nil
}

// Pack writes files into one deterministic gzipped tar and returns it.
//
// THE FILE LIST IS THE WALK'S, PASSED IN RATHER THAN RE-DISCOVERED. The
// walk is the single canonical scan this package promises, and a packer
// that walked again could disagree with the list the user was shown and
// consented to — which is the one difference nobody would think to look
// for.
//
// REGULAR FILES AND NOTHING ELSE GO IN. No directory entries, because
// extraction creates parents and an empty directory carries nothing a
// site build reads; and no link entries of any kind, because a link
// entry pointing outside the extraction root is precisely the payload
// the service's extractor is hardened against, and a client that
// routinely emitted them would make every real attack look like ordinary
// traffic. The walk never puts a link in the file list, and this loop
// could not emit one if it did.
//
// dir is where the temporary archive is created; empty means the
// system's temporary directory, which is what the CLI passes. It is a
// parameter because the archive is a file on a real volume: a caller
// with a reason to choose the volume can, and this package's own suite
// uses a directory it owns so that "nothing was left behind" is a
// question it can actually answer.
//
// NOTHING SURVIVES A FAILURE. Every error path removes the file before
// returning, so a caller that got an error has nothing to clean up and
// no path to clean it up with.
func Pack(fsys FS, root, dir string, files []File) (Archive, error) {
	// CreateTemp makes the file with mode 0600, which is the mode this
	// archive wants for the reason given on Archive.Path — noted here
	// because it is a property inherited from the standard library
	// rather than requested, and the suite pins it either way.
	f, err := os.CreateTemp(dir, archivePattern)
	if err != nil {
		return Archive{}, fmt.Errorf("creating the temporary archive: %w", err)
	}

	name := f.Name()
	packed := false
	defer func() {
		// Close is called on every path. On the success path the file is
		// already closed and this second call fails harmlessly; the
		// alternative is a close that happens on some paths and not
		// others, which is how a descriptor leaks on the branch nobody
		// exercised.
		_ = f.Close()
		if !packed {
			_ = os.Remove(name)
		}
	}()

	digest := sha256.New()
	zw, err := gzip.NewWriterLevel(io.MultiWriter(f, digest), compressionLevel)
	if err != nil {
		return Archive{}, fmt.Errorf("preparing the archive: %w", err)
	}
	// The gzip header's own metadata, stated. A gzip header can carry the
	// original file name, a comment and the time of writing, and any of
	// the three would reintroduce exactly the nondeterminism the tar
	// layer removes.
	//
	// IT RESTATES WHAT THE LIBRARY ALREADY DEFAULTS TO, so deleting this
	// line changes nothing today and its absence is invisible. It is here
	// because these bytes are inside the digest and a default is not a
	// decision: the day a default changes, this file is where the change
	// has to be argued with, rather than a digest quietly moving under
	// everybody.
	zw.Header = gzip.Header{OS: gzipOSUnknown}

	tw := tar.NewWriter(zw)
	for _, file := range files {
		if err := writeEntry(tw, fsys, root, file); err != nil {
			return Archive{}, err
		}
	}

	// Three closes in order, each checked. A tar writer flushes its
	// trailer on Close and the gzip writer its own; ignoring either would
	// mean returning a digest for an archive that is missing its end.
	if err := tw.Close(); err != nil {
		return Archive{}, fmt.Errorf("finishing the archive: %w", err)
	}
	if err := zw.Close(); err != nil {
		return Archive{}, fmt.Errorf("finishing the archive: %w", err)
	}
	if err := f.Close(); err != nil {
		return Archive{}, fmt.Errorf("writing the archive to disk: %w", err)
	}

	info, err := os.Stat(name)
	if err != nil {
		return Archive{}, fmt.Errorf("reading back the archive: %w", err)
	}

	packed = true
	return Archive{
		Path:    name,
		Size:    info.Size(),
		SHA256:  hex.EncodeToString(digest.Sum(nil)),
		Entries: len(files),
	}, nil
}

// gzipOSUnknown is the "operating system unknown" byte. Naming the
// producing system in the header would make the archive differ between
// machines for no reason a reader of it could use.
const gzipOSUnknown = 255

// writeEntry writes one file's header and bytes.
//
// THE FILE IS OPENED BEFORE THE HEADER IS WRITTEN, so a file that
// vanished between the walk and the pack fails before anything is
// committed to the stream rather than after a header promising bytes
// that never arrive.
func writeEntry(tw *tar.Writer, fsys FS, root string, file File) error {
	rc, err := fsys.Open(filepath.Join(root, filepath.FromSlash(file.Path)))
	if err != nil {
		return fmt.Errorf("reading %s: %w", file.Path, err)
	}
	defer rc.Close()

	if err := tw.WriteHeader(header(file)); err != nil {
		return fmt.Errorf("writing the archive entry for %s: %w", file.Path, err)
	}

	// A SIZE MISMATCH IS ITS OWN DIAGNOSIS, in both directions. The
	// header promised the length the walk measured, so a file edited
	// between the two produces either a tar writer refusing the extra
	// bytes or a short copy — and "your file changed while it was being
	// packed" is a sentence somebody can act on, where the library's own
	// message is not.
	n, err := io.Copy(tw, rc)
	if errors.Is(err, tar.ErrWriteTooLong) {
		return fmt.Errorf("%s grew while it was being packed; run `curious deploy` again", file.Path)
	}
	if err != nil {
		return fmt.Errorf("reading %s: %w", file.Path, err)
	}
	if n != file.Size {
		return fmt.Errorf("%s changed while it was being packed; run `curious deploy` again", file.Path)
	}
	return nil
}

// header is the normalisation table, and it is THE ONLY PLACE IN THIS
// PACKAGE A HEADER IS BUILT. One site is what lets the suite drive the
// table directly, field by field, instead of inferring each decision
// from the bytes that came out the other end.
//
// THE ZERO-VALUED FIELDS ARE SPELLED OUT ON PURPOSE. Uid, Gid, Uname and
// Gname are what a header built from a FileInfo would carry, and they
// are the fields that make an archive differ between two people's
// machines. A field absent from this literal reads as an oversight; a
// field written as zero reads as the decision it is.
//
// FORMAT IS SET RATHER THAN INFERRED, AND WHAT THAT BUYS IS NARROWER
// THAN IT LOOKS. Measured against the toolchain this builds with: naming
// this format does NOT make every entry encode the same way. The
// standard writer reads a request for it as "and the plainer encoding is
// still allowed", so a name inside a hundred bytes gets the plain
// header, a longer one gets split across two fields, and only a name
// that fits neither is written with an extended record. The encoding
// still varies with how deep a project nests, and no assertion about the
// archive's own bytes can see this line at all — which is why the row
// that pins it pins the header this package BUILDS.
//
// What the pin does buy is worth the line, and it is about the day
// somebody adds a field here rather than about today: it rules out the
// third format a writer may otherwise reach for, and it turns a header
// this package cannot encode in the named format into an error instead
// of a silent switch to a different one.
//
// WHAT IT DOES NOT DO IS REWRITE THE NAME. The path arrives from the
// walk already project-relative and slash-separated, and it goes into
// the archive byte for byte: no case folding, no Unicode normalisation,
// no cleaning. Two spellings of a name that look identical on screen are
// different files, and a packer that quietly made them one would be
// undetectable from outside.
func header(f File) *tar.Header {
	return &tar.Header{
		Name:       f.Path,
		Typeflag:   tar.TypeReg,
		Mode:       packedMode,
		Size:       f.Size,
		ModTime:    packedModTime,
		AccessTime: time.Time{},
		ChangeTime: time.Time{},
		Uid:        0,
		Gid:        0,
		Uname:      "",
		Gname:      "",
		Format:     tar.FormatPAX,
	}
}
