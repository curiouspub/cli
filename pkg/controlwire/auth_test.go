package controlwire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

func TestControlLoginProtocol(t *testing.T) {
	b, err := os.ReadFile("testdata/auth-protocol.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != "bff70f0593a224a5184116e0e12eb249e2194b23ae171f4ef041be9b92a27219" {
		t.Fatal("protocol fixture bytes changed")
	}
	var fixture struct {
		Examples []struct {
			Name    string `json:"name"`
			Request struct {
				Path string          `json:"path"`
				Body json.RawMessage `json:"body"`
			} `json:"request"`
			Response struct {
				Status int             `json:"status"`
				Body   json.RawMessage `json:"body"`
			} `json:"response"`
		} `json:"examples"`
	}
	if err = json.Unmarshal(b, &fixture); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, example := range fixture.Examples {
		switch example.Name {
		case "capabilities":
			var got Capabilities
			if err = json.Unmarshal(example.Response.Body, &got); err != nil || got.Version != 1 || got.Endpoints.Poll != PollPath || got.Endpoints.Requests != RequestsPath || got.Endpoints.Revoke != RevokePath || got.Endpoints.Session != SessionPath || got.Endpoints.Decision != DecisionPath || got.Endpoints.Verification != VerificationPath || got.Endpoints.Capabilities != CapabilitiesPath {
				t.Fatal("capabilities do not match fixture")
			}
			found[example.Name] = true
		case "create":
			var request CreateRequest
			var response CreateResponse
			if json.Unmarshal(example.Request.Body, &request) != nil || json.Unmarshal(example.Response.Body, &response) != nil || request.ClientKind != "cli" || response.IntervalSeconds != IntervalSeconds || example.Request.Path != RequestsPath {
				t.Fatal("create does not match fixture")
			}
			found[example.Name] = true
		case "pending":
			var request PollRequest
			var response ErrorResponse
			if json.Unmarshal(example.Request.Body, &request) != nil || json.Unmarshal(example.Response.Body, &response) != nil || example.Request.Path != PollPath || response.Error != "authorization_pending" || example.Response.Status != 202 {
				t.Fatal("pending does not match fixture")
			}
			found[example.Name] = true
		case "issued":
			var response IssuanceResponse
			if json.Unmarshal(example.Response.Body, &response) != nil || response.Credential == "" || response.CredentialID == "" || response.UserID == "" || response.ExpiresAt == "" {
				t.Fatal("issuance does not match fixture")
			}
			found[example.Name] = true
		}
	}
	for _, name := range []string{"capabilities", "create", "pending", "issued"} {
		if !found[name] {
			t.Fatalf("fixture missing %s", name)
		}
	}
}
