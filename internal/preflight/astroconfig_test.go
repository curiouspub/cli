package preflight

import (
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/check"
)

// ---------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------

// countingFS wraps the real filesystem and records every path passed to
// Open, plus the bytes actually read back out through it. It is the
// read-counting wrapper the specification asks for: the common project
// (no config, a pages directory already present) must open zero files,
// every path that does read a config must open exactly one, and an
// oversized config must never have more than the size cap read out of
// it.
type countingFS struct {
	opens     []string
	bytesRead int
}

func (c *countingFS) Stat(name string) (fs.FileInfo, error)  { return os.Stat(name) }
func (c *countingFS) Lstat(name string) (fs.FileInfo, error) { return os.Lstat(name) }

func (c *countingFS) Open(name string) (io.ReadCloser, error) {
	c.opens = append(c.opens, name)
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return &countingReadCloser{f: f, n: &c.bytesRead}, nil
}

type countingReadCloser struct {
	f *os.File
	n *int
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.f.Read(p)
	*c.n += n
	return n, err
}

func (c *countingReadCloser) Close() error { return c.f.Close() }

func mustContainAll(t *testing.T, message string, substrs []string) {
	t.Helper()
	for _, s := range substrs {
		if !strings.Contains(message, s) {
			t.Errorf("message %q does not contain %q", message, s)
		}
	}
}

func mustContainNone(t *testing.T, message string, substrs []string) {
	t.Helper()
	for _, s := range substrs {
		if strings.Contains(message, s) {
			t.Errorf("message %q must not contain %q", message, s)
		}
	}
}

// copyDir copies src into dst, used to give a fixture its own scratch
// copy before a test mutates it (adding a pages directory the committed
// fixture deliberately does not have).
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
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
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copying fixture %s: %v", src, err)
	}
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("testdata/astroconfig")
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// ---------------------------------------------------------------------
// The main table: one row per acceptance case, one fixture directory
// per row under testdata/astroconfig/.
// ---------------------------------------------------------------------

type wantFinding struct {
	checkID      string
	severity     check.Severity
	messageHas   []string
	messageLacks []string
}

// wantDecline is the same shape one column over: a decline is a kind and
// a reason where a finding is a severity and a message.
type wantDecline struct {
	checkID     string
	kind        check.DeclineKind
	reasonHas   []string
	reasonLacks []string
}

var astroConfigTable = []struct {
	dir string
	// want is the ADVISORIES this fixture must produce, in order: what a
	// surface puts in front of the person or agent running the deploy.
	want []wantFinding
	// declines is what it must record on the ROW rather than say to
	// anybody: facts kept in the structured result and shown by nothing.
	// These were note-severity findings until the fact moved to the row,
	// and the expectations are the same substrings against the reason
	// the note used to carry.
	//
	// Asserted exactly, not loosely: a row that stops recording its
	// decline reds, because "recorded but suppressed" and "not recorded"
	// are the two states this whole mechanism exists to keep apart, and
	// only one of them is visible from the outside by looking at output.
	declines []wantDecline
}{
	// The two rows that anchor the read-counting property itself have
	// their own dedicated test below (TestCheckAstroConfig_ReadCounting);
	// they are included here too so the outcome table stays complete.
	{"no-config-pages-present", nil, nil},
	{"config-present-pages-present", nil, nil},

	// build.format: each fixture has src/pages present, so the
	// hard-stop path is out of the picture and only this key is live.
	{"build-format-file", []wantFinding{
		{check.IDBuildFormat, check.SeverityWarning, []string{"file", "directory"}, nil},
	}, nil},
	{"build-format-preserve", []wantFinding{
		{check.IDBuildFormat, check.SeverityWarning, []string{"preserve", "directory"}, nil},
	}, nil},
	{"build-format-directory", nil, nil},
	{"build-format-absent", nil, nil},
	{"build-format-bare-toplevel", nil, nil}, // a bare top-level `format` is not `build.format`
	{"build-format-computed", nil, nil},      // see TestCheckAstroConfig_BuildFormatComputedStaysSilent
	{"build-format-in-comment", nil, nil},
	{"build-format-in-string", nil, nil},
	// The measured case: a config this check cannot finish reading, on a
	// project where nothing is wrong. Zero advisories, one by-design
	// decline. Has its own dedicated test
	// (TestCheckAstroConfig_GateTripIsSilentButRecorded) so the required
	// mutations have one obvious row to target; included here too so the
	// outcome table stays complete.
	{"buildformat-gate-trip-pages-present", nil, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, []string{"substitution"}, nil},
	}},

	// srcDir resolution: every fixture below has src/pages ABSENT, which
	// is what makes this path run at all.
	{"srcdir-relative", nil, nil},
	{"srcdir-resolves-no-pages-dir", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"astro.config.mjs", "www", "www/pages"}, nil},
	}, nil},
	{"srcdir-backtick", nil, nil},
	// PINNED TO A MECHANISM, not to a severity. This row and the three
	// below used to assert only "a pages-dir warning, of some kind",
	// which stopped distinguishing anything the moment the subset gate
	// gave every unreadable construct the same severity: a regression
	// that made "+" or "path.join" trip the WHOLE-FILE gate would have
	// passed all four unchanged. Each now names the sentence its own
	// mechanism produces, and — where the mechanism is a local unknown —
	// asserts the whole-file admission is ABSENT.
	//
	// A template interpolation is a global gate trip: this scanner can
	// see where "${" starts and cannot find where the substitution ends,
	// so both keys go unresolved and build-format breaks its usual
	// silence too.
	{"srcdir-template-interp", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"couldn't be fully read", "substitution"}, []string{"source"}},
	}, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, []string{"substitution"}, nil},
	}},
	{"srcdir-fileurltopath", nil, nil},
	// A call this check doesn't recognise: a LOCAL unknown. The key was
	// found and its value can't be read — the file itself scanned fine,
	// which is what the "couldn't be fully read" exclusion pins.
	{"srcdir-pathjoin", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"isn't a plain string"}, []string{"couldn't be fully read"}},
	}, nil},
	{"srcdir-commented-out", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"src/pages"}, []string{"old", "sets srcDir to"}},
	}, nil},
	{"srcdir-block-comment", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"src/pages"}, []string{"old", "sets srcDir to"}},
	}, nil},
	{"srcdir-url-before-key", nil, nil},
	// A duplicate key is a LOCAL unknown too — bounded by the object it
	// was counted in — so it reports ambiguity, not a whole-file
	// refusal.
	{"srcdir-two-keys", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"more than once"}, []string{"couldn't be fully read"}},
	}, nil},
	{"srcdir-absolute", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"absolute"}, nil},
	}, nil},
	{"srcdir-outside-root", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"escapes"}, nil},
	}, nil},
	{"srcdir-both-configs-ambiguous", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"astro.config.mjs", "astro.config.ts"}, nil},
	}, nil},
	{"srcdir-no-config", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"astro.config"}, nil},
	}, nil},

	// AMENDED: no live srcDir key at all is UNRESOLVED, never "the
	// default of src applies" — a scanner reporting zero occurrences of
	// a key cannot establish that the key is truly absent from what
	// Astro would load; it can only establish that this scan didn't see
	// one. Before this fix, this exact fixture (a config file that
	// exists but never mentions srcDir) hard-stopped, claiming
	// "astro.config.mjs sets srcDir to 'src'" — a claim the config
	// never makes.
	{"srcdir-no-key", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"src/pages"}, []string{"sets srcDir to"}},
	}, nil},

	// The five rows below came from an independent reconstruction of this
	// suite: a reader who wrote an acceptance suite from the written
	// specification alone, without ever seeing this file, this table, or
	// the fixtures above. Comparing the two suites row by row turned up
	// no real disagreement — but it did turn up a coverage gap, merged
	// here with origin notes on the rows that close it.

	// REPURPOSED. This fixture was written to pin the ORDER of the two
	// legacy candidates against each other, back when this check still
	// searched for them. It now pins the opposite fact: that neither is
	// a candidate at all. Both files are present, the .cjs one resolves
	// to a directory (cjssrc/pages) that really exists, and the correct
	// outcome is still "no astro.config file was found" — because a
	// current Astro would not load either of them, and a claim drawn
	// from a file the build never reads is a false positive whichever
	// way it points.
	//
	// Keeping the fixture rather than deleting it is the point: the
	// files that used to make this check speak are still sitting there,
	// so a regression that put .cjs or .cts back into configCandidates
	// reds this row immediately with a message naming cjssrc.
	{"legacy-extensions-not-candidates", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning,
			[]string{"no astro.config file was found"},
			[]string{"astro.config.cjs", "astro.config.cts", "cjssrc"}},
	}, nil},

	// The existing "// inside a string" row proves the comment stripper
	// doesn't misread a URL as starting a comment. It does not prove the
	// stripper's escape handling inside that same string is correct — an
	// escaped quote that were treated as the string's real end would
	// desynchronise everything scanned after it. This row is the
	// independent reconstruction's check on that: a preceding string
	// containing \' must not confuse the scan that finds the real srcDir
	// key later on the same line.
	{"srcdir-escaped-quote-in-string", nil, nil},

	// Both existing hard-stop and combined-warning rows only ever
	// exercise ONE finding at a time. Neither proves that the pages-dir
	// and build-format concerns are independent findings drawn off the
	// same single read rather than, say, one silently suppressing the
	// other. These two rows do: a hard stop plus a build-format warning
	// together, and two independent warnings together, each asserting
	// both findings are present rather than just one.
	{"srcdir-resolves-no-pages-dir-plus-buildformat-warning", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"astro.config.mjs", "www", "www/pages"}, nil},
		{check.IDBuildFormat, check.SeverityWarning, []string{"file"}, nil},
	}, nil},
	{"srcdir-pathjoin-plus-buildformat-warning", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"isn't a plain string"}, nil},
		{check.IDBuildFormat, check.SeverityWarning, []string{"preserve"}, nil},
	}, nil},

	// Rows added for the false-hard-stop fix round: every one of these
	// was an executed false positive on the shipped code, found by two
	// independent adversarial reviews. Every outcome below is a warning
	// or a pass, never a hard stop — that is now true of every row in
	// this entire table, not just these, since the hard stop this file
	// used to be able to emit no longer exists as reachable code.

	// The scanner must not claim knowledge it cannot have: a shorthand
	// property has no "key:" pair to find at all.
	{"srcdir-shorthand-property", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"src/pages"}, []string{"./source", "sets srcDir to"}},
	}, nil},
	// A spread from an imported base config. This used to reach the
	// "no live srcDir key" message, which happened to be the right
	// outcome for the wrong reason: the scan reported what it did not
	// see, when the fact worth reporting is that a construct in this
	// object can set the key from a file the scan never opens. It is now
	// a gate trip, and it says so.
	{"srcdir-spread-base-config", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"spread"}, []string{"sets srcDir to"}},
	}, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, []string{"spread"}, nil},
	}},
	// The shape a documentation-site integration ships: routes injected
	// by the integration, no srcDir key, and no pages directory
	// anywhere. Must warn at most, never hard-stop — this is the
	// concrete case the hard stop's removal exists for.
	{"srcdir-integration-injects-routes", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"src/pages"}, []string{"sets srcDir to"}},
	}, nil},

	// Scope anchoring: the key must belong to the EXPORTED config, not
	// to any code in the file. A lone srcDir in an unused local object,
	// with the real config imported and re-exported by identifier, must
	// not resolve from that local object — "wrong" must never appear.
	{"srcdir-scope-unused-local-object", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"src/pages"}, []string{"wrong", "sets srcDir to"}},
	}, nil},
	// srcDir inside an integration's own options is not Astro's own
	// setting — it's nested inside the integration call's argument
	// object, never a direct property of the exported config. Wrapped in
	// defineConfig(), the shape nearly every real Astro project actually
	// uses.
	{"srcdir-scope-integration-options", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"src/pages"}, []string{"wrong", "sets srcDir to"}},
	}, nil},

	// The call-wrapped export shape itself — export default
	// defineConfig({ ... }) — resolving cleanly on its own. Every other
	// srcDir fixture in this table uses a bare object literal; without
	// this row, findExportedConfigObject's call-wrapped branch has no
	// positive-path coverage at all despite being the shape almost every
	// generated Astro project actually ships.
	{"srcdir-defineconfig-wrapper", nil, nil},

	// String values are decoded as JavaScript, not merely de-slashed.
	// All three resolve cleanly to "source/pages" existing on disk; if
	// the escape were merely stripped instead of decoded, the resolved
	// path would be wrong and none of these would pass.
	{"srcdir-unicode-escape", nil, nil},
	{"srcdir-hex-escape", nil, nil},
	{"srcdir-line-continuation", nil, nil},

	// URL semantics: the fileURLToPath(new URL(...)) idiom's argument is
	// a URL reference, so a percent-escape in it is URL syntax, not
	// JavaScript syntax. "source%20files" must decode to "source files"
	// (with a real space) — the fixture directory is literally named
	// with a space, so a raw-text reading would look for a directory
	// that does not exist and this row would not pass.
	{"srcdir-fileurltopath-percent-encoded", nil, nil},

	// Expression continuation: a string literal that only BEGINS an
	// expression is not the value. The fixture directory "source/pages"
	// exists on disk specifically so that a buggy scanner reading only
	// the first literal ('./source') would incorrectly pass; the correct
	// scanner must warn instead, because "./source" + "/nested" is not
	// provably "./source".
	{"srcdir-string-concat", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"isn't a plain string"}, []string{"couldn't be fully read"}},
	}, nil},

	// Windows path forms are invisible to path.IsAbs/path.Clean (POSIX,
	// slash-only) and must be caught explicitly instead of silently
	// treated as an ordinary relative path segment.
	{"srcdir-windows-backslash-relative", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"Windows"}, nil},
	}, nil},
	{"srcdir-windows-drive-absolute", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"Windows", "drive"}, nil},
	}, nil},
	{"srcdir-windows-unc", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"Windows", "UNC"}, nil},
	}, nil},

	// build.format scope anchoring: a build.format that belongs to some
	// other object in the file — never exported, or nested inside
	// something that isn't the real build key — must stay silent.
	{"build-format-outside-export", nil, nil},
	// vite.build shadowing: the code used to take the first "build"
	// object anywhere in the file, so "vite: { build: {...} } }" ahead
	// of the real "build: { format: 'file' }" silently swallowed the
	// warning. The real one must still warn.
	{"build-format-vite-shadow", []wantFinding{
		{check.IDBuildFormat, check.SeverityWarning, []string{"file", "directory"}, nil},
	}, nil},

	// ---------------------------------------------------------------
	// Rows added by the maximum-rigour round: THE SUBSET GATE. Every
	// fixture below is one of the review's own probe configs, taken
	// verbatim. Each used to produce a CONFIDENT, WRONG claim; each must
	// now produce an admission — both keys unresolved, warning on both,
	// "for everything, not locally" — never a silent or a wrong answer.
	// ---------------------------------------------------------------

	// Finding 1a: a regex containing an escaped closer used to truncate
	// the anchored object without failing, promoting the nested
	// "srcDir: './wrong'" to a live top-level key. Must never mention
	// "wrong": the gate must trip on the bare "/" before either key is
	// ever inspected, and build-format's own silence must break too,
	// since the same construct makes it unresolved as well.
	{"srcdir-regex-escaped-closer", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"astro.config.mjs"}, []string{"wrong"}},
	}, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, nil, []string{"wrong"}},
	}},
	// Finding 1b (srcDir variant): a quote inside a regex used to let
	// the following string literal's CONTENTS re-emerge as live code,
	// reading "srcDir: \"wrong\"," out of a description string. Must
	// never mention "wrong".
	{"srcdir-quote-inside-regex", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"astro.config.mjs"}, []string{"wrong"}},
	}, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, nil, []string{"wrong"}},
	}},
	// Finding 1b (build.format variant): the same mechanism firing the
	// 404 warning on a config that never sets build.format at all —
	// the string content "build: { format: \"file\" }," leaking back as
	// if it were the real key. The old, wrong finding named "file" with
	// confidence; the fix must admit it cannot tell, not name a value.
	{"srcdir-quote-inside-regex-buildformat", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"astro.config.mjs"}, nil},
	}, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, nil, []string{"build.format is set to"}},
	}},
	// Finding 2: a regex ending in "\/" produces the byte pair that
	// opens a line comment, eating the rest of the statement and
	// leaving brackets unclosed — which used to make the WHOLE parse
	// return empty, silently, even though build.format really is set
	// to 'file' two lines later. The fix must reach unresolved AND SAY
	// SO on build-format — a silent empty parse and a declared
	// unresolved are different outcomes, and only the warning is
	// honest.
	//
	// WHAT THIS ROW DOES AND DOES NOT PIN, said plainly because the
	// comment above narrates a mechanism the row no longer reaches. The
	// subset gate trips on this fixture at the regex's OPENING "/", four
	// tokens before the trailing "\/" that named the finding, so any
	// regex at all would produce the same outcome here and this row
	// cannot tell the two apart. That is the gate working — the
	// mechanism is unreachable by construction rather than handled — and
	// it is why the row is kept as a regression fixture rather than
	// rewritten: the historical defect's own input must stay in the
	// suite, and its byte-identical control below is what proves the
	// gate, not the surrounding shape, is what changed the answer.
	{"srcdir-regex-trailing-escaped-slash", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"astro.config.mjs"}, nil},
	}, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, nil, []string{"build.format is set to"}},
	}},
	// The review's own "byte-identical control": the same shape with a
	// plain string instead of a regex must parse NORMALLY — a real,
	// confident build-format warning naming 'file', not an admission.
	// Without this row, nothing proves the admission above is really
	// about the regex and not about the surrounding shape.
	{"srcdir-regex-trailing-slash-control", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"src/pages"}, []string{"couldn't be fully read"}},
		{check.IDBuildFormat, check.SeverityWarning, []string{"file", "directory"}, nil},
	}, nil},
	// The ternary: its ":" used to be read as a property colon, so
	// "outDir: useCustom ? srcDir : 'dist'" claimed srcDir was "dist".
	// Must never mention "dist".
	{"srcdir-ternary", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"astro.config.mjs"}, []string{"dist"}},
	}, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, nil, []string{"dist"}},
	}},

	// Duplicate build/format keys: JavaScript takes the LAST, this
	// scanner used to silently take the FIRST. Now ambiguous, matching
	// how a duplicate srcDir key is already ambiguous rather than
	// "first wins" — and, since ambiguity is itself informative, this
	// is one of the two states that breaks build-format's usual
	// silence. Both fixtures have src/pages present, isolating the
	// build-format concern the way every other build-format row does.
	{"build-format-duplicate-build-key", []wantFinding{
		{check.IDBuildFormat, check.SeverityWarning, []string{"more than once"}, nil},
	}, nil},
	{"build-format-duplicate-format-key", []wantFinding{
		{check.IDBuildFormat, check.SeverityWarning, []string{"more than once"}, nil},
	}, nil},

	// Byte-oriented escapes, fixed to be code-point-oriented. Both
	// fixtures resolve CLEANLY — the directory each one names really
	// exists on disk with that exact name — which is the point: a
	// byte-for-byte (rather than code-point) decode would look for a
	// directory that was never there and warn instead of passing.
	// caf\xe9 must become "café" (U+00E9, UTF-8 0xC3 0xA9), not the raw
	// byte 0xE9, which is not valid UTF-8 on its own and cannot match a
	// real "café" directory.
	{"srcdir-latin1-escape", nil, nil},
	// A UTF-16 surrogate pair (😀, the emoji directory this
	// fixture is named for) must combine into the ONE astral character
	// it names, not two independently-written, invalid runes.
	{"srcdir-surrogate-pair-escape", nil, nil},

	// escapeUnresolved coverage: previously zero, per the review — the
	// two guards keying on it (readStringLiteralValue and
	// matchFileURLIdiom) could be deleted and the suite stayed green.
	// All three rows below are LOCAL rejections (the malformed escape
	// stays bounded inside its own string; the scan around it is not
	// desynchronised), so — unlike the SUBSET GATE rows above — these
	// must NOT claim the whole file is unreadable; they hit the
	// ordinary "found a key, can't read its value" path.
	{"srcdir-malformed-hex-escape", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"isn't a plain string"}, []string{"couldn't be fully read"}},
	}, nil},
	{"srcdir-fileurltopath-malformed-escape", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"isn't a plain string"}, []string{"couldn't be fully read"}},
	}, nil},
	// An unpaired high surrogate: valid hex, but not a valid Unicode
	// scalar value on its own, and not followed by the low surrogate
	// that would complete it. Must be unresolved, never a mangled path.
	{"srcdir-unpaired-surrogate", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"isn't a plain string"}, []string{"couldn't be fully read"}},
	}, nil},

	// A quoted key ("srcDir": "./quoted") is a legitimate spelling this
	// scanner has always claimed to support (isKeyToken checks it) but
	// which, per the review, "works and is pinned by nothing" — nothing
	// in the suite would notice if that branch were deleted. Must
	// resolve exactly like a bare identifier does.
	{"srcdir-quoted-key", nil, nil},

	// ---------------------------------------------------------------
	// Rows added by the FIFTH round. Two independent readings refuted
	// the subset gate's completeness claim by different routes, and the
	// four fixtures below are their probe configs, taken verbatim. Every
	// one of them produced a CONFIDENT WRONG claim on the code that had
	// just been written to make confident wrong claims impossible.
	// ---------------------------------------------------------------

	// A backtick INSIDE a "${...}" substitution ends the outer template
	// early, so "srcDir: './wrong'" — template text, nested one level
	// down inside a markdown block — became live code at the top level.
	// The old code reported "sets srcDir to wrong"; a real Astro build of
	// this exact project emits from real/pages, which is why that
	// directory is the one on disk here. The mechanism is the durable
	// part: the scanner RECOGNISED the interpolation (a per-token flag
	// said so) and could not locate its end. Both keys must go
	// unresolved, and "wrong" must never appear.
	{"srcdir-template-nested-backtick", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"substitution"}, []string{"wrong"}},
	}, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, []string{"substitution"}, []string{"wrong"}},
	}},

	// Annex B HTML-like comment markers, in an "astro.config.js" — which
	// is a candidate a CURRENT Astro really does load, and loads as
	// CommonJS when the package is not a module, so Annex B applies to
	// it. "<", "!" and "-" are each in the inert bucket; the SEQUENCES
	// are comment openers, which is the direct counterexample to the
	// per-byte argument this gate used to carry.
	//
	// Both fixtures set srcDir twice, with the first occurrence on the
	// commented line — so a JavaScript engine sees exactly one live key
	// and resolves "./real", which is the directory on disk. Verified by
	// running both files through node: each yields {"srcDir":"./real"}.
	// The old scanner skipped the markers a byte at a time, saw two live
	// keys and warned "sets srcDir more than once" — a false positive on
	// a project that is unambiguous and working. The ruled outcome is
	// neither that warning nor a silent pass: both keys unresolved, and
	// the message names the construct.
	{"srcdir-html-comment-open", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"HTML-style comment"}, []string{"more than once", "wrong"}},
	}, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, []string{"HTML-style comment"}, nil},
	}},
	{"srcdir-html-comment-close", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"HTML-style comment"}, []string{"more than once", "wrong"}},
	}, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, []string{"HTML-style comment"}, nil},
	}},

	// A spread AFTER the key. JavaScript gives the spread the last word,
	// so base.srcDir wins and the literal two tokens earlier does not —
	// yet the old code read it with full confidence. The existing
	// spread-base-config fixture only ever covered spread-BEFORE-key,
	// where the old code happened to stay silent, so the suite recorded
	// a pass on the half that was accidentally right.
	{"srcdir-spread-after-key", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"spread"}, []string{"wrong"}},
	}, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, []string{"spread"}, nil},
	}},

	// A wrapper that is not defineConfig. The old code accepted ANY
	// single-object-argument call as returning its argument, which is a
	// guess about a function whose body is in another file. Only
	// defineConfig is known to be the identity; withDefaults may return
	// anything at all.
	{"srcdir-unknown-wrapper", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"withDefaults"}, []string{"wrong"}},
	}, []wantDecline{
		{check.IDBuildFormat, check.ByDesign, []string{"withDefaults"}, nil},
	}},

	// A legacy octal escape: "'./\163ource'" is "./source" to a
	// JavaScript engine, and was "./163ource" to this scanner — a
	// confident path that exists nowhere. The fixture's source/pages
	// really is on disk, so a scanner that decoded the escape correctly
	// would pass this row silently and a scanner that dropped the
	// backslash would warn about the wrong directory; the ruled outcome
	// is neither, because this check does not decode legacy octal. It is
	// a LOCAL unknown — the string still terminates where it appears
	// to — so it must NOT claim the whole file was unreadable.
	{"srcdir-octal-escape", []wantFinding{
		{check.IDPagesDir, check.SeverityWarning, []string{"isn't a plain string"}, []string{"couldn't be fully read", "163ource", "source/pages"}},
	}, nil},

	// THE INERT BUCKET'S POSITIVE CONTROL, and it did not exist before
	// this round. tokenize's doc comment lists the bytes it drops
	// silently — numbers, "=", ";", "+", "-", "<", ">", "!", "&", "|",
	// "^", "~", "@", "#" — and called them provably inert. Nothing
	// asserted it: no fixture expecting a clean result contained a
	// number, an arrow function, a comparison or a bitwise operator, so
	// "provably inert" was a claim in prose sitting next to a gate that
	// had just been shown to over- and under-reach. This config uses
	// every one of them and still resolves srcDir cleanly. A regression
	// that moved any of those bytes into the gate reds this row with a
	// whole-file admission where a silent pass belongs.
	{"srcdir-inert-bytes-control", nil, nil},

	// The build-format string-scanning control: the existing
	// build-format-in-string fixture is a valid control against a naive
	// regex, but proves nothing about THIS scanner's own string
	// boundary tracking. This one mirrors srcdir-escaped-quote-in-string
	// for the second key: an escaped quote inside a description string
	// must not end the string early and let "build: { format: ... }"
	// leak out as if it were live code.
	{"build-format-in-string-escaped-quote", nil, nil},
}

// ---------------------------------------------------------------------
// The ambiguous-candidate note: N-aware wording, and firing independent
// of the pages-dir path now that build-format opens the config on every
// project.
// ---------------------------------------------------------------------

// TestCheckAstroConfig_AmbiguousNoteFiresWithPagesPresent is the review's
// third finding: the note used to be decided entirely inside
// resolvePagesDirFindings, which never runs once src/pages exists — so
// two (or more) candidate configs sitting right next to an otherwise
// unremarkable, pages-present project produced no note at all, even
// though build-format now opens one of them on every single run.
func TestCheckAstroConfig_AmbiguousNoteFiresWithPagesPresent(t *testing.T) {
	root := fixtureRoot(t)
	findings := CheckAstroConfig(&countingFS{}, filepath.Join(root, "ambiguous-configs-pages-present")).Findings
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one (the ambiguity note, standalone)", findings)
	}
	f := findings[0]
	if f.CheckID != check.IDPagesDir || f.Severity != check.SeverityWarning {
		t.Errorf("finding = %+v, want a pages-dir warning", f)
	}
	mustContainAll(t, f.Message, []string{"astro.config.mjs", "astro.config.ts", "Both"})
}

// TestCheckAstroConfig_AmbiguousNoteIsNAware is the review's own example:
// with three candidates, the old wording read "Both A and B and C
// exist" — "Both" names exactly two things, and using it for three reads
// as a grammar mistake to anyone who notices. This asserts the corrected
// wording directly, appended onto the base srcDir warning (this
// fixture's winning config sets no srcDir key at all).
func TestCheckAstroConfig_AmbiguousNoteIsNAware(t *testing.T) {
	root := fixtureRoot(t)
	findings := CheckAstroConfig(&countingFS{}, filepath.Join(root, "ambiguous-configs-three-way")).Findings
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one", findings)
	}
	f := findings[0]
	mustContainAll(t, f.Message, []string{"All of", "astro.config.mjs", "astro.config.js", "astro.config.ts"})
	mustContainNone(t, f.Message, []string{"Both"})
}

func TestCheckAstroConfig_Table(t *testing.T) {
	root := fixtureRoot(t)

	for _, tc := range astroConfigTable {
		t.Run(tc.dir, func(t *testing.T) {
			cfs := &countingFS{}
			res := CheckAstroConfig(cfs, filepath.Join(root, tc.dir))
			findings := res.Findings

			if len(cfs.opens) > 1 {
				t.Errorf("astro.config opened %d times, want at most 1: %v", len(cfs.opens), cfs.opens)
			}

			// The two columns are asserted SEPARATELY and both exactly.
			// Advisories are what a surface shows; declines are what the
			// run records and shows nobody. Checking only the first
			// would make "stopped recording it" invisible, and checking
			// only the total would let something suppressed become
			// something shown — the exact regression the criterion
			// behind all of this exists to prevent.
			//
			// EVERY finding is now an advisory: the one family that was
			// not is the family that moved to the row. A finding below
			// advisory severity appearing here is therefore a state
			// nothing produces, and this row says so rather than
			// filtering it out silently.
			for _, f := range findings {
				if !f.Advisory() {
					t.Errorf("finding below advisory severity: %+v — that fact belongs on "+
						"the row now, and the gate refuses a finding under a declined id", f)
				}
			}
			assertFindings(t, "advisory", check.Advisories(findings), tc.want)
			assertDeclines(t, res.Declined, tc.declines)
		})
	}
}

// assertDeclines checks the declines a run recorded against what the
// table expects, exactly and in the declared order.
func assertDeclines(t *testing.T, got map[string]check.Decline, want []wantDecline) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d declines, want %d\ngot:  %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for _, wd := range want {
		d, ok := got[wd.checkID]
		if !ok {
			t.Errorf("no decline for %s; got %+v", wd.checkID, got)
			continue
		}
		if d.Kind != wd.kind {
			t.Errorf("%s: kind = %v, want %v", wd.checkID, d.Kind, wd.kind)
		}
		mustContainAll(t, d.Reason, wd.reasonHas)
		mustContainNone(t, d.Reason, wd.reasonLacks)
	}
}

func assertFindings(t *testing.T, kind string, got []check.Finding, want []wantFinding) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d %s findings, want %d\ngot:  %+v\nwant: %+v", len(got), kind, len(want), got, want)
	}
	for i, wf := range want {
		f := got[i]
		if f.CheckID != wf.checkID {
			t.Errorf("%s %d: CheckID = %q, want %q", kind, i, f.CheckID, wf.checkID)
		}
		if f.Severity != wf.severity {
			t.Errorf("%s %d: Severity = %q, want %q", kind, i, f.Severity, wf.severity)
		}
		mustContainAll(t, f.Message, wf.messageHas)
		mustContainNone(t, f.Message, wf.messageLacks)
	}
}

// TestConfigCandidates_PinnedExactly pins the candidate list by deep
// equality: every position, plus the LENGTH, in one assertion that cannot
// rot into covering only part of the slice the way per-position checks
// could. It was added when mutating any of the four current extensions
// left the whole suite green; it now also pins the round that removed
// .cjs and .cts, since re-adding either is a change to a fact about which
// files a current Astro loads and should not be possible to make quietly.
//
// REQUIRED MUTATION: swap any two adjacent entries in configCandidates
// (astroconfig.go) — e.g. ".mjs" and ".js" — or append "astro.config.cjs"
// back onto it, and this test reds immediately. Run and observed to fail
// before this comment was committed; see the report for the red and the
// checksum-verified revert.
func TestConfigCandidates_PinnedExactly(t *testing.T) {
	want := []string{
		"astro.config.mjs",
		"astro.config.js",
		"astro.config.ts",
		"astro.config.mts",
	}
	if !reflect.DeepEqual(configCandidates, want) {
		t.Fatalf("configCandidates = %#v, want %#v", configCandidates, want)
	}
}

// TestCheckAstroConfig_EachExtensionAloneResolves merges a row an
// independent reconstruction of this suite found missing: every fixture
// elsewhere in this file uses .mjs, with a single .ts appearing only as
// the losing candidate in an ambiguity row, never alone. Built with
// t.TempDir() rather than a testdata fixture per extension, since the
// cases differ only in file name and this loop is the whole of what would
// otherwise be four near-identical directories.
func TestCheckAstroConfig_EachExtensionAloneResolves(t *testing.T) {
	for _, ext := range configCandidates {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "candsrc", "pages"), 0o755); err != nil {
				t.Fatal(err)
			}
			body := "export default {\n  srcDir: './candsrc',\n};\n"
			if err := os.WriteFile(filepath.Join(dir, ext), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			findings := CheckAstroConfig(&countingFS{}, dir).Findings
			if len(findings) != 0 {
				t.Fatalf("a lone %s candidate should resolve srcDir cleanly, got %+v", ext, findings)
			}
		})
	}

	// The negative half, and the reason this loop reads configCandidates
	// rather than repeating it: a name that is NOT a candidate must not
	// resolve, and the only way that stays true as the list changes is to
	// derive both halves from the same source. Deleting a name from
	// configCandidates moves it from the loop above into the check below
	// automatically.
	for _, ext := range []string{"astro.config.cjs", "astro.config.cts"} {
		t.Run(ext+" (not a candidate)", func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "candsrc", "pages"), 0o755); err != nil {
				t.Fatal(err)
			}
			body := "export default {\n  srcDir: './candsrc',\n};\n"
			if err := os.WriteFile(filepath.Join(dir, ext), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			cfs := &countingFS{}
			findings := CheckAstroConfig(cfs, dir).Findings
			if len(cfs.opens) != 0 {
				t.Errorf("opens = %v, want none — %s is not a file a current Astro loads", cfs.opens, ext)
			}
			if len(findings) != 1 || findings[0].CheckID != check.IDPagesDir {
				t.Fatalf("findings = %+v, want the one no-config warning", findings)
			}
			mustContainAll(t, findings[0].Message, []string{"no astro.config file was found"})
		})
	}
}

// ---------------------------------------------------------------------
// The read-counting property itself, isolated from the outcome table.
// ---------------------------------------------------------------------

func TestCheckAstroConfig_ReadCounting(t *testing.T) {
	root := fixtureRoot(t)

	t.Run("no config, pages present: never opened", func(t *testing.T) {
		cfs := &countingFS{}
		findings := CheckAstroConfig(cfs, filepath.Join(root, "no-config-pages-present")).Findings
		if len(cfs.opens) != 0 {
			t.Errorf("opens = %v, want none", cfs.opens)
		}
		if len(findings) != 0 {
			t.Errorf("findings = %+v, want none", findings)
		}
	})

	t.Run("config present, pages present: opened at most once, no srcDir resolution", func(t *testing.T) {
		// This fixture's astro.config.mjs sets srcDir to "www", and no
		// www/pages directory exists anywhere near it. If srcDir
		// resolution ran despite src/pages already being present, this
		// would hard-stop. It must not, because the property that
		// survives the build.format amendment is that nothing on this
		// path can escalate beyond a warning.
		cfs := &countingFS{}
		findings := CheckAstroConfig(cfs, filepath.Join(root, "config-present-pages-present")).Findings
		if len(cfs.opens) != 1 {
			t.Errorf("opens = %v, want exactly 1", cfs.opens)
		}
		for _, f := range findings {
			if f.Severity == check.SeverityHardStop {
				t.Errorf("got a hard stop with pages present (srcDir resolution ran when it must not have): %+v", f)
			}
		}
	})

	t.Run("positive control: pages absent, config present, opened exactly once", func(t *testing.T) {
		// Every other row in this file asserts a count of zero or "at
		// most one"; neither can tell "nothing was read" apart from "the
		// wrapper counts nothing". This fixture's config is read, so the
		// wrapper must show it.
		cfs := &countingFS{}
		CheckAstroConfig(cfs, filepath.Join(root, "srcdir-relative"))
		if len(cfs.opens) != 1 {
			t.Errorf("opens = %v, want exactly 1 — the wrapper must be able to observe an open at all", cfs.opens)
		}
	})
}

// TestCheckAstroConfig_NoHardStopWhenPagesPresent re-runs every fixture
// whose config content alone (src/pages absent) resolves to a warning or
// a hard stop, this time with src/pages added. The amended guarantee —
// the one that survives "the config is never opened" once build.format
// has to be read on every project — is that none of this content can
// escalate past a warning once a pages directory already exists,
// because srcDir resolution must not run at all on that path.
func TestCheckAstroConfig_NoHardStopWhenPagesPresent(t *testing.T) {
	root := fixtureRoot(t)

	parserFailureFixtures := []string{
		"srcdir-resolves-no-pages-dir",
		"srcdir-template-interp",
		"srcdir-pathjoin",
		"srcdir-commented-out",
		"srcdir-block-comment",
		"srcdir-two-keys",
		"srcdir-absolute",
		"srcdir-outside-root",
		"srcdir-both-configs-ambiguous",
		"srcdir-no-config",
		"srcdir-no-key",
		"srcdir-shorthand-property",
		"srcdir-spread-base-config",
		"srcdir-integration-injects-routes",
		"srcdir-scope-unused-local-object",
		"srcdir-scope-integration-options",
		"srcdir-string-concat",
		"srcdir-windows-backslash-relative",
		"srcdir-windows-drive-absolute",
		"srcdir-windows-unc",
		"srcdir-template-nested-backtick",
		"srcdir-html-comment-open",
		"srcdir-html-comment-close",
		"srcdir-spread-after-key",
		"srcdir-unknown-wrapper",
		"srcdir-octal-escape",
	}

	for _, name := range parserFailureFixtures {
		t.Run(name, func(t *testing.T) {
			dst := t.TempDir()
			copyDir(t, filepath.Join(root, name), dst)
			if err := os.MkdirAll(filepath.Join(dst, "src", "pages"), 0o755); err != nil {
				t.Fatal(err)
			}

			findings := CheckAstroConfig(&countingFS{}, dst).Findings
			for _, f := range findings {
				if f.Severity == check.SeverityHardStop {
					t.Errorf("hard stop with src/pages present: %+v", f)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------
// Runtime-property cases: size cap and unreadable file. These are built
// in a temp directory at test time rather than committed as testdata,
// because a 300 KB fixture bloats the repository for no reason a reader
// benefits from and a mode-0000 file does not reliably survive a git
// checkout (Windows has no equivalent permission bit, and git does not
// track one). Everything else in this file is a real fixture under
// testdata/, per row shape; these two are the exception, called out here
// rather than left silent.
// ---------------------------------------------------------------------

func TestCheckAstroConfig_OversizedConfigUnresolved(t *testing.T) {
	dir := t.TempDir()

	var b strings.Builder
	b.WriteString("// ")
	for b.Len() < 300*1024 {
		b.WriteString("x")
	}
	b.WriteString("\nexport default {};\n")
	if err := os.WriteFile(filepath.Join(dir, "astro.config.mjs"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	cfs := &countingFS{}
	findings := CheckAstroConfig(cfs, dir).Findings

	if cfs.bytesRead > maxConfigBytes {
		t.Errorf("read %d bytes, want at most %d", cfs.bytesRead, maxConfigBytes)
	}
	if len(findings) != 1 || findings[0].CheckID != check.IDPagesDir || findings[0].Severity != check.SeverityWarning {
		t.Fatalf("findings = %+v, want exactly one pages-dir warning", findings)
	}
}

func TestCheckAstroConfig_UnreadableConfigWarnsNotCrashes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits don't gate reads the same way on Windows")
	}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "astro.config.mjs")
	if err := os.WriteFile(cfgPath, []byte("export default {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cfgPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cfgPath, 0o644) })

	if _, err := os.ReadFile(cfgPath); err == nil {
		t.Skip("this process ignores file permission bits (running as root?) — cannot construct an unreadable file")
	}

	findings := CheckAstroConfig(&countingFS{}, dir).Findings
	if len(findings) != 1 || findings[0].CheckID != check.IDPagesDir || findings[0].Severity != check.SeverityWarning {
		t.Fatalf("findings = %+v, want exactly one pages-dir warning, not a crash", findings)
	}
}

// ---------------------------------------------------------------------
// An advisory names something the user can act on or observe, or it
// doesn't fire.
// ---------------------------------------------------------------------

// TestCheckAstroConfig_GateTripIsSilentButRecorded pins the ruling of
// 2026-09-07 that superseded build.format's break-silence-on-unresolved
// rule, and it pins BOTH halves of it, because either one alone is the
// wrong answer: a gate trip must produce ZERO user-facing advisories,
// and must still record the fact where a caller can find it.
//
// WHERE THE RECORD LIVES CHANGED, and the row is rewritten to follow it
// rather than to keep asserting the old home. The fact used to be a
// note-severity FINDING; it is now a by-design DECLINE on the manifest
// row for the id it is about. Two homes for one fact diverge, and the
// gate now refuses a report carrying a finding under a declined id — so
// keeping both stopped being untidy and became impossible.
//
// The row carries strictly more than the note did: a kind, which decides
// that this costs the user no question, and a reason a caller can read.
// The reason is the note's own wording, carried over unchanged, so
// nothing a person could have read is lost.
//
// The fixture is the shape that produced the measurement. Two of the
// three real Astro configs available when the gate widened began warning
// on every run, both for the same reason: an analytics integration
// building a string with "${}". src/pages is present here, so the
// pages-dir concern is out of the picture and this row isolates the one
// key whose convention is silence.
//
// REQUIRED MUTATION: give the decline Environmental instead of ByDesign.
// The kind half reds — the user is stopped and asked about a config
// containing a template literal, which is the false positive the whole
// ruling exists to prevent.
//
// SECOND REQUIRED MUTATION, for the other half: drop the unresolved
// branch so nothing is recorded at all. The decline half reds — the fact
// stops being kept, which is the error a test asserting only "nothing is
// shown" would call a pass.
func TestCheckAstroConfig_GateTripIsSilentButRecorded(t *testing.T) {
	root := fixtureRoot(t)
	res := CheckAstroConfig(&countingFS{}, filepath.Join(root, "buildformat-gate-trip-pages-present"))

	if len(res.Findings) != 0 {
		t.Errorf("findings = %+v, want none — a config this check couldn't finish reading "+
			"gives the user nothing to act on or observe, so it must say nothing", res.Findings)
	}

	declined, ok := res.Declined[check.IDBuildFormat]
	if !ok {
		t.Fatalf("declined = %+v, want a row for %s — silence must not be achieved by "+
			"forgetting", res.Declined, check.IDBuildFormat)
	}
	if declined.Kind != check.ByDesign {
		t.Errorf("kind = %v, want by-design: the check looked and chose not to guess, and "+
			"nothing about that is the user's to act on", declined.Kind)
	}
	mustContainAll(t, declined.Reason, []string{"substitution", "build.format"})
	mustContainNone(t, declined.Reason, []string{"check it by hand"})
}

// TestAdvisoriesSuppressesNotesAndNothingElse is the filter's own row.
// Every surface will call Advisories rather than compare against a
// severity constant, so a bug here is a bug in what every surface shows —
// and "returns its input unchanged" is the failure mode that looks
// exactly like working code on a slice with no notes in it.
func TestAdvisoriesSuppressesNotesAndNothingElse(t *testing.T) {
	in := []check.Finding{
		{CheckID: "a", Severity: check.SeverityHardStop},
		{CheckID: "b", Severity: check.SeverityNote},
		{CheckID: "c", Severity: check.SeverityWarning},
		{CheckID: "d", Severity: check.SeverityNote},
	}
	got := check.Advisories(in)
	if len(got) != 2 || got[0].CheckID != "a" || got[1].CheckID != "c" {
		t.Fatalf("Advisories = %+v, want the hard stop and the warning, in order", got)
	}
	if len(check.Advisories(nil)) != 0 {
		t.Error("Advisories(nil) must be empty")
	}
}

// ---------------------------------------------------------------------
// "Couldn't check" is not "isn't there": the three-valued filesystem
// answer. Built at test time rather than committed, because a mode-0000
// directory does not survive a git checkout (Windows has no equivalent
// bit and git does not track one).
// ---------------------------------------------------------------------

// unreadableDir makes name under root, puts child inside it, and then
// removes every permission bit from name — so a Stat of anything BENEATH
// it fails with a permission error rather than with "not found". It
// skips, rather than fails, wherever that cannot be constructed: Windows
// has no equivalent, and a process running as root ignores the bits.
func unreadableDir(t *testing.T, root, name, child string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits don't gate stat the same way on Windows")
	}
	full := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(full, child), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(full, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(full, 0o755) })
	if _, err := os.Stat(filepath.Join(full, child)); err == nil {
		t.Skip("this process ignores directory permission bits (running as root?)")
	}
}

// TestCheckAstroConfig_UnreadableSrcDirTargetSaysCouldntCheck is the
// strongest form of the "errors collapsed into absence" finding, and the
// one that reaches the single message in this file that asserts absence
// rather than inability. srcDir resolves to "source"; source/pages really
// is on disk; source itself cannot be read. With a bool, the stat error
// became "not a directory" became "source/pages doesn't exist" — a
// confident claim of absence assembled out of a permission error, printed
// about a directory that is right there.
//
// REQUIRED MUTATION: in isDir (astroconfig.go), replace the switch with
// "return err == nil && info.IsDir()" coerced to present/absent — i.e.
// drop the undetermined branch — and this test reds on the "must not
// contain" half, with the message claiming the directory doesn't exist.
// Run and observed to fail before this comment was committed; see the
// report for the red and the checksum-verified revert.
func TestCheckAstroConfig_UnreadableSrcDirTargetSaysCouldntCheck(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "astro.config.mjs"),
		[]byte("export default {\n  srcDir: './source',\n};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadableDir(t, dir, "source", "pages")

	findings := CheckAstroConfig(&countingFS{}, dir).Findings
	if len(findings) != 1 || findings[0].CheckID != check.IDPagesDir || findings[0].Severity != check.SeverityWarning {
		t.Fatalf("findings = %+v, want exactly one pages-dir warning", findings)
	}
	mustContainAll(t, findings[0].Message, []string{"couldn't be checked", "source/pages"})
	mustContainNone(t, findings[0].Message, []string{"doesn't exist"})
}

// TestCheckAstroConfig_UnreadableSrcPagesSaysCouldntCheck is the same
// defect one level up: every pages-dir message in this file opens by
// asserting src/pages is missing, and with a bool that sentence was also
// what an unreadable src/ produced. The premise of the whole concern
// could not be established, so nothing downstream of it runs — in
// particular this must NOT go on to resolve a custom srcDir and print the
// answer under a claim that src/pages is missing.
func TestCheckAstroConfig_UnreadableSrcPagesSaysCouldntCheck(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "astro.config.mjs"),
		[]byte("export default {\n  srcDir: './elsewhere',\n};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unreadableDir(t, dir, "src", "pages")

	findings := CheckAstroConfig(&countingFS{}, dir).Findings
	if len(findings) != 1 || findings[0].CheckID != check.IDPagesDir || findings[0].Severity != check.SeverityWarning {
		t.Fatalf("findings = %+v, want exactly one pages-dir warning", findings)
	}
	mustContainAll(t, findings[0].Message,
		[]string{"Couldn't check whether src/pages exists", "the build will still succeed"})
	mustContainNone(t, findings[0].Message, []string{"Couldn't find src/pages", "elsewhere"})
}

// TestCheckAstroConfig_UncheckableCandidatesAreNotAbsence covers the
// third absence claim this check publishes: "no astro.config file was
// found". A project directory this process cannot read makes every
// candidate's Lstat fail with a permission error, which a bool turned
// into "none of them are there" — said about a directory whose contents
// are unknown.
func TestCheckAstroConfig_UncheckableCandidatesAreNotAbsence(t *testing.T) {
	parent := t.TempDir()
	unreadableDir(t, parent, "project", "src")
	root := filepath.Join(parent, "project")

	findings := CheckAstroConfig(&countingFS{}, root).Findings
	if len(findings) != 1 || findings[0].CheckID != check.IDPagesDir || findings[0].Severity != check.SeverityWarning {
		t.Fatalf("findings = %+v, want exactly one pages-dir warning", findings)
	}
	mustContainAll(t, findings[0].Message, []string{"couldn't be checked", "astro.config.mjs"})
	mustContainNone(t, findings[0].Message, []string{"no astro.config file was found"})
}

// TestCheckAstroConfig_UncheckedCandidateQualifiesTheWinner is the state
// where a config IS found and an EARLIER candidate could not be checked
// for. Which file Astro loads is then not something this check knows, so
// the finding says so rather than reporting on the file it happened to
// reach. Constructed with a directory named astro.config.mjs whose own
// contents cannot be read — Lstat succeeds and reports a directory, which
// is "absent" — so the uncheckable case is built instead from a symlink
// loop, whose Stat fails with ELOOP on every platform that has symlinks.
func TestCheckAstroConfig_UncheckedCandidateQualifiesTheWinner(t *testing.T) {
	dir := t.TempDir()
	loop := filepath.Join(dir, "astro.config.mjs")
	if err := os.Symlink(loop, loop); err != nil {
		t.Skipf("cannot create a symlink on this platform/permission set: %v", err)
	}
	if _, err := os.Stat(loop); err == nil {
		t.Skip("this platform resolves a self-referential symlink without an error")
	}
	if err := os.WriteFile(filepath.Join(dir, "astro.config.ts"),
		[]byte("export default {\n  srcDir: './source',\n};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "source", "pages"), 0o755); err != nil {
		t.Fatal(err)
	}

	findings := CheckAstroConfig(&countingFS{}, dir).Findings
	if len(findings) != 1 || findings[0].CheckID != check.IDPagesDir {
		t.Fatalf("findings = %+v, want exactly one pages-dir note", findings)
	}
	mustContainAll(t, findings[0].Message,
		[]string{"astro.config.mjs", "couldn't be checked for", "may not be the config Astro actually loads"})
}

// ---------------------------------------------------------------------
// A "//" comment ends at any JavaScript LineTerminator, not only LF.
// ---------------------------------------------------------------------

// TestTokenize_LineCommentEndsAtEveryLineTerminator pins the terminator
// set a single-line comment stops at: LF, a lone CR, and U+2028/U+2029.
// The first version of this loop stopped at LF alone, so a config with
// old-Mac line endings, or one carrying a stray Unicode separator, had
// its real srcDir key swallowed by a comment that had already ended. It
// is a false NEGATIVE rather than a false claim — the check goes quiet
// instead of lying — but a scan that reads less of the file than it
// believes it does is the same defect one direction over, and it is the
// direction that hides.
//
// Built at test time rather than committed, deliberately: .gitattributes
// normalises this repository's checkouts to LF, and a fixture whose whole
// point is a lone CR is exactly the file a line-ending normaliser is
// entitled to rewrite.
//
// REQUIRED MUTATION: in tokenize (astroconfig_lex.go), change the line-
// comment loop's condition back to "src[i] != '\n'" and the three
// non-LF subtests red — the srcDir key on the following line is eaten by
// the comment and the check reports it as never set. Run and observed to
// fail before this comment was committed; see the report for the red and
// the checksum-verified revert.
func TestTokenize_LineCommentEndsAtEveryLineTerminator(t *testing.T) {
	terminators := map[string]string{
		"LF":          "\n",
		"CR":          "\r",
		"U+2028 (LS)": "\u2028",
		"U+2029 (PS)": "\u2029",
	}

	for name, term := range terminators {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "source", "pages"), 0o755); err != nil {
				t.Fatal(err)
			}
			body := "export default {" + term +
				"  // a comment ending in " + name + term +
				"  srcDir: './source'," + term +
				"};" + term
			if err := os.WriteFile(filepath.Join(dir, "astro.config.mjs"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			findings := CheckAstroConfig(&countingFS{}, dir).Findings
			if len(findings) != 0 {
				t.Fatalf("findings = %+v, want none — the comment ends at %s and srcDir "+
					"resolves to ./source, whose pages directory exists", findings, name)
			}
		})
	}
}

// ---------------------------------------------------------------------
// The inverted build.format convention, isolated so the required
// mutation below has one obvious row to target.
// ---------------------------------------------------------------------

// REQUIRED MUTATION: in buildFormatFinding (astroconfig.go), delete the
// `if !parsed.buildFormatResolved { return nil }` guard and instead
// return a Warning Finding there — i.e. make an unresolved build.format
// warn, matching srcDir's convention instead of build.format's own
// inverted one. This fixture's format value is the bare identifier
// `fmt`, not a string literal, so findBuildFormatValue reports
// found=true, resolved=false for it — exactly the state that guard
// exists to keep silent. The mutation must turn this row's "no finding"
// into a warning. Run and observed to fail before this comment was
// committed; see the report for the red and the checksum-verified
// revert.
func TestCheckAstroConfig_BuildFormatComputedStaysSilent(t *testing.T) {
	root := fixtureRoot(t)
	findings := CheckAstroConfig(&countingFS{}, filepath.Join(root, "build-format-computed")).Findings
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none — a computed build.format value must stay silent", findings)
	}
}

// ---------------------------------------------------------------------
// The advisory sentence: corrected to name the real consequence.
// ---------------------------------------------------------------------

// TestCheckAstroConfig_AdvisoryNamesRealConsequence pins the corrected
// ending across every pages-dir warning shape this file can produce. The
// retired premise — "if they don't, the build will fail with the reason
// in its log" — survived the hard stop's own removal because an earlier
// correction preserved the surrounding wording "as drafted", and the
// drafted sentence still asserted a build failure a reviewer had already
// disproved by running real builds: a missing pages directory does not
// fail an Astro build, it exits 0 and publishes an empty output
// directory. Every warning below must name THAT consequence instead, and
// none may repeat the retired one.
//
// REQUIRED MUTATION: put "if they don't, the build will fail with the
// reason in its log." back in any one of resolvePagesDirFindings' or
// CheckAstroConfig's messages (astroconfig.go) and every row in this
// table reds on the "must not contain" half. Run and observed to fail
// before this comment was committed; see the report for the red and the
// checksum-verified revert.
func TestCheckAstroConfig_AdvisoryNamesRealConsequence(t *testing.T) {
	root := fixtureRoot(t)

	// One fixture per static-testdata message template that carries the
	// advisory sentence — no-config, unresolved-parse, ambiguous, no-key,
	// non-literal-value and rejected-path. The seventh shape (an
	// unreadable config) has no testdata fixture of its own — see
	// TestCheckAstroConfig_UnreadableConfigWarnsNotCrashes, built at test
	// time — and is exercised there instead of duplicated here.
	fixtures := []string{
		"srcdir-no-config",
		"srcdir-regex-escaped-closer", // unresolved (subset gate)
		"srcdir-two-keys",             // ambiguous
		"srcdir-no-key",               // no live key
		"srcdir-pathjoin",             // found but not a plain string
		"srcdir-absolute",             // resolved but rejected (absolute path)
		"srcdir-outside-root",         // resolved but rejected (escapes root)
	}

	for _, name := range fixtures {
		t.Run(name, func(t *testing.T) {
			findings := CheckAstroConfig(&countingFS{}, filepath.Join(root, name)).Findings
			var msg string
			for _, f := range findings {
				if f.CheckID == check.IDPagesDir {
					msg = f.Message
					break
				}
			}
			if msg == "" {
				t.Fatalf("no pages-dir finding among %+v", findings)
			}
			mustContainAll(t, msg, []string{"the build will still succeed", "publish a site with nothing in it"})
			mustContainNone(t, msg, []string{"the build will fail", "reason in its log"})
		})
	}
}

// ---------------------------------------------------------------------
// The structural guarantee behind the central ruling of this round: this
// check cannot hard-stop a deploy, full stop.
// ---------------------------------------------------------------------

// TestCheckAstroConfig_NeverHardStopsAnywhere is the mutation target for
// the ruling that removed this file's one hard stop. It runs every
// fixture the outcome table above knows about and fails if any of them
// ever produces a SeverityHardStop finding. A real Astro build with no
// pages directory does not fail — Astro warns and completes with zero
// routes — so refusing a deploy on that evidence is refusing working
// projects on a premise that doesn't hold, and an integration that
// injects its own routes is the concrete case where it would be wrong
// every time. Reintroducing SeverityHardStop anywhere this file's
// resolution logic runs reds this test immediately, on the very fixture
// (srcdir-resolves-no-pages-dir) that named the defect in the first
// place.
func TestCheckAstroConfig_NeverHardStopsAnywhere(t *testing.T) {
	root := fixtureRoot(t)
	for _, tc := range astroConfigTable {
		t.Run(tc.dir, func(t *testing.T) {
			findings := CheckAstroConfig(&countingFS{}, filepath.Join(root, tc.dir)).Findings
			for _, f := range findings {
				if f.Severity == check.SeverityHardStop {
					t.Errorf("got a hard stop: %+v — this check must never hard-stop a deploy", f)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------
// No node, ever.
// ---------------------------------------------------------------------

// TestOSExecNotInProductionDependencyGraph proves exactly one thing:
// os/exec is absent from this package's PRODUCTION dependency graph, so
// this package's own code contains no path to exec.Command, exec.Cmd or
// anything built on them — node included. It deliberately omits -test:
// this test file itself uses exec.Command to run `go list`, and a
// self-referential failure from that would prove nothing about the
// check this package ships.
//
// What this does NOT prove, and the reason for this test's name: "no
// os/exec import" is not the same fact as "no process can ever spawn".
// This package legitimately imports "os" for Stat and Open, and os
// itself exposes os.StartProcess — a lower-level primitive this test
// cannot see, because it isn't os/exec. A test asserting "os/exec is
// absent" is honest about proving exactly that; a test or comment
// asserting "no process can spawn" would be claiming more than an
// import-graph check can establish.
func TestOSExecNotInProductionDependencyGraph(t *testing.T) {
	root := moduleRootForTest(t)

	cmd := exec.Command("go", "list", "-e", "-deps", "./internal/preflight")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "os/exec") {
		t.Fatalf("internal/preflight's own dependency graph includes os/exec, which this "+
			"package must never import — it reads someone else's project and has no business "+
			"holding a way to run one:\n%s", out)
	}
}

func moduleRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the module root walking up from the test's working directory")
		}
		dir = parent
	}
}

// ---------------------------------------------------------------------
// The FIFO row: a regression must hang THIS ROW, never the suite.
// ---------------------------------------------------------------------

// TestCheckAstroConfig_FIFODoesNotHang is the review's FIFO finding: a
// named pipe called astro.config.mjs used to make isFile's "!IsDir()"
// check pass (a FIFO is not a directory), so readConfigCapped went on to
// Open it — and opening a FIFO for reading, with no writer ever
// connecting the other end, blocks forever, with no timeout anywhere in
// this package to notice. newFIFOFixture (platform-specific, see
// astroconfig_fifo_unix_test.go / astroconfig_fifo_other_test.go) makes
// a real one and never writes to it.
//
// CheckAstroConfig runs in its own goroutine specifically so a
// regression fails THIS TEST after the timeout rather than hanging `go
// test` itself: the goroutine leaked by a real regression stays blocked
// in a syscall in the background, but the test function returns (via
// t.Fatal) on schedule, and process exit does not wait for it.
//
// REQUIRED MUTATION: in isFile (astroconfig.go), change
// "info.Mode().IsRegular()" back to "!info.IsDir()" and this test reds
// with the 2-second timeout firing — not instantly, which is the whole
// point: a regression here is a HANG, not a wrong answer. Run and
// observed to fail (by timing out) before this comment was committed;
// see the report for the measured duration and the checksum-verified
// revert.
func TestCheckAstroConfig_FIFODoesNotHang(t *testing.T) {
	dir, ok := newFIFOFixture(t)
	if !ok {
		t.Skip("named pipes are not portably constructible on this platform")
	}

	done := make(chan []check.Finding, 1)
	go func() {
		done <- CheckAstroConfig(&countingFS{}, dir).Findings
	}()

	select {
	case findings := <-done:
		// isFile must have rejected the FIFO before Open was ever
		// reachable, so this is indistinguishable from "no config file
		// at all": no src/pages either, in this fixture, so the ordinary
		// no-config warning fires — never a crash, never a resolved
		// value read from a pipe that was never written to.
		if len(findings) != 1 || findings[0].CheckID != check.IDPagesDir || findings[0].Severity != check.SeverityWarning {
			t.Fatalf("findings = %+v, want exactly one pages-dir warning (the FIFO must be treated as no config found)", findings)
		}
		mustContainAll(t, findings[0].Message, []string{"astro.config"})
	case <-time.After(2 * time.Second):
		t.Fatal("CheckAstroConfig hung for more than 2s reading a FIFO named astro.config.mjs — " +
			"isFile must reject a non-regular file before Open is ever called")
	}
}

// TestCheckAstroConfig_SymlinkedConfigResolves is the regression row: the
// first fix for the FIFO problem (Lstat, and require the Lstat result
// itself to be regular) closed the hang but also made a symlinked
// astro.config.mjs report as absent, since Lstat never calls a symlink
// itself "regular" no matter what it points at. A symlinked config
// pointing at a file shared across a monorepo is an ordinary layout, and
// reporting it as absent is a confident WRONG claim about absence — the
// exact category this task has ruled out twice. The corrected isFile
// follows a symlink and judges the TARGET, so this must resolve exactly
// as if the real file had been named directly.
//
// Built at test time rather than committed as a testdata fixture,
// because a symlink committed to git behaves differently across
// checkouts (Windows needs Developer Mode or an elevated process to
// materialise one at all) — constructing it here and skipping cleanly if
// this platform/permission set refuses is the portable form of this row.
//
// REQUIRED MUTATION: revert isFile (astroconfig.go) to Lstat-only
// ("return err == nil && info.Mode().IsRegular()" with no symlink
// branch) and this test reds — the symlink is reported as not found.
// Run and observed to fail before this comment was committed; see the
// report for the red and the checksum-verified revert.
func TestCheckAstroConfig_SymlinkedConfigResolves(t *testing.T) {
	dir := t.TempDir()

	sharedDir := filepath.Join(dir, "shared-config-location")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	realConfig := filepath.Join(sharedDir, "astro.config.mjs")
	body := "export default {\n  srcDir: './source',\n};\n"
	if err := os.WriteFile(realConfig, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "source", "pages"), 0o755); err != nil {
		t.Fatal(err)
	}

	linkPath := filepath.Join(dir, "astro.config.mjs")
	if err := os.Symlink(realConfig, linkPath); err != nil {
		t.Skipf("cannot create a symlink on this platform/permission set: %v", err)
	}

	cfs := &countingFS{}
	findings := CheckAstroConfig(cfs, dir).Findings
	if len(findings) != 0 {
		t.Fatalf("a symlinked astro.config.mjs pointing at a regular, resolvable config must "+
			"pass cleanly, got %+v", findings)
	}
	if len(cfs.opens) != 1 {
		t.Errorf("opens = %v, want exactly 1 (the symlink, opened once, same as a direct file)", cfs.opens)
	}
}

// TestCheckAstroConfig_SymlinkToFIFODoesNotHang is the shape the naive
// fix (follow every symlink unconditionally, drop the regular-file
// requirement) would have missed in the other direction: a symlink named
// astro.config.mjs pointing at a FIFO must still be refused, not
// resolved through to a blocking Open. Correcting the symlink regression
// without keeping the target's own type check would have reopened the
// exact hang TestCheckAstroConfig_FIFODoesNotHang exists to close, just
// one level of indirection away.
//
// REQUIRED MUTATION: in isFile, change "target.Mode().IsRegular()" to
// always return true when a symlink is found — this test reds, timing
// out at 2s exactly like the direct-FIFO row does on the equivalent
// mutation. Run and observed to fail before this comment was committed;
// see the report for the measured duration and the checksum-verified
// revert.
func TestCheckAstroConfig_SymlinkToFIFODoesNotHang(t *testing.T) {
	dir, ok := newSymlinkToFIFOFixture(t)
	if !ok {
		t.Skip("named pipes and/or symlinks are not portably constructible on this platform")
	}

	done := make(chan []check.Finding, 1)
	go func() {
		done <- CheckAstroConfig(&countingFS{}, dir).Findings
	}()

	select {
	case findings := <-done:
		if len(findings) != 1 || findings[0].CheckID != check.IDPagesDir || findings[0].Severity != check.SeverityWarning {
			t.Fatalf("findings = %+v, want exactly one pages-dir warning (a symlink to a FIFO "+
				"must be treated as no config found)", findings)
		}
		mustContainAll(t, findings[0].Message, []string{"astro.config"})
	case <-time.After(2 * time.Second):
		t.Fatal("CheckAstroConfig hung for more than 2s reading a symlink to a FIFO named " +
			"astro.config.mjs — isFile must reject a non-regular TARGET before Open is ever called")
	}
}
