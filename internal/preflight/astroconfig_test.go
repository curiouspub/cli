package preflight

import (
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

func (c *countingFS) Stat(name string) (fs.FileInfo, error) { return os.Stat(name) }

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
	{"srcdir-hardstop", []wantFinding{
		{CheckIDPagesDir, SeverityHardStop, []string{"astro.config.mjs", "www", "www/pages"}, nil},
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
		{CheckIDPagesDir, SeverityHardStop, []string{"src/pages"}, []string{"old"}},
	}},
	{"srcdir-block-comment", []wantFinding{
		{CheckIDPagesDir, SeverityHardStop, []string{"src/pages"}, []string{"old"}},
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

	// The five rows below came from an independent reconstruction of this
	// suite: a reader who wrote an acceptance suite from the written
	// specification alone, without ever seeing this file, this table, or
	// the fixtures above. Comparing the two suites row by row turned up
	// no real disagreement — but it did turn up a coverage gap, merged
	// here with origin notes on the rows that close it.

	// .cjs and .cts had NEVER been exercised, alone or together, before
	// this row. That is notable specifically because their ORDER is the
	// one Astro fact this check got wrong once already, in an earlier
	// draft, and corrected only by reading Astro's own source rather than
	// reasoning about it a second time (see the comment on
	// configCandidates). A fact that was wrong once, then fixed and
	// re-verified, still went untested until a reader who had never seen
	// either the mistake or the fix wrote this row from the written rule
	// alone. .cts's srcDir deliberately points at a directory with no
	// pages/ subdirectory, so a regression back to the wrong order would
	// flip this row from a warning to a wrong hard stop.
	{"srcdir-cjs-precedes-cts", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, []string{"astro.config.cjs", "astro.config.cts"}, nil},
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
	{"srcdir-hardstop-plus-buildformat-warning", []wantFinding{
		{CheckIDPagesDir, SeverityHardStop, []string{"astro.config.mjs", "www", "www/pages"}, nil},
		{CheckIDBuildFormat, SeverityWarning, []string{"file"}, nil},
	}},
	{"srcdir-pathjoin-plus-buildformat-warning", []wantFinding{
		{CheckIDPagesDir, SeverityWarning, nil, nil},
		{CheckIDBuildFormat, SeverityWarning, []string{"preserve"}, nil},
	}},
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
		"srcdir-hardstop",
		"srcdir-template-interp",
		"srcdir-pathjoin",
		"srcdir-commented-out",
		"srcdir-block-comment",
		"srcdir-two-keys",
		"srcdir-absolute",
		"srcdir-outside-root",
		"srcdir-both-configs-ambiguous",
		"srcdir-no-config",
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
// No node, ever.
// ---------------------------------------------------------------------

// TestNoNodeProcessSpawned proves this package cannot spawn any process,
// node included, by checking that os/exec is absent from its PRODUCTION
// dependency graph. It deliberately omits -test: this test file itself
// uses exec.Command to run `go list`, and a self-referential failure
// from that would prove nothing about the check this package ships.
func TestNoNodeProcessSpawned(t *testing.T) {
	root := moduleRootForTest(t)

	cmd := exec.Command("go", "list", "-e", "-deps", "./internal/preflight")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "os/exec") {
		t.Fatalf("internal/preflight's own dependency graph includes os/exec — "+
			"this check reads someone else's project and must never be able to spawn a "+
			"process, node included:\n%s", out)
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
