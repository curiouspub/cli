package api

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// canonicalDefaultPorts is the port a scheme implies when the URL does
// not spell one out. Only the two schemes this client will ever dial are
// listed, and a scheme absent from this map is an error rather than a
// URL canonicalised without a port — see CanonicalKey for why that
// invariant is worth an error.
var canonicalDefaultPorts = map[string]string{
	"https": "443",
	"http":  "80",
}

// CanonicalKey returns the COMPARISON-ONLY canonical form of a base URL:
// lowercased scheme, lowercased host with any trailing dot removed, an
// EXPLICIT port even when it is the scheme's default, no trailing slash,
// and the path otherwise untouched.
//
// # It is not a wire base, and using it as one is a bug
//
// The string this returns is for asking "are these two configured
// endpoints the same endpoint". It is deliberately NOT what the client
// dials: validateBaseURL produces that, it is unchanged by this
// function's existence, and it preserves the caller's own spelling
// because request semantics are not this function's business to move.
// A base that reaches the wire through here would carry a port the user
// never wrote, into an Authorization-bearing request, on the strength of
// a comparison helper. Two forms, two jobs, and the split is the point.
//
// # Why an explicit port
//
// "https://api.example.com" and "https://api.example.com:443" are the
// same endpoint and differ by nine bytes. Adding the default rather than
// stripping an explicit one is the direction that cannot lose
// information: stripping would have to know that :443 is redundant under
// https and not under some other scheme, which is the same knowledge in
// the harder direction.
//
// # Why this is exported at all
//
// A config file records the endpoint a token was issued against, so that
// a token issued against a local development server is never sent to
// production, or the reverse. That check is a comparison of two
// configured endpoints, and it lives in another package.
//
// Left to a byte comparison it is wrong in ways nobody probes:
// "http://localhost:8080/" logs a user out of "http://localhost:8080",
// and so — measured, not supposed — does "http://LOCALHOST:8080", because
// validateBaseURL lowercases the host into a local variable for its own
// loopback test and returns the URL with the caller's case intact. Left
// to a second normaliser written next to the caller, the two drift, and
// the drift shows up as "login never sticks" rather than as a diff.
//
// # What it refuses
//
// Anything it cannot canonicalise WITHOUT GUESSING: an unparseable URL,
// an opaque one, one with no host, or one whose scheme has no default
// port this function knows. A caller comparing two endpoints treats an
// error as "not the same endpoint", which is the safe direction — it
// costs a login, where the other direction spends a token against a
// server it was never issued for.
//
// It deliberately does NOT apply the address guard. Refusing plaintext
// to a non-loopback host is a decision about what this client may DIAL,
// made once at construction; a stored value that today's guard would
// refuse still has to be comparable, or a tightened guard would strand a
// config it can no longer describe.
func CanonicalKey(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("cannot canonicalise API base URL %q: %w", raw, err)
	}
	if u.Opaque != "" {
		return "", fmt.Errorf(
			"cannot canonicalise API base URL %q: it has no host to compare", raw)
	}

	scheme := strings.ToLower(u.Scheme)
	port, known := canonicalDefaultPorts[scheme]
	if !known {
		return "", fmt.Errorf(
			"cannot canonicalise API base URL %q: scheme %q is not one this client "+
				"speaks, so there is no default port to make explicit", raw, u.Scheme)
	}

	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return "", fmt.Errorf(
			"cannot canonicalise API base URL %q: it has no host to compare", raw)
	}
	if explicit := u.Port(); explicit != "" {
		port = explicit
	}
	if u.User != nil {
		// The address guard refuses userinfo outright, so an effective
		// endpoint can never carry one; a STORED value that does is
		// corrupt. Refusing here rather than dropping the segment keeps
		// a credential out of a comparison string and avoids the quieter
		// error of canonicalising two visibly different URLs to one key.
		return "", fmt.Errorf(
			"cannot canonicalise API base URL %q: it carries a username or password", raw)
	}

	// Rebuilt through url.URL.String() rather than concatenated. Two
	// reasons, and the first is not style: writing the scheme separator
	// as a literal puts a URL-shaped constant in a non-test source, which
	// internal/guard's compiled-in-hostname rule refuses — correctly, and
	// the rule is not loosened for the code that trips it. The second is
	// that String() already knows an IPv6 host keeps its brackets and a
	// path gets escaped, both of which are easy to get wrong by hand.
	//
	// Query and fragment are carried through UNCHANGED rather than
	// stripped. The guard refuses both for a wire base, so an effective
	// endpoint has neither and a stored value carrying one can only ever
	// be a mismatch — which is the safe direction. Dropping them would
	// make that stored value compare EQUAL to the endpoint in force,
	// which is the other one.
	canon := *u
	canon.Scheme = scheme
	canon.Host = net.JoinHostPort(host, port)
	canon.Path = strings.TrimRight(canon.Path, "/")
	canon.RawPath = ""
	return canon.String(), nil
}
