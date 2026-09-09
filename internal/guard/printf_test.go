package guard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// THE ANALYSER IS PART OF THIS PROJECT'S SAFETY ARGUMENT, so it is
// asserted rather than assumed.
//
// internal/ui escapes every string it is handed as an ARGUMENT. What no
// escaping can catch is a caller who hands somebody else's sentence over
// as the FORMAT — by then this program's prose and the far end's words
// are one string, and there is nothing left to tell apart. go vet's
// printf analyser can tell, because it knows the difference between a
// constant and a variable, and it is the only thing in this repository
// that can.

// runVet runs go vet and hands back everything it said.
func runVet(t *testing.T, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("go", append([]string{"vet"}, args...)...)
	cmd.Dir = moduleRoot(t)
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

// TestVetIsCleanAcrossTheRepository.
//
// REQUIRED MUTATION, run 2026-09-09: pass a server's message as the
// format — `render.Step(apiErr.Message)` in internal/flow. Reds here,
// naming the file and line, with "non-constant format string in call
// to". That is the seam closed: someone else's text cannot become this
// program's prose without the gate saying so.
func TestVetIsCleanAcrossTheRepository(t *testing.T) {
	out, ok := runVet(t, "./...")
	if !ok {
		t.Errorf("go vet ./... is not clean:\n%s", out)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("go vet ./... said something:\n%s", out)
	}
}

// TestVetStillRecognisesTheRenderingMethods is the presence half, and
// the row above is worth nothing without it.
//
// A clean vet run is satisfied by an analyser that has stopped looking.
// The detection is not declared anywhere — there is no directive for it,
// and -printf.funcs does not supply one for these methods; it is
// INFERRED from the shape of Step and Result, which a refactor can
// remove silently. So this drives a package that is excluded from every
// ordinary build and requires vet to report both calls by name.
//
// REQUIRED MUTATION, run 2026-09-09: in internal/ui, escape the
// arguments into a new slice and forward that instead — the tidier
// shape, and the one measured NOT to be detected. Reds here on both
// methods while everything else in the repository stays green, which is
// exactly the silent regression this row exists for.
func TestVetStillRecognisesTheRenderingMethods(t *testing.T) {
	out, _ := runVet(t, "-tags=printfprobe", "./internal/guard/printfprobe/")
	for _, want := range []string{
		"(*github.com/curiouspub/cli/internal/ui.UI).Step",
		"(*github.com/curiouspub/cli/internal/ui.UI).Result",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("go vet no longer reports a non-constant format string for %s.\n"+
				"The analyser is not seeing it as a printf wrapper any more, so a "+
				"caller passing a server's sentence as the format would now go "+
				"unreported everywhere.\nvet said:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "non-constant format string") {
		t.Errorf("vet reported nothing at all for the probe, so this row is not "+
			"measuring the analyser:\n%s", out)
	}
}

// TestNothingSpreadsASliceIntoTheRenderers.
//
// The escaping in Step and Result happens IN PLACE, because escaping
// into a new slice is what stops the analyser recognising them — the two
// properties are in tension and this is how both are kept. In place
// means a caller who spreads their own slice would have its elements
// rewritten under them.
//
// No call site does. This says so, rather than a comment hoping.
func TestNothingSpreadsASliceIntoTheRenderers(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	calls, spreads := 0, 0

	for _, path := range goFiles(t, root, true) {
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		// The enclosing function's own variadic parameter, if it has
		// one. Forwarding THAT is how a decorator is written and is
		// safe: the slice it forwards is the one its own caller made.
		var variadic string
		ast.Inspect(parsed, func(n ast.Node) bool {
			if fn, ok := n.(*ast.FuncDecl); ok {
				variadic = variadicParamName(fn)
				return true
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || call.Ellipsis == token.NoPos {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Step" && sel.Sel.Name != "Result") {
				return true
			}
			calls++
			last, ok := call.Args[len(call.Args)-1].(*ast.Ident)
			if ok && variadic != "" && last.Name == variadic {
				return true
			}
			spreads++
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s:%d spreads a slice into a renderer that is not this "+
				"function's own variadic parameter.\nThe escaping in Step and "+
				"Result happens IN PLACE — it has to, or go vet stops seeing them "+
				"as printf wrappers — so this would rewrite the caller's own "+
				"values. Pass the arguments individually.",
				filepath.ToSlash(rel), fset.Position(call.Pos()).Line)
			return true
		})
	}

	// The control. A walk that matched nothing would report a clean
	// repository for the wrong reason, and the forwarding decorators in
	// the flow suite are the ones it must find and permit.
	if calls < 2 {
		t.Fatalf("only %d spread calls into a renderer were examined, so this row "+
			"is looking in the wrong place", calls)
	}
	if spreads != 0 {
		t.Logf("%d of %d spreads were not a forwarded variadic", spreads, calls)
	}
}

// variadicParamName is the name of a function's variadic parameter, or
// empty if it has none.
func variadicParamName(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil || len(fn.Type.Params.List) == 0 {
		return ""
	}
	last := fn.Type.Params.List[len(fn.Type.Params.List)-1]
	if _, ok := last.Type.(*ast.Ellipsis); !ok {
		return ""
	}
	if len(last.Names) == 0 {
		return ""
	}
	return last.Names[0].Name
}
