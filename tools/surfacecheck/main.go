// Command surfacecheck reads the published surfaces that a scan of file
// CONTENTS cannot see, and fails when one of them says something this
// repository does not publish.
//
// A commit message, a branch name, a tag and its message, and a pull
// request's title and body are all exactly as world-readable as a source
// file, and the rule about what may be written in one is the same rule.
// It was enforced by attention until attention failed twice in this
// repository — once by the same hand that had written the note describing
// the hole. A rule with no mechanism gets followed until the moment
// somebody is busy, and that is the moment a release is cut.
//
// It reads the two rule files the content scan already owns and keeps no
// copy of either, so deleting a line from one measurably changes what
// this catches. It reads them at BOTH ENDS of the range, which is the
// part that is not obvious and is explained where it happens.
//
// WHAT IT STILL DOES NOT REACH, stated because implying coverage is worse
// than having none:
//
//   - A release note, which is authored after the tag and is not a git
//     object at all.
//   - A message hand-edited in the merge button at the moment of merging.
//     The push check on the default branch sees it AFTER publication,
//     which is detection plus repair rather than enforcement, and calling
//     it enforcement would be the claim this check exists because
//     somebody made once already.
//
// Both are hand-checked, and a narrowed hand-check that names its scope
// is a different artefact from a blanket one nobody performs.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// What each exit code means. They are three because the third answer is
// real: "I could not look" is not "I looked and found nothing", and a
// check that collapses them reports a shallow checkout as a clean range.
const (
	exitClean        = 0
	exitFindings     = 1
	exitUndetermined = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("surfacecheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dir := flags.String("repo", ".", "the checkout to read")
	base := flags.String("base", "", "the revision the range starts after")
	head := flags.String("head", "", "the revision the range ends at")
	branch := flags.String("branch", "", "a branch or tag name to check alongside the range")
	if err := flags.Parse(args); err != nil {
		return exitUndetermined
	}

	r := repo{dir: *dir}
	req, err := buildRequest(r, getenv, *base, *head, *branch)
	if err != nil {
		fmt.Fprintf(stderr, "the range could not be resolved: %v\n", err)
		fmt.Fprintln(stderr, "This is not a pass. A range that resolves to nothing examines "+
			"nothing, and a run that examined nothing is green for the same reason a clean "+
			"one is.")
		return exitUndetermined
	}
	if req.Skip != "" {
		fmt.Fprintf(stdout, "nothing to check: %s\n", req.Skip)
		return exitClean
	}

	baseSHA, err := r.resolve(req.Base)
	if err != nil {
		return unresolvable(stderr, "base", req.Base, err)
	}
	headSHA, err := r.resolve(req.Head)
	if err != nil {
		return unresolvable(stderr, "head", req.Head, err)
	}

	rules, narrowings, err := LoadRules(r.atRevision(baseSHA), r.workingTree())
	if err != nil {
		fmt.Fprintf(stderr, "the vocabulary could not be assembled: %v\n", err)
		return exitUndetermined
	}

	// FIRST, BEFORE ANY RANGE IS EXAMINED.
	if err := selfTest(r, rules, stdout); err != nil {
		fmt.Fprintf(stderr, "the self-test failed, so nothing below it would have meant "+
			"anything: %v\n", err)
		return exitUndetermined
	}

	findings, examined, err := examine(r, rules, req, func() ([]string, error) {
		return r.commits(baseSHA, headSHA)
	})
	if err != nil {
		fmt.Fprintf(stderr, "the range could not be read: %v\n", err)
		// WHAT WAS READ BEFORE THAT IS STILL TRUE. A branch name carrying
		// a citation does not stop carrying it because the commits behind
		// it could not be walked, and throwing it away leaves the author
		// with an undetermined run and no idea a surface was found. The
		// run stays undetermined either way: what could not be read may
		// carry more.
		describe(stdout, findings, narrowings)
		return exitUndetermined
	}
	fmt.Fprintf(stdout, "examined %d published surface(s) over %s..%s\n",
		examined, short(baseSHA), short(headSHA))

	// THE FLOOR. Both endpoints resolved, so an empty commit list is a
	// legitimate answer — a branch created at the default tip has nothing
	// new in it — but a run that read no surface at all read nothing, and
	// that must never be spelled the same way as a clean run.
	if examined == 0 {
		fmt.Fprintln(stderr, "no published surface was examined at all, so this run is "+
			"evidence about nothing. A range with no commits is legitimate; a range with "+
			"no commits AND no name is the plumbing having gone quiet.")
		return exitUndetermined
	}

	return report(stdout, findings, narrowings)
}

// buildRequest takes the range from explicit flags when they are given,
// and from the workflow event otherwise.
//
// THE FLAGS ARE NOT A CONVENIENCE. A check that exists only inside a
// workflow file is a check nobody can run before pushing, and this one is
// wanted most on the machine that wrote the message.
func buildRequest(r repo, getenv func(string) string, base, head, branch string) (request, error) {
	if head != "" || base != "" {
		if head == "" || base == "" {
			return request{}, fmt.Errorf("a range needs both ends: -base %q and -head %q", base, head)
		}
		req := request{Base: base, Head: head}
		if branch != "" {
			req.Named = append(req.Named, named{Subject: "branch name", Text: branch})
		}
		return req, nil
	}

	eventName, eventPath := getenv("GITHUB_EVENT_NAME"), getenv("GITHUB_EVENT_PATH")
	if eventName == "" || eventPath == "" {
		return request{}, fmt.Errorf("no range was given and no workflow event was found; " +
			"pass -base and -head to check a range on this machine")
	}
	raw, err := os.ReadFile(eventPath)
	if err != nil {
		return request{}, fmt.Errorf("reading the %s event: %w", eventName, err)
	}
	return requestFromEvent(r, eventName, getenv("GITHUB_REF_NAME"), raw)
}

// unresolvable is the one message for an endpoint this run could not turn
// into a commit, so both ends say the same thing about the same failure.
//
// THE ADVICE IS NOT UNCONDITIONAL, because there are two failures here
// and only one of them has that repair. "This checkout does not carry the
// commit" is fixed by fetching; "git would not answer" is not, and
// telling somebody to fetch more history when the trouble is that there
// is no repository to fetch into sends them to the one place the answer
// is not. Git's own words are printed either way; what changes is whether
// this check adds a diagnosis on top of them.
func unresolvable(stderr io.Writer, which, rev string, err error) int {
	fmt.Fprintf(stderr, "the %s of the range could not be resolved: %v\n", which, err)
	if errors.Is(err, errNotInHistory) {
		fmt.Fprintln(stderr, "A checkout without the commits at the ends of its own range "+
			"produces an empty range, and an empty range examines nothing. Fetch the full "+
			"history and run again.")
		return exitUndetermined
	}
	fmt.Fprintln(stderr, "That is git declining to answer rather than answering no, so this "+
		"says nothing about whether the range is here. Read its words above: fetching more "+
		"history will not change them.")
	return exitUndetermined
}

// commitRange is where the commits a run examines come from.
//
// It is an enumeration to run rather than a pair of revisions so that one
// commit and a whole range reach the scanning below by the same road. The
// self-test hands it a single historical commit; a workflow hands it the
// range being published. Neither gets a path of its own, which is what
// makes the first one evidence about the second.
type commitRange func() ([]string, error)

// examine reads every published surface the request names and returns
// what it found, with the number of surfaces actually read.
//
// The count is separate from the findings because zero of each means two
// different things, and only one of them is good news.
//
// A FAILURE PART WAY THROUGH STILL RETURNS WHAT WAS ALREADY READ. The
// named surfaces are scanned before the range is walked, so a branch name
// carrying a citation is a fact this function is holding by the time git
// declines to walk anything — and discarding it on the way out tells the
// author nothing about the one surface that WAS read. The run is still
// undetermined, because what could not be read may carry more; that is
// the caller's decision, and it does not need this evidence destroyed to
// make it.
//
// THE COMMITS ARRIVE AS AN ENUMERATION TO RUN, not as two revisions,
// because the self-test goes through this same function over a single
// commit. Sharing the path is the point: the subject a finding is named
// by, the message reading and the listing are then all proven by the
// control that runs before any range is examined, instead of being
// reached for the first time by the measurement itself.
func examine(r repo, rules Rules, req request, commits commitRange) ([]Finding, int, error) {
	var findings []Finding
	examined := 0

	for _, n := range req.Named {
		if strings.TrimSpace(n.Text) == "" {
			// An empty pull request body is a real and ordinary thing. It
			// is not a surface that was examined, though, and counting it
			// would let the floor above be satisfied by nothing.
			continue
		}
		examined++
		findings = append(findings, rules.Scan(n.Subject, n.Text)...)
	}

	shas, err := commits()
	if err != nil {
		return findings, examined, err
	}
	for _, sha := range shas {
		message, err := r.message(sha)
		if err != nil {
			return findings, examined, err
		}
		examined++
		findings = append(findings, rules.Scan("commit "+short(sha), message)...)
	}
	return findings, examined, nil
}

// report prints what was found and decides the exit code.
func report(stdout io.Writer, findings []Finding, narrowings []Narrowing) int {
	if describe(stdout, findings, narrowings) {
		return exitFindings
	}
	fmt.Fprintln(stdout, "clean.")
	return exitClean
}

// describe prints everything the run has to say about what it read, and
// reports whether there was anything. It is separate from the exit code
// above because a run that could not finish still has whatever it read
// before that to hand over — and the code for THAT run is decided by what
// it could not do, not by what it found.
func describe(stdout io.Writer, findings []Finding, narrowings []Narrowing) bool {
	for _, n := range narrowings {
		fmt.Fprintf(stdout, "pattern set narrowed: %s no longer declares %s\n", n.Path, n.Line)
	}
	if len(narrowings) > 0 {
		fmt.Fprintln(stdout, "\nA line removed from a rule file is still in force for the "+
			"range that removes it, so nothing here has been let through. What this says is "+
			"that the vocabulary is smaller from now on, which is a decision somebody has to "+
			"take on purpose — land it on its own, with nothing else in the range, so the "+
			"diff shows exactly what is being given up.")
		// SAID OUT LOUD, because it is a consequence of the design that
		// reads like a mistake. A narrowing is a finding and findings fail,
		// so the change that retires a rule is merged over a red check.
		// That was implicit in a rule and left the person at the merge
		// button to work out whether they were doing something wrong.
		fmt.Fprintln(stdout, "\nThis check will be red on that change, and that is the "+
			"designed path rather than a thing to work around. There is no way to retire a "+
			"rule without the run that retires it reporting the retirement; merging it over "+
			"this red is somebody deciding in the open, which is the whole reason a narrowing "+
			"fails at all.")
		// AND THE OTHER THING THE LINE MAY HAVE BEEN HOLDING UP. This
		// check proves itself before every run against a commit in this
		// repository's history that a rule must catch. Retire that rule
		// and every later run fails undetermined, saying only that the
		// commit reported nothing — with nothing anywhere connecting it to
		// the change that caused it. The moment to say so is here, to the
		// person who can still do something about it.
		fmt.Fprintln(stdout, "\nCheck what else the line was holding up before you land it. "+
			"This check proves itself against a commit in this repository's history that a "+
			"rule must catch, and if the line above is that rule, every run after this one "+
			"fails saying that commit reported nothing. Retire the line and replace the "+
			"recorded commit in the same change.")
	}

	for _, f := range findings {
		fmt.Fprintln(stdout, f.String())
	}
	if len(findings) > 0 {
		fmt.Fprintln(stdout, "\nThese surfaces are as world-readable as the files beside "+
			"them. State the conclusion and the reasoning without the private artefact "+
			"either came from; a published message cannot be unpublished by a later one.")
	}

	return len(findings) > 0 || len(narrowings) > 0
}

// String renders one finding for whoever has to fix it, and NEVER the
// text that tripped it.
//
// THE MATCH IS THE THING THIS CHECK EXISTS TO KEEP OUT OF PUBLISHED
// TEXT — a private citation, or a provider name — and this report goes
// into a run's log. On a pull request from a fork that log is more public
// than the message being read, so a report carrying the match publishes
// what it caught, to a wider audience than the surface it was defending.
// The refusal that is written by the one piece of code guaranteed to be
// holding the thing it refuses is the first place a leak gets out.
//
// The self-test already refuses exactly this, a file away: it discards
// its findings and prints a sha and a count, because rendering one would
// write the identifier into a log. The hazard was understood and guarded
// in one place; this is the other place agreeing with it.
//
// WHAT IS LEFT IS ENOUGH TO ACT ON, which is the other half and not a
// smaller one — a report redacted into uselessness is a leak closed by
// removing the check. The surface, the line and the rule name a single
// line of the author's own text, and the rule is either a line of this
// repository's own published manifest or the name of the vendor check.
// Neither discloses anything the reader could not already read here.
//
// Match stays on the struct: it is what the counting is done on, and a
// row can assert on it in memory without any of it reaching a page.
func (f Finding) String() string {
	where := f.Subject
	if f.Line > 0 {
		where = fmt.Sprintf("%s, line %d", f.Subject, f.Line)
	}
	if f.Rule == vendorVocabulary {
		return where + " names infrastructure"
	}
	return fmt.Sprintf("%s matches %s", where, f.Rule)
}
