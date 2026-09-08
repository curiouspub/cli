package preflight

import (
	"bytes"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/curiouspub/cli/internal/check"
)

// devURLNeedle is the exact string the pre-flight table names, and it is
// ONE literal on purpose.
//
// It was first written as two constants joined at package level, because
// no file this repository ships may carry a compiled-in URL and the
// guard that enforces that fired on the honest spelling. That was the
// wrong repair. Splitting a URL until no piece looks like one is the
// technique the guard's own diagnostic names as the way past it, and a
// value spelled with two names is no less compiled in than a value
// spelled with one — it is only harder to grep for, which is the property
// the rule exists to protect. The guard has since learned to fold
// constants, so the two-constant form no longer passes either.
//
// This string is a NEEDLE and never a destination: nothing in this
// package imports a network package, and the only thing done with it is a
// substring comparison against bytes read off the user's own disk. That
// argument was always the right one — it just belonged at the guard's
// allow-list, where it is now recorded, rather than in a spelling that
// routed around the check.
//
// ONE STRING AND NOT A FAMILY. A secure scheme on the same host, and the
// loopback address written as numbers, are deliberately NOT matched: the
// table names one string, this check is already the noisiest of the
// four, and every extra pattern adds false positives to a prompt people
// are learning to dismiss. Nothing is filtered on the other side either
// — no skipping comments, no reading development-only branches — because
// half-clever filtering hides the one real hit while still missing
// others, and the warning severity was chosen with that noise in mind.
const devURLNeedle = "http://localhost"

// scannedExtensions are the file types this check reads, lower-cased.
//
// THE TABLE SAYS ".astro/.js SOURCES" AND THIS IS WIDER, deliberately.
// Taken literally that misses TypeScript, JSX and every framework
// component, which is most of a modern Astro project. This widening
// costs a prompt when it is wrong rather than a refusal, which is why it
// is safe to make here and was not safe to make on the dependency check.
//
// Markdown is EXCLUDED, and that is the other half of the same
// judgement: prose about running something locally is the single biggest
// source of false hits, and a link inside a blog post is not a
// production hazard worth stopping anybody for.
var scannedExtensions = map[string]bool{
	".astro":  true,
	".js":     true,
	".mjs":    true,
	".cjs":    true,
	".jsx":    true,
	".ts":     true,
	".mts":    true,
	".cts":    true,
	".tsx":    true,
	".vue":    true,
	".svelte": true,
}

const (
	// maxScannedFileBytes is the size past which a file is not
	// hand-written source. A minified vendor bundle checked into a
	// repository is the everyday case, and reading it would cost more
	// than the rest of this program put together while finding nothing
	// anybody wrote.
	maxScannedFileBytes = 1 << 20

	// maxReportedHits is how many hits are listed before the rest are
	// counted. A project that hard-coded a development URL in two
	// hundred places needs the first ten and the number, not two hundred
	// lines of scrollback.
	maxReportedHits = 10

	// maxQuotedLineRunes bounds how much of a matching line is quoted
	// back. A single line can be the whole file — that is what minified
	// means — and a finding is read by a person and rendered into a
	// result an agent parses, neither of which wants half a megabyte on
	// one line.
	maxQuotedLineRunes = 120
)

// LocalhostCheck is the localhost row as the engine registers it, over
// the file list it is given.
//
// THE LIST IS A PARAMETER BECAUSE THE ENGINE'S SEAM DOES NOT CARRY ONE.
// A check is handed a filesystem and a root and nothing else, and the
// walk that produces the canonical file list deliberately sits outside
// the engine — so the list reaches this check by being closed over at
// registration. Scanning the walk's output rather than the raw tree is
// what keeps the dependency directory, the build output and everything
// the project's own ignore rules exclude out of the scan; those are
// where a hit would be both slow to find and irrelevant.
//
// It takes plain strings rather than the walk's own file type so that
// this package does not import the walk. There is no cycle either way;
// what a shared type would cost is the property that neither producer of
// findings owns the other — and a test can then drive this check from a
// literal slice instead of arranging a real traversal.
//
// The paths are project-relative and slash-separated on every platform,
// as the walk emits them. They are joined back into host form before
// anything touches the filesystem: the matrix this ships on includes
// Windows, where a slash-separated path is not what the operating system
// is being asked about.
func LocalhostCheck(files []string) Check {
	return Check{
		IDs: []string{check.IDLocalhost},
		Run: func(fsys FS, root string) Result {
			return scanForDevURLs(fsys, root, files)
		},
	}
}

// scanForDevURLs reads every scannable file in the list and reports the
// hard-coded development URLs it finds.
func scanForDevURLs(fsys FS, root string, files []string) Result {
	var findings []check.Finding
	reported, total := 0, 0

	for _, rel := range files {
		if !scannedExtensions[strings.ToLower(path.Ext(rel))] {
			continue
		}

		data, note := readScannable(fsys, root, rel)
		if note != nil {
			findings = append(findings, *note)
			continue
		}
		if data == nil || !bytes.Contains(data, []byte(devURLNeedle)) {
			continue
		}

		for number, line := range bytes.Split(data, []byte("\n")) {
			if !bytes.Contains(line, []byte(devURLNeedle)) {
				continue
			}
			total++
			if reported >= maxReportedHits {
				continue
			}
			reported++
			findings = append(findings, check.Finding{
				CheckID:  check.IDLocalhost,
				Severity: check.SeverityWarning,
				Message:  fmt.Sprintf("%s:%d: %s", rel, number+1, quoteLine(line)),
				Paths:    check.NewPaths(rel),
			})
		}
	}

	if total > reported {
		findings = append(findings, check.Finding{
			CheckID:  check.IDLocalhost,
			Severity: check.SeverityWarning,
			Message: fmt.Sprintf("and %d more %s reference%s in this project.",
				total-reported, devURLNeedle, plural(total-reported)),
		})
	}

	return Result{Findings: findings}
}

// readScannable returns the bytes of one file, or a NOTE saying it could
// not be read, or neither when the file is simply too big to be source.
//
// A FILE THAT COULD NOT BE READ IS RECORDED RATHER THAN SKIPPED, and
// that is the distinction this whole result model is built around: "I
// scanned everything and found nothing" and "one file defeated me" are
// different facts, and silently merging them makes a clean verdict
// partly unearned. It is a note rather than a warning because of the
// standing criterion that an advisory names something the reader can act
// on or observe — a file that vanished between the walk and the scan is
// neither — so the fact is kept in the structured result for a caller
// that asks and shown by no surface by default.
//
// The size gate is not a note, and the asymmetry is deliberate: a
// minified bundle is EXPECTED to be skipped and reporting it would fire
// on ordinary, healthy projects.
func readScannable(fsys FS, root, rel string) ([]byte, *check.Finding) {
	name := filepath.Join(root, filepath.FromSlash(rel))

	info, err := fsys.Stat(name)
	if err != nil {
		return nil, unreadableNote(rel)
	}
	if info.Size() > maxScannedFileBytes {
		return nil, nil
	}

	data, err := readCapped(fsys, name, maxScannedFileBytes)
	if err != nil {
		return nil, unreadableNote(rel)
	}
	return data, nil
}

// unreadableNote is the record that one file in the list was not
// actually scanned.
func unreadableNote(rel string) *check.Finding {
	return &check.Finding{
		CheckID:  check.IDLocalhost,
		Severity: check.SeverityNote,
		Message:  "couldn't read " + rel + ", so it was not scanned.",
		Paths:    check.NewPaths(rel),
	}
}

// quoteLine renders a matching line for a reader: trimmed of the
// surrounding whitespace and of the carriage return a file written on
// Windows carries, and bounded in length.
//
// The bound counts RUNES rather than bytes, so a line of accented text
// is cut where a reader would expect and never in the middle of a
// character — a truncated multi-byte sequence renders as a replacement
// glyph, which looks like corrupted output rather than like a long line.
func quoteLine(line []byte) string {
	trimmed := strings.TrimSpace(strings.TrimSuffix(string(line), "\r"))
	runes := []rune(trimmed)
	if len(runes) <= maxQuotedLineRunes {
		return trimmed
	}
	return string(runes[:maxQuotedLineRunes]) + "…"
}

// plural is the "s" on a counted noun. Nothing here ever counts one —
// the summary finding only exists past the cap — but a message that
// would read "1 more references" if it ever did is a message waiting to
// embarrass somebody.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
