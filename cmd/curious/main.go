// Command curious is the curious.pub CLI: pack an Astro project, upload
// it, and stream the build. See the repository README for what exists
// today.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/curiouspub/cli/internal/flow"
	"github.com/curiouspub/cli/internal/mcp"
	"github.com/curiouspub/cli/internal/ui"
)

// version, commit and date are overridden at build time via
// `-ldflags "-X main.version=... -X main.commit=... -X main.date=..."`.
// A plain `go build` leaves them at these values, so an unreleased
// binary says so honestly instead of guessing.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run dispatches to a subcommand and returns the process exit code. It
// takes its arguments and all three streams explicitly so dispatch is
// testable without a subprocess.
//
// STDIN ARRIVES HERE TOO, and it did not have to until `mcp` landed: one
// subcommand reads a protocol off it rather than answering a person, and
// a command that reached for os.Stdin itself would be one no test could
// drive without giving the test process a pipe of its own.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}

	switch args[0] {
	case "-h", "--help", "help":
		// Consistent with parseSubcommand below: help is a request, not a
		// mistake. It answered on stderr with exit 2, which made
		// `curious -h | less` show nothing.
		printUsage(stdout)
		return 0
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "deploy":
		return runDeploy(args[1:], stdout, stderr)
	case "mcp":
		return runMCP(args[1:], stdin, stdout, stderr)
	default:
		printUsage(stderr)
		return 2
	}
}

// printUsage writes the command surface to w. Deliberately small: this
// binary has three commands today, and anything not listed here is not
// yet public surface.
func printUsage(w io.Writer) {
	fmt.Fprint(w, `usage: curious <command> [arguments]

commands:
  deploy [dir]   pack an Astro project, upload it, and stream the build
  mcp            serve the Model Context Protocol on stdin and stdout
  version        print the version, commit and build date

Run 'curious <command> -h' for a command's own flags.
`)
}

const versionUsage = `usage: curious version

Print the version, commit, build date and Go runtime version.
Takes no arguments.
`

const deployUsage = `usage: curious deploy [dir]

Pack the Astro project in [dir] (default: the current directory),
upload it, and stream the build.
`

const mcpUsage = `usage: curious mcp

Serve the Model Context Protocol on stdin and stdout, so an agent can
deploy the same way a person does. Takes no arguments, and is meant to be
started by a client rather than run by hand: stdout carries the protocol,
so anything you type is read as a message.
`

// parseSubcommand parses a subcommand's arguments and reports whether the
// caller should stop. It returns (code, true) when the run is over — help
// was asked for, or the arguments were wrong — and (0, false) to carry on.
//
// Every diagnostic goes to STDERR and every wrong invocation exits 2,
// including a stray operand: a subcommand that silently ignores an
// argument it was given is telling the user it did something it did not.
// `curious version unexpected` used to print the version and exit 0.
//
// The flag package's own output is discarded so that this function owns
// every byte the user sees. Left at its default it wrote a bare error to
// stderr and no usage, and with output discarded it wrote nothing at all
// — a wrong flag exited 2 in complete silence, which is the worst of the
// three since the exit code is the only evidence and scripts are the only
// readers of it.
func parseSubcommand(fs *flag.FlagSet, args []string, maxOperands int, usage string, stdout, stderr io.Writer) (int, bool) {
	fs.SetOutput(io.Discard)
	err := fs.Parse(args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		// -h is a request, not a mistake: it answers on stdout and exits
		// 0, so `curious deploy -h | less` works.
		fmt.Fprint(stdout, usage)
		return 0, true
	case err != nil:
		fmt.Fprintf(stderr, "curious %s: %v\n\n", fs.Name(), err)
		fmt.Fprint(stderr, usage)
		return 2, true
	}
	if fs.NArg() > maxOperands {
		fmt.Fprintf(stderr, "curious %s: unexpected argument %q\n\n", fs.Name(), fs.Arg(maxOperands))
		fmt.Fprint(stderr, usage)
		return 2, true
	}
	return 0, false
}

// runVersion prints the version, commit, build date and Go runtime
// version. One flag.FlagSet per subcommand, even one with no flags of its
// own yet, so every subcommand is dispatched the same way.
func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	if code, done := parseSubcommand(fs, args, 0, versionUsage, stdout, stderr); done {
		return code
	}
	fmt.Fprintf(stdout, "curious %s (commit %s, built %s, %s)\n", version, commit, date, runtime.Version())
	return 0
}

// runDeploy runs the deploy sequence and returns the exit code it cost.
//
// THE SEQUENCE ITSELF IS NOT HERE, and that is the layout rule rather
// than a preference: this package is dispatch, and the order the steps
// run in is a product decision with reasons that want testing without a
// terminal, a real endpoint or a subprocess. What this function owns is
// the wiring — which directory, which terminal, and who removes the
// archive.
//
// THE TWO WRITERS PARSE THE FLAGS AND NOTHING ELSE, which is worth
// saying because every other subcommand here uses them throughout. The
// run's own output goes through the terminal type, which owns the split
// between narration and machine-readable output, the decision about
// whether a question may be asked at all, and the colour question — none
// of which a pair of buffers can answer, and all of which would have to
// be answered twice if this wrote to them directly.
//
// ONE OPTIONAL OPERAND, THE DIRECTORY. It is a positional argument
// rather than a flag because it is the thing being deployed; empty means
// the directory the user is standing in.
//
// Release is deferred rather than called at the end: it removes the
// archive on the way out of every ordinary path, and it is safe on the
// nil hand-off a refused run returns. The interrupt path cannot use it —
// nothing deferred runs once a signal has killed the process — which is
// why the handler is handed its own copy of the same tidy-up.
func runDeploy(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	if code, done := parseSubcommand(fs, args, 1, deployUsage, stdout, stderr); done {
		return code
	}

	u := ui.New()
	handoff, err := flow.Deploy(context.Background(), flow.DeployDeps{
		Dir:        fs.Arg(0),
		Prompt:     u,
		Interrupts: u.Interrupts,
	})
	defer handoff.Release()
	return u.ExitCode(err)
}

// runMCP serves the Model Context Protocol on this process's own pipes
// until the client closes them.
//
// STDOUT IS THE PROTOCOL. Every diagnostic this command produces goes to
// stderr, which is why the server is handed the two writers separately
// rather than being left to pick one: a stray byte on stdout is a
// message the client cannot parse, and the symptom a user reports is a
// client that disconnected without saying why.
//
// The server is given this binary's own version rather than a literal,
// so an unreleased build introduces itself to a client as "dev" for the
// same reason `curious version` does.
func runMCP(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	if code, done := parseSubcommand(fs, args, 0, mcpUsage, stdout, stderr); done {
		return code
	}

	if err := mcp.New("curious", version).Serve(stdin, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "curious mcp: %v\n", err)
		return 1
	}
	return 0
}
