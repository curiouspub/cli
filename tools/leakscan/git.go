package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// FetchRefspec is what this scan's universe IS, written once.
//
// TWO REFSPECS AND NOT `--all`. A clone carries refs origin never had:
// this repository's own has a refs/original/ left by a history rewrite
// and a refs/backup/, and `git rev-list --all` reaches all of them. A
// scan whose universe depends on which clone it runs in is not
// reproducible even on the day the answers happen to agree, and the day
// they stop agreeing is the day a rewrite orphans content — which is
// exactly when somebody needs this to be trustworthy.
//
// refs/pull/* IS EXCLUDED BY RULING, and the reason is that it is
// covered rather than that it is harmless: a pull request's content is
// read at pull-request time by the surface check and by the suite, before
// anything can be merged. Scanning it here would report on text that was
// never published to a branch and can still be rewritten by its author.
var FetchRefspec = []string{
	"+refs/heads/*:refs/remotes/origin/*",
	"+refs/tags/*:refs/tags/*",
}

// universeRefs are the ref namespaces the refspec above lands in. They
// are derived from it in the only sense that matters — a ref this scan
// reads is a ref that fetch would write — and they are named separately
// because a scan usually runs on a checkout that has already fetched.
var universeRefs = []string{"refs/remotes/origin/", "refs/tags/"}

// repo is one checkout, addressed by directory so that a fixture and a
// real run take the same path.
type repo struct{ dir string }

func (r repo) run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	var out, errs bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errs
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err,
			strings.TrimSpace(errs.String()))
	}
	return out.String(), nil
}

// Fetch performs the explicit fetch this scan's universe is defined by.
//
// IT IS NOT WHAT AN ORDINARY RUN DOES, and that is deliberate. A gate
// that reaches the network is a gate that goes red when the network does,
// and the checkout a run already has is normally the answer — a workflow
// fetching the whole history has performed exactly this refspec before
// anything ran. What this exists for is the machine where somebody wants
// to be sure, and for the refspec to be a thing you can run rather than a
// sentence in a comment.
func (r repo) Fetch() error {
	_, err := r.run(append([]string{"fetch", "--prune", "--quiet", "origin"}, FetchRefspec...)...)
	return err
}

// Refs returns the roots of the universe: every ref the refspec above
// lands in, minus the two kinds that are not history.
//
// refs/remotes/origin/HEAD is a symbolic ref — it names another ref in
// this same list, so following it counts one branch twice — and anything
// under a pull namespace is excluded by the ruling above.
func (r repo) Refs() ([]string, error) {
	out, err := r.run(append([]string{"for-each-ref", "--format=%(refname)"}, universeRefs...)...)
	if err != nil {
		return nil, err
	}
	var refs []string
	for _, line := range strings.Split(out, "\n") {
		name := strings.TrimSpace(line)
		switch {
		case name == "":
		case name == "refs/remotes/origin/HEAD":
		case strings.Contains(name, "/pull/"):
		default:
			refs = append(refs, name)
		}
	}
	sort.Strings(refs)
	return refs, nil
}

// blob is one reachable piece of content and every path it was seen at.
//
// THE PATHS ARE A SET AND THE CONTENT IS ONE THING, which is the whole
// reason identity is the blob. The same bytes committed at two paths are
// one object; git stores it once, and a reader who finds it finds the
// same string whichever name it wore.
type blob struct {
	SHA   string
	Size  int64
	Paths []string
}

// Blobs enumerates every blob reachable from the given refs, with the
// paths each was seen at.
//
// BLOBS RATHER THAN PATCHES, and this was measured rather than reasoned.
// A secret introduced only while resolving a merge conflict gives a patch
// walk zero hits and a blob enumeration one: a bare log of patches omits
// merge diffs and walks one ancestry. Merge commits are included here by
// construction, because a blob is a blob however it entered.
func (r repo) Blobs(refs []string) ([]blob, error) {
	listed, err := r.run(append([]string{"rev-list", "--objects"}, refs...)...)
	if err != nil {
		return nil, err
	}

	paths := map[string][]string{}
	seen := map[string]map[string]bool{}
	var order []string
	for _, line := range strings.Split(listed, "\n") {
		sha, path, named := strings.Cut(line, " ")
		if !named || path == "" || len(sha) == 0 {
			// A commit is listed with no path. A tree has one, and is
			// filtered out below by its type rather than by its name.
			continue
		}
		if seen[sha] == nil {
			seen[sha] = map[string]bool{}
			order = append(order, sha)
		}
		if !seen[sha][path] {
			seen[sha][path] = true
			paths[sha] = append(paths[sha], path)
		}
	}

	types, sizes, err := r.batchCheck(order)
	if err != nil {
		return nil, err
	}
	var out []blob
	for _, sha := range order {
		if types[sha] != "blob" {
			continue
		}
		sort.Strings(paths[sha])
		out = append(out, blob{SHA: sha, Size: sizes[sha], Paths: paths[sha]})
	}
	return out, nil
}

// batchCheck asks git what each object is, in ONE process rather than one
// per object. The difference is not cosmetic: a process per object turns
// a two-second scan into a thirty-second one, and a check nobody can
// afford to run on every push is a check that moves to a nightly job and
// then to nowhere.
func (r repo) batchCheck(shas []string) (map[string]string, map[string]int64, error) {
	cmd := exec.Command("git", "cat-file", "--batch-check")
	cmd.Dir = r.dir
	cmd.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")
	var errs bytes.Buffer
	cmd.Stderr = &errs
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("classifying %d object(s): %w: %s", len(shas), err,
			strings.TrimSpace(errs.String()))
	}
	types := map[string]string{}
	sizes := map[string]int64{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 {
			continue
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			continue
		}
		types[fields[0]] = fields[1]
		sizes[fields[0]] = size
	}
	return types, sizes, nil
}

// contents streams the bytes of every named blob through one git process
// and hands each to visit.
//
// THE ERROR IS NOT SWALLOWED PER OBJECT. A blob that could not be read is
// a blob nobody looked at, and the whole point of the three exit codes is
// that "I could not look" is not "I looked and found nothing".
func (r repo) contents(shas []string, visit func(sha string, content []byte) error) error {
	cmd := exec.Command("git", "cat-file", "--batch")
	cmd.Dir = r.dir
	cmd.Stdin = strings.NewReader(strings.Join(shas, "\n") + "\n")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errs bytes.Buffer
	cmd.Stderr = &errs
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = cmd.Wait() }()

	reader := bufio.NewReaderSize(stdout, 1<<16)
	for range shas {
		header, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading an object header: %w: %s", err, strings.TrimSpace(errs.String()))
		}
		fields := strings.Fields(strings.TrimSpace(header))
		if len(fields) != 3 {
			return fmt.Errorf("git answered %q, which is not an object header", strings.TrimSpace(header))
		}
		size, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			return fmt.Errorf("git reported the size of %s as %q", fields[0], fields[2])
		}
		content := make([]byte, size)
		if _, err := io.ReadFull(reader, content); err != nil {
			return fmt.Errorf("reading %s: %w", fields[0], err)
		}
		if _, err := reader.Discard(1); err != nil {
			return fmt.Errorf("reading past %s: %w", fields[0], err)
		}
		if err := visit(fields[0], content); err != nil {
			return err
		}
	}
	return nil
}

// commit is one commit with everything the attribution ordering needs.
type commit struct {
	SHA       string
	Author    int64
	Committer int64

	// Topo is this commit's index in a topological listing, newest first,
	// so a LARGER value is further back. It is the third tiebreak and it
	// is a property of the graph rather than of a clock, which is what
	// makes it usable when two clocks agree.
	Topo int
}

// attributionOrder returns every commit in the universe, ordered the way
// attribution reads them: earliest first.
//
// THE ORDER IS FULLY DETERMINED, and it has to be. Ties on author date
// are ordinary — three commits in this repository share one date for a
// single blob — and an attribution that varies between runs on the same
// repository is the one thing a ledger cannot have. So: author date, then
// committer date, then topological position, then the object id, which
// cannot tie.
func (r repo) attributionOrder(refs []string) ([]commit, error) {
	out, err := r.run(append([]string{"rev-list", "--topo-order", "--format=%at %ct"}, refs...)...)
	if err != nil {
		return nil, err
	}
	var commits []commit
	lines := strings.Split(out, "\n")
	for i := 0; i+1 < len(lines); i++ {
		sha, ok := strings.CutPrefix(lines[i], "commit ")
		if !ok {
			continue
		}
		fields := strings.Fields(lines[i+1])
		if len(fields) != 2 {
			return nil, fmt.Errorf("git described %s with %q rather than two dates", sha, lines[i+1])
		}
		author, err1 := strconv.ParseInt(fields[0], 10, 64)
		committer, err2 := strconv.ParseInt(fields[1], 10, 64)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("git described %s with %q rather than two dates", sha, lines[i+1])
		}
		commits = append(commits, commit{
			SHA: strings.TrimSpace(sha), Author: author, Committer: committer, Topo: len(commits),
		})
		i++
	}
	sort.Slice(commits, func(i, j int) bool {
		a, b := commits[i], commits[j]
		switch {
		case a.Author != b.Author:
			return a.Author < b.Author
		case a.Committer != b.Committer:
			return a.Committer < b.Committer
		case a.Topo != b.Topo:
			return a.Topo > b.Topo
		default:
			return a.SHA < b.SHA
		}
	})
	return commits, nil
}

// Attribute returns, for each wanted blob, the earliest commit in the
// universe whose TREE CONTAINS it.
//
// THE TEST IS TREE MEMBERSHIP, and the alternative was measured to be
// wrong rather than merely slower. Asking git which commits an object's
// occurrence CHANGED at selects additions and REMOVALS alike, so it
// reports the commit that took a citation out as readily as the one that
// put it in — and it does not even agree about membership: on this
// repository it ranks a commit earliest for a blob whose tree does not
// contain that blob at all.
//
// A COMMIT IS NOT CALLED THE INTRODUCING ONE. A blob can enter by copy,
// by rewrite, by merge resolution, or from a branch that was later
// deleted. "The earliest reachable commit whose tree contains it" is
// checkable; "introduced" is a story about somebody's intent that the
// data cannot support.
func (r repo) Attribute(refs []string, wanted []string) (map[string]string, error) {
	if len(wanted) == 0 {
		return map[string]string{}, nil
	}
	outstanding := map[string]bool{}
	for _, sha := range wanted {
		outstanding[sha] = true
	}

	commits, err := r.attributionOrder(refs)
	if err != nil {
		return nil, err
	}
	found := map[string]string{}
	for _, c := range commits {
		listing, err := r.run("ls-tree", "-r", c.SHA)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(listing, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			if outstanding[fields[2]] {
				found[fields[2]] = c.SHA
				delete(outstanding, fields[2])
			}
		}
		if len(outstanding) == 0 {
			break
		}
	}
	return found, nil
}

// short renders a sha the way a person reads one.
func short(sha string) string {
	const shownCharacters = 7 // what this repository's own log prints
	if len(sha) <= shownCharacters {
		return sha
	}
	return sha[:shownCharacters]
}
