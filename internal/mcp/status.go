package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/curiouspub/cli/internal/flow"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// notYetReported is the status of a deploy whose build log has not
// reached its terminal event.
//
// IT IS A VALUE OF ITS OWN AND NOT A MEMBER OF THE CONTRACT'S
// VOCABULARY. The API has no endpoint that answers a deploy's current
// state — there is a create, a start, a publish and the event stream,
// and nothing that reads a record back — so the only thing in reach is
// the log, and a live tail is not a state read. Until the stream says
// how the deploy ended, nothing here knows.
//
// THE TEMPTING WRONG ANSWER IS THE LAST PHASE, and it is wrong in the
// way that is hardest to notice: "building" is a member of BOTH
// vocabularies, so an agent switching on it would be right until the day
// it was not. The phase travels in a field of its own instead, which is
// what it is.
//
// The spelling is a SENTENCE rather than an identifier, deliberately: a
// caller that reaches for it as though it were an enum member should
// find something that does not look like one, and the boolean beside it
// is what a caller is meant to branch on.
const notYetReported = "not yet reported"

// deployStatusArgs is what deploy_status is called with.
type deployStatusArgs struct {
	DeployID string `json:"deploy_id"`
}

// deployStatusResult is what the build log said about one deploy.
//
// Reported IS THE FIELD TO BRANCH ON. Status carries either a value the
// contract defines or the sentence above, and a caller comparing strings
// would have to know which are which; this one answers "did the stream
// say how it ended" in the one shape that cannot be misread.
type deployStatusResult struct {
	DeployID string `json:"deploy_id"`

	// Reported is whether the build log reached its terminal event.
	Reported bool `json:"reported"`

	// Status is the status that event carried, or notYetReported.
	Status string `json:"status"`

	// Phase is the last phase the stream named, and it is what the
	// stream said about where a build that has not finished had got to.
	// Omitted when it named none.
	Phase string `json:"phase,omitempty"`

	// Log is the most recent lines of build output, oldest first.
	Log []string `json:"log,omitempty"`

	// Errors are the diagnostics the stream carried. They EXPLAIN and
	// never terminate — a failed build still reaches its terminal event —
	// so they sit beside the status rather than in place of it, and a
	// result can carry both.
	Errors []toolStreamError `json:"errors,omitempty"`
}

// toolStreamError is one diagnostic the build log carried.
type toolStreamError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// deployStatusDescription. No number appears in it: there is no window
// constant in the wire contract, so a figure for how long a deploy lives
// would be this client stating server policy it does not take from the
// server. The expiry a caller can act on is the instant the deploy's own
// publish returned, which deploy_site hands back.
//
// The sentinel is INTERPOLATED rather than retyped, so the description
// and the value cannot drift into saying different things.
func deployStatusDescription() string {
	return fmt.Sprintf(
		"Report what a deploy's build log has said, for a deploy id %s returned. "+
			"It follows the log to its end and answers with the status the stream "+
			"finished on, the last phase it named, the most recent lines of output, and "+
			"any diagnostics the server wrote into the log.\n\n"+
			"THIS IS A TAIL OF A LOG, NOT A READ OF A RECORD. curious.pub has no endpoint "+
			"that answers a deploy's current state, so this reports only what the stream "+
			"actually said. While a build is still running the log stays open, so this "+
			"call follows it until the build ends — it is not a quick poll.\n\n"+
			"Until the stream reaches its terminal event the status reads %q and reported "+
			"is false. That is the honest answer and it is never replaced with a guess: do "+
			"not read the phase as a status, because the two vocabularies share spellings "+
			"and would agree with each other right up until they did not.\n\n"+
			"A stream that stops short because the build is still going and one that stops "+
			"short because the connection went away are the same answer from here. In both "+
			"cases the thing to do is call this again.",
		toolDeploySite, notYetReported)
}

func deployStatusTool() Tool {
	return Tool{
		Name:        toolDeployStatus,
		Title:       "Report on a deploy",
		Description: deployStatusDescription(),
		InputSchema: objectSchema(
			`"deploy_id":{"type":"string","description":"The deploy id `+toolDeploySite+
				` returned. There is no way to list deploys, so this has to be one you were given."}`,
			"deploy_id"),
		Handler: func(ctx context.Context, arguments json.RawMessage, _ Progress) Result {
			var args deployStatusArgs
			if bad := decodeArguments(arguments, &args); bad != nil {
				return *bad
			}
			if args.DeployID == "" {
				return missingArgument("deploy_id", "the deploy to report on")
			}

			login, err := flow.OpenStoredLogin(endpoint())
			if err != nil {
				if errors.Is(err, flow.ErrNoLogin) {
					return ErrorResult("%s", noLoginText(err))
				}
				return refusal(err)
			}

			report, err := flow.ReadDeployStream(ctx, flow.StreamReportDeps{
				Events: func(ctx context.Context) (io.ReadCloser, error) {
					return login.Client.DeployEvents(ctx, args.DeployID)
				},
			})
			if err != nil {
				return refusal(err)
			}

			return jsonResult(deployStatusResult{
				DeployID: args.DeployID,
				Reported: report.Reported,
				// THE SENTINEL IS CHOSEN BY WHETHER THE STREAM SPOKE, and
				// never by whether the status happens to be empty. A
				// terminal event carrying a status this build has never
				// heard of is still the stream reporting one, and
				// rendering the sentinel for it would be this client
				// saying the deploy had not finished when it had.
				Status: reportedStatus(report),
				Phase:  string(report.Phase),
				Log:    safeLines(report.Log),
				Errors: streamErrors(report.Errors),
			})
		},
	}
}

// reportedStatus is the status field's value: what the terminal event
// said, or the sentinel when there was no terminal event.
func reportedStatus(report flow.StreamReport) string {
	if !report.Reported {
		return notYetReported
	}
	return string(report.Status)
}

// streamErrors renders the diagnostics a stream carried.
//
// The message is escaped and the code is not, which is the same split
// every other rendering in this package makes: a code is a value from
// the contract's own enumeration, and a message is a sentence somebody
// else wrote.
func streamErrors(carried []wire.Error) []toolStreamError {
	if len(carried) == 0 {
		return nil
	}
	out := make([]toolStreamError, 0, len(carried))
	for _, e := range carried {
		out = append(out, toolStreamError{
			Code:    string(e.Code),
			Message: ui.SanitizeLines(e.Message),
		})
	}
	return out
}
