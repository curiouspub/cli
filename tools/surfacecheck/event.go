package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// zeroSHA is what a push event carries where a commit would go when there
// is no commit: before a branch's first push, and after its deletion. It
// is git's own spelling of "nothing", not a number this check chose.
const zeroSHA = "0000000000000000000000000000000000000000"

// named is a surface with no commit of its own — a branch name, a tag
// name, a tag message, a pull request's title or body.
type named struct {
	Subject string
	Text    string
}

// request is one push, pull request or merge-queue entry, reduced to what
// this check needs.
type request struct {
	// Base and Head bound the commit range. They are revisions, not
	// resolved commits: resolving them is where an unresolvable range is
	// found, and that has to happen where it can be reported.
	Base, Head string

	// Named are the surfaces that are not commits.
	Named []named

	// Skip says nothing is being published, and why. A branch deletion is
	// the case: there is no message, no tree and no new name.
	Skip string
}

// payload is the part of a workflow event this check reads.
//
// READ FROM THE EVENT FILE rather than interpolated into a command by the
// workflow, and that is a security property rather than a preference. A
// pull request's title and body are written by whoever opened it; a
// workflow that pastes them into a shell line has handed a stranger the
// runner. Decoding the JSON cannot execute anything.
type payload struct {
	Before     string `json:"before"`
	After      string `json:"after"`
	Ref        string `json:"ref"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	PullRequest *struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Base  struct {
			SHA string `json:"sha"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
			Ref string `json:"ref"`
		} `json:"head"`
	} `json:"pull_request"`
	MergeGroup *struct {
		BaseSHA string `json:"base_sha"`
		HeadSHA string `json:"head_sha"`
		HeadRef string `json:"head_ref"`
	} `json:"merge_group"`
}

// requestFromEvent turns a workflow event into the range and the surfaces
// to check.
//
// AN EVENT THIS FUNCTION DOES NOT KNOW IS AN ERROR, never a pass. A check
// that shrugs at an unfamiliar trigger is a check somebody widens the
// triggers past.
func requestFromEvent(r repo, eventName, refName string, raw []byte) (request, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return request{}, fmt.Errorf("reading the %s event: %w", eventName, err)
	}

	switch eventName {
	case "push":
		return pushRequest(r, p, refName)

	case "pull_request", "pull_request_target":
		if p.PullRequest == nil {
			return request{}, fmt.Errorf("the %s event carries no pull request", eventName)
		}
		base, err := r.mergeBase(p.PullRequest.Base.SHA, p.PullRequest.Head.SHA)
		if err != nil {
			return request{}, err
		}
		// THE CANDIDATE SET, not the tip. Every commit from the merge
		// base to head is what a merge publishes, and a message four
		// commits back is published exactly as loudly as the last one.
		//
		// The title and body are here because a squash merge composes a
		// commit message out of them. They are not a surface this check
		// merely cannot reach — they become a commit on the default
		// branch.
		return request{
			Base: base,
			Head: p.PullRequest.Head.SHA,
			Named: []named{
				{Subject: "branch name", Text: p.PullRequest.Head.Ref},
				{Subject: "pull request title", Text: p.PullRequest.Title},
				{Subject: "pull request body", Text: p.PullRequest.Body},
			},
		}, nil

	case "merge_group":
		if p.MergeGroup == nil {
			return request{}, fmt.Errorf("the %s event carries no merge group", eventName)
		}
		return request{
			Base:  p.MergeGroup.BaseSHA,
			Head:  p.MergeGroup.HeadSHA,
			Named: []named{{Subject: "merge queue ref", Text: p.MergeGroup.HeadRef}},
		}, nil
	}

	return request{}, fmt.Errorf("this check does not know how to find the published "+
		"surfaces of a %q event, and refusing is the only honest answer: a trigger it "+
		"shrugs at is a trigger somebody adds", eventName)
}

// pushRequest resolves a push, which is the event with all three of the
// awkward shapes: a new ref, a deleted ref, and a tag.
func pushRequest(r repo, p payload, refName string) (request, error) {
	if p.Deleted || p.After == zeroSHA {
		return request{Skip: "the push deletes " + describeRef(p.Ref, refName) +
			"; nothing is being published"}, nil
	}

	req := request{Head: p.After}
	if strings.HasPrefix(p.Ref, "refs/tags/") {
		surfaces, err := tagSurfaces(r, refName)
		if err != nil {
			return request{}, err
		}
		req.Named = append(req.Named, surfaces...)
	} else {
		req.Named = append(req.Named, named{Subject: "branch name", Text: refName})
	}

	// A BEFORE-SHA CAN BE PRESENT AND GONE, which is not the same as
	// absent and is the case this originally got wrong. A force-push —
	// every rebase of a branch under review is one — names a commit the
	// push itself discarded, so the checkout has a range whose base is
	// not in it. The run then refused, correctly and permanently: the
	// same head would fail on every re-run, and a required check that can
	// never go green blocks the merge queue for ever.
	//
	// It is REFUSED RATHER THAN GUESSED only when there is nothing to
	// fall back to. A discarded base is measured against the default
	// branch, which is exactly what a new ref does and for the same
	// reason: what is new here is what this branch has that the default
	// branch does not.
	if p.Before != zeroSHA {
		if _, err := r.resolve(p.Before + "^{commit}"); err == nil {
			req.Base = p.Before
			return req, nil
		}
	}

	// A NEW REF HAS NO BEFORE-SHA, so the range is taken against the
	// default branch. A branch created at the default tip then has a
	// legitimately EMPTY range and passes on the strength of its name
	// having been checked: there are no commits because there is nothing
	// new.
	base, err := defaultBranchTip(r, p.Repository.DefaultBranch)
	if err != nil {
		return request{}, err
	}
	merged, err := r.mergeBase(base, p.After)
	if err != nil {
		return request{}, err
	}
	req.Base = merged
	return req, nil
}

// tagSurfaces returns everything a tag publishes.
//
// A TAG IS THE SURFACE WITH THE LEAST CHANCE OF A SECOND LOOK, and it has
// two of them: the name a person typed and the message they wrote with
// it. Both are published the instant the tag is pushed.
//
// ONE FUNCTION, because the workflow and the pre-push hook reach a tag by
// different roads — an event payload and a command line — and a hook that
// read one surface where the workflow reads two would pass a push that
// the run then fails. A fast check that disagrees with the slow one is a
// fast check people stop running.
func tagSurfaces(r repo, name string) ([]named, error) {
	out := []named{{Subject: "tag name", Text: name}}
	message, annotated, err := r.tagMessage(name)
	if err != nil {
		return nil, err
	}
	if annotated {
		out = append(out, named{Subject: "tag message", Text: message})
	}
	return out, nil
}

// defaultBranchTip finds the default branch in this checkout, preferring
// the remote-tracking ref because that is the one a workflow checkout
// actually has.
func defaultBranchTip(r repo, branch string) (string, error) {
	if branch == "" {
		return "", fmt.Errorf("the event names no default branch, so a new ref has nothing " +
			"to be measured against")
	}
	for _, candidate := range []string{"refs/remotes/origin/" + branch, "refs/heads/" + branch} {
		if sha, err := r.resolve(candidate); err == nil {
			return sha, nil
		}
	}
	return "", fmt.Errorf("neither the remote-tracking nor the local ref for the default "+
		"branch %q is %w — a shallow or partial checkout cannot answer what is new on "+
		"this ref", branch, errNotInHistory)
}

// describeRef renders a ref for a human without pretending to know more
// than the event said.
func describeRef(ref, name string) string {
	switch {
	case strings.HasPrefix(ref, "refs/tags/"):
		return "the tag " + name
	case name != "":
		return "the branch " + name
	default:
		return "a ref"
	}
}
