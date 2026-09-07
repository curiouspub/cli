package main

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// skip is one test row that did not run, and why its author said so.
type skip struct {
	Package string
	Test    string
	Reason  string
}

// event is the part of the test runner's machine-readable stream this
// tool reads. The rest of the fields are ignored on purpose: a decoder
// that names every field breaks when the toolchain adds one.
type event struct {
	Action  string
	Package string
	Test    string
	Output  string
}

// collect reads the stream, writes the human-readable run to w, and
// returns every skipped row together with the number of test outcomes it
// saw.
//
// THE OUTPUT DISCIPLINE IS WHY THIS IS NOT JUST `-v`. Making every run
// verbose to surface three lines produces a log nobody reads, and a log
// nobody reads hides a skip exactly as well as no log at all. So a
// passing test's chatter is dropped, a failing test's is kept, package
// summary lines are kept — the run looks like the ordinary one — and the
// skips are gathered for a section of their own at the end.
//
// The COUNT is returned because a tool that read nothing reports the
// same clean result as a tool that read a clean run, and those are
// different states.
func collect(r io.Reader, w io.Writer) ([]skip, int, error) {
	scanner := bufio.NewScanner(r)
	// A single output event can carry a long line — a diff of two golden
	// files, say — and the default limit would truncate it into invalid
	// JSON.
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	buffered := map[string][]string{}
	var skips []skip
	outcomes := 0

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var e event
		if err := json.Unmarshal(line, &e); err != nil {
			// Anything the runner writes that is not an event is
			// written straight through rather than swallowed: a
			// toolchain error arrives that way, and it is the one
			// message a caller most needs to see.
			if _, err := io.WriteString(w, string(line)+"\n"); err != nil {
				return nil, 0, err
			}
			continue
		}

		key := e.Package + "\x00" + e.Test
		switch e.Action {
		case "run":
			delete(buffered, key)
		case "output":
			buffered[key] = append(buffered[key], e.Output)
		case "pass":
			if e.Test == "" {
				if err := flush(w, buffered[key]); err != nil {
					return nil, 0, err
				}
			} else {
				outcomes++
			}
			delete(buffered, key)
		case "fail":
			if e.Test != "" {
				outcomes++
			}
			if err := flush(w, buffered[key]); err != nil {
				return nil, 0, err
			}
			delete(buffered, key)
		case "skip":
			// A PACKAGE-LEVEL SKIP IS NOT A SKIPPED ROW. The stream
			// spells "this package has no test files" with the same
			// word, and counting it would fail every run over a package
			// that has none — a state this repository is in on purpose.
			if e.Test == "" {
				if err := flush(w, buffered[key]); err != nil {
					return nil, 0, err
				}
				delete(buffered, key)
				break
			}
			outcomes++
			skips = append(skips, skip{
				Package: e.Package,
				Test:    e.Test,
				Reason:  reasonFrom(buffered[key]),
			})
			delete(buffered, key)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	return skips, outcomes, nil
}

// flush writes a buffered block, dropping the two markers the
// machine-readable stream adds that an ordinary run does not print.
//
// The stream is produced in verbose mode whether anybody asked or not,
// so every package emits a bare PASS or FAIL line above its summary.
// Passing them through would put two lines where a plain run puts one,
// on every package, which is the sort of small difference that makes a
// reader stop trusting that this wrapper is showing them the real thing.
func flush(w io.Writer, lines []string) error {
	for _, line := range lines {
		if trimmed := strings.TrimSpace(line); trimmed == "PASS" || trimmed == "FAIL" {
			continue
		}
		if _, err := io.WriteString(w, line); err != nil {
			return err
		}
	}
	return nil
}

// reasonFrom pulls the author's sentence out of a skipped row's output.
//
// The runner's own framing lines are dropped and what is left is what
// somebody wrote at the skip, with the file and line still attached —
// that pointer is half the value, since the next question a reader has
// is where the condition is decided.
func reasonFrom(lines []string) string {
	var kept []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
		case strings.HasPrefix(trimmed, "=== "):
		case strings.HasPrefix(trimmed, "--- "):
		default:
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, " ")
}
