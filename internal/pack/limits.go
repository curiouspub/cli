package pack

import (
	"compress/gzip"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/curiouspub/cli/internal/check"
	"github.com/curiouspub/cli/internal/units"
	"github.com/curiouspub/cli/pkg/wire"
)

// The local limits: how many files a project may send, how big any one
// of them may be, how big they may be together, and how big the archive
// may be once they are packed.
//
// THE FIRST THREE ARE MEASURED BEFORE ANYTHING IS PACKED, and that
// ordering is the product rather than an optimisation. Compressing 30 MB
// to discover it was never going to be allowed spends the user's time at
// the one point in the flow where they are watching a progress line, and
// spends it on an answer that was available from the file list alone.
//
// THE NUMBERS COME FROM THE PUBLIC CONTRACT and are not restated here.
// They are the same numbers the server validates against, and a client
// holding its own copy is a client that disagrees with the boundary at
// exactly the byte where the disagreement is least explainable.
//
// THEY ALWAYS RUN, AND ONLY THEIR VERDICTS ARE CONDITIONAL. Arithmetic
// over a list already in memory costs nothing to perform and cannot be
// skipped for free: the combiner refuses a report that does not cover
// every declared check, so a run that measured nothing could not build a
// report at all — including the report carrying whatever hard stop
// caused it to give up. A completeness gate that refuses its own
// caller's honest report is the enforcement eating the thing it
// protects, and the fix belongs upstream of the gate rather than inside
// it as an exception.

// The number of offenders a message lists before it stops naming them.
//
// TEN IS A LIST SOMEBODY READS; forty is a wall they scroll past, and
// the fortieth file is not more actionable than the first. The count of
// the rest is still stated, because "and 30 more" and a list of ten are
// different situations and a reader who cannot tell them apart will fix
// ten files and run again.
const listedOffenders = 10

// The number of directories the too-many-files message names, and the
// number of poorly-compressing files the packed-size message names.
//
// FIVE RATHER THAN TEN because these are not offenders to be fixed one
// by one — they are a pointer at where the weight is. One line saying
// most of the files are under a gallery directory ends the conversation;
// five is enough for a tree with several such places and few enough that
// the useful line is still visible.
const listedContributors = 5

// limitIDs are the check ids this file claims, in report order. They are
// declared in the result package and only referenced here, for the same
// reason the walk's three are: the set has to be enumerable from one
// place.
var limitIDs = []string{
	check.IDLimitFiles,
	check.IDLimitFileSize,
	check.IDLimitTotal,
	check.IDLimitPacked,
}

// Prepared is what a run that passed every local limit is holding: the
// archive, and the one line that says what is about to leave the
// machine.
//
// A run that was refused gets the zero value, whose Archive has no Path
// — so "was anything packed" is a question about the value rather than
// about which branch the caller believed it took.
type Prepared struct {
	Archive Archive

	// Receipt is the success summary: file count, source total, archive
	// size. It is the receipt for an upload that is about to happen, and
	// it is empty on every path that will not happen.
	Receipt string
}

// Limits measures the three source limits over a walked file list and
// reports all four limit rows.
//
// IT REPORTS THE PACKED ROW TOO, as a decline, and that is what makes it
// usable for the report a person sees before anything is packed. The
// combined report has to carry a row for every declared check; the
// packed size is a fact about an artefact that does not exist yet, so
// the honest row says the check did not answer rather than answering
// with a zero.
//
// The decline is BY DESIGN rather than environmental, which decides what
// it costs: nothing outside the check went wrong, nothing about it is
// the user's to fix, and a surface that stopped to ask somebody about it
// would be asking them to decide about the passage of time.
func Limits(files []File) check.Results {
	findings := sourceFindings(files)
	manifest := check.Manifest{
		answered(check.IDLimitFiles),
		answered(check.IDLimitFileSize),
		answered(check.IDLimitTotal),
		declined(check.IDLimitPacked, check.ByDesign,
			"nothing has been packed yet, so there is no archive to measure"),
	}
	check.SortFindings(findings)
	return check.Results{Findings: findings, Manifest: manifest}
}

// Prepare is the whole local gate between a walked project and an
// upload: the three source limits, then — only if they passed — the
// archive, then the packed-size limit, then the receipt.
//
// IT IS ONE FUNCTION RATHER THAN AN ORDER A CALLER IS TRUSTED TO KEEP,
// and the order is the reason. Both the packed-size check and the
// receipt happen after packing; written the other way round a refused
// run prints a receipt for an upload that will not happen, directly
// above the refusal — a transcript contradicting itself in two
// consecutive lines. With both inside one function the receipt cannot be
// reached without the check having already passed, so the rule is a
// property of the type rather than a sentence somebody has to remember.
//
// NOTHING IS LEFT ON DISK BY A REFUSAL. A run stopped by a source limit
// never creates the archive; a run stopped by the packed size removes
// the one it made. A caller that got a zero Prepared has nothing to
// clean up, which is the same promise Pack makes about its own failures.
//
// The error return is for I/O alone — a file that vanished, a disk that
// filled. A project that is simply too large is not an error here: it is
// a finding, and the caller renders it with everything else.
func Prepare(fsys FS, root, dir string, files []File) (Prepared, check.Results, error) {
	findings := sourceFindings(files)
	manifest := check.Manifest{
		answered(check.IDLimitFiles),
		answered(check.IDLimitFileSize),
		answered(check.IDLimitTotal),
	}

	if len(findings) > 0 {
		manifest = append(manifest, declined(check.IDLimitPacked, check.ByDesign,
			"the project was not packed, because it is over a limit above"))
		check.SortFindings(findings)
		return Prepared{}, check.Results{Findings: findings, Manifest: manifest}, nil
	}

	archive, err := Pack(fsys, root, dir, files)
	if err != nil {
		return Prepared{}, check.Results{}, err
	}
	manifest = append(manifest, answered(check.IDLimitPacked))

	if archive.Size > wire.MaxPackedBytes {
		// THE ARCHIVE GOES BEFORE THE MESSAGE IS BUILT, so a failure to
		// remove it is reported instead of being hidden behind a
		// finding the caller was going to render anyway.
		if removeErr := archive.Remove(); removeErr != nil {
			return Prepared{}, check.Results{}, removeErr
		}
		findings = append(findings, packedFinding(fsys, root, files, archive))
		check.SortFindings(findings)
		return Prepared{}, check.Results{Findings: findings, Manifest: manifest}, nil
	}

	return Prepared{Archive: archive, Receipt: receipt(files, archive)},
		check.Results{Manifest: manifest}, nil
}

// declined builds a manifest row for a check that did not answer.
//
// IT GOES THROUGH answered RATHER THAN WRITING ITS OWN LITERAL, which
// keeps the row's single construction site single. A second literal
// would compile, behave identically today, and quietly become the place
// the next change to the row's shape forgets — which is the whole reason
// this package's own suite reads its source and counts them.
func declined(id string, kind check.DeclineKind, reason string) check.Status {
	row := answered(id)
	row.Outcome = check.Declined
	row.Kind = kind
	row.Reason = reason
	return row
}

// sourceFindings is the three pre-pack limits, in declared order.
//
// ALL THREE ARE EVALUATED, ALWAYS. A project with four oversize files
// AND too many of them learns both facts from one run; reporting the
// first and stopping is the round-trip this whole surface exists to
// prevent.
func sourceFindings(files []File) []check.Finding {
	var out []check.Finding
	if f, over := fileCountFinding(files); over {
		out = append(out, f)
	}
	if f, over := fileSizeFinding(files); over {
		out = append(out, f)
	}
	if f, over := totalSizeFinding(files); over {
		out = append(out, f)
	}
	return out
}

// fileCountFinding is the too-many-files hard stop.
//
// THE COUNT IS OVER THE WALK'S LIST, which is the list that would be
// packed: directories are not in it, skipped links are not in it, and
// neither is anything the forced set or the project's own ignore rules
// removed. Counting anything else would refuse projects that were always
// going to be fine, and this client is the stricter of the two ends by
// construction — so a client that is wrong is wrong in a direction
// nobody can appeal.
func fileCountFinding(files []File) (check.Finding, bool) {
	if len(files) <= wire.MaxSourceFiles {
		return check.Finding{}, false
	}

	headline := fmt.Sprintf("This project has %s files, and %s is the most one deploy can carry.",
		units.Count(len(files)), units.Count(wire.MaxSourceFiles))

	var b strings.Builder
	b.WriteString(headline)
	b.WriteString("\n\nThe directories holding the most of them:\n")
	for _, d := range topDirectories(files) {
		fmt.Fprintf(&b, "\n  %8s  %s", units.Count(d.count), d.dir)
	}

	return check.Finding{
		CheckID:  check.IDLimitFiles,
		Severity: check.SeverityHardStop,
		Message:  headline,
		What:     b.String(),
		Why:      alreadyExcluded(),
		Next: "Add whatever the site does not need to .gitignore, then run " +
			"`curious deploy` again.",
	}, true
}

// fileSizeFinding is the oversize-file hard stop, and it names EVERY
// offender in one finding rather than one finding each.
//
// The reason is the same one that made the walk's collision check report
// a group: the sentence "these are bigger than the limit" is one
// sentence, and printing it once per file turns a useful message into a
// wall on the project that has forty of them. It is the opposite call
// from the name-charset check next door, and the difference is that that
// check's REASON differs per file — a space and a character outside
// ASCII are different problems — while here the reason is identical and
// only the number changes.
func fileSizeFinding(files []File) (check.Finding, bool) {
	over := make([]File, 0, len(files))
	for _, f := range files {
		if f.Size > wire.MaxSourceFileBytes {
			over = append(over, f)
		}
	}
	if len(over) == 0 {
		return check.Finding{}, false
	}
	sortBySizeThenPath(over)

	headline := fmt.Sprintf("%s larger than %s, which is the most any single file may be.",
		countOf(len(over), "file is", "files are"), units.Bytes(wire.MaxSourceFileBytes))

	listed := capped(over)
	return check.Finding{
		CheckID:  check.IDLimitFileSize,
		Severity: check.SeverityHardStop,
		Message:  headline,
		// THE TABLE IS NOT WRITTEN HERE ANY MORE. This used to lay the
		// offenders out in What and also set Paths, so the renderer
		// printed them twice — once measured, once bare. The facts now
		// travel as facts and one renderer formats them, which is also
		// what puts a number on the agent-facing surface instead of an
		// English sentence a machine has to parse back.
		What:  headline + "\n\nLargest first:" + andMore(len(over)),
		Paths: check.NewPaths(pathsOfFiles(listed)...),
		Sizes: sizesOfFiles(listed),
		Why:   alreadyExcluded(),
		Next: "Shrink or remove each one, or add it to .gitignore if the site does not " +
			"need it, then run `curious deploy` again.",
	}, true
}

// totalSizeFinding is the whole-project hard stop.
//
// IT SUMS THE UNCOMPRESSED SIZES, deliberately, and that makes this
// client the stricter of the two ends: a tree at this limit always
// compresses to something the server will accept, so a project refused
// here would have been accepted there. The alternative — measuring what
// the upload would weigh — means packing first, which is exactly the
// wait this check exists to spare somebody, and it means a project that
// happens to compress well can be dozens of times over a limit the
// message would then have to explain.
func totalSizeFinding(files []File) (check.Finding, bool) {
	total := totalSize(files)
	if total <= wire.MaxSourceTotalBytes {
		return check.Finding{}, false
	}

	largest := append([]File(nil), files...)
	sortBySizeThenPath(largest)

	headline := fmt.Sprintf(
		"This project is %s of source, and %s is the most one deploy can carry.",
		units.Bytes(total), units.Bytes(wire.MaxSourceTotalBytes))

	listed := capped(largest)
	return check.Finding{
		CheckID:  check.IDLimitTotal,
		Severity: check.SeverityHardStop,
		Message:  headline,
		What:     headline + "\n\nThe largest files:" + andMore(len(largest)),
		Paths:    check.NewPaths(pathsOfFiles(listed)...),
		Sizes:    sizesOfFiles(listed),
		Why:      alreadyExcluded(),
		Next: "Add whatever the site does not need to .gitignore, or move large assets " +
			"out of the project, then run `curious deploy` again.",
	}, true
}

// packedFinding is the one refusal a user can reach having passed every
// other check here, which is why its message does more explaining than
// the rest.
//
// WHEN THE SOURCES FIT AND THE ARTEFACT DOES NOT, the numbers look like
// a contradiction — "my files are 30 MB and you say 32 MB" — so the
// message names the overflow as the archive's own overhead rather than
// as their content. Every entry costs a record of a few hundred bytes on
// top of its bytes, and a file that does not compress cannot pay that
// back; three thousand incompressible small files at exactly the source
// limit is the shape that reaches here, and nothing the author did was
// wrong.
//
// THE ACTIONABLE HALF IS THE FILE LIST. "It is 2 MB over" leaves a
// reader with nothing to do; the files that compressed least are the
// ones worth removing or converting, and they are found by compressing
// each one on its own — which costs a pass over the tree and happens
// only on a path that has already failed.
func packedFinding(fsys FS, root string, files []File, archive Archive) check.Finding {
	headline := fmt.Sprintf(
		"The packed archive is %s, and %s is the most one deploy can carry.",
		units.Bytes(archive.Size), units.Bytes(wire.MaxPackedBytes))

	var b strings.Builder
	b.WriteString(headline)
	fmt.Fprintf(&b, "\n\nYour files are %s together, so the difference is the archive's own "+
		"overhead:\neach file costs a record on top of its content, and a file that does "+
		"not\ncompress cannot pay that back.", units.Bytes(totalSize(files)))

	stubborn := leastCompressible(fsys, root, files)
	var paths []string
	if len(stubborn) > 0 {
		b.WriteString("\n\nThe files that compressed least, on their own:\n")
		for _, s := range stubborn {
			fmt.Fprintf(&b, "\n  %10s of %-10s %s",
				units.Bytes(s.packed), units.Bytes(s.file.Size), s.file.Path)
			paths = append(paths, s.file.Path)
		}
	}

	return check.Finding{
		CheckID:  check.IDLimitPacked,
		Severity: check.SeverityHardStop,
		Message:  headline,
		What:     b.String(),
		Paths:    check.NewPaths(paths...),
		Why:      alreadyExcluded(),
		Next: "Remove one of those files, convert it to a format that compresses, or add " +
			"it to .gitignore if the site does not need it, then run `curious deploy` again.",
	}
}

// receipt is the success line: what is about to leave the machine.
//
// IT NAMES ALL THREE NUMBERS because they answer different questions. A
// reader wanting to know whether their ignore rules worked reads the
// count; one wondering why the upload is slow reads the archive size;
// and the pair together is what makes the difference between the two
// visible rather than something to be inferred from a progress bar.
func receipt(files []File, archive Archive) string {
	// "an archive of X" rather than "a X archive", because the article
	// would have to agree with a number nobody can predict: "a 8.4 kB
	// archive" is what the obvious wording prints, and an English rule
	// applied to a rendered value is a rule that is wrong some of the
	// time and looks like a typo every time.
	return fmt.Sprintf("Packed %s — %s of source — into an archive of %s.",
		countOf(len(files), "file", "files"),
		units.Bytes(totalSize(files)), units.Bytes(archive.Size))
}

// alreadyExcluded is the sentence every limit message carries, and it
// renders the walk's forced set rather than naming it again.
//
// IT IS IN EVERY ONE OF THEM because it is the first thing anybody
// wonders. Told their project has too many files, a reader's next
// thought is whether the dependency directory was counted — and a
// message that leaves them to find out ends with them checking, which is
// a round-trip a sentence could have prevented.
func alreadyExcluded() string {
	names := forcedExcludeNames()
	return "Already left out: " + strings.Join(names, ", ") +
		", and anything your .gitignore files exclude."
}

// dirCount is one directory and how many of the walked files sit
// directly inside it.
type dirCount struct {
	dir   string
	count int
}

// topDirectories is where the files are, worst first.
//
// THE IMMEDIATE PARENT IS THE UNIT, not every ancestor. A reader is
// deciding what to put in an ignore file, and the directory a rule would
// name is the one holding the files — attributing a gallery's thousand
// images to the project root as well would put the root at the top of
// every list and say nothing.
//
// TIES BREAK BY PATH, and that is not a detail. Several directories with
// the same count is the ordinary shape of a generated tree, and an
// unstated tie-break is a message that differs between two runs over one
// unchanged project — in a package whose whole promise is that it
// answers the same way twice.
func topDirectories(files []File) []dirCount {
	counts := map[string]int{}
	for _, f := range files {
		counts[displayParent(f.Path)]++
	}

	out := make([]dirCount, 0, len(counts))
	for dir, n := range counts {
		out = append(out, dirCount{dir: dir, count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].dir < out[j].dir
	})
	if len(out) > listedContributors {
		out = out[:listedContributors]
	}
	return out
}

// displayParent names the directory a file sits in, as an ignore rule
// would have to write it. A file at the top level has no directory to
// name, and saying so in words beats printing the dot the standard
// library returns.
func displayParent(p string) string {
	dir := path.Dir(p)
	if dir == "." || dir == "/" {
		return "the project root"
	}
	return dir + "/"
}

// compressed is one file measured on its own: what it weighs on disk,
// and what it weighs after the same compression the archive uses.
type compressed struct {
	file   File
	packed int64
}

// leastCompressible ranks files by how little compression helped them.
//
// EACH FILE IS COMPRESSED ALONE, at the archive's own level, which is an
// approximation of its contribution rather than a measurement of it: the
// real archive compresses one stream, so a file's bytes there are helped
// by whatever preceded it. The approximation is the right one for this
// message — it separates "this file is already compressed" from "this
// file is text", which is the distinction a reader can act on — and it
// is stated here rather than implied, because a number presented as
// exact and derived differently is worse than an honest estimate.
//
// A FILE IT CANNOT READ IS SKIPPED RATHER THAN GUESSED AT. This runs on
// a path that is already failing, and a message is not worth turning a
// refusal into an I/O error the reader cannot connect to anything.
func leastCompressible(fsys FS, root string, files []File) []compressed {
	measured := make([]compressed, 0, len(files))
	for _, f := range files {
		if f.Size == 0 {
			continue
		}
		size, err := compressedSize(fsys, root, f)
		if err != nil {
			continue
		}
		measured = append(measured, compressed{file: f, packed: size})
	}

	// Least compressible first — the highest packed-to-disk ratio —
	// then the largest, then by path so two runs over one project agree.
	sort.Slice(measured, func(i, j int) bool {
		ri := float64(measured[i].packed) / float64(measured[i].file.Size)
		rj := float64(measured[j].packed) / float64(measured[j].file.Size)
		if ri != rj {
			return ri > rj
		}
		if measured[i].file.Size != measured[j].file.Size {
			return measured[i].file.Size > measured[j].file.Size
		}
		return measured[i].file.Path < measured[j].file.Path
	})
	if len(measured) > listedContributors {
		measured = measured[:listedContributors]
	}
	return measured
}

func compressedSize(fsys FS, root string, f File) (int64, error) {
	rc, err := fsys.Open(filepath.Join(root, filepath.FromSlash(f.Path)))
	if err != nil {
		return 0, err
	}
	defer rc.Close()

	counter := &byteCounter{}
	zw, err := gzip.NewWriterLevel(counter, compressionLevel)
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(zw, rc); err != nil {
		return 0, err
	}
	if err := zw.Close(); err != nil {
		return 0, err
	}
	return counter.n, nil
}

// byteCounter is a sink that keeps the length and nothing else. The
// bytes themselves are of no interest here — a second copy of the user's
// source in memory would be the whole project again, on the one path
// where the project is already known to be enormous.
type byteCounter struct{ n int64 }

func (c *byteCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// sizedList renders "size  path" lines, capped, with a count of the rest.
//
// THE SIZE COMES FIRST so the numbers form a column a reader can scan
// down. Paths vary in length and putting them first pushes every number
// to a different place on the line, which is the difference between a
// list and a table.
// sizesOfFiles is the measurement half of a finding's path list, in the
// same order, so the two can be read by index.
func sizesOfFiles(files []File) []int64 {
	sizes := make([]int64, len(files))
	for i, f := range files {
		sizes[i] = f.Size
	}
	return sizes
}

// andMore is the sentence that follows a capped list, or nothing.
//
// It stays in the copy rather than moving to the renderer with the
// table, because it is a statement about what this check MEASURED and
// chose not to list. The renderer knows how many paths it was handed; it
// does not know how many there were.
func andMore(total int) string {
	if rest := total - listedOffenders; rest > 0 {
		return fmt.Sprintf("\n\n(and %s more, not listed)", units.Count(rest))
	}
	return ""
}

func capped(files []File) []File {
	if len(files) > listedOffenders {
		return files[:listedOffenders]
	}
	return files
}

// sortBySizeThenPath orders files largest first, in place, breaking ties
// by path for the same reason the directory list does: equal sizes are
// ordinary — a generated tree is full of them — and an unstated
// tie-break is a message that changes between two runs over one project.
func sortBySizeThenPath(files []File) {
	sort.Slice(files, func(i, j int) bool {
		if files[i].Size != files[j].Size {
			return files[i].Size > files[j].Size
		}
		return files[i].Path < files[j].Path
	})
}

func pathsOfFiles(files []File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func totalSize(files []File) int64 {
	var total int64
	for _, f := range files {
		total += f.Size
	}
	return total
}
