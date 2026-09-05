// Command curious is the curious.pub CLI: pack an Astro project, upload
// it, and stream the build. See the repository README for what exists
// today.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
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
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches to a subcommand and returns the process exit code. It
// takes its arguments and output streams explicitly so dispatch is
// testable without a subprocess.
func run(args []string, stdout, stderr io.Writer) int {
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
	default:
		printUsage(stderr)
		return 2
	}
}

// printUsage writes the command surface to w. Deliberately small: this
// binary has two commands today, and anything not listed here is not yet
// public surface.
func printUsage(w io.Writer) {
	fmt.Fprint(w, `usage: curious <command> [arguments]

commands:
  deploy [dir]   pack an Astro project, upload it, and stream the build
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

// runDeploy is a stub: the actual deploy sequence — pre-flight, pack,
// upload, stream — lands in a later change. Until then this exits
// non-zero so nothing downstream can mistake a stub for a real deploy.
//
// It accepts one optional operand, the directory, so the stub already has
// the argument shape the real command will.
func runDeploy(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	if code, done := parseSubcommand(fs, args, 1, deployUsage, stdout, stderr); done {
		return code
	}
	fmt.Fprintln(stderr, "curious deploy: not implemented yet")
	return 1
}
