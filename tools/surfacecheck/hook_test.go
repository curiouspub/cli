package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hookScript is the tracked script `make hooks` installs a shim to.
const hookScript = "scripts/pre-push"

// driveHook runs the pre-push hook over one line of the input git gives
// it, with the checker replaced by a shell function that records how it
// was called.
//
// A FUNCTION RATHER THAN A PROGRAM ON THE PATH, following the harness the
// release script's rows already use: whether a file is executable, and
// whether it needs an extension, are properties of the platform, and this
// question is not. The path is emptied as well, so nothing real can be
// reached even by accident.
func driveHook(t *testing.T, line string) []string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		// DELIBERATELY UNDECLARED, on the same reasoning the release
		// script's rows use: every platform this repository's workflow
		// runs invokes its steps through this interpreter, so a machine
		// without it cannot be running the suite the way CI does, and the
		// skip can only mean something is wrong.
		t.Skip("no interpreter to drive the pre-push hook with")
	}

	root := moduleRoot(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	harness := filepath.Join(dir, "harness.sh")
	script := `set -eu
go() {
  printf '%s' "go" >>"$CALL_LOG"
  for a in "$@"; do printf ' %s' "$a" >>"$CALL_LOG"; done
  printf '\n' >>"$CALL_LOG"
}
PATH="$EMPTY_PATH"
. "$HOOK" origin <<'INPUT'
` + line + `
INPUT
`
	if err := os.WriteFile(harness, []byte(script), 0o644); err != nil {
		t.Fatalf("writing the harness: %v", err)
	}

	cmd := exec.Command(bash, harness)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"CALL_LOG="+log,
		"EMPTY_PATH="+t.TempDir(),
		"HOOK="+filepath.Join(root, filepath.FromSlash(hookScript)),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the hook failed on a push that changes nothing: %v\n%s", err, out)
	}

	recorded, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("the hook called nothing at all: %v", err)
	}
	var calls []string
	for _, l := range strings.Split(string(recorded), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			calls = append(calls, l)
		}
	}
	if len(calls) == 0 {
		t.Fatal("the hook ran and asked the checker nothing, so the assertions below would " +
			"pass over an empty set")
	}
	return calls
}

// TestTheHookReadsWhatTheWorkflowReads is the row for a convenience that
// was quietly narrower than the gate it stands in for.
//
// The hook is NOT the enforcement and says so — it lives in a directory
// git does not clone, it is skipped by --no-verify, and it is absent on
// CI. What it must never be is DIFFERENT. A hook that reads fewer
// surfaces passes a push the run then fails, and the lesson its user
// draws is that the fast check disagrees with the slow one, so they stop
// running the fast one and the two-second failure becomes a two-minute
// one again.
//
// Both halves are here: a tag is asked for as a tag, and a branch is
// still asked for as a branch. Without the second, the row is satisfied
// by a hook that calls everything a tag.
//
// MUTATION RUN: put the tag branch of the case statement back to the
// single -branch call. The tag half reds, naming the call the hook
// actually made; the branch half stays green.
func TestTheHookReadsWhatTheWorkflowReads(t *testing.T) {
	const sha = "1111111111111111111111111111111111111111"
	const remote = "2222222222222222222222222222222222222222"

	t.Run("a tag is published as a tag, so its message is read too", func(t *testing.T) {
		calls := driveHook(t, "refs/tags/v9.9.9 "+sha+" refs/tags/v9.9.9 "+remote)
		joined := strings.Join(calls, "\n")
		if !strings.Contains(joined, "-tag v9.9.9") {
			t.Errorf("the hook did not ask for the tag as a tag, so its MESSAGE is never "+
				"read and its name is reported as a branch's:\n%s", joined)
		}
		if strings.Contains(joined, "-branch") {
			t.Errorf("the hook called a tag a branch:\n%s\nA report naming the wrong kind of "+
				"surface sends its reader to look for something that does not exist.", joined)
		}
	})

	t.Run("and a branch is still published as a branch", func(t *testing.T) {
		calls := driveHook(t, "refs/heads/pack/some-work "+sha+" refs/heads/pack/some-work "+remote)
		joined := strings.Join(calls, "\n")
		if !strings.Contains(joined, "-branch pack/some-work") {
			t.Errorf("the hook did not ask for the branch as a branch:\n%s", joined)
		}
		if strings.Contains(joined, "-tag") {
			t.Errorf("the hook called a branch a tag:\n%s", joined)
		}
	})
}

// TestTheTagFlagReadsBothOfATagsSurfaces asserts what the flag the hook
// now passes actually does, which the harness above cannot see: it stops
// at the call.
func TestTheTagFlagReadsBothOfATagsSurfaces(t *testing.T) {
	f := newFixture(t)
	f.write("notes.txt", "one\n")
	base := f.commit("first", "notes.txt")
	f.write("notes.txt", "two\n")
	head := f.commit("second", "notes.txt")
	f.git("tag", "--annotate", "--message", "the release note lives elsewhere", "v9.9.9")

	none := func(string) string { return "" }

	req, err := buildRequest(f.repo, none, base, head, "", "v9.9.9")
	if err != nil {
		t.Fatalf("building a request for a tag: %v", err)
	}
	var subjects []string
	for _, n := range req.Named {
		subjects = append(subjects, n.Subject)
	}
	if strings.Join(subjects, ",") != "tag name,tag message" {
		t.Fatalf("named surfaces %v, want the tag's name and its message — the same two the "+
			"workflow reads off a push event", subjects)
	}
	if !strings.Contains(req.Named[1].Text, "the release note lives elsewhere") {
		t.Errorf("the tag's message was read as %q", req.Named[1].Text)
	}

	// THE CONTROL. The branch flag still produces one surface, named as
	// a branch — so the row above is about tags rather than about a
	// checker that has started calling everything a tag.
	branchReq, err := buildRequest(f.repo, none, base, head, "pack/some-work", "")
	if err != nil {
		t.Fatalf("building a request for a branch: %v", err)
	}
	if len(branchReq.Named) != 1 || branchReq.Named[0].Subject != "branch name" {
		t.Errorf("named surfaces %v, want the branch name alone", branchReq.Named)
	}

	// AND A PUSH MOVES ONE REF. Asking for both is a mistake worth
	// refusing rather than resolving: whichever this picked would be a
	// silent decision about which surface went unread.
	if _, err := buildRequest(f.repo, none, base, head, "pack/some-work", "v9.9.9"); err == nil {
		t.Error("a request naming both a branch and a tag was accepted")
	}
}
