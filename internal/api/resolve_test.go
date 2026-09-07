package api

import "testing"

// TestResolveBaseURL_Precedence pins the fallback chain, which is the
// whole of what this function is. The rows are ordered by what wins, and
// each one turns OFF the source above it so no row can pass by accident
// of the environment it happened to run in.
//
// REQUIRED MUTATION: swap the explicit-argument branch below the
// environment branch in ResolveBaseURL. The first row reds — an explicit
// endpoint loses to a stale variable in the caller's shell, which is the
// direction that silently sends a token somewhere nobody asked for.
//
// SECOND REQUIRED MUTATION: return defaultAPIBase unconditionally. Rows
// one and two red; row three stays green, which is why the default's own
// row cannot be the only one.
//
// Both run and observed red before this comment was committed; see the
// report for the reds and the checksum-verified revert.
func TestResolveBaseURL_Precedence(t *testing.T) {
	const explicit = "https://explicit.example.com"
	const fromEnv = "https://from-env.example.com"

	t.Run("an explicit argument wins over everything", func(t *testing.T) {
		t.Setenv("CURIOUS_API_URL", fromEnv)
		if got := ResolveBaseURL(explicit); got != explicit {
			t.Errorf("ResolveBaseURL(%q) = %q, want the explicit argument — an endpoint "+
				"the caller named must never lose to a variable in their shell",
				explicit, got)
		}
	})

	t.Run("the environment wins over the default", func(t *testing.T) {
		t.Setenv("CURIOUS_API_URL", fromEnv)
		if got := ResolveBaseURL(""); got != fromEnv {
			t.Errorf("ResolveBaseURL(\"\") = %q, want %q", got, fromEnv)
		}
	})

	t.Run("the default applies when nothing else is set", func(t *testing.T) {
		t.Setenv("CURIOUS_API_URL", "")
		if got := ResolveBaseURL(""); got != defaultAPIBase {
			t.Errorf("ResolveBaseURL(\"\") = %q, want the compiled-in default", got)
		}
	})

	// An empty variable is not a setting. Without this row, "unset" and
	// "exported as empty" are indistinguishable — and the second is what
	// a shell script produces from an unset variable it forwards anyway.
	t.Run("an empty environment variable is not a setting", func(t *testing.T) {
		t.Setenv("CURIOUS_API_URL", "")
		if got := ResolveBaseURL(""); got == "" {
			t.Error("ResolveBaseURL(\"\") returned empty — an exported-but-empty variable " +
				"must fall through to the default, not become the endpoint")
		}
	})
}

// TestResolveBaseURL_IsTheOneAnswerNewUses ties the client's observed
// endpoint to the resolver's answer, so the two are asserted equal rather
// than merely believed to be.
//
// The check is on the CANONICAL form rather than the raw string, because
// New puts its base through the address guard, which trims trailing
// slashes — comparing raw would fail for a reason that has nothing to do
// with the property under test.
//
// REQUIRED MUTATION: in New, replace ResolveBaseURL(baseURL) with a
// re-inlined chain reading a DIFFERENT variable name. This row reds.
//
// WHAT THIS ROW IS AND IS NOT, because the first draft of this comment
// claimed more than the mutation showed and the mutation is what said so.
// It claimed this row would red "while every other test stays green".
// That is false: the client's own resolution test reds too, on the
// environment-fallback leg, and it covers all three legs already. So this
// row is not the only detector of a changed chain and was never load
// bearing in that way.
//
// What it is: the assertion that New's endpoint and the resolver's answer
// are the SAME VALUE, stated once, where the client's own test states
// three separate expectations about what that value should be. A leg
// added to one and not the other diverges silently; this cannot.
//
// What it cannot do, stated so nobody assumes otherwise: it cannot see a
// second copy of the chain that still behaves identically. Nothing
// behavioural can — identical behaviour is identical — and detecting a
// duplicated-but-agreeing implementation would need a source-level check
// like the guards use. That is a real limit and it is not worth closing
// here; the drift it would catch shows up the moment either copy changes,
// which is when both this row and the client's own reds.
func TestResolveBaseURL_IsTheOneAnswerNewUses(t *testing.T) {
	const endpoint = "https://from-env.example.com/"
	t.Setenv("CURIOUS_API_URL", endpoint)

	c, err := New("")
	if err != nil {
		t.Fatalf("New(\"\"): %v", err)
	}

	wantKey, err := CanonicalKey(ResolveBaseURL(""))
	if err != nil {
		t.Fatalf("CanonicalKey of the resolved endpoint: %v", err)
	}
	gotKey, err := CanonicalKey(c.baseURL)
	if err != nil {
		t.Fatalf("CanonicalKey of the client's base: %v", err)
	}
	if gotKey != wantKey {
		t.Errorf("the client dials %q but the resolver answers %q — New must not carry "+
			"its own copy of the fallback chain", gotKey, wantKey)
	}
}

// TestResolveBaseURL_DoesNotValidate states the division of labour as an
// assertion rather than leaving it to the doc comment. The resolver
// answers "which endpoint did the caller mean"; the address guard decides
// whether this client may dial it. Folding the guard in here would mean a
// stored endpoint that a tightened guard now refuses could not even be
// compared against the current one — the guard stranding a configuration
// it can no longer describe.
func TestResolveBaseURL_DoesNotValidate(t *testing.T) {
	const refused = "http://plaintext.example.com"

	if got := ResolveBaseURL(refused); got != refused {
		t.Errorf("ResolveBaseURL(%q) = %q, want it returned unchanged", refused, got)
	}
	// The control: the guard really does refuse this one, so the row
	// above is about a division of labour rather than about a value that
	// happens to be acceptable everywhere.
	if _, err := validateBaseURL(refused); err == nil {
		t.Error("the address guard accepted plaintext to a non-loopback host, so this " +
			"test is no longer demonstrating that the resolver skips validation")
	}
}
