package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
)

// deployPath builds the path of a per-deploy endpoint.
//
// The id is ESCAPED rather than concatenated. It arrives from the
// server, so it is not attacker-chosen in any ordinary sense — and that
// is exactly the reasoning that ages badly: escaping costs nothing, and
// the alternative is a client whose request path is decided by a value it
// did not choose.
func deployPath(deployID, action string) string {
	return "/v1/deploys/" + url.PathEscape(deployID) + "/" + action
}

// DeployEvents opens GET /v1/deploys/{id}/events and returns the raw
// stream for the caller to read and close. The caller owns the reader
// from the moment this returns without an error; a call that returns an
// error returns no reader, so a deferred close is safe at every call
// site.
//
// # It deliberately does not go through do
//
// Every other call in this package is a small request and a small answer
// bounded end to end by the client's own per-request timeout. This one is
// a connection held open for the whole of a build, and applying that
// timeout to it would kill any build quieter than the timeout — a TOTAL
// DEADLINE CANNOT EXPRESS "IS THIS MAKING PROGRESS", and the server's own
// keep-alive frames cannot rescue one, because keeping a connection alive
// does not extend a deadline that is counting anyway.
//
// So there is no deadline here at all. What ends this request is the
// caller's context: the stream's own terminating event, a cancellation,
// or a liveness rule the caller applies to the bytes it is reading, which
// is where that rule can actually see the thing it is about.
//
// It is not retried either, and for once that is not a decision this
// function has to make: a stream that ends early is reconnected by
// whoever is reading it, from the beginning, because that is the only
// place that knows how much has already been shown.
//
// The bearer token goes on this request — the event stream is an
// authenticated call on this project's own API, unlike the upload, where
// the link somebody else signed IS the credential.
func (c *Client) DeployEvents(ctx context.Context, deployID string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+deployPath(deployID, "events"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("User-Agent", c.userAgent)
	c.withBearerToken()(req)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// networkError names the host and never the request, which is
		// what keeps the Authorization header this call just set out of
		// the message.
		return nil, networkError(err, c.baseURL)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, decodeAPIError(resp)
	}
	return resp.Body, nil
}
