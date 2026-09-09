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
	"os"
	"path/filepath"
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

// TestThePublishMeasuresTheRegistryRatherThanAssumingIt pins the order
// of the two commands the wrapper's publish rests on.
//
// Whether publishing a version moves the registry's newest-version
// pointer, when a HIGHER version is already published under that name,
// is not written down anywhere anybody could find. This name is in
// exactly that state. So the release does not guess: it publishes, and
// then asks the registry what it now believes, and a disagreement fails
// the job.
//
// THE ORDER IS THE PROPERTY. A readback before the publish measures the
// state the publish was meant to change.
func TestThePublishMeasuresTheRegistryRatherThanAssumingIt(t *testing.T) {
	root := moduleRoot(t)
	body := readRepoFile(t, root, releaseWorkflow)

	publish := strings.Index(body, "npm publish")
	if publish < 0 {
		t.Fatalf("%s publishes no package", releaseWorkflow)
	}
	readback := strings.Index(body, "npm view curiouspub dist-tags")
	if readback < 0 {
		t.Fatalf("%s publishes and never asks the registry what it did\n"+
			"The one behaviour nobody could settle by reading is the one this release "+
			"depends on, so it is measured on every release instead.", releaseWorkflow)
	}
	if readback < publish {
		t.Errorf("%s reads the registry's tags before it publishes, which measures the state "+
			"the publish was supposed to change", releaseWorkflow)
	}
	if !strings.Contains(body, "--provenance") {
		t.Errorf("%s publishes without provenance\n"+
			"The attestation tying this package to the run that built it is half of what "+
			"makes the embedded digest worth anything.", releaseWorkflow)
	}
}

// TestThePublishJobRunsNobodyElsesCode states the property the split
// between the two publishing jobs exists to buy, and checks it.
//
// Under credential-free publishing the identity token IS the registry
// credential. The job that builds the release runs a full Go build and
// three actions from outside the actions organisation; this one runs
// two, both from inside it. That is the whole reason there are two
// jobs, and it is a property nothing else in the tree records.
func TestThePublishJobRunsNobodyElsesCode(t *testing.T) {
	root := moduleRoot(t)
	lines := readYAMLLines(readRepoFile(t, root, releaseWorkflow))

	var wrapper *job
	for i, j := range jobs(lines) {
		if j.Name == "wrapper" {
			wrapper = &jobs(lines)[i]
		}
	}
	if wrapper == nil {
		t.Fatal("the release workflow has no job publishing the npm wrapper")
	}

	uses := 0
	for _, line := range textsOf(wrapper.Body) {
		m := usesEntry.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		uses++
		action := strings.TrimSpace(strings.SplitN(m[1], "@", 2)[0])
		if !strings.HasPrefix(action, "actions/") {
			t.Errorf("the wrapper's publish job runs %s, which is not from the actions "+
				"organisation\n"+
				"This job holds the ability to publish a package to a registry. Everything "+
				"running inside it holds that too.", action)
		}
	}
	if uses == 0 {
		t.Fatal("the wrapper's publish job runs no action at all, so this row observed nothing")
	}
	// The grant, in full: a job-level block replaces the workflow floor
	// rather than adding to it.
	if !hasEntry(wrapper.Body, "contents", "read") {
		t.Error("the wrapper's publish job does not grant itself read access, and it checks " +
			"out the tree")
	}
	if !hasEntry(wrapper.Body, "id-token", "write") {
		t.Error("the wrapper's publish job mints no identity token, so it has no way to " +
			"prove who it is without a stored credential")
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
