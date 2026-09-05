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
