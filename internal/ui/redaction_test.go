package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// configWithToken is the shape of the file this CLI persists: where it
// talks to, who it is, and the bearer token it holds. It is declared
// HERE rather than in the package that will own it because that package
// is another change's to write, and a test has no business fixing the
// shape of somebody else's type. What it has to be is faithful in the
// one way this test is about — an ordinary struct, ordinary exported
// fields, with the token held as a Secret.
type configWithToken struct {
	APIBase string `json:"api_base"`
	Email   string `json:"email"`
	Token   Secret `json:"token"`
}

// configWithPlainToken is the same shape with the token held as an
// ordinary string. It is the positive control, and the test below is
// worth nothing without it: "the token appears in none of these
// renderings" is also true of a formatting call that silently produced
// nothing, of a verb that does not apply, and of a test that populated
// the wrong field.
type configWithPlainToken struct {
	APIBase string `json:"api_base"`
	Email   string `json:"email"`
	Token   string `json:"token"`
}

// TestAConfigStructNeverRendersItsToken is the rule that has to hold
// everywhere: a token never reaches a terminal, a log line or a file
// this program writes for a person to read.
//
// It is tested through a STRUCT rather than through a bare Secret
// because that is how the value actually travels. Nobody formats a token
// on purpose; somebody prints the config that holds one while working
// out why a deploy failed, and every rendering path below is one they
// might reach for. A type that cannot print itself is how the rule
// survives a careless call in a change nobody has written yet.
func TestAConfigStructNeverRendersItsToken(t *testing.T) {
	const token = "secret-abc123"

	cfg := configWithToken{
		APIBase: "the configured endpoint",
		Email:   "someone@example.test",
		Token:   Secret(token),
	}

	renderings := map[string]string{
		"%v":     fmt.Sprintf("%v", cfg),
		"%+v":    fmt.Sprintf("%+v", cfg),
		"%#v":    fmt.Sprintf("%#v", cfg),
		"%s":     fmt.Sprintf("%s", cfg),
		"%q":     fmt.Sprintf("%q", cfg),
		"Sprint": fmt.Sprint(cfg),
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	renderings["json.Marshal"] = string(encoded)

	indented, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent: %v", err)
	}
	renderings["json.MarshalIndent"] = string(indented)

	for name, got := range renderings {
		// REQUIRED MUTATION: delete MarshalJSON from Secret — the two
		// JSON rows go red. Delete Format and String and the fmt rows
		// go red with them.
		if strings.Contains(got, token) {
			t.Errorf("%s rendered the token: %s", name, got)
		}
		if !strings.Contains(got, redactedPlaceholder) {
			t.Errorf("%s did not render the placeholder: %s", name, got)
		}
		// The surrounding fields must survive, or "the token is absent"
		// would also be true of a rendering that dropped everything.
		if !strings.Contains(got, "someone@example.test") {
			t.Errorf("%s lost the rest of the struct, so its silence about the token "+
				"is not evidence: %s", name, got)
		}
	}
}

// TestThePlainStringControlDoesLeak is the instrument check for the test
// above, and it asserts the OPPOSITE outcome on purpose. Every rendering
// path used there must be shown to be capable of printing a token, or
// none of those assertions means anything.
func TestThePlainStringControlDoesLeak(t *testing.T) {
	const token = "secret-abc123"

	cfg := configWithPlainToken{
		APIBase: "the configured endpoint",
		Email:   "someone@example.test",
		Token:   token,
	}

	renderings := map[string]string{
		"%v":     fmt.Sprintf("%v", cfg),
		"%+v":    fmt.Sprintf("%+v", cfg),
		"%#v":    fmt.Sprintf("%#v", cfg),
		"%s":     fmt.Sprintf("%s", cfg),
		"%q":     fmt.Sprintf("%q", cfg),
		"Sprint": fmt.Sprint(cfg),
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	renderings["json.Marshal"] = string(encoded)

	indented, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent: %v", err)
	}
	renderings["json.MarshalIndent"] = string(indented)

	for name, got := range renderings {
		if !strings.Contains(got, token) {
			t.Errorf("%s did not print a plain-string token: %s\n"+
				"If this fails, the matching assertion in the redaction test is not "+
				"observing that rendering path at all.", name, got)
		}
	}
}

// TestASecretSurvivesEveryMethodAndEveryVerbThisPackageCanBeGiven walks
// every method on UI that can put bytes anywhere, CROSSED WITH every
// formatting verb a caller might reasonably reach for. The name changed
// with the shape, and the reason is worth more than the rows.
//
// The previous version was called
// "...ThroughNothingThisPackageWrites" and its comment said it walked
// every method that can put bytes anywhere. Both were true and the claim
// was still wrong: it walked every METHOD and exactly one VERB. A
// reviewer handed the same methods a different verb and a token came
// straight out on stderr. The coverage was per-method; the defeating
// axis was per-verb; and the name asserted a universal over both.
//
// A test's name is a claim about what it establishes, and this one was
// the only thing standing between a reader and the belief that no format
// string could leak a token here.
//
// THE RESIDUE IS STATED RATHER THAN CLOSED — see the %p row in
// TestSecretKnownUncoveredPaths. fmt resolves %p before consulting a
// Formatter, so no method here can intercept it, and this package does
// not try: it records which verb defeats it and where the limit is
// written down. A limit named is a limit a reader can work with; a limit
// left to a test name's optimism is not.
func TestASecretSurvivesEveryMethodAndEveryVerbThisPackageCanBeGiven(t *testing.T) {
	const token = "secret-abc123"
	secret := Secret(token)

	// Every verb fmt routes through a Formatter, each paired with the
	// marker that proves the rendering HAPPENED — without which "the
	// token is absent" is also true of a call that produced nothing.
	//
	// %T is the one verb here that does not carry the placeholder, and
	// it is in the table rather than out of it because that is a fact
	// worth pinning: fmt answers %T from the type itself and never
	// consults the value, so the type name is what proves the call ran.
	// A reader who finds it missing from this list would have to work
	// out for themselves whether it was safe or forgotten.
	//
	// %p is deliberately ABSENT, and its absence is recorded rather than
	// silent: fmt resolves it before consulting a Formatter, so no method
	// here can intercept it. It is covered as a known limit in
	// TestSecretKnownUncoveredPaths, which fails if it ever starts
	// redacting — the honest place for a limit is a row that watches it,
	// not a table that pretends it away.
	verbs := []struct{ verb, marker string }{
		{"%v", redactedPlaceholder},
		{"%s", redactedPlaceholder},
		{"%q", redactedPlaceholder},
		{"%d", redactedPlaceholder},
		{"%x", redactedPlaceholder},
		{"%X", redactedPlaceholder},
		{"%#v", redactedPlaceholder},
		{"%+v", redactedPlaceholder},
		{"%08s", redactedPlaceholder},
		{"%T", "ui.Secret"},
	}

	// Each method, as a function of the format string, so the cross
	// product is written once. Fail and Internal take the token through
	// a Failure and an error respectively, which is how it would really
	// arrive at them.
	// env is non-nil only for Internal: without CURIOUS_DEBUG it prints
	// no detail at all, so the token never reaches a formatter and the
	// row would pass while observing nothing. Debug mode is also where
	// this method actually matters — it is the one path that prints an
	// error's own text verbatim.
	methods := map[string]struct {
		env  map[string]string
		call func(u *UI, format string)
	}{
		"Step":   {nil, func(u *UI, f string) { u.Step("token "+f, secret) }},
		"Result": {nil, func(u *UI, f string) { u.Result("token "+f, secret) }},
		"Fail": {nil, func(u *UI, f string) {
			u.Fail(NewFailure("What.", fmt.Sprintf("token "+f, secret), "Next."))
		}},
		"Internal": {map[string]string{debugEnvVar: "1"}, func(u *UI, f string) {
			u.Internal(fmt.Errorf("token "+f+" was refused", secret))
		}},
	}

	for name, m := range methods {
		for _, v := range verbs {
			t.Run(name+" "+v.verb, func(t *testing.T) {
				u, out, errOut := testUI("", false, m.env)
				m.call(u, v.verb)
				written := out.String() + errOut.String()

				if strings.Contains(written, token) {
					t.Errorf("%s with %s put the token on a stream: %q", name, v.verb, written)
				}
				// The positive control, per method and per verb: a
				// rendering that dropped everything would satisfy the
				// assertion above, and so would one this test failed to
				// trigger at all.
				if !strings.Contains(written, v.marker) {
					t.Errorf("%s with %s produced %q, which carries no %q — nothing was "+
						"rendered, so the check above observed nothing",
						name, v.verb, written, v.marker)
				}
			})
		}
	}
}
