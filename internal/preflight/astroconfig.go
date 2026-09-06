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
)

// Check ids this file emits. Both are warning-only. pages-dir resolves a
// custom srcDir out of astro.config when the default pages directory is
// missing, and warns — never blocks — when it still can't find a pages
// directory once srcDir is accounted for: a real Astro build with no
// pages directory does not fail, so refusing the deploy here would be
// wrong more often than it would be right, and an integration that
// injects its own routes is exactly the case where it would be wrong
// every time. build-format runs on every project and warns when a build
// setting would break this platform's routing.
const (
	CheckIDPagesDir    = "pages-dir"
	CheckIDBuildFormat = "build-format"
)

// maxConfigBytes bounds how much of a candidate config this check will
// ever read. A file bigger than this is not a config; the check reports
// unresolved rather than reading an accidental blob into memory.
const maxConfigBytes = 256 * 1024

var errConfigTooLarge = errors.New("exceeds the 256 KB limit this check reads")

// configCandidates is Astro's own config-resolution order: the file
// names it searches for, in the order it searches for them, with the
// first match winning.
//
// All six names Astro has ever searched for are carried, in two groups:
//
//	position  candidate            status
//	1         astro.config.mjs     current
//	2         astro.config.js      current
//	3         astro.config.ts      current
//	4         astro.config.mts     current
//	5         astro.config.cjs     legacy — dropped upstream after 5.x
//	6         astro.config.cts     legacy — dropped upstream after 5.x
//
// The first four are confirmed directly against two real, independently
// installed CURRENT Astro packages — versions 7.2.1 and 7.2.10 — both of
// which freeze exactly these four names, in this order, in their own
// config-search module, with no trace of the legacy two. A project still
// on a version that searches for six is rare, but this check reads the
// legacy two anyway — the lenient direction, since a project still
// carrying one gets its srcDir read rather than ignored — in the same
// order those older releases searched for them, because resolving from
// the file Astro would NOT load is exactly the false positive this check
// exists to avoid, and guessing an order is how that happens. This tool
// has no live copy of an Astro release old enough to search for six to
// re-confirm today — a version 5.4.2 install checked during this file's
// own history turned out to belong to an unrelated project and does not
// stand in for one — so positions 5 and 6 are this tool's own documented
// preference among the legacy pair rather than a fact re-derived from a
// running instance, unlike positions 1 through 4, which are.
//
// A dedicated test pins this slice against the exact six-entry list
// above by deep equality — every position, not only the legacy pair.
// Before that test existed, only the legacy pair was ever asserted by
// anything in this suite, so a swap among the four CURRENT extensions —
// the four positions that actually are facts about a running Astro,
// corrected twice and re-derived against two live installs — passed the
// whole suite silently.
var configCandidates = []string{
	"astro.config.mjs",
	"astro.config.js",
	"astro.config.ts",
	"astro.config.mts",
	"astro.config.cjs",
	"astro.config.cts",
}

// FS is the narrow filesystem surface this check needs. Production code
// uses OSFileSystem; tests substitute a counting wrapper so the "opened
// at most once" and "never opened" properties have something to measure
// rather than something to assert on trust.
type FS interface {
	// Stat reports whether name exists and, when it does, its size and
	// mode, FOLLOWING a symlink to whatever it points at. Used for
	// existence checks (a pages directory) and the size gate below.
	// Never counted as "opening" a file's contents, and never used to
	// decide whether something is safe to Open — see Lstat for that.
	Stat(name string) (fs.FileInfo, error)
	// Lstat reports on name itself, WITHOUT following a symlink — it is
	// what isFile uses to decide whether a candidate config is a regular
	// file before ever calling Open on it. A symlink is deliberately not
	// resolved and then judged by its target: resolving it is itself an
	// operation that can be slow or blocking for reasons that have
	// nothing to do with this check (an unresponsive mount at the far
	// end of the link), so this check does not perform it at all — a
	// symlinked candidate is simply not a regular file as far as this
	// check is concerned.
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
//   - pages-dir only runs when <root>/src/pages does not exist. It tries
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
//     warn, never hard stop, and it stays silent whenever it cannot
//     positively resolve the value — the opposite default from
//     pages-dir, because here "cannot tell" overwhelmingly means the
//     default applies, and warning on every config this check cannot
//     fully parse would be noise on projects that are otherwise fine.
//
// Both concerns are read from the same parse of the same file, so a
// project whose pages directory is exactly where Astro expects it never
// has its config opened more than once, and a project with no config at
// all never has one opened.
//
// A THIRD, independent note is attached whenever more than one candidate
// config file exists on disk, regardless of which of the two concerns
// above produced anything: which file Astro's own resolution order picks
// is exactly the kind of thing that surprises people, and now that
// build-format opens the config on every project, this is no longer
// something only the pages-dir path can observe.
func CheckAstroConfig(fsys FS, root string) []Finding {
	pagesPresent := isDir(fsys, filepath.Join(root, "src", "pages"))

	configPath, extraCandidates, found := findConfig(fsys, root)
	if !found {
		if pagesPresent {
			return nil
		}
		return []Finding{{
			CheckID:  CheckIDPagesDir,
			Severity: SeverityWarning,
			Message: "Couldn't find src/pages, and no astro.config file was found to check " +
				"for a custom srcDir. If your pages live somewhere else, this is fine and the " +
				"build will work; if they don't, the build will fail with the reason in its log.",
		}}
	}

	content, readErr := readConfigCapped(fsys, configPath)
	if readErr != nil {
		if pagesPresent {
			return nil
		}
		return []Finding{{
			CheckID:  CheckIDPagesDir,
			Severity: SeverityWarning,
			Message: fmt.Sprintf(
				"Couldn't find src/pages, and %s couldn't be read (%v), so a custom srcDir "+
					"couldn't be checked either. If your pages live somewhere else, this is "+
					"fine and the build will work; if they don't, the build will fail with the "+
					"reason in its log.",
				filepath.Base(configPath), readErr),
		}}
	}

	configName := filepath.Base(configPath)
	parsed := parseAstroConfig(content)

	var findings []Finding
	if !pagesPresent {
		findings = append(findings, resolvePagesDirFindings(fsys, root, configName, parsed)...)
	}
	if bf := buildFormatFinding(configName, parsed); bf != nil {
		findings = append(findings, *bf)
	}

	if len(extraCandidates) > 0 {
		findings = attachAmbiguityNote(findings, configName, extraCandidates)
	}

	return findings
}

// attachAmbiguityNote folds the ambiguous-candidate note into findings:
// onto the first existing pages-dir finding, if resolvePagesDirFindings
// already produced one — preserving the single-message shape earlier
// callers of this check assert on — or as its own standalone pages-dir
// finding otherwise. The "otherwise" branch is now the common one: with
// build-format read on every project, a pages-dir finding often does not
// exist at all (pages already present, or srcDir resolved cleanly), and
// the note used to be silently dropped on exactly that path.
func attachAmbiguityNote(findings []Finding, configName string, extra []string) []Finding {
	note := ambiguousConfigNote(configName, extra)
	for i := range findings {
		if findings[i].CheckID == CheckIDPagesDir {
			findings[i].Message = findings[i].Message + " " + note
			return findings
		}
	}
	return append(findings, Finding{CheckID: CheckIDPagesDir, Severity: SeverityWarning, Message: note})
}

// findConfig searches configCandidates, in order, for the first that
// exists. Every other existing candidate is returned as extra — an
// ambiguity worth a warning of its own, per the note on configCandidates
// above — without ever being opened.
func findConfig(fsys FS, root string) (winner string, extra []string, found bool) {
	for _, name := range configCandidates {
		candidate := filepath.Join(root, name)
		if !isFile(fsys, candidate) {
			continue
		}
		if !found {
			winner, found = candidate, true
			continue
		}
		extra = append(extra, name)
	}
	return winner, extra, found
}

// readConfigCapped reads at most maxConfigBytes from name, opening it
// exactly once. Stat rules out an oversized file before Open is ever
// called, so an OVERSIZED candidate costs one syscall and zero bytes
// read — but that is the SPECIAL case, not the worst one: an ordinary
// config under the cap costs a stat, an open, one or more bounded reads
// (never more than maxConfigBytes+1 bytes, never a slurp into memory)
// and a close. What this function does not do, on any path, is block
// waiting for something that never arrives — isFile has already
// restricted every candidate reaching this function to an Lstat-
// confirmed regular file, so this never opens a FIFO with no writer, a
// socket, or a device node. That guarantee lives in isFile, not here:
// this function has no timeout of its own and would hang exactly as
// before if that guarantee were ever weakened.
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
// unresolved is a FIFTH state, and it outranks every field below it: it
// means tokenize itself refused to produce a trustworthy token stream —
// a bare "/" or "?" outside the enumerated subset, see tokenize's own
// doc comment — so nothing past that point in the file was ever
// inspected. When unresolved is true, every other field is meaningless
// for BOTH keys, not only the one nearest whatever tripped the gate:
// the scanner cannot bound how far a desynchronisation would have
// reached had it kept going, so it makes no claim about anything past
// the point where its own model stopped applying.
type astroConfig struct {
	unresolved       bool   // tokenize hit a construct outside its enumerated subset; every field below is meaningless for BOTH keys
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
func resolvePagesDirFindings(fsys FS, root, configName string, parsed astroConfig) []Finding {
	var base *Finding
	switch {
	case parsed.unresolved:
		// tokenize refused the file outright — see astroConfig's own doc
		// comment. This is a different state from "no live key found"
		// below: there, the scan trusts its own token stream and simply
		// didn't see one; here, the scan does not trust anything it
		// would have produced past the point it gave up, so it says so
		// explicitly rather than folding into the generic case.
		base = &Finding{
			CheckID:  CheckIDPagesDir,
			Severity: SeverityWarning,
			Message: fmt.Sprintf(
				"Couldn't find src/pages, and %s couldn't be fully read: it contains %s, which "+
					"this check doesn't parse, so it can't confirm a custom srcDir either. If your "+
					"pages live somewhere else, this is fine and the build will work; if they "+
					"don't, the build will fail with the reason in its log.",
				configName, parsed.unresolvedReason),
		}
	case parsed.srcDirAmbiguous:
		base = &Finding{
			CheckID:  CheckIDPagesDir,
			Severity: SeverityWarning,
			Message: fmt.Sprintf(
				"Couldn't find src/pages, and %s sets srcDir more than once, so which value "+
					"applies is ambiguous. If your pages live somewhere else, this is fine and "+
					"the build will work; if they don't, the build will fail with the reason in "+
					"its log.", configName),
		}
	case !parsed.srcDirFound:
		// No live key at all: this scan cannot tell a plain "src" project
		// from a project whose srcDir key is written in a shape this
		// scan doesn't parse. Astro's default is stated, never asserted
		// to be in force — see the astroConfig doc comment above.
		base = &Finding{
			CheckID:  CheckIDPagesDir,
			Severity: SeverityWarning,
			Message: fmt.Sprintf(
				"Couldn't find src/pages, and %s doesn't set srcDir in a way this check can "+
					"read, so it can't confirm where your pages live. Astro's own default is "+
					"src, but a missing key here doesn't prove that default is actually in "+
					"force. If your pages live somewhere else, this is fine and the build will "+
					"work; if they don't, the build will fail with the reason in its log.",
				configName),
		}
	case !parsed.srcDirResolved:
		base = &Finding{
			CheckID:  CheckIDPagesDir,
			Severity: SeverityWarning,
			Message: fmt.Sprintf(
				"Couldn't find src/pages, and couldn't read srcDir out of %s (the value isn't "+
					"a plain string). If your pages live somewhere else, this is fine and the "+
					"build will work; if they don't, the build will fail with the reason in its "+
					"log.", configName),
		}
	default:
		resolvedDir, rejectReason := resolveSrcDirPath(parsed.srcDirValue)
		switch {
		case rejectReason != "":
			base = &Finding{
				CheckID:  CheckIDPagesDir,
				Severity: SeverityWarning,
				Message: fmt.Sprintf(
					"Couldn't find src/pages, and %s sets srcDir to %q, which %s. If your pages "+
						"live somewhere else, this is fine and the build will work; if they "+
						"don't, the build will fail with the reason in its log.",
					configName, parsed.srcDirValue, rejectReason),
			}
		default:
			pagesDir := resolvedDir + "/pages"
			if isDir(fsys, filepath.Join(root, filepath.FromSlash(pagesDir))) {
				base = nil
			} else {
				// Warning, never a hard stop: a real Astro build with no
				// pages directory does not fail — Astro warns and
				// completes with zero routes — so refusing the deploy
				// here would be wrong on the one case this message
				// describes with confidence.
				base = &Finding{
					CheckID:  CheckIDPagesDir,
					Severity: SeverityWarning,
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
	return []Finding{*base}
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
//   - parsed.unresolved: tokenize refused the whole file, so "not found"
//     would be a lie — the scanner does not know whether build.format
//     was set, only that it could not finish looking. Finding 2's regex
//     ending in "\/" used to make this silent by DESYNCING THE SCAN
//     rather than by there being no live key, and the two are not the
//     same fact: one means "the default applies", the other means "this
//     check has no idea". Staying silent on the second is the dishonest
//     half of "a silent empty parse and a declared unresolved are
//     different outcomes".
//   - parsed.buildFormatAmbiguous: a duplicate "build" or "format" key.
//     Ambiguity is itself informative in exactly the way absence is
//     not — srcDir's own ambiguous state already warns rather than
//     staying silent, for the same reason.
func buildFormatFinding(configName string, parsed astroConfig) *Finding {
	if parsed.unresolved {
		return &Finding{
			CheckID:  CheckIDBuildFormat,
			Severity: SeverityWarning,
			Message: fmt.Sprintf(
				"Couldn't fully read %s: it contains %s, which this check doesn't parse, so it "+
					"can't confirm build.format either. If you haven't changed build.format away "+
					"from Astro's default, this is fine; otherwise, check it by hand.",
				configName, parsed.unresolvedReason),
		}
	}
	if parsed.buildFormatAmbiguous {
		return &Finding{
			CheckID:  CheckIDBuildFormat,
			Severity: SeverityWarning,
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
		return &Finding{
			CheckID:  CheckIDBuildFormat,
			Severity: SeverityWarning,
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

// isFile reports whether name is a REGULAR file, and only a regular
// file — checked with Lstat rather than Stat specifically so a symlink
// is never followed to find out. A FIFO, a socket, a device node, or a
// symlink to any of those used to pass this check (it only asked
// "!IsDir()", which every one of those satisfies), and readConfigCapped
// would then call Open on it. Opening a FIFO with no writer on the other
// end blocks forever, with no timeout anywhere in this package to
// notice — a named pipe called astro.config.mjs hung this check
// indefinitely. Restricting entry to what Lstat reports as a regular
// file closes that off before Open is ever reachable, rather than
// trying to detect or time out the hang after the fact.
func isFile(fsys FS, name string) bool {
	info, err := fsys.Lstat(name)
	return err == nil && info.Mode().IsRegular()
}

func isDir(fsys FS, name string) bool {
	info, err := fsys.Stat(name)
	return err == nil && info.IsDir()
}
