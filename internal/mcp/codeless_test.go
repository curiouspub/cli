package mcp

import (
	"net/http"
	"strings"
	"testing"
)

// TestALoginRefusalWithNoCodeIsRenderedAsTheFailure. A login call answered
// by a proxy's empty 502 is not one the agent can recover from by asking
// for the code again: the server may have spent that code. So the tool
// hands back the failure, with its id and its next step, and not the
// recoverable answer that invites resending the same code.
func TestALoginRefusalWithNoCodeIsRenderedAsTheFailure(t *testing.T) {
	for _, tc := range []struct{ tool, args string }{
		{toolLoginStart, `{"email":"someone@example.com"}`},
		{toolLoginVerify, `{"email":"someone@example.com","code":"123456"}`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			run := newToolsRun(t, refusingScript(http.StatusBadGateway, "", ""))
			result := run.call(tc.tool, tc.args)
			if !result.IsError {
				t.Fatalf("a refused login answered as a success: %s", text(t, result))
			}
			said := text(t, result)
			if strings.Contains(said, "can be recovered from") {
				t.Errorf("a refusal with no code was offered as recoverable:\n%s", said)
			}
			for _, want := range []string{"server-error-unexplained", "Try again in a moment",
				"may be spent"} {
				if !strings.Contains(said, want) {
					t.Errorf("the refusal never says %q:\n%s", want, said)
				}
			}
		})
	}
}
