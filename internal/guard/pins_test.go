package guard

// Guards over what a workflow FETCHES, as opposed to which actions it
// uses. The action references are pinned by the rows in TestWorkflows;
// these rows read the shell the steps run, and the version inputs of the
// jobs that hold a credential.
//
// ONE RULE: A WORKFLOW INSTALLS NOTHING AT A VERSION IT DID NOT NAME. An
// install at a range, a tag, or no version at all is resolved on the
// morning it runs, so two runs of one commit can build or publish with
// different tools and neither says so. The rule is the same for every
// package manager it knows. IT KNOWS ONLY THE ONES LISTED IN
// unpinnedInstall: an install through any other passes unread, so a
// workflow that starts using a new package manager extends that list in
// the same change. That is a limit stated, not a property claimed.
//
// A LOCKFILE INSTALL IS PINNED. `npm ci` installs exactly what the
// lockfile records, with an integrity hash per package, which is a
// stronger pin than a version number.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// exactVersion is a version that names one release: three numbers, an
// optional leading v, and an optional pre-release or build suffix. Not a
// range, not a tag, not a major on its own.
var exactVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)

// pipedToShell is a script fetched from the network and handed straight
// to an interpreter: the fetch cannot be pinned at all, because what runs
// is whatever the address serves that minute.
var pipedToShell = regexp.MustCompile(`\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba|z|da)?sh\b`)

// runLines returns each line of shell the steps of a workflow run, whether
// the script is written inline after the key or as a block beneath it.
func runLines(lines []yamlLine) []yamlLine {
	var out []yamlLine
	for i, l := range lines {
		text := strings.TrimPrefix(l.Text, "- ")
		if text != "run:" && !strings.HasPrefix(text, "run: ") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(text, "run:"))
		switch value {
		case "", "|", "|-", "|+", ">", ">-", ">+":
			out = append(out, blockAt(lines, i)...)
		default:
			out = append(out, yamlLine{N: l.N, Indent: l.Indent, Text: value})
		}
	}
	return out
}

// segments splits one line of shell at the operators that start a new
// command, so an install after `&&` is read as the install it is.
func segments(line string) []string {
	var out []string
	for _, a := range strings.Split(line, "&&") {
		for _, b := range strings.Split(a, "||") {
			for _, c := range strings.Split(b, ";") {
				for _, d := range strings.Split(c, "|") {
					if f := strings.TrimSpace(d); f != "" {
						out = append(out, f)
					}
				}
			}
		}
	}
	return out
}

// commandWords drops what comes before the program itself: sudo, and
// environment assignments.
func commandWords(segment string) []string {
	words := strings.Fields(segment)
	for len(words) > 0 && (words[0] == "sudo" || (strings.Contains(words[0], "=") && !strings.HasPrefix(words[0], "-"))) {
		words = words[1:]
	}
	return words
}

// operands are the words after the subcommand that are not flags.
func operands(words []string) []string {
	var out []string
	for _, w := range words {
		if !strings.HasPrefix(w, "-") {
			out = append(out, w)
		}
	}
	return out
}

// localPackage is an install from a path on this runner, which fetches
// nothing.
func localPackage(spec string) bool {
	return strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") ||
		strings.HasPrefix(spec, "/") || strings.HasSuffix(spec, ".tgz")
}

// npmSpecIsExact reports whether a package spec names one version:
// name@1.2.3, or @scope/name@1.2.3.
func npmSpecIsExact(spec string) bool {
	at := strings.LastIndex(spec, "@")
	if at <= 0 {
		return false
	}
	return exactVersion.MatchString(spec[at+1:])
}

// flagValue returns a long flag's value, spelled either --flag=value or
// --flag value.
func flagValue(words []string, flag string) (string, bool) {
	for i, w := range words {
		if v, ok := strings.CutPrefix(w, flag+"="); ok {
			return v, true
		}
		if w == flag && i+1 < len(words) {
			return words[i+1], true
		}
	}
	return "", false
}

// unpinnedInstall reports why a line of shell installs something at a
// version it did not name, or false when it does not.
func unpinnedInstall(line string) (string, bool) {
	if pipedToShell.MatchString(line) {
		return "a script fetched from the network is piped into a shell, which no version can pin", true
	}
	for _, seg := range segments(line) {
		words := commandWords(seg)
		if len(words) < 2 {
			continue
		}
		program, sub, rest := words[0], words[1], words[2:]
		switch {
		case program == "npm" && (sub == "install" || sub == "i" || sub == "add" || sub == "in"):
			pkgs := operands(rest)
			if len(pkgs) == 0 {
				return "`npm " + sub + "` with no package resolves the manifest's ranges; `npm ci` installs the lockfile", true
			}
			for _, p := range pkgs {
				if !localPackage(p) && !npmSpecIsExact(p) {
					return "npm installs " + p + ", which is not one exact version", true
				}
			}
		case program == "npx" || (program == "npm" && sub == "exec"):
			args := words[1:]
			if program == "npm" {
				args = rest
			}
			spec, ok := flagValue(args, "--package")
			if !ok {
				if ops := operands(args); len(ops) > 0 {
					spec = ops[0]
				}
			}
			if spec != "" && !localPackage(spec) && !npmSpecIsExact(spec) {
				return program + " runs " + spec + ", which is not one exact version", true
			}
		case program == "go" && sub == "install":
			for _, p := range operands(rest) {
				_, version, found := strings.Cut(p, "@")
				if !found || !exactVersion.MatchString(version) {
					return "go installs " + p + ", which is not one exact version", true
				}
			}
		case program == "choco" && sub == "install":
			if v, ok := flagValue(rest, "--version"); !ok || !exactVersion.MatchString(v) {
				return "choco installs without an exact --version", true
			}
		case (program == "pip" || program == "pip3") && sub == "install":
			for _, p := range operands(rest) {
				if !strings.Contains(p, "==") {
					return "pip installs " + p + " without an exact ==version", true
				}
			}
		case (program == "apt-get" || program == "apt") && sub == "install":
			for _, p := range operands(rest) {
				if !strings.Contains(p, "=") {
					return program + " installs " + p + " without an exact =version", true
				}
			}
		case program == "brew" && sub == "install":
			return "brew installs whatever its formula names today and takes no version", true
		case program == "gem" && sub == "install":
			if v, ok := flagValue(rest, "--version"); !ok || !exactVersion.MatchString(v) {
				return "gem installs without an exact --version", true
			}
		}
	}
	return "", false
}

// TestTheInstallRuleSeesBothDirections drives the rule against text
// written here, so a rule that went blind reds here rather than reading
// every real workflow as clean.
func TestTheInstallRuleSeesBothDirections(t *testing.T) {
	unpinned := []string{
		"npm install -g npm@^11.5.1",
		"npm install -g npm@latest",
		"npm i -g npm",
		"npm install",
		"npx some-tool --flag",
		"npx some-tool@2 --flag",
		"npm exec --package=some-tool@^1.0.0 -- some-tool",
		"choco install make -y --no-progress",
		"choco install make --version=4 -y",
		"go install example.com/tool@latest",
		"go install example.com/tool",
		"pip install some-tool",
		"sudo apt-get install -y some-tool",
		"brew install some-tool",
		"curl -fsSL https://example.com/install.sh | sh",
		"set -e && npm install -g npm@11",
		"FOO=1 npm install -g npm@~11.5.1",
	}
	for _, line := range unpinned {
		if _, bad := unpinnedInstall(line); !bad {
			t.Errorf("the rule passes %q, which installs at a version it did not name", line)
		}
	}
	pinned := []string{
		"npm install -g npm@11.20.0",
		"npm ci --ignore-scripts --no-audit --no-fund",
		"cd npm && npm ci --ignore-scripts",
		"npm install -g ./curiouspub-0.1.1.tgz",
		"npm install -g @scope/tool@1.2.3",
		"npx some-tool@1.2.3 --flag",
		"choco install make --version=4.4.1 -y --no-progress",
		"choco install make --version 4.4.1 -y",
		"go install example.com/tool@v1.2.3",
		"pip install some-tool==1.2.3",
		"sudo apt-get install -y some-tool=1.2.3-1",
		"npm publish --provenance --access public --tag latest",
		"npm view curiouspub dist-tags --json",
		"go run ./tools/skipcheck -- -count=1",
		"curl -fsS https://example.com/health",
	}
	for _, line := range pinned {
		if why, bad := unpinnedInstall(line); bad {
			t.Errorf("the rule refuses %q, which installs nothing unpinned: %s", line, why)
		}
	}
}

// TestEveryInstallAWorkflowRunsIsPinned applies the rule to every line of
// shell every workflow here runs.
func TestEveryInstallAWorkflowRunsIsPinned(t *testing.T) {
	root := moduleRoot(t)
	scanned := 0
	for _, path := range workflowFiles(t, root) {
		rel := relativeTo(t, root, path)
		for _, l := range runLines(readYAMLLines(readWorkflow(t, path))) {
			scanned++
			if why, bad := unpinnedInstall(l.Text); bad {
				t.Errorf("%s:%d: %s\n\t%s\n"+
					"A workflow here installs nothing at a version it did not name: a range is "+
					"resolved on the morning it runs, so two runs of one commit can use different "+
					"tools and neither says so.", rel, l.N, why, l.Text)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no line of shell was read from any workflow, so this row would pass over an empty set")
	}
}

// credentialed reports whether a job holds a credential it could publish
// or write with: an identity token, or write access to the repository.
func credentialed(body []yamlLine) bool {
	return hasEntry(body, "id-token", "write") || hasEntry(body, "contents", "write")
}

// versionInput reports whether a with: key chooses the version of a tool
// the job then runs.
func versionInput(key string) bool {
	return key == "version" || strings.HasSuffix(key, "-version") || strings.HasSuffix(key, "-release")
}

// TestACredentialedJobResolvesNothingFromARange reads the version inputs
// of every job that holds a credential. Each must name one release. A
// version read from a file is allowed only where the file itself names
// one: go.mod's toolchain line does, and a package manifest's engines
// range does not.
func TestACredentialedJobResolvesNothingFromARange(t *testing.T) {
	root := moduleRoot(t)
	checked := 0
	for _, path := range workflowFiles(t, root) {
		rel := relativeTo(t, root, path)
		for _, j := range jobs(readYAMLLines(readWorkflow(t, path))) {
			if !credentialed(j.Body) {
				continue
			}
			for _, l := range j.Body {
				key, value, found := strings.Cut(strings.TrimPrefix(l.Text, "- "), ":")
				if !found {
					continue
				}
				key = strings.TrimSpace(key)
				value = strings.Trim(strings.TrimSpace(value), `"'`)
				if i := strings.Index(value, " #"); i >= 0 {
					value = strings.TrimSpace(value[:i])
				}
				switch {
				case versionInput(key):
					checked++
					if !exactVersion.MatchString(value) {
						t.Errorf("%s:%d: the %q job, which holds a credential, sets %s: %q, which is not "+
							"one exact version\nA job that holds a credential resolves nothing from a "+
							"range.", rel, l.N, j.Name, key, value)
					}
				case strings.HasSuffix(key, "-version-file"):
					checked++
					if value != "go.mod" || !goModNamesAToolchain(t, root) {
						t.Errorf("%s:%d: the %q job, which holds a credential, reads its version from %q, "+
							"which does not name one exact release\nOnly go.mod's toolchain line does.",
							rel, l.N, j.Name, value)
					}
				case key == "check-latest" && value == "true":
					t.Errorf("%s:%d: the %q job, which holds a credential, asks for the latest tool",
						rel, l.N, j.Name)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no version input of any credentialed job was read, so this row would pass over an empty set")
	}
}

// readWorkflow reads one workflow file by the absolute path
// workflowFiles returns.
func readWorkflow(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// relativeTo names a file the way a reader of this repository would.
func relativeTo(t *testing.T, root, path string) string {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("%s is not under %s: %v", path, root, err)
	}
	return filepath.ToSlash(rel)
}

// goModNamesAToolchain reports whether go.mod pins its toolchain to one
// exact release.
func goModNamesAToolchain(t *testing.T, root string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "toolchain go"); ok {
			return exactVersion.MatchString(v)
		}
	}
	return false
}

// TestTheWrapperJobHasNoNetworkReadOfTheRelease pins where the package's
// checksum table comes from. The installer verifies every download
// against that table, so its source must be this run and never the
// release: a release asset can be replaced by anyone with write access
// between the job that built it and the one that publishes the package.
func TestTheWrapperJobHasNoNetworkReadOfTheRelease(t *testing.T) {
	root := moduleRoot(t)
	lines := readYAMLLines(readRepoFile(t, root, releaseWorkflow))
	wrapper := wrapperJob(t, lines)

	for _, l := range wrapper.Body {
		for _, read := range []string{"gh release", "gh api", "releases/download", "curl ", "wget "} {
			if strings.Contains(l.Text, read) {
				t.Errorf("%s:%d: the %q job reads from the network (%q)\n"+
					"The checksum table comes from this run's artefact, and this job makes no "+
					"network read of the release at all.", releaseWorkflow, l.N, wrapperJobName, read)
			}
		}
	}

	var publishJob job
	for _, j := range jobs(lines) {
		if j.Name == "publish" {
			publishJob = j
		}
	}
	upload := stepUsing(publishJob.Body, "actions/upload-artifact@")
	if upload == nil {
		t.Fatalf("the publish job hands no artefact to the %q job", wrapperJobName)
	}
	download := stepUsing(wrapper.Body, "actions/download-artifact@")
	if download == nil {
		t.Fatalf("the %q job takes no artefact from the publish job", wrapperJobName)
	}
	up, _ := valueOf(upload, "name")
	down, _ := valueOf(download, "name")
	if up == "" || up != down {
		t.Errorf("the publish job uploads the artefact %q and the %q job downloads %q", up, wrapperJobName, down)
	}
	if path, _ := valueOf(upload, "path"); path != "dist/checksums.txt" {
		t.Errorf("the publish job uploads %q, not the checksum file the release tool writes", path)
	}
	if dir, _ := valueOf(download, "path"); dir != "dist" {
		t.Errorf("the %q job downloads the checksums into %q, and builds its table from dist/checksums.txt",
			wrapperJobName, dir)
	}
}

// stepUsing returns the step whose action reference starts with prefix.
func stepUsing(body []yamlLine, prefix string) []yamlLine {
	for _, step := range sequenceEntries(body) {
		if v, ok := valueOf(step, "uses"); ok && strings.HasPrefix(v, prefix) {
			return step
		}
	}
	return nil
}
