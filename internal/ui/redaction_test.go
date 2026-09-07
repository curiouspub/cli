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

// TestASecretReachesTheTerminalThroughNothingThisPackageWrites walks
// every method on UI that can put bytes anywhere and hands each one a
// Secret. The redaction rule is only useful if it holds at the point
// where text actually leaves the program, and this package is that
// point.
func TestASecretReachesTheTerminalThroughNothingThisPackageWrites(t *testing.T) {
	const token = "secret-abc123"
	secret := Secret(token)

	u, out, errOut := testUI("", false, nil)

	u.Step("token is %v", secret)
	u.Result("token is %v", secret)
	u.Fail(&Failure{
		What: fmt.Sprintf("Token %v was refused.", secret),
		Why:  fmt.Sprintf("The server did not accept %v.", secret),
		Next: "Run `curious deploy` again to log in.",
	})
	u.Internal(fmt.Errorf("token %v was refused", secret))
	_ = u.ExitCode(fmt.Errorf("token %v was refused", secret))

	for name, stream := range map[string]string{"stdout": out.String(), "stderr": errOut.String()} {
		if strings.Contains(stream, token) {
			t.Errorf("a Secret reached %s: %s", name, stream)
		}
		if !strings.Contains(stream, redactedPlaceholder) {
			t.Errorf("%s carries no placeholder, so nothing was actually rendered "+
				"through these paths: %s", name, stream)
		}
	}
}
