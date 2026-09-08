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

// run executes one git command and returns its standard output. Standard
// error is folded into the error, because a git failure this check cannot
// explain is exactly the case where the operator needs git's own words.
func (r repo) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
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
func (r repo) resolve(rev string) (string, error) {
	out, err := r.run("rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil || strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("%q is %w", rev, errNotInHistory)
	}
	return strings.TrimSpace(out), nil
}

// mergeBase is the commit two revisions last had in common.
func (r repo) mergeBase(a, b string) (string, error) {
	out, err := r.run("merge-base", a, b)
	if err != nil {
		return "", fmt.Errorf("no common commit between %q and %q: %w", a, b, errNotInHistory)
	}
	return strings.TrimSpace(out), nil
}

// commits lists the range, oldest first, so a report reads in the order
// the commits were written.
func (r repo) commits(base, head string) ([]string, error) {
	out, err := r.run("rev-list", "--reverse", base+".."+head)
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
func (r repo) tagMessage(name string) (string, bool, error) {
	ref := "refs/tags/" + name
	kind, err := r.run("cat-file", "-t", ref)
	if err != nil {
		return "", false, fmt.Errorf("the tag %q is %w", name, errNotInHistory)
	}
	if strings.TrimSpace(kind) != "tag" {
		return "", false, nil
	}
	out, err := r.run("tag", "--list", "--format=%(contents)", name)
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
