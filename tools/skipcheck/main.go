// Command skipcheck runs this repository's tests and refuses to let a
// skipped row pass unnoticed.
//
// THE PROBLEM IT SOLVES IS THAT A GREEN RUN LOOKS THE SAME EITHER WAY.
// The suite runs non-verbosely, so a skipped test prints nothing at all
// — and several rows here skip on conditions that are legitimate on one
// platform and a broken environment on another. A row needing a program
// that is not installed, a filesystem feature the account cannot use, a
// permission the runner does not have: each of them turns into silence,
// and silence is what a passing run is made of.
//
// Three properties, and the third is the one that makes the first two
// worth having:
//
//   - every skip is printed, with the reason its author wrote;
//   - a manifest records which skips are legitimate on which platform;
//   - a skip nobody declared FAILS the run.
//
// It wraps the test command rather than replacing it: the same
// arguments, the same exit code, and output that reads like the ordinary
// non-verbose run with a skip section added. Making the whole suite
// verbose would surface the same three lines inside ten thousand, which
// is a way of hiding them that also annoys everybody.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
)

func main() {
	manifestPath := flag.String("manifest", "scripts/expected-skips.txt",
		"the file recording which skips are legitimate on which platform")
	flag.Parse()

	if err := run(*manifestPath, flag.Args(), os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(manifestPath string, testArgs []string, stdout, stderr io.Writer) error {
	file, err := os.Open(manifestPath)
	if err != nil {
		return fmt.Errorf("reading the expected-skips manifest: %w", err)
	}
	defer file.Close()

	rules, err := parseManifest(file)
	if err != nil {
		return fmt.Errorf("%s: %w", manifestPath, err)
	}

	args := append([]string{"test", "-json"}, testArgs...)
	cmd := exec.Command("go", args...)
	cmd.Stderr = stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	skips, outcomes, collectErr := collect(pipe, stdout)
	waitErr := cmd.Wait()
	if collectErr != nil {
		return collectErr
	}

	// A RUN THAT REACHED NO TESTS PROVES NOTHING, and it reports the same
	// empty skip list as a clean one. It is only a failure when the
	// command itself succeeded: a build error is already an error, and
	// saying "no tests ran" on top of it buries the message that matters.
	if outcomes == 0 && waitErr == nil {
		return errors.New("no tests ran at all, so nothing here is evidence about skips")
	}

	if undeclared := report(stdout, rules, runtime.GOOS, skips); undeclared > 0 {
		return fmt.Errorf("%d skipped row(s) nobody declared", undeclared)
	}
	return waitErr
}
