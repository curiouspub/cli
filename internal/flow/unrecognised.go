package flow

import (
	"slices"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// updatingMayHelp is the whole of the advice for an answer this build has
// no copy for. It is not a retry, because the client does not retry one.
const updatingMayHelp = "Updating curious may help: this build may be older than the server."

// unrecognisedAnswer is the one answer to a code a step has no copy for:
// a code this build predates, or a code it declares that this step was
// never written to meet.
//
// IT NEVER SUGGESTS TRYING AGAIN, in words or as an action. Every caller
// stops on such a code rather than retrying it, and the contract is
// additive-only, so the server will keep adding codes for as long as
// there are older builds in use. Copy that said "try again in a moment"
// contradicted the program for every one of them. Retrying cannot help;
// the one thing that may is a newer curious, which is outside the run —
// so the action is GiveUp.
//
// IT SHOWS THE CODE AS SENT, because the code is the half of the answer a
// bug report can be searched for, and the server's own message, quoted
// verbatim, is the half that knows what happened. The code sits in this
// program's prose, which is laid out line by line, so a line break in it
// is shown escaped rather than allowed to start a line that reads as ours.
//
// Every switch over a code ends here, and a guard holds them to it. The
// caller says where the person was and what did not happen; this says
// everything else, in one place. An answer with no code at all never
// reaches it: each switch meets that in a case of its own, because it is
// not a code the server sent.
func unrecognisedAnswer(stage ui.Stage, what string, apiErr *api.APIError, situation string) *ui.Failure {
	why := ui.Written("The server answered with the code \"%s\",\n"+
		"which this build of curious does not recognise.", string(apiErr.Code))
	if slices.Contains(wire.AllErrorCodes, apiErr.Code) {
		// A code this build declares. Calling it unrecognised would be
		// false; what is true is that this step has no copy for it.
		why = ui.Written("The server answered with the code \"%s\",\n"+
			"which curious has no answer for at this step.", string(apiErr.Code))
	}
	if situation != "" {
		why += "\n\n" + situation
	}
	return ui.NewFailure(
		ui.IDServerAnswerUnrecognised,
		stage,
		what,
		why, ui.NextGiveUp,
		updatingMayHelp).Quoting(apiErr.Message)
}
