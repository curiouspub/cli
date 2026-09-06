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
