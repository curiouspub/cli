package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAPanickingToolIsContainedAndTheSameSessionKeepsServing is the
// obligation this host inherits and no other host in this program has.
//
// A tool handler is code this package does not own, and everywhere else
// in this binary a panicking callback costs ONE RUN — the process was
// going to exit anyway. Here the process outlives the call: the client
// started it once and will send every later request down the same pipe,
// so an uncontained panic costs the session and everything the agent was
// part-way through.
//
// THE SECOND HALF IS THE WHOLE CLAIM. "The call returned an error" is
// satisfied by a server that answered and then died, which is exactly
// the failure being prevented, so the row sends a second request down
// the SAME pipe pair afterwards and requires a normal answer. The pipes
// are real rather than a string and a buffer, because the second request
// is written only after the first reply has been read — an ordering a
// string reader cannot express, since it hands over the whole input
// before the server starts.
//
// REQUIRED MUTATION, run 2026-09-08: delete the deferred recover from
// invoke. The panic escapes, the goroutine running Serve takes the
// process down with it, and this row reds — as does the rest of the
// suite, which is what an uncontained panic does.
func TestAPanickingToolIsContainedAndTheSameSessionKeepsServing(t *testing.T) {
	s := testServer()
	s.Register(Tool{
		Name: "boom",
		Handler: func(json.RawMessage) Result {
			panic("a handler panicking while holding a-value-that-must-not-reach-the-client")
		},
	})
	s.Register(echoTool("echo"))

	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating the client-to-server pipe: %v", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating the server-to-client pipe: %v", err)
	}
	defer func() { inR.Close(); outR.Close() }()

	var logw bytes.Buffer
	served := make(chan error, 1)
	go func() {
		err := s.Serve(inR, outW, &logw)
		outW.Close()
		served <- err
	}()
	replies := bufio.NewReader(outR)

	read := func(what string) reply {
		t.Helper()
		var line string
		mustWithin(t, serveDeadline, "reading the reply to "+what, func() {
			var readErr error
			line, readErr = replies.ReadString('\n')
			if readErr != nil {
				t.Errorf("reading the reply to %s: %v", what, readErr)
			}
		})
		var r reply
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("the reply to %s is not valid JSON (%v): %q", what, err, line)
		}
		return r
	}

	if _, err := fmt.Fprintf(inW, "%s\n", callMessage("1", "boom", `{}`)); err != nil {
		t.Fatalf("writing the call that panics: %v", err)
	}
	contained := read("the call that panics")

	// A STRUCTURED ERROR, not a protocol error. The request was
	// perfectly well formed and the tool was real; what failed was the
	// tool, which is the case the protocol has a result flag for.
	if contained.Error != nil {
		t.Fatalf("a panicking handler was reported as a protocol fault: %+v", contained.Error)
	}
	got := resultOf(t, contained)
	if !got.IsError {
		t.Error("a panicking handler's call came back unmarked as an error")
	}
	if len(got.Content) != 1 || !strings.Contains(got.Content[0].Text, "boom") {
		t.Errorf("the failure does not name the tool that failed: %+v", got.Content)
	}

	// THE PANIC VALUE DOES NOT REACH THE CLIENT. A panic carries
	// whatever the panicking code was holding, which in this program can
	// be a token or an upload URL, and the client's stream is read by a
	// model and then usually pasted into a transcript. The operator gets
	// the value; the client gets the name of the tool.
	//
	// REQUIRED MUTATION, run 2026-09-08: in invoke, interpolate the
	// recovered value into the ErrorResult. Reds here.
	if strings.Contains(got.Content[0].Text, "a-value-that-must-not-reach-the-client") {
		t.Errorf("the panic value reached the client: %q", got.Content[0].Text)
	}
	if !strings.Contains(logw.String(), "a-value-that-must-not-reach-the-client") {
		t.Errorf("the panic value did not reach the log either, so it is simply lost: %q", logw.String())
	}
	// A stack, on the log. A panic with no stack is the hardest kind of
	// failure to act on, and this is the one place in the program where
	// the panic is swallowed rather than printed by the runtime.
	if !strings.Contains(logw.String(), "runtime/debug.Stack") && !strings.Contains(logw.String(), "goroutine ") {
		t.Errorf("the log carries no stack for the contained panic: %q", logw.String())
	}

	// THE SESSION SURVIVED. Same pipes, same server, written only now.
	if _, err := fmt.Fprintf(inW, "%s\n", callMessage("2", "echo", `{"say":"still here"}`)); err != nil {
		t.Fatalf("writing the call after the panic: %v", err)
	}
	after := read("the call after the panic")
	survivor := resultOf(t, after)
	if survivor.IsError || len(survivor.Content) != 1 || !strings.Contains(survivor.Content[0].Text, "still here") {
		t.Errorf("the call after the panic was not answered normally: %+v", survivor)
	}

	inW.Close()
	mustWithin(t, serveDeadline, "Serve returning at end of input", func() {
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
}

// recoverSite is one call to recover() found in this package's shipped
// source.
type recoverSite struct {
	file string
	fn   string
}

// TestRecoverAppearsOnlyAroundTheToolHandler is the structural half of
// the containment, and it exists because the behavioural row above
// cannot see the difference between a recover in the right place and one
// three functions out.
//
// THE POSITION IS THE RULE. A panic in a HANDLER is somebody else's
// programming error arriving in a process that has to survive it. A
// panic in the transport loop or the dispatcher is a fault in THIS
// package, and turning it into a tidy structured error would hide a real
// defect behind the mechanism built to survive somebody else's — the
// symptom being a server that answers every request with the same polite
// failure and looks, from outside, like a server that is working.
//
// It is guarded rather than merely written down because the repair is
// tempting and cheap: a server that crashed once in front of a user
// invites exactly one edit, moving the defer one function outwards, and
// nothing else in this suite would notice.
//
// REQUIRED MUTATION, run 2026-09-08, both directions:
//
//   - wrap Serve's scan loop in a deferred recover. Two sites, reds.
//   - delete the recover from invoke. Zero sites, reds — which is the
//     half that keeps this guard from passing over a package whose
//     containment was removed entirely.
func TestRecoverAppearsOnlyAroundTheToolHandler(t *testing.T) {
	const (
		wantFile = "tool.go"
		wantFunc = "invoke"
	)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	fset := token.NewFileSet()
	var scanned int
	var sites []recoverSite
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		scanned++

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// Inspecting the BODY rather than the file catches the call
			// wherever it sits inside the function, including in the
			// deferred closure it has to live in to work at all.
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "recover" {
					sites = append(sites, recoverSite{file: name, fn: fn.Name.Name})
				}
				return true
			})
		}
	}

	// The guard fails loudly rather than passing over an empty set: a
	// walk that found no files would otherwise report "exactly the
	// expected number of recovers" for the wrong reason.
	if scanned == 0 {
		t.Fatal("no shipped .go files were scanned, so this guard measured nothing")
	}

	if len(sites) != 1 {
		t.Fatalf("found %d calls to recover in this package's shipped source, want exactly 1: %+v\n"+
			"The containment belongs around the tool handler and nowhere else. A recover around the "+
			"transport loop or the dispatcher converts a fault in THIS package into a structured "+
			"error, which hides the defect behind the mechanism built to survive somebody else's.",
			len(sites), sites)
	}
	if sites[0].file != wantFile || sites[0].fn != wantFunc {
		t.Errorf("recover is called in %s (%s), want %s (%s)",
			sites[0].fn, sites[0].file, wantFunc, wantFile)
	}
}
