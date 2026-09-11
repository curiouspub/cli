package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/curiouspub/cli/internal/check"
	"github.com/curiouspub/cli/internal/flow"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// deploySiteArgs is what deploy_site is called with.
type deploySiteArgs struct {
	Dir string `json:"dir"`
}

// toolFinding is one pre-flight finding as an agent receives it.
//
// EVERY FINDING TRAVELS, INCLUDING THE ONES A TERMINAL HIDES. The
// severity is a field, so a caller decides for itself what a note is
// worth — and the surface that suppresses them by default is a surface
// with a person in front of it who would have to read them. An agent is
// the caller these facts were made machine-readable for, and dropping
// them here would make this type the third opinion on a question two
// renderers already answer differently on purpose.
//
// The measurements ride BESIDE the paths rather than inside a sentence,
// one per path and in the same order, which is the whole reason they are
// a parallel field: a number folded into prose is a number only a person
// can use.
type toolFinding struct {
	Check    string   `json:"check"`
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	Paths    []string `json:"paths,omitempty"`
	Sizes    []int64  `json:"sizes,omitempty"`
	What     string   `json:"what,omitempty"`
	Why      string   `json:"why,omitempty"`
	Next     string   `json:"next,omitempty"`
}

// deploySiteResult is what a finished deploy hands back.
//
// # It is built from what the run PRODUCED, not from what it printed
//
// Every field here comes from the sequence's own outcome value. The
// alternative — reading the address back out of the line the command
// prints — is parsing this program's prose, which is the thing it
// refuses to do with anybody else's.
//
// THE URL IS COMPOSED BY THE SEQUENCE, from the label the server sent.
// The server holds the label and has no representation of the domain, so
// the address is assembled somewhere; assembling it here as well would be
// a second composer, and the day the domain moves only one of the two
// would be updated. There is a guard asserting this package cannot spell
// it.
//
// Notes is what the command would have printed for a person, kept
// because some of it is said nowhere else: why a stored login was not
// usable, that trial capacity is running low, what the archive weighed.
// It is prose and nothing should be parsed out of it — every fact worth
// acting on is a field above — and it is carried rather than dropped
// because a renderer that withholds data on an author's behalf is making
// an editorial decision it cannot see the consequences of.
type deploySiteResult struct {
	URL       string        `json:"url"`
	DeployID  string        `json:"deploy_id"`
	ExpiresAt string        `json:"expires_at,omitempty"`
	Findings  []toolFinding `json:"findings,omitempty"`
	Notes     []string      `json:"notes,omitempty"`
}

// deploySiteDescription is what a model reads to decide whether this is
// the tool it wants, and what it is committing to by calling it.
//
// EVERY NUMBER IN IT IS READ FROM THE WIRE CONTRACT, which is why it is
// built rather than written. A limit typed into a description is a second
// home for a number the server enforces, free to go stale silently — and
// a description that states a limit the server does not enforce is worse
// than one that states none, because a caller acts on it.
//
// QUOTA AND EXPIRY GET NO NUMBER, and their absence is deliberate rather
// than an omission somebody will helpfully fill in. Neither is in the
// contract: there is no quota constant and no window constant, so any
// figure here would be this client inventing server policy. What is true
// is that the server reports the expiry on publish and reports a rate
// limit with a time to retry after, and both of those are said in words.
func deploySiteDescription() string {
	return fmt.Sprintf(
		"Deploy an Astro project and return the address it answers at. This runs the "+
			"whole sequence: it checks the project, packs it, uploads it, builds it on "+
			"curious.pub, publishes the result, and answers with the URL, the deploy id "+
			"and when the deploy expires.\n\n"+
			"Everything local happens first, so a project that cannot deploy makes no "+
			"network call at all. The client refuses a project with more than %d files, "+
			"any single file over %d bytes, more than %d bytes of source in total, or an "+
			"archive over %d bytes once packed — and the server checks every one of them "+
			"again, so these are a fast local answer rather than the boundary. The build "+
			"must also produce no more than %d files and no more than %d bytes.\n\n"+
			"WARNINGS DO NOT STOP A DEPLOY HERE. Anything the pre-flight checks found "+
			"comes back in findings, with a severity on each, and the deploy goes ahead — "+
			"there is nobody to ask, and a question asked over a pipe is a hang. Read them "+
			"and tell the user about the ones that matter.\n\n"+
			"The server reports the site's expiry on publish, so it is in the answer rather "+
			"than worked out here. Rate limits are reported by the server with a time to "+
			"retry after. A login is needed first; see %s.\n\n"+
			"The address starts answering a little after the deploy is published, so opening "+
			"it immediately may show a placeholder page.",
		wire.MaxSourceFiles, wire.MaxSourceFileBytes, wire.MaxSourceTotalBytes,
		wire.MaxPackedBytes, wire.MaxOutputFiles, wire.MaxOutputTotalBytes,
		toolLoginStart)
}

func deploySiteTool() Tool {
	return Tool{
		Name:        toolDeploySite,
		Title:       "Deploy a site",
		Description: deploySiteDescription(),
		InputSchema: objectSchema(
			`"dir":{"type":"string","description":"The project directory to deploy — the one ` +
				`holding package.json. Relative paths resolve against the directory this server ` +
				`was started in. Leave it out to deploy that directory itself."}`),
		Handler: func(ctx context.Context, arguments json.RawMessage, progress Progress) Result {
			var args deploySiteArgs
			if bad := decodeArguments(arguments, &args); bad != nil {
				return *bad
			}

			prompt := &agentPrompter{}
			// THE SEQUENCE IS THE ONE THE COMMAND RUNS, entered at the
			// same door with the same arguments. What differs is the
			// terminal it is handed and the channel it reports through;
			// every decision about order, limits, retries and copy is
			// made once, over there, for both surfaces.
			handoff, err := flow.Deploy(ctx, flow.DeployDeps{
				Dir:      args.Dir,
				Prompt:   prompt,
				Progress: progress.Report,
			})
			// Deferred beside the error check rather than after it: it is
			// safe on the nil hand-off a refused run returns, and this is
			// the line that removes the archive on every way out.
			defer handoff.Release()

			if err != nil {
				return deployRefusal(err, prompt)
			}

			outcome := handoff.Outcome
			return jsonResult(deploySiteResult{
				URL:       flow.PublishedURL(outcome.Subdomain),
				DeployID:  outcome.DeployID,
				ExpiresAt: instant(outcome.ExpiresAt),
				Findings:  toolFindings(outcome.Preflight),
				Notes:     safeLines(prompt.notes),
			})
		},
	}
}

// deployRefusal renders a refused deploy, with the build's own output
// attached when there was any.
//
// THE LOG IS ATTACHED BECAUSE THE COPY POINTS AT IT. A build the server
// refuses ends with a message saying what went wrong is in the log above
// — which is true at a terminal, where the log has just scrolled past,
// and false here unless it is carried: an agent that was not watching
// progress has seen none of it, and a refusal that names evidence the
// reader does not have is worse than one that names none.
func deployRefusal(err error, prompt *agentPrompter) Result {
	text := refusalText(err)
	// THE ID THE REFUSAL WAS THROWING AWAY. A deploy refused after the
	// server created its record has one, and it is the only handle the
	// one follow-up call takes: there is no way to list deploys, so an
	// agent told that a deploy failed and not WHICH deploy cannot ask
	// what happened. The refusal ended the conversation exactly where
	// the next question begins.
	//
	// It is rendered here and not in the copy the terminal prints,
	// because a person does not type a base36 id at anything — they fix
	// something and run the command again.
	var failure *ui.Failure
	if errors.As(err, &failure) && failure.DeployID != "" {
		text += "\n\nThe deploy id is " + ui.Sanitize(failure.DeployID) +
			". Call " + toolDeployStatus + " with it to read what the build said."
	}
	if len(prompt.build) > 0 {
		text += "\n\nThe end of the build log:\n\n" +
			strings.Join(safeLines(prompt.build), "\n")
	}
	if len(prompt.notes) > 0 {
		text += "\n\nWhat the run said on the way:\n\n" +
			strings.Join(safeLines(prompt.notes), "\n")
	}
	return ErrorResult("%s", text)
}

// safeLines escapes text on its way to a client, keeping the line breaks
// that are this program's layout.
//
// IT IS THE SAME BOUNDARY THE TERMINAL KEEPS. A build log is arbitrary
// program output and a terminal obeys some of those bytes; the narration
// is this program's prose with somebody else's values interpolated into
// it — a status the server sent, a phase, a path off the user's disk.
// The client decodes this and a person reads it, so the escaping cannot
// be skipped just because the first hop is a pipe.
func safeLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, ui.SanitizeLines(line))
	}
	return out
}

// toolFindings renders the run's findings for an agent, in report order.
func toolFindings(findings []check.Finding) []toolFinding {
	if len(findings) == 0 {
		return nil
	}
	out := make([]toolFinding, 0, len(findings))
	for _, f := range findings {
		out = append(out, toolFinding{
			Check:    f.CheckID,
			Severity: string(f.Severity),
			Message:  ui.SanitizeLines(f.Message),
			Paths:    safeLines(f.Paths),
			Sizes:    f.Sizes,
			What:     ui.SanitizeLines(f.What),
			Why:      ui.SanitizeLines(f.Why),
			Next:     ui.SanitizeLines(f.Next),
		})
	}
	return out
}

// instant renders a time the server sent, or nothing at all when it sent
// none.
//
// NOTHING IS COMPUTED AND NOTHING IS DEFAULTED. A zero value means the
// server said nothing, which it is entitled to do, and a field omitted
// is the honest rendering of that — where a zero timestamp would arrive
// as a date in 1970 and read as a deploy that expired before it existed.
//
// RFC 3339 because the reader is a machine: it is the one timestamp
// format a caller can parse without being told which one it is.
func instant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
