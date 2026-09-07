package preflight

import (
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
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
			return CheckAstroConfig(fsys, root)
		},
	}
}

// render turns a run's whole output into bytes, which is what the
// determinism row compares. Both halves are included deliberately: a
// manifest that reordered between runs would be just as broken as
// findings that did, and only one of the two is visible in a findings
// slice.
func render(r check.Results) string {
	var b strings.Builder
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "finding %s %s %q %v\n", f.CheckID, f.Severity, f.Message, f.Paths)
	}
	for _, row := range r.Manifest {
		fmt.Fprintf(&b, "manifest %s ran=%v reason=%q\n", row.CheckID, row.Status, row.Reason)
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

// declining builds a single-id check's result when that check did not
// answer. The kind is environmental, which is what every decline in
// these rows means: something outside the check stopped it looking.
func declining(id, reason string) Result {
	return Result{Declined: map[string]check.Decline{id: {Reason: reason}}}
}

// engineFixture names a real fixture tree. The engine stats its root
// before it runs anything, so a row whose checks are meant to RUN has to
// point at a directory that exists — a placeholder string now produces a
// manifest of skipped checks, which is the new behaviour working rather
// than the row failing.
func engineFixture(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join("testdata", "engine", name)
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("fixture %s: %v", name, err)
	}
	return root
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

	res := Run(checks, OSFileSystem{}, filepath.Join("testdata", "engine", "clean"))
	findings, manifest := res.Findings, res.Manifest

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

	findings := Run(checks, OSFileSystem{}, engineFixture(t, "clean")).Findings

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

	res := Run(backwards, OSFileSystem{}, engineFixture(t, "clean"))
	findings, manifest := res.Findings, res.Manifest

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

	res := Run(checks, OSFileSystem{}, filepath.Join("testdata", "engine", "clean"))
	findings, manifest := res.Findings, res.Manifest

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
		if row.Status != check.Answered {
			t.Errorf("%s: Ran = false on a clean project, want true", row.CheckID)
		}
		if row.Reason != "" {
			t.Errorf("%s: Reason = %q on a check that ran, want empty", row.CheckID, row.Reason)
		}
	}
	if got := manifest.Declines(); got != nil {
		t.Errorf("Declines() = %+v, want nothing", got)
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
		newStub(&journal, declining(check.IDLockfile, "couldn't read package.json"), check.IDLockfile).check(),
	}

	res := Run(checks, OSFileSystem{}, filepath.Join("testdata", "engine", "clean"))
	findings, manifest := res.Findings, res.Manifest

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
	if lockfile.Status != check.Declined {
		t.Errorf("%s: status = %v, want declined — it could not look", check.IDLockfile, lockfile.Status)
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

	manifest := Run(checks, OSFileSystem{}, filepath.Join("testdata", "engine", "clean")).Manifest

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
			Declined: map[string]check.Decline{
				check.IDLockfile: {Reason: "couldn't read package.json"},
			},
			Findings: []check.Finding{{
				CheckID: check.IDLockfile, Severity: check.SeverityWarning, Message: "y",
			}},
		}, check.IDLockfile).check(),
	}

	res := Run(checks, OSFileSystem{}, engineFixture(t, "clean"))
	findings, manifest := res.Findings, res.Manifest

	want := []string{check.IDLockfile, "some-check-nobody-declared"}
	if got := findingIDs(findings); !equalStrings(got, want) {
		t.Errorf("findings = %v, want %v — an undeclared id sorts last but is never dropped",
			got, want)
	}
	if rows := manifest.Declines(); len(rows) != 1 || rows[0].CheckID != check.IDLockfile {
		t.Errorf("Declines() = %+v, want the lockfile row — a finding does not make a check ran",
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
			newStub(&journal, declining(check.IDLockfile, "nothing to read"), check.IDLockfile).check(),
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

	Run([]Check{s.check()}, fsys, engineFixture(t, "clean"))

	if s.gotRoot != engineFixture(t, "clean") {
		t.Errorf("root = %q, want %q", s.gotRoot, engineFixture(t, "clean"))
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

	manifest := Run([]Check{backwards.check()}, OSFileSystem{}, engineFixture(t, "clean")).Manifest

	want := []string{check.IDPagesDir, check.IDBuildFormat}
	if got := manifestIDs(manifest); !equalStrings(got, want) {
		t.Errorf("manifest = %v, want the declared order %v regardless of how the check "+
			"listed its own ids", got, want)
	}
}

// The combined-coverage row that used to sit here has MOVED to the
// result package's external test package, where it can see every
// producer rather than only this one. A whole-program guarantee kept
// inside one producer's suite disappears the day that suite is
// reorganised. Its sibling below stays: that one is about THIS engine's
// output reaching the gate, which is a fact about this package.

// TestCombineRefusesASecondProducerClaimingAnEngineCheck is the other
// half of the same seam, from this side of it. The engine has no way to
// know what another producer claimed, so the refusal has to happen where
// the two meet — and this row is what says the engine's output really
// does flow through that gate rather than around it.
func TestCombineRefusesASecondProducerClaimingAnEngineCheck(t *testing.T) {
	var journal []string
	engineOutput := Run([]Check{
		newStub(&journal, Result{}, check.IDAstroDep).check(),
	}, OSFileSystem{}, engineFixture(t, "clean"))

	imposter := check.Results{Manifest: check.Manifest{{CheckID: check.IDAstroDep}}}

	if _, err := check.Combine(engineOutput, imposter); err == nil {
		t.Fatal("a second producer claimed a check the engine had already run, and it was accepted")
	}
}

// ---------------------------------------------------------------------
// Wiring mistakes, and the root
// ---------------------------------------------------------------------

// TestEngineNilRunReportsThatItDidNotRun. The manifest exists to tell
// FOUND NOTHING from NEVER LOOKED, and a check registered without a
// function is the purest case of never looked there is — so it was the
// one case the manifest got wrong. The engine tolerated the missing
// function, computed Ran from a zero result whose reason is empty, and
// reported a tick.
//
// The distinction the manifest was built to carry must not fail on the
// wiring mistake it should be loudest about.
//
// MUTATION: report Ran: true for a nil function. Reds here.
// MUTATION: change the reason to anything not naming the check. Reds
// here. Both directions, because the branch was previously unpinned in
// both — a reason could be added or removed with every suite green.
// MUST NOT MOVE: every row whose checks have real functions.
func TestEngineNilRunReportsThatItDidNotRun(t *testing.T) {
	res := Run([]Check{{IDs: []string{check.IDAstroDep}}}, OSFileSystem{}, engineFixture(t, "clean"))

	if len(res.Manifest) != 1 {
		t.Fatalf("manifest = %v, want one row", manifestIDs(res.Manifest))
	}
	row := res.Manifest[0]
	if row.Status != check.Declined {
		t.Errorf("%s: reported as answered, but nothing was executed", row.CheckID)
	}
	if !strings.Contains(row.Reason, check.IDAstroDep) {
		t.Errorf("%s: Reason = %q, want it to name the check that was left unwired",
			row.CheckID, row.Reason)
	}
	if len(res.Findings) != 0 {
		t.Errorf("findings = %+v, want none from a check that never ran", res.Findings)
	}
}

// TestEngineStatsTheRootBeforeAnythingElse. Handed a directory that is
// not there, the engine used to run every check against it and let each
// one draw its own conclusion — which for the astro.config check is a
// confident advisory that the pages directory is missing, under a
// manifest saying every check ran.
//
// That is the worst available answer. Nothing was wrong with the
// project; the caller was pointed at the wrong place, and the report
// described a project that does not exist as though it had been read.
//
// MUTATION: drop the stat. Both rows here red on Ran.
// MUST NOT MOVE: every row that passes a real fixture directory.
func TestEngineStatsTheRootBeforeAnythingElse(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing-here")

	notADirectory := filepath.Join(t.TempDir(), "package.json")
	if err := os.WriteFile(notADirectory, []byte("{}"), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}

	cases := []struct {
		name string
		root string
	}{
		{"root does not exist", missing},
		{"root is a file", notADirectory},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var journal []string
			res := Run([]Check{
				newStub(&journal, Result{Findings: []check.Finding{{
					CheckID:  check.IDAstroDep,
					Severity: check.SeverityHardStop,
					Message:  "a check that should never have been asked",
				}}}, check.IDAstroDep).check(),
				astroConfigCheck(),
			}, OSFileSystem{}, tc.root)

			if len(journal) != 0 {
				t.Errorf("checks ran against an unusable root: %v", journal)
			}
			if len(res.Findings) != 0 {
				t.Errorf("findings = %+v, want none — nothing was read", res.Findings)
			}

			wantIDs := []string{check.IDAstroDep, check.IDPagesDir, check.IDBuildFormat}
			if got := manifestIDs(res.Manifest); !equalStrings(got, wantIDs) {
				t.Fatalf("manifest = %v, want a row per requested id %v", got, wantIDs)
			}
			for _, row := range res.Manifest {
				if row.Status != check.Declined {
					t.Errorf("%s: reported as answered against an unusable root", row.CheckID)
				}
				if row.Reason == "" {
					t.Errorf("%s: Reason is empty; a skipped check with no reason renders "+
						"as a bare colon", row.CheckID)
				}
			}
		})
	}
}

// TestEngineStatsTheRootThroughTheFilesystemItWasGiven. The seam is the
// whole reason the engine takes an FS, and a stat that went straight to
// the operating system would be the one read this package does that a
// caller cannot substitute.
func TestEngineStatsTheRootThroughTheFilesystemItWasGiven(t *testing.T) {
	fsys := &countingFS{}
	var journal []string

	res := Run([]Check{newStub(&journal, Result{}, check.IDAstroDep).check()},
		fsys, engineFixture(t, "clean"))

	if len(journal) != 1 {
		t.Fatalf("the check did not run against a good root: %v", journal)
	}
	if len(res.Manifest) != 1 || res.Manifest[0].Status != check.Answered {
		t.Errorf("manifest = %+v, want the check reported as having run", res.Manifest)
	}
}

// TestRunProducesReportsTheGateRefuses covers the two shapes this engine
// can legally emit that must never reach a renderer.
//
// NEITHER IS A DEFECT IN Run, and that is the point of putting them
// here. An empty registration and a duplicated id are wiring mistakes
// the engine has no way to distinguish from a deliberate choice, so it
// reports what it was asked to do and the gate is what refuses the
// result. Before the gate existed, both rendered: the empty one as a
// clean project that let a deploy proceed, the duplicated one as three
// skipped-check lines for one check with the deploy proceeding after.
//
// MUTATION: skip the coverage enforcement — the empty case reds.
// MUTATION: skip the duplicate enforcement — the duplicate case reds.
// MUST NOT MOVE: every row that registers each id exactly once.
func TestRunProducesReportsTheGateRefuses(t *testing.T) {
	root := engineFixture(t, "clean")

	t.Run("no checks registered at all", func(t *testing.T) {
		res := Run(nil, OSFileSystem{}, root)
		if len(res.Findings) != 0 || len(res.Manifest) != 0 {
			t.Fatalf("Run(nil) = %+v, want an empty result", res)
		}

		report, err := check.Combine(res)
		if err == nil {
			t.Fatal("an engine with no checks produced a usable report")
		}
		if report.Valid() {
			t.Error("a refused Combine returned a validated report")
		}
		var gap *check.CoverageError
		if !errors.As(err, &gap) {
			t.Fatalf("error = %#v, want a coverage failure naming what nobody ran", err)
		}
		if len(gap.Missing) != len(check.DeclaredOrder()) {
			t.Errorf("Missing = %v, want every declared check", gap.Missing)
		}
	})

	t.Run("two legal registrations claiming one id", func(t *testing.T) {
		var journal []string
		res := Run([]Check{
			newStub(&journal, Result{}, check.IDAstroDep, check.IDAstroDep).check(),
			newStub(&journal, Result{}, check.IDAstroDep).check(),
		}, OSFileSystem{}, root)

		if len(res.Manifest) != 3 {
			t.Fatalf("manifest = %v, want the three rows two registrations produce",
				manifestIDs(res.Manifest))
		}

		_, err := check.Combine(res)
		var dup *check.DuplicateCoverageError
		if !errors.As(err, &dup) {
			t.Fatalf("error = %#v, want a duplicate-claim failure", err)
		}
		if !equalStrings(dup.CheckIDs, []string{check.IDAstroDep}) {
			t.Errorf("CheckIDs = %v, want [%s]", dup.CheckIDs, check.IDAstroDep)
		}
	})
}

// ---------------------------------------------------------------------
// Per-id status
// ---------------------------------------------------------------------

// declineRow finds one id's row, or fails.
func declineRow(t *testing.T, m check.Manifest, id string) check.Ran {
	t.Helper()
	for _, row := range m {
		if row.CheckID == id {
			return row
		}
	}
	t.Fatalf("manifest = %v, want a row for %s", manifestIDs(m), id)
	return check.Ran{}
}

// TestEngineCarriesAPerIDDecline is the state the manifest could not
// express and has needed since before the sentence excusing it was
// written. One check covers two ids off one read of one file; it answers
// the first and gives up on the second, and both facts reach the row.
//
// A whole-check flag reported the give-up as a tick — on the very check
// that motivated the manifest.
//
// MUTATION: report every id of a check with the same status. The
// build-format half reds; the pages-dir half does not move.
func TestEngineCarriesAPerIDDecline(t *testing.T) {
	var journal []string
	partial := Check{
		IDs: []string{check.IDPagesDir, check.IDBuildFormat},
		Run: func(FS, string) Result {
			journal = append(journal, "ran")
			return Result{
				Findings: []check.Finding{{
					CheckID:  check.IDPagesDir,
					Severity: check.SeverityWarning,
					Message:  "Couldn't find src/pages.",
				}},
				Declined: map[string]check.Decline{
					check.IDBuildFormat: {
						Kind:   check.ByDesign,
						Reason: "the config builds this value at run time",
					},
				},
			}
		},
	}

	res := Run([]Check{partial}, OSFileSystem{}, engineFixture(t, "clean"))

	if len(journal) != 1 {
		t.Fatalf("the check did not run: %v", journal)
	}
	if got := manifestIDs(res.Manifest); !equalStrings(got,
		[]string{check.IDPagesDir, check.IDBuildFormat}) {
		t.Fatalf("manifest = %v, want a row per covered id", got)
	}

	answered := declineRow(t, res.Manifest, check.IDPagesDir)
	if answered.Status != check.Answered {
		t.Errorf("%s: status = %v, want answered — the check did answer it",
			check.IDPagesDir, answered.Status)
	}
	if answered.Reason != "" {
		t.Errorf("%s: Reason = %q on an answered row", check.IDPagesDir, answered.Reason)
	}

	declined := declineRow(t, res.Manifest, check.IDBuildFormat)
	if declined.Status != check.Declined {
		t.Errorf("%s: status = %v, want declined", check.IDBuildFormat, declined.Status)
	}
	if declined.Kind != check.ByDesign {
		t.Errorf("%s: kind = %v, want by-design — the check looked and chose not to guess",
			check.IDBuildFormat, declined.Kind)
	}
	if declined.Reason == "" {
		t.Errorf("%s: a decline with no reason renders as a bare colon", check.IDBuildFormat)
	}
}

// TestEngineDeclinesEveryIDWhenTheWholeCheckCannotRun. A root that is
// not there and a check with no function are decisions about the CHECK,
// so they land on every id it covers — and both are environmental,
// because both are something outside the check that stopped it looking.
//
// MUTATION: decline only the first id. The length assertion reds.
func TestEngineDeclinesEveryIDWhenTheWholeCheckCannotRun(t *testing.T) {
	cases := []struct {
		name  string
		root  string
		check Check
	}{
		{
			name:  "the root is not there",
			root:  filepath.Join(t.TempDir(), "nothing-here"),
			check: Check{IDs: []string{check.IDPagesDir, check.IDBuildFormat}, Run: func(FS, string) Result { return Result{} }},
		},
		{
			name:  "nothing is wired up to run it",
			root:  filepath.Join("testdata", "engine", "clean"),
			check: Check{IDs: []string{check.IDPagesDir, check.IDBuildFormat}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Run([]Check{tc.check}, OSFileSystem{}, tc.root)

			if len(res.Manifest) != 2 {
				t.Fatalf("manifest = %v, want a row per covered id", manifestIDs(res.Manifest))
			}
			for _, row := range res.Manifest {
				if row.Status != check.Declined {
					t.Errorf("%s: reported as answered", row.CheckID)
				}
				if row.Kind != check.Environmental {
					t.Errorf("%s: kind = %v, want environmental — something outside the "+
						"check stopped it looking, and the user may be able to fix it",
						row.CheckID, row.Kind)
				}
				if row.Reason == "" {
					t.Errorf("%s: no reason", row.CheckID)
				}
			}
		})
	}
}

// TestEngineKeepsADeclineForAnIDTheCheckDidNotClaim. A check declining
// something it never covered is a wiring mistake, and the engine has no
// way to tell it from a deliberate choice — so it emits the row rather
// than dropping it, and the gate refuses the result. Dropping it would
// be the failure with no symptom: the report looks complete and a
// producer's answer has vanished.
//
// FOUR STRAYS AND REPEATED RUNS, because one of each proves less than it
// looks. The row began with a single stray id and a single run, and the
// mutation that removes the ordering sailed straight past it: with one
// element there is no order to lose, and with one run there is nothing
// to compare against. Go randomises map iteration precisely so that this
// kind of dependency cannot hide, and the row now uses that rather than
// being defeated by it.
//
// MUTATION: skip declines for ids outside the check's own list. The
// membership assertion reds.
// MUTATION: stop ordering the strays. The stability assertion reds.
// MUST NOT MOVE: every other engine row, none of which has a stray.
func TestEngineKeepsADeclineForAnIDTheCheckDidNotClaim(t *testing.T) {
	// UNDECLARED ids, and that is the second correction this row needed.
	// Declared ones are put in order by the manifest's own sort, which
	// keys on the declared universe — so with declared strays the
	// engine's ordering of them is dead code and the mutation that
	// removes it changes nothing. Undeclared ids all rank equal, so
	// their order among themselves is decided here or nowhere.
	strays := []string{"stray-delta", "stray-alpha", "stray-charlie", "stray-bravo"}
	build := func() Check {
		return Check{
			IDs: []string{check.IDPagesDir},
			Run: func(FS, string) Result {
				declined := map[string]check.Decline{}
				for _, id := range strays {
					declined[id] = check.Decline{Reason: "declined something it never claimed"}
				}
				return Result{Declined: declined}
			},
		}
	}

	first := manifestIDs(Run([]Check{build()}, OSFileSystem{}, engineFixture(t, "clean")).Manifest)

	if !equalStrings(first[len(first)-len(strays):],
		[]string{"stray-alpha", "stray-bravo", "stray-charlie", "stray-delta"}) {
		t.Errorf("manifest = %v, want the strays in a settled order after the claimed id",
			first)
	}

	for _, id := range append([]string{check.IDPagesDir}, strays...) {
		found := false
		for _, got := range first {
			found = found || got == id
		}
		if !found {
			t.Errorf("manifest = %v, want the stray decline for %s kept where the gate "+
				"can see it", first, id)
		}
	}

	// Map iteration order changes between runs, so an unordered result
	// disagrees with itself within a handful of them.
	for i := 0; i < 20; i++ {
		again := manifestIDs(Run([]Check{build()}, OSFileSystem{}, engineFixture(t, "clean")).Manifest)
		if !equalStrings(again, first) {
			t.Fatalf("run %d ordered the manifest differently:\nfirst %v\nagain %v",
				i+2, first, again)
		}
	}
}
