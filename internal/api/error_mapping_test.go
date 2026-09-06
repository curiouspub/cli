package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/curiouspub/cli/pkg/wire"
)

// errorServer answers every request with status and body, optionally
// setting extra response headers. AuthStart is what every test in this
// file drives it with: it is never retried, so a stateless handler
// answering the same thing every time cannot be mistaken for evidence
// about retry behaviour, which is a separate test's job.
func errorServer(t *testing.T, status int, body string, headers map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func requireAPIError(t *testing.T, err error) *APIError {
	t.Helper()
	if err == nil {
		t.Fatal("got a nil error, want a non-2xx response to produce one")
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("err is %T, want *APIError: %v", err, err)
	}
	return apiErr
}

// TestErrorMapping_KnownCode is the ordinary case: a 400 with a
// well-formed envelope decodes into an APIError carrying both fields.
func TestErrorMapping_KnownCode(t *testing.T) {
	srv := errorServer(t, http.StatusBadRequest,
		`{"error":{"code":"bad_request","message":"email is required"}}`, nil)
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.AuthStart(context.Background(), wire.AuthStartRequest{})
	apiErr := requireAPIError(t, err)
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("Status = %d, want %d", apiErr.Status, http.StatusBadRequest)
	}
	if apiErr.Code != wire.CodeBadRequest {
		t.Errorf("Code = %q, want %q", apiErr.Code, wire.CodeBadRequest)
	}
	if apiErr.Message != "email is required" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "email is required")
	}
}

// TestErrorMapping_UnknownCode is the additive-contract case: a code
// this binary has never seen is not an error in itself. pkg/wire says
// the server may add codes at any time, and a client that panicked, or
// swallowed the message, on an unrecognised one would break the day the
// server actually exercised that freedom.
func TestErrorMapping_UnknownCode(t *testing.T) {
	srv := errorServer(t, http.StatusTeapot,
		`{"error":{"code":"teapot_overflow","message":"Try a smaller pot."}}`, nil)
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.AuthStart(context.Background(), wire.AuthStartRequest{})
	apiErr := requireAPIError(t, err)
	if apiErr.Code != "teapot_overflow" {
		t.Errorf("Code = %q, want %q", apiErr.Code, "teapot_overflow")
	}
	if apiErr.Message != "Try a smaller pot." {
		t.Errorf("Message = %q, want %q — the server's own wording must survive "+
			"to the caller verbatim, never replaced by a generic \"unknown error\"",
			apiErr.Message, "Try a smaller pot.")
	}
}

// TestErrorMapping_UndecodableBody is the captive-portal / corporate-
// proxy case: a non-2xx body that is not the wire envelope at all — a
// proxy's own HTML error page, here — must never reach a caller raw.
func TestErrorMapping_UndecodableBody(t *testing.T) {
	srv := errorServer(t, http.StatusBadGateway,
		"<html><body>502 Bad Gateway</body></html>", nil)
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.AuthStart(context.Background(), wire.AuthStartRequest{})
	apiErr := requireAPIError(t, err)
	if apiErr.Status != http.StatusBadGateway {
		t.Errorf("Status = %d, want %d", apiErr.Status, http.StatusBadGateway)
	}
	if strings.Contains(apiErr.Message, "<") {
		t.Errorf("Message %q contains a raw tag — an undecodable body must never "+
			"surface its own content", apiErr.Message)
	}
}

// TestErrorMapping_AllCodes ranges wire.AllErrorCodes rather than a
// hand-typed table: a code added to the contract is covered the day it
// lands, and this test does not need editing when that happens. A
// hand-typed table would stop covering the day the server adds a ninth
// code — the exact failure wire.AllErrorCodes exists to remove.
func TestErrorMapping_AllCodes(t *testing.T) {
	for _, code := range wire.AllErrorCodes {
		t.Run(string(code), func(t *testing.T) {
			message := fmt.Sprintf("message for %s", code)
			srv := errorServer(t, http.StatusBadRequest,
				fmt.Sprintf(`{"error":{"code":%q,"message":%q}}`, code, message), nil)
			defer srv.Close()

			c, err := New(srv.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = c.AuthStart(context.Background(), wire.AuthStartRequest{})
			apiErr := requireAPIError(t, err)
			if apiErr.Code != code {
				t.Errorf("Code = %q, want %q", apiErr.Code, code)
			}
			if apiErr.Message != message {
				t.Errorf("Message = %q, want %q", apiErr.Message, message)
			}
		})
	}
}

// TestErrorMapping_RetryAfterRanged ranges the same contract set for the
// other half of the rule: Retry-After is parsed whenever the header is
// present, independent of the code, while wire.CarriesRetryAfter names
// which codes GUARANTEE the server sends it. Both branches are exercised
// for every code so this stays true the day a code's guarantee changes.
func TestErrorMapping_RetryAfterRanged(t *testing.T) {
	for _, code := range wire.AllErrorCodes {
		t.Run(string(code)+"/header present", func(t *testing.T) {
			srv := errorServer(t, http.StatusTooManyRequests,
				fmt.Sprintf(`{"error":{"code":%q,"message":"m"}}`, code),
				map[string]string{"Retry-After": "42"})
			defer srv.Close()

			c, err := New(srv.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = c.AuthStart(context.Background(), wire.AuthStartRequest{})
			apiErr := requireAPIError(t, err)
			if apiErr.RetryAfter != 42*time.Second {
				t.Errorf("RetryAfter = %v, want 42s — parsing does not depend on "+
					"the code", apiErr.RetryAfter)
			}
		})

		t.Run(string(code)+"/header absent", func(t *testing.T) {
			srv := errorServer(t, http.StatusTooManyRequests,
				fmt.Sprintf(`{"error":{"code":%q,"message":"m"}}`, code), nil)
			defer srv.Close()

			c, err := New(srv.URL)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = c.AuthStart(context.Background(), wire.AuthStartRequest{})
			apiErr := requireAPIError(t, err)
			// wire.CarriesRetryAfter(code) says a response with THIS code
			// is guaranteed to carry the header — a server that omits it
			// for such a code has a defect of its own, which is not this
			// client's to paper over by inventing a value it was never
			// sent. So the observable result here is 0 regardless of the
			// code, which is exactly what "the parse is code-independent"
			// means.
			if apiErr.RetryAfter != 0 {
				t.Errorf("RetryAfter = %v, want 0 with no header present",
					apiErr.RetryAfter)
			}
		})
	}
}
