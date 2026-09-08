package flow

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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
)

// DeployPrompter is the slice of the terminal the whole sequence needs,
// which is the union of what its steps need: narration, a line of text,
// an address, and a yes-or-no question.
//
// It is declared HERE, by the consumer, for the same reason every other
// seam in this package is. That it happens to have the same four methods
// as LoginPrompter is arithmetic rather than design — this is the union
// of four steps' requirements and that is one step's — and reusing the
// name would mean a step that later stopped needing a method silently
// narrowing what the sequence asks for.
type DeployPrompter interface {
	Step(format string, args ...any)
	Line(prompt string) (string, error)
	Email(prompt string) (string, error)
	Confirm(question string, defaultYes bool) (bool, error)
}

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
}

// Handoff is the formed deploy request: everything the upload step needs
// and nothing it does not.
//
// IT IS AN INTERNAL TYPE RATHER THAN A WIRE ONE, because it is a
// different thing rather than a nicer spelling of one. The wire request
// carries a single field — the packed size — and this carries five, four
// of which the server never sees: where the archive is on this machine,
// what it hashes to, how many entries went into it, and the client that
// will send it. Reshaping a wire type into a roomier internal one is how
// two copies of a contract drift; this is not that, because there is no
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

// stopsHere is the honest end of this release's deploy. It names what is
// missing rather than stopping silently, because a command that packs an
// archive and says nothing more reads as one that failed quietly.
const stopsHere = "That is as far as this release goes — uploading the archive and " +
	"streaming\nthe build arrive in the next one."

// Deploy runs the local half of `curious deploy` and returns the formed
// request for the upload step.
//
// # The order, and why it is the order
//
//  1. Resolve the directory.
//  2. Load the stored token. No network call.
//  3. Walk the project, ONCE.
//  4. Pre-flight and 5. the local limits, over that one walk.
//  6. Only with no usable token: the capacity check, then the login.
//  7. Pack.
//  8. Hand back the formed request.
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
		client, err := api.New(endpoint)
		if err != nil {
			return nil, endpointUnusableFailure()
		}
		// HaveToken is left at false because this branch is the only way
		// in: the question it asks was answered one line above, and
		// asking it again here would be a second answer that could
		// disagree with the first.
		if err := CapacityGate(ctx, CapacityDeps{
			Prompt: deps.Prompt,
			API:    client,
			Now:    now,
		}); err != nil {
			return nil, err
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
			return nil, err
		}
		token = issued
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
	if refused := check.Advisories(packedResults.Findings); len(refused) > 0 {
		// The one refusal a project can reach having passed every other
		// check here. It carries its own copy, so it renders through the
		// same path a lone pre-flight hard stop does.
		return nil, blockedFailure(refused)
	}

	deps.Prompt.Step("%s", prepared.Receipt)
	deps.Prompt.Step("%s", stopsHere)

	handedOver = true
	return &Handoff{
		ArchivePath: prepared.Archive.Path,
		SHA256:      prepared.Archive.SHA256,
		Bytes:       prepared.Archive.Size,
		Entries:     prepared.Archive.Entries,
		Client:      authed,
		release:     release,
	}, nil
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
				"longer\nexists.", dir, err),
			"Change into a directory that exists and run `curious deploy` again.")
	}

	info, err := os.Stat(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", ui.NewFailure(
			"There is nothing at "+abs+".",
			"curious deploys a directory, and that one does not exist.",
			"Check the path and run `curious deploy <dir>` again — or run it with\n"+
				"no argument at all to deploy the directory you are standing in.")
	case err != nil:
		return "", ui.NewFailure(
			"curious couldn't read "+abs+".",
			err.Error(),
			"Check that the path exists and that you can read it, then run\n"+
				"`curious deploy` again.")
	case !info.IsDir():
		return "", ui.NewFailure(
			abs+" is a file, not a directory.",
			"curious deploys a project directory — the one holding package.json —\n"+
				"rather than a single file.",
			"Name the directory instead, or run `curious deploy` from inside it.")
	}
	return abs, nil
}

// configFailure is what an unreadable configuration LOCATION ends the run
// as. It is the only thing config.Load reports as an error; every other
// problem with the file comes back as an empty token and a reason, and
// every one of those is recoverable by logging in again.
func configFailure(err error) *ui.Failure {
	return ui.NewFailure(
		"curious couldn't work out where to keep your login.",
		err.Error(),
		"Set CURIOUS_CONFIG to the full path of a config file and run\n"+
			"`curious deploy` again.")
}

// unreadableProjectFailure is what a traversal that could not finish
// ends the run as. The walk's own error names the directory it stopped
// at, which is the half a person can act on.
func unreadableProjectFailure(err error) *ui.Failure {
	return ui.NewFailure(
		"curious couldn't read the whole project.",
		err.Error(),
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
			"fragment.",
		"Check CURIOUS_API_URL, or unset it to use the default, then run\n"+
			"`curious deploy` again. "+uploadedNothing)
}

// tempDirFailure is what a machine with nowhere to write the archive
// ends the run as: a full disk, or a temporary directory that is not
// writable.
func tempDirFailure(err error) *ui.Failure {
	return ui.NewFailure(
		"curious couldn't make a place to write the archive.",
		err.Error(),
		"Check that the temporary directory exists, is writable and has space,\n"+
			"then run `curious deploy` again. "+uploadedNothing)
}
