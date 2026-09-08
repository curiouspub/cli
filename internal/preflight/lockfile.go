package preflight

import (
	"fmt"
	"path/filepath"

	"github.com/curiouspub/cli/internal/check"
)

// LockfileCheck is the lockfile row as the engine registers it.
func LockfileCheck() Check {
	return Check{IDs: []string{check.IDLockfile}, Run: CheckLockfile}
}

// installableLockfiles are the lockfiles a deploy can actually be built
// from, in the order this check looks for them. The first one present
// settles the question; nothing warns and nothing prefers one over
// another.
//
// THE THIRD ENTRY DEPENDS ON A BEHAVIOUR THAT LIVES IN THE BUILD AGENT,
// and the dependency runs backwards — the agent is written after this
// client, so it is invisible from here unless it is written down. The
// agent DETECTS its install command from the lockfile it finds, and its
// image ships both package managers, which is the only reason accepting
// a pnpm lockfile here is honest. If that detection is ever replaced by
// one fixed install command, this row starts passing projects the build
// will reject, and the third entry has to come out or carry a warning.
var installableLockfiles = []string{
	"package-lock.json",
	"npm-shrinkwrap.json",
	"pnpm-lock.yaml",
}

// otherLockfiles are lockfiles this check RECOGNISES and cannot install
// from. They are listed separately rather than treated as absence
// because the two conditions need different words: telling somebody with
// a lockfile in front of them that they have no lockfile is how a
// message loses its reader.
var otherLockfiles = []string{"yarn.lock", "bun.lockb"}

// workspaceMarkers are the three signals that this directory is a
// package inside a monorepo, checked in this order in each ancestor.
//
// The order is fixed so that a root carrying more than one names the
// same file on every run — a message that changes between two runs over
// one project is one nobody can compare against a colleague's.
var workspaceMarkers = []string{"pnpm-workspace.yaml", "turbo.json"}

// CheckLockfile answers whether this project can be installed from a
// lockfile, and — when it cannot — which of three different things went
// wrong.
//
// IT DECLINES RATHER THAN FAILS WHEN THERE IS NO package.json. "Commit a
// lockfile" is bad advice for a directory that is not a Node project at
// all: the astro-dep check has already said the useful thing, and a
// second, wronger instruction underneath it only muddies the fix. The
// decline is ENVIRONMENTAL — something outside the check stopped it
// looking, the reader can see it, and it joins the question the surface
// asks. That price is unobservable today, because the same condition
// hard-stops astro-dep in the same run and a hard stop never prompts; it
// is chosen here on purpose rather than discovered later to have been
// chosen by default.
func CheckLockfile(fsys FS, root string) Result {
	if reason := cannotAskAboutLockfiles(fsys, root); reason != "" {
		return Result{Declined: map[string]check.Decline{
			check.IDLockfile: {Kind: check.Environmental, Reason: reason},
		}}
	}

	// UNDETERMINED IS CHECKED BEFORE ANYTHING IS CONCLUDED, and the
	// order is the point. A lockfile this check could not stat may well
	// be there, so every sentence below it — "you have none", "the only
	// one here is yarn.lock" — would be a confident claim of absence
	// built out of a permission error. The check says it could not look
	// instead.
	var unanswerable string
	for _, name := range installableLockfiles {
		switch isFile(fsys, filepath.Join(root, name)) {
		case present:
			return Result{}
		case undetermined:
			if unanswerable == "" {
				unanswerable = name
			}
		}
	}
	if unanswerable != "" {
		return Result{Declined: map[string]check.Decline{
			check.IDLockfile: {
				Kind:   check.Environmental,
				Reason: "couldn't check whether " + unanswerable + " is there",
			},
		}}
	}

	if found := presentLockfiles(fsys, root, otherLockfiles); len(found) > 0 {
		names := joinWithAnd(found)
		return hardStop(check.IDLockfile,
			fmt.Sprintf("The only lockfile here is %s, which curious can't install from.", names),
			"No lockfile curious can install from.",
			fmt.Sprintf("curious installs from package-lock.json, npm-shrinkwrap.json or\n"+
				"pnpm-lock.yaml so it builds the exact versions you tested. This\n"+
				"project has %s, which it can't install from.", names),
			"Run `npm install` (or `pnpm install`), commit the lockfile it\n"+
				"creates, and try again. You can keep the one you already have.")
	}

	// THE WORKSPACE SNIFF CHANGES THE MESSAGE, NEVER THE VERDICT. A
	// package inside a workspace genuinely cannot be built on its own —
	// its dependencies resolve through the workspace root — so a deploy
	// that got past here would fail in the sandbox with a worse message.
	// What was wrong was only the instruction: telling somebody to
	// create a file they already have, three directories up.
	if marker, found := workspaceAbove(fsys, root); found {
		return hardStop(check.IDLockfile,
			"This is a package inside a workspace, and workspace projects aren't supported yet.",
			"This looks like a package inside a workspace.",
			fmt.Sprintf("curious deploys one standalone project. %s sits above this\n"+
				"directory, so this package's dependencies resolve through the\n"+
				"workspace root and its lockfile isn't here.", marker),
			"Workspace projects aren't supported yet. Deploy a standalone project\n"+
				"for now.")
	}

	// The words below are the authored ones, and the build agent reaches
	// this same condition from the other side and emits its own. The
	// obligation is that the two say the SAME THING — same claim, same
	// named cause, same instruction — and deliberately not that they are
	// byte-identical: this renders three paragraphs to a terminal and
	// the agent emits one compact line into a log stream. What must not
	// happen is a user reading one story here and a different one from
	// the build, and concluding the two halves of the product disagree
	// about what went wrong. The three spellings are named because a
	// project carrying only an npm-shrinkwrap.json builds perfectly
	// well, and a message naming two of them would tell its owner they
	// have no lockfile.
	return hardStop(check.IDLockfile, "No lockfile found.",
		"No lockfile found.",
		"curious installs your dependencies from a lockfile, so it builds the\n"+
			"exact versions you tested. Your project has package.json but no\n"+
			"package-lock.json, npm-shrinkwrap.json or pnpm-lock.yaml.",
		"Run `npm install` (or `pnpm install`), commit the lockfile it\n"+
			"creates, and try again.")
}

// cannotAskAboutLockfiles returns the reason this check has no business
// asking about lockfiles at all, or "" when it has.
//
// The reason reaches a surface verbatim, so it is written for a person
// and names the file rather than the operation.
func cannotAskAboutLockfiles(fsys FS, root string) string {
	switch readPackageJSON(fsys, root).fault {
	case packageJSONFound:
		return ""
	case packageJSONMissing, packageJSONUnreadable:
		return "couldn't read package.json"
	default:
		return "couldn't read package.json (it isn't a package.json this check can parse)"
	}
}

// presentLockfiles returns the names from candidates that are really
// there, in the order given.
//
// AN UNDETERMINED ANSWER IS NOT A PRESENT ONE and is not an absent one
// either; here it is dropped, which is safe only because of where this
// is called from. Every installable lockfile has already been settled by
// the time this runs, so the worst a missed yarn.lock costs is the
// generic no-lockfile message instead of the more specific one — a
// less helpful sentence, never a wrong claim.
func presentLockfiles(fsys FS, root string, candidates []string) []string {
	var found []string
	for _, name := range candidates {
		if isFile(fsys, filepath.Join(root, name)) == present {
			found = append(found, name)
		}
	}
	return found
}

// workspaceAbove walks the ancestors of root looking for a workspace
// root, and returns how to NAME what it found.
//
// IT READS OUTSIDE THE PROJECT DIRECTORY, which is the one thing in this
// package that does, and it is worth being explicit about the limits.
// It reads only: no file is written, no link is followed out of the tree
// (the climb is lexical, and each marker is stat-ed without resolving a
// link to somewhere else), and nothing here executes. It stops at the
// filesystem root — and at a drive root on Windows, where the parent of
// a volume is that volume. It is a message-selection input and nothing
// more: no verdict anywhere depends on what it finds.
//
// The ancestors are searched, never the project directory itself. A
// marker sitting in the directory being deployed says that THIS project
// is the workspace root, which is a different thing to tell somebody —
// and the standing advice, run an install and commit the lockfile, is
// correct for that case.
//
// The path is made absolute first, and that matters rather than being
// tidy: a relative root walks up to "." and stops there, three
// directories short of anything, so the sniff would silently answer "no
// workspace" for every caller that passed a relative path.
func workspaceAbove(fsys FS, root string) (string, bool) {
	dir := root
	if abs, err := filepath.Abs(root); err == nil {
		dir = abs
	}

	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent

		for _, marker := range workspaceMarkers {
			if isFile(fsys, filepath.Join(dir, marker)) == present {
				return marker, true
			}
		}
		if declaresWorkspaces(fsys, filepath.Join(dir, packageJSONName)) {
			return "a package.json with a workspaces field", true
		}
	}
}
