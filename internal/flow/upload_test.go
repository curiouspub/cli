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

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/pack"
	"github.com/curiouspub/cli/internal/timing"
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
	store := newObjectStore(t, nil, 0)

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
	store := newObjectStore(t, nil, 0)
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

// The paced-store fixture, DERIVED FROM THE WINDOW rather than written
// down beside it.
// -------------------------------------------------------------------
//
// The slow-upload row asserts two things at once: that no gap between
// two progress events reached the stall window, and that the upload as a
// whole spent at least THREE windows — which is what makes it evidence
// that a total deadline would have killed the same upload. The second
// assertion is what costs money, and it costs money in proportion to the
// window, because the only way to spend three windows uploading is to
// pace three windows' worth of bytes past a store that drains at a fixed
// rate.
//
// So the fixture cannot be a constant: it follows whatever window the
// leg's own measurement produced, and written as constants the two
// numbers would agree with the window on the leg they were typed on and
// nowhere else.
//
// WHAT IT CANNOT DO IS FOLLOW A WINDOW ALL THE WAY UP. An earlier
// version of this comment said a leg that cannot pin its socket buffers
// simply pays — a wider window and a fixture grown to match. pacingFor
// below refuses at about a 2.47 second window, because past that the
// body exceeds this client's own input limit, and that limit is the
// product's rather than the suite's. A leg that cannot pin therefore
// stops; see the Pin field in internal/timing.

const (
	// pacedChunk and pacedPause are the store's drain rate: it swallows
	// pacedChunk bytes, waits pacedPause, and goes again. They are the
	// GOVERNING quantity's other half — the gap a window bounds is the
	// time for the client's send buffer to free space, and space frees
	// at exactly this rate.
	pacedChunk = 64 << 10
	pacedPause = 25 * time.Millisecond

	// The paced phase is this many windows long, as a fraction, because
	// the row asserts three and a fixture sized at exactly three would
	// be one scheduling hiccup from failing its own assertion. The
	// halves are real: 3.5 is a margin over the assertion, not over the
	// gap, and the two margins are different questions.
	pacedWindowsNum = 7
	pacedWindowsDen = 2

	// pacedFloor is the least paced volume this fixture is ever built
	// with, however small the window gets, and it is a floor on the
	// MECHANISM rather than on the duration. The client does not begin
	// waiting on buffer space until it has handed over a block point's
	// worth of body; a paced phase shorter than that is a phase the
	// client spent writing into a buffer, and a row that never blocked
	// measured nothing at all.
	//
	// Six MiB is roughly ten times the largest block point recorded in
	// internal/timing — 622,592 bytes on darwin, with a 16 KiB pin — and
	// it is deliberately not derived from that record. A floor exists to
	// be right when the record is empty, which is the state of two of the
	// three legs, and a floor computed from a leg's own measurement would
	// be widest exactly where least is known.
	pacedFloor = 6 << 20

	// drainTail is how much body is left after the pacing stops, to be
	// drained at full speed. It matters for the reason the paced phase
	// does: once the client has handed its last byte to the socket there
	// is no progress left to report, so a paced drain of a bufferful
	// would look like a stall the client did not cause. It is a constant
	// rather than a fraction because what it has to exceed is a socket
	// buffer, which does not grow with the window.
	drainTail = 6 << 20
)

// uploadPacing is one built fixture: the store's knobs, how much it
// paces, and how large the body has to be.
type uploadPacing struct {
	chunk      int
	pause      time.Duration
	pacedBytes int64
	bodySize   int64
}

// pacingFor derives the fixture from the stall window it has to spend
// three of.
//
// IT REFUSES RATHER THAN TRUNCATES when the answer will not fit. A
// window wide enough that the row proving it cannot be built inside this
// product's own 30 MB input limit is a finding about the ROW — the two
// constraints, a five-times margin and a transfer spanning three
// windows, are set against each other by one kernel buffer — and the
// answer to that is to say so, not to quietly build a smaller fixture
// that passes by asserting less.
func pacingFor(t *testing.T, window time.Duration) uploadPacing {
	t.Helper()
	paced := int64(pacedChunk) * pacedWindowsNum * window.Nanoseconds() /
		(pacedWindowsDen * int64(pacedPause))
	if paced < pacedFloor {
		paced = pacedFloor
	}
	body := paced + drainTail
	// The project carries a few hundred bytes of Astro scaffolding
	// besides its payload, and the packed archive is measured against
	// the same ceiling as the source, so the headroom is not decorative.
	if body > wire.MaxSourceTotalBytes-(1<<20) {
		t.Fatalf("a %v stall window needs %d bytes of paced body to spend three of "+
			"itself, and a %d-byte body will not pass this client's own %d-byte input "+
			"limit.\nThat is not a fixture to shrink: the window and the "+
			"three-window assertion are set against each other by one kernel buffer, "+
			"and which of them gives is a ruling. Pin the buffers on this leg, or "+
			"rule on the row.",
			window, paced, body, int64(wire.MaxSourceTotalBytes))
	}
	return uploadPacing{chunk: pacedChunk, pause: pacedPause, pacedBytes: paced, bodySize: body}
}

// bulkyProject is a deployable project whose archive is far larger than
// any socket buffer between this client and a store on loopback.
//
// THE SIZE IS THE POINT, and it is sized against a MEASUREMENT. A small
// body is swallowed whole by the kernel's socket buffers, so the client
// finishes writing before the store has read anything and the rows below
// would measure nothing — a "slow" store the client never waited for, and
// a "wedged" one it had already finished with. The bytes the client hands
// over before it blocks — the block point — is recorded per leg in
// internal/timing, beside the window it governs, and pacingFor above is
// what keeps this fixture past it as the window moves. The content is
// random so the packer's gzip cannot shrink it back to nothing.
//
// THE FILES ARE SPLIT AT FOUR MiB, which is under the per-file cap this
// client enforces with room to spare. A fixture that tripped a local
// limit would fail these rows at the pre-flight, before a byte reached a
// socket, and the failure would name a limit rather than a window.
func bulkyProject(t *testing.T, size int64) string {
	t.Helper()
	const perFile = 4 << 20
	files := astroProject(nil)
	src := rand.New(rand.NewSource(1))
	for written := int64(0); written < size; {
		n := int64(perFile)
		if remaining := size - written; remaining < n {
			n = remaining
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(src, buf); err != nil {
			t.Fatalf("building the bulky fixture: %v", err)
		}
		files[fmt.Sprintf("public/bulk-%d.bin", written/perFile)] = buf
		written += n
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
// THE WINDOW IS NOT WRITTEN HERE, and that is the mechanism rather than
// a tidiness preference. It lives in internal/timing with the quantity
// it bounds, the reader it was measured through, the worst gap seen on
// each leg, the run count and the date — because a number written
// inline carries its value and not the reason it was chosen, and four of
// this repository's five stall windows were once numbers somebody
// picked. The first window here was one of them: 250 ms, sized against
// the store fixture's own pacing knob, failing roughly one run in six.
// A timing row whose margin is smaller than the thing it did not measure
// is a flake with a schedule.
//
// The paced phase covers the first half of the body and the rest is
// drained at full speed. The tail must exceed the socket buffers: once
// the client has handed over its last byte there is no progress left to
// report, so a paced drain of a bufferful would look like a stall the
// client did not cause.
//
// THE SOCKETS ARE PINNED AT BOTH ENDS, BEFORE EITHER END EXISTS, and
// that is what makes the window a margin over something. The gap this
// row must not reach is the time for the kernel's send buffer to free
// space — a quantity neither end of this connection holds still, because
// both kernels grow a connection's buffers as it carries traffic.
// Measured rather than argued: the same probe over a warm connection
// reported 594 ms where a fresh one reported 434 ms. Pinned, the gap
// collapses towards the store fixture's own pacing quantum, which is a
// number this repository chose.
//
// The pin is a PAIR because a socket option has an end and an upload has
// two of them; it is read back because setsockopt may clamp, round,
// double or ignore a request and says so nowhere; and it arrives through
// pinnedUploadRun rather than a setter because a size set after the
// store's listener is already accepting is a size some connection can
// beat. It is also SAMPLED while the body moves, because a read-back is
// an instant and a condition is a duration — on darwin the receive end
// does not stay where it is put. See socketpin_test.go.
//
// THE FIXTURE IS DERIVED FROM THE WINDOW rather than written here, so
// the three-window assertion below keeps meaning what it says on a leg
// whose window is not this one's. See pacingFor.
//
// REQUIRED MUTATION: stop resetting the watchdog on progress.
//
// REQUIRED MUTATION, RUN 2026-09-10, for the fixture derivation: make
// pacingFor return its floor whatever the window is. Reds here and
// nowhere else —
//
//	the upload took 2.701646834s, which is under 3s — this row did not
//	spend long enough to prove a total deadline would have killed it
//
// — which is the assertion this row exists for going quiet by 300 ms.
// The two other mutations this row carries, on the pin itself, are
// recorded beside the functions they break in socketpin_test.go: they
// red HERE, and the record belongs where the property lives.
func TestASlowUploadIsNotAStalledOne(t *testing.T) {
	// THE SELECTOR IS WRITTEN OUT rather than reached through a local
	// holding the entry, because the guard in internal/timing resolves a
	// stall window through exactly that shape and one level of local
	// assigned once from it. A local entry with `stall := entry.Window`
	// resolves to nothing and reds there — which is the guard working:
	// an identifier that starts at a registry window and could be
	// replaced by a number of somebody's own is the hole it exists to
	// close.
	stall := timing.UploadSlowIsNotStalled.Window
	pacing := pacingFor(t, stall)

	run, client, fixture := pinnedUploadRun(t, bulkyProject(t, pacing.bodySize), pinnedBuffer)
	run.scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.deps.UploadStallTimeout = stall
	run.store.readChunk = pacing.chunk
	run.store.readPause = pacing.pause
	run.store.pauseUntil = pacing.pacedBytes

	started := time.Now()
	handoff, err := run.run()
	defer handoff.Release()
	elapsed := time.Since(started)

	// THE POSITIVE CONTROL FIRST, because everything below it is a claim
	// about a pinned connection and a pin nobody applied is silent.
	pinsWereApplied(t, client, fixture)
	confirmPin(t, &timing.UploadSlowIsNotStalled, probeLeg(), observedPin(client, fixture))

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
// IT IS PINNED LIKE ITS SIBLING, for the reason the two share a window:
// one number has to let a slow upload through and stop a wedged one, so
// a wedged row running over an autotuned socket while the slow row runs
// over a pinned one would be two rows proving two different things with
// one constant.
//
// REQUIRED MUTATION: collapse the stall branch into the unreachable one.
func TestAWedgedUploadStopsAndSaysSo(t *testing.T) {
	// Written out for the reason its sibling's is, one row up.
	stall := timing.UploadWedgedStops.Window

	run, client, fixture := pinnedUploadRun(t, bulkyProject(t, pacingFor(t, stall).bodySize), pinnedBuffer)
	run.scriptedLogin()
	run.prompt.confirms = []answer{no()}
	run.deps.UploadStallTimeout = stall
	run.store.stopReadingAfter = 64 << 10

	started := time.Now()
	handoff, err := run.run()
	defer handoff.Release()
	elapsed := time.Since(started)

	pinsWereApplied(t, client, fixture)
	confirmPin(t, &timing.UploadWedgedStops, probeLeg(), observedPin(client, fixture))

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

// -------------------------------------------------------------------
// The upload's transport, and what production gets when nothing is
// injected
// -------------------------------------------------------------------

// The names the syntax-tree half of the row below looks for, written out
// for the reason the two at the top of this file are: a rename that
// misses one produces a FAILURE rather than a silent pass.
const (
	uploadTransportFuncName = "uploadTransport"
	uploadDepsTypeName      = "uploadDeps"
)

// TestTheUploadTakesTheAPIClientsOwnTransportWithNoSeamSupplied is the
// price of DeployDeps.UploadTransport, paid in the round that added it.
//
// A SEAM IS A SECOND WAY FOR A VALUE TO ARRIVE, and the failure it
// invites is not the injected value being wrong — a row supplies that one
// and can watch it work. It is the DEFAULT quietly ceasing to be what
// ships. Every row in this package injects a transport, so every row
// would go on passing while production moved to a different one, and the
// suite would be measuring the seam rather than the client.
//
// The thing that must not change is stated as IDENTITY rather than as
// resemblance, because the near miss is a transport that looks right. A
// bare &http.Transport{} has no Proxy, so it ignores HTTPS_PROXY,
// HTTP_PROXY and NO_PROXY altogether — and it does so quietly, since a
// direct connection still works on every dev machine and every CI runner
// and fails only at the one desk behind a corporate proxy. So the row
// asserts the pointer, and then asserts the property the pointer buys,
// so that a client whose own transport had lost its proxy resolver could
// not satisfy this by identity alone.
//
// THE THIRD HALF IS THE CALL SITE. The two checks above are about the
// resolver, and a resolver nothing calls is a correct answer to a
// question nobody asked. The syntax-tree pass reads deploy.go and
// requires the upload's Transport field to be filled by that function,
// so a later edit putting a fresh transport there is a red rather than a
// discovery.
//
// THAT THE SEAM IS ACTUALLY THREADED is asserted next door and not here:
// the two write-side rows fail outright if the transport they supply
// never dialled, because the pin they hang everything on is applied in
// its dialler. A seam that accepted a transport and dropped it would
// leave those two rows unable to find a single pinned connection.
//
// REQUIRED MUTATIONS, ALL THREE RUN 2026-09-10, one per assertion,
// because a row with three claims and one mutation has tested one claim:
//
//  1. Return a fresh &http.Transport{Proxy: http.ProxyFromEnvironment}
//     from uploadTransport instead of the client's — the near miss, and
//     the whole reason the check is identity. Reds on the first
//     assertion with two pointers; every other row in the package stays
//     green, which is the finding: they all inject.
//  2. Drop the seam branch from uploadTransport, so a supplied transport
//     is accepted and ignored. Reds on the third assertion AND in
//     TestASlowUploadIsNotAStalledOne, which cannot find a single pinned
//     connection — the seam being threaded is asserted over there, and
//     this is what that looks like when it breaks.
//  3. Fill the upload's Transport at the call site with authed
//     .Transport() directly. The first three assertions stay green and
//     the syntax-tree pass reds, naming the file and line: a resolver
//     nothing calls is a correct answer to a question nobody asked.
func TestTheUploadTakesTheAPIClientsOwnTransportWithNoSeamSupplied(t *testing.T) {
	client, err := api.New("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("building an API client to ask what its transport is: %v", err)
	}

	if got := uploadTransport(DeployDeps{}, client); got != client.Transport() {
		t.Errorf("with no transport supplied the upload got %p and the API client's "+
			"own is %p. Production passes nothing here, so this is the transport "+
			"every real upload travels over — and one that merely resembles the "+
			"client's is how a proxy configuration, extra TLS trust material or any "+
			"other connection-level setting silently stops applying to the largest "+
			"request this program makes.", got, client.Transport())
	}

	// THE PROPERTY THE IDENTITY BUYS. Without this, a client whose own
	// transport had lost its proxy resolver would satisfy the check
	// above and the row would be asserting that two things are the same
	// thing without any claim about what that thing is.
	if client.Transport().Proxy == nil {
		t.Error("the API client's transport no longer resolves a proxy from the " +
			"environment, so the identity asserted above is an identity with " +
			"something that ignores HTTPS_PROXY, HTTP_PROXY and NO_PROXY")
	}

	// AND IT MUST STILL SAY YES TO THE SEAM. A resolver that returned
	// the client's transport unconditionally would pass everything above
	// and make the seam a field nothing reads — which is the defect this
	// round is meant to avoid rather than commit.
	seam := &http.Transport{}
	if got := uploadTransport(DeployDeps{UploadTransport: seam}, client); got != seam {
		t.Errorf("a supplied transport was not the one resolved: got %p, want %p. "+
			"A field that is accepted and not used is worse than one that was never "+
			"accepted, because it reads as a working knob.", got, seam)
	}

	assertTheUploadFillsItsTransportFromTheResolver(t)
}

// assertTheUploadFillsItsTransportFromTheResolver reads deploy.go and
// requires the upload's Transport field to be the resolver's answer.
//
// It is a function rather than more lines in the row above so the
// failures read apart: one is "the resolver answers wrongly" and this is
// "nothing asks the resolver", and an implementer looking at a red
// should not have to work out which.
func assertTheUploadFillsItsTransportFromTheResolver(t *testing.T) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "deploy.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing deploy.go: %v", err)
	}

	literals, fields := 0, 0
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		name, ok := lit.Type.(*ast.Ident)
		if !ok || name.Name != uploadDepsTypeName {
			return true
		}
		literals++
		for _, element := range lit.Elts {
			kv, ok := element.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := kv.Key.(*ast.Ident)
			if !ok || key.Name != "Transport" {
				continue
			}
			fields++
			call, ok := kv.Value.(*ast.CallExpr)
			if !ok {
				t.Errorf("the upload's Transport at %s is not a call at all, so "+
					"whatever production now uploads through, it is not what %s "+
					"answers", fset.Position(kv.Pos()), uploadTransportFuncName)
				continue
			}
			fn, ok := call.Fun.(*ast.Ident)
			if !ok || fn.Name != uploadTransportFuncName {
				t.Errorf("the upload's Transport at %s is filled by something other "+
					"than %s, so the default this row asserts is no longer the "+
					"default anything uses",
					fset.Position(kv.Pos()), uploadTransportFuncName)
			}
		}
		return true
	})

	if literals == 0 {
		t.Fatalf("deploy.go builds no %s literal, so this row passed over nothing "+
			"— the upload is now started somewhere else and its transport is "+
			"unasserted", uploadDepsTypeName)
	}
	if fields == 0 {
		t.Fatalf("no %s literal in deploy.go sets a Transport, so the upload takes "+
			"the zero value and every connection-level setting the API client "+
			"carries is being dropped", uploadDepsTypeName)
	}
}
