package flow

import (
	"bytes"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
)

// The two golden files this row reads. They are named here rather than
// spelled at each use so that the pair is obviously a pair: one run
// produces both, and a capture that refreshed one of them alone would be
// two halves of two different runs.
const (
	successStdoutGolden = "successful-run-stdout.golden"
	successStderrGolden = "successful-run-stderr.golden"
)

// TestASuccessfulRunWritesTheseExactBytes is the whole of what
// `curious deploy` puts on a terminal when everything works, held to the
// byte on each stream separately.
//
// WHY BYTES, WHEN THE SPLIT IS ALREADY ASSERTED NEXT DOOR. The rows in
// publish_test.go establish the PROPERTIES: the address is the last
// thing on stdout, the claim and the caveat and the expiry are on
// stderr. Every one of them asks whether something it names is present,
// and a property row cannot see what it does not name — a line that
// moved, a word that changed, a sentence that appeared between two it
// was checking for, a trailing blank that arrived. This row names
// nothing and therefore sees all of it. It is the byte-level row those
// three imply and none of them makes.
//
// WHAT IT IS FOR, and this is the reason it exists before the change it
// guards rather than beside it. `Deploy` is about to start handing its
// caller a structured result. A refactor that also moved a line of
// output would be a behaviour change wearing a refactor's clothes, and
// the only thing that can tell the two apart is a capture taken while
// the output is still known-good. A golden regenerated after the change
// asserts that the code equals itself.
//
// THE RENDERING IS THE PROGRAM'S OWN, not this package's double. The
// scripted terminal the rest of this suite uses calls Sprintf and keeps
// the result, so its bytes are the double's idea of the rendering rather
// than what a person's terminal receives — the escaping, the sanitising
// and the stream split all live in the real renderer. ui.Writing is that
// renderer wired to two buffers, asking nothing and reading no
// environment, which is exactly the shape a transcript needs.
//
// SO THE RUN MUST HAVE NOTHING TO ASK. ui.Writing is not interactive, so
// a question — the warning prompt's "Continue anyway?", or an email —
// ends the run with the no-terminal sentinel and there is no transcript
// to capture. That is why the project is the clean one and the token is
// already stored: not to make the row easy, but because a run that stops
// to ask has no complete output to be golden about.
//
// WHAT MAKES IT REPRODUCIBLE, stated so that a future one-byte diff has
// somewhere to be looked up rather than being argued about:
//
//   - the clock is fixed and so is its zone, so the expiry line renders
//     the same instant in the same offset on every machine;
//   - the archive is byte-identical anywhere, because the packer writes
//     one timestamp, one mode and zeroed ownership into every entry and
//     pins the gzip OS byte;
//   - the server and the object store are scripted, so the build log is
//     the one line the default script writes;
//   - .gitattributes checks these files out with LF on all three
//     platforms, so the comparison is not a line-ending test.
//
// AND THE ONE THING THAT WILL MOVE IT WITHOUT ANYBODY MEANING TO: the
// receipt names the archive's SIZE, which is the compressor's output. A
// change to what the packer writes should move this file and is the
// point. A change in the standard library's compressor would move it
// too, and that is the maintenance cost of measuring real bytes — named
// here so that whoever meets the diff knows to check `go version` before
// concluding the deploy sequence changed.
func TestASuccessfulRunWritesTheseExactBytes(t *testing.T) {
	run := newDeployRun(t, writeProject(t, astroProject(nil)))
	run.storedToken("stored-token", run.srv.URL)

	var stdout, stderr bytes.Buffer
	run.deps.Prompt = ui.Writing(&stdout, &stderr)

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	// THE TWO STREAMS ARE COMPARED SEPARATELY AND BOTH ARE COMPARED.
	// Joined into one buffer the row would pass over a line that moved
	// from stderr to stdout, which is the single most damaging thing
	// that can happen to this program's output: it is what puts
	// narration into `curious deploy > url.txt`.
	if got, want := stdout.String(), readGolden(t, successStdoutGolden); got != want {
		t.Errorf("stdout is not the captured transcript.\n got: %q\nwant: %q", got, want)
	}
	if got, want := stderr.String(), readGolden(t, successStderrGolden); got != want {
		t.Errorf("stderr is not the captured transcript.\n got: %q\nwant: %q", got, want)
	}
}
