package api

import "testing"

// TestCanonicalKey_Equivalences is the row the config comparison rests
// on: every spelling of one endpoint canonicalises to one string. The
// table is written as EQUIVALENCE CLASSES rather than as input/output
// pairs on purpose — the property under test is "these are the same
// endpoint", and asserting it as a shared key means a change to the
// canonical form's spelling does not have to be re-typed into fourteen
// expectations before the suite can tell you whether the property still
// holds.
//
// The first two classes are the measured defects. "http://localhost:8080/"
// and "http://localhost:8080" differ by one byte and logged a user out;
// "http://LOCALHOST:8080" does the same and was found only by running the
// delivered normaliser rather than reading its description.
//
// REQUIRED MUTATION: in CanonicalKey (canonical.go), drop the
// strings.ToLower from the host line — `host := strings.TrimSuffix(u.Hostname(), ".")`
// — and three of the four classes red immediately. Run and observed to
// fail before this comment was committed; see the report for the red and
// the checksum-verified revert.
//
// SECOND REQUIRED MUTATION, and what it reds is the point: delete the
// `if explicit := u.Port()` branch so the scheme default always wins.
// EVERY CLASS IN THIS TABLE STAYS GREEN. What reds is
// TestCanonicalKey_DistinctEndpointsStayDistinct, because ":8443"
// collapses into ":443" — an endpoint confusion, which is the whole
// failure this key exists to prevent, and this table cannot see it.
//
// The first draft of this comment claimed the port classes would red.
// They do not: with the default always winning, every spelling in a class
// gets the same wrong port and the class stays internally consistent.
// **An equivalence table is blind to any change that is uniformly wrong**,
// including a CanonicalKey that returns a constant — measured, and it
// passes this table in full. That is the argument for the distinctness
// test below, and it is a stronger one than "for completeness".
func TestCanonicalKey_Equivalences(t *testing.T) {
	classes := map[string][]string{
		"loopback, explicit port": {
			"http://localhost:8080",
			"http://localhost:8080/",
			"http://localhost:8080///",
			"http://LOCALHOST:8080",
			"HTTP://LocalHost:8080/",
		},
		"production, default port": {
			"https://api.example.com",
			"https://api.example.com/",
			"https://api.example.com:443",
			"https://API.Example.Com",
			"https://api.example.com.",
			"https://api.example.com.:443/",
		},
		"path-carrying base": {
			"https://api.example.com/v1",
			"https://api.example.com/v1/",
			"https://API.example.com:443/v1",
		},
		"ipv6 loopback": {
			"http://[::1]:8080",
			"http://[::1]:8080/",
			"HTTP://[::1]:8080",
		},
	}

	for name, spellings := range classes {
		t.Run(name, func(t *testing.T) {
			var want string
			for i, raw := range spellings {
				got, err := CanonicalKey(raw)
				if err != nil {
					t.Fatalf("CanonicalKey(%q) errored: %v", raw, err)
				}
				if i == 0 {
					want = got
					continue
				}
				if got != want {
					t.Errorf("CanonicalKey(%q) = %q, want %q — every spelling in this "+
						"class names one endpoint", raw, got, want)
				}
			}
			t.Logf("class key = %q", want)
		})
	}
}

// TestCanonicalKey_DistinctEndpointsStayDistinct is the other half, and
// without it the function could return a constant and pass the table
// above. Each pair below is two DIFFERENT endpoints, and a key that
// collapsed them would send a token issued against one to the other —
// the failure the comparison exists to prevent, arriving through the
// mechanism meant to prevent it.
func TestCanonicalKey_DistinctEndpointsStayDistinct(t *testing.T) {
	pairs := [][2]string{
		{"http://localhost:8080", "http://localhost:9090"},
		{"http://localhost:8080", "https://localhost:8080"},
		{"https://api.example.com", "https://api.example.org"},
		{"https://api.example.com", "https://api.example.com:8443"},
		{"https://api.example.com/v1", "https://api.example.com/v2"},
		{"https://api.example.com", "https://staging.api.example.com"},
		// http and https differ even at each other's default port,
		// because the scheme is part of the key and the token is not.
		{"http://api.example.com:443", "https://api.example.com:443"},
	}

	for _, p := range pairs {
		a, err := CanonicalKey(p[0])
		if err != nil {
			t.Fatalf("CanonicalKey(%q): %v", p[0], err)
		}
		b, err := CanonicalKey(p[1])
		if err != nil {
			t.Fatalf("CanonicalKey(%q): %v", p[1], err)
		}
		if a == b {
			t.Errorf("CanonicalKey(%q) == CanonicalKey(%q) == %q — these are different "+
				"endpoints and a token issued against one must not be sent to the other",
				p[0], p[1], a)
		}
	}
}

// TestCanonicalKey_RefusesWhatItCannotCanonicalise pins the error side.
// A caller reads an error as "not the same endpoint", which costs a
// login; the alternative reading costs a token sent to a server it was
// never issued for, so every case here must error rather than return a
// best-effort string.
func TestCanonicalKey_RefusesWhatItCannotCanonicalise(t *testing.T) {
	for _, raw := range []string{
		"",                        // empty
		"://nope",                 // unparseable
		"mailto:someone@host",     // opaque, no authority
		"https:///v1",             // parses, no host
		"ftp://files.example.com", // a scheme this client does not speak
		"api.example.com",         // no scheme: a host-shaped string, not a URL
		// Userinfo: refused rather than dropped, so a credential never
		// reaches a comparison string and two visibly different URLs
		// never canonicalise to one key.
		"https://user:pass@api.example.com",
	} {
		if got, err := CanonicalKey(raw); err == nil {
			t.Errorf("CanonicalKey(%q) = %q, want an error", raw, got)
		}
	}
}

// TestCanonicalKey_DoesNotMoveTheWireBase is the guarantee the maintainer
// ruling turned on: adding a comparison form must not change what this
// client dials. validateBaseURL keeps the caller's own spelling — same
// case, no invented port — and this row fails if a later change ever
// routes the wire base through CanonicalKey for tidiness.
//
// A user who wrote "https://API.Example.com" gets exactly that in their
// requests and in any Host header derived from it; a port they never
// typed appearing on the wire because a comparison helper added one is
// the concrete regression this guards.
func TestCanonicalKey_DoesNotMoveTheWireBase(t *testing.T) {
	const raw = "https://API.Example.com/"

	wire, err := validateBaseURL(raw)
	if err != nil {
		t.Fatalf("validateBaseURL(%q): %v", raw, err)
	}
	if wire != "https://API.Example.com" {
		t.Errorf("wire base = %q, want the caller's own spelling with the trailing "+
			"slash trimmed and nothing else rewritten", wire)
	}

	key, err := CanonicalKey(raw)
	if err != nil {
		t.Fatalf("CanonicalKey(%q): %v", raw, err)
	}
	if key == wire {
		t.Errorf("CanonicalKey and validateBaseURL returned the same string (%q) — "+
			"the two forms answer different questions and this test exists to keep "+
			"them from being quietly merged", key)
	}
}
