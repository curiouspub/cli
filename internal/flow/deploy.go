package flow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/check"
	"github.com/curiouspub/cli/internal/config"
	"github.com/curiouspub/cli/internal/pack"
	"github.com/curiouspub/cli/internal/preflight"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// DeployPrompter is the slice of the terminal the whole sequence needs,
// which is the union of what its steps need: narration, the user's own
// build output, a line of text, an address, and a yes-or-no question.
//
// It is declared HERE, by the consumer, for the same reason every other
// seam in this package is. That it happens to share four methods with
// LoginPrompter is arithmetic rather than design — this is the union of
// five steps' requirements and that is one step's — and reusing the name
// would mean a step that later stopped needing a method silently
// narrowing what the sequence asks for.
//
// Result is the odd one and it is here because of the stream split: it
// writes to STDOUT, and the only thing that belongs there is the build's
// own output. Everything this program says about that output is narration
// and goes to stderr through Step, so a redirected stdout collects the
// build log and nothing else.
type DeployPrompter interface {
	Step(format string, args ...any)
	Result(format string, args ...any)
	Line(prompt string) (string, error)
	Email(prompt string) (string, error)
	Confirm(question string, defaultYes bool) (bool, error)
}

// DeployProgress is where a run says what it is doing WHILE it is doing
// it, for a caller that is not a terminal.
//
// IT IS NOT A SECOND RENDERER. The terminal already learns all of this,
// as prose, through the prompter — and a caller that is not a terminal
// would have to parse that prose back into a phase and a line, which is
// exactly what this project refuses to do everywhere else. What arrives
// here are the two values the stream carried, in the types the contract
// declares them in.
//
// THE PHASE IS THE CONTRACT'S OWN TYPE rather than a string, so a phase
// this build predates travels through unchanged and a vocabulary cannot
// grow a second spelling on the way past.
//
// BOTH HALVES, EVERY TIME. A phase event supplies the phase and carries
// the most recent line with it; a log line supplies the line and carries
// the phase it arrived under. Either may be empty — a build that has
// said nothing yet has no last line, and a line that arrives before any
// phase event has no phase — and an empty half is the honest answer
// rather than an omission.
//
// A REPORT IS MADE ONLY FOR SOMETHING THE READER HAS NOT ALREADY BEEN
// SHOWN. The event stream replays from the beginning on every
// reconnection, and a channel that spoke for each replayed event would
// narrate one build several times over.
type DeployProgress func(phase wire.Phase, line string)

// DeployDeps is everything Deploy needs from outside itself.
type DeployDeps struct {
	// Dir is the directory to deploy, as the user typed it. Empty means
	// the current one, which is what a bare `curious deploy` asks for.
	Dir string

	// Prompt is the terminal. Required.
	Prompt DeployPrompter

	// APIURL is an explicit endpoint override, or empty to resolve the
	// one this run would use anyway. It goes through the same resolver
	// every other caller uses, so a stored token's endpoint and the
	// endpoint actually dialled are answers to one question.
	APIURL string

	// TempDir is the directory the archive's working directory is made
	// in; empty means the system's own. It is a parameter for the reason
	// the packer's is: the archive is a file on a real volume, and a
	// suite that owns the directory can answer "was anything left
	// behind" rather than guessing at a path.
	TempDir string

	// FS is the filesystem the walk and the packer read the project
	// through. Optional; the real one is used without it.
	//
	// IT EXISTS SO THE TRAVERSAL CAN BE COUNTED. "One walk, three
	// readers" is a rule about how many times the tree is read, and from
	// outside a sequence that walked three times and one that walked once
	// produce identical output — so without something that counts, the
	// rule has no row that can see it. The pre-flight engine keeps its
	// own seam for its own reasons and this deliberately does not borrow
	// it: the two interfaces are different shapes, and widening either to
	// serve the other adds a method for one caller.
	FS pack.FS

	// Interrupts installs the Ctrl-C handler and returns the function
	// that removes it again, with the tidy-up this run needs on that
	// path. Optional; without one a run simply gets the default signal
	// behaviour, which ends the process and leaves the archive.
	//
	// IT IS A SEAM RATHER THAN A DIRECT CALL because the handler ends
	// the process, so nothing in a test can observe the real one having
	// been installed by installing it. What a row can see through here is
	// that this sequence hands over a tidy-up at all, and that the
	// function it hands over removes what the run put on the machine.
	Interrupts func(cleanup func()) (stop func())

	// Now is the clock. Optional.
	Now func() time.Time

	// Progress is where the build's phases and output are reported to a
	// caller that is not a terminal. Optional; without one the run says
	// nothing anywhere except through Prompt, which is what the command
	// passes and what every existing row measures.
	//
	// IT IS A FIELD ON THE SEQUENCE RATHER THAN A METHOD ON THE PROMPTER,
	// and the reason is the types. A prompter's two methods take a format
	// and arguments, so a phase reaching a caller through one arrives as
	// a sentence with the value inside it — and the caller that needs
	// this is the one that must not parse sentences.
	Progress DeployProgress

	// UploadStallTimeout is how long the upload waits for the next byte
	// before giving up. Optional; without one the upload's own constant
	// applies.
	//
	// IT IS A SEAM FOR THE SAME REASON Now IS. The behaviour it governs
	// is "a slow upload must survive and a stopped one must not", and
	// the only way to see either at the real thirty seconds is to spend
	// thirty seconds. Injecting it lets a row prove both in
	// milliseconds. It is the one constant arriving by argument, not a
	// second constant: nothing here chooses a different number, and
	// production passes none at all.
	UploadStallTimeout time.Duration

	// StreamStallTimeout is how long the build log waits for the next
	// byte before deciding the connection has stopped talking, and
	// StreamReconnectStep is the step of the delay schedule between
	// attempts to pick it up again. Both optional; without them the
	// stream's own constants apply.
	//
	// THEY ARE SEAMS FOR THE REASON UploadStallTimeout IS, and they are
	// its NEIGHBOURS RATHER THAN ITS REUSE. The upload's window bounds a
	// body being pushed at an object store; this one bounds a silence on
	// a connection whose far end sends a keep-alive on a published
	// interval. The two are the same shape and answer different
	// questions, so each is chosen where it is used.
	StreamStallTimeout  time.Duration
	StreamReconnectStep time.Duration

	// PublishRetryInterval is the pause between two asks when the server
	// says the deploy is not ready yet. Optional; without one the
	// publish's own constant applies.
	//
	// IT IS A SEAM FOR THE REASON THE OTHERS ARE, and it moves the PACE
	// alone. What bounds the asking is a window measured on Now, so a
	// row drives the race by advancing the clock and this only keeps it
	// from spending real seconds doing so.
	PublishRetryInterval time.Duration

	// UploadTransport is the transport the archive's body travels over.
	// Optional; without one the upload takes the API client's, which is
	// what production gets and what uploadTransport below returns.
	//
	// IT IS A SEAM FOR A REASON THE OTHERS ARE NOT, and the reason is
	// worth stating because nothing about the shipped behaviour needs
	// it. UploadStallTimeout exists so a row can see a thirty-second
	// window in milliseconds; this one exists so a row can see the same
	// window over a socket whose BUFFERS ARE KNOWN. The quantity the
	// upload's stall window is a margin over — the time for the kernel's
	// send buffer to free space — is not a property of this program at
	// all: both kernels grow a connection's buffers as it carries
	// traffic, and a margin over an autotuned quantity is a margin over
	// a number nobody chose. Measured rather than assumed: the same
	// probe over a warm connection reported 594 ms where a fresh one
	// reported 434 ms. A test cannot pin a socket it never sees, and
	// before this field the only transport the upload could use was one
	// built out of reach inside this function.
	//
	// A SEAM THAT CAN SILENTLY CHANGE WHAT SHIPS IS WORSE THAN NO SEAM,
	// so what production gets when this is empty is asserted by a row of
	// its own rather than left to reading — see uploadTransport.
	UploadTransport *http.Transport
}

// uploadTransport is the transport the upload's body travels over: the
// caller's when one was supplied, and otherwise the API client's own.
//
// THE DEFAULT IS THE POINT OF THIS FUNCTION EXISTING. Written inline at
// the call site, "the seam or the client's" is one `if` that a later
// change can quietly rewrite into "a fresh transport" — and a fresh
// &http.Transport{} is not the client's: it has no Proxy, so it ignores
// HTTPS_PROXY, HTTP_PROXY and NO_PROXY altogether, and it does so
// quietly, because a direct connection still works everywhere except the
// one desk behind a corporate proxy. Pulled out here it has a name a row
// can call, and the row beside it asserts that with no seam supplied the
// answer is the API client's transport ITSELF rather than one that
// resembles it.
func uploadTransport(deps DeployDeps, client *api.Client) *http.Transport {
	if deps.UploadTransport != nil {
		return deps.UploadTransport
	}
	return client.Transport()
}

// Outcome is what a completed run PRODUCED, as a value rather than as
// something printed.
//
// IT IS A GO SURFACE AND NOT A CHANGE TO THE COMMAND. The CLI still
// writes the address to stdout and everything it says about the address
// to stderr, and that is asserted byte for byte by the transcript row
// next door rather than left to this sentence. What this adds is a
// caller that is not a terminal: an agent-facing surface renders these
// same facts as fields, and the alternative — parsing them back out of
// the prose a person reads — is exactly what this project refuses to do
// with prose everywhere else.
//
// IT IS A FIELD ON Handoff RATHER THAN A SECOND RETURN VALUE, and the
// reason is what the two spellings would each claim. Deploy returns a
// Handoff on success and nil on every failure, so an Outcome returned
// beside it would be nil in exactly the same cases and never in any
// other: two results whose presence is ONE fact, in a signature that
// says you can have either without the other. And a caller wanting the
// outcome holds the Handoff regardless, because it has an archive to
// release. So there is no call this shape makes awkward, and the
// command's own call site does not move at all.
//
// DeployID LIVES HERE AND NOWHERE ELSE. It was a field of Handoff, with
// no reader; it is one home rather than two, which is the whole of why
// it moved instead of being copied.
//
// EVERY FINDING, NOT THE ADVISORIES. What a warning costs — whether it
// stops a run, whether it is worth asking about, whether it is worth
// showing at all — is a decision the surface rendering it makes, and
// this package is where those decisions live rather than a second set of
// checks. Filtering here would make this type the third opinion on a
// question two renderers already answer differently on purpose: the
// terminal asks about a warning and an agent cannot be asked. So the
// report's own list is handed over whole, in report order, as the fresh
// slice Findings already returns per call.
//
// NOTHING IS COMPUTED. ExpiresAt is the instant the server sent, carried
// as it arrived; the zero value means the server sent none, which is a
// thing it is entitled to do and not a reason to invent one.
type Outcome struct {
	// DeployID is the record the server created for this archive, and
	// the handle every later call in the sequence names. NOTHING
	// PERSISTS IT: it belongs to this run, and a create whose upload
	// never starts leaves a record the server discards on its own.
	DeployID string

	// Subdomain is the LABEL the publish returned — the left-hand part
	// alone, with no domain and no scheme. The server holds the label
	// and has no representation of the base domain, so the address is
	// composed from this by PublishedURL and never assembled twice.
	Subdomain string

	// ExpiresAt is when the server says this deploy stops answering.
	// Zero when the server sent nothing.
	ExpiresAt time.Time

	// Preflight is the validated report's findings, in report order.
	Preflight []check.Finding
}

// Handoff is what a completed run leaves in the caller's hands: the
// archive to release, what was measured on the way, and what the run
// produced.
//
// IT IS AN INTERNAL TYPE RATHER THAN A WIRE ONE, because it is a
// different thing rather than a nicer spelling of one. The wire request
// carries a single field — the packed size — and this carries six, four
// of which the server never sees: where the archive is on this machine,
// what it hashes to, how many entries went into it, and the client that
// sent it. Reshaping a wire type into a roomier internal one is how two
// copies of a contract drift; this is not that, because there is no
// second copy of anything the contract defines.
//
// THE ARCHIVE OUTLIVES Deploy AND THE CALLER OWNS IT from the moment
// this is returned. Release is how it gives it back, and a caller that
// defers Release the moment it has a Handoff has covered every ordinary
// exit path in one line.
type Handoff struct {
	// ArchivePath is the packed project on this machine.
	ArchivePath string

	// SHA256 is the archive's digest, computed as it was written.
	SHA256 string

	// Bytes is the archive's size on disk — the number the wire request
	// declares, which is the packed one rather than the source total.
	Bytes int64

	// Entries is how many files went into the archive.
	Entries int

	// Client is the API client carrying this run's bearer token.
	Client *api.Client

	// Outcome is what the run produced. See the type above for why it
	// lives here rather than beside this one.
	Outcome Outcome

	release func()
}

// Release removes everything this run put on the machine and hands the
// interrupt signal back to the runtime. It is safe to call more than
// once, and safe on a nil Handoff, so a caller can defer it beside the
// error check rather than after it.
func (h *Handoff) Release() {
	if h == nil || h.release == nil {
		return
	}
	h.release()
}

// workDirPattern names the directory the archive is written into. The
// archive gets a random name of its own inside it; what this directory
// buys is a single thing to remove — including a half-written archive
// from a run that was killed mid-pack, which has a name nobody recorded.
const workDirPattern = "curious-deploy-*"

// Deploy runs the whole of `curious deploy`: everything local, then the
// create, the upload, the build, and the address the build answers at.
//
// # The order, and why it is the order
//
//  1. Resolve the directory.
//  2. Load the stored token. No network call.
//  3. Walk the project, ONCE.
//  4. Pre-flight and 5. the local limits, over that one walk.
//  6. Only with no usable token: the capacity check, then the login.
//  7. Pack.
//  8. Create the deploy.
//  9. Upload the archive.
//  10. Start the build.
//  11. Render the build log.
//  12. Publish, and print the address.
//
// LOCAL TRUTHS BEFORE GLOBAL STATE, and the consequence is the sentence
// worth keeping: A PROJECT THAT CANNOT DEPLOY MAKES ZERO NETWORK CALLS.
// Steps 3 to 5 are free, local and instant, so nothing on the network
// runs until they have passed. Written the other way round — login
// first, as the obvious reading of "authenticate, then work" suggests —
// it is wrong twice over: a first-timer standing in the wrong directory
// is walked through email verification, spending one of the sends the
// server allows in an hour and a real email, before being told there is
// no package.json; and an unattended run with a fresh config has no
// token, so it dies at the email prompt with a message about needing a
// terminal instead of the one naming what is wrong with the project.
//
// THE CAPACITY CHECK STAYS IMMEDIATELY BEFORE THE LOGIN IT GATES. That
// adjacency is the one that matters, because the daily cap counts
// ACCOUNTS and an account is spent at the verify step — so a returning
// user with a usable token reaches neither call, and the pair moved down
// the sequence together rather than separately.
//
// ONE WALK, THREE READERS. The file list feeds the scan for hard-coded
// development URLs, the limits, and the packer. Walking again for any of
// them would let one of them disagree with the list the user was shown
// and consented to, which is the one difference nobody would think to
// look for.
//
// Nothing is written except the config file on a successful login, and
// the archive — which the caller removes with Release.
func Deploy(ctx context.Context, deps DeployDeps) (*Handoff, error) {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	fsys := deps.FS
	if fsys == nil {
		fsys = pack.OSFileSystem{}
	}

	// 1. THE DIRECTORY.
	root, err := resolveProjectDir(deps.Dir)
	if err != nil {
		return nil, err
	}

	// 2. THE STORED TOKEN. Read before anything is dialled, because the
	// answer decides whether a capacity check is even the right question
	// — and because a second read later would be a second chance to
	// disagree with this one about which file was read.
	endpoint := api.ResolveBaseURL(deps.APIURL)
	cfg, err := config.Load(endpoint)
	if err != nil {
		return nil, configFailure(err)
	}
	for _, warning := range cfg.Warnings {
		deps.Prompt.Step("%s", warning)
	}
	if cfg.Token == "" && cfg.NoTokenReason != nil {
		// Why a login is about to happen. The reason is written for a
		// person by the package that found it out, and a run that
		// silently asked for an email again would leave somebody
		// wondering what became of the login they did yesterday.
		deps.Prompt.Step("%s", cfg.NoTokenReason.Error())
	}
	token := cfg.Token

	// 3. THE WALK.
	started := now()
	tree, err := pack.Walk(fsys, root)
	if err != nil {
		return nil, unreadableProjectFailure(err)
	}

	// 4 and 5. PRE-FLIGHT AND THE LIMITS, in ONE report.
	//
	// They are two steps and one render, and that is forced rather than
	// chosen: the combiner refuses a report that does not cover every
	// declared check, and the limits are declared checks. So a report
	// carrying a pre-flight hard stop has to carry the limit rows too,
	// which means the limits are measured on runs whose verdict nobody
	// will read. They are arithmetic over a list already in memory.
	//
	// The same gate is what makes the check list below checkable from
	// outside: forget one, and the report cannot be built at all.
	limits := pack.Limits(tree.Files)
	report, err := check.Combine(
		preflight.Run(deployChecks(tree.Files), preflight.OSFileSystem{}, root),
		tree.Results,
		limits,
	)
	if err != nil {
		// A producer claimed the wrong questions, or none. Nothing here
		// is the user's, so it renders as the internal fault it is.
		return nil, err
	}
	if err := RenderPreflight(deps.Prompt, report, now().Sub(started)); err != nil {
		return nil, err
	}

	// 6. CAPACITY, THEN THE LOGIN — only with no usable token.
	if token == "" {
		token, err = authenticate(ctx, deps, endpoint, now)
		if err != nil {
			return nil, err
		}
	}

	authed, err := api.New(endpoint, api.WithToken(token))
	if err != nil {
		return nil, endpointUnusableFailure()
	}

	// 7. THE PACK, and the tidy-up that covers every way out of it.
	//
	// The working directory is created HERE rather than at the top, so a
	// project that never gets this far leaves nothing on the machine at
	// all — not even an empty directory nobody removed.
	workdir, err := os.MkdirTemp(deps.TempDir, workDirPattern)
	if err != nil {
		return nil, tempDirFailure(err)
	}

	// ONE CLEANUP, TWO WAYS TO REACH IT. The interrupt handler cannot
	// use the defer below — it ends the process by letting the signal
	// kill it, and nothing deferred runs after that — and the ordinary
	// paths cannot use the handler. So both are given the same function
	// rather than each removing the archive its own way, which is how
	// one of the two comes to remove something slightly different.
	cleanup := func() { _ = os.RemoveAll(workdir) }
	stop := func() {}
	if deps.Interrupts != nil {
		stop = deps.Interrupts(cleanup)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			stop()
			cleanup()
		})
	}

	handedOver := false
	defer func() {
		if !handedOver {
			release()
		}
	}()

	prepared, packedResults, err := pack.Prepare(fsys, root, workdir, tree.Files, limits)
	if err != nil {
		return nil, err
	}
	// THE REPORT LEARNS WHAT THE PACK MEASURED. The report a person
	// consented to was built before anything was packed, so its
	// packed-size row is a by-design decline — the honest answer at that
	// moment. Once the archive exists the check has run for real, and a
	// validated report still carrying the decline is TWO HOMES FOR ONE
	// ID with the machine-readable one false: the person reads a refusal
	// with real numbers while an agent reading the manifest is told
	// nothing was ever packed.
	//
	// Superseding is narrow by construction — only a declined row, only
	// by the same id — so this cannot quietly rewrite anything an earlier
	// producer answered.
	report, err = report.Supersede(packedResults)
	if err != nil {
		return nil, err
	}

	if refused := check.Advisories(packedResults.Findings); len(refused) > 0 {
		// The one refusal a project can reach having passed every other
		// check here. It carries its own copy, so it renders through the
		// same path a lone pre-flight hard stop does.
		return nil, blockedFailure(refused)
	}

	deps.Prompt.Step("%s", ui.Prose(prepared.Receipt))

	// 8. THE CREATE.
	//
	// THE DECLARED SIZE COMES FROM THE PACK, not from a second stat.
	// prepared.Archive.Size was read back off the file the packer had
	// just closed; measuring it again here would be a second answer to
	// one question, free to disagree with the first — and the server
	// signs the upload link with this exact number, so a disagreement
	// is not a warning, it is every upload refused.
	resp, err := createDeploy(ctx, createDeps{
		Client: authed,
		Reauthenticate: func(ctx context.Context) (deployCreator, error) {
			// THE WHOLE FRONT OF THE SEQUENCE, re-entered — capacity
			// first, then the login — rather than a bare second verify.
			// The daily cap counts accounts and an account is spent at
			// the verify step, so skipping the gate here would spend
			// one the server had already said it had no room for.
			fresh, err := authenticate(ctx, deps, endpoint, now)
			if err != nil {
				return nil, err
			}
			client, err := api.New(endpoint, api.WithToken(fresh))
			if err != nil {
				return nil, endpointUnusableFailure()
			}
			authed = client
			return client, nil
		},
		Bytes: prepared.Archive.Size,
		Now:   now,
	})
	if err != nil {
		return nil, err
	}

	// 9. THE UPLOAD.
	//
	// A FAILURE HERE LEAVES A CREATED DEPLOY RECORD, which is the
	// server's to expire. This step adds no state and no file of its
	// own, and the copy says so rather than implying it cleaned up.
	if err := uploadArchive(ctx, uploadDeps{
		URL:          resp.UploadURL,
		ArchivePath:  prepared.Archive.Path,
		Bytes:        prepared.Archive.Size,
		ExpiresAt:    resp.ExpiresAt,
		Transport:    uploadTransport(deps, authed),
		Now:          now,
		StallTimeout: deps.UploadStallTimeout,
	}); err != nil {
		return nil, err
	}

	// 10. THE START, sent ONCE and never again.
	//
	// The status it answers with INFORMS and never BRANCHES: the next
	// action is identical for every value it can carry, so it is rendered
	// and nothing switches on it. A value this build has never heard of
	// renders like any other.
	startResp, err := authed.DeployStart(ctx, resp.DeployID)
	if err != nil {
		return nil, carryingDeployID(startFailure(err), resp.DeployID)
	}
	deps.Prompt.Step("%s%s.", startNarration, string(startResp.Status))

	// 11. THE BUILD LOG.
	//
	// A FAILURE HERE IS NOT A FAILED BUILD, and the copy each ending
	// carries keeps them apart: the build is running on the server and
	// this is only the window onto it.
	status, err := streamBuild(ctx, streamDeps{
		Events: func(ctx context.Context) (io.ReadCloser, error) {
			return authed.DeployEvents(ctx, resp.DeployID)
		},
		Render:        deps.Prompt,
		DeployID:      resp.DeployID,
		StallTimeout:  deps.StreamStallTimeout,
		ReconnectStep: deps.StreamReconnectStep,
		Progress:      deps.Progress,
	})
	if err != nil {
		return nil, carryingDeployID(err, resp.DeployID)
	}
	if status == wire.StatusFailed {
		// THE ONLY VALUE THIS CLIENT ACTS ON, and it acts on it by
		// stopping. Everything else continues, including a value this
		// build predates — the stream is a narrator rather than an
		// authority, and what the output validator makes of the build is
		// a question for the next call rather than for this one.
		return nil, carryingDeployID(buildFailedFailure(), resp.DeployID)
	}

	// 12. THE PUBLISH, AND THE LAST LINE OF THE COMMAND.
	//
	// IT SITS INSIDE THE HAND-OFF'S OWN GUARD, deliberately. Every way
	// this step can fail returns before handedOver is set, so the
	// deferred release still removes the archive — a failure that handed
	// over a file nobody will release would be a temp file left on the
	// machine by the error path, which is the one thing an error path
	// must not do.
	published, err := publishDeploy(ctx, publishDeps{
		Client:        authed,
		DeployID:      resp.DeployID,
		Render:        deps.Prompt,
		Now:           now,
		RetryInterval: deps.PublishRetryInterval,
	})
	if err != nil {
		return nil, carryingDeployID(err, resp.DeployID)
	}
	renderPublished(deps.Prompt, published, now())

	handedOver = true
	return &Handoff{
		ArchivePath: prepared.Archive.Path,
		SHA256:      prepared.Archive.SHA256,
		Bytes:       prepared.Archive.Size,
		Entries:     prepared.Archive.Entries,
		Client:      authed,
		// THE FACTS ARE THE ONES ALREADY IN HAND, and the two sources
		// are deliberately the two that answered: the label and the
		// expiry come from the publish's own response, the deploy id
		// from the create's. Neither is re-derived and nothing is asked
		// a second time — a second answer to a question already answered
		// is free to disagree with the first.
		//
		// THE REPORT IS THE SUPERSEDED ONE — the variable, not a copy
		// taken before the pack. The report a person consented to was
		// built before anything was packed, and the packed-size check
		// has run for real since; superseding is what makes the article
		// true of a run that got this far, and reading the variable is
		// what keeps this from being a second, staler answer. What
		// travels here is the findings half of it, so on a successful
		// run the difference is usually invisible from outside — which
		// is a reason to take the right one without thinking about it,
		// not a reason to think the choice does not matter.
		Outcome: Outcome{
			DeployID:  resp.DeployID,
			Subdomain: published.Subdomain,
			ExpiresAt: published.ExpiresAt,
			Preflight: report.Findings(),
		},
		release: release,
	}, nil
}

// authenticate runs the capacity check and then the login, and returns
// the token that was stored.
//
// IT IS ONE FUNCTION BECAUSE IT IS ENTERED TWICE — once when a run holds
// no usable token, and once when the server refuses the one it holds.
// The order is the load-bearing half: the daily cap counts ACCOUNTS and
// an account is spent at the verify step, so the gate belongs
// immediately before the login it gates, on both routes in.
func authenticate(ctx context.Context, deps DeployDeps, endpoint string, now func() time.Time) (ui.Secret, error) {
	client, err := api.New(endpoint)
	if err != nil {
		return "", endpointUnusableFailure()
	}

	// HaveToken is left at false because every way in here is a run
	// with no token the server will accept. Asking the question again
	// would be a second answer that could disagree with the first.
	if err := CapacityGate(ctx, CapacityDeps{
		Prompt: deps.Prompt,
		API:    client,
		Now:    now,
	}); err != nil {
		return "", err
	}

	// The token is CAUGHT ON ITS WAY TO DISK rather than read back
	// afterwards. Re-loading the file would be a second answer to
	// "which token does this run hold", and the two could differ —
	// another process writing between them is all it takes.
	var issued ui.Secret
	save := func(tok ui.Secret, issuedAgainst string) error {
		if err := defaultTokenWriter(tok, issuedAgainst); err != nil {
			return err
		}
		issued = tok
		return nil
	}
	if err := Login(ctx, LoginDeps{
		Prompt:   deps.Prompt,
		Auth:     client,
		Endpoint: endpoint,
		Save:     save,
		Offer:    NewWaitlistOffer(deps.Prompt, client, now),
		Now:      now,
	}); err != nil {
		return "", err
	}
	return issued, nil
}

// deployChecks is the pre-flight set this sequence runs, over the walked
// file list the development-URL scan reads.
//
// ASSEMBLING IT IS THE SEQUENCE'S DECISION rather than the engine's, and
// that is why the engine exports no constructor for the set: the file
// list comes from a traversal that happens out here, so a package-level
// one would have to either take that list or invent a second traversal.
//
// A SECOND LIST OF THE SAME CHECKS IS SAFE HERE, which is not usually
// true. The combiner refuses a report that leaves a declared check
// uncovered, so forgetting one of these does not produce a quietly
// smaller report — it produces a report that cannot be built.
func deployChecks(files []pack.File) []preflight.Check {
	return []preflight.Check{
		preflight.AstroDepCheck(),
		preflight.LockfileCheck(),
		preflight.AstroConfigCheck(),
		preflight.LocalhostCheck(walkedPaths(files)),
	}
}

// walkedPaths is the walk's list as the scan wants it: project-relative,
// slash-separated, in the walk's own order.
func walkedPaths(files []pack.File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// resolveProjectDir turns what the user typed into an absolute path, and
// refuses anything that is not a directory.
//
// A SYMLINKED ROOT IS FOLLOWED, deliberately, and that is not in tension
// with the walk refusing to follow links INSIDE the tree. Pointing this
// command at a symlink to a project is an ordinary thing to do and names
// a directory the user chose; a link found during the traversal names
// one they did not.
func resolveProjectDir(dir string) (string, error) {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		// Reachable when the working directory itself has gone — a
		// deleted checkout, most often — so the path it could not
		// resolve is the useful half.
		return "", ui.NewFailure(
			"curious couldn't work out which directory you mean.",
			fmt.Sprintf("Resolving %q against the current directory failed: %v.\n\n"+
				"That usually means the directory this command was started in no "+
				"longer\nexists.", dir, err), ui.NextFreshDeploy,
			"Change into a directory that exists and run `curious deploy` again.")
	}

	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", ui.NewFailure(
			"There is nothing at "+abs+".",
			"curious deploys a directory, and that one does not exist.", ui.NextFreshDeploy,
			"Check the path and run `curious deploy <dir>` again — or run it with\n"+
				"no argument at all to deploy the directory you are standing in.")
	case err != nil:
		return "", ui.Quoted(
			"curious couldn't read "+abs+".",
			err.Error(), ui.NextFreshDeploy,
			"Check that the path exists and that you can read it, then run\n"+
				"`curious deploy` again.")
	case !info.IsDir():
		return "", ui.NewFailure(
			abs+" is a file, not a directory.",
			"curious deploys a project directory — the one holding package.json —\n"+
				"rather than a single file.", ui.NextFreshDeploy,
			"Name the directory instead, or run `curious deploy` from inside it.")
	}
	return abs, nil
}

// configFailure is what an unreadable configuration LOCATION ends the run
// as. It is the only thing config.Load reports as an error; every other
// problem with the file comes back as an empty token and a reason, and
// every one of those is recoverable by logging in again.
func configFailure(err error) *ui.Failure {
	return ui.Quoted(
		"curious couldn't work out where to keep your login.",
		err.Error(), ui.NextFreshDeploy,
		"Set CURIOUS_CONFIG to the full path of a config file and run\n"+
			"`curious deploy` again.")
}

// unreadableProjectFailure is what a traversal that could not finish
// ends the run as. The walk's own error names the directory it stopped
// at, which is the half a person can act on.
func unreadableProjectFailure(err error) *ui.Failure {
	return ui.Quoted(
		"curious couldn't read the whole project.",
		err.Error(), ui.NextFreshDeploy,
		"Check that every directory in the project is readable, then run\n"+
			"`curious deploy` again. "+uploadedNothing)
}

// endpointUnusableFailure is what an API address this client refuses to
// dial ends the run as.
//
// IT ECHOES NEITHER THE VALUE NOR THE UNDERLYING ERROR, and that is the
// same rule the login flow's own endpoint refusal keeps rather than a
// separate preference: a base URL can carry a username and password, the
// guard's message quotes the string it was handed, and a refusal is not
// where somebody should find that out. What the reader needs is which
// variable to look at, and that is nameable without printing its value.
func endpointUnusableFailure() *ui.Failure {
	return ui.NewFailure(
		"curious can't use that API address.",
		"The endpoint this run was pointed at is not one this client will talk\n"+
			"to. It has to name a server over https — or a loopback host over http,\n"+
			"for local development — and carry no username, password, query or\n"+
			"fragment. The address comes from CURIOUS_API_URL.", ui.NextFreshDeploy,
		"Check CURIOUS_API_URL, or unset it to use the default, then run\n"+
			"`curious deploy` again. "+uploadedNothing)
}

// tempDirFailure is what a machine with nowhere to write the archive
// ends the run as: a full disk, or a temporary directory that is not
// writable.
func tempDirFailure(err error) *ui.Failure {
	return ui.Quoted(
		"curious couldn't make a place to write the archive.",
		err.Error(), ui.NextFreshDeploy,
		"Check that the temporary directory exists, is writable and has space,\n"+
			"then run `curious deploy` again. "+uploadedNothing)
}

// carryingDeployID attaches the server's record for this deploy to a
// failure raised after that record existed.
//
// # It is the answer to a refusal that ends where the question begins
//
// Every step from the start onwards can fail with the deploy already
// created, and until this existed each of those failures threw the id
// away. At a terminal that costs nothing — the reader fixes something
// and runs the command again, and nobody types a base36 id at anything.
// To an agent it is the whole difference between "your deploy failed"
// and a fact it can act on, because the one call that says what happened
// takes an id and there is no way to list deploys.
//
// IT SETS THE FIELD AND NEVER THE COPY. What a terminal prints is
// unchanged, byte for byte: the id is a field on the failure and
// Paragraphs() does not read it.
//
// AN ID ALREADY THERE IS LEFT ALONE. A failure raised deeper in the
// sequence may know a more specific record than the caller does, and the
// inner one is the one that was measured.
func carryingDeployID(err error, id string) error {
	if id == "" {
		return err
	}
	var failure *ui.Failure
	if errors.As(err, &failure) && failure.DeployID == "" {
		failure.DeployID = id
	}
	return err
}
