package mcp

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// nextStepLabel opens the paragraph a refusal's advice is rendered in.
const nextStepLabel = "What to do next: "

// declaredNextActions is every value the failure type's action is
// declared to take, read out of the package that declares them.
//
// # The set comes from the type, never from a list typed here
//
// A list of four names written in this file would be a claim about
// whoever last remembered to edit it, and the value most likely to leak
// is the one added after the list was written. Go's reflection cannot
// enumerate a package's constants from outside it, which is why this
// parses rather than asks.
//
// WHAT IT LOOKS FOR IS THE TYPE OF THE DECLARATION rather than a name
// prefix: a constant declared with the action's type, or converted to
// it. A constant spelled without the usual prefix is still seen, and a
// string constant of any other type is not.
func declaredNextActions(t *testing.T) []string {
	t.Helper()
	dir := filepath.Join("..", "ui")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		matched, err := build.Default.MatchFile(dir, name)
		if err != nil {
			t.Fatalf("matching %s: %v", name, err)
		}
		if !matched {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		files = append(files, file)
	}
	values, err := nextActionsIn(files)
	if err != nil {
		t.Fatalf("reading the action's declared values: %v", err)
	}
	if len(values) == 0 {
		t.Fatalf("no constant of the action's type was found in %s, so the walk has "+
			"stopped matching the tree rather than the tree having none", dir)
	}
	return values
}

// nextActionTypeName is the type whose values are read. Named once, so
// the walk and its bench cannot disagree about it.
const nextActionTypeName = "NextAction"

// nextActionsIn reads the values of every constant declared with the
// action's type, or converted to it, in the files given.
//
// A CONSTANT IT CANNOT READ IS AN ERROR, not a skip. A value written as
// anything but a string literal — another constant, a concatenation — is
// a value this walk would otherwise leave out of the set, and a row
// sweeping for a set with a hole in it is green about the hole.
func nextActionsIn(files []*ast.File) ([]string, error) {
	seen := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			gen, isGen := decl.(*ast.GenDecl)
			if !isGen || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				typed := isIdentNamed(value.Type, nextActionTypeName)
				if typed && len(value.Values) == 0 {
					return nil, fmt.Errorf("%s is declared with the action's type and "+
						"no value of its own", value.Names[0].Name)
				}
				for _, expr := range value.Values {
					lit := expr
					if call, isCall := expr.(*ast.CallExpr); isCall &&
						isIdentNamed(call.Fun, nextActionTypeName) && len(call.Args) == 1 {
						lit = call.Args[0]
					} else if !typed {
						continue
					}
					basic, isBasic := lit.(*ast.BasicLit)
					if !isBasic || basic.Kind != token.STRING {
						return nil, fmt.Errorf("a value of the action's type is written "+
							"as something other than a string literal, so this walk "+
							"cannot read it: %T", lit)
					}
					text, err := strconv.Unquote(basic.Value)
					if err != nil {
						return nil, err
					}
					seen[text] = true
				}
			}
		}
	}
	values := make([]string, 0, len(seen))
	for v := range seen {
		values = append(values, v)
	}
	sort.Strings(values)
	return values, nil
}

func isIdentNamed(expr ast.Expr, name string) bool {
	ident, isIdent := expr.(*ast.Ident)
	return isIdent && ident.Name == name
}

// actionNamesIn reports every declared action value that a rendered
// result carries AS A VALUE: the value standing as a token of its own —
// a bare paragraph, the end of a labelled line, a value with a full stop
// or a bracket after it. That is the shape an enum takes in prose, and
// "What to do next: GiveUp" is the shape this surface used to print.
//
// IT IS NOT A SEARCH FOR THE WORD. Two of the values are English, and
// one shipped next-step sentence opens with one of them used as the verb
// it is ("Wait a few minutes and …"). A row that reddened on the word
// would red on correct output, and the only way past it would be
// exempting a value by hand — which is a list typed here again. So a
// value that a sentence carries straight on from, a space and a lower-case
// word, is a word; any other occurrence of it as a whole token is the
// value, punctuation after it included.
//
// IT SWEEPS THE WHOLE RESULT, the server's quoted sentence and the
// attached build log included, because a value can surface in any part.
// Those foreign parts are this file's own fixtures, so a fixture whose
// text is a bare "Wait." would red here; it would be the fixture to
// change, never the match.
func actionNamesIn(said string, names []string) []string {
	var found []string
	for _, name := range names {
		for from := 0; ; {
			at := strings.Index(said[from:], name)
			if at < 0 {
				break
			}
			start, end := from+at, from+at+len(name)
			from = end
			if start > 0 && isWordByte(said[start-1]) {
				continue
			}
			if end < len(said) && isWordByte(said[end]) {
				continue
			}
			if carriesOn(said[end:]) {
				continue
			}
			found = append(found, name)
		}
	}
	return found
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// carriesOn reports whether what follows a token is a sentence going on
// in lower case — " a few minutes" — which is what makes the token a word
// rather than a value. Punctuation in between does not count: "GiveUp,
// because …" is the value with a reason after it.
func carriesOn(rest string) bool {
	if !strings.HasPrefix(rest, " ") || len(rest) < 2 {
		return false
	}
	return rest[1] >= 'a' && rest[1] <= 'z'
}

// nextStepOf is the remainder of a result's next-step paragraph, and
// whether it has one.
func nextStepOf(said string) (string, bool) {
	for _, paragraph := range strings.Split(said, "\n\n") {
		if rest, ok := strings.CutPrefix(paragraph, nextStepLabel); ok {
			return rest, true
		}
	}
	return "", false
}

// TestTheActionWalkSeesWhatItClaimsTo is the instrument's own bench.
//
// The row that uses it runs over a tree where no refusal names an
// action, so on its own it can only show the walk saying yes. A walk
// that returned nothing would let that row report a clean surface for
// the wrong reason, and one that returned every string constant would
// make it red on words that are not actions at all.
func TestTheActionWalkSeesWhatItClaimsTo(t *testing.T) {
	const source = `package ui

type NextAction string
type Other string

const (
	First NextAction = "First"
	Second NextAction = "Second"
	Decoy Other = "Decoy"
	untyped = "Untyped"
)

const Converted = NextAction("Converted")

var NotAConstant NextAction = "NotAConstant"
`
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got, err := nextActionsIn([]*ast.File{file})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Converted", "First", "Second"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("the walk read %q from the bench, want %q", got, want)
	}

	// AND IT REFUSES what it cannot read, rather than leaving it out.
	const unreadable = `package ui

type NextAction string

const prefix = "Gi"

const GiveUp NextAction = prefix + "veUp"
`
	file, err = parser.ParseFile(token.NewFileSet(), "fixture.go", unreadable, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := nextActionsIn([]*ast.File{file}); err == nil {
		t.Errorf("a value written as a concatenation was read as %q rather than "+
			"refused, so a value the walk cannot see would leave the set short", got)
	}

	// AND THE MATCH: the value as a token of its own reds wherever it
	// stands; a value a sentence carries on from does not.
	names := []string{"Wait", "GiveUp", "None"}
	for _, c := range []struct {
		said string
		want int
	}{
		{"Something failed.\n\n" + nextStepLabel + "GiveUp", 1},
		{"Something failed.\n\nGiveUp", 1},
		{nextStepLabel + "GiveUp.", 1},
		{"Action: GiveUp (terminal)", 1},
		{nextStepLabel + "Wait\n\nFailure ID: x", 1},
		{nextStepLabel + "Wait a few minutes and try again.", 0},
		{"None of the files could be read.", 0},
		{"Action: GiveUp, because retrying cannot help.", 1},
		{"GiveUpNow and Waiting are other words.", 0},
		{"Something failed.\n\n" + nextStepLabel + "Try again in a moment.", 0},
	} {
		if got := actionNamesIn(c.said, names); len(got) != c.want {
			t.Errorf("the match found %q in %q, want %d", got, c.said, c.want)
		}
	}
}

// TestNoRefusalNamesAnActionInItsProse.
//
// # The prose says what to do in words, and the value stays a field
//
// A refusal's advice used to arrive on this surface as the action's
// value — "What to do next: GiveUp" — which is an identifier, sitting in
// prose a model reads and may repeat to a person word for word. The
// paragraph now carries the failure's own next-step sentence, escaped as
// the paragraphs around it are. The value is unchanged where it is a
// field: on the failure, and under the failure's id in the published
// catalogue, which the failure-id line the refusal prints is the handle to.
//
// # Every failure this surface can render is covered, and this is why
//
// Every failure reaches a caller through one branch of the refusal
// renderer, which reads the failure's parts and its id and never who
// built it. So constructors differ only in what they put in those fields,
// and the field that must never surface is the action: a failure carrying
// each declared value, rendered through that branch, is what every
// constructor's refusal looks like if the value ever does. The failure
// contract, checked elsewhere in this repository, holds
// every construction site to one of those values and a next-step
// sentence that is not blank. The values come from the type, so a fifth
// one is driven the day it is declared.
//
// The scripted runs below are the same branch reached the way a client
// reaches it, through the paths the scripted server here can end in a
// failure: each refusal at the publish, each attribution of a failed
// build, the pre-flight and both login calls. The status tool reaches the
// same branch through a refused stream, which this server never refuses;
// the failures above, one per declared value, are what cover it.
//
// REQUIRED MUTATION, run 2026-10-07: render string(failure.Next) in the
// next-step paragraph again. Reds here, naming the value it found.
func TestNoRefusalNamesAnActionInItsProse(t *testing.T) {
	names := declaredNextActions(t)
	// THE POSITIVE CONTROL for the walk: the full-store refusal's action
	// is one the type declares. If it were not, this row would be
	// sweeping for a set that cannot contain the value it most needs to.
	if !contains(names, string(ui.NextGiveUp)) {
		t.Fatalf("the walk read %q, which leaves out the value the full-store "+
			"refusal carries", names)
	}

	refused := func(t *testing.T, result Result) string {
		t.Helper()
		said := text(t, result)
		if !result.IsError {
			t.Fatalf("the call succeeded, so there is no refusal here to read:\n%s", said)
		}
		return said
	}
	publishRefused := func(t *testing.T, status int, code wire.ErrorCode, message string) string {
		t.Helper()
		run := newToolsRun(t, &deployScript{
			uploadPath: "/object-store/put",
			deployID:   "dpl-refused-at-publish",
			frames: []string{
				phaseAt(wire.PhaseInstalling),
				finished(wire.StatusBuilt),
			},
			refusePublish: &scriptedRefusal{status: status, code: code, message: message},
		})
		return refused(t, run.call(toolDeploySite,
			fmt.Sprintf(`{"dir":%q}`, project(t, "localhost-hits"))))
	}
	buildFailed := func(t *testing.T, origin wire.FailureOrigin) string {
		t.Helper()
		done, err := json.Marshal(wire.DoneEvent{Status: wire.StatusFailed, Origin: origin})
		if err != nil {
			t.Fatal(err)
		}
		run := newToolsRun(t, &deployScript{
			uploadPath: "/object-store/put",
			deployID:   "dpl-build-failed",
			frames: []string{
				phaseAt(wire.PhaseBuilding),
				frame(string(wire.EventDone), string(done)),
			},
		})
		return refused(t, run.call(toolDeploySite,
			fmt.Sprintf(`{"dir":%q}`, project(t, "localhost-hits"))))
	}

	type rendered struct {
		name string
		run  func(t *testing.T) []string
		// check is what this case asserts beyond the sweep, if anything.
		check func(t *testing.T, said string)
	}
	var cases []rendered

	// EVERY DECLARED VALUE, through the branch every failure takes.
	for _, name := range names {
		cases = append(cases, rendered{
			name: "a failure whose action is " + name,
			run: func(t *testing.T) []string {
				return []string{refusalText(ui.NewFailure(ui.IDInternalFault, ui.StageThisRun,
					"Something went wrong.", "For a reason.", ui.NextAction(name),
					"Do the one thing that helps."))}
			},
			check: func(t *testing.T, said string) {
				if next, ok := nextStepOf(said); !ok || next != "Do the one thing that helps." {
					t.Errorf("the next-step paragraph is %q, want the failure's own "+
						"sentence:\n%s", next, said)
				}
			},
		})
	}

	cases = append(cases,
		rendered{
			// THE REFUSAL A REAL SERVER RETURNED, rebuilt from its code
			// alone: a full store at the publish, the one input that still
			// produces this failure id and its give-up action.
			name: "the publish refused because the store is full",
			run: func(t *testing.T) []string {
				return []string{publishRefused(t, http.StatusServiceUnavailable,
					wire.CodeStoreFull, "the store is full")}
			},
			check: func(t *testing.T, said string) {
				if !strings.Contains(said, ui.FailureIDLine(ui.IDDeployNotCompletedByServer)) {
					t.Fatalf("this refusal does not carry the id it exists to show, "+
						"so nothing below is about it:\n%s", said)
				}
				if strings.Contains(said, string(ui.NextGiveUp)) {
					t.Errorf("the refusal shows the action's value %q:\n%s",
						ui.NextGiveUp, said)
				}
				// WORD FOR WORD, not a prefix: a prefix passes a sentence cut
				// short, and the whole sentence is what this refusal now says.
				const fullStore = "curious.pub is at capacity and cannot publish sites " +
					"right now. There is\nnothing to fix at this end and nothing here " +
					"worth retrying. If it keeps\nhappening, please get in touch."
				if next, ok := nextStepOf(said); !ok || next != fullStore {
					t.Errorf("the next-step paragraph is %q, want the failure's own "+
						"sentence about a full store, %q:\n%s", next, fullStore, said)
				}
			},
		},
		rendered{
			name: "the publish refused as a server fault",
			run: func(t *testing.T) []string {
				return []string{publishRefused(t, http.StatusInternalServerError,
					wire.CodeInternal, "the publisher fell over")}
			},
		},
		rendered{
			name: "the publish refused because the site was withdrawn",
			run: func(t *testing.T) []string {
				return []string{publishRefused(t, http.StatusGone,
					wire.CodeSiteWithdrawn, "the site is gone")}
			},
		},
		rendered{
			// THE NEGATIVE CONTROL. This sentence opens with a declared
			// value used as the English verb it is, and the row must stay
			// green on it — or the only way past would be exempting the
			// value by hand.
			name: "a build the service could not run",
			run: func(t *testing.T) []string {
				return []string{buildFailed(t, wire.OriginService)}
			},
			check: func(t *testing.T, said string) {
				next, _ := nextStepOf(said)
				for _, name := range names {
					if strings.HasPrefix(next, name+" ") {
						return
					}
				}
				t.Fatalf("no next-step sentence here opens with a declared value used "+
					"as a word, so this case no longer shows the match leaves one "+
					"alone:\n%s", said)
			},
		},
		rendered{
			name: "a build the project failed",
			run: func(t *testing.T) []string {
				return []string{buildFailed(t, wire.OriginProject)}
			},
		},
		rendered{
			name: "a build nobody said the cause of",
			run: func(t *testing.T) []string {
				return []string{buildFailed(t, wire.OriginUnstated)}
			},
		},
		rendered{
			name: "a project the pre-flight refuses",
			run: func(t *testing.T) []string {
				run := newToolsRun(t, &deployScript{uploadPath: "/object-store/put"})
				return []string{refused(t, run.call(toolDeploySite,
					fmt.Sprintf(`{"dir":%q}`, t.TempDir())))}
			},
		},
		rendered{
			name: "the server refusing both login calls",
			run: func(t *testing.T) []string {
				run := newToolsRun(t, refusingScript(http.StatusTooManyRequests,
					wire.CodeRateLimited, "slow down"))
				return []string{
					refused(t, run.call(toolLoginStart, `{"email":"someone@example.com"}`)),
					refused(t, run.call(toolLoginVerify,
						`{"email":"someone@example.com","code":"123456"}`)),
				}
			},
		},
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i, said := range tc.run(t) {
				if _, ok := nextStepOf(said); !ok {
					t.Errorf("result %d has no next-step paragraph, so this sweep "+
						"read nothing where the value used to be:\n%s", i, said)
				}
				for _, name := range actionNamesIn(said, names) {
					t.Errorf("result %d names the action %q in its prose. The "+
						"paragraph says what to do in words; the value stays in the "+
						"failure's own field, behind the failure id this refusal "+
						"prints:\n%s", i, name, said)
				}
				if tc.check != nil {
					tc.check(t, said)
				}
			}
		})
	}
}
