package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ONE PACKAGE PUTS BYTES IN FRONT OF A PERSON, and the guard is here
// because that is a property of the repository rather than of any file
// in it.
//
// internal/ui escapes everything it renders. That is worth exactly as
// much as the number of places that can write to a terminal WITHOUT
// going through it — one bypass and the boundary is decoration. So:
// nothing outside internal/ui may name os.Stdout or os.Stderr.
//
// The exceptions are named individually rather than by a pattern,
// because a pattern is how the next one gets in.
var streamExceptions = map[string]string{
	// The process's entry point, which must obtain the real streams in
	// order to hand them to anything at all. It passes them on and
	// writes usage text; it renders no server-supplied string.
	"cmd/curious/main.go": "the entry point obtains the streams and hands them on",

	// Developer tools, not the shipped CLI. Neither is on any path a
	// user runs, and neither renders anything a server said.
	"tools/skipcheck/main.go":    "a build tool, not the CLI",
	"tools/surfacecheck/main.go": "a build tool, not the CLI",

	// Rows that CAPTURE the streams rather than write to them. Each one
	// swaps the process's stream for a file or a pipe so it can assert
	// on what the real renderer produced — which is the opposite of a
	// bypass: it is how the boundary gets measured at all.
	"internal/flow/preflight_test.go":  "swaps both streams to capture a real rendering",
	"internal/flow/upload_test.go":     "swaps both streams to capture a real rendering",
	"internal/mcp/interactive_test.go": "swaps stdin and stderr to drive a real session",
	"cmd/curious/main_test.go":         "swaps stderr to capture what the entry point wrote",

	// A helper PROCESS, re-executed by its own test. It is not the CLI
	// and it never renders a server's words; it reports its own setup
	// failures before the process it is standing in for exists.
	"internal/api/proxytest/main_test.go": "a re-executed helper process reporting its own setup",

	// This file. It names both streams as the strings it searches for,
	// which is the one place naming them is the whole point.
	"internal/guard/streams_test.go": "the guard names the strings it looks for",
}

// TestNothingOutsideTheUIPackageWritesToATerminal.
//
// TEST FILES ARE INCLUDED ON PURPOSE. A row that swaps os.Stderr to
// capture output is legitimate and several do it — but a row is also the
// easiest place for a bypass to be written and never noticed, so they
// are listed by path here rather than exempted as a class.
//
// REQUIRED MUTATION, run 2026-09-09: write one line straight to
// os.Stderr from internal/flow. Reds with that file and line named. The
// control is that the exceptions above are still reported as present:
// deleting cmd/curious/main.go's use would red the completeness check
// below, so this row cannot pass by looking at nothing.
func TestNothingOutsideTheUIPackageWritesToATerminal(t *testing.T) {
	root := moduleRoot(t)
	var offenders []string
	seen := map[string]bool{}

	for _, path := range goFiles(t, root, true) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("relative path for %s: %v", path, err)
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/ui/") {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		if !strings.Contains(string(body), "os.Stdout") &&
			!strings.Contains(string(body), "os.Stderr") {
			continue
		}
		if _, allowed := streamExceptions[rel]; allowed {
			seen[rel] = true
			continue
		}
		offenders = append(offenders, rel)
	}

	for _, rel := range offenders {
		t.Errorf("%s names os.Stdout or os.Stderr. Everything a person reads goes "+
			"through internal/ui, which is the one place it is escaped; a write "+
			"that goes round it is a terminal control sequence away from the "+
			"screen. If this file genuinely needs the stream, add it to "+
			"streamExceptions with the reason.", rel)
	}

	// THE ALLOWLIST IS ASSERTED AS A SET, not merely consulted. An entry
	// for a file that no longer names the stream is a permission nobody
	// is using and nobody will notice going stale — and, worse, it makes
	// this row's silence mean less than it looks.
	for rel := range streamExceptions {
		if !seen[rel] {
			t.Errorf("streamExceptions permits %s, which no longer names either "+
				"stream. Remove the entry: an allowance nobody uses is an "+
				"allowance nobody is checking.", rel)
		}
	}
}
