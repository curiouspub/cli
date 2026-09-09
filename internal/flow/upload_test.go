package flow

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/pack"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// The names the syntax-tree row below looks for. They are written out
// here rather than derived, because a rename that missed one produces a
// FAILURE ("no such function") rather than a silent pass — which is the
// property that matters, and the one a derived name could not offer.
const (
	uploadFuncName        = "uploadArchive"
	uploadConstructorName = "uploadFailed"
)

// -------------------------------------------------------------------
// The declared size
// -------------------------------------------------------------------

// TestTheCreateDeclaresThePackedSize. The server signs the presigned
// upload with this number as an exact Content-Length, so a wrong one
// gets every upload refused by the store rather than warned about.
//
// TWO FIXTURES OF DIFFERENT PACKED SIZES, and the difference is asserted
// before anything else is. One fixture and one known constant is passed
// by a client that hardcodes the constant, and the row would read as
// though it had measured something.
//
// THE SOURCE TOTAL IS ASSERTED TO BE A DIFFERENT NUMBER, so a client
// sending the uncompressed total — the other size this sequence has in
// hand, and the easy one to reach for — is visibly wrong rather than
// coincidentally right.
//
// REQUIRED MUTATION, run 2026-09-08: send the walk's uncompressed total
// as the create's bytes. Both fixtures red on the declared size.
func TestTheCreateDeclaresThePackedSize(t *testing.T) {
	type measured struct {
		declared    int64
		onDisk      int64
		sourceTotal int64
	}
	got := map[string]measured{}

	for _, name := range []string{"small", "larger"} {
		t.Run(name, func(t *testing.T) {
			root := sizedProject(t, name)
			run := newDeployRun(t, root).scriptedLogin()
			run.prompt.confirms = []answer{no()}
			// THIS ROW'S SUBJECT IS THE NUMBER THE CREATE DECLARED, and
			// nothing else. Left checking its signature, the store would
			// refuse an upload whose length disagrees with a wrong
			// declaration — and the row would red on a refusal without
			// ever naming which of the two sizes was sent. The store's
			// own behaviour has its own row.
			run.store.acceptAnyLength = true

			// THE ARCHIVE IS MEASURED WHILE THE CREATE IS BEING SERVED,
			// which is the only moment it is certainly on disk: a run
			// that fails afterwards removes it, and this row must be
			// able to say which number was declared even then. It is
			// also what keeps the row honest about WHEN — an assertion
			// about the size the create declared should be compared
			// with the file as it was at that instant, not with one
			// re-measured later.
			var onDisk int64
			run.script.onCreate = func(*http.Request) {
				onDisk = archiveSizeUnder(t, run.tempParent)
			}

			handoff, err := run.run()
			defer handoff.Release()

			if onDisk == 0 {
				t.Fatalf("no archive was found while the create was being served, so "+
					"every comparison below is against zero (Deploy: %v)", err)
			}

			run.script.mu.Lock()
			creates := append([]wire.DeployCreateRequest(nil), run.script.creates...)
			run.script.mu.Unlock()
			if len(creates) != 1 {
				t.Fatalf("the run made %d creates, want exactly 1", len(creates))
			}
			if creates[0].Bytes != onDisk {
				t.Errorf("the create declared %d bytes, want the archive's own %d",
					creates[0].Bytes, onDisk)
			}

			total := sourceTotal(t, root)
			if creates[0].Bytes == total {
				t.Errorf("the create declared %d bytes, which is also the uncompressed "+
					"source total — this row cannot tell the two numbers apart", total)
			}
			got[name] = measured{declared: creates[0].Bytes, onDisk: onDisk, sourceTotal: total}
		})
	}

	if len(got) != 2 {
		t.Fatalf("only %d of the two fixtures were measured", len(got))
	}
	if got["small"].onDisk == got["larger"].onDisk {
		t.Fatalf("both fixtures packed to %d bytes, so one number was checked twice "+
			"and a client hardcoding it would pass", got["small"].onDisk)
	}
}

// sizedProject writes a deployable project whose packed size is
// deliberately different from its sibling's.
func sizedProject(t *testing.T, name string) string {
	t.Helper()
	files := astroProject(nil)
	switch name {
	case "small":
		files["src/pages/one.astro"] = []byte("<h1>one</h1>\n")
	case "larger":
		// Enough distinct text that the archive cannot come out the same
		// size as the small one's, without relying on how well anything
		// compresses.
		for i := 0; i < 12; i++ {
			files[fmt.Sprintf("src/pages/page-%02d.astro", i)] =
				[]byte(fmt.Sprintf("<h1>page %d</h1>\n%s\n", i, strings.Repeat("filler ", 200)))
		}
	default:
		t.Fatalf("no such fixture: %s", name)
	}
	return writeProject(t, files)
}

// archiveSizeUnder is the size of the one regular file below dir, which
// during a run is the archive the packer has just written.
func archiveSizeUnder(t *testing.T, dir string) int64 {
	t.Helper()
	var sizes []int64
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, statErr := d.Info()
		if statErr != nil {
			return statErr
		}
		sizes = append(sizes, info.Size())
		return nil
	})
	if err != nil {
		t.Fatalf("looking for the archive under %s: %v", dir, err)
	}
	if len(sizes) != 1 {
		t.Fatalf("found %d files under the working directory, want exactly the "+
			"archive", len(sizes))
	}
	return sizes[0]
}

// sourceTotal is the uncompressed total the local limits measure — the
// other number this sequence holds, and the one that must not be sent.
func sourceTotal(t *testing.T, root string) int64 {
	t.Helper()
	tree, err := pack.Walk(pack.OSFileSystem{}, root)
	if err != nil {
		t.Fatalf("walking the fixture to total it: %v", err)
	}
	var total int64
	for _, f := range tree.Files {
		total += f.Size
	}
	if total == 0 {
		t.Fatal("the walk totalled zero bytes, so the comparison below says nothing")
	}
	return total
}

// -------------------------------------------------------------------
// The upload itself
// -------------------------------------------------------------------

// TestAMatchingUploadIsAcceptedAndAWrongLengthIsRefused runs both halves
// past ONE store, because "the store received both" is not a claim two
// separate handlers can make.
//
// A FAKE THAT REFUSES EVERYTHING MAKES A WRONG LENGTH LOOK CORRECTLY
// REFUSED, and a row that only observes the refusal is also passed by a
// client that never sent anything. So the first run is signed for the
// length the client actually sends and must be ACCEPTED, and the second
// is signed for a length one byte off and must be refused — with both
// requests visible in the same store's record.
//
// REQUIRED MUTATION, run 2026-09-08: change only the PUT's declared
// Content-Length. The accepted half reds; the create's declared size,
// asserted above, does not move.
func TestAMatchingUploadIsAcceptedAndAWrongLengthIsRefused(t *testing.T) {
	store := newObjectStore(t, nil)

	first := newDeployRun(t, fixtureProject(t, "valid")).uploadingTo(store).scriptedLogin()
	first.prompt.confirms = []answer{no()}
	handoff, err := first.run()
	if err != nil {
		t.Fatalf("the matching upload failed: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	second := newDeployRun(t, fixtureProject(t, "valid")).uploadingTo(store).scriptedLogin()
	second.prompt.confirms = []answer{no()}
	second.script.signSkew = 1
	badHandoff, err := second.run()
	defer badHandoff.Release()
	if err == nil {
		t.Fatal("a body whose length the signature does not cover was reported as uploaded")
	}

	puts := store.received()
	if len(puts) != 2 {
		t.Fatalf("the store received %d requests, want 2 — one accepted and one "+
			"refused, both seen by the same handler", len(puts))
	}
	if puts[0].status != http.StatusOK {
		t.Errorf("the matching upload was answered %d, want 200", puts[0].status)
	}
	if puts[0].method != http.MethodPut {
		t.Errorf("the upload used %s, want PUT", puts[0].method)
	}
	if puts[0].contentLength != handoff.Bytes {
		t.Errorf("the upload declared Content-Length %d, want the archive's %d",
			puts[0].contentLength, handoff.Bytes)
	}
	if puts[0].bodyLength != handoff.Bytes {
		t.Errorf("the store read %d bytes of body, want the archive's %d",
			puts[0].bodyLength, handoff.Bytes)
	}
	if puts[1].status != http.StatusForbidden {
		t.Errorf("the mismatched upload was answered %d, want 403", puts[1].status)
	}
}

// TestTheUploadCarriesNoCredentialAndTheCreateDoes. The presigned URL IS
// the credential; adding a bearer token sends a second one to an origin
// that never asked for it.
//
// THE ABSENCE IS ASSERTED ON A REQUEST THE STORE CONFIRMS IT ACCEPTED,
// never on the absence of a request — and the create carrying
// "Bearer <token>" in the same run is the positive control, because a
// client that attaches the header nowhere would otherwise pass.
//
// REQUIRED MUTATION, run 2026-09-08: attach the bearer token to the PUT.
// The no-credential half reds while the create half stays green.
func TestTheUploadCarriesNoCredentialAndTheCreateDoes(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}

	handoff, err := run.run()
	if err != nil {
		t.Fatalf("Deploy: %v\n%s", err, rendered(err))
	}
	defer handoff.Release()

	puts := run.store.received()
	if len(puts) != 1 {
		t.Fatalf("the store received %d requests, want exactly 1", len(puts))
	}
	if puts[0].status != http.StatusOK {
		t.Fatalf("the store answered %d, so the header assertion below is about a "+
			"request nobody accepted", puts[0].status)
	}
	if puts[0].authorization != "" {
		t.Errorf("the upload sent Authorization %q, want none — the link is the "+
			"credential", puts[0].authorization)
	}

	run.script.mu.Lock()
	bearers := append([]string(nil), run.script.createBearers...)
	run.script.mu.Unlock()
	if len(bearers) != 1 {
		t.Fatalf("the run made %d creates, want exactly 1", len(bearers))
	}
	if !strings.HasPrefix(bearers[0], "Bearer ") || bearers[0] == "Bearer " {
		t.Errorf("the create sent Authorization %q, want a bearer token — without "+
			"this, the absence above is passed by a client that sends the header "+
			"nowhere at all", bearers[0])
	}
}

// TestARedirectFromTheStoreIsNotASuccess. The upload refuses redirects
// the way the rest of this client does, and the SHAPE is the trap: the
// refusal makes a 3xx arrive as a RESPONSE rather than an error, so code
// that only checks for 4xx and 5xx reads it as a success and reports a
// deploy that never uploaded.
//
// The positive control is the store confirming it received the PUT: an
// error is also what a run that never sent anything produces.
func TestARedirectFromTheStoreIsNotASuccess(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.store.status = http.StatusFound
	run.store.location = "https://elsewhere.example/somewhere"

	handoff, err := run.run()
	defer handoff.Release()
	if err == nil {
		t.Fatal("a redirect from the store was reported as a completed upload")
	}
	if puts := run.store.received(); len(puts) != 1 {
		t.Fatalf("the store received %d requests, want 1 — an error with no request "+
			"says nothing about how a redirect is read", len(puts))
	}
	if _, code := renderedBytes(t, err); code != 1 {
		t.Errorf("a redirected upload cost %d, want 1", code)
	}
}

// -------------------------------------------------------------------
// The two faces of a refused upload
// -------------------------------------------------------------------

// TestTheTwoFacesOfARefusedUpload. The server signs an exact
// Content-Length, so the store answers 403 both for a link whose window
// has closed and for a body whose length does not match the signature.
// One means wait and run again; the other is a client fault that
// reproduces forever, and rendering the first when it is the second
// names the one cause that has been ruled out.
//
// THE CLOCK IS THE DISCRIMINATOR, and the zero case is not a legacy
// path: the expiry field is published in the contract and the server
// does not populate it yet, so the ambiguous branch is the one that runs
// today. Both branches are built and both are exercised, which is the
// only way the day the server starts sending it is not the day the other
// branch is first run.
//
// EVERY ROW IS TWO-SIDED: the copy that must appear is asserted present
// AND the two that must not are asserted absent, because a message that
// says nothing satisfies an absence check on its own.
//
// REQUIRED MUTATION. Rendering the expired copy for a refusal INSIDE the
// window reds the first row alone. Dropping the announced window from the
// request's deadline reds BOTH closed-window rows.
//
// It reds them EARLIER than predicted, and the correction is worth
// keeping. The prediction was "on the count" — the zero becomes a one.
// What actually happens is that those two cases set no status at all
// (they are never meant to reach the store), so with the deadline gone
// the upload SUCCEEDS and the row stops at "a refused upload was
// reported as a completed one" before the count is ever read. Same
// mutation, same two rows, a stricter assertion firing first. A
// prediction about which line reports is a claim about the table.
func TestTheTwoFacesOfARefusedUpload(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expires  time.Time
		want     string
		absent   []string
		status   int
		wantPuts int
	}{
		{
			name:     "refused inside the window",
			expires:  fixedNowLocal.Add(10 * time.Minute),
			want:     refusedInsideWindow,
			absent:   []string{linkExpired, mayHaveExpired},
			status:   http.StatusForbidden,
			wantPuts: 1,
		},
		{
			// THE CEILING, and it is why these two send nothing. The
			// announced window is this request's own deadline, so a link
			// that has already run out is refused before a byte leaves
			// the machine — wantPuts is 0, and that zero is the
			// assertion. Round 1 drove these through a refusal the store
			// returned, which is a round trip spent to be told what the
			// client already knew.
			name:     "the window had already closed",
			expires:  fixedNowLocal,
			want:     linkExpired,
			absent:   []string{refusedInsideWindow, mayHaveExpired},
			wantPuts: 0,
		},
		{
			name:     "the window closed a minute ago",
			expires:  fixedNowLocal.Add(-time.Minute),
			want:     linkExpired,
			absent:   []string{refusedInsideWindow, mayHaveExpired},
			wantPuts: 0,
		},
		{
			name:     "the server told this client no window at all",
			expires:  time.Time{},
			want:     mayHaveExpired,
			absent:   []string{refusedInsideWindow, linkExpired},
			status:   http.StatusForbidden,
			wantPuts: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
			run.prompt.confirms = []answer{no()}
			run.script.expiresAt = tc.expires
			run.store.status = tc.status
			// A well-formed error document from the store, so a client
			// that read one would render its words instead of these.
			run.store.body = storeErrorDocument

			handoff, err := run.run()
			defer handoff.Release()
			if err == nil {
				t.Fatal("a refused upload was reported as a completed one")
			}
			if puts := run.store.received(); len(puts) != tc.wantPuts {
				t.Fatalf("the store received %d requests, want %d", len(puts), tc.wantPuts)
			}

			text, _ := renderedBytes(t, err)
			if !strings.Contains(text, tc.want) {
				t.Errorf("the refusal never said %q:\n%s", tc.want, text)
			}
			// Every refusal closes by saying what was and was not left
			// behind, because a failed upload DOES leave a deploy
			// record and copy implying a clean-up would be inventing
			// one.
			if !strings.Contains(text, "is discarded on its own") {
				t.Errorf("the refusal never said what became of the deploy record:\n%s", text)
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(text, unwanted) {
					t.Errorf("the refusal also said %q, which names a cause this "+
						"one has ruled out:\n%s", unwanted, text)
				}
			}

			// The store's own document is never parsed and never
			// rendered — not its code, not its message.
			for _, leaked := range []string{"RefusedByTheStore", "Do not render me", "<Error>"} {
				if strings.Contains(text, leaked) {
					t.Errorf("the store's error document reached the reader (%q):\n%s",
						leaked, text)
				}
			}

			// The status reaches the structured result rather than only
			// the sentence.
			var refusal *storeRefused
			if !errors.As(err, &refusal) {
				t.Fatalf("the refusal does not carry the store's status: %v", err)
			}
			if refusal.Status != tc.status {
				t.Errorf("the structured result carries status %d, want %d",
					refusal.Status, tc.status)
			}
		})
	}
}

// TestNoObjectStoreVocabularyInTheUploadPath. The alternative to the
// clock was reading the store's error body, and it is rejected: the
// response belongs to the substrate rather than to this contract, and a
// public client that switches on one store's error vocabulary has made
// the substrate part of the contract by accident.
//
// IT ASKS THE SOURCE rather than a message, because a row that only
// looked at rendered copy would be satisfied by a client that parsed the
// body and happened not to print it this time.
func TestNoObjectStoreVocabularyInTheUploadPath(t *testing.T) {
	forbidden := []string{
		"encoding/xml",
		"xml.",
		"SignatureDoesNotMatch",
		"AccessDenied",
		"RequestTimeTooSkewed",
		"ExpiredToken",
		"NoSuchKey",
		"MalformedXML",
		"InvalidArgument",
		"wire.ErrorResponse",
	}

	scanned := 0
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading this package's own directory: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatalf("reading %s: %v", name, readErr)
		}
		scanned++
		for _, term := range forbidden {
			if strings.Contains(string(data), term) {
				t.Errorf("%s names %q — the store's answer is the substrate's, and "+
					"switching on its vocabulary makes the substrate part of this "+
					"client's contract", name, term)
			}
		}
	}
	if scanned == 0 {
		t.Fatal("no source file was scanned, so this row would pass over anything")
	}
	if len(forbidden) == 0 {
		t.Fatal("the forbidden set is empty, so this row would pass over anything")
	}
}

// -------------------------------------------------------------------
// The signed URL appears nowhere
// -------------------------------------------------------------------

// TestTheSignedURLNeverReachesOutputOrError is the row the leak
// mechanism actually rests on, and the scenario is a TRANSPORT failure
// on purpose. Every transport failure — a refused connection, a timeout,
// a TLS error — comes back from net/http as a *url.Error carrying the
// whole signed URL, through %v and %+v alike. That is the default on
// every ordinary path, and no redacting type can reach it: the request
// is built from a string and net/http composes the failure itself.
//
// So the mechanism is call-site discipline — the transport error is
// never returned — and this row is what guards it. All four surfaces are
// captured, and the REDACTED HOST is asserted PRESENT in the message,
// because absence alone is satisfied by an empty error.
//
// REQUIRED MUTATION, run 2026-09-08: return the transport error
// unwrapped from the upload. This reds on the error's %v and %+v, both
// quoting the whole signed link — and it reds the return-set row below
// at the same time, which is the pair working rather than a duplicate:
// one catches the leak, the other catches the SHAPE that allows it.
func TestTheSignedURLNeverReachesOutputOrError(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}

	// An address nothing answers on: the store's own server, closed
	// before the run starts, so the failure is a dial that cannot
	// succeed and needs no name resolution and no network.
	dead := deadAddress(t)
	run.script.uploadOverride = dead

	var handoff *Handoff
	var err error
	stdout, stderr := captureStdStreams(t, func() {
		handoff, err = run.run()
	})
	defer handoff.Release()
	if err == nil {
		t.Fatal("an upload to an address nothing answers on was reported as done")
	}

	surfaces := map[string]string{
		"stdout":               stdout,
		"stderr":               stderr,
		"the error's %v":       fmt.Sprintf("%v", err),
		"the error's %+v":      fmt.Sprintf("%+v", err),
		"what the run printed": run.prompt.out.String(),
		"the rendered failure": rendered(err),
	}
	for name, text := range surfaces {
		if strings.Contains(text, storeSignature) {
			t.Errorf("%s carries the signature from the upload link:\n%s", name, text)
		}
		if strings.Contains(text, dead) {
			t.Errorf("%s carries the whole upload link:\n%s", name, text)
		}
	}

	// The positive half. Without it every assertion above is satisfied
	// by an error that says nothing at all.
	host := hostOf(t, dead)
	if !strings.Contains(rendered(err), host) {
		t.Errorf("the failure never names the host it could not reach (%s), so the "+
			"absences above are also satisfied by an empty message:\n%s",
			host, rendered(err))
	}
}

// TestARefusedUploadDoesNotLeakTheLinkEither is kept as a SECOND row
// rather than folded into the one above. It exercises a different path
// and produces no transport error at all, which is exactly why it cannot
// stand in for the row that does.
func TestARefusedUploadDoesNotLeakTheLinkEither(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.store.status = http.StatusForbidden
	run.store.body = storeErrorDocument

	handoff, err := run.run()
	defer handoff.Release()
	if err == nil {
		t.Fatal("a refused upload was reported as a completed one")
	}
	if puts := run.store.received(); len(puts) != 1 {
		t.Fatalf("the store received %d requests, want 1", len(puts))
	}

	text, _ := renderedBytes(t, err)
	for name, surface := range map[string]string{
		"the rendered failure": text,
		"the error's %v":       fmt.Sprintf("%v", err),
		"the error's %+v":      fmt.Sprintf("%+v", err),
	} {
		if strings.Contains(surface, storeSignature) {
			t.Errorf("%s carries the signature from the upload link:\n%s", name, surface)
		}
	}
	if !strings.Contains(text, hostOf(t, run.store.url)) {
		t.Errorf("the refusal never names the host that refused it:\n%s", text)
	}
}

// TestEveryUploadReturnIsBuiltByOneConstructor asserts the SET rather
// than a path. A leak row that exercises one branch is satisfied by the
// branch it exercised, and the upload has several — a file that will not
// open, a request that will not build, a transport failure, a redirect,
// a refusal, a status nobody expected. Each is a place the signed URL
// could be formatted into a message by whoever adds the next one.
//
// So this reads the function's own syntax tree and requires every
// non-nil error it returns to be a call to the single constructor.
func TestEveryUploadReturnIsBuiltByOneConstructor(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "upload.go", nil, 0)
	if err != nil {
		t.Fatalf("parsing upload.go: %v", err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == uploadFuncName {
			fn = d
			break
		}
	}
	if fn == nil {
		t.Fatalf("upload.go declares no function called %s, so this row would pass "+
			"over nothing", uploadFuncName)
	}

	returns := 0
	errorReturns := 0
	literals := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		// A RETURN INSIDE A NESTED FUNCTION LITERAL IS NOT ONE OF THIS
		// FUNCTION'S RETURN PATHS, and descending into one would make
		// the row assert something it does not mean — the redirect
		// policy handed to the http client is a callback whose return
		// value never leaves this function at all. Counted rather than
		// silently skipped, so "there were none to skip" and "the skip
		// swallowed everything" are different readings.
		if _, isLit := n.(*ast.FuncLit); isLit {
			literals++
			return false
		}
		ret, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		returns++
		if len(ret.Results) != 1 {
			t.Errorf("%s returns %d values at %s, want exactly one error",
				uploadFuncName, len(ret.Results), fset.Position(ret.Pos()))
			return true
		}
		if ident, isIdent := ret.Results[0].(*ast.Ident); isIdent && ident.Name == "nil" {
			return true
		}
		errorReturns++
		call, isCall := ret.Results[0].(*ast.CallExpr)
		if !isCall {
			t.Errorf("%s returns something other than a call to %s at %s",
				uploadFuncName, uploadConstructorName, fset.Position(ret.Pos()))
			return true
		}
		name, isIdent := call.Fun.(*ast.Ident)
		if !isIdent || name.Name != uploadConstructorName {
			t.Errorf("%s returns an error built by something other than %s at %s",
				uploadFuncName, uploadConstructorName, fset.Position(ret.Pos()))
		}
		return true
	})

	if returns == 0 {
		t.Fatal("no return statement was inspected, so this row would pass over anything")
	}
	if literals == 0 {
		t.Error("no nested function literal was skipped, which means the redirect " +
			"policy is no longer stated inside this function — the row's universe " +
			"has changed under it")
	}
	if errorReturns < 4 {
		t.Errorf("only %d error returns were inspected; the upload has more failure "+
			"shapes than that, so this row is looking at the wrong function", errorReturns)
	}
}

// -------------------------------------------------------------------
// The create's failure shapes
// -------------------------------------------------------------------

// TestACreateThatNeverAnswersIsSentOnce. A repeat is not idempotent: it
// creates a second deploy record and spends quota again. A failure after
// the request left this process is indistinguishable from one before it,
// so the honest move is to stop and SAY a deploy may have been created —
// which is also why the copy here must not claim nothing happened.
func TestACreateThatNeverAnswersIsSentOnce(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// The answer never arrives: the run is cancelled the moment the
	// create is seen, and the handler holds the connection until the
	// request is abandoned. From this client's side that is the same
	// position as a deadline expiring with the request already sent.
	run.script.onCreate = func(r *http.Request) {
		cancel()
		<-r.Context().Done()
	}

	handoff, err := Deploy(ctx, run.deps)
	defer handoff.Release()
	if err == nil {
		t.Fatal("a create that never answered was reported as a success")
	}
	if sent := run.script.sentTo("/v1/deploys"); sent != 1 {
		t.Errorf("the create was sent %d times, want exactly 1", sent)
	}
	if puts := run.store.received(); len(puts) != 0 {
		t.Errorf("the store received %d requests after a create that never answered, "+
			"want none", len(puts))
	}

	text, _ := renderedBytes(t, err)
	if !strings.Contains(text, deployMayExist) {
		t.Errorf("the failure never said a deploy may have been created:\n%s", text)
	}
	if strings.Contains(text, uploadedNothing) {
		t.Errorf("the failure claims nothing was uploaded, which is the one thing "+
			"this client cannot know here:\n%s", text)
	}
}

// TestAnUnauthorizedCreateReAuthenticatesAndRetriesOnce.
//
// THE CALLS ARE ASSERTED AT THE ENDPOINT LEVEL AND IN ORDER, not through
// a callback counter: the claim is that the run re-entered at the
// capacity check and then the login before trying again, and a count
// cannot tell that from a client that simply repeated the create.
//
// The second half is the loop guard. A second refusal stops.
func TestAnUnauthorizedCreateReAuthenticatesAndRetriesOnce(t *testing.T) {
	t.Run("one refusal is recovered from, exactly once", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "valid"))
		run.storedToken("stale-token", run.srv.URL)
		run.scriptedLogin()
		run.prompt.confirms = []answer{no()}
		run.script.refuseCreatesUntil = 1

		handoff, err := run.run()
		if err != nil {
			t.Fatalf("Deploy: %v\n%s", err, rendered(err))
		}
		defer handoff.Release()

		want := []string{
			"POST /v1/deploys",
			"GET /v1/capacity",
			"POST /v1/auth/start",
			"POST /v1/auth/verify",
			"POST /v1/deploys",
			"POST /v1/deploys/deploy-1/start",
			"GET /v1/deploys/deploy-1/events",
			"POST /v1/deploys/deploy-1/publish",
		}
		if got := endpointCalls(run.journal.all()); !equalEvents(got, want) {
			t.Errorf("the run called %v, want %v\n\nfull journal:\n  %s",
				got, want, strings.Join(run.journal.all(), "\n  "))
		}
		if puts := run.store.received(); len(puts) != 1 {
			t.Errorf("the store received %d uploads, want 1", len(puts))
		}
	})

	t.Run("a second refusal stops rather than looping", func(t *testing.T) {
		run := newDeployRun(t, fixtureProject(t, "valid"))
		run.storedToken("stale-token", run.srv.URL)
		run.scriptedLogin()
		run.prompt.confirms = []answer{no()}
		run.script.refuseCreatesUntil = 99

		handoff, err := run.run()
		defer handoff.Release()
		if err == nil {
			t.Fatal("a create refused twice was reported as a success")
		}
		if sent := run.script.sentTo("/v1/deploys"); sent != 2 {
			t.Errorf("the create was sent %d times, want exactly 2 — one try and one "+
				"retry after a fresh login", sent)
		}
		if sent := run.script.sentTo("/v1/auth/verify"); sent != 1 {
			t.Errorf("the run logged in %d times, want exactly 1", sent)
		}

		text, code := renderedBytes(t, err)
		if code != 1 {
			t.Errorf("a run that could not authenticate cost %d, want 1", code)
		}
		if !strings.Contains(text, authenticationFailed) {
			t.Errorf("the failure never said authentication failed:\n%s", text)
		}
		// Five distinct causes reach one byte-identical refusal, so the
		// copy must not name any of them.
		for _, forbidden := range []string{"revoked", "expired token", "unknown token"} {
			if strings.Contains(strings.ToLower(text), forbidden) {
				t.Errorf("the failure names %q, which this client cannot know:\n%s",
					forbidden, text)
			}
		}
	})
}

// TestWhatACreateFailureCosts pins the routing table's endings against
// the shipped rendering, so a code that changes route changes a number a
// script can see.
func TestWhatACreateFailureCosts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outcome  outcome
		wantCode int
		wantText string
	}{
		{
			name: "the kill switch is on",
			outcome: fails(http.StatusServiceUnavailable, wire.CodeMaintenance,
				"curious.pub is down for maintenance"),
			wantCode: ui.ExitServerClosed,
			wantText: "curious.pub is down for maintenance",
		},
		{
			name: "too many requests",
			outcome: outcome{
				status: http.StatusTooManyRequests, code: wire.CodeRateLimited,
				message: "slow down", retryAfter: "120",
			},
			wantCode: 1,
			wantText: "slow down",
		},
		{
			name: "the server would not accept the request",
			outcome: fails(http.StatusBadRequest, wire.CodeBadRequest,
				"that archive is larger than this server accepts"),
			wantCode: 1,
			wantText: "that archive is larger than this server accepts",
		},
		{
			name: "a code this build predates",
			outcome: fails(http.StatusTeapot, wire.ErrorCode("brand_new_code"),
				"something newer than this build"),
			wantCode: 1,
			wantText: "something newer than this build",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
			run.prompt.confirms = []answer{no()}
			run.script.deployOutcome = tc.outcome

			handoff, err := run.run()
			defer handoff.Release()
			if err == nil {
				t.Fatal("a refused create was reported as a success")
			}

			text, code := renderedBytes(t, err)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d\n%s", code, tc.wantCode, text)
			}
			if !strings.Contains(text, tc.wantText) {
				t.Errorf("the failure never said %q:\n%s", tc.wantText, text)
			}
			if puts := run.store.received(); len(puts) != 0 {
				t.Errorf("the store received %d requests after a refused create, "+
					"want none", len(puts))
			}
		})
	}
}

// TestEveryContractCodeHasACreateRoute reads the contract's own
// enumeration rather than a list typed beside it, so a ninth code
// arrives already needing an answer here. A hand-written copy of the
// vocabulary agrees with itself by construction and never looks at the
// contract at all.
func TestEveryContractCodeHasACreateRoute(t *testing.T) {
	if len(wire.AllErrorCodes) == 0 {
		t.Fatal("the contract enumerates no codes, so this row would pass over nothing")
	}
	for _, code := range wire.AllErrorCodes {
		if _, stated := createRouting[code]; !stated {
			t.Errorf("the contract defines %q and the create has no stated route for it", code)
		}
	}
	for code := range createRouting {
		found := false
		for _, known := range wire.AllErrorCodes {
			if code == known {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the create routes %q, which the contract does not define", code)
		}
	}
}

// -------------------------------------------------------------------
// Small instruments
// -------------------------------------------------------------------

// endpointCalls reduces a journal to the requests that left this client,
// in order, dropping everything else.
func endpointCalls(events []string) []string {
	var out []string
	for _, e := range events {
		if strings.HasPrefix(e, "GET /") || strings.HasPrefix(e, "POST /") {
			out = append(out, e)
		}
	}
	return out
}

// deadAddress is a URL nothing answers on: a real server's address,
// closed before it is used. It needs no name resolution and no network,
// so the dial failure it produces is the same on every machine.
func deadAddress(t *testing.T) string {
	t.Helper()
	store := newObjectStore(t, nil)
	store.closeNow()
	return store.url
}

// hostOf is the host part of a URL, which is what a redacted message may
// carry.
func hostOf(t *testing.T, raw string) string {
	t.Helper()
	host := redactedHost(raw)
	if host == "" {
		t.Fatalf("could not take the host out of a URL the test wrote")
	}
	return host
}

// captureStdStreams runs fn with the process's own stdout and stderr
// redirected into files, and returns what each collected. It exists
// because "the signed URL appears nowhere" is a claim about the
// PROCESS's output, not only about an error value.
func captureStdStreams(t *testing.T, fn func()) (stdout, stderr string) {
	t.Helper()
	dir := t.TempDir()
	outFile, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatalf("creating the stdout capture: %v", err)
	}
	errFile, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatalf("creating the stderr capture: %v", err)
	}

	realOut, realErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outFile, errFile
	fn()
	os.Stdout, os.Stderr = realOut, realErr

	if err := outFile.Close(); err != nil {
		t.Fatalf("closing the stdout capture: %v", err)
	}
	if err := errFile.Close(); err != nil {
		t.Fatalf("closing the stderr capture: %v", err)
	}
	return readFileString(t, filepath.Join(dir, "stdout")),
		readFileString(t, filepath.Join(dir, "stderr"))
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// bulkyProject is a deployable project whose archive is far larger than
// any socket buffer between this client and a store on loopback.
//
// THE SIZE IS THE POINT, and it was MEASURED rather than guessed. A small
// body is swallowed whole by the kernel's socket buffers, so the client
// finishes writing before the store has read anything and the rows below
// would measure nothing — a "slow" store the client never waited for, and
// a "wedged" one it had already finished with. On this machine those
// buffers hold about 4 MiB: a probe on the client's own progress showed
// it hand over exactly 4,194,304 bytes and then block. Twelve MiB is
// comfortably past that in three files, each under the per-file cap. The
// content is random so the packer's gzip cannot shrink it back to
// nothing.
func bulkyProject(t *testing.T) string {
	t.Helper()
	files := astroProject(nil)
	src := rand.New(rand.NewSource(1))
	for _, name := range []string{"public/a.bin", "public/b.bin", "public/c.bin"} {
		buf := make([]byte, 4<<20)
		if _, err := io.ReadFull(src, buf); err != nil {
			t.Fatalf("building the bulky fixture: %v", err)
		}
		files[name] = buf
	}
	return writeProject(t, files)
}

// TestASlowUploadIsNotAStalledOne is the row the fixed deadline could
// never have passed.
//
// The store consumes the body at a pace that makes the whole upload take
// SEVERAL stall windows, while never letting the gap between two bytes
// reach one. Under a total deadline this is indistinguishable from a
// hang; under a stall timer it is exactly what an ordinary uplink and a
// large project look like, and it must succeed.
//
// THE NUMBERS COME FROM A MEASUREMENT, and the first set did not. A
// 250 ms window failed roughly one run in six, and instrumenting the
// client's own progress said why: the gap between two bytes is not the
// store's pause but the time for the kernel's socket buffers to free
// enough space, which on this machine ran to 88-110 ms at a 20 ms pace
// and spiked past 250 under load. The window is now 600 ms — five times
// the observed worst gap — and the paced phase is long enough that the
// upload still spends several windows. A timing row whose margin is
// smaller than the thing it did not measure is a flake with a schedule.
//
// The paced phase covers the first half of the body and the rest is
// drained at full speed. The tail must exceed the socket buffers: once
// the client has handed over its last byte there is no progress left to
// report, so a paced drain of a bufferful would look like a stall the
// client did not cause.
//
// WHERE THE MEASUREMENT WAS TAKEN, and where it was not. The 88-110 ms
// worst gap and the 4 MiB the client hands over before it blocks were
// measured on ONE environment: local macOS on arm64. That is not even
// one of the three legs this project's gate runs — those are Linux,
// macOS and Windows runners, and the macOS one is different hardware.
//
// So the margin is measured on one environment and CARRIED UNMEASURED to
// three. Socket buffer sizes and their autotuning are a property of the
// kernel and the runner, and Windows in particular has neither the same
// defaults nor the same behaviour, so there is no reason the numbers
// transfer. This is the same shape as the defect that produced this
// round — a value measured at one site and reused at another without
// asking what the new site does — one artefact along, and it is written
// here rather than left implicit because the alternative is a margin
// nobody knows the size of on two thirds of the gate. Twenty runs per
// leg with this row's own instrumentation is what would settle it; a
// follow-up carries that.
//
// REQUIRED MUTATION: stop resetting the watchdog on progress.
func TestASlowUploadIsNotAStalledOne(t *testing.T) {
	const stall = 600 * time.Millisecond

	run := newDeployRun(t, bulkyProject(t)).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.deps.UploadStallTimeout = stall
	run.store.readChunk = 64 << 10
	run.store.readPause = 25 * time.Millisecond
	run.store.pauseUntil = 6 << 20

	started := time.Now()
	handoff, err := run.run()
	defer handoff.Release()
	elapsed := time.Since(started)

	if err != nil {
		t.Fatalf("a slow but progressing upload was refused after %v: %v", elapsed, err)
	}
	if elapsed < 3*stall {
		t.Fatalf("the upload took %v, which is under %v — this row did not spend "+
			"long enough to prove a total deadline would have killed it",
			elapsed, 3*stall)
	}
	puts := run.store.received()
	if len(puts) != 1 {
		t.Fatalf("the store received %d requests, want 1", len(puts))
	}
	if puts[0].bodyLength != puts[0].contentLength {
		t.Errorf("the store read %d bytes of a %d-byte body, so the upload did not "+
			"finish", puts[0].bodyLength, puts[0].contentLength)
	}
}

// TestAWedgedUploadStopsAndSaysSo is the other side of the row above,
// and the pair is the whole ruling: a stall timer must let one through
// and stop the other.
//
// The store reads a little and then stops reading at all, holding the
// request open. Nothing is broken — the connection is up and the host is
// answering — which is why the copy must not say the host could not be
// reached. It says the upload stalled, and it names the host so a person
// can tell a wrong address from a quiet one.
//
// REQUIRED MUTATION: collapse the stall branch into the unreachable one.
func TestAWedgedUploadStopsAndSaysSo(t *testing.T) {
	const stall = 600 * time.Millisecond

	run := newDeployRun(t, bulkyProject(t)).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.deps.UploadStallTimeout = stall
	run.store.stopReadingAfter = 64 << 10

	started := time.Now()
	handoff, err := run.run()
	defer handoff.Release()
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a wedged upload was reported as a completed one")
	}
	if elapsed > 30*time.Second {
		t.Fatalf("the wedged upload took %v to give up, which is the whole failure "+
			"this round removed", elapsed)
	}

	text, _ := renderedBytes(t, err)
	if !strings.Contains(text, uploadStalled) {
		t.Errorf("a wedged upload never said it had stalled:\n%s", text)
	}
	// THE HOST IS NAMED and the signature is not. The redacted half is
	// the actionable half.
	host := hostOf(t, run.store.url)
	if !strings.Contains(text, host) {
		t.Errorf("the stall never named the host %q:\n%s", host, text)
	}
	if strings.Contains(text, storeSignature) {
		t.Errorf("the stall leaked the signature from the upload link:\n%s", text)
	}
	// THE EXCLUSIVITY, and it is the point of having three sentences.
	// Bytes crossed this connection, so neither "could not reach" nor
	// "the connection dropped" is a true thing to tell somebody.
	if strings.Contains(text, couldNotReach) {
		t.Errorf("a stall AFTER progress rendered the unreachable copy, which "+
			"describes a connection that plainly worked:\n%s", text)
	}
	if strings.Contains(text, connectionDropped) {
		t.Errorf("a stall rendered the dropped-connection copy:\n%s", text)
	}
}

// TestTheUnreachableCopyIsOnlyForAFailureThatSentNothing is the positive
// half of the exclusivity the row above asserts negatively. Without it,
// deleting the unreachable sentence outright would leave both rows green.
func TestTheUnreachableCopyIsOnlyForAFailureThatSentNothing(t *testing.T) {
	run := newDeployRun(t, fixtureProject(t, "valid")).scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.script.uploadOverride = deadAddress(t)

	handoff, err := run.run()
	defer handoff.Release()
	if err == nil {
		t.Fatal("an upload to an address nothing answers on was reported as completed")
	}
	text, _ := renderedBytes(t, err)
	if !strings.Contains(text, couldNotReach) {
		t.Errorf("a failure that sent nothing did not render the unreachable copy:\n%s", text)
	}
	if strings.Contains(text, uploadStalled) {
		t.Errorf("a dial failure rendered the stall copy:\n%s", text)
	}
}
