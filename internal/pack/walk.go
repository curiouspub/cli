package pack

import (
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/curiouspub/cli/internal/check"
)

// File is one regular file the walk will pack.
type File struct {
	// Path is relative to the project root and SLASH-SEPARATED on every
	// platform. It is built by joining components rather than by asking
	// the standard library for a relative path, which would hand back
	// the host's own separator — and this value is sorted, compared and
	// rendered into a result an agent may act on, so a separator that
	// changed with the machine would be a difference nobody asked for.
	Path string
	Size int64
	Mode fs.FileMode
}

// Tree is the walk's whole answer: what would be packed, what was
// skipped, and what the walk has to say about the project.
//
// THE THIRD FIELD IS THE SAME SHAPE THE PRE-FLIGHT ENGINE RETURNS. There
// is more than one producer of findings by design, and the place where
// two of them meet takes them as they come rather than re-wrapping at
// the call site.
type Tree struct {
	Files    []File
	Symlinks []string
	Results  check.Results
}

// Walk produces one canonical, ordered file list for a project.
//
// It is the single scan three later steps read — the local limits, the
// scan for hard-coded development URLs, and the archive itself — so it
// has to answer the same way twice, on two machines, whatever order the
// filesystem hands its directories back in.
//
// THE EXCLUSION ORDER IS FIXED AND IT IS THE SECURITY PROPERTY. The
// forced set is evaluated FIRST and cannot be switched off by anything
// inside the project; the project's own ignore rules come next, with
// their full semantics including negation; everything else is included.
// A negation that could re-enable a forced exclude would let a file in
// the repository decide that a secrets file gets published.
//
// Nothing here follows a symbolic link, which is what makes a link
// pointing at one of its own ancestors harmless, and nothing here reads
// a file's contents except an ignore file — whose rules are bytes and
// cannot be had any other way.
func Walk(fsys FS, root string) (Tree, error) {
	w := &walker{fsys: fsys, root: root}
	if err := w.descend("", nil); err != nil {
		return Tree{}, err
	}

	// Sorted byte-wise on the slash-separated path, which is a stronger
	// promise than "sorted": it does not depend on the host's separator
	// or on the order the traversal happened to visit directories in.
	sort.Slice(w.files, func(i, j int) bool { return w.files[i].Path < w.files[j].Path })
	sort.Strings(w.symlinks)

	return Tree{
		Files:    w.files,
		Symlinks: w.symlinks,
		Results:  results(w.files, w.symlinks),
	}, nil
}

type walker struct {
	fsys     FS
	root     string
	files    []File
	symlinks []string
}

func (w *walker) osPath(rel string) string {
	if rel == "" {
		return w.root
	}
	return filepath.Join(w.root, filepath.FromSlash(rel))
}

// descend visits one directory with the ignore files in force above it.
//
// The stack is passed down rather than held on the walker so that
// leaving a directory cannot forget to pop it — the kind of bookkeeping
// slip that shows up as a rule from one branch of the tree quietly
// applying to another.
func (w *walker) descend(rel string, inherited []*ignoreFile) error {
	dir := w.osPath(rel)
	entries, err := w.fsys.ReadDir(dir)
	if err != nil {
		if rel == "" {
			return fmt.Errorf("reading the project directory: %w", err)
		}
		return fmt.Errorf("reading %s: %w", rel, err)
	}

	stack := inherited
	for _, e := range entries {
		// A link or a pipe wearing the name is not a set of rules, and
		// opening one of those is how a walk hangs rather than fails.
		if e.Name() != gitignoreName || !e.Type().IsRegular() {
			continue
		}
		loaded, err := w.loadIgnoreFile(rel, dir)
		if err != nil {
			return err
		}
		stack = append(append([]*ignoreFile{}, inherited...), loaded)
		break
	}

	for _, e := range entries {
		name := e.Name()
		child := name
		if rel != "" {
			child = rel + "/" + name
		}

		if forcedExclude(name, rel == "") {
			continue
		}
		if ignoredBy(stack, child, e.IsDir()) {
			// NOT DESCENDED INTO, which is behaviour-preserving rather
			// than merely quick: a file inside an excluded directory
			// cannot be brought back by a negation, so there is nothing
			// down there to find.
			continue
		}

		switch {
		case e.Type()&fs.ModeSymlink != 0:
			// Recorded and never followed. What it points at is either
			// already in the tree, in which case following it would pack
			// the bytes twice, or outside the project, in which case
			// packing it would put a file the author did not choose into
			// their site.
			w.symlinks = append(w.symlinks, child)
		case e.IsDir():
			if err := w.descend(child, stack); err != nil {
				return err
			}
		case e.Type().IsRegular():
			info, err := e.Info()
			if err != nil {
				return fmt.Errorf("reading %s: %w", child, err)
			}
			w.files = append(w.files, File{Path: child, Size: info.Size(), Mode: info.Mode()})
		default:
			// Sockets, named pipes and device nodes, dropped without a
			// word: they cannot be meaningfully archived and never
			// belong to an Astro project, so a warning about one would
			// be noise in front of a decision.
		}
	}
	return nil
}

func (w *walker) loadIgnoreFile(rel, dir string) (*ignoreFile, error) {
	rc, err := w.fsys.Open(filepath.Join(dir, gitignoreName))
	if err != nil {
		return nil, fmt.Errorf("reading the ignore rules in %s: %w", displayDir(rel), err)
	}
	defer rc.Close()

	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("reading the ignore rules in %s: %w", displayDir(rel), err)
	}
	return parseIgnoreFile(rel, data), nil
}

func displayDir(rel string) string {
	if rel == "" {
		return "the project directory"
	}
	return rel
}

// forcedExclude is the absolute half of the exclusion order: names that
// go whatever the project says about them.
//
// IT MATCHES ON THE NAME AND NOT ON THE KIND OF THING WEARING IT, which
// looks over-broad and is not. A linked worktree and a submodule both
// spell the version-control directory as a FILE holding a pointer, so a
// rule that only excluded directories would upload one; a dependency
// directory is sometimes a link rather than a directory, and a rule that
// only excluded directories would report it as a skipped link instead of
// silently doing the right thing.
//
// THE DEPTH RULES DIFFER, AND THE DIFFERENCE IS THE POINT. Dependency
// and version-control directories go at any depth, because a workspace
// has several of the first and a linked checkout puts the second
// anywhere. The build output and the framework's cache go at the ROOT
// ONLY: a directory of the same name three levels down is somebody's
// real source directory, and deleting it from their site would be a bug
// they could not diagnose from the outside.
//
// The environment-file rule is the whole prefix, sample files included.
// A rule with an exception is a rule somebody finds the wrong edge of,
// and the cost of the exception — a sample file the build never reads is
// absent — is nothing beside the cost of the mistake.
func forcedExclude(name string, atRoot bool) bool {
	switch name {
	case "node_modules", ".git", ".DS_Store", "Thumbs.db":
		return true
	case "dist", ".astro":
		return atRoot
	}
	return strings.HasPrefix(name, ".env")
}
