package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/curiouspub/cli/pkg/wire"
)

// APIError is a non-2xx /v1 response, decoded once into a shape callers
// can branch on without re-parsing wire.ErrorResponse themselves.
type APIError struct {
	Status  int
	Code    wire.ErrorCode
	Message string
	// RetryAfter is parsed from the response header whenever it is
	// present, whatever the code — see decodeAPIError's doc comment.
	// Zero means the header was absent.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s: %s (status %d)", e.Code, e.Message, e.Status)
	}
	return fmt.Sprintf("%s (status %d)", e.Message, e.Status)
}

// undecodableBodyMessage is what a caller sees when a non-2xx body is
// not the wire error envelope at all — a captive portal's or a
// misconfigured proxy's own HTML page, an empty body, truncated JSON.
// The raw body never reaches this message: a proxy's HTML page rendered
// straight to a terminal is unreadable at best.
const undecodableBodyMessage = "the server returned an error this client could not parse"

// decodeAPIError builds an APIError from a non-2xx response.
//
// An unrecognised Code is not a parse failure in itself: pkg/wire's
// contract is additive-only, so the server is free to introduce a code
// this binary predates, and this client carries it through with its
// own, server-written Message intact rather than replacing it with a
// generic one — callers switch on the codes they handle and fall
// through to showing the message for everything else. Only a body that
// does not decode as the envelope AT ALL — or decodes with no code,
// which is the same thing as far as a caller is concerned — falls back
// to the generic message above.
//
// Retry-After is read here, independent of Code and independent of
// whether the decode above succeeded: it is the server's header to
// send, not something this client should expect only from codes it
// recognises. wire.CarriesRetryAfter names which codes GUARANTEE the
// header is present; it says nothing about whether this client parses
// one that turned up anyway, which it always does.
func decodeAPIError(resp *http.Response) *APIError {
	apiErr := &APIError{
		Status:     resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}

	var envelope wire.ErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil || envelope.Error.Code == "" {
		apiErr.Message = undecodableBodyMessage
		return apiErr
	}

	apiErr.Code = envelope.Error.Code
	apiErr.Message = envelope.Error.Message
	return apiErr
}

// parseRetryAfter accepts either form RFC 7231 allows: an integer
// delta-seconds, or an HTTP-date. A value this client cannot parse, or a
// date already in the past, reports zero rather than guessing.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
