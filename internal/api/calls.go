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
