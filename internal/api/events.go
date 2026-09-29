package api

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"sync"
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
// So there is no deadline on the stream as a whole. Getting it OPEN is
// bounded, phase by phase, by the transport: dialling, the TLS handshake,
// and the server answering each have their own bound, and a failure in
// any of them comes back as a StreamOpenError naming the phase. Once the
// response has arrived, what ends this request is the caller's context:
// the stream's own terminating event, a cancellation, or a liveness rule
// the caller applies to the bytes it is reading, which is where that rule
// can actually see the thing it is about.
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

	// HOW FAR THE REQUEST GOT is recorded as it goes, so a failure can say
	// which phase it failed in: the dial, the TLS handshake, or the server
	// answering. Each phase has its own bound on the transport and its own
	// sentence for the person watching, and the standard library exports
	// none of the errors that would tell them apart. The phase reached is
	// the fact; an error's wording is not.
	secure := req.URL.Scheme == "https"
	var mu sync.Mutex
	var reached streamStage
	reach := func(s streamStage) {
		mu.Lock()
		if s > reached {
			reached = s
		}
		mu.Unlock()
	}
	trace := &httptrace.ClientTrace{
		// A reused connection has already been dialled and secured.
		GotConn: func(info httptrace.GotConnInfo) {
			if info.Reused {
				reach(stageAnswering)
			}
		},
		// Over plain HTTP, the loopback-only development case, there is
		// no handshake to wait for, so a connection moves straight to
		// the server answering.
		ConnectDone: func(_, _ string, err error) {
			if err != nil {
				return
			}
			if secure {
				reach(stageSecuring)
			} else {
				reach(stageAnswering)
			}
		},
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			if err == nil {
				reach(stageAnswering)
			}
		},
		// A plain-HTTP connection has no handshake: writing the request is
		// what moves it past the phases before the answer.
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				reach(stageAnswering)
			}
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// networkError names the host and never the request, which is
		// what keeps the Authorization header this call just set out of
		// the message.
		mu.Lock()
		stage := reached.stage()
		mu.Unlock()
		return nil, &StreamOpenError{Stage: stage, Err: networkError(err, c.baseURL)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, decodeAPIError(resp)
	}
	return resp.Body, nil
}

// StreamStage is the phase of opening the build log that a failure
// happened in. Each has its own bound on the transport.
type StreamStage string

const (
	// StageConnect is dialling the server.
	StageConnect StreamStage = "connect"
	// StageTLS is the TLS handshake on an established connection.
	StageTLS StreamStage = "tls"
	// StageResponse is waiting for the server to answer a request that
	// was sent.
	StageResponse StreamStage = "response"
)

// streamStage is the phase a request has reached, ordered so that later
// compares greater. The trace moves it forward from more than one
// goroutine, so it is read and written under a lock. Its zero value is
// the first phase.
type streamStage int

const (
	stageConnecting streamStage = iota
	stageSecuring
	stageAnswering
)

func (s streamStage) stage() StreamStage {
	switch s {
	case stageSecuring:
		return StageTLS
	case stageAnswering:
		return StageResponse
	default:
		return StageConnect
	}
}

// StreamOpenError is a build-log request that failed before any response
// arrived, carrying the phase it failed in. The message is the underlying
// network error's, which names the host and never the request.
type StreamOpenError struct {
	Stage StreamStage
	Err   error
}

func (e *StreamOpenError) Error() string { return e.Err.Error() }
func (e *StreamOpenError) Unwrap() error { return e.Err }
