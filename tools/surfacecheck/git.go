package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// repo is a checkout this check reads. Every question it asks git is a
// read; nothing here writes anything anywhere.
type repo struct {
	dir string
}

// errNotInHistory is what a revision this checkout does not carry comes
// back as. It exists so the caller can say "the range could not be
// resolved" rather than "the range was empty", which are opposite
// findings that look identical from a distance.
var errNotInHistory = errors.New("not in this checkout's history")

// gitError is one git command that did not succeed, kept as git left it.
//
// THE READING IS THE PART THAT CAN BE WRONG, which is why the exit status
// and git's own words are carried rather than collapsed into a sentence
// here. "There is no such revision" and "there is no git here" arrive at
// the same call site, and a check that maps both to "not in this
// checkout's history" answers a missing git binary with "fetch the full
// history and run again" — advice that fixes nothing, given to somebody
// who has been told the diagnosis and cannot see it is the wrong one.
type gitError struct {
	Args   []string
	Code   int // the exit status, or -1 when git never ran at all
	Stderr string
	Err    error
}

func (e *gitError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("git %s: %v: %s", strings.Join(e.Args, " "), e.Err, e.Stderr)
}

func (e *gitError) Unwrap() error { return e.Err }

// answered reports whether git ran, understood the question and said no —
// as against not answering it at all.
//
// KEYED ON SHAPES OBSERVED FROM THE REAL PROGRAM rather than on ones that
// seemed likely. Watched on the machine this was written on:
//
//	rev-parse --verify --quiet <a revision that is absent>  exit 1, nothing on stderr
//	rev-parse --verify --quiet, outside any repository      exit 128, "fatal: not a git repository ..."
//	merge-base <two commits with nothing in common>         exit 1, nothing on stderr
//	merge-base <a name that is not a commit>                exit 128, "fatal: Not a valid commit name ..."
//
// An error shape nobody has watched the real program produce is a guess,
// and the guess here is what decides whether "I could not look" gets
// reported as "there is nothing there".
func (e *gitError) answered() bool { return e.Code == 1 && e.Stderr == "" }

// answered lifts the question above through however an error was wrapped.
func answered(err error) bool {
	var e *gitError
	return errors.As(err, &e) && e.answered()
}

// run executes one git command and returns its standard output. A failure
// comes back as a gitError carrying the exit status and git's own words,
// because a git failure this check cannot explain is exactly the case
// where the operator needs them.
func (r repo) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		failure := &gitError{Args: args, Code: -1, Stderr: strings.TrimSpace(stderr.String()), Err: err}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			failure.Code = exit.ExitCode()
		}
		return "", failure
	}
	return stdout.String(), nil
}

// resolve turns a revision into the commit it names, or reports that this
// checkout does not have it.
//
// THIS IS THE EMPTY-VERSUS-UNDETERMINED SEAM. A shallow checkout answers
// "no such revision" for a commit that exists perfectly well in the
// repository, and a range computed against a checkout missing its own
// endpoints resolves to nothing. Nothing then gets examined and the run
// goes green, which is this check's whole failure mode.
//
// AND THE SEAM HAS A THIRD SIDE. Mapping every failure to "not in this
// checkout's history" turns a git that could not be run into a statement
// about the repository — so the two are separated on the shape git
// itself produced, and a failure that is not an answer keeps git's words.
func (r repo) resolve(rev string) (string, error) {
	out, err := r.run("rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		if !answered(err) {
			return "", fmt.Errorf("git could not be asked whether %q is in this checkout: %w",
				rev, err)
		}
		return "", fmt.Errorf("%q is %w", rev, errNotInHistory)
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("%q is %w", rev, errNotInHistory)
	}
	return strings.TrimSpace(out), nil
}

// mergeBase is the commit two revisions last had in common — or an answer
// that there is none, or git's own words about why it could not say.
func (r repo) mergeBase(a, b string) (string, error) {
	out, err := r.run("merge-base", a, b)
	if err != nil {
		if !answered(err) {
			return "", fmt.Errorf("git could not be asked what %q and %q have in common: %w",
				a, b, err)
		}
		return "", fmt.Errorf("no common commit between %q and %q: %w", a, b, errNotInHistory)
	}
	return strings.TrimSpace(out), nil
}

// commits lists the range, oldest first, so a report reads in the order
// the commits were written.
func (r repo) commits(base, head string) ([]string, error) {
	return r.revList("--reverse", base+".."+head)
}

// only lists exactly the commit named and nothing else.
//
// IT IS THE SAME ENUMERATION, and that is the whole reason it exists
// rather than the caller writing down the one sha it already has. The
// self-test reads a historical commit through this, so a break in the
// listing — the walk, the parsing, the ordering — is visible to the
// control that runs before any range is examined. A control that took a
// shortcut past the plumbing would vouch for a checker whose plumbing is
// dead.
//
// --no-walk rather than a parent range, because a parent range is not one
// commit for a merge: the clean commit this check proves itself against
// happens to be one, and its parent range covers six.
func (r repo) only(sha string) ([]string, error) {
	return r.revList("--no-walk", sha)
}

// revList runs one revision listing and returns the commits it named.
func (r repo) revList(args ...string) ([]string, error) {
	out, err := r.run(append([]string{"rev-list"}, args...)...)
	if err != nil {
		return nil, err
	}
	var shas []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			shas = append(shas, line)
		}
	}
	return shas, nil
}

// message returns a commit's whole message: subject and body, exactly as
// it was written. Both halves are published and both are checked.
func (r repo) message(sha string) (string, error) {
	return r.run("show", "--no-patch", "--format=%B", sha)
}

// tagMessage returns an annotated tag's message. A lightweight tag has
// none, which is an absence rather than a failure — but a tag ref this
// checkout does not carry is neither, and comes back as an error.
//
// THE OBJECT TYPE IS ASKED FIRST, and that is not belt and braces. A ref
// format asking for a tag's contents falls through to the COMMIT for a
// lightweight tag, so the commit's message comes back wearing the name
// "tag message" — scanned twice, and reported against a surface that does
// not exist. Found by the row that asserts a lightweight tag has no
// message, which is the control for the row that asserts an annotated one
// does.
//
// AND BOTH QUESTIONS ARE ASKED BY EXACT REF, never by name in a position
// where git reads options. A tag name is chosen by whoever pushed it, and
// listing tags by pattern puts that name where an option goes. Watched on
// the machine this was written on, against real tags:
//
//   - a tag named "-dash" exits 129, "options '-d' and '--list' cannot be
//     used together" — which at least fails;
//   - a tag named "-n1" exits 0 and prints the message of EVERY tag in
//     the repository, because the name was eaten as an option and the
//     pattern slot left empty. Four other tags' messages then arrive as
//     this one's, reported against a surface they did not come from;
//   - and a name that consumes the pattern slot without matching exits 0
//     with nothing, which is "no message" said without having looked.
//
// Asking by ref also gives this the three-way answer the rest of the
// check has, and gives it cleanly — measured the same way:
//
//	an annotated tag  -> "tag",    exit 0
//	a lightweight tag -> "commit", exit 0
//	a ref that is not there -> nothing at all, exit 0
//	no repository to ask     -> exit 128, "fatal: not a git repository ..."
//
// Absent and undetermined are different exits here, so neither has to be
// inferred from a message. That is why this asks a ref-listing rather
// than the object store, whose "no such object" and "no such repository"
// are the same exit and the same shape.
func (r repo) tagMessage(name string) (string, bool, error) {
	ref := "refs/tags/" + name
	kind, err := r.run("for-each-ref", "--format=%(objecttype)", ref)
	if err != nil {
		return "", false, fmt.Errorf("git could not be asked what %q is: %w", ref, err)
	}
	switch strings.TrimSpace(kind) {
	case "":
		return "", false, fmt.Errorf("the tag %q is %w", name, errNotInHistory)
	case "tag":
	default:
		return "", false, nil
	}
	out, err := r.run("for-each-ref", "--format=%(contents)", ref)
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(out) == "" {
		return "", false, nil
	}
	return out, true, nil
}

// atRevision reads a rule file as one revision has it.
//
// PRESENCE IS ASKED SEPARATELY, with ls-tree, and that is the whole
// reason this is two commands rather than one. `git show rev:path` fails
// both for a path the revision does not carry and for a revision this
// checkout does not have, and reporting the second as the first is
// reporting an absence when the honest answer is "I could not look".
// ls-tree answers with an empty listing and a zero exit for a path that
// is genuinely not there, and fails for anything else.
func (r repo) atRevision(rev string) end {
	return func(path string) (string, bool, error) {
		listed, err := r.run("ls-tree", "--name-only", rev, "--", path)
		if err != nil {
			return "", false, err
		}
		if strings.TrimSpace(listed) == "" {
			return "", false, nil
		}
		text, err := r.run("show", rev+":"+path)
		if err != nil {
			return "", false, err
		}
		return text, true, nil
	}
}

// workingTree reads a rule file as it is on disk right now.
//
// HEAD IS THE WORKING TREE ON PURPOSE, matching the content rule that
// owns these files: deleting a line from one has to measurably change
// what a check catches, or nobody can tell the file is really being read.
// The union above is what keeps that property from becoming a hole.
func (r repo) workingTree() end {
	return func(path string) (string, bool, error) {
		data, err := os.ReadFile(filepath.Join(r.dir, filepath.FromSlash(path)))
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		return string(data), true, nil
	}
}
