// Command curious is the curious.pub CLI: pack an Astro project, upload
// it, and stream the build. See the repository README for what exists
// today.
package main

import (
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
	case "version":
		return runVersion(args[1:], stdout)
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

// runVersion prints the version, commit, build date and Go runtime
// version. One flag.FlagSet per subcommand, even one with no flags of its
// own yet, so every subcommand is dispatched the same way.
func runVersion(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmt.Fprintf(stdout, "curious %s (commit %s, built %s, %s)\n", version, commit, date, runtime.Version())
	return 0
}

// runDeploy is a stub: the actual deploy sequence — pre-flight, pack,
// upload, stream — lands in a later change. Until then this exits
// non-zero so nothing downstream can mistake a stub for a real deploy.
func runDeploy(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("deploy", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmt.Fprintln(stderr, "curious deploy: not implemented yet")
	return 1
}
