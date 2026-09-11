package flow

import (
	"errors"

	"github.com/curiouspub/cli/internal/api"
	"github.com/curiouspub/cli/internal/config"
)

// ErrNoLogin marks a run that holds no token for the endpoint it is
// talking to.
//
// IT IS A SENTINEL BECAUSE THE ACTION IS THE CALLER'S TO NAME. The FACT
// is the same everywhere — there is no usable credential on this machine
// for this endpoint — and what to do about it is not: the command walks
// the person through a login as part of the run it was already doing,
// and an agent has two separate calls to make and has to be told which.
// A failure built here would have to guess which surface was asking, and
// would be wrong for one of them.
//
// It says THAT there is no login and nothing about why. The why, where
// there is one, rides on the type below — and nothing branches on it,
// which is the config package's own contract for that value: it is for a
// person to read, and a caller that must behave differently for
// different causes branches on a field instead.
var ErrNoLogin = errors.New("this machine holds no login for the endpoint this run is talking to")

// NoLogin is that sentinel with the configuration's own explanation
// attached.
//
// IT IS A TYPE AND NOT A FORMATTED MESSAGE, and the difference is the
// only reason it exists. The reason is a thing a caller RENDERS — a
// surface writes its own headline and puts the explanation under it —
// and carried inside a sentence the only way back to it is trimming a
// prefix off text this package owns. That works, until the day somebody
// rewords the sentinel or changes the separator, and then it silently
// stops working: the caller renders its headline, drops the half that
// says what actually happened, and nothing anywhere goes red.
//
// errors.Is still finds the sentinel, so a caller that only needs to
// know THAT there is no login never has to know this type exists.
type NoLogin struct {
	// Reason is the configuration's own explanation, written for a
	// person and passed along as it was written. It is EMPTY on a first
	// run, where there is nothing to explain and the sentinel already
	// says everything true — so a caller renders it only when it is
	// there rather than filling the space.
	Reason string
}

func (n *NoLogin) Error() string {
	if n.Reason == "" {
		return ErrNoLogin.Error()
	}
	return ErrNoLogin.Error() + ": " + n.Reason
}

func (n *NoLogin) Unwrap() error { return ErrNoLogin }

// StoredLogin is what this machine holds for one endpoint.
type StoredLogin struct {
	// Client carries the stored token and is ready for an authenticated
	// call. The token is not exposed beside it, and that is not
	// decoration: a field holding one is a field something eventually
	// renders.
	Client *api.Client

	// Warnings are things worth telling the caller that do not stop
	// anything — a token file other accounts can read is the standing
	// example. They are the configuration's own words, passed along
	// rather than reworded, and a surface that shows them shows them as
	// they are.
	Warnings []string
}

// OpenStoredLogin loads the login this machine holds for endpoint and
// returns a client ready to spend it.
//
// # It is the same two steps the deploy sequence takes, and that is the point
//
// Which config file is read, which endpoint a stored token has to have
// been issued against, and what an unusable endpoint costs are three
// decisions with one right answer each, and the deploy sequence already
// makes all three. A second surface making them again would be free to
// read a different file or accept a token issued somewhere else, and the
// symptom would be a deploy that works from the terminal and not from an
// agent — or worse, the reverse.
//
// WHAT IT DOES NOT DO IS LOG ANYBODY IN. A run with no usable token
// comes back as ErrNoLogin carrying the configuration's own explanation,
// because the way out of that differs by surface and nothing here knows
// which one is asking.
func OpenStoredLogin(endpoint string) (StoredLogin, error) {
	cfg, err := config.Load(endpoint)
	if err != nil {
		return StoredLogin{}, configFailure(err)
	}
	if cfg.Token == "" {
		// THE REASON IS OPTIONAL AND ITS ABSENCE IS MEANINGFUL: nil is a
		// first run, where there is nothing to explain and the sentinel
		// already says everything true.
		missing := &NoLogin{}
		if cfg.NoTokenReason != nil {
			missing.Reason = cfg.NoTokenReason.Error()
		}
		return StoredLogin{}, missing
	}
	client, err := api.New(endpoint, api.WithToken(cfg.Token))
	if err != nil {
		return StoredLogin{}, endpointUnusableFailure()
	}
	return StoredLogin{Client: client, Warnings: cfg.Warnings}, nil
}
