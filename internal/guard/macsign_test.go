package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// The macOS signing rows. The binaries are signed with a developer
// certificate and notarised, because an unsigned one is killed by the
// operating system on any Mac that has not exempted the terminal running
// it. The material that does that is the only long-lived signing
// credential this repository's workflows can reach, so where it may be
// read, and what happens when it is missing, are both held here.

// macSigningSecrets are the five values the signing block reads. The
// guard's allowlist names each of them; these rows say where.
var macSigningSecrets = []string{
	"MACOS_SIGN_P12",
	"MACOS_SIGN_PASSWORD",
	"MACOS_NOTARY_KEY",
	"MACOS_NOTARY_KEY_ID",
	"MACOS_NOTARY_ISSUER_ID",
}

// macSigningJob is the one job that may read them. It is the job that
// publishes, because the release tool signs between building and
// archiving inside one run, and there is no way to hand a signed binary
// from one job to another without handing over everything else too.
const macSigningJob = "publish"

// preflightStep names the step that refuses to build without all five.
const preflightStep = "the macOS signing material is all present"

// releaseToolStep is how the release tool's own step is recognised.
const releaseToolStep = "goreleaser/goreleaser-action@"

// jobAt returns the name of the job in the release workflow that holds
// line n, or "" if no job does.
func jobAt(all []job, n int) string {
	for _, j := range all {
		if len(j.Body) == 0 {
			continue
		}
		if n >= j.N && n <= j.Body[len(j.Body)-1].N {
			return j.Name
		}
	}
	return ""
}

// TestTheMacOSSigningSecretsAreReadByThePublishJobAlone holds the five
// names to one job in one workflow. The allowlist says a name may be
// referenced; this says where.
//
// THE WORKFLOW THAT RUNS ON PULL REQUESTS MUST NEVER NAME THEM, and
// neither may the snapshot build or any job of the release that is not
// the one that signs. A credential named in a job that does not use it is
// a credential every action in that job can read.
func TestTheMacOSSigningSecretsAreReadByThePublishJobAlone(t *testing.T) {
	root := moduleRoot(t)
	wanted := map[string]bool{}
	for _, name := range macSigningSecrets {
		wanted[name] = true
		if _, ok := allowedSecrets[name]; !ok {
			t.Errorf("%s is not on the allowlist, so the release could not read it", name)
		}
	}

	release := jobs(readYAMLLines(readRepoFile(t, root, releaseWorkflow)))
	seen := map[string]int{}
	for _, path := range githubFiles(t, root) {
		rel := filepath.ToSlash(mustRel(t, root, path))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, m := range secretReference.FindAllStringSubmatch(line, -1) {
				name := m[1]
				if name == "" {
					name = m[2]
				}
				if !wanted[name] {
					continue
				}
				seen[name]++
				if rel != releaseWorkflow {
					t.Errorf("%s:%d reads %s. Only the release workflow's %q job may, and a "+
						"workflow that runs on other events must never name it.",
						rel, i+1, name, macSigningJob)
					continue
				}
				if got := jobAt(release, i+1); got != macSigningJob {
					t.Errorf("%s:%d reads %s in the %q job. Only the %q job signs, so only it "+
						"may name the material; anywhere else, every action in that job can read it.",
						rel, i+1, name, got, macSigningJob)
				}
			}
		}
	}
	for _, name := range macSigningSecrets {
		if seen[name] == 0 {
			t.Errorf("nothing reads %s, so the release cannot sign with it", name)
		}
	}
}

// TestAReleaseWithoutTheSigningMaterialPublishesNothing holds the rule
// that signing is not optional on a real release.
//
// WHY A STEP, AND NOT THE TOOL'S OWN SWITCH. A secret that does not exist
// reaches a workflow as an EMPTY STRING. The release tool then signs
// without notarising whenever any notary value is empty, and says so at
// info level; with its documented switch ("is the certificate variable
// set") it skips signing altogether. Either way the release is green and
// the binaries are refused on every Mac. So the switch is a constant, and
// a step before the tool refuses to go on without all five.
//
// The row reads the names three ways — the configuration's templates, the
// tool step's environment, and the refusing step — and requires all three
// to be the same five. Then it RUNS the refusing step's own script, once
// with each name empty, because a script that names all five and checks
// none of them would pass every static reading.
func TestAReleaseWithoutTheSigningMaterialPublishesNothing(t *testing.T) {
	root := moduleRoot(t)

	config := readRepoFile(t, root, releaseConfig)
	notarize, ok := topLevelBlock(readYAMLLines(config), "notarize")
	if !ok {
		t.Fatalf("%s has no notarize block, so the macOS binaries ship unsigned", releaseConfig)
	}
	enabled, _ := valueOf(notarize, "enabled")
	if enabled != "true" {
		t.Errorf("%s's signing block is enabled by %q, want the constant true.\n"+
			"A condition here is a way for a release to skip signing and stay green; the "+
			"release workflow refuses a missing secret before the tool runs instead.",
			releaseConfig, enabled)
	}
	fromConfig := uniqueSorted(regexp.MustCompile(`\.Env\.(MACOS_[A-Z0-9_]+)`).FindAllStringSubmatch(strings.Join(textsOf(notarize), "\n"), -1))
	want := append([]string(nil), macSigningSecrets...)
	sort.Strings(want)
	if strings.Join(fromConfig, " ") != strings.Join(want, " ") {
		t.Errorf("the signing block reads %v from the environment, want %v", fromConfig, want)
	}

	var publish job
	for _, j := range jobs(readYAMLLines(readRepoFile(t, root, releaseWorkflow))) {
		if j.Name == macSigningJob {
			publish = j
		}
	}
	steps := sequenceEntries(publish.Body)
	pre, tool := -1, -1
	for i, step := range steps {
		if v, _ := valueOf(step, "name"); v == preflightStep {
			pre = i
		}
		if v, _ := valueOf(step, "uses"); strings.HasPrefix(v, releaseToolStep) {
			tool = i
		}
	}
	if pre < 0 {
		t.Fatalf("the %q job has no step %q, so a release that lost a secret signs "+
			"without notarising, or not at all, and reports green", macSigningJob, preflightStep)
	}
	if tool < 0 {
		t.Fatalf("the %q job does not run the release tool", macSigningJob)
	}
	if pre > tool {
		t.Errorf("%q runs after the release tool. It has to run first: by the time the tool "+
			"finishes, the release is public.", preflightStep)
	}
	for _, at := range []int{pre, tool} {
		for _, name := range macSigningSecrets {
			if _, ok := lineWith(steps[at], name+": ${{ secrets."+name+" }}"); !ok {
				t.Errorf("step %d of the %q job does not pass %s from the secret of that name",
					at+1, macSigningJob, name)
			}
		}
	}

	script, ok := runBlock(steps[pre], "missing=")
	if !ok {
		t.Fatalf("%q has no script this row can read", preflightStep)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		// Undeclared for the reason the release script's rows give: every
		// leg this repository's own workflow runs has this interpreter.
		t.Skip("no interpreter to run the step with")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "preflight.sh")
	if err := os.WriteFile(file, []byte(strings.Join(textsOf(script), "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(empty string) (string, error) {
		envFile := filepath.Join(dir, "env-"+empty)
		cmd := exec.Command(bash, file)
		cmd.Env = append(os.Environ(), "GITHUB_ENV="+envFile)
		for _, name := range macSigningSecrets {
			value := "set"
			if name == empty {
				value = ""
			}
			cmd.Env = append(cmd.Env, name+"="+value)
		}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for _, name := range macSigningSecrets {
		out, err := run(name)
		if err == nil {
			t.Errorf("with %s empty, %q passed. A release without it would have gone on "+
				"to build and publish.\n%s", name, preflightStep, out)
		} else if !strings.Contains(out, name) {
			t.Errorf("with %s empty, %q failed without naming it:\n%s", name, preflightStep, out)
		}
	}
	if out, err := run(""); err != nil {
		t.Errorf("with all five present, %q failed: %v\n%s", preflightStep, err, out)
	}
}

func uniqueSorted(matches [][]string) []string {
	set := map[string]bool{}
	for _, m := range matches {
		set[m[1]] = true
	}
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// macCheckJob is the release job that asks macOS itself.
const macCheckJob = "mac-check"

// gatekeeperScript is the assessment, kept in a file so that anyone
// holding a downloaded archive can run the same check the release runs.
const gatekeeperScript = "scripts/gatekeeper-check.sh"

// TestTheReleaseAsksMacOSWhetherItWouldRunTheBinary holds the release's
// macOS check to its shape: it follows the publish job, runs on a Mac,
// holds no credential, and asks the assessment a download gets.
//
// THE ASSESSMENT TYPE IS THE POINT. The default one expects an application
// bundle and rejects every command-line tool, notarised or not, so a check
// written with it would red on a correct release and teach everyone to
// ignore it. And "accepted" alone does not say notarised: the source line
// does.
func TestTheReleaseAsksMacOSWhetherItWouldRunTheBinary(t *testing.T) {
	root := moduleRoot(t)
	var check job
	found := false
	for _, j := range jobs(readYAMLLines(readRepoFile(t, root, releaseWorkflow))) {
		if j.Name == macCheckJob {
			check, found = j, true
		}
	}
	if !found {
		t.Fatalf("%s has no %q job, so nothing asks a Mac whether it would run what the "+
			"release published", releaseWorkflow, macCheckJob)
	}
	depth := -1
	for _, l := range check.Body {
		if depth < 0 || l.Indent < depth {
			depth = l.Indent
		}
	}
	own := map[string]string{}
	for _, l := range check.Body {
		if l.Indent == depth {
			key, value, _ := strings.Cut(l.Text, ":")
			own[key] = strings.TrimSpace(value)
		}
	}
	if got := own["needs"]; got != "publish" {
		t.Errorf("the %q job needs %q; it reads what the publish job released, and nothing "+
			"else has to finish first", macCheckJob, got)
	}
	if got := own["runs-on"]; !strings.HasPrefix(got, "macos-") {
		t.Errorf("the %q job runs on %q. Only macOS can say whether macOS would run it.", macCheckJob, got)
	}
	if _, reviewed := own["environment"]; reviewed {
		t.Errorf("the %q job runs in a deployment environment; it publishes nothing and "+
			"must hold no credential", macCheckJob)
	}
	for _, l := range check.Body {
		if secretReference.MatchString(l.Text) {
			t.Errorf("%s:%d: the %q job names a secret. It reads public archives and needs none.",
				releaseWorkflow, l.N, macCheckJob)
		}
	}
	script, ok := runBlock(check.Body, gatekeeperScript)
	if !ok {
		t.Fatalf("the %q job does not run %s", macCheckJob, gatekeeperScript)
	}
	joined := strings.Join(textsOf(script), "\n")
	for _, want := range []string{
		`want="${GITHUB_REF_NAME#v}"`,
		"shasum -a 256 -c",
		gatekeeperScript + " dl/amd64/curious dl/arm64/curious",
		"com.apple.quarantine",
		"dl/arm64/curious version",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the %q job's script does not carry %q:\n%s", macCheckJob, want, joined)
		}
	}

	body := readRepoFile(t, root, gatekeeperScript)
	for _, want := range []string{"spctl -a -vv -t install", "grep -qx 'source=Notarized Developer ID'"} {
		if !strings.Contains(body, want) {
			t.Errorf("%s does not carry %q. Without the install assessment every tool is "+
				"rejected; without the source line a merely signed binary passes.", gatekeeperScript, want)
		}
	}
}

// TestTheGatekeeperCheckRefusesABinaryThatIsNotNotarised runs the check on
// a Mac, against a binary built here. The linker signs it ad hoc, which is
// exactly what the two releases before signing shipped, so the check must
// refuse it. A check that passed it would pass a release that skipped
// notarisation.
//
// The positive half — a notarised binary accepted — cannot be built here,
// because notarisation needs the release's credentials. The release's own
// macOS job is that half, on every release.
func TestTheGatekeeperCheckRefusesABinaryThatIsNotNotarised(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("the assessment is macOS's own, and there is none to ask for here")
	}
	root := moduleRoot(t)
	bin := filepath.Join(t.TempDir(), "curious")
	build := exec.Command("go", "build", "-trimpath", "-o", bin, "./cmd/curious")
	build.Dir = root
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the binary: %v\n%s", err, out)
	}
	cmd := exec.Command(filepath.Join(root, gatekeeperScript), bin)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("%s accepted an ad-hoc-signed binary:\n%s", gatekeeperScript, out)
	}
	if !strings.Contains(string(out), "would be refused") {
		t.Errorf("%s failed without saying why:\n%s", gatekeeperScript, out)
	}
}
