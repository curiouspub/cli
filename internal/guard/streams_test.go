package guard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// ONE PACKAGE PUTS BYTES IN FRONT OF A PERSON, and this is the guard on
// that being true.
//
// internal/ui escapes everything it renders. That is worth exactly as
// much as the number of places that can reach a terminal WITHOUT going
// through it — one bypass and the boundary is decoration.
//
// WHY THIS IS AN AST WALK AND NOT A GREP. It was a grep, for the two
// strings `os.Stdout` and `os.Stderr`, and a cold reviewer pointed out
// what that cannot see: `fmt.Println`, the package-level `log` calls,
// `os.NewFile(2, …)` and the `println` builtin all reach the process's
// real streams without either string appearing anywhere. A file taking
// any of those routes was skipped, silently, by a row whose name claims
// the opposite. The enumeration below is the set of routes, asserted as
// a set rather than as the two somebody thought of first.
//
// AND THE EXCEPTIONS CARRY A REASON THE ROW CHECKS. The old list carried
// prose. One entry said `cmd/curious/main.go` "renders no server-supplied
// string" — which was FALSE: it hands its raw stderr to the MCP server,
// which logs a client-controlled method name straight down it. A reason
// nobody can check is a reason nobody did.

// exceptionReason is a claim about a file that this row can verify.
type exceptionReason int

const (
	// passesThemOn: the file names a standard stream and never writes to
	// one. It obtains them and hands them to something else — which is
	// what an entry point does, and is only safe because whatever
	// receives them is itself held to this rule.
	passesThemOn exceptionReason = iota + 1

	// swapsThemForCapture: the file ASSIGNS to a standard stream. That is
	// a row capturing output in order to assert on it, which is the
	// opposite of a bypass: it is how the boundary gets measured.
	swapsThemForCapture

	// outsideTheShippedCLI: the file is a developer tool under tools/.
	// Nothing it prints is on a path a user runs, and it renders nothing
	// a server said.
	outsideTheShippedCLI

	// aHelperBinary: a package that exists only to be re-executed by its
	// own test, reporting its own setup before the process it stands in
	// for exists.
	aHelperBinary
)

// streamExceptions names every file allowed to reach a standard stream,
// with the reason — and the reason is checked, not read.
var streamExceptions = map[string]exceptionReason{
	"cmd/curious/main.go": passesThemOn,

	"tools/skipcheck/main.go":    outsideTheShippedCLI,
	"tools/surfacecheck/main.go": outsideTheShippedCLI,

	"internal/flow/preflight_test.go":  swapsThemForCapture,
	"internal/flow/upload_test.go":     swapsThemForCapture,
	"internal/mcp/interactive_test.go": swapsThemForCapture,

	"internal/api/proxytest/main_test.go": aHelperBinary,
}

// reach is one way a file was found to touch a standard stream.
type reach struct {
	how  string
	line int
	// write is true when the stream is being written to rather than
	// merely named.
	write bool
	// assign is true when the stream is being replaced.
	assign bool
	// literalsOnly is true when every argument of a write is a constant.
	literalsOnly bool
}

// reachesAStandardStream enumerates the routes. THE LIST IS THE POINT:
// each entry is a way to reach the process's own output that does not
// name os.Stdout or os.Stderr, or does.
func reachesAStandardStream(file *ast.File, fset *token.FileSet) []reach {
	var found []reach
	at := func(n ast.Node) int { return fset.Position(n.Pos()).Line }

	ast.Inspect(file, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.AssignStmt:
			for _, target := range e.Lhs {
				if standardStream(target) != "" {
					found = append(found, reach{
						how: "replaces " + standardStream(target), line: at(e), assign: true})
				}
			}

		case *ast.CallExpr:
			switch fun := e.Fun.(type) {
			case *ast.Ident:
				// The builtins. print and println write to stderr.
				if fun.Name == "print" || fun.Name == "println" {
					found = append(found, reach{
						how:  "the " + fun.Name + " builtin, which writes to stderr",
						line: at(e), write: true, literalsOnly: allLiterals(e.Args)})
				}
			case *ast.SelectorExpr:
				pkg, ok := fun.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch pkg.Name {
				case "fmt":
					// Print, Println and Printf go to the process's own
					// stdout with nothing named.
					if fun.Sel.Name == "Print" || fun.Sel.Name == "Println" ||
						fun.Sel.Name == "Printf" {
						found = append(found, reach{
							how:  "fmt." + fun.Sel.Name + ", which writes to the process's stdout",
							line: at(e), write: true, literalsOnly: allLiterals(e.Args)})
						return true
					}
					// The Fprint family names its target.
					if strings.HasPrefix(fun.Sel.Name, "Fprint") && len(e.Args) > 0 {
						if stream := standardStream(e.Args[0]); stream != "" {
							found = append(found, reach{
								how:  "fmt." + fun.Sel.Name + " to " + stream,
								line: at(e), write: true, literalsOnly: allLiterals(e.Args[1:])})
						}
					}
				case "log":
					// THE WRITING ONES, named. log.New and log.Flags are
					// not writes — the first takes its own destination —
					// and treating "anything on the log package" as a
					// write reported a row that builds a logger over a
					// buffer. The set is small enough to write down.
					if logWrites[fun.Sel.Name] {
						found = append(found, reach{
							how:  "log." + fun.Sel.Name + ", which writes to stderr",
							line: at(e), write: true, literalsOnly: allLiterals(e.Args)})
					}
				case "os":
					// os.NewFile(1|2, …) reopens a standard descriptor
					// under a different name.
					if fun.Sel.Name == "NewFile" && len(e.Args) > 0 {
						if lit, ok := e.Args[0].(*ast.BasicLit); ok &&
							(lit.Value == "1" || lit.Value == "2") {
							found = append(found, reach{
								how: "os.NewFile(" + lit.Value + ", …), a standard descriptor " +
									"under another name", line: at(e), write: true})
						}
					}
				}
			}

		case *ast.SelectorExpr:
			if stream := standardStream(e); stream != "" {
				found = append(found, reach{how: "names " + stream, line: at(e)})
			}
		}
		return true
	})
	return found
}

// logWrites is every package-level log function that writes to the
// standard logger, whose destination is stderr unless somebody has
// changed it. A logger built with log.New writes where it was told and
// is not one of these.
var logWrites = map[string]bool{
	"Print": true, "Printf": true, "Println": true,
	"Fatal": true, "Fatalf": true, "Fatalln": true,
	"Panic": true, "Panicf": true, "Panicln": true,
	"Output": true,
}

// standardStream names the stream an expression is, or "".
func standardStream(e ast.Expr) string {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "os" {
		return ""
	}
	if sel.Sel.Name == "Stdout" || sel.Sel.Name == "Stderr" {
		return "os." + sel.Sel.Name
	}
	return ""
}

func allLiterals(args []ast.Expr) bool {
	for _, arg := range args {
		if _, ok := arg.(*ast.BasicLit); !ok {
			return false
		}
	}
	return true
}

// TestNothingOutsideTheUIPackageReachesATerminal.
//
// REQUIRED MUTATION, run 2026-09-10: put `fmt.Println(apiErr.Message)`
// in a flow file. Reds naming the file, the line and the route — where
// the grep this replaced saw nothing at all, because that line names
// neither stream.
func TestNothingOutsideTheUIPackageReachesATerminal(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	scanned, used := 0, map[string]bool{}

	for _, path := range goFiles(t, root, true) {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("relative path for %s: %v", path, err)
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/ui/") {
			continue
		}
		scanned++
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", rel, err)
		}
		found := reachesAStandardStream(parsed, fset)
		if len(found) == 0 {
			continue
		}
		reason, allowed := streamExceptions[rel]
		if !allowed {
			for _, r := range found {
				t.Errorf("%s:%d reaches a standard stream — %s.\n"+
					"Everything a person reads goes through internal/ui, which is "+
					"the one place it is escaped; a write that goes round it is a "+
					"terminal control sequence away from the screen. If this file "+
					"genuinely needs the stream, add it to streamExceptions with a "+
					"reason this row can check.", rel, r.line, r.how)
			}
			continue
		}
		used[rel] = true
		if why := reasonFails(reason, rel, found); why != "" {
			t.Errorf("%s is excepted for a reason that is no longer true: %s.\n"+
				"The exception list used to carry prose, and one entry claimed a "+
				"file rendered nothing a server said while it handed its raw "+
				"stderr to something that did. A reason nobody can check is a "+
				"reason nobody did.", rel, why)
		}
	}

	// THE ALLOWLIST IS ASSERTED AS A SET. An entry for a file that no
	// longer reaches a stream is a permission nobody is using.
	for rel := range streamExceptions {
		if !used[rel] {
			t.Errorf("streamExceptions permits %s, which no longer reaches a "+
				"standard stream. Remove the entry: an allowance nobody uses is "+
				"an allowance nobody is checking.", rel)
		}
	}

	// The control for the walk itself.
	if scanned < 40 {
		t.Fatalf("only %d files were scanned, so this row is looking in the "+
			"wrong place", scanned)
	}
}

// reasonFails reports why an exception's stated reason does not hold, or
// "" when it does.
func reasonFails(reason exceptionReason, rel string, found []reach) string {
	switch reason {
	case passesThemOn:
		for _, r := range found {
			if r.write {
				return "it claims to pass the streams on, and line " +
					itoa(r.line) + " writes to one (" + r.how + ")"
			}
		}
		return ""

	case swapsThemForCapture:
		for _, r := range found {
			if r.assign {
				return ""
			}
		}
		return "it claims to swap the streams for capture, and it never assigns to one"

	case outsideTheShippedCLI:
		if strings.HasPrefix(rel, "tools/") {
			return ""
		}
		return "it claims to be outside the shipped CLI and is not under tools/"

	case aHelperBinary:
		if strings.HasSuffix(rel, "_test.go") && strings.Contains(rel, "test/") {
			return ""
		}
		return "it claims to be a re-executed helper and is not a test file in a " +
			"helper package"
	}
	return "the reason is not one this row knows how to check"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
