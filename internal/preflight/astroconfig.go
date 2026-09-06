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

// Check ids this file emits. pages-dir is the hard-stop-capable check
// (does a pages directory actually exist, once a custom srcDir is
// accounted for); build-format is warning-only and runs on every
// project, because it reads a value that matters even when the pages
// directory is exactly where Astro expects it.
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
// Verified against Astro 5.4.2 (the version installed at implementation
// time), dist/core/config/config.js — its frozen `configPaths` array,
// which the internal search() walks in order and returns the first hit
// from. That source is read directly rather than reasoned about, because
// a plausible-sounding version of this order was wrong once already: the
// last two entries were transposed in an earlier draft of this check's
// own specification, and nothing about "cjs before cts" or "cts before
// cjs" is guessable from first principles. A project carrying both is
// rare, but when it happens the file this check reads and the file Astro
// actually loads must be the same one — resolving from the wrong file is
// exactly the false positive this check exists to avoid.
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
	// mode. Used for existence checks (a pages directory, a candidate
	// config file) and the size gate below. Never counted as "opening" a
	// file's contents.
	Stat(name string) (fs.FileInfo, error)
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

// Open implements FS.
func (OSFileSystem) Open(name string) (io.ReadCloser, error) { return os.Open(name) }

// CheckAstroConfig is the pages-directory pre-flight check, config-aware.
// It runs two independent concerns off of at most one file read:
//
//   - pages-dir only runs when <root>/src/pages does not exist. It tries
//     to resolve a custom srcDir out of astro.config and hard-stops when
//     the resolved directory has no pages/ subdirectory either — but
//     never when it cannot tell. A wrong hard stop makes a working
//     project undeployable with no recourse; a missed one only costs the
//     user a server-side build log they can read. Every outcome that
//     cannot positively resolve srcDir downgrades to a warning instead
//     of a hard stop.
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

	parsed := parseAstroConfig(content)

	var findings []Finding
	if !pagesPresent {
		findings = append(findings, resolvePagesDirFindings(fsys, root, configPath, extraCandidates, parsed)...)
	}
	if bf := buildFormatFinding(parsed); bf != nil {
		findings = append(findings, *bf)
	}
	return findings
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
// called, so the worst case costs one syscall and zero bytes read, never
// a slurp into memory.
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
// "resolved" from "cannot tell" from "ambiguous".
type astroConfig struct {
	srcDirFound     bool   // a live srcDir key exists at all
	srcDirAmbiguous bool   // more than one live srcDir key was found
	srcDirResolved  bool   // the key's value (or the default, when absent) is a literal path
	srcDirValue     string // meaningful only when srcDirResolved && !srcDirAmbiguous

	buildFormatFound    bool   // a live format key exists, nested one level inside a top-level build object
	buildFormatResolved bool   // that key's value is a plain string literal
	buildFormatValue    string // meaningful only when buildFormatResolved
}

// resolvePagesDirFindings turns a parsed config into the pages-dir
// outcome: pass (nil), a downgraded warning when srcDir cannot be
// positively resolved, or a hard stop naming the config file, the
// resolved srcDir, and the pages path that does not exist. When more
// than one candidate config file exists, a warning naming all of them is
// attached — synthesised on its own when the underlying outcome would
// otherwise have been a silent pass, or appended to whatever finding the
// resolution already produced.
func resolvePagesDirFindings(fsys FS, root, configPath string, extraCandidates []string, parsed astroConfig) []Finding {
	configName := filepath.Base(configPath)

	var base *Finding
	switch {
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
				base = &Finding{
					CheckID:  CheckIDPagesDir,
					Severity: SeverityHardStop,
					Message: fmt.Sprintf(
						"%s sets srcDir to %q, but %s doesn't exist. Astro looks for pages in "+
							"%s — create it, or point srcDir at the directory that actually has "+
							"your pages.", configName, resolvedDir, pagesDir, pagesDir),
				}
			}
		}
	}

	if len(extraCandidates) == 0 {
		if base == nil {
			return nil
		}
		return []Finding{*base}
	}

	note := ambiguousConfigNote(configName, extraCandidates)
	if base == nil {
		return []Finding{{CheckID: CheckIDPagesDir, Severity: SeverityWarning, Message: note}}
	}
	base.Message = base.Message + " " + note
	return []Finding{*base}
}

// ambiguousConfigNote names every config candidate that exists on disk
// and says which one wins. Astro's pick being a documented order rather
// than, say, alphabetical or most-recently-modified is exactly the kind
// of thing that surprises people, so it is worth surfacing even when the
// winning file resolves cleanly on its own.
func ambiguousConfigNote(winner string, extra []string) string {
	all := append([]string{winner}, extra...)
	return fmt.Sprintf(
		"Both %s exist. Astro's own resolution order picks %s; the rest are ignored.",
		strings.Join(all, " and "), winner)
}

// resolveSrcDirPath applies the packer's own constraint to a resolved
// srcDir value: relative to the config's directory, no leading "./", and
// never absolute or escaping the project root. Rejecting those here
// rather than resolving them is deliberate — the packer never includes
// files outside the project root, so a build from that layout cannot
// succeed, and saying so up front is less confusing than resolving to a
// path this check would then have to refuse to pack.
func resolveSrcDirPath(value string) (resolved string, rejectReason string) {
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

// buildFormatFinding turns a parsed config's build.format value into a
// warning, or nothing. Only a positively resolved 'file' or 'preserve'
// warns; an absent key, a computed value, or a config this check
// couldn't otherwise read all say nothing, because a wrong warning here
// is just noise on a project that is fine and the default almost
// certainly applies. This is the opposite default from srcDir, and
// deliberately so: for srcDir, not knowing may mean a hard stop is about
// to fire wrongly, so it warns; for build.format, not knowing means the
// default overwhelmingly applies, so silence is correct until the value
// is positively known to be one of the two that matter.
func buildFormatFinding(parsed astroConfig) *Finding {
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

func isFile(fsys FS, name string) bool {
	info, err := fsys.Stat(name)
	return err == nil && !info.IsDir()
}

func isDir(fsys FS, name string) bool {
	info, err := fsys.Stat(name)
	return err == nil && info.IsDir()
}
