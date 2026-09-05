package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersionPlainBuild(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"version"}, &out, &bytes.Buffer{})
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
	run([]string{"version"}, &out, &bytes.Buffer{})
	if !strings.Contains(out.String(), "1.2.3") {
		t.Errorf("output %q does not contain the overridden version %q", out.String(), "1.2.3")
	}
}

func TestRunDeployStub(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"deploy"}, &out, &errOut)
	if code == 0 {
		t.Fatal("exit code = 0, want non-zero for a stub that isn't built yet")
	}
	if !strings.Contains(errOut.String(), "not implemented") {
		t.Errorf("stderr %q does not say the command is not implemented", errOut.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"frobnicate"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if errOut.Len() == 0 {
		t.Fatal("usage was not written to stderr")
	}
	if out.Len() != 0 {
		t.Errorf("usage leaked to stdout: %q", out.String())
	}
	if strings.Contains(strings.ToLower(errOut.String()), "mcp") {
		t.Error("usage text mentions mcp, which is not public surface yet")
	}
}

func TestRunNoArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(nil, &out, &errOut)
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
		{"version help", []string{"version", "-h"}, 0, true, false, ""},
		{"deploy help", []string{"deploy", "-h"}, 0, true, false, ""},
		{"top-level help", []string{"-h"}, 0, true, false, ""},
		{"deploy one operand is legal", []string{"deploy", "dir"}, 1, false, true, "not implemented"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tc.args, &stdout, &stderr)
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
