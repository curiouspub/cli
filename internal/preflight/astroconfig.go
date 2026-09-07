package preflight

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/curiouspub/cli/internal/check"
)

// The two check ids this file emits are declared in the result package,
// with the rest of them, and both are warning-only. pages-dir resolves a
// custom srcDir out of astro.config when the default pages directory is
// missing, and warns — never blocks — when it still can't find a pages
// directory once srcDir is accounted for: a real Astro build with no
// pages directory does not fail, so refusing the deploy here would be
// wrong more often than it would be right, and an integration that
// injects its own routes is exactly the case where it would be wrong
// every time. build-format runs on every project and warns when a build
// setting would break this platform's routing.

// maxConfigBytes bounds how much of a candidate config this check will
// ever read. A file bigger than this is not a config; the check reports
// unresolved rather than reading an accidental blob into memory.
const maxConfigBytes = 256 * 1024

var errConfigTooLarge = errors.New("exceeds the 256 KB limit this check reads")

// configCandidates is Astro's own config-resolution order: the file
// names it searches for, in the order it searches for them, with the
// first match winning.
//
//	position  candidate
//	1         astro.config.mjs
//	2         astro.config.js
//	3         astro.config.ts
//	4         astro.config.mts
//
// Confirmed directly against two real, independently installed Astro
// packages — versions 7.2.1 and 7.2.10 — both of which freeze exactly
// these four names, in this order, in their own config-search module.
//
// WHY ".cjs" AND ".cts" ARE ABSENT, since a reader who remembers them
// will assume an omission. Astro searched for six names on its 5.x line
// and dropped the two CommonJS spellings after it. This check carried
// them anyway for a while, reasoning that reading one was the lenient
// direction — a project still on 5.x would get its srcDir read rather
// than ignored. That reasoning was wrong in the direction this whole
// task is about: on a CURRENT Astro, a stale astro.config.cjs left in a
// project is a file the build never loads, so every claim this check
// drew from one was a confident statement about a file with no effect.
// For srcDir that produced a warning about the wrong directory; for
// build.format it produced a routing warning about a setting Astro would
// never read at all. The lenience was priced against a project on a
// version this tool has no way to detect, and paid for with false
// positives on every project on a current one.
//
// A project still on 5.x with only a .cjs or .cts config now gets "no
// astro.config file was found" — an admission, and this check's cheapest
// possible outcome, since every finding it can produce is advisory.
// Trading a wrong claim for an admission is the trade this task makes
// everywhere else, and there is no reason for these two names to be the
// exception.
//
// A dedicated test pins this slice against the exact four-entry list
// above by deep equality — every position, not just one. Before that
// test existed, only the legacy pair was ever asserted by anything in
// this suite, so a swap among the CURRENT extensions — the positions
// that actually are facts about a running Astro — passed the whole suite
// silently.
var configCandidates = []string{
	"astro.config.mjs",
	"astro.config.js",
	"astro.config.ts",
	"astro.config.mts",
}

// FS is the narrow filesystem surface this check needs. Production code
// uses OSFileSystem; tests substitute a counting wrapper so the "opened
// at most once" and "never opened" properties have something to measure
// rather than something to assert on trust.
type FS interface {
	// Stat reports whether name exists and, when it does, its size and
	// mode, FOLLOWING a symlink to whatever it points at. Used for
	// existence checks (a pages directory) and the size gate below — and,
	// by isFile, to classify what a symlinked candidate config actually
	// points at. Never counted as "opening" a file's contents.
	Stat(name string) (fs.FileInfo, error)
	// Lstat reports on name itself, WITHOUT following a symlink. isFile
	// calls this FIRST, so a FIFO, socket or device node named directly
	// (not through a link) is rejected without ever resolving anything —
	// the operation that matters for the FIFO problem is Open, not the
	// stat that precedes it, so Lstat alone is enough to keep a direct
	// FIFO from ever reaching Open. When Lstat reports a symlink, isFile
	// goes on to Stat the same name — see isFile's own doc comment for
	// why a symlink is resolved rather than refused outright.
	Lstat(name string) (fs.FileInfo, error)
	// Open opens name for reading its contents. This is the operation a
	// read-counting wrapper counts: the common project (a pages
	// directory already present, no config at all) calls this zero
	// times, and every path that reads a config calls it exactly once.
	Open(name string) (io.ReadCloser, error)
}

// OSFileSystem is the production FS: the real filesystem, through the
// standard library.
type OSFileSystem struct{}

// Stat implements FS.
func (OSFileSystem) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }

// Lstat implements FS.
func (OSFileSystem) Lstat(name string) (fs.FileInfo, error) { return os.Lstat(name) }

// Open implements FS.
func (OSFileSystem) Open(name string) (io.ReadCloser, error) { return os.Open(name) }

// CheckAstroConfig is the pages-directory pre-flight check, config-aware.
// It runs two independent concerns off of at most one file read:
//
//   - pages-dir runs only when <root>/src/pages is established to be
//     absent — a successful stat saying so, not merely a stat that
//     failed; the third state ("couldn't look") gets its own message and
//     stops there, because resolving a custom srcDir would be answering
//     a question nobody has shown needs asking. It tries
//     to resolve a custom srcDir out of astro.config and warns when the
//     resolved directory has no pages/ subdirectory either. It never
//     hard-stops, on any outcome: a real Astro build with no pages
//     directory does not fail — Astro warns and completes, emitting an
//     empty output directory — so the one justification a hard stop here
//     could have (a doomed build) does not hold. An integration that
//     injects its own routes ships a perfectly working project with no
//     pages directory anywhere, and a hard stop would have refused every
//     one of them on the same wrong premise. Whether a project actually
//     ends up with zero published pages is a fact only the build's own
//     output can establish, not a pre-flight guess, so catching that
//     belongs downstream of the build rather than in this check. And a
//     scanner reporting no srcDir key is not the same fact as "the
//     default applies" — it may mean the file uses a shape this scan
//     doesn't parse, so it is treated exactly like every other
//     can't-resolve outcome, never as grounds to assume the default.
//   - build-format runs on every project, because it controls whether a
//     built page lands at .../index.html or as a flat .html file, and
//     only one of those shapes is reachable once published. It can only
//     warn, never hard stop, and its default is SILENCE where pages-dir's
//     is a warning: here "cannot tell" overwhelmingly means the default
//     applies, and warning on every config this check cannot fully parse
//     would be noise on projects that are otherwise fine — which is not a
//     hypothetical, it was measured. Exactly one state breaks that
//     silence, a duplicate key; a file the scan could not finish reading
//     is RECORDED at SeverityNote and shown by nothing. See
//     buildFormatFinding for the ruling that changed and what changed it.
//
// Both concerns are read from the same parse of the same file, so a
// project whose pages directory is exactly where Astro expects it never
// has its config opened more than once, and a project with no config at
// all never has one opened.
//
// TWO further notes are attached independently of either concern, both
// about WHICH file Astro would load rather than about what is in one:
// that more than one candidate config exists on disk (Astro's resolution
// order picks the first, which is exactly the kind of thing that
// surprises people), and that a candidate could not be checked for at all
// (in which case the file this check read may not be the file Astro
// loads). Both attach to a pages-dir finding if one exists and stand
// alone if none does — the ambiguity note used to be decided inside the
// pages-dir path, and was therefore silently dropped on every project
// whose pages directory was already where Astro expects it.
func CheckAstroConfig(fsys FS, root string) []check.Finding {
	pagesDir := isDir(fsys, filepath.Join(root, "src", "pages"))

	configPath, extraCandidates, uncheckedCandidates, found := findConfig(fsys, root)
	if !found {
		if pagesDir == present {
			return finish(nil, "", uncheckedCandidates)
		}
		return finish([]check.Finding{{
			CheckID:  check.CheckIDPagesDir,
			Severity: check.SeverityWarning,
			Message:  openingClause(pagesDir) + noConfigClause(uncheckedCandidates) + advisoryTail,
		}}, "", uncheckedCandidates)
	}

	configName := filepath.Base(configPath)

	content, readErr := readConfigCapped(fsys, configPath)
	if readErr != nil {
		if pagesDir == present {
			return finish(nil, configName, uncheckedCandidates)
		}
		return finish([]check.Finding{{
			CheckID:  check.CheckIDPagesDir,
			Severity: check.SeverityWarning,
			Message: fmt.Sprintf(
				"%s%s couldn't be read (%v), so a custom srcDir couldn't be checked either.%s",
				openingClause(pagesDir), configName, readErr, advisoryTail),
		}}, configName, uncheckedCandidates)
	}

	parsed := parseAstroConfig(content)

	var findings []check.Finding
	switch pagesDir {
	case absent:
		findings = append(findings, resolvePagesDirFindings(fsys, root, configName, parsed)...)
	case undetermined:
		// The premise of the whole pages-dir concern — that src/pages is
		// not there — could not be established, so nothing downstream of
		// it is attempted: resolving a custom srcDir would be answering
		// a question nobody has shown needs asking, and the answer would
		// be printed under a sentence claiming src/pages is missing.
		findings = append(findings, check.Finding{
			CheckID:  check.CheckIDPagesDir,
			Severity: check.SeverityWarning,
			Message: "Couldn't check whether src/pages exists (the directory couldn't be " +
				"read), so this check can't tell whether your pages are where Astro looks " +
				"for them." + advisoryTail,
		})
	case present:
	}

	if bf := buildFormatFinding(configName, parsed); bf != nil {
		findings = append(findings, *bf)
	}

	if len(extraCandidates) > 0 {
		findings = attachPagesDirNote(findings, ambiguousConfigNote(configName, extraCandidates))
	}

	return finish(findings, configName, uncheckedCandidates)
}

// advisoryTail is the sentence every pages-dir warning ends with, held in
// one place so it cannot drift between the seven message shapes that use
// it. It names the consequence a reviewer established by running real
// builds — a missing pages directory does not fail an Astro build, it
// exits 0 and publishes an empty output directory — and deliberately
// does not name the one an earlier draft asserted.
const advisoryTail = " If your pages live somewhere else, this is fine and the build will work. " +
	"If they don't, the build will still succeed — and publish a site with nothing in it."

// openingClause opens a pages-dir message with a claim this check can
// actually support: that src/pages is missing, or only that it could not
// be looked at. It is a function rather than a literal because the same
// seven messages are reached from both states, and the difference between
// them is exactly the difference this round exists to stop collapsing.
func openingClause(pagesDir presence) string {
	if pagesDir == undetermined {
		return "Couldn't check whether src/pages exists, and "
	}
	return "Couldn't find src/pages, and "
}

// noConfigClause says no config file was found — or, when a candidate's
// type could not be established, says only that none was found among the
// ones this check could look at, naming the ones it could not.
func noConfigClause(unchecked []string) string {
	if len(unchecked) == 0 {
		return "no astro.config file was found to check for a custom srcDir."
	}
	return "no astro.config file could be read to check for a custom srcDir (" +
		joinWithAnd(unchecked) + " couldn't be checked)."
}

// finish attaches the unchecked-candidate note, if one is warranted, to
// whatever findings the run produced. It is the last thing every return
// path in CheckAstroConfig goes through, so the note cannot be dropped by
// an early return — which is how the ambiguity note it sits beside was
// silently lost on the pages-present path once build.format started
// opening the config on every project.
//
// The note is only warranted when a config WAS found, and then it matters
// beyond tidiness: an unchecked candidate earlier in Astro's own
// resolution order would be the file Astro actually loads, so the one
// this check read may not be the one that counts. With no winner there is
// nothing to qualify — noConfigClause has already said, in the finding
// itself, which candidates could not be checked.
func finish(findings []check.Finding, winner string, unchecked []string) []check.Finding {
	if len(unchecked) == 0 || winner == "" {
		return findings
	}
	return attachPagesDirNote(findings, fmt.Sprintf(
		"%s couldn't be checked for, so %s may not be the config Astro actually loads.",
		joinWithAnd(unchecked), winner))
}

// attachPagesDirNote folds a standalone note into findings: onto the
// first existing pages-dir finding, if there is one — preserving the
// single-message shape earlier callers of this check assert on — or as
// its own standalone pages-dir finding otherwise. The "otherwise" branch
// is the common one: with build-format read on every project, a pages-dir
// finding often does not exist at all (pages already present, or srcDir
// resolved cleanly), and the ambiguity note used to be silently dropped
// on exactly that path.
func attachPagesDirNote(findings []check.Finding, note string) []check.Finding {
	for i := range findings {
		if findings[i].CheckID == check.CheckIDPagesDir {
			findings[i].Message = findings[i].Message + " " + note
			return findings
		}
	}
	return append(findings, check.Finding{CheckID: check.CheckIDPagesDir, Severity: check.SeverityWarning, Message: note})
}

// findConfig searches configCandidates, in order, for the first that
// exists. Every other existing candidate is returned as extra — an
// ambiguity worth a warning of its own — without ever being opened.
//
// unchecked is the third bucket, and it is why this returns three slices
// instead of a name and a bool: a candidate whose type could not be
// established (an unreadable project directory, a symlink loop) is
// neither present nor absent, and folding it into "absent" is how "no
// astro.config file was found" gets printed about a project that has
// one. Nothing is opened on any path here.
func findConfig(fsys FS, root string) (winner string, extra, unchecked []string, found bool) {
	for _, name := range configCandidates {
		switch isFile(fsys, filepath.Join(root, name)) {
		case present:
			if !found {
				winner, found = filepath.Join(root, name), true
			} else {
				extra = append(extra, name)
			}
		case undetermined:
			unchecked = append(unchecked, name)
		case absent:
		}
	}
	return winner, extra, unchecked, found
}

// readConfigCapped reads at most maxConfigBytes from name, opening it
// exactly once. Stat rules out an oversized file before Open is ever
// called, so an OVERSIZED candidate costs one syscall and zero bytes
// read — but that is the SPECIAL case, not the worst one: an ordinary
// config under the cap costs a stat, an open, one or more bounded reads
// (never more than maxConfigBytes+1 bytes, never a slurp into memory)
// and a close.
//
// This function has no timeout of its own, so it relies entirely on
// never being handed a path whose Open can block: isFile has already
// restricted every candidate reaching here to one whose ultimate target
// (following a symlink, if there is one) is a regular file, which rules
// out a FIFO with no writer, a socket and a device node, reached
// directly or through a link.
//
// That reliance is a CHECK-THEN-USE with a real window in it, and saying
// otherwise was the lie in this comment's first draft. isFile stats a
// path and this function opens it a moment later; between the two, the
// name can be replaced. A user who swaps their own astro.config.mjs for
// a FIFO in that window gets exactly the hang isFile exists to prevent.
// It is not fixed here, and the reasoning is that the window is only
// reachable by a writer inside the very directory this tool was asked to
// read, who can equally well hang the build itself — while closing it
// would mean opening first and stat-ing the descriptor, which this
// package's FS interface cannot express and which does not portably
// avoid blocking on the open in the first place. Both adversarial
// readings that found it judged it not worth fixing. What was worth
// fixing is that this comment claimed a guarantee the code does not
// have: "never opens a FIFO" is true of every ordinary run and is not a
// property, and a comment that states a property is read as one.
func readConfigCapped(fsys FS, name string) ([]byte, error) {
	info, err := fsys.Stat(name)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxConfigBytes {
		return nil, errConfigTooLarge
	}

	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxConfigBytes {
		return nil, errConfigTooLarge
	}
	return data, nil
}

// astroConfig is what parseAstroConfig extracts from one config file:
// the two keys this check reads, and enough shape around each to tell
// "resolved" from "found but can't tell" from "never found at all" from
// "ambiguous" — four distinct states, because a scanner reporting zero
// occurrences of a key is not the same fact as "so the default applies".
// It means "this scan did not see one", and the reasons include a
// shorthand property, a spread from a base config, a key belonging to
// something else in the file entirely, and shapes nobody has thought of
// yet. Treating that as a positive claim about the default is exactly
// how a partial scan produces a false positive, so the zero value of
// every field below means "unresolved", never "resolved to the default".
//
// unresolved is a FIFTH state, and it outranks every field below it. It
// means the scan hit something whose extent it cannot bound: a construct
// outside tokenize's enumerated subset (see that function's SUBSET GATE
// doc comment), a spread in the exported config object, or an export
// wrapped in a call this check cannot assume is the identity function.
// When unresolved is true, every other field is meaningless for BOTH
// keys, not only the one nearest whatever tripped the gate — the scanner
// cannot bound how far the unknown reaches, so it makes no claim about
// anything past it.
//
// The line between this state and the LOCAL ones below it is one rule,
// stated in full in tokenize's LOCAL VERSUS GLOBAL UNKNOWNS section: an
// unknown is local when its extent is bounded by its own token, global
// when it is not. A malformed escape inside a terminated string and a
// duplicate key are both bounded, and are reported as an unreadable
// value or an ambiguity rather than as a whole-file refusal.
type astroConfig struct {
	unresolved       bool   // the scan hit an unknown it cannot bound; every field below is meaningless for BOTH keys
	unresolvedReason string // names the construct, for the messages below; meaningful only when unresolved

	srcDirFound     bool   // a live, top-level srcDir key exists at all
	srcDirAmbiguous bool   // more than one live top-level srcDir key was found
	srcDirResolved  bool   // the key's value is a literal path; meaningless unless srcDirFound && !srcDirAmbiguous
	srcDirValue     string // meaningful only when srcDirResolved

	buildFormatFound     bool   // a live format key exists, nested one level inside a top-level build object
	buildFormatAmbiguous bool   // more than one live "build" key, or more than one live "format" key inside it, was found
	buildFormatResolved  bool   // that key's value is a plain string literal
	buildFormatValue     string // meaningful only when buildFormatResolved
}

// resolvePagesDirFindings turns a parsed config into the pages-dir
// outcome: pass (nil), or one of several warnings — the parse was
// globally unresolved, srcDir is ambiguous, no live srcDir key was found
// at all, the key's value couldn't be read, the resolved value can't be
// packed, or the resolved directory has no pages/ subdirectory. Every
// one of these is a Warning; none of them can ever be a hard stop; see
// CheckAstroConfig's own doc comment for why. The ambiguous-candidate
// note (more than one astro.config.* file on disk) is NOT decided here
// any more — see CheckAstroConfig, which attaches it whenever it
// applies, independent of whether this function ran at all.
func resolvePagesDirFindings(fsys FS, root, configName string, parsed astroConfig) []check.Finding {
	warn := func(format string, args ...any) *check.Finding {
		return &check.Finding{
			CheckID:  check.CheckIDPagesDir,
			Severity: check.SeverityWarning,
			Message:  fmt.Sprintf(format, args...) + advisoryTail,
		}
	}

	var base *check.Finding
	switch {
	case parsed.unresolved:
		// tokenize refused the file outright — see astroConfig's own doc
		// comment. This is a different state from "no live key found"
		// below: there, the scan trusts its own token stream and simply
		// didn't see one; here, the scan does not trust anything it
		// would have produced past the point it gave up, so it says so
		// explicitly rather than folding into the generic case.
		base = warn("Couldn't find src/pages, and %s couldn't be fully read: it contains %s, "+
			"so it can't confirm a custom srcDir either.",
			configName, parsed.unresolvedReason)
	case parsed.srcDirAmbiguous:
		base = warn("Couldn't find src/pages, and %s sets srcDir more than once, so which "+
			"value applies is ambiguous.", configName)
	case !parsed.srcDirFound:
		// No live key at all: this scan cannot tell a plain "src" project
		// from a project whose srcDir key is written in a shape this
		// scan doesn't parse. Astro's default is stated, never asserted
		// to be in force — see the astroConfig doc comment above.
		base = warn("Couldn't find src/pages, and %s doesn't set srcDir in a way this check "+
			"can read, so it can't confirm where your pages live. Astro's own default is src, "+
			"but a missing key here doesn't prove that default is actually in force.",
			configName)
	case !parsed.srcDirResolved:
		base = warn("Couldn't find src/pages, and couldn't read srcDir out of %s (the value "+
			"isn't a plain string).", configName)
	default:
		resolvedDir, rejectReason := resolveSrcDirPath(parsed.srcDirValue)
		switch {
		case rejectReason != "":
			base = warn("Couldn't find src/pages, and %s sets srcDir to %q, which %s.",
				configName, parsed.srcDirValue, rejectReason)
		default:
			pagesDir := resolvedDir + "/pages"
			switch isDir(fsys, filepath.Join(root, filepath.FromSlash(pagesDir))) {
			case present:
				base = nil
			case undetermined:
				// The one message in this function that used to assert
				// absence off a failed stat, and the one a reviewer
				// named: with srcDir resolved to "source" and "source"
				// itself unreadable, the bool form printed "source/pages
				// doesn't exist" about a directory that may well be
				// sitting right there. This says what actually happened.
				base = warn("%s sets srcDir to %q, and %s couldn't be checked (the directory "+
					"couldn't be read), so this check can't tell whether your pages are there.",
					configName, resolvedDir, pagesDir)
			case absent:
				// Warning, never a hard stop: a real Astro build with no
				// pages directory does not fail — Astro warns and
				// completes with zero routes — so refusing the deploy
				// here would be wrong on the one case this message
				// describes with confidence. This message does NOT carry
				// advisoryTail: it is the one shape that has already
				// established where the pages should be and found
				// nothing there, so it names the fix instead of
				// restating the general advice.
				base = &check.Finding{
					CheckID:  check.CheckIDPagesDir,
					Severity: check.SeverityWarning,
					Message: fmt.Sprintf(
						"%s sets srcDir to %q, but %s doesn't exist. Astro looks for pages in "+
							"%s — create it, or point srcDir at the directory that actually has "+
							"your pages.", configName, resolvedDir, pagesDir, pagesDir),
				}
			}
		}
	}

	if base == nil {
		return nil
	}
	return []check.Finding{*base}
}

// ambiguousConfigNote names every config candidate that exists on disk
// and says which one wins. Astro's pick being a documented order rather
// than, say, alphabetical or most-recently-modified is exactly the kind
// of thing that surprises people, so it is worth surfacing even when the
// winning file resolves cleanly on its own.
//
// The subject line is N-AWARE rather than hard-coded to two: "Both" is a
// word for exactly two things, and reads as a grammar mistake once a
// third config candidate joins ("Both A and B and C exist"). This check
// now opens the config on every project (build.format has to), so three
// or more real candidates is not this note's own hard-to-reach edge
// case — it can happen on any project carrying, say, both .mjs and .ts
// alongside a stray legacy .cjs.
func ambiguousConfigNote(winner string, extra []string) string {
	all := append([]string{winner}, extra...)
	subject := "Both " + strings.Join(all, " and ")
	if len(all) > 2 {
		subject = "All of " + joinWithAnd(all)
	}
	return fmt.Sprintf(
		"%s exist. Astro's own resolution order picks %s; the rest are ignored.",
		subject, winner)
}

// joinWithAnd renders items as a natural-language list: "a", "a and b",
// or "a, b and c" — the Oxford-comma-free house style used throughout
// this check's own messages.
func joinWithAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}

// resolveSrcDirPath applies the packer's own constraint to a resolved
// srcDir value: relative to the config's directory, no leading "./", and
// never absolute or escaping the project root. Rejecting those here
// rather than resolving them is deliberate — the packer never includes
// files outside the project root, so a build from that layout cannot
// succeed, and saying so up front is less confusing than resolving to a
// path this check would then have to refuse to pack.
//
// The Windows forms are checked first and separately, because path.Clean
// and path.IsAbs (the slash-only package, deliberately: this value came
// out of an astro.config as forward-slash-style JavaScript/POSIX text)
// cannot see a backslash as a separator at all. "..\outside" never
// matches a "../" prefix once cleaned, and "C:\proj\source" never
// matches path.IsAbs, so both would sail through as ordinary relative
// path segments — one of them literally treated as a directory named
// "C:\proj\source" — without this check catching them first.
func resolveSrcDirPath(value string) (resolved string, rejectReason string) {
	if reason, has := rejectWindowsPathForm(value); has {
		return "", reason
	}

	v := strings.TrimPrefix(value, "./")
	if v == "" {
		v = "."
	}
	if path.IsAbs(v) {
		return "", "is an absolute path — a build can only ever pack files under the project root"
	}
	cleaned := path.Clean(v)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", "escapes the project root — a build can only ever pack files under the project root"
	}
	return cleaned, ""
}

// rejectWindowsPathForm reports a reason, and true, when value is a
// Windows-style path shape this check refuses to resolve: a UNC path
// (checked first, since it also contains backslashes and needs the more
// specific reason), a drive-letter absolute path, or any value merely
// containing a backslash as a separator. None of these can ever resolve
// to a path a build could actually pack — the packer only ever sees the
// project root as a POSIX-style tree — so the reason matters more than
// it would for an ordinary rejection: a bare "escapes the project root"
// would leave a Windows author no idea their srcDir was never going to
// resolve at all.
func rejectWindowsPathForm(value string) (reason string, has bool) {
	switch {
	case strings.HasPrefix(value, `\\`):
		return "is a Windows UNC path — a build can only ever pack files under the project root", true
	case len(value) >= 2 && value[1] == ':' && isASCIILetter(value[0]):
		return "is a Windows drive-letter path — a build can only ever pack files under the project root", true
	case strings.Contains(value, `\`):
		return `uses a Windows-style "\" path separator, which this check can't safely resolve`, true
	default:
		return "", false
	}
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// buildFormatFinding turns a parsed config's build.format value into a
// warning, or nothing. Only a positively resolved 'file' or 'preserve'
// warns; an absent key, or a computed value, says nothing, because a
// wrong warning here is just noise on a project that is fine and the
// default almost certainly applies. This is the opposite default from
// srcDir, and deliberately so: for srcDir, not knowing means this check
// cannot confirm the pages directory it's about to warn about is really
// missing, so it warns to say exactly that; for build.format, not
// knowing means the default overwhelmingly applies, so silence is
// correct until the value is positively known to be one of the two that
// matter.
//
// Two states BREAK that silence, ahead of the ordinary found/resolved
// check, because both are stronger signals than "not found" and each is
// its own finding in the maximum-rigour review:
//
//   - parsed.unresolved: the scan hit something it could not bound, so
//     "not found" would be a lie — this check does not know whether
//     build.format was set, only that it could not finish looking. That
//     is recorded, at SeverityNote, and NOT raised. See below for why
//     this stopped being a warning.
//   - parsed.buildFormatAmbiguous: a duplicate "build" or "format" key.
//     Ambiguity is itself informative in exactly the way absence is
//     not — srcDir's own ambiguous state already warns rather than
//     staying silent, for the same reason. It also passes the criterion
//     below on its own terms: the user has two of the same key in a file
//     they wrote, which they can go and look at.
//
// THE UNRESOLVED CASE USED TO WARN, AND THAT RULING IS SUPERSEDED —
// 2026-09-07. The argument for warning was sound as far as it went: a
// desynchronised scan going quiet is not the same fact as "the default
// applies", and staying silent on the second was the dishonest half of
// "a silent empty parse and a declared unresolved are different
// outcomes". What it could not anticipate is that the subset gate would
// make "could not finish looking" COMMON. Measured the day the gate
// widened to cover template interpolation: two of the three real Astro
// configs on the authors' machine began emitting this warning on every
// run, both because of an analytics snippet building a string with
// "${}". Nothing was wrong with either project.
//
// It is also, on inspection, the state this function ALREADY answers
// with silence one branch down. A "format" key whose value is a bare
// identifier is found-but-unreadable — this check has no idea what it
// resolves to — and that stays silent, with a required mutation
// protecting the silence. "The file was unreadable" is the same
// epistemic state with strictly LESS information, and it was the louder
// of the two. That is not a defensible line.
//
// The criterion this now follows, minted with the ruling and stated in
// full on Severity: AN ADVISORY NAMES SOMETHING THE USER CAN ACT ON OR
// OBSERVE, OR IT DOES NOT FIRE. The pages-dir concern's own gate-trip
// warning passes it — the user really does have no src/pages, which they
// can check — and keeps warning. This one does not: there is nothing to
// look at and nothing to do.
func buildFormatFinding(configName string, parsed astroConfig) *check.Finding {
	if parsed.unresolved {
		// Recorded below advisory severity: no surface shows this by
		// default. It is kept because "the scan gave up" and "the key is
		// absent" are different states, and a caller that needs to tell
		// them apart — a future --verbose, a support transcript — has
		// nowhere else to learn it.
		return &check.Finding{
			CheckID:  check.CheckIDBuildFormat,
			Severity: check.SeverityNote,
			Message: fmt.Sprintf(
				"%s couldn't be fully read: it contains %s, so build.format couldn't be "+
					"confirmed either way.",
				configName, parsed.unresolvedReason),
		}
	}
	if parsed.buildFormatAmbiguous {
		return &check.Finding{
			CheckID:  check.CheckIDBuildFormat,
			Severity: check.SeverityWarning,
			Message: fmt.Sprintf(
				"%s sets build, or format inside it, more than once, so which value actually "+
					"applies is ambiguous. If you're relying on Astro's default output format, "+
					"double-check which one wins.",
				configName),
		}
	}
	if !parsed.buildFormatFound {
		return nil
	}
	if !parsed.buildFormatResolved {
		return nil
	}
	switch parsed.buildFormatValue {
	case "file", "preserve":
		return &check.Finding{
			CheckID:  check.CheckIDBuildFormat,
			Severity: check.SeverityWarning,
			Message: fmt.Sprintf(
				"build.format is set to %q. This platform's routing expects Astro's default "+
					"(%q), which emits /about as about/index.html; %q emits about.html instead, "+
					"and every page but the site's own index will 404 once it's published.",
				parsed.buildFormatValue, "directory", parsed.buildFormatValue),
		}
	default:
		return nil
	}
}

// presence is this check's three-valued answer to every question it asks
// the filesystem. It exists because a bool cannot tell "I looked and it
// is not there" apart from "I could not look", and this check publishes
// sentences that assert the first — "Couldn't find src/pages", "no
// astro.config file was found", "source/pages doesn't exist". Every one
// of those was, with a bool, also what an EACCES on an unreadable parent
// directory produced: a confident claim of absence built out of a
// permission error. AN ABSENCE CLAIM REQUIRES A SUCCESSFUL STAT SAYING
// SO; anything else is undetermined and gets a different sentence.
//
// undetermined is the ZERO VALUE deliberately. A new code path that
// forgets to set one of these reports "couldn't tell", which costs an
// advisory note; the alternative zero value would have it report
// "absent", which is the wrong claim this type exists to prevent.
type presence int

const (
	undetermined presence = iota
	absent
	present
)

// isFile reports whether name is safe for readConfigCapped to Open: a
// regular file, or a symlink whose TARGET is a regular file — and, as a
// third answer, whether that could not be established at all. Lstat runs
// first and never follows anything — a FIFO, socket or device node named
// directly is rejected right there, before this function ever resolves
// a link. Only when Lstat itself reports a symlink does isFile go on to
// Stat the same name, which follows the link and reports on whatever is
// at the far end.
//
// The two-step shape is a CORRECTION, not the original design. The first
// version of this fix used Lstat alone and required the result itself to
// be regular — which closes the FIFO problem (a named pipe called
// astro.config.mjs used to make "!IsDir()" pass, and readConfigCapped
// would then Open it and block forever with no writer ever connecting,
// no timeout anywhere in this package to notice) but ALSO reports a
// symlinked config as absent, since Lstat never reports a symlink itself
// as regular no matter what it points at. A symlinked astro.config.mjs
// pointing at a file shared across a monorepo is an ordinary layout, and
// "absent" is not an admission — it is a confident wrong claim about
// absence, the exact category this task has ruled out twice over. The
// intent was always don't-block, not don't-follow: Stat-ing the target
// of a symlink is an ordinary stat() call and cannot itself block the
// way Opening a FIFO can, so following the link costs nothing this check
// is trying to avoid, and refusing to follow it was solving a problem
// that was never actually about symlinks.
//
// Both properties hold together: a FIFO reached directly is refused by
// the first Lstat; a FIFO reached THROUGH a symlink is refused by the
// second call, because Stat on the link reports the FIFO's own type, not
// "regular", regardless of how it was reached; a symlink to an ordinary
// file resolves and reads exactly as if the file had been named
// directly.
func isFile(fsys FS, name string) presence {
	info, err := fsys.Lstat(name)
	switch {
	case err == nil:
	case errors.Is(err, fs.ErrNotExist):
		return absent
	default:
		return undetermined
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err := fsys.Stat(name)
		switch {
		case err == nil:
			if target.Mode().IsRegular() {
				return present
			}
			return absent
		case errors.Is(err, fs.ErrNotExist):
			// A dangling symlink. The stat succeeded in the only sense
			// that matters here: it established that nothing is at the
			// far end, which is a determination rather than a failure to
			// make one.
			return absent
		default:
			return undetermined
		}
	}
	if info.Mode().IsRegular() {
		return present
	}
	return absent
}

// isDir is the directory half of the same three-valued answer. A path
// that exists and is not a directory is ABSENT rather than undetermined:
// the stat succeeded and settled the question this function asks, which
// is not "does something exist here" but "is there a directory here".
func isDir(fsys FS, name string) presence {
	info, err := fsys.Stat(name)
	switch {
	case err == nil && info.IsDir():
		return present
	case err == nil:
		return absent
	case errors.Is(err, fs.ErrNotExist):
		return absent
	default:
		return undetermined
	}
}
