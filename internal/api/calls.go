package api

import (
	"context"
	"net/http"

	"github.com/curiouspub/cli/pkg/wire"
)

// Capacity calls GET /v1/capacity: the client's step-zero check, before
// any account-creating work, so a user is never asked to do work that
// cannot land. Unauthenticated, and idempotent per the endpoint's own
// contract, so a transient failure is retried.
func (c *Client) Capacity(ctx context.Context) (*wire.CapacityResponse, error) {
	var out wire.CapacityResponse
	if err := c.do(ctx, http.MethodGet, "/v1/capacity", nil, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// Waitlist calls POST /v1/waitlist. Idempotent per the endpoint's own
// contract, so a transient failure is retried. The success body is
// empty by design (wire.WaitlistResponse) — decoded here, never branched
// on.
func (c *Client) Waitlist(ctx context.Context, req wire.WaitlistRequest) (*wire.WaitlistResponse, error) {
	var out wire.WaitlistResponse
	if err := c.do(ctx, http.MethodPost, "/v1/waitlist", req, &out, true); err != nil {
		return nil, err
	}
	return &out, nil
}

// AuthStart calls POST /v1/auth/start, the first step of the email +
// code login flow. NEVER retried: each call spends one of four sends
// allowed per identity in a rolling hour, so a silent retry would spend
// a third of that budget on one keystroke. The success body is empty by
// design — wire.AuthStartResponse's own doc comment explains why it
// carries nothing a caller could branch on: the endpoint answers
// identically whether or not a code was actually sent.
func (c *Client) AuthStart(ctx context.Context, req wire.AuthStartRequest) (*wire.AuthStartResponse, error) {
	var out wire.AuthStartResponse
	if err := c.do(ctx, http.MethodPost, "/v1/auth/start", req, &out, false); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeployCreate calls POST /v1/deploys: the first authenticated call this
// client makes, and the one that turns a packed archive into a deploy
// record plus somewhere to send it.
//
// NEVER RETRIED, and the mechanism is the flag rather than new
// machinery. A repeat is not idempotent — it creates a second deploy
// record and spends quota again — and a failure after the request left
// this process is indistinguishable from one before it, so the honest
// move is to stop and say a deploy may have been created. auth/start and
// auth/verify are the standing precedent for the same argument.
//
// IT AUTHENTICATES PER CALL. The bearer token goes on this request and
// on nothing else, through withBearerToken; see that method for why the
// header is not set in the shared path.
//
// req.Bytes is the PACKED archive's size. The server signs the presigned
// upload with it as an exact Content-Length, so a wrong number here gets
// every upload refused rather than warned about — which is why the
// number is taken from whoever measured the archive rather than measured
// a second time.
func (c *Client) DeployCreate(ctx context.Context, req wire.DeployCreateRequest) (*wire.DeployCreateResponse, error) {
	var out wire.DeployCreateResponse
	if err := c.do(ctx, http.MethodPost, "/v1/deploys", req, &out, false, c.withBearerToken()); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeployStart calls POST /v1/deploys/{id}/start: the call that turns an
// uploaded archive into a running build.
//
// NEVER RETRIED, and the reason is not that a repeat would be harmful.
// The endpoint is honest about one — a start on a deploy that is already
// building answers with that status rather than an error — so a retry is
// not wrong. It is still the wrong instinct to build in, because of where
// the information lives: once this call has returned, everything the user
// is waiting for arrives on the event stream, so a client that reacts to
// a timeout by asking again is asking the one surface that has nothing
// left to tell it. Reconnect to the stream instead.
//
// The success body's Status INFORMS and never BRANCHES. The contract says
// so at the type, and it is contract rather than convention: the client
// takes the identical next action for every value the field can carry, so
// a switch on it is the defect and not the omission. It is rendered, and
// that is all.
//
// It authenticates per call, like the create.
func (c *Client) DeployStart(ctx context.Context, deployID string) (*wire.DeployStartResponse, error) {
	var out wire.DeployStartResponse
	if err := c.do(ctx, http.MethodPost, deployPath(deployID, "start"), nil, &out, false, c.withBearerToken()); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeployPublish calls POST /v1/deploys/{id}/publish: the last call of a
// deploy, and the one that gives a built deploy an address.
//
// NEVER RETRIED, and the reason is the same one the create carries
// rather than the start's. A failure after the request left this process
// is indistinguishable from one before it, and the two possible truths —
// the deploy has an address, or it has none — are exactly what a caller
// would be retrying to find out. Asking again cannot answer that
// question: a second call arriving after a first one succeeded is a
// second decision about a deploy this client has already been told
// nothing about.
//
// The success body carries a subdomain LABEL and an expiry, and per the
// contract's own rule no client behaviour depends on either: they are
// shown to a person. The label is the half a client cannot derive, and
// assembling an address out of it belongs to whoever knows the domain,
// which is not this package.
//
// It authenticates per call, like the create and the start.
func (c *Client) DeployPublish(ctx context.Context, deployID string) (*wire.DeployPublishResponse, error) {
	var out wire.DeployPublishResponse
	if err := c.do(ctx, http.MethodPost, deployPath(deployID, "publish"), nil, &out, false, c.withBearerToken()); err != nil {
		return nil, err
	}
	return &out, nil
}

// AuthVerify calls POST /v1/auth/verify, the second step of the login
// flow, and returns the bearer token for every subsequent authenticated
// call. NEVER retried: the submitted code is consumed atomically, so a
// retry after an ambiguous timeout could report failure for a verify
// that had already succeeded and already spent the code.
func (c *Client) AuthVerify(ctx context.Context, req wire.AuthVerifyRequest) (*wire.AuthVerifyResponse, error) {
	var out wire.AuthVerifyResponse
	if err := c.do(ctx, http.MethodPost, "/v1/auth/verify", req, &out, false); err != nil {
		return nil, err
	}
	return &out, nil
}
