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
	severity     Severity
	messageHas   []string
	messageLacks []string
}

var astroConfigTable = []struct {
	dir  string
	want []wantFinding
}{
	// The two rows that anchor the read-counting property itself have
	// their own dedicated test below (TestCheckAstroConfig_ReadCounting);
	// they are included here too so the outcome table stays complete.
	{"no-config-pages-present", nil},
	{"config-present-pages-present", nil},

	// build.format: each fixture has src/pages present, so the
	// hard-stop path is out of the picture and only this key is live.
	{"build-format-file", []wantFinding{
		{CheckIDBuildFormat, SeverityWarning, []string{"file", "directory"}, nil},
	}},
	{"build-format-preserve", []wantFinding{
		{CheckIDBuildFormat, SeverityWarning, []string{"preserve", "directory"}, nil},
	}},
	{"build-format-directory", nil},
	{"build-format-absent", nil},
	{"build-format-bare-toplevel", nil}, // a bare top-level `format` is not `build.format`
	{"build-format-computed", nil},      // see TestCheckAstroConfig_BuildFormatComputedStaysSilent
	{"build-format-in-comment", nil},
	{"build-format-in-string", nil},

	// srcDir resolution: every fixture below has src/pages ABSENT, which
	// is what makes this path run at all.
	{"srcdir-relative", nil},
	{"srcdir-resolves-no-pages-dir", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"astro.config.mjs", "www", "www/pages"}, nil},
	}},
	{"srcdir-backtick", nil},
	{"srcdir-template-interp", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, nil, nil},
	}},
	{"srcdir-fileurltopath", nil},
	{"srcdir-pathjoin", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, nil, nil},
	}},
	{"srcdir-commented-out", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"src/pages"}, []string{"old", "sets srcDir to"}},
	}},
	{"srcdir-block-comment", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"src/pages"}, []string{"old", "sets srcDir to"}},
	}},
	{"srcdir-url-before-key", nil},
	{"srcdir-two-keys", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, nil, nil},
	}},
	{"srcdir-absolute", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"absolute"}, nil},
	}},
	{"srcdir-outside-root", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"escapes"}, nil},
	}},
	{"srcdir-both-configs-ambiguous", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"astro.config.mjs", "astro.config.ts"}, nil},
	}},
	{"srcdir-no-config", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"astro.config"}, nil},
	}},

	// AMENDED: no live srcDir key at all is UNRESOLVED, never "the
	// default of src applies" — a scanner reporting zero occurrences of
	// a key cannot establish that the key is truly absent from what
	// Astro would load; it can only establish that this scan didn't see
	// one. Before this fix, this exact fixture (a config file that
	// exists but never mentions srcDir) hard-stopped, claiming
	// "astro.config.mjs sets srcDir to 'src'" — a claim the config
	// never makes.
	{"srcdir-no-key", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"src/pages"}, []string{"sets srcDir to"}},
	}},

	// The five rows below came from an independent reconstruction of this
	// suite: a reader who wrote an acceptance suite from the written
	// specification alone, without ever seeing this file, this table, or
	// the fixtures above. Comparing the two suites row by row turned up
	// no real disagreement — but it did turn up a coverage gap, merged
	// here with origin notes on the rows that close it.

	// .cjs and .cts had NEVER been exercised, alone or together, before
	// this row. Their ORDER here is NOT a fact about current Astro —
	// current Astro doesn't search for either extension at all, both
	// having been dropped upstream after the 5.x line (see the comment
	// on configCandidates for what was actually verified and against
	// what). This row asserts this tool's OWN documented preference
	// among the two legacy candidates, kept for the projects still on an
	// older release that searches for both. .cts's srcDir deliberately
	// points at a directory with no pages/ subdirectory, so a regression
	// in that documented order would flip which file wins and change
	// what this row's warning names.
	{"srcdir-cjs-precedes-cts", []wantFinding{
		// Naming both files alone isn't order-sensitive: both appear in
		// the ambiguity note regardless of which one wins. "picks
		// astro.config.cjs" is the part that only holds if cjs actually
		// precedes cts in configCandidates — the fixture's .cjs resolves
		// cleanly (cjssrc/pages exists) while its .cts does not
		// (ctssrc/pages doesn't), so a regression to cts-first would
		// flip both the picked file AND the message shape (a warning
		// about a missing ctssrc/pages, not a clean pass-plus-note).
		{CheckIDPagesDir, SeverityWarning,
			[]string{"astro.config.cjs", "astro.config.cts", "picks astro.config.cjs"},
			[]string{"picks astro.config.cts", "ctssrc"}},
	}},

	// The existing "// inside a string" row proves the comment stripper
	// doesn't misread a URL as starting a comment. It does not prove the
	// stripper's escape handling inside that same string is correct — an
	// escaped quote that were treated as the string's real end would
	// desynchronise everything scanned after it. This row is the
	// independent reconstruction's check on that: a preceding string
	// containing \' must not confuse the scan that finds the real srcDir
	// key later on the same line.
	{"srcdir-escaped-quote-in-string", nil},

	// Both existing hard-stop and combined-warning rows only ever
	// exercise ONE finding at a time. Neither proves that the pages-dir
	// and build-format concerns are independent findings drawn off the
	// same single read rather than, say, one silently suppressing the
	// other. These two rows do: a hard stop plus a build-format warning
	// together, and two independent warnings together, each asserting
	// both findings are present rather than just one.
	{"srcdir-resolves-no-pages-dir-plus-buildformat-warning", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"astro.config.mjs", "www", "www/pages"}, nil},
		{CheckIDBuildFormat, SeverityWarning, []string{"file"}, nil},
	}},
	{"srcdir-pathjoin-plus-buildformat-warning", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, nil, nil},
		{CheckIDBuildFormat, SeverityWarning, []string{"preserve"}, nil},
	}},

	// Rows added for the false-hard-stop fix round: every one of these
	// was an executed false positive on the shipped code, found by two
	// independent adversarial reviews. Every outcome below is a warning
	// or a pass, never a hard stop — that is now true of every row in
	// this entire table, not just these, since the hard stop this file
	// used to be able to emit no longer exists as reachable code.

	// The scanner must not claim knowledge it cannot have: a shorthand
	// property has no "key:" pair to find at all.
	{"srcdir-shorthand-property", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"src/pages"}, []string{"./source", "sets srcDir to"}},
	}},
	// A spread from an imported base config: no live srcDir key is
	// visible, and none should be invented.
	{"srcdir-spread-base-config", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"src/pages"}, []string{"sets srcDir to"}},
	}},
	// The shape a documentation-site integration ships: routes injected
	// by the integration, no srcDir key, and no pages directory
	// anywhere. Must warn at most, never hard-stop — this is the
	// concrete case the hard stop's removal exists for.
	{"srcdir-integration-injects-routes", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"src/pages"}, []string{"sets srcDir to"}},
	}},

	// Scope anchoring: the key must belong to the EXPORTED config, not
	// to any code in the file. A lone srcDir in an unused local object,
	// with the real config imported and re-exported by identifier, must
	// not resolve from that local object — "wrong" must never appear.
	{"srcdir-scope-unused-local-object", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"src/pages"}, []string{"wrong", "sets srcDir to"}},
	}},
	// srcDir inside an integration's own options is not Astro's own
	// setting — it's nested inside the integration call's argument
	// object, never a direct property of the exported config. Wrapped in
	// defineConfig(), the shape nearly every real Astro project actually
	// uses.
	{"srcdir-scope-integration-options", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"src/pages"}, []string{"wrong", "sets srcDir to"}},
	}},

	// The call-wrapped export shape itself — export default
	// defineConfig({ ... }) — resolving cleanly on its own. Every other
	// srcDir fixture in this table uses a bare object literal; without
	// this row, findExportedConfigObject's call-wrapped branch has no
	// positive-path coverage at all despite being the shape almost every
	// generated Astro project actually ships.
	{"srcdir-defineconfig-wrapper", nil},

	// String values are decoded as JavaScript, not merely de-slashed.
	// All three resolve cleanly to "source/pages" existing on disk; if
	// the escape were merely stripped instead of decoded, the resolved
	// path would be wrong and none of these would pass.
	{"srcdir-unicode-escape", nil},
	{"srcdir-hex-escape", nil},
	{"srcdir-line-continuation", nil},

	// URL semantics: the fileURLToPath(new URL(...)) idiom's argument is
	// a URL reference, so a percent-escape in it is URL syntax, not
	// JavaScript syntax. "source%20files" must decode to "source files"
	// (with a real space) — the fixture directory is literally named
	// with a space, so a raw-text reading would look for a directory
	// that does not exist and this row would not pass.
	{"srcdir-fileurltopath-percent-encoded", nil},

	// Expression continuation: a string literal that only BEGINS an
	// expression is not the value. The fixture directory "source/pages"
	// exists on disk specifically so that a buggy scanner reading only
	// the first literal ('./source') would incorrectly pass; the correct
	// scanner must warn instead, because "./source" + "/nested" is not
	// provably "./source".
	{"srcdir-string-concat", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, nil, nil},
	}},

	// Windows path forms are invisible to path.IsAbs/path.Clean (POSIX,
	// slash-only) and must be caught explicitly instead of silently
	// treated as an ordinary relative path segment.
	{"srcdir-windows-backslash-relative", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"Windows"}, nil},
	}},
	{"srcdir-windows-drive-absolute", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"Windows", "drive"}, nil},
	}},
	{"srcdir-windows-unc", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"Windows", "UNC"}, nil},
	}},

	// build.format scope anchoring: a build.format that belongs to some
	// other object in the file — never exported, or nested inside
	// something that isn't the real build key — must stay silent.
	{"build-format-outside-export", nil},
	// vite.build shadowing: the code used to take the first "build"
	// object anywhere in the file, so "vite: { build: {...} } }" ahead
	// of the real "build: { format: 'file' }" silently swallowed the
	// warning. The real one must still warn.
	{"build-format-vite-shadow", []wantFinding{
		{CheckIDBuildFormat, SeverityWarning, []string{"file", "directory"}, nil},
	}},

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
		{CheckIDPagesDir, SeverityWarning, []string{"astro.config.mjs"}, []string{"wrong"}},
		{CheckIDBuildFormat, SeverityWarning, nil, []string{"wrong"}},
	}},
	// Finding 1b (srcDir variant): a quote inside a regex used to let
	// the following string literal's CONTENTS re-emerge as live code,
	// reading "srcDir: \"wrong\"," out of a description string. Must
	// never mention "wrong".
	{"srcdir-quote-inside-regex", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"astro.config.mjs"}, []string{"wrong"}},
		{CheckIDBuildFormat, SeverityWarning, nil, []string{"wrong"}},
	}},
	// Finding 1b (build.format variant): the same mechanism firing the
	// 404 warning on a config that never sets build.format at all —
	// the string content "build: { format: \"file\" }," leaking back as
	// if it were the real key. The old, wrong finding named "file" with
	// confidence; the fix must admit it cannot tell, not name a value.
	{"srcdir-quote-inside-regex-buildformat", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"astro.config.mjs"}, nil},
		{CheckIDBuildFormat, SeverityWarning, nil, []string{"build.format is set to"}},
	}},
	// Finding 2: a regex ending in "\/" produces the byte pair that
	// opens a line comment, eating the rest of the statement and
	// leaving brackets unclosed — which used to make the WHOLE parse
	// return empty, silently, even though build.format really is set
	// to 'file' two lines later. The fix must reach unresolved AND SAY
	// SO on build-format — a silent empty parse and a declared
	// unresolved are different outcomes, and only the warning is
	// honest.
	{"srcdir-regex-trailing-escaped-slash", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"astro.config.mjs"}, nil},
		{CheckIDBuildFormat, SeverityWarning, nil, []string{"build.format is set to"}},
	}},
	// The review's own "byte-identical control": the same shape with a
	// plain string instead of a regex must parse NORMALLY — a real,
	// confident build-format warning naming 'file', not an admission.
	// Without this row, nothing proves the admission above is really
	// about the regex and not about the surrounding shape.
	{"srcdir-regex-trailing-slash-control", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"src/pages"}, []string{"couldn't be fully read"}},
		{CheckIDBuildFormat, SeverityWarning, []string{"file", "directory"}, nil},
	}},
	// The ternary: its ":" used to be read as a property colon, so
	// "outDir: useCustom ? srcDir : 'dist'" claimed srcDir was "dist".
	// Must never mention "dist".
	{"srcdir-ternary", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"astro.config.mjs"}, []string{"dist"}},
		{CheckIDBuildFormat, SeverityWarning, nil, []string{"dist"}},
	}},

	// Duplicate build/format keys: JavaScript takes the LAST, this
	// scanner used to silently take the FIRST. Now ambiguous, matching
	// how a duplicate srcDir key is already ambiguous rather than
	// "first wins" — and, since ambiguity is itself informative, this
	// is one of the two states that breaks build-format's usual
	// silence. Both fixtures have src/pages present, isolating the
	// build-format concern the way every other build-format row does.
	{"build-format-duplicate-build-key", []wantFinding{
		{CheckIDBuildFormat, SeverityWarning, []string{"more than once"}, nil},
	}},
	{"build-format-duplicate-format-key", []wantFinding{
		{CheckIDBuildFormat, SeverityWarning, []string{"more than once"}, nil},
	}},

	// Byte-oriented escapes, fixed to be code-point-oriented. Both
	// fixtures resolve CLEANLY — the directory each one names really
	// exists on disk with that exact name — which is the point: a
	// byte-for-byte (rather than code-point) decode would look for a
	// directory that was never there and warn instead of passing.
	// caf\xe9 must become "café" (U+00E9, UTF-8 0xC3 0xA9), not the raw
	// byte 0xE9, which is not valid UTF-8 on its own and cannot match a
	// real "café" directory.
	{"srcdir-latin1-escape", nil},
	// A UTF-16 surrogate pair (😀, the emoji directory this
	// fixture is named for) must combine into the ONE astral character
	// it names, not two independently-written, invalid runes.
	{"srcdir-surrogate-pair-escape", nil},

	// escapeUnresolved coverage: previously zero, per the review — the
	// two guards keying on it (readStringLiteralValue and
	// matchFileURLIdiom) could be deleted and the suite stayed green.
	// All three rows below are LOCAL rejections (the malformed escape
	// stays bounded inside its own string; the scan around it is not
	// desynchronised), so — unlike the SUBSET GATE rows above — these
	// must NOT claim the whole file is unreadable; they hit the
	// ordinary "found a key, can't read its value" path.
	{"srcdir-malformed-hex-escape", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"isn't a plain string"}, []string{"couldn't be fully read"}},
	}},
	{"srcdir-fileurltopath-malformed-escape", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"isn't a plain string"}, []string{"couldn't be fully read"}},
	}},
	// An unpaired high surrogate: valid hex, but not a valid Unicode
	// scalar value on its own, and not followed by the low surrogate
	// that would complete it. Must be unresolved, never a mangled path.
	{"srcdir-unpaired-surrogate", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"isn't a plain string"}, []string{"couldn't be fully read"}},
	}},

	// A quoted key ("srcDir": "./quoted") is a legitimate spelling this
	// scanner has always claimed to support (isKeyToken checks it) but
	// which, per the review, "works and is pinned by nothing" — nothing
	// in the suite would notice if that branch were deleted. Must
	// resolve exactly like a bare identifier does.
	{"srcdir-quoted-key", nil},

	// The build-format string-scanning control: the existing
	// build-format-in-string fixture is a valid control against a naive
	// regex, but proves nothing about THIS scanner's own string
	// boundary tracking. This one mirrors srcdir-escaped-quote-in-string
	// for the second key: an escaped quote inside a description string
	// must not end the string early and let "build: { format: ... }"
	// leak out as if it were live code.
	{"build-format-in-string-escaped-quote", nil},
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
	findings := CheckAstroConfig(&countingFS{}, filepath.Join(root, "ambiguous-configs-pages-present"))
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want exactly one (the ambiguity note, standalone)", findings)
	}
	f := findings[0]
	if f.CheckID != CheckIDPagesDir || f.Severity != SeverityWarning {
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
	findings := CheckAstroConfig(&countingFS{}, filepath.Join(root, "ambiguous-configs-three-way"))
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
			findings := CheckAstroConfig(cfs, filepath.Join(root, tc.dir))

			if len(cfs.opens) > 1 {
				t.Errorf("astro.config opened %d times, want at most 1: %v", len(cfs.opens), cfs.opens)
			}

			if len(findings) != len(tc.want) {
				t.Fatalf("got %d findings, want %d\ngot:  %+v\nwant: %+v", len(findings), len(tc.want), findings, tc.want)
			}
			for i, wf := range tc.want {
				f := findings[i]
				if f.CheckID != wf.checkID {
					t.Errorf("finding %d: CheckID = %q, want %q", i, f.CheckID, wf.checkID)
				}
				if f.Severity != wf.severity {
					t.Errorf("finding %d: Severity = %q, want %q", i, f.Severity, wf.severity)
				}
				mustContainAll(t, f.Message, wf.messageHas)
				mustContainNone(t, f.Message, wf.messageLacks)
			}
		})
	}
}

// TestConfigCandidates_PinnedExactly is the review's third finding:
// mutating any of the four CURRENT extension positions in configCandidates
// — .mjs, .js, .ts, .mts — left the whole suite green, because only the
// legacy .cjs/.cts pair (this tool's own documented preference, not a
// fact about Astro) was ever pinned by anything. One deep-equality
// assertion against the whole six-entry slice covers every position at
// once and cannot rot into covering only one, the way six separate
// per-position checks could.
//
// REQUIRED MUTATION: swap any two adjacent entries in configCandidates
// (astroconfig.go) — e.g. ".mjs" and ".js" — and this test reds
// immediately. Run and observed to fail before this comment was
// committed; see the report for the red and the checksum-verified
// revert.
func TestConfigCandidates_PinnedExactly(t *testing.T) {
	want := []string{
		"astro.config.mjs",
		"astro.config.js",
		"astro.config.ts",
		"astro.config.mts",
		"astro.config.cjs",
		"astro.config.cts",
	}
	if !reflect.DeepEqual(configCandidates, want) {
		t.Fatalf("configCandidates = %#v, want %#v", configCandidates, want)
	}
}

// TestCheckAstroConfig_EachExtensionAloneResolves merges the sixth row an
// independent reconstruction of this suite found missing: every fixture
// elsewhere in this file uses .mjs, with a single .ts appearing only as
// the losing candidate in an ambiguity row, never alone. The other four
// of Astro's six recognised extensions — .js, .mts, .cjs and .cts — had
// never been exercised in any form. Built with t.TempDir() rather than a
// testdata fixture per extension, since the six cases differ only in
// file name and this loop is the whole of what would otherwise be six
// near-identical directories.
func TestCheckAstroConfig_EachExtensionAloneResolves(t *testing.T) {
	extensions := []string{
		"astro.config.mjs",
		"astro.config.js",
		"astro.config.ts",
		"astro.config.mts",
		"astro.config.cjs",
		"astro.config.cts",
	}

	for _, ext := range extensions {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "candsrc", "pages"), 0o755); err != nil {
				t.Fatal(err)
			}
			body := "export default {\n  srcDir: './candsrc',\n};\n"
			if err := os.WriteFile(filepath.Join(dir, ext), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			findings := CheckAstroConfig(&countingFS{}, dir)
			if len(findings) != 0 {
				t.Fatalf("a lone %s candidate should resolve srcDir cleanly, got %+v", ext, findings)
			}
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
		findings := CheckAstroConfig(cfs, filepath.Join(root, "no-config-pages-present"))
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
		findings := CheckAstroConfig(cfs, filepath.Join(root, "config-present-pages-present"))
		if len(cfs.opens) != 1 {
			t.Errorf("opens = %v, want exactly 1", cfs.opens)
		}
		for _, f := range findings {
			if f.Severity == SeverityHardStop {
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
	}

	for _, name := range parserFailureFixtures {
		t.Run(name, func(t *testing.T) {
			dst := t.TempDir()
			copyDir(t, filepath.Join(root, name), dst)
			if err := os.MkdirAll(filepath.Join(dst, "src", "pages"), 0o755); err != nil {
				t.Fatal(err)
			}

			findings := CheckAstroConfig(&countingFS{}, dst)
			for _, f := range findings {
				if f.Severity == SeverityHardStop {
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
	findings := CheckAstroConfig(cfs, dir)

	if cfs.bytesRead > maxConfigBytes {
		t.Errorf("read %d bytes, want at most %d", cfs.bytesRead, maxConfigBytes)
	}
	if len(findings) != 1 || findings[0].CheckID != CheckIDPagesDir || findings[0].Severity != SeverityWarning {
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

	findings := CheckAstroConfig(&countingFS{}, dir)
	if len(findings) != 1 || findings[0].CheckID != CheckIDPagesDir || findings[0].Severity != SeverityWarning {
		t.Fatalf("findings = %+v, want exactly one pages-dir warning, not a crash", findings)
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
	findings := CheckAstroConfig(&countingFS{}, filepath.Join(root, "build-format-computed"))
	if len(findings) != 0 {
		t.Fatalf("findings = %+v, want none — a computed build.format value must stay silent", findings)
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
			findings := CheckAstroConfig(&countingFS{}, filepath.Join(root, tc.dir))
			for _, f := range findings {
				if f.Severity == SeverityHardStop {
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

	done := make(chan []Finding, 1)
	go func() {
		done <- CheckAstroConfig(&countingFS{}, dir)
	}()

	select {
	case findings := <-done:
		// isFile must have rejected the FIFO before Open was ever
		// reachable, so this is indistinguishable from "no config file
		// at all": no src/pages either, in this fixture, so the ordinary
		// no-config warning fires — never a crash, never a resolved
		// value read from a pipe that was never written to.
		if len(findings) != 1 || findings[0].CheckID != CheckIDPagesDir || findings[0].Severity != SeverityWarning {
			t.Fatalf("findings = %+v, want exactly one pages-dir warning (the FIFO must be treated as no config found)", findings)
		}
		mustContainAll(t, findings[0].Message, []string{"astro.config"})
	case <-time.After(2 * time.Second):
		t.Fatal("CheckAstroConfig hung for more than 2s reading a FIFO named astro.config.mjs — " +
			"isFile must reject non-regular files (Lstat, regular files only) before Open is ever called")
	}
}
