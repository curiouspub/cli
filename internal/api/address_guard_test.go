package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// addressGuardCase is one row of addressGuardTable below.
type addressGuardCase struct {
	name    string
	base    string
	allowed bool
}

// addressGuardTable probes this client's own address guard across the
// spelling space rather than trusting a reading of the implementation:
// three successive, correct-looking address guards elsewhere in this
// project each fell to a different spelling nobody had probed, and every
// one was found by a table like this rather than by review.
//
// It is a package-level var, rather than a literal local to one test
// function, because two tests need the SAME table: one exercises the
// real guard against every row, and a second measures whether the table
// itself is strong enough to catch a naive implementation — see
// TestAddressGuardTable_HasDiscriminatingPower's own doc comment for why
// that has to be a different question from whether the guard passes.
var addressGuardTable = []addressGuardCase{
	{"https production host", "https://api.curious.pub", true},
	{"http localhost with port", "http://localhost:8080", true},
	{"http 127.0.0.1 with port", "http://127.0.0.1:8080", true},
	{"http bracketed IPv6 loopback", "http://[::1]:8080", true},
	{"http localhost case-folded", "http://LOCALHOST:8080", true},
	{"http localhost trailing dot stripped", "http://localhost.:8080", true},
	{"http localhost prefix trap — refused", "http://localhost.evil.com", false},
	{"http 127.0.0.1 prefix trap — refused", "http://127.0.0.1.evil.com", false},
	{"http userinfo trap, real host is evil.com — refused", "http://localhost@evil.com", false},
	{"http localhost in the PATH, not the host — refused", "http://evil.com/localhost", false},
	{"http localhost in the FRAGMENT, not the host — refused", "http://evil.com#localhost", false},
	{"http 127.0.0.2 — exact set, not a range — refused", "http://127.0.0.2:8080", false},
	{"http arbitrary host — refused", "http://example.com", false},
	// The five rows below are not spelling traps on the HOST — the host
	// in each is otherwise fine — they probe the OTHER parts of a URL
	// this guard must also refuse outright: userinfo, a query, a
	// fragment, an opaque form, and a missing host.
	{"userinfo present even though the host is loopback — refused", "http://user:pass@localhost:8080", false},
	{"query string on an otherwise-valid https host — refused", "https://api.curious.pub?debug=1", false},
	{"fragment on an otherwise-valid https host — refused", "https://api.curious.pub#section", false},
	{"opaque URL, no authority at all — refused", "https:opaque-no-host", false},
	{"empty host — refused", "https:///path", false},
}

// TestAddressGuard_SpellingSpace runs addressGuardTable against the real
// guard.
//
// New validates the base URL eagerly, before any Client exists. A
// refused row therefore never produces a Client at all, which makes "0
// requests" for that row a STRUCTURAL fact rather than an empirical
// one — there is no object left to send a request with. That is a
// stronger guarantee than a live counter gives for these rows, and it is
// also why only the positive-control row needs a real listener: a live
// counter is what proves the ALLOWED path actually reaches the network,
// which "New returned no error" alone does not.
//
// The positive control exists because a suite of negative rows cannot
// tell a working guard from a client nothing ever wired to send
// anything — both read as "0 requests". So this function also builds a
// real Client against a real listener and checks the listener's own
// counter reads exactly 1.
func TestAddressGuard_SpellingSpace(t *testing.T) {
	for _, tc := range addressGuardTable {
		c, err := New(tc.base)
		gotAllowed := err == nil
		if gotAllowed != tc.allowed {
			t.Errorf("%s: New(%q) allowed=%v (err=%v), want allowed=%v",
				tc.name, tc.base, gotAllowed, err, tc.allowed)
		}
		if !tc.allowed && c != nil {
			t.Errorf("%s: a refused base URL still produced a non-nil Client", tc.name)
		}
	}

	// Positive control: an allowed base URL, pointed at a real listener,
	// must actually reach it — not merely fail to error.
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"open":true,"accounts_left":1,"resets_at":"2026-01-01T00:00:00Z"}`))
	}))
	defer srv.Close()

	allowed, err := New(srv.URL)
	if err != nil {
		t.Fatalf("New(%q) (a real loopback listener) was refused: %v", srv.URL, err)
	}
	if _, err := allowed.Capacity(context.Background()); err != nil {
		t.Fatalf("Capacity() against the allowed listener failed: %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("listener saw %d requests, want exactly 1 — an allowed address "+
			"must be reachable, not merely fail to error", got)
	}
}

// TestAddressGuardTable_HasDiscriminatingPower never calls validateBaseURL
// or New at all. It answers a different question from the test above: not
// "does the real guard get this table right", but "would this TABLE catch
// a naive implementation if one shipped" — a property of the table's own
// verdicts, checked against a stand-in that never touches the guard. The
// two questions look alike and are not: a table that happened to contain
// only rows a bad guard also gets right would let
// TestAddressGuard_SpellingSpace pass for the wrong reason, green against
// both the real guard and a HasPrefix shortcut alike.
//
// A strings.HasPrefix(base, "http://localhost") stand-in must disagree
// with this table's verdicts on at least 3 rows: it ALLOWS the
// prefix-trap row ("http://localhost.evil.com" begins with the literal
// prefix) and it REFUSES the case-folded row (it begins "http://LOCALHOST",
// not the lowercase literal) and the bracketed-IPv6 row (it does not begin
// with the literal prefix at all). Counted below rather than asserted by
// eye, so nobody "simplifies" the real guard back to a prefix check on the
// strength of it looking fine on a couple of examples.
func TestAddressGuardTable_HasDiscriminatingPower(t *testing.T) {
	disagreements := 0
	for _, tc := range addressGuardTable {
		hasPrefixVerdict := strings.HasPrefix(tc.base, "http://localhost")
		if hasPrefixVerdict != tc.allowed {
			disagreements++
		}
	}
	if disagreements < 3 {
		t.Fatalf("expected a naive HasPrefix comparison to disagree with this "+
			"table's verdicts on at least 3 rows, got %d — the table has lost the "+
			"discriminating power this guard's tests depend on", disagreements)
	}
}

// TestAddressGuard_RefusalNamesAReason is a small companion to the table
// above: CLAUDE.md's own rule for this repo is that an error names an
// action the reader can take, and a bare "refused" is not that.
func TestAddressGuard_RefusalNamesAReason(t *testing.T) {
	_, err := New("http://example.com")
	if err == nil {
		t.Fatal("New(\"http://example.com\") succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Errorf("refusal message %q does not mention https, the way out", err.Error())
	}
}
