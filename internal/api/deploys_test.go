package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/curiouspub/cli/internal/ui"
	"github.com/curiouspub/cli/pkg/wire"
)

// createRecorder is a real server speaking the real contract, so every
// row below goes through the shipped client's own encoding and decoding.
type createRecorder struct {
	mu sync.Mutex

	requests int
	paths    []string
	methods  []string
	bearers  []string
	bodies   []wire.DeployCreateRequest

	// status and body decide what the server answers. A zero status
	// means the success body below.
	status int
	reply  wire.DeployCreateResponse
}

func (r *createRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.requests++
	r.paths = append(r.paths, req.URL.Path)
	r.methods = append(r.methods, req.Method)
	r.bearers = append(r.bearers, req.Header.Get("Authorization"))

	var body wire.DeployCreateRequest
	_ = json.NewDecoder(req.Body).Decode(&body)
	r.bodies = append(r.bodies, body)

	w.Header().Set("Content-Type", "application/json")
	if r.status != 0 {
		w.WriteHeader(r.status)
		_ = json.NewEncoder(w).Encode(wire.ErrorResponse{
			Error: wire.Error{Code: wire.CodeInternal, Message: "something went wrong"},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(r.reply)
}

func (r *createRecorder) seen() (int, []string, []string, []string, []wire.DeployCreateRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests,
		append([]string(nil), r.paths...),
		append([]string(nil), r.methods...),
		append([]string(nil), r.bearers...),
		append([]wire.DeployCreateRequest(nil), r.bodies...)
}

// TestDeployCreate_SendsTheTokenAndTheDeclaredSize is the MIRROR of
// TestToken_NeverSentOnUnauthenticatedCalls next door, and the pair is
// the whole rule: authentication is per CALL, so one row has to prove a
// token IS sent where it belongs and the other that it is not sent where
// it does not. Either row alone is satisfied by a client that attaches
// the header nowhere, or by one that attaches it everywhere.
//
// REQUIRED MUTATION, run 2026-09-08: drop the per-call option from
// DeployCreate. This reds on the Authorization assertion; the guard next
// door stays green, because none of the four calls it exercises changed.
func TestDeployCreate_SendsTheTokenAndTheDeclaredSize(t *testing.T) {
	expires := time.Date(2026, 9, 8, 12, 30, 0, 0, time.UTC)
	rec := &createRecorder{
		reply: wire.DeployCreateResponse{
			DeployID:  "d-1",
			UploadURL: "https://store.example/put",
			ExpiresAt: expires,
		},
	}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	c, err := New(srv.URL, WithToken(ui.Secret(testToken)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Token != ui.Secret(testToken) {
		t.Fatal("WithToken did not take effect — every assertion below would pass vacuously")
	}

	out, err := c.DeployCreate(context.Background(), wire.DeployCreateRequest{Bytes: 4242})
	if err != nil {
		t.Fatalf("DeployCreate: %v", err)
	}

	count, paths, methods, bearers, bodies := rec.seen()
	if count != 1 {
		t.Fatalf("the create made %d requests, want exactly 1", count)
	}
	if paths[0] != "/v1/deploys" {
		t.Errorf("the create called %q, want %q", paths[0], "/v1/deploys")
	}
	if methods[0] != http.MethodPost {
		t.Errorf("the create used %s, want POST", methods[0])
	}
	if want := "Bearer " + testToken; bearers[0] != want {
		t.Errorf("the create sent Authorization %q, want %q", bearers[0], want)
	}
	if bodies[0].Bytes != 4242 {
		t.Errorf("the create declared %d bytes, want 4242", bodies[0].Bytes)
	}

	if out.DeployID != "d-1" {
		t.Errorf("DeployID = %q, want %q", out.DeployID, "d-1")
	}
	if out.UploadURL != "https://store.example/put" {
		t.Errorf("UploadURL = %q, want the one the server sent", out.UploadURL)
	}
	if !out.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v — the window is decoded, not dropped", out.ExpiresAt, expires)
	}
}

// TestDeployCreate_IsNeverRetried. A repeat is not idempotent: it creates
// a second deploy record and spends quota again, and a failure after the
// request left this client is indistinguishable from one before it.
//
// THE POSITIVE CONTROL IS THE SECOND SUBTEST. A count of one is also
// what a client that sends nothing produces on a server that answers
// nothing, so the control drives the same recorder to a success and
// requires the same count of one against a 2xx — proving the recorder
// counts at all.
//
// REQUIRED MUTATION, run 2026-09-08: pass idempotent=true from
// DeployCreate. The first subtest reds on a count of 3.
func TestDeployCreate_IsNeverRetried(t *testing.T) {
	t.Run("a server fault is not repeated", func(t *testing.T) {
		rec := &createRecorder{status: http.StatusInternalServerError}
		srv := httptest.NewServer(rec)
		defer srv.Close()

		c, err := New(srv.URL, WithToken(ui.Secret(testToken)))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := c.DeployCreate(context.Background(), wire.DeployCreateRequest{Bytes: 1}); err == nil {
			t.Fatal("a 500 from the create came back as a success")
		}
		if count, _, _, _, _ := rec.seen(); count != 1 {
			t.Errorf("the create was sent %d times, want exactly 1 — a repeat spends "+
				"quota and creates a second deploy record", count)
		}
	})

	t.Run("control: the recorder counts a request that succeeds", func(t *testing.T) {
		rec := &createRecorder{reply: wire.DeployCreateResponse{DeployID: "d-2"}}
		srv := httptest.NewServer(rec)
		defer srv.Close()

		c, err := New(srv.URL, WithToken(ui.Secret(testToken)))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := c.DeployCreate(context.Background(), wire.DeployCreateRequest{Bytes: 1}); err != nil {
			t.Fatalf("DeployCreate: %v", err)
		}
		if count, _, _, _, _ := rec.seen(); count != 1 {
			t.Errorf("the control counted %d requests, want 1 — a zero would mean the "+
				"count above says nothing about retrying and everything about the "+
				"instrument", count)
		}
	})
}
