package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// blockingGitOnPath builds a stub named `git`, puts it first on PATH, and
// returns nothing but the guarantee that every git invocation from this
// process now blocks until the test ends.
//
// IT IS A BUILT BINARY RATHER THAN A SHELL SCRIPT, because this package's
// suite runs on three platforms and a script with a shebang is not one of
// them. The build costs a second and buys a row that runs everywhere
// instead of one declared skip on Windows.
func blockingGitOnPath(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	// It sleeps rather than spinning: the point is a command that never
	// returns, and a busy loop would make the row's cost depend on how
	// many cores the runner has.
	const stub = `package main

import "time"

func main() { time.Sleep(10 * time.Minute) }
`
	if err := os.WriteFile(src, []byte(stub), 0o600); err != nil {
		t.Fatalf("writing the stub: %v", err)
	}

	name := "git"
	if runtime.GOOS == "windows" {
		name = "git.exe"
	}
	binDir := t.TempDir()
	build := exec.Command("go", "build", "-o", filepath.Join(binDir, name), src)
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the blocking git stub: %v\n%s", err, out)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The stub must actually be what `git` resolves to now, or this row
	// would pass by testing the real git's speed.
	resolved, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("after putting the stub first on PATH, git does not resolve: %v", err)
	}
	if filepath.Dir(resolved) != binDir {
		t.Fatalf("git resolves to %s, not the stub in %s — this row would be measuring the "+
			"real git", resolved, binDir)
	}
}

// TestAGitCallThatNeverReturnsFailsInSeconds is the row the fix exists
// for, and it is written against a command that genuinely never returns
// rather than a slow one.
//
// The failure being guarded: a git invocation that does not come back
// takes the whole run down with it, at whatever cap the caller happens to
// have — twenty-five minutes for the test binary that found this — and
// says nothing about which object was being read.
//
// THE ROW CARRIES ITS OWN BOUND. If the timeout is removed, the call
// under test blocks for ever, and a row that hangs is the defect rather
// than a report of it: the suite would die at its own cap with this row's
// name on it and no assertion message. So the call runs in a goroutine
// and this row stops waiting after a margin of its own.
func TestAGitCallThatNeverReturnsFailsInSeconds(t *testing.T) {
	blockingGitOnPath(t)

	r := repo{dir: t.TempDir()}

	type outcome struct {
		err     error
		elapsed time.Duration
	}
	done := make(chan outcome, 1)
	go func() {
		start := time.Now()
		_, err := r.run("ls-tree", "-r", "-z", "--full-tree", "deadbeef")
		done <- outcome{err: err, elapsed: time.Since(start)}
	}()

	// Generous against gitCallTimeout and still far below any suite cap:
	// this bound exists so the row REPORTS rather than joins the hang.
	const rowBound = 2 * time.Minute

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatal("a git command that never returns was reported as a success")
		}
		if !strings.Contains(got.err.Error(), "no result after") {
			t.Errorf("the failure does not say the command never returned: %v", got.err)
		}
		// AND IT NAMES WHAT WAS BEING READ. A timeout that says only
		// "timed out" leaves the reader where the original failure did.
		if !strings.Contains(got.err.Error(), "deadbeef") {
			t.Errorf("the failure does not name the object being read, which is the whole "+
				"difference from the silence it replaces: %v", got.err)
		}
		if got.elapsed > rowBound {
			t.Errorf("the call took %s to give up", got.elapsed)
		}
		t.Logf("gave up after %s, bound %s", got.elapsed.Round(time.Second), gitCallTimeout)

	case <-time.After(rowBound):
		t.Fatalf("a git call that never returns did not fail within %s — the deadline is "+
			"absent or ineffective, which is the defect this row exists for", rowBound)
	}
}

// TestEveryGitInvocationGoesThroughTheDeadline is the invariant, and it is
// the one that keeps the others true: the walk's WaitGroup can never wait
// on a process with no deadline.
//
// The timeout lives in runInput. runInputNow is the undeadlined call it
// wraps, and a second caller of runInputNow — added by somebody reaching
// past the wrapper for a reason that looked good at the time — would
// reintroduce the entire failure while every row above stayed green.
//
// So this reads the package's own source and asserts that runInputNow has
// exactly one caller, by name, and that it is runInput.
func TestEveryGitInvocationGoesThroughTheDeadline(t *testing.T) {
	const wrapped = "runInputNow"
	const wrapper = "runInput"

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing this package: %v", err)
	}

	callers := map[string]int{}
	scanned := 0
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			var enclosing string
			ast.Inspect(file, func(n ast.Node) bool {
				if fd, ok := n.(*ast.FuncDecl); ok {
					enclosing = fd.Name.Name
				}
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != wrapped {
					return true
				}
				scanned++
				callers[enclosing]++
				return true
			})
		}
	}

	if scanned == 0 {
		t.Fatalf("this row found no reference to %s at all — it was renamed, and the "+
			"invariant it holds is no longer being checked by anything", wrapped)
	}

	for name, n := range callers {
		if name != wrapper {
			t.Errorf("%s calls %s %d time(s), bypassing the deadline in %s.\n"+
				"Every git invocation has to carry one: a single call without it puts a "+
				"process the worker pool's WaitGroup can wait on for ever back into the "+
				"package, and every other row here would stay green while it did.\n"+
				"Route it through %s, or give this one its own deadline and say why here.",
				name, wrapped, n, wrapper, wrapper)
		}
	}
	if callers[wrapper] == 0 {
		t.Errorf("%s is never called from %s — the wrapper this invariant names is not the "+
			"one in use", wrapped, wrapper)
	}
}

// TestTheSpawnBoundIsAtTheSpawn asserts the ceiling is a property of the
// package rather than of one loop.
//
// walkTrees caps itself at eight workers, and that cap describes one
// caller. The semaphore describes the package, which is what makes a
// second concurrent caller safe without it having to remember anything.
func TestTheSpawnBoundIsAtTheSpawn(t *testing.T) {
	if got := cap(gitSpawnSlots); got <= 0 {
		t.Fatalf("gitSpawnSlots has capacity %d, so it bounds nothing", got)
	}
	// NO ASSERTION THAT THE SLOTS ARE FREE, and the first draft of this
	// row had one. It failed, correctly: the row above deliberately
	// leaves a goroutine stuck in a git call that never returns, and that
	// goroutine keeps its slot for the lifetime of the process — which is
	// the documented consequence of not being able to interrupt a thread
	// blocked in a syscall. "All slots are free" is therefore never a
	// safe thing to assert about a shared package-level bound, and a row
	// that asserted it would pass or fail on test ORDER.
	if cap(gitSpawnSlots) > 32 {
		t.Errorf("the spawn bound is %d, which is high enough that it is not really a "+
			"bound; the failure it exists for gets likelier with every concurrent fork",
			cap(gitSpawnSlots))
	}
}
