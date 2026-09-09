package guard

// Guards over what this repository SHIPS TO PEOPLE, as opposed to what
// the binary does once they have it.
//
// Everything else in internal/guard defends a property of the source. The
// rows here defend the release pipeline: which third-party code runs in a
// job that can publish, which credentials that job may name, what the
// build actually builds, and what a human has to do before any of it
// fires. The adversary is not a user with an odd project; it is somebody
// who wants their code on other people's machines, and a release pipeline
// is the shortest path there.
//
// THE UNIVERSE, named out loud because a traversal's scope is the
// assumption most likely to be inherited rather than chosen. These rows
// read exactly two things:
//
//   - every published text file under .github/ — workflows, the
//     dependency-update policy, and any composite action that lands there
//     later. "Published" is taken from git rather than from the disk, for
//     the reason publishedTextFiles already gives.
//   - the release configuration at the module root.
//
// They do NOT read: the module's Go sources (other guards own those), the
// scripts an operator runs by hand beyond the two properties asserted
// below, or anything a workflow reaches over the network. A workflow that
// curls a script and pipes it to a shell is invisible here and is not
// invisible to a reader — which is the honest statement of this guard's
// field of view rather than a claim that it has none.
//
// GRANULARITY. The workflow rows read YAML as indented lines rather than
// through a parser, because a parser is a dependency this module does not
// carry and would not otherwise want. The reader below is deliberately a
// small named thing with its own reference row, so its limits are
// checkable rather than assumed: it understands block mappings, block
// sequences and flow sequences, and it does not understand anchors,
// merge keys, multi-line scalars or a key written in flow style. Every
// file it reads is authored in this repository, so those are limits and
// not holes — but a workflow arriving from elsewhere would need this
// stated before it was trusted.

import (
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

// ---------------------------------------------------------------------
// The indented-line reader, and the two block extractors built on it.
// ---------------------------------------------------------------------

// yamlLine is one significant physical line: its number, how deeply it is
// indented, and its text with leading space removed. Blank lines and
// whole-line comments are dropped, because neither can carry structure.
type yamlLine struct {
	N      int
	Indent int
	Text   string
}

func readYAMLLines(data string) []yamlLine {
	var out []yamlLine
	for i, raw := range strings.Split(data, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimLeft(line, " ")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		out = append(out, yamlLine{N: i + 1, Indent: len(line) - len(trimmed), Text: trimmed})
	}
	return out
}

// blockAt returns the lines belonging to the entry at index i: every
// following line indented more deeply than it, stopping at the first line
// that is not.
func blockAt(lines []yamlLine, i int) []yamlLine {
	if i < 0 || i >= len(lines) {
		return nil
	}
	depth := lines[i].Indent
	var out []yamlLine
	for _, l := range lines[i+1:] {
		if l.Indent <= depth {
			break
		}
		out = append(out, l)
	}
	return out
}

// topLevelBlock finds a key at indent zero and returns its body. The
// second result distinguishes "the key is absent" from "the key is
// present and its body is empty", which are different findings: a
// workflow with no permissions block inherits whatever the repository
// default is, and a workflow with an empty one is a different mistake.
func topLevelBlock(lines []yamlLine, key string) ([]yamlLine, bool) {
	for i, l := range lines {
		if l.Indent != 0 {
			continue
		}
		if l.Text == key+":" || strings.HasPrefix(l.Text, key+": ") {
			return blockAt(lines, i), true
		}
	}
	return nil, false
}

// job is one entry under the top-level jobs mapping.
type job struct {
	Name string
	N    int
	Body []yamlLine
}

func jobs(lines []yamlLine) []job {
	body, ok := topLevelBlock(lines, "jobs")
	if !ok {
		return nil
	}
	// Job names sit at the shallowest indent inside the block; anything
	// deeper belongs to a job rather than naming one.
	depth := -1
	for _, l := range body {
		if depth < 0 || l.Indent < depth {
			depth = l.Indent
		}
	}
	var out []job
	for i, l := range body {
		if l.Indent != depth || !strings.HasSuffix(l.Text, ":") {
			continue
		}
		out = append(out, job{
			Name: strings.TrimSuffix(l.Text, ":"),
			N:    l.N,
			Body: blockAt(body, i),
		})
	}
	return out
}

// listValues reads the value of the entry at index i as a sequence,
// accepting both spellings this repository might use: a flow sequence on
// the same line, and a block sequence on the lines below it.
func listValues(lines []yamlLine, i int) []string {
	text := lines[i].Text
	if open := strings.Index(text, "["); open >= 0 {
		if closeAt := strings.LastIndex(text, "]"); closeAt > open {
			var out []string
			for _, item := range strings.Split(text[open+1:closeAt], ",") {
				item = strings.Trim(strings.TrimSpace(item), `"'`)
				if item != "" {
					out = append(out, item)
				}
			}
			return out
		}
	}
	var out []string
	for _, l := range blockAt(lines, i) {
		if !strings.HasPrefix(l.Text, "- ") {
			continue
		}
		out = append(out, strings.Trim(strings.TrimSpace(strings.TrimPrefix(l.Text, "- ")), `"'`))
	}
	return out
}

// findKey returns the index of the first line whose key is key, at any
// depth, or -1.
func findKey(lines []yamlLine, key string) int {
	for i, l := range lines {
		if l.Text == key+":" || strings.HasPrefix(l.Text, key+": ") ||
			l.Text == "- "+key+":" || strings.HasPrefix(l.Text, "- "+key+": ") {
			return i
		}
	}
	return -1
}

// valueOf returns the scalar value of the first entry named key, and
// whether there was one.
//
// SEPARATED FROM A SUBSTRING SEARCH ON PURPOSE, and the reason was
// measured rather than anticipated. Several rows here first asked
// whether a file CONTAINED a setting, and a containment test is
// satisfied by any longer string that has it inside — so a mutation
// renaming the signing tool to something with that name inside it left
// the row green, and a deployment environment named for the reviewed one
// but without its reviewer would have passed the same way. Asking for
// the VALUE is the same amount of code and cannot be satisfied by a
// near-miss.
func valueOf(lines []yamlLine, key string) (string, bool) {
	i := findKey(lines, key)
	if i < 0 {
		return "", false
	}
	text := strings.TrimPrefix(lines[i].Text, "- ")
	_, after, found := strings.Cut(text, ":")
	if !found {
		return "", false
	}
	return strings.Trim(strings.TrimSpace(after), `"'`), true
}

// sequenceEntries splits a block into the entries of a YAML sequence:
// each line introduced by a dash at the shallowest indent, plus
// everything indented under it.
//
// It exists because a row about ONE member of a list cannot be written
// against the whole list. Asking whether a block CONTAINS a setting is
// satisfied by any sibling that happens to carry it, which is how a row
// keeps its name while it stops pinning what the name says.
func sequenceEntries(lines []yamlLine) [][]yamlLine {
	depth := -1
	for _, l := range lines {
		if strings.HasPrefix(l.Text, "- ") && (depth < 0 || l.Indent < depth) {
			depth = l.Indent
		}
	}
	var out [][]yamlLine
	for i, l := range lines {
		if l.Indent != depth || !strings.HasPrefix(l.Text, "- ") {
			continue
		}
		out = append(out, append([]yamlLine{l}, blockAt(lines, i)...))
	}
	return out
}

// listOf reads a named sequence, or nothing when the key is absent.
//
// SEPARATE FROM listValues BECAUSE OF WHAT ABSENCE DOES THERE: an index
// of -1 indexes a slice out of range and the guard panics. A panicking
// guard still fails the build, and it fails it with a stack trace
// instead of the sentence explaining what is missing — which is the
// difference between a row that reports and a row that merely stops.
// Found by running the mutation that removes the key.
func listOf(lines []yamlLine, key string) []string {
	at := findKey(lines, key)
	if at < 0 {
		return nil
	}
	return listValues(lines, at)
}

// entryWithID finds the sequence entry whose id is the one named.
func entryWithID(lines []yamlLine, id string) ([]yamlLine, bool) {
	for _, entry := range sequenceEntries(lines) {
		if got, ok := valueOf(entry, "id"); ok && got == id {
			return entry, true
		}
	}
	return nil, false
}

// contains is the one-line membership test these rows need. The module
// targets a toolchain that has this in the standard library; it is
// written out here to keep the guard's import list to what it reads
// rather than what it computes.
func contains(haystack []string, needle string) bool {
	for _, got := range haystack {
		if got == needle {
			return true
		}
	}
	return false
}

// hasEntry reports whether any line is exactly the given key and value.
func hasEntry(lines []yamlLine, key, value string) bool {
	for _, l := range lines {
		if k, after, found := strings.Cut(strings.TrimPrefix(l.Text, "- "), ":"); found &&
			strings.TrimSpace(k) == key && strings.Trim(strings.TrimSpace(after), `"'`) == value {
			return true
		}
	}
	return false
}

// TestWorkflowBlockReaderSeesTheStructureItClaims drives the reader above
// against a fixture written here, so its limits are demonstrated rather
// than described. Without this row the reader is enforcement machinery
// with nothing reporting on it, and every row below inherits whatever it
// gets wrong — silently, because a reader that finds nothing makes an
// assertion over an empty set.
func TestWorkflowBlockReaderSeesTheStructureItClaims(t *testing.T) {
	const fixture = `name: Fixture

# a comment line carries no structure
on:
  push:
    tags: ['v*']

permissions:
  contents: read

jobs:
  first:
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: someone/something@0123456789abcdef0123456789abcdef01234567 # v1.2.3
  second:
    goos: [darwin, linux]
    goarch:
      - amd64
      - arm64
`
	lines := readYAMLLines(fixture)

	if _, ok := topLevelBlock(lines, "nothing-like-this"); ok {
		t.Error("topLevelBlock reported a key the fixture does not contain")
	}

	perms, ok := topLevelBlock(lines, "permissions")
	if !ok {
		t.Fatal("the top-level permissions block was not found")
	}
	if len(perms) != 1 || perms[0].Text != "contents: read" {
		t.Errorf("top-level permissions block = %v, want exactly one entry reading contents: read", perms)
	}

	found := jobs(lines)
	if len(found) != 2 {
		t.Fatalf("found %d jobs, want 2: %v", len(found), found)
	}
	if found[0].Name != "first" || found[1].Name != "second" {
		t.Errorf("job names = %q and %q, want first and second", found[0].Name, found[1].Name)
	}
	// The first job's body must include its own nested permissions entry:
	// a reader that stopped at the first deeper line would miss it, and
	// every privilege row below depends on seeing it.
	if idx := findKey(found[0].Body, "permissions"); idx < 0 {
		t.Error("the first job's body does not contain its own permissions entry")
	} else if got := blockAt(found[0].Body, idx); len(got) != 1 || got[0].Text != "contents: write" {
		t.Errorf("first job's permissions body = %v, want one entry reading contents: write", got)
	}
	if strings.Contains(strings.Join(textsOf(found[0].Body), "\n"), "goos") {
		t.Error("the first job's body swallowed lines belonging to the second job")
	}

	// Both sequence spellings, because the release configuration uses one
	// and a later edit may use the other.
	flow := findKey(found[1].Body, "goos")
	block := findKey(found[1].Body, "goarch")
	if flow < 0 || block < 0 {
		t.Fatalf("goos/goarch not found in the second job: %v", found[1].Body)
	}
	if got := listValues(found[1].Body, flow); len(got) != 2 || got[0] != "darwin" || got[1] != "linux" {
		t.Errorf("flow sequence read as %v, want [darwin linux]", got)
	}
	if got := listValues(found[1].Body, block); len(got) != 2 || got[0] != "amd64" || got[1] != "arm64" {
		t.Errorf("block sequence read as %v, want [amd64 arm64]", got)
	}
}

func textsOf(lines []yamlLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

// ---------------------------------------------------------------------
// What the workflows may reference.
// ---------------------------------------------------------------------

// allowedSecrets is this guard's own stated allowance, and it is an
// ALLOWLIST rather than a list of forbidden provider credentials on
// purpose. A denylist can only refuse the providers somebody thought of;
// this refuses every name nobody justified, which includes all of them.
//
// It is stated here rather than read from a file so that widening it is
// an edit to the guard, reviewable in the diff that needs it. A guard
// whose allowance can be widened by editing the thing that trips it is
// not a guard.
var allowedSecrets = map[string]string{
	"GITHUB_TOKEN":       "minted per job by the runner, scoped by the job's own permissions block, never a repository secret",
	"HOMEBREW_TAP_TOKEN": "write access to the tap repository and nothing else; the job token cannot reach another repository",
	// ~~NPM_TOKEN~~ REMOVED 2026-09-08. The wrapper publishes through
	// trusted publishing: the job proves its identity with the token the
	// runner mints for it and holds no registry credential at all, so
	// there is nothing here to allow. It was listed before the publish
	// path was designed, against a token that was never created.
	//
	// An allowlist entry for a secret that does not exist is not inert.
	// It is a standing permission nobody has to ask for again, and the
	// next reader finds a guard naming a credential and a brief saying
	// there is none, and has to guess which is stale. Removed so that
	// reintroducing a token means editing this file, in the diff that
	// wants it — which is what the comment above says this list is for.
}

// requiredEnvironment is the deployment environment whose reviewer is the
// brake on an outward publish. A tag push fires the whole irreversible
// mutation at once and no script can stand in front of that, because
// pushing a tag is not a script.
const requiredEnvironment = "release"

// releaseWorkflow and snapshotWorkflow are named because the rows below
// ask different questions of each: one publishes and one proves the
// pipeline without publishing anything.
const (
	releaseWorkflow  = ".github/workflows/release.yml"
	snapshotWorkflow = ".github/workflows/snapshot.yml"
	dependabotPolicy = ".github/dependabot.yml"
)

// pinnedSHA is a full 40-character commit identifier and nothing else. A
// tag is mutable: whoever can move one runs their code inside a job that
// holds the publish credentials.
var pinnedSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// versionComment is the human-readable version a pinned reference must
// carry, so a reader can tell what the identifier is without resolving
// it. A pin nobody can read is a pin nobody updates.
var versionComment = regexp.MustCompile(`^v[0-9]+(\.[0-9]+)*`)

// secretReference matches both spellings a workflow can use to name a
// secret.
var secretReference = regexp.MustCompile(`secrets\.([A-Za-z_][A-Za-z0-9_]*)|secrets\[\s*['"]([^'"]+)['"]\s*\]`)

// usesEntry matches a step's action reference. The key can be introduced
// by a sequence dash, so the prefix is a boundary rather than a line
// start.
var usesEntry = regexp.MustCompile(`(?:^|[\s-])uses:\s*(.*)$`)

// reference is one action a workflow runs.
type reference struct {
	File    string
	Line    int
	Action  string
	Ref     string
	Comment string
}

// loadProviderAuthActions reads the committed list of action paths that
// authenticate to an infrastructure provider.
//
// It is a FILE for the same two reasons the dependency denylist is one.
// The guard reads it on every run and keeps no copy, so deleting a line
// measurably changes what the guard can see — which is how anyone can
// check it is really being read. And it has to SPELL the names it
// forbids, which the vendor check would otherwise red; a rule file's data
// lines are exempt from that check and only from that check.
//
// IT COVERS A CLASS, NOT A VENDOR, and that is not thoroughness for its
// own sake. This file is public. A lint asserting the absence of one
// named provider's action tells every reader which provider the project
// uses, which is the fact the rule exists to keep out — and it is a real
// widening besides, because a rule naming one provider passes a workflow
// that authenticates to any other.
func loadProviderAuthActions(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "scripts", "provider-auth-actions.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var fragments []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// The same strictness the dependency denylist learned: neither
		// whitespace nor a hash is legal inside an action path, so either
		// one means somebody wrote a trailing comment — which does not
		// terminate the line here, it becomes part of the fragment. The
		// fragment then matches nothing and the rule is silently off with
		// the guard still green.
		if strings.ContainsAny(line, " \t#") {
			t.Fatalf("scripts/provider-auth-actions.txt: %q is not a bare action-path fragment "+
				"(whitespace or # present). A trailing comment silently disables the entry it "+
				"is attached to; put the comment on its own line.", line)
		}
		fragments = append(fragments, strings.ToLower(line))
	}
	if len(fragments) == 0 {
		t.Fatal("scripts/provider-auth-actions.txt lists no fragments — this guard would silently pass")
	}
	return fragments
}

// TestProviderActionListIsTreatedAsARuleFile pins the one existing guard
// this change widened.
//
// The provider list has to SPELL the names it forbids, and the vendor
// vocabulary check forbids those same names in authored text — two
// correct rules pointing opposite ways at one string. The resolution is
// the exemption that already existed for the dependency denylist, with
// one more member. An exemption is the only kind of change that makes a
// guard QUIETER, so both directions are asserted: the file is in the set,
// and a file that merely sits beside it is not.
func TestProviderActionListIsTreatedAsARuleFile(t *testing.T) {
	set := ruleFileNames()
	if !set["scripts/provider-auth-actions.txt"] {
		t.Error("the provider list is not a rule file, so the vendor check reds on the " +
			"entries it has to spell in order to match them — and the fastest way out of " +
			"that red is deleting one of the two rules")
	}
	// THE FLOOR. The waiver is for files whose job is to name what is
	// forbidden, and nothing else may inherit it by proximity.
	for _, notARule := range []string{
		".goreleaser.yaml",
		".github/workflows/release.yml",
		"scripts/cut-release.sh",
		"scripts/expected-skips.txt",
	} {
		if set[notARule] {
			t.Errorf("%s is exempt from the vendor check and has no reason to be", notARule)
		}
	}
	// And the waiver is still per-line and per-check where it does apply.
	if text, ok := vendorScanLine("scripts/provider-auth-actions.txt", "some-owner/some-action", true); ok {
		t.Errorf("a data line in the provider list is scanned by the vendor check (as %q)", text)
	}
	if _, ok := vendorScanLine("scripts/provider-auth-actions.txt", "# some-owner/some-action", true); !ok {
		t.Error("a comment line in the provider list is not scanned; the prose explaining a " +
			"rule has no need to name what the rule forbids")
	}
}

// githubFiles returns every published text file under .github/.
func githubFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, p := range publishedTextFiles(t, root) {
		rel := filepath.ToSlash(mustRel(t, root, p))
		if rel == ".github" || strings.HasPrefix(rel, ".github/") {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		t.Fatal("no published files under .github/ — every row below would pass over an empty set")
	}
	return out
}

// readRepoFile reads a repository-relative path, failing rather than
// skipping if it is absent: every file these rows name is one this
// repository is supposed to have, and a missing one is the finding.
func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

// collectReferences gathers every action reference under .github/.
func collectReferences(t *testing.T, root string) []reference {
	t.Helper()
	var refs []reference
	for _, path := range githubFiles(t, root) {
		rel := filepath.ToSlash(mustRel(t, root, path))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		for i, raw := range strings.Split(string(data), "\n") {
			line := strings.TrimRight(raw, "\r")
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			m := usesEntry.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			rest := strings.TrimSpace(m[1])
			value, comment := rest, ""
			if hash := strings.Index(rest, "#"); hash >= 0 {
				value = strings.TrimSpace(rest[:hash])
				comment = strings.TrimSpace(strings.TrimLeft(rest[hash:], "# "))
			}
			value = strings.Trim(value, `"'`)
			ref := reference{File: rel, Line: i + 1, Action: value, Comment: comment}
			if at := strings.LastIndex(value, "@"); at >= 0 {
				ref.Action, ref.Ref = value[:at], value[at+1:]
			}
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		t.Fatal("no action references found under .github/ — the pinning rows would pass over an empty set")
	}
	return refs
}

// TestWorkflows is the workflow lint. Every row is mechanical and every
// row runs in CI, because CI runs the suite this file is in.
//
// MUTATIONS RUN AGAINST THESE ROWS, with what each one actually
// reddened. Every line below was performed and observed rather than
// predicted; the files were preserved by copy and the restores verified
// by checksum.
//
//   - a pinned reference changed to a moving tag, its version comment
//     left intact -> the pinning row alone. The comment row does not
//     move, which is the point of running the pair.
//   - a version comment deleted, the identifier left intact -> the
//     comment row alone.
//   - a provider authentication action added to a workflow, correctly
//     pinned and commented -> the provider row alone. Neither the
//     pinning row nor the vendor vocabulary check moves, and the second
//     of those is the finding: the action chosen for this mutation names
//     no term the vocabulary carries, so the vocabulary would have let it
//     through and the class list is what sees it.
//   - the same action added AND its entry deleted from the class list ->
//     NOTHING reddens. That is the intended result and the proof the
//     list is genuinely read on every run rather than duplicated in
//     this file.
//   - a credential secret added to the publishing job -> the allowlist
//     row alone. Removing a member from the allowlist while the workflow
//     still names it reddens the same row, which is the other direction.
//   - blanket write at workflow level -> the blanket-write row AND the
//     default-permissions row, because it is both.
//   - the workflow-level permissions block deleted -> the
//     default-permissions row alone.
//   - a manual trigger added beside the tag trigger, and separately the
//     tag pattern widened to everything -> the trigger row, both times.
//   - the deployment environment removed from the publishing job -> the
//     privileged-job row AND the identity row. Renaming it to something
//     with the reviewed environment's name INSIDE it reds both as well,
//     which it did not before these rows asked for the value rather than
//     for containment.
//   - the identity permission removed -> the signing-join row alone. Not
//     the identity row, which is correctly vacuous when nothing mints
//     one, and that vacancy is the whole reason the join row exists.
//   - full history removed from the job that checks ancestry, and
//     separately the ancestry command itself removed -> the tag row,
//     both times. The history mutation reddens nothing if the row is
//     asked of the file rather than of the job, because a sibling job
//     also asks for full history; that was found by running it.
//   - the update policy pointed at a different ecosystem -> the policy
//     row alone.
//   - the block reader made to return nothing -> its own reference row
//     first, and twelve rows across both tests behind it. Enforcement
//     machinery with nothing reporting on it is what that row is for.
func TestWorkflows(t *testing.T) {
	root := moduleRoot(t)
	refs := collectReferences(t, root)

	// A local composite action has no reference to pin: it is this
	// repository's own code, arriving through the checkout that already
	// happened. Nothing else is exempt, and a reference that is neither
	// local nor a full identifier fails below rather than being ignored.
	local := func(r reference) bool { return strings.HasPrefix(r.Action, "./") }

	t.Run("every action is pinned to a full commit identifier", func(t *testing.T) {
		checked := 0
		for _, r := range refs {
			if local(r) {
				continue
			}
			checked++
			if !pinnedSHA.MatchString(r.Ref) {
				t.Errorf("%s:%d pins %q to %q, which is not a 40-character commit identifier\n"+
					"A tag moves, and whoever can move it runs their code in a job that may hold "+
					"the credentials this repository publishes with.", r.File, r.Line, r.Action, r.Ref)
			}
		}
		if checked == 0 {
			t.Fatal("no third-party action references were checked — this row would pass over an empty set")
		}
	})

	t.Run("every pinned action carries its version in a comment", func(t *testing.T) {
		checked := 0
		for _, r := range refs {
			if local(r) {
				continue
			}
			checked++
			if !versionComment.MatchString(r.Comment) {
				t.Errorf("%s:%d pins %q with no readable version comment (found %q)\n"+
					"A pin nobody can read is a pin nobody updates, and pinning that decays is "+
					"pinning to something old with a known hole.", r.File, r.Line, r.Action, r.Comment)
			}
		}
		if checked == 0 {
			t.Fatal("no third-party action references were checked — this row would pass over an empty set")
		}
	})

	t.Run("no action authenticates to an infrastructure provider", func(t *testing.T) {
		fragments := loadProviderAuthActions(t, root)
		for _, r := range refs {
			action := strings.ToLower(r.Action)
			for _, f := range fragments {
				if strings.Contains(action, f) {
					t.Errorf("%s:%d runs %q, which authenticates to an infrastructure provider\n"+
						"There is nothing in this repository to deploy and no infrastructure "+
						"credential anywhere in it. A job that can authenticate to a provider is a "+
						"job that can be made to do something with one.", r.File, r.Line, r.Action)
					break
				}
			}
		}
	})

	t.Run("no secret outside the allowlist is referenced", func(t *testing.T) {
		seen := 0
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
					seen++
					if _, ok := allowedSecrets[name]; !ok {
						t.Errorf("%s:%d references the secret %q, which is not on this repository's allowlist\n"+
							"The allowlist is a statement about what may be referenced, not an inventory of "+
							"what exists. An infrastructure credential has no place here at all: there is "+
							"nothing in this repository to deploy.", rel, i+1, name)
					}
				}
			}
		}
		if seen == 0 {
			t.Fatal("no secret reference was found anywhere under .github/, so this row observed nothing " +
				"— the release workflow is supposed to name at least one")
		}
	})

	t.Run("no workflow grants blanket write", func(t *testing.T) {
		for _, path := range githubFiles(t, root) {
			rel := filepath.ToSlash(mustRel(t, root, path))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", rel, err)
			}
			for i, line := range strings.Split(string(data), "\n") {
				if strings.Contains(line, "write-all") {
					t.Errorf("%s:%d grants write-all\n"+
						"Every permission a job holds is one an action running inside it holds too.",
						rel, i+1)
				}
			}
		}
	})

	t.Run("every workflow defaults to read", func(t *testing.T) {
		checked := 0
		for _, path := range workflowFiles(t, root) {
			rel := filepath.ToSlash(mustRel(t, root, path))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s: %v", rel, err)
			}
			checked++
			lines := readYAMLLines(string(data))
			body, ok := topLevelBlock(lines, "permissions")
			if !ok {
				t.Errorf("%s declares no workflow-level permissions block\n"+
					"Without one the jobs inherit whatever the repository default is, which is a "+
					"setting this file cannot see and a reviewer cannot check in the diff.", rel)
				continue
			}
			if got := textsOf(body); len(got) != 1 || got[0] != "contents: read" {
				t.Errorf("%s declares workflow-level permissions %v, want exactly contents: read\n"+
					"A job that needs more says so itself, in the job, where the reason is visible.",
					rel, got)
			}
		}
		if checked == 0 {
			t.Fatal("no workflow files were found — this row would pass over an empty set")
		}
	})

	t.Run("the release workflow runs only on a version tag", func(t *testing.T) {
		lines := readYAMLLines(readRepoFile(t, root, releaseWorkflow))
		body, ok := topLevelBlock(lines, "on")
		if !ok {
			t.Fatalf("%s declares no triggers at all", releaseWorkflow)
		}
		var triggers []string
		depth := -1
		for _, l := range body {
			if depth < 0 || l.Indent < depth {
				depth = l.Indent
			}
		}
		for _, l := range body {
			if l.Indent == depth {
				triggers = append(triggers, strings.TrimSuffix(l.Text, ":"))
			}
		}
		if len(triggers) != 1 || triggers[0] != "push" {
			t.Errorf("%s triggers on %v, want push and nothing else\n"+
				"Any second trigger is a way to reach the publishing job without a tag.",
				releaseWorkflow, triggers)
		}
		if idx := findKey(body, "branches"); idx >= 0 {
			t.Errorf("%s:%d filters the push trigger by branch\n"+
				"A branch push would then reach the publishing job.", releaseWorkflow, body[idx].N)
		}
		idx := findKey(body, "tags")
		if idx < 0 {
			t.Fatalf("%s does not filter its push trigger by tag, so every push publishes", releaseWorkflow)
		}
		if got := listValues(body, idx); len(got) != 1 || got[0] != "v*" {
			t.Errorf("%s filters tags as %v, want exactly one pattern reading v*", releaseWorkflow, got)
		}
	})

	t.Run("every privileged job needs a reviewer", func(t *testing.T) {
		// EVERY workflow, not only the one that publishes. Scoping this to
		// the release workflow was this row's own first shape and it is
		// the wrong field of view: the job that would do damage without a
		// reviewer is precisely the one somebody added to a workflow
		// nobody thinks of as a publishing workflow.
		inRelease := 0
		for _, path := range workflowFiles(t, root) {
			rel := filepath.ToSlash(mustRel(t, root, path))
			found := jobs(readYAMLLines(readRepoFile(t, root, rel)))
			if len(found) == 0 {
				t.Errorf("%s declares no jobs", rel)
				continue
			}
			for _, j := range found {
				body := strings.Join(textsOf(j.Body), "\n")
				// A job is privileged if it can write anything, or if it
				// names a credential the runner did not mint for it. Both
				// are the same question asked of two surfaces: can this
				// job, or anything running inside it, reach the outside
				// world with authority.
				writes := strings.Contains(body, ": write")
				var named []string
				for _, m := range secretReference.FindAllStringSubmatch(body, -1) {
					name := m[1]
					if name == "" {
						name = m[2]
					}
					if name != "GITHUB_TOKEN" {
						named = append(named, name)
					}
				}
				if !writes && len(named) == 0 {
					continue
				}
				if rel == releaseWorkflow {
					inRelease++
				}
				if !hasEntry(j.Body, "environment", requiredEnvironment) {
					t.Errorf("%s: job %q can write or names %v, and does not run in the %q environment\n"+
						"An environment nothing references gates nothing. A tag push fires binaries, "+
						"checksums, a signature, a release and a tap write at once, and the only brake "+
						"that cannot be bypassed by declining to use it is a server-side one.",
						rel, j.Name, named, requiredEnvironment)
				}
			}
		}
		// THE POSITIVE CONTROL. Every assertion above is satisfied by a
		// repository with no privileged job anywhere — which is also what
		// a release pipeline that has quietly stopped being able to
		// publish looks like, and the rows would report it as compliance.
		if inRelease == 0 {
			t.Errorf("%s has no privileged job at all, so the rows above observed nothing — "+
				"the workflow that publishes is supposed to have one", releaseWorkflow)
		}
	})

	t.Run("an identity token is minted only where a reviewer has approved", func(t *testing.T) {
		for _, path := range workflowFiles(t, root) {
			rel := filepath.ToSlash(mustRel(t, root, path))
			lines := readYAMLLines(readRepoFile(t, root, rel))
			for _, j := range jobs(lines) {
				body := strings.Join(textsOf(j.Body), "\n")
				if !strings.Contains(body, "id-token: write") {
					continue
				}
				if !hasEntry(j.Body, "environment", requiredEnvironment) {
					t.Errorf("%s: job %q mints an identity token outside the %q environment\n"+
						"That token is what a signature is made with, and it says this repository's "+
						"workflow produced the artefact. It belongs only where a person has approved "+
						"the run.", rel, j.Name, requiredEnvironment)
				}
			}
		}
	})

	t.Run("what the configuration signs with, the workflow mints", func(t *testing.T) {
		// THE JOIN, and it is the row the two above cannot cover between
		// them. The row before this one says an identity token appears
		// only under a reviewer, and it is vacuously true when no job
		// mints one at all — which is exactly the state a release that
		// has quietly stopped signing is in. The configuration asks for a
		// signature; nothing in the configuration can tell whether the
		// workflow grants what making one requires. Two files, each
		// correct alone, and the failure lives between them: the release
		// runs, the signing step cannot get an identity, and what ships
		// is a checksum file with no answer to "who built this".
		if _, ok := topLevelBlock(readYAMLLines(readRepoFile(t, root, releaseConfig)), "signs"); !ok {
			t.Fatalf("%s signs nothing, so this row has no join to check and the release has "+
				"no answer to the question a signature exists to answer", releaseConfig)
		}
		minted := false
		for _, j := range jobs(readYAMLLines(readRepoFile(t, root, releaseWorkflow))) {
			if strings.Contains(strings.Join(textsOf(j.Body), "\n"), "id-token: write") {
				minted = true
			}
		}
		if !minted {
			t.Errorf("%s signs its checksums and no job in %s mints an identity to sign with\n"+
				"The signing step then fails, or worse succeeds against nothing, and the "+
				"artefact ships with a checksum that cannot say who produced it.",
				releaseConfig, releaseWorkflow)
		}
	})

	t.Run("the tag is refused unless it is on the default branch", func(t *testing.T) {
		body := readRepoFile(t, root, releaseWorkflow)
		// A SPELLING ASSERTION, and it is written down as one. It cannot
		// prove the command is correct; what it does is make deleting the
		// command loud, which is the failure this row exists for. The
		// command's own behaviour is exercised for the first time on the
		// first real tag, and that residue is stated in the workflow.
		if !strings.Contains(body, "merge-base --is-ancestor") {
			t.Errorf("%s contains no ancestry check\n"+
				"A tag can be pushed at any commit, including one no review ever saw. Without "+
				"this the tag is the only thing deciding what gets published.", releaseWorkflow)
		}
		// The ancestry check cannot work against a shallow checkout: the
		// commits it needs are not there, and the command fails for a
		// reason that has nothing to do with the tag. It fails closed,
		// which is the right direction and a bad diagnosis.
		//
		// ASKED OF THE JOB THAT DOES THE CHECK, not of the file. Every
		// job in this workflow asks for full history and only one of them
		// needs it for this reason, so a check against the file's whole
		// text is satisfied by a sibling job and cannot see the one that
		// matters losing it.
		checked := 0
		for _, j := range jobs(readYAMLLines(body)) {
			text := strings.Join(textsOf(j.Body), "\n")
			if !strings.Contains(text, "merge-base --is-ancestor") {
				continue
			}
			checked++
			if !strings.Contains(text, "fetch-depth: 0") {
				t.Errorf("%s: job %q checks ancestry without asking for the history to check "+
					"it against\n"+
					"A shallow checkout has neither the default branch nor the commits between, "+
					"so the check refuses every tag for a reason that is not about the tag.",
					releaseWorkflow, j.Name)
			}
		}
		if checked == 0 {
			t.Errorf("no job in %s performs the ancestry check, so the row above matched the "+
				"command somewhere it does not run", releaseWorkflow)
		}
	})

	t.Run("action versions are kept current by policy", func(t *testing.T) {
		raw := readRepoFile(t, root, dependabotPolicy)
		lines := readYAMLLines(raw)
		if !strings.Contains(raw, "version: 2") {
			t.Errorf("%s does not declare the policy version this service reads", dependabotPolicy)
		}
		updates, ok := topLevelBlock(lines, "updates")
		if !ok {
			t.Fatalf("%s declares no updates at all", dependabotPolicy)
		}
		covered := false
		for _, l := range updates {
			if strings.Contains(l.Text, "package-ecosystem:") &&
				strings.Contains(strings.Trim(l.Text, `"'`), "github-actions") {
				covered = true
			}
		}
		if !covered {
			t.Errorf("%s does not cover action updates\n"+
				"Pinning done once decays into a pin on something old with a known hole, and "+
				"nothing in this repository would notice.", dependabotPolicy)
		}
	})
}

// workflowFiles returns the workflow definitions, which are the subset of
// .github/ that declares jobs and permissions.
func workflowFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	for _, p := range githubFiles(t, root) {
		rel := filepath.ToSlash(mustRel(t, root, p))
		if !strings.HasPrefix(rel, ".github/workflows/") {
			continue
		}
		if strings.HasSuffix(rel, ".yml") || strings.HasSuffix(rel, ".yaml") {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		t.Fatal("no workflow files found — this guard would silently pass")
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------
// What the release configuration builds, and how.
// ---------------------------------------------------------------------

const releaseConfig = ".goreleaser.yaml"

// requiredTargets is the set of platform and architecture pairs a release
// must carry, written out rather than derived from anything, because it
// is the promise being made to whoever downloads a binary.
var requiredTargets = map[string]bool{
	"darwin/amd64":  true,
	"darwin/arm64":  true,
	"linux/amd64":   true,
	"linux/arm64":   true,
	"windows/amd64": true,
	"windows/arm64": true,
}

// TestReleaseConfig asserts the properties of the release configuration
// that the release tool itself will not check, because they are choices
// rather than schema.
//
// The tool validates that the document is well formed and current. It has
// no opinion about WHAT is built, and building the wrong thing is the
// failure that ships a test tool to strangers as though it were the
// product.
//
// MUTATIONS RUN AGAINST THESE ROWS, performed and observed rather than
// predicted, with the files preserved by copy and the restores verified
// by checksum:
//
//   - the build target widened to everything the module root builds ->
//     the target row alone.
//   - a platform dropped from the list, and separately an architecture
//     added that nothing here promises -> the matrix row, both times and
//     in both directions.
//   - path trimming removed, and separately turned OFF BY VALUE with the
//     flag still present -> the provenance row, both times. The second
//     spelling passed until this row asked for a list member rather than
//     for text somewhere in the block.
//   - the module timestamp removed, and separately pointed at the clock
//     -> the provenance row, both times. The clock spelling passed until
//     this row asked for the value.
//   - the checksum algorithm changed -> the checksum row.
//   - the signing block removed entirely -> the checksum row AND the
//     signing-join row in the test above, which is the join doing its
//     job across two files.
//   - the signing tool renamed to something with its name INSIDE it ->
//     nothing, until this row asked for the value; then the same
//     mutation reds, as does deleting the line, as does pointing the
//     signature at a different artefact kind. That inert mutation is
//     where the four value-rather-than-containment corrections came
//     from, and three of them closed evasions no mutation had been
//     aimed at.
//   - the archive override for the one platform without a tar removed
//     -> the archive row.
//   - the tap credential removed -> the tap row.
func TestReleaseConfig(t *testing.T) {
	body := readRepoFile(t, moduleRoot(t), releaseConfig)
	lines := readYAMLLines(body)

	builds, ok := topLevelBlock(lines, "builds")
	if !ok {
		t.Fatalf("%s declares no builds", releaseConfig)
	}

	t.Run("the build target is the command and only the command", func(t *testing.T) {
		idx := findKey(builds, "main")
		if idx < 0 {
			t.Fatalf("%s names no build target\n"+
				"Left unnamed the tool picks up whatever the module root builds, and this module "+
				"root builds more than the product.", releaseConfig)
		}
		got := strings.TrimSpace(strings.SplitN(builds[idx].Text, ":", 2)[1])
		if strings.Trim(got, `"'`) != "./cmd/curious" {
			t.Errorf("%s builds %q, want ./cmd/curious\n"+
				"This module also carries a program the build invokes and ships to nobody. A "+
				"target written as a wildcard would publish it as a release artefact.",
				releaseConfig, got)
		}
	})

	t.Run("every platform this repository promises is built", func(t *testing.T) {
		goosIdx, goarchIdx := findKey(builds, "goos"), findKey(builds, "goarch")
		if goosIdx < 0 || goarchIdx < 0 {
			t.Fatalf("%s does not name both a platform list and an architecture list", releaseConfig)
		}
		if idx := findKey(builds, "ignore"); idx >= 0 {
			t.Errorf("%s:%d drops a pair from the build matrix\n"+
				"Whatever it drops, somebody's machine is it.", releaseConfig, builds[idx].N)
		}
		produced := map[string]bool{}
		for _, o := range listValues(builds, goosIdx) {
			for _, a := range listValues(builds, goarchIdx) {
				produced[o+"/"+a] = true
			}
		}
		var missing, extra []string
		for want := range requiredTargets {
			if !produced[want] {
				missing = append(missing, want)
			}
		}
		for got := range produced {
			if !requiredTargets[got] {
				extra = append(extra, got)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 {
			t.Errorf("%s builds nothing for %v", releaseConfig, missing)
		}
		if len(extra) > 0 {
			t.Errorf("%s builds %v, which nothing in this repository promises\n"+
				"An artefact nobody asked for is one nobody tests.", releaseConfig, extra)
		}
	})

	t.Run("the binary carries its own provenance and nothing of the machine that built it", func(t *testing.T) {
		flags := strings.Join(textsOf(builds), "\n")
		for _, want := range []string{
			"CGO_ENABLED=0",
			"-X main.version=",
			"-X main.commit=",
			"-X main.date=",
		} {
			if !strings.Contains(flags, want) {
				t.Errorf("%s does not set %s\n"+
					"Without the stamps a released binary reports itself as an unreleased one.",
					releaseConfig, want)
			}
		}
		// The trimming is asked for as a LIST MEMBER rather than as text
		// somewhere in the block: a flag can be spelled with a value that
		// turns it off, and that spelling contains the flag.
		trimmed := false
		if i := findKey(builds, "flags"); i >= 0 {
			for _, f := range listValues(builds, i) {
				if f == "-trimpath" {
					trimmed = true
				}
			}
		}
		if !trimmed {
			t.Errorf("%s does not trim build paths\n"+
				"An untrimmed binary reports the filesystem of whoever built it, which is both "+
				"a disclosure and a reason two builds of one commit differ.", releaseConfig)
		}
		// The VALUE, not the presence of the key. A timestamp taken from
		// the clock is spelled exactly like one taken from the commit.
		if ts, ok := valueOf(builds, "mod_timestamp"); !ok || !strings.Contains(ts, "CommitTimestamp") {
			t.Errorf("%s fixes the module timestamp to %q rather than to the commit's own\n"+
				"Anything else means the same commit produces different bytes on different days, "+
				"and nobody can check that the published binary is the published source.",
				releaseConfig, ts)
		}
	})

	t.Run("the artefacts are checksummed and the checksums are signed", func(t *testing.T) {
		checksum, ok := topLevelBlock(lines, "checksum")
		if !ok {
			t.Fatalf("%s produces no checksums", releaseConfig)
		}
		if got, ok := valueOf(checksum, "algorithm"); !ok || got != "sha256" {
			t.Errorf("%s states the checksum algorithm as %q, want sha256", releaseConfig, got)
		}
		signs, ok := topLevelBlock(lines, "signs")
		if !ok {
			t.Fatalf("%s signs nothing\n"+
				"A checksum says the file was not altered in transit. It cannot say who built it, "+
				"and that is the question somebody downloading a binary is actually asking.",
				releaseConfig)
		}
		if got, ok := valueOf(signs, "artifacts"); !ok || got != "checksum" {
			t.Errorf("%s signs %q rather than the checksum file\n"+
				"Signing the checksums covers every artefact through one signature; signing "+
				"something else leaves the rest unattested.", releaseConfig, got)
		}
		// The VALUE again, and this one was found by a mutation that
		// should have reddened and did not: a containment test for the
		// signing tool's name is satisfied by every longer name with it
		// inside, so a wrapper of unknown provenance would have passed
		// the row asserting what makes the signature.
		if got, ok := valueOf(signs, "cmd"); !ok || got != "cosign" {
			t.Errorf("%s signs with %q\n"+
				"The row above says the checksums are signed; this is the one that says by "+
				"what, and without it the claim is about an unnamed program.", releaseConfig, got)
		}
	})

	t.Run("the archives are the shapes each platform can open", func(t *testing.T) {
		// THE ROW NAMES WHICH BLOCK IT IS TALKING ABOUT, and that is the
		// correction rather than the original shape. This asked whether
		// the archives block CONTAINED a tar.gz and a windows override,
		// which a second archive alongside it satisfies without being
		// the archive under discussion — a row that would pass on either
		// of two blocks has stopped pinning the thing it names.
		archives, ok := topLevelBlock(lines, "archives")
		if !ok {
			t.Fatalf("%s produces no archives", releaseConfig)
		}
		human, found := entryWithID(archives, "curious")
		if !found {
			t.Fatalf("%s has no archive named curious — the one a person downloads", releaseConfig)
		}
		formats := listOf(human, "formats")
		if len(formats) != 1 || formats[0] != "tar.gz" {
			t.Errorf("%s builds the human archive as %v, want exactly tar.gz", releaseConfig, formats)
		}
		if !hasEntry(human, "goos", "windows") {
			t.Errorf("%s does not override the archive format for the one platform whose "+
				"users cannot be assumed to have a tar", releaseConfig)
		}
		overrideAt := findKey(human, "format_overrides")
		if overrideAt < 0 ||
			!strings.Contains(strings.Join(textsOf(blockAt(human, overrideAt)), "\n"), "zip") {
			t.Errorf("%s does not give the one platform without a tar something it can open",
				releaseConfig)
		}
		// The paperwork is the whole reason this archive still exists.
		files := listOf(human, "files")
		for _, want := range []string{"LICENSE", "README.md"} {
			if !contains(files, want) {
				t.Errorf("%s ships the human archive without %s\n"+
					"Stripping the licence out of every download of an MIT-licensed tool to "+
					"please an install script is the wrong trade, and it is the reason there "+
					"are two archives rather than one.", releaseConfig, want)
			}
		}
	})

	t.Run("the wrapper's archive is a single member with no paperwork", func(t *testing.T) {
		// The npm wrapper's install script has to OPEN what it
		// downloads, and it does so with a standard library that reads
		// neither of the formats above. Every property here is one the
		// format's own one-member constraint forces: get either wrong
		// and the release fails to build rather than degrading.
		archives, ok := topLevelBlock(lines, "archives")
		if !ok {
			t.Fatalf("%s produces no archives", releaseConfig)
		}
		wrapper, found := entryWithID(archives, "curious-gz")
		if !found {
			t.Fatalf("%s publishes nothing the wrapper's install script can open", releaseConfig)
		}
		formats := listOf(wrapper, "formats")
		if len(formats) != 1 || formats[0] != "gz" {
			t.Errorf("%s builds the wrapper archive as %v, want exactly gz", releaseConfig, formats)
		}
		files := listOf(wrapper, "files")
		if len(files) != 1 || files[0] != "none*" {
			t.Errorf("%s leaves the wrapper archive's file list as %v\n"+
				"It defaults to the licence and readme globs, and a compression-only format "+
				"errors on the second member — so an unemptied list fails the build.",
				releaseConfig, files)
		}
		// RESTATED, NOT INHERITED. The default template spells the name
		// differently, and every download address the install script
		// builds would miss by a word.
		human, _ := entryWithID(archives, "curious")
		humanName, _ := valueOf(human, "name_template")
		wrapperName, hasName := valueOf(wrapper, "name_template")
		if !hasName || wrapperName != humanName {
			t.Errorf("%s names the wrapper archive %q and the human one %q\n"+
				"They differ only by extension on purpose; a wrapper archive under the "+
				"default template is a 404 at the first real release.",
				releaseConfig, wrapperName, humanName)
		}
	})

	t.Run("the tap is written by a token scoped to the tap", func(t *testing.T) {
		text := strings.Join(textsOf(lines), "\n")
		if !strings.Contains(text, "homebrew-tap") {
			t.Fatalf("%s publishes to no tap", releaseConfig)
		}
		if !strings.Contains(text, "HOMEBREW_TAP_TOKEN") {
			t.Errorf("%s writes to another repository without naming the token that may do so\n"+
				"The job's own token cannot reach another repository, so a tap write either names "+
				"a scoped credential or fails the whole release.", releaseConfig)
		}
	})
}

// TestReleaseConfigIsAcceptedByTheReleaseTool runs the tool's own
// validator over the configuration.
//
// IT SKIPS WHEN THE TOOL IS ABSENT, and that skip is declared in the
// manifest rather than hidden behind a build tag, deliberately: a
// build-tagged row never enters the test binary at all, so the gate that
// reports skips cannot see it and a reader has no way to tell a row that
// was excluded from one that passed.
//
// WHAT THE SKIP COSTS, stated because a declared skip is a claim: this
// row is the only thing in the suite that asks the release tool whether
// it still accepts the configuration — deprecations, renamed keys, a
// schema that moved under a version bump. Every other property of that
// file is asserted above and runs everywhere. The workflow that builds a
// snapshot on every change installs the tool at a pinned version and
// therefore runs this row's subject for real, so the coverage lost to the
// skip is local signal rather than the check itself.
func TestReleaseConfigIsAcceptedByTheReleaseTool(t *testing.T) {
	bin, err := exec.LookPath("goreleaser")
	if err != nil {
		t.Skip("the release tool is not installed on this machine, so its own validator " +
			"cannot be run here; the snapshot workflow installs it at a pinned version and " +
			"runs the same configuration through a real build")
	}
	cmd := exec.Command(bin, "check")
	cmd.Dir = moduleRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("the release tool rejects %s: %v\n%s", releaseConfig, err, out)
	}
}

// ---------------------------------------------------------------------
// The script a human runs before a release.
// ---------------------------------------------------------------------

const cutReleaseScript = "scripts/cut-release.sh"

// TestScriptsAreExecutableAsGitRecordsThem asserts the mode git carries,
// not the mode this filesystem reports.
//
// The two differ, and the one that matters is git's: a checkout on a
// platform with no permission bits reproduces whatever the index says. A
// script that lost its executable bit to an in-place rewrite passes every
// test invoked as `bash script` and fails the moment anything runs it the
// way an operator does.
//
// THE UNIVERSE IS WHAT THE FILE DECLARES ITSELF TO BE, not what it is
// called. This row used to select on a .sh suffix, and a suffix is a
// naming habit rather than a fact about the file — so the git hook under
// scripts/, which carries a shebang, is run by path, and has no extension
// because the directory git installs it into does not allow one, sat
// outside the guard entirely. It had the bit; nothing kept it. Reading
// the first line asks the question the mode is actually about: is this a
// file something will try to execute?
func TestScriptsAreExecutableAsGitRecordsThem(t *testing.T) {
	root := moduleRoot(t)
	cmd := exec.Command("git", "ls-files", "-s", "--", "scripts")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("listing scripts/ from the index: %v", err)
	}
	checked, skipped := 0, 0
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		name := fields[len(fields)-1]
		if !hasShebang(t, filepath.Join(root, filepath.FromSlash(name))) {
			skipped++
			continue
		}
		checked++
		if fields[0] != "100755" {
			t.Errorf("%s is recorded as mode %s, want 100755\n"+
				"A script an operator invokes by path cannot run without it, and every test that "+
				"invokes it through an interpreter will pass anyway.", name, fields[0])
		}
	}
	if checked == 0 {
		t.Fatal("no executable script is tracked under scripts/ — this row would pass over an empty set")
	}
	// AND THE OTHER HALF, because "look for a shebang" is only a
	// narrowing if something is narrowed. The rule files under scripts/
	// are data and carry none, so a reader that found a shebang in
	// everything — or in nothing — would satisfy the loop above while
	// measuring the wrong set.
	if skipped == 0 {
		t.Error("every tracked file under scripts/ was read as executable, including the rule " +
			"files, so this row is not distinguishing a script from data")
	}
}

// hasShebang reports whether a file begins with the two bytes that make
// the kernel look for an interpreter.
func hasShebang(t *testing.T, path string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	defer f.Close()
	var first [2]byte
	n, err := f.Read(first[:])
	if err != nil && n == 0 {
		return false
	}
	return n == 2 && first[0] == '#' && first[1] == '!'
}

// TestCutReleaseMakesNoOutwardChangeWithoutConsent drives the script with
// every program it calls replaced by a stub that records its arguments
// and answers plausibly.
//
// WHY A STUB RATHER THAN A READING. The property is that nothing outward
// happens without an explicit word from the operator, and a reading of
// the script proves that only for the paths the reader thought to follow.
// The stubs make the claim observable: whatever the script decides to do,
// it does it to these, and the log is the whole record of what it did.
//
// The stubs also make the row hermetic. It contacts nothing, so it is the
// same row on a machine with no network and no credentials, which is
// where most people will run it.
//
// MUTATIONS RUN, performed and observed: moving the tag push above the
// consent gate reds this row and nothing else. Loosening the version
// pattern to accept anything reds six of the seven refusal rows below
// and leaves this one alone, which is the two of them dividing the
// subject correctly. Clearing the script's executable bit in the index
// reds the mode row and neither of these, because both invoke the script
// through an interpreter and an interpreter does not need the bit — which
// is exactly why that row reads the index rather than the filesystem.
func TestCutReleaseMakesNoOutwardChangeWithoutConsent(t *testing.T) {
	root := moduleRoot(t)
	bash, err := exec.LookPath("bash")
	if err != nil {
		// DELIBERATELY UNDECLARED. Every platform this repository's own
		// workflow runs invokes its steps through this interpreter, so a
		// machine without it cannot be running the suite the way CI does
		// and the skip can only mean something is wrong. That is the same
		// reasoning the manifest already applies to the row needing the
		// program this source was obtained with.
		t.Skip("no interpreter to drive the release script with")
	}

	log := filepath.Join(t.TempDir(), "calls.log")
	harness := writeHarness(t, t.TempDir())

	cmd := exec.Command(bash, harness)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"CALL_LOG="+log,
		"EMPTY_PATH="+t.TempDir(),
		"SCRIPT="+filepath.Join(root, filepath.FromSlash(cutReleaseScript)),
		"VERSION=v9.9.9",
	)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("the release script failed on a run that changes nothing: %v\n%s", runErr, out)
	}

	recorded, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("reading the call log: %v", err)
	}
	calls := strings.Split(strings.TrimSpace(string(recorded)), "\n")
	if len(calls) == 0 || calls[0] == "" {
		t.Fatal("the script called nothing at all, so this row cannot tell a script that " +
			"refused to act from one that never ran")
	}

	// The outward half. Anything here reaches a place this run had no
	// permission to reach.
	outward := []string{"tag -a", "tag -s", "push", "release create", "release upload", "publish"}
	for _, call := range calls {
		for _, forbidden := range outward {
			if strings.Contains(call, forbidden) {
				t.Errorf("without consent the script ran: %s\n"+
					"Everything this script does before the operator agrees must be a reading. "+
					"A brake anybody gets past by not using it is not a brake, and the only reason "+
					"this one is worth having is that it stops by default.", call)
			}
		}
	}

	// The printing half. Stopping is only useful if the operator is left
	// holding the exact commands, because a stop that makes somebody
	// compose the command themselves has moved the risk rather than
	// removed it.
	text := string(out)
	for _, want := range []string{"git tag", "git push", "v9.9.9"} {
		if !strings.Contains(text, want) {
			t.Errorf("the script stopped without printing %q\n%s", want, text)
		}
	}
	if !strings.Contains(text, "--yes") {
		t.Errorf("the script stopped without saying what would make it proceed\n%s", text)
	}
}

// writeHarness writes a wrapper that drives the release script with
// every program it calls replaced by a shell FUNCTION recording the
// invocation, and returns the wrapper's path.
//
// FUNCTIONS RATHER THAN FILES ON A PATH, and it is not a style choice.
// A stand-in placed on the PATH has to be an executable file, and
// whether a file is executable — whether it needs an extension, whether
// a mode bit survives the filesystem, whether the interpreter line is
// resolvable — is a different answer on each of the three platforms this
// suite runs on. A shell function takes precedence over any PATH lookup
// on all of them, and is exactly as visible to `command -v`.
//
// PATH IS EMPTIED ANYWAY, which is the half the functions do not buy: it
// turns a call to any program the script was not supposed to make into a
// loud failure rather than a real invocation. The three functions plus an
// empty PATH say together that the log below is the WHOLE record of what
// the script did.
//
// The answers are the ones the real programs gave when these rows were
// written. They are canned because the subject is what the script DOES
// with an answer, not what the world says back.
func writeHarness(t *testing.T, dir string) string {
	t.Helper()
	const harness = `set -euo pipefail
record() {
  printf '%s' "$1" >>"$CALL_LOG"; shift
  for a in "$@"; do printf ' %s' "$a" >>"$CALL_LOG"; done
  printf '\n' >>"$CALL_LOG"
}
git() {
  record git "$@"
  case "$1 ${2:-}" in
    "tag -l") printf '%s\n' "v0.0.1" ;;
    "rev-parse --abbrev-ref") printf '%s\n' "main" ;;
    *) : ;;
  esac
}
gh() {
  record gh "$@"
  case "$1" in
    api) printf '%s\n' "[]" ;;
    *) : ;;
  esac
}
npm() {
  record npm "$@"
  printf '%s\n' '{"latest":"1.0.0"}'
}
PATH="$EMPTY_PATH"
source "$SCRIPT" "$VERSION"
`
	path := filepath.Join(dir, "harness.sh")
	if err := os.WriteFile(path, []byte(harness), 0o644); err != nil {
		t.Fatalf("writing the harness: %v", err)
	}
	return path
}

// usageExit is the status the script answers a wrong invocation with. It
// is a SPECIFIC number rather than "non-zero", and the reason is the
// defect this row was written with and caught on its own first run.
//
// The first version asserted only that the run failed. It passed with no
// script in the tree at all, because a missing file makes the interpreter
// exit 127 and 127 is not zero. An assertion whose success condition is
// "an error occurred" is satisfied by every error, including the ones
// meaning the subject was never reached — so it has to say WHICH error,
// and the script has to be the thing that produced it.
const usageExit = 2

// TestCutReleaseRefusesAVersionThatIsNotOne checks the one piece of
// parsing the script does, at the boundary where a mistyped argument
// becomes a tag nobody can delete from other people's machines.
func TestCutReleaseRefusesAVersionThatIsNotOne(t *testing.T) {
	root := moduleRoot(t)
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no interpreter to drive the release script with")
	}
	script := filepath.Join(root, filepath.FromSlash(cutReleaseScript))
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("%s: %v — with no script every row below would report a refusal that is "+
			"really the interpreter failing to find a file", cutReleaseScript, err)
	}

	// A run with NO network and NO stubs: the refusal must happen before
	// the script asks the world anything, or a mistyped version reaches a
	// registry before it reaches a check.
	for _, arg := range []string{"1.2.3", "v1.2", "v1.2.3.4", "latest", "v1.2.3 ", "v1.2.3;rm", ""} {
		t.Run(strconv.Quote(arg), func(t *testing.T) {
			args := []string{script}
			if arg != "" {
				args = append(args, arg)
			}
			cmd := exec.Command(bash, args...)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "PATH="+t.TempDir())
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			switch {
			case err == nil:
				t.Errorf("the script accepted %q as a version\n%s", arg, out)
			case !errors.As(err, &exit):
				t.Errorf("the script did not run at all for %q: %v\n%s", arg, err, out)
			case exit.ExitCode() != usageExit:
				t.Errorf("the script exited %d for %q, want %d — any other status means it "+
					"failed for a reason other than refusing the argument\n%s",
					exit.ExitCode(), arg, usageExit, out)
			case !strings.Contains(string(out), "version"):
				t.Errorf("the script refused %q without saying what was wrong\n%s", arg, out)
			}
		})
	}

	// The POSITIVE CONTROL. Every row above passes if the script refuses
	// everything, including a version that is one — which is a script
	// nobody can release with and a row that cannot tell the difference.
	t.Run("a version that IS one is not refused here", func(t *testing.T) {
		log := filepath.Join(t.TempDir(), "calls.log")
		harness := writeHarness(t, t.TempDir())
		cmd := exec.Command(bash, harness)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"CALL_LOG="+log,
			"EMPTY_PATH="+t.TempDir(),
			"SCRIPT="+script,
			"VERSION=v1.2.3",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("the script refused a well-formed version: %v\n%s", err, out)
		}
	})
}

// ---------------------------------------------------------------------
// The surface check's own triggers and history.
// ---------------------------------------------------------------------

// ciWorkflow is the workflow that runs on every ref.
const ciWorkflow = ".github/workflows/ci.yml"

// surfaceCheckCommand is what a job runs to read the published surfaces
// a scan of file contents cannot see.
const surfaceCheckCommand = "surface-check"

// TestTheSurfaceCheckRunsWhenItMustAndCanSeeWhatItNeeds holds two
// settings that decide whether that check is coverage or decoration, and
// which nothing else in this repository can observe.
//
// A WORKFLOW IS A CLAIM ABOUT WHEN SOMETHING RUNS, and neither of these
// can be tested by running the check: one is about an event that has not
// happened, the other about a checkout that has not been made. They are
// asserted from the file, which is the only place the fact exists.
func TestTheSurfaceCheckRunsWhenItMustAndCanSeeWhatItNeeds(t *testing.T) {
	root := moduleRoot(t)

	t.Run("a pull request's title and body are re-read after they are edited",
		func(t *testing.T) {
			// The title and body are checked surfaces because a squash
			// merge composes its commit message out of them — and both can
			// be rewritten in a browser after the check has passed. The
			// default activity types are opened, synchronize and reopened;
			// an edit is not among them, so without `edited` the title
			// that becomes the commit message is the one nobody read.
			//
			// ALL FOUR ARE REQUIRED, because naming types REPLACES the
			// defaults rather than adding to them. Dropping synchronize
			// would stop this running on every push to a pull request, and
			// nothing about the line would look wrong.
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ciWorkflow)))
			if err != nil {
				t.Fatalf("reading %s: %v", ciWorkflow, err)
			}
			lines := readYAMLLines(string(data))
			triggers, ok := topLevelBlock(lines, "on")
			if !ok {
				t.Fatalf("%s declares no triggers at all", ciWorkflow)
			}
			at := findKey(triggers, "pull_request")
			if at < 0 {
				t.Fatalf("%s does not run on a pull request", ciWorkflow)
			}
			typesAt := findKey(blockAt(triggers, at), "types")
			if typesAt < 0 {
				t.Fatalf("%s names no activity types for a pull request, so it takes the "+
					"defaults — and an edit to the title is not one of them", ciWorkflow)
			}
			got := map[string]bool{}
			for _, v := range listValues(blockAt(triggers, at), typesAt) {
				got[v] = true
			}
			for _, want := range []string{"opened", "synchronize", "reopened", "edited"} {
				if !got[want] {
					t.Errorf("%s does not run on a pull request being %s.\n"+
						"Naming types replaces the defaults rather than adding to them, so "+
						"every one this check needs has to be spelled out.", ciWorkflow, want)
				}
			}
		})

	t.Run("every job that runs the surface check checks out the whole history",
		func(t *testing.T) {
			// THE CHECK FAILS CLOSED WITHOUT IT, which is the good half:
			// a range it cannot resolve is undetermined rather than clean.
			// The bad half is that it fails for every new branch and every
			// release tag, blaming a shallow checkout — which is true, and
			// leaves whoever reads it looking at the wrong line.
			//
			// A push of a new ref carries no before-sha, so what is new is
			// measured against the default branch. The checkout action
			// fetches only the pushed ref UNLESS the depth is unbounded,
			// in which case it fetches every head and tag. That behaviour
			// is a property of the pinned action rather than of this
			// repository, so what is asserted here is the input this
			// repository chooses.
			found := 0
			for _, path := range workflowFiles(t, root) {
				rel := filepath.ToSlash(mustRel(t, root, path))
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("reading %s: %v", rel, err)
				}
				for _, j := range jobs(readYAMLLines(string(data))) {
					body := strings.Join(textsOf(j.Body), "\n")
					if !strings.Contains(body, surfaceCheckCommand) {
						continue
					}
					found++
					if !hasEntry(j.Body, "fetch-depth", "0") {
						t.Errorf("%s:%d — the job %q reads a published range and does not ask "+
							"for the history to read it in.\nA new branch and a release tag "+
							"both have to be measured against the default branch, which a "+
							"shallow checkout does not carry.", rel, j.N, j.Name)
					}
				}
			}
			// THE FLOOR. Both workflows run this check, and a reader that
			// found neither would report every workflow as compliant.
			if found < 2 {
				t.Fatalf("found %d job(s) running the surface check, want at least 2 — the "+
					"row above would pass over an empty set", found)
			}
		})
}
