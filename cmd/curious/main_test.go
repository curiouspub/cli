package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// emptyStdin is the input a subcommand that does not read one gets. It
// is a function rather than a shared value because a reader is stateful:
// two rows sharing one would have the second find it already at its end,
// which is only invisible while nothing reads it.
func emptyStdin() *strings.Reader { return strings.NewReader("") }

func TestRunVersionPlainBuild(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"version"}, emptyStdin(), &out, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "dev") {
		t.Errorf("output %q does not contain the unreleased-build version %q", out.String(), "dev")
	}
}

func TestRunVersionOverridden(t *testing.T) {
	orig := version
	version = "1.2.3"
	defer func() { version = orig }()

	var out bytes.Buffer
	run([]string{"version"}, emptyStdin(), &out, &bytes.Buffer{})
	if !strings.Contains(out.String(), "1.2.3") {
		t.Errorf("output %q does not contain the overridden version %q", out.String(), "1.2.3")
	}
}

// TestDeployIsDispatchedToTheRealSequence drives the subcommand the way
// a person does and is the row that proves dispatch reaches the sequence
// rather than reaching a name. It is the analogue of the server row at
// the bottom of this file, and it exists for the same reason: every
// other `deploy` row here exits during flag parsing, so a subcommand
// that parsed correctly and then did nothing would satisfy all of them.
//
// IT NAMES A DIRECTORY THAT IS NOT THERE, deliberately. That is the
// earliest stop the sequence has — before the config file is read and
// long before anything is dialled — so this row needs no endpoint, no
// stored login and no fixture, and it still cannot pass without the real
// sequence having run.
//
// The process's own stderr is redirected, because the run's output goes
// through the terminal type rather than through the writers dispatch was
// handed, and a row that ignored that would print its diagnostic into
// the suite's output.
//
// REQUIRED MUTATION, run 2026-09-08: return 0 from runDeploy without
// calling flow.Deploy. This reds; the flag rows above stay green.
func TestDeployIsDispatchedToTheRealSequence(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-project")

	var out bytes.Buffer
	text, code := withCapturedStderr(t, func() int {
		return run([]string{"deploy", missing}, emptyStdin(), &out, io.Discard)
	})

	if code != 1 {
		t.Errorf("exit code = %d, want 1 for a directory that is not there", code)
	}
	if !strings.Contains(text, missing) {
		t.Errorf("the run did not name the path it could not find:\n%s", text)
	}
	if out.Len() != 0 {
		t.Errorf("a failure leaked to stdout: %q", out.String())
	}
}

// withCapturedStderr runs fn with the process's stderr pointed at a file
// and returns what was written to it, plus fn's own result.
//
// Redirected rather than captured, because the terminal type writes to
// the streams it was built from and this is the only portable way to
// hand it one a test can read back.
func withCapturedStderr(t *testing.T, fn func() int) (string, int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stderr")
	sink, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating the capture file: %v", err)
	}

	real := os.Stderr
	os.Stderr = sink
	code := fn()
	os.Stderr = real

	if err := sink.Close(); err != nil {
		t.Fatalf("closing the capture file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the capture file: %v", err)
	}
	return string(data), code
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"frobnicate"}, emptyStdin(), &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if errOut.Len() == 0 {
		t.Fatal("usage was not written to stderr")
	}
	if out.Len() != 0 {
		t.Errorf("usage leaked to stdout: %q", out.String())
	}
	// THIS ROW WAS FLIPPED DELIBERATELY. It asserted that the usage text
	// does NOT mention mcp, which was correct for exactly as long as the
	// command did not exist; the change that built the command is the
	// change that owed this line. Named here rather than quietly edited,
	// because a row that gets in the way is otherwise a row somebody
	// deletes.
	//
	// REQUIRED MUTATION, run 2026-09-08: remove the mcp line from
	// printUsage. Reds here.
	usage := strings.ToLower(errOut.String())
	if !strings.Contains(usage, "mcp") {
		t.Errorf("usage text does not mention mcp, which is public surface now: %q", errOut.String())
	}
	// The other half, unchanged: the surface is what EXISTS, and a usage
	// text advertising a command that is not built yet is a promise with
	// no date on it.
	//
	// REQUIRED MUTATION, run 2026-09-08: add a login line to printUsage.
	// Reds here.
	for _, absent := range []string{"login", "whoami"} {
		if strings.Contains(usage, absent) {
			t.Errorf("usage text mentions %s, which is not public surface yet: %q", absent, errOut.String())
		}
	}
}

func TestRunNoArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(nil, emptyStdin(), &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if errOut.Len() == 0 {
		t.Fatal("usage was not written to stderr")
	}
}

// TestArgumentErrorsAreAnswered is the regression for a dispatch that
// used to swallow wrong input: `version unexpected` printed the version
// and exited 0, and `version -nope` exited 2 with both streams empty.
// The fix is right today and nothing kept it right — the next edit to
// parseSubcommand could restore the silence with the suite green.
func TestArgumentErrorsAreAnswered(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		wantCode   int
		wantOut    bool // something on stdout
		wantErr    bool // something on stderr
		errMustSay string
	}{
		{"version stray operand", []string{"version", "unexpected"}, 2, false, true, "unexpected"},
		{"version unknown flag", []string{"version", "-nope"}, 2, false, true, "-nope"},
		{"deploy second operand", []string{"deploy", "a", "b"}, 2, false, true, `"b"`},
		{"deploy unknown flag", []string{"deploy", "-nope"}, 2, false, true, "-nope"},
		{"mcp stray operand", []string{"mcp", "unexpected"}, 2, false, true, "unexpected"},
		{"mcp unknown flag", []string{"mcp", "-nope"}, 2, false, true, "-nope"},
		{"version help", []string{"version", "-h"}, 0, true, false, ""},
		{"deploy help", []string{"deploy", "-h"}, 0, true, false, ""},
		{"mcp help", []string{"mcp", "-h"}, 0, true, false, ""},
		{"top-level help", []string{"-h"}, 0, true, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tc.args, emptyStdin(), &stdout, &stderr)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d", code, tc.wantCode)
			}
			if got := stdout.Len() > 0; got != tc.wantOut {
				t.Errorf("stdout non-empty = %v, want %v (%q)", got, tc.wantOut, stdout.String())
			}
			if got := stderr.Len() > 0; got != tc.wantErr {
				t.Errorf("stderr non-empty = %v, want %v (%q)", got, tc.wantErr, stderr.String())
			}
			// The reason must NAME the thing that was wrong. A bare exit
			// code is what this test exists to prevent coming back.
			if tc.errMustSay != "" && !strings.Contains(stderr.String(), tc.errMustSay) {
				t.Errorf("stderr does not name %q: %q", tc.errMustSay, stderr.String())
			}
		})
	}
}

// TestMCPIsDispatchedToARealServer drives the subcommand the way a
// client does — a handshake in on stdin, a reply out on stdout — and is
// the row that proves the dispatch reaches the server rather than
// reaching a name.
//
// IT IS DELIBERATELY NOT A FLAG TEST. The other rows about `mcp` all
// exit before a byte is read, so a subcommand that parsed its flags
// correctly and then did nothing at all would satisfy every one of them.
// This is the row that would notice.
//
// REQUIRED MUTATION, run 2026-09-08: in runMCP, drop the Serve call and
// return 0. This row reds; the flag rows above stay GREEN, which is the
// asymmetry that shows what each of them is measuring.
//
// A PREDICTION IN THIS COMMENT WAS WRONG AND IS RECORDED AS WRONG. The
// mutation first written here was "delete the mcp case from run's
// switch", claimed to red this row alone. Run, it reds the flag rows
// too — dispatch falls through to the unknown-command branch, which
// prints the generic usage rather than naming the bad operand, so the
// table's own "the reason must name the thing that was wrong" assertion
// fires. The claim was plausible and untrue, and the only reason it is
// not still written above is that the mutation was actually run.
func TestMCPIsDispatchedToARealServer(t *testing.T) {
	const handshake = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`

	var stdout, stderr bytes.Buffer
	code := run([]string{"mcp"}, strings.NewReader(handshake+"\n"), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 for a client that closed the pipe cleanly (stderr: %q)", code, stderr.String())
	}

	var reply struct {
		JSONRPC string `json:"jsonrpc"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &reply); err != nil {
		t.Fatalf("stdout %q is not the single JSON reply a handshake produces: %v", stdout.String(), err)
	}
	if reply.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want \"2.0\"", reply.JSONRPC)
	}
	if reply.Result.ProtocolVersion == "" {
		t.Error("the handshake answered with no protocol revision")
	}

	// THE BINARY INTRODUCES ITSELF AS THE BINARY. The server is handed
	// this program's own version rather than a literal, so an unreleased
	// build says "dev" to a client for the same reason `curious version`
	// says it to a person.
	//
	// REQUIRED MUTATION, run 2026-09-08: pass a literal version string to
	// mcp.New instead of the version variable. Reds here.
	if reply.Result.ServerInfo.Name != "curious" {
		t.Errorf("serverInfo.name = %q, want %q", reply.Result.ServerInfo.Name, "curious")
	}
	if reply.Result.ServerInfo.Version != version {
		t.Errorf("serverInfo.version = %q, want this binary's own version %q",
			reply.Result.ServerInfo.Version, version)
	}
}
