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

// unlockableTempDir is a temp directory whose removal is BEST EFFORT,
// for the directories a process that never returns holds open.
//
// t.TempDir removes its directory when the test ends and FAILS THE TEST
// if it cannot. On Windows it cannot, twice over, and both were found on
// CI rather than reasoned out here:
//
//   - the stub's own image: "unlinkat ...\git.exe" — a running
//     executable cannot be unlinked;
//   - the stub's WORKING DIRECTORY: "unlinkat ...\002: The process
//     cannot access the file because it is being used by another
//     process" — a process holds its cwd, and this stub is started with
//     cmd.Dir set to the repository under test.
//
// The stub is still running at cleanup BY DESIGN: a command that never
// returns is the one thing this row cannot do without, and making it
// exit early would make it a command that returns. So these directories
// outlive the test and the operating system reclaims them.
func unlockableTempDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatalf("making a temp directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

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

	// The stub's own image, which it holds open — see unlockableTempDir.
	binDir := unlockableTempDir(t, "leakscan-blocking-git")
	build := exec.Command("go", "build", "-o", filepath.Join(binDir, name), src)
	build.Dir = dir
	if out, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("building the blocking git stub: %v\n%s", buildErr, out)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// The stub must actually be what `git` resolves to now, or this row
	// would pass by testing the real git's speed.
	resolved, lookErr := exec.LookPath("git")
	if lookErr != nil {
		t.Fatalf("after putting the stub first on PATH, git does not resolve: %v", lookErr)
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

	// The stub runs with this as its working directory and never exits,
	// so it holds it open — see unlockableTempDir.
	//
	// THE DEADLINE IS INJECTED, and short. This row proves that a call
	// which never returns is cut off and named; it does not prove the
	// production number, which has its own row below. Running the real
	// thirty seconds here would cost thirty seconds on every leg of
	// every run to demonstrate a mechanism two seconds demonstrates
	// exactly as well.
	const injected = 2 * time.Second
	r := repo{dir: unlockableTempDir(t, "leakscan-blocked-repo"), timeout: injected}

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

	// Generous against the injected deadline and still far below any
	// suite cap: this bound exists so the row REPORTS rather than joins
	// the hang.
	const rowBound = 60 * time.Second

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
		t.Logf("gave up after %s, injected bound %s", got.elapsed.Round(time.Second), injected)

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

// TestTheProductionDeadlineIsStillThirtySeconds pins the number the row
// above stopped exercising.
//
// IT EXISTS BECAUSE THAT ROW STOPPED EXERCISING IT. Injecting a short
// deadline made the suite thirty seconds faster and, on its own, would
// have removed the only thing in this repository that touched the
// production value — a trade nobody would have written down, arriving as
// a side effect of a speed-up.
//
// So this asserts both halves: the constant is what it is, and a repo
// that does NOT ask for an override gets it. The second is the one that
// matters, because the override's whole safety argument is that its zero
// value means "the production deadline" rather than "no deadline".
func TestTheProductionDeadlineIsStillThirtySeconds(t *testing.T) {
	const want = 30 * time.Second

	if gitCallTimeout != want {
		t.Errorf("gitCallTimeout = %s, want %s.\n"+
			"The number is measured rather than chosen — a full scan makes 454 git "+
			"invocations whose slowest is 32.5ms, against a failure that ran for 25 "+
			"minutes — so changing it means re-measuring, not re-deciding.",
			gitCallTimeout, want)
	}

	// The zero value, which is every construction site in the program.
	if got := (repo{dir: "."}).deadline(); got != want {
		t.Errorf("a repo with no timeout set gets a deadline of %s, want %s — the override's "+
			"zero value must mean the production deadline, because a zero that meant NO "+
			"deadline would let a forgotten field reintroduce the hang", got, want)
	}

	// And an override is honoured, or the row above proves nothing.
	if got := (repo{dir: ".", timeout: time.Second}).deadline(); got != time.Second {
		t.Errorf("an injected deadline of 1s was reported as %s", got)
	}
}
