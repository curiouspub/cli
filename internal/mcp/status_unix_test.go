//go:build unix

package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/config"
	"github.com/curiouspub/cli/pkg/wire"
)

// TestAStatusReadCarriesWhatTheConfigurationWarnedAbout.
//
// # What is lost without it, and who loses it
//
// The configuration reports things worth telling a user that stop
// nothing, and the standing one is a credential file other accounts on
// the machine can read. A person meets it on every run of the command; a
// deploy through this surface carries it in the run's own narration.
// This tool reads the same configuration and used to drop it — so
// somebody who only ever talks to an agent would never be told, which is
// the one reader the warning most needs to reach, because they are not
// watching a terminal that could have shown it.
//
// # Why this row is tagged rather than skipped
//
// The warning is about FILE MODE, and mode bits do not carry that
// meaning on every platform this ships to — the configuration package
// says so itself and reports whether it was able to check at all. So the
// condition cannot be built everywhere, and a fixture's construction is
// a platform assumption whether or not anybody writes it down.
//
// A wholly skipped file is one nobody notices has stopped compiling, so
// this is a tagged file with its reason in it rather than a runtime
// skip. WHAT THE OTHER LEG IS NOT CLAIMING, stated where a reader of a
// green matrix will meet it: on a platform whose modes carry no such
// meaning nothing here asserts that a warning reaches the result,
// because there is no warning for it to carry.
func TestAStatusReadCarriesWhatTheConfigurationWarnedAbout(t *testing.T) {
	script := &deployScript{
		uploadPath: "/object-store/put",
		frames:     []string{finished(wire.StatusBuilt)},
	}
	run := newToolsRun(t, script)

	// THE FILE IS MADE READABLE BY OTHERS AFTER IT IS WRITTEN, because
	// the writer creates it locked down on purpose — which is the
	// behaviour, and is why this row has to undo it to produce the
	// condition rather than arranging for it.
	path, err := config.Path()
	if err != nil {
		t.Fatalf("resolving the config path: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("loosening the config file's mode: %v", err)
	}

	var answer deployStatusResult
	decodeInto(t, run.call(toolDeployStatus, `{"deploy_id":"dpl-warned"}`), &answer)

	if len(answer.Notes) == 0 {
		t.Fatalf("the result carried no note, and this machine's configuration has "+
			"one to give:\n%+v", answer)
	}
	// THE CONTROL. A row asserting only that SOMETHING arrived would pass
	// against a surface carrying any string at all, and what has to reach
	// the reader is the file — a warning about a credential nobody can
	// locate is a warning nobody can act on.
	if !strings.Contains(strings.Join(answer.Notes, "\n"), filepath.Base(path)) {
		t.Errorf("the note does not name the file it is about:\n%q", answer.Notes)
	}

	// AND THE CALL STILL WORKED. A note is not a refusal, and a surface
	// that reported the warning by failing would have made a readable
	// file into a broken tool.
	if !answer.Reported || answer.Status != string(wire.StatusBuilt) {
		t.Errorf("reported=%v status=%q, want the call to have answered normally",
			answer.Reported, answer.Status)
	}
}
