package mcp

import (
	"bytes"
	"strings"
	"testing"
)

// EVERYTHING THIS SERVER SAYS ABOUT A CLIENT IS ABOUT SOMETHING THE
// CLIENT SENT: a method name it chose, a protocol revision it asked for,
// the name it gave itself. Those went straight to a raw io.Writer this
// package was handed — an operator's stderr — so a client could put an
// escape sequence on the operator's screen by naming a notification
// after one.
//
// A cold review found it. The route was invisible to the guard that
// watches for bypasses, because this package never named a stream: it
// was handed one.
//
// REQUIRED MUTATION, run 2026-09-10: write the diagnostics with
// fmt.Fprintf to the raw writer again. Reds on both rows below.
func TestNothingAClientSentReachesTheOperatorUnescaped(t *testing.T) {
	// ESC and BEL, written the only way a JSON string may carry them.
	// The first attempt at this row put the raw bytes in and saw
	// nothing, because the message was simply invalid JSON and never
	// reached the branch it was aimed at.
	const osc = `\u001b]0;pwned\u0007`

	for _, tc := range []struct {
		name string
		line string
		says string
	}{
		{
			name: "a notification named after an escape sequence",
			line: `{"jsonrpc":"2.0","method":"notifications/` + osc + `"}`,
			says: "ignoring the",
		},
		{
			name: "a client that names itself an escape sequence",
			line: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":` +
				`{"protocolVersion":"1999-01-01","clientInfo":{"name":"` + osc + `"}}}`,
			says: "asked for protocol revision",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, logw bytes.Buffer
			if err := New("curious", "0.0.0").
				Serve(strings.NewReader(tc.line+"\n"), &out, &logw); err != nil {
				t.Fatalf("Serve: %v", err)
			}
			said := logw.String()
			if !strings.Contains(said, tc.says) {
				t.Fatalf("the server never reported it, so this row is about a "+
					"branch it did not reach:\n%q", said)
			}
			for i := 0; i < len(said); i++ {
				if b := said[i]; b != '\n' && (b < 0x20 || b == 0x7f) {
					t.Fatalf("byte %#02x reached the operator: %q", b, said)
				}
			}
			// INERT, NOT DELETED: an operator still has to be able to see
			// what the client actually sent.
			if !strings.Contains(said, "pwned") {
				t.Errorf("the client's text was dropped rather than escaped:\n%q", said)
			}
			// AND NOT ON THE PROTOCOL STREAM. stdout is the protocol and
			// carries nothing else.
			if strings.Contains(out.String(), "pwned") {
				t.Errorf("a diagnostic reached the protocol stream:\n%q", out.String())
			}
		})
	}
}
