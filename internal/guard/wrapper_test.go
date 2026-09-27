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
	"regexp"
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
//
// # REACHABLE, WHICH IS NOT THE SAME AS "IN THE PREREQUISITE LINE"
//
// This row read the prerequisite line and nothing else until the day
// `test` became a recipe that invokes each half and
// collects the status — so that the first red half no longer stops the
// others from running at all. The gate still ran the wrapper's suite;
// the row went red anyway, because its KEY was narrower than its claim.
// That is the same failure as a guard whose allowlist is still accurate
// under a coarsened key: it reads as coverage of "reachable" and covers
// one spelling of it.
//
// So both spellings count, and the row says which one it found — a
// message naming only one form would send the next person to add a
// prerequisite that the recipe form does not need.
func TestTheGateRunsTheWrapperSuite(t *testing.T) {
	root := moduleRoot(t)
	makefile := readRepoFile(t, root, "Makefile")

	prerequisites := recipeLine(t, makefile, "test:")
	gateBody := recipeBody(makefile, "test:")
	if !strings.Contains(prerequisites, "test-npm") && !strings.Contains(gateBody, "test-npm") {
		t.Errorf("make test neither depends on the wrapper's suite nor invokes it "+
			"(prerequisites %q, recipe:\n%s)\n"+
			"A postinstall script that downloads and executes a binary on other people's "+
			"machines is not a thing to test on request.", prerequisites, gateBody)
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
// that holds the identity token, and the program itself is executed.
//
// AND IT IS EXECUTED AGAINST A REGISTRY THAT TAKES ITS TIME. The first
// version of the readback was one read, and it went red on two releases
// whose publishes were correct: the registry says its publish is
// eventual, and it was read before it had finished. So the cases below
// run the real program against a stand-in registry that answers from a
// script — stale first, then settled; failing a read, then answering;
// never settling at all — with the interval and the window shrunk to
// fractions of a second. The shrinking is the program's own arguments,
// not a copy of it, so the loop under test is the loop that ships.
//
// MUTATIONS RUN, performed and observed: replacing the loop with a single
// read reds the stale-then-settled and failed-then-answered cases and
// nothing else; making the two window endings share one message reds
// the case whose registry never finished, which must not tell anybody to
// move a pointer.
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
	const tagsRead = `"dist-tags"`
	readback, ok := lineWith(wrapper.Body, tagsRead)
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

	script, ok := runBlock(wrapper.Body, tagsRead)
	if !ok {
		t.Fatalf("the readback is not a script this row can read, so nothing below it ran")
	}
	joined := strings.Join(textsOf(script), "\n")

	// THE HALF THAT IS READ RATHER THAN RUN, and saying which is which is
	// the point. The version has to come from the tag that triggered the
	// release, and the arguments the job passes — the interval, the
	// window and the client — are what make the loop below the one that
	// runs there. The shell that supplies them cannot be executed from
	// here on every machine this suite runs on, so they are read.
	if !strings.Contains(joined, `version="${GITHUB_REF_NAME#v}"`) {
		t.Errorf("the readback does not take its version from the tag that triggered it:\n%s",
			joined)
	}
	const shipped = `' "${version}" 10 300 npm`
	if !strings.Contains(joined, "\n"+shipped) {
		t.Errorf("the readback is not run as %q: every 10 seconds, for up to 300, through the "+
			"registry client.\n%s", shipped, joined)
	}

	program, ok := inlineNodeProgram(script)
	if !ok {
		t.Fatalf("the readback carries no program this row can run:\n%s", joined)
	}

	for _, tc := range []struct {
		name      string
		tags      []string
		published bool
		pass      bool
		mention   []string
		unspoken  []string
	}{
		{name: "the registry agrees at once", tags: []string{`{"latest":"0.1.2"}`},
			published: true, pass: true, mention: []string{"0.1.2", "after the publish"}},
		{name: "stale, then settled", tags: []string{`{"latest":"0.1.0"}`, `{"latest":"0.1.0"}`, `{"latest":"0.1.2"}`},
			published: true, pass: true, mention: []string{"answers 0.1.0", "0.1.2"}},
		{name: "a failed read, then an answer", tags: []string{"fail", `{"latest":"0.1.2"}`},
			published: true, pass: true, mention: []string{"a failed read"}},
		{name: "the version is there and the pointer names another", tags: []string{`{"latest":"1.0.0"}`},
			published: true, pass: false, mention: []string{"1.0.0", "npm dist-tag add"}},
		{name: "the registry never finished", tags: []string{`{"latest":"0.1.0"}`},
			published: false, pass: false, mention: []string{"did not finish", "0.1.0"},
			unspoken: []string{"dist-tag add"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A WINDOW PER OUTCOME. Each read starts an interpreter, which
			// takes a noticeable fraction of a second on some runners, so a
			// case that must SETTLE gets a window it cannot outrun and
			// returns as soon as it settles; only the two that must run out
			// get a short one.
			window := "30"
			if !tc.pass {
				window = "0.3"
			}
			client := standInRegistry(t, tc.tags, tc.published)
			code, out := runNodeProgram(t, program, append([]string{"0.1.2", "0.02", window}, client...)...)
			if (code == 0) != tc.pass {
				t.Errorf("exit %d, want the readback to %s\n%s", code,
					map[bool]string{true: "pass", false: "fail"}[tc.pass], out)
			}
			for _, want := range tc.mention {
				if !strings.Contains(out, want) {
					t.Errorf("the readback does not say %q\n%s", want, out)
				}
			}
			for _, not := range tc.unspoken {
				if strings.Contains(out, not) {
					t.Errorf("the readback says %q to a registry that had not finished, which "+
						"sends the operator to move a pointer that was about to move itself\n%s", not, out)
				}
			}
		})
	}
}

// standInRegistry writes a stand-in for the registry client and returns
// the command that runs it. It answers the newest-pointer read from
// tags in order, repeating the last answer once they run out; "fail"
// makes that read fail. The single-version read answers 0.1.2 when
// published and fails as an unknown version otherwise.
//
// A SCRIPT RUN BY THE INTERPRETER, NOT A FILE ON THE PATH, for the reason
// the release script's harness gives: whether a file on the path is
// executable is a different answer on each of the three platforms this
// suite runs on. The readback takes its client as arguments, so the
// stand-in needs no path at all.
func standInRegistry(t *testing.T, tags []string, published bool) []string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("node is not on PATH, and this row RUNS the release's readback: %v", err)
	}
	dir := t.TempDir()
	state := filepath.Join(dir, "state.json")
	data, err := json.Marshal(map[string]any{"tags": tags, "published": published, "reads": 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, data, 0o644); err != nil {
		t.Fatal(err)
	}
	const stub = `const fs = require("node:fs");
const [, , state, ...args] = process.argv;
const s = JSON.parse(fs.readFileSync(state, "utf8"));
if (args.includes("dist-tags")) {
  const answer = s.tags[Math.min(s.reads, s.tags.length - 1)];
  s.reads += 1;
  fs.writeFileSync(state, JSON.stringify(s));
  if (answer === "fail") { console.error("npm error code ETIMEDOUT"); process.exit(1); }
  console.log(answer);
} else if (s.published) {
  console.log("0.1.2");
} else {
  console.error("npm error code E404"); process.exit(1);
}
`
	script := filepath.Join(dir, "registry.js")
	if err := os.WriteFile(script, []byte(stub), 0o644); err != nil {
		t.Fatal(err)
	}
	return []string{node, script, state}
}

// registryReaders are the files that ask the package registry anything:
// the release, which reads back what it published, and the release
// script, which prints the registry's state above the decision to tag.
var registryReaders = []string{releaseWorkflow, cutReleaseScript}

// TestEveryRegistryReadIsALiveRead requires the flag that makes the
// registry client ask the registry rather than its own cache, on every
// read in the files that read it.
//
// A CACHED ANSWER IS INDISTINGUISHABLE FROM A FRESH ONE AT THE CALL SITE,
// which is why the flag belongs in the command rather than in anybody's
// memory of when to add it. During one release the cache answered a
// missing package for one that existed, and an old newest version for one
// that had moved, and both were reported as the registry's state.
//
// A read is recognised in both spellings the files use: the shell's
// command line, and the argument list a program hands the client.
// Comment lines are prose and are not reads.
//
// MUTATION RUN, performed and observed: dropping the flag from one read
// reds this row, naming the file and line, and no other row.
func TestEveryRegistryReadIsALiveRead(t *testing.T) {
	root := moduleRoot(t)
	read := regexp.MustCompile(`npm view|\["view",`)
	scanned := 0
	for _, rel := range registryReaders {
		for i, line := range strings.Split(readRepoFile(t, root, rel), "\n") {
			text := strings.TrimSpace(line)
			if strings.HasPrefix(text, "#") || !read.MatchString(text) {
				continue
			}
			scanned++
			if !strings.Contains(text, "--prefer-online") {
				t.Errorf("%s:%d reads the registry without --prefer-online:\n  %s\n"+
					"Without it the client may answer from its cache, and that answer is "+
					"reported as the registry's state.", rel, i+1, text)
			}
		}
	}
	if scanned < 3 {
		t.Fatalf("found %d registry reads across %v, and the release makes at least three; "+
			"this row is not looking where the reads are", scanned, registryReaders)
	}
}

// wrapperPublishActions is the list this job's design claims, written
// out because the claim is the exact three and not the namespace.
//
// checkout, because the package files are in the tree; setup-node,
// because publishing needs a newer package manager than the wrapper's
// own floor; download-artifact, because the checksum table is built
// from a file the job before this one hands over inside the run, and
// never from the release, whose assets can be replaced between the two
// jobs by anyone with write access. Widening this list is an edit to
// this file, in the diff that wants it, which is the only way a reader
// ever sees the question asked. The third entry arrived that way.
var wrapperPublishActions = []string{"actions/checkout", "actions/download-artifact", "actions/setup-node"}

// TestThePublishJobRunsNobodyElsesCode states the property the split
// between the two publishing jobs exists to buy, and checks it.
//
// Under credential-free publishing the identity token IS the registry
// credential. The job that builds the release runs a full Go build and
// three actions from outside the actions organisation; this one runs
// three, all from inside it. That is the whole reason there are two
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
