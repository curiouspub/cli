package preflight

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/check"
)

// ---------------------------------------------------------------------
// Test doubles
//
// THREE OF THE FIVE CHECKS DO NOT EXIST YET and are not this change's to
// write. The engine's job is ordering, aggregation and the manifest, so
// every row below drives the absent checks with a stand-in whose result
// the row states outright, and the two that DO exist — pages-dir and
// build-format, both produced by the astro.config check — run for real
// against a fixture tree. A stand-in here is a statement about what the
// engine does with a check's answer, never a guess at what that check's
// answer will be.
// ---------------------------------------------------------------------

// stub is a check whose answer the caller dictates. It records the
// arguments it was handed and the order it ran in, through a shared
// journal, so a row can assert that every check ran and that they ran in
// the declared order.
type stub struct {
	ids     []string
	result  Result
	journal *[]string
	gotRoot string
	gotFS   FS
}

func (s *stub) check() Check {
	return Check{
		IDs: s.ids,
		Run: func(fsys FS, root string) Result {
			s.gotFS = fsys
			s.gotRoot = root
			*s.journal = append(*s.journal, strings.Join(s.ids, "+"))
			return s.result
		},
	}
}

// stubs builds a check that reports the given result for the given ids,
// sharing one journal.
func newStub(journal *[]string, result Result, ids ...string) *stub {
	return &stub{ids: ids, result: result, journal: journal}
}

// astroConfigCheck wraps the real astro.config check in the shape the
// engine registers. It is the one adapter this file needs, and its
// existence is the argument that the engine's seam fits a check that was
// written before the engine was.
func astroConfigCheck() Check {
	return Check{
		IDs: []string{check.IDPagesDir, check.IDBuildFormat},
		Run: func(fsys FS, root string) Result {
			return Result{Findings: CheckAstroConfig(fsys, root)}
		},
	}
}

// render turns a run's whole output into bytes, which is what the
// determinism row compares. Both halves are included deliberately: a
// manifest that reordered between runs would be just as broken as
// findings that did, and only one of the two is visible in a findings
// slice.
func render(findings []check.Finding, manifest check.Manifest) string {
	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "finding %s %s %q %v\n", f.CheckID, f.Severity, f.Message, f.Paths)
	}
	for _, row := range manifest {
		fmt.Fprintf(&b, "manifest %s ran=%v reason=%q\n", row.CheckID, row.Ran, row.Reason)
	}
	return b.String()
}

func manifestIDs(m check.Manifest) []string {
	var out []string
	for _, row := range m {
		out = append(out, row.CheckID)
	}
	return out
}

func findingIDs(findings []check.Finding) []string {
	var out []string
	for _, f := range findings {
		out = append(out, f.CheckID)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------
// Aggregate behaviour
// ---------------------------------------------------------------------

// TestEngineRunsEveryCheckDespiteAHardStop is the aggregate rule the
// whole engine exists for. A project missing astro AND a lockfile should
// learn both facts in one run rather than discover the second only after
// fixing the first, so nothing short-circuits on the first hard failure.
//
// MUTATION: return as soon as a hard stop is seen. Both halves red — the
// journal loses two entries and the second hard stop disappears.
func TestEngineRunsEveryCheckDespiteAHardStop(t *testing.T) {
	var journal []string
	checks := []Check{
		newStub(&journal, Result{Findings: []check.Finding{{
			CheckID:  check.IDAstroDep,
			Severity: check.SeverityHardStop,
			Message:  "not an Astro project",
		}}}, check.IDAstroDep).check(),
		newStub(&journal, Result{Findings: []check.Finding{{
			CheckID:  check.IDLockfile,
			Severity: check.SeverityHardStop,
			Message:  "no lockfile found",
		}}}, check.IDLockfile).check(),
		newStub(&journal, Result{Findings: []check.Finding{{
			CheckID:  check.IDLocalhost,
			Severity: check.SeverityWarning,
			Message:  "a development URL is hard-coded",
		}}}, check.IDLocalhost).check(),
	}

	findings, manifest := Run(checks, OSFileSystem{}, filepath.Join("testdata", "engine", "clean"))

	if len(journal) != 3 {
		t.Fatalf("checks that ran = %v, want all three", journal)
	}
	want := []string{check.IDAstroDep, check.IDLockfile, check.IDLocalhost}
	if got := findingIDs(findings); !equalStrings(got, want) {
		t.Errorf("findings = %v, want %v — every check's finding, hard stops first", got, want)
	}
	if len(manifest) != 3 {
		t.Errorf("manifest = %v, want one row per check id", manifestIDs(manifest))
	}
}

// TestEngineReportsHardStopsBeforeWarnings pins the report's shape: every
// hard stop, then every warning, and notes last because no surface shows
// them. Within one severity the order is the declared check order, so a
// person in the wrong directory reads "this isn't an Astro project"
// first rather than fourth.
//
// MUTATION: drop the severity key from the sort. The row reds on the
// warning arriving before the second hard stop.
func TestEngineReportsHardStopsBeforeWarnings(t *testing.T) {
	var journal []string
	checks := []Check{
		newStub(&journal, Result{Findings: []check.Finding{{
			CheckID: check.IDAstroDep, Severity: check.SeverityHardStop, Message: "a",
		}}}, check.IDAstroDep).check(),
		newStub(&journal, Result{Findings: []check.Finding{{
			CheckID: check.IDLockfile, Severity: check.SeverityWarning, Message: "b",
		}}}, check.IDLockfile).check(),
		newStub(&journal, Result{Findings: []check.Finding{{
			CheckID: check.IDPagesDir, Severity: check.SeverityHardStop, Message: "c",
		}, {
			CheckID: check.IDBuildFormat, Severity: check.SeverityNote, Message: "d",
		}}}, check.IDPagesDir, check.IDBuildFormat).check(),
		newStub(&journal, Result{Findings: []check.Finding{{
			CheckID: check.IDLocalhost, Severity: check.SeverityWarning, Message: "e",
		}}}, check.IDLocalhost).check(),
	}

	findings, _ := Run(checks, OSFileSystem{}, "irrelevant")

	want := []string{
		check.IDAstroDep,    // hard stop, rank 0
		check.IDPagesDir,    // hard stop, rank 2
		check.IDLockfile,    // warning,   rank 1
		check.IDLocalhost,   // warning,   rank 4
		check.IDBuildFormat, // note,      rank 3
	}
	if got := findingIDs(findings); !equalStrings(got, want) {
		t.Errorf("report order = %v, want %v", got, want)
	}
}

// TestEngineOrderIsFixedRegardlessOfRegistrationOrder is the other half
// of "the order is declared". Order that merely mirrors the caller's
// slice is the caller's rule, not the engine's, and a second caller
// assembling the same checks differently would get a different report.
//
// MUTATION: rank by position in the checks slice instead of by the
// declared order. This row reds; the row above does not, because there
// the two orders happen to agree.
func TestEngineOrderIsFixedRegardlessOfRegistrationOrder(t *testing.T) {
	var journal []string
	backwards := []Check{
		newStub(&journal, Result{Findings: []check.Finding{{
			CheckID: check.IDLocalhost, Severity: check.SeverityWarning, Message: "e",
		}}}, check.IDLocalhost).check(),
		newStub(&journal, Result{Findings: []check.Finding{{
			CheckID: check.IDLockfile, Severity: check.SeverityWarning, Message: "b",
		}}}, check.IDLockfile).check(),
		newStub(&journal, Result{Findings: []check.Finding{{
			CheckID: check.IDAstroDep, Severity: check.SeverityWarning, Message: "a",
		}}}, check.IDAstroDep).check(),
	}

	findings, manifest := Run(backwards, OSFileSystem{}, "irrelevant")

	want := []string{check.IDAstroDep, check.IDLockfile, check.IDLocalhost}
	if got := findingIDs(findings); !equalStrings(got, want) {
		t.Errorf("report order = %v, want the declared order %v", got, want)
	}
	if got := manifestIDs(manifest); !equalStrings(got, want) {
		t.Errorf("manifest order = %v, want the declared order %v", got, want)
	}
	if !equalStrings(journal, want) {
		t.Errorf("execution order = %v, want the declared order %v — the cheapest and "+
			"most fundamental check runs first", journal, want)
	}
}

// ---------------------------------------------------------------------
// The manifest
// ---------------------------------------------------------------------

// TestEngineCleanProjectYieldsNoFindingsAndAFullManifest is the row that
// separates FOUND NOTHING from NEVER LOOKED. A findings-only engine
// passes every other manifest row by accident if the manifest is never
// wired at all, because a clean project's finding slice is empty either
// way — this one fails unless the manifest is really built.
//
// The astro.config check runs FOR REAL against the fixture, which was
// measured to produce no findings on a tree with src/pages present and
// no config file.
//
// MUTATION (the required one): drop a check's manifest row while leaving
// its finding logic intact. This row reds by length and by order.
func TestEngineCleanProjectYieldsNoFindingsAndAFullManifest(t *testing.T) {
	var journal []string
	checks := []Check{
		newStub(&journal, Result{}, check.IDAstroDep).check(),
		newStub(&journal, Result{}, check.IDLockfile).check(),
		astroConfigCheck(),
		newStub(&journal, Result{}, check.IDLocalhost).check(),
	}

	findings, manifest := Run(checks, OSFileSystem{}, filepath.Join("testdata", "engine", "clean"))

	if len(findings) != 0 {
		t.Errorf("findings on a clean project = %+v, want none", findings)
	}

	wantIDs := []string{
		check.IDAstroDep,
		check.IDLockfile,
		check.IDPagesDir,
		check.IDBuildFormat,
		check.IDLocalhost,
	}
	if got := manifestIDs(manifest); !equalStrings(got, wantIDs) {
		t.Fatalf("manifest = %v, want one row per requested check id in the declared order %v",
			got, wantIDs)
	}
	for _, row := range manifest {
		if !row.Ran {
			t.Errorf("%s: Ran = false on a clean project, want true", row.CheckID)
		}
		if row.Reason != "" {
			t.Errorf("%s: Reason = %q on a check that ran, want empty", row.CheckID, row.Reason)
		}
	}
	if got := manifest.NotRun(); got != nil {
		t.Errorf("NotRun() = %+v, want nothing", got)
	}
}

// TestEngineManifestCarriesWhyACheckDidNotRun is the state a findings-only
// stream cannot express. With package.json missing, the check that reads
// it fails with that as its reason and every check that NEEDED it did not
// look — and "did not look" has to be recoverable, because a skipped
// check rendered as a tick is a lie the user will act on.
//
// ASSERTED ON THE MANIFEST, never on the absence of a finding: those two
// are indistinguishable from outside, which is the entire reason the
// manifest exists.
//
// MUTATION: stop copying Result.NotRun into the row. The Ran and Reason
// assertions both red.
func TestEngineManifestCarriesWhyACheckDidNotRun(t *testing.T) {
	var journal []string
	checks := []Check{
		newStub(&journal, Result{Findings: []check.Finding{{
			CheckID:  check.IDAstroDep,
			Severity: check.SeverityHardStop,
			Message:  "couldn't read package.json in this directory",
			Paths:    []string{"package.json"},
		}}}, check.IDAstroDep).check(),
		newStub(&journal, Result{NotRun: "couldn't read package.json"}, check.IDLockfile).check(),
	}

	findings, manifest := Run(checks, OSFileSystem{}, filepath.Join("testdata", "engine", "clean"))

	if len(findings) != 1 || findings[0].CheckID != check.IDAstroDep {
		t.Fatalf("findings = %+v, want one from %s", findings, check.IDAstroDep)
	}
	if !strings.Contains(findings[0].Message, "package.json") {
		t.Errorf("message = %q, want it to name package.json", findings[0].Message)
	}
	if !equalStrings(findings[0].Paths, []string{"package.json"}) {
		t.Errorf("Paths = %v, want the file the finding is about", findings[0].Paths)
	}

	var lockfile check.Ran
	var found bool
	for _, row := range manifest {
		if row.CheckID == check.IDLockfile {
			lockfile, found = row, true
		}
	}
	if !found {
		t.Fatalf("manifest = %v, want a row for %s", manifestIDs(manifest), check.IDLockfile)
	}
	if lockfile.Ran {
		t.Errorf("%s: Ran = true, want false — it could not look", check.IDLockfile)
	}
	if !strings.Contains(lockfile.Reason, "package.json") {
		t.Errorf("%s: Reason = %q, want it to name package.json", check.IDLockfile, lockfile.Reason)
	}
}

// TestEngineManifestRowPerDeclaredID covers a check that reports on more
// than one id — the astro.config check reports on two. One row per CHECK
// would leave the second id with no manifest entry at all, and a renderer
// could then never say whether it had been looked at.
//
// MUTATION: emit one row per check rather than per id. Reds on length.
func TestEngineManifestRowPerDeclaredID(t *testing.T) {
	var journal []string
	checks := []Check{astroConfigCheck(), newStub(&journal, Result{}, check.IDLocalhost).check()}

	_, manifest := Run(checks, OSFileSystem{}, filepath.Join("testdata", "engine", "clean"))

	want := []string{check.IDPagesDir, check.IDBuildFormat, check.IDLocalhost}
	if got := manifestIDs(manifest); !equalStrings(got, want) {
		t.Errorf("manifest = %v, want %v", got, want)
	}
}

// TestEngineKeepsWhatItWasNotExpecting covers the two ways a check can
// contradict its own declaration. Neither is silently dropped, because
// dropping something a check went to the trouble of reporting is the one
// failure mode with no symptom: the run looks clean.
//
// MUTATION: filter findings to the check's declared ids. The undeclared
// finding vanishes and the row reds.
func TestEngineKeepsWhatItWasNotExpecting(t *testing.T) {
	var journal []string
	checks := []Check{
		// Declares one id, reports on another.
		newStub(&journal, Result{Findings: []check.Finding{{
			CheckID: "some-check-nobody-declared", Severity: check.SeverityWarning, Message: "x",
		}}}, check.IDAstroDep).check(),
		// Says it could not run AND has something to say anyway.
		newStub(&journal, Result{
			NotRun: "couldn't read package.json",
			Findings: []check.Finding{{
				CheckID: check.IDLockfile, Severity: check.SeverityWarning, Message: "y",
			}},
		}, check.IDLockfile).check(),
	}

	findings, manifest := Run(checks, OSFileSystem{}, "irrelevant")

	want := []string{check.IDLockfile, "some-check-nobody-declared"}
	if got := findingIDs(findings); !equalStrings(got, want) {
		t.Errorf("findings = %v, want %v — an undeclared id sorts last but is never dropped",
			got, want)
	}
	if rows := manifest.NotRun(); len(rows) != 1 || rows[0].CheckID != check.IDLockfile {
		t.Errorf("NotRun() = %+v, want the lockfile row — a finding does not make a check ran",
			rows)
	}
}

// ---------------------------------------------------------------------
// Determinism
// ---------------------------------------------------------------------

// TestEngineOutputIsByteIdenticalAcrossRuns is what makes the report
// something a person can diff and a test can pin. Both halves of the
// output are compared: a manifest that reordered between runs is as
// broken as findings that did, and only one of those is visible in a
// findings slice.
//
// MUTATION: swap the stable sort for an unstable one and give two
// findings the same rank. Reds intermittently, which is why the ranking
// is total rather than merely sorted.
func TestEngineOutputIsByteIdenticalAcrossRuns(t *testing.T) {
	build := func() []Check {
		var journal []string
		return []Check{
			newStub(&journal, Result{Findings: []check.Finding{
				{CheckID: check.IDAstroDep, Severity: check.SeverityWarning, Message: "first"},
				{CheckID: check.IDAstroDep, Severity: check.SeverityWarning, Message: "second"},
				{CheckID: check.IDAstroDep, Severity: check.SeverityWarning, Message: "third"},
			}}, check.IDAstroDep).check(),
			newStub(&journal, Result{NotRun: "nothing to read"}, check.IDLockfile).check(),
			astroConfigCheck(),
			newStub(&journal, Result{}, check.IDLocalhost).check(),
		}
	}
	root := filepath.Join("testdata", "engine", "no-pages-dir")

	first := render(Run(build(), OSFileSystem{}, root))
	for i := 0; i < 8; i++ {
		if again := render(Run(build(), OSFileSystem{}, root)); again != first {
			t.Fatalf("run %d differs:\n--- first ---\n%s\n--- again ---\n%s", i+2, first, again)
		}
	}
	if !strings.Contains(first, "finding pages-dir warning") {
		t.Errorf("output did not contain the real check's warning:\n%s", first)
	}
}

// TestEngineHandsEachCheckTheRootAndFilesystem pins the seam. A check
// that is handed a different root than the caller asked about reports on
// the wrong project, and there is nothing in a finding that would say so.
func TestEngineHandsEachCheckTheRootAndFilesystem(t *testing.T) {
	var journal []string
	fsys := &countingFS{}
	s := newStub(&journal, Result{}, check.IDAstroDep)

	Run([]Check{s.check()}, fsys, filepath.Join("some", "project"))

	if s.gotRoot != filepath.Join("some", "project") {
		t.Errorf("root = %q, want %q", s.gotRoot, filepath.Join("some", "project"))
	}
	if s.gotFS != FS(fsys) {
		t.Errorf("filesystem = %#v, want the one the caller passed", s.gotFS)
	}
}

// ---------------------------------------------------------------------
// What the engine must NOT do
// ---------------------------------------------------------------------

// dialRecorder stands in for the process's default transport and refuses
// every request, counting them. Anything reaching the network through
// the standard client goes through here.
type dialRecorder struct{ dials int }

func (d *dialRecorder) RoundTrip(*http.Request) (*http.Response, error) {
	d.dials++
	return nil, errors.New("pre-flight must not reach the network")
}

// dialsDuring runs the engine with the default transport replaced and
// reports how many requests it made.
func dialsDuring(t *testing.T, checks []Check, root string) int {
	t.Helper()
	recorder := &dialRecorder{}
	restore := http.DefaultTransport
	http.DefaultTransport = recorder
	t.Cleanup(func() { http.DefaultTransport = restore })

	Run(checks, OSFileSystem{}, root)
	return recorder.dials
}

// TestEngineMakesNoNetworkCalls is the local-truths-before-global-state
// rule made checkable: a project that cannot deploy makes zero network
// calls, so pre-flight must complete before anything is sent anywhere.
//
// THE POSITIVE CONTROL IS THE SECOND HALF AND IT IS NOT DECORATION. A
// refusing transport nobody has watched fire is a transport that may not
// be wired at all, and the first row would pass just as happily against
// an instrument that observes nothing.
func TestEngineMakesNoNetworkCalls(t *testing.T) {
	var journal []string
	real := []Check{
		newStub(&journal, Result{}, check.IDAstroDep).check(),
		astroConfigCheck(),
		newStub(&journal, Result{}, check.IDLocalhost).check(),
	}
	root := filepath.Join("testdata", "engine", "no-pages-dir")

	if dials := dialsDuring(t, real, root); dials != 0 {
		t.Errorf("network requests during pre-flight = %d, want 0", dials)
	}

	t.Run("positive control: the instrument sees a dial", func(t *testing.T) {
		dialing := Check{
			IDs: []string{check.IDLocalhost},
			Run: func(FS, string) Result {
				resp, err := http.Get("http://a-host-that-does-not-resolve.invalid/")
				if err == nil {
					_ = resp.Body.Close()
				}
				return Result{}
			},
		}
		if dials := dialsDuring(t, []Check{dialing}, root); dials != 1 {
			t.Errorf("the refusing transport recorded %d requests from a check that "+
				"deliberately makes one, want 1 — it is not wired", dials)
		}
	})
}

// TestEngineWritesNothing pins the other half of the same promise. The
// engine reads files under the project directory and nothing else: it
// creates nothing, touches nothing, and runs none of the user's code.
//
// Names, sizes and modification times are all compared, because a
// rewrite that happened to produce identical bytes would still be a
// write, and it would be the kind that shows up later as a dirty working
// tree somebody else has to explain.
func TestEngineWritesNothing(t *testing.T) {
	root := filepath.Join("testdata", "engine", "clean")

	type entry struct {
		size int64
		mod  time.Time
	}
	snapshot := func() map[string]entry {
		out := map[string]entry{}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			out[path] = entry{size: info.Size(), mod: info.ModTime()}
			return nil
		})
		if err != nil {
			t.Fatalf("walking the fixture: %v", err)
		}
		return out
	}

	before := snapshot()

	var journal []string
	Run([]Check{
		newStub(&journal, Result{}, check.IDAstroDep).check(),
		astroConfigCheck(),
	}, OSFileSystem{}, root)

	after := snapshot()

	for path, was := range before {
		now, still := after[path]
		if !still {
			t.Errorf("%s disappeared during pre-flight", path)
			continue
		}
		if now.size != was.size {
			t.Errorf("%s: size %d -> %d", path, was.size, now.size)
		}
		if !now.mod.Equal(was.mod) {
			t.Errorf("%s: modification time %s -> %s", path, was.mod, now.mod)
		}
	}
	for path := range after {
		if _, existed := before[path]; !existed {
			t.Errorf("%s was created during pre-flight", path)
		}
	}
}

// TestEngineManifestOrderSurvivesACheckDeclaringItsIDsBackwards.
//
// ADDED AFTER A MUTATION FAILED TO RED, which is the only reason it
// exists and is worth saying. Removing the manifest's own sort changed
// nothing in any other row here, because checks are executed in the
// declared order and their rows are therefore appended in it — the sort
// was doing real work only for a case nothing exercised. An unreddened
// mutation means the line is either dead or unguarded; this row settles
// which.
func TestEngineManifestOrderSurvivesACheckDeclaringItsIDsBackwards(t *testing.T) {
	var journal []string
	backwards := &stub{
		ids:     []string{check.IDBuildFormat, check.IDPagesDir},
		journal: &journal,
	}

	_, manifest := Run([]Check{backwards.check()}, OSFileSystem{}, "irrelevant")

	want := []string{check.IDPagesDir, check.IDBuildFormat}
	if got := manifestIDs(manifest); !equalStrings(got, want) {
		t.Errorf("manifest = %v, want the declared order %v regardless of how the check "+
			"listed its own ids", got, want)
	}
}
