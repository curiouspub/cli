package guard

// Guards over the npm wrapper's place in this repository's pipeline, as
// opposed to what the wrapper itself does — that is covered by its own
// suite, in its own language, against real servers and real files.
//
// THE UNIVERSE, named for the same reason the release guard names its
// own: these rows read the Makefile, the two workflows, and the
// wrapper's manifest. They read no JavaScript. A row here that tried to
// assert the install script's behaviour by reading its source would be
// the weaker half of a pair whose stronger half already exists one
// directory over.
//
// WHAT THEY ARE FOR is the seam between the two suites. The wrapper's
// own rows cannot see whether anything runs them, whether the release
// builds what they assume, or whether the version they check against is
// the version CI installs. Every one of those is a fact about this
// repository, and every one of them is silent when it breaks.

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ciWorkflow is declared beside the release guards, which reached it
// first — one name for one path, rather than two constants that agree
// today.
const wrapperManifest = "npm/package.json"

// declaredNodeFloor reads the major version the wrapper's manifest
// declares. It is THE floor: the install script derives its own check
// from this same field at run time, so there is one number, and the
// rows below hold everything else to it.
func declaredNodeFloor(t *testing.T, root string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(wrapperManifest)))
	if err != nil {
		t.Fatalf("reading %s: %v", wrapperManifest, err)
	}
	var manifest struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("parsing %s: %v", wrapperManifest, err)
	}
	declared := strings.TrimSpace(manifest.Engines.Node)
	if !strings.HasPrefix(declared, ">=") {
		t.Fatalf("%s declares its Node requirement as %q, which is not a floor this row can "+
			"read — and the install script reads it the same way", wrapperManifest, declared)
	}
	major, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(declared, ">=")))
	if err != nil {
		t.Fatalf("%s declares %q: %v", wrapperManifest, declared, err)
	}
	return major
}

// TestTheGateRunsTheWrapperSuite is the row that makes every row in the
// wrapper's own suite count for something.
//
// A suite nobody runs is a suite that goes red at leisure and is
// discovered by whoever next happens to type the command. This
// repository's stated invariant is that the gate is one target and
// every check that can fail a change is inside it, so the wrapper's
// suite has to be reachable from that target — not merely present.
func TestTheGateRunsTheWrapperSuite(t *testing.T) {
	root := moduleRoot(t)
	makefile := readRepoFile(t, root, "Makefile")

	prerequisites := recipeLine(t, makefile, "test:")
	if !strings.Contains(prerequisites, "test-npm") {
		t.Errorf("make test does not depend on the wrapper's suite (%q)\n"+
			"A postinstall script that downloads and executes a binary on other people's "+
			"machines is not a thing to test on request.", prerequisites)
	}
	if !strings.Contains(makefile, "\ntest-npm:") {
		t.Fatal("the Makefile names test-npm as a prerequisite and does not define it")
	}
	body := recipeBody(makefile, "test-npm:")
	if !strings.Contains(body, "node --test") {
		t.Errorf("make test-npm does not run the wrapper's test runner:\n%s", body)
	}
	// A target that quietly does nothing when the interpreter is absent
	// is the skip this repository already refuses elsewhere: a green
	// tick over a row that stopped running looks exactly like a green
	// tick over one that passed.
	if !strings.Contains(body, "exit 1") {
		t.Errorf("make test-npm does not fail when node is missing:\n%s\n"+
			"Degrading to a no-op turns the gate into a suggestion on any machine "+
			"without the interpreter, and says nothing while it does it.", body)
	}
}

// TestTheSnapshotChecksWhatItBuilt keeps the only observation of a
// build-time archive failure attached to the build that produces one.
//
// The release tool's own validator reads the configuration as a
// document. It cannot see a compression-only format handed two members,
// and it cannot see a name template that spells an archive differently
// from the address the install script builds. Both are build-time
// facts, and the snapshot build is the only place in this repository
// where they exist to be observed.
func TestTheSnapshotChecksWhatItBuilt(t *testing.T) {
	root := moduleRoot(t)
	body := recipeBody(readRepoFile(t, root, "Makefile"), "snapshot:")
	if !strings.Contains(body, "goreleaser") {
		t.Fatalf("make snapshot builds nothing:\n%s", body)
	}
	if !strings.Contains(body, "check-release-assets") {
		t.Errorf("make snapshot builds every archive and checks none of them:\n%s\n"+
			"A wrongly-named archive is a 404 at somebody's first install, and the "+
			"configuration validator cannot see one.", body)
	}
	if _, err := os.Stat(filepath.Join(root, "scripts", "check-release-assets.js")); err != nil {
		t.Errorf("make snapshot runs a check that is not in the tree: %v", err)
	}
}

// TestCIRunsTheWrapperOnItsDeclaredFloor holds the version CI proves
// against to the version the package promises.
//
// THE FLOOR LEG IS THE ONE THAT MATTERS. It is the only leg that can
// catch an interface that arrived after the version the package
// declares; the other is what everybody developing this will use by
// accident. They are two separate questions — what this repository
// needs to RUN the suite, and what a person needs to INSTALL the
// package — which is why this row asks the matrix to CONTAIN the floor
// rather than to equal it.
func TestCIRunsTheWrapperOnItsDeclaredFloor(t *testing.T) {
	root := moduleRoot(t)
	floor := declaredNodeFloor(t, root)
	lines := readYAMLLines(readRepoFile(t, root, ciWorkflow))

	var versions []string
	for _, j := range jobs(lines) {
		at := findKey(j.Body, "node")
		if at < 0 {
			continue
		}
		versions = append(versions, listValues(j.Body, at)...)
	}
	if len(versions) == 0 {
		t.Fatalf("%s pins no Node version anywhere, so this row would pass over an empty set",
			ciWorkflow)
	}
	if !contains(versions, strconv.Itoa(floor)) {
		t.Errorf("%s runs the wrapper's suite on %v and the package declares a floor of %d\n"+
			"The floor leg is the only one that can catch an interface that arrived after "+
			"the version this package promises to run on.", ciWorkflow, versions, floor)
	}
	if !contains(versions, "current") {
		t.Errorf("%s runs the wrapper's suite on %v and never on current\n"+
			"Current is what everybody developing this will use by accident, and a break "+
			"there is a break every contributor meets first.", ciWorkflow, versions)
	}
}

// wrapperJobName is the job that publishes the package.
//
// THE ROWS BELOW ARE ABOUT THAT JOB RATHER THAN ABOUT THE FILE. A
// property asserted over a whole workflow is satisfied by any job in it,
// which is how a row keeps its name while it stops pinning what the name
// says — and this is the job that holds an identity token, which is the
// worst place for a row that checks a silhouette.
const wrapperJobName = "wrapper"

func wrapperJob(t *testing.T, lines []yamlLine) job {
	t.Helper()
	for _, j := range jobs(lines) {
		if j.Name == wrapperJobName {
			return j
		}
	}
	t.Fatalf("%s has no %q job, and it is the one that publishes the package",
		releaseWorkflow, wrapperJobName)
	return job{}
}

// lineWith returns the first line of a block containing text.
func lineWith(lines []yamlLine, text string) (yamlLine, bool) {
	for _, l := range lines {
		if strings.Contains(l.Text, text) {
			return l, true
		}
	}
	return yamlLine{}, false
}

// runBlock returns the shell script of the step whose text mentions
// marker, reconstructed from the workflow's own lines.
func runBlock(body []yamlLine, marker string) ([]yamlLine, bool) {
	for _, step := range sequenceEntries(body) {
		if _, ok := lineWith(step, marker); !ok {
			continue
		}
		at := findKey(step, "run")
		if at < 0 {
			return nil, false
		}
		return blockAt(step, at), true
	}
	return nil, false
}

// inlineNodeProgram lifts the program out of a `node -e` invocation
// whose quote opens at the end of a line and closes at the start of a
// later one, which is the only spelling a shell script can use for a
// program of more than one line.
//
// The indentation is gone by the time these lines arrive here, and that
// costs nothing: the program is JavaScript, and the only thing the block
// reader drops is a line beginning with a hash, which this language has
// no use for.
func inlineNodeProgram(script []yamlLine) (string, bool) {
	start := -1
	for i, l := range script {
		if strings.HasSuffix(l.Text, "node -e '") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return "", false
	}
	var out []string
	for _, l := range script[start:] {
		if strings.HasPrefix(l.Text, "'") {
			return strings.Join(out, "\n"), len(out) > 0
		}
		out = append(out, l.Text)
	}
	return "", false
}

// runNodeProgram executes the readback's own program and returns what it
// did.
//
// IT FAILS RATHER THAN SKIPS when the interpreter is absent. This
// repository's gate already refuses to run without one — the wrapper's
// suite is a prerequisite of make test — and a row that quietly stops
// asserting looks exactly like a row that passed.
func runNodeProgram(t *testing.T, program string, args ...string) (int, string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node is not on PATH, and this row RUNS the release's readback rather than "+
			"reading it: %v", err)
	}
	cmd := exec.Command(node, append([]string{"-e", program}, args...)...)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("running the readback: %v", err)
	}
	return cmd.ProcessState.ExitCode(), string(out)
}

// TestThePublishMeasuresTheRegistryRatherThanAssumingIt RUNS the
// readback the release rests on, rather than reading it.
//
// Whether publishing a version moves the registry's newest-version
// pointer, when a HIGHER version is already published under that name,
// is not written down anywhere anybody could find. This name is in
// exactly that state. So the release does not guess: it publishes, and
// then asks the registry what it now believes, and a disagreement fails
// the job.
//
// THE ORDER OF TWO COMMANDS WAS ALL THIS ROW USED TO CHECK, and an order
// is a silhouette. It found the first "npm publish" anywhere in the file
// and the first readback anywhere after it, so deleting the comparison
// that makes the readback mean anything left it green — and so would a
// readback belonging to some other job. Both are now bound to the job
// that holds the identity token, and the comparison itself is executed:
// against a registry that agrees it must exit zero, and against one
// pointing elsewhere it must exit non-zero and say where.
func TestThePublishMeasuresTheRegistryRatherThanAssumingIt(t *testing.T) {
	root := moduleRoot(t)
	lines := readYAMLLines(readRepoFile(t, root, releaseWorkflow))
	wrapper := wrapperJob(t, lines)

	publish, ok := lineWith(wrapper.Body, "npm publish")
	if !ok {
		t.Fatalf("the %q job publishes no package", wrapperJobName)
	}
	if !strings.Contains(publish.Text, "--provenance") {
		t.Errorf("%s:%d publishes without provenance\n"+
			"The attestation tying this package to the run that built it is half of what "+
			"makes the embedded digest worth anything.", releaseWorkflow, publish.N)
	}
	readback, ok := lineWith(wrapper.Body, "npm view curiouspub dist-tags")
	if !ok {
		t.Fatalf("the %q job publishes and never asks the registry what it did\n"+
			"The one behaviour nobody could settle by reading is the one this release "+
			"depends on, so it is measured on every release instead.", wrapperJobName)
	}
	// THE ORDER IS STILL A PROPERTY. A readback before the publish
	// measures the state the publish was meant to change.
	if readback.N < publish.N {
		t.Errorf("%s reads the registry's tags at line %d and publishes at line %d",
			releaseWorkflow, readback.N, publish.N)
	}

	script, ok := runBlock(wrapper.Body, "npm view curiouspub dist-tags")
	if !ok {
		t.Fatalf("the readback is not a script this row can read, so nothing below it ran")
	}
	joined := strings.Join(textsOf(script), "\n")

	// THE HALF THAT IS READ RATHER THAN RUN, and saying which is which is
	// the point. The version being compared has to come from the tag that
	// triggered the release, and the shell that derives it cannot be
	// executed from here on every machine this suite runs on. So the
	// derivation is read, and the comparison — the part the finding was
	// about — is executed below.
	if !strings.Contains(joined, `version="${GITHUB_REF_NAME#v}"`) {
		t.Errorf("the readback does not take its version from the tag that triggered it:\n%s",
			joined)
	}
	if !strings.Contains(joined, `"${version}"`) {
		t.Errorf("the readback compares against something other than that version:\n%s", joined)
	}

	program, ok := inlineNodeProgram(script)
	if !ok {
		t.Fatalf("the readback carries no program this row can run:\n%s", joined)
	}

	// The arrangement nobody could find documented, which is this
	// package's own: a placeholder already published at a higher version
	// than the one being released.
	if code, out := runNodeProgram(t, program, "0.1.0", `{"latest":"0.1.0"}`); code != 0 {
		t.Errorf("the readback fails a release the registry agrees with (exit %d):\n%s", code, out)
	}
	code, out := runNodeProgram(t, program, "0.1.0", `{"latest":"1.0.0"}`)
	if code == 0 {
		t.Errorf("the readback passes while the registry points at another version\n" +
			"Without the comparison this step is a command whose output nobody reads, and " +
			"the release ships a package nobody can install by name.")
	}
	if !strings.Contains(out, "1.0.0") {
		t.Errorf("the readback refuses without saying where the registry points:\n%s\n"+
			"The operator action on a red readback is to move the pointer by hand, which "+
			"needs to know what it points at now.", out)
	}
}

// wrapperPublishActions is the list this job's design claims, written
// out because the claim is the exact two and not the namespace.
//
// checkout, because the package files are in the tree; setup-node,
// because publishing needs a newer package manager than the wrapper's
// own floor. Widening this list is an edit to this file, in the diff
// that wants it, which is the only way a reader ever sees the question
// asked.
var wrapperPublishActions = []string{"actions/checkout", "actions/setup-node"}

// TestThePublishJobRunsNobodyElsesCode states the property the split
// between the two publishing jobs exists to buy, and checks it.
//
// Under credential-free publishing the identity token IS the registry
// credential. The job that builds the release runs a full Go build and
// three actions from outside the actions organisation; this one runs
// two, both from inside it. That is the whole reason there are two
// jobs, and it is a property nothing else in the tree records.
//
// THE NAMESPACE WAS A SILHOUETTE. Asking only whether each action came
// from the actions organisation admitted any number of them: adding
// actions/github-script to this job passed the row while contradicting
// the exact list the design claims, measured before this change. An
// action that runs arbitrary script from a workflow input, inside the
// job holding the publish credential, is precisely the widening the
// split was made to prevent — and it would have arrived green.
func TestThePublishJobRunsNobodyElsesCode(t *testing.T) {
	root := moduleRoot(t)
	lines := readYAMLLines(readRepoFile(t, root, releaseWorkflow))
	wrapper := wrapperJob(t, lines)

	var ran []string
	for _, line := range textsOf(wrapper.Body) {
		m := usesEntry.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		value := strings.TrimSpace(m[1])
		if hash := strings.Index(value, "#"); hash >= 0 {
			value = strings.TrimSpace(value[:hash])
		}
		value = strings.Trim(value, `"'`)
		action, ref := value, ""
		if at := strings.LastIndex(value, "@"); at >= 0 {
			action, ref = value[:at], value[at+1:]
		}
		ran = append(ran, action)
		// PINNED, and asserted here rather than left to the repository-wide
		// pinning row: that row is about every workflow, and this one is
		// about the two things allowed inside a job that can publish.
		if !pinnedSHA.MatchString(ref) {
			t.Errorf("the %q job runs %s at %q, which is not a commit identifier\n"+
				"A tag is mutable, and whoever can move one runs their code inside the job "+
				"that holds the publish credential.", wrapperJobName, action, ref)
		}
	}

	sort.Strings(ran)
	want := append([]string(nil), wrapperPublishActions...)
	sort.Strings(want)
	// Joined rather than compared element by element: an action path
	// carries no spaces, so one string is the whole comparison.
	if strings.Join(ran, " ") != strings.Join(want, " ") {
		t.Errorf("the %q job runs %v, and its design claims exactly %v\n"+
			"Everything running in this job holds the ability to publish a package to a "+
			"registry. The list is short on purpose, and it is the SET that is the "+
			"property — a namespace admits any number of them.", wrapperJobName, ran, want)
	}

	// The grant, in full: a job-level block replaces the workflow floor
	// rather than adding to it.
	if !hasEntry(wrapper.Body, "contents", "read") {
		t.Errorf("the %q job does not grant itself read access, and it checks out the tree",
			wrapperJobName)
	}
	if !hasEntry(wrapper.Body, "id-token", "write") {
		t.Errorf("the %q job mints no identity token, so it has no way to prove who it is "+
			"without a stored credential", wrapperJobName)
	}
}

// recipeLine returns the text after a Makefile target's colon.
func recipeLine(t *testing.T, makefile, target string) string {
	t.Helper()
	for _, line := range strings.Split(makefile, "\n") {
		if strings.HasPrefix(line, target) {
			return strings.TrimSpace(strings.TrimPrefix(line, target))
		}
	}
	t.Fatalf("the Makefile has no %s target", target)
	return ""
}

// recipeBody returns the indented lines under a Makefile target.
func recipeBody(makefile, target string) string {
	lines := strings.Split(makefile, "\n")
	var out []string
	collecting := false
	for _, line := range lines {
		if strings.HasPrefix(line, target) {
			collecting = true
			continue
		}
		if !collecting {
			continue
		}
		if line == "" || strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "#") {
			out = append(out, line)
			continue
		}
		break
	}
	return strings.Join(out, "\n")
}
