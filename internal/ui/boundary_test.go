package ui

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// The rendering boundary: every exported way this package puts
// caller-supplied text in front of a person, driven with the bytes a
// terminal obeys.
//
// WHY THE PROOF LIVES HERE AND NOT AT THE CALL SITES. It used to sit in
// internal/flow, where one path — the build log — escaped its own lines
// before handing them over. That proved the call site, not the property:
// the failure copy three packages along carried a server's sentence with
// no escaping at all, and a row asserting "the build log is escaped" was
// green the whole time. So the escaping moved into the renderer, and the
// proof moved with it, ranged over the whole exported surface rather
// than one method of it.

// The vocabulary this boundary exists for, named rather than generated,
// because each of these does something different to a terminal and a
// reader should be able to see which.
const (
	// CSI: clears the screen. The classic build-log injection.
	hostileCSI = "before\x1b[2Jafter"

	// OSC: retitles the window, and the title outlives the command. This
	// one arrived from a review of the publish step, where a server's
	// error message reached the terminal with these bytes intact.
	hostileOSC = "before\x1b]0;pwned\x07after"

	// BEL and DEL on their own — a terminal obeys the first and prints
	// nothing for the second, so neither can be audited by looking.
	hostileBEL = "before\x07after"
	hostileDEL = "before\x7fafter"
)

func hostileInputs() []string {
	return []string{hostileCSI, hostileOSC, hostileBEL, hostileDEL}
}

// aRenderer is one exported method that shows caller-supplied text, and
// how to drive it. render returns everything BOTH streams received, so a
// method that wrote to the wrong one is still caught by these rows
// rather than passing for lack of looking.
type aRenderer struct {
	name   string
	render func(t *testing.T, text string) string
}

func bothStreams(out, errOut *bytes.Buffer) string {
	return out.String() + errOut.String()
}

func renderers() []aRenderer {
	return []aRenderer{
		// THE ARGUMENT FORM. The prose form has a row of its own below,
		// because a variable cannot be a format string in this project
		// any more — go vet reports it, which is the whole of the guard
		// two files over. Driving it here would be writing the thing the
		// analyser exists to refuse.
		{"Step/argument", func(t *testing.T, text string) string {
			u, out, errOut := testUI("", false, nil)
			u.Step("%s", text)
			return bothStreams(out, errOut)
		}},

		{"Result/argument", func(t *testing.T, text string) string {
			u, out, errOut := testUI("", false, nil)
			u.Result("%s", text)
			return bothStreams(out, errOut)
		}},

		{"Help", func(t *testing.T, text string) string {
			// Prose, because that is the only thing Help takes: it is
			// the exception to one-record-per-line and is reachable
			// only deliberately.
			u, out, errOut := testUI("", false, nil)
			u.Help(Prose(text))
			return bothStreams(out, errOut)
		}},
		{"Fail", func(t *testing.T, text string) string {
			u, out, errOut := testUI("", false, nil)
			u.Fail(NewFailure(text, text, NextGiveUp, text))
			return bothStreams(out, errOut)
		}},
		{"Internal", func(t *testing.T, text string) string {
			// Under the debug switch, because that is the only branch
			// that renders anything the caller supplied.
			u, out, errOut := testUI("", false, map[string]string{debugEnvVar: "1"})
			u.Internal(errors.New(text))
			return bothStreams(out, errOut)
		}},
		{"Confirm", func(t *testing.T, text string) string {
			u, out, errOut := testUI("y\n", true, nil)
			if _, err := u.Confirm(text, true); err != nil {
				t.Fatalf("Confirm: %v", err)
			}
			return bothStreams(out, errOut)
		}},
		{"Line", func(t *testing.T, text string) string {
			u, out, errOut := testUI("answer\n", true, nil)
			if _, err := u.Line(text); err != nil {
				t.Fatalf("Line: %v", err)
			}
			return bothStreams(out, errOut)
		}},
		{"Email", func(t *testing.T, text string) string {
			u, out, errOut := testUI("someone@example.com\n", true, nil)
			if _, err := u.Email(text); err != nil {
				t.Fatalf("Email: %v", err)
			}
			return bothStreams(out, errOut)
		}},
	}
}

// rendersNoCallerText names the exported methods that show a person
// something without being handed a string to show. They are listed so
// the completeness check below can be about the WHOLE surface rather
// than about the part somebody remembered.
var rendersNoCallerText = map[string]string{
	"Cancelled":   "one lowercase word, compiled in",
	"ExitCode":    "routes to the methods above and returns a number",
	"Interactive": "answers a question, writes nothing",
	"Interrupts":  "installs a signal handler, writes nothing",
}

// TestEveryExportedRendererIsInTheTable reads this package's own source
// and proves the table above holds every exported method on *UI.
//
// WITHOUT IT THE ROWS BELOW ARE ABOUT WHATEVER SOMEBODY LISTED. A method
// added next year that writes a caller's string to stderr would be
// covered by none of them and reported by nothing — which is the exact
// shape of the defect this whole file exists because of.
func TestEveryExportedRendererIsInTheTable(t *testing.T) {
	covered := map[string]bool{}
	for _, r := range renderers() {
		// A method may appear more than once — once per FORM it has.
		covered[strings.SplitN(r.name, "/", 2)[0]] = true
	}

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing this package: %v", err)
	}
	fset := token.NewFileSet()
	found := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(fset, file, nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", file, parseErr)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() {
				continue
			}
			if !receiverIsUI(fn.Recv) {
				continue
			}
			found++
			name := fn.Name.Name
			if covered[name] {
				continue
			}
			if _, stated := rendersNoCallerText[name]; stated {
				continue
			}
			t.Errorf("%s is an exported method on *UI and is in neither the "+
				"renderers table nor rendersNoCallerText — so nothing here asks "+
				"whether it escapes what it is given (%s)", name,
				fset.Position(fn.Pos()))
		}
	}
	// The control: a glob that matched nothing, or a parser that found no
	// methods, would report a clean surface for the wrong reason.
	if found < len(covered) {
		t.Fatalf("the source scan found %d exported methods on *UI and the table "+
			"names %d — the scan is looking in the wrong place", found, len(covered))
	}
}

func receiverIsUI(recv *ast.FieldList) bool {
	if len(recv.List) != 1 {
		return false
	}
	expr := recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "UI"
}

// TestNoRendererLetsATerminalControlByteThrough is the ruled row: every
// method in the table, fed every hostile vocabulary, and nothing a
// terminal obeys arrives.
//
// REQUIRED MUTATION, run 2026-09-09: remove the SanitizeLines call from
// renderFailure. Reds on Fail for all four inputs, and on nothing else —
// which is the shape that proves the rows are per-method rather than
// aggregate. Removing it from Step and Result instead reds those two.
func TestNoRendererLetsATerminalControlByteThrough(t *testing.T) {
	for _, r := range renderers() {
		for _, hostile := range hostileInputs() {
			t.Run(r.name+":"+strings.TrimPrefix(printable(hostile), "before"), func(t *testing.T) {
				got := r.render(t, hostile)
				if got == "" {
					t.Fatalf("%s wrote nothing, so this row asserts an absence "+
						"that would hold for a method that renders at all", r.name)
				}
				for i := 0; i < len(got); i++ {
					b := got[i]
					if b == '\n' {
						continue
					}
					if b < 0x20 || b == 0x7f {
						t.Fatalf("%s let byte %#02x reach the terminal: %q",
							r.name, b, got)
					}
				}
				// INERT, NOT DELETED. An absence check is satisfied by a
				// method that throws the text away, and a person then
				// cannot see what the far end actually said.
				for _, want := range []string{"before", "after"} {
					if !strings.Contains(got, want) {
						t.Errorf("%s dropped %q instead of escaping it: %q",
							r.name, want, got)
					}
				}
				if strings.Contains(got, "beforeafter") {
					t.Errorf("%s deleted the sequence rather than rendering it: %q",
						r.name, got)
				}
			})
		}
	}
}

// printable names a hostile fixture for a subtest name.
func printable(s string) string {
	return strings.NewReplacer("\x1b", "ESC", "\x07", "BEL", "\x7f", "DEL",
		"[", "_", "]", "_", ";", "_").Replace(s)
}

// TestEveryControlByteIsEscapedAtTheBoundary is the ranged form: all 33
// of them, one at a time, through the method that carries somebody
// else's build output.
//
// It moved here from internal/flow when the escaping did. The line count
// is asserted first for the reason it was originally: an unescaped
// newline in somebody's build output silently becomes a second line, and
// that is a stricter thing to notice than any per-byte check.
func TestEveryControlByteIsEscapedAtTheBoundary(t *testing.T) {
	var control []byte
	for b := 0x00; b < 0x20; b++ {
		control = append(control, byte(b))
	}
	control = append(control, 0x7f)
	if len(control) != 33 {
		t.Fatalf("the row built %d control bytes, want 33", len(control))
	}

	u, out, _ := testUI("", false, nil)
	for _, b := range control {
		u.Result("%s", "a"+string([]byte{b})+"z")
	}

	printed := out.String()
	lines := strings.Split(strings.TrimSuffix(printed, "\n"), "\n")
	if len(lines) != len(control) {
		t.Fatalf("the terminal received %d lines, want one per control byte (%d):\n%q",
			len(lines), len(control), printed)
	}
	for i, b := range control {
		line := lines[i]
		if line == "az" {
			t.Errorf("byte %#02x was dropped rather than escaped: %q — deleting the "+
				"byte satisfies an absence check and loses what it said", b, line)
			continue
		}
		if !strings.HasPrefix(line, "a") || !strings.HasSuffix(line, "z") {
			t.Errorf("byte %#02x took its neighbours with it: %q", b, line)
			continue
		}
		for j := 0; j < len(line); j++ {
			if line[j] < 0x20 || line[j] == 0x7f {
				t.Errorf("byte %#02x reached the terminal unescaped: %q", b, line)
				break
			}
		}
	}
}

// TestOrdinaryTextReachesTheTerminalUnchanged is the positive control for
// every row in this file: without it, a boundary that escaped or dropped
// everything would pass all of them. Multibyte text is included because
// escaping BYTES rather than runes is what keeps it intact.
func TestOrdinaryTextReachesTheTerminalUnchanged(t *testing.T) {
	lines := []string{
		"[build] 12 pages built in 1.20s",
		"düğüm — 日本語 — 🚀",
		`C:\Users\build\output`,
	}
	u, out, _ := testUI("", false, nil)
	for _, line := range lines {
		u.Result("%s", line)
	}
	want := strings.Join(lines, "\n") + "\n"
	if got := out.String(); got != want {
		t.Errorf("ordinary output was rewritten on its way out:\n got %q\nwant %q",
			got, want)
	}
}

// TestTheBoundaryIsIdempotent. The far end escapes the same vocabulary,
// so text arriving here has almost always been through an instance of
// this already. On clean input an identity function would pass, which is
// why the fixture carries a real ESC and why the first result is
// asserted to differ from the input before the second is compared to it.
func TestTheBoundaryIsIdempotent(t *testing.T) {
	const dirty = "before\x1b[2Jafter\x00\x07\x7f end"

	once := renderOnce(t, dirty)
	if once == dirty+"\n" {
		t.Fatalf("the fixture came through unchanged, so this row is about clean "+
			"input: %q", once)
	}
	if strings.ContainsRune(once, 0x1b) {
		t.Fatalf("the first pass left a raw ESC: %q", once)
	}
	twice := renderOnce(t, strings.TrimSuffix(once, "\n"))
	if twice != once {
		t.Errorf("text the far end had already escaped was mangled again:\n"+
			"  once:  %q\n  twice: %q", once, twice)
	}
}

func renderOnce(t *testing.T, text string) string {
	t.Helper()
	u, out, _ := testUI("", false, nil)
	u.Result("%s", text)
	return out.String()
}

// TestAFailureKeepsItsParagraphs. The boundary escapes the whole C0 set,
// newline included, so a renderer that ran Sanitize over an assembled
// failure would deliver three paragraphs as one line of backslash-n.
// This is the row that says the layout survives.
func TestAFailureKeepsItsParagraphs(t *testing.T) {
	u, _, errOut := testUI("", false, nil)
	u.Fail(NewFailure("What.", "First line.\n\nSecond paragraph.", NextGiveUp, "Do this."))
	got := errOut.String()
	if strings.Contains(got, `\n`) {
		t.Errorf("a line break was escaped as text: %q", got)
	}
	if want := "First line.\n\nSecond paragraph."; !strings.Contains(got, want) {
		t.Errorf("the paragraphs did not survive: %q", got)
	}
}

// TestAnArgumentCannotAddALineOfItsOwn is the property compose exists
// for, and the one the escape rows above cannot see because they skip
// the newline.
//
// A NEWLINE IS NOT A CONTROL SEQUENCE AND IT IS STILL A FORGERY. This
// program's narration is one claim per line, so a status or a label that
// arrives with a line break in it can add a line that reads as something
// curious said. The format is this program's words and may have layout
// in it; an argument is somebody else's and may not.
//
// REQUIRED MUTATION, run 2026-09-09: stop escaping string arguments in
// compose. Reds here — and, before this row existed, reddened NOTHING,
// which is why it is here.
func TestAnArgumentCannotAddALineOfItsOwn(t *testing.T) {
	for _, r := range []struct {
		name  string
		lines func(u *UI, out, errOut *bytes.Buffer) string
	}{
		{"Step", func(u *UI, out, errOut *bytes.Buffer) string {
			u.Step("The build finished: %s.", "ok\nPublished. https://not-really.example")
			return bothStreams(out, errOut)
		}},
		{"Result", func(u *UI, out, errOut *bytes.Buffer) string {
			u.Result("%s", "ok\nPublished. https://not-really.example")
			return bothStreams(out, errOut)
		}},
	} {
		t.Run(r.name, func(t *testing.T) {
			u, out, errOut := testUI("", false, nil)
			got := r.lines(u, out, errOut)
			if n := strings.Count(strings.TrimSuffix(got, "\n"), "\n"); n != 0 {
				t.Errorf("%s let an argument add %d line(s) of its own: %q",
					r.name, n, got)
			}
			// Inert, not deleted: the text still has to arrive.
			if !strings.Contains(got, "Published.") {
				t.Errorf("%s dropped the argument instead of escaping it: %q",
					r.name, got)
			}
		})
	}
}

// TestThisProgramsOwnProseKeepsItsLayout is the other side of the rule
// above: the format is this program's words, and a Step written as prose
// with paragraphs in it must arrive with them.
func TestThisProgramsOwnProseKeepsItsLayout(t *testing.T) {
	u, _, errOut := testUI("", false, nil)
	u.Step("First line.\n\nSecond paragraph.")
	got := errOut.String()
	if strings.Contains(got, `\n`) {
		t.Errorf("a line break in this program's own copy was escaped: %q", got)
	}
	if !strings.Contains(got, "First line.\n\nSecond paragraph.") {
		t.Errorf("the paragraphs did not survive: %q", got)
	}
}

// TestTheProseFormEscapesToo drives the OTHER form of the two methods
// that have two: the caller's whole string as the format, nothing to
// interpolate.
//
// EVERY CALL BELOW USES A CONSTANT, and it has to. A variable format is
// exactly what go vet now reports for these methods, everywhere in the
// repository — so a row that drove this path with a variable would be
// writing the defect the analyser was taught to find. The fixtures are
// constants already, which is what makes the path reachable at all.
//
// REQUIRED MUTATION, run 2026-09-09: remove the sanitiser from Step, and
// then from Result. Reds here and on nothing else, which is what makes
// this row rather than the ranged one above the guard for the prose
// path.
func TestTheProseFormEscapesToo(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(u *UI)
	}{
		{"Step/CSI", func(u *UI) { u.Step(hostileCSI) }},
		{"Step/OSC", func(u *UI) { u.Step(hostileOSC) }},
		{"Step/BEL", func(u *UI) { u.Step(hostileBEL) }},
		{"Step/DEL", func(u *UI) { u.Step(hostileDEL) }},
		{"Result/CSI", func(u *UI) { u.Result(hostileCSI) }},
		{"Result/OSC", func(u *UI) { u.Result(hostileOSC) }},
		{"Result/BEL", func(u *UI) { u.Result(hostileBEL) }},
		{"Result/DEL", func(u *UI) { u.Result(hostileDEL) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, out, errOut := testUI("", false, nil)
			tc.call(u)
			got := bothStreams(out, errOut)
			if got == "" {
				t.Fatal("nothing was written, so this row asserts an absence that " +
					"would hold for a method that renders at all")
			}
			for i := 0; i < len(got); i++ {
				if b := got[i]; b != '\n' && (b < 0x20 || b == 0x7f) {
					t.Fatalf("byte %#02x reached the terminal: %q", b, got)
				}
			}
			for _, want := range []string{"before", "after"} {
				if !strings.Contains(got, want) {
					t.Errorf("dropped %q instead of escaping it: %q", want, got)
				}
			}
		})
	}
}

// TestAQuotedSentenceCannotAddALineOfItsOwn is the Failure's half of the
// rule the renderers already keep.
//
// A FAILURE IS PARAGRAPHS THIS PROGRAM WROTE, and one of them is a
// quotation. The two used to arrive as one string a caller had joined,
// which made them indistinguishable — and a server's sentence carrying a
// line break could add a paragraph in this program's voice. Now the
// quotation is a field: it is escaped whole, so it stays one line, while
// the copy around it keeps its layout.
//
// REQUIRED MUTATION, run 2026-09-09: escape Detail per line, as the
// prose around it is. Reds here on the line count, and on nothing else.
func TestAQuotedSentenceCannotAddALineOfItsOwn(t *testing.T) {
	const forged = "not built\n\nPublished. https://not-really.example"

	u, _, errOut := testUI("", false, nil)
	u.Fail(NewFailure(
		"The build finished, and the server would not take the result.",
		"Nothing has been deployed.\n\nThe deploy is deploy-1.", NextFreshDeploy,
		"Run it again.").Quoting(forged))
	got := errOut.String()

	// THE QUOTATION IS ONE LINE. Both halves of the forged sentence have
	// to arrive on it — asserted by finding the line rather than by
	// counting paragraphs, because this program's own copy has
	// paragraphs of its own and a count cannot say whose they are.
	var quoted string
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "not built") {
			quoted = line
			break
		}
	}
	if quoted == "" {
		t.Fatalf("the quotation is not in the rendering at all:\n%q", got)
	}
	if !strings.Contains(quoted, "Published.") {
		t.Errorf("the quoted sentence was split across lines — the far end wrote "+
			"a paragraph in this program's voice:\n  line: %q\n  whole: %q",
			quoted, got)
	}
	if !strings.Contains(got, `not built\n\nPublished.`) {
		t.Errorf("the quotation's line breaks were not escaped:\n%q", got)
	}
	// AND THE COPY AROUND IT KEEPS ITS LAYOUT, which is the whole reason
	// the two are separate fields rather than one escaping rule.
	if !strings.Contains(got, "Nothing has been deployed.\n\nThe deploy is deploy-1.") {
		t.Errorf("this program's own paragraphs were escaped:\n%q", got)
	}
	// Inert, not deleted.
	for _, want := range []string{"not built", "Published."} {
		if !strings.Contains(got, want) {
			t.Errorf("the quotation lost %q:\n%q", want, got)
		}
	}
}

// TestThisProgramsComposedProseKeepsItsParagraphs.
//
// THE DEFECT THIS ROW EXISTS FOR SHIPPED, and nothing here saw it. An
// argument is escaped whole so that a server's sentence cannot add a
// line — and this program's own closing narration is composed at run
// time and passed as an argument, so every successful deploy printed
// "Published.\n\nIt can take..." on ONE line with visible backslash-n.
// The flow suite renders through a double that formats and does not
// escape, so the mangling existed only in the shipped path.
//
// Prose is the mark that says whose words these are. Both halves are
// asserted here: the mark keeps the layout, and its absence does not.
//
// REQUIRED MUTATION, run 2026-09-09: escape a Prose argument like a
// string one — `case Prose: args[i] = Prose(Sanitize(string(v)))`. Reds
// on the first half.
func TestThisProgramsComposedProseKeepsItsParagraphs(t *testing.T) {
	const composed = "Published.\n\nIt can take up to about a minute." + hostileCSI

	u, _, marked := testUI("", false, nil)
	u.Step("%s", Prose(composed))
	got := marked.String()

	if !strings.Contains(got, "Published.\n\nIt can take") {
		t.Errorf("this program's own paragraphs were turned into text:\n%q", got)
	}
	// AND IT IS STILL INERT. Prose says whose words they are, not that
	// they may drive a terminal.
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("a Prose argument carried a raw ESC to the terminal:\n%q", got)
	}
	if !strings.Contains(got, "[2Jafter") {
		t.Errorf("the escape was dropped rather than rendered inert:\n%q", got)
	}

	// The other half: an unmarked string is still somebody else's, and
	// its line breaks are still content.
	u2, _, plain := testUI("", false, nil)
	u2.Step("%s", composed)
	if strings.Contains(plain.String(), "Published.\n\nIt can take") {
		t.Errorf("an unmarked argument kept line breaks, so the mark means "+
			"nothing:\n%q", plain.String())
	}
}

// TestAParagraphThatMerelyREADSLikeTheQuotationKeepsItsLayout.
//
// The quotation used to be found by asking whether a paragraph EQUALS
// Detail, which is the right answer for the quotation and the wrong one
// for anything that happens to read the same — a Why identical to a
// Detail was escaped whole and lost its paragraphs. It is found by
// position now.
//
// REQUIRED MUTATION, run 2026-09-09: identify the quotation by value
// again. Reds here, and on nothing else.
func TestAParagraphThatMerelyREADSLikeTheQuotationKeepsItsLayout(t *testing.T) {
	const same = "first\n\nsecond"

	u, _, errOut := testUI("", false, nil)
	u.Fail(NewFailure("What.", same, NextGiveUp, "Next.").Quoting(same))
	got := errOut.String()

	// The quotation is one line; the Why that reads the same is two
	// paragraphs. Both appear, and they must not look alike.
	if !strings.Contains(got, `first\n\nsecond`) {
		t.Errorf("the quotation kept its line breaks:\n%q", got)
	}
	if !strings.Contains(got, "first\n\nsecond") {
		t.Errorf("this program's own paragraph lost its layout because it read "+
			"like the quotation:\n%q", got)
	}
}

// TestAPromptCannotAddALineAboveItself.
//
// THE ROW THAT WAS MISSING. The escape rows above drive every renderer
// with the hostile vocabulary and skip the newline, so they were green
// while the prompt kept line breaks — a cold reviewer put `before\nforged`
// through a prompt and got two lines, the second reading as this
// program's own words directly above the answer it was asking for.
//
// REQUIRED MUTATION, run 2026-09-10: escape a prompt per line again.
// Reds here and on nothing else.
func TestAPromptCannotAddALineAboveItself(t *testing.T) {
	const forged = "Which directory?\nEverything looks fine, press enter to deploy"

	for _, tc := range []struct {
		name  string
		input string
		ask   func(u *UI) error
	}{
		{"Confirm", "y\n", func(u *UI) error { _, err := u.Confirm(forged, true); return err }},
		{"Line", "anything\n", func(u *UI) error { _, err := u.Line(forged); return err }},
		// An answer this prompt ACCEPTS, because a re-ask asks twice and
		// the row is about one question's shape.
		{"Email", "someone@example.com\n", func(u *UI) error { _, err := u.Email(forged); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, _, errOut := testUI(tc.input, true, nil)
			if err := tc.ask(u); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			asked := errOut.String()
			if strings.Contains(asked, "\n") {
				t.Errorf("the question was asked over more than one line, so text "+
					"inside it can read as this program's own:\n%q", asked)
			}
			// Inert, not deleted: the question still has to arrive.
			for _, want := range []string{"Which directory?", "press enter to deploy"} {
				if !strings.Contains(asked, want) {
					t.Errorf("the prompt lost %q:\n%q", want, asked)
				}
			}
		})
	}
}

// TestAValueInThisProgramsProseCannotAddAParagraph.
//
// THE RESIDUE, CLOSED. When the quotation became a field, one thing was
// left interpolated into prose and disclosed at the time: identifiers.
// "The deploy is " + id + "." put a server-supplied value inside a
// paragraph whose line breaks are kept, so an id carrying one could add
// a paragraph in this program's voice — the same forgery the quotation
// had just been moved out of prose to prevent.
//
// Written is the shape that closes it: the format is the caller's own
// copy, layout and all, and every string argument is escaped whole.
//
// REQUIRED MUTATION, run 2026-09-10: drop the escaping from Written.
// Reds here, with the value's line breaks in the rendering.
func TestAValueInThisProgramsProseCannotAddAParagraph(t *testing.T) {
	const forged = "deploy-1.\n\nPublished. https://not-really.example"

	why := Written("Nothing has been deployed.\n\nThe deploy is %s.", forged)

	u, _, errOut := testUI("", false, nil)
	u.Fail(NewFailure("What.", why, NextGiveUp, "Next."))
	got := errOut.String()

	// OUR paragraph break survives; the value's does not.
	if !strings.Contains(got, "Nothing has been deployed.\n\nThe deploy is") {
		t.Errorf("this program's own layout was escaped:\n%q", got)
	}
	var line string
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "The deploy is") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("the sentence is not in the rendering:\n%q", got)
	}
	if !strings.Contains(line, "Published.") {
		t.Errorf("a value added a paragraph in this program's voice:\n  line: %q\n"+
			"  whole: %q", line, got)
	}
}
